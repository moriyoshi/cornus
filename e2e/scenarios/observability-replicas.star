# Multi-replica logging: every instance is recorded, not just the first.
#
# `Backend.Logs` historically streamed a deployment's FIRST instance only — an
# interface shortcut inherited from `docker logs <name>` semantics, where one name
# means one container. That was a visible partial view for the interactive command
# but a silent hole in the RECORDER, which builds the durable record: a scaled
# workload's store would quietly hold a fraction of what the service printed,
# while the whole premise is that the store holds everything the live tail shows
# and more.
#
# Only a live multi-replica deploy can prove the fix. Every unit test here runs
# against a fake `logSource` that answers whatever index it is asked for — so a
# backend that ignored `LogOptions.Instance` and returned replica 0 three times
# would pass every one of them, and produce triplicated records in production.
# This scenario makes each replica print something only IT prints.
#
# Source of truth: api.LogOptions.Instance, each backend's instanceAt/podAt,
# pkg/server/logrecorder.go (one tail per replica, per-replica resume watermark).

if TARGET == "local":
    log("skip observability-replicas: needs a running server + deploy backend")
else:
    srv = serve(env = {"CORNUS_OBS": "1"})
    base = "http://" + srv

    probe = http_get(base + "/.cornus/v1/obs/status", allow_error = True)
    if probe.get("status", 0) != 200:
        log("skip observability-replicas: this cornus build has no observability store (needs -tags imbh); probe returned %r" % probe)
    else:
        marker = "cornus-replica-marker"

        # Each replica prints its own hostname, which the container runtime sets to
        # something instance-specific. That is what makes the assertion real: if the
        # recorder tailed replica 0 three times, all three records would carry the
        # SAME hostname and the distinct-value count would be 1.
        deploy(
            name = "replica-app",
            image = "busybox:latest",
            replicas = 3,
            command = ["sh", "-c", "echo %s-$(hostname); sleep 120" % marker],
        )
        wait(name = "replica-app", timeout = "90s")

        # Poll until all three distinct lines are recorded.
        bodies = []
        for _ in range(40):
            rows = cornus("observe", "query", "--server", base,
                          "SELECT body FROM logs WHERE service = 'replica-app'")
            bodies = [l for l in rows.split("\n") if marker in l]
            if len(bodies) >= 3:
                break
            sleep("1s")

        assert_true(
            len(bodies) >= 3,
            "recorded %d marker lines, want one per replica (3); the recorder is not tailing every instance:\n%s" % (len(bodies), bodies),
        )

        # The distinctness check is the one that catches an ignored Instance option.
        # Compare the HOSTNAME each replica printed, not the whole line: a line
        # carries a timestamp, and a workload that restarts (or a tail that
        # re-attaches, since delivery is at-least-once) legitimately produces the
        # same hostname twice.
        def hostnames(lines):
            out = {}
            for l in lines:
                _, _, rest = l.partition(marker + "-")
                if rest:
                    out[rest.split(" ")[0].strip()] = True
            return out

        distinct = hostnames(bodies)
        assert_true(
            len(distinct) >= 3,
            "the %d recorded lines are not distinct, so the same instance was tailed more than once: %r" % (len(bodies), distinct.keys()),
        )
        log("✓ all three replicas were recorded, each with its own output")

        # --- the replica ordinal is stamped and filterable ---------------------
        replicas = cornus("observe", "query", "--server", base,
                          "SELECT DISTINCT \"cornus.replica\" AS r FROM logs WHERE service = 'replica-app'")
        for want in ["0", "1", "2"]:
            assert_contains(replicas, want, "no records carry replica ordinal %s:\n%s" % (want, replicas))
        log("✓ every record carries its replica ordinal")

        one = cornus("observe", "logs", "--server", base, "--service", "replica-app", "--replica", "1")
        one_lines = [l for l in one.split("\n") if marker in l]
        assert_true(len(one_lines) > 0, "--replica 1 returned nothing")
        one_distinct = hostnames(one_lines)
        assert_eq(
            len(one_distinct),
            1,
            "--replica 1 returned lines from more than one instance: %r" % one_distinct.keys(),
        )
        # And it must be a real subset: filtering to one replica has to exclude the
        # others, not just return everything.
        assert_true(
            len(distinct) > len(one_distinct),
            "--replica 1 returned every replica's output, so the filter is not applied",
        )
        log("✓ observe logs --replica filters to a single instance")

        remove(name = "replica-app")

        # --- compose logs --all-replicas fans the LIVE runtime in --------------
        # The same backend selector, reached through the interactive command
        # rather than the recorder. Asserting only that the flag exists would
        # prove nothing; this checks that it actually produces output from more
        # than one instance, which is what an ignored LogOptions.Instance would
        # silently fail to do.
        work = temp_dir()
        compose_file = work + "/compose.yaml"
        write_file(
            path = compose_file,
            content = """services:
  fan:
    image: busybox:latest
    deploy:
      replicas: 3
    command: ["sh", "-c", "echo %s-$(hostname); sleep 120"]
""" % marker,
        )
        compose_up(file = compose_file, project = "obsreplicas", detach = True)

        fanned = {}
        for _ in range(30):
            out = cornus("compose", "-f", compose_file, "-p", "obsreplicas", "logs",
                         "--from", "runtime", "--all-replicas", env = {"CORNUS_HOST": base})
            fanned = hostnames([l for l in out.split("\n") if marker in l])
            if len(fanned) >= 3:
                break
            sleep("1s")
        assert_true(
            len(fanned) >= 3,
            "compose logs --all-replicas showed %d distinct instances, want 3: %r" % (len(fanned), fanned.keys()),
        )
        log("✓ compose logs --all-replicas streams every instance of a scaled service")

        # Without the flag it stays on one instance, so the default is unchanged.
        single = cornus("compose", "-f", compose_file, "-p", "obsreplicas", "logs",
                        "--from", "runtime", env = {"CORNUS_HOST": base})
        single_hosts = hostnames([l for l in single.split("\n") if marker in l])
        assert_eq(
            len(single_hosts),
            1,
            "compose logs without --all-replicas showed %d instances; the default must be unchanged" % len(single_hosts),
        )
        log("✓ without --all-replicas the default single-instance behavior is unchanged")

        compose_down(file = compose_file, project = "obsreplicas")
        log("observability-replicas: all assertions passed")
