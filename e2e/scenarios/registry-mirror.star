# Pull-through mirror: on a local miss, the builtin registry proxies the pull to
# an upstream registry (the offline stand-in for Docker Hub) — but only when the
# image is not already local. Opt-in via CORNUS_REGISTRY_MIRROR; caching is on by
# default and can be disabled with CORNUS_REGISTRY_MIRROR_CACHE=false. Fully
# self-contained (no network): upstream_registry() runs an in-process registry on
# loopback that the served cornus reaches over plain HTTP. Backend-agnostic.

# An upstream holding one image the local registry does not have.
up = upstream_registry(seed = ["library/busybox:latest"])

# --- caching mirror (default) -------------------------------------------------
reg = serve(env = {"CORNUS_REGISTRY_MIRROR": up})

# The image is absent locally, so the registry proxies its manifest upstream.
m = http(method = "GET", url = "http://" + reg + "/v2/library/busybox/manifests/latest")
assert_eq(m["status"], 200, "mirror should proxy the manifest on a local miss")

# With caching on, the repo now exists locally (appears in the catalog).
cat = http(method = "GET", url = "http://" + reg + "/v2/_catalog")
assert_contains(cat["body"], "library/busybox", "caching mirror should persist the pulled repo")
log("✓ caching mirror served + persisted the upstream manifest")

# An image absent both locally and upstream still returns the standard 404.
miss = http(method = "GET", url = "http://" + reg + "/v2/library/nope/manifests/latest")
assert_eq(miss["status"], 404, "image absent everywhere should 404")

# --- transparent mirror (cache off) -------------------------------------------
stop_server()
reg2 = serve(env = {"CORNUS_REGISTRY_MIRROR": up, "CORNUS_REGISTRY_MIRROR_CACHE": "false"})

m2 = http(method = "GET", url = "http://" + reg2 + "/v2/library/busybox/manifests/latest")
assert_eq(m2["status"], 200, "transparent mirror should still serve the manifest")

# Transparent mode persists nothing: the catalog stays empty.
cat2 = http(method = "GET", url = "http://" + reg2 + "/v2/_catalog")
assert_contains(cat2["body"], "\"repositories\":[]", "transparent mirror must not persist")
log("✓ transparent mirror served without persisting")
