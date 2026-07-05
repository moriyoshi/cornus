# Project-level `x-cornus-egress:` default (env mode): a service with no egress block
# of its own inherits the project default's proxy vars; a service that declares its
# own fully overrides it. Runs on docker/containerd/kube (env mode needs no relay).
# Also implicitly proves the `x-`-prefixed extension key is parsed at both levels.
#
# Source of truth: pkg/compose Project.Egress + Plan inheritance (translateEgress),
# pkg/clientproxy ApplyEgressEnv. Unit E2E: pkg/compose TestProjectEgress* tests.

if TARGET == "local":
    log("compose-egress-project: skipped (needs a runtime backend)")
else:
    addr = serve()
    compose_up(file = "e2e/scenarios/compose-egress-project.yaml", project = "egp", detach = True)
    wait(name = "egp-inherits", running = 1, timeout = "240s")
    wait(name = "egp-overrides", running = 1, timeout = "240s")
    log("✓ project-egress workloads Running")

    # The inheriting service carries the PROJECT default's proxy.
    inh = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "egp-inherits", "sh", "-c", "printenv HTTP_PROXY || echo MISSING"])
    assert_contains(inh["output"], "http://project-proxy.example:8080", "inheriting service must get the project default proxy, got %r" % inh["output"])
    log("✓ service with no egress inherits the project-level default")

    # The overriding service carries ITS OWN proxy, not the project default.
    ovr = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "egp-overrides", "sh", "-c", "printenv HTTP_PROXY || echo MISSING"])
    assert_contains(ovr["output"], "http://override-proxy.example:9090", "overriding service must get its own proxy, got %r" % ovr["output"])
    assert_true("project-proxy" not in ovr["output"], "override must NOT inherit the project default, got %r" % ovr["output"])
    log("✓ service with its own egress fully overrides the project default")

    compose_down(file = "e2e/scenarios/compose-egress-project.yaml", project = "egp")
    log("✓ project-level egress default + override proven end to end")
