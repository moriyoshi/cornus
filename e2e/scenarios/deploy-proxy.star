# Enforcing egress proxy on kubernetes: proves the caretaker proxy ACTUALLY
# blocks cross-network traffic on a flat pod network, independent of the CNI.
# A `driver_opts: {proxy: "true"}` network makes cornus compute each member's
# same-network allow-set at plan time and inject, per pod: an init container that
# nftables-redirects (via netlink, no CLI) all app egress into a caretaker
# running as an exempt uid, which forwards a connection ONLY if its original
# destination resolves to an allow peer.
#
# web + client share the `mesh` proxy network; outsider is on a different plain
# network. So client -> web is allowed (same network), and client -> outsider is
# DENIED even though client can resolve outsider's IP via CoreDNS — the proxy
# drops it by destination IP. That is the enforcement NetworkPolicy also gives,
# but here purely in userspace (works even where NetworkPolicy is not enforced).
#
# kube-only. Runs on the plain cluster (no fabric); needs the cornus:e2e
# sidecar image the kube target already loads.

if TARGET != "kube":
    log("deploy-proxy: skipped (kube-only; enforcing egress proxy)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-proxy.yaml", project = "prx", detach = True)
    wait(name = "prx-web", running = 1, timeout = "240s")
    wait(name = "prx-client", running = 1, timeout = "240s")
    wait(name = "prx-outsider", running = 1, timeout = "240s")
    log("✓ proxied workloads Running (net-redirect init + caretaker sidecar came up)")

    # Same network: client reaches web THROUGH the proxy (allow peer). Retry to
    # let the caretaker's allow-set resolve web on first start.
    ok = ""
    for _ in range(20):
        ok = pod_exec(app = "prx-client", cmd = "wget -qO- --timeout=5 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
        if "nginx" in ok:
            break
        sleep(duration = "3s")
    assert_contains(ok, "nginx", "client should reach web (same proxy network) via the proxy, got %r" % ok)
    log("✓ same-network traffic forwarded by the proxy (client -> web)")

    # Cross network: outsider IS resolvable (has a headless Service), so this
    # proves the proxy denies by destination IP, not merely by name resolution.
    ip = kubectl("-n", "cornus-e2e", "get", "svc", "outsider", "-o", "jsonpath={.spec.clusterIP}")
    log("outsider resolvable (clusterIP=%s); the proxy must still block it" % ip)
    denied = pod_exec(app = "prx-client", cmd = "wget -qO- --timeout=5 http://outsider 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(denied, "BLOCKED", "client must be BLOCKED from outsider (different network), got %r" % denied)
    log("✓ cross-network traffic DENIED by the enforcing proxy (client -/-> outsider)")

    compose_down(file = "e2e/scenarios/deploy-proxy.yaml", project = "prx")
    log("✓ enforcing proxy proven end to end")
