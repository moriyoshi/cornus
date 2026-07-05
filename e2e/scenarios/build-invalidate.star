# Incremental cache correctness on the SAME build engine: touching a context file
# a RUN step consumes must INVALIDATE that RUN (its marker reappears), while an
# unchanged rebuild must REUSE it (marker absent). This complements
# build-cache.star, which only proves registry cache IMPORT across FRESH engines
# and never same-engine invalidation on a file change. The Dockerfile's RUN cats
# marker.txt to stdout, so CACHE-RUN-EXECUTED shows on a miss (step executes) and
# is absent on a hit (step served from cache). The marker lives in a file rather
# than the RUN command text because drainProgress prints the vertex NAME on a
# local cache hit too — see the Dockerfile comment. Every build here shares one
# local engine data dir (no fresh_cache), so the cache persists across the three
# invocations.
# Requires the build engine (root / rootless); --target local is enough.

serve()
ctx = "e2e/scenarios/build-invalidate"

# A writable named-context dir holding the file the RUN consumes. It lives in a
# temp dir so we can MUTATE it between builds without touching the committed tree.
data = temp_dir()
dep = data + "/dep.txt"

write_file(path = dep, content = "v1")

# Build 1: cold cache, so the RUN executes (marker present).
b1 = build(
    name = "invalidateapp",
    context = ctx,
    build_context = {"data": data},
    no_push = True,
    capture = True,
)
assert_contains(b1["log"], "CACHE-RUN-EXECUTED")
log("✓ build 1 ran the RUN step (cold cache miss, as expected)")

# Build 2: the context is UNCHANGED, so the RUN must be served from cache and
# CACHE-RUN-EXECUTED must NOT appear.
b2 = build(
    name = "invalidateapp",
    context = ctx,
    build_context = {"data": data},
    no_push = True,
    capture = True,
)
assert_true(
    "CACHE-RUN-EXECUTED" not in b2["log"],
    "RUN re-executed on an unchanged rebuild — the local cache was not reused",
)
log("✓ build 2 reused the cache (RUN served from cache, not re-executed)")

# Modify the file the RUN depends on. Its COPY layer's content hash changes, which
# must invalidate the downstream RUN — the marker is present again.
write_file(path = dep, content = "v2-changed")
b3 = build(
    name = "invalidateapp",
    context = ctx,
    build_context = {"data": data},
    no_push = True,
    capture = True,
)
assert_contains(b3["log"], "CACHE-RUN-EXECUTED")
log("✓ build 3 re-ran the RUN after the context file changed (cache correctly invalidated)")
