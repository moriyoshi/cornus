# cornus port-forward (and, by the same ForwardPort bridge, cornus tunnel)
# rerouted through the always-on remote companion (CORNUS_DOCKER_REMOTE),
# for a deployment that declares NO client-local --mount at all — proving the
# companion is created (and ForwardPort rerouted through it) purely because
# the backend is in remote mode, not because of any mount. Without this
# rerouting, ForwardPort would try to dial the container's bridge IP directly
# from the server process and simply fail once the server is not co-located
# with the Docker daemon it drives (see ARCHITECTURE.md "Port forwarding").
#
# Needs: TARGET == "docker", a prebuilt cornus-embedding agent image
# (CORNUS_AGENT_IMAGE) and privileged Docker (the companion runs Privileged:
# true) — self-skips otherwise, mirroring deploy-mounts-sidecar-docker.star.

agent_image = getenv("CORNUS_AGENT_IMAGE", "")

if TARGET != "docker":
    log("deploy-remote-portforward-docker: skipped (docker-only; exercises dockerhost's remote-companion ForwardPort reroute)")
elif agent_image == "":
    log("deploy-remote-portforward-docker: skipped (set CORNUS_AGENT_IMAGE to a prebuilt cornus-embedding image, e.g. cornus:e2e)")
else:
    serve(env = {
        "CORNUS_DOCKER_REMOTE": "1",
        "CORNUS_AGENT_IMAGE": agent_image,
    })

    # nginx serving on :80 with NO published ports and NO --mount at all: the
    # remote companion must still be created (mount-less) and ForwardPort must
    # still reroute through it.
    deploy(name = "pf-remote", image = "nginx:alpine")
    wait(name = "pf-remote", running = 1, timeout = "240s")
    log("✓ workload Running with an UNPUBLISHED :80, no --mount")

    caretaker_ids = docker("ps", "-a", "--filter", "label=cornus.app=pf-remote", "--filter", "label=cornus.role=mount-caretaker", "--format", "{{.ID}}")
    assert_true(len(caretaker_ids.strip()) > 0, "expected an always-on remote companion even with no --mount")
    log("✓ remote companion present despite no client-local mounts")

    # Forward a fresh local port to the container's :80 through the server —
    # this can ONLY succeed via the companion reroute in remote mode, since the
    # server has no direct route to the daemon's bridge network.
    local = port_forward(name = "pf-remote", port = 80)
    r = http_get(url = "http://" + local + "/", retry = "30s")
    assert_eq(r["status"], 200, "port-forward GET status (got %r)" % r["status"])
    assert_contains(r["body"], "nginx", "port-forward did not reach the container's :80, got %r" % r["body"])
    log("✓ `cornus port-forward` reached an unpublished container port via the remote companion")

    remove(name = "pf-remote")
