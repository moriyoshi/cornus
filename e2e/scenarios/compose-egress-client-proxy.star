# G1: client-side egress dials through the CLIENT's OWN proxy. Beyond
# deploy-egress-proxy.star (which proves egress traverses the client at all), this
# proves the client-side egress BACKING dials each relayed destination through the
# client's own resolved proxy (a corporate/SASE proxy in the real world): we point the
# held client session's ALL_PROXY at an in-process harness SOCKS5 proxy, have the app
# egress a distinctive sentinel destination (default-routed `client`), and assert the
# harness proxy RECORDED that exact destination — which happens only if the client
# routed its dial through its own proxy rather than dialing direct. socks5h keeps DNS
# at the proxy (the air-gapped-faithful path), so the sentinel need not resolve.
#
# kube-only (the cornus:e2e caretaker sidecar). Source of truth: pkg/clientproxy
# (Dialer/DialerFor, socks5h) + pkg/deploywire serve.go (clientproxy.Dialer wired into
# the egress backing). Unit E2E: pkg/clientproxy dialer_test,
# pkg/deploywire TestServeEgressDialsThroughClientProxy.

SENTINEL = "g1-sentinel.example:80"

if TARGET != "kube":
    log("compose-egress-client-proxy: skipped (kube-only; caretaker forward proxy + relay)")
else:
    proxy = egress_proxy()  # in-process SOCKS5 on the harness loopback
    serve()

    # The HELD (foreground) client session serves the egress backing; env ALL_PROXY
    # makes its OWN egress dialer route through the harness proxy (socks5h = remote DNS).
    handle = compose_up_bg(
        file = "e2e/scenarios/compose-egress-client-proxy.yaml",
        project = "egcp",
        env = {"ALL_PROXY": "socks5h://" + proxy},
    )
    wait(name = "egcp-app", running = 1, timeout = "240s")
    log("✓ client-proxy egress workload Running")

    # Sanity: the app is pointed at the caretaker forward proxy.
    envout = pod_exec(app = "egcp-app", cmd = "printenv HTTP_PROXY || echo MISSING")
    assert_contains(envout, "http://127.0.0.1:15002", "app HTTP_PROXY should point at the caretaker proxy, got %r" % envout)

    # The app egresses a distinctive sentinel; routed `client`, it is relayed to the
    # held session, which must dial it through ALL_PROXY. Retry to allow propagation.
    hit = False
    for _ in range(20):
        pod_exec(app = "egcp-app", cmd = "wget -qO- --timeout=5 http://%s/ >/dev/null 2>&1 || true" % SENTINEL)
        if SENTINEL in egress_proxy_hits(addr = proxy):
            hit = True
            break
        sleep(duration = "3s")
    assert_true(hit, "the client did NOT dial %s through its own proxy; proxy saw %r" % (SENTINEL, egress_proxy_hits(addr = proxy)))
    log("✓ client dialed the relayed destination through its OWN proxy (G1)")

    compose_up_stop(handle = handle)
    log("✓ client-side egress via the client's own proxy proven end to end")
