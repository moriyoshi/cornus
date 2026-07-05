# Client-local bind mounts on dockerhost via the NEW caretaker-sidecar path
# (CORNUS_DOCKER_REMOTE), instead of the existing single-host kernel-9p fast
# path deploy-mounts.star already covers for the "docker" target. This proves
# the same shared-propagation mechanism Kubernetes uses (a companion caretaker
# container binds a Docker-managed volume with "rshared" and performs the
# kernel 9P mount there; the app container binds the SAME volume with
# "rslave" and sees the mount appear) works for a plain Docker host — i.e.
# dockerhost now supports the "remote" (server not co-located with the
# daemon) mount-relay topology, gated behind an explicit opt-in.
#
# Needs: TARGET == "docker", a prebuilt cornus-embedding agent image
# (CORNUS_AGENT_IMAGE — e.g. "cornus:e2e", built the same way the kube
# target's caretaker sidecar image is), and privileged Docker (the caretaker
# container runs Privileged: true for its own kernel 9P mount syscall) — self-
# skips otherwise, the same idiom deploy-tunnel.star uses for an external
# prerequisite that is not guaranteed to be present.

agent_image = getenv("CORNUS_AGENT_IMAGE", "")

if TARGET != "docker":
    log("deploy-mounts-sidecar-docker: skipped (docker-only; exercises dockerhost's new sidecar mount-relay path)")
elif agent_image == "":
    log("deploy-mounts-sidecar-docker: skipped (set CORNUS_AGENT_IMAGE to a prebuilt cornus-embedding image, e.g. cornus:e2e)")
else:
    addr = serve(env = {
        "CORNUS_DOCKER_REMOTE": "1",
        "CORNUS_AGENT_IMAGE": agent_image,
    })

    # A local directory with a marker file, served read-only over 9P.
    local = temp_dir()
    write_file(path = local + "/marker", content = "LIVE-9P-MOUNT-DOCKER-SIDECAR")
    log("serving local dir: " + local)

    deploy_attach(
        name = "mnt-sidecar",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [local + ":/data:ro"],
        timeout = "240s",
    )

    # Read the file back from inside the running container via a real `cornus
    # exec` (pod_exec is kube-only) — proves the caretaker's kernel 9P mount
    # propagated (rshared -> rslave) into the app container before it ran.
    got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "mnt-sidecar", "cat", "/data/marker"])
    assert_contains(got["output"], "LIVE-9P-MOUNT-DOCKER-SIDECAR", "mounted file content read from inside the app container")
    log("✓ live client-local mount visible inside the dockerhost sidecar-mounted container")

    # The mount must be realized purely via the propagation bind — never a
    # plain host bind of the (meaningless, pre-rewrite) client-local source —
    # so a stale/absent path on the DAEMON's own host must not matter. Confirm
    # the mount caretaker companion actually exists and is not mistaken for an
    # app instance by Status.
    st = status(name = "mnt-sidecar")
    assert_eq(st["running"], 1, "Status must report exactly the app instance, not the mount caretaker companion")
    caretaker_ids = docker("ps", "-a", "--filter", "label=cornus.app=mnt-sidecar", "--filter", "label=cornus.role=mount-caretaker", "--format", "{{.ID}}")
    assert_true(len(caretaker_ids.strip()) > 0, "expected a mount-caretaker companion container to exist")
    log("✓ mount-caretaker companion present and filtered out of Status")

    # Graceful disconnect must tear the deployment AND its companion down.
    attach_stop(name = "mnt-sidecar")
    remaining = docker("ps", "-a", "--filter", "label=cornus.app=mnt-sidecar", "--format", "{{.ID}}")
    assert_eq(remaining.strip(), "", "app + mount-caretaker companion must both be gone after attach_stop")
    log("✓ torn down: app and mount-caretaker companion both reaped")

    # Read-WRITE mount: the container writes a file, which must appear in the
    # client's local directory (proving the writable confined 9P export end to
    # end over the sidecar path, not just the read-only case above).
    rwdir = temp_dir()
    deploy_attach(
        name = "mntrw-sidecar",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [rwdir + ":/data"],  # no :ro -> read-write
        timeout = "240s",
    )
    exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "mntrw-sidecar", "sh", "-c", "printf %s WROTE-FROM-CONTAINER > /data/fromcontainer"])
    back = read_file(path = rwdir + "/fromcontainer", default = "MISSING")
    assert_contains(back, "WROTE-FROM-CONTAINER")
    log("✓ container write propagated back to the client's local dir (read-write sidecar mount)")
    attach_stop(name = "mntrw-sidecar")
