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

    addr = serve()

    # A long-lived workload with the default (unless-stopped) restart policy.
    deploy(name = "surv", image = "alpine:3.20", command = ["sleep", "3600"])
    wait(name = "surv", running = 1, timeout = "240s")

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
