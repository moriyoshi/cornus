# Distributed tracing: client -> server context propagation, end to end through
# the REAL cornus binary. Proves the client-side half of the client <> server <>
# caretaker trace that pkg/client and cmd/cornus now emit: when telemetry is
# enabled the CLI injects a valid, sampled W3C `traceparent` into its server
# requests, and when telemetry is off it injects nothing (the opt-in, zero-cost
# gate). The unit tests cover pkg/client in isolation; only an E2E can prove the
# real binary's main() actually calls observability.Setup and wires the
# instrumented transport.
#
# trace_sink() is an in-harness HTTP endpoint that records the traceparent header
# of every request and answers 204 itself, so the driving `cornus deploy --delete`
# call needs no deploy backend and the scenario runs on every target (no serve()).
#
# Source of truth: cmd/cornus/main.go (Setup + root span), pkg/client/client.go
# (otelhttp transport + dialHeader), pkg/observability/observability.go.

sink = trace_sink()
server = "http://" + sink

work = temp_dir()
spec = work + "/spec.yaml"
write_file(path = spec, content = "name: trace-probe\nimage: busybox:latest\n")

# --- 1. telemetry ON: a valid, sampled traceparent must reach the server -------
# CORNUS_OTEL enables telemetry; OTEL_*_EXPORTER=none keeps it network-free (a
# no-op exporter), so spans are still sampled and injected but nothing is shipped
# and process exit stays fast. `deploy --delete` issues one DELETE /.cornus/v1/deploy/...
# through the instrumented client; the sink answers 204 so the CLI exits 0.
otel_env = {
    "CORNUS_OTEL": "1",
    "OTEL_TRACES_EXPORTER": "none",
    "OTEL_METRICS_EXPORTER": "none",
    "OTEL_LOGS_EXPORTER": "none",
}
cornus("deploy", "-f", spec, "--delete", "--server", server, env = otel_env)

seen = trace_sink_headers(sink)
assert_eq(len(seen), 1, "the telemetry-on CLI should have made exactly one request to the sink (got %r)" % seen)
tp = seen[0]
# W3C traceparent = "00-<32 hex trace-id>-<16 hex span-id>-<2 hex flags>" (55 chars).
assert_eq(len(tp), 55, "traceparent %r is not a 55-char W3C traceparent" % tp)
assert_true(tp.startswith("00-"), "traceparent %r should start with version 00-" % tp)
assert_true(tp.endswith("-01"), "traceparent %r should carry the sampled flag 01" % tp)
log("✓ telemetry on: CLI injected sampled traceparent %s" % tp)

# W3C Baggage: main() attaches the invoking command as a `cornus.command` member,
# so the `baggage` header must carry it (the command here is "deploy").
bag = trace_sink_headers(sink, name = "baggage")[0]
assert_contains(bag, "cornus.command=deploy", "baggage %r should carry the invoking command" % bag)
log("✓ telemetry on: CLI propagated W3C baggage %s" % bag)

# --- 2. telemetry OFF: no traceparent is injected (zero-cost opt-in gate) -------
# Same request with no OTEL env at all: Setup is a no-op, the global propagator
# stays no-op, and the injector yields an empty header.
cornus("deploy", "-f", spec, "--delete", "--server", server)

seen = trace_sink_headers(sink)
assert_eq(len(seen), 2, "the telemetry-off CLI should have reached the sink too (got %r)" % seen)
assert_eq(seen[1], "", "telemetry-off CLI must inject no traceparent (got %r)" % seen[1])
log("✓ telemetry off: CLI injected no traceparent")
