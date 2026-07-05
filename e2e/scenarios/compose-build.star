# `cornus compose` building a service from a Dockerfile and running it, then
# exercising the compose lifecycle CLI (stop/start/restart) on a mount-free
# service. compose_build builds + pushes the image; compose_up deploys it.
# Docker-only + build engine (the `build:` section builds through the server).

def wait_running(name, want, steps = 90):
    for _ in range(steps):
        if status(name = name)["running"] == want:
            return
        sleep(duration = "2s")
    fail(msg = "%s never reached running=%d" % (name, want))

compose_file = "e2e/scenarios/compose-build.yaml"

if TARGET != "docker":
    log("compose-build: skipped (docker-only; needs the build engine + dockerhost)")
else:
    serve()

    # Explicit build step: builds and pushes the service image into the registry.
    compose_build(file = compose_file, project = "cbld")
    log("✓ compose build produced the service image")

    # up deploys it (build services are rebuilt on up; that's fine). Detached so it
    # deploys and returns: a foreground `up` (no -d) holds the session until Ctrl-C
    # (auto-forwarding is on by default), which would hang this synchronous scenario.
    compose_up(file = compose_file, project = "cbld", detach = True)
    st = wait(name = "cbld-app", running = 1, timeout = "180s")
    assert_eq(st["running"], 1, "cbld-app not running after up")

    ps = compose_ps(file = compose_file, project = "cbld")
    assert_contains(ps, "app")
    log("✓ built service is up")

    # Lifecycle via the compose CLI (mount-free -> plain deploy actions).
    compose_stop(file = compose_file, project = "cbld")
    wait_running("cbld-app", 0)
    compose_start(file = compose_file, project = "cbld")
    wait_running("cbld-app", 1)
    compose_restart(file = compose_file, project = "cbld")
    wait_running("cbld-app", 1)
    log("✓ compose stop/start/restart drove the service")

    compose_down(file = compose_file, project = "cbld")
    gone = status(name = "cbld-app")
    assert_eq(gone["total"], 0, "cbld-app not removed after compose down")
    log("✓ compose down removed the deployment")
