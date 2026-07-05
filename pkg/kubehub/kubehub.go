// Package kubehub is a Kubernetes-native hub.Store: the multi-replica hub service
// registry for cornus's kubernetes backend, using the API server itself as the
// shared, watchable, lease-backed KV instead of Redis. It lives in its own package
// (not pkg/hub) so that pkg/hub — which the caretaker links — stays free
// of client-go.
//
// A KubeStore writes each provider a spoke registers as a namespaced HubEndpoint
// custom resource (a small CRD the store self-installs), so a dial-direct service
// registered on any replica is reachable from every replica. A DELIVERY service,
// however, holds a process-local *yamux.Session to the hosting spoke — only the
// owning replica can open the ingress stream — so a Lookup on a peer replica returns
// a ForwardAddr disposition instead and server.hubRelay forwards the relay to the
// owner (see /.cornus/v1/hub/forward), exactly like the RedisStore.
//
// Liveness is a native coordination.k8s.io Lease per replica, its RenewTime bumped
// by a heartbeat: Lookup and Catalog drop providers whose owner replica's Lease is
// missing or stale, the native analogue of the RedisStore alive-TTL. On top of that,
// each HubEndpoint CR carries an ownerReference to the replica's own Pod (downward
// API), so a hard crash still garbage-collects the CRs when the Pod is deleted.
//
// Index: the merged view is maintained push-based by a dynamic shared informer over
// the HubEndpoint GVR and a typed informer over the store's Leases; Lookup/Catalog
// read the in-memory index without a per-call LIST. A direct-LIST resync warms the
// caches at construction (and is what the unit tests exercise deterministically).
// This replica's OWN providers are additionally tracked synchronously (the disjoint
// partition it authoritatively owns), so a Lookup right after Register sees them
// without waiting on the informer.
//
// See .agents/docs/ARCHITECTURE.md ("Multi-replica design rationale", backend option D).
package kubehub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"cornus/pkg/hub"
)

const (
	// crdGroup/crdVersion/crdKind name the HubEndpoint CRD cornus self-installs.
	crdGroup   = "cornus.dev"
	crdVersion = "v1"
	crdKind    = "HubEndpoint"
	crdPlural  = "hubendpoints"

	// labelService marks a HubEndpoint CR (and is the informer's existence filter);
	// labelOwner records the sanitized replica id; leaseLabel marks our Leases.
	labelService = "cornus.dev/hub-service"
	labelOwner   = "cornus.dev/hub-owner"
	leaseLabel   = "cornus.dev/hub-lease"

	// leaseDuration is how long a replica is considered live past its last heartbeat;
	// heartbeatEvery must be comfortably shorter so a live replica never lapses.
	leaseDuration  = 15 * time.Second
	heartbeatEvery = 5 * time.Second
)

// hubEndpointGVR is the HubEndpoint custom resource; crdGVR is the CRD resource
// itself (cluster-scoped), used for self-install via the dynamic client so no
// apiextensions clientset dependency is needed.
var (
	hubEndpointGVR = schema.GroupVersionResource{Group: crdGroup, Version: crdVersion, Resource: crdPlural}
	crdGVR         = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// provider is one registered endpoint, decoded from a HubEndpoint CR (or held for
// this replica's own partition). mode is "direct" (dial addr) or "deliver" (open an
// ingress stream to owner's spoke). owner is the registering replica; forwardAddr is
// that replica's inter-replica base URL a peer dials for a remote delivery.
type provider struct {
	objName     string
	connID      string
	service     string
	mode        string
	addr        string
	owner       string
	forwardAddr string
	protocol    string
}

// leaseInfo is the liveness window decoded from a replica's Lease.
type leaseInfo struct {
	renew    time.Time
	duration time.Duration
}

func (l leaseInfo) live(now time.Time) bool {
	return !l.renew.IsZero() && !l.renew.Add(l.duration).Before(now)
}

// KubeStore is the Kubernetes-native distributed hub.Store.
type KubeStore struct {
	dyn         dynamic.Interface
	cs          kubernetes.Interface
	namespace   string
	replicaID   string
	forwardAddr string
	// ownerRef, when non-nil, is this replica's Pod; every HubEndpoint CR (and the
	// Lease) carries it so a hard Pod delete garbage-collects them.
	ownerRef *metav1.OwnerReference

	// ctx governs the heartbeat and best-effort CR writes; Close cancels it. stopCh
	// stops the informers.
	ctx    context.Context
	cancel context.CancelFunc
	stopCh chan struct{}

	mu sync.Mutex
	// own is this replica's authoritative partition: service -> objName -> provider,
	// visible to Lookup synchronously (before the informer observes the CR).
	own map[string]map[string]provider
	// owned maps a spoke connID to the objNames it registered, for RemoveConn.
	owned map[string]map[string]string
	// muxes holds this replica's own delivery sessions keyed by objName (the
	// process-local state a peer replica cannot see and must forward to reach).
	muxes map[string]*yamux.Session
	// index is the informer-maintained merged view of ALL replicas' providers:
	// service -> objName -> provider. Lookup reads peers from here.
	index map[string]map[string]provider
	// leases is the informer-maintained liveness view: replicaID -> window.
	leases map[string]leaseInfo
	// rr is the per-name round-robin cursor across live providers.
	rr map[string]int
}

// NewFromEnv builds a KubeStore from in-cluster config (falling back to the local
// kubeconfig, the same path the kubernetes deploy backend uses). namespace is the
// deploy namespace; replicaID names this replica's partition and Lease; forwardAddr
// is the base URL peers dial to forward a delivery to it. POD_NAME/POD_NAMESPACE
// (downward API), when set, provide the Pod ownerReference for crash GC. Any
// construction failure (no cluster, CRD install) is a hard startup error.
func NewFromEnv(ctx context.Context, namespace, replicaID, forwardAddr string) (*KubeStore, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("kubehub: load config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubehub: clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubehub: dynamic client: %w", err)
	}
	return New(ctx, dyn, cs, namespace, replicaID, forwardAddr, os.Getenv("POD_NAME"), os.Getenv("POD_NAMESPACE"))
}

// New builds a KubeStore over explicit clients. It self-installs the CRD, resolves
// the Pod ownerReference (best-effort), warms the index with a LIST, starts the
// informers and the heartbeat, and writes the first Lease. A CRD install failure is
// returned as a hard error.
func New(ctx context.Context, dyn dynamic.Interface, cs kubernetes.Interface, namespace, replicaID, forwardAddr, podName, podNamespace string) (*KubeStore, error) {
	s := newStore(dyn, cs, namespace, replicaID, forwardAddr)
	if err := s.ensureCRD(ctx); err != nil {
		return nil, err
	}
	s.ownerRef = s.resolveOwnerRef(ctx, podName, podNamespace)
	if err := s.beat(s.ctx); err != nil {
		return nil, fmt.Errorf("kubehub: initial lease: %w", err)
	}
	s.resync(s.ctx)
	s.startInformers()
	go s.heartbeat()
	return s, nil
}

// newStore builds the in-memory shell (no cluster I/O), shared by New and tests.
func newStore(dyn dynamic.Interface, cs kubernetes.Interface, namespace, replicaID, forwardAddr string) *KubeStore {
	if namespace == "" {
		namespace = "default"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &KubeStore{
		dyn:         dyn,
		cs:          cs,
		namespace:   namespace,
		replicaID:   replicaID,
		forwardAddr: forwardAddr,
		ctx:         ctx,
		cancel:      cancel,
		stopCh:      make(chan struct{}),
		own:         map[string]map[string]provider{},
		owned:       map[string]map[string]string{},
		muxes:       map[string]*yamux.Session{},
		index:       map[string]map[string]provider{},
		leases:      map[string]leaseInfo{},
		rr:          map[string]int{},
	}
}

func loadConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// endpointName is the DNS-safe, deterministic object name for a provider: a hash of
// replicaID:connID:service. Providers are disjoint per replica, so the name is
// unique and re-registration idempotently targets the same object (no contention).
func endpointName(replicaID, connID, service string) string {
	sum := sha256.Sum256([]byte(replicaID + "\x00" + connID + "\x00" + service))
	return "he-" + hex.EncodeToString(sum[:])[:32]
}

// leaseName is the DNS-safe Lease name for a replica.
func leaseName(replicaID string) string {
	sum := sha256.Sum256([]byte(replicaID))
	return "cornus-hub-" + hex.EncodeToString(sum[:])[:16]
}

// sanitizeLabel makes s a valid label value (<=63 chars, alnum-bounded), for the
// existence-filtered informer labels. The real value always lives in the CR spec.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	v := strings.Trim(b.String(), "-_.")
	if v == "" {
		v = "x"
	}
	if len(v) > 63 {
		v = strings.Trim(v[:63], "-_.")
	}
	return v
}

// --- hub.Store ---------------------------------------------------------------

// Register adds a dial-direct provider (any replica can dial addr) for name.
func (s *KubeStore) Register(connID, name, addr, protocol string) {
	s.put(connID, provider{
		objName:     endpointName(s.replicaID, connID, name),
		connID:      connID,
		service:     name,
		mode:        "direct",
		addr:        addr,
		owner:       s.replicaID,
		forwardAddr: s.forwardAddr,
		protocol:    protocol,
	}, nil)
}

// RegisterDeliver adds a delivery provider for name and keeps the spoke's mux
// process-local (only this replica can open the ingress stream; peers forward).
func (s *KubeStore) RegisterDeliver(connID, name string, mux *yamux.Session) {
	s.put(connID, provider{
		objName:     endpointName(s.replicaID, connID, name),
		connID:      connID,
		service:     name,
		mode:        "deliver",
		owner:       s.replicaID,
		forwardAddr: s.forwardAddr,
	}, mux)
}

// put records a provider in this replica's own partition and writes its CR
// (best-effort, like the RedisStore HSet — the hub.Store methods are fire-and-forget).
func (s *KubeStore) put(connID string, rec provider, mux *yamux.Session) {
	s.mu.Lock()
	if s.own[rec.service] == nil {
		s.own[rec.service] = map[string]provider{}
	}
	s.own[rec.service][rec.objName] = rec
	if s.owned[connID] == nil {
		s.owned[connID] = map[string]string{}
	}
	s.owned[connID][rec.objName] = rec.service
	if mux != nil {
		s.muxes[rec.objName] = mux
	}
	s.mu.Unlock()
	s.writeCR(s.ctx, rec)
}

// writeCR creates or updates the HubEndpoint CR for a provider.
func (s *KubeStore) writeCR(ctx context.Context, rec provider) {
	obj := s.endpointObject(rec)
	res := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace)
	if _, err := res.Create(ctx, obj, metav1.CreateOptions{}); err == nil {
		return
	} else if !apierrors.IsAlreadyExists(err) {
		return
	}
	// Already present (re-registration): overwrite the spec at the current version.
	existing, err := res.Get(ctx, rec.objName, metav1.GetOptions{})
	if err != nil {
		return
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, _ = res.Update(ctx, obj, metav1.UpdateOptions{})
}

func (s *KubeStore) endpointObject(rec provider) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": crdGroup + "/" + crdVersion,
		"kind":       crdKind,
		"metadata": map[string]any{
			"name": rec.objName,
			"labels": map[string]any{
				labelService: sanitizeLabel(rec.service),
				labelOwner:   sanitizeLabel(rec.owner),
			},
		},
		"spec": map[string]any{
			"service":     rec.service,
			"mode":        rec.mode,
			"addr":        rec.addr,
			"owner":       rec.owner,
			"forwardAddr": rec.forwardAddr,
			"protocol":    rec.protocol,
			"connID":      rec.connID,
		},
	}}
	if s.ownerRef != nil {
		u.SetOwnerReferences([]metav1.OwnerReference{*s.ownerRef})
	}
	return u
}

// Lookup returns one live provider for name, round-robin across the merged view of
// this replica's own partition plus peers' providers whose owner Lease is live. A
// remote delivery resolves to a ForwardAddr/ForwardName disposition.
func (s *KubeStore) Lookup(name string) (hub.Target, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveProvidersLocked(name, now)
	if len(live) == 0 {
		return hub.Target{}, false
	}
	i := s.rr[name] % len(live)
	s.rr[name] = i + 1
	rec := live[i]
	switch rec.mode {
	case "direct":
		return hub.Target{Addr: rec.addr, Protocol: rec.protocol}, true
	case "deliver":
		if rec.owner == s.replicaID {
			mux := s.muxes[rec.objName]
			if mux == nil {
				return hub.Target{}, false
			}
			return hub.Target{Mux: mux}, true
		}
		// Remote delivery: the owner replica holds the spoke session; forward to it.
		return hub.Target{ForwardAddr: rec.forwardAddr, ForwardName: name}, true
	}
	return hub.Target{}, false
}

// liveProvidersLocked returns the live providers for name in a stable order (sorted
// objName): this replica's own partition (always live) plus peers whose owner Lease
// is live. s.mu must be held.
func (s *KubeStore) liveProvidersLocked(name string, now time.Time) []provider {
	seen := map[string]provider{}
	for objName, rec := range s.own[name] {
		seen[objName] = rec
	}
	for objName, rec := range s.index[name] {
		if rec.owner == s.replicaID {
			continue // our own partition is authoritative (already in seen)
		}
		if l, ok := s.leases[rec.owner]; ok && l.live(now) {
			seen[objName] = rec
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for objName := range seen {
		names = append(names, objName)
	}
	sort.Strings(names)
	out := make([]provider, 0, len(names))
	for _, objName := range names {
		out = append(out, seen[objName])
	}
	return out
}

// Catalog returns the sorted service names that currently have at least one live
// provider anywhere in the cluster.
func (s *KubeStore) Catalog() []string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]struct{}{}
	for name := range s.own {
		if len(s.own[name]) > 0 {
			set[name] = struct{}{}
		}
	}
	for name := range s.index {
		if len(s.liveProvidersLocked(name, now)) > 0 {
			set[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RemoveConn drops every provider this replica registered under connID (deleting the
// CRs) and forgets its local delivery muxes. Called when the spoke's hub connection
// drops.
func (s *KubeStore) RemoveConn(connID string) {
	s.mu.Lock()
	objs := s.owned[connID]
	delete(s.owned, connID)
	toDel := make(map[string]string, len(objs))
	for objName, service := range objs {
		toDel[objName] = service
		delete(s.muxes, objName)
		if m := s.own[service]; m != nil {
			delete(m, objName)
			if len(m) == 0 {
				delete(s.own, service)
			}
		}
	}
	s.mu.Unlock()
	res := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace)
	for objName := range toDel {
		_ = res.Delete(s.ctx, objName, metav1.DeleteOptions{})
	}
}

// Close stops the heartbeat and informers, best-effort deletes this replica's CRs
// and Lease, and is safe to call once.
func (s *KubeStore) Close() error {
	s.cancel()
	close(s.stopCh)

	s.mu.Lock()
	var objNames []string
	for _, m := range s.owned {
		for objName := range m {
			objNames = append(objNames, objName)
		}
	}
	s.own = map[string]map[string]provider{}
	s.owned = map[string]map[string]string{}
	s.muxes = map[string]*yamux.Session{}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace)
	for _, objName := range objNames {
		_ = res.Delete(ctx, objName, metav1.DeleteOptions{})
	}
	_ = s.cs.CoordinationV1().Leases(s.namespace).Delete(ctx, leaseName(s.replicaID), metav1.DeleteOptions{})
	return nil
}

// --- liveness ----------------------------------------------------------------

// heartbeat refreshes this replica's Lease until Close cancels s.ctx.
func (s *KubeStore) heartbeat() {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			_ = s.beat(s.ctx)
		}
	}
}

// beat creates or updates this replica's Lease with a fresh RenewTime.
func (s *KubeStore) beat(ctx context.Context) error {
	leases := s.cs.CoordinationV1().Leases(s.namespace)
	now := metav1.NewMicroTime(time.Now())
	dur := int32(leaseDuration / time.Second)
	name := leaseName(s.replicaID)
	holder := s.replicaID
	existing, err := leases.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{leaseLabel: "true"},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &dur,
				RenewTime:            &now,
			},
		}
		if s.ownerRef != nil {
			lease.OwnerReferences = []metav1.OwnerReference{*s.ownerRef}
		}
		_, err = leases.Create(ctx, lease, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &dur
	existing.Spec.RenewTime = &now
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[leaseLabel] = "true"
	_, err = leases.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// resolveOwnerRef looks up this replica's own Pod so HubEndpoint CRs (and the Lease)
// carry it as an owner: a hard Pod delete then GCs them. Best-effort — nil when
// POD_NAME is unset or the Pod cannot be read.
func (s *KubeStore) resolveOwnerRef(ctx context.Context, podName, podNamespace string) *metav1.OwnerReference {
	if podName == "" {
		return nil
	}
	ns := podNamespace
	if ns == "" {
		ns = s.namespace
	}
	pod, err := s.cs.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	controller := false
	return &metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
		Controller: &controller,
	}
}

// --- index (informers + resync) ---------------------------------------------

// startInformers wires the push-based index: a dynamic HubEndpoint informer and a
// typed Lease informer, both filtered to cornus's objects, updating s.index and
// s.leases on every event.
func (s *KubeStore) startInformers() {
	epFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(s.dyn, 0, s.namespace, func(o *metav1.ListOptions) {
		o.LabelSelector = labelService // existence filter
	})
	epInf := epFactory.ForResource(hubEndpointGVR).Informer()
	epInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.onEndpoint(obj) },
		UpdateFunc: func(_, obj any) { s.onEndpoint(obj) },
		DeleteFunc: func(obj any) { s.onEndpointDelete(obj) },
	})
	epFactory.Start(s.stopCh)

	leaseFactory := informers.NewSharedInformerFactoryWithOptions(s.cs, 0,
		informers.WithNamespace(s.namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = leaseLabel + "=true" }),
	)
	leaseInf := leaseFactory.Coordination().V1().Leases().Informer()
	leaseInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.onLease(obj) },
		UpdateFunc: func(_, obj any) { s.onLease(obj) },
		DeleteFunc: func(obj any) { s.onLeaseDelete(obj) },
	})
	leaseFactory.Start(s.stopCh)
}

func (s *KubeStore) onEndpoint(obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	rec := parseProvider(u)
	s.mu.Lock()
	if s.index[rec.service] == nil {
		s.index[rec.service] = map[string]provider{}
	}
	s.index[rec.service][rec.objName] = rec
	s.mu.Unlock()
}

func (s *KubeStore) onEndpointDelete(obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	rec := parseProvider(u)
	s.mu.Lock()
	if m := s.index[rec.service]; m != nil {
		delete(m, rec.objName)
		if len(m) == 0 {
			delete(s.index, rec.service)
		}
	}
	s.mu.Unlock()
}

func (s *KubeStore) onLease(obj any) {
	l, ok := toLease(obj)
	if !ok || l.Spec.HolderIdentity == nil {
		return
	}
	s.mu.Lock()
	s.leases[*l.Spec.HolderIdentity] = leaseInfoOf(l)
	s.mu.Unlock()
}

func (s *KubeStore) onLeaseDelete(obj any) {
	l, ok := toLease(obj)
	if !ok || l.Spec.HolderIdentity == nil {
		return
	}
	s.mu.Lock()
	delete(s.leases, *l.Spec.HolderIdentity)
	s.mu.Unlock()
}

// resync rebuilds the index and lease caches from a direct LIST. It warms the caches
// at construction and is the deterministic path the unit tests drive.
func (s *KubeStore) resync(ctx context.Context) {
	index := map[string]map[string]provider{}
	if list, err := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelService}); err == nil {
		for i := range list.Items {
			rec := parseProvider(&list.Items[i])
			if index[rec.service] == nil {
				index[rec.service] = map[string]provider{}
			}
			index[rec.service][rec.objName] = rec
		}
	}
	leases := map[string]leaseInfo{}
	if list, err := s.cs.CoordinationV1().Leases(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: leaseLabel + "=true"}); err == nil {
		for i := range list.Items {
			l := &list.Items[i]
			if l.Spec.HolderIdentity != nil {
				leases[*l.Spec.HolderIdentity] = leaseInfoOf(l)
			}
		}
	}
	s.mu.Lock()
	s.index = index
	s.leases = leases
	s.mu.Unlock()
}

func leaseInfoOf(l *coordinationv1.Lease) leaseInfo {
	var info leaseInfo
	if l.Spec.RenewTime != nil {
		info.renew = l.Spec.RenewTime.Time
	}
	if l.Spec.LeaseDurationSeconds != nil {
		info.duration = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
	}
	return info
}

func parseProvider(u *unstructured.Unstructured) provider {
	get := func(k string) string {
		v, _, _ := unstructured.NestedString(u.Object, "spec", k)
		return v
	}
	return provider{
		objName:     u.GetName(),
		connID:      get("connID"),
		service:     get("service"),
		mode:        get("mode"),
		addr:        get("addr"),
		owner:       get("owner"),
		forwardAddr: get("forwardAddr"),
		protocol:    get("protocol"),
	}
}

func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	switch v := obj.(type) {
	case *unstructured.Unstructured:
		return v, true
	case cache.DeletedFinalStateUnknown:
		u, ok := v.Obj.(*unstructured.Unstructured)
		return u, ok
	}
	return nil, false
}

func toLease(obj any) (*coordinationv1.Lease, bool) {
	switch v := obj.(type) {
	case *coordinationv1.Lease:
		return v, true
	case cache.DeletedFinalStateUnknown:
		l, ok := v.Obj.(*coordinationv1.Lease)
		return l, ok
	}
	return nil, false
}

// --- CRD self-install --------------------------------------------------------

// ensureCRD idempotently creates the HubEndpoint CRD and waits until it is
// Established (bounded). It uses the dynamic client against the CRD resource, so no
// apiextensions clientset dependency is needed.
func (s *KubeStore) ensureCRD(ctx context.Context) error {
	res := s.dyn.Resource(crdGVR)
	if _, err := res.Create(ctx, crdObject(), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kubehub: create CRD: %w", err)
	}
	// Wait for the Established condition (bounded). A fake dynamic client never sets
	// status conditions, so a missing status short-circuits after the object exists.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := res.Get(ctx, crdPlural+"."+crdGroup, metav1.GetOptions{})
		if err == nil && crdEstablished(got) {
			return nil
		}
		if time.Now().After(deadline) {
			return nil // best-effort: the resource exists; proceed rather than block startup forever
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func crdEstablished(u *unstructured.Unstructured) bool {
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "Established" && m["status"] == "True" {
			return true
		}
	}
	return false
}

// crdObject builds the HubEndpoint CustomResourceDefinition (a structural schema).
func crdObject() *unstructured.Unstructured {
	strProps := map[string]any{"type": "string"}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": crdPlural + "." + crdGroup},
		"spec": map[string]any{
			"group": crdGroup,
			"scope": "Namespaced",
			"names": map[string]any{
				"plural":   crdPlural,
				"singular": "hubendpoint",
				"kind":     crdKind,
				"listKind": crdKind + "List",
			},
			"versions": []any{map[string]any{
				"name":    crdVersion,
				"served":  true,
				"storage": true,
				"schema": map[string]any{
					"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"service":     strProps,
									"mode":        strProps,
									"addr":        strProps,
									"owner":       strProps,
									"forwardAddr": strProps,
									"protocol":    strProps,
									"connID":      strProps,
								},
							},
						},
					},
				},
			}},
		},
	}}
}
