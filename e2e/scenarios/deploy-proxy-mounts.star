# Enforcing egress proxy TOGETHER WITH a client-local mount on kubernetes —
# proves the two combine in ONE privileged caretaker, exempted from the egress
# redirect by a firewall MARK (it must run as root for the mount, so it cannot be
# exempted by uid as the proxy-only path is).
#
# pmclient is on a `driver_opts: {proxy: "true"}` network (all egress redirected
# into its caretaker) AND has a client-local bind mount served over 9P. The mount
# coming up live is the key proof: the caretaker's 9P relay dial has to escape the
# redirect, which only works if the SO_MARK exemption is programmed correctly.
# Then same-network reach (pmweb) still works and cross-network (pmoutsider) is
# still DENIED — the proxy enforces exactly as in deploy-proxy.star.
#
# kube-only. Runs on the plain cluster; needs the cornus:e2e sidecar image the
# kube target already loads (also the mount-agent/9p-relay image).

if TARGET != "kube":
    log("deploy-proxy-mounts: skipped (kube-only; enforcing proxy + client-local mount)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-proxy-mounts.yaml", project = "pmx", detach = True)
    wait(name = "pmx-pmweb", running = 1, timeout = "240s")
    wait(name = "pmx-pmclient", running = 1, timeout = "240s")
    wait(name = "pmx-pmoutsider", running = 1, timeout = "240s")
    log("✓ proxy+mount workloads Running (net-redirect init + ONE privileged caretaker)")

    # The mount is live INSIDE the proxied pod: the caretaker's 9P relay dial
    # escaped the redirect via the firewall mark. This is the core proxy+mounts proof.
    got = pod_exec(app = "pmx-pmclient", cmd = "cat /data/marker 2>&1 || echo NOMOUNT")
    assert_contains(got, "PROXY-MOUNT-OK", "mount must be live in the proxied pod (mark exemption lets the 9P relay through), got %r" % got)
    log("✓ client-local mount live inside the proxied pod (mark-exempted 9P relay)")

    # Same network: pmclient reaches pmweb THROUGH the proxy (allow peer). Retry
    # to let the caretaker's allow-set resolve pmweb on first start.
    ok = ""
    for _ in range(20):
        ok = pod_exec(app = "pmx-pmclient", cmd = "wget -qO- --timeout=5 http://pmweb 2>&1 | grep -om1 nginx || echo BLOCKED")
        if "nginx" in ok:
            break
        sleep(duration = "3s")
    assert_contains(ok, "nginx", "pmclient should reach pmweb (same proxy network) via the proxy, got %r" % ok)
    log("✓ same-network traffic forwarded by the proxy (pmclient -> pmweb)")

    # Cross network: pmoutsider is resolvable but must be BLOCKED by destination IP.
    denied = pod_exec(app = "pmx-pmclient", cmd = "wget -qO- --timeout=5 http://pmoutsider 2>&1 | grep -om1 nginx || echo BLOCKED")
    assert_contains(denied, "BLOCKED", "pmclient must be BLOCKED from pmoutsider (different network), got %r" % denied)
    log("✓ cross-network traffic DENIED by the enforcing proxy (pmclient -/-> pmoutsider)")

    compose_down(file = "e2e/scenarios/deploy-proxy-mounts.yaml", project = "pmx")
    log("✓ enforcing proxy + client-local mount proven end to end")
