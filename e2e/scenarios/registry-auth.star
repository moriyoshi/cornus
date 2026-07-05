# Registry authentication + authorization, end to end. The happy registry
# scenarios run against an open server; this boots an AUTH-ENABLED server via
# serve(env=...) and proves the two enforcement layers on a registry push (opening
# a blob-upload session, a registry WRITE):
#   401 — no credential                       (authentication layer, auth.go)
#   403 — valid credential, identity lacks push (authorization layer, CORNUS_API_POLICY)
#   202 — valid credential, identity has push   (allowed)
# Real HS256 JWTs are minted with `cornus token issue` (the server is verify-only).
# Target-agnostic: registry only, no build/deploy privilege.
#
# Source of truth: pkg/server/auth.go (challenge / authenticate), pkg/server/
# apipolicy.go + server.go (registryAuthz), pkg/server/registry_authz_test.go.

# 32-byte HS256 secret shared by the issuer (token issue) and the verifier (server).
SECRET = "0123456789abcdef0123456789abcdef"

# The server: verify HS256-signed JWTs, and only identity "ci-bot" may push.
addr = serve(env = {
    "CORNUS_JWT_HS256_SECRET": SECRET,
    "CORNUS_API_POLICY": '{"ci-bot":["push"]}',
})
base = "http://" + addr
UPLOAD = base + "/v2/e2e/authtest/blobs/uploads/"

# Mint two JWTs against the same secret: an authorized pusher and an unauthorized
# identity. token issue prints just the token; strip the trailing newline.
push_tok = cornus("token", "issue", "--sub", "ci-bot", "--hs256-secret", SECRET).strip()
intruder_tok = cornus("token", "issue", "--sub", "intruder", "--hs256-secret", SECRET).strip()
assert_true(len(push_tok) > 0, "token issue produced no token")

# --- 401: no credential -------------------------------------------------------
anon = http(method = "POST", url = UPLOAD)
assert_eq(anon["status"], 401, "an unauthenticated push should be 401")
assert_contains(anon["body"], "authentication required")
assert_contains(anon["headers"]["Www-Authenticate"], "Basic", "a /v2/ challenge must be Basic (docker login)")
log("✓ unauthenticated push -> 401 authentication required (Basic challenge)")

# --- 403: authenticated but not permitted to push -----------------------------
denied = http(method = "POST", url = UPLOAD, headers = {"Authorization": "Bearer " + intruder_tok})
assert_eq(denied["status"], 403, "a valid identity without push must be 403")
assert_contains(denied["body"], "forbidden: identity not permitted to push")
log("✓ authenticated-but-unauthorized push -> 403 forbidden")

# --- 202: authenticated and permitted -----------------------------------------
allowed = http(method = "POST", url = UPLOAD, headers = {"Authorization": "Bearer " + push_tok})
assert_eq(allowed["status"], 202, "the authorized identity should open an upload session (202)")
assert_contains(allowed["headers"]["Location"], "/v2/e2e/authtest/blobs/uploads/", "a 202 upload must carry a Location")
log("✓ authorized push -> 202 (auth + policy both satisfied)")
