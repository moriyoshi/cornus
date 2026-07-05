# Writable, cache-coherent async mount over the block protocol.
#
# A client-local dir is mounted read-WRITE into the workload with the `async`
# option (`--local-mount SRC:DST:async`): the container mounts kernel-9p with
# cache=mmap (read-ahead + writeback file cache — writes absorbed by its page
# cache, flushed asynchronously via writeback) and the cornus server terminates
# the mount in the writable block proxy (ServeBlockProxy), keeping a coherent read
# cache in front of the caller's files. This is the path for write-intensive
# workloads (databases).
#
# Flushes use per-file fsync via `dd conv=fsync` (issues Tfsync through the block
# proxy — the durable path a database uses), NOT the global `sync`/syncfs.
#
# Requires the server-side file cache (the block proxy only engages when it is
# configured) and the 9p KERNEL MODULE, plus a privileged, root environment for
# the pod mount-agent — so, like deploy-mounts.star, this is the kube sidecar path.
# Other targets self-skip. Not in the default suite; run explicitly (e.g. the
# containerized kube runner).

if TARGET != "kube":
    log("async-write-9p: skipped (kube-only; the sidecar mount path with cache=mmap)")
else:
    # Boot the server with the per-file block cache enabled — the writable block
    # proxy engages only when a cache is present.
    serve(env = {"CORNUS_FILE_CACHE": "1", "CORNUS_FILE_CACHE_DIR": "filecache"})

    rwdir = temp_dir()
    log("serving writable async mount from: " + rwdir)

    # Deploy the cornus image as the app (doubles as the mount-agent image); keep
    # it alive with sleep, mount the local dir read-write + async at /data.
    deploy_attach(
        name = "asyncdb",
        image = "cornus:e2e",
        entrypoint = ["sleep"],  # override cornus:e2e's `cornus` ENTRYPOINT
        command = ["3600"],
        local_mount = [rwdir + ":/data:async"],  # writable, cache-coherent
        timeout = "240s",
    )

    n = 200

    # Write a WAL-ish sequence from inside the pod and fsync it, so the kernel-9p
    # writeback (cache=mmap) flushes the dirty pages through the block proxy to the
    # caller's authoritative file. `dd conv=fsync` fsyncs the fd before close.
    pod_exec(app = "asyncdb", cmd = "sh -c 'seq 1 %d | sed s/^/line-/ | dd of=/data/wal conv=fsync 2>/dev/null'" % n)

    # Write-through-on-fsync: the lines must reach the client's local dir.
    back = read_file(path = rwdir + "/wal", default = "MISSING")
    assert_contains(back, "line-1")
    assert_contains(back, "line-%d" % n)
    log("✓ async writes propagated through the block proxy to the client's dir")

    # Read-after-write coherence from inside the pod: the block-cache-backed mount
    # must return exactly what was written (all n lines).
    count = pod_exec(app = "asyncdb", cmd = "sh -c 'wc -l < /data/wal'")
    assert_eq(count.strip(), str(n), "pod read-after-write must see all appended lines, got %r" % count)

    # In-place overwrite then read back: exercises the writable proxy's RMW +
    # hash-verified cache update (not just appends). conv=notrunc,fsync overwrites
    # in place and fsyncs.
    pod_exec(app = "asyncdb", cmd = "sh -c 'dd if=/dev/zero of=/data/page bs=1024 count=64 conv=fsync 2>/dev/null'")
    pod_exec(app = "asyncdb", cmd = "sh -c 'printf OVERWRITE | dd of=/data/page bs=1 seek=100 conv=notrunc,fsync 2>/dev/null'")
    patched = pod_exec(app = "asyncdb", cmd = "sh -c 'dd if=/data/page bs=1 skip=100 count=9 2>/dev/null'")
    assert_eq(patched.strip(), "OVERWRITE", "in-place overwrite must read back coherently, got %r" % patched)
    log("✓ in-place overwrite is cache-coherent (RMW self-verify)")

    # Graceful disconnect tears the deployment down.
    attach_stop(name = "asyncdb")
    log("✓ async writable mount torn down")
