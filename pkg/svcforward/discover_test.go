package svcforward

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// labeledSvc builds a Service with the given labels, clusterIP and ports.
func labeledSvc(ns, name, clusterIP string, labels map[string]string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.ServiceSpec{ClusterIP: clusterIP, Ports: ports},
	}
}

var (
	helmLabels     = map[string]string{"app.kubernetes.io/name": "cornus", "app.kubernetes.io/instance": "prod"}
	manifestLabels = map[string]string{"app": "cornus"}
)

func TestDiscoverHelm(t *testing.T) {
	cs := fake.NewSimpleClientset(
		labeledSvc("cornus-system", "prod-cornus", "10.0.0.1", helmLabels,
			corev1.ServicePort{Name: "http", Port: 5000}),
	)
	res, err := discover(context.Background(), cs, "cornus-system")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if res.Service != "prod-cornus" || res.RemotePort != 5000 || res.Managed != "helm" {
		t.Fatalf("discover = %+v; want prod-cornus/5000/helm", res)
	}
}

func TestDiscoverManifest(t *testing.T) {
	cs := fake.NewSimpleClientset(
		labeledSvc("cornus", "cornus", "10.0.0.2", manifestLabels,
			corev1.ServicePort{Name: "http", Port: 5000}),
	)
	res, err := discover(context.Background(), cs, "cornus")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if res.Service != "cornus" || res.RemotePort != 5000 || res.Managed != "manifest" {
		t.Fatalf("discover = %+v; want cornus/5000/manifest", res)
	}
}

func TestDiscoverExcludesHeadlessHub(t *testing.T) {
	cs := fake.NewSimpleClientset(
		// The Helm headless hub Service shares the labels but is inter-replica
		// traffic (clusterIP None); the client-facing ClusterIP Service wins.
		labeledSvc("cornus-system", "prod-cornus-hub", corev1.ClusterIPNone, helmLabels,
			corev1.ServicePort{Name: "http", Port: 5000}),
		labeledSvc("cornus-system", "prod-cornus", "10.0.0.1", helmLabels,
			corev1.ServicePort{Name: "http", Port: 5000}),
	)
	res, err := discover(context.Background(), cs, "cornus-system")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if res.Service != "prod-cornus" {
		t.Fatalf("discover = %+v; want the client-facing prod-cornus", res)
	}
}

func TestDiscoverNoMatch(t *testing.T) {
	cs := fake.NewSimpleClientset(
		labeledSvc("other", "unrelated", "10.0.0.9", map[string]string{"app": "nginx"},
			corev1.ServicePort{Port: 80}),
	)
	_, err := discover(context.Background(), cs, "other")
	if err == nil || !strings.Contains(err.Error(), "no cornus service") {
		t.Fatalf("discover(no match) err = %v; want a no-cornus-service error", err)
	}
}

func TestDiscoverAmbiguous(t *testing.T) {
	cs := fake.NewSimpleClientset(
		labeledSvc("cornus-system", "prod-cornus", "10.0.0.1", helmLabels,
			corev1.ServicePort{Name: "http", Port: 5000}),
		labeledSvc("cornus-system", "staging-cornus", "10.0.0.2", helmLabels,
			corev1.ServicePort{Name: "http", Port: 5000}),
	)
	_, err := discover(context.Background(), cs, "cornus-system")
	if err == nil {
		t.Fatal("discover(ambiguous) = nil error, want error")
	}
	// The message lists both candidates so the user can pick with --pf-service.
	for _, want := range []string{"prod-cornus", "staging-cornus", "--pf-service"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestDiscoverUnnamedSinglePort(t *testing.T) {
	// A single non-http, non-5000 port is still resolved (it is the sole port).
	cs := fake.NewSimpleClientset(
		labeledSvc("ns", "cornus", "10.0.0.1", manifestLabels,
			corev1.ServicePort{Port: 8080}),
	)
	res, err := discover(context.Background(), cs, "ns")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if res.RemotePort != 8080 {
		t.Fatalf("RemotePort = %d, want 8080", res.RemotePort)
	}
}

func TestDiscoverMultiPortUnidentifiable(t *testing.T) {
	// Multiple ports, none named http or numbered 5000: ask for --pf-remote-port.
	cs := fake.NewSimpleClientset(
		labeledSvc("ns", "cornus", "10.0.0.1", manifestLabels,
			corev1.ServicePort{Name: "a", Port: 8080},
			corev1.ServicePort{Name: "b", Port: 9090}),
	)
	_, err := discover(context.Background(), cs, "ns")
	if err == nil || !strings.Contains(err.Error(), "--pf-remote-port") {
		t.Fatalf("discover(multi-port) err = %v; want a --pf-remote-port hint", err)
	}
}
