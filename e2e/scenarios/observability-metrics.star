# Zero-touch workload and server metrics: the resource-usage feed.
#
# The unit tests drive the recorder against a fake sampler and assert the OTLP it
# builds. What they structurally cannot show is whether a REAL backend answers at
# all — every one of them replaces the backend with something that returns
# whatever it is asked for, and "the backend has a metrics source" is precisely
# the fact a fake cannot testify to.
#
# Three things only a live run proves:
#
#   1. A real backend samples, so the chain from Backend.SampleMetrics through
#      the encoder, the acceptance path, the store, and PromQL actually composes.
#   2. Each REPLICA is sampled separately. This is the one that matters most: a
#      collector that read instance 0 N times would emit N series that all look
#      right, differing only in a label. Nothing short of running several replicas
#      of a workload doing DIFFERENT amounts of work can tell the two apart.
#   3. Recorded metrics reach the re-export upstream, not just the store. The log
#      feed had exactly this bug — writing to the store directly, bypassing
#      re-export — and it passed every unit test.
#
# Source of truth: pkg/server/metricsrecorder.go, pkg/server/selfmetrics.go,
# pkg/observability/otlpbridge.go, pkg/obsstore/otlpenc_metrics.go, and each
# backend's SampleMetrics.

def distinct_values(d):
    """The set of distinct values in d, as a dict used as a set."""
    out = {}
    for k in d:
        out[d[k]] = True
    return out

if TARGET == "local":
    log("skip observability-metrics: needs a running server + deploy backend")
else:
    upstream = otlp_collector()

    # A short sampling interval so the scenario does not spend a minute waiting
    # for the default 15s cadence to produce enough points to be interesting.
    srv = serve(env = {
        "CORNUS_OBS": "1",
        "CORNUS_OBS_METRICS_INTERVAL": "2s",
        "CORNUS_OBS_EXPORT_ENDPOINT": "http://" + upstream,
    })
    base = "http://" + srv

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("skip observability-metrics: this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        # Three replicas of a workload that burns CPU, so there is something to
        # measure and the numbers have room to diverge between replicas.
        deploy(
            name = "metrics-probe",
            image = "busybox:latest",
            replicas = 3,
            command = ["sh", "-c", "while :; do dd if=/dev/zero of=/dev/null bs=1M count=50 2>/dev/null; sleep 1; done"],
        )
        wait(name = "metrics-probe", timeout = "90s", running = 3)

        # --- 1. the recorder is running and finding replicas -------------------
        # observe status is checked BEFORE any query, because it distinguishes the
        # failure modes an empty series cannot: nothing deployed, the backend
        # refusing, or the readings being shed.
        # Poll until THIS workload has been sampled, not merely until some
        # reading has landed: the backend may be managing other deployments
        # (the E2E shares the host daemon), and a `sampled > 0` that came from
        # one of those says nothing about the workload under test.
        status = {}
        m = {}
        probed = ""
        for _ in range(60):
            status = json.decode(http_get(base + "/.cornus/v1/obs/status")["body"])
            m = status.get("metrics", {})
            probed = cornus("observe", "metrics", "--server", base, "container_memory_usage", "--since", "10m")
            if "metrics-probe" in probed:
                break
            sleep("2s")
        assert_true(m != None and m != {}, "observe status reports no metrics recorder at all: %r" % status)
        assert_true(
            m.get("sampled", 0) > 0,
            "the recorder took no readings (replicas=%r failed=%r); a real backend refused to sample" %
            (m.get("replicas"), m.get("failed")),
        )
        assert_true(
            m.get("failed", 0) == 0,
            "the recorder reported %r failed samples — the backend is refusing" % m.get("failed"),
        )
        log("✓ the recorder sampled %d reading(s) across %d replica(s)" % (m.get("sampled", 0), m.get("replicas", 0)))

        # --- 2. the metrics are queryable, under the names the docs promise ----
        # These are the PROMQL spellings, which are not the OTLP names: the store
        # resolves `container.memory.usage` as `container_memory_usage`, with no
        # unit suffix. Asserting the documented spelling is the point — a name
        # that only works in SQL is a name the docs would be lying about.
        assert_contains(probed, "metrics-probe", "PromQL `container_memory_usage` never returned the workload")
        for name in ["container_cpu_time", "cornus_container_pids"]:
            out = cornus("observe", "metrics", "--server", base, name, "--since", "10m")
            assert_true(
                "metrics-probe" in out,
                "PromQL `%s` returned nothing for the workload; got %r" % (name, out),
            )
        log("✓ container metrics answer PromQL under their documented names")

        # --- 3. EVERY replica, with DIFFERENT numbers --------------------------
        # The label must be `cornus_replica` and not `cornus.replica`: the store's
        # PromQL cannot express a dotted label name, so the dotted spelling makes
        # the series silently unfilterable — a matcher for it returns zero series
        # and no error.
        replicas = {}
        for _ in range(30):
            series = json.decode(http_get(
                base + "/.cornus/v1/obs/metrics?query=container_cpu_time&since=10m&step=15s",
            )["body"])
            replicas = {}
            for entry in series:
                labels = entry.get("labels", {})
                if labels.get("service") != "metrics-probe":
                    continue
                if labels.get("cpu_mode") != "user":
                    continue
                assert_true(
                    "cornus_replica" in labels,
                    "a datapoint carries no cornus_replica label, so replicas cannot be compared: %r" % labels,
                )
                pts = entry.get("points", [])
                if pts:
                    replicas[labels["cornus_replica"]] = pts[-1]["v"]
            if len(replicas) == 3 and len(distinct_values(replicas)) > 1:
                break
            sleep("2s")

        assert_true(
            len(replicas) == 3,
            "found %d replica series, want 3 — the collector is not sampling each instance: %r" % (len(replicas), replicas),
        )
        # Distinct VALUES, not merely distinct labels. Three identical readings
        # under three ordinals is exactly what a first-instance-only collector
        # produces, and it is indistinguishable from a balanced workload unless
        # the numbers are compared.
        assert_true(
            len(distinct_values(replicas)) > 1,
            "all 3 replicas reported the identical CPU time %r — instance 0 is being sampled three times" % replicas,
        )
        log("✓ all 3 replicas sampled independently, with distinct values: %r" % replicas)

        # --- 4. the server's own resource usage --------------------------------
        # This rides a different path from the workload metrics: observable
        # instruments on the global meter, collected by the SDK and delivered
        # through the in-process bridge. Its own end-to-end assertion, because the
        # workload path passing says nothing about it.
        # Queried over HTTP with allow_error rather than through the CLI: until
        # the first bridge collection lands, the metric name does not resolve and
        # the store answers 400 — which is the EXPECTED state while waiting, not a
        # scenario failure. The CLI would exit non-zero and abort the run.
        found_self = False
        for _ in range(40):
            resp = http_get(
                base + "/.cornus/v1/obs/metrics?query=process_memory_usage&since=10m&step=15s",
                allow_error = True,
            )
            if resp.get("status", 0) == 200 and "process.memory.usage" in resp.get("body", ""):
                found_self = True
                break
            sleep("2s")
        assert_true(
            found_self,
            "the server never recorded its own memory usage; the SDK metric bridge is not reaching the store",
        )
        log("✓ the server's own process metrics reached the store")

        # --- 5. re-export gets the readings too --------------------------------
        # The regression this exists for: a feed that writes to the store directly
        # reaches the store and nothing else, and every unit test still passes.
        forwarded = otlp_metrics(addr = upstream, min = 1, name = "container.memory.usage", timeout = "60s")
        names = {}
        for p in forwarded:
            names[p["name"]] = True
        assert_true(
            "container.memory.usage" in names,
            "no workload metrics reached the re-export upstream; the recorder is bypassing acceptOTLP. Got %r" % names.keys(),
        )
        # The upstream sees OTLP names (dotted), not the PromQL spelling — the
        # normalization is the store's, not the wire's.
        assert_true(
            "container_memory_usage" not in names,
            "the forwarded payload used the PromQL spelling; the upstream must receive the OTLP name",
        )
        log("✓ recorded metrics reached the re-export upstream as OTLP")

        remove(name = "metrics-probe")
        log("observability-metrics: all assertions passed")
