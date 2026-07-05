# Caretaker Transport and Hub Synthesis (unified connection, mounts, overlay)

## Summary

One pod-scoped, always-on yamux-over-WebSocket connection (`/.cornus/v1/caretaker/attach`) carries
everything between a workload's caretaker sidecar (or a laptop CLI spoke) and the Cornus server:
client-local 9P mount relays, hub control/data/deliver streams, catalog-watch pushes, and their
metrics — with optional TLS on every dial and cross-replica forwarding for both hub delivery and
mount sessions. This synthesis merges the deploy-attach mount feature, the hub network overlay,
and the caretaker's role-host
design, because they share the transport, the stream-tag namespace, the identity/policy model,
and the caretaker process itself.

## Included Documents

| Document | Focus |
|----------|-------|
| [client-local-mounts-deploy.md](./client-local-mounts-deploy.md) | Deploy-attach 9P mounts, dockerhost/k8s realization, rw mounts, metrics |
| [hub-network-overlay.md](./hub-network-overlay.md) | Hub registry/relay, discovery, policy/mTLS, UDP, multi-replica stores |
| [user-networks-and-caretaker.md](./user-networks-and-caretaker.md) | Caretaker role composition + exemption matrix (also feeds the kubernetes synthesis) |
| [client-side-egress.md](./client-side-egress.md) | Egress caretaker role, relay transport, gateway path, host companions, and metrics |

Note on paths: older docs say `internal/wire`, `internal/caretaker`, etc.; current paths are
`pkg/wire`, `pkg/caretaker`, `pkg/deploywire`, `pkg/hub`, `pkg/kubehub`, `pkg/server`.

## Stable Knowledge

### The unified connection

- Stream tags on the one yamux session: `'C'` hub control, `'M'` mount, `'D'` hub data,
  `'I'` hub ingress/deliver, `'L'` lazy/9P backing; the build wire separately uses `'S'` for SSH.
  Legacy endpoints (`/.cornus/v1/deploy/mount/{session}/{name}`, `/.cornus/v1/deploy/caretaker/{session}`,
  `/.cornus/v1/hub/attach`) are removed — everything rides `/.cornus/v1/caretaker/attach`
  (`handleCaretakerUnified`, `relayMountMuxed`).
- Role inversion on the mount leg: the HTTP/WS client (caller CLI or pod caretaker) is the 9P
  *server*; the Cornus server is the 9P *client*.
- The kernel 9p client needs one transport per `mount(2)` and bare 9P has no demux framing — a
  stream muxer (yamux) is mandatory for multi-mount pods. SOCKS is not a multiplexer (one SOCKS
  conn = one stream) and its connect-anywhere model is a security downgrade.
- Transport is swappable behind a `net.Conn` seam (`wire.AcceptConn`/`DialConn`/
  `DialControlHeader`); WebSocket earns its keep on NAT/ingress traversal.
- Every hub/caretaker WS dial can carry TLS: `caretaker.Config.TLSClientConfig *tls.Config`
  (json:"-", the in-process `cornus hub` CLI path — profile TLS material flows into the dial) plus
  `TLS *TLSFiles` (serializable ca/cert/key file paths for sidecars, loaded fail-fast at `Run`).
  Dials go through `wire.DialControlHeaderTLS`; nil config is byte-identical to the plain path.
  Inter-replica forward dials honor `CORNUS_HUB_FORWARD_CA` (PEM appended to the system roots;
  malformed CA = hard startup error, never silent system-roots-only).
- One handshake authenticates the whole connection: the caretaker adds
  `Authorization: Bearer <token>` (scoped `CORNUS_CARETAKER_TOKEN` or `caretaker`-scoped JWT,
  valid ONLY on this endpoint — see [auth-and-security.md](./auth-and-security.md)).

### Client-local mounts (deploy-attach)

- `cornus deploy --server <url> --local-mount SRC:DST[:ro]` streams caller-local dirs over 9P;
  the mount lives exactly as long as the client's `/.cornus/v1/deploy/attach` WebSocket (disconnect ->
  server tears the deployment down) — dev/inner-loop scoping by design.
- Server dispatch in `handleDeployAttach`: no local mounts -> plain `Apply`; kubernetes
  (`MountingBackend.ApplyWithMounts`) -> privileged native-sidecar 9P mount inside the pod (never
  hostPath, never on the node host); dockerhost -> `MountManager` kernel-9p mount under
  `<DataDir>/mounts/<session>/<name>` + source rewrite; anything else -> loud error.
- The NAT'd caller is unreachable from pods, so the server relays: sessions register under a
  random id (the capability; `AllowsMount` gates the name), and the pod caretaker dials back
  through the unified connection.
- Read-write mounts use `writablefs.go`, sharing `confinedfs.go`'s guard (no `..`, no symlink
  escape) with mutating ops delegated after a policy check; writes stay jailed to the export root.
- A kernel 9P mount root must be a directory. For a single-file source (notably Compose file-backed
  configs/secrets), `Client.DeployAttach` exports the parent and sends the basename as
  `LocalMount.Subpath`; dockerhost rewrites the runtime source to `<mountpoint>/<Subpath>`. The
  Kubernetes shared-emptyDir sidecar cannot realize one file at an arbitrary rootfs target, so it
  rejects file mounts explicitly rather than silently presenting a directory.
- Per-mount RX/TX byte metrics via `wire.MeteredConn` — callback-based so `pkg/wire`/
  `pkg/deploywire` stay OTel-free; it embeds the `net.Conn` interface so `io.Copy` cannot
  shortcut via `ReadFrom`/`WriteTo`. The server also emits a `cornus.mount.relay` span per
  relayed mount stream (session digest, transport `local|forwarded`, rx/tx) — see
  [observability-and-logging.md](./observability-and-logging.md).
- Multi-replica mount sessions ride the EXISTING hub store — no second store: a delivery-mode
  routing record under the reserved name `cornus.mount/<sha256(sessionID)[:16]>` (only the digest
  goes on the wire, so the raw session id stays an unguessable capability; nil mux so a hub relay
  can never open ingress onto the session; filtered out of both `/.cornus/v1/hub/catalog` and
  catalog-watch pushes). A non-owning replica forwards via `GET /.cornus/v1/mount/forward` through the
  shared `dialForward` helper (same bearer token and `CORNUS_HUB_FORWARD_CA` TLS as the hub
  forward). The local-session fast path consults NO store — single-replica behavior is
  byte-identical (asserted by a counting-store test); crash-safety rides the stores' existing
  TTL/Lease/ownerRef GC.
- dockerhost/containerdhost also have an opt-in remote-mode mount-relay path realized through a
  caretaker companion container/task instead of a server-host kernel mount — see
  [client-local-mounts-deploy.md](./client-local-mounts-deploy.md#mount-relay-via-a-caretaker-companion-dockerhostcontainerdhost-remote-mode)
  and [[remote-companion-and-agent-forwarding]] for how that companion later became a unified,
  always-on relay for port-forwarding and ssh-agent forwarding too.

### Diagnosability: stale mount-session ids reset silently, now WARN-logged

A deployment's mount session id is minted fresh per deploy-attach connection
(`newSessionID()` in `applyWithSidecarMounts`, `pkg/server/deploy_attach.go`) and baked FIXED into the
pod template's caretaker sidecar env for the pod's whole life (`caretakerConfigEnv`,
`pkg/deploy/kubernetes/kubernetes.go`), while the server's `s.mounts` registry is in-memory per
process. Whenever the pod's caretaker presents an id the server no longer holds — the server process
restarted (wiping `s.mounts`), or the owning deploy-attach connection was replaced under a fresh id —
`relayMountMuxed` misses, `relayMountRemote`'s single-replica path returns immediately, and the stream
closes before any 9P handshake completes. The kernel 9P mount over the caretaker's unix socket
surfaces this mid-handshake close as `connection reset by peer`, which is why every mount on the pod
fails together and near-instantly (observed ~4ms after "connected") with historically NOTHING logged
server-side — `mount_relay.go` deliberately tells the caretaker nothing beyond closing the stream, and
the outcome previously lived only in a trace span, invisible with tracing off (the default).

Fixed by `logMountReset` (`pkg/server/mount_relay.go`): a server-side WARN fires at every relay
rejection point — single-replica unknown session (the common case above), no-owner/missing routing
record, forward-to-owner failed, mount-name-not-allowed, 9P-backing-open failed — carrying the reason
and mount name; only the session-id DIGEST is logged, matching `traceMountRelay`, never the raw
capability. `TestRelayMountRemoteSingleReplicaLogsReset` (`pkg/server/mount_relay_reset_test.go`)
asserts the WARN fires with the reason, carries the mount name, and never leaks the raw id.

This failure mode is distinct from, and NOT resolved by, the one-shot-workload-as-Job fix in
[[kubernetes-backend]] — that fix stops a restart-forever Deployment from repeatedly re-presenting a
session that ended; a stale session id from a genuine server restart or connection replacement is
still a live possibility on any long-running deployment and still only surfaces as this WARN today.
Open design question, not yet resolved: make a deployment's mount session id stable across
server/client reconnects — e.g. on re-apply of an existing deployment, reuse the id already baked
into the running Deployment's caretaker env instead of minting a new one, so a reconnecting client
re-registers under the id the pod already presents.

### Hub overlay

- Spokes register named services over the `'C'` control stream; `hubRelay` serves `'D'` streams
  by name with two modes per registration: dial-direct (registry `Addr` the hub dials) and
  delivery (hub opens an `'I'` stream to the hosting spoke, which dials its local target —
  NAT-safe). Traffic: app -> caretaker Reach loopback listener -> hub -> target.
- Discovery: `hub.SyntheticIP(name)` deterministic `127.0.0.0/8` address; the caretaker `dns`
  role serves the SAME synthetic IP the Reach listener binds. Computed at deploy time;
  `GET /.cornus/v1/hub/catalog` lists live names.
- Reactive catalog push: a spoke setting the `Registration.Watch` capability receives
  `CatalogUpdate` frames over its existing control stream — an immediate kick on local
  register/disconnect plus a 3s hash-compare poll to converge cross-replica stores; the poll
  goroutine exists only while watchers are subscribed. Explicit skew story: unknown field
  ignored, no Watch = no frames.
- Dynamic reach: `HubRole.ReachDynamic` consumes catalog updates to bind/unbind synthetic-IP
  Reach listeners at runtime with drain-not-kill semantics (unbind stops accepting, in-flight
  splices finish); k8s deploys opt in via `api.HubSpec.ImportDynamic {Ports, Protocol}`.
  `runDynamicReach` publishes `name -> synthetic IP` into the dns role's `DynamicRecords`
  overlay after a successful bind and withdraws on unbind, joined via a process-wide rendezvous
  (one caretaker process = one pod) — so plain `dial(peer)` works for dynamically discovered
  names. Static records always win; the overlay serves positive A answers only.
- Policy: `CORNUS_HUB_POLICY` (reach, enforced in `hubRelay`) and `CORNUS_HUB_REGISTER_POLICY`
  (register, enforced in `hubControl`); nil = allow-all; malformed = hard startup error. Identity
  comes from `Identity(r)` (JWT sub / mTLS CN) with a `verifiedIdentity(r)` fallback, overriding
  any spoke-declared identity.
- UDP: 2-byte big-endian length-prefix framing (`wire/datagram.go`); per-source-flow model on the
  Reach end with 60s idle-GC; the relay itself stays byte-agnostic.
- Multi-replica `hub.Store` backends: in-memory (default), `RedisStore` (`CORNUS_HUB_REDIS`),
  `kubehub.KubeStore` (`CORNUS_HUB_STORE=kube`, CRD + Leases, zero external infra). Delivery
  Targets hold a process-local `*yamux.Session`, so distributed stores forward the relay to the
  owning replica via `GET /.cornus/v1/hub/forward` (local-delivery-Mux loop guard); mount sessions
  forward analogously (above).
- Helm multi-replica: `replicas > 1` wires the whole story — `CORNUS_HUB_STORE=kube`,
  downward-API identity, a per-pod `CORNUS_HUB_FORWARD_URL` via a headless Service (also the
  StatefulSet serviceName), preferred anti-affinity — and REFUSES to render unless `storage` is
  shared s3 (per-pod PVC CAS would split-brain). The single-replica render stays byte-identical.
- Cross-network spoke CLI: `cornus hub --server ... --register name=host:port --reach ...`.

### Caretaker (the role host)

- ONE caretaker sidecar per pod, ever. `caretaker.Config{Mounts, Proxy, DNS, Hub, Mark, Token}`
  is delivered via the `CORNUS_CARETAKER_CONFIG` env JSON; all roles run under one errgroup with
  per-role fail-fast (k8s restarts the sidecar). `cornus caretaker-check` backs the startup probe.
- Privilege/exemption matrix (full table in user-networks-and-caretaker.md): mounts need
  privileged root; proxy-only runs as uid 1337 with a uid redirect exemption; proxy+mounts
  collapses to a firewall-mark exemption (`SO_MARK` on EVERY caretaker socket including the 9P
  relay dial); DNS-only needs NET_BIND_SERVICE. hub+proxy and dns+proxy are rejected
  (conflicting egress interception).
- Connections group by server (`groupByServer`); a pod's mounts share one deploy-attach session
  id stamped by `applyWithSidecarMounts` with one shared `RelayURL` (`CORNUS_ADVERTISE_URL`).
- Sidecar TLS wiring (k8s): `CORNUS_CARETAKER_TLS_SECRET` mounts the Secret read-only at
  `/cornus/tls` in server-bound sidecars only (hub + mounts; dns/proxy untouched) and stamps the
  `Config.TLS` file paths into the embedded config. CA-only Secrets are supported; an unreadable
  Secret assumes the FULL layout with a loud warning — intended TLS never silently degrades to
  plaintext; unset = byte-identical pod specs. Helm exposes it as `caretakerTlsSecret` (env plus
  `resourceNames`-scoped `secrets get` RBAC).

## Operational Guidance

- New server-bound caretaker traffic should be a new tagged stream on the unified connection,
  not a new endpoint. Keep `pkg/hub` client-go-free (`pkg/kubehub` exists for that reason) and
  `pkg/wire`/`pkg/deploywire` BuildKit- and OTel-free.
- Anything touching relay/identity must be validated on a real cluster: nearly every real bug in
  this area (GC races, SO_ORIGINAL_DST offsets, IPv6 advertise URLs, register-only policy hangs)
  passed unit fakes and was caught only live (dind + kind; `make e2e-container`).
- Config env vars: `CORNUS_ADVERTISE_URL` (bracket IPv6), `CORNUS_AGENT_IMAGE`,
  `CORNUS_K8S_SIDECAR_IMAGE`, `CORNUS_HUB_POLICY`, `CORNUS_HUB_REGISTER_POLICY`,
  `CORNUS_HUB_REDIS`, `CORNUS_HUB_STORE`, `CORNUS_REPLICA_ID`, `CORNUS_HUB_FORWARD_URL`,
  `CORNUS_HUB_FORWARD_CA`, `CORNUS_CARETAKER_TLS_SECRET`.

## Files

- `pkg/wire/` — tags, `DialControlHeader`/`DialControlHeaderTLS`/`DialTLS`/`DialConnControl`,
  `datagram.go`, `metered.go`, `ninep_backing.go`
- `pkg/deploywire/` — deploy-attach spec/serve/backing, `Mount9P`/`Unmount9P`, preflight
- `pkg/hub/` — `Registry`, `Store`, `Policy`, `SyntheticIP`, client helpers, `redisstore.go`
- `pkg/kubehub/` — KubeStore (HubEndpoint CRD + Leases)
- `pkg/server/` — `deploy_attach.go`, `mount_relay.go`, `handleCaretakerUnified`, `hubControl`,
  `hubRelay`, `handleHubForward`, `GET /.cornus/v1/mount/forward`, shared `dialForward`,
  `CORNUS_HUB_FORWARD_CA` loading
- `pkg/caretaker/` — `caretaker.go` (`Config.TLSClientConfig`/`TLS *TLSFiles`),
  `runCaretakerConn`, mount/proxy/dns/hub roles (`ReachDynamic`), `hub_dynamic.go`
  (`runDynamicReach`), `dns.go` (`DynamicRecords` overlay), `mark_linux.go`
- `pkg/build/buildwire/writablefs.go`, `confinedfs.go` — confined 9P attachers (ro + rw)
- `cmd/cornus/` — `hub.go`, `caretaker.go`, `mountagent.go` (deprecated aliases), `commands.go`
  (`--local-mount`)
- `deploy/helm/cornus/` — `replicas` (multi-replica hub wiring), `caretakerTlsSecret`

## Tests

- In-process, hermetic: `caretaker_relay_test.go` (two mounts over one muxed connection, real
  9P), `hub_test.go` (relay/delivery/policy/mTLS/catalog), `hub_multireplica_test.go`
  (miniredis), datagram + UDP flow tests, writablefs confinement (raw p9.Client),
  `TestMountBytesMetered`; mount-relay forwarding: a two-replica end-to-end test over miniredis
  (9P read through the non-owning replica) plus a counting-store test asserting the local fast
  path touches no store.
- Live: `deploy-mounts.star`, `deploy-hub.star` (known dind-flaky on kube), `deploy-hub-udp.star`,
  `deploy-proxy-mounts.star`; `make e2e-multireplica` (real Redis) and
  `make e2e-multireplica-kube` (real kind KubeStore).

## Pitfalls

- Every caretaker socket must carry the exemption mark (including the 9P relay dial) or the
  caretaker's own traffic is swallowed by its redirect; never program a uid==0 exemption.
- A register-only hub policy must not block anonymous callers (`Policy.ReachEnforced` gates the
  identity wait) — real bug, caught by test.
- Trust the verified credential (mTLS CN / JWT sub) over the spoke-declared identity, set before
  any stream.
- The deploy lives only as long as the client's WebSocket — by design, not a bug; the server
  proactively tears down on disconnect because a dead 9P mount only yields EIO.
- dockerhost teardown order: containers first, then `unix.Unmount` (`MNT_DETACH` on EBUSY), then
  cleanup. Preflight cannot detect the mount-propagation trap — run the server in the host mount
  ns or bind `<DataDir>/mounts` rshared.
- hostPath is never a valid realization of a bind mount on kubernetes; reject loudly.
- UDP payloads must be copied before framing (read-buffer reuse); flows need idle-GC.
- Mount routing records ride the hub store but must never behave like services: store only the
  sessionID DIGEST (the raw id is the capability), register with a nil mux, and filter
  `cornus.mount/*` out of both the catalog endpoint and catalog-watch pushes.
- Catalog-push polling must exist only while watchers are subscribed; `ReachDynamic` unbind must
  drain (stop accepting, let in-flight splices finish), not kill.
- TLS is fail-closed everywhere: an unreadable `CORNUS_CARETAKER_TLS_SECRET` assumes the full
  layout and warns loudly (never silent plaintext); a malformed `CORNUS_HUB_FORWARD_CA` is a
  hard error (never silent system-roots-only).
- In the caretaker DNS overlay, static records always win over dynamic ones, and the overlay
  contributes positive A answers only — dynamic records must not change NODATA/forwarding
  semantics.
- Version skew is closed as moot: the legacy endpoints are gone, both sides ship in one binary,
  and each newer protocol addition (catalog-push Watch, UDP port-forward ack) carries its own
  explicit skew story. The only residual risk is `CORNUS_AGENT_IMAGE` pinning an old sidecar.
- A relay rejection (unknown session, no owner, forward failure, disallowed name, backing-open
  failure) closes the stream with NOTHING told to the caretaker beyond the close itself — always
  check the server's `logMountReset` WARN (session digest + reason + mount name) before assuming a
  pod-side "connection reset by peer" is a caretaker-side bug; it usually isn't.

## Egress Caretaker Role

`client-side-egress.md` adds a server-bound caretaker role alongside mounts, hub, DNS, and credentials. Proxy and transparent egress use the caretaker attach connection; transparent mode programs `pkg/netredirect`, marks its own upstream sockets, and must not coexist with the enforcing proxy role.

Egress connections and bytes are traced at the caretaker edge. Plain HTTP forwarding requires explicit write counting, unlike splice-based CONNECT and SOCKS paths. The one-caretaker-per-pod rule remains load-bearing: egress must fold into the existing caretaker rather than add a duplicate sidecar.

The startup probe calls `egressReady`, dialing the configured listener on loopback after the relay
session is established; `ListenPort == 0` means no listener is expected. This gates the app until
interception is usable. Relay-backed proxy/transparent services also require a persistent
deploy-attach session even when they have no mounts or published ports. Detached Compose therefore
routes `Egress.NeedsRelay()` services through the background agent; deploying on a temporary held
session and immediately tearing it down removes the workload.
