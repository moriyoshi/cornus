# Registry remote cache: export a BuildKit cache to cornus's own registry with
# --cache-to, then prove a second build with an EMPTY local cache reuses it via
# --cache-from. The Dockerfile's RUN cats marker.txt to stdout, so a miss shows
# CACHE-RUN-EXECUTED and a hit does not. (The marker is in a file, not the RUN
# command text, so the vertex NAME drainProgress prints on a cache hit can't
# false-match — see build-cache/Dockerfile.) fresh_cache=True gives each build a
# brand-new engine data dir, so the only cache the second build can hit is the
# registry's. Requires the build engine (root / rootless); --target local is enough.

addr = serve()
ctx = "e2e/scenarios/build-cache"
ref = addr + "/e2e-buildcache/app:cache"
cache_to = "type=registry,mode=max,ref=" + ref + ",registry.insecure=true"
cache_from = "type=registry,ref=" + ref + ",registry.insecure=true"

# Build 1: empty local cache, so the RUN executes (marker present); export the
# resulting cache to the registry.
first = build(
    name = "cacheapp",
    context = ctx,
    fresh_cache = True,
    cache_to = cache_to,
    no_push = True,
    capture = True,
)
assert_contains(first["log"], "CACHE-RUN-EXECUTED")
log("✓ first build ran the RUN step (cache miss, as expected)")

# The cache manifest is now a tag in cornus's registry.
man = http_get(url = "http://" + addr + "/v2/e2e-buildcache/app/manifests/cache")
assert_eq(man["status"], 200, "cache manifest was not exported to the registry")
log("✓ registry cache manifest exported")

# Build 2: ANOTHER empty local cache + --cache-from. The RUN must be served from
# the registry cache, so CACHE-RUN-EXECUTED must NOT appear.
second = build(
    name = "cacheapp",
    context = ctx,
    fresh_cache = True,
    cache_from = cache_from,
    no_push = True,
    capture = True,
)
assert_true(
    "CACHE-RUN-EXECUTED" not in second["log"],
    "RUN re-executed on the second build — registry cache was not imported",
)
log("✓ registry cache imported (RUN served from cache, not re-executed)")
