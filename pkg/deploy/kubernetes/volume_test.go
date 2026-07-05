package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestRemoveVolume checks the deploy.VolumeRemover path: RemoveVolume deletes the
// PVC that backs a named, project-scoped volume (keyed by namedPVCName), and
// removing an absent volume is a no-op success (delete-if-exists).
func TestRemoveVolume(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	pvc := namedPVCName("proj_cache")
	if _, err := cs.CoreV1().PersistentVolumeClaims("default").Create(ctx,
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvc}},
		metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed PVC: %v", err)
	}

	if err := b.RemoveVolume(ctx, "proj_cache"); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, pvc, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("PVC %s survived RemoveVolume (get err = %v)", pvc, err)
	}

	if err := b.RemoveVolume(ctx, "does_not_exist"); err != nil {
		t.Fatalf("RemoveVolume of an absent volume should succeed, got %v", err)
	}
}
