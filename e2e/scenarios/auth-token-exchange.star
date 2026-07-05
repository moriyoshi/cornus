# OAuth 2.0 Token Exchange (RFC 8693) against a LIVE server.
#
# The unit tests drive the handler directly and cover its decision table. What
# they structurally cannot show is that the endpoint is REACHABLE and that the
# credential it returns is honoured by the running server: route registration,
# the auth-middleware exemption, and the exchange verifier are three separate
# pieces of wiring, and each one is invisible to a test that calls the handler.
#
# The shape here is the real one. A stand-in third-party issuer (a checked-in EC
# key whose public half is the server's JWKS) mints the subject token at RUN TIME
# via `cornus token issue`, so nothing checked in can expire. The server's scope
# map grants that subject `registry:pull`.
#
# The load-bearing assertion is what the exchange returns: the subject token says
# `scope: api`, and the exchange issues `registry:pull`. A third party's scope
# claim is evidence, never a grant — that is the whole point of the scope map, and
# a run where the issuer could name its own authority would return "api" here.
#
# Source of truth: pkg/server/tokenexchange.go, pkg/authscope, pkg/server/auth.go.

KEY = "e2e/scenarios/certs/exchange-issuer.key"
JWKS = "e2e/scenarios/certs/exchange-issuer-jwks.json"
KID = "exchange-e2e"
AUD = "cornus"

MAPPED = "system:serviceaccount:ci:runner"   # the scope map names this subject
UNMAPPED = "system:serviceaccount:other:bot"  # equally valid, equally signed, unnamed

EXCHANGE_GRANT = "urn:ietf:params:oauth:grant-type:token-exchange"
JWT_TOKEN_TYPE = "urn:ietf:params:oauth:token-type:jwt"

SCOPE_MAP = """rules:
  - name: ci runners pull images
    scope: registry:pull
    match:
      sub: { equals: "%s" }
""" % MAPPED

def subject_token(sub, scope = "api"):
    """A token from the stand-in third-party issuer.

    scope defaults to "api" deliberately: the strongest thing a cornus token can
    say, so every assertion below is made against an issuer that IS claiming full
    authority for itself.
    """
    return cornus(
        "token", "issue",
        "--sub", sub, "--scope", scope, "--ttl", "1h",
        "--aud", AUD, "--kid", KID, "--private-key", KEY,
    ).strip()

def exchange(base, token, scope = ""):
    """POST an RFC 8693 token exchange; returns the raw response dict.

    The form is concatenated rather than escaped because a JWT's compact
    serialization is base64url with no padding — only [A-Za-z0-9-_.] — so every
    value here is already form-safe.
    """
    form = "grant_type=%s&subject_token_type=%s&subject_token=%s" % (
        EXCHANGE_GRANT, JWT_TOKEN_TYPE, token,
    )
    if scope != "":
        form += "&scope=" + scope
    return http(
        method = "POST",
        url = base + "/.cornus/v1/auth/exchange",
        body = form,
        headers = {"Content-Type": "application/x-www-form-urlencoded"},
    )

def last_line(out):
    """The final non-empty line of a CLI capture.

    `cornus token exchange` prints the issued scope as an INFO line and the token
    on stdout, so `TOKEN=$(cornus token exchange ...)` is usable in a shell. The
    harness captures the two streams COMBINED, so the scenario has to do here what
    stream separation does for a shell.
    """
    lines = [l for l in out.strip().split("\n") if l.strip() != ""]
    return lines[-1].strip()

def bearer(base, path, token):
    """GET path carrying token as a bearer credential."""
    return http(method = "GET", url = base + path, headers = {"Authorization": "Bearer " + token})

if TARGET != "docker":
    log("auth-token-exchange: skipped (docker-only; exercises the server's auth surface, not a backend)")
else:
    workdir = temp_dir()
    map_path = workdir + "/scopes.yaml"
    write_file(path = map_path, content = SCOPE_MAP)

    addr = serve(env = {
        "CORNUS_JWT_JWKS_FILE": JWKS,
        "CORNUS_JWT_AUDIENCE": AUD,
        "CORNUS_JWT_SCOPE_MAP": map_path,
    })
    base = "http://" + addr

    # ---- 1. the exchange issues what POLICY says, not what the token claims ---
    tok = subject_token(MAPPED)
    resp = exchange(base, tok)
    assert_eq(resp["status"], 200, "exchange failed: %r" % resp["body"])
    body = json.decode(resp["body"])
    assert_eq(body["scope"], "registry:pull",
              "the exchange issued %r — the subject token's own `scope: api` must not grant" % body["scope"])
    assert_eq(body["token_type"], "Bearer", "token_type = %r" % body["token_type"])
    assert_true(body["expires_in"] > 0, "expires_in = %r" % body["expires_in"])
    access = body["access_token"]
    assert_true(access != "", "no access_token in the response")
    log("✓ exchanged a third-party token for a cornus credential scoped registry:pull (the token claimed api)")

    # ---- 2. the issued credential is honoured, and bounded ---------------------
    # Through the real server, so this covers the exchange verifier and the fact
    # that the minted token's issuer/audience line up with what authenticate expects.
    r = bearer(base, "/v2/", access)
    assert_eq(r["status"], 200, "the exchanged credential was refused on the registry: %r" % r["body"])
    r = bearer(base, "/.cornus/v1/deploy", access)
    assert_eq(r["status"], 401,
              "the exchanged registry:pull credential reached the client API (%r) — the scope did not bound it" % r["status"])
    log("✓ the issued credential authenticates the registry and is refused on the client API")

    # ---- 3. both paths agree ---------------------------------------------------
    # The SUBJECT token used directly is mapped by the same rule on every request,
    # so it must reach exactly what the exchanged credential reaches. If these ever
    # diverged, one of the two paths would be applying a different policy.
    r = bearer(base, "/v2/", tok)
    assert_eq(r["status"], 200, "the subject token was refused on the registry via the direct path")
    r = bearer(base, "/.cornus/v1/deploy", tok)
    assert_eq(r["status"], 401, "the subject token reached the client API via the direct path")
    log("✓ the direct path and the exchange grant the same subject the same access")

    # ---- 4. an unmapped subject is entitled to nothing --------------------------
    # Same issuer, same audience, same signature, unexpired — everything the JWKS
    # verifier checks passes. Only the policy declines it.
    other = subject_token(UNMAPPED)
    resp = exchange(base, other)
    assert_eq(resp["status"], 400, "an unmapped subject was exchanged: %r" % resp["body"])
    assert_contains(resp["body"], "invalid_grant", "expected an RFC-shaped invalid_grant: %r" % resp["body"])
    assert_true("access_token" not in resp["body"], "a refused exchange returned a token: %r" % resp["body"])
    log("✓ a valid token for an unmapped subject is refused — the policy grants, not the signature")

    # ---- 5. scope may narrow, never widen --------------------------------------
    resp = exchange(base, tok, scope = "api")
    assert_eq(resp["status"], 400, "an upscope request succeeded: %r" % resp["body"])
    assert_contains(resp["body"], "invalid_scope", "expected invalid_scope: %r" % resp["body"])

    # caretaker IS contained in the access matrix under a full grant, so this is
    # refused by the client-credential rule rather than by containment — the case
    # that would slip through if only containment were checked.
    resp = exchange(base, tok, scope = "caretaker")
    assert_eq(resp["status"], 400, "a downscope into caretaker succeeded: %r" % resp["body"])
    assert_contains(resp["body"], "invalid_scope", "expected invalid_scope: %r" % resp["body"])

    # The one narrowing that is legal here is a no-op (pull -> pull); asking for it
    # explicitly proves the parameter is honoured rather than ignored wholesale.
    resp = exchange(base, tok, scope = "registry:pull")
    assert_eq(resp["status"], 200, "an in-policy scope request was refused: %r" % resp["body"])
    assert_eq(json.decode(resp["body"])["scope"], "registry:pull", "requested scope was not echoed")
    log("✓ scope may only narrow: api and caretaker refused, an in-policy request honoured")

    # ---- 6. the CLI client speaks the same protocol ----------------------------
    # `cornus token exchange` is the scripting surface and the way to ask "what
    # does policy grant this subject right now". Driving it here covers the client
    # half — request encoding, response parsing, the issued scope — which the
    # server-side assertions above cannot see.
    out = last_line(cornus("token", "exchange", "--server", base, "--subject-token", tok))
    assert_true(out != "", "cornus token exchange printed no token")
    r = bearer(base, "/v2/", out)
    assert_eq(r["status"], 200, "the CLI-exchanged credential was refused on the registry: %r" % r["body"])
    r = bearer(base, "/.cornus/v1/deploy", out)
    assert_eq(r["status"], 401, "the CLI-exchanged credential reached the client API")
    log("✓ `cornus token exchange` issues a credential bounded by the same policy")

    # The client must surface a policy refusal as a failure, not print an empty
    # token and exit 0 — a script doing `TOKEN=$(cornus token exchange ...)` would
    # otherwise carry on with nothing and fail somewhere far away.
    err = cornus("token", "exchange", "--server", base, "--subject-token", other, expect_fail = True)
    assert_contains(err, "invalid_grant", "an unentitled subject should fail with the RFC code: %r" % err)
    log("✓ the CLI fails loudly on a policy refusal rather than printing nothing")

    # ---- 7. protocol errors are RFC-shaped -------------------------------------
    resp = http(
        method = "POST",
        url = base + "/.cornus/v1/auth/exchange",
        body = "grant_type=client_credentials&subject_token=x&subject_token_type=" + JWT_TOKEN_TYPE,
        headers = {"Content-Type": "application/x-www-form-urlencoded"},
    )
    assert_eq(resp["status"], 400, "a wrong grant_type was accepted")
    assert_contains(resp["body"], "unsupported_grant_type", "expected unsupported_grant_type: %r" % resp["body"])
    log("✓ a standard client gets an RFC-shaped diagnosis, not a bare 400")
