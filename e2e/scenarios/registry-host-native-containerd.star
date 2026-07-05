# host-native registry re-export on the containerd backend — the read-WRITE view.
#
# Unlike the dockerhost view (read-only docker save), host-native on containerd
# backs /v2/* with the host containerd content store directly: a push imports
# into it and a pull reads it back. So a build that pushes to /v2/* lands the
# image straight in the store the containerd deploy backend runs from — no CAS,
# no docker-load, no build-worker configuration.
#
# The containerd E2E target sets CORNUS_REGISTRY_SOURCE=off by default (the
# registry scenarios exercise the CAS); serve(env=) overrides it to host-native.
# containerd-only, so self-skip elsewhere.

if TARGET != "containerd":
    log("skip: registry-host-native-containerd is containerd-only (TARGET=%s)" % TARGET)
else:
    addr = serve(env = {"CORNUS_REGISTRY_SOURCE": "host-native"})

    # A normal build+push: the push imports blobs + an image record into the host
    # containerd content store (read-write /v2/*), unlike the dockerhost view which
    # would 405 the push.
    image = build(name = "reexport-ctd", context = "e2e/scenarios/app")

    # The pushed image is now re-exportable from the store: its manifest resolves,
    # and — because the push imported a real image record — the catalog lists it
    # (the dockerhost read-only view keeps an empty catalog instead).
    m = http(method = "GET", url = "http://" + addr + "/v2/reexport-ctd/manifests/latest")
    assert_eq(m["status"], 200, "manifest should resolve from the containerd store")
    cat = http(method = "GET", url = "http://" + addr + "/v2/_catalog")
    assert_contains(cat["body"], "reexport-ctd", "a push must import into the containerd store (catalog lists it)")
    log("✓ push imported into the containerd content store; /v2/* re-exports it")

    # Deploy it: the image is already in the containerd store, so the backend runs
    # it (pull re-exports from the same store, or the local-image fallback).
    deploy(name = "reexport-ctd", image = image, replicas = 1)
    st = wait(name = "reexport-ctd", running = 1, timeout = "120s")
    assert_eq(st["running"], 1, "deploy should run the image imported via the /v2/* push")
    log("✓ containerd deployed the pushed image with no separate registry")

    remove(name = "reexport-ctd")
