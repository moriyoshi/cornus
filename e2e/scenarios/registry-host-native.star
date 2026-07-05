# host-native registry re-export (CORNUS_REGISTRY_SOURCE=host-native).
#
# On a host backend, host-native makes cornus's /v2/* registry a *view* over the
# backend's own local image store, with no separate content store (CAS). On the
# docker target that store is the local Docker daemon. This scenario proves the
# whole loop:
#   - a server-side build lands the image in the daemon (docker-archive -> load),
#     not a registry push;
#   - a /v2/* manifest GET re-exports that image on demand (docker save);
#   - the registry keeps no CAS (empty _catalog) and rejects writes (405);
#   - a dockerhost deploy runs the image WITHOUT a registry pull (skip-pull) — a
#     pull would fail (a bare ref is not on docker.io), so a running replica is
#     the proof that the pull was skipped.
#
# The docker E2E target sets CORNUS_REGISTRY_SOURCE=off by default (the registry
# scenarios exercise the CAS); serve(env=) overrides it to host-native here.
# docker-only, so self-skip on every other target.

if TARGET != "docker":
    log("skip: registry-host-native is docker-only (TARGET=%s)" % TARGET)
else:
    addr = serve(env = {"CORNUS_REGISTRY_SOURCE": "host-native"})

    tag = "cornus-e2e-reexport:v1"
    repo = "cornus-e2e-reexport"

    # Build through the server (POST /.cornus/v1/build). In host-native docker mode
    # the result is exported as a docker-archive and loaded into the local daemon
    # (docker load), not pushed to a CAS. The app Dockerfile defaults GREETING, so
    # no build args are needed.
    res = build_upload(target = tag, context = "e2e/scenarios/app")
    assert_eq(res["status"], 200, "build request should stream (HTTP 200)")
    assert_contains(res["log"], "BUILD OK", "build should succeed and load into the daemon")
    log("✓ built %s and loaded it into the local Docker daemon" % tag)

    # Re-export: a /v2/* manifest GET is served straight from the daemon via
    # `docker save`, reconstructed into an OCI manifest — no CAS involved.
    m = http(method = "GET", url = "http://" + addr + "/v2/" + repo + "/manifests/v1")
    assert_eq(m["status"], 200, "manifest should be re-exported from the daemon (docker save)")
    log("✓ /v2/* re-exported the daemon image on demand")

    # No separate CAS: the catalog stays empty (the daemon exposes no catalog),
    # and write verbs are rejected rather than silently swallowed.
    cat = http(method = "GET", url = "http://" + addr + "/v2/_catalog")
    assert_contains(cat["body"], "\"repositories\":[]", "host-native keeps no CAS; catalog must be empty")
    wr = http(method = "PUT", url = "http://" + addr + "/v2/" + repo + "/manifests/v2")
    assert_eq(wr["status"], 405, "read-only host-native registry must reject writes with 405")
    log("✓ no CAS: empty catalog and writes rejected (405)")

    # Deploy on dockerhost: the daemon already has the image, so the backend runs
    # it without a registry pull (skip-pull-if-local). A registry pull of a bare
    # ref would try docker.io and fail, so a running replica proves it was skipped.
    deploy(name = "reexport", image = tag, replicas = 1)
    st = wait(name = "reexport", running = 1, timeout = "120s")
    assert_eq(st["running"], 1, "deploy should run the daemon image without a registry pull")
    log("✓ dockerhost deployed the local image with no registry round-trip")

    remove(name = "reexport")
