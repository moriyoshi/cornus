# Server-restart reattach: a deployed workload must SURVIVE the cornus server
# going away and be re-adopted (and re-supervised) by a fresh server started
# against the same data dir. This is the behavior with no daemon safety net on
# the bare backend — the cornus server IS the restart monitor, so on restart it
# must re-establish supervision (barehost reconcile) rather than lose the
# workload. It is target-agnostic (dockerhost/containerd survive via their own
# daemons; the local target has no backend), which is exactly why running it on
# `bare` proves the daemonless path matches the daemon-backed contract.

if TARGET == "local":
    log("server-restart: skipped (needs a real backend)")
else:
    def wait_running(name, want, steps = 90):
        for _ in range(steps):
            if status(name = name)["running"] == want:
                return
            sleep(duration = "2s")
        fail(msg = "timed out waiting for %s to reach running=%d" % (name, want))

    # HEALTH_BACKENDS run cornus's OWN probe engine (pkg/deploy/healthengine), so a
    # server restart is the only thing standing between a deployed workload and no
    # health at all: the probe loops die with the process and something has to
    # re-arm them from the healthcheck each backend persisted (a container label on
    # containerd, the instance record on bare, the instance config on incus).
    #
    # The health assertions below are deliberately NOT run on dockerhost/podman or
    # kubernetes. There the daemon owns the probe and outlives cornus entirely, so
    # health surviving a cornus restart says nothing about cornus — the assertion
    # would pass on those targets no matter how broken the re-arm was, and a check
    # that cannot fail is worse than no check.
    HEALTH_BACKENDS = ["containerd", "bare", "incus"]
    probes = TARGET in HEALTH_BACKENDS

    def health():
        insts = status(name = "surv")["instances"]
        if len(insts) == 0:
            return "NO-INSTANCES"
        return insts[0]["health"]

    def wait_health(want, steps = 120):
        for _ in range(steps):
            if health() == want:
                return True
            sleep(duration = "1s")
        return False

    addr = serve()

    # A long-lived workload with the default (unless-stopped) restart policy. On the
    # probing backends it also carries a healthcheck; `true` is in every image here
    # and needs no extra tooling, and the 1s interval keeps the waits below short.
    hc = None
    if probes:
        hc = {"test": "CMD,true", "interval": "1s", "timeout": "5s", "retries": "3"}
    deploy(name = "surv", image = "alpine:3.20", command = ["sleep", "3600"], healthcheck = hc)
    wait(name = "surv", running = 1, timeout = "240s")

    # BASELINE. Without this the post-restart check proves nothing: "healthy" after
    # the restart would be indistinguishable from a probe that had never run.
    if probes:
        if not wait_health("healthy"):
            fail(msg = "workload never reported healthy before the restart; got %s" % health())
        log("✓ healthy before the restart")

    # Baseline: the workload is reachable via exec on the first server.
    r0 = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "surv", "sh", "-c", "echo BEFORE_RESTART"])
    assert_contains(r0["output"], "BEFORE_RESTART", "baseline exec failed")
    log("✓ deployed and reachable before the restart")

    # Kill the server. The workload's container keeps running (reparented) — the
    # backend's Close must NOT tear it down.
    stop_server()
    log("• server stopped; workload should keep running unsupervised")

    # A fresh server process over the SAME data dir (the harness reuses CORNUS_DATA
    # across serve() calls, so the bare records + content store persist).
    addr = serve()
    log("restarted the server against the same data dir")

    # 1) SURVIVAL: the workload is still running after the restart.
    st = status(name = "surv")
    assert_eq(st["running"], 1, "workload did not survive the server restart (running=%d)" % st["running"])
    log("✓ workload survived the server restart")

    # 2) ADOPTION: the NEW server re-read the record and can reach the live
    # container — exec works without a redeploy.
    r1 = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "surv", "sh", "-c", "echo AFTER_RESTART"])
    assert_contains(r1["output"], "AFTER_RESTART", "new server could not exec into the reattached workload")
    log("✓ new server re-adopted the workload (exec works)")

    # 2b) HEALTH RE-ARM: the probe loops died with the old process, so the new
    # server has to recover the healthcheck from wherever the deploy persisted it
    # and start probing again. Nothing about the workload changed — it never
    # stopped being healthy — so a failure here is cornus having silently stopped
    # LOOKING, which is invisible until a compose `depends_on: service_healthy`
    # against this workload hangs much later.
    if probes:
        if not wait_health("healthy"):
            fail(msg = "health did not come back after the server restart; got %s — the probe was " % health() +
                       "not re-armed, and the healthcheck was not recovered from where the deploy persisted it")
        log("✓ health came back after the server restart: the new server re-armed the probe")

    # 3) SUPERVISION REATTACH: crash the container's init (kill PID 1). Only a
    # re-established supervisor brings it back — a `sleep 3600` never exits on its
    # own, so if reconcile failed to re-watch, running stays 0 and this times out.
    # (We assert only the stable end state, running=1: the restart backoff is
    # ~100ms, far shorter than the 2s poll, so the transient down state is not
    # reliably observable and is deliberately not asserted. The exec itself dies
    # with the PID namespace when PID 1 exits, so its status is ignored.)
    exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "surv", "sh", "-c", "kill 1"])
    wait_running("surv", 1)
    log("✓ reattached supervisor restarted the crashed workload")

    # 4) FULL MANAGEMENT: the new server can also stop/remove what it adopted.
    stop(name = "surv")
    wait_running("surv", 0)
    remove(name = "surv")
    log("✓ new server fully manages the adopted workload (stop/remove)")
