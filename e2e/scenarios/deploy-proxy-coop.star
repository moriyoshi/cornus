# Cooperative (no-privilege) egress proxy on kubernetes: proves the LIGHT proxy
# mode works with no NET_ADMIN, no iptables redirect, and no special uid.
#
# A `driver_opts: {proxy: "true", mode: cooperative}` network makes cornus,
# per pod: (a) inject hostAliases pointing each same-network peer's name at a
# distinct 127/8 loopback address, and (b) run a plain caretaker sidecar that
# listens on each (loopback, declared-port) and forwards to the peer's REAL
# headless Service. So the app dialing `coweb:80` actually hits the local
# sidecar (coweb resolves to a loopback only the sidecar serves) which splices it
# to coweb — proving the sidecar is in the data path with zero privilege.
#
# Cooperative isolation is intentionally SOFT: a peer NOT on a shared network
# (cooutsider) has no hostAlias, so the app resolves it via CoreDNS and connects
# directly — it is NOT blocked. That is the documented trade-off vs enforcing
# mode (deploy-proxy.star), which captures ALL egress by IP.
#
# kube-only. Runs on the plain cluster (no fabric); needs the cornus:e2e
# sidecar image the kube target already loads.

if TARGET != "kube":
    log("deploy-proxy-coop: skipped (kube-only; cooperative egress proxy)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-proxy-coop.yaml", project = "cpx", detach = True)
    wait(name = "cpx-coweb", running = 1, timeout = "240s")
    wait(name = "cpx-coclient", running = 1, timeout = "240s")
    wait(name = "cpx-cooutsider", running = 1, timeout = "240s")
    log("✓ cooperative workloads Running (no privileged init container needed)")

    # The peer's name is pinned to a loopback address by the injected hostAlias
    # (only the sidecar serves it) — the mechanism that puts the sidecar in path.
    hosts = pod_exec(app = "cpx-coclient", cmd = "grep -w coweb /etc/hosts || echo NONE")
    assert_contains(hosts, "127.0.", "coweb should resolve to a loopback via hostAliases, got %r" % hosts)
    log("✓ peer name pinned to a loopback the sidecar owns (%s)" % hosts.strip())

    # Same network: coclient reaches coweb THROUGH the sidecar. Success proves the
    # sidecar forwarded (coweb only resolves to a loopback nothing else listens on).
    # Retry to let the sidecar bind its loopback listeners on first start.
    ok = ""
    for _ in range(20):
        ok = pod_exec(app = "cpx-coclient", cmd = "wget -qO- --timeout=5 http://coweb 2>&1 | grep -om1 nginx || echo BLOCKED")
        if "nginx" in ok:
            break
        sleep(duration = "3s")
    assert_contains(ok, "nginx", "coclient should reach coweb via the cooperative sidecar, got %r" % ok)
    log("✓ same-network traffic forwarded by the cooperative sidecar (coclient -> coweb)")

    # Cross network: cooperative isolation is SOFT, so cooutsider stays reachable
    # (no hostAlias, resolved directly via CoreDNS). This documents the trade-off.
    reach = pod_exec(app = "cpx-coclient", cmd = "wget -qO- --timeout=5 http://cooutsider 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(reach, "nginx", "cooperative mode is soft: cooutsider stays directly reachable, got %r" % reach)
    log("✓ cross-network reachable (documented soft-isolation trade-off vs enforcing)")

    compose_down(file = "e2e/scenarios/deploy-proxy-coop.yaml", project = "cpx")
    log("✓ cooperative proxy proven end to end")
