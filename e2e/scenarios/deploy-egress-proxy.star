# Client-side egress mode "proxy" (kube-only): proves the caretaker forward proxy
# (HTTP + SOCKS5 on loopback, pointed to by the injected HTTP_PROXY) relays app
# egress by route. Same three-app default-route differential as the transparent
# scenario: `cluster` reaches the in-cluster `web` (dialed directly by the caretaker),
# `client` cannot (relayed to the out-of-cluster client), `deny` is dropped. Also
# confirms HTTP_PROXY is injected into the app container.
#
# kube-only (the cornus:e2e sidecar image the kube target loads). Source of truth:
# pkg/caretaker/egress.go (HTTP/SOCKS proxy), pkg/server/egress_relay.go,
# pkg/deploy/kubernetes egressProxyEnv/addEgressToCaretaker. Unit E2E: pkg/caretaker
# egress proxy tests, pkg/deploy/kubernetes TestEgressProxy*.

def reach(app, url, timeout):
    got = ""
    for _ in range(20):
        got = pod_exec(app = app, cmd = "wget -qO- --timeout=%s %s 2>&1 | grep -om1 nginx || echo BLOCKED" % (timeout, url))
        if "nginx" in got:
            break
        sleep(duration = "3s")
    return got

if TARGET != "kube":
    log("deploy-egress-proxy: skipped (kube-only; caretaker forward proxy + relay)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-egress-proxy.yaml", project = "epx", detach = True)
    wait(name = "epx-web", running = 1, timeout = "240s")
    wait(name = "epx-app-direct", running = 1, timeout = "240s")
    wait(name = "epx-app-relay", running = 1, timeout = "240s")
    wait(name = "epx-app-deny", running = 1, timeout = "240s")
    log("✓ proxy-egress workloads Running (caretaker forward proxy came up)")

    # The forward proxy is advertised to the app via HTTP_PROXY (http:// scheme).
    envout = pod_exec(app = "epx-app-direct", cmd = "printenv HTTP_PROXY || echo MISSING")
    assert_contains(envout, "http://127.0.0.1:15002", "app HTTP_PROXY should point at the caretaker proxy, got %r" % envout)
    log("✓ HTTP_PROXY points the app at the caretaker forward proxy")

    # cluster route: the caretaker dials web directly on the pod network.
    direct = reach("epx-app-direct", "http://web", "5")
    assert_contains(direct, "nginx", "cluster-route app should reach web via the proxy's direct dial, got %r" % direct)
    log("✓ cluster route dials directly and reaches web")

    # deny route: dropped by the proxy.
    denied = pod_exec(app = "epx-app-deny", cmd = "wget -qO- --timeout=5 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(denied, "BLOCKED", "deny-route app must be blocked from web, got %r" % denied)
    log("✓ deny route drops egress")

    # client route: relayed to the out-of-cluster client, which cannot reach the
    # cluster Service.
    relayed = pod_exec(app = "epx-app-relay", cmd = "wget -qO- --timeout=8 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(relayed, "BLOCKED", "client-route app must NOT reach the in-cluster web (relayed off-cluster), got %r" % relayed)
    log("✓ client route relays egress off-cluster")

    compose_down(file = "e2e/scenarios/deploy-egress-proxy.yaml", project = "epx")
    log("✓ client-side egress (proxy mode) proven end to end")
