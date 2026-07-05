# Compose User Networks and the Caretaker Sidecar

## Summary

Compose `networks:` are fully supported on both backends: services reach each other by bare name, and per-network isolation is available via a pluggable Kubernetes provider pipeline (`netdriver`) offering services-DNS, Multus CNI attachments (bridge/ipvlan/macvlan, overlaid or detached, with plan-time static IPs and secondary-IP DNS), NetworkPolicy, Cilium CNP, and two userspace proxy modes. On the pod side, a single multi-role "caretaker" sidecar hosts 9P mounts, the egress proxy, and a per-pod DNS resolver. The whole user-networks validation matrix (bridge/ipvlan/macvlan overlaid + detached) is live-validated in the dind E2E runner; the sole exception is cross-node macvlan (environment-sensitive, permanently gated, no plan to validate in dind).

Note on paths: the packages were later relocated from `internal/` to `pkg/`; all paths below are the current ones (e.g. `pkg/deploy/kubernetes/internal/netdriver/`, `pkg/caretaker/`).

## Key Facts

- `api.NetworkAttachment{Name, Driver, DriverOpts, Aliases, Default}` + `DeploySpec.Networks` carry network membership through compose -> dockerproxy -> both backends. `Driver` selects the k8s provider pipeline ("" => `CORNUS_K8S_NET_DRIVER` env, default `services`); `Default` marks the Multus detached-primary mode (at most one).
- Compose contract: every service joins the implicit `<project>_default` network when it lists none; network name resolution mirrors volumes (`name:` override > `external:` literal > `<project>_<net>`); aliases = [service, container_name, declared...] deduped. `Service.Networks` accepts both list and map-with-aliases forms.
- The k8s side is a provider pipeline (`Provider` interface: NetworkScoped/WorkloadScoped/MutatePod/Requires; providers are pure builders, the `Engine` does all I/O). New fabrics are data, not control flow — the `cilium` provider landed as ~50 lines + a pipeline entry + one GC line, zero backend changes.
- Pod membership is stamped as `cornus.net/<netLabel>` labels; shared network objects (NADs, NetworkPolicies, CNPs) are reaped by mark-and-sweep GC keyed on those labels, called from `Backend.Delete`.
- Multus attachments get plan-time deterministic static IPs (`pkg/compose/usernet.go`; `ipv4_address` overrides; dynamic fallback for `replicas>1`), and the caretaker DNS role serves those secondary IPs so named traffic provably rides the user network (`api.DNSSpec.RequireUserNet`).
- One caretaker sidecar per pod, ever. Roles (mounts, proxy, DNS) compose into `caretaker.Config` delivered via the `CORNUS_CARETAKER_CONFIG` env var (JSON, no ConfigMap). `CORNUS_K8S_SIDECAR_IMAGE` decouples the sidecar image from the app image.
- The enforcing proxy's control plane is computed at COMPOSE-PLAN time (`Project.applyProxyPolicy` sees the whole topology), not in the per-service backend Apply — this was the insight that made the proxy tractable.
- Redirect exemption identity: proxy-only = dedicated uid 1337 (Istio-style); proxy+mounts (root caretaker) = firewall mark via `SO_MARK` on EVERY caretaker socket including the 9P WebSocket relay dial (`wire.DialConnControl`). A uid==0 exemption rule is never programmed.
- dockerhost maps k8s pseudo-drivers (`services`/`policy`/`cilium`) to Docker's default bridge (they are not Docker drivers and would 500); real drivers pass through. So one compose file works unchanged on both backends.
- The dind runner (`e2e/container/`) is THE validation vehicle; every real bug in this area was live-only (fakes passed).

## Details

### Provider/driver matrix (Kubernetes)

`pipelineFor(driver, net)` in `pkg/deploy/kubernetes/internal/netdriver/netdriver.go` (attachment-aware):

| Compose `driver` | Pipeline | Mechanism | Isolation | Capability gate |
|---|---|---|---|---|
| (none) / `services` | [services] | headless Service per alias | none (DNS baseline) | none — any cluster |
| `bridge` / `ipvlan` / `macvlan` | [services, multus] | NetworkAttachmentDefinition + pod annotation | topological (separate L2) | CapMultus = NAD CRD served |
| `policy` | [services, networkpolicy] | shared Ingress-only NetworkPolicy | kernel, if the CNI enforces | none (emitted unconditionally, advisory) |
| `cilium` | [services, cilium] | shared CiliumNetworkPolicy (`cilium.io/v2`) | kernel (Cilium) | CapCilium = `cilium.io/v2` served |
| any + `driver_opts: {policy: "true"}` | ...+ networkpolicy | appended to ANY pipeline | additive | as above |
| any + `driver_opts: {proxy: "true"}` | (backend pod mutation, not a netdriver provider) | enforcing egress proxy (nftables redirect + `SO_ORIGINAL_DST`) | userspace, CNI-independent, hard | none |
| ...`+ mode: cooperative` | (backend pod mutation) | hostAliases -> loopback -> caretaker splice | userspace, zero-privilege, SOFT | none |

Capability resolution: fallback-to-services with a once-per-network warning (default), or hard error with `CORNUS_K8S_NET_STRICT`. `CapPolicyCNI` = `CORNUS_K8S_POLICY_CNI=true` OR a kube-system calico/cilium DaemonSet probe (used only for the advisory warning — the NetworkPolicy is emitted regardless).

### Caretaker role/exemption matrix

| Combination | Caretaker privilege | Redirect exemption | Notes |
|---|---|---|---|
| mounts only | privileged (9P kernel mount), root | n/a | startup probe `cornus caretaker-check` gates the app until every mount is live |
| enforcing proxy only | none, uid 1337 | `--exempt-uid 1337` (`meta skuid == 1337 return`) | `net-redirect` init container needs NET_ADMIN only |
| cooperative proxy only | none | n/a (no redirect) | plain sidecar + pod hostAliases |
| enforcing proxy + mounts | privileged root, one caretaker with both roles | `--exempt-mark N` (`meta mark == N return`); `caretaker.Config.Mark` marks proxy upstream dials AND the 9P relay dial | uid exemption collapses when caretaker is root; uid==0 rule never programmed |
| DNS only | NET_BIND_SERVICE only (bind :53) | n/a | pod `dnsConfig`: DNSNone, nameserver 127.0.0.1, namespace search domains, ndots:5 |
| DNS + mounts | folds into the one privileged mount caretaker | n/a | |
| DNS + proxy | REJECTED | — | would need two conflicting caretakers (uid-1337/root-with-mark vs NET_BIND_SERVICE) |

### services provider

One HEADLESS Service (ClusterIP None) per alias, selecting the workload's pods — bare-name CoreDNS resolution on any cluster, and works with zero published ports (previously a port-less service had no Service at all). Create-if-absent; cross-project (and cross-scenario) alias collisions: first owner keeps the name.

### multus provider

Shared un-owned NetworkAttachmentDefinition (`k8s.cni.cncf.io/v1`, dynamic client). CNI JSON from DriverOpts: subnet (default deterministic `10.222.<h>.0/24` host-local), bridge name (<=15 chars, derived), master/mode for ipvlan/macvlan (master required), mtu, ipmasq; dhcp IPAM rejected. Overlaid mode appends `k8s.v1.cni.cncf.io/networks`; detached (`Default: true`) sets `v1.multus-cni.io/default-network` (two defaults conflict). Both annotations are namespace-qualified (`<namespace>/<nad>`, `Attachment.Namespace` threaded through the netdriver Engine) — Multus resolves an UNQUALIFIED reference in ITS configured default namespace (kube-system), not the pod's, so unqualified refs hang pods in ContainerCreating with NoNetworkFound. Bare multi-homing is not isolation: CoreDNS/Endpoints only publish the pod's PRIMARY cluster IP — making named traffic ride the secondary is the static-IPAM + overlaid-DNS path below.

### Multus static IPAM + overlaid DNS (matrix row A')

Plan-time deterministic IP allocation makes named same-network traffic provably ride the user network:

- **Allocator** (`pkg/compose/usernet.go`): plan-time deterministic IPs — sha256 of the resource name mapped onto the subnet's host range, with salted-probe collision handling; the compose `ipv4_address` field is an explicit override; falls back to dynamic (host-local) IPAM for `replicas > 1` or when pinning is not possible.
- **NAD**: renders `static` IPAM plus the `ips` capability; the pod annotation upgrades from the name form to the Multus JSON selection form carrying the pinned IPs.
- **DNS**: in overlaid mode the caretaker DNS role serves peer SECONDARY IPs, driven by `api.DNSSpec.RequireUserNet`; on non-Multus clusters this degrades gracefully to plain services DNS.
- **Rollout**: pinned specs use the Recreate deployment strategy (two pods cannot hold the same static IP during a rolling update).
- **Runner**: the `static` CNI plugin is staged into the dind runner alongside bridge/macvlan/ipvlan.

### Multus fabric validation matrix

| Row | Fabric | Scenario | Gate | Notes |
|---|---|---|---|---|
| A' | bridge, overlaid, static IPs | `deploy-multus.star`, `ftp-usernet.star`, `deploy-network.star` | `E2E_MULTUS=1` | named traffic asserted to ride the user bridge on pinned IPs |
| b | ipvlan, overlaid | `deploy-multus-ipvlan.star` | `E2E_MULTUS_IPVLAN=1` (+ kube + NAD CRD) | parent eth0; pinned IPs on net1, DNS answers secondary IPs, NAD GC |
| c | macvlan, overlaid | `deploy-multus-macvlan.star` | `E2E_MULTUS_MACVLAN=1` | asserts POD-TO-POD only — macvlan slave-to-parent is impossible by kernel semantics; cross-node macvlan permanently gated (environment-sensitive) |
| D | detached (primary) | `deploy-multus-detached.star` | `E2E_MULTUS_DETACHED=1` | driven via `cornus deploy --detach` + `networks[].default: true`; the user network IS the pod's primary interface, host-local IPAM on the derived subnet, name-only annotation, no net1 and no caretaker, direct-IP data path, NAD GC on last delete |

Row D flushed out two real bugs: (1) `pkg/client.New` did not normalize `ws://`/`wss://` bases for plain HTTP calls, so the `--detach` POST failed with "unsupported protocol scheme" against the WS-spelled endpoints the attach surfaces pass around — now normalized once in `New` (`TestClientWSBaseNormalized`); (2) the `default-network` annotation was emitted unqualified and resolved in Multus's default namespace — now `<ns>/<nad>` (regression test in netdriver_test.go).

### networkpolicy provider

One SHARED per-network NetworkPolicy named by the netLabel: podSelector = the `cornus.net/<netLabel>` membership label; a single Ingress rule allows from the same label. Ingress-ONLY (egress/CoreDNS untouched); multiple memberships give additive allows. Emitted UNCONDITIONALLY (`Requires()` empty) — harmless no-op on a non-enforcing CNI, and the identical manifest starts enforcing under Calico/Cilium/kindnet. kind's default kindnet DOES enforce NetworkPolicy on `kindest/node:v1.31.0` (nftables-based enforcer) — enforcement (row F) was proven live on the plain default cluster, no extra CNI needed.

### cilium provider

`ciliumProvider.NetworkScoped` emits ONE shared un-owned CiliumNetworkPolicy (unstructured via the dynamic client): both `spec.endpointSelector.matchLabels` and `spec.ingress[0].fromEndpoints[0].matchLabels` key on `cornus.net/<netLabel>`. Ingress-only, additive. Unlike networkpolicy, the CNP is a real CRD that only exists on a Cilium cluster, so it `Requires` CapCilium and falls back to services elsewhere. GC loops over both `nadGVR` and `cnpGVR`.

### Enforcing proxy (Phase 2b)

- Plan-time control plane: for a service on any `driver_opts: {proxy: "true"}` network, `Project.applyProxyPolicy` (pkg/compose) collects its same-network peers (bare service names) into `api.DeploySpec.Proxy{Allow, ListenPort}`. No dynamic API queries, no staleness within a project.
- Runtime (`pkg/caretaker/proxy.go` + `origdst_linux.go`): resolves Allow names into a permitted destination-IP set (`allowSet`, refreshed every 5s so scaling a peer is picked up; never shrinks to empty on a total-DNS-failure refresh). Accepts iptables-redirected connections, reads the pre-DNAT destination via `SO_ORIGINAL_DST`, forwards only if the dest IP is permitted — denial by IP, not by name, so a directly-resolved IP is still blocked.
- Capture: `cornus net-redirect` (`cmd/cornus/netredirect_linux.go`) programs an `ip` nat OUTPUT chain via `github.com/google/nftables` directly over netlink — no iptables/nft binary in the image. RETURN for the exemption(s), REDIRECT the rest of the app's TCP to the proxy port. nftables coexists with legacy-iptables rules at the netfilter NAT hook, so it works regardless of kube-proxy/CNI backend. Flags: `--exempt-uid` (optional), `--exempt-mark` (at least one required; only rules > 0 are programmed).
- Injection (`pkg/deploy/kubernetes`): `injectProxy` adds a `net-redirect` init container (NET_ADMIN, not privileged) + the caretaker as uid 1337. With mounts, `addProxyToMountCaretaker` folds the proxy role into the privileged mount caretaker and switches to the mark exemption (`caretaker.Config.Mark`; Mark==0 = uid path).

### Cooperative proxy

`driver_opts: {proxy: "true", mode: cooperative}` — no NET_ADMIN, no redirect, no special uid:
1. pod `hostAliases` pin each same-network peer name to a distinct 127/8 loopback (`loopbackFor(i)` = 127.0.1.1, 127.0.1.2, ... — starts past 127.0.0.1);
2. the caretaker (`runCooperative`) listens on each (loopback, declared-port) and splices to the peer's REAL FQDN `<peer>.<ns>.svc.cluster.local` — the dotted FQDN deliberately dodges the bare-name hostAlias (which the caretaker's own pod shares), so CoreDNS resolves the real Service instead of looping.

Cooperative needs peer ports (it can only intercept a port it binds): compose gathers each peer's container ports — union of `ports:` container side and the `expose:` field (`ExposeList`, tolerant int-or-string) — into `api.ProxySpec.Ports[peer]`. A peer with no declared ports is not intercepted. Soft isolation by design: an off-network peer has no hostAlias, resolves via CoreDNS, and is reachable — the E2E asserts this honestly. `ProxySpec` shape: `Mode` + `Ports` added; enforcing keeps `Allow` + `ListenPort`. `caretaker.ProxyRole` gained `Mode` + `Coop []CoopUpstream{Listen, Forward, Ports}`; `spliceBidir` is the shared connection splice.

### Caretaker (roles host)

`pkg/caretaker`: `Config{Mounts []MountRole, Proxy *ProxyRole, DNS *DNSRole, Mark}` + `Run(ctx, cfg)` — all roles run under one errgroup; if any fails to establish, the group cancels, Run returns non-zero, and Kubernetes restarts the sidecar (per-role fail-fast). `Ready(cfg)`/`IsMountpoint` back the startup probe. CLI: `cornus caretaker` (reads `CORNUS_CARETAKER_CONFIG`) and `cornus caretaker-check` (exit 0 iff every role live); `mount-agent`/`mountcheck` remain as deprecated single-mount aliases. Mount role: dial the relay, pipe a local unix socket to it, `deploywire.Mount9P`, hold until ctx, then unmount. k8s `deploymentWithMounts` emits ONE privileged `cornus-caretaker` sidecar with all scratch emptyDirs Bidirectional-mounted; app-container side (per-mount emptyDir + HostToContainer volumeMount) unchanged. Linux-only primitives ride existing build-tagged stubs, so the package cross-compiles (darwin/windows).

### Caretaker DNS role

`pkg/caretaker/dns.go`, built on `github.com/miekg/dns` (v1.1.72; replaced an initial hand-rolled parser — behaviour identical): a `dns.Server` over a UDP PacketConn. Owned names get an authoritative `*dns.A` reply; a non-A query of an owned name gets authoritative NODATA; everything else is forwarded via `dns.Client.Exchange` to the upstream. Records keyed by `dns.CanonicalName` of both the bare name and the search-expanded `name.<Domain>` — a pod's `peer` lookup (search-expanded to `peer.<ns>.svc.cluster.local`) hits; `peer.example.com` is forwarded. Backend `injectDNS`: standalone caretaker with ONLY NET_BIND_SERVICE + pod `dnsConfig` (DNSNone, nameserver 127.0.0.1, namespace search domains, ndots:5); upstream = discovered `kube-system/kube-dns` ClusterIP (empty => own records only, NODATA otherwise). Pod-level dnsConfig reaches every container (kubelet renders one resolv.conf per pod). api: `DeploySpec.DNS *DNSSpec{Records}` plus `api.DNSSpec.RequireUserNet` — set by the overlaid-Multus plan so peer records carry the user-network SECONDARY IPs (pinned static addresses); off-Multus the spec degrades to plain services DNS. Purpose: Multus modes where a peer must resolve to its user-network secondary IP, or where a detached pod cannot reach CoreDNS.

### dockerhost (Docker backend)

Native Docker networks: `networkEnsure` (POST /networks/create, `cornus.managed` label, CheckDuplicate, 409 => exists incl. external). Primary network goes in the create body (`HostConfig.NetworkMode` + `NetworkingConfig.EndpointsConfig[net].Aliases` so embedded DNS serves names from boot); secondaries connect via `POST /networks/{n}/connect` BEFORE start. GC: memberships recorded on a `cornus.networks` container label; `reapNetwork` removes only managed+member-less networks (external never touched). `k8sPseudoDriver` = {services, policy, cilium} are not forwarded to `docker network create --driver`. dockerproxy skips `predefinedNetworks` (default/bridge/host/none) in `toDeploySpec` — a plain `docker run` sends `EndpointsConfig["default"]` and turning it into a user network 403s ("operation not permitted on predefined default network").

### Kubernetes wiring details

- Backend construction: `NewWithClients(cs, dyn, ns)`; 2-arg `NewWithClient` kept as a nil-dyn wrapper. Dynamic client is nil-tolerant (CRD fabrics read as unavailable).
- `MutateTemplate` stamps membership labels; mutation+apply funnel through `applyDeployment` (both plain and with-mounts builders).
- detached (`Default: true`) + attach-mounts is rejected in `ApplyWithMounts` (the 9P relay rides the cluster network).
- GC must SKIP Deployments with a non-nil DeletionTimestamp: `Backend.Delete` uses `DeletePropagationForeground`, so the just-deleted Deployment lingers Terminating in the list and would keep the network's shared objects alive forever. Fake clientsets hide this (immediate deletion); only the live run caught it.

### dind E2E environment (Multus staging)

`e2e/container/` (docker:27-dind + kind/kubectl + binaries + full `e2e/scenarios/*.star` glob). Run: `make e2e-image && docker run --rm --privileged -e E2E_TARGETS="docker kube" cornus-e2e:latest` (via `sg docker`); Multus suite gated by `E2E_MULTUS=1`; `E2E_SCENARIOS` scopes the glob. Hermetic Multus staging — nothing fetched at run time:
- CNI reference plugins (bridge/macvlan/ipvlan) downloaded at runner-image build into `/opt/cornus/cni`; `install_multus` `docker cp`s them onto every kind node's `/opt/cni/bin` (kind ships only `ptp host-local portmap loopback`).
- Multus image `crane pull`-ed to `/opt/cornus/multus.tar` at build (crane tarballs are `kind load image-archive`-compatible) and loaded on demand.
- DaemonSet manifest vendored (`e2e/container/multus-daemonset-thick.yml`, pinned `v4.1.4-thick`).
- Readiness canary (`multus_canary`): a bridge-NAD Deployment recreated until its pod actually has `net1`, gating scenarios against the Multus startup race.
A custom kind node image is the WRONG approach — Multus installs via a DaemonSet that rewrites node CNI config at runtime; bake plugins + image + manifest into the runner instead. Debug method: kept-alive dind container (`--keep`, `docker cp` new .star files in) + a fresh kind cluster; the `--rm` automated run gives no post-mortem.

### compose-build on kube (ftp-usernet finding)

`compose_up` on the kube target pushes a compose `build:` image to Cornus's registry but did NOT `kind load` it (only the `build()` builtin did) => ImagePullBackOff. Fixed generally in the harness: `prepareComposeBuildImages` (`pkg/e2e/harness.go`) enumerates `build:` services on the kube target, pre-runs `cornus compose build`, and `PrepareImage`s each `<registry>/<project>-<service>:latest` ref (the same kind-load path `build()` uses); no-op on other targets and build-free files. Scenario-level pre-builds are no longer needed (ftp-usernet's workaround was removed).

## Files

- `pkg/api/deploy.go` — `NetworkAttachment`, `DeploySpec.Networks`, `ProxySpec{Allow, ListenPort, Mode, Ports}`, `DNSSpec{Records, RequireUserNet}`, `VolumeSpec.Name`.
- `pkg/compose/` — `Project.Networks`/`Service.Networks` parsing (`decodeKeyVals`, `ExposeList`), implicit `<project>_default`, `applyProxyPolicy` (plan-time allow/ports tables), `usernet.go` (plan-time deterministic IP allocator, `ipv4_address` override).
- `pkg/dockerproxy/` — `EndpointsConfig` aliases threading, `toDeploySpec` (sorted attachments, service label first alias, `predefinedNetworks` skip).
- `pkg/deploy/dockerhost/` — `networkEnsure`, `reapNetwork`, `cornus.managed`/`cornus.networks` labels, `k8sPseudoDriver`.
- `pkg/deploy/kubernetes/internal/netdriver/` — `netdriver.go` (Provider/Object/Engine, `pipelineFor`, capability detection, mark-and-sweep GC), `services.go`, `multus.go`, `networkpolicy.go`, `cilium.go`.
- `pkg/deploy/kubernetes/kubernetes.go` — `NewWithClients`, `applyDeployment`, `injectProxy`/`injectProxyEnforcing`/`injectProxyCooperative`, `addProxyToMountCaretaker`, `injectDNS`, `deploymentWithMounts`, `netRedirectInit`, `cooperativeAliases`.
- `pkg/caretaker/` — `caretaker.go` (Config/Run/Ready), `proxy.go` (`runEnforcing`/`runCooperative`/`spliceBidir`, allowSet), `origdst_linux.go` (`SO_ORIGINAL_DST`; port at bytes [2:4] of the raw sockaddr_in), `dns.go` (miekg/dns resolver), `mark_linux.go`/`mark_other.go` (`SO_MARK`, `markDialer`).
- `pkg/wire/export.go` — `DialConnControl(ctx, url, control)` (threads `net.Dialer.Control` into the WebSocket handshake transport; `DialConn` delegates with nil control).
- `cmd/cornus/` — `caretaker.go` (caretaker / caretaker-check subcommands), `mountagent.go` (deprecated aliases), `netredirect_linux.go` + `netredirect_other.go` (`net-redirect`, google/nftables over netlink), `winch_unix.go`/`winch_windows.go` (`notifyResize`, the SIGWINCH cross-build fix).
- `e2e/container/` — dind runner, `entrypoint.sh` (`install_multus`, `multus_canary`), `multus-daemonset-thick.yml`.
- `pkg/deploy/kubernetes/internal/netdriver/multus.go` — static IPAM + ips capability NAD, Multus JSON selection-form annotation with pinned IPs, namespace-qualified `<ns>/<nad>` references (`Attachment.Namespace`).
- Env vars: `CORNUS_K8S_NET_DRIVER`, `CORNUS_K8S_NET_STRICT`, `CORNUS_K8S_POLICY_CNI`, `CORNUS_K8S_SIDECAR_IMAGE`, `CORNUS_CARETAKER_CONFIG`; harness `E2E_TARGETS`, `E2E_MULTUS`, `E2E_MULTUS_IPVLAN`, `E2E_MULTUS_MACVLAN`, `E2E_MULTUS_DETACHED`, `E2E_SCENARIOS`.

## Test Coverage

Unit/fake (full `go test ./...` gate incl. darwin + windows cross-builds):
- netdriver: netLabel stability/collisions, services shape, multus NAD/annotation/validation, policy shape + pipeline resolution, `TestGCIgnoresTerminatingDeployment`, `TestCiliumProvider`/`TestCiliumResolveAndCapability`/`TestCiliumApplyAndGC`, strict/non-strict fallback vs fake typed/dynamic/discovery clients.
- kubernetes backend: services fallback, full Multus path, detached guard, `TestNetworkPolicyIsolation`, `TestProxyInjectsRedirectAndCaretaker`, `TestProxyCooperativeInjectsHostAliases`, `TestProxyWithMountsSingleCaretaker` (mark, `--exempt-mark` not `--exempt-uid`), `TestApplyWithMountsInjectsSidecar`/`TestApplyWithMountsSingleCaretaker`, `TestDNSInjection`, `TestDNSProxyRejected`.
- compose: `TestNetworks`, `TestProxyAllowTable`, `TestProxyCooperativeMode`; dockerproxy `TestToDeploySpecNetworks` (incl. predefined-network skip); dockerhost create-body/lifecycle/`TestNetworkEnsureDriver`.
- caretaker: config round-trips (mount/proxy/coop/mark/DNS), allow-set membership + no-shrink-on-DNS-failure, `TestCooperativeForwards` (real in-process data path), `TestDNSRole` (drives Go's own net.Resolver through the server).

E2E scenarios (`e2e/scenarios/`, all in the default SCENARIOS, all live-validated in the dind runner unless noted):
- `deploy-network.star` — services-DNS baseline (headless alias Service without published ports, bare-name reach, membership label, ownerRef cascade on down).
- `deploy-multus.star` — bridge NAD, pod annotation, `net1` with pinned static IPs, GC (needs `E2E_MULTUS=1`; self-skips without the NAD CRD).
- `deploy-multus-ipvlan.star` — ipvlan NAD on parent eth0, static IPAM, pinned secondary IPs on net1, caretaker DNS answering them, named traffic on the ipvlan network, NAD GC (gate `E2E_MULTUS_IPVLAN=1`).
- `deploy-multus-macvlan.star` — macvlan NAD on parent eth0, same asserts but strictly pod-to-pod (gate `E2E_MULTUS_MACVLAN=1`).
- `deploy-multus-detached.star` — row D via `cornus deploy --detach` + `networks[].default: true`: user network as the primary interface, host-local IPAM, no net1/caretaker, direct-IP data path, NAD GC (gate `E2E_MULTUS_DETACHED=1`).
- `deploy-netpolicy.star` — policy object shape + GC; `deploy-netpolicy-enforce.star` — live BLOCKING on kindnet (friend->web allowed, stranger->web denied).
- `deploy-proxy.star` — enforcing proxy: allow forwarded, cross-network denied by destination IP despite DNS resolvability.
- `deploy-proxy-coop.star` — hostAlias in /etc/hosts, sidecar-in-path, soft-isolation trade-off asserted honestly.
- `deploy-proxy-mounts.star` — mount live inside a proxied pod (mark-exempted relay escaped the redirect) + reach/deny regressions.
- `deploy-cilium.star` — self-skips without the `ciliumnetworkpolicies.cilium.io` CRD (skip path validated; active path needs a real Cilium cluster).
- `deploy-dns.star` — injected record answered locally, `kubernetes.default` forwarded to kube-dns, app resolv.conf points at the caretaker (harness `deploy(dns={name: ip})` kwarg).
- `ftp-usernet.star` — compose-built FTP server + client on a user network, bidirectional transfer (pre-builds via `build()` for the kind-load gap).

## Pitfalls

- **kind ships a MINIMAL CNI plugin set** (`ptp host-local portmap loopback` only on `kindest/node:v1.31.0`); `bridge`/`macvlan`/`ipvlan` must be staged onto nodes or Multus's CNI ADD fails and the pod hangs in ContainerCreating.
- **Multus startup race**: DaemonSet Ready != able to attach secondaries (NAD informer sync lag). A NAD-annotated pod created in the window runs default-only, stays Running, never self-heals. Gate with a canary that recreates a NAD pod until it has `net1`.
- **Foreground-deletion GC race**: reference-counting GC over Deployments must skip non-nil `DeletionTimestamp` or the last-member delete never reaps shared network objects. Fake clientsets cannot reproduce this.
- **`SO_ORIGINAL_DST` byte offsets**: the raw `sockaddr_in` in the IPv6Mreq Multiaddr has sin_family at [0:2] and sin_port at [2:4]; reading the port from [0:2] yields 512 (0x0200 = AF_INET) and blocks everything. Found only live.
- **Every caretaker socket must carry the exemption mark** — including the WebSocket 9P relay dial — or the caretaker's own traffic is swallowed by its redirect and (for the relay) DENIED by its own allow-set.
- **Never program a uid==0 exemption** in net-redirect: it would exempt the root app container and defeat the proxy.
- **`EndpointsConfig["default"]` is Docker's builtin bridge**; skip default/bridge/host/none in the proxy or the backend 403s creating a predefined network.
- **Shared-namespace headless-Service name collisions across kube scenarios**: the services provider names Services by BARE alias; two scenarios both using `outsider` raced each other's create/cascade-delete. Same-namespace scenarios must use distinct service names (coweb.../pmweb... pattern).
- **kindnet DOES enforce NetworkPolicy** (recent versions, nftables-based) — do not claim non-enforcement in warnings; CapPolicyCNI detection stays conservative (calico/cilium DaemonSet or env opt-in) without falsely denying enforcement.
- **CoreDNS never publishes Multus secondary IPs** (Endpoints carry primary cluster IPs only) — overlaid name resolution rides the primary unless the caretaker DNS role serves the pinned secondary IPs (the static-IPAM + `DNSSpec.RequireUserNet` path).
- **Unqualified Multus network references resolve in Multus's OWN default namespace** (kube-system), not the pod's — pods hang in ContainerCreating with NoNetworkFound. Always emit namespace-qualified `<ns>/<nad>` annotations (both the `networks` and `default-network` forms).
- **macvlan slave-to-parent traffic is impossible by kernel semantics** — a macvlan pod cannot reach its node's parent interface; assert pod-to-pod only. Cross-node macvlan is environment-sensitive (parent NIC promiscuity/switch behavior) and stays behind its own gate.
- **Pinned static IPs require the Recreate strategy** — a rolling update would briefly run two pods claiming the same static IP.
- **`pkg/client.New` must normalize `ws://`/`wss://` bases to `http://`/`https://` for plain HTTP methods** — the attach surfaces pass WS-spelled endpoints around, and un-normalized bases fail with "unsupported protocol scheme" (`TestClientWSBaseNormalized`).
- **Unit tests with fakes passed through every one of the above** — the dind full-suite run is the real gate for anything in this area.

## Caretaker Egress And Docker Roles

Egress is a fifth server-bound caretaker role. DNS, hub discovery, mounts, credentials, and egress must fold into one `cornus-caretaker`; relay egress and detached-primary networking are incompatible because the caretaker needs its attach connection. Transparent redirect exempts loopback and captures TCP only, so caretaker DNS and hub loopback traffic stay outside egress interception. Enforcing proxy and egress are mutually exclusive.

`DeploySpec.Docker` adds an opt-in in-pod Docker Engine API endpoint. `DockerRole` serves the existing `pkg/dockerproxy` on TCP, Unix, or both and injects `DOCKER_HOST`. Unix transports share an `emptyDir` socket. The role folds into existing mount, hub, or DNS caretakers and runs standalone only when no other role is active; duplicate caretaker names make that invariant mandatory.

The Docker role requires a separate client-scoped credential because its proxy calls `/.cornus/v1/deploy/*`; the attach-scoped caretaker token must remain rejected. Prefer Secret-backed `CORNUS_DOCKER_CLIENT_TOKEN` from `CORNUS_CLIENT_TOKEN_SECRET`; do not add Docker to the `serverBound` set that injects `CORNUS_TOKEN`. Docker with enforcing proxy is rejected. `/_ping` and `/version` prove endpoint wiring only, not a server round trip.
