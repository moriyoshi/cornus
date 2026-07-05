# Async writable mount I/O benchmark (opt-in).
#
# Measures the writable, cache-coherent block-proxy mount (`--local-mount
# SRC:DST:async`, cache=mmap + ServeBlockProxy) on the dockerhost HOST-MOUNT path:
# sequential write throughput, a container-local baseline (to expose the 9P +
# block-proxy overhead), sequential read-back, and small-op (WAL-like) fsync
# latency. All I/O is driven inside the container via `dd conv=fsync` (per-file
# fsync = Tfsync through the proxy — the durable path a database uses); the harness
# times each run with now() and reports throughput/latency via bench_record.
#
# Opt-in on TWO axes: docker target only, and CORNUS_E2E_BENCH must be set. It
# lives under e2e/benchmarks/ (outside the e2e/scenarios/*.star suite glob), runs
# via `make e2e-bench`, and is parse-checked by `make e2e-check`. Set
# CORNUS_E2E_BENCH_JSON=<path> to also collect the numbers as JSONL.

if TARGET != "docker":
    log("bench-mount-write: skipped (docker-only; the dockerhost host-mount block-proxy path)")
elif not getenv("CORNUS_E2E_BENCH", ""):
    log("bench-mount-write: skipped (set CORNUS_E2E_BENCH=1 to run benchmarks)")
else:
    addr = serve(env = {"CORNUS_FILE_CACHE": "1", "CORNUS_FILE_CACHE_DIR": "filecache"})

    rwdir = temp_dir()
    log("benchmarking writable async mount served from: " + rwdir)

    deploy_attach(
        name = "benchdb",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        local_mount = [rwdir + ":/data:async"],  # writable, cache-coherent async
        timeout = "240s",
    )

    def run(cmd, timeout = "180s"):
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "benchdb", "sh", "-c", cmd], timeout = timeout)
        return got["output"]

    # Time a container-side command, returning elapsed wall-clock seconds. The
    # single exec's startup overhead is a fixed cost, negligible against a
    # multi-second bulk transfer, so the number tracks the mount, not exec.
    def timed(cmd, timeout = "180s"):
        t0 = now()
        run(cmd, timeout = timeout)
        return now() - t0

    mb = 64  # payload size for the sequential transfers
    bs = 1048576  # 1 MiB dd block size (explicit bytes for busybox portability)

    # 1. Sequential WRITE throughput over the async mount (fsync'd).
    dt_w = timed("dd if=/dev/zero of=/data/seq bs=%d count=%d conv=fsync 2>/dev/null" % (bs, mb))
    bench_record("mount-seq-write", dt_w, unit = "s", extra = {"MB": mb, "MBps": mb / dt_w})

    # 2. Baseline: the same write to a CONTAINER-LOCAL path (overlay/tmpfs, no 9P),
    #    isolating the 9P + block-proxy overhead as a ratio.
    dt_l = timed("dd if=/dev/zero of=/tmp/seq bs=%d count=%d conv=fsync 2>/dev/null" % (bs, mb))
    bench_record("local-seq-write", dt_l, unit = "s", extra = {"MB": mb, "MBps": mb / dt_l})

    # Report both MB/s so the 9P + block-proxy overhead is visible (Starlark's %
    # format has no precision specifier, so print the raw floats).
    log("sequential write MB/s: async-mount=%s vs container-local=%s" % (mb / dt_w, mb / dt_l))

    # 3. Sequential READ-back throughput (read-after-write; served warm via the
    #    kernel readahead cache in front of the block proxy).
    dt_r = timed("dd if=/data/seq of=/dev/null bs=%d 2>/dev/null" % bs)
    bench_record("mount-seq-read", dt_r, unit = "s", extra = {"MB": mb, "MBps": mb / dt_r})

    # 4. Small-op (WAL-like) fsync latency: N tiny fsync'd writes in a container-side
    #    loop, reported as ops/s and ms/op. This is the write-intensive-DB pattern.
    n = 100
    dt_wal = timed("i=0; while [ $i -lt %d ]; do dd if=/dev/zero of=/data/w bs=4096 count=1 conv=fsync 2>/dev/null; i=$((i+1)); done" % n)
    bench_record("mount-fsync-latency", dt_wal, unit = "s", extra = {"ops": n, "ops_per_s": n / dt_wal, "ms_per_op": 1000.0 * dt_wal / n})

    attach_stop(name = "benchdb")
    log("✓ async mount I/O benchmark complete")
