package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBenchBuiltins exercises now()/benchmark()/bench_record() end to end through
// the Starlark interpreter (no external runtime), and asserts the JSONL sink
// (CORNUS_E2E_BENCH_JSON) receives one record per report.
func TestBenchBuiltins(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "bench.jsonl")
	t.Setenv("CORNUS_E2E_BENCH_JSON", jsonPath)

	scenario := filepath.Join(dir, "bench.star")
	script := `
def noop():
    x = 1

t0 = now()
m = bench_record(name = "throughput", value = 92.5, unit = "MB/s", extra = {"MB": 64})
assert_eq(m["unit"], "MB/s")
assert_eq(m["MB"], 64)
res = benchmark(name = "noop", fn = noop, iters = 3, warmup = 1)
assert_eq(res["iters"], 3)
assert_true(res["avg_s"] >= 0.0, "avg_s must be non-negative")
assert_true(now() >= t0, "now() must be monotonic")
`
	if err := os.WriteFile(scenario, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(nopTarget{}, "", "mem://", io.Discard)
	if err := h.RunFile(context.Background(), scenario); err != nil {
		t.Fatalf("bench scenario failed: %v", err)
	}

	// Two records should have been appended as JSONL: the metric and the timing.
	f, err := os.Open(jsonPath)
	if err != nil {
		t.Fatalf("bench JSONL not written: %v", err)
	}
	defer f.Close()
	var kinds []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		if rec["target"] != "test" {
			t.Errorf("record missing target=test: %v", rec)
		}
		if k, _ := rec["kind"].(string); k != "" {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) != 2 {
		t.Fatalf("expected 2 JSONL records (metric+timing), got %d (%v)", len(kinds), kinds)
	}
}

// TestBenchmarkScenariosParse parse+resolve-checks the opt-in benchmark scenarios
// (they live in e2e/benchmarks/, outside the e2e/scenarios/ glob TestScenariosParse
// covers), so a benchmark using an undefined builtin fails in `go test`, not only
// in `make e2e-bench`.
func TestBenchmarkScenariosParse(t *testing.T) {
	matches, err := filepath.Glob("../../e2e/benchmarks/*.star")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no benchmark scenarios found under e2e/benchmarks/")
	}
	for _, m := range matches {
		if err := Check(m); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}
