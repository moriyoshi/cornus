package kubernetes

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newTestPVC(name string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
}

// TestEnsurePVCFreshAndLive: a fresh claim is created, and an existing LIVE claim of
// the same name is reused without error (its spec is immutable).
func TestEnsurePVCFreshAndLive(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if err := b.ensurePVC(ctx, newTestPVC("web-vol-0")); err != nil {
		t.Fatalf("fresh create: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, "web-vol-0", metav1.GetOptions{}); err != nil {
		t.Fatalf("claim should exist after ensurePVC: %v", err)
	}
	if err := b.ensurePVC(ctx, newTestPVC("web-vol-0")); err != nil {
		t.Fatalf("reuse of a live claim must not error: %v", err)
	}
}

// TestEnsurePVCWaitsOutTerminatingClaim is the regression guard: a same-name claim
// that is mid-deletion (its owning workload was just removed, cascading a GC) must
// be waited out and recreated fresh — NOT silently adopted, which left the new pod
// wedged Unschedulable ("persistentvolumeclaim ... not found") once the GC finished.
func TestEnsurePVCWaitsOutTerminatingClaim(t *testing.T) {
	prev := pvcPollInterval
	pvcPollInterval = time.Millisecond
	defer func() { pvcPollInterval = prev }()

	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")

	// The old claim lingers (DeletionTimestamp set) for `linger` polls, then is gone.
	const linger = 3
	gets, creates := 0, 0
	del := metav1.Now()
	terminating := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "web-vol-0", Namespace: "default", DeletionTimestamp: &del,
	}}
	cs.PrependReactor("create", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		creates++
		if gets < linger { // still racing the lingering claim
			return true, nil, apierrors.NewAlreadyExists(corev1.Resource("persistentvolumeclaims"), "web-vol-0")
		}
		return false, nil, nil // GC done: let the tracker create it
	})
	cs.PrependReactor("get", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets <= linger {
			return true, terminating, nil
		}
		return true, nil, apierrors.NewNotFound(corev1.Resource("persistentvolumeclaims"), "web-vol-0")
	})

	if err := b.ensurePVC(context.Background(), newTestPVC("web-vol-0")); err != nil {
		t.Fatalf("ensurePVC must wait out the terminating claim and recreate, got %v", err)
	}
	if creates < 2 {
		t.Fatalf("expected repeated create attempts while the claim terminated, got %d", creates)
	}
}
