# Automatic port-forward to an IN-CLUSTER cornus (svcforward), plus workload logs
# and port-forward through BOTH the direct-to-pod and server-routed transports.
# kube-only.
#
# Unlike the other scenarios, cornus does not run on the host here: it runs INSIDE
# the kind cluster as a Deployment + ClusterIP Service (image cornus:e2e, which
# prepare_kube loads). The CLI reaches it purely through a `port-forward` connection
# profile — no -H/--host, no published port — so this exercises the whole
# svcforward path: load KUBECONFIG, resolve the Service to a ready pod, SPDY-forward
# a local port, and speak the cornus API over it. A workload deployed through that
# tunnel, then observed with kubectl, proves the request truly landed on the
# in-cluster cornus.
#
# Registered in SCENARIOS but self-skips off the kube target. On kube it relies on
# the cornus:e2e image being loaded into kind (containerized e2e flow prepare_kube).

NS = "cornus-e2e"

if TARGET != "kube":
    log("incluster-portforward: skipped (kube-only; needs an in-cluster cornus Service)")
else:
    manifest = "e2e/scenarios/incluster-cornus.yaml"
    kubectl("-n", NS, "apply", "-f", manifest)
    # Wait until the in-cluster cornus is Ready (readinessProbe /readyz) so its
    # Service has a ready endpoint for svcforward to resolve.
    kubectl("-n", NS, "rollout", "status", "deploy/cornus-incluster", "--timeout=180s")
    log("✓ in-cluster cornus rolled out behind svc/cornus-incluster")

    # A profile that names the in-cluster Service and carries NO server URL. The
    # cornus() builtin runs on the kube target with KUBECONFIG in its env, so
    # svcforward reaches the cluster with the current kubeconfig context.
    workdir = temp_dir()
    cfg = workdir + "/config.yaml"
    env = {"CORNUS_CONFIG": cfg}
    cornus(
        "config", "set-context", "incluster",
        "--pf-namespace", NS, "--pf-service", "cornus-incluster", "--pf-remote-port", "5000",
        env = env,
    )
    cornus("config", "use-context", "incluster", env = env)

    # Deploy a workload THROUGH the port-forwarded in-cluster cornus: the argv has no
    # -H/--host, so the endpoint can only come from the port-forward profile.
    app = "e2e/scenarios/connection-profile-app.yaml"
    cornus("compose", "-f", app, "up", "-d", env = env)
    ps = cornus("compose", "-f", app, "ps", env = env)
    assert_contains(ps, "web", "compose ps via the port-forward profile should list the service")
    log("✓ CLI reached the in-cluster cornus via the automatic port-forward")

    # Cross-check on the cluster: the in-cluster cornus really created the workload
    # Deployment (a false-positive endpoint could not have done this). The Deployment
    # object exists as soon as the deploy is accepted, independent of image-pull time.
    deploys = kubectl("-n", NS, "get", "deploy", "-o", "name")
    assert_contains(deploys, "profile-web", "in-cluster cornus should have created the workload Deployment")
    log("✓ in-cluster cornus created the workload — svcforward confirmed end to end")

    # Now exercise workload logs & port-forward through BOTH transports for the SAME
    # cluster profile: the default direct-to-pod path (the CLI reaches the pod with
    # the developer kubeconfig over pods/portforward + pods/log) and the server-routed
    # path (CORNUS_VIA_SERVER=1 forces the request through the in-cluster cornus, whose
    # RBAC now grants those pod subresources). Both must reach the same nginx workload
    # (compose project "profile", service "web" -> resource "profile-web").
    res = "profile-web"
    env_via = {"CORNUS_CONFIG": cfg, "CORNUS_VIA_SERVER": "1"}

    # Port-forward to the workload's :80 (never published) — direct, then via-server.
    pf_direct = port_forward(name = res, port = 80, env = env)
    rd = http_get(url = "http://" + pf_direct + "/", retry = "30s")
    assert_eq(rd["status"], 200, "direct port-forward GET status (got %r)" % rd["status"])
    assert_contains(rd["body"], "nginx", "direct port-forward did not reach the workload :80")
    log("✓ workload port-forward reached :80 (direct-to-pod)")

    pf_via = port_forward(name = res, port = 80, env = env_via)
    rv = http_get(url = "http://" + pf_via + "/", retry = "30s")
    assert_eq(rv["status"], 200, "via-server port-forward GET status (got %r)" % rv["status"])
    assert_contains(rv["body"], "nginx", "via-server port-forward did not reach the workload :80")
    log("✓ workload port-forward reached :80 (server-routed)")

    # compose logs — direct, then via-server. nginx's entrypoint prints a stable
    # startup line to stdout, proving the workload's log stream reached us each way.
    logs_direct = cornus("compose", "-f", app, "logs", "web", env = env)
    assert_contains(logs_direct, "ready for start up", "direct compose logs did not stream the workload log")
    log("✓ compose logs streamed the workload log (direct-to-pod)")

    logs_via = cornus("compose", "-f", app, "logs", "web", env = env_via)
    assert_contains(logs_via, "ready for start up", "via-server compose logs did not stream the workload log")
    log("✓ compose logs streamed the workload log (server-routed)")

    cornus("compose", "-f", app, "down", env = env)
    kubectl("-n", NS, "delete", "-f", manifest, "--ignore-not-found")
