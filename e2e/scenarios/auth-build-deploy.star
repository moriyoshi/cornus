# Authenticated dataplane specification. With client auth enabled and anonymous
# registry pull disabled, both local and server-side builds must push to Cornus's
# co-located registry, and the deploy backend must pull those images with a
# short-lived internal credential. Controls first prove that anonymous API access
# is denied and the caller's JWT works for both API and registry writes.
#
# docker-only: the deploy-time pull under test is the dockerhost backend's
# POST /images/create. Source of truth: pkg/server/auth.go, pkg/server/server.go
# (localPushTargets), pkg/build/builder/engine_linux.go,
# pkg/deploy/dockerhost/{dockerhost,engine}.go.

# 32-byte HS256 secret shared by the issuer (token issue) and the verifier (server).
SECRET = "0123456789abcdef0123456789abcdef"

if TARGET != "docker":
    log("auth-build-deploy: skipped (docker-only; the deploy-time pull under test is dockerhost's POST /images/create)")
else:
    # Verify HS256-signed JWTs; identity "ci-bot" may do everything. Anonymous
    # registry pull is deliberately NOT enabled — the point is a closed registry.
    addr = serve(env = {
        "CORNUS_JWT_HS256_SECRET": SECRET,
        "CORNUS_API_POLICY": '{"ci-bot":["*"]}',
    })
    base = "http://" + addr

    # The client's credential: a real HS256 JWT, exactly as registry-auth.star mints it.
    tok = cornus("token", "issue", "--sub", "ci-bot", "--hs256-secret", SECRET).strip()
    assert_true(len(tok) > 0, "token issue produced no token")
    client_env = {"CORNUS_TOKEN": tok}

    # --- control A: auth really is on, and THIS credential really works -------
    anon = http(method = "GET", url = base + "/.cornus/v1/deploy")
    assert_eq(anon["status"], 401, "auth is not enabled: an anonymous API read should be 401")
    authed = http(
        method = "GET",
        url = base + "/.cornus/v1/deploy",
        headers = {"Authorization": "Bearer " + tok},
    )
    assert_eq(authed["status"], 200, "the client credential must reach the API")
    log("✓ auth enabled (anonymous API read -> 401) and the client token is accepted (200)")

    work = temp_dir()

    # --- leg 1a: REMOTE build (the SERVER's own engine) --push ----------------
    # --builder runs the build on the server; its push target is rewritten to the
    # server's own loopback registry (Server.localPushTargets). So this is the
    # server authenticating to ITSELF: the BuildKit exporter carries an internal
    # registry:push credential minted from the installation secret, which is what
    # lets the push succeed with no operator-supplied registry login. The WS
    # handshake separately carries the caller's own bearer token (bearerHeader,
    # cmd/cornus/build.go), so a 401 here isolates to the exporter.
    #
    # The stored profile exercises the resolver path that HAS a selected context,
    # where CORNUS_TOKEN must still outrank the profile's own token. The
    # neighbouring no-context case — ResolveWith once returned before assigning
    # cn.Token and dropped CORNUS_TOKEN entirely — is fixed and covered by
    # cmd/cornus/internal/clientconn/token_test.go rather than re-proved here.
    cfg = work + "/client-config.yaml"
    prof_env = {"CORNUS_CONFIG": cfg, "CORNUS_TOKEN": tok}
    cornus("config", "set-context", "e2e-auth", "--server", base, env = prof_env)
    cornus("config", "use-context", "e2e-auth", env = prof_env)

    remote_built = addr + "/e2e-auth-build-remote:latest"
    cornus(
        "build",
        "-t",
        remote_built,
        "--insecure",
        "--builder",
        "ws://" + addr + "/.cornus/v1/build/attach",
        "e2e/scenarios/app",
        env = prof_env,
    )
    log("✓ remote (server-side) build pushed %s into the auth-enabled registry" % remote_built)

    # --- leg 1b: LOCAL build --push into the closed registry ------------------
    # The strongest form of "the client did everything right": an explicit
    # CORNUS_TOKEN in the build client's environment. Its own data dir keeps
    # BuildKit's boltdb lock off the server's.
    built = addr + "/e2e-auth-build:latest"
    cornus(
        "build",
        "-t",
        built,
        "--insecure",
        "e2e/scenarios/app",
        env = {"CORNUS_TOKEN": tok, "CORNUS_DATA": work + "/build"},
    )
    log("✓ tokened `cornus build` pushed %s into the auth-enabled registry" % built)

    # --- control B: an authenticated CLIENT push works ------------------------
    # `cornus push` DOES carry CORNUS_TOKEN (bearerForRegistry), so this both
    # re-proves the credential on a registry WRITE and seeds an image the deploy
    # leg can pull independently of the build leg.
    seeded = addr + "/e2e-auth/alpine:v1"
    docker("pull", "alpine:3.20")
    docker("save", "-o", work + "/alpine.tar", "alpine:3.20")
    cornus("push", work + "/alpine.tar", seeded, env = client_env)
    tags = http(
        method = "GET",
        url = base + "/v2/e2e-auth/alpine/tags/list",
        headers = {"Authorization": "Bearer " + tok},
    )
    assert_eq(tags["status"], 200, "the seeded image should be listed")
    assert_contains(tags["body"], "v1")
    log("✓ authenticated client push into the closed registry OK")

    # --- leg 2: deploy-time image pull ---------------------------------------
    # A --detach deploy applies the spec through the server, whose dockerhost
    # backend must pull from the co-located, credential-requiring registry. A
    # failed pull is reported synchronously, so a non-zero exit here IS the defect.
    spec_pull = work + "/seeded.yaml"
    write_file(
        path = spec_pull,
        content = 'name: authpull\nimage: %s\ncommand: ["sh", "-c", "sleep infinity"]\n' % seeded,
    )
    cornus("deploy", "-f", spec_pull, "--server", base, "--detach", env = client_env)
    running = docker("inspect", "cornus-authpull-0", "--format", "{{.State.Running}}")
    assert_contains(running, "true", "the deployed workload should be running")
    log("✓ deploy pulled %s from the auth-enabled registry" % seeded)
    cornus("deploy", "-f", spec_pull, "--delete", "--server", base, env = client_env)

    # --- leg 2b: deploy the image the build just pushed ------------------------
    spec_built = work + "/built.yaml"
    write_file(path = spec_built, content = "name: authbuilt\nimage: %s\n" % built)
    cornus("deploy", "-f", spec_built, "--server", base, "--detach", env = client_env)
    running2 = docker("inspect", "cornus-authbuilt-0", "--format", "{{.State.Running}}")
    assert_contains(running2, "true", "the built-image workload should be running")
    log("✓ deployed the freshly built image under auth")
    cornus("deploy", "-f", spec_built, "--delete", "--server", base, env = client_env)
