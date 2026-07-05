# The workload hub

The Cornus server doubles as a **star hub** connecting workloads that share no
routable network — cross-node, cross-cluster, or a NAT'd laptop. It generalizes
the live-mount rendezvous idea from "relay one caller's export into one pod" to
"relay arbitrary TCP/UDP flows between registered workloads." Each participant is
a **spoke**; a spoke registers the services it hosts and reaches other spokes'
services by name. For the public-internet counterpart — exposing one workload
port to the outside world instead of to other workloads — see
[public tunnels](/topics/tunnels).

## Joining from the CLI

`cornus hub` joins the overlay from anywhere — for example a NAT'd laptop — to
offer a local service to the overlay and/or reach an overlay service by name.
`--server` is optional and falls back to the selected connection profile (an
explicit `--server` wins).

```sh
cornus hub --server ws://cornus.example:5000 \
  --register api=localhost:8080 --reach db=127.0.0.1:5432
```

`--register name=addr` offers a local service under a name; `--reach
name=local-addr` binds a local address that forwards into the overlay's service
of that name. See [`cornus hub`](/cli/hub). (A profile carrying client-TLS
material — a custom CA, an mTLS cert, or insecure-skip-verify — is refused for
`hub` for now; use a server certificate the system trust store accepts.)

## The relay model

A spoke registers each service in one of two modes:

- **dial-direct** — registered with an address the hub can reach; the hub dials
  it itself.
- **delivery** — registered with no address; the hub reaches the service by
  opening an ingress stream *back to the hosting spoke*, which dials its own
  local target and splices. This is what makes NAT'd and cross-cluster targets
  reachable — the hub never needs a route to them.

To reach a peer, a source spoke opens a data stream naming the service; the hub
looks it up and either dials (direct) or delivers (via the owning spoke), then
splices the bytes. Both **TCP and UDP** work — the relay just copies bytes, so
UDP only needs framing at the two conversion points, selected with a `/udp` port
suffix. Traffic flows `app -> caretaker -> hub -> {dial | ingress to spoke}`.

## Discovery in the deploy spec

For workloads deployed on Kubernetes, hub membership is declared with a `hub:`
block in the deploy spec rather than the CLI. `export` lists services the
workload hosts; `import` lists services it reaches (the backend allocates a
synthetic loopback IP per import and wires a DNS record plus a caretaker listener
to it, so a plain `dial(peer)` in the app resolves to the synthetic IP and
funnels into the hub with no application awareness).

```yaml
name: api
image: localhost:5000/api:v1
hub:
  identity: api                 # policy identity (defaults to the deployment name)
  export:
    - { name: api, port: 8080 }
    - { name: udpecho, port: 9000, protocol: udp, deliver: true }
  import:
    - { name: db, ports: [5432] }
```

Set `deliver: true` on an export when the service is not reachable from the hub
(the hub relays to the pod, which dials the port on localhost). `importDynamic`
opts a workload into dynamic discovery: instead of a static `import` list, the
caretaker subscribes to hub catalog pushes and binds a listener at the
deterministic synthetic IP of every cataloged service as services appear and
vanish. The full field reference is in the [deploy spec](/reference/deploy-spec).

## Policy

Access is governed by two optional matrices, each enforced only when configured:
a **reach** matrix (caller identity to allowed callee services,
`CORNUS_HUB_POLICY`) and a **register** matrix (identity to hostable service
names, `CORNUS_HUB_REGISTER_POLICY`). A spoke declares its identity on its
control stream, but under mTLS the identity is taken authoritatively from the
verified client certificate's CommonName, so policy keys on a credential the
spoke cannot forge. See [auth and TLS](/topics/auth-and-tls) for how identity is
established.

## Running more than one replica

The hub can run single-replica (the default in-memory registry, fine for most
deployments) or multi-replica for HA and connection-count scale. Each replica is
the sole authority for the spokes connected to it, so replicas own disjoint
registry partitions and their union is the distributed registry — no write
conflicts, no CRDT merge. A dead replica's whole partition drops and its spokes
reconnect through the load balancer under a new owner. Store selection is
`CORNUS_HUB_REDIS` (two replicas sharing one Redis form one hub), then
`CORNUS_HUB_STORE=kube` (the Kubernetes API server as a lease-backed registry,
no external infrastructure), else the in-memory single-replica registry. When a
delivery lookup lands on a non-owner replica it forwards to the owner over an
authenticated internal endpoint, giving a two-hop delivery path.
