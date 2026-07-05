package kubernetes

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
)

// TestListIncludesOneShotJobs pins that a one-shot workload appears in List.
//
// The regression: List enumerated AppsV1().Deployments only, while a `restart: no`
// / `on-failure` spec (deploy.IsOneShot) is realized as a Job. Such a workload was
// therefore invisible to List — and invisible is worse than absent here, because
// pkg/server/logrecorder.go drives its per-replica log tails off List. A one-shot
// workload on kubernetes was never tailed: nothing was recorded and nothing
// reported an error, which is precisely the case a durable log store exists for
// (the container is gone by the time anyone asks). Three E2E scenarios failed on
// this one cause.
//
// Status and Delete had always handled Deployments, Jobs, and Knative Services;
// List was the only one that did not, which is how the gap survived review.
func TestListIncludesOneShotJobs(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// A long-running workload (Deployment) and a one-shot workload (Job).
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("apply web: %v", err)
	}
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "migrate", Image: "img", Restart: "no"}); err != nil {
		t.Fatalf("apply migrate: %v", err)
	}

	// Guard the premise: the one-shot really is a Job, not a Deployment. Without
	// this the test could pass for the wrong reason if applyWorkload ever changed.
	if _, err := cs.BatchV1().Jobs("default").Get(ctx, "migrate", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected a Job for the one-shot workload: %v", err)
	}

	got, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, st := range got {
		names[st.Name] = true
	}
	if !names["web"] {
		t.Error("List dropped the long-running workload")
	}
	if !names["migrate"] {
		t.Error("List dropped the one-shot workload: the log recorder tails off List, so its output is never captured and nothing reports an error")
	}
	if len(got) != 2 {
		t.Errorf("List returned %d workloads, want 2: %+v", len(got), got)
	}
	// Sorted by name, as the doc comment and every caller assume.
	if len(got) == 2 && got[0].Name != "migrate" {
		t.Errorf("List is not sorted by name: %q before %q", got[0].Name, got[1].Name)
	}
}

// TestListDeduplicatesByName covers the transitional case the dedup exists for: a
// spec edited from long-running to one-shot leaves the old Deployment in place
// until Apply converges, so both objects carry the same name for a moment.
// Reporting it twice would make replica arithmetic wrong for every caller.
func TestListDeduplicatesByName(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "worker", Image: "img"}); err != nil {
		t.Fatalf("apply long-running: %v", err)
	}
	// Now the same name as a one-shot, without deleting the Deployment first.
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "worker", Image: "img", Restart: "on-failure"}); err != nil {
		t.Fatalf("apply one-shot: %v", err)
	}

	got, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, st := range got {
		if st.Name == "worker" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("List reported %q %d times, want 1: a name realized by two object kinds must appear once", "worker", count)
	}
}
