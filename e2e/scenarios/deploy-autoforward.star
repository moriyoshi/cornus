# Automatic client-side forwarding of published ports: a `cornus deploy --server`
# session whose spec publishes a port must open a local listener on that host port
# for the session's lifetime, tunneling connections to the container through the
# server — no explicit `cornus port-forward` invocation. Target-agnostic, but note
# the docker-target caveat: dockerhost ALSO publishes the host port on the deploy
# host (the same machine here), so the client-side forwarder may skip on
# EADDRINUSE and the GET then rides Docker's own publish — still proving the port
# is reachable, but the kube target is the real proof (kubernetes previously
# dropped `host:` entirely; only the auto-forward makes it reachable there).
# The local target has no runtime backend, so it is skipped.

if TARGET == "local":
    log("deploy-autoforward: skipped (needs a real backend)")
else:
    serve()

    # Pick a free host port up front: the spec publishes it, and the deploy
    # session's auto-forward must bind it locally once the workload is ready.
    p = free_port()

    # nginx's default command already serves on :80 (no `command` override, which
    # keeps the scenario backend-agnostic).
    deploy_attach(name = "af", image = "nginx:alpine", ports = [p + ":80"], timeout = "240s")

    r = http_get(url = "http://127.0.0.1:" + p + "/", retry = "30s")
    assert_eq(r["status"], 200, "auto-forward GET status (got %r)" % r["status"])
    assert_contains(r["body"], "nginx", "auto-forward did not reach the container's :80, got %r" % r["body"])
    log("✓ published port auto-forwarded to 127.0.0.1:" + p + " by the deploy session")

    # A second connection over the same forward (each opens its own tunnel).
    r2 = http_get(url = "http://127.0.0.1:" + p + "/", retry = "10s")
    assert_eq(r2["status"], 200, "second auto-forward GET status (got %r)" % r2["status"])
    log("✓ concurrent connections over the auto-forward both served")

    # Ctrl-C the session: the workload is torn down AND the local listener is
    # released with it.
    attach_stop(name = "af")
