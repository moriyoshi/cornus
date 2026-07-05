# Registry error paths: the OCI Distribution *failure* surface that the happy-path
# builtins (registry_roundtrip, http_get) and the wire-edge scenario
# (registry-edges.star) do not assert — bad routes, unsupported methods, missing
# digests, unknown upload sessions, missing manifests, the cross-repo digest-leak
# guard, and the manifest-validation gap. Registry only: it uses serve() + the
# generic http() builtin, so it needs no build or deploy privilege and is
# target-agnostic.
#
# pkg/registry/registry.go (writeError) and pkg/storage/cas.go are the source of
# truth for every status/code/message asserted here.

addr = serve()
base = "http://" + addr

# sha256("hello"), a valid digest for the missing-session edges.
HELLO_DIGEST = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

# --- 1. The /v2/ ping is open; an unrecognized /v2/... path is 404 --------------
ping = http(method = "GET", url = base + "/v2/")
assert_eq(ping["status"], 200, "GET /v2/ (API-version ping) should be 200")
assert_contains(ping["body"], "{}", "ping body should be {}")

bad_path = http(method = "GET", url = base + "/v2/foo")
assert_eq(bad_path["status"], 404, "an unrecognized /v2/ path should be 404")
assert_contains(bad_path["body"], "NAME_UNKNOWN")
assert_contains(bad_path["body"], "unrecognized path")
log("✓ /v2/ ping is 200; an unrecognized path is 404 NAME_UNKNOWN")

# --- 2. Unsupported method on a resource -> 405 --------------------------------
# tags/list is GET-only; any other method hits the handler default (405 UNSUPPORTED).
bad_method = http(method = "PUT", url = base + "/v2/e2e/demo/tags/list")
assert_eq(bad_method["status"], 405, "PUT on tags/list should be 405")
assert_contains(bad_method["body"], "UNSUPPORTED")
assert_contains(bad_method["body"], "method not allowed")
log("✓ unsupported method -> 405 UNSUPPORTED")

# --- 3. Chunked upload PUT with no ?digest -> 400 DIGEST_INVALID ---------------
# The digest check runs before the session lookup, so even a bogus session id
# yields "missing digest" when ?digest is absent.
no_digest = http(method = "PUT", url = base + "/v2/e2e/x/blobs/uploads/whatever")
assert_eq(no_digest["status"], 400, "PUT closing an upload with no ?digest should be 400")
assert_contains(no_digest["body"], "DIGEST_INVALID")
assert_contains(no_digest["body"], "missing digest")
log("✓ chunked PUT without ?digest -> 400 DIGEST_INVALID (missing digest)")

# --- 4. PATCH / PUT to an unknown upload session -> 404 BLOB_UPLOAD_UNKNOWN ----
patch_unknown = http(method = "PATCH", url = base + "/v2/e2e/x/blobs/uploads/nonexistent-session", body = "hi")
assert_eq(patch_unknown["status"], 404, "PATCH to an unknown upload session should be 404")
assert_contains(patch_unknown["body"], "BLOB_UPLOAD_UNKNOWN")
assert_contains(patch_unknown["body"], "upload unknown")

put_unknown = http(method = "PUT", url = base + "/v2/e2e/x/blobs/uploads/nonexistent-session?digest=" + HELLO_DIGEST)
assert_eq(put_unknown["status"], 404, "PUT to an unknown upload session should be 404")
assert_contains(put_unknown["body"], "BLOB_UPLOAD_UNKNOWN")
log("✓ PATCH/PUT to an unknown upload session -> 404 BLOB_UPLOAD_UNKNOWN")

# --- 5. GET a nonexistent manifest -> 404 MANIFEST_UNKNOWN ---------------------
missing_manifest = http(method = "GET", url = base + "/v2/e2e/demo/manifests/does-not-exist")
assert_eq(missing_manifest["status"], 404, "GET of a missing manifest should be 404")
assert_contains(missing_manifest["body"], "MANIFEST_UNKNOWN")
assert_contains(missing_manifest["body"], "manifest unknown")
log("✓ missing manifest -> 404 MANIFEST_UNKNOWN")

# --- 6. Cross-repo digest-leak guard ------------------------------------------
# Push an image under repo A, then GET its manifest BY DIGEST under repo B. The
# content blob lives in the shared CAS, but the per-repo membership marker only
# exists under A, so B must NOT resolve it — 404, not a 200 content leak.
digest = registry_roundtrip(ref = "e2e/leak-a:v1")
assert_contains(digest, "sha256:")
leak = http(method = "GET", url = base + "/v2/e2e/leak-b/manifests/" + digest)
assert_eq(leak["status"], 404, "a manifest digest from another repo must not resolve (cross-repo leak)")
assert_contains(leak["body"], "MANIFEST_UNKNOWN")
log("✓ cross-repo digest does not leak -> 404 MANIFEST_UNKNOWN")

# --- 7. Manifest-validation gap (regression lock) -----------------------------
# The registry stores a manifest body WITHOUT parsing it or checking referenced
# blobs exist, so a garbage body with an arbitrary media type is accepted with
# 201. This asserts the CURRENT (permissive) behavior so a future move to real
# manifest validation is a deliberate, visible change rather than a silent one.
garbage = http(
    method = "PUT",
    url = base + "/v2/e2e/garbage/manifests/bad",
    body = "this is not a manifest at all",
    headers = {"Content-Type": "application/vnd.oci.image.manifest.v1+json"},
)
assert_eq(garbage["status"], 201, "the registry currently accepts an unvalidated manifest with 201 (validation gap)")
assert_contains(garbage["headers"]["Docker-Content-Digest"], "sha256:", "even an unvalidated manifest gets a content digest")
log("✓ manifest-validation gap locked: a garbage manifest body is stored with 201")

# --- 8. By-digest manifest PUT must match the body's real digest --------------
# A tag push is unvalidated (section 7), but OCI requires the server to REJECT a
# by-digest push whose body does not hash to the referenced digest. PUT a body to
# a digest reference (all-zeros) that its content can never match; the server must
# answer 400 DIGEST_INVALID, not silently store it under the real digest and 201.
WRONG_DIGEST = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
mismatch = http(
    method = "PUT",
    url = base + "/v2/e2e/bydigest/manifests/" + WRONG_DIGEST,
    body = "this body's real digest is not all-zeros",
    headers = {"Content-Type": "application/vnd.oci.image.manifest.v1+json"},
)
assert_eq(mismatch["status"], 400, "a by-digest manifest PUT whose body digest differs must be rejected with 400 (got %r)" % mismatch["status"])
assert_contains(mismatch["body"], "DIGEST_INVALID")
assert_contains(mismatch["body"], "does not match")
log("✓ by-digest manifest PUT with a mismatched body -> 400 DIGEST_INVALID")
