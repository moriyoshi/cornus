# Registry served over a Google Cloud Storage backend (gs://, real or
# fake-gcs-server). Same round-trip + discovery as registry.star, but with
# serve(storage="gs://...") so the WHOLE registry HTTP surface (blob
# upload/commit, manifest, catalog, tags) is exercised end to end against the
# GCS driver — not just the storage-unit level covered by
# pkg/storage/cloudblob_test.go.
#
# SELF-SKIPS unless CORNUS_TEST_GCS names a bucket ref (the same opt-in env as
# the storage unit tests), so default runs stay green without one. The gs://
# driver only exists in a `-tags cloudblob` build of cornus; serve() in a
# default build fails with an unsupported-scheme error. Emulator endpoint and
# credentials come from the SDK's standard environment — serve() spawns cornus
# with the runner's environment, so STORAGE_EMULATOR_HOST (fake-gcs-server) set
# when invoking cornus-e2e reaches the server process. Run it via
# `make e2e-cloudblob` after starting the emulator + creating the bucket; see
# .agents/docs/TESTING.md "Cloud-storage backends (emulator runs)".
# Target-agnostic (registry-only; runs on --target local).

GCS_REF = getenv(name = "CORNUS_TEST_GCS")

if GCS_REF == "":
    log("registry-gcs: skipped (set CORNUS_TEST_GCS to a gs://bucket ref, plus STORAGE_EMULATOR_HOST for an emulator, and use a -tags cloudblob cornus)")
else:
    addr = serve(storage = GCS_REF)

    # Push a random image through the GCS-backed registry with a real OCI client
    # and verify the pulled digest matches (blob upload + manifest, all on GCS).
    digest = registry_roundtrip(ref = "e2e/gcsdemo:v1")
    assert_contains(digest, "sha256:")
    log("round-tripped digest over gs: " + digest)

    # Discovery endpoints must reflect the GCS-stored repo.
    ping = http_get(url = "http://" + addr + "/v2/")
    assert_eq(ping["status"], 200, "/v2/ ping should be 200")

    cat = http_get(url = "http://" + addr + "/v2/_catalog")
    assert_eq(cat["status"], 200, "catalog should be 200")
    assert_contains(cat["body"], "e2e/gcsdemo", "pushed repo missing from the GCS-backed catalog")

    tags = http_get(url = "http://" + addr + "/v2/e2e/gcsdemo/tags/list")
    assert_eq(tags["status"], 200, "tags/list should be 200")
    assert_contains(tags["body"], "v1", "pushed tag missing from the GCS-backed tag list")
    log("✓ registry over gs: push/pull + catalog + tags all served from Google Cloud Storage")
