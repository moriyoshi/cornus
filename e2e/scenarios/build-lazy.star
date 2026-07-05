# Lazy build contexts: a 16 MiB named build context whose big file the build
# never reads. With --lazy the context is served on demand, so a build that only
# touches small.txt must still succeed without eagerly materialising the 16 MiB.
# This exercises the DEFAULT host-bind lazy path (the production default; no 9p
# kernel module required), asserting functional correctness. The byte-level
# measurement (only-the-touched-bytes-cross-the-wire) needs the CORNUS_LAZY_9P
# kernel-9p mount and is covered, local + remote, by build-lazy-9p.star.
# Requires the build engine (root / rootless); --target local is enough.

serve()

# A named-context dir with a big untouched file + the small file the build reads.
datadir = temp_dir()
r = sh(cmd = "truncate -s 16M " + datadir + "/big.bin")
assert_eq(r["code"], 0, "make the 16 MiB sparse file")
write_file(path = datadir + "/small.txt", content = "SMALL")

res = build(
    name = "lazyapp",
    context = "e2e/scenarios/build-lazy",
    build_context = {"data": datadir},
    lazy = True,
    no_cache = True,
    no_push = True,
    capture = True,
)
assert_contains(res["log"], "LAZY-COPY-OK")
log("✓ host-bind lazy build succeeded without materialising the 16 MiB file")
