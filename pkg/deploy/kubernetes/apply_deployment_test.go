package kubernetes

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"cornus/pkg/api"
)

// TestApplyWaitsOutTerminatingDeployment is the regression guard for a re-deploy
// that races the deletion of a same-name Deployment. Delete uses foreground
// propagation, so the old object stays alive (DeletionTimestamp set) until its
// ReplicaSet and pods finish terminating. Updating that doomed object "succeeds"
// and then evaporates when the GC completes, so the deploy reports no error and
// never gets a pod — the silent wedge that timed out `deploy-hub.star` on the CI
// kube leg (run 30359492738) after the harness reaped the previous scenario's
// same-named deployment moments earlier. Apply must wait the old object out and
// create a fresh one instead.
func TestApplyWaitsOutTerminatingDeployment(t *testing.T) {
	prev := deploymentPollInterval
	deploymentPollInterval = time.Millisecond
	defer func() { deploymentPollInterval = prev }()

	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")

	// The old deployment lingers (DeletionTimestamp set) for `linger` gets, then
	// the GC finishes and it is gone.
	const linger = 3
	del := metav1.Now()
	gets, updates := 0, 0
	terminating := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "web", Namespace: "default", DeletionTimestamp: &del,
	}}
	cs.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets <= linger {
			return true, terminating, nil
		}
		return false, nil, nil // GC done: fall through to the tracker
	})
	cs.PrependReactor("update", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})

	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply must wait out the terminating deployment and recreate, got %v", err)
	}
	if updates != 0 {
		t.Fatalf("Apply must not update a terminating deployment (it would evaporate with the GC), got %d update(s)", updates)
	}
	got, err := cs.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("a fresh deployment must exist after Apply: %v", err)
	}
	if got.DeletionTimestamp != nil {
		t.Fatal("Apply adopted the doomed deployment instead of creating a fresh one")
	}
}

// TestApplyUpdatesLiveDeployment is the contrast: an existing LIVE deployment of
// the same name is updated in place (a normal re-deploy / rollout), not deleted
// and recreated.
func TestApplyUpdatesLiveDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("initial Apply: %v", err)
	}
	updates := 0
	cs.PrependReactor("update", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "img2"}); err != nil {
		t.Fatalf("re-Apply: %v", err)
	}
	if updates == 0 {
		t.Fatal("re-applying over a live deployment must update it in place")
	}
	got, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after re-apply: %v", err)
	}
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "img2" {
		t.Fatalf("update did not take: image = %q, want img2", img)
	}
}

// TestApplyRecreatesWhenDeploymentVanishesMidUpdate covers the narrow window where
// the object is live at the first Get but gone by the time the update retry
// re-fetches it (a concurrent delete that completed). Apply must fall back to
// creating it rather than surfacing a NotFound.
func TestApplyRecreatesWhenDeploymentVanishesMidUpdate(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")

	gets := 0
	cs.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		switch gets {
		case 1: // applyDeployment's first look: live
			return true, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}, nil
		case 2: // the update retry's re-fetch: the concurrent delete completed
			return true, nil, apierrors.NewNotFound(appsv1.Resource("deployments"), "web")
		}
		return false, nil, nil
	})

	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply must recreate a deployment that vanished mid-update, got %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("deployment must exist after Apply: %v", err)
	}
}
