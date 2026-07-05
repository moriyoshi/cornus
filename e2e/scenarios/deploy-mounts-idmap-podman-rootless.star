# Id translation for a client-local mount on ROOTLESS podman: the workload must
# see the files as its OWN, not as the overflow uid.
#
# The caller's ids and the workload's are unrelated id spaces. The export lives on
# the caller's machine and carries that machine's uids; the workload reads it
# inside a user namespace mapping a different range. An untranslated id lands
# outside the container's map, so the kernel reports 65534 — the OVERFLOW uid, not
# an owner — and no mode bit rescues it, because a userns root holds
# CAP_DAC_OVERRIDE only over ids inside its own map.
#
# Measured with the translation disabled, on this exact path:
#
#     -rw-r--r-- 1 65534 65534 /data/f.txt
#     touch: /data/w: Permission denied
#
# and with it enabled: owner 0:0, write succeeds. Both halves matter — ownership
# alone could be cosmetic, and the write alone proves little, since the block
# protocol performs writes as the CALLER's process. What makes the write
# meaningful is that the kernel checks permission client-side against the
# ownership the mount reports, so it fails when the reported owner is unmapped.
#
# This uses an :async mount because the writable block proxy is where the mount
# terminates in userspace on the server, and so is the only place attributes can
# be rewritten. The default (uncached) mount is a blind frame pipe with no decode
# point at all. That is why this scenario sets CORNUS_FILE_CACHE.
#
# Needs: the containerized rootless runner (shared propagation on /, see
# start_podman_rootless in e2e/container/entrypoint.sh).

if TARGET != "podman-rootless":
    log("deploy-mounts-idmap-podman-rootless: skipped (rootless-podman-only; needs a remapping runtime)")
else:
    addr = serve(env = {"CORNUS_FILE_CACHE": "1", "CORNUS_FILE_CACHE_DIR": "filecache"})

    d = temp_dir()
    write_file(path = d + "/f.txt", content = "CLIENT-BYTES\n")

    deploy_attach(
        name = "idmapped",
        image = "alpine:3.20",
        command = ["sleep", "600"],
        local_mount = [d + ":/data:async"],
        timeout = "240s",
    )
    wait(name = "idmapped", running = 1, timeout = "240s")

    def run(cmd, timeout = "60s"):
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "idmapped", "sh", "-c", cmd], timeout = timeout)
        return got["output"]

    # The mount is really 9P in the container's namespace, read from its own
    # mountinfo (busybox `stat -f -c %T` answers UNKNOWN for 9p). Without this the
    # rest could pass against a plain directory podman bound in its place.
    fstype = run("awk '$5==\"/data\"{for(i=1;i<=NF;i++) if($i==\"-\"){print $(i+1); exit}}' /proc/self/mountinfo")
    assert_contains(fstype, "9p")

    # Ownership as the WORKLOAD sees it. 65534 here is the failure this scenario
    # exists for, so assert the id rather than merely "not nobody".
    # Bracketed so the match cannot land inside a longer number: busybox `ls -ln`
    # column-pads, and a bare "0 0" would also be a substring of other renderings.
    owner = run("echo \"[$(stat -c %u:%g /data/f.txt)]\"")
    assert_contains(owner, "[0:0]")
    log("✓ the workload sees the mount as its own uid, not the overflow id")

    body = run("cat /data/f.txt")
    assert_contains(body, "CLIENT-BYTES")

    # Writable, which is what the ownership buys: the kernel checks permission
    # client-side against the reported owner, so an unmapped owner fails here.
    rc = run("touch /data/w 2>&1; echo rc=$?")
    assert_contains(rc, "rc=0")
    log("✓ the workload can write through the mount")

    # And the write reached the CALLER's authoritative directory.
    back = read_file(path = d + "/w", default = "MISSING")
    if back == "MISSING":
        fail("the container's write did not reach the caller's directory")
    log("✓ the write propagated back to the caller")
