# Compose user-network name resolution on kubernetes: the `services` DNS
# baseline. Every compose service joins the implicit `<project>_default`
# network, and the netdriver `services` provider emits a headless Service per
# alias (the compose service name), so `cli` resolves and reaches `srv` by its
# BARE name via CoreDNS — with srv publishing NO ports (the pre-networks
# backend only created a Service when ports were published).
#
# kube-only (CoreDNS + Services need a real cluster); other targets skip. Runs
# on a PLAIN kind cluster: the baseline needs no Multus / policy CNI. Validated
# 2026-07-03 inside the dind E2E runner (e2e/container, kind-in-dind).

NS = "cornus-e2e"  # KubeTarget's default namespace

def svc_gone(name, steps = 30):
    for _ in range(steps):
        out = kubectl("-n", NS, "get", "svc", name, "--ignore-not-found", "-o", "name")
        if name not in out:
            return True
        sleep(duration = "2s")
    return False

if TARGET != "kube":
    log("deploy-network: skipped (kube-only; services-DNS baseline needs CoreDNS)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-network.yaml", project = "dnet", detach = True)
    wait(name = "dnet-srv", running = 1, timeout = "240s")
    wait(name = "dnet-cli", running = 1, timeout = "240s")

    # The alias Service is headless (ClusterIP None) and exists despite srv
    # publishing no ports.
    ip = kubectl("-n", NS, "get", "svc", "srv", "-o", "jsonpath={.spec.clusterIP}")
    assert_eq(ip, "None", "alias Service srv should be headless (got clusterIP %r)" % ip)
    log("✓ headless alias Service srv exists (no published ports needed)")

    # cli reaches srv by its BARE compose service name: DNS resolves through
    # the headless Service to the pod IP, and nginx answers on 80.
    out = pod_exec(app = "dnet-cli", cmd = "wget -qO- http://srv 2>&1 || true")
    assert_contains(out, "nginx", "cli could not fetch nginx by bare name 'srv': %r" % out)
    log("✓ cli reached srv by bare service name (headless-Service DNS)")

    # The pod template carries the network membership label the policy/GC
    # machinery keys on.
    lbls = kubectl("-n", NS, "get", "deployment", "dnet-srv", "-o", "jsonpath={.spec.template.metadata.labels}")
    assert_contains(lbls, "cornus.net/", "pod template missing the network membership label: %r" % lbls)
    log("✓ pod template carries the cornus.net/* membership label")

    # Teardown: the alias Services are owner-ref'd to their Deployments, so
    # compose down cascades them away.
    compose_down(file = "e2e/scenarios/deploy-network.yaml", project = "dnet")
    assert_true(svc_gone("srv"), "alias Service srv survived compose down (ownerRef GC)")
    log("✓ compose down cascaded the alias Service away")

    # --- Duplicate TCP+UDP port-name regression --------------------------------
    # A service exposing the SAME container port on tcp AND udp used to produce
    # two ServicePorts with the identical Name ("p53"), which the API server
    # rejects (spec.ports[1].name: Duplicate value), failing the entire deploy.
    # The port Name must now include the protocol. Deploy such a service and
    # assert it actually comes up (the Service was accepted).
    # Source: pkg/deploy/kubernetes/internal/netdriver/services.go (WorkloadScoped)
    # and pkg/deploy/kubernetes/kubernetes.go (service).
    compose_up(file = "e2e/scenarios/deploy-port-dedup.yaml", project = "pdedup", detach = True)
    dd = wait(name = "pdedup-dns", running = 1, timeout = "240s")
    assert_eq(dd["running"], 1, "a tcp+udp same-port service must deploy (Service accepted with per-protocol port names)")
    log("✓ same container port on tcp+udp deploys (no duplicate Service port names)")
    compose_down(file = "e2e/scenarios/deploy-port-dedup.yaml", project = "pdedup")
