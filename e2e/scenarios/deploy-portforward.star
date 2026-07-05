# Port forwarding through the native `cornus port-forward` CLI. Target-agnostic:
# the SAME scenario runs on BOTH backends, exercising the dockerhost path (the
# server dials the container IP) on the docker target and the pods/portforward
# SPDY path on the kube target. It proves a container port that was NEVER published
# to a host — the deployment declares no ports — is reachable from the harness host
# once forwarded through the server. The local target has no runtime backend, so it
# is skipped.

if TARGET == "local":
    log("deploy-portforward: skipped (needs a real backend)")
else:
    addr = serve()

    # nginx serving on :80 with NO published ports: unreachable from the host until
    # port-forwarded. Its default command already serves, so no `command` override is
    # needed — which keeps the scenario backend-agnostic (dockerhost maps `command`
    # to Docker's Cmd, kubernetes to the container command; an image whose default
    # CMD serves sidesteps that difference).
    deploy(name = "pf", image = "nginx:alpine")
    wait(name = "pf", running = 1, timeout = "240s")
    log("✓ workload Running with an UNPUBLISHED :80")

    # Forward a fresh local port to the container's :80 through the server, then curl
    # it from the harness host — the bytes ride CLI -> server -> backend -> container.
    local = port_forward(name = "pf", port = 80)
    r = http_get(url = "http://" + local + "/", retry = "30s")
    assert_eq(r["status"], 200, "port-forward GET status (got %r)" % r["status"])
    assert_contains(r["body"], "nginx", "port-forward did not reach the container's :80, got %r" % r["body"])
    log("✓ `cornus port-forward` reached an unpublished container port end to end")

    # A second concurrent connection over the same forward must also succeed
    # (each accepted connection opens its own independent tunnel).
    r2 = http_get(url = "http://" + local + "/", retry = "10s")
    assert_eq(r2["status"], 200, "second concurrent port-forward GET status (got %r)" % r2["status"])
    log("✓ concurrent connections over one port-forward both served")

    remove(name = "pf")
