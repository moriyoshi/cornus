# Compose network isolation on kubernetes via the `cilium` provider. A compose
# network with `driver: cilium` makes the netdriver emit ONE shared
# CiliumNetworkPolicy per network: its endpointSelector and single ingress
# fromEndpoints both key on the network's member label (cornus.net/<netLabel>),
# so members accept traffic only from members — the Cilium-native counterpart of
# the `networkpolicy` provider. The services DNS baseline is unaffected.
#
# kube-only, and SELF-SKIPS unless the Cilium CRD is installed. kind ships no
# Cilium (and staging it hermetically is out of scope here — see the plan), so on
# the default plain cluster cornus's capability detection falls back to
# services-only and emits NO CNP; this scenario therefore skips. Its active path
# asserts the CNP OBJECT shape (like deploy-netpolicy.star does for NetworkPolicy)
# and is exercised on a real Cilium cluster. NOT meaningfully run in default CI.

NS = "cornus-e2e"

def cilium_present():
    out = kubectl("get", "crd", "ciliumnetworkpolicies.cilium.io", "--ignore-not-found", "-o", "name")
    return out.strip() != ""

if TARGET != "kube":
    log("deploy-cilium: skipped (kube-only; CiliumNetworkPolicy generation)")
elif not cilium_present():
    log("deploy-cilium: skipped (Cilium CRD not installed; needs a Cilium cluster)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-cilium.yaml", project = "cil", detach = True)
    wait(name = "cil-a", running = 1, timeout = "240s")
    wait(name = "cil-b", running = 1, timeout = "240s")

    # Exactly one cornus-managed CiliumNetworkPolicy for the network.
    names = kubectl("-n", NS, "get", "ciliumnetworkpolicies", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
    parts = [n for n in names.split(" ") if n]
    assert_eq(len(parts), 1, "want exactly one managed CiliumNetworkPolicy, got %r" % names)
    cnp = parts[0]
    log("✓ CiliumNetworkPolicy generated for the network: %s" % cnp)

    # The endpointSelector and the ingress fromEndpoints target the SAME member
    # label (cornus.net/<netLabel>): endpoints accept traffic only from members.
    sel = kubectl("-n", NS, "get", "ciliumnetworkpolicies", cnp, "-o", "jsonpath={.spec.endpointSelector.matchLabels}")
    frm = kubectl("-n", NS, "get", "ciliumnetworkpolicies", cnp, "-o", "jsonpath={.spec.ingress[0].fromEndpoints[0].matchLabels}")
    assert_contains(sel, "cornus.net/", "endpointSelector = %r, want a cornus.net/* member label" % sel)
    assert_eq(sel, frm, "ingress fromEndpoints selector must equal the endpointSelector (same-network only)")
    log("✓ CNP isolates the network to its own members (fromEndpoints == endpointSelector)")

    # DNS baseline is unaffected: b reaches a by bare name (same network).
    out = pod_exec(app = "cil-b", cmd = "wget -qO- http://a 2>&1 || true")
    assert_contains(out, "nginx", "b could not reach a by name on the cilium network: %r" % out)
    log("✓ same-network name resolution still works alongside the CNP")

    compose_down(file = "e2e/scenarios/deploy-cilium.yaml", project = "cil")
    # GC reaps the now-unreferenced CiliumNetworkPolicy.
    for _ in range(30):
        left = kubectl("-n", NS, "get", "ciliumnetworkpolicies", "-l", "cornus.managed=true", "-o", "name")
        if left.strip() == "":
            break
        sleep(duration = "2s")
    assert_eq(kubectl("-n", NS, "get", "ciliumnetworkpolicies", "-l", "cornus.managed=true", "-o", "name").strip(), "", "CNP not reaped after down")
    log("✓ compose down reaped the CiliumNetworkPolicy (GC)")
