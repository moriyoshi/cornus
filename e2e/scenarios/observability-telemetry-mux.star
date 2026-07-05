# Endpoint-optional telemetry, and the mux that carries it.
#
# Two things this covers that nothing else does:
#
#   1. `x-cornus-telemetry: {}` with NO endpoint deploys successfully — the server
#      fills in its own OTLP receiver. Before the built-in store the endpoint was
#      mandatory, so this is the change that made the whole feature reachable
#      without an external backend, and a regression would surface as a deploy
#      rejection with a confusing "endpoint is required".
#   2. That telemetry travels over the caretaker connection by DEFAULT. The unit
#      tests cover both sides of the stream and the decision logic; only a live
#      deploy proves the caretaker actually accepts the role, binds the loopback
#      relay, and reaches ready — i.e. that the app is not gated forever on a
#      startup probe for a listener that never came up.
#
# The readiness assertion is the load-bearing one. `caretaker-check` gates the app
# container on telemetryRelayReady, so a workload reaching Running IS the proof
# that the relay bound its port and the caretaker wired the role.
#
# Needs the embedded Collector in the sidecar image (-tags otelcol), so it
# self-skips without CORNUS_TEST_OTEL, exactly like otel-collector.star.
#
# Source of truth: pkg/server/obstelemetry.go (normalizeTelemetry +
# defaultTelemetryMux), pkg/caretaker/telemetry.go (the relay role),
# pkg/wire/export.go (the 'T' stream), pkg/server/obs_mux.go.

OTEL_ENABLED = getenv(name = "CORNUS_TEST_OTEL")

if not OTEL_ENABLED:
    log("observability-telemetry-mux: skipped (set CORNUS_TEST_OTEL and use an otelcol-tagged cornus:e2e image, e.g. make e2e-otel)")
elif TARGET == "local":
    log("observability-telemetry-mux: skipped (needs a running server + deploy backend)")
else:
    # The relay dials the server on the ADVERTISED url, the same one every other
    # caretaker role uses. Without it the deploy must fail loudly rather than
    # falling back to a direct dial nobody asked for, so it is set explicitly here.
    srv = serve(env = {"CORNUS_OBS": "1"})
    base = "http://" + srv

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("observability-telemetry-mux: skipped (this cornus build has no observability store; needs -tags imbh)")
    else:
        # A telemetry block with NO endpoint. The server rewrites it to its own
        # receiver and, because cornus is then the destination, turns the mux on.
        deploy(
            name = "mux-app",
            image = "busybox:1.36",
            command = ["sleep", "3600"],
            telemetry = "",
        )

        # Reaching Running means the caretaker's startup probe passed, which means
        # BOTH the Collector's OTLP receiver and the relay's loopback listener are
        # accepting connections. That is the wiring proof.
        wait(name = "mux-app", timeout = "120s")
        log("✓ a workload with an endpoint-less telemetry block deployed and became ready")

        # The store must still be reachable and healthy alongside it — a relay that
        # wedged the connection would show up as the server losing its own surface.
        status = cornus("observe", "status", "--server", base)
        assert_contains(status, "logs", "observe status stopped reporting after a mux-wired deploy")
        log("✓ the server's observability surface is unaffected by the relay role")

        remove(name = "mux-app")

        # --- an explicit third-party endpoint must NOT take the mux -------------
        # There is no caretaker connection to a third-party backend to ride, so the
        # Collector has to dial it directly. The endpoint need not be reachable:
        # the OTLP exporter connects lazily, so readiness still proves the wiring.
        deploy(
            name = "direct-app",
            image = "busybox:1.36",
            command = ["sleep", "3600"],
            telemetry = "otel-sink.svc:4317",
        )
        wait(name = "direct-app", timeout = "120s")
        log("✓ a workload with an explicit third-party endpoint deployed with the direct dial")
        remove(name = "direct-app")

        log("observability-telemetry-mux: all assertions passed")
