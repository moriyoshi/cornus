package kubernetes

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// knativeServiceGVR is the Knative Serving Service resource (a "ksvc").
var knativeServiceGVR = schema.GroupVersionResource{
	Group: "serving.knative.dev", Version: "v1", Resource: "services",
}

// knativeAutoscalingPrefix is the annotation-key prefix Knative reads autoscaler
// knobs from on the revision template.
const knativeAutoscalingPrefix = "autoscaling.knative.dev/"

// knativeEnabled reports whether the spec asks to deploy as a Knative Service.
func knativeEnabled(spec api.DeploySpec) bool {
	return spec.Knative != nil && spec.Knative.Enabled
}

// knativeStrict reports whether a Knative deploy on a cluster WITHOUT Knative
// must hard-error instead of degrading to a plain Deployment
// (CORNUS_KNATIVE_STRICT=true).
func knativeStrict() bool { return os.Getenv("CORNUS_KNATIVE_STRICT") == "true" }

// knativeServed reports (and caches) whether the cluster serves the
// serving.knative.dev/v1 Service resource. Without it — or without a dynamic
// client — a Knative deploy cannot round-trip and falls back to a Deployment.
func (b *Backend) knativeServed() bool {
	b.knativeCapOnce.Do(func() {
		b.knativeCapVal = b.detectKnative()
	})
	return b.knativeCapVal
}

func (b *Backend) detectKnative() bool {
	if b.dyn == nil {
		return false
	}
	list, err := b.clientset.Discovery().ServerResourcesForGroupVersion("serving.knative.dev/v1")
	if err != nil || list == nil {
		return false
	}
	for _, r := range list.APIResources {
		if r.Name == knativeServiceGVR.Resource {
			return true
		}
	}
	return false
}

// knativeGuard rejects a Knative deploy that combines with a feature the v1
// Knative path does not yet support, so the request fails fast with a clear
// message instead of silently dropping intent. A non-Knative spec passes
// through. It is called from the single applyWorkload funnel.
func knativeGuard(spec api.DeploySpec) error {
	if !knativeEnabled(spec) {
		return nil
	}
	if err := spec.Knative.Validate(); err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	if deploy.IsOneShot(spec) {
		return fmt.Errorf("kubernetes: a Knative Service is long-lived; restart policy %q (one-shot) is incompatible with knative", deploy.RestartPolicy(spec))
	}
	// A client-emulated ingress creates no server object (the client runs the proxy),
	// so it is transparent to knative — only a real/native ingress conflicts with
	// knative's own Route/URL routing. Gate on ingressEnabled (which is false for a
	// ClientEmulated ingress), not a bare non-nil check.
	if ingressEnabled(spec.Ingress) {
		return fmt.Errorf("kubernetes: knative provides its own routing (a Route/URL); remove the ingress block")
	}
	switch {
	case len(spec.Mounts) > 0:
		return fmt.Errorf("kubernetes: client-local mounts are not supported with knative yet")
	case len(spec.Networks) > 0:
		return fmt.Errorf("kubernetes: user networks are not supported with knative yet")
	case len(spec.Volumes) > 0:
		return fmt.Errorf("kubernetes: managed volumes are not supported with knative yet")
	case spec.Proxy != nil:
		return fmt.Errorf("kubernetes: the enforcing proxy is not supported with knative yet")
	case spec.DNS != nil:
		return fmt.Errorf("kubernetes: the caretaker DNS role is not supported with knative yet")
	case spec.Hub != nil:
		return fmt.Errorf("kubernetes: the workload hub is not supported with knative yet")
	case spec.Docker != nil:
		return fmt.Errorf("kubernetes: the docker endpoint is not supported with knative yet")
	case spec.AgentForward:
		return fmt.Errorf("kubernetes: agent forwarding is not supported with knative yet")
	case spec.Egress != nil && spec.Egress.NeedsRelay():
		return fmt.Errorf("kubernetes: relay-mode client-side egress is not supported with knative yet")
	}
	return nil
}

// applyKnativeService creates or updates the serving.knative.dev/v1 Service for
// the spec, reusing the fully-built pod template from deployment() (its
// container, env, args, resources, probes) as the revision template. Unlike a
// plain Deployment, a Knative Service owns its own Route and autoscaling, so it
// creates no ClusterIP Service, Ingress, or netdriver objects — the applyDependents
// tail is deliberately skipped.
func (b *Backend) applyKnativeService(ctx context.Context, spec api.DeploySpec, desired *appsv1.Deployment) (api.DeployStatus, error) {
	obj, err := b.knativeService(spec, desired)
	if err != nil {
		return api.DeployStatus{}, err
	}
	ksvcs := b.dyn.Resource(knativeServiceGVR).Namespace(b.namespace)
	if _, err := ksvcs.Get(ctx, spec.Name, metav1.GetOptions{}); err == nil {
		// Re-fetch the current resourceVersion inside the retry loop: the Knative
		// controller writes status concurrently, so a bare Get→Update can 409.
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cur, gerr := ksvcs.Get(ctx, spec.Name, metav1.GetOptions{})
			if gerr != nil {
				return gerr
			}
			obj.SetResourceVersion(cur.GetResourceVersion())
			_, uerr := ksvcs.Update(ctx, obj, metav1.UpdateOptions{})
			return uerr
		}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("update knative service: %w", err)
		}
	} else if apierrors.IsNotFound(err) {
		if _, err := ksvcs.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("create knative service: %w", err)
		}
	} else {
		return api.DeployStatus{}, err
	}
	return b.Status(ctx, spec.Name)
}

// knativeService builds the unstructured ksvc object. The revision template
// carries the cornus.app / cornus.managed labels (Knative propagates template
// labels to the pods) so exec/logs/port-forward/status keep resolving pods by
// cornus.app, exactly as for a Deployment.
func (b *Backend) knativeService(spec api.DeploySpec, desired *appsv1.Deployment) (*unstructured.Unstructured, error) {
	kn := spec.Knative
	appc := desired.Spec.Template.Spec.Containers[0]
	cmap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&appc)
	if err != nil {
		return nil, fmt.Errorf("knative: encode container: %w", err)
	}
	// Knative rejects these on a revision container; drop the ones deployment()
	// may have set (tty/stdin are never meaningful for a serverless request path,
	// and volumeMounts are guarded out).
	delete(cmap, "stdin")
	delete(cmap, "tty")
	delete(cmap, "volumeMounts")

	// A Knative revision exposes exactly one port.
	port, hasPort, err := chooseKnativePort(spec)
	if err != nil {
		return nil, err
	}
	if hasPort {
		cmap["ports"] = []any{map[string]any{"containerPort": int64(port)}}
	} else {
		delete(cmap, "ports")
	}

	podSpec := map[string]any{"containers": []any{cmap}}
	if kn.Concurrency != nil {
		podSpec["containerConcurrency"] = int64(*kn.Concurrency)
	}
	if kn.TimeoutSeconds != nil {
		podSpec["timeoutSeconds"] = int64(*kn.TimeoutSeconds)
	}

	tmplMeta := map[string]any{"labels": toAnyMap(labels(spec.Name))}
	if annots := knativeAnnotations(kn); len(annots) > 0 {
		tmplMeta["annotations"] = toAnyMap(annots)
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.knative.dev/v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      spec.Name,
			"namespace": b.namespace,
			"labels":    toAnyMap(labels(spec.Name)),
		},
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": tmplMeta,
				"spec":     podSpec,
			},
		},
	}}, nil
}

// chooseKnativePort resolves the single container port Knative routes to:
// KnativeSpec.Port when set (and it must match a published port), else the first
// published port. ok is false when the spec publishes no ports (Knative then
// defaults to 8080).
func chooseKnativePort(spec api.DeploySpec) (port int, ok bool, err error) {
	want := spec.Knative.Port
	if len(spec.Ports) == 0 {
		if want != 0 {
			return 0, false, fmt.Errorf("knative: port %d requested but the spec publishes no ports", want)
		}
		return 0, false, nil
	}
	if want == 0 {
		return spec.Ports[0].Container, true, nil
	}
	for _, p := range spec.Ports {
		if p.Container == want {
			return want, true, nil
		}
	}
	return 0, false, fmt.Errorf("knative: port %d does not match any published container port", want)
}

// knativeAnnotations renders the KnativeSpec's autoscaling knobs as revision-
// template annotations. Passthrough Annotations are written first so an explicit
// field always wins over a colliding raw key.
func knativeAnnotations(kn *api.KnativeSpec) map[string]string {
	out := map[string]string{}
	for k, v := range kn.Annotations {
		out[k] = v
	}
	if kn.MinScale != nil {
		out[knativeAutoscalingPrefix+"minScale"] = strconv.Itoa(*kn.MinScale)
	}
	if kn.MaxScale != nil {
		out[knativeAutoscalingPrefix+"maxScale"] = strconv.Itoa(*kn.MaxScale)
	}
	if kn.Target != nil {
		out[knativeAutoscalingPrefix+"target"] = strconv.Itoa(*kn.Target)
	}
	if kn.Class != "" {
		out[knativeAutoscalingPrefix+"class"] = knativeClassAnnotation(kn.Class)
	}
	if kn.Metric != "" {
		out[knativeAutoscalingPrefix+"metric"] = kn.Metric
	}
	return out
}

// knativeClassAnnotation expands the short class word to Knative's fully-
// qualified class annotation value.
func knativeClassAnnotation(class string) string {
	switch class {
	case "kpa", "hpa":
		return class + ".autoscaling.knative.dev"
	default:
		return class
	}
}

// toAnyMap copies a string map into the map[string]any shape unstructured content
// requires for nested objects.
func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// knativeStatus reports a Knative workload's state as a DeployStatus, or ok=false
// when no ksvc of that name exists (so Status can fall through to "not found").
// It is the Knative analogue of jobStatus.
func (b *Backend) knativeStatus(ctx context.Context, name string) (api.DeployStatus, bool, error) {
	if b.dyn == nil || !b.knativeServed() {
		return api.DeployStatus{}, false, nil
	}
	obj, err := b.dyn.Resource(knativeServiceGVR).Namespace(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// Forbidden is treated like NotFound: an SA without serving.knative.dev RBAC
		// cannot own a ksvc, so "not a Knative workload" is the correct answer here
		// rather than a hard status error. See deleteKnative for the same rationale.
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return api.DeployStatus{}, false, nil
		}
		return api.DeployStatus{}, false, err
	}
	return b.statusOfKnative(ctx, obj), true, nil
}

// statusOfKnative renders a ksvc as a DeployStatus: its status.url, and instances
// from the pods its revisions run (selected by cornus.app). A scaled-to-zero
// service has no pods; it then reports a single instance reflecting the ksvc
// Ready condition.
func (b *Backend) statusOfKnative(ctx context.Context, obj *unstructured.Unstructured) api.DeployStatus {
	name := obj.GetName()
	st := api.DeployStatus{Name: name, Backend: b.Name(), Image: knativeTemplateImage(obj)}
	if url, ok, _ := unstructured.NestedString(obj.Object, "status", "url"); ok {
		st.URL = url
	}
	var pods []corev1.Pod
	if pl, err := b.clientset.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelApp + "=" + name,
	}); err == nil {
		pods = pl.Items
	}
	if len(pods) > 0 {
		for i := range pods {
			inst := api.InstanceStatus{ID: name + "-" + strconv.Itoa(i)}
			// A Knative revision has its own readiness probe; treat health as
			// unreported here (no Docker healthcheck vocabulary).
			fillInstanceFromPod(&inst, &pods[i], false)
			st.Instances = append(st.Instances, inst)
		}
		return st
	}
	ready, msg := knativeReady(obj)
	inst := api.InstanceStatus{ID: name + "-0", State: "pending"}
	if ready {
		// The Route is Ready but no replica is running: scaled to zero, will scale
		// up on the next request.
		inst.State = "scaled-to-zero"
	} else {
		inst.Message = msg
	}
	st.Instances = append(st.Instances, inst)
	return st
}

// knativeTemplateImage reads the revision template's container image.
func knativeTemplateImage(obj *unstructured.Unstructured) string {
	containers, found, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return ""
	}
	if c, ok := containers[0].(map[string]any); ok {
		if img, ok := c["image"].(string); ok {
			return img
		}
	}
	return ""
}

// knativeReady reads the ksvc's top-level Ready condition (status + message).
func knativeReady(obj *unstructured.Unstructured) (ready bool, message string) {
	conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false, ""
	}
	for _, ci := range conds {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		if c["type"] == "Ready" {
			status, _ := c["status"].(string)
			msg, _ := c["message"].(string)
			return status == "True", msg
		}
	}
	return false, ""
}

// knativeExists reports whether a ksvc of the given name exists, so the
// lifecycle verbs (Stop/Start/Restart) can dispatch a Knative workload
// differently from a Deployment.
func (b *Backend) knativeExists(ctx context.Context, name string) (bool, error) {
	if b.dyn == nil {
		return false, nil
	}
	_, err := b.dyn.Resource(knativeServiceGVR).Namespace(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// Forbidden is treated like NotFound: without serving.knative.dev RBAC this
		// SA cannot own a ksvc, so the lifecycle verbs correctly see "no Knative
		// workload" and dispatch the Deployment path. See deleteKnative for details.
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// restartKnative rolls a Knative workload by stamping the revision template with
// a fresh annotation: any change to spec.template makes Knative cut a new
// Revision and shift traffic to it — the serverless analogue of a Deployment
// rollout.
func (b *Backend) restartKnative(ctx context.Context, name string) error {
	ksvcs := b.dyn.Resource(knativeServiceGVR).Namespace(b.namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj, err := ksvcs.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annots, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		if err != nil {
			return err
		}
		if annots == nil {
			annots = map[string]string{}
		}
		annots[restartAnnotation] = metav1.Now().Format("20060102150405")
		if err := unstructured.SetNestedStringMap(obj.Object, annots, "spec", "template", "metadata", "annotations"); err != nil {
			return err
		}
		_, err = ksvcs.Update(ctx, obj, metav1.UpdateOptions{})
		return err
	})
}

// deleteKnative removes a ksvc by name (best-effort, delete-if-exists). Knative's
// own GC then cascades its Configuration, Revisions, and Route. Called from
// Delete alongside the Deployment/Job deletes.
func (b *Backend) deleteKnative(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	if b.dyn == nil || !b.knativeServed() {
		return nil
	}
	// Tolerate Forbidden as well as NotFound: the cluster may serve Knative
	// (knativeServed) while this ServiceAccount holds no serving.knative.dev RBAC.
	// It could then never have created a ksvc — this speculative delete-if-exists
	// has nothing of ours to remove — so a 403 here is "not mine", not a failure.
	// Without this, `down` on a plain-Deployment workload fails in any Knative
	// cluster where cornus was not granted Knative permissions.
	if err := b.dyn.Resource(knativeServiceGVR).Namespace(b.namespace).Delete(ctx, name, opts); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return err
	}
	return nil
}

// warnKnativeDegraded logs the fall-through-to-Deployment path once per apply.
func warnKnativeDegraded(ctx context.Context, name string) {
	logging.FromContext(ctx, slog.Group("kubernetes", "deployment", name)).WarnContext(ctx,
		"cluster does not serve serving.knative.dev; deploying as a Deployment (autoscaling and scale-to-zero not realized). Set CORNUS_KNATIVE_STRICT=true to fail instead")
}
