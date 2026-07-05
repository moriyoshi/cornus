# Multus bridge attachment on kubernetes (netdriver `bridge` provider, overlaid
# mode). A compose network with `driver: bridge` makes cornus emit a
# NetworkAttachmentDefinition and annotate the pods so Multus wires a real
# secondary bridge interface. A pod reaching Running is the end-to-end signal:
# if the NAD were malformed or the CNI plugin missing, Multus would fail the
# CNI ADD and the pod would be stuck ContainerCreating.
#
# Overlaid semantics (matrix row A'): the compose planner pins a deterministic
# static IP per service on the network (NAD delegates to the `static` IPAM
# plugin; the pod annotation carries the address) and injects the caretaker DNS
# role with every peer's SECONDARY address — CoreDNS only ever publishes the
# PRIMARY cluster IP — so traffic between members BY NAME actually rides the
# user network, not just multi-homes beside it.
#
# kube-only, and SELF-SKIPS unless the Multus CRD is installed (the dind runner
# installs it when E2E_MULTUS=1; see e2e/container/entrypoint.sh — that staging
# includes the `static` IPAM plugin the pinned addressing delegates to).

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
    log("deploy-multus: skipped (kube-only; Multus bridge attachment)")
elif not multus_present():
    log("deploy-multus: skipped (Multus CRD not installed; run the dind runner with E2E_MULTUS=1)")
else:
    serve()
    compose_up(file = "e2e/scenarios/deploy-multus.yaml", project = "mesh", detach = True)

    # Reaching Running proves Multus performed the CNI ADD for the bridge NAD.
    wait(name = "mesh-a", running = 1, timeout = "240s")
    wait(name = "mesh-b", running = 1, timeout = "240s")
    log("✓ pods Running -> Multus wired the bridge secondary interface (CNI ADD succeeded)")

    # cornus created exactly one managed NAD for the network.
    nads = kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
    parts = [n for n in nads.split(" ") if n]
    assert_eq(len(parts), 1, "want exactly one managed NetworkAttachmentDefinition, got %r" % nads)
    nad = parts[0]
    log("✓ NetworkAttachmentDefinition generated: %s" % nad)

    # The NAD carries a bridge CNI config delegating to the `static` IPAM
    # plugin (plan-time pinned addressing), declaring the ips capability Multus
    # needs to forward the annotation's addresses.
    cfg = kubectl("-n", NS, "get", "net-attach-def", nad, "-o", "jsonpath={.spec.config}")
    assert_contains(cfg, "\"type\":\"bridge\"", "NAD config is not a bridge CNI config: %r" % cfg)
    assert_contains(cfg, "\"ipam\":{\"type\":\"static\"}", "NAD config does not delegate to static IPAM: %r" % cfg)
    assert_contains(cfg, "\"capabilities\":{\"ips\":true}", "NAD config does not declare the ips capability: %r" % cfg)
    log("✓ NAD delegates to the static IPAM plugin (deterministic per-service addressing)")

    # Each pod template is annotated to attach the NAD (overlaid secondary),
    # in the JSON selection form pinning the service's static user-net IP.
    ann_a = kubectl("-n", NS, "get", "deployment", "mesh-a", "-o", "jsonpath={.spec.template.metadata.annotations}")
    assert_contains(ann_a, "k8s.v1.cni.cncf.io/networks", "pod template missing the Multus attach annotation: %r" % ann_a)
    assert_contains(ann_a, nad, "Multus annotation does not reference the NAD %s: %r" % (nad, ann_a))
    ip_a = pinned_ip(ann_a)
    ann_b = kubectl("-n", NS, "get", "deployment", "mesh-b", "-o", "jsonpath={.spec.template.metadata.annotations}")
    ip_b = pinned_ip(ann_b)
    if not ip_a.startswith("10.222.") or not ip_b.startswith("10.222."):
        fail("annotations do not pin static 10.222.x.y addresses: a=%r b=%r" % (ann_a, ann_b))
    if ip_a == ip_b:
        fail("mesh-a and mesh-b were pinned the same IP %s" % ip_a)
    log("✓ pod templates pin distinct static user-net IPs: a=%s b=%s" % (ip_a, ip_b))

    # The pod really has a SECOND interface now: Multus adds net1 on the bridge.
    # Checking the live interface from inside the pod is the most direct proof
    # the CNI ADD attached it (and sidesteps kubectl jsonpath's
    # dotted-annotation-key escaping). Poll, since net1 can appear a beat after
    # the pod first reports Running. Static IPAM means the address is not
    # merely in-subnet but EXACTLY the pinned one.
    net1 = ""
    for _ in range(30):
        net1 = pod_exec(app = "mesh-b", cmd = "ip -4 -o addr show net1 2>&1 || true")
        if "inet 10.222." in net1:
            break
        sleep(duration = "2s")
    assert_contains(net1, "inet 10.222.", "pod never got a net1 bridge interface (Multus attach failed): %r" % net1)
    assert_contains(net1, "inet %s/" % ip_b, "net1 address does not match the pinned static IP %s: %r" % (ip_b, net1))
    log("✓ running pod has a net1 bridge interface at its pinned static IP: %s" % net1.strip())

    # The caretaker DNS sidecar is injected and the pod's resolver rides it.
    inits = kubectl("-n", NS, "get", "deployment", "mesh-b", "-o", "jsonpath={.spec.template.spec.initContainers[*].name}")
    assert_contains(inits, "cornus-caretaker", "mesh-b has no caretaker DNS sidecar: %r" % inits)
    resolv = pod_exec(app = "mesh-b", cmd = "cat /etc/resolv.conf")
    assert_contains(resolv, "127.0.0.1", "mesh-b resolv.conf does not point at the caretaker DNS: %r" % resolv)

    # Row A' payoff: `a` resolves to its SECONDARY (user-network) address — the
    # pinned static IP, which CoreDNS never publishes — so named traffic rides
    # the user network. Query the search-expanded FQDN explicitly to keep the
    # assert independent of busybox nslookup's search-domain behaviour.
    res = pod_exec(app = "mesh-b", cmd = "nslookup a.%s.svc.cluster.local. 127.0.0.1 2>&1 || true" % NS)
    assert_contains(res, ip_a, "caretaker DNS did not answer a's user-net IP %s: %r" % (ip_a, res))
    log("✓ caretaker DNS resolves peer `a` to its user-net secondary IP %s" % ip_a)

    # And the data path follows the name: the fetch works even though the
    # resolved 10.222.x.y address exists ONLY on the user bridge, so the
    # connection necessarily transited the secondary interface.
    out = pod_exec(app = "mesh-b", cmd = "wget -qO- http://a 2>&1 || true")
    assert_contains(out, "nginx", "b could not reach a by name over the user network: %r" % out)
    log("✓ named traffic between members rides the user network (matrix row A')")

    compose_down(file = "e2e/scenarios/deploy-multus.yaml", project = "mesh")
    # GC reaps the now-unreferenced NAD.
    for _ in range(30):
        left = kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "name")
        if left.strip() == "":
            break
        sleep(duration = "2s")
    assert_eq(kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "name").strip(), "", "NAD not reaped after down")
    log("✓ compose down reaped the NetworkAttachmentDefinition (GC)")
