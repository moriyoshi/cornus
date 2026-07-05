package netdriver

import (
	"context"
	"strconv"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/compose"
	"cornus/pkg/deploy"
)

// newFakeDynamic builds a dynamic fake that can list the CRDs cornus manages
// (NADs and CiliumNetworkPolicies — CRDs with no compiled Go type, so their list
// kinds must be registered explicitly).
func newFakeDynamic(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		nadGVR: "NetworkAttachmentDefinitionList",
		cnpGVR: "CiliumNetworkPolicyList",
	})
}

// multusCapable marks the fake discovery as serving the NAD CRD.
func multusCapable(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("fake discovery type assertion failed")
	}
	fd.Resources = append(fd.Resources, &metav1.APIResourceList{
		GroupVersion: "k8s.cni.cncf.io/v1",
		APIResources: []metav1.APIResource{{Name: "network-attachment-definitions"}},
	})
}

// ciliumCapable marks the fake discovery as serving the Cilium API group.
func ciliumCapable(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("fake discovery type assertion failed")
	}
	fd.Resources = append(fd.Resources, &metav1.APIResourceList{
		GroupVersion: "cilium.io/v2",
		APIResources: []metav1.APIResource{{Name: "ciliumnetworkpolicies"}},
	})
}

func TestNetLabelName(t *testing.T) {
	a, b := netLabelName("proj_front"), netLabelName("proj-front")
	// DNS-1123 safe...
	for _, n := range []string{a, b} {
		if strings.ContainsAny(n, "_A") || len(n) > 63 {
			t.Errorf("netLabelName produced invalid label %q", n)
		}
	}
	// ...stable, and distinct even when sanitization collides.
	if a != netLabelName("proj_front") {
		t.Error("netLabelName is not stable")
	}
	if a == b {
		t.Errorf("proj_front and proj-front must map to distinct labels, both %q", a)
	}
	if !strings.HasPrefix(a, "proj-front-") {
		t.Errorf("label %q should keep the sanitized base for readability", a)
	}
}

// TestServicesProvider asserts the DNS baseline: one headless Service per
// alias, selecting the workload's pods, carrying its container ports; aliases
// that sanitize to nothing are skipped.
func TestServicesProvider(t *testing.T) {
	a := attachment(
		api.DeploySpec{
			Name:  "proj-web",
			Ports: []api.PortMapping{{Host: 8080, Container: 80}},
		},
		api.NetworkAttachment{Name: "proj_default", Aliases: []string{"web", "www.alias", "___"}},
		map[string]string{"cornus.app": "proj-web"},
	)
	objs, err := servicesProvider{}.WorkloadScoped(a)
	if err != nil {
		t.Fatalf("WorkloadScoped: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("objects = %d, want 2 (the all-underscore alias is skipped)", len(objs))
	}
	svc, ok := objs[0].Typed.(*corev1.Service)
	if !ok {
		t.Fatalf("object 0 is %T, want *corev1.Service", objs[0].Typed)
	}
	if svc.Name != "web" || objs[0].Shared {
		t.Errorf("service = %q shared=%v, want owned headless Service web", svc.Name, objs[0].Shared)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("ClusterIP = %q, want None (headless)", svc.Spec.ClusterIP)
	}
	if svc.Spec.Selector["cornus.app"] != "proj-web" {
		t.Errorf("selector = %v, want the workload's pod selector", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 80 {
		t.Errorf("ports = %+v, want container port 80", svc.Spec.Ports)
	}
	if second, _ := objs[1].Typed.(*corev1.Service); second == nil || second.Name != "www-alias" {
		t.Errorf("object 1 = %+v, want sanitized alias service www-alias", objs[1].Typed)
	}
}

// TestServicesProviderDualProtocolPortNames guards against duplicate
// ServicePort names: the same container port exposed on tcp and udp (a
// DNS-style workload) must produce two ports with distinct, valid names or the
// API server rejects the headless alias Service.
func TestServicesProviderDualProtocolPortNames(t *testing.T) {
	a := attachment(
		api.DeploySpec{
			Name: "proj-dns",
			Ports: []api.PortMapping{
				{Host: 53, Container: 53, Protocol: "tcp"},
				{Host: 53, Container: 53, Protocol: "udp"},
			},
		},
		api.NetworkAttachment{Name: "proj_default", Aliases: []string{"dns"}},
		map[string]string{"cornus.app": "proj-dns"},
	)
	objs, err := servicesProvider{}.WorkloadScoped(a)
	if err != nil {
		t.Fatalf("WorkloadScoped: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("objects = %d, want 1", len(objs))
	}
	svc := objs[0].Typed.(*corev1.Service)
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("ports = %d, want 2", len(svc.Spec.Ports))
	}
	if p0, p1 := svc.Spec.Ports[0], svc.Spec.Ports[1]; p0.Name == "" || p1.Name == "" || p0.Name == p1.Name {
		t.Errorf("port names must be non-empty and distinct, got %q and %q", p0.Name, p1.Name)
	}
}

// TestServicesProviderLongAliasSkipped guards against emitting a headless
// Service whose name exceeds the RFC1123 label limit (63 chars): Kubernetes
// rejects such a name, which would fail the whole Engine.Apply. An
// over-length alias must be skipped, while a 63-char alias (the boundary) is
// still emitted.
func TestServicesProviderLongAliasSkipped(t *testing.T) {
	ok63 := strings.Repeat("a", 63)
	tooLong := strings.Repeat("a", 64)
	a := attachment(
		api.DeploySpec{Name: "proj-web"},
		api.NetworkAttachment{Name: "proj_default", Aliases: []string{ok63, tooLong, "web"}},
		map[string]string{"cornus.app": "proj-web"},
	)
	objs, err := servicesProvider{}.WorkloadScoped(a)
	if err != nil {
		t.Fatalf("WorkloadScoped: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("objects = %d, want 2 (the 64-char alias is skipped)", len(objs))
	}
	for _, o := range objs {
		svc := o.Typed.(*corev1.Service)
		if len(svc.Name) > 63 {
			t.Errorf("service name %q is %d chars, exceeds the 63-char RFC1123 limit", svc.Name, len(svc.Name))
		}
		if svc.Name == tooLong {
			t.Errorf("over-length alias %q was emitted as a Service name", tooLong)
		}
	}
	if objs[0].Typed.(*corev1.Service).Name != ok63 {
		t.Errorf("first service = %q, want the 63-char boundary alias emitted", objs[0].Typed.(*corev1.Service).Name)
	}
}

// TestMultusProvider asserts the NAD shape and the pod annotations for both
// attach modes, plus the option validation.
func TestMultusProvider(t *testing.T) {
	m := multusProvider{plugin: "bridge"}
	a := attachment(api.DeploySpec{Name: "proj-web"}, api.NetworkAttachment{Name: "proj_front"}, nil)

	objs, err := m.NetworkScoped(a)
	if err != nil {
		t.Fatalf("NetworkScoped: %v", err)
	}
	if len(objs) != 1 || !objs[0].Shared || objs[0].Unstructured == nil {
		t.Fatalf("objects = %+v, want one shared unstructured NAD", objs)
	}
	nad := objs[0].Unstructured
	if nad.GetName() != a.NetLabel || nad.GetKind() != "NetworkAttachmentDefinition" {
		t.Errorf("NAD name/kind = %s/%s", nad.GetName(), nad.GetKind())
	}
	if nad.GetLabels()[deploy.LabelManaged] != "true" {
		t.Errorf("NAD labels = %v, want cornus.managed=true (GC marker)", nad.GetLabels())
	}
	config, _, _ := unstructuredNestedString(nad.Object, "spec", "config")
	for _, want := range []string{`"type":"bridge"`, `"isGateway":true`, `"host-local"`, `"subnet":"10.222.`} {
		if !strings.Contains(config, want) {
			t.Errorf("CNI config missing %s: %s", want, config)
		}
	}

	// Overlaid annotations accumulate; detached sets the default network; two
	// detached networks conflict.
	tmpl := &corev1.PodTemplateSpec{}
	b := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "proj_back"}, nil)
	if err := m.MutatePod(a, tmpl); err != nil {
		t.Fatal(err)
	}
	if err := m.MutatePod(b, tmpl); err != nil {
		t.Fatal(err)
	}
	if got := tmpl.Annotations[networksAnnotation]; got != a.NetLabel+","+b.NetLabel {
		t.Errorf("networks annotation = %q, want both attachments accumulated", got)
	}
	det := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "proj_main", Default: true}, nil)
	if err := m.MutatePod(det, tmpl); err != nil {
		t.Fatal(err)
	}
	if got := tmpl.Annotations[defaultNetworkAnnotation]; got != det.NetLabel {
		t.Errorf("default-network annotation = %q, want %q", got, det.NetLabel)
	}
	det2 := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "proj_other", Default: true}, nil)
	if err := m.MutatePod(det2, tmpl); err == nil {
		t.Error("two default=true networks must conflict")
	}

	// With a namespace on the attachment (the Engine path), the default-network
	// annotation must be namespace-qualified: Multus resolves an unqualified
	// reference in kube-system, not the pod's namespace.
	nsTmpl := &corev1.PodTemplateSpec{}
	detNS := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "proj_main", Default: true}, nil)
	detNS.Namespace = "prod"
	if err := m.MutatePod(detNS, nsTmpl); err != nil {
		t.Fatal(err)
	}
	if got := nsTmpl.Annotations[defaultNetworkAnnotation]; got != "prod/"+detNS.NetLabel {
		t.Errorf("default-network annotation = %q, want %q", got, "prod/"+detNS.NetLabel)
	}
	// Idempotent re-mutate with the same qualified value must not conflict.
	if err := m.MutatePod(detNS, nsTmpl); err != nil {
		t.Errorf("same default network re-applied must not conflict: %v", err)
	}

	// Option validation: dhcp IPAM rejected; ipvlan needs a master.
	bad := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "n", DriverOpts: map[string]string{"ipam": "dhcp"}}, nil)
	if _, err := m.NetworkScoped(bad); err == nil {
		t.Error("dhcp ipam must be rejected")
	}
	if _, err := (multusProvider{plugin: "ipvlan"}).NetworkScoped(a); err == nil {
		t.Error("ipvlan without driver_opts master must be rejected")
	}
}

// TestCiliumProvider asserts the CiliumNetworkPolicy shape: a shared, un-owned
// CNP selecting the network's member label and allowing ingress only from the
// same label (the Cilium-native counterpart of the NetworkPolicy provider).
func TestCiliumProvider(t *testing.T) {
	a := attachment(api.DeploySpec{Name: "proj-web"}, api.NetworkAttachment{Name: "proj_iso"}, nil)
	objs, err := ciliumProvider{}.NetworkScoped(a)
	if err != nil {
		t.Fatalf("NetworkScoped: %v", err)
	}
	if len(objs) != 1 || !objs[0].Shared || objs[0].Unstructured == nil || objs[0].GVR != cnpGVR {
		t.Fatalf("objects = %+v, want one shared unstructured CNP with the cilium GVR", objs)
	}
	cnp := objs[0].Unstructured
	if cnp.GetName() != a.NetLabel || cnp.GetKind() != "CiliumNetworkPolicy" || cnp.GetAPIVersion() != "cilium.io/v2" {
		t.Errorf("CNP name/kind/apiVersion = %s/%s/%s", cnp.GetName(), cnp.GetKind(), cnp.GetAPIVersion())
	}
	if cnp.GetLabels()[deploy.LabelManaged] != "true" {
		t.Errorf("CNP labels = %v, want cornus.managed=true (GC marker)", cnp.GetLabels())
	}
	// endpointSelector and the single ingress fromEndpoints both key on the
	// membership label — same-network reaches, cross-network is denied.
	want := netLabelPrefix + a.NetLabel
	sel, _, _ := unstructuredNestedString(cnp.Object, "spec", "endpointSelector", "matchLabels", want)
	if sel != "true" {
		t.Errorf("endpointSelector missing %s=true: %+v", want, cnp.Object["spec"])
	}
	from, _, _ := unstructuredNestedString(cnp.Object,
		"spec", "ingress", "0", "fromEndpoints", "0", "matchLabels", want)
	if from != "true" {
		t.Errorf("ingress fromEndpoints missing %s=true: %+v", want, cnp.Object["spec"])
	}
	// WorkloadScoped / MutatePod are no-ops (isolation is purely the shared CNP).
	if o, _ := (ciliumProvider{}).WorkloadScoped(a); o != nil {
		t.Errorf("WorkloadScoped = %v, want nil", o)
	}
}

// TestCiliumResolveAndCapability covers driver resolution: cilium resolves to
// [services, cilium] where the Cilium API is served, and falls back to
// services-only where it is not (non-strict) / hard-errors (strict).
func TestCiliumResolveAndCapability(t *testing.T) {
	// Absent Cilium => services-only fallback.
	cs := fake.NewSimpleClientset()
	e := New(cs, newFakeDynamic(t), "default")
	e.warnf = func(string, ...any) {}
	provs, err := e.resolve(api.NetworkAttachment{Name: "n", Driver: "cilium"})
	if err != nil {
		t.Fatalf("non-strict resolve: %v", err)
	}
	if len(provs) != 1 || provs[0].Name() != "services" {
		t.Errorf("providers = %v, want services-only fallback where Cilium is absent", provs)
	}

	// Present Cilium => full pipeline.
	capable := fake.NewSimpleClientset()
	ciliumCapable(t, capable)
	full := New(capable, newFakeDynamic(t), "default")
	provs, err = full.resolve(api.NetworkAttachment{Name: "n", Driver: "cilium"})
	if err != nil {
		t.Fatalf("capable resolve: %v", err)
	}
	if len(provs) != 2 || provs[1].Name() != "cilium" {
		t.Errorf("providers = %v, want [services cilium]", provs)
	}

	// Strict + absent => hard error naming the capability.
	t.Setenv("CORNUS_K8S_NET_STRICT", "true")
	strict := New(fake.NewSimpleClientset(), newFakeDynamic(t), "default")
	if _, err := strict.resolve(api.NetworkAttachment{Name: "n", Driver: "cilium"}); err == nil {
		t.Error("strict resolve must error when Cilium is missing")
	}
}

// TestCiliumApplyAndGC drives the engine over the cilium driver: Apply creates
// exactly one shared, un-owned CNP; GC reaps it once its last member is gone.
func TestCiliumApplyAndGC(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ciliumCapable(t, cs)
	dyn := newFakeDynamic(t)
	e := New(cs, dyn, "default")
	e.warnf = func(string, ...any) {}
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:     "proj-web",
		Networks: []api.NetworkAttachment{{Name: "proj_iso", Driver: "cilium", Aliases: []string{"web"}}},
	}
	tmpl := &corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
	if err := e.MutateTemplate(spec, map[string]string{"cornus.app": "proj-web"}, tmpl); err != nil {
		t.Fatalf("MutateTemplate: %v", err)
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-web", Labels: map[string]string{deploy.LabelManaged: "true"}},
		Spec:       appsv1.DeploymentSpec{Template: *tmpl},
	}
	if _, err := cs.AppsV1().Deployments("default").Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "proj-web"}
	if err := e.Apply(ctx, spec, map[string]string{"cornus.app": "proj-web"}, owner); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	netLabel := netLabelName("proj_iso")
	cnps, err := dyn.Resource(cnpGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list CNPs: %v", err)
	}
	if len(cnps.Items) != 1 || cnps.Items[0].GetName() != netLabel {
		t.Fatalf("CNPs = %d, want exactly one %s", len(cnps.Items), netLabel)
	}
	if len(cnps.Items[0].GetOwnerReferences()) != 0 {
		t.Error("shared CNP must be un-owned (survives one workload's delete)")
	}

	// Remove the only member, then GC must reap the CNP.
	if err := cs.AppsV1().Deployments("default").Delete(ctx, "proj-web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.GC(ctx)
	cnps, _ = dyn.Resource(cnpGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if len(cnps.Items) != 0 {
		t.Errorf("GC must reap the CNP once its last member is gone, still have %d", len(cnps.Items))
	}
}

// unstructuredNestedString is a tiny local helper to keep the test
// dependency-light. A numeric field indexes into a []any (for CNI/policy configs
// with array members); every other field indexes a map[string]any.
func unstructuredNestedString(obj map[string]any, fields ...string) (string, bool, error) {
	cur := any(obj)
	for _, f := range fields {
		if idx, err := strconv.Atoi(f); err == nil {
			s, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(s) {
				return "", false, nil
			}
			cur = s[idx]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur = m[f]
	}
	s, ok := cur.(string)
	return s, ok, nil
}

// TestNetworkPolicyProvider asserts the isolation policy shape: a shared
// NetworkPolicy selecting the network's member label and allowing ingress only
// from the same label.
func TestNetworkPolicyProvider(t *testing.T) {
	a := attachment(api.DeploySpec{Name: "proj-web"}, api.NetworkAttachment{Name: "proj_iso"}, nil)
	objs, err := networkPolicyProvider{}.NetworkScoped(a)
	if err != nil {
		t.Fatalf("NetworkScoped: %v", err)
	}
	if len(objs) != 1 || !objs[0].Shared {
		t.Fatalf("objects = %+v, want one shared NetworkPolicy", objs)
	}
	np, ok := objs[0].Typed.(*networkingv1.NetworkPolicy)
	if !ok {
		t.Fatalf("object is %T, want *networkingv1.NetworkPolicy", objs[0].Typed)
	}
	label := "cornus.net/" + a.NetLabel
	if np.Name != a.NetLabel || np.Labels[label] != "true" || np.Labels["cornus.managed"] != "true" {
		t.Errorf("np name/labels = %s/%v", np.Name, np.Labels)
	}
	if np.Spec.PodSelector.MatchLabels[label] != "true" {
		t.Errorf("podSelector = %v, want the member label", np.Spec.PodSelector.MatchLabels)
	}
	if len(np.Spec.PolicyTypes) != 1 || np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("policyTypes = %v, want [Ingress] (egress untouched so DNS works)", np.Spec.PolicyTypes)
	}
	if len(np.Spec.Ingress) != 1 || len(np.Spec.Ingress[0].From) != 1 ||
		np.Spec.Ingress[0].From[0].PodSelector.MatchLabels[label] != "true" {
		t.Errorf("ingress = %+v, want allow-from same member label", np.Spec.Ingress)
	}
}

// TestPipelinePolicy covers policy selection: the `policy` driver = services +
// networkpolicy, and `driver_opts: {policy: true}` appends networkpolicy to any
// fabric.
func TestPipelinePolicy(t *testing.T) {
	names := func(provs []Provider, _ error) []string {
		var out []string
		for _, p := range provs {
			out = append(out, p.Name())
		}
		return out
	}

	if got := names(pipelineFor("policy", api.NetworkAttachment{})); !strEq(got, []string{"services", "networkpolicy"}) {
		t.Errorf("policy driver = %v, want [services networkpolicy]", got)
	}
	optNet := api.NetworkAttachment{DriverOpts: map[string]string{"policy": "true"}}
	if got := names(pipelineFor("bridge", optNet)); !strEq(got, []string{"services", "multus-bridge", "networkpolicy"}) {
		t.Errorf("bridge+policy = %v, want [services multus-bridge networkpolicy]", got)
	}
	// The policy driver does not double-append on the opt.
	if got := names(pipelineFor("policy", optNet)); !strEq(got, []string{"services", "networkpolicy"}) {
		t.Errorf("policy driver + opt = %v, want no duplicate networkpolicy", got)
	}
}

func strEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNetworkPolicyEmittedWithoutEnforcingCNI confirms the advisory model: a
// `policy` network on a plain cluster (no policy CNI) still EMITS the
// NetworkPolicy (forward-compatible, harmless no-op) — it is not gated away —
// and the policy is reaped by GC once unreferenced.
func TestNetworkPolicyEmittedWithoutEnforcingCNI(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := New(cs, nil, "default") // no policy CNI, no dynamic client
	e.warnf = func(string, ...any) {}
	ctx := context.Background()

	spec := api.DeploySpec{Name: "proj-web", Networks: []api.NetworkAttachment{{Name: "proj_iso", Driver: "policy"}}}
	sel := map[string]string{"cornus.app": "proj-web"}
	tmpl := &corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
	if err := e.MutateTemplate(spec, sel, tmpl); err != nil {
		t.Fatalf("MutateTemplate: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "proj-web", Labels: map[string]string{deploy.LabelManaged: "true"}},
		Spec:       appsv1.DeploymentSpec{Template: *tmpl},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "proj-web"}
	if err := e.Apply(ctx, spec, sel, owner); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	netLabel := netLabelName("proj_iso")
	if _, err := cs.NetworkingV1().NetworkPolicies("default").Get(ctx, netLabel, metav1.GetOptions{}); err != nil {
		t.Fatalf("NetworkPolicy not emitted without an enforcing CNI: %v", err)
	}

	// GC reaps it once the deployment is gone.
	if err := cs.AppsV1().Deployments("default").Delete(ctx, "proj-web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.GC(ctx)
	if _, err := cs.NetworkingV1().NetworkPolicies("default").Get(ctx, netLabel, metav1.GetOptions{}); err == nil {
		t.Error("NetworkPolicy must be reaped once its network is unreferenced")
	}
}

// TestGCIgnoresTerminatingDeployment reproduces the foreground-deletion race:
// a Deployment mid-teardown lingers in the list with a DeletionTimestamp, and
// must NOT keep its network's shared objects alive — otherwise the delete that
// removed the last workload could never reap them.
func TestGCIgnoresTerminatingDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := New(cs, nil, "default")
	e.warnf = func(string, ...any) {}
	ctx := context.Background()

	netLabel := netLabelName("proj_iso")
	// A managed NetworkPolicy for the network...
	if _, err := cs.NetworkingV1().NetworkPolicies("default").Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: netLabel, Labels: map[string]string{deploy.LabelManaged: "true"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// ...and the only member deployment is terminating (deletionTimestamp set).
	now := metav1.Now()
	if _, err := cs.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "proj-web",
			Labels:            map[string]string{deploy.LabelManaged: "true"},
			DeletionTimestamp: &now,
			Finalizers:        []string{"kubernetes"}, // fake requires a finalizer to accept a deletionTimestamp
		},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{netLabelPrefix + netLabel: "true"}},
		}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	e.GC(ctx)
	if _, err := cs.NetworkingV1().NetworkPolicies("default").Get(ctx, netLabel, metav1.GetOptions{}); err == nil {
		t.Error("GC must reap the network's policy when its only member is terminating")
	}
}

// TestResolveFallback covers the capability/driver policy: a fabric driver on
// a cluster without the capability degrades to services-only by default and
// hard-errors under CORNUS_K8S_NET_STRICT; unknown drivers likewise.
func TestResolveFallback(t *testing.T) {
	cs := fake.NewSimpleClientset() // no multus in discovery
	e := New(cs, nil, "default")
	e.warnf = func(string, ...any) {}

	provs, err := e.resolve(api.NetworkAttachment{Name: "n", Driver: "bridge"})
	if err != nil {
		t.Fatalf("non-strict resolve: %v", err)
	}
	if len(provs) != 1 || provs[0].Name() != "services" {
		t.Errorf("providers = %v, want services-only fallback", provs)
	}

	t.Setenv("CORNUS_K8S_NET_STRICT", "true")
	strict := New(cs, nil, "default")
	if _, err := strict.resolve(api.NetworkAttachment{Name: "n", Driver: "bridge"}); err == nil {
		t.Error("strict resolve must error on a missing capability")
	}
	if _, err := strict.resolve(api.NetworkAttachment{Name: "n", Driver: "nonsense"}); err == nil {
		t.Error("strict resolve must error on an unknown driver")
	}

	// With the capability present (discovery + dynamic client), bridge resolves
	// to the full pipeline.
	capable := fake.NewSimpleClientset()
	multusCapable(t, capable)
	full := New(capable, newFakeDynamic(t), "default")
	provs, err = full.resolve(api.NetworkAttachment{Name: "n", Driver: "bridge"})
	if err != nil {
		t.Fatalf("capable resolve: %v", err)
	}
	if len(provs) != 2 || provs[1].Name() != "multus-bridge" {
		t.Errorf("providers = %v, want [services multus-bridge]", provs)
	}
}

// TestEngineApplyAndGC drives the full engine I/O against fake clients: Apply
// creates the headless alias Service (owner-ref'd) and the shared NAD
// (un-owned, once across two workloads); GC reaps the NAD only after the last
// referencing Deployment is gone.
func TestEngineApplyAndGC(t *testing.T) {
	cs := fake.NewSimpleClientset()
	multusCapable(t, cs)
	dyn := newFakeDynamic(t)
	e := New(cs, dyn, "default")
	ctx := context.Background()

	specFor := func(app string) api.DeploySpec {
		return api.DeploySpec{
			Name: app,
			Networks: []api.NetworkAttachment{
				{Name: "proj_front", Driver: "bridge", Aliases: []string{app}},
			},
		}
	}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "proj-web"}
	sel := map[string]string{"cornus.app": "proj-web"}

	// Deploy two workloads on the same network (the engine is also fed the pod
	// templates so GC has membership labels to mark from).
	deployOne := func(app string) {
		spec := specFor(app)
		tmpl := &corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
		if err := e.MutateTemplate(spec, sel, tmpl); err != nil {
			t.Fatalf("MutateTemplate %s: %v", app, err)
		}
		_, err := cs.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: app, Labels: map[string]string{deploy.LabelManaged: "true"}},
			Spec:       appsv1.DeploymentSpec{Template: *tmpl},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("create deployment %s: %v", app, err)
		}
		if err := e.Apply(ctx, spec, sel, owner); err != nil {
			t.Fatalf("Apply %s: %v", app, err)
		}
	}
	deployOne("proj-web")
	deployOne("proj-db")

	// The alias Service exists, headless, owner-ref'd.
	svc, err := cs.CoreV1().Services("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get alias service: %v", err)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone || len(svc.OwnerReferences) != 1 {
		t.Errorf("alias service = clusterIP %q owners %d, want headless + owned", svc.Spec.ClusterIP, len(svc.OwnerReferences))
	}

	// Exactly one shared NAD, un-owned.
	netLabel := netLabelName("proj_front")
	nads, err := dyn.Resource(nadGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list NADs: %v", err)
	}
	if len(nads.Items) != 1 || nads.Items[0].GetName() != netLabel {
		t.Fatalf("NADs = %d, want exactly one shared %s", len(nads.Items), netLabel)
	}
	if len(nads.Items[0].GetOwnerReferences()) != 0 {
		t.Error("shared NAD must be un-owned (survives one workload's delete)")
	}

	// One member deleted: still referenced, NAD stays.
	if err := cs.AppsV1().Deployments("default").Delete(ctx, "proj-web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.GC(ctx)
	if _, err := dyn.Resource(nadGVR).Namespace("default").Get(ctx, netLabel, metav1.GetOptions{}); err != nil {
		t.Fatalf("NAD reaped while still referenced: %v", err)
	}

	// Last member deleted: swept.
	if err := cs.AppsV1().Deployments("default").Delete(ctx, "proj-db", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.GC(ctx)
	if _, err := dyn.Resource(nadGVR).Namespace("default").Get(ctx, netLabel, metav1.GetOptions{}); err == nil {
		t.Error("NAD must be reaped once no deployment references its network")
	}
}

// TestEngineApplyReconcilesSharedNAD asserts that redeploying a network whose
// driver_opts changed reconciles the existing shared NAD instead of silently
// keeping the stale on-cluster config: the object name is a stable NetLabel
// hash, so create() gets AlreadyExists and must Update rather than no-op.
func TestEngineApplyReconcilesSharedNAD(t *testing.T) {
	cs := fake.NewSimpleClientset()
	multusCapable(t, cs)
	dyn := newFakeDynamic(t)
	e := New(cs, dyn, "default")
	ctx := context.Background()

	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "proj-web"}
	sel := map[string]string{"cornus.app": "proj-web"}
	specWith := func(subnet string) api.DeploySpec {
		return api.DeploySpec{
			Name: "proj-web",
			Networks: []api.NetworkAttachment{{
				Name:       "proj_front",
				Driver:     "bridge",
				DriverOpts: map[string]string{"subnet": subnet},
			}},
		}
	}

	// First deploy: subnet 10.10.0.0/24.
	if err := e.Apply(ctx, specWith("10.10.0.0/24"), sel, owner); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	netLabel := netLabelName("proj_front")
	nad, err := dyn.Resource(nadGVR).Namespace("default").Get(ctx, netLabel, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get NAD: %v", err)
	}
	if config, _, _ := unstructuredNestedString(nad.Object, "spec", "config"); !strings.Contains(config, "10.10.0.0/24") {
		t.Fatalf("initial NAD config = %s, want subnet 10.10.0.0/24", config)
	}

	// Redeploy with a changed subnet: the NAD name is unchanged, so the engine
	// must reconcile (Update) the existing object, not swallow AlreadyExists.
	if err := e.Apply(ctx, specWith("10.20.0.0/24"), sel, owner); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	nad, err = dyn.Resource(nadGVR).Namespace("default").Get(ctx, netLabel, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get NAD after redeploy: %v", err)
	}
	config, _, _ := unstructuredNestedString(nad.Object, "spec", "config")
	if !strings.Contains(config, "10.20.0.0/24") || strings.Contains(config, "10.10.0.0/24") {
		t.Errorf("NAD config not reconciled on redeploy: %s, want subnet 10.20.0.0/24", config)
	}
	// Still exactly one shared NAD, still un-owned.
	nads, _ := dyn.Resource(nadGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if len(nads.Items) != 1 {
		t.Errorf("NADs = %d, want exactly one", len(nads.Items))
	}
	if len(nads.Items[0].GetOwnerReferences()) != 0 {
		t.Error("reconciled shared NAD must stay un-owned")
	}
}

// TestMultusStaticIPPin covers the pinned-addressing path (compose user
// networks, matrix row A'): an attachment carrying a plan-time static IP makes
// the NAD delegate to the `static` IPAM plugin (declaring the ips capability
// Multus needs to forward annotation addresses), and the pod's
// network-selection annotation switches to the JSON form carrying the address.
func TestMultusStaticIPPin(t *testing.T) {
	m := multusProvider{plugin: "bridge"}
	pinned := attachment(api.DeploySpec{Name: "proj-web"},
		api.NetworkAttachment{Name: "proj_front", IP: "10.222.14.7/24"}, nil)

	objs, err := m.NetworkScoped(pinned)
	if err != nil {
		t.Fatalf("NetworkScoped: %v", err)
	}
	config, _, _ := unstructuredNestedString(objs[0].Unstructured.Object, "spec", "config")
	for _, want := range []string{`"ipam":{"type":"static"}`, `"capabilities":{"ips":true}`, `"type":"bridge"`} {
		if !strings.Contains(config, want) {
			t.Errorf("CNI config missing %s: %s", want, config)
		}
	}
	if strings.Contains(config, "host-local") {
		t.Errorf("pinned CNI config must not keep host-local IPAM: %s", config)
	}

	// Annotation: a pinned attachment renders the JSON selection form with the
	// address; an unpinned attachment accumulating onto it is carried along.
	tmpl := &corev1.PodTemplateSpec{}
	if err := m.MutatePod(pinned, tmpl); err != nil {
		t.Fatal(err)
	}
	plain := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "proj_back"}, nil)
	if err := m.MutatePod(plain, tmpl); err != nil {
		t.Fatal(err)
	}
	got := tmpl.Annotations[networksAnnotation]
	want := `[{"name":"` + pinned.NetLabel + `","ips":["10.222.14.7/24"]},{"name":"` + plain.NetLabel + `"}]`
	if got != want {
		t.Errorf("networks annotation = %s, want %s", got, want)
	}

	// The reverse order also converges on JSON (a comma list is upgraded the
	// moment a pinned attachment joins it).
	tmpl2 := &corev1.PodTemplateSpec{}
	if err := m.MutatePod(plain, tmpl2); err != nil {
		t.Fatal(err)
	}
	if got := tmpl2.Annotations[networksAnnotation]; got != plain.NetLabel {
		t.Errorf("unpinned-only annotation = %q, want the byte-identical comma form", got)
	}
	if err := m.MutatePod(pinned, tmpl2); err != nil {
		t.Fatal(err)
	}
	want2 := `[{"name":"` + plain.NetLabel + `"},{"name":"` + pinned.NetLabel + `","ips":["10.222.14.7/24"]}]`
	if got := tmpl2.Annotations[networksAnnotation]; got != want2 {
		t.Errorf("upgraded annotation = %s, want %s", got, want2)
	}

	// A detached attachment ignores the pin and keeps the name-only default
	// network annotation (byte-identical detached behaviour).
	det := attachment(api.DeploySpec{}, api.NetworkAttachment{Name: "proj_main", Default: true, IP: "10.222.14.9/24"}, nil)
	tmpl3 := &corev1.PodTemplateSpec{}
	if err := m.MutatePod(det, tmpl3); err != nil {
		t.Fatal(err)
	}
	if got := tmpl3.Annotations[defaultNetworkAnnotation]; got != det.NetLabel {
		t.Errorf("default-network annotation = %q, want name-only %q", got, det.NetLabel)
	}
	detObjs, err := m.NetworkScoped(det)
	if err != nil {
		t.Fatal(err)
	}
	detConfig, _, _ := unstructuredNestedString(detObjs[0].Unstructured.Object, "spec", "config")
	if !strings.Contains(detConfig, "host-local") {
		t.Errorf("detached NAD must keep host-local IPAM: %s", detConfig)
	}
}

// TestComposeSubnetDerivationInSync pins the contract between the compose
// planner's plan-time allocator and this package: both must derive the SAME
// default subnet for a network (compose pins addresses inside the subnet the
// NAD would use), so compose.MultusDefaultSubnet replicates
// subnetFor(netLabelName(name), "") and this test keeps them in lockstep.
func TestComposeSubnetDerivationInSync(t *testing.T) {
	for _, name := range []string{"proj_front", "mesh_mesh", "UPPER case!", "a", strings.Repeat("x", 80)} {
		if got, want := compose.MultusDefaultSubnet(name), subnetFor(netLabelName(name), ""); got != want {
			t.Errorf("MultusDefaultSubnet(%q) = %s, netdriver derives %s — the derivations drifted", name, got, want)
		}
	}
}

// TestMultusActive covers the backend's RequireUserNet gate: active only when a
// Multus-driver attachment actually resolves against a capable cluster.
func TestMultusActive(t *testing.T) {
	spec := api.DeploySpec{
		Name:     "proj-web",
		Networks: []api.NetworkAttachment{{Name: "proj_front", Driver: "bridge", IP: "10.222.14.7/24"}},
	}

	capable := fake.NewSimpleClientset()
	multusCapable(t, capable)
	e := New(capable, newFakeDynamic(t), "default")
	if !e.MultusActive(spec) {
		t.Error("MultusActive = false on a Multus-capable cluster with a bridge attachment")
	}

	// No NAD CRD: the bridge driver falls back to services-only, so the pinned
	// secondary addresses will never exist.
	e = New(fake.NewSimpleClientset(), nil, "default")
	if e.MultusActive(spec) {
		t.Error("MultusActive = true without the Multus capability")
	}

	// A services-driver spec is never Multus-active.
	e = New(capable, newFakeDynamic(t), "default")
	if e.MultusActive(api.DeploySpec{Networks: []api.NetworkAttachment{{Name: "n", Driver: "services"}}}) {
		t.Error("MultusActive = true for a services-only spec")
	}
}
