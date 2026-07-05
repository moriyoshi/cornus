# Connection profiles: a stored `cornus config` context supplies the server
# endpoint, so the compose CLI reaches the served cornus with NO -H/--host on the
# command line — the endpoint can only come from the selected profile. This drives
# the same public-image project as compose.star, but purely through a profile
# (proving the whole resolver path: config file -> context -> client -> server).
# Skipped on the local target, which has no runtime backend to deploy the workload.

if TARGET == "local":
    log("connection-profile: skipped (needs a real backend)")
else:
    addr = serve()

    # Hermetic client config under a throwaway dir, so the test never reads or
    # writes the developer's real ~/.config/cornus/config.yaml. Every `cornus`
    # invocation below points CORNUS_CONFIG here via env=.
    workdir = temp_dir()
    cfg = workdir + "/config.yaml"
    env = {"CORNUS_CONFIG": cfg}

    # Create the profile pointing at the served cornus, and make it current.
    cornus("config", "set-context", "e2e", "--server", "http://" + addr, env = env)
    cornus("config", "use-context", "e2e", env = env)

    # get-contexts reflects the stored profile (name, endpoint, current marker).
    ctxs = cornus("config", "get-contexts", env = env)
    assert_contains(ctxs, "e2e", "get-contexts should list the profile")
    assert_contains(ctxs, addr, "get-contexts should show the server endpoint")
    assert_contains(ctxs, "*", "get-contexts should mark the current context")
    log("✓ profile stored and selected")

    # Deploy a compose project via the PROFILE alone: the argv carries no -H/--host,
    # so the client endpoint is resolved from the stored context. Detached (-d) so
    # up deploys and returns; a foreground up holds the session until Ctrl-C.
    compose_file = "e2e/scenarios/connection-profile-app.yaml"
    cornus("compose", "-f", compose_file, "up", "-d", env = env)

    ps = cornus("compose", "-f", compose_file, "ps", env = env)
    assert_contains(ps, "web", "compose ps (via profile) should list the service")
    log("✓ compose reached the served cornus purely via the stored profile")

    # Cross-check on the server itself (harness client -> same server) that the
    # profile-driven `compose up` actually deployed the workload.
    st = wait(name = "profile-web", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "workload deployed via profile should be running")
    log("✓ workload confirmed running on the server")

    cornus("compose", "-f", compose_file, "down", env = env)
