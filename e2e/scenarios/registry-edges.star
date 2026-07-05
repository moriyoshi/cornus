# Registry wire-protocol edges: exercise the OCI Distribution surface that the
# higher-level builtins (registry_roundtrip, http_get) never reach — HEAD, the
# resumable/chunked blob upload dance (POST -> PATCH -> PUT), cross-repo blob
# mount, digest rejection, manifest DELETE, and unsupported methods. Registry
# only: it uses serve() + the generic http() builtin, so it needs no build or
# deploy privilege and is target-agnostic.
#
# The registry code in pkg/registry/registry.go is the source of truth for
# every status/header asserted here.

addr = serve()
base = "http://" + addr

# sha256("hello"), reused throughout for blob upload/mount/reject edges.
HELLO_DIGEST = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
# A digest that no blob will ever hash to, used for the not-found edges.
ZERO_DIGEST = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

# Seed a repo so the manifest edges have something to inspect.
digest = registry_roundtrip(ref = "e2e/demo:v1")
log("seeded e2e/demo:v1 -> " + digest)

# --- 1. HEAD a manifest by tag ----------------------------------------------
# A HEAD on an existing tag returns 200 with the content digest and length but
# no body (handleManifest short-circuits before writing content on HEAD).
head = http(method = "HEAD", url = base + "/v2/e2e/demo/manifests/v1")
assert_eq(head["status"], 200, "HEAD manifest by tag should be 200")
assert_contains(head["headers"]["Docker-Content-Digest"], "sha256:")
assert_true("Content-Length" in head["headers"], "HEAD manifest must set Content-Length")
log("✓ HEAD manifest by tag -> 200 with digest + length")

# --- 2. Resumable / chunked blob upload (POST -> PATCH -> PUT) ---------------
# POST with neither ?digest nor ?mount opens an upload session and returns 202
# with a relative Location like /v2/e2e/edges/blobs/uploads/<id>.
post = http(method = "POST", url = base + "/v2/e2e/edges/blobs/uploads/")
assert_eq(post["status"], 202, "opening an upload session should be 202")
loc = post["headers"]["Location"]
assert_contains(loc, "/v2/e2e/edges/blobs/uploads/")
log("✓ opened upload session at " + loc)

# PATCH streams a chunk into the session; the registry echoes 202 + Range.
patch = http(method = "PATCH", url = base + loc, body = "hello")
assert_eq(patch["status"], 202, "PATCH upload chunk should be 202")

# PUT with ?digest closes the session, verifies the digest, and returns 201
# with the canonical content digest.
put = http(method = "PUT", url = base + loc + "?digest=" + HELLO_DIGEST)
assert_eq(put["status"], 201, "PUT closing the upload should be 201")
assert_eq(put["headers"]["Docker-Content-Digest"], HELLO_DIGEST, "committed digest must match")

# The blob is now retrievable under its repo.
head_blob = http(method = "HEAD", url = base + "/v2/e2e/edges/blobs/" + HELLO_DIGEST)
assert_eq(head_blob["status"], 200, "HEAD of the uploaded blob should be 200")
log("✓ chunked upload committed and HEAD-able")

# --- 3. Cross-repo blob mount -----------------------------------------------
# POST ?mount=<digest>&from=<repo> mounts an existing blob into a new repo
# without re-uploading, returning 201 immediately.
mount = http(method = "POST", url = base + "/v2/e2e/edges2/blobs/uploads/?mount=" + HELLO_DIGEST + "&from=e2e/edges")
assert_eq(mount["status"], 201, "cross-repo mount should be 201")
# The mount is proven by HEAD-ing the same blob under the destination repo.
head_mount = http(method = "HEAD", url = base + "/v2/e2e/edges2/blobs/" + HELLO_DIGEST)
assert_eq(head_mount["status"], 200, "mounted blob should be HEAD-able under e2e/edges2")
log("✓ cross-repo mount made the blob visible under e2e/edges2")

# --- 4. DIGEST_INVALID rejection --------------------------------------------
# A monolithic POST ?digest=<wrong> whose body does not hash to the claimed
# digest is rejected 400 DIGEST_INVALID (storage.ErrDigestMismatch).
bad = http(method = "POST", url = base + "/v2/e2e/bad/blobs/uploads/?digest=" + ZERO_DIGEST, body = "hello")
assert_eq(bad["status"], 400, "digest mismatch should be 400")
assert_contains(bad["body"], "DIGEST_INVALID")
log("✓ mismatched digest rejected with DIGEST_INVALID")

# --- 5. Manifest DELETE (by digest) -----------------------------------------
# DeleteManifest requires a digest — passing a tag fails ParseDigest and yields
# 404 — so the OCI-conformant flow is: HEAD the tag to learn its digest, then
# DELETE by digest. Registry returns 202 on success.
registry_roundtrip(ref = "e2e/deltest:v1")
head_del = http(method = "HEAD", url = base + "/v2/e2e/deltest/manifests/v1")
assert_eq(head_del["status"], 200, "HEAD of the manifest to delete should be 200")
del_digest = head_del["headers"]["Docker-Content-Digest"]
assert_contains(del_digest, "sha256:")
delete = http(method = "DELETE", url = base + "/v2/e2e/deltest/manifests/" + del_digest)
assert_eq(delete["status"], 202, "DELETE manifest by digest should be 202")
# After deleting the membership marker, HEAD by tag can no longer resolve it.
head_gone = http(method = "HEAD", url = base + "/v2/e2e/deltest/manifests/v1")
assert_eq(head_gone["status"], 404, "the deleted manifest should be gone (404)")
log("✓ manifest deleted by digest; subsequent HEAD is 404")

# --- 6. Blob DELETE ----------------------------------------------------------
# handleBlob implements DELETE (202 on success): the blob is removed from the
# CAS, a subsequent HEAD is 404, and deleting it again is 404 BLOB_UNKNOWN —
# mirroring pkg/registry/features_test.go TestBlobDelete.
blob_del = http(method = "DELETE", url = base + "/v2/e2e/edges/blobs/" + HELLO_DIGEST)
assert_eq(blob_del["status"], 202, "blob DELETE should be accepted with 202")
blob_gone = http(method = "HEAD", url = base + "/v2/e2e/edges/blobs/" + HELLO_DIGEST)
assert_eq(blob_gone["status"], 404, "the deleted blob should be gone (404)")
blob_del2 = http(method = "DELETE", url = base + "/v2/e2e/edges/blobs/" + HELLO_DIGEST)
assert_eq(blob_del2["status"], 404, "deleting an already-deleted blob should be 404")
log("✓ blob DELETE removes the blob (202, then 404s)")

# --- 7. HEAD a nonexistent blob ---------------------------------------------
# StatBlob misses, so HEAD/GET of an unknown blob returns 404 BLOB_UNKNOWN.
missing = http(method = "HEAD", url = base + "/v2/e2e/edges/blobs/" + ZERO_DIGEST)
assert_eq(missing["status"], 404, "HEAD of a missing blob should be 404")
# NOTE: HEAD carries no body, so assert BLOB_UNKNOWN via a GET, which returns the
# JSON error document.
missing_get = http(method = "GET", url = base + "/v2/e2e/edges/blobs/" + ZERO_DIGEST)
assert_eq(missing_get["status"], 404, "GET of a missing blob should be 404")
assert_contains(missing_get["body"], "BLOB_UNKNOWN")
log("✓ missing blob reports 404 BLOB_UNKNOWN")
