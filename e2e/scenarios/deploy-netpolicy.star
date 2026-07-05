# Compose network isolation on kubernetes via the `networkpolicy` provider.
# A compose network with `driver_opts: {policy: "true"}` makes the netdriver
# append the networkpolicy provider, which emits ONE shared NetworkPolicy per
# network: it selects the network's member pods (cornus.net/<netLabel>) and
# allows ingress only from the same label — L3/L4 isolation on a flat pod net.
#
# kube-only. Runs on a PLAIN kind cluster, but kindnet does NOT enforce
# NetworkPolicy, so this asserts the OBJECT is correctly generated (shape:
# name, member podSelector, ingress-from-same, egress untouched) rather than
# live blocking — enforcement needs a policy CNI (Calico/Cilium), a separate
# cluster config (see the plan's E2E feasibility matrix row F). Both services
# still reach each other by name (the services DNS baseline is unaffected).
# NOT in the default SCENARIOS: it self-skips off kube, but keep it explicit.

NS = "cornus-e2e"

if TARGET != "kube":
    log("deploy-netpolicy: skipped (kube-only; NetworkPolicy generation)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-netpolicy.yaml", project = "npol", detach = True)
    wait(name = "npol-a", running = 1, timeout = "240s")
    wait(name = "npol-b", running = 1, timeout = "240s")

    # Exactly one cornus-managed NetworkPolicy for THIS project's isolated network.
    # Scope by the project name prefix (policies are named "<project>-<net>-<hash>")
    # so a policy leaked into the shared namespace by another scenario — cornus-e2e
    # is shared across scenarios, and a scenario that fails before its compose_down
    # leaves its managed policies behind — cannot make this count trip.
    names = kubectl("-n", NS, "get", "networkpolicy", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
    parts = [n for n in names.split(" ") if n.startswith("npol-")]
    assert_eq(len(parts), 1, "want exactly one managed NetworkPolicy for project npol, got %r" % names)
    np = parts[0]
    log("✓ NetworkPolicy generated for the isolated network: %s" % np)

    # It isolates INGRESS only (egress untouched so CoreDNS keeps resolving).
    ptypes = kubectl("-n", NS, "get", "networkpolicy", np, "-o", "jsonpath={.spec.policyTypes[*]}")
    assert_eq(ptypes, "Ingress", "policyTypes = %r, want just Ingress" % ptypes)

    # The podSelector and the ingress-from selector target the SAME member label
    # (cornus.net/<netLabel>): members accept traffic only from members.
    sel = kubectl("-n", NS, "get", "networkpolicy", np, "-o", "jsonpath={.spec.podSelector.matchLabels}")
    frm = kubectl("-n", NS, "get", "networkpolicy", np, "-o", "jsonpath={.spec.ingress[0].from[0].podSelector.matchLabels}")
    assert_contains(sel, "cornus.net/", "podSelector = %r, want a cornus.net/* member label" % sel)
    assert_eq(sel, frm, "ingress-from selector must equal the podSelector (same-network only)")
    log("✓ NetworkPolicy isolates the network to its own members (ingress-from == podSelector)")

    # DNS baseline is unaffected: b reaches a by bare name.
    out = pod_exec(app = "npol-b", cmd = "wget -qO- http://a 2>&1 || true")
    assert_contains(out, "nginx", "b could not reach a by name under the policy driver: %r" % out)
    log("✓ same-network name resolution still works alongside the policy")

    compose_down(file = "e2e/scenarios/deploy-netpolicy.yaml", project = "npol")

    # GC reaps THIS project's now-unreferenced NetworkPolicy. Scope the wait to the
    # npol- prefix so an unrelated leftover policy in the shared namespace can't keep
    # this loop spinning (or trip the final assertion) for something npol did not own.
    def npol_policies():
        out = kubectl("-n", NS, "get", "networkpolicy", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
        return [n for n in out.split(" ") if n.startswith("npol-")]

    for _ in range(30):
        if len(npol_policies()) == 0:
            break
        sleep(duration = "2s")
    assert_eq(len(npol_policies()), 0, "NetworkPolicy not reaped after down")
    log("✓ compose down reaped the NetworkPolicy (GC)")
