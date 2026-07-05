# Registry round-trip: push a random image through cornus's registry with a
# real OCI client and verify the pulled digest matches. Then exercise the
# discovery endpoints (v2 ping, catalog, tag list). Target-agnostic.

addr = serve()

digest = registry_roundtrip(ref = "e2e/demo:v1")
log("round-tripped digest: " + digest)
assert_contains(digest, "sha256:")

# The /v2/ ping advertises the OCI Distribution API version.
ping = http_get(url = "http://" + addr + "/v2/")
assert_eq(ping["status"], 200, "/v2/ ping should be 200")

# The pushed repository shows up in the catalog...
cat = http_get(url = "http://" + addr + "/v2/_catalog")
assert_eq(cat["status"], 200, "catalog should be 200")
assert_contains(cat["body"], "e2e/demo")

# ...and its tag in the repo's tag list.
tags = http_get(url = "http://" + addr + "/v2/e2e/demo/tags/list")
assert_eq(tags["status"], 200, "tags/list should be 200")
assert_contains(tags["body"], "v1")
log("✓ catalog + tag list expose the pushed image")
