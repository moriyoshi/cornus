package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// newKnativeBackend builds a backend over a fake typed clientset and a dynamic
// fake that knows the ksvc list kind. When served is true, the fake discovery
// advertises serving.knative.dev/v1 so knativeServed() reports the round-trip is
// available.
func newKnativeBackend(t *testing.T, served bool) (*Backend, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	t.Setenv("CORNUS_K8S_SIDECAR_IMAGE", "cornus:test")
	cs := fake.NewSimpleClientset()
	// Register the ksvc list kind plus the netdriver CRD list kinds — Delete runs
	// the netdriver GC, which LISTs NADs/CNPs and panics if their list kinds are
	// unregistered.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		knativeServiceGVR: "ServiceList",
		{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}: "NetworkAttachmentDefinitionList",
		{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}:                "CiliumNetworkPolicyList",
	})
	if served {
		fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
		if !ok {
			t.Fatal("fake discovery type assertion failed")
		}
		fd.Resources = append(fd.Resources, &metav1.APIResourceList{
			GroupVersion: "serving.knative.dev/v1",
			APIResources: []metav1.APIResource{{Name: "services"}},
		})
	}
	return NewWithClients(cs, dyn, "default"), dyn
}

func getKsvc(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name string) *unstructured.Unstructured {
	t.Helper()
	obj, err := dyn.Resource(knativeServiceGVR).Namespace("default").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ksvc %s: %v", name, err)
	}
	return obj
}

// asNum coerces an unstructured numeric (int64 or float64) to int64.
func asNum(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

func TestApplyCreatesKnativeService(t *testing.T) {
	b, dyn := newKnativeBackend(t, true)
	spec := api.DeploySpec{
		Name:       "hello",
		Image:      "ghcr.io/x/hello:1",
		Entrypoint: []string{"/server"},
		Command:    []string{"--port=8080"},
		Env:        map[string]string{"GREETING": "hi"},
		Ports:      []api.PortMapping{{Container: 8080}},
		Knative: &api.KnativeSpec{
			Enabled:     true,
			MinScale:    ptr.To(1),
			MaxScale:    ptr.To(5),
			Concurrency: ptr.To(80),
			Class:       "kpa",
		},
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	obj := getKsvc(t, dyn, "hello")

	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(containers))
	}
	c := containers[0].(map[string]any)
	if c["image"] != "ghcr.io/x/hello:1" {
		t.Errorf("image = %v", c["image"])
	}
	// ksvc container.command is the entrypoint override; args are its arguments.
	if cmd, _ := c["command"].([]any); len(cmd) != 1 || cmd[0] != "/server" {
		t.Errorf("command = %v, want [/server]", c["command"])
	}
	if args, _ := c["args"].([]any); len(args) != 1 || args[0] != "--port=8080" {
		t.Errorf("args = %v, want [--port=8080]", c["args"])
	}
	ports, _ := c["ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("want 1 port, got %v", c["ports"])
	}
	if p, _ := asNum(ports[0].(map[string]any)["containerPort"]); p != 8080 {
		t.Errorf("containerPort = %v, want 8080", ports[0])
	}

	annots, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
	if annots["autoscaling.knative.dev/minScale"] != "1" || annots["autoscaling.knative.dev/maxScale"] != "5" {
		t.Errorf("scale annotations = %v", annots)
	}
	if annots["autoscaling.knative.dev/class"] != "kpa.autoscaling.knative.dev" {
		t.Errorf("class annotation = %v", annots["autoscaling.knative.dev/class"])
	}
	if cc, found, _ := unstructured.NestedInt64(obj.Object, "spec", "template", "spec", "containerConcurrency"); !found || cc != 80 {
		t.Errorf("containerConcurrency = %v (found=%v)", cc, found)
	}
	// The revision template carries cornus.app so pods stay selectable for
	// exec/logs/port-forward/status.
	labels, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "labels")
	if labels[deploy.LabelApp] != "hello" {
		t.Errorf("template %s label = %v", deploy.LabelApp, labels)
	}
	// No Deployment or Service was created — the ksvc owns routing.
	if _, err := b.clientset.AppsV1().Deployments("default").Get(context.Background(), "hello", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected no Deployment, got err=%v", err)
	}
	if _, err := b.clientset.CoreV1().Services("default").Get(context.Background(), "hello", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected no Service, got err=%v", err)
	}
}

func TestApplyKnativePortSelection(t *testing.T) {
	b, dyn := newKnativeBackend(t, true)
	spec := api.DeploySpec{
		Name:    "multi",
		Image:   "nginx",
		Ports:   []api.PortMapping{{Container: 8080}, {Container: 9090}},
		Knative: &api.KnativeSpec{Enabled: true, Port: 9090},
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	obj := getKsvc(t, dyn, "multi")
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	ports, _ := containers[0].(map[string]any)["ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("want single routed port, got %v", ports)
	}
	if p, _ := asNum(ports[0].(map[string]any)["containerPort"]); p != 9090 {
		t.Errorf("routed port = %v, want 9090", ports[0])
	}
}

func TestApplyKnativeBadPortRejected(t *testing.T) {
	b, _ := newKnativeBackend(t, true)
	spec := api.DeploySpec{
		Name:    "bad",
		Image:   "nginx",
		Ports:   []api.PortMapping{{Container: 8080}},
		Knative: &api.KnativeSpec{Enabled: true, Port: 1234},
	}
	if _, err := b.Apply(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want port-mismatch error, got %v", err)
	}
}

func TestApplyDegradesWhenKnativeAbsent(t *testing.T) {
	b, dyn := newKnativeBackend(t, false)
	spec := api.DeploySpec{
		Name:    "web",
		Image:   "nginx",
		Ports:   []api.PortMapping{{Container: 80}},
		Knative: &api.KnativeSpec{Enabled: true},
	}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Falls back to a plain Deployment (+ Service), no ksvc.
	if _, err := b.clientset.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected a Deployment, got %v", err)
	}
	if _, err := dyn.Resource(knativeServiceGVR).Namespace("default").Get(context.Background(), "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected no ksvc, got err=%v", err)
	}
}

func TestApplyKnativeStrictErrorsWhenAbsent(t *testing.T) {
	t.Setenv("CORNUS_KNATIVE_STRICT", "true")
	b, _ := newKnativeBackend(t, false)
	spec := api.DeploySpec{Name: "web", Image: "nginx", Ports: []api.PortMapping{{Container: 80}}, Knative: &api.KnativeSpec{Enabled: true}}
	if _, err := b.Apply(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "CORNUS_KNATIVE_STRICT") {
		t.Fatalf("want strict error, got %v", err)
	}
}

func TestDeleteRemovesKnativeService(t *testing.T) {
	b, dyn := newKnativeBackend(t, true)
	spec := api.DeploySpec{Name: "hello", Image: "nginx", Ports: []api.PortMapping{{Container: 8080}}, Knative: &api.KnativeSpec{Enabled: true}}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(context.Background(), "hello"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := dyn.Resource(knativeServiceGVR).Namespace("default").Get(context.Background(), "hello", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("expected ksvc gone, got err=%v", err)
	}
}

// TestDeleteToleratesKnativeForbidden reproduces the in-cluster RBAC gap: the
// cluster serves Knative (knativeServed) but the ServiceAccount holds no
// serving.knative.dev permissions. cornus deployed a plain Deployment, so `down`
// must not fail just because its speculative ksvc delete is 403 Forbidden — the
// SA could never have owned a ksvc. The speculative status/exists probes must
// read Forbidden the same way (no Knative workload), not surface a hard error.
func TestDeleteToleratesKnativeForbidden(t *testing.T) {
	b, dyn := newKnativeBackend(t, true)
	forbid := func(verb string) {
		dyn.PrependReactor(verb, "services", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "serving.knative.dev", Resource: "services"},
				"web", fmt.Errorf("no serving.knative.dev RBAC"))
		})
	}
	forbid("delete")
	forbid("get")

	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete must tolerate a 403 on the speculative ksvc delete, got %v", err)
	}
	if st, ok, err := b.knativeStatus(context.Background(), "web"); err != nil || ok {
		t.Fatalf("knativeStatus: ok=%v err=%v st=%+v (want false, nil)", ok, err, st)
	}
	if ok, err := b.knativeExists(context.Background(), "web"); err != nil || ok {
		t.Fatalf("knativeExists: ok=%v err=%v (want false, nil)", ok, err)
	}
}

func TestKnativeGuardRejectsOneShot(t *testing.T) {
	b, _ := newKnativeBackend(t, true)
	spec := api.DeploySpec{Name: "job", Image: "busybox", Restart: "no", Knative: &api.KnativeSpec{Enabled: true}}
	if _, err := b.Apply(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "one-shot") {
		t.Fatalf("want one-shot rejection, got %v", err)
	}
}

func TestKnativeGuardRejectsIngress(t *testing.T) {
	b, _ := newKnativeBackend(t, true)
	spec := api.DeploySpec{
		Name:    "web",
		Image:   "nginx",
		Ports:   []api.PortMapping{{Container: 80}},
		Ingress: &api.IngressSpec{Enabled: true},
		Knative: &api.KnativeSpec{Enabled: true},
	}
	if _, err := b.Apply(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "routing") {
		t.Fatalf("want ingress rejection, got %v", err)
	}
}

func TestKnativeRestartCutsNewRevision(t *testing.T) {
	b, dyn := newKnativeBackend(t, true)
	spec := api.DeploySpec{Name: "hello", Image: "nginx", Ports: []api.PortMapping{{Container: 8080}}, Knative: &api.KnativeSpec{Enabled: true}}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Restart(context.Background(), "hello"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	obj := getKsvc(t, dyn, "hello")
	annots, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
	if annots[restartAnnotation] == "" {
		t.Errorf("expected %s annotation on the revision template, got %v", restartAnnotation, annots)
	}
}

func TestKnativeStopStartUnsupported(t *testing.T) {
	b, _ := newKnativeBackend(t, true)
	spec := api.DeploySpec{Name: "hello", Image: "nginx", Ports: []api.PortMapping{{Container: 8080}}, Knative: &api.KnativeSpec{Enabled: true}}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Stop(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Stop: want unsupported error, got %v", err)
	}
	if err := b.Start(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Start: want unsupported error, got %v", err)
	}
}

func TestKnativeStatusReportsURL(t *testing.T) {
	b, dyn := newKnativeBackend(t, true)
	spec := api.DeploySpec{Name: "hello", Image: "nginx", Ports: []api.PortMapping{{Container: 8080}}, Knative: &api.KnativeSpec{Enabled: true}}
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Simulate the Knative controller filling status.url + Ready.
	obj := getKsvc(t, dyn, "hello")
	_ = unstructured.SetNestedField(obj.Object, "http://hello.default.example.com", "status", "url")
	_ = unstructured.SetNestedSlice(obj.Object, []any{
		map[string]any{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	if _, err := dyn.Resource(knativeServiceGVR).Namespace("default").Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	st, err := b.Status(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.URL != "http://hello.default.example.com" {
		t.Errorf("Status URL = %q", st.URL)
	}
	if len(st.Instances) != 1 || st.Instances[0].State != "scaled-to-zero" {
		t.Errorf("instances = %+v, want one scaled-to-zero (no pods)", st.Instances)
	}
}
