package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

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

	sample := b.statsSampler(context.Background(), "web-abc123")
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
