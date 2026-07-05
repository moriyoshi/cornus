# Client-side egress mode "transparent" (kube-only): proves the nftables-redirect +
# SO_ORIGINAL_DST caretaker relays app egress by route. Three apps differ only in
# their DEFAULT route, so reaching the SAME in-cluster `web` discriminates the
# routes: `cluster` reaches it (direct from the pod network), `client` cannot (the
# traffic is relayed to the out-of-cluster harness/client, which has no route to the
# cluster Service — proving it physically left the cluster), and `deny` is dropped.
#
# kube-only (nftables NET_ADMIN init + the cornus:e2e sidecar image the kube target
# loads). Source of truth: pkg/caretaker/egress_transparent.go, pkg/server/egress_relay.go,
# pkg/deploy/kubernetes addEgressToCaretaker. Unit E2E: pkg/server egress relay tests,
# pkg/deploy/kubernetes TestEgressTransparent*.

def reach(app, url):
    # CAN-reach with retry (let the caretaker + redirect settle on first start).
    got = ""
    for _ in range(20):
        got = pod_exec(app = app, cmd = "wget -qO- --timeout=5 %s 2>&1 | grep -om1 nginx || echo BLOCKED" % url)
        if "nginx" in got:
            break
        sleep(duration = "3s")
    return got

if TARGET != "kube":
    log("deploy-egress-transparent: skipped (kube-only; nftables redirect + relay)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-egress-transparent.yaml", project = "etr", detach = True)
    wait(name = "etr-web", running = 1, timeout = "240s")
    wait(name = "etr-app-direct", running = 1, timeout = "240s")
    wait(name = "etr-app-relay", running = 1, timeout = "240s")
    wait(name = "etr-app-deny", running = 1, timeout = "240s")
    log("✓ transparent-egress workloads Running (net-redirect init + caretaker came up)")

    # cluster route: reaches web directly on the pod network.
    direct = reach("etr-app-direct", "http://web")
    assert_contains(direct, "nginx", "cluster-route app should reach web directly, got %r" % direct)
    log("✓ cluster route egresses directly and reaches web")

    # deny route: dropped.
    denied = pod_exec(app = "etr-app-deny", cmd = "wget -qO- --timeout=5 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(denied, "BLOCKED", "deny-route app must be blocked from web, got %r" % denied)
    log("✓ deny route drops egress")

    # client route: relayed to the out-of-cluster client, which cannot reach the
    # cluster Service — proving the traffic left the cluster.
    relayed = pod_exec(app = "etr-app-relay", cmd = "wget -qO- --timeout=8 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(relayed, "BLOCKED", "client-route app must NOT reach the in-cluster web (relayed off-cluster), got %r" % relayed)
    log("✓ client route relays egress off-cluster (in-cluster Service unreachable via the client)")

    compose_down(file = "e2e/scenarios/deploy-egress-transparent.yaml", project = "etr")
    log("✓ client-side egress (transparent mode) proven end to end")
