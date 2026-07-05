// Package netdriver realises Compose user-network semantics on Kubernetes via
// a pluggable pipeline of network providers. Each api.NetworkAttachment on a
// DeploySpec resolves (by driver name) to an ordered list of Providers that
// contribute cumulatively: a DNS provider and an isolation provider can both
// act on the same attachment. Providers are pure builders — they emit desired
// objects and mutate the pod template but perform no I/O; the Engine applies,
// deduplicates, and garbage-collects what they return, so providers stay
// trivially unit-testable and all cluster access funnels through one place.
package netdriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// Capability names a cluster feature a provider depends on. The Engine detects
// capabilities lazily via the discovery API and either falls back to the
// "services" DNS baseline (default) or hard-errors (CORNUS_K8S_NET_STRICT).
type Capability string

const (
	// CapMultus is the Multus meta-CNI: the NetworkAttachmentDefinition CRD
	// (k8s.cni.cncf.io/v1) is served.
	CapMultus Capability = "multus"
	// CapCilium is the Cilium CNI: cilium.io/v2 is served.
	CapCilium Capability = "cilium"
	// CapPolicyCNI marks a NetworkPolicy-ENFORCING CNI. The NetworkPolicy API
	// exists on every cluster (kindnet included) without being enforced, so
	// this is an explicit opt-in via CORNUS_K8S_POLICY_CNI=true.
	CapPolicyCNI Capability = "policy-cni"
)

// netLabelPrefix is the pod-template label-key prefix marking membership of a
// user network: cornus.net/<netLabel>=true. The networkpolicy provider
// selects on it, and Engine.GC uses it to reference-count shared objects.
const netLabelPrefix = "cornus.net/"

// nadGVR is the Multus NetworkAttachmentDefinition resource.
var nadGVR = schema.GroupVersionResource{
	Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions",
}

// cnpGVR is the CiliumNetworkPolicy resource.
var cnpGVR = schema.GroupVersionResource{
	Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies",
}

// Attachment is the resolved per-network context handed to every provider in
// the pipeline.
type Attachment struct {
	// Spec is the workload being deployed.
	Spec api.DeploySpec
	// Net is the network membership being realised.
	Net api.NetworkAttachment
	// Selector is the deployment's pod-selector labels, so DNS/policy objects
	// can target this workload's pods.
	Selector map[string]string
	// NetLabel is a stable DNS-1123 identifier derived from Net.Name (which
	// may contain characters like '_' that Kubernetes names reject). It is
	// reused as the NAD name, the membership label suffix, and the policy key.
	NetLabel string
	// Namespace is where the provider's objects (e.g. the NAD) are created —
	// the backend's workload namespace. Annotations that Multus resolves in
	// its own default namespace (kube-system) unless qualified, like
	// default-network, must carry it.
	Namespace string
}

// Object is a namespaced dependent a provider wants created alongside the
// Deployment. Exactly one of Typed / Unstructured is set: typed core objects
// (Services, NetworkPolicies) route to the typed clientset, *unstructured CRD
// objects (NetworkAttachmentDefinition, CiliumNetworkPolicy) to the dynamic
// client. Shared objects back the network itself: they are created
// idempotently, carry no owner reference, and are reaped by Engine.GC once no
// workload references their network. Non-shared (workload-scoped) objects are
// stamped with the Deployment owner reference so Kubernetes GC removes them
// with the workload.
type Object struct {
	Typed        any
	Unstructured *unstructured.Unstructured
	GVR          schema.GroupVersionResource
	Shared       bool
}

// Provider realises one facet of a network attachment. Multiple providers
// compose per attachment; the Engine runs the resolved pipeline in order.
type Provider interface {
	// Name identifies the provider ("services", "multus-bridge", ...).
	Name() string
	// NetworkScoped returns objects backing the network itself, independent of
	// any one workload (e.g. a NetworkAttachmentDefinition). Returned objects
	// must be Shared and keyed by NetLabel so every member converges the same
	// object.
	NetworkScoped(a Attachment) ([]Object, error)
	// WorkloadScoped returns objects tied to this workload's membership (e.g.
	// headless Services selecting its pods).
	WorkloadScoped(a Attachment) ([]Object, error)
	// MutatePod attaches the workload's pods to the network by editing the pod
	// template in place (annotations, labels). Implementations must merge into
	// existing values, not overwrite — multiple attachments accumulate.
	MutatePod(a Attachment, tmpl *corev1.PodTemplateSpec) error
	// Requires reports the cluster capabilities this provider needs.
	Requires() []Capability
}

// Engine resolves attachments to provider pipelines and performs all cluster
// I/O on their behalf. A nil dynamic client is tolerated: unstructured objects
// are then unavailable, which reads as the corresponding capabilities being
// absent (the services-only path needs neither).
type Engine struct {
	cs        kubernetes.Interface
	dyn       dynamic.Interface
	namespace string
	defDriver string
	strict    bool
	warnf     func(format string, args ...any)

	mu     sync.Mutex
	caps   map[Capability]bool
	warned map[string]bool // one fallback warning per network, not one per resolve
}

// New builds an Engine over the backend's clients. Defaults come from the
// environment: CORNUS_K8S_NET_DRIVER (driver for attachments that name
// none; default "services") and CORNUS_K8S_NET_STRICT (hard-error instead
// of falling back to the services baseline when a capability is missing).
func New(cs kubernetes.Interface, dyn dynamic.Interface, namespace string) *Engine {
	defDriver := os.Getenv("CORNUS_K8S_NET_DRIVER")
	if defDriver == "" {
		defDriver = "services"
	}
	return &Engine{
		cs:        cs,
		dyn:       dyn,
		namespace: namespace,
		defDriver: defDriver,
		strict:    os.Getenv("CORNUS_K8S_NET_STRICT") == "true",
		warnf: func(format string, args ...any) {
			logging.FromContext(context.Background(), slog.String("component", "netdriver")).Warn(fmt.Sprintf(format, args...))
		},
		caps:   map[Capability]bool{},
		warned: map[string]bool{},
	}
}

// warnOnce emits one fallback warning per network name; MutateTemplate and
// Apply both resolve the same attachments, and re-applies recur.
func (e *Engine) warnOnce(netName, format string, args ...any) {
	e.mu.Lock()
	dup := e.warned[netName]
	e.warned[netName] = true
	e.mu.Unlock()
	if !dup {
		e.warnf(format, args...)
	}
}

// attachment builds the per-network provider context (namespace-less; unit
// tests use it directly to exercise providers in isolation).
func attachment(spec api.DeploySpec, net api.NetworkAttachment, selector map[string]string) Attachment {
	return Attachment{Spec: spec, Net: net, Selector: selector, NetLabel: netLabelName(net.Name)}
}

// attachment builds the per-network provider context, stamped with the
// Engine's workload namespace (Multus's default-network annotation must be
// namespace-qualified).
func (e *Engine) attachment(spec api.DeploySpec, net api.NetworkAttachment, selector map[string]string) Attachment {
	a := attachment(spec, net, selector)
	a.Namespace = e.namespace
	return a
}

// netLabelName maps a network's logical name (e.g. Compose's "myproj_front",
// invalid as a Kubernetes name because of the underscore) to a stable
// DNS-1123-safe identifier usable as an object name and a label key/value. A
// short hash of the original disambiguates names that sanitise identically.
func netLabelName(logical string) string {
	sum := sha256.Sum256([]byte(logical))
	base := sanitizeDNS1123(logical)
	if base == "" {
		base = "net"
	}
	// Label values cap at 63 chars; leave room for the 8-hex suffix and dash.
	if len(base) > 54 {
		base = strings.Trim(base[:54], "-")
	}
	return base + "-" + hex.EncodeToString(sum[:4])
}

// sanitizeDNS1123 lowercases s and replaces every character outside [a-z0-9-]
// with '-', trimming leading/trailing dashes.
func sanitizeDNS1123(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// pipelineFor maps a driver to its ordered provider list. The "services" DNS
// baseline is prepended to every driver so name resolution always works, and a
// `driver_opts: {policy: "true"}` appends the networkpolicy provider on top of
// any driver (belt-and-suspenders isolation). Unknown or not-yet-implemented
// drivers return an error; the caller decides between hard-erroring and falling
// back per the strict setting.
func pipelineFor(driver string, net api.NetworkAttachment) ([]Provider, error) {
	var provs []Provider
	switch driver {
	case "services":
		provs = []Provider{servicesProvider{}}
	case "bridge", "ipvlan", "macvlan":
		provs = []Provider{servicesProvider{}, multusProvider{plugin: driver}}
	case "policy":
		// Flat-network isolation: DNS + NetworkPolicy, no CNI attachment.
		provs = []Provider{servicesProvider{}, networkPolicyProvider{}}
	case "cilium":
		// DNS baseline + native CiliumNetworkPolicy isolation.
		provs = []Provider{servicesProvider{}, ciliumProvider{}}
	default:
		return nil, fmt.Errorf("netdriver: unknown network driver %q", driver)
	}
	if driver != "policy" && net.DriverOpts["policy"] == "true" {
		provs = append(provs, networkPolicyProvider{})
	}
	return provs, nil
}

// hasProvider reports whether the pipeline includes a provider by name.
func hasProvider(provs []Provider, name string) bool {
	for _, p := range provs {
		if p.Name() == name {
			return true
		}
	}
	return false
}

// servicesOnly is the fallback pipeline: DNS via headless Services, no fabric.
func servicesOnly() []Provider { return []Provider{servicesProvider{}} }

// resolve picks the provider pipeline for one attachment, applying the default
// driver, capability detection, and the strict-vs-fallback policy.
func (e *Engine) resolve(net api.NetworkAttachment) ([]Provider, error) {
	driver := net.Driver
	if driver == "" {
		driver = e.defDriver
	}
	provs, err := pipelineFor(driver, net)
	if err != nil {
		if e.strict {
			return nil, err
		}
		e.warnOnce(net.Name, "network %s: %v; falling back to services-only name resolution", net.Name, err)
		return servicesOnly(), nil
	}
	for _, p := range provs {
		for _, c := range p.Requires() {
			if e.capable(c) {
				continue
			}
			if e.strict {
				return nil, fmt.Errorf("netdriver: network %s: driver %q needs cluster capability %q, which is not available", net.Name, driver, c)
			}
			e.warnOnce(net.Name, "network %s: driver %q needs missing cluster capability %q; falling back to services-only name resolution", net.Name, driver, c)
			return servicesOnly(), nil
		}
	}
	// NetworkPolicy is emitted regardless (a no-op where unenforced), but say so
	// once when no policy-enforcing CNI is detected, so silent non-enforcement
	// does not read as isolation.
	if hasProvider(provs, "networkpolicy") && !e.capable(CapPolicyCNI) {
		e.warnOnce(net.Name+"\x00policy", "network %s: emitting a NetworkPolicy for isolation, but could not confirm a policy-enforcing CNI; whether it is enforced depends on the cluster CNI (Calico, Cilium, and recent kindnet enforce; some do not). Set CORNUS_K8S_POLICY_CNI=true to silence.", net.Name)
	}
	return provs, nil
}

// MultusActive reports whether at least one of the spec's attachments actually
// resolves to a pipeline containing a Multus provider — i.e. the pods will get
// real secondary interfaces rather than the services-only fallback. The backend
// consults it before injecting DNS records that point at user-network secondary
// addresses (api.DNSSpec.RequireUserNet): on a cluster without the Multus
// fabric those addresses would never exist, and the pod is better served by the
// plain cluster DNS. Capability probes are cached, so calling this alongside
// MutateTemplate/Apply costs no extra discovery round-trips.
func (e *Engine) MultusActive(spec api.DeploySpec) bool {
	for _, net := range spec.Networks {
		provs, err := e.resolve(net)
		if err != nil {
			continue // strict-mode resolution failure; Apply will surface it
		}
		for _, p := range provs {
			if _, ok := p.(multusProvider); ok {
				return true
			}
		}
	}
	return false
}

// capable reports (and caches) whether the cluster offers a capability.
func (e *Engine) capable(c Capability) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if got, ok := e.caps[c]; ok {
		return got
	}
	e.caps[c] = e.detect(c)
	return e.caps[c]
}

// detect probes one capability. Unstructured-object capabilities additionally
// require the dynamic client, since without it the Engine could not create the
// CRD objects anyway.
func (e *Engine) detect(c Capability) bool {
	switch c {
	case CapPolicyCNI:
		// The NetworkPolicy API exists on every cluster (kindnet included)
		// without being ENFORCED, so presence of the API proves nothing. Take
		// an explicit opt-in, or probe kube-system for a known policy-enforcing
		// CNI's DaemonSet (Calico / Cilium).
		if os.Getenv("CORNUS_K8S_POLICY_CNI") == "true" {
			return true
		}
		return e.policyCNIDaemonSet()
	case CapMultus:
		return e.dyn != nil && e.groupVersionServed("k8s.cni.cncf.io/v1", nadGVR.Resource)
	case CapCilium:
		return e.dyn != nil && e.groupVersionServed("cilium.io/v2", "")
	}
	return false
}

// policyCNIDaemonSet reports whether kube-system runs a DaemonSet whose name
// marks a policy-enforcing CNI (calico-node, cilium). Best-effort: a list error
// (RBAC, etc.) reads as "not detected".
func (e *Engine) policyCNIDaemonSet() bool {
	if e.cs == nil {
		return false
	}
	ds, err := e.cs.AppsV1().DaemonSets("kube-system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return false
	}
	for i := range ds.Items {
		n := ds.Items[i].Name
		if strings.Contains(n, "calico") || strings.Contains(n, "cilium") {
			return true
		}
	}
	return false
}

// groupVersionServed reports whether the API server serves groupVersion (and,
// when resource is non-empty, that specific resource in it).
func (e *Engine) groupVersionServed(groupVersion, resource string) bool {
	if e.cs == nil {
		return false
	}
	list, err := e.cs.Discovery().ServerResourcesForGroupVersion(groupVersion)
	if err != nil || list == nil {
		return false
	}
	if resource == "" {
		return true
	}
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}

// MutateTemplate attaches the spec's networks to the pod template: it stamps
// the cornus.net/<netLabel> membership label for each attachment (consumed
// by policy selection and GC reference-counting) and runs every resolved
// provider's MutatePod in order.
func (e *Engine) MutateTemplate(spec api.DeploySpec, selector map[string]string, tmpl *corev1.PodTemplateSpec) error {
	for _, net := range spec.Networks {
		provs, err := e.resolve(net)
		if err != nil {
			return err
		}
		a := e.attachment(spec, net, selector)
		if tmpl.Labels == nil {
			tmpl.Labels = map[string]string{}
		}
		tmpl.Labels[netLabelPrefix+a.NetLabel] = "true"
		for _, p := range provs {
			if err := p.MutatePod(a, tmpl); err != nil {
				return fmt.Errorf("netdriver: %s: %w", p.Name(), err)
			}
		}
	}
	return nil
}

// Apply creates the provider-emitted objects for the spec's networks: shared
// (network-scoped) objects idempotently and un-owned, workload-scoped objects
// stamped with the Deployment owner reference. Objects are deduplicated by
// kind and name across providers and attachments, first emitter wins.
func (e *Engine) Apply(ctx context.Context, spec api.DeploySpec, selector map[string]string, owner metav1.OwnerReference) error {
	seen := map[string]bool{}
	for _, net := range spec.Networks {
		provs, err := e.resolve(net)
		if err != nil {
			return err
		}
		a := e.attachment(spec, net, selector)
		for _, p := range provs {
			shared, err := p.NetworkScoped(a)
			if err != nil {
				return fmt.Errorf("netdriver: %s: %w", p.Name(), err)
			}
			owned, err := p.WorkloadScoped(a)
			if err != nil {
				return fmt.Errorf("netdriver: %s: %w", p.Name(), err)
			}
			for _, o := range shared {
				if err := e.create(ctx, o, seen, nil); err != nil {
					return fmt.Errorf("netdriver: %s: %w", p.Name(), err)
				}
			}
			for _, o := range owned {
				if err := e.create(ctx, o, seen, &owner); err != nil {
					return fmt.Errorf("netdriver: %s: %w", p.Name(), err)
				}
			}
		}
	}
	return nil
}

// create routes one object to the right client, deduplicating by kind/name and
// tolerating AlreadyExists (shared objects converge from many workloads;
// workload-scoped alias objects may collide across deployments, in which case
// the first owner keeps it — a documented shared-namespace limitation).
func (e *Engine) create(ctx context.Context, o Object, seen map[string]bool, owner *metav1.OwnerReference) error {
	switch {
	case o.Unstructured != nil:
		key := o.GVR.String() + "/" + o.Unstructured.GetName()
		if seen[key] {
			return nil
		}
		seen[key] = true
		if e.dyn == nil {
			return fmt.Errorf("no dynamic client for %s %s", o.GVR.Resource, o.Unstructured.GetName())
		}
		if owner != nil {
			o.Unstructured.SetOwnerReferences([]metav1.OwnerReference{*owner})
		}
		_, err := e.dyn.Resource(o.GVR).Namespace(e.namespace).Create(ctx, o.Unstructured, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			if !o.Shared {
				// Workload-scoped alias object: first owner keeps it (documented
				// shared-namespace limitation).
				return nil
			}
			// Shared object (NAD / CiliumNetworkPolicy): reconcile the spec so
			// config changes (driver_opts, policy rules) take effect on redeploy
			// even though NetLabel — and thus the object name — is unchanged.
			return e.updateUnstructured(ctx, o)
		}
		return err
	case o.Typed != nil:
		switch obj := o.Typed.(type) {
		case *corev1.Service:
			key := "service/" + obj.Name
			if seen[key] {
				return nil
			}
			seen[key] = true
			if owner != nil {
				obj.OwnerReferences = []metav1.OwnerReference{*owner}
			}
			_, err := e.cs.CoreV1().Services(e.namespace).Create(ctx, obj, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return err
		case *networkingv1.NetworkPolicy:
			key := "networkpolicy/" + obj.Name
			if seen[key] {
				return nil
			}
			seen[key] = true
			if owner != nil {
				obj.OwnerReferences = []metav1.OwnerReference{*owner}
			}
			_, err := e.cs.NetworkingV1().NetworkPolicies(e.namespace).Create(ctx, obj, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				if !o.Shared {
					return nil
				}
				// Shared NetworkPolicy: reconcile the spec so changed policy
				// rules take effect on redeploy (the name is a stable NetLabel
				// hash, so it never triggers a create).
				existing, gerr := e.cs.NetworkingV1().NetworkPolicies(e.namespace).Get(ctx, obj.Name, metav1.GetOptions{})
				if gerr != nil {
					return gerr
				}
				obj.ResourceVersion = existing.ResourceVersion
				_, uerr := e.cs.NetworkingV1().NetworkPolicies(e.namespace).Update(ctx, obj, metav1.UpdateOptions{})
				return uerr
			}
			return err
		default:
			return fmt.Errorf("unsupported typed object %T", o.Typed)
		}
	}
	return nil
}

// updateUnstructured reconciles an existing shared CRD object (NAD /
// CiliumNetworkPolicy) to the provider's desired spec by carrying over the live
// resourceVersion and issuing an Update, so configuration changes take effect
// even though the object name (a stable NetLabel hash) never changes.
func (e *Engine) updateUnstructured(ctx context.Context, o Object) error {
	existing, err := e.dyn.Resource(o.GVR).Namespace(e.namespace).Get(ctx, o.Unstructured.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	o.Unstructured.SetResourceVersion(existing.GetResourceVersion())
	_, err = e.dyn.Resource(o.GVR).Namespace(e.namespace).Update(ctx, o.Unstructured, metav1.UpdateOptions{})
	return err
}

// GC reaps shared network-scoped objects (NADs, NetworkPolicies) whose network
// no longer has any member workload: it collects the cornus.net/<netLabel>
// membership labels from the remaining managed Deployments' pod templates
// (mark), then deletes cornus-managed network objects — named by their
// netLabel — not referenced by any of them (sweep). Best-effort: a failed sweep
// leaves the object for the next GC.
func (e *Engine) GC(ctx context.Context) {
	deps, err := e.cs.AppsV1().Deployments(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelManaged + "=true",
	})
	if err != nil {
		return
	}
	referenced := map[string]bool{}
	for i := range deps.Items {
		// A deployment being torn down (foreground deletion leaves it in the
		// list with a deletion timestamp until its dependents are gone) no
		// longer counts as a member — otherwise GC could never reap a network
		// on the delete that removed its last workload.
		if deps.Items[i].DeletionTimestamp != nil {
			continue
		}
		for k := range deps.Items[i].Spec.Template.Labels {
			if lbl, ok := strings.CutPrefix(k, netLabelPrefix); ok {
				referenced[lbl] = true
			}
		}
	}

	// Shared CRD objects via the dynamic client (absent => that fabric was never
	// used here): Multus NADs and CiliumNetworkPolicies, reaped by netLabel.
	if e.dyn != nil {
		for _, gvr := range []schema.GroupVersionResource{nadGVR, cnpGVR} {
			list, err := e.dyn.Resource(gvr).Namespace(e.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: deploy.LabelManaged + "=true",
			})
			if err != nil {
				continue
			}
			for i := range list.Items {
				if name := list.Items[i].GetName(); !referenced[name] {
					_ = e.dyn.Resource(gvr).Namespace(e.namespace).Delete(ctx, name, metav1.DeleteOptions{})
				}
			}
		}
	}

	// NetworkPolicies (typed).
	if nps, err := e.cs.NetworkingV1().NetworkPolicies(e.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelManaged + "=true",
	}); err == nil {
		for i := range nps.Items {
			if name := nps.Items[i].Name; !referenced[name] {
				_ = e.cs.NetworkingV1().NetworkPolicies(e.namespace).Delete(ctx, name, metav1.DeleteOptions{})
			}
		}
	}
}
