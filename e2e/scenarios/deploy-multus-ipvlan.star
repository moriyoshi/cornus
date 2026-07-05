# Multus ipvlan attachment on kubernetes (netdriver `ipvlan` provider, overlaid
# mode). Same shape as deploy-multus.star, but the compose network uses
# `driver: ipvlan` with `driver_opts: {master: <parent NIC>}`, so the NAD's CNI
# config is the ipvlan plugin bound to the kind node's default interface
# (E2E_MULTUS_PARENT, default eth0). The compose planner still pins a
# deterministic static IP per service (static IPAM + network-selection
# annotation) and injects the caretaker DNS role, so named traffic between
# members rides the ipvlan secondary interfaces — two ipvlan slaves of the same
# parent are L2-switched inside the parent, no gateway needed.
#
# kube-only, and DOUBLY gated: it self-skips unless the Multus CRD is installed
# AND E2E_MULTUS_IPVLAN=1 — ipvlan against a nested-container parent NIC is
# genuinely environment-sensitive in kind/dind, so it must never break the
# default kube leg. Live attempt:
#   make e2e-container E2E_TARGETS=kube \
#     (with -e E2E_MULTUS=1 -e E2E_MULTUS_IPVLAN=1 on the docker run)

NS = "cornus-e2e"

# pinned_ip extracts the static user-net address from a deployment's
# network-selection annotation as kubectl-jsonpath renders it (the annotation
# value is a JSON string INSIDE the annotations map, so its quotes arrive
# backslash-escaped): ..."ips":["10.222.x.y/24"]... -> "10.222.x.y".
def pinned_ip(ann):
    s = ann.replace("\\", "")
    marker = '"ips":["'
    i = s.find(marker)
    if i < 0:
        return ""
    rest = s[i + len(marker):]
    j = rest.find("/")
    return rest[:j] if j >= 0 else ""

def multus_present():
    out = kubectl("get", "crd", "network-attachment-definitions.k8s.cni.cncf.io", "--ignore-not-found", "-o", "name")
    return out.strip() != ""

if TARGET != "kube":
    log("deploy-multus-ipvlan: skipped (kube-only; Multus ipvlan attachment)")
elif getenv(name = "E2E_MULTUS_IPVLAN", default = "") != "1":
    log("deploy-multus-ipvlan: skipped (set E2E_MULTUS_IPVLAN=1; ipvlan needs a cooperative parent NIC, fiddly in kind/dind)")
elif not multus_present():
    log("deploy-multus-ipvlan: skipped (Multus CRD not installed; run the dind runner with E2E_MULTUS=1)")
else:
    parent = getenv(name = "E2E_MULTUS_PARENT", default = "eth0")
    log("deploy-multus-ipvlan: parent interface %s" % parent)
    serve()
    compose_up(file = "e2e/scenarios/deploy-multus-ipvlan.yaml", project = "imesh", detach = True)

    # Reaching Running proves Multus performed the CNI ADD for the ipvlan NAD.
    wait(name = "imesh-a", running = 1, timeout = "240s")
    wait(name = "imesh-b", running = 1, timeout = "240s")
    log("✓ pods Running -> Multus wired the ipvlan secondary interface (CNI ADD succeeded)")

    # cornus created exactly one managed NAD for the network.
    nads = kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
    parts = [n for n in nads.split(" ") if n]
    assert_eq(len(parts), 1, "want exactly one managed NetworkAttachmentDefinition, got %r" % nads)
    nad = parts[0]
    log("✓ NetworkAttachmentDefinition generated: %s" % nad)

    # The NAD carries an ipvlan CNI config bound to the requested parent and
    # delegating to the `static` IPAM plugin (plan-time pinned addressing),
    # declaring the ips capability Multus needs to forward the annotation's
    # addresses.
    cfg = kubectl("-n", NS, "get", "net-attach-def", nad, "-o", "jsonpath={.spec.config}")
    assert_contains(cfg, "\"type\":\"ipvlan\"", "NAD config is not an ipvlan CNI config: %r" % cfg)
    assert_contains(cfg, "\"master\":\"%s\"" % parent, "NAD config does not bind the parent interface %s: %r" % (parent, cfg))
    assert_contains(cfg, "\"ipam\":{\"type\":\"static\"}", "NAD config does not delegate to static IPAM: %r" % cfg)
    assert_contains(cfg, "\"capabilities\":{\"ips\":true}", "NAD config does not declare the ips capability: %r" % cfg)
    log("✓ NAD is an ipvlan config on parent %s with static IPAM" % parent)

    # Each pod template is annotated to attach the NAD, pinning distinct static
    # user-net IPs (derived /24 inside 10.222.0.0/16).
    ann_a = kubectl("-n", NS, "get", "deployment", "imesh-a", "-o", "jsonpath={.spec.template.metadata.annotations}")
    assert_contains(ann_a, "k8s.v1.cni.cncf.io/networks", "pod template missing the Multus attach annotation: %r" % ann_a)
    assert_contains(ann_a, nad, "Multus annotation does not reference the NAD %s: %r" % (nad, ann_a))
    ip_a = pinned_ip(ann_a)
    ann_b = kubectl("-n", NS, "get", "deployment", "imesh-b", "-o", "jsonpath={.spec.template.metadata.annotations}")
    ip_b = pinned_ip(ann_b)
    if not ip_a.startswith("10.222.") or not ip_b.startswith("10.222."):
        fail("annotations do not pin static 10.222.x.y addresses: a=%r b=%r" % (ann_a, ann_b))
    if ip_a == ip_b:
        fail("imesh-a and imesh-b were pinned the same IP %s" % ip_a)
    log("✓ pod templates pin distinct static user-net IPs: a=%s b=%s" % (ip_a, ip_b))

    # The pod really has a second interface: Multus adds net1 as an ipvlan
    # slave of the parent, addressed EXACTLY at the pinned static IP. Poll,
    # since net1 can appear a beat after the pod first reports Running.
    net1 = ""
    for _ in range(30):
        net1 = pod_exec(app = "imesh-b", cmd = "ip -4 -o addr show net1 2>&1 || true")
        if "inet 10.222." in net1:
            break
        sleep(duration = "2s")
    assert_contains(net1, "inet 10.222.", "pod never got a net1 ipvlan interface (Multus attach failed): %r" % net1)
    assert_contains(net1, "inet %s/" % ip_b, "net1 address does not match the pinned static IP %s: %r" % (ip_b, net1))
    log("✓ running pod has a net1 ipvlan interface at its pinned static IP: %s" % net1.strip())

    # The caretaker DNS sidecar is injected and the pod's resolver rides it.
    inits = kubectl("-n", NS, "get", "deployment", "imesh-b", "-o", "jsonpath={.spec.template.spec.initContainers[*].name}")
    assert_contains(inits, "cornus-caretaker", "imesh-b has no caretaker DNS sidecar: %r" % inits)
    resolv = pod_exec(app = "imesh-b", cmd = "cat /etc/resolv.conf")
    assert_contains(resolv, "127.0.0.1", "imesh-b resolv.conf does not point at the caretaker DNS: %r" % resolv)

    # `a` resolves to its user-network (secondary) address — the pinned static
    # IP CoreDNS never publishes.
    res = pod_exec(app = "imesh-b", cmd = "nslookup a.%s.svc.cluster.local. 127.0.0.1 2>&1 || true" % NS)
    assert_contains(res, ip_a, "caretaker DNS did not answer a's user-net IP %s: %r" % (ip_a, res))
    log("✓ caretaker DNS resolves peer `a` to its user-net secondary IP %s" % ip_a)

    # And the data path follows the name: the resolved 10.222.x.y address
    # exists ONLY on the ipvlan interfaces, so a successful fetch necessarily
    # transited the ipvlan slaves (L2-switched inside the shared parent).
    out = pod_exec(app = "imesh-b", cmd = "wget -qO- http://a 2>&1 || true")
    assert_contains(out, "nginx", "b could not reach a by name over the ipvlan network: %r" % out)
    log("✓ named traffic between members rides the ipvlan user network")

    compose_down(file = "e2e/scenarios/deploy-multus-ipvlan.yaml", project = "imesh")
    # GC reaps the now-unreferenced NAD.
    for _ in range(30):
        left = kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "name")
        if left.strip() == "":
            break
        sleep(duration = "2s")
    assert_eq(kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "name").strip(), "", "NAD not reaped after down")
    log("✓ compose down reaped the NetworkAttachmentDefinition (GC)")
