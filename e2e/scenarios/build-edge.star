# Build-backend edge cases and regressions. Each Dockerfile asserts with `test`,
# so a green build proves the behavior; build(expect_fail=True) proves the backend
# rejects what it must. Runs local AND remote (9P/WebSocket) so a regression in
# either path is caught. no_cache forces every RUN (hence every assertion) to
# actually execute. Requires the build engine (root / rootless stack); --target local.

base = "e2e/scenarios/build-edge"

# edge() runs one case both locally and remotely. Wrapped in a def because
# Starlark forbids for/if at module top level.
def edge(name, expect_fail = False, **kwargs):
    build(name = name, builder = False, no_cache = True, expect_fail = expect_fail, **kwargs)
    build(name = name, builder = True, no_cache = True, expect_fail = expect_fail, **kwargs)

serve()

# 1. .dockerignore excludes files from the main context (local + remote).
edge("edge-dockerignore", context = base + "/dockerignore")
log("1. .dockerignore filtering: local + remote OK")

# 2. A named build context honors its OWN .dockerignore. Applied by cornus's
#    caller-side 9P export (confinedfs), so exercised on the remote path only.
#    The local path does not filter named contexts (known asymmetry; see JOURNAL).
build(name = "edge-named-ignore", context = base + "/named/context",
      build_context = {"data": base + "/named/data"}, builder = True, no_cache = True)
log("2. named-context .dockerignore (remote): OK")

# 3. Symlinks transmit with their target intact (Linkname). Matters on the remote
#    9P path (p9fs.go); local is the control.
edge("edge-symlink", context = base + "/symlink")
log("3. symlink transmission: local + remote OK")

# 4. Multi-stage COPY --from (local + remote).
edge("edge-multistage", context = base + "/multistage")
log("4. multi-stage build: local + remote OK")

# 5. Build args reach RUN (local + remote).
edge("edge-buildarg", context = base + "/buildarg", args = {"VER": "1.2.3"})
log("5. build-arg: local + remote OK")

# 6. Custom Dockerfile name prefers <name>.dockerignore over .dockerignore.
edge("edge-customdf", context = base + "/customdf", dockerfile = "Build.Dockerfile")
log("6. custom <dockerfile>.dockerignore precedence: local + remote OK")

# 7. Negative: COPY of a .dockerignore'd file MUST fail (exclusion is real).
edge("edge-mustfail", context = base + "/mustfail", expect_fail = True)
log("7. COPY of ignored file correctly fails: local + remote OK")
