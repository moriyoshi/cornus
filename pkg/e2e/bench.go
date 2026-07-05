package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.starlark.net/starlark"
)

// Benchmark builtins for the opt-in E2E benchmark suite (e2e/benchmarks/*.star,
// run via `make e2e-bench`). They add wall-clock timing and result reporting on
// top of the normal scenario builtins: a benchmark scenario drives real work
// (deploy_attach + exec_tty, build, ...) and records throughput / latency numbers.
//
// Output is a human-readable "▸ BENCH ..." line per record. Additionally, if
// CORNUS_E2E_BENCH_JSON names a file, every record is appended to it as one JSON
// object per line (JSONL), so a run's numbers can be collected across scenarios
// (each scenario gets a fresh Harness, but they all append to the same file) and
// processed by a machine.

// benchEpoch is a process-wide monotonic origin for now(); a scenario diffs two
// now() readings to time a section of work.
var benchEpoch = time.Now()

// bNow returns monotonic seconds since the process started, as a float. Diff two
// readings to time a block of work: t0 = now(); ...work...; dt = now() - t0.
func (h *Harness) bNow(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("now", args, kwargs); err != nil {
		return nil, err
	}
	return starlark.Float(time.Since(benchEpoch).Seconds()), nil
}

// bBenchmark times a Starlark callable over `iters` runs (after `warmup` untimed
// runs), prints a summary, and returns {name, iters, warmup, avg_s, min_s, max_s,
// total_s}. Use it for repeatable Starlark-driven work; for one-shot throughput,
// prefer now() + bench_record so process/exec overhead is not folded into the
// number (e.g. an exec_tty round trip dwarfs the mount op it drives).
func (h *Harness) bBenchmark(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	var fn starlark.Value
	iters, warmup := 1, 0
	if err := starlark.UnpackArgs("benchmark", args, kwargs, "name", &name, "fn", &fn, "iters?", &iters, "warmup?", &warmup); err != nil {
		return nil, err
	}
	callable, ok := fn.(starlark.Callable)
	if !ok {
		return nil, fmt.Errorf("benchmark %q: fn must be callable, got %s", name, fn.Type())
	}
	if iters < 1 {
		iters = 1
	}
	call := func(phase string, i int) error {
		if _, err := starlark.Call(thread, callable, nil, nil); err != nil {
			return fmt.Errorf("benchmark %q %s %d: %w", name, phase, i, err)
		}
		return nil
	}
	for i := 0; i < warmup; i++ {
		if err := call("warmup", i); err != nil {
			return nil, err
		}
	}
	var total, minD, maxD time.Duration
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		if err := call("iter", i); err != nil {
			return nil, err
		}
		d := time.Since(t0)
		total += d
		if i == 0 || d < minD {
			minD = d
		}
		if i == 0 || d > maxD {
			maxD = d
		}
	}
	avg := total / time.Duration(iters)
	h.logf("▸ BENCH %s: %d iters  avg=%s  min=%s  max=%s", name, iters,
		avg.Round(time.Microsecond), minD.Round(time.Microsecond), maxD.Round(time.Microsecond))
	rec := map[string]any{
		"name": name, "iters": iters, "warmup": warmup,
		"avg_s": avg.Seconds(), "min_s": minD.Seconds(), "max_s": maxD.Seconds(), "total_s": total.Seconds(),
	}
	h.benchEmit("timing", rec)
	return anyDict(rec), nil
}

// bBenchRecord records a pre-measured metric (e.g. a throughput the scenario
// computed itself from now() timings): value carries the number, unit its label
// (default "s"), and extra is an optional dict of additional fields (e.g.
// {"MB": 128, "MBps": 92.4}). Prints a "▸ BENCH ..." line and, if
// CORNUS_E2E_BENCH_JSON is set, appends a JSONL record. Returns the record dict.
func (h *Harness) bBenchRecord(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name, unit string
	var value float64
	var extra starlark.Value
	unit = "s"
	if err := starlark.UnpackArgs("bench_record", args, kwargs, "name", &name, "value", &value, "unit?", &unit, "extra?", &extra); err != nil {
		return nil, err
	}
	ex, err := anyMap(extra)
	if err != nil {
		return nil, fmt.Errorf("bench_record %q: extra: %w", name, err)
	}
	rec := map[string]any{"name": name, "value": value, "unit": unit}
	for k, v := range ex {
		rec[k] = v
	}
	h.logf("▸ BENCH %s: %s %s%s", name, formatBenchNum(value), unit, formatBenchExtra(ex))
	h.benchEmit("metric", rec)
	return anyDict(rec), nil
}

// benchEmit appends rec as one JSONL line to CORNUS_E2E_BENCH_JSON, if set,
// stamping it with the kind, the target name, and an RFC3339 timestamp.
// Best-effort: any I/O error is ignored so a benchmark never fails on reporting.
func (h *Harness) benchEmit(kind string, rec map[string]any) {
	path := os.Getenv("CORNUS_E2E_BENCH_JSON")
	if path == "" {
		return
	}
	out := map[string]any{"kind": kind, "target": h.target.Name(), "ts": time.Now().UTC().Format(time.RFC3339Nano)}
	for k, v := range rec {
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func formatBenchNum(v float64) string { return fmt.Sprintf("%.4g", v) }

func formatBenchExtra(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return "  (" + strings.Join(parts, " ") + ")"
}
