# NetworkPolicy ENFORCEMENT on kubernetes: proves the `networkpolicy` provider's
# per-network policies actually BLOCK cross-network traffic (not just that the
# object is well-formed — deploy-netpolicy.star covers the shape). Each network
# with `driver_opts: {policy: "true"}` gets a NetworkPolicy allowing ingress only
# from its own members (cornus.net/<netLabel>). So a service on netb accepts
# traffic from other netb members but not from a service on neta.
#
# kube-only. Requires a NetworkPolicy-ENFORCING CNI; kind's default kindnet
# (v1.31+, nftables enforcer) qualifies, which is what `make e2e-kube` and the
# dind runner both provision, so this runs on the plain cluster. On a
# non-enforcing CNI the cross-network case would not be blocked.

if TARGET != "kube":
    log("deploy-netpolicy-enforce: skipped (kube-only; NetworkPolicy enforcement)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-netpolicy-enforce.yaml", project = "npe", detach = True)
    wait(name = "npe-web", running = 1, timeout = "240s")
    wait(name = "npe-friend", running = 1, timeout = "240s")
    wait(name = "npe-stranger", running = 1, timeout = "240s")

    # Both managed NetworkPolicies exist (one per network).
    npc = kubectl("-n", "cornus-e2e", "get", "networkpolicy", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
    assert_eq(len([n for n in npc.split(" ") if n]), 2, "want two managed NetworkPolicies (one per network), got %r" % npc)

    # Same network: friend (netb) reaches web (netb). Retry to let the policy
    # program before asserting the positive path.
    ok = ""
    for _ in range(15):
        ok = pod_exec(app = "npe-friend", cmd = "wget -qO- --timeout=8 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
        if "nginx" in ok:
            break
        sleep(duration = "2s")
    assert_contains(ok, "nginx", "friend (same network) should reach web, got %r" % ok)
    log("✓ same-network traffic allowed (friend -> web)")

    # Cross network: stranger (neta) is BLOCKED from web (netb) by web's policy.
    denied = pod_exec(app = "npe-stranger", cmd = "wget -qO- --timeout=8 http://web 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(denied, "BLOCKED", "stranger (different network) must be BLOCKED from web, got %r" % denied)
    log("✓ cross-network traffic BLOCKED (stranger -/-> web) — NetworkPolicy enforced")

    compose_down(file = "e2e/scenarios/deploy-netpolicy-enforce.yaml", project = "npe")
    log("✓ NetworkPolicy enforcement proven end to end")
