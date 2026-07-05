# Restart semantics against a live daemon: the default restart policy
# (unless-stopped) must resurrect a workload whose PID 1 dies, and an explicit
# `stop` must keep it stopped past the restart monitor's reconcile interval.
#
# On docker this is dockerd's own restart policy; on containerd it is the
# runtime/restart monitor plugin driven by the containerd.io/restart labels the
# cornus backend sets; on bare it is cornus's own in-process supervisor
# (pidfd-waits each init and re-runs it per policy, honoring the persisted
# explicitly-stopped flag). Either way the observable contract is identical, so
# the SAME scenario runs on all three. kube is skipped: there the kubelet restarts a
# dead container regardless of cornus, and `stop` scales the Deployment away —
# different machinery, covered by lifecycle.star. local has no backend.
#
# Fresh-start proof: the workload appends a line to a bind-mounted boot log on
# every start, so a second line is direct evidence the monitor started a NEW
# process — no need to catch the sub-second exited window on docker (its
# restart backoff starts at 100ms). PID 1 is a shell with a TERM trap: a
# pid-namespace init only receives signals it has a handler for, so a bare
# `sleep` PID 1 would ignore the kill.

def boots(path):
    return read_file(path = path, default = "").count("boot")

def wait_boots(path, want, steps = 45):
    # containerd's restart monitor reconciles roughly every 10s; 90s is ample.
    for _ in range(steps):
        if boots(path) >= want:
            return
        sleep(duration = "2s")
    fail(msg = "timed out waiting for %d boots (have %d)" % (want, boots(path)))

def wait_running(name, want, steps = 60):
    for _ in range(steps):
        if status(name = name)["running"] == want:
            return
        sleep(duration = "2s")
    fail(msg = "timed out waiting for %s to reach running=%d" % (name, want))

if TARGET != "docker" and TARGET != "containerd" and TARGET != "bare":
    log("lifecycle-restart: skipped (docker/containerd/bare only; restart-monitor semantics)")
else:
    addr = serve()

    data = temp_dir()
    bootlog = data + "/boots"

    # Explicit restart=unless-stopped (also the cornus default) — the policy
    # under test. The TERM trap makes PID 1 killable from inside via exec.
    deploy(
        name = "rst",
        image = "alpine:3.20",
        restart = "unless-stopped",
        mounts = [data + ":/data"],
        command = ["sh", "-c", "echo boot >> /data/boots; trap 'exit 1' TERM; while true; do sleep 1; done"],
    )
    wait(name = "rst", running = 1, timeout = "240s")
    wait_boots(bootlog, 1)
    assert_eq(boots(bootlog), 1, "expected exactly one boot before the kill")
    log("workload up (1 boot recorded)")

    # Kill PID 1 from inside. The exec session dies with the container, so its
    # exit code is not asserted.
    exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "rst", "sh", "-c", "kill 1"])

    # The restart monitor must start a FRESH instance: a second boot line.
    wait_boots(bootlog, 2)
    wait_running("rst", 1)
    st = status(name = "rst")
    assert_eq(st["running"], 1, "resurrected instance not running")
    log("✓ restart monitor resurrected the workload after PID 1 died (2 boots, running again)")

    # Explicit stop must stick: the explicitly-stopped mark keeps the monitor
    # from resurrecting it. Wait out more than one reconcile interval (~10s on
    # containerd) and assert it is still down and was NOT restarted.
    stop(name = "rst")
    wait_running("rst", 0)
    sleep(duration = "15s")
    assert_eq(status(name = "rst")["running"], 0, "explicitly-stopped workload was resurrected by the restart monitor")
    assert_eq(boots(bootlog), 2, "explicitly-stopped workload booted again (restart monitor ignored the stop)")
    log("✓ explicit stop sticks past a restart-monitor interval (still stopped, no new boot)")

    remove(name = "rst")
