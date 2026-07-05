# Re-export: cornus as an aggregation point rather than a destination.
#
# The unit tests drive the forwarder directly against a stub upstream. What they
# cannot show is the whole chain actually composing on a live server: a real
# workload prints to stdout, the zero-touch recorder turns that into OTLP, the
# store keeps it, AND the forwarder ships the same bytes to a real backend. Every
# link there is a place the payload could be dropped or mangled, and only an
# end-to-end run crosses all of them at once.
#
# It also covers the shape that has no other coverage anywhere: the GATEWAY, where
# cornus stores nothing and exists purely to forward. That configuration needs no
# `imbh` build at all, so it is the one part of this file that runs on a stock
# binary — and it is asserted separately for exactly that reason.
#
# Source of truth: pkg/server/obsexport.go (the forwarder), pkg/server/obs_otlp.go
# (acceptOTLP, the single point both transports converge on), pkg/server/logrecorder.go.

if TARGET == "local":
    log("skip observability-export: needs a running server + deploy backend")
else:
    # The harness's in-process OTLP receiver stands in for the operator's real
    # backend. It decodes the logs signal, so what the workload printed can be read
    # back on the far side rather than merely counted.
    upstream = otlp_collector()

    # --- 1. store AND forward, together ---------------------------------------
    srv = serve(env = {
        "CORNUS_OBS": "1",
        "CORNUS_OBS_EXPORT_ENDPOINT": "http://" + upstream,
    })
    base = "http://" + srv

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("skip observability-export (store leg): this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        marker = "cornus-export-e2e-marker"

        # Long-lived deliberately. A `restart="no"` spec is run-to-completion, and
        # the kubernetes backend realizes that as a **Job** — which
        # (*kubernetes.Backend).List does not enumerate (it lists Deployments
        # only), so the log recorder never opens a tail and nothing is ever
        # recorded. That is a real defect, but it belongs to the one-shot path, not
        # to re-export; it is pinned by the gated section at the end of
        # observability-store.star so this scenario keeps testing its own subject.
        deploy(
            name = "export-probe",
            image = "busybox:latest",
            command = ["sh", "-c", "echo %s; sleep 600" % marker],
        )
        wait(name = "export-probe", timeout = "60s")

        # The local copy.
        stored = ""
        for _ in range(30):
            got = cornus("observe", "logs", "--server", base, "--service", "export-probe", "--limit", "50")
            if marker in got:
                stored = got
                break
            sleep("1s")
        assert_contains(stored, marker, "the recorder did not store the workload's output")
        log("✓ the workload's output reached the local store")

        # The same bytes, at the upstream. This is the assertion the feature is
        # for: one export by the workload, two destinations.
        forwarded = otlp_logs(addr = upstream, min = 1, service = "export-probe", timeout = "30s")
        bodies = [rec["body"] for rec in forwarded]
        assert_true(
            marker in " ".join(bodies),
            "the recorded line never reached the re-export upstream; got %r" % bodies,
        )
        log("✓ the same output was forwarded to the upstream backend")

        # The forwarder's own counters, which are how an operator tells "slow
        # upstream" (dropped) from "broken upstream" (failed) from "fine".
        status = cornus("observe", "status", "--server", base)
        assert_contains(status, "re-export", "observe status does not report the re-export upstream")
        assert_contains(status, upstream, "observe status does not name the configured upstream")
        assert_true(
            "FAILED" not in status,
            "the forwarder reported failures against a healthy upstream:\n%s" % status,
        )
        log("✓ observe status reports the forwarder and shows no failures")

        remove(name = "export-probe")

    stop_server()

    # --- 2. the GATEWAY shape: forward only, store nothing ---------------------
    # The store is turned OFF explicitly, not merely left unrequested: with the
    # store compiled in (-tags imbh) it defaults to ON (cmd/cornus/serve.go
    # resolveObsEnabled follows the BUILD when --obs/--no-obs is unspecified),
    # and this scenario's data dir already holds the store the leg above wrote.
    # Omitting CORNUS_OBS therefore did not produce a gateway at all — it
    # produced a second store-backed server that answered the query route with
    # the PREVIOUS server's records. That went unnoticed for as long as no
    # containerized leg had the store compiled in.
    #
    # The receive route must still exist, because there IS somewhere to put
    # telemetry — just not locally. This configuration needs no store at all, so
    # it is asserted even when the store leg above skipped.
    gwUp = otlp_collector()
    gw = serve(env = {"CORNUS_OBS": "0", "CORNUS_OBS_EXPORT_ENDPOINT": "http://" + gwUp})
    gwBase = "http://" + gw

    # The QUERY surface must be absent: there is nothing to read back.
    q = http_get(gwBase + "/.cornus/v1/obs/logs", allow_error = True)
    assert_eq(
        q.get("status", 0),
        404,
        "a store-less gateway answered a query route; there is nothing to query: %r" % q,
    )
    log("✓ gateway mode exposes no query surface")

    # The RECEIVE surface must be present. An empty body is a well-formed no-op
    # export, so a 200 here proves the route exists and accepts without needing to
    # hand-build protobuf in Starlark.
    r = http("POST", gwBase + "/.cornus/v1/otlp/v1/logs", body = "", headers = {"Content-Type": "application/x-protobuf"})
    assert_eq(r["status"], 200, "a store-less gateway refused an OTLP export: %r" % r)
    log("✓ gateway mode accepts OTLP with no store configured")

    # And it still rejects garbage, so "accepts" does not mean "accepts anything".
    bad = http("POST", gwBase + "/.cornus/v1/otlp/v1/logs", body = "definitely not protobuf", headers = {"Content-Type": "application/x-protobuf"})
    assert_eq(bad["status"], 400, "gateway mode accepted an unparseable export: %r" % bad)
    log("✓ gateway mode still rejects an unparseable export with 400")

    log("observability-export: all assertions passed")
