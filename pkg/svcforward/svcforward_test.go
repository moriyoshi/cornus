package svcforward

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

func endpoints(ns, name string, subsets ...corev1.EndpointSubset) *corev1.Endpoints {
	return &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Subsets:    subsets,
	}
}

func podAddr(pod string) corev1.EndpointAddress {
	return corev1.EndpointAddress{IP: "10.0.0.1", TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: pod}}
}

func TestResolveEndpointSinglePort(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("cornus", "cornus", corev1.ServicePort{Port: 5000}),
		endpoints("cornus", "cornus", corev1.EndpointSubset{
			Addresses: []corev1.EndpointAddress{podAddr("cornus-0")},
			Ports:     []corev1.EndpointPort{{Port: 8080}},
		}),
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
		endpoints("ns", "multi", corev1.EndpointSubset{
			Addresses: []corev1.EndpointAddress{podAddr("multi-abc")},
			Ports: []corev1.EndpointPort{
				{Name: "http", Port: 8080},
				{Name: "api", Port: 9090},
			},
		}),
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
		endpoints("ns", "cornus", corev1.EndpointSubset{
			// Only not-ready addresses: no ready backing pod.
			NotReadyAddresses: []corev1.EndpointAddress{podAddr("cornus-0")},
			Ports:             []corev1.EndpointPort{{Port: 8080}},
		}),
	)
	if _, _, err := resolveEndpoint(context.Background(), cs, "ns", "cornus", 0); err == nil {
		t.Error("resolveEndpoint(no ready pods) = nil error, want error")
	}
}

func TestResolveEndpointMissingService(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if _, _, err := resolveEndpoint(context.Background(), cs, "ns", "absent", 0); err == nil {
		t.Error("resolveEndpoint(missing service) = nil error, want error")
	}
}
