# `docker push` through the `cornus daemon docker` proxy. The daemon push
# protocol sends no image content, so the proxy treats the builtin registry as
# the local image store: a bare ref is acknowledged there, a registry-qualified
# ref is copied out to the named registry, and an unknown image errors. Drives a
# real `docker` CLI pointed at the proxy. Docker-only.

if TARGET != "docker":
    log("docker-push: skipped (docker-only; drives real docker push against the proxy)")
else:
    reg = serve()

    # Seed an image into the builtin registry (the "local store").
    registry_roundtrip(ref = "app:latest")
    host = dockerd_up()

    # (1) Bare push -> acknowledged against the builtin registry; the image is
    # already present in the local store, so the push succeeds with its digest.
    out = docker("-H", host, "push", "app:latest")
    assert_contains(out, "digest:", "bare push should report the manifest digest")
    log("✓ bare docker push acknowledged by the builtin registry")

    # (2) Registry-qualified push -> copied out of the builtin store to the named
    # (loopback = plain-HTTP) registry.
    upstream = upstream_registry()  # empty second registry
    docker("-H", host, "push", upstream + "/app:latest")
    landed = http(method = "GET", url = "http://" + upstream + "/v2/app/manifests/latest")
    assert_eq(landed["status"], 200, "qualified push should copy the image to the external registry")
    log("✓ qualified docker push copied the image out to the external registry")

    # (3) Pushing an image absent from the local store -> a docker error frame
    # (exec_tty, since it returns the exit code instead of aborting).
    res = exec_tty(argv = ["docker", "-H", host, "push", "ghost:latest"], timeout = "60s")
    assert_contains(res["output"], "does not exist locally", "unknown image should report 'does not exist locally'")
    log("✓ push of an unknown image reports 'does not exist locally'")
