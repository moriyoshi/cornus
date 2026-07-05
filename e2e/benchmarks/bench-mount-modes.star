# Raw 9P vs the block protocol, measured on the same workload (opt-in).
#
# Cornus has two ways to serve a client-local mount, and until now the choice was
# made by a flag rather than by evidence:
#
#   raw 9P   (proxyPipe)      the server blindly splices 9P frames between the
#                             kernel mount and the caller's export. No decode, so
#                             also no place to rewrite anything.
#   block    (ServeBlockProxy) the server terminates 9P in userspace and speaks the
#                             block protocol to the caller. Decodes, so id
#                             translation and caching are possible here.
#
# The block protocol has a NO-CACHE mode (nil cache -> blockcache.NullStore, files
# never marked cacheable), which is the mode that could replace the pipe without
# provisioning cache storage. This measures that trade: what does terminating 9P
# in userspace cost, when it buys nothing back in caching?
#
# Both mounts are served by ONE server so the comparison is apples to apples: same
# host, same caller, same container image, same dd invocations. The container-local
# baseline isolates how much of each number is the mount at all.
#
# Opt-in on two axes: docker target only, and CORNUS_E2E_BENCH must be set.

if TARGET != "docker":
    log("bench-mount-modes: skipped (docker-only; the dockerhost host-mount path)")
elif not getenv("CORNUS_E2E_BENCH", ""):
    log("bench-mount-modes: skipped (set CORNUS_E2E_BENCH=1 to run benchmarks)")
else:
    # No file cache: :async then selects the block protocol's NO-CACHE mode, which
    # is the variant being compared. A cached run is a separate question.
    addr = serve()

    pipedir = temp_dir()
    blockdir = temp_dir()

    deploy_attach(name = "benchpipe", image = "alpine:3.20", command = ["sleep", "3600"],
                  local_mount = [pipedir + ":/data"], timeout = "240s")
    deploy_attach(name = "benchblock", image = "alpine:3.20", command = ["sleep", "3600"],
                  local_mount = [blockdir + ":/data:async"], timeout = "240s")

    def run(name, cmd, timeout = "300s"):
        got = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, name, "sh", "-c", cmd], timeout = timeout)
        return got["output"]

    def timed(name, cmd, timeout = "300s"):
        t0 = now()
        run(name, cmd, timeout = timeout)
        return now() - t0

    # Every measurement is the BEST of `reps` runs, and every sample is logged.
    #
    # One sample per metric is not enough here, and that is measured rather than
    # assumed: a run of this scenario reported the block mount reading at 0.21x
    # of raw 9P, which no other instrument could reproduce -- an in-process A/B,
    # a real kernel mount under a privileged container, and the very next run of
    # this scenario all put the same build at parity. A single sample on a shared,
    # containerized host is one hiccup away from arguing for a change nothing is
    # wrong with. Best-of is the right summary because the noise here is
    # one-sided: contention can only make a run slower.
    reps = 3

    def timed_best(name, cmd, label, timeout = "300s"):
        best = 0.0
        for i in range(reps):
            dt = timed(name, cmd, timeout = timeout)
            log("  %s sample %d/%d: %s s" % (label, i + 1, reps, dt))
            if best == 0.0 or dt < best:
                best = dt
        return best

    mb = 64
    bs = 1048576
    n = 100

    def measure(name, label):
        # Sequential write, fsync'd (dd conv=fsync issues Tfsync -- the durable path).
        dt_w = timed_best(name, "dd if=/dev/zero of=/data/seq bs=%d count=%d conv=fsync 2>/dev/null" % (bs, mb), label + " seq-write")
        bench_record(label + "-seq-write", dt_w, unit = "s", extra = {"MB": mb, "MBps": mb / dt_w})

        # Sequential read-back.
        dt_r = timed_best(name, "dd if=/data/seq of=/dev/null bs=%d 2>/dev/null" % bs, label + " seq-read")
        bench_record(label + "-seq-read", dt_r, unit = "s", extra = {"MB": mb, "MBps": mb / dt_r})

        # Small-op fsync latency: the write-intensive-DB pattern, where per-op
        # round trips dominate and a protocol's chattiness shows up.
        dt_wal = timed_best(name, "i=0; while [ $i -lt %d ]; do dd if=/dev/zero of=/data/w bs=4096 count=1 conv=fsync 2>/dev/null; i=$((i+1)); done" % n, label + " fsync")
        bench_record(label + "-fsync-latency", dt_wal, unit = "s",
                     extra = {"ops": n, "ops_per_s": n / dt_wal, "ms_per_op": 1000.0 * dt_wal / n})

        log("%s: seq-write %s MB/s | seq-read %s MB/s | fsync %s ms/op" %
            (label, mb / dt_w, mb / dt_r, 1000.0 * dt_wal / n))
        return (mb / dt_w, mb / dt_r, 1000.0 * dt_wal / n)

    pipe_w, pipe_r, pipe_f = measure("benchpipe", "raw9p")
    blk_w, blk_r, blk_f = measure("benchblock", "block-nocache")

    # Container-local baseline: the same writes with no mount involved, so the
    # numbers above can be read as a fraction of what the host can do at all.
    dt_l = timed("benchpipe", "dd if=/dev/zero of=/tmp/seq bs=%d count=%d conv=fsync 2>/dev/null" % (bs, mb))
    bench_record("local-seq-write", dt_l, unit = "s", extra = {"MB": mb, "MBps": mb / dt_l})

    log("== block-nocache relative to raw 9P ==")
    log("seq-write  x%s   (raw9p %s MB/s -> block %s MB/s)" % (blk_w / pipe_w, pipe_w, blk_w))
    log("seq-read   x%s   (raw9p %s MB/s -> block %s MB/s)" % (blk_r / pipe_r, pipe_r, blk_r))
    log("fsync      x%s   (raw9p %s ms/op -> block %s ms/op)" % (pipe_f / blk_f, pipe_f, blk_f))
    log("container-local write %s MB/s (no mount)" % (mb / dt_l))

    attach_stop(name = "benchpipe")
    attach_stop(name = "benchblock")
    log("✓ mount-mode comparison complete")
