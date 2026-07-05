# Registry served over an S3 storage backend (winterbaume / MinIO). This is the
# same round-trip + discovery as registry.star, but with serve(storage="s3://...")
# so the WHOLE registry HTTP surface (blob upload/commit, manifest, catalog, tags)
# is exercised end to end against real object storage — not just the storage-unit
# level covered by pkg/storage/s3_test.go.
#
# SELF-SKIPS unless CORNUS_TEST_S3_ENDPOINT names a reachable S3-compatible
# server with a "cornus" bucket (same opt-in env as the storage unit tests),
# so the full-glob containerized run (e2e/container) stays green without one.
# Run it via the registry-s3 runner under .agents-workspace/tmp, which starts
# winterbaume + creates the bucket + sets the env first.
# Target-agnostic (registry-only; runs on --target local).

S3_ENDPOINT = getenv(name = "CORNUS_TEST_S3_ENDPOINT")

if S3_ENDPOINT == "":
    log("registry-s3: skipped (set CORNUS_TEST_S3_ENDPOINT to a live S3 server to run)")
else:
    addr = serve(storage = "s3://cornus?endpoint=" + S3_ENDPOINT + "&path_style=true&access_key=test&secret_key=test&region=us-east-1")

    # Push a random image through the S3-backed registry with a real OCI client
    # and verify the pulled digest matches (blob multipart upload + manifest,
    # all on S3).
    digest = registry_roundtrip(ref = "e2e/s3demo:v1")
    assert_contains(digest, "sha256:")
    log("round-tripped digest over s3: " + digest)

    # Discovery endpoints must reflect the S3-stored repo.
    ping = http_get(url = "http://" + addr + "/v2/")
    assert_eq(ping["status"], 200, "/v2/ ping should be 200")

    cat = http_get(url = "http://" + addr + "/v2/_catalog")
    assert_eq(cat["status"], 200, "catalog should be 200")
    assert_contains(cat["body"], "e2e/s3demo", "pushed repo missing from the S3-backed catalog")

    tags = http_get(url = "http://" + addr + "/v2/e2e/s3demo/tags/list")
    assert_eq(tags["status"], 200, "tags/list should be 200")
    assert_contains(tags["body"], "v1", "pushed tag missing from the S3-backed tag list")
    log("✓ registry over s3: push/pull + catalog + tags all served from object storage")
