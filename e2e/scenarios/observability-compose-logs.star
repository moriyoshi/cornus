# `compose logs --from`: the same command, two sources.
#
# The whole UX bet of the built-in store is that reading logs did NOT become a
# second command — `compose logs` kept its meaning and gained history. That bet is
# only worth anything if the two sources are genuinely interchangeable in the
# cases where both can answer, and if the refusals are legible in the cases where
# only one can. Neither property is checkable from unit tests against a fake: they
# are about a real runtime stream and a real recorded copy agreeing.
#
# The assertion that matters most is the one that cannot be faked: after
# `compose down`, `--from=store` still answers.
#
# Source of truth: cmd/cornus/internal/composecli/logs.go (the --from dispatch),
# logs_store.go (the store read + rendering).

if TARGET == "local":
    log("skip observability-compose-logs: needs a running server + deploy backend")
else:
    srv = serve(env = {"CORNUS_OBS": "1"})
    base = "http://" + srv
    host_env = {"CORNUS_HOST": base}

    def logs_from(compose_file, project, source):
        """Runs `cornus compose logs --from <source>`."""
        return cornus("compose", "-f", compose_file, "-p", project, "logs", "--from", source, env = host_env)

    def logs_matching(compose_file, project, text):
        """Runs `cornus compose logs --match <text>` (which implies --from=store)."""
        return cornus("compose", "-f", compose_file, "-p", project, "logs", "--match", text, env = host_env)

    def logs_from_until(compose_file, project, source, marker):
        """Polls `compose logs --from <source>` until marker appears.

        The recorder batches before flushing, so a store read taken the instant
        the container prints would flake; polling keeps a slow machine honest
        without making a fast one wait.
        """
        out = ""
        for _ in range(30):
            out = logs_from(compose_file, project, source)
            if marker in out:
                return out
            sleep("1s")
        return out

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("skip observability-compose-logs: this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        marker = "cornus-compose-logs-marker"
        work = temp_dir()
        compose_file = work + "/compose.yaml"
        write_file(
            path = compose_file,
            content = """services:
  web:
    image: busybox:latest
    command: ["sh", "-c", "echo %s-hello; echo %s-boom 1>&2; sleep 60"]
""" % (marker, marker),
        )

        compose_up(file = compose_file, project = "obslogs", detach = True)

        # --- 1. runtime and store agree while the container is alive -----------
        runtime_out = logs_from_until(compose_file, "obslogs", "runtime", marker)
        assert_contains(runtime_out, marker + "-hello", "the live runtime stream lost the workload's stdout")
        log("✓ --from=runtime reads the live container output")

        store_out = logs_from_until(compose_file, "obslogs", "store", marker)
        assert_contains(store_out, marker + "-hello", "the store did not record the workload's stdout")
        assert_contains(store_out, marker + "-boom", "the store did not record the workload's stderr")
        log("✓ --from=store reads the same output back out of the recorder")

        # --- 2. searching, which only the store can do -------------------------
        matched = logs_matching(compose_file, "obslogs", marker + "-boom")
        assert_contains(matched, marker + "-boom", "--match did not find the recorded line")
        assert_true(
            (marker + "-hello") not in matched,
            "--match returned a non-matching line, so the filter is not applied:\n%s" % matched,
        )
        log("✓ --match searches recorded logs and excludes non-matches")

        # --- 3. the contradictions are refused, not silently resolved ----------
        follow_err = cornus(
            "compose", "-f", compose_file, "-p", "obslogs", "logs", "--follow", "--from", "store",
            env = host_env,
            expect_fail = True,
        )
        assert_contains(follow_err, "--follow", "refusing --follow with --from=store did not explain why")

        runtime_err = cornus(
            "compose", "-f", compose_file, "-p", "obslogs", "logs", "--match", "x", "--from", "runtime",
            env = host_env,
            expect_fail = True,
        )
        assert_contains(runtime_err, "--from=runtime", "refusing --match with --from=runtime did not explain why")
        log("✓ --follow+store and --match+runtime are refused with an explanation")

        # --- 4. THE point: the record outlives the container --------------------
        compose_down(file = compose_file, project = "obslogs")

        survived = logs_from(compose_file, "obslogs", "store")
        assert_contains(
            survived,
            marker + "-hello",
            "the workload's output vanished with its container — the whole reason --from=store exists",
        )
        log("✓ --from=store still answers after compose down")

        log("observability-compose-logs: all assertions passed")
