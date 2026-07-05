# Workload telemetry: the caretaker's embedded OpenTelemetry Collector (the `otel`
# role). We deploy a plain workload with `telemetry=` and prove the end-to-end
# WIRING on a real kube backend:
#
#   1. the workload reaches Running — which, because the caretaker startup probe
#      gates the app on otelReady (the OTLP receiver accepting connections on pod
#      loopback), proves the embedded Collector is compiled into the sidecar image,
#      started, and listening;
#   2. the backend auto-injected OTEL_EXPORTER_OTLP_ENDPOINT + OTEL_SERVICE_NAME into
#      the app container (read back from inside it over pod_exec).
#
# The collector must be compiled into the cornus:e2e sidecar image (-tags otelcol),
# so this SELF-SKIPS unless CORNUS_TEST_OTEL is set (the `make e2e-otel` target builds
# the tagged image and sets it). Kube-only: pod_exec + the caretaker-sidecar path.
#
# The export endpoint below need not be reachable — the OTLP exporter connects
# lazily and does not block startup, so the receiver still binds and the wiring is
# provable without a live backend or a telemetry-emitting app (that deeper
# pass-through is covered by the pkg/otelcollector collector test).
#
# Source of truth: pkg/otelcollector (embedded collector), pkg/caretaker (otel role
# + otelReady), pkg/deploy/kubernetes (addTelemetryRole / injectTelemetry),
# pkg/deploy/telemetry.go (BuildTelemetryWiring).

OTEL_ENABLED = getenv(name = "CORNUS_TEST_OTEL")

if not OTEL_ENABLED:
    log("otel-collector: skipped (set CORNUS_TEST_OTEL and use an otelcol-tagged cornus:e2e image, e.g. make e2e-otel)")
elif TARGET != "kube":
    log("otel-collector: skipped (kube-only; the otel role rides the caretaker sidecar)")
else:
    serve()

    # busybox app kept alive with sleep; the caretaker runs the embedded Collector on
    # pod loopback and the backend injects OTEL_* into the app pointing at it.
    deploy(
        name = "otel-app",
        image = "busybox:1.36",
        command = ["sleep", "3600"],
        telemetry = "otel-sink.svc:4317",
    )

    # Reaching Running proves the caretaker's otel readiness gate passed — i.e. the
    # embedded Collector started and its OTLP receiver is accepting connections. If
    # the collector were not compiled into the sidecar image the probe would fail and
    # this would time out.
    wait(name = "otel-app", running = 1, timeout = "240s")
    log("✓ workload is up — the embedded collector started and its receiver is bound")

    # The app was auto-wired to the loopback receiver.
    ep = pod_exec(app = "otel-app", cmd = "printenv OTEL_EXPORTER_OTLP_ENDPOINT")
    assert_contains(ep, "127.0.0.1:4317", "OTEL_EXPORTER_OTLP_ENDPOINT injected into the app")
    log("✓ OTEL_EXPORTER_OTLP_ENDPOINT reaches the app container")

    sn = pod_exec(app = "otel-app", cmd = "printenv OTEL_SERVICE_NAME")
    assert_contains(sn, "otel-app", "OTEL_SERVICE_NAME defaults to the deployment name")
    log("✓ OTEL_SERVICE_NAME auto-set to the deployment name")

    remove(name = "otel-app")
    log("✓ removed the otel workload")
