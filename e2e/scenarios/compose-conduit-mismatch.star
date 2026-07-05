# Reusing a detached Compose project with changed conduit settings emits a warning.
# Docker-only: the background agent owns the first project's conduit and preserves
# it when the second `up -d` requests a different configuration.

compose_file = "e2e/scenarios/compose-app.yaml"

if TARGET != "docker":
    log("compose-conduit-mismatch: skipped (docker-only)")
else:
    serve()
    p1 = free_port()
    p2 = free_port()
    compose_up(file = compose_file, project = "cmismatch", detach = True,
               conduit = "socks5://.shared:" + p1)
    wait(name = "cmismatch-web", running = 1, timeout = "180s")

    # Same project/agent, but a different conduit endpoint. The existing live
    # resources must be retained and the CLI should explain the mismatch.
    warning = compose_up(file = compose_file, project = "cmismatch", detach = True,
                         conduit = "socks5://.shared:" + p2)
    assert_contains(warning, "conduit", "expected a conduit mismatch warning (got %r)" % warning)
    assert_contains(warning, "compose down", "warning should suggest compose down (got %r)" % warning)
    log("✓ changed conduit settings emit a same-project reuse warning")

    compose_down(file = compose_file, project = "cmismatch")
