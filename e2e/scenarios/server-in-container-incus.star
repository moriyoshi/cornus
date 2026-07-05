# The cornus server CONTAINERIZED BESIDE incusd, on the incus host it manages.
#
# On this backend the containerized question is not about paths — incusd owns every
# path cornus ever hands it, so there is nothing to translate and nothing to bind
# but the daemon socket. It is entirely about ROUTING: instances are networked by
# incusd onto its own bridge in the HOST's network namespace, which a cornus in a
# container beside it cannot reach and cannot join (a cornus container is not an
# incus instance, so dockerhost's self-attach has no analogue).
#
# What this pins is that the preflight SAYS so. Before it did, a containerized incus
# operator's entire preflight output was a warning about PATHS whose remedy
# (CORNUS_HOST_PATH_MAP) changed nothing on this backend, while the consequence that
# does bite went unmentioned until a port-forward dial failed with a bare timeout.
#
# The runner container IS the topology: it is a container, co-located with the
# incusd it drives. So the assertion needs no image, no instance and no bind — just
# the preflight, run where the suite already runs.
#
# NOT covered here, deliberately, and stated so nobody reads this as full coverage:
# the OTHER containerized incus topology, cornus running AS an instance on the same
# daemon. That one has no routing problem at all (it sits on incusd's bridge
# alongside the workloads) and the preflight reports it as OK naming the instance.
# It was verified by hand — see the recipe in the guide and the note in TODO.md —
# and needs a cornus-embedding OCI image plus a proxy device for the incusd socket,
# which is more scaffolding than this arm can carry. Its logic is unit-covered in
# pkg/deploy/incushost/selfinspect_linux_test.go and pkg/hostcheck.

if TARGET != "incus":
    log("server-in-container-incus: skipped (incus-only)")
else:
    data_dir = temp_dir()
    pf = sh(cmd = "CORNUS_DEPLOY_BACKEND=incus CORNUS_DATA=" + data_dir + " " + CORNUS_BIN + " daemon preflight 2>&1")

    # The preflight must exit clean either way: nothing here is fatal. Deploys are
    # unaffected, and a `ports:` mapping still publishes on the host because incus
    # realizes it with a proxy device listening in the DAEMON's netns, not cornus's.
    assert_eq(pf["code"], 0, "nothing about a containerized incus server is fatal: " + pf["output"])

    if "runs in a container" not in pf["output"]:
        # `make e2e-incus` against a host incusd runs the harness as a host process,
        # which is not this topology at all. Say which case ran rather than assert
        # something false about it.
        log("server-in-container-incus: harness is not containerized here, so the beside-incusd topology is not exercised: " + pf["output"].split("\n")[0])
    else:
        assert_contains(pf["output"], "workload-routing", "a containerized incus server must be told about routing, not about paths")
        assert_contains(pf["output"], "no route to an instance's address", "the warning must name the actual consequence")

        # All three remedies, because which one an operator can take depends on their
        # deployment and naming only one would strand the others.
        for remedy in ["--network host", "incus instance", "CORNUS_INCUS_REMOTE"]:
            assert_contains(pf["output"], remedy, "the remedy list must include " + remedy)

        # And it must NOT be the old path warning, whose remedy does nothing here.
        assert_true("CORNUS_HOST_PATH_MAP" not in pf["output"], "the path-map warning is unactionable on incus (cornus hands incusd no path of its own) and must not be what an operator is told")
        log("✓ a containerized incus server is told about routing, with all three remedies, and not about paths it does not own")

        # --- the OTHER containerized incus topology: cornus AS an instance -----
        #
        # This is the one with no routing problem at all, and the reason it is worth
        # a live arm rather than unit tests alone: an instance sits on incusd's
        # bridge alongside its workloads, so every claim about "it can reach them
        # without host networking or a companion" is only really settled by reaching
        # one. serve_instance() cannot be serve_container(): the incus leg starts no
        # dockerd, and on an incus host the server's container has to be made BY
        # incus anyway.
        #
        # Needs an OCI image incusd can pull. Self-skips if it cannot get one, since
        # that is a network property of the runner rather than anything about cornus.
        base = getenv("CORNUS_E2E_INCUS_BASE_IMAGE", "docker.io/library/alpine:3.20")
        addr = serve_instance(image = base, name = "cornus-e2e-srv")

        # 1. The server must know it IS an instance. That is what suppresses the
        #    unreachable hint and what makes the routing verdict OK rather than a
        #    warning — and it comes from incusd, via the instance's own hostname.
        #
        #    Run INSIDE the instance: `daemon preflight` reports on the environment of
        #    the process running it, so the harness's own `cornus` builtin would
        #    describe the runner instead and pass for entirely the wrong reason.
        pf2 = sh(cmd = "incus exec cornus-e2e-srv --env CORNUS_DEPLOY_BACKEND=incus -- /usr/local/bin/cornus daemon preflight 2>&1")
        assert_eq(pf2["code"], 0, "preflight inside the server instance: " + pf2["output"])
        assert_contains(pf2["output"], "cornus-e2e-srv", "the server must identify its own incus instance by name")
        assert_contains(pf2["output"], "workload-routing", "the routing check must run for a containerized incus server")
        assert_true("no route to an instance's address" not in pf2["output"], "an instance is a peer of its workloads on incusd's bridge, so it must NOT be told it has no route")
        log("✓ the server identifies itself as an incus instance and the routing verdict is clean")

        # 2. The payload: a port-forward to a port the workload never published,
        #    dialed by a server that has neither host networking nor a companion.
        #    Asserting the preflight alone would only prove cornus BELIEVES it can
        #    route; this proves the bytes arrive. Same shape as
        #    deploy-portforward.star, whose image the incus backend already pulls.
        deploy(name = "inst-web", image = "nginx:alpine")
        wait(name = "inst-web", running = 1, timeout = "240s")
        local = port_forward(name = "inst-web", port = 80)
        r = http_get(url = "http://" + local + "/", retry = "30s")
        assert_eq(r["status"], 200, "port-forward from a server running AS an incus instance (got %r)" % r["status"])
        assert_contains(r["body"], "nginx", "port-forward reached something other than the workload's :80")
        log("✓ port-forward crossed from the harness, through a server that IS an instance, to a sibling instance")

        remove(name = "inst-web")
