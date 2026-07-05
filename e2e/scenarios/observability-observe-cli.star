# `cornus observe`: the query surfaces, against a real store.
#
# The unit tests render each result type from canned data and check the query
# translation against a stub. What is unproven until a live run is that the whole
# chain agrees on the SCHEMA — that what the recorder writes is what the SQL
# tables expose, what `--output json` emits parses, and what `status` reports
# matches what was actually stored. Those are the seams where a rename or a
# column-name drift breaks the feature while every unit test still passes.
#
# Grafana's datasource envelopes are asserted here too, for the same reason: the
# shaping is unit-tested, but only a live server proves the routes are registered
# and answer through the real middleware chain.
#
# Source of truth: cmd/cornus/observe.go, pkg/server/obsquery.go,
# pkg/server/obsgrafana.go.

if TARGET == "local":
    log("skip observability-observe-cli: needs a running server + deploy backend")
else:
    srv = serve(env = {"CORNUS_OBS": "1"})
    base = "http://" + srv

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("skip observability-observe-cli: this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        marker = "cornus-observe-cli-marker"
        deploy(
            name = "observe-app",
            image = "busybox:latest",
            restart = "no",
            command = ["sh", "-c", "echo %s-one; echo %s-two; sleep 60" % (marker, marker)],
        )
        wait(name = "observe-app", timeout = "60s")

        # Wait for the recorder to flush before asserting on any read surface.
        seen = ""
        for _ in range(30):
            got = cornus("observe", "logs", "--server", base, "--service", "observe-app", "--limit", "50")
            if marker in got:
                seen = got
                break
            sleep("1s")
        assert_contains(seen, marker + "-one", "the recorder never stored the workload's output")
        log("✓ observe logs returns the recorded output")

        # --- json output, which is what a script consumes ----------------------
        raw = cornus("observe", "logs", "--server", base, "--service", "observe-app", "--output", "json")
        assert_true(raw.strip().startswith("["), "observe logs --output json is not a JSON array: %r" % raw[:120])
        assert_contains(raw, "\"body\"", "the JSON records carry no body field")
        assert_contains(raw, "\"service\"", "the JSON records carry no service field")
        log("✓ observe logs --output json emits parseable records")

        # --- SQL: the schema the recorder writes is the schema SQL exposes -----
        rows = cornus("observe", "query", "--server", base,
                      "SELECT service, count(*) AS n FROM logs GROUP BY service")
        assert_contains(rows, "observe-app", "SQL over the logs table does not see the recorded service")
        log("✓ observe query reaches the logs table over SQL")

        # A deliberately broken query must come back as a diagnostic, not an empty
        # result — an empty result would read as "no rows matched".
        bad_sql = cornus("observe", "query", "--server", base, "SELECT * FROM no_such_table", expect_fail = True)
        assert_true(len(bad_sql) > 0, "a bad SQL query produced no diagnostic")
        log("✓ a bad SQL query is reported rather than returning nothing")

        # --- status: what is held, and whether anything was shed ---------------
        status = cornus("observe", "status", "--server", base)
        assert_contains(status, "logs", "observe status does not report the logs table")
        assert_contains(status, "dropped", "observe status omits the dropped counter")
        log("✓ observe status reports contents and the dropped counter")

        # --- traces: empty, but the surface must ANSWER -------------------------
        # Nothing here exports spans (that needs an instrumented app), so the
        # assertion is that the command works and reports emptiness clearly rather
        # than erroring — an error would be indistinguishable, to a user, from
        # "the feature is broken".
        traces = cornus("observe", "traces", "--server", base)
        assert_contains(traces, "no matching traces", "observe traces did not report an empty result clearly")
        log("✓ observe traces answers cleanly with nothing recorded")

        # --- metrics: an unknown metric is a DIAGNOSTIC, not an empty series ----
        # The engine resolves metric names against the stored catalog and rejects
        # what it cannot resolve, rather than approximating. That is the property
        # worth pinning: a silently empty series would read as "the value is zero"
        # and send someone debugging their application instead of their query.
        unknown = cornus("observe", "metrics", "--server", base, "up", expect_fail = True)
        assert_contains(unknown, "not resolved", "an unknown metric did not produce a resolution diagnostic")
        log("✓ an unresolvable PromQL metric is rejected with a diagnostic, not an empty series")

        # An unknown trace id must be an empty trace, not a crash.
        one = cornus("observe", "trace", "--server", base, "0123456789abcdef0123456789abcdef")
        assert_contains(one, "no recorded spans", "observe trace on an unknown id did not say so")
        log("✓ observe trace reports an unknown trace clearly")

        # --- Grafana datasource envelopes --------------------------------------
        loki = http_get(base + '/.cornus/v1/obs/loki/api/v1/query_range?query={service="observe-app"}', allow_error = True)
        assert_eq(loki.get("status", 0), 200, "the Loki-compatible endpoint did not answer: %r" % loki)
        assert_contains(loki["body"], "streams", "the Loki response is not in the streams envelope Grafana expects")

        prom = http_get(base + "/.cornus/v1/obs/prom/api/v1/query_range?query=up&start=0&end=1&step=1", allow_error = True)
        assert_true(
            prom.get("status", 0) in (200, 400),
            "the Prometheus-compatible endpoint neither answered nor reported a query error: %r" % prom,
        )
        assert_contains(prom["body"], "status", "the Prometheus response carries no status envelope")

        tempo = http_get(base + "/.cornus/v1/obs/tempo/api/traces/0123456789abcdef0123456789abcdef", allow_error = True)
        assert_eq(
            tempo.get("status", 0),
            404,
            "Tempo must answer 404 for an unknown trace so Grafana renders 'not found': %r" % tempo,
        )
        log("✓ the Prometheus / Loki / Tempo datasource routes answer in their expected envelopes")

        remove(name = "observe-app")
        log("observability-observe-cli: all assertions passed")
