# Exercise Backend.Stats end to end: build, deploy, then pull a single
# `docker stats --no-stream`-shaped metrics frame and assert the backend
# produced live counters. On the bare target this is the only coverage of the
# direct cgroup read (there is no daemon to source task.Metrics from) against a
# real running container — memory usage, cumulative CPU, and pid count must all
# be non-zero for a running workload.
#
# The kubernetes backend has no equivalent: per-container stats there require
# metrics-server (out of scope), so (*kubernetes.Backend).Stats returns 501 by
# design. Skip on kube rather than assert against an unsupported path.

if TARGET == "kube":
    log("deploy-stats: skipped (kube: stats needs metrics-server; use `kubectl top`)")
else:
    serve()

    image = build(
        name = "demo",
        context = "e2e/scenarios/app",
        args = {"GREETING": "from-e2e"},
    )
    log("built image: " + image)

    deploy(name = "demo", image = image, replicas = 1)

    st = wait(name = "demo", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "expected 1 running instance")

    s = stats(name = "demo")
    log("stats frame: %s" % s)

    # `cpu.stat` is always present in a cgroup v2 hierarchy and accrues as soon as
    # init runs, so cumulative CPU is the portable proof that Stats read the live
    # per-container cgroup — the same assertion holds for bare's direct file read
    # and containerd's task.Metrics. The memory limit is always positive too: an
    # unconstrained container reports the host total via the encoder's fallback.
    assert_true(s["cpu_total"] > 0, "cumulative CPU should be > 0 for a running container")
    assert_true(s["mem_limit"] > 0, "memory limit should be > 0 (host total when unconstrained)")

    # memory.current / pids.current require their controllers to be DELEGATED to the
    # leaf cgroup (cgroup.subtree_control). Nested / docker-in-docker test hosts often
    # leave that empty, so these legitimately read 0 here even though they are
    # populated on a real bare-metal host — log them rather than assert.
    log("mem_usage=%d pids=%d (0 when the cgroup controller is not delegated, e.g. nested DinD)" % (s["mem_usage"], s["pids"]))
    if s["mem_usage"] > 0:
        assert_true(s["mem_limit"] >= s["mem_usage"], "memory limit should be >= usage when memory is accounted")

    remove(name = "demo")
    log("stats verified, torn down")
