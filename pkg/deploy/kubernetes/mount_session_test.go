package kubernetes

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/caretaker"
)

// caretakerInitContainer builds a native-sidecar caretaker container carrying a
// mount session, the way the apply paths bake it into a pod template.
func caretakerInitContainer(session string) corev1.Container {
	cfg := caretaker.Config{Mounts: []caretaker.MountRole{{Session: session, Name: "m0"}}}
	raw, _ := json.Marshal(cfg)
	return corev1.Container{
		Name: caretakerContainerName,
		Env:  []corev1.EnvVar{{Name: "CORNUS_CARETAKER_CONFIG", Value: string(raw)}},
	}
}

// TestExistingMountSession proves the read-back that the stable-session-id reuse
// relies on: the deploy-attach session id baked into a running Deployment or Job
// caretaker is recovered, and absence (no workload, or a workload with no caretaker)
// reads as "" rather than an error — so the server falls back to minting a fresh id.
func TestExistingMountSession(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	if id, err := b.ExistingMountSession(ctx, "web"); err != nil || id != "" {
		t.Fatalf("absent workload: want \"\",nil; got %q,%v", id, err)
	}

	// Deployment (long-lived service) carrying a caretaker session.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{caretakerInitContainer("sess-web")},
			Containers:     []corev1.Container{{Name: "app", Image: "img"}},
		}}},
	}
	if _, err := cs.AppsV1().Deployments("default").Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if id, err := b.ExistingMountSession(ctx, "web"); err != nil || id != "sess-web" {
		t.Fatalf("deployment: want sess-web; got %q,%v", id, err)
	}

	// Job (one-shot service) carrying a caretaker session.
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "init", Namespace: "default"},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{caretakerInitContainer("sess-init")},
			Containers:     []corev1.Container{{Name: "app", Image: "img"}},
		}}},
	}
	if _, err := cs.BatchV1().Jobs("default").Create(ctx, job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if id, err := b.ExistingMountSession(ctx, "init"); err != nil || id != "sess-init" {
		t.Fatalf("job: want sess-init; got %q,%v", id, err)
	}

	// A workload with no caretaker sidecar -> "" (nothing to reuse).
	plain := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "img"}},
		}}},
	}
	if _, err := cs.AppsV1().Deployments("default").Create(ctx, plain, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if id, err := b.ExistingMountSession(ctx, "plain"); err != nil || id != "" {
		t.Fatalf("no-caretaker workload: want \"\",nil; got %q,%v", id, err)
	}
}
