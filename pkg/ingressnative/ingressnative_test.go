package ingressnative

import (
	"context"
	"net"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestNewDefaultsPorts(t *testing.T) {
	d := New(Controller{Namespace: "ns", Service: "ctrl"})
	if d.HTTPPort() != 80 || d.HTTPSPort() != 443 {
		t.Fatalf("default ports = %d/%d, want 80/443", d.HTTPPort(), d.HTTPSPort())
	}
	d = New(Controller{Namespace: "ns", Service: "ctrl", HTTPPort: 8080, HTTPSPort: 8443})
	if d.HTTPPort() != 8080 || d.HTTPSPort() != 8443 {
		t.Fatalf("explicit ports = %d/%d", d.HTTPPort(), d.HTTPSPort())
	}
}

func TestDialServiceResolvesThenDials(t *testing.T) {
	d := New(Controller{KubeContext: "kctx", Namespace: "ingress-nginx", Service: "ctrl"})

	var loadCtx, loadNs string
	d.load = func(kctx, ns string) (kubernetes.Interface, *rest.Config, string, error) {
		loadCtx, loadNs = kctx, ns
		return nil, nil, ns, nil
	}
	var resNs, resSvc string
	var resPort int
	d.resolve = func(_ context.Context, _ kubernetes.Interface, ns, service string, remotePort int) (string, int, error) {
		resNs, resSvc, resPort = ns, service, remotePort
		return "ctrl-pod-1", 8443, nil
	}
	var dialPod string
	var dialPort int
	d.dial = func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, ns, pod string, port int) (net.Conn, error) {
		dialPod, dialPort = pod, port
		near, far := net.Pipe()
		_ = far.Close()
		return near, nil
	}

	conn, err := d.DialService(context.Background(), 443)
	if err != nil {
		t.Fatalf("DialService: %v", err)
	}
	_ = conn.Close()

	if loadCtx != "kctx" || loadNs != "ingress-nginx" {
		t.Errorf("load(%q,%q), want (kctx, ingress-nginx)", loadCtx, loadNs)
	}
	if resNs != "ingress-nginx" || resSvc != "ctrl" || resPort != 443 {
		t.Errorf("resolve(ns=%q, svc=%q, port=%d), want (ingress-nginx, ctrl, 443)", resNs, resSvc, resPort)
	}
	if dialPod != "ctrl-pod-1" || dialPort != 8443 {
		t.Errorf("dial(pod=%q, port=%d), want (ctrl-pod-1, 8443)", dialPod, dialPort)
	}
}

func TestDialServiceNoServiceErrors(t *testing.T) {
	d := New(Controller{Namespace: "ns"})
	if _, err := d.DialService(context.Background(), 80); err == nil {
		t.Fatal("DialService with no controller service should error")
	}
}
