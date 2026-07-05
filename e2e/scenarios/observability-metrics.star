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

# Backend-dependent metric families. Every host backend reads a cgroup and can
# report CUMULATIVE cpu time split by mode, plus a pid count. The kubernetes
# backend cannot: it has no access to the node, so its only portable source is
# the metrics.k8s.io aggregated API, which reports an instantaneous CPU RATE and
# no pid count at all (pkg/deploy/kubernetes/stats.go). The recorder therefore
# emits `container.cpu.usage` (gauge) instead of `container.cpu.time` (sum) and
# skips `cornus.container.pids` — see pkg/server/metricsrecorder.go. Asserting
# the host spelling on kube would be asserting something the product
# deliberately does not do; asserting nothing would be worse. So the family
# names are parameterized and everything else is identical.
CPU_METRIC = "container_cpu_time"
CPU_MODE = "user"  # container.cpu.time is split by mode; the rate gauge is not
EXTRA_METRICS = ["cornus_container_pids"]
if TARGET == "kube":
    CPU_METRIC = "container_cpu_usage"
    CPU_MODE = ""
    EXTRA_METRICS = []

def distinct_values(d):
    """The set of distinct values in d, as a dict used as a set."""
    out = {}
    for k in d:
        out[d[k]] = True
    return out

def series_ready(base, name, service = "metrics-probe"):
    """One probe: does the store already hold a `name` series for `service`?

    Asked over HTTP with allow_error, NEVER through the CLI. Until the first
    reading for a family lands, the metric name does not resolve and the store
    answers `400 ... metric "X" is not resolved` — which is the EXPECTED state
    while waiting, not a failure. `cornus observe metrics` exits non-zero on it
    and the `cornus()` builtin aborts the scenario, which is exactly what
    happened the first time this scenario ran on kube: on a host backend the
    first cgroup read lands within one 2s recorder tick, but on kube the first
    reading waits on a metrics-server SCRAPE WINDOW (15s by default), so the
    very first CLI query raced it and killed the run. So: poll over HTTP, then
    let the CLI assert the documented PromQL spelling once the answer exists.
    """
    resp = http_get(
        base + "/.cornus/v1/obs/metrics?query=" + name + "&since=10m&step=15s",
        allow_error = True,
    )
    return resp.get("status", 0) == 200 and service in resp.get("body", "")

def wait_series(base, name, service = "metrics-probe", steps = 45):
    """Poll series_ready until it is true, or give up after `steps` x 2s."""
    for _ in range(steps):
        if series_ready(base, name, service):
            return True
        sleep("2s")
    return False

def metrics_server_available():
    """True when this cluster serves metrics.k8s.io; fails if it is broken.

    Probed off the CLUSTER rather than off E2E_METRICS_SERVER, the same shape as
    deploy-ingress.star's ingress-controller gate: the flag installs
    metrics-server, but this scenario also runs (via `make e2e-kube`, and the
    plain kube CI leg) against clusters where it was never set. An APIService
    read with --ignore-not-found exits 0 either way, so it comes first.

    An APIService that EXISTS but is not Available is a hard failure, never a
    reason to fall back to skipping: metrics-server having been installed is the
    whole reason this leg exists, and "installed but broken" is exactly the
    result it should report.
    """
    api = kubectl("get", "apiservice", "v1beta1.metrics.k8s.io", "--ignore-not-found", "-o", "name").strip()
    if api == "":
        return False
    cond = kubectl("get", "apiservice", "v1beta1.metrics.k8s.io", "-o",
                   "jsonpath={.status.conditions[?(@.type=='Available')].status}").strip()
    assert_eq(cond, "True",
              "metrics.k8s.io is registered but not Available (status=%r); metrics-server is installed " % cond +
              "and broken, so the kubernetes backend has no metric source")
    return True

# The kube gate. Without metrics-server the kubernetes backend's SampleMetrics
# 404s on every call, so there is nothing to exercise and the scenario would only
# report that a real backend refused — which is true but says nothing about the
# code under test.
metrics_server = False
if TARGET == "kube":
    metrics_server = metrics_server_available()

if TARGET == "local":
    log("skip observability-metrics: needs a running server + deploy backend")
elif TARGET == "kube" and not metrics_server:
    log("skip observability-metrics: this cluster has no metrics-server, so metrics.k8s.io " +
        "does not exist and the kubernetes backend has no metric source. Install it with " +
        "E2E_METRICS_SERVER=1 (make e2e-container E2E_TARGETS=kube E2E_METRICS_SERVER=1 ...).")
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

    # A leg that went to the trouble of installing metrics-server must not go
    # green having skipped: "no observability store" is a different absence from
    # "no metrics-server", and only the second one is this scenario's to tolerate.
    # Building the runner with the store is `E2E_BUILD_TAGS="netgo osusergo imbh
    # sable_extern_lib"` (a cgo build; see e2e/container/Dockerfile).
    if metrics_server and probe.get("status", 0) != 200:
        fail(msg = "metrics-server is installed in this cluster, so this leg exists to exercise the " +
                   "kubernetes metric path — but this cornus build has no observability store " +
                   "(needs -tags 'imbh sable_extern_lib'), so none of the assertions can run. " +
                   "Probe returned %r" % probe)

    if probe.get("status", 0) != 200:
        log("skip observability-metrics: this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        # Up-front cleanup: Starlark has no defer, so a run that failed midway
        # leaves the probe workload behind. A leftover would satisfy the wait()
        # below and feed the series assertions with a previous run's replicas.
        if status(name = "metrics-probe")["total"] > 0:
            remove(name = "metrics-probe")

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
        obs_status = {}
        m = {}
        landed = False
        for _ in range(90):
            obs_status = json.decode(http_get(base + "/.cornus/v1/obs/status")["body"])
            m = obs_status.get("metrics", {})
            landed = series_ready(base, "container_memory_usage")
            if landed:
                break
            sleep("2s")
        assert_true(m != None and m != {}, "observe status reports no metrics recorder at all: %r" % obs_status)
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
        # On failure, show what the store actually answered. `the recorder reported
        # sampled=608 failed=0` says the sampling half worked and tells you nothing
        # about why the query half did not — which is exactly the shape this gate
        # failed with in CI (608 readings across 7 replicas, no series), leaving
        # nothing to act on. One extra request, only on the failure path.
        if not landed:
            probe = http_get(
                base + "/.cornus/v1/obs/metrics?query=container_memory_usage&since=10m&step=15s",
                allow_error = True,
            )
            fail(msg = "no `container_memory_usage` series ever appeared for the workload; " +
                       "the recorder reported %r, and the store answered status=%r body=%r" %
                       (m, probe.get("status", 0), probe.get("body", probe.get("error", ""))[:800]))
        for name in ["container_memory_usage"] + [CPU_METRIC] + EXTRA_METRICS:
            assert_true(wait_series(base, name),
                        "no `%s` series appeared for the workload within the poll window" % name)
            out = cornus("observe", "metrics", "--server", base, name, "--since", "10m")
            assert_true(
                "metrics-probe" in out,
                "PromQL `%s` returned nothing for the workload; got %r" % (name, out),
            )
        log("✓ container metrics answer PromQL under their documented names (cpu family: %s)" % CPU_METRIC)

        # --- 3. EVERY replica, with DIFFERENT numbers --------------------------
        # The label must be `cornus_replica` and not `cornus.replica`: the store's
        # PromQL cannot express a dotted label name, so the dotted spelling makes
        # the series silently unfilterable — a matcher for it returns zero series
        # and no error.
        # 60 x 2s rather than 30 x 2s: on kube each reading is a metrics-server
        # scrape, whose default resolution is 15s, so three replicas need several
        # scrape windows before their numbers can be compared at all.
        # allow_error + the status check are not defensive padding: this endpoint
        # answers a JSON OBJECT ({"error": ...}) on 400 and a JSON ARRAY on 200,
        # and 400 is the documented transient state while a metric name is still
        # unresolved (see series_ready). Decoding both and iterating gives dict
        # KEYS — strings — so the loop died with `string has no .get field or
        # method`, which names neither the query, the status, nor the reason. Poll
        # through a non-200 the way every other section here already does, and let
        # the assertions below report what was actually seen.
        replicas = {}
        last = {}
        for _ in range(60):
            last = http_get(
                base + "/.cornus/v1/obs/metrics?query=" + CPU_METRIC + "&since=10m&step=15s",
                allow_error = True,
            )
            if last.get("status", 0) != 200:
                sleep("2s")
                continue
            series = json.decode(last["body"])
            replicas = {}
            for entry in series:
                labels = entry.get("labels", {})
                if labels.get("service") != "metrics-probe":
                    continue
                if CPU_MODE != "" and labels.get("cpu_mode") != CPU_MODE:
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
            "found %d replica series, want 3 — the collector is not sampling each instance: %r (last query: status=%r body=%r)" %
            (len(replicas), replicas, last.get("status", 0), last.get("body", last.get("error", ""))[:400]),
        )
        # Distinct VALUES, not merely distinct labels. Three identical readings
        # under three ordinals is exactly what a first-instance-only collector
        # produces, and it is indistinguishable from a balanced workload unless
        # the numbers are compared.
        assert_true(
            len(distinct_values(replicas)) > 1,
            "all 3 replicas reported the identical %s %r — instance 0 is being sampled three times" % (CPU_METRIC, replicas),
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
