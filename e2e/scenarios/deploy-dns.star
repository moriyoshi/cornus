# Caretaker DNS role on kubernetes. A deploy with a `dns` spec makes cornus
# run a per-pod caretaker DNS resolver and point the pod's resolver at it: the
# named records are answered locally (to their user-network IPs — what CoreDNS
# cannot publish for Multus-secondary addresses), and every other name is
# forwarded to the cluster DNS. This proves the resolver end to end WITHOUT
# needing a Multus fabric: a synthetic record resolves to the injected IP, and a
# real cluster Service still resolves via forwarding.
#
# kube-only. Runs on the plain cluster; needs the cornus:e2e sidecar image the
# kube target already loads.

if TARGET != "kube":
    log("deploy-dns: skipped (kube-only; caretaker DNS resolver)")
else:
    serve()
    deploy(
        name = "dnstest",
        image = "alpine:3.20",
        command = ["sleep", "3600"],
        dns = {"peer-svc": "10.222.0.99"},
    )
    wait(name = "dnstest", running = 1, timeout = "240s")
    log("✓ workload Running (caretaker DNS sidecar + dnsConfig applied)")

    # The pod-level dnsConfig must reach the MAIN (app) container, not just the
    # sidecar: pod_exec runs in the app container, so its /etc/resolv.conf is the
    # app's. It must point at the local caretaker and keep the cluster search
    # domains (so bare names still expand to <ns>.svc.cluster.local).
    resolv = pod_exec(app = "dnstest", cmd = "cat /etc/resolv.conf")
    assert_contains(resolv, "nameserver 127.0.0.1", "app container resolv.conf must point at the caretaker, got %r" % resolv)
    assert_contains(resolv, "svc.cluster.local", "app container resolv.conf must keep the cluster search domains, got %r" % resolv)
    log("✓ app container /etc/resolv.conf points at the caretaker with cluster search domains")

    # A name in the caretaker's records resolves to the injected user-network IP —
    # authoritative, served locally. Retry to let the sidecar bind :53 on start.
    got = ""
    for _ in range(20):
        got = pod_exec(app = "dnstest", cmd = "nslookup peer-svc 2>&1 | grep -A2 -i name || echo PENDING")
        if "10.222.0.99" in got:
            break
        sleep(duration = "3s")
    assert_contains(got, "10.222.0.99", "caretaker must resolve the injected record peer-svc -> 10.222.0.99, got %r" % got)
    log("✓ injected record resolved locally by the caretaker (peer-svc -> 10.222.0.99)")

    # A name NOT in the records is forwarded to the cluster DNS: the kubernetes
    # Service still resolves, proving the forward path (not a black hole).
    fwd = pod_exec(app = "dnstest", cmd = "nslookup kubernetes.default.svc.cluster.local 2>&1 | grep -A2 -i name || echo NONE")
    assert_contains(fwd, "Address", "unknown names must forward to the cluster DNS (kubernetes.default should resolve), got %r" % fwd)
    log("✓ unknown name forwarded to the cluster DNS (kubernetes.default resolved)")

    log("✓ caretaker DNS resolver proven end to end (local records + upstream forwarding)")
