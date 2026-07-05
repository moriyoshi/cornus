# Host-reboot survival on the bare (daemonless) backend — the second behavior
# with no daemon safety net. A real reboot wipes the /run tmpfs (the runc state
# root and the pinned network namespaces) and every mount, while the persistent
# truth (instance record, image snapshot chain, bundle config.json, CNI subnet
# allocation) survives on disk. cornus IS the restart monitor here, so on the next
# server start its reconcile must REBUILD the ephemeral pieces — re-mount the
# rootfs off the surviving snapshot, rebuild the netns + CNI attachment + pin,
# repoint the bundle spec — and relaunch the workload, WITHOUT any API request.
#
# Bare-specific: the reboot is simulated by clearing exactly what a boot clears
# (runc state + netns pins under /run/cornus, plus every cornus rootfs mount), so
# this drives the real recovery path (barehost reboot_linux.go) end to end. Other
# targets resurrect via their own daemon and have no such path, so they skip.

if TARGET != "bare":
    log("reboot-survival: skipped (bare-specific: simulates a host reboot via runc + the /run tmpfs)")
else:
    def wait_running(name, want, steps = 90):
        for _ in range(steps):
            if status(name = name)["running"] == want:
                return
            sleep(duration = "2s")
        fail(msg = "timed out waiting for %s to reach running=%d" % (name, want))

    addr = serve()

    # A long-lived workload that re-creates a marker in its rootfs each start.
    deploy(
        name = "rboot",
        image = "alpine:3.20",
        command = ["sh", "-c", "echo BOOTED > /tmp/marker; sleep 3600"],
    )
    wait(name = "rboot", running = 1, timeout = "240s")
    r0 = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "rboot", "cat", "/tmp/marker"])
    assert_contains(r0["output"], "BOOTED", "baseline exec failed before the reboot")
    log("✓ deployed and running before the reboot")

    # Kill the server, then SIMULATE A HOST REBOOT: kill the (reparented) container
    # inits, unmount every cornus rootfs, unmount + remove the nsfs netns pins, and
    # clear the runc state root — exactly what a boot would leave, minus the
    # persistent on-disk state. (netns pins are bind mounts, so they must be
    # unmounted before removal; a plain rm cannot clear an active mountpoint.)
    stop_server()
    reboot = """
set -u
RUNC="runc --root /run/cornus/bare-runc"
# A real reboot kills every process, including any detached supervision shims
# (CORNUS_BARE_SHIM); a no-op when the in-process supervisor is in use.
pkill -9 -f 'bare-shim' 2>/dev/null || true
for c in $($RUNC list -q 2>/dev/null); do $RUNC kill "$c" KILL 2>/dev/null; done
sleep 1
for m in $(awk '$2 ~ "bare/bundles" {print $2}' /proc/mounts | sort -r); do umount -l "$m" 2>/dev/null; done
for n in /run/cornus/netns/*; do [ -e "$n" ] && { umount "$n" 2>/dev/null || umount -l "$n" 2>/dev/null; }; done
rm -rf /run/cornus/bare-runc/* /run/cornus/netns/* 2>/dev/null
echo "reboot simulated; runc list: [$($RUNC list -q 2>/dev/null)]"
"""
    res = sh(cmd = reboot)
    log("• " + res["output"])

    # A fresh cornus server. Its startup reconcile (bare owns supervision) rebuilds
    # the rootfs mount + netns/CNI pin and relaunches the workload with NO API
    # request — the whole point of eager startup recovery.
    addr = serve()
    wait_running("rboot", 1)
    log("✓ workload recovered after the simulated host reboot")

    # Reachable again with a rebuilt rootfs + netns: the restarted command re-created
    # the marker.
    r1 = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "rboot", "cat", "/tmp/marker"])
    assert_contains(r1["output"], "BOOTED", "recovered container not reachable / rootfs not rebuilt")
    log("✓ recovered container reachable with a rebuilt rootfs + netns")

    remove(name = "rboot")
