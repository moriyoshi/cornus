# Client-local bind mounts on the containerd backend via the NEW
# caretaker-sidecar path (CORNUS_CONTAINERD_REMOTE). Mechanically the same
# shared-propagation trick as deploy-mounts-sidecar-docker.star (a companion
# caretaker container binds a host directory "rshared" and performs the
# kernel 9P mount there; the app container binds the SAME directory
# "rslave"), but the companion runs in the HOST's own network namespace
# (no CNI attachment — it only needs outbound reachability to the cornus
# server, which loopback already provides) rather than a Docker bridge.
#
# Unlike the dockerhost path this does NOT achieve true remote-daemon
# reachability: containerd's client dialer is hard-coded to a local unix
# socket, so this backend is unconditionally co-located with the cornus
# server regardless of this flag (see pkg/deploy/containerdhost/mounts_linux.go).
# The point of this scenario is the sidecar MOUNT mechanism itself, which is
# also the substrate future features (e.g. exec-time agent forwarding) would
# reuse — not remote reachability.
#
# Needs: TARGET == "containerd" and a prebuilt cornus-embedding agent image
# (CORNUS_AGENT_IMAGE); self-skips otherwise.

agent_image = getenv("CORNUS_AGENT_IMAGE", "")

if TARGET != "containerd":
    log("deploy-mounts-sidecar-containerd: skipped (containerd-only; exercises the new sidecar mount-relay path)")
elif agent_image == "":
    log("deploy-mounts-sidecar-containerd: skipped (set CORNUS_AGENT_IMAGE to a prebuilt cornus-embedding image, e.g. cornus:e2e)")
else:
    addr = serve(env = {
        "CORNUS_CONTAINERD_REMOTE": "1",
        "CORNUS_AGENT_IMAGE": agent_image,
    })

    local = temp_dir()
    write_file(path = local + "/marker", content = "LIVE-9P-MOUNT-CONTAINERD-SIDECAR")
    log("serving local dir: " + local)

    deploy_attach(
        name = "mnt-sidecar-ctd",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [local + ":/data:ro"],
        timeout = "240s",
    )

    got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "mnt-sidecar-ctd", "cat", "/data/marker"])
    assert_contains(got["output"], "LIVE-9P-MOUNT-CONTAINERD-SIDECAR", "mounted file content read from inside the app container")
    log("✓ live client-local mount visible inside the containerd sidecar-mounted container")

    st = status(name = "mnt-sidecar-ctd")
    assert_eq(st["running"], 1, "Status must report exactly the app instance, not the mount caretaker companion")
    log("✓ mount-caretaker companion filtered out of Status")

    attach_stop(name = "mnt-sidecar-ctd")
    log("torn down")

    # Read-write mount over the sidecar path.
    rwdir = temp_dir()
    deploy_attach(
        name = "mntrw-sidecar-ctd",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [rwdir + ":/data"],
        timeout = "240s",
    )
    exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "mntrw-sidecar-ctd", "sh", "-c", "printf %s WROTE-FROM-CONTAINER > /data/fromcontainer"])
    back = read_file(path = rwdir + "/fromcontainer", default = "MISSING")
    assert_contains(back, "WROTE-FROM-CONTAINER")
    log("✓ container write propagated back to the client's local dir (read-write sidecar mount)")
    attach_stop(name = "mntrw-sidecar-ctd")
