# Registry served over an Azure Blob storage backend (azblob://, real or
# Azurite). Same round-trip + discovery as registry.star, but with
# serve(storage="azblob://...") so the WHOLE registry HTTP surface (blob
# upload/commit, manifest, catalog, tags) is exercised end to end against the
# Azure driver — not just the storage-unit level covered by
# pkg/storage/cloudblob_test.go.
#
# SELF-SKIPS unless CORNUS_TEST_AZBLOB names a container ref (the same opt-in
# env as the storage unit tests), so default runs stay green without one. The
# azblob:// driver only exists in a `-tags cloudblob` build of cornus; serve()
# in a default build fails with an unsupported-scheme error. Credentials and
# the emulator endpoint come from the SDK's standard environment — serve()
# spawns cornus with the runner's environment, so the AZURE_STORAGE_* variables
# (account, key, domain, protocol, is-local-emulator for Azurite) set when
# invoking cornus-e2e reach the server process. Run it via `make e2e-cloudblob`
# after starting Azurite + creating the container; see .agents/docs/TESTING.md
# "Cloud-storage backends (emulator runs)".
# Target-agnostic (registry-only; runs on --target local).

AZBLOB_REF = getenv(name = "CORNUS_TEST_AZBLOB")

if AZBLOB_REF == "":
    log("registry-azblob: skipped (set CORNUS_TEST_AZBLOB to an azblob://container ref, plus the AZURE_STORAGE_* env, and use a -tags cloudblob cornus)")
else:
    addr = serve(storage = AZBLOB_REF)

    # Push a random image through the Azure-backed registry with a real OCI
    # client and verify the pulled digest matches (blob upload + manifest, all
    # on Azure Blob).
    digest = registry_roundtrip(ref = "e2e/azdemo:v1")
    assert_contains(digest, "sha256:")
    log("round-tripped digest over azblob: " + digest)

    # Discovery endpoints must reflect the Azure-stored repo.
    ping = http_get(url = "http://" + addr + "/v2/")
    assert_eq(ping["status"], 200, "/v2/ ping should be 200")

    cat = http_get(url = "http://" + addr + "/v2/_catalog")
    assert_eq(cat["status"], 200, "catalog should be 200")
    assert_contains(cat["body"], "e2e/azdemo", "pushed repo missing from the Azure-backed catalog")

    tags = http_get(url = "http://" + addr + "/v2/e2e/azdemo/tags/list")
    assert_eq(tags["status"], 200, "tags/list should be 200")
    assert_contains(tags["body"], "v1", "pushed tag missing from the Azure-backed tag list")
    log("✓ registry over azblob: push/pull + catalog + tags all served from Azure Blob storage")
