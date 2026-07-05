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

# The memory limit the probe declares, and the one number this scenario asserts
# by VALUE rather than by existence.
#
# It is here because `cornus_container_memory_limit` was missing on kube for a
# different reason from the families above, and one that looked identical from a
# query: metrics.k8s.io does not report a limit, so the sampler left MemLimit at
# zero and the recorder — which skips a zero limit, correctly, since "unlimited"
# and "a ceiling of nothing" are opposite claims — emitted no series at all. But
# the limit is not a measurement. It is a number cornus itself wrote into the pod
# spec, and reading it back needs no metrics source. Every host backend reports a
# limit whether or not one was declared (the cgroup's, or the host's total as
# docker does), so only kube could ever have shown this, and only with a workload
# that actually declares one.
MEM_LIMIT = 256 * 1024 * 1024

# Every workload family the recorder can emit, in PromQL spelling. Used to check
# the backend's `unsupported` declaration against what the store actually holds —
# see section 2c.
ALL_FAMILIES = [
    "container_cpu_time",
    "container_cpu_usage",
    "container_memory_usage",
    "cornus_container_memory_limit",
    "container_network_io",
    "container_disk_io",
    "cornus_container_pids",
]

# What THIS scenario obtains positive proof of, on this target: it queries each
# one and requires a series for the probe workload. Anything in here must not be
# declared unsupported, or the dashboard is hiding a chart that works.
OBSERVED = ["container_memory_usage", "cornus_container_memory_limit"] + [CPU_METRIC] + EXTRA_METRICS

# What the kubernetes backend declares it cannot record (pkg/deploy/kubernetes/
# stats.go). Pinned exactly, and only for kube, because this is the backend the
# declaration exists for and its list is a stable property of metrics.k8s.io
# rather than of the host the test happens to run on. Note the absence of
# cornus_container_memory_limit: it used to belong here and does not any more.
KUBE_UNSUPPORTED = [
    "container_cpu_time",
    "cornus_container_pids",
    "container_network_io",
    "container_disk_io",
]

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
        #
        # mem_limit is what makes the memory-limit family observable on kube at
        # all: without a declared limit the pod spec carries none, and "no limit"
        # correctly produces no series, so a scenario without it could not tell a
        # working read-back from the bug. Far above what busybox + dd needs, so the
        # limit is never the reason a replica dies.
        deploy(
            name = "metrics-probe",
            image = "busybox:latest",
            replicas = 3,
            mem_limit = MEM_LIMIT,
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
        for name in OBSERVED:
            assert_true(wait_series(base, name),
                        "no `%s` series appeared for the workload within the poll window" % name)
            out = cornus("observe", "metrics", "--server", base, name, "--since", "10m")
            assert_true(
                "metrics-probe" in out,
                "PromQL `%s` returned nothing for the workload; got %r" % (name, out),
            )
        log("✓ container metrics answer PromQL under their documented names (cpu family: %s)" % CPU_METRIC)

        # --- 2a. the memory limit is the one we asked for ----------------------
        # By VALUE, because existence is not the property that broke. On kube the
        # limit is read back off the pod spec, so the recorded number must be
        # exactly the number the deploy declared — an approximation here would mean
        # it came from somewhere else. On the host backends the cgroup is the
        # source and the kernel may round, or report the host's total when the
        # limit could not be enforced, so those assert only that a real ceiling was
        # recorded rather than a zero.
        limits = {}
        limit_series = http_get(
            base + "/.cornus/v1/obs/metrics?query=cornus_container_memory_limit&since=10m&step=15s",
            allow_error = True,
        )
        assert_true(
            limit_series.get("status", 0) == 200,
            "cornus_container_memory_limit did not resolve after its series appeared: status=%r body=%r" %
            (limit_series.get("status", 0), limit_series.get("body", limit_series.get("error", ""))[:400]),
        )
        for entry in json.decode(limit_series["body"]):
            labels = entry.get("labels", {})
            if labels.get("service") != "metrics-probe":
                continue
            pts = entry.get("points", [])
            if pts:
                limits[labels.get("cornus_replica", "?")] = pts[-1]["v"]
        assert_true(
            len(limits) > 0,
            "cornus_container_memory_limit resolved but carried no series for the workload: %r" %
            limit_series.get("body", "")[:400],
        )
        for replica in limits:
            if TARGET == "kube":
                assert_eq(
                    int(limits[replica]), MEM_LIMIT,
                    ("replica %s recorded a memory limit of %r, want exactly %d — on kube this number " +
                     "comes from the pod spec cornus itself wrote (podMemLimit in " +
                     "pkg/deploy/kubernetes/stats.go), so anything else means it was read from " +
                     "somewhere it should not have been") % (replica, limits[replica], MEM_LIMIT),
                )
            else:
                assert_true(
                    limits[replica] > 0,
                    "replica %s recorded a memory limit of %r; a zero limit is dropped by the recorder, " %
                    (replica, limits[replica]) +
                    "so a zero here means the series exists carrying a value that should never have been sent",
                )
        log("✓ the enforced memory limit was recorded for %d replica(s): %r" % (len(limits), limits))


        # --- 2b. and KEEP answering as readings accumulate ---------------------
        # Section 2 above asserts a series APPEARS. That is satisfied by the very
        # first reading, and a first reading cannot be a duplicate of anything —
        # so the whole section can pass while the feed is already writing data
        # that will never be queryable again.
        #
        # It did. On kube the recorder's source is metrics-server, which stamps
        # every sample with the KUBELET SCRAPE time and republishes that value
        # unchanged until its next scrape (15-30s), while the recorder here polls
        # every 2s. Recording each poll put many datapoints at ONE timestamp in
        # one series, and the store's PromQL engine refuses to evaluate such a
        # series at all — so every query of `container_memory_usage` and
        # `container_cpu_usage` returned `400 ... duplicate timestamps in one
        # PromQL series`, over every window, while `observe status` cheerfully
        # reported hundreds of readings recorded. Found on a live k3s cluster on
        # 2026-08-04, not here, precisely because this scenario stopped looking
        # after the first reading. pkg/server/metricsrecorder.go now drops a
        # reading whose timestamp it has already written.
        #
        # So: wait out several recorder cycles AND at least one metrics-server
        # scrape window, then ask again. A regression makes these queries fail,
        # not return something subtly wrong, so the check is cheap and total.
        settle = "40s" if TARGET == "kube" else "12s"
        log("waiting %s for the feed to accumulate several readings per replica" % settle)
        sleep(settle)
        for name in OBSERVED:
            again = http_get(
                base + "/.cornus/v1/obs/metrics?query=" + name + "&since=10m&step=15s",
                allow_error = True,
            )
            assert_true(
                again.get("status", 0) == 200,
                ("`%s` was queryable when its first reading landed and is NOT queryable now " +
                 "(status=%r body=%r). A `duplicate timestamps in one PromQL series` here means the " +
                 "recorder wrote one backend reading more than once: see alreadyRecorded in " +
                 "pkg/server/metricsrecorder.go.") %
                (name, again.get("status", 0), again.get("body", again.get("error", ""))[:400]),
            )
            assert_true(
                "metrics-probe" in again.get("body", ""),
                "`%s` stopped returning the workload's series once readings accumulated; got %r" %
                (name, again.get("body", "")[:400]),
            )
        log("✓ container metrics stay queryable after several recorder cycles")

        # The repeat-suppression counter, asserted where its two answers differ.
        # On kube the source is coarser than the 2s interval, so repeats are
        # certain and a zero would mean the guard never ran — the series is
        # queryable above for some other reason, and the regression this section
        # exists for is unprotected. On a host backend each read takes its own
        # timestamp, so a non-zero would mean the guard is discarding readings it
        # should be keeping, which costs resolution silently.
        after = json.decode(http_get(base + "/.cornus/v1/obs/status")["body"]).get("metrics", {})
        if TARGET == "kube":
            assert_true(
                after.get("stale", 0) > 0,
                ("the recorder reported no repeated readings on kube, where metrics-server's scrape " +
                 "window (15-30s) is far coarser than the 2s sampling interval — the repeat guard " +
                 "cannot have been exercised: %r") % after,
            )
            log("✓ %d repeated metrics-server readings were suppressed" % after.get("stale", 0))
        else:
            assert_true(
                after.get("stale", 0) == 0,
                ("the recorder suppressed %r readings on a backend that timestamps every read " +
                 "afresh — readings are being discarded, not deduplicated: %r") %
                (after.get("stale", 0), after),
            )

        # --- 2c. the backend's account of itself matches what it recorded ------
        # `metrics.unsupported` on /obs/status is what the web dashboard uses to
        # decide which panels to leave out entirely, so a wrong entry is invisible
        # in exactly the way that matters: too many and a working chart disappears,
        # too few and a chart is offered that can never fill. Neither shows up as
        # an error anywhere.
        #
        # Checked as an AGREEMENT between the declaration and the store rather than
        # against a hardcoded list per target — a hardcoded list is a second copy of
        # the claim, and would drift the moment a backend gains a source, which is
        # precisely what just happened to the memory limit. The two directions are
        # asserted separately because only one of them is deterministic on every
        # host: a declared-unsupported family must have NO series (always true, or
        # the declaration is wrong), while a supported family only certainly has
        # one for the families this scenario actually queried — a host whose cgroup
        # reports no blkio entries would otherwise fail for a reason that is not
        # about the declaration at all.
        declared = {}
        for dotted in after.get("unsupported", []):
            declared[dotted.replace(".", "_")] = True
            assert_true(
                dotted.replace(".", "_") in ALL_FAMILIES,
                "the backend declared %r unsupported, which is not a workload family the recorder emits; " % dotted +
                "metricNameForField in pkg/server/metricsrecorder.go and ALL_FAMILIES here disagree",
            )
        for name in OBSERVED:
            assert_true(
                name not in declared,
                ("`%s` is declared unsupported, but this run just read a series of it for the workload. " +
                 "The dashboard hides that panel, so a working chart is invisible: %r") % (name, after.get("unsupported")),
            )
        for name in declared:
            assert_true(
                not series_ready(base, name),
                ("`%s` is NOT declared unsupported yet the store holds a series of it for the workload — " +
                 "so the dashboard offers a panel the backend cannot fill. Declaration: %r") %
                (name, after.get("unsupported")),
            )
        if TARGET == "kube":
            # Sorted comparison: the recorder sorts only the no-sampler case, and
            # the order of a declaration is not part of the contract.
            assert_eq(
                sorted(declared.keys()), sorted(KUBE_UNSUPPORTED),
                "the kubernetes backend's unsupported list changed: %r" % after.get("unsupported"),
            )
        else:
            # Every host backend records cumulative CPU time, so none of them can
            # produce the instantaneous gauge — an empty declaration here would mean
            # the status route lost the field on the way out, which would silently
            # un-hide the kube-only panel on every other backend.
            assert_true(
                "container_cpu_usage" in declared,
                ("a host backend must declare container_cpu_usage unsupported (its CPU source is " +
                 "cumulative), but the status route reported %r") % after.get("unsupported"),
            )
        log("✓ the backend's unsupported declaration agrees with what it recorded: %r" % after.get("unsupported"))

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

        # --- 5. the build counter, on the route the CLI actually uses ----------
        # `cornus build` does NOT take POST /.cornus/v1/build. It opens a buildwire
        # session on /.cornus/v1/build/attach, and that handler recorded nothing —
        # so a server that had built all day reported the counter flat at zero,
        # which is what a user reported on 2026-08-04. The same run turned up a
        # second hole underneath it: on a host where mount(2) is not permitted the
        # server DELEGATES to a containerized builder, and both relay paths returned
        # before any counter, so every build on such a host was uncounted whichever
        # route it took. Both are recorded now, the relayed ones labelled
        # delegated="true".
        #
        # This assertion is deliberately agnostic about which of the two happened:
        # it says a build served over /build/attach is counted, which is the claim
        # that holds on a privileged host and an unprivileged one alike. Only
        # build() runs here, never build_upload() — a scenario that ran both could
        # not tell which route moved the counter.
        #
        # The window is the same one the recorder uses (CORNUS_OBS_METRICS_INTERVAL
        # = 2s above sets the in-process bridge's collection interval too), so the
        # point lands within a couple of seconds rather than the SDK's 1m default.
        # no_push: the counter is recorded by the build handler, so a push adds a
        # registry dependency to an assertion that is not about registries. It also
        # keeps this leg working on hosts where the local build engine cannot reach
        # the server's loopback registry from inside its user namespace.
        # builder=True is what makes this a test of the SERVER: the harness's default
        # build() runs the engine inside the CLI process and never contacts the
        # server at all, so it could not move a server-side counter no matter what
        # the handler did. This one opens the buildwire session on
        # /.cornus/v1/build/attach — the route `cornus build` takes against a remote
        # server, and the route that was not recording.
        #
        # no_push: the counter is recorded by the build handler, so a push would add
        # a registry dependency to an assertion that is not about registries.
        build(
            name = "obs-build-counter",
            context = "e2e/scenarios/app-build",
            builder = True,
            no_cache = True,
            no_push = True,
        )

        counted = False
        for _ in range(40):
            resp = http_get(
                base + "/.cornus/v1/obs/metrics?query=cornus_builds&since=10m&step=15s",
                allow_error = True,
            )
            # Before the first collection the NAME does not resolve and the store
            # answers 400 — the expected state while waiting, as in section 4.
            # The outcome label has to be there, not merely the metric name: a
            # series with no attributes would mean the instrument fired from
            # somewhere that did not classify the build.
            if resp.get("status", 0) == 200 and "\"outcome\":\"ok\"" in resp.get("body", ""):
                counted = True
                break
            sleep("2s")
        assert_true(
            counted,
            "a build over /.cornus/v1/build/attach did not move cornus_builds{outcome=ok}; " +
            "either handleBuildAttach ran engine.Solve without recording the counter, or it " +
            "delegated to a containerized builder and the relay path did not record it",
        )
        log("✓ a buildwire build was counted in cornus_builds")

        # --- 6. re-export gets the readings too --------------------------------
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
