# Client-local 9P mount on ROOTLESS podman — the co-located host fast path where
# the mount is made by one user and consumed by another.
#
# This is the leg that broke twice, both times silently:
#
#  1. The session directory was created by os.MkdirTemp, which always makes 0700
#     and ignores umask, so podman (uid 1001) could not TRAVERSE to the
#     mountpoint. The deploy failed at create naming the mountpoint — which was
#     0755 and innocent — rather than its parent.
#  2. With that fixed the deploy SUCCEEDED and /data was silently EMPTY: the
#     cornus server (root) makes the kernel 9P mount in its own mount namespace,
#     rootless podman runs containers in another, and without shared propagation
#     the mount never reaches it. Nothing errored; the workload just saw nothing.
#
# Failure 2 is why this asserts the FSTYPE and not only the bytes. A mount that
# did not propagate leaves podman binding the underlying directory, and a
# content-only assertion would still be a real check today but would stop being
# one the moment anything wrote to that directory. Pinning "the container's /data
# is 9p" observes the propagation itself.
#
# Needs: the containerized rootless runner, whose entrypoint sets shared
# propagation on / BEFORE podman unshares its mount namespace — a peer group
# cannot be joined retroactively. See start_podman_rootless in
# e2e/container/entrypoint.sh.

if TARGET != "podman-rootless":
    log("deploy-mounts-local-podman-rootless: skipped (rootless-podman-only; the cross-user host-mount path)")
else:
    addr = serve()

    d = temp_dir()
    write_file(path = d + "/f.txt", content = "CLIENT-BYTES\n")
    log("serving client-local mount from: " + d)

    deploy_attach(
        name = "localmnt",
        image = "alpine:3.20",
        command = ["sleep", "600"],
        local_mount = [d + ":/data"],
        timeout = "240s",
    )
    wait(name = "localmnt", running = 1, timeout = "240s")

    def run(cmd, timeout = "60s"):
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "localmnt", "sh", "-c", cmd], timeout = timeout)
        return got["output"]

    # The mount reached the container's mount namespace. Read the fstype out of the
    # container's OWN mountinfo — the field right after the "-" separator — rather
    # than with `stat -f -c %T`, which is busybox here and answers UNKNOWN for 9p.
    # Extracting just that field matters: the whole mountinfo line also carries the
    # backing socket path (/tmp/cornus-9pback-.../ctx.sock), so asserting "9p"
    # against the raw line would pass on the SOURCE string even if the filesystem
    # were something else.
    fstype = run("awk '$5==\"/data\"{for(i=1;i<=NF;i++) if($i==\"-\"){print $(i+1); exit}}' /proc/self/mountinfo")
    assert_contains(fstype, "9p")
    log("✓ the container's /data is a 9p mount, so the mount propagated across the namespace")

    # And it carries the client's bytes rather than an empty directory.
    body = run("cat /data/f.txt")
    assert_contains(body, "CLIENT-BYTES")
    log("✓ the workload reads the client's bytes through the 9P mount")

    # Ownership as the workload sees it. This used to be 65534 — the OVERFLOW id,
    # because the server owns the export as host root and host uid 0 is not in this
    # container's user-namespace map — and reads worked only because the mode was
    # world-readable. The DEFAULT mount is a frame splice with no 9P termination,
    # so the fix rewrites the ownership in each Rgetattr as it passes
    # (wire.pipeMappingOwner), rather than paying to terminate 9P.
    #
    # Bracketed so the match cannot land inside a longer number.
    owner = run("echo \"[$(stat -c %u:%g /data/f.txt)]\"")
    assert_contains(owner, "[0:0]")
    log("✓ the workload sees the DEFAULT mount as its own uid, not the overflow id")

    # And the ownership is what makes it writable: the kernel checks permission
    # client-side against the owner the mount reports.
    rc = run("touch /data/w 2>&1; echo rc=$?")
    assert_contains(rc, "rc=0")
    log("✓ the workload can write through the default mount")
