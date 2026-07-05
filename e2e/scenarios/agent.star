# The unified client agent. `cornus daemon docker` and `cornus compose up -d` now
# run inside ONE background agent reached over a single control socket
# (`cornus daemon agent`), instead of a per-project mounts daemon plus a separate
# docker proxy process. This drives BOTH frontends against one agent and asserts
# `cornus daemon status` sees them and a single `cornus daemon stop` tears the
# whole thing down. Docker-only (drives a real docker CLI + compose against the
# proxy; the agent is isolated per scenario via CORNUS_AGENT_DIR by the harness).

compose_file = "e2e/scenarios/compose-app.yaml"

def wait_gone(name, steps = 60):
    for _ in range(steps):
        if status(name = name)["total"] == 0:
            return
        sleep(duration = "2s")
    fail(msg = "%s not removed" % name)

if TARGET != "docker":
    log("agent: skipped (docker-only; drives compose up -d + daemon docker on one agent)")
else:
    serve()

    # Frontend 1: a compose project, detached -> held by the background agent.
    compose_up(file = compose_file, detach = True)
    wait(name = "e2e-web", running = 1, timeout = "180s")

    # Frontend 2: a docker frontend hosted by the SAME agent (dockerd_up runs
    # `cornus daemon docker`, which now registers with the agent).
    host = dockerd_up()
    docker("-H", host, "run", "-d", "--name", "agtbox", "alpine:3.20", "sh", "-c", "sleep infinity")
    wait(name = "agtbox", running = 1, timeout = "180s")
    log("✓ compose up -d and daemon docker are both up")

    # One agent hosts both: `daemon status` reports the docker frontend AND the
    # compose project (proof they share a single control socket / process).
    st = cornus("daemon", "status")
    log(st)
    assert_contains(st, "docker frontend")
    assert_contains(st, "e2e")
    log("✓ one agent hosts both the compose project and the docker frontend")

    # The compose-published port is reachable through the agent's conduit.
    resp = http_get(url = "http://127.0.0.1:8080/", retry = "30s")
    assert_eq(resp["status"], 200, "compose-published port not reachable through the agent")
    assert_contains(resp["body"], "nginx")

    # A SINGLE `cornus daemon stop` tears the whole agent down.
    cornus("daemon", "stop")
    assert_contains(cornus("daemon", "status"), "no cornus client agent")
    log("✓ one daemon stop tore down the whole agent")

    # The docker-run workload was held by the docker frontend's deploy-attach
    # session, so stopping the agent dropped it and the server removed the workload.
    wait_gone("agtbox")

    # The compose deployments were deployed fire-and-forget (the agent only held
    # web's port conduit, not an attach session), so they persist -> clean them up.
    # `down` finds no agent and proceeds straight to the server-side deletes.
    compose_down(file = compose_file)
    wait_gone("e2e-web")
