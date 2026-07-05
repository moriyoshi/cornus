# Client-local bind mounts over the deploy-attach path. A directory on THIS
# machine is streamed to the workload over 9P for the container's lifetime, and
# we read it back from inside the running container to prove the live mount.
#
# kube target: the mount is realized inside the pod by a privileged native-sidecar
# mount-agent (no host-namespace mount), relayed back through the cornus server.
# Other targets: skipped (dockerhost needs root to kernel-9p-mount on the host;
# the sidecar path is the interesting one).

if TARGET != "kube":
    log("deploy-mounts: skipped (kube-only; the sidecar mount path)")
else:
    serve()

    # A local directory with a marker file, served read-only over 9P.
    local = temp_dir()
    write_file(path = local + "/marker", content = "LIVE-9P-MOUNT")
    log("serving local dir: " + local)

    # Deploy the cornus image itself as the app (so its image doubles as the
    # mount-agent image); keep it alive with sleep, mount the local dir at /data.
    deploy_attach(
        name = "mnt",
        image = "cornus:e2e",
        entrypoint = ["sleep"],  # override cornus:e2e's `cornus` ENTRYPOINT
        command = ["3600"],
        local_mount = [local + ":/data:ro"],
        timeout = "240s",
    )

    # Read the file back from inside the running container: proves the sidecar
    # 9P mount is live and propagated into the app container before it ran.
    got = pod_exec(app = "mnt", cmd = "cat /data/marker")
    assert_eq(got, "LIVE-9P-MOUNT", "mounted file content read from inside the pod")
    log("✓ live client-local mount visible inside the pod")

    # The mount-agent sidecar must pin `cornus` as its container command rather
    # than rely on the image ENTRYPOINT: sidecarImageFor falls back to the app
    # image, whose entrypoint is not `cornus`, so `args: [caretaker]` alone would
    # run the app's entrypoint with a stray argument and never mount. Assert the
    # emitted pod spec carries command: [cornus] (regression guard).
    caretaker_cmd = kubectl(
        "-n",
        "cornus-e2e",
        "get",
        "deployment",
        "mnt",
        "-o",
        "jsonpath={.spec.template.spec.initContainers[?(@.name=='cornus-caretaker')].command[0]}",
    )
    assert_eq(caretaker_cmd.strip(), "cornus", "caretaker sidecar must pin `cornus` as command[0], got %r" % caretaker_cmd)
    log("✓ caretaker sidecar pins `cornus` entrypoint (command[0]=cornus)")

    # Graceful disconnect must tear the deployment down.
    attach_stop(name = "mnt")
    log("torn down")

    # Read-WRITE mount: the pod writes a file, which must appear in the client's
    # local directory (proving the writable confined 9P export end to end).
    rwdir = temp_dir()
    deploy_attach(
        name = "mntrw",
        image = "cornus:e2e",
        entrypoint = ["sleep"],  # override cornus:e2e's `cornus` ENTRYPOINT
        command = ["3600"],
        local_mount = [rwdir + ":/data"],  # no :ro -> read-write
        timeout = "240s",
    )
    pod_exec(app = "mntrw", cmd = "printf %s WROTE-FROM-POD > /data/frompod")
    back = read_file(path = rwdir + "/frompod", default = "MISSING")
    assert_contains(back, "WROTE-FROM-POD")
    log("✓ pod write propagated back to the client's local dir (read-write mount)")
    attach_stop(name = "mntrw")

    # TWO client-local mounts on ONE workload: the pod's caretaker carries both
    # over a SINGLE multiplexed connection to the server (Phase 0). Read both back
    # to prove the multiplex — one connection, one stream per mount.
    da = temp_dir()
    db = temp_dir()
    write_file(path = da + "/marker", content = "MOUNT-A")
    write_file(path = db + "/marker", content = "MOUNT-B")
    deploy_attach(
        name = "mnt2",
        image = "cornus:e2e",
        entrypoint = ["sleep"],  # override cornus:e2e's `cornus` ENTRYPOINT
        command = ["3600"],
        local_mount = [da + ":/da:ro", db + ":/db:ro"],
        timeout = "240s",
    )
    assert_eq(pod_exec(app = "mnt2", cmd = "cat /da/marker"), "MOUNT-A", "first mount over the multiplexed caretaker connection")
    assert_eq(pod_exec(app = "mnt2", cmd = "cat /db/marker"), "MOUNT-B", "second mount over the SAME connection")
    log("✓ two client-local mounts multiplexed over one caretaker connection")
    attach_stop(name = "mnt2")
