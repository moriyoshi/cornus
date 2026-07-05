# Build FAILURE paths. The happy build scenarios (build-edge, build-mounts, ...)
# prove green builds; this proves the engine and the build HTTP surface reject and
# report bad builds end to end. Three layers:
#   A. the client `cornus build` path (local in-process engine AND remote
#      9P/build-attach) must FAIL on a bad build — build(expect_fail=True).
#   B. the POST /.cornus/v1/build streaming contract: a build streams after HTTP 200, so a
#      failure arrives in-band as a "BUILD FAILED:" trailer, not a status code
#      (pkg/server/build.go:133). Driven via build_upload(), which returns the log.
#   C. pre-stream request validation on POST /.cornus/v1/build: 400 for a missing target
#      and 400 for a body that is not a valid tar.
# Requires the build engine (root / rootless stack); runs on any build-capable
# target (like build-edge).

addr = serve()
base = "http://" + addr

# --- A. the client build path fails on a bad build (local + remote) -----------
# fails() runs one bad context through BOTH the local in-process engine and the
# remote --builder path, asserting each rejects it. Wrapped in a def because
# Starlark forbids for/if at module top level (mirrors build-edge.star's edge()).
def fails(name, ctx):
    build(name = name, context = ctx, builder = False, no_cache = True, expect_fail = True)
    # capture=True on an expected failure now hands back the streamed log, so we
    # can confirm the remote path actually produced diagnostics (not a silent die).
    res = build(name = name, context = ctx, builder = True, no_cache = True, expect_fail = True, capture = True)
    assert_true(len(res["log"]) > 0, "remote build %s failed but streamed no log" % name)

fails("bf-run", "e2e/scenarios/build-fail/run")            # RUN exits non-zero
fails("bf-badbase", "e2e/scenarios/build-fail/badbase")    # FROM an unresolvable registry
fails("bf-parse", "e2e/scenarios/build-fail/parse")        # invalid Dockerfile syntax
fails("bf-badcopy", "e2e/scenarios/build-fail/badcopy")    # COPY of a missing source
log("✓ client build path (local + remote) rejects failing RUN / bad base / parse error / bad COPY")

# --- B. POST /.cornus/v1/build in-band failure trailer -------------------------------
# The upload endpoint returns HTTP 200 and then streams; a failing build ends the
# stream with a "BUILD FAILED:" line rather than a non-200 status.
up = build_upload(target = addr + "/e2e-buildfail/run:v1", context = "e2e/scenarios/build-fail/run", no_cache = True)
assert_eq(up["status"], 200, "POST /.cornus/v1/build streams after a 200, even on failure (got %r)" % up["status"])
assert_contains(up["log"], "BUILD FAILED:", "a failing build must stream the in-band BUILD FAILED trailer")
log("✓ POST /.cornus/v1/build reports a failing build in-band as BUILD FAILED: (HTTP still 200)")

# --- C. pre-stream request validation on POST /.cornus/v1/build ----------------------
# Missing target (?t=) is rejected before streaming with a real 400.
no_target = http(method = "POST", url = base + "/.cornus/v1/build")
assert_eq(no_target["status"], 400, "POST /.cornus/v1/build with no ?t= should be 400")
assert_contains(no_target["body"], "missing target (?t=)")

# A body that is not a valid tar is rejected before the engine runs, also 400.
bad_tar = http(method = "POST", url = base + "/.cornus/v1/build?t=" + addr + "/e2e-buildfail/x:v1", body = "this is not a tar archive")
assert_eq(bad_tar["status"], 400, "POST /.cornus/v1/build with a non-tar body should be 400")
assert_contains(bad_tar["body"], "bad context tar")
log("✓ POST /.cornus/v1/build rejects a missing target and a non-tar body with 400 (pre-stream)")
