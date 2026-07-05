package svcforward

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func TestWaitClosedReturnsWhenClosed(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if !waitClosed(done, time.Second) {
		t.Fatal("waitClosed(closed) = false; want true")
	}
}

func TestWaitClosedTimesOut(t *testing.T) {
	// A never-closed channel (mirrors a stuck ForwardPorts goroutine) must not
	// block waitClosed past its grace period.
	done := make(chan struct{})
	start := time.Now()
	if waitClosed(done, 20*time.Millisecond) {
		t.Fatal("waitClosed(never) = true; want false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitClosed blocked %v; want to return near its grace period", elapsed)
	}
}

func svc(ns, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

// slice builds an EndpointSlice as the EndpointSlice controller writes them: named
// arbitrarily (here <service>-<suffix>) and discoverable only by the
// kubernetes.io/service-name label.
func slice(ns, service, name string, endpoints []discoveryv1.Endpoint, ports ...discoveryv1.EndpointPort) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   endpoints,
		Ports:       ports,
	}
}

func port(name string, num int32) discoveryv1.EndpointPort {
	p := discoveryv1.EndpointPort{Port: ptr.To(num)}
	if name != "" {
		p.Name = ptr.To(name)
	}
	return p
}

func podEndpoint(pod string, ready bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses:  []string{"10.0.0.1"},
		Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(ready)},
		TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: pod},
	}
}

func TestResolveEndpointSinglePort(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("cornus", "cornus", corev1.ServicePort{Port: 5000}),
		slice("cornus", "cornus", "cornus-x7q2p",
			[]discoveryv1.Endpoint{podEndpoint("cornus-0", true)},
			port("", 8080)),
	)
	// remotePort 0 selects the sole port; the pod target port is the endpoint port.
	pod, port, err := resolveEndpoint(context.Background(), cs, "cornus", "cornus", 0)
	if err != nil || pod != "cornus-0" || port != 8080 {
		t.Fatalf("resolveEndpoint = %q, %d, %v; want cornus-0, 8080, nil", pod, port, err)
	}
	// An explicit matching remote port works too.
	if pod, port, err := resolveEndpoint(context.Background(), cs, "cornus", "cornus", 5000); err != nil || pod != "cornus-0" || port != 8080 {
		t.Fatalf("resolveEndpoint(5000) = %q, %d, %v", pod, port, err)
	}
	// A non-existent remote port errors.
	if _, _, err := resolveEndpoint(context.Background(), cs, "cornus", "cornus", 9999); err == nil {
		t.Error("resolveEndpoint(9999) = nil error, want error")
	}
}

func TestResolveEndpointNamedMultiPort(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("ns", "multi",
			corev1.ServicePort{Name: "http", Port: 80},
			corev1.ServicePort{Name: "api", Port: 5000},
		),
		slice("ns", "multi", "multi-j4k1z",
			[]discoveryv1.Endpoint{podEndpoint("multi-abc", true)},
			port("http", 8080), port("api", 9090)),
	)
	// The requested service port (5000, "api") maps to endpoint port 9090.
	pod, port, err := resolveEndpoint(context.Background(), cs, "ns", "multi", 5000)
	if err != nil || pod != "multi-abc" || port != 9090 {
		t.Fatalf("resolveEndpoint = %q, %d, %v; want multi-abc, 9090, nil", pod, port, err)
	}
	// Ambiguous: multiple ports and no remote port selected.
	if _, _, err := resolveEndpoint(context.Background(), cs, "ns", "multi", 0); err == nil {
		t.Error("resolveEndpoint(multi-port, 0) = nil error, want error")
	}
}

func TestResolveEndpointNoReadyPods(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("ns", "cornus", corev1.ServicePort{Port: 5000}),
		slice("ns", "cornus", "cornus-abc12",
			// The only endpoint is not ready: no ready backing pod.
			[]discoveryv1.Endpoint{podEndpoint("cornus-0", false)},
			port("", 8080)),
	)
	if _, _, err := resolveEndpoint(context.Background(), cs, "ns", "cornus", 0); err == nil {
		t.Error("resolveEndpoint(no ready pods) = nil error, want error")
	}
}

// TestResolveEndpointTerminatingPodSkipped covers a slice written with Ready unset
// (nil, which the API contract says to read as ready) but Terminating true: the pod
// is going away and must not be dialled.
func TestResolveEndpointTerminatingPodSkipped(t *testing.T) {
	term := podEndpoint("cornus-0", true)
	term.Conditions = discoveryv1.EndpointConditions{Terminating: ptr.To(true)}
	live := podEndpoint("cornus-1", true)
	cs := fake.NewSimpleClientset(
		svc("ns", "cornus", corev1.ServicePort{Port: 5000}),
		slice("ns", "cornus", "cornus-t3rm1",
			[]discoveryv1.Endpoint{term, live}, port("", 8080)),
	)
	pod, p, err := resolveEndpoint(context.Background(), cs, "ns", "cornus", 0)
	if err != nil || pod != "cornus-1" || p != 8080 {
		t.Fatalf("resolveEndpoint = %q, %d, %v; want cornus-1, 8080, nil", pod, p, err)
	}
}

// TestResolveEndpointShardedSlices covers a Service whose endpoints are spread over
// several slices: the pod lives in a later shard, and slices for other Services in
// the same namespace must not be picked up (they carry a different service-name
// label, which is the only thing tying a slice to its Service).
func TestResolveEndpointShardedSlices(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("ns", "cornus", corev1.ServicePort{Port: 5000}),
		// Named to sort before the cornus slices, so listing the namespace without
		// the service-name selector would return this foreign pod first.
		slice("ns", "other", "aaa-other-slice",
			[]discoveryv1.Endpoint{podEndpoint("other-0", true)}, port("", 8080)),
		slice("ns", "cornus", "cornus-shard1",
			[]discoveryv1.Endpoint{podEndpoint("cornus-0", false)}, port("", 8080)),
		slice("ns", "cornus", "cornus-shard2",
			[]discoveryv1.Endpoint{podEndpoint("cornus-1", true)}, port("", 8080)),
	)
	pod, p, err := resolveEndpoint(context.Background(), cs, "ns", "cornus", 0)
	if err != nil || pod != "cornus-1" || p != 8080 {
		t.Fatalf("resolveEndpoint = %q, %d, %v; want cornus-1, 8080, nil", pod, p, err)
	}
}

// TestResolveEndpointDoesNotReadDeprecatedEndpoints is the regression guard for the
// deprecation warning the apiserver returns for every core/v1 Endpoints request on
// Kubernetes 1.33+ ("v1 Endpoints is deprecated in v1.33+; use discovery.k8s.io/v1
// EndpointSlice"), which client-go logs through klog and which surfaced during
// `cornus setup`. Resolution must touch endpointslices and never endpoints.
func TestResolveEndpointDoesNotReadDeprecatedEndpoints(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("ns", "cornus", corev1.ServicePort{Port: 5000}),
		slice("ns", "cornus", "cornus-p0q9r",
			[]discoveryv1.Endpoint{podEndpoint("cornus-0", true)}, port("", 8080)),
	)
	if _, _, err := resolveEndpoint(context.Background(), cs, "ns", "cornus", 0); err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	sawSlices := false
	for _, a := range cs.Actions() {
		switch a.GetResource().Resource {
		case "endpoints":
			t.Errorf("resolveEndpoint issued a deprecated core/v1 Endpoints %s", a.GetVerb())
		case "endpointslices":
			sawSlices = true
		}
	}
	if !sawSlices {
		t.Error("resolveEndpoint issued no endpointslices request; the guard above proves nothing")
	}
}

func TestResolveEndpointMissingService(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if _, _, err := resolveEndpoint(context.Background(), cs, "ns", "absent", 0); err == nil {
		t.Error("resolveEndpoint(missing service) = nil error, want error")
	}
}
