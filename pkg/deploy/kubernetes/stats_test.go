package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// metricsServer stands in for the aggregated metrics API. A real clientset is
// pointed at it, so the test exercises the actual request path the backend
// builds — the thing most likely to be wrong and least likely to be caught by
// testing the decode in isolation.
func metricsServer(t *testing.T, handler http.HandlerFunc) *Backend {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building clientset: %v", err)
	}
	return &Backend{clientset: cs, namespace: "apps"}
}

func TestPodMetricsRequestsTheAggregatedAPI(t *testing.T) {
	var gotPath string
	b := metricsServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"timestamp": "2026-07-26T10:00:00Z",
			"window":    "15s",
			"containers": []map[string]any{
				{"name": "app", "usage": map[string]string{"cpu": "250m", "memory": "128Mi"}},
			},
		})
	})

	pm, err := b.podMetrics(context.Background(), "web-abc123")
	if err != nil {
		t.Fatalf("podMetrics: %v", err)
	}
	want := "/apis/metrics.k8s.io/v1beta1/namespaces/apps/pods/web-abc123"
	if gotPath != want {
		t.Errorf("requested %q, want %q", gotPath, want)
	}
	if len(pm.Containers) != 1 || pm.Containers[0].Usage.CPU != "250m" {
		t.Errorf("decoded %+v, want one container using 250m", pm.Containers)
	}
}

// TestPodMetricsMissingNamesMetricsServer pins the diagnostic, not just the
// error type. A 404 here has two very different causes with two different
// remedies, and a bare "not found" leaves an operator with no way to tell an
// uninstalled metrics-server from a pod that simply has not been scraped yet.
func TestPodMetricsMissingNamesMetricsServer(t *testing.T) {
	b := metricsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := b.podMetrics(context.Background(), "web-abc123")
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("error = %v, want deploy.ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "metrics-server") {
		t.Errorf("error %q does not name metrics-server", err)
	}
}

func TestToResourceSamplePrefersTheAppContainer(t *testing.T) {
	pm := podMetrics{}
	pm.Containers = append(pm.Containers,
		containerUsage("caretaker", "500m", "512Mi"),
		containerUsage(execContainer, "250m", "128Mi"),
	)
	rs := pm.toResourceSample()

	if rs.CPUCores == nil || *rs.CPUCores < 0.24 || *rs.CPUCores > 0.26 {
		t.Errorf("CPUCores = %v, want ~0.25 (the app container alone, not the pod total)", rs.CPUCores)
	}
	if rs.MemUsage != 128<<20 {
		t.Errorf("MemUsage = %d, want %d (the app container alone)", rs.MemUsage, 128<<20)
	}
	// CPUTime must stay nil: metrics-server has no cumulative source, and
	// inventing one would let a reader rate() a number that never accumulated.
	if rs.CPUTime != nil {
		t.Errorf("CPUTime = %+v, want nil on a rate-only backend", rs.CPUTime)
	}
	// Absent, not zero. A series of zeros would assert this pod moved no bytes;
	// the truth is that cornus cannot see whether it did.
	if rs.Networks != nil {
		t.Errorf("Networks = %v, want nil (metrics.k8s.io reports no network counters)", rs.Networks)
	}
	if rs.DiskRead != nil || rs.DiskWrite != nil {
		t.Errorf("disk counters = %v/%v, want nil", rs.DiskRead, rs.DiskWrite)
	}
}

// A pod without an "app" container was not laid out by this version of cornus;
// summing is a worse answer than the right container but a better one than none.
func TestToResourceSampleFallsBackToTheSum(t *testing.T) {
	pm := podMetrics{}
	pm.Containers = append(pm.Containers,
		containerUsage("one", "250m", "100Mi"),
		containerUsage("two", "250m", "28Mi"),
	)
	rs := pm.toResourceSample()
	if rs.CPUCores == nil || *rs.CPUCores < 0.49 || *rs.CPUCores > 0.51 {
		t.Errorf("CPUCores = %v, want ~0.5 (the sum)", rs.CPUCores)
	}
	if rs.MemUsage != 128<<20 {
		t.Errorf("MemUsage = %d, want %d (the sum)", rs.MemUsage, 128<<20)
	}
}

// TestStatsSamplerIntegratesTheRate is the load-bearing one for `docker stats`
// against kubernetes.
//
// metrics-server reports a RATE; Docker's frame carries a COUNTER that the CLI
// differences against the previous frame. Putting the rate straight into the
// counter field would make the first frame look plausible and every subsequent
// delta meaningless (often negative). Integrating rate x elapsed keeps the
// counter monotonic, so a difference over any two frames is real CPU time.
func TestStatsSamplerIntegratesTheRate(t *testing.T) {
	stamps := []string{"2026-07-26T10:00:00Z", "2026-07-26T10:00:10Z", "2026-07-26T10:00:20Z"}
	var call int
	b := metricsServer(t, func(w http.ResponseWriter, r *http.Request) {
		ts := stamps[min(call, len(stamps)-1)]
		call++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"timestamp": ts,
			"window":    "10s",
			"containers": []map[string]any{
				{"name": "app", "usage": map[string]string{"cpu": "500m", "memory": "64Mi"}},
			},
		})
	})

	sample := b.statsSampler(context.Background(), "web-abc123", 256<<20)
	var prev uint64
	for i := range stamps {
		s, err := sample()
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		if s.CPUTotal <= prev {
			t.Fatalf("sample %d: CPUTotal = %d, not greater than the previous %d; the counter must be monotonic", i, s.CPUTotal, prev)
		}
		// Half a core for ten seconds is five seconds of CPU time per interval.
		if delta := s.CPUTotal - prev; delta < 4_500_000_000 || delta > 5_500_000_000 {
			t.Errorf("sample %d: CPU delta = %dns, want ~5s (0.5 cores x 10s)", i, delta)
		}
		prev = s.CPUTotal
		if s.MemUsage != 64<<20 {
			t.Errorf("sample %d: MemUsage = %d, want %d", i, s.MemUsage, 64<<20)
		}
		// The limit comes from the pod SPEC, not from metrics-server, so it is
		// carried on every frame rather than re-read. Without it the docker CLI
		// shows a memory percentage against a zero limit.
		if s.MemLimit != 256<<20 {
			t.Errorf("sample %d: MemLimit = %d, want %d", i, s.MemLimit, 256<<20)
		}
		// Nil, not empty: hostrun omits the field entirely so the docker CLI
		// prints "--" rather than a fabricated 0B / 0B.
		if s.Networks != nil {
			t.Errorf("sample %d: Networks = %v, want nil", i, s.Networks)
		}
	}
}

func TestWindowSeconds(t *testing.T) {
	for in, want := range map[string]float64{"": 30, "15s": 15, "1m": 60, "garbage": 30, "-5s": 30} {
		if got := windowSeconds(in); got != want {
			t.Errorf("windowSeconds(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseQuantities(t *testing.T) {
	for in, want := range map[string]uint64{"128Mi": 128 << 20, "1Gi": 1 << 30, "0": 0, "bogus": 0} {
		if got := parseBytes(in); got != want {
			t.Errorf("parseBytes(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]float64{"2": 2, "500m": 0.5, "bogus": 0} {
		got := parseCPUCores(in)
		if got < want-0.01 || got > want+0.01 {
			t.Errorf("parseCPUCores(%q) = %v, want %v", in, got, want)
		}
	}
}

// containerUsage builds one entry of the anonymous container struct podMetrics
// declares inline.
func containerUsage(name, cpu, mem string) struct {
	Name  string `json:"name"`
	Usage struct {
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
	} `json:"usage"`
} {
	var c struct {
		Name  string `json:"name"`
		Usage struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"usage"`
	}
	c.Name = name
	c.Usage.CPU = cpu
	c.Usage.Memory = mem
	return c
}

// --- Memory limit, which does not come from metrics-server ---------------

// podWithLimits builds a pod carrying the given per-container memory limits. A
// container whose limit is "" declares none, which is a different state from
// declaring "0" and is the one an unlimited workload is actually in.
func podWithLimits(name string, limits map[string]string) corev1.Pod {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	names := make([]string, 0, len(limits))
	for c := range limits {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		ctr := corev1.Container{Name: c}
		if limits[c] != "" {
			ctr.Resources.Limits = corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(limits[c]),
			}
		}
		pod.Spec.Containers = append(pod.Spec.Containers, ctr)
	}
	return pod
}

// TestPodMemLimitPrefersTheAppContainer pairs with
// TestToResourceSamplePrefersTheAppContainer: usage and limit must be read off
// the SAME container, or the headroom a reader computes from the two is wrong in
// the reassuring direction — app usage against app+sidecar limit always looks
// like there is room to spare.
func TestPodMemLimitPrefersTheAppContainer(t *testing.T) {
	pod := podWithLimits("web-abc123", map[string]string{
		"caretaker":   "64Mi",
		execContainer: "128Mi",
	})
	if got := podMemLimit(&pod); got != 128<<20 {
		t.Errorf("podMemLimit = %d, want %d (the app container alone, not the pod total)", got, 128<<20)
	}
}

func TestPodMemLimitFallsBackToTheSum(t *testing.T) {
	pod := podWithLimits("web-abc123", map[string]string{"one": "100Mi", "two": "28Mi"})
	if got := podMemLimit(&pod); got != 128<<20 {
		t.Errorf("podMemLimit = %d, want %d (the sum, matching the usage fallback)", got, 128<<20)
	}
}

// An unlimited container is not a limit of zero. The recorder skips a zero
// MemLimit entirely, which is right: "no ceiling" and "a ceiling of nothing" are
// opposite claims, and recording the second would draw a workload permanently at
// 100% of its limit.
func TestPodMemLimitIsZeroWhenUnlimited(t *testing.T) {
	pod := podWithLimits("web-abc123", map[string]string{execContainer: ""})
	if got := podMemLimit(&pod); got != 0 {
		t.Errorf("podMemLimit = %d, want 0 for a container with no limit", got)
	}
	// And in the summing path, one unlimited container makes the whole pod
	// unlimited — summing the rest would report a ceiling the pod can exceed.
	mixed := podWithLimits("web-abc123", map[string]string{"one": "100Mi", "two": ""})
	if got := podMemLimit(&mixed); got != 0 {
		t.Errorf("podMemLimit = %d, want 0 when any container is unlimited", got)
	}
}

// sampleServer fakes both halves of what SampleMetrics needs: the pods List that
// resolves a replica to a pod, and the metrics.k8s.io reading for it. Both go
// through a real clientset, so a request the backend spells wrongly fails here
// the same way it would against an API server.
//
// The pod list MUST carry an application/json content type. Without it client-go
// looks for a serializer for text/plain and the call fails with a decoding error
// that says nothing about pods — a failure mode worth naming once here rather
// than rediscovering per test.
func sampleServer(t *testing.T, pod corev1.Pod, cpu, mem string) *Backend {
	t.Helper()
	return metricsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, metricsAPIPath) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"timestamp":  "2026-07-26T10:00:00Z",
				"window":     "15s",
				"containers": []map[string]any{{"name": execContainer, "usage": map[string]string{"cpu": cpu, "memory": mem}}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(corev1.PodList{Items: []corev1.Pod{pod}})
	})
}

// TestSampleMetricsFillsTheMemoryLimit is the end-to-end one, and the regression
// this whole change exists for: metrics.k8s.io carries no limit, so a sampler
// that reads only PodMetrics leaves MemLimit zero and the recorder drops
// `cornus.container.memory.limit` on every kubernetes workload. The limit is not
// a measurement — it is in the pod spec, which the sampler already fetches to
// resolve the replica.
func TestSampleMetricsFillsTheMemoryLimit(t *testing.T) {
	pod := podWithLimits("web-abc123", map[string]string{execContainer: "256Mi"})
	b := sampleServer(t, pod, "250m", "128Mi")

	rs, err := b.SampleMetrics(context.Background(), "web", 0)
	if err != nil {
		t.Fatalf("SampleMetrics: %v", err)
	}
	if rs.MemLimit != 256<<20 {
		t.Fatalf("MemLimit = %d, want %d from the pod spec", rs.MemLimit, 256<<20)
	}
	// The rest of the sample must be unaffected — the limit is added to what
	// metrics-server reports, not substituted for any of it.
	if rs.MemUsage != 128<<20 {
		t.Errorf("MemUsage = %d, want %d", rs.MemUsage, 128<<20)
	}
	if rs.CPUCores == nil || *rs.CPUCores < 0.24 || *rs.CPUCores > 0.26 {
		t.Errorf("CPUCores = %v, want ~0.25", rs.CPUCores)
	}
}

// A workload that declared no limit must still report none. Otherwise the fix
// above would trade a missing series for a fabricated one.
func TestSampleMetricsReportsNoLimitWhenThePodHasNone(t *testing.T) {
	pod := podWithLimits("web-abc123", map[string]string{execContainer: ""})
	b := sampleServer(t, pod, "250m", "128Mi")

	rs, err := b.SampleMetrics(context.Background(), "web", 0)
	if err != nil {
		t.Fatalf("SampleMetrics: %v", err)
	}
	if rs.MemLimit != 0 {
		t.Errorf("MemLimit = %d, want 0 for a workload that declared no limit", rs.MemLimit)
	}
}

// TestUnsupportedMetricsMatchesWhatTheSamplerFills pins the declaration against
// what the sampler actually produces, which is the only way this stays true: a
// list maintained by hand drifts the moment a field starts being filled, and it
// drifted exactly once already — MemLimit was unfillable, then was not.
//
// The pod here has everything available to report (a memory limit, real usage),
// so a field left unfilled is unfilled because the SOURCE has nothing to say
// about it, not because this particular workload is quiet.
func TestUnsupportedMetricsMatchesWhatTheSamplerFills(t *testing.T) {
	pod := podWithLimits("web-abc123", map[string]string{execContainer: "256Mi"})
	b := sampleServer(t, pod, "250m", "128Mi")
	rs, err := b.SampleMetrics(context.Background(), "web", 0)
	if err != nil {
		t.Fatalf("SampleMetrics: %v", err)
	}

	declared := map[api.SampleField]bool{}
	for _, f := range b.UnsupportedMetrics() {
		declared[f] = true
	}
	filled := map[api.SampleField]bool{
		api.SampleFieldCPUTime:  rs.CPUTime != nil,
		api.SampleFieldCPUCores: rs.CPUCores != nil,
		api.SampleFieldMemUsage: rs.MemUsage > 0,
		api.SampleFieldMemLimit: rs.MemLimit > 0,
		api.SampleFieldPids:     rs.Pids > 0,
		api.SampleFieldNetwork:  rs.Networks != nil,
		api.SampleFieldDisk:     rs.DiskRead != nil || rs.DiskWrite != nil,
	}
	for field, got := range filled {
		if got && declared[field] {
			t.Errorf("%s is declared unsupported but the sampler filled it", field)
		}
		if !got && !declared[field] {
			t.Errorf("%s was not filled but is not declared unsupported, so the dashboard will show a chart that can never fill", field)
		}
	}
}
