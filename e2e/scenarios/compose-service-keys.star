# Prove the Tier-3 runtime service keys actually take effect in the running
# container (not merely that they parse). One service `app` sets user,
# working_dir, hostname, read_only, tmpfs, and a namespaced sysctl; we bring it
# up and read each back over `cornus exec` (docker exec inherits the container's
# user and working dir, so `id -u` / `pwd` reflect the applied keys).
#
# Public image, no build: runs on the docker target without root / a build
# engine. If the container never reaches running with these keys set, that is a
# real implementation bug (see the NOTE in the task) — the wait() below fails
# loud rather than being weakened.

if TARGET == "local":
    log("compose-service-keys: skipped (needs a real backend)")
else:
    compose_file = "e2e/scenarios/compose-service-keys.yaml"

    addr = serve()
    host = {"CORNUS_HOST": "http://" + addr}
    srv = "http://" + addr

    cornus("compose", "-f", compose_file, "up", "-d", env = host)
    wait(name = "csk-app", running = 1, timeout = "120s")
    log("✓ csk-app reached running with all Tier-3 keys set")

    def exec_out(cmd):
        r = exec_tty(argv = ["cornus", "exec", "--server", srv, "csk-app", "sh", "-c", cmd])
        return r["output"]

    # user: docker exec inherits the container's configured user.
    assert_contains(exec_out("id -u"), "65534", "user: uid did not reach the container")
    log("✓ user 65534 applied")

    # working_dir: docker exec inherits WorkingDir.
    assert_contains(exec_out("pwd"), "/tmp", "working_dir did not reach the container")
    log("✓ working_dir /tmp applied")

    # hostname.
    assert_contains(exec_out("hostname"), "csk-host", "hostname did not reach the container")
    log("✓ hostname csk-host applied")

    # read_only: the rootfs mount must carry the ro flag. Reading the mount flags
    # avoids a write test, which would fail on permissions anyway (uid 65534).
    rootmnt = exec_out("grep ' / ' /proc/mounts")
    assert_true(
        (" ro," in rootmnt) or ("ro " in rootmnt),
        "read_only did not make the rootfs read-only (mount line: %r)" % rootmnt,
    )
    log("✓ read_only rootfs applied")

    # tmpfs: /cache must be a tmpfs mount.
    cachemnt = exec_out("grep ' /cache ' /proc/mounts")
    assert_contains(cachemnt, "tmpfs", "tmpfs mount at /cache not present")
    log("✓ tmpfs /cache applied")

    # sysctl: the namespaced net.* param must be set inside the container.
    assert_contains(
        exec_out("cat /proc/sys/net/ipv4/ip_unprivileged_port_start"),
        "2048",
        "sysctl net.ipv4.ip_unprivileged_port_start did not reach the container",
    )
    log("✓ sysctl ip_unprivileged_port_start=2048 applied")

    cornus("compose", "-f", compose_file, "down", env = host)
    assert_eq(status(name = "csk-app")["total"], 0, "csk-app still present after down")
    log("✓ compose service keys take effect end to end")
