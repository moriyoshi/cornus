# Client-local bind mount whose HOST SOURCE DOES NOT EXIST YET. Docker (and
# Compose, `bind.create_host_path` default true) auto-creates a missing bind
# source before mounting it; cornus matches that — the client creates the source
# as an empty directory, then serves it over 9P for the container's lifetime.
#
# Regression for the "kernel-9p mount ...: connection reset by peer" failure a
# `cornus compose up` hit when a service bound a nonexistent host path (e.g.
# `~/.aws/`): the client used to hand the absent path to the 9P export, whose
# confined attacher cannot EvalSymlinks a path that isn't there, so it closed the
# stream and the server-side mount(2) surfaced an opaque ECONNRESET.
#
# The fix is client-side (pkg/client resolveLocalMounts, unit-tested by
# TestResolveLocalMountsCreatesMissingBindSource) and therefore backend-agnostic;
# the `bind.create_host_path: false` opt-out is covered by the compose parsing
# unit test (TestVolumeCreateHostPath) + TestResolveLocalMountsNoCreateHostPath.
# This scenario proves the positive path end to end over the real 9P mount.
#
# kube target: the mount is realized inside the pod by the privileged native 9P
# caretaker sidecar, relayed back through the cornus server. Other targets are
# skipped for the same reason deploy-mounts.star is kube-only (dockerhost needs a
# root server with the 9p kernel module).

if TARGET != "kube":
    log("deploy-mounts-create-host-path: skipped (kube-only; the sidecar 9P mount path)")
else:
    serve()

    # A path several levels below a fresh temp dir that DOES NOT exist yet. The
    # client must create it before serving, or the 9P mount fails.
    base = temp_dir()
    missing = base + "/created/by/cornus"
    r = sh(cmd = "test -e '%s' && echo present || echo absent" % missing)
    assert_contains(r["output"], "absent", "precondition: the bind source must not exist yet")
    log("bind source absent before mount: " + missing)

    # Read-WRITE mount (no :ro): the workload writes into the auto-created dir and
    # the write must land back in the client's local directory, proving BOTH the
    # auto-create and a live confined 9P mount of the freshly made directory.
    deploy_attach(
        name = "mkhp",
        image = "cornus:e2e",
        entrypoint = ["sleep"],  # override cornus:e2e's `cornus` ENTRYPOINT
        command = ["3600"],
        local_mount = [missing + ":/data"],  # missing source, read-write
        timeout = "240s",
    )
    log("✓ workload came up — the missing bind source was auto-created and mounted")

    # The client must have created the source as a directory (Docker create_host_path
    # parity), not left it absent.
    r = sh(cmd = "test -d '%s' && echo isdir || echo notdir" % missing)
    assert_contains(r["output"], "isdir", "missing bind source was not auto-created as a directory")
    log("✓ client auto-created the missing bind source as a directory")

    # Pod write -> client's (auto-created) dir: end-to-end proof the mount is live.
    pod_exec(app = "mkhp", cmd = "printf %s CREATE-HOST-PATH-OK > /data/frompod")
    back = read_file(path = missing + "/frompod", default = "MISSING")
    assert_contains(back, "CREATE-HOST-PATH-OK")
    log("✓ pod write propagated back through the freshly auto-created 9P mount")

    attach_stop(name = "mkhp")
    log("torn down")
