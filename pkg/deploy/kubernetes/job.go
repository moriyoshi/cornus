package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// applyWorkload writes the built pod template to the cluster as the workload kind
// the spec's restart policy calls for: a run-to-completion (one-shot / init)
// workload — restart "no" or "on-failure" — becomes a Kubernetes Job so it runs
// once and stops (its native-sidecar caretaker terminates when the main container
// completes), while a long-lived service becomes a Deployment. It is the single
// funnel every apply path (plain, mounts, credentials, egress) routes through, so
// the network mutation and the Deployment-vs-Job choice live in one place.
//
// Deploying a one-shot as a Deployment was the cause of a real caretaker mount
// crashloop: a Deployment always restarts its pods, so after the init finished
// and its deploy-attach session (which backs the client-local mounts) ended,
// Kubernetes kept restarting the pod, whose caretaker then reset every mount
// against the now-gone session. A Job does not restart a completed pod.
func (b *Backend) applyWorkload(ctx context.Context, spec api.DeploySpec, desired *appsv1.Deployment) (api.DeployStatus, error) {
	// Reject a Knative deploy that combines with a not-yet-supported feature
	// before any object is written (this is the single funnel every apply path
	// routes through, so the guard lives here once).
	if err := knativeGuard(spec); err != nil {
		return api.DeployStatus{}, err
	}
	// Attach the spec's user networks to the pod template (membership labels,
	// Multus annotations) before the workload is written — the Job reuses this
	// same template, so mutate it once here for both kinds.
	if err := b.net.MutateTemplate(spec, labels(spec.Name), &desired.Spec.Template); err != nil {
		return api.DeployStatus{}, err
	}
	// A Knative Service is a third workload kind alongside Deployment and Job. On
	// a cluster that serves serving.knative.dev, round-trip to a native ksvc
	// (autoscaling, scale-to-zero); otherwise degrade to a Deployment with a
	// warning (or hard-error under CORNUS_KNATIVE_STRICT).
	if knativeEnabled(spec) {
		if b.knativeServed() {
			return b.applyKnativeService(ctx, spec, desired)
		}
		if knativeStrict() {
			return api.DeployStatus{}, fmt.Errorf("kubernetes: cluster does not serve serving.knative.dev/v1 and CORNUS_KNATIVE_STRICT is set")
		}
		warnKnativeDegraded(ctx, spec.Name)
	}
	if deploy.IsOneShot(spec) {
		return b.applyJob(ctx, spec, jobFromDeployment(spec, desired))
	}
	return b.applyDeployment(ctx, spec, desired)
}

// podRestartPolicy maps a one-shot spec's restart policy to the Job pod's
// restartPolicy: "on-failure" retries the pod on failure (bounded by the Job's
// backoffLimit), "no" never restarts it. Only called for one-shot specs.
func podRestartPolicy(spec api.DeploySpec) corev1.RestartPolicy {
	if strings.HasPrefix(deploy.RestartPolicy(spec), "on-failure") {
		return corev1.RestartPolicyOnFailure
	}
	return corev1.RestartPolicyNever
}

// jobBackoffLimit is how many times the Job retries a failed pod: 0 for "no" (a
// single attempt, matching restart:no), and RestartMaxAttempts for "on-failure"
// (falling back to Kubernetes' default of 6 when unset). Only called for one-shots.
func jobBackoffLimit(spec api.DeploySpec) int32 {
	if !strings.HasPrefix(deploy.RestartPolicy(spec), "on-failure") {
		// restart:"no" — the app should not be RESTARTED after it exits, so this is
		// not the on-failure retry budget. But a bare backoffLimit=0 gave the pod a
		// single attempt, making a TRANSIENT infrastructure failure (a scheduling
		// race, a mount that has not attached yet, a PVC still being provisioned)
		// permanently fatal with no retry — the brittleness that turned every startup
		// glitch terminal. A small budget lets those self-heal while still failing a
		// genuinely-broken one-shot quickly. Migrations/init tasks are near-universally
		// idempotent, so the extra attempts are safe; a workload that must run at most
		// once should carry its own guard.
		return oneShotTransientRetries
	}
	if spec.RestartMaxAttempts > 0 {
		return int32(spec.RestartMaxAttempts)
	}
	return 6 // Kubernetes' default backoffLimit
}

// oneShotTransientRetries is the small backoffLimit a restart:"no" one-shot Job
// gets so a transient scheduling/mount/PVC race self-heals instead of being fatal
// on the first attempt.
const oneShotTransientRetries = 3

// jobFromDeployment converts the built Deployment into a Job that runs the SAME
// pod template exactly once. The template's pod restartPolicy is set per the
// spec's one-shot policy (a Deployment's template is implicitly restart-Always,
// which a Job forbids). Completions/Parallelism are 1: a one-shot init runs a
// single pod to completion. Labels mirror the Deployment so exec/logs/status
// selectors (cornus.app=NAME) resolve the Job's pods identically.
func jobFromDeployment(spec api.DeploySpec, dep *appsv1.Deployment) *batchv1.Job {
	tmpl := dep.Spec.Template
	tmpl.Spec.RestartPolicy = podRestartPolicy(spec)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dep.Name,
			Namespace: dep.Namespace,
			Labels:    labels(spec.Name),
		},
		Spec: batchv1.JobSpec{
			Completions:  ptr.To[int32](1),
			Parallelism:  ptr.To[int32](1),
			BackoffLimit: ptr.To(jobBackoffLimit(spec)),
			Template:     tmpl,
		},
	}
}

// applyJob creates or updates the Job, then the shared dependents (Service, PVCs,
// Ingress, network objects) owned by it. A Job's pod template is immutable, so a
// re-apply of an existing one-shot deletes the old Job first (foreground, so its
// pods go too) and recreates it — the run-to-completion analogue of a Deployment
// rollout.
func (b *Backend) applyJob(ctx context.Context, spec api.DeploySpec, desired *batchv1.Job) (api.DeployStatus, error) {
	jobs := b.clientset.BatchV1().Jobs(b.namespace)
	var job *batchv1.Job
	if _, err := jobs.Get(ctx, spec.Name, metav1.GetOptions{}); err == nil {
		// Job spec (template, selector) is immutable: replace rather than update.
		policy := metav1.DeletePropagationForeground
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			derr := jobs.Delete(ctx, spec.Name, metav1.DeleteOptions{PropagationPolicy: &policy})
			if derr != nil && !apierrors.IsNotFound(derr) {
				return derr
			}
			return nil
		}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("replace job: delete existing: %w", err)
		}
		// Wait for the old Job (and its pods) to clear before recreating, so the
		// create does not race a still-terminating object of the same name.
		if err := b.waitJobGone(ctx, spec.Name); err != nil {
			return api.DeployStatus{}, err
		}
		if job, err = jobs.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("recreate job: %w", err)
		}
	} else if apierrors.IsNotFound(err) {
		if job, err = jobs.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("create job: %w", err)
		}
	} else {
		return api.DeployStatus{}, err
	}

	if err := b.applyDependents(ctx, spec, jobOwnerRef(job)); err != nil {
		return api.DeployStatus{}, err
	}
	return b.Status(ctx, spec.Name)
}

// jobDeleteTimeout bounds how long waitJobGone will wait for a foreground Job
// deletion to complete. Foreground propagation keeps the Job object around until
// its pods are gone, so this must comfortably exceed a pod's termination grace
// period (30s by default, and higher when a service sets stop_grace_period).
const jobDeleteTimeout = 3 * time.Minute

// waitJobGone polls until no Job named name exists, so a replace can safely
// recreate it. A foreground delete does not clear the Job until its pods have
// terminated (which takes the pod's grace period), so this genuinely polls —
// bounded by ctx and capped at jobDeleteTimeout — rather than giving up after a
// few sub-second retries. A missing Job returns immediately.
func (b *Backend) waitJobGone(ctx context.Context, name string) error {
	jobs := b.clientset.BatchV1().Jobs(b.namespace)
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, jobDeleteTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := jobs.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("job %q still terminating: %w", name, err)
	}
	return nil
}

// jobOwnerRef is the owner reference dependents (Service, PVCs, Ingress) carry so
// Kubernetes garbage-collects them when the Job is deleted.
func jobOwnerRef(job *batchv1.Job) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         "batch/v1",
		Kind:               "Job",
		Name:               job.Name,
		UID:                job.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// jobStatus reports a one-shot workload's state as instances derived from its
// pod (a Job has no Deployment-style ready-replica counter). It returns ok=false
// when no Job of that name exists, so Status can fall through to "not found".
func (b *Backend) jobStatus(ctx context.Context, name string) (api.DeployStatus, bool, error) {
	job, err := b.clientset.BatchV1().Jobs(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return api.DeployStatus{}, false, nil
		}
		return api.DeployStatus{}, false, err
	}
	var pods []corev1.Pod
	if pl, err := b.clientset.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelApp + "=" + name,
	}); err == nil {
		pods = pl.Items
	}
	return statusOfJob(job, pods, b.Name()), true, nil
}

// statusOfJob renders a one-shot Job as a single instance reflecting its
// representative pod (the pod that best describes the run: a Succeeded one over a
// Running one over the newest). A pod that has terminated cleanly reports exit 0
// and not-running — which the deploy-attach readiness wait treats as ready for a
// one-shot (see allReady). A failed pod carries its exit code and a diagnostic.
func statusOfJob(job *batchv1.Job, pods []corev1.Pod, backend string) api.DeployStatus {
	st := api.DeployStatus{
		Name:    job.Name,
		Image:   templateImage(&job.Spec.Template),
		Backend: backend,
	}
	inst := api.InstanceStatus{ID: job.Name + "-0", State: "pending"}
	if rep := representativePod(pods); rep != nil {
		fillInstanceFromPod(&inst, rep, templateHasProbe(&job.Spec.Template))
	}
	st.Instances = append(st.Instances, inst)
	return st
}

// representativePod picks the pod that best describes a single-completion Job:
// a Succeeded pod (the run finished) beats a Running one, which beats the newest
// (so a retried Job reflects its latest attempt, not a stale failed one). nil when
// the Job has produced no pods yet.
func representativePod(pods []corev1.Pod) *corev1.Pod {
	var running, newest *corev1.Pod
	for i := range pods {
		p := &pods[i]
		switch p.Status.Phase {
		case corev1.PodSucceeded:
			return p
		case corev1.PodRunning:
			running = p
		}
		if newest == nil || p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	if running != nil {
		return running
	}
	return newest
}

// fillInstanceFromPod maps a pod's phase to the InstanceStatus fields, mirroring
// statusOf's vocabulary (running/succeeded/failed/pending) but read straight from
// the pod because a Job has no ready-replica counter. hasProbe gives container
// readiness a Docker-style health meaning (see healthFromReady).
func fillInstanceFromPod(inst *api.InstanceStatus, pod *corev1.Pod, hasProbe bool) {
	cs := appContainerStatus(pod)
	switch pod.Status.Phase {
	case corev1.PodRunning:
		ready := cs != nil && cs.Ready
		inst.Running = true
		inst.State = "running"
		inst.Health = healthFromReady(hasProbe, ready)
	case corev1.PodSucceeded:
		inst.State = "succeeded"
		ec := 0
		if cs != nil && cs.State.Terminated != nil {
			ec = int(cs.State.Terminated.ExitCode)
		}
		inst.ExitCode = &ec
	case corev1.PodFailed:
		inst.State = "failed"
		if cs != nil && cs.State.Terminated != nil {
			ec := int(cs.State.Terminated.ExitCode)
			inst.ExitCode = &ec
		}
		inst.Message = instanceDiagnostic(pod)
	default: // Pending / Unknown
		inst.State = "pending"
		inst.Message = instanceDiagnostic(pod)
	}
}

// templateImage / templateHasProbe are the pod-template analogues of imageOf /
// appHasProbe, used by the Job status path (which has a template, not a
// Deployment).
func templateImage(tmpl *corev1.PodTemplateSpec) string {
	if cs := tmpl.Spec.Containers; len(cs) > 0 {
		return cs[0].Image
	}
	return ""
}

func templateHasProbe(tmpl *corev1.PodTemplateSpec) bool {
	cs := tmpl.Spec.Containers
	for i := range cs {
		if cs[i].Name == execContainer {
			return cs[i].ReadinessProbe != nil || cs[i].LivenessProbe != nil
		}
	}
	if len(cs) > 0 {
		return cs[0].ReadinessProbe != nil || cs[0].LivenessProbe != nil
	}
	return false
}
