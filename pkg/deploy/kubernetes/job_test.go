package kubernetes

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
)

// TestApplyOneShotCreatesJob proves a run-to-completion spec (restart: no) is
// deployed as a Job — not a Deployment — so Kubernetes never restarts the
// completed pod (the caretaker-mount crashloop fix). Status resolves it via the
// Job path, and Delete removes the Job.
func TestApplyOneShotCreatesJob(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "init", Image: "img", Restart: "no"}); err != nil {
		t.Fatalf("Apply one-shot: %v", err)
	}
	if _, err := cs.BatchV1().Jobs("default").Get(ctx, "init", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected a Job 'init': %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Get(ctx, "init", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a one-shot must NOT create a Deployment, got err=%v", err)
	}

	// Status resolves through the Job fallback (no pods yet -> a single pending
	// instance), not "not found".
	st, err := b.Status(ctx, "init")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 1 || st.Instances[0].Running {
		t.Fatalf("want 1 non-running (pending) instance, got %+v", st.Instances)
	}

	if err := b.Delete(ctx, "init"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.BatchV1().Jobs("default").Get(ctx, "init", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Delete must remove the Job, got err=%v", err)
	}
}

// TestApplyLongLivedCreatesDeployment is the contrast: a default (long-lived)
// spec stays a Deployment and creates no Job.
func TestApplyLongLivedCreatesDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply service: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("expected a Deployment 'web': %v", err)
	}
	if _, err := cs.BatchV1().Jobs("default").Get(ctx, "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a long-lived service must NOT create a Job, got err=%v", err)
	}
}

// TestStatusOfJobSucceeded confirms a completed one-shot pod is reported as
// not-running with exit 0 — the state the deploy-attach readiness wait accepts as
// ready for a one-shot (allReady), and the compose
// depends_on:service_completed_successfully gate reads.
func TestStatusOfJobSucceeded(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "init", Namespace: "default"},
		Spec:       batchv1.JobSpec{Completions: ptr.To[int32](1), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: execContainer, Image: "img"}}}}},
	}
	zero := int32(0)
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "init-x", Labels: labels("init")},
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  execContainer,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: zero}},
			}},
		},
	}
	st := statusOfJob(job, []corev1.Pod{pod}, "kube")
	if len(st.Instances) != 1 {
		t.Fatalf("want 1 instance, got %d", len(st.Instances))
	}
	in := st.Instances[0]
	if in.Running {
		t.Errorf("a succeeded one-shot must not be Running: %+v", in)
	}
	if in.State != "succeeded" || in.ExitCode == nil || *in.ExitCode != 0 {
		t.Errorf("want state=succeeded exit=0, got state=%q exit=%v", in.State, in.ExitCode)
	}
	if st.Image != "img" {
		t.Errorf("image = %q, want img", st.Image)
	}
}

// TestRepresentativePodPrefersSucceeded confirms a retried Job (a failed pod plus
// a later succeeded one) is described by the succeeded pod, not the stale failure.
func TestRepresentativePodPrefersSucceeded(t *testing.T) {
	failed := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	succeeded := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	if rep := representativePod([]corev1.Pod{failed, succeeded}); rep == nil || rep.Status.Phase != corev1.PodSucceeded {
		t.Fatalf("want the succeeded pod, got %+v", rep)
	}
	if representativePod(nil) != nil {
		t.Error("no pods must yield nil representative")
	}
}
