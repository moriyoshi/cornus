# Writable, cache-coherent async mount over the block protocol — dockerhost
# HOST-MOUNT path (no pod sidecar, no cross-network relay). This isolates the
# real kernel-9p cache=mmap mount + block proxy from the kube sidecar/relay path:
# the cornus server itself kernel-9p-mounts the caller's dir (cache=mmap, async
# writeback) in its own mount namespace and terminates it in ServeBlockProxy; the
# caller is the `cornus deploy` CLI over the same connection.
#
# Flushes use per-file fsync via `dd conv=fsync` (issues Tfsync through the block
# proxy — the path a database uses), NOT the global `sync`/syncfs: cache=mmap is
# the documented writeback mode (9p.rst: "read-ahead + writeback file cache");
# cache=loose is for read-only/exclusive mounts and wedges syncfs in D state.
#
# Needs: TARGET == "docker", the 9p kernel module, and a privileged/root
# environment for the server-side kernel mount (the containerized runner). Not in
# the default suite; run explicitly.

if TARGET != "docker":
    log("async-write-docker: skipped (docker-only; the dockerhost host-mount block-proxy path)")
else:
    addr = serve(env = {"CORNUS_FILE_CACHE": "1", "CORNUS_FILE_CACHE_DIR": "filecache"})

    rwdir = temp_dir()
    log("serving writable async mount from: " + rwdir)

    deploy_attach(
        name = "asyncdb",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [rwdir + ":/data:async"],  # writable, cache-coherent async
        timeout = "240s",
    )

    def run(cmd, timeout = "60s"):
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "asyncdb", "sh", "-c", cmd], timeout = timeout)
        return got["output"]

    n = 200

    # Write n WAL lines and fsync them, forcing the cache=mmap writeback through the
    # block proxy to the caller's authoritative file. `dd conv=fsync` fsyncs the fd
    # before close (Tfsync), the durable path a DB uses — no global syncfs.
    run("seq 1 %d | sed 's/^/line-/' | dd of=/data/wal conv=fsync 2>/dev/null" % n)

    # Write-through-on-fsync: the data must reach the client's local dir.
    back = read_file(path = rwdir + "/wal", default = "MISSING")
    assert_contains(back, "line-1")
    assert_contains(back, "line-%d" % n)
    log("✓ async writes propagated through the block proxy to the client's dir")

    # Read-after-write coherence from inside the container. exec_tty runs under a
    # pty, so the captured output carries terminal-query escapes (OSC 11 / DSR)
    # around the real bytes; assert on containment rather than exact equality.
    count = run("wc -l < /data/wal")
    assert_contains(count, str(n))
    log("✓ read-after-write coherent (container sees all %d lines)" % n)

    # In-place overwrite: exercises the RMW + hash-verified update. Verify it via
    # the CLIENT-side authoritative file (read_file) rather than a container
    # byte-at-a-time read-back: dd bs=1 emits one write per byte, and exec_tty's pty
    # capture frames each write, so "OVERWRITE" is not a contiguous substring there.
    # read_file is clean and still proves the RMW overwrite wrote through on fsync.
    run("dd if=/dev/zero of=/data/page bs=1024 count=64 conv=fsync 2>/dev/null")
    run("printf OVERWRITE | dd of=/data/page bs=1 seek=100 conv=notrunc,fsync 2>/dev/null")
    page = read_file(path = rwdir + "/page", default = "MISSING")
    assert_contains(page, "OVERWRITE")
    log("✓ in-place overwrite is cache-coherent (RMW self-verify)")

    attach_stop(name = "asyncdb")
    log("✓ async writable mount (dockerhost host-mount path) torn down")
