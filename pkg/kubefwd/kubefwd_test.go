package kubefwd

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"cornus/pkg/deploy"
)

func pod(name, resource string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{deploy.LabelManaged: "true", deploy.LabelApp: resource},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// testDialer builds a Dialer whose kubeconfig load and pod dial are stubbed, so
// pod resolution and caching can be exercised without a live API server. It
// records the pods dialPod was asked to reach and how many times load ran.
type dialRecord struct {
	pod  string
	port int
}

func testDialer(cs kubernetes.Interface, loadErr error) (*Dialer, *[]dialRecord, *int) {
	var dialed []dialRecord
	loads := 0
	d := &Dialer{
		load: func(_, _ string) (kubernetes.Interface, *rest.Config, string, error) {
			loads++
			if loadErr != nil {
				return nil, nil, "", loadErr
			}
			return cs, &rest.Config{}, "default", nil
		},
		dialPod: func(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _, podName string, port int) (net.Conn, error) {
			dialed = append(dialed, dialRecord{pod: podName, port: port})
			c, _ := net.Pipe()
			return c, nil
		},
	}
	return d, &dialed, &loads
}

func TestPortForwardRejectsUDP(t *testing.T) {
	d, _, loads := testDialer(fake.NewSimpleClientset(), nil)
	if _, err := d.PortForward(context.Background(), "proj-web", 53, "udp"); err == nil {
		t.Fatal("expected udp to be rejected")
	}
	if *loads != 0 {
		t.Fatalf("udp rejection should short-circuit before loading kubeconfig; loads=%d", *loads)
	}
}

func TestPortForwardResolvesPod(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("web-abc", "proj-web", corev1.PodRunning))
	d, dialed, _ := testDialer(cs, nil)

	conn, err := d.PortForward(context.Background(), "proj-web", 8080, "tcp")
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}
	defer conn.Close()
	if len(*dialed) != 1 || (*dialed)[0].pod != "web-abc" || (*dialed)[0].port != 8080 {
		t.Fatalf("dialed = %+v, want one dial to web-abc:8080", *dialed)
	}
}

func TestPortForwardNoPod(t *testing.T) {
	d, _, _ := testDialer(fake.NewSimpleClientset(), nil)
	_, err := d.PortForward(context.Background(), "proj-web", 8080, "tcp")
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPortForwardCachesLoad(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("web-abc", "proj-web", corev1.PodRunning))
	d, _, loads := testDialer(cs, nil)

	for i := 0; i < 3; i++ {
		c, err := d.PortForward(context.Background(), "proj-web", 8080, "tcp")
		if err != nil {
			t.Fatalf("PortForward #%d: %v", i, err)
		}
		c.Close()
	}
	if *loads != 1 {
		t.Fatalf("kubeconfig should load once and be cached; loads=%d", *loads)
	}
}

func TestPortForwardCachesLoadError(t *testing.T) {
	d, _, loads := testDialer(nil, errors.New("no kubeconfig"))
	for i := 0; i < 2; i++ {
		if _, err := d.PortForward(context.Background(), "proj-web", 8080, "tcp"); err == nil {
			t.Fatalf("PortForward #%d: expected load error", i)
		}
	}
	if *loads != 1 {
		t.Fatalf("a load failure should be cached, not re-probed; loads=%d", *loads)
	}
}

// stubDialer is a portfwd.Dialer for exercising Fallback.
type stubDialer struct {
	err    error
	called *int
}

func (s stubDialer) PortForward(_ context.Context, _ string, _ int, _ string) (net.Conn, error) {
	if s.called != nil {
		*s.called++
	}
	if s.err != nil {
		return nil, s.err
	}
	c, _ := net.Pipe()
	return c, nil
}

func TestFallbackPrimarySucceeds(t *testing.T) {
	secondaryCalls := 0
	f := Fallback{Primary: stubDialer{}, Secondary: stubDialer{called: &secondaryCalls}}
	c, err := f.PortForward(context.Background(), "web", 80, "tcp")
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}
	c.Close()
	if secondaryCalls != 0 {
		t.Fatalf("secondary should not be called when primary succeeds; calls=%d", secondaryCalls)
	}
}

func TestFallbackToSecondary(t *testing.T) {
	secondaryCalls := 0
	f := Fallback{
		Primary:   stubDialer{err: errors.New("rbac denied")},
		Secondary: stubDialer{called: &secondaryCalls},
	}
	c, err := f.PortForward(context.Background(), "web", 80, "tcp")
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}
	c.Close()
	if secondaryCalls != 1 {
		t.Fatalf("secondary should be used when primary fails; calls=%d", secondaryCalls)
	}
}

func TestFallbackBothFail(t *testing.T) {
	f := Fallback{
		Primary:   stubDialer{err: errors.New("rbac denied on pods/portforward")},
		Secondary: stubDialer{err: errors.New("server unreachable")},
	}
	_, err := f.PortForward(context.Background(), "web", 80, "tcp")
	if err == nil {
		t.Fatal("expected an error when both primary and secondary fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rbac denied") || !strings.Contains(msg, "server unreachable") {
		t.Fatalf("combined error should name both attempts; got %q", msg)
	}
}

func TestFallbackNotOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	secondaryCalls := 0
	f := Fallback{
		Primary:   stubDialer{err: errors.New("cancelled")},
		Secondary: stubDialer{called: &secondaryCalls},
	}
	if _, err := f.PortForward(ctx, "web", 80, "tcp"); err == nil {
		t.Fatal("expected an error on a cancelled context")
	}
	if secondaryCalls != 0 {
		t.Fatalf("a cancelled context must not fall back; calls=%d", secondaryCalls)
	}
}
