package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// Container metrics for the kubernetes backend, via metrics-server.
//
// Every other backend reads its numbers from a source it owns — a cgroup, a
// containerd task, an Incus instance state. Kubernetes has none of those: the
// cornus server is not on the node the pod runs on, and the only portable path
// to a pod's resource usage through the API server is the metrics.k8s.io
// aggregated API that metrics-server serves. So this backend asks the cluster,
// and inherits two limitations from that choice which are stated rather than
// worked around:
//
//   - **No network or disk counters, and no pid count.** metrics.k8s.io reports
//     CPU and memory and nothing else. The fuller set lives behind the kubelet's
//     Summary API (/api/v1/nodes/{node}/proxy/stats/summary), which needs
//     nodes/proxy — a grant that lets the holder reach every kubelet in the
//     cluster, and one a sane operator denies. Trading that blast radius for
//     three more metric families is not a good deal, so the families are simply
//     absent, and absent means no series at all rather than a series reading zero.
//   - **CPU arrives as a rate, not a counter.** metrics-server reports usage over
//     a sampling window, not cumulative time. SampleMetrics reports that honestly
//     as api.ResourceSample.CPUCores; only Stats, which must emit Docker's
//     counter-shaped frame, integrates it (see statsSampler).
//
// The memory LIMIT is not one of those limitations, and used to be treated as
// one. metrics.k8s.io does not carry it, but the limit is not a measurement — it
// is a number cornus itself put in the pod spec (see resourceLimits), readable
// off the pod object the sampler already fetches to resolve the replica, under
// RBAC the backend already holds. Leaving it unfilled cost the whole
// `cornus_container_memory_limit` series on kubernetes and left `docker stats`
// showing a memory percentage against a zero limit.
//
// When metrics-server is not installed the API group does not exist and every
// call 404s. That is reported as an error naming metrics-server, because "no
// metrics" with no explanation is the kind of failure an operator cannot act on.

var (
	_ deploy.MetricsSampler      = (*Backend)(nil)
	_ deploy.MetricsCapabilities = (*Backend)(nil)
)

// UnsupportedMetrics implements deploy.MetricsCapabilities: the families
// metrics.k8s.io structurally cannot answer, so a consumer stops offering charts
// for them rather than showing four permanently empty boxes. See the file comment
// for why each one is missing — and note that memory usage, memory limit, and the
// instantaneous CPU figure are all absent from this list because they ARE
// reported.
func (b *Backend) UnsupportedMetrics() []api.SampleField {
	return []api.SampleField{
		api.SampleFieldCPUTime,
		api.SampleFieldPids,
		api.SampleFieldNetwork,
		api.SampleFieldDisk,
	}
}

// metricsAPIPath is the aggregated metrics API's pod resource for one namespace.
const metricsAPIPath = "/apis/metrics.k8s.io/v1beta1/namespaces/"

// podMetrics is the subset of metrics.k8s.io/v1beta1 PodMetrics cornus reads.
// Hand-rolled rather than importing k8s.io/metrics: the module exists only to
// carry these three structs, and the deploy tree already hand-rolls Docker's
// stats types for the same reason.
type podMetrics struct {
	Timestamp  time.Time `json:"timestamp"`
	Window     string    `json:"window"`
	Containers []struct {
		Name  string `json:"name"`
		Usage struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"usage"`
	} `json:"containers"`
}

// SampleMetrics implements deploy.MetricsSampler for the kubernetes backend.
func (b *Backend) SampleMetrics(ctx context.Context, name string, instance int) (api.ResourceSample, error) {
	pod, err := b.podObjectAt(ctx, name, instance)
	if err != nil {
		return api.ResourceSample{}, err
	}
	pm, err := b.podMetrics(ctx, pod.Name)
	if err != nil {
		return api.ResourceSample{}, err
	}
	out := pm.toResourceSample()
	// From the SPEC, not from metrics-server, which does not report it. Zero when
	// the workload declared no limit, which is the same thing every other backend
	// reports for an unlimited container.
	out.MemLimit = podMemLimit(pod)
	return out, nil
}

// podMemLimit reads the enforced memory limit off a pod spec, in bytes.
//
// Mirrors toResourceSample's container selection exactly, and for the same
// reason: the app container's limit is what constrains the workload, and pairing
// the app container's USAGE with a limit summed over the caretaker sidecar too
// would produce a headroom figure that is wrong in the safe-looking direction. The
// no-"app"-container fallback sums, so the pair stays consistent in that case as
// well.
//
// Zero for a container with no limit — genuinely unlimited, which the recorder
// then records as no series rather than as a limit of zero bytes.
func podMemLimit(pod *corev1.Pod) uint64 {
	for _, c := range pod.Spec.Containers {
		if c.Name == execContainer {
			return quantityBytes(c.Resources.Limits.Memory())
		}
	}
	var total uint64
	for _, c := range pod.Spec.Containers {
		n := quantityBytes(c.Resources.Limits.Memory())
		if n == 0 {
			// One unlimited container makes the pod unlimited: summing the rest
			// would report a ceiling the workload can exceed.
			return 0
		}
		total += n
	}
	return total
}

// quantityBytes reads a resource.Quantity as bytes, treating absent and negative
// as zero.
func quantityBytes(q *resource.Quantity) uint64 {
	if q == nil {
		return 0
	}
	n := q.Value()
	if n <= 0 {
		return 0
	}
	return uint64(n)
}

// podMetrics fetches one pod's metrics through the aggregated API.
func (b *Backend) podMetrics(ctx context.Context, pod string) (podMetrics, error) {
	raw, err := b.clientset.Discovery().RESTClient().
		Get().
		AbsPath(metricsAPIPath + b.namespace + "/pods/" + pod).
		DoRaw(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Two different absences produce the same status, and the operator's
			// next action differs completely, so say both.
			return podMetrics{}, fmt.Errorf(
				"kubernetes: no metrics for pod %q: either metrics-server is not installed in this cluster, or the pod has no sample yet (metrics-server needs a scrape interval after a pod starts): %w",
				pod, deploy.ErrNotFound)
		}
		return podMetrics{}, fmt.Errorf("kubernetes: reading pod metrics for %q: %w", pod, err)
	}
	var pm podMetrics
	if err := json.Unmarshal(raw, &pm); err != nil {
		return podMetrics{}, fmt.Errorf("kubernetes: decoding pod metrics for %q: %w", pod, err)
	}
	return pm, nil
}

// toResourceSample projects PodMetrics onto the neutral shape.
//
// The app container is reported alone rather than summed with the pod's other
// containers: a cornus pod also runs a caretaker sidecar, and folding its usage
// into the workload's would make the recorded numbers disagree with what the
// workload itself is doing. A pod with no container named "app" (deployed by
// something other than cornus, or an older layout) falls back to the sum, which
// is a worse answer than the right container but a better one than nothing.
func (pm podMetrics) toResourceSample() api.ResourceSample {
	when := pm.Timestamp
	if when.IsZero() {
		when = time.Now().UTC()
	}
	out := api.ResourceSample{Time: when}

	var cores float64
	var mem uint64
	var found bool
	for _, c := range pm.Containers {
		if c.Name != execContainer {
			continue
		}
		cores, mem = parseCPUCores(c.Usage.CPU), parseBytes(c.Usage.Memory)
		found = true
		break
	}
	if !found {
		for _, c := range pm.Containers {
			cores += parseCPUCores(c.Usage.CPU)
			mem += parseBytes(c.Usage.Memory)
		}
	}
	out.CPUCores = &cores
	out.MemUsage = mem
	// Networks stays nil, DiskRead/DiskWrite stay nil: see the file comment. This
	// is the "cannot observe" case, which must produce no series rather than a
	// series of zeros. MemLimit stays zero here because PodMetrics does not carry
	// it — the caller fills it from the pod spec, which does.
	return out
}

// parseCPUCores reads a Kubernetes CPU quantity ("250m", "1234n", "2") as cores.
func parseCPUCores(v string) float64 {
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}

// parseBytes reads a Kubernetes memory quantity ("128Mi", "12345Ki") as bytes.
func parseBytes(v string) uint64 {
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return 0
	}
	n := q.Value()
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// Stats streams Docker-format stats JSON for the deployment's opts.Instance-th
// pod, so `docker stats` and the web UI's metrics pane work against a
// kubernetes-backed cornus the same as against any other backend.
//
// Memory and pids pass straight through. CPU is integrated rather than passed
// through, because Docker's frame carries a cumulative counter that the CLI
// differences against the previous frame, while metrics-server reports a rate
// over a window. statsSampler turns the rate back into a counter by accumulating
// rate x elapsed, which reproduces the correct percentage in both the one-shot
// and the streaming case — and, unlike stuffing the rate into the counter field,
// stays correct when the CLI takes a difference.
func (b *Backend) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	pod, err := b.podObjectAt(ctx, name, opts.Instance)
	if err != nil {
		return err
	}
	return hostrun.StreamStats(ctx, w, pod.Name, name, opts.Stream, b.statsSampler(ctx, pod.Name, podMemLimit(pod)))
}

// statsSampler returns a closure that turns successive metrics-server readings
// into the monotonic counters Docker's frame expects.
//
// The accumulators are closure state, which is exactly right for their lifetime:
// they are meaningful only within one Stats call, and a value carried on the
// Backend would be shared between concurrent viewers of different pods.
//
// memLimit is read once from the pod spec and held for the stream's lifetime,
// which is as current as the number can be: a pod's resource limits are fixed for
// its lifetime, and a rollout that changes them replaces the pod — ending this
// stream with it.
func (b *Backend) statsSampler(ctx context.Context, pod string, memLimit uint64) func() (hostrun.StatsSample, error) {
	// onlineCPUs must match what ToDockerStats stamps on the frame, or the
	// percentage the CLI computes from these two counters comes out scaled by
	// the ratio between them.
	onlineCPUs := float64(runtime.NumCPU())
	var accumCPU, accumSys float64
	var last time.Time
	return func() (hostrun.StatsSample, error) {
		pm, err := b.podMetrics(ctx, pod)
		if err != nil {
			return hostrun.StatsSample{}, err
		}
		rs := pm.toResourceSample()

		// On the first reading there is no previous frame to measure against, so
		// the server's own sampling window is the honest elapsed time.
		elapsed := rs.Time.Sub(last).Seconds()
		if last.IsZero() || elapsed <= 0 {
			elapsed = windowSeconds(pm.Window)
		}
		last = rs.Time

		var cores float64
		if rs.CPUCores != nil {
			cores = *rs.CPUCores
		}
		accumCPU += cores * elapsed * 1e9
		accumSys += onlineCPUs * elapsed * 1e9

		return hostrun.StatsSample{
			Read:     rs.Time,
			CPUTotal: uint64(accumCPU),
			SysUsage: uint64(accumSys),
			MemUsage: rs.MemUsage,
			MemLimit: memLimit,
			// Networks is left nil rather than empty: hostrun omits the field
			// entirely, so the docker CLI shows "--" instead of a false 0B / 0B.
		}, nil
	}
}

// windowSeconds parses metrics-server's reported sampling window, defaulting to
// 30s (its own default) when the field is absent or unparseable.
func windowSeconds(v string) float64 {
	if v == "" {
		return 30
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 30
	}
	return d.Seconds()
}
