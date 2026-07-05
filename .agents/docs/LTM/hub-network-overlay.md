# Hub Network Overlay (caretaker mux, star relay, discovery, multi-replica)

## Summary

The hub overlay turns the Cornus server into a star hub for workload-to-workload traffic: spokes (pod caretakers or a laptop CLI) register named services and reach peers by name, with all bytes relayed through the server over one pod-scoped yamux connection. It grew from Phase 0 (multiplex the caretaker↔server mount leg) through registration/relay, ingress delivery, discovery (synthetic-IP DNS), policy/mTLS, UDP, three pluggable multi-replica registry backends, TLS on every hub dial, reactive catalog push with dynamic imports (listeners + DNS), cross-replica mount-relay forwarding, and Helm multi-replica wiring — all validated on real kind clusters in dind, not just unit tests.

Note on paths: the packages were relocated from `internal/` to `pkg/` (e.g. `pkg/hub`, `pkg/caretaker`, `pkg/server`, `pkg/wire`); older sections below may still spell `internal/`.

## Key Facts

- One pod-scoped, always-on connection per pod: `/.cornus/v1/caretaker/attach`, yamux-multiplexed, carrying mount `'M'` streams plus hub `'C'` (control), `'D'` (data), `'I'` (deliver) streams. The three older endpoints (`/.cornus/v1/deploy/mount/{session}/{name}`, `/.cornus/v1/deploy/caretaker/{session}`, `/.cornus/v1/hub/attach`) are removed.
- Role inversion: on the mount leg the HTTP/WS *client* (caller CLI or pod caretaker) is the 9P *server*; the Cornus server is the 9P *client*.
- The kernel 9p client opens one transport connection per `mount(2)`; collapsing a pod's N mounts onto one connection REQUIRES a stream muxer (yamux) — bare 9P cannot be demuxed. SOCKS was rejected (it demultiplexes at the proxy, never multiplexes onto one conn).
- Two relay modes per registration: dial-direct (registry entry has an `Addr` the hub dials) and delivery (hub opens an `'I'` ingress stream to the hosting spoke, which dials its own local target) — delivery makes NAT'd / cross-cluster destinations work.
- Discovery (Phase 3): `hub.SyntheticIP(name)` deterministic `127.0.0.0/8` address; the caretaker `dns` role serves a peer record with the SAME synthetic IP the Reach listener binds, so `dial(peer)` funnels into the hub.
- Policy: `CORNUS_HUB_POLICY` (reach: caller→callee, enforced in `hubRelay`) and `CORNUS_HUB_REGISTER_POLICY` (register: identity→hostable-service, enforced in `hubControl`). mTLS client-cert CommonName (`verifiedIdentity`) overrides any spoke-declared identity.
- UDP is supported via 2-byte big-endian length-prefix framing over the yamux stream; the hub relay itself stays byte-agnostic.
- `hub.Store` seam with three backends: in-memory `Registry` (single-replica default), `RedisStore` (`CORNUS_HUB_REDIS`), and `kubehub.KubeStore` (`CORNUS_HUB_STORE=kube`, recommended on k8s — zero external infra). Cross-replica delivery forwards via `GET /.cornus/v1/hub/forward`.
- Catalog: `GET /.cornus/v1/hub/catalog` returns `{"services":[...]}` (sorted live names; appear on register, drop on disconnect). The catalog is also PUSHED reactively: a spoke setting the `Registration.Watch` capability receives `CatalogUpdate` frames over its existing control stream.
- Every hub WS dial can carry TLS: `caretaker.Config.TLSClientConfig *tls.Config` (json:"-", in-process CLI path) + `TLS *TLSFiles` (serializable ca/cert/key file paths for sidecars, fail-fast load at Run); dials go through `wire.DialControlHeaderTLS` (nil config = byte-identical to before). `CORNUS_HUB_FORWARD_CA` (PEM appended to system roots, fail-closed parse) secures inter-replica forward dials.
- Deploy-attach mount sessions are cross-replica too: digest-keyed routing records through the same hub store + `GET /.cornus/v1/mount/forward` (shared `dialForward` helper with the hub forward).
- k8s sidecars get TLS via `CORNUS_CARETAKER_TLS_SECRET` and dynamic imports via `api.HubSpec.ImportDynamic {Ports, Protocol}` -> caretaker `HubRole.ReachDynamic`; dynamically bound names are also published into the caretaker DNS overlay.
- Helm chart `replicas > 1` wires the whole multi-replica story (KubeStore, per-pod forward URL, anti-affinity) and refuses to render unless `storage` is shared s3.
- Version-skew fallback is closed as moot: the old endpoints are removed, both sides ship in one binary, and each newer protocol addition (catalog-push Watch, UDP port-forward ack, compose daemon Protocol stamp) carries its own explicit skew story (unknown field ignored / no Watch = no frames).
- Cross-network spoke CLI: `cornus hub --server ... --register name=host:port --reach name=ip:port` (`cmd/cornus/hub.go`) runs the caretaker hub role from a laptop (register-via-delivery is NAT-safe).
- Validated on real clusters: `deploy-hub.star` (TCP) and `deploy-hub-udp.star` in dind+kind both passed first try; Redis and KubeStore multi-replica paths validated with two real replicas.

## Details

### Phase 0: caretaker↔server multiplexing

The pod caretaker originally opened one WebSocket per client-local mount. Phase 0 replaced this with one yamux connection per pod and one `'M'`-tagged stream per mount, plus a reserved `'C'` control stream. `internal/wire` gained `TagMount = 'M'` and `DialControlHeader` (yamux client dial with the SO_MARK control hook + trace-context headers); `Accept`/`AcceptTagged`/`OpenTagged`/`OpenBacking`/`Pipe`/`ReadLine` were reused verbatim. Semantics unchanged: each stream mounts synchronously before the startup probe passes; failure is shared-fate per pod (the errgroup already tore the whole sidecar down on any mount failure). Observability: one `caretaker.session` span per connection with `caretaker.mount` child spans; trace context rides the session handshake (linking is per-session, not per-mount).

Key facts that shaped the design (from the transport analysis):

- 9P is the only server-bound caretaker traffic (invariant). Of the three caretaker roles (`internal/caretaker`), only `mount` dials the server; `proxy` and `dns` sit between the app container and the cluster with config baked into the pod spec. The hub's `'C'` control stream is the first deliberate break of that invariant.
- Transport is swappable behind a `net.Conn` seam: `wire.AcceptConn`/`DialConn`/`DialControlHeader` all return `net.Conn`; yamux and 9P sit above. Switching WebSocket → raw hijacked-HTTP upgrade or mTLS-TCP is a localized change (recorded lever, not adopted — WS earns its keep via the one-port/ingress story, and WAN round-trips on the caller leg dominate).
- A pod's mounts share one deploy-attach session id: `applyWithSidecarMounts` (`internal/server/deploy_attach.go`) mints one id per Deployment and stamps it on every `AttachMount` with one shared `RelayURL` (`CORNUS_ADVERTISE_URL`), so grouping by (server, session) — later (server) — yields exactly one connection per pod.
- The caller leg keeps WebSocket untouched (crosses NAT/ingress, genuinely multiplexes).

Design docs: `.agents/docs/HUB_NETWORK_DESIGN.md` (star-overlay proposal, 6 phases) and `.agents/docs/HUB_PHASE0_CARETAKER_MUX.md`. README `## Networking` has 6 mermaid diagrams; ARCHITECTURE has the caretaker roles table + the server-bound-traffic invariant.

### Registry and relay (Phases 1-2)

`internal/hub` (new pkg, no BuildKit dep): `Registry` maps name → targets, connection-scoped (a spoke's entries vanish on disconnect); `Lookup` round-robins replicas. Client helpers: `Dial`/`Register`/`OpenTo`/`OpenDeliver` over the yamux session. `internal/wire` tags: `TagData = 'D'`, `TagDeliver = 'I'` alongside `'C'`/`'M'`/`'L'`.

Server side: a spoke's `'C'` control stream carries `hub.Registration` JSON, decoded into the store under a per-connection id and removed on drop (`hubControl`). Each `'D'` data stream carries a service-name line; `hubRelay` looks it up and either dials the registered `Addr` (dial-direct) or opens `hub.OpenDeliver(tgt.Mux, name)` to the hosting spoke's mux (delivery), then `wire.Pipe`s. Registry entries hold a `Target{Addr, Mux}`; the mode is chosen per registration by whether `Addr` is set. `Register` = dial-direct, `RegisterDeliver(connID, name, mux)` = delivery.

Caretaker side: the `hub` role (`Hub *HubRole` config) registers hosted services and runs Reach loopback listeners that forward accepted conns to the hub via `OpenTo` (reuses `spliceBidir`); when any delivered service is present it runs `serveIngress` (accepts `'I'` streams, dials the local target from a name→Target map, splices). A delivered service registers with empty `Addr` + a spoke-side-only local `Target` (not on the wire). Egress interception mirrors the cooperative proxy.

Traffic path: `app → caretaker(Reach loopback) → hub → (dial Addr | deliver via 'I' to hosting spoke) → service`.

### Connection unification

Hub membership is workload-lifetime and caller-independent, whereas the mount connection was scoped to a caller's deploy-attach session — so instead of riding hub streams on the mount session, the caretaker connection was promoted to a pod-scoped, always-on identity and BOTH mount and hub streams migrated onto it. The caretaker dials `/.cornus/v1/caretaker/attach` once per server (`runCaretakerConn`, grouped by server via `groupByServer`/`serverBundle`); the deploy-attach session moved from the URL into each `'M'` stream (`session\nname\n` framing) — the session id stays the capability, `AllowsMount` gates the name. Server handler: `handleCaretakerUnified` + `relayMountMuxed`. After removal of the legacy endpoints, `mount_relay.go` is just the mount session registry + `newSessionID`; `hubControl`/`hubRelay`/`relayMountMuxed`/`hubConn`/`verifiedIdentity` serve the unified handler.

### Policy and mTLS (Phase 4)

`hub.Policy` holds two matrices:

| Env var | Matrix | Enforced in | Effect |
|---|---|---|---|
| `CORNUS_HUB_POLICY` | caller identity → allowed callee services | `hubRelay` | denied reaches refused |
| `CORNUS_HUB_REGISTER_POLICY` | identity → hostable services | `hubControl` | unauthorized registrations dropped |

nil policy = allow-all. Per-connection identity is tracked in a `hubConn`; a spoke declares it via control-stream `Registration.Identity`, with a ready-gate so a data stream waits for it. `Policy.ReachEnforced` gates that wait so a register-only policy does not block callers that never declare an identity (a real bug found and fixed via test). With mTLS (`wire.DialTLS`/`hub.DialTLS` carry a client cert), `verifiedIdentity(r)` reads the VERIFIED client-cert CommonName and declares it as the connection identity BEFORE any stream, overriding whatever the spoke declares.

### Discovery + k8s injection (Phase 3)

`api.HubSpec` (Export/Import) on DeploySpec. k8s `hubDiscovery`/`injectHub` build a caretaker `hub` role plus a `dns` role whose record for a peer is the SAME synthetic IP (`hub.SyntheticIP(name)`, deterministic `127.0.0.0/8`) that the peer's Reach listener binds, and point the pod resolver at the caretaker. Exports register dial-direct (cluster Service) or delivery. Hub injection subsumes standalone dns injection. `deploymentWithMounts` folds the hub role (and its synthetic-IP DNS records) into the ONE privileged mount caretaker (`base.Hub=nil` so `deployment` doesn't add a second); hub+proxy is rejected (conflicting egress interception), extending the existing DNS+proxy rejection. Static discovery is computed at deploy time; the `GET /.cornus/v1/hub/catalog` endpoint (`Store.Catalog()`, sorted live names) plus the reactive catalog push (below) cover post-deploy dynamism — reach/replica dynamism within a name already works because `Lookup` round-robins live entries and misses retry.

E2E surface: `hub_identity`/`hub_export`/`hub_import` kwargs on the e2e `deploy()` builtin (`parseHubSpec`); scenario `e2e/scenarios/deploy-hub.star` (exporter offers "greeter" via delivery; importer curls it by name).

### UDP

The hub relay is byte-agnostic; UDP awareness lives only at the conversion endpoints:

- `internal/wire/datagram.go`: `WriteDatagram`/`ReadDatagram` (2-byte big-endian length prefix, max 65535) preserve datagram boundaries over the yamux byte stream; `BridgeDatagram` couples a framed stream to a connected UDP socket (one conn.Read = one datagram).
- Reach near end (caretaker): a UDP peer binds `net.ListenUDP` and runs a per-source-flow model — each client source addr gets its own hub `'D'` stream plus a reply-reader goroutine that `WriteToUDP`s framed replies back to that source; 60s idle-GC (UDP has no close). The payload is copied before framing (read-buffer safety).
- Far ends: delivery (`deliverIngress`) and server dial-direct (`hubRelay`) dial UDP + `BridgeDatagram` when the target protocol is udp; TCP unchanged.
- Protocol ("tcp" default / "udp") is threaded through `api.HubExport`/`Import`, `caretaker.HubService`/`HubPeer`, `hub.Service`/`Target` (+ `Register` arg), and k8s `hubDiscovery`. Surface: a `/udp` port suffix in `hub_export`/`hub_import`.

### Multi-replica registry backends (hub.Store seam)

`hub.Store` interface: `Register`/`RegisterDeliver`/`Lookup`/`RemoveConn`/`Catalog`; the server holds a `hub.Store`. A delivery `Target` holds a process-local `*yamux.Session`, so any routed registry must forward the relay to the process owning that connection — the `Target.ForwardAddr`/`ForwardName` remote-delivery disposition: `hubRelay` forwards to the owner's `GET /.cornus/v1/hub/forward` (carrying the server's own full auth token); `handleHubForward` looks up LOCALLY and requires a local-delivery Mux (loop guard). Reach policy is enforced only at the forwarding replica (the owner trusts the authenticated peer). Dial-direct needs no forwarding (shared Addr).

| Backend | Selector | Notes |
|---|---|---|
| in-memory `Registry` | (default, env unset) | single-replica; byte-for-byte unchanged behavior |
| `hub.RedisStore` (`internal/hub/redisstore.go`) | `CORNUS_HUB_REDIS` | per-service provider hash + per-replica alive-TTL liveness (5s heartbeat, 15s TTL, pipelined EXISTS filter of dead owners); disjoint per-replica partitions, no CRDT |
| `kubehub.KubeStore` (`internal/kubehub/`) | `CORNUS_HUB_STORE=kube` | self-installed `HubEndpoint` CRD (`cornus.dev/v1`, dynamic informer index) + native `coordination.k8s.io` Leases per replica (`cornus-hub-<hash>`, ownerReferences to the Pod for GC); recommended on k8s, zero external infra, no new module dep (CRD self-install uses the dynamic client) |

Related env vars: `CORNUS_REPLICA_ID` (fallback hostname/crypto-rand), `CORNUS_HUB_FORWARD_URL`. `internal/kubehub` is a separate package so `internal/hub` stays client-go-free. RBAC for KubeStore (hubendpoints + leases + CRD get/list/create) is in both helm and the raw manifest. Deps: `redis/go-redis/v9`, `alicebob/miniredis/v2` (test only).

### Hub WS dial TLS plumbing

`caretaker.Config` carries `TLSClientConfig *tls.Config` (json:"-", for the in-process `cornus hub` CLI path) and `TLS *TLSFiles` (serializable ca/cert/key file paths for sidecars; loaded fail-fast at `Run`). All hub/caretaker WS dials go through `wire.DialControlHeaderTLS`; a nil TLS config makes it byte-identical to the plain `wire.DialControlHeader` path. The `cornus hub` CLI's former refusal of client-TLS-bearing connection profiles is removed — profile TLS material now flows into the dial. Inter-replica dials (`dialHubForward` and the shared `dialForward` used by mount forwarding) honor `CORNUS_HUB_FORWARD_CA`: a PEM bundle appended to the system roots, with a fail-closed parse (malformed CA = hard error, never silent system-roots-only).

### Reactive catalog push + dynamic reach

The catalog is pushed, not only polled: a spoke sets the `Registration.Watch` capability flag on its control-stream registration and the server sends `CatalogUpdate` frames over that SAME control stream. Update triggers: an immediate kick on local register/disconnect, plus a 3s hash-compare poll to converge cross-replica stores (Redis/Kube); the poll goroutine exists only while watchers are subscribed (zero cost otherwise). Old peers are unaffected — the unknown field is ignored, and no Watch means no frames (the explicit skew story).

On the caretaker side, `HubRole.ReachDynamic` consumes catalog updates to bind/unbind synthetic-IP Reach listeners at runtime with drain-not-kill semantics (an unbind stops accepting but lets in-flight splices finish). k8s deploys opt in via `api.HubSpec.ImportDynamic {Ports, Protocol}`, which maps to `ReachDynamic`.

### Dynamic-import DNS

The caretaker dns role has a concurrency-safe `DynamicRecords` overlay (RWMutex map, allocation-light lookups): static records always win, and the overlay serves positive A answers only — NODATA/upstream-forwarding semantics are unchanged. `runDynamicReach` publishes `name -> synthetic IP` after a successful listener bind and withdraws the record on unbind/teardown, joined to the dns role via a process-wide rendezvous (one caretaker process = one pod, so no Config plumbing is needed). Plain `dial(peer)` therefore works for dynamically discovered names when the pod runs both the hub and dns roles.

### Multi-replica mount-relay forwarding

Deploy-attach mount sessions publish routing records through the EXISTING hub store — additive, no second store: a delivery-mode record under the reserved name `cornus.mount/<sha256(sessionID)[:16]>`. Only the digest goes on the wire/store, so the raw session id stays an unguessable capability; the record carries a nil mux so a hub relay can never open ingress onto the session; the records are filtered out of both `/.cornus/v1/hub/catalog` AND catalog-watch pushes. A replica that does not hold the session forwards via `GET /.cornus/v1/mount/forward`, dialed through the shared `dialForward` helper (same bearer token, same `CORNUS_HUB_FORWARD_CA` TLS as the hub forward). The local-session fast path consults NO store, so single-replica behavior is byte-identical (asserted by a counting-store test). Teardown unregisters; crash-safety rides the stores' existing TTL/Lease/ownerRef GC. Covered by a two-replica end-to-end test over miniredis (9P read through the non-owning replica).

### Hub sidecar TLS wiring (k8s)

`CORNUS_CARETAKER_TLS_SECRET` (a Secret name following k8s TLS-secret key conventions) mounts the Secret read-only at `/cornus/tls` in server-bound sidecars only (hub + mounts caretakers; dns/proxy untouched) and stamps the `Config.TLS` file paths into the embedded caretaker config. CA-only Secrets are supported; an unreadable Secret assumes the FULL layout with a loud warning — intended TLS never silently degrades to plaintext. Unset = byte-identical pod specs. The Helm chart exposes this as the `caretakerTlsSecret` value, rendering the env plus `resourceNames`-scoped `secrets get` RBAC.

### Helm multi-replica hub

The chart's `replicas` value, when > 1, wires: `CORNUS_HUB_STORE=kube`, POD_NAME/POD_NAMESPACE/`CORNUS_K8S_NAMESPACE` downward-API identity, a per-pod `CORNUS_HUB_FORWARD_URL` via a new headless Service (which is also the StatefulSet serviceName), preferred hostname anti-affinity, and wss + hub SANs under TLS. Template rendering FAILS unless `storage` is shared s3 — per-pod PVC CAS would split-brain. The default (single-replica) render is byte-identical; the static `deploy/k8s/cornus.yaml` stays single-replica with a pointer comment.

### Real-cluster validation results

- `deploy-hub.star` in dind+kind (public images http-echo + curl, kube-loaded cornus:e2e sidecar): exporter `hubsrv` registered "greeter" (delivery); importer `hubcli` got the synthetic-IP DNS record + Reach listener; `curl greeter:8080` inside the app container returned HELLO-FROM-HUB. Passed first try. The hub needs no Multus — it is Cornus's own relay, not a CNI.
- `deploy-hub-udp.star` in dind+kind: socat UDP-echo exported (delivery) + busybox `nc -u` importer; HELLO-UDP-HUB round-tripped. Passed first try (after rebuilding the e2e image so the sidecar carries UDP).
- Redis: `e2e/multireplica-hub.sh` (`make e2e-multireplica`) — real Redis container + two real `cornus serve` replicas + two `cornus hub` spokes; `PING-MULTIREPLICA` round-tripped B → forward to A → hosting spoke → echo. Proves real RESP/TTL + real cross-process WebSocket forwarding (which miniredis cannot).
- KubeStore: `e2e/multireplica-hub-kube.sh` (`make e2e-multireplica-kube`) — real kind cluster as the store, two replicas with `CORNUS_HUB_STORE=kube`; CRD self-installed, both replica Leases appeared, `PING-KUBESTORE` round-tripped through the forward path.

## Files

- `internal/wire/` — tags `'C'/'M'/'L'/'D'/'I'`, `DialControlHeader`, `DialControlHeaderTLS`, `DialTLS`, `datagram.go` (`WriteDatagram`/`ReadDatagram`/`BridgeDatagram`)
- `internal/hub/` — `Registry`, `Store` interface, `Policy`, `SyntheticIP`, client helpers (`Dial`/`Register`/`OpenTo`/`OpenDeliver`/`DialTLS`), `redisstore.go`
- `internal/kubehub/` — `KubeStore` (HubEndpoint CRD + Leases)
- `internal/server/` — `handleCaretakerUnified`, `relayMountMuxed`, `hubControl`, `hubRelay`, `hubConn`, `verifiedIdentity`, `handleHubForward`, catalog endpoint; `mount_relay.go` (session registry + `newSessionID`); `deploy_attach.go` (`applyWithSidecarMounts`)
- `internal/caretaker/` — `runCaretakerConn`, `groupByServer`/`serverBundle`, hub role (`HubRole`/`HubService`/`HubPeer`/`ReachDynamic`, `serveIngress`), UDP per-source flows; `Config.TLSClientConfig`/`TLS *TLSFiles`; `pkg/caretaker/hub_dynamic.go` (`runDynamicReach`) + `pkg/caretaker/dns.go` (`DynamicRecords` overlay)
- `internal/deploy/kubernetes/` — `hubDiscovery`/`injectHub`, `deploymentWithMounts` single-caretaker folding, `CORNUS_CARETAKER_TLS_SECRET` sidecar wiring, `api.HubSpec.ImportDynamic` mapping
- `cmd/cornus/hub.go` — cross-network spoke CLI (`hubRoleFromFlags`; resolves via the shared clientconn resolver, profile TLS flows through)
- `pkg/server/` — mount routing records + `GET /.cornus/v1/mount/forward`, shared `dialForward`, `CORNUS_HUB_FORWARD_CA` loading
- `deploy/helm/cornus/` — `replicas`, `caretakerTlsSecret` values; headless Service + downward-API + forward-URL wiring for replicas > 1
- `e2e/scenarios/deploy-hub.star`, `e2e/scenarios/deploy-hub-udp.star` (both in the Makefile SCENARIOS list), `e2e/multireplica-hub.sh`, `e2e/multireplica-hub-kube.sh`, `e2e/echoserver` (static Go echo, committed)
- Design docs: `.agents/docs/HUB_NETWORK_DESIGN.md`, `.agents/docs/HUB_PHASE0_CARETAKER_MUX.md`

## Test Coverage

- `internal/server/caretaker_relay_test.go` — full caller→server→caretaker path in-process, two mounts over one muxed connection read back over real 9P; `TestCaretakerUnifiedMount`; `TestCaretakerMountUnknownSession` (per-stream close on the unified endpoint)
- `internal/hub/registry_test.go` — round-robin, `RemoveConn`, `TestRegistryDeliverTarget`
- `internal/server/hub_test.go` — `TestHubRelaysToRegisteredService`, `TestHubUnknownServiceCloses`, `TestHubRoleEndToEnd`, `TestHubDeliveryEndToEnd`, `TestHubPolicyEnforced`, `TestHubRegisterPolicy`, `TestHubMTLSIdentityIsAuthoritative` (in-process CA + RequireAndVerifyClientCert), `TestHubCatalog`
- `internal/server/hub_multireplica_test.go` — two in-process `*Server` + miniredis: cross-replica delivery, cross-replica dial-direct, liveness expiry (in CI)
- Mount-relay forwarding — two-replica end-to-end test over miniredis (9P read through the non-owning replica); counting-store test asserting the local fast path touches no store
- UDP: datagram codec tests (round-trip/empty/max/short-read), `TestUDPReachFlow` (two sources prove per-source reply routing + flow reuse), `TestDeliverIngressUDP`
- k8s injection: `TestSyntheticIP`, `internal/deploy/kubernetes/hub_test.go` (record IP == Reach listen IP; export modes), `TestHubWithMountsSingleCaretaker`, `TestHubProxyRejected`
- caretaker: `TestCaretakerURL`, `TestGroupMounts`; CLI: `hubRoleFromFlags` parsing
- E2E: `deploy-hub.star`, `deploy-hub-udp.star` (real dind+kind runs, both passed), `make e2e-multireplica`, `make e2e-multireplica-kube`

## Pitfalls

- Do not try bare 9P over one shared connection for multi-mount pods — the kernel 9p client needs one transport per `mount(2)` and bare 9P has no demux framing; a stream muxer (yamux) is mandatory. Bare-9P/mTLS stays viable only for the one-conn-per-mount case, in-cluster.
- SOCKS looks like a multiplexer but is not (one SOCKS conn = one proxied stream; `ssh -D`'s multiplexing is SSH channels). Its connect-to-arbitrary-address model is also a security downgrade vs the capability-scoped relay.
- A register-only policy must not block anonymous callers: `Policy.ReachEnforced` gates the caller-identity wait — without it, data streams from spokes that never declare an identity hang (real bug, caught by test).
- Trust the client-cert identity, not the declared one: `verifiedIdentity` must be set before any stream so it overrides the spoke's `Registration.Identity`.
- hub+proxy on the same pod is rejected (conflicting egress interception), like dns+proxy.
- Delivery Targets hold a process-local `*yamux.Session`; any distributed registry MUST route the relay to the owning process (`/.cornus/v1/hub/forward`), and the forward handler must require a local-delivery Mux as a loop guard.
- Mount routing records ride the hub store but must never behave like services: store only the sessionID DIGEST (the raw id is the capability), register with a nil mux (no hub ingress onto the session), and filter `cornus.mount/*` out of both the catalog endpoint and catalog-watch pushes.
- The mount-forward local fast path must consult no store — otherwise single-replica deployments pay store traffic per mount stream (asserted by a counting-store test).
- Catalog-push polling must exist only while watchers are subscribed; unbind of dynamic Reach listeners must drain (stop accepting, let in-flight splices finish), not kill.
- An unreadable `CORNUS_CARETAKER_TLS_SECRET` must assume the full TLS layout and warn loudly — never silently fall back to plaintext when TLS was intended. `CORNUS_HUB_FORWARD_CA` parse failures are similarly fail-closed.
- In the caretaker DNS overlay, static records always win over dynamic ones, and the overlay contributes positive A answers only — do not let dynamic records change NODATA/forwarding semantics.
- UDP flows need idle-GC (60s) since UDP has no close, and payloads must be copied before framing (read-buffer reuse).
- New scenarios must be added to the Makefile SCENARIOS list — `deploy-hub.star` was initially missed, so `make e2e-check`/`make e2e-kube` silently skipped it.
- dind harness traps (from the KubeStore validation): the e2e image ships no socat/python3 (use the committed static `e2e/echoserver`); `kind create cluster` already writes ~/.kube/config (use it directly); dockerd startup remounts /tmp as tmpfs in the dind image, wiping dirs created before it — put work dirs under $HOME and create them after dockerd/kind come up.
- Version skew: the injecting server == the relaying server (same binary), so endpoint removals are safe unless `CORNUS_AGENT_IMAGE` pins an old sidecar image explicitly. A new-caretaker → old-server fallback was deliberately never built (closed as moot); newer protocol additions (catalog-push Watch, UDP port-forward ack, compose daemon Protocol stamp) each carry their own explicit skew story instead.
