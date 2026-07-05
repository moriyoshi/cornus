# Multus DETACHED mode on kubernetes (matrix row D). A NetworkAttachment with
# `default: true` makes the netdriver render the name-only
# `v1.multus-cni.io/default-network` annotation instead of the overlaid
# `k8s.v1.cni.cncf.io/networks` list: Multus REPLACES the pod's primary
# (cluster-CNI) interface with the named NAD, so the workload lives entirely on
# the user network. The detached contract this scenario asserts (all documented
# in pkg/deploy/kubernetes/internal/netdriver/multus.go and unit-tested against
# fakes — this is the live-validation leg):
#   - the NAD keeps HOST-LOCAL IPAM (dynamic addressing on the derived
#     10.222.x.0/24 subnet) — never static IPAM / the ips capability, and the
#     annotation is name-only: plan-time pinned IPs are not honoured detached;
#   - the pod's PRIMARY interface (eth0, and status.podIP with it) carries a
#     user-net address, and there is no net1 (nothing is overlaid);
#   - no caretaker DNS is injected (detached deploys carry no DNSSpec; the pod
#     resolver still points at the cluster DNS, which is UNREACHABLE off the
#     cluster net by design — a documented detached caveat, so no assert here
#     dials it);
#   - members reach each other by direct IP over the user network;
#   - deleting the last member GCs the shared NAD.
# (The companion guard — detached + client-local mounts rejected because the 9P
# relay rides the cluster network — stays unit-tested; exercising it live would
# need a foreground attach session.)
#
# Detached mode has no compose surface (the compose planner never sets
# `default: true`), so this drives the other producer of the API field: a raw
# DeploySpec via `cornus deploy -f <spec> --server ... --detach`, torn down with
# `--delete`.
#
# kube-only, and TRIPLE-gated like the ipvlan/macvlan variants: it self-skips
# unless the Multus CRD is installed AND E2E_MULTUS_DETACHED=1 — whether a
# nested kind/dind Multus honours the default-network override is genuinely
# environment-sensitive, so an unvalidated row must never break the default
# kube leg (E2E_MULTUS=1 already runs in CI). Live attempt:
#   make e2e-container E2E_TARGETS=kube E2E_MULTUS=1 E2E_MULTUS_DETACHED=1

NS = "cornus-e2e"

def multus_present():
    out = kubectl("get", "crd", "network-attachment-definitions.k8s.cni.cncf.io", "--ignore-not-found", "-o", "name")
    return out.strip() != ""

# nad_subnet extracts the host-local subnet prefix ("10.222.N.") from the NAD's
# CNI config JSON: ..."subnet":"10.222.N.0/24"...
def nad_subnet(cfg):
    marker = '"subnet":"'
    i = cfg.find(marker)
    if i < 0:
        return ""
    rest = cfg[i + len(marker):]
    j = rest.find("0/")
    return rest[:j] if j >= 0 else ""

if TARGET != "kube":
    log("deploy-multus-detached: skipped (kube-only; Multus detached-primary mode)")
elif getenv(name = "E2E_MULTUS_DETACHED", default = "") != "1":
    log("deploy-multus-detached: skipped (set E2E_MULTUS_DETACHED=1; replacing the primary interface is environment-sensitive in kind/dind)")
elif not multus_present():
    log("deploy-multus-detached: skipped (Multus CRD not installed; run the dind runner with E2E_MULTUS=1)")
else:
    srv = serve()
    work = temp_dir()

    # Raw DeploySpecs: two workloads whose ONLY network is a detached-primary
    # bridge NAD. --detach applies statelessly (no client session to hold).
    spec_a = work + "/dpri-a.yaml"
    write_file(path = spec_a, content = "name: dpri-a\nimage: nginx:alpine\nnetworks:\n  - name: dmesh\n    driver: bridge\n    default: true\n")
    spec_b = work + "/dpri-b.yaml"
    write_file(path = spec_b, content = 'name: dpri-b\nimage: alpine:3.20\ncommand: ["sleep", "3600"]\nnetworks:\n  - name: dmesh\n    driver: bridge\n    default: true\n')
    cornus("deploy", "-f", spec_a, "--server", "ws://" + srv, "--detach")
    cornus("deploy", "-f", spec_b, "--server", "ws://" + srv, "--detach")

    # Reaching Running proves Multus honoured the default-network override and
    # the bridge NAD's CNI ADD succeeded AS THE PRIMARY (a broken override
    # leaves the pod stuck ContainerCreating and times out legibly here).
    wait(name = "dpri-a", running = 1, timeout = "240s")
    wait(name = "dpri-b", running = 1, timeout = "240s")
    log("✓ pods Running -> Multus wired the detached-primary interface (CNI ADD succeeded)")

    # cornus created exactly one managed NAD for the network.
    nads = kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "jsonpath={.items[*].metadata.name}")
    parts = [n for n in nads.split(" ") if n]
    assert_eq(len(parts), 1, "want exactly one managed NetworkAttachmentDefinition, got %r" % nads)
    nad = parts[0]
    log("✓ NetworkAttachmentDefinition generated: %s" % nad)

    # Detached NAD shape: a bridge CNI config that KEEPS host-local IPAM on the
    # derived 10.222.x.0/24 subnet — detached addressing stays dynamic, so no
    # static IPAM and no ips capability.
    cfg = kubectl("-n", NS, "get", "net-attach-def", nad, "-o", "jsonpath={.spec.config}")
    assert_contains(cfg, "\"type\":\"bridge\"", "NAD config is not a bridge CNI config: %r" % cfg)
    assert_contains(cfg, "\"type\":\"host-local\"", "detached NAD must keep host-local IPAM: %r" % cfg)
    assert_contains(cfg, "\"subnet\":\"10.222.", "NAD does not use the derived 10.222.0.0/16 subnetting: %r" % cfg)
    assert_true("\"type\":\"static\"" not in cfg, "detached NAD must not delegate to static IPAM: %r" % cfg)
    assert_true("\"capabilities\"" not in cfg, "detached NAD must not declare the ips capability: %r" % cfg)
    subnet = nad_subnet(cfg)
    assert_true(subnet.startswith("10.222."), "cannot extract the host-local subnet from the NAD config: %r" % cfg)
    log("✓ NAD keeps host-local IPAM on the derived subnet %s0/24" % subnet)

    # The pod template carries the NAME-ONLY default-network annotation and no
    # overlaid networks list (nothing is pinned, nothing is secondary).
    ann_a = kubectl("-n", NS, "get", "deployment", "dpri-a", "-o", "jsonpath={.spec.template.metadata.annotations}")
    assert_contains(ann_a, "v1.multus-cni.io/default-network", "pod template missing the default-network annotation: %r" % ann_a)
    assert_contains(ann_a, nad, "default-network annotation does not reference the NAD %s: %r" % (nad, ann_a))
    assert_true("k8s.v1.cni.cncf.io/networks" not in ann_a, "detached pod must not also carry an overlaid networks annotation: %r" % ann_a)
    assert_true('"ips"' not in ann_a, "detached annotation must be name-only (no pinned ips): %r" % ann_a)
    log("✓ pod template carries the name-only default-network annotation")

    # The PRIMARY interface is the user network now: status.podIP (what kubelet
    # read back from the Multus default-network CNI result) is a user-net
    # address, eth0 carries it inside the pod, and there is no net1.
    ip_a = kubectl("-n", NS, "get", "pods", "-l", "cornus.app=dpri-a", "-o", "jsonpath={.items[0].status.podIP}").strip()
    assert_true(ip_a.startswith(subnet), "dpri-a podIP %r is not on the user-net subnet %s0/24 (pod not detached?)" % (ip_a, subnet))
    eth0 = pod_exec(app = "dpri-b", cmd = "ip -4 -o addr show eth0 2>&1 || true")
    assert_contains(eth0, "inet " + subnet, "dpri-b eth0 does not carry a user-net address (pod not detached?): %r" % eth0)
    ifaces = pod_exec(app = "dpri-b", cmd = "ip -4 -o addr show 2>&1 || true")
    assert_true("net1" not in ifaces, "detached pod must have no overlaid net1 interface: %r" % ifaces)
    log("✓ user network IS the primary: podIP a=%s, eth0 on %s0/24, no net1" % (ip_a, subnet))

    # No caretaker is injected for a detached deploy (no DNSSpec, no pinned
    # records): the pod resolver still points at the cluster DNS — which the
    # detached pod cannot reach, the documented row-D caveat — NOT at a
    # pod-local caretaker.
    inits = kubectl("-n", NS, "get", "deployment", "dpri-b", "-o", "jsonpath={.spec.template.spec.initContainers[*].name}")
    assert_true("cornus-caretaker" not in inits, "detached deploy must not inject a caretaker: %r" % inits)
    resolv = pod_exec(app = "dpri-b", cmd = "cat /etc/resolv.conf")
    assert_true("127.0.0.1" not in resolv, "detached pod resolv.conf must not point at a caretaker DNS: %r" % resolv)
    log("✓ no caretaker injected; resolver left on the (off-net) cluster DNS as documented")

    # Members reach each other by DIRECT IP over the user network — the only
    # network these pods share. Names are deliberately NOT exercised: detached
    # pods have no DNS path (row-D contract). -T 20 keeps a dead bridge from
    # hanging the run.
    out = pod_exec(app = "dpri-b", cmd = "wget -T 20 -qO- http://%s 2>&1 || true" % ip_a)
    assert_contains(out, "nginx", "dpri-b could not reach dpri-a at %s over the detached user network: %r" % (ip_a, out))
    log("✓ direct-IP data path between detached members rides the user network")

    # Teardown mirrors the stateless deploy; the last member's delete must GC
    # the shared NAD.
    cornus("deploy", "-f", spec_b, "--server", "ws://" + srv, "--delete")
    cornus("deploy", "-f", spec_a, "--server", "ws://" + srv, "--delete")
    for _ in range(30):
        left = kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "name")
        if left.strip() == "":
            break
        sleep(duration = "2s")
    assert_eq(kubectl("-n", NS, "get", "net-attach-def", "-l", "cornus.managed=true", "-o", "name").strip(), "", "NAD not reaped after the last member's delete")
    log("✓ deleting the last member reaped the NetworkAttachmentDefinition (GC)")
