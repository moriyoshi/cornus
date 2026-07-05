# Client-side egress mode "env" (all runtime backends): proves the caller's proxy
# settings are propagated into the container as HTTP(S)_PROXY / NO_PROXY, and that a
# `cluster`-route rule folds its pattern into NO_PROXY (so in-cluster traffic
# bypasses the proxy). No relay, no sidecar — the client injects the vars into
# spec.Env at deploy time (clientproxy.ApplyEgressEnv). Runs on docker/containerd/kube.
#
# Source of truth: pkg/clientproxy/egress.go (ApplyEgressEnv), pkg/egresspolicy
# (NoProxyPatterns), pkg/compose egress translation. Unit E2E: pkg/clientproxy tests.

if TARGET == "local":
    log("compose-egress-env: skipped (needs a runtime backend)")
else:
    addr = serve()
    compose_up(file = "e2e/scenarios/compose-egress-env.yaml", project = "egr", detach = True)
    wait(name = "egr-app", running = 1, timeout = "240s")
    log("✓ egress env-mode workload Running")

    # The injected proxy vars must be visible in the container environment.
    http = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "egr-app", "sh", "-c", "printenv HTTP_PROXY || echo MISSING"])
    assert_contains(http["output"], "http://proxy.example:8080", "HTTP_PROXY not injected, got %r" % http["output"])
    log("✓ HTTP_PROXY propagated into the container")

    # NO_PROXY carries the explicit value AND the cluster-route rule pattern.
    noproxy = exec_tty(argv = ["cornus", "exec", "--server", "http://" + addr, "egr-app", "sh", "-c", "printenv NO_PROXY || echo MISSING"])
    assert_contains(noproxy["output"], "10.0.0.0/8", "NO_PROXY lost its explicit value, got %r" % noproxy["output"])
    assert_contains(noproxy["output"], "*.svc.cluster.local", "cluster-route rule not folded into NO_PROXY, got %r" % noproxy["output"])
    log("✓ NO_PROXY carries the explicit entries and the cluster-route pattern")

    compose_down(file = "e2e/scenarios/compose-egress-env.yaml", project = "egr")
    log("✓ client-side egress (env mode) proven end to end")
