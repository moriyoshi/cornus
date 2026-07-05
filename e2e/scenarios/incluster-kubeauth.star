# kube-auth: authenticate to an in-cluster cornus with a cluster-minted
# ServiceAccount token instead of a static bearer token. kube-only.
#
# Stands up an auth-enabled in-cluster cornus whose JWT verifier trusts the
# cluster's own SA-token JWKS (published into a ConfigMap it mounts) for audience
# "cornus". Then drives the CLI through a profile that BOTH port-forwards to it AND
# mints an audience-scoped SA token via the TokenRequest API (no static token
# anywhere). A negative control — the same port-forward with NO kube-auth — must be
# rejected, proving the minted token is what authenticates (i.e. auth is enforced,
# not silently off).
#
# Registered in SCENARIOS; self-skips off kube. Relies on the cornus:e2e image
# loaded into kind (containerized e2e flow prepare_kube). Issuer is left unset on
# the server (no iss constraint), so the scenario need not parse the OIDC discovery
# document — audience scoping + JWKS signature are what it verifies.

NS = "cornus-e2e"

if TARGET != "kube":
    log("incluster-kubeauth: skipped (kube-only; needs an auth-enabled in-cluster cornus)")
else:
    workdir = temp_dir()

    # Publish the cluster's SA-token JWKS into a ConfigMap the cornus pod mounts, so
    # cornus verifies TokenRequest-minted tokens with no in-cluster fetch/TLS.
    jwks = kubectl("get", "--raw", "/openid/v1/jwks")
    jwks_path = workdir + "/jwks.json"
    write_file(path = jwks_path, content = jwks)
    kubectl("-n", NS, "delete", "configmap", "cornus-jwks", "--ignore-not-found")
    kubectl("-n", NS, "create", "configmap", "cornus-jwks", "--from-file=jwks.json=" + jwks_path)

    manifest = "e2e/scenarios/incluster-cornus-auth.yaml"
    kubectl("-n", NS, "apply", "-f", manifest)
    kubectl("-n", NS, "rollout", "status", "deploy/cornus-auth", "--timeout=180s")
    log("✓ auth-enabled in-cluster cornus rolled out (JWKS + audience=cornus)")

    cfg = workdir + "/config.yaml"
    env = {"CORNUS_CONFIG": cfg}

    # Profile that port-forwards to the in-cluster cornus AND mints a cluster SA
    # token (audience "cornus") as the bearer credential — no static token.
    cornus(
        "config", "set-context", "kubeauth",
        "--pf-namespace", NS, "--pf-service", "cornus-auth", "--pf-remote-port", "5000",
        "--kube-auth-service-account", "default", "--kube-auth-audience", "cornus",
        env = env,
    )
    # Negative control: same port-forward, but NO kube-auth (no token minted).
    cornus(
        "config", "set-context", "noauth",
        "--pf-namespace", NS, "--pf-service", "cornus-auth", "--pf-remote-port", "5000",
        env = env,
    )
    cornus("config", "use-context", "kubeauth", env = env)

    app = "e2e/scenarios/connection-profile-app.yaml"

    # Positive: the minted token authenticates -> the API call succeeds.
    cornus("compose", "-f", app, "ps", env = env)
    log("✓ minted kube token authenticated to the in-cluster cornus via the port-forward")

    # Negative: no token -> the auth-enabled server rejects the call. expect_fail
    # returns the output instead of aborting, so we can log the rejection.
    out = cornus("--context", "noauth", "compose", "-f", app, "ps", env = env, expect_fail = True)
    log("✓ unauthenticated call rejected — auth is enforced, the minted token is what satisfies it")

    kubectl("-n", NS, "delete", "-f", manifest, "--ignore-not-found")
    kubectl("-n", NS, "delete", "configmap", "cornus-jwks", "--ignore-not-found")
