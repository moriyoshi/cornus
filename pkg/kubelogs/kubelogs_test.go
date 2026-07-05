package kubelogs

import (
	"context"
	"errors"
	"io"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

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

// TestOpenStreamsPod confirms open selects the deployment's pod by app label and
// returns a readable log stream. The fake clientset's GetLogs yields "fake logs".
func TestOpenStreamsPod(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("web-abc123", "proj-web", corev1.PodRunning))

	rc, err := open(context.Background(), cs, "default", Options{Resource: "proj-web", Follow: true, Tail: "10"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "fake logs" {
		t.Fatalf("logs = %q, want %q", string(b), "fake logs")
	}
}

// TestOpenSelectsByLabel confirms a pod for a different deployment is not picked.
func TestOpenSelectsByLabel(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("other-xyz", "proj-db", corev1.PodRunning))

	_, err := open(context.Background(), cs, "default", Options{Resource: "proj-web"})
	if err == nil {
		t.Fatal("expected an error when no pod matches the app label")
	}
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestOpenNoPods reports a not-found error when the deployment has no pods.
func TestOpenNoPods(t *testing.T) {
	cs := fake.NewSimpleClientset()

	_, err := open(context.Background(), cs, "default", Options{Resource: "missing"})
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestOpenInvalidTail surfaces a bad tail value rather than degrading to all logs.
func TestOpenInvalidTail(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("web-abc123", "proj-web", corev1.PodRunning))

	_, err := open(context.Background(), cs, "default", Options{Resource: "proj-web", Tail: "20x"})
	if err == nil {
		t.Fatal("expected an error for an invalid tail value")
	}
}

// TestOpenInvalidSince surfaces a bad since value rather than degrading to all logs.
func TestOpenInvalidSince(t *testing.T) {
	cs := fake.NewSimpleClientset(pod("web-abc123", "proj-web", corev1.PodRunning))

	_, err := open(context.Background(), cs, "default", Options{Resource: "proj-web", Since: "not-a-time"})
	if err == nil {
		t.Fatal("expected an error for an invalid since value")
	}
}
