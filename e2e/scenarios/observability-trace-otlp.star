# Distributed tracing: client -> server span PARENTAGE, proven with a real OTLP
# collector. Where observability-trace.star only checks the headers the client
# injects, this exports the spans themselves: both a served cornus and a client
# cornus push traces (OTLP/HTTP) to an in-harness collector, and we assert a
# client span (service "cornus-cli") and a REAL server span (service "cornus")
# share one trace id and the server span is a child (non-empty parent). That is
# the server half of the client <> server <> caretaker trace.
#
# Needs a running server + a reachable deploy backend (the DELETE hits the deploy
# backend), so it self-skips on the local target, which has neither.
#
# Source of truth: cmd/cornus/serve.go (server Setup, service "cornus"),
# cmd/cornus/main.go (client Setup, service "cornus-cli"), pkg/server/observability.go
# (otelHandler extracts the incoming context and parents the request span to it).

if TARGET == "local":
    log("skip observability-trace-otlp: needs a running server + deploy backend")
else:
    col = otlp_collector()
    # Both processes export ONLY traces to the collector (metrics/logs off), over
    # OTLP/HTTP protobuf. A short batch schedule flushes the server span quickly.
    otel_env = {
        "CORNUS_OTEL": "1",
        "OTEL_TRACES_EXPORTER": "otlp",
        "OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
        "OTEL_EXPORTER_OTLP_ENDPOINT": "http://" + col,
        "OTEL_METRICS_EXPORTER": "none",
        "OTEL_LOGS_EXPORTER": "none",
        "OTEL_BSP_SCHEDULE_DELAY": "300",
    }
    addr = serve(env = otel_env)

    work = temp_dir()
    spec = work + "/spec.yaml"
    write_file(path = spec, content = "name: trace-probe\nimage: busybox:latest\n")

    # One instrumented client -> server request. delete-if-exists is a no-op
    # success, so the CLI exits 0; the point is the DELETE span it and the server
    # both export.
    cornus("deploy", "-f", spec, "--delete", "--server", "http://" + addr, env = otel_env)

    # Wait specifically for the server ("cornus") span — the client spans flush
    # eagerly on CLI exit, but the server span arrives on the server's batch
    # schedule, so racing on a bare count would return before it lands.
    spans = otlp_spans(col, service = "cornus", min = 1, timeout = "30s")
    assert_true(len(spans) >= 2, "expected client + server spans, got %r" % spans)

    # Collect the trace ids the client (cornus-cli) exported.
    client_traces = {}
    for s in spans:
        if s["service"] == "cornus-cli":
            client_traces[s["trace_id"]] = True
    assert_true(len(client_traces) > 0, "no client (cornus-cli) span exported; spans=%r" % spans)

    # A server (cornus) span must share one of those trace ids and be a child.
    matched = None
    for s in spans:
        if s["service"] == "cornus" and s["trace_id"] in client_traces:
            matched = s
    assert_true(matched != None, "no server span shares the client's trace id; spans=%r" % spans)
    assert_true(matched["parent_span_id"] != "", "server span %r should be a child (non-empty parent)" % matched)
    log("✓ client + server spans share trace %s; server span %s (%s) parent %s" % (
        matched["trace_id"],
        matched["span_id"],
        matched["name"],
        matched["parent_span_id"],
    ))
