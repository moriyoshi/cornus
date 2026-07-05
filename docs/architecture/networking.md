# Networking: port forwarding, tunnels, ingress, and the hub

Four mechanisms carry traffic to and between workloads, each aimed at a
different direction: **port forwarding** brings a workload port to the caller's
machine, a **public tunnel** exposes one to the internet, an **ingress** is the
cluster-native front door, and the **hub** connects workloads to each other
across networks. They share transport underneath — everything except ingress
rides the same server-bridged byte tunnels.

## Port forwarding

`cornus port-forward --server <url> <name> [LOCAL:]REMOTE ...` forwards a local
TCP port to a container port of a deployment's first instance. The CLI binds
one local listener per mapping and, for each accepted connection, opens its own
WebSocket tunnel to the server, which bridges it to the backend — so the
feature is backend-agnostic and reaches ports the workload never published:

- **dockerhost** inspects the container for its IP and dials it directly. This
  assumes the server can route to the Docker bridge, which holds when the
  server runs on or with the Docker host — the normal deployment.
- **kubernetes** rides the `pods/portforward` SPDY subresource through the API
  server, so it works from an out-of-cluster kubeconfig with no sidecar and no
  route to the pod network. Kubernetes port-forward is TCP-only.
- **containerd** dials the instance's recorded CNI IP directly.

On a cluster connection profile the client does **not** route a kubernetes
forward through the server by default — the server's own ServiceAccount usually
lacks `pods/portforward` RBAC, so a server-proxied forward would silently fail.
Instead the client dials the workload pod directly with the developer's own
kubeconfig and keeps the server-proxied path only as a pre-traffic fallback.
The `via-server` profile toggle forces the server-routed path; the same
direct-first, proxy-fallback rule governs workload logs.

**UDP mappings** (`5353:53/udp`) work on the dockerhost and containerd
backends: the tunnel carries length-framed datagrams, one tunnel per client
source address with an idle timeout. The server acks a UDP tunnel before the
first frame, so an incapable backend or an older server rejects the dial
cleanly.

**Published ports forward automatically.** Every client session surface —
`cornus deploy --server`, `cornus compose up` (foreground or `-d`), and the
docker frontend — publishes `DeploySpec.Ports` through the same engine, so a
`host:` port means "reachable on the client at `127.0.0.1:<host>`" on every
backend. Each surface takes `--no-forward-ports`; skips (an unforwardable UDP
mapping, an already-bound local port) warn and continue rather than failing the
session. See the [networking guide](/guides/networking) for the workflows.

## Public tunnels

Where port-forward hands the caller a local listener, `cornus tunnel <name> <port>`
hands back a **public URL**. The server hosts the tunnel in-process and
bridges each inbound connection to the workload through the *same* byte-bridge
port-forward uses — so it reaches unpublished ports on any backend, and the
tunnel is just a hosted relay put in front of that bridge.

The client injects the tunnel credential on the already-authenticated request;
the server never knows the credential beforehand (an operator can set a
server-side default instead). The provider seam has two shapes: a **listener
model** for backends that yield a real listener the server accepts on (ngrok),
and an **upstream model** for backends that can only forward to a local URL
(cloudflared, tailscale), where the server stands up a loopback shim and hands
the backend its address. Four backends ship — ngrok (in-process, default), ssh
(remote-forward with fail-closed host-key verification; works with sish,
serveo, or plain sshd), cloudflare, and tailscale. See the
[backends table](/topics/tunnels) for what each needs and the
[Tunnels guide](/guides/tunnels) for step-by-step setup instructions.

## Automatic ingress (Kubernetes only)

Where a tunnel is a hosted relay the server runs per invocation, an **ingress**
is a cluster-native front door: when `DeploySpec.Ingress` opts in, the
kubernetes backend creates a standard Ingress alongside the ClusterIP Service,
owner-referenced to the Deployment so it is reaped on delete. The other
backends log a warning and ignore the field, so a Compose file stays portable.

The distinctive property is **automatic host derivation**, aimed at ephemeral
preview environments. With a base domain configured on the server
(`CORNUS_INGRESS_DOMAIN`, plus `CORNUS_INGRESS_CLASS` and
`CORNUS_INGRESS_TLS_ISSUER`), a deploy that merely *enables* ingress gets a
public URL for free: the compose translator derives `<service>.<project>` and
the backend prefixes it to the domain — `web.pr-123.<domain>` — so many
projects coexist on one wildcard domain. Every default is client-overridable in
the spec, and a multi-tenant server can pin its domain with
`CORNUS_INGRESS_ENFORCE_DOMAIN`, which rejects any resolved host outside it so
a client cannot claim an arbitrary hostname. See
[public ingress](/topics/ingress) for the full field reference.

The trade-off vs a tunnel: ingress is cluster-native and survives detached
deploys, but needs an ingress controller plus wildcard DNS (and a cert-issuer
for HTTPS); `cornus tunnel` needs none of that and works on any backend, but
lives only as long as its command.

## Session conduits: port-forward or SOCKS5

Automatic forwarding is the default **conduit mode** a client session uses to
expose workloads to the caller. The opt-in alternative replaces per-port local
listeners with a single client-side **SOCKS5 split-tunnel proxy**: a CONNECT
target is matched against resolution rules; a match is rewritten to a
`service:port` and tunneled inward over the same transport, while an unmatched
target is dialed directly from the caller's host — cluster names go in,
everything else egresses normally.

The everyday default rule strips a service-host suffix:
`web.cornus.internal:8080` reaches service `web`, so one proxy reaches every
service by name without pre-declaring ports. A session alias table additionally
maps short compose service names to their project-prefixed deployments, so the
bare `web:8080` also routes inward when it unambiguously names a live service.
The mode is resolved per session (`--conduit` flag, `CORNUS_CONDUIT`, or the
connection profile), and the shared proxy spans compose services and docker
containers on the same connection; `cornus socks5` runs the same proxy
standalone. SOCKS5 CONNECT is TCP-only. See
[remote workflows](/topics/remote-workflows) for configuration and precedence.

## The workload-to-workload hub

The server doubles as a **star hub** connecting workloads that share no
routable network — cross-node, cross-cluster, or a NAT'd laptop. Each
participant is a **spoke** (a pod's caretaker, or `cornus hub` from a CLI); a
spoke registers the services it hosts and reaches other spokes' services by
name.

### The relay model

A spoke registers each service in one of two modes:

- **dial-direct** — registered with an address the hub can reach; the hub dials
  it itself.
- **delivery** — registered with no address; the hub reaches the service by
  opening an ingress stream *back to the hosting spoke*, which dials its own
  local target and splices. This is what makes NAT'd and cross-cluster targets
  reachable — the hub never needs a route to them.

One connection per spoke carries control, egress streams, and ingress streams;
traffic flows `app -> caretaker -> hub -> {dial | ingress to spoke}`. The relay
is byte-agnostic, so **TCP and UDP** both work — datagrams are length-framed
onto the stream and converted back at the two ends.

### Discovery and policy

An imported peer maps deterministically to a **synthetic `127.0.0.0/8` IP**. On
Kubernetes the caretaker's DNS role serves each peer's name at the same
synthetic IP its loopback listener binds — so an app's plain `dial(peer)`
funnels into the hub with no application awareness. Imports can be **dynamic**:
a spoke with the `watch` capability receives catalog updates pushed over its
control stream and binds a listener per discovered service.

Policy is two optional matrices, enforced only when configured: a **reach**
matrix (caller identity to allowed callee services, `CORNUS_HUB_POLICY`) and a
**register** matrix (identity to hostable names,
`CORNUS_HUB_REGISTER_POLICY`). Under mTLS the identity is taken from the
verified client certificate, so policy keys on a credential the spoke cannot
forge.

### Running more than one replica

Delivery targets hold live sessions to their spokes, and only the replica
holding a spoke's connection can open an ingress stream to it. The
load-bearing simplification: each replica is the *sole authority* for the
spokes connected to it, so replicas own **disjoint registry partitions** — the
distributed registry is just their union, with no write conflicts and no merge
logic. A dead replica's partition drops; its spokes reconnect through the load
balancer and re-register under a new owner.

Two shared stores ship: **Redis** (`CORNUS_HUB_REDIS`; liveness is a TTL'd
heartbeat key) and a **Kubernetes-native store** (`CORNUS_HUB_STORE=kube`;
providers are custom resources, liveness is a Lease, GC rides an owner
reference — no external infrastructure). When a lookup lands on a replica that
is not the delivery owner, it forwards to the owner and splices, giving a
two-hop path. Honest framing: the hub is a control-plane relay, and a single
replica is fine for most deployments; multi-replica buys HA of the overlay and
connection-count scale at the cost of that extra hop.

## Related pages

- [Networking guide](/guides/networking) — recipes for forwarding and reaching
  workloads.
- [Public tunnels](/topics/tunnels) — tunnel backends and per-backend setup.
- [The workload hub](/topics/hub) — the overlay's relay model and usage.
- [Public ingress](/topics/ingress) — the ingress field reference.
- [Remote workflows](/topics/remote-workflows) — conduit modes and profiles.
- [cornus port-forward](/cli/port-forward) · [cornus tunnel](/cli/tunnel) ·
  [cornus socks5](/cli/socks5) · [cornus hub](/cli/hub)
