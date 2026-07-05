# The caretaker and client-side features

Several Cornus features let a workload running on a remote server or cluster
behave as if it were running next to the developer: directories bind-mounted
from the caller's machine, outbound traffic leaving from the caller's network,
credentials minted on the caller's side. On Kubernetes these are realized by
exactly **one** injected sidecar per pod — the **caretaker** — and on the host
backends by equivalent host-side or companion-container mechanisms.

## Client-local bind mounts

A stateless deploy assumes any mount source is a path on the deploy host. That
breaks when a caller deploys to a *remote* server but wants to bind-mount
directories from the *caller's* machine. Cornus solves this by reusing the
build transport: a long-lived WebSocket deploy-attach session carries the
deploy, and the caller serves each named local directory over **9P** — the
caller is the 9P server, exactly as in a remote build. Drive it with
`cornus deploy --server <url> --local-mount SRC:DST[:ro]`.

The server-side realization differs by backend:

- **dockerhost** — the server kernel-9p-mounts each backing under
  its data dir and rewrites the mount source to that mountpoint before calling
  the backend, which binds it like any host path.
- **kubernetes** — the mount is realized *inside the pod*, never on a node
  host, so the pod can schedule anywhere. Per mount, the backend injects a
  shared `emptyDir`, a privileged native-sidecar mount agent that
  kernel-9p-mounts it with `Bidirectional` propagation, and an app-container
  `volumeMount` at the target. The sidecar's startup probe gates the app
  container until the mount is live.

The kernel 9P mount root must be a directory. For one file, dockerhost exports
the parent and binds the basename through a subpath. Kubernetes rejects a
single-file source because the shared sidecar mount cannot project it onto an
arbitrary rootfs target; directory mounts remain supported. The containerd
backend does not currently support client-local deploy mounts.

Because the mount is served *from the caller*, the deployment lives exactly as
long as the caller stays connected: when the session drops, the handler removes
the containers first, then unmounts the 9P backings. This deliberately scopes
the feature to dev / inner-loop use, not durable production workloads. A
read-write mount uses a writable confined export, so container writes propagate
back to the caller's local directory.

The pod cannot reach a NAT'd caller directly, so the **server is the
rendezvous**: it registers each attach session by id, and the pod's caretaker
dials one pod-scoped connection and opens one stream per mount, which the
server bridges to a fresh stream on the caller.

## One sidecar, many roles

The caretaker reads a single role config assembled by the kubernetes backend at
apply time and runs every role the pod needs under one process, so a workload
never carries more than one Cornus sidecar:

| Role | Talks to the server? | What it does |
|------|:---:|---|
| `mount` | yes | Relays each 9P mount back to the caller through the server. |
| `credential` | yes | Fetches client-minted credentials on demand through the server relay. |
| `proxy` | no | Intercepts app-container egress and forwards it to peer Services per the compose network policy. |
| `egress` | yes | Routes the app's outbound traffic through a client-side vantage point (below). |
| `dns` | no | Serves the app container on `127.0.0.1:53`; forwards unknown names upstream. |
| `hub` | yes | Registers hosted services and reaches peers through the [hub](/architecture/networking#the-workload-to-workload-hub). |
| `docker` | yes (client API) | Runs a Docker Engine API proxy on pod loopback, advertised via `DOCKER_HOST` (below). |
| `otel` | no | Runs an embedded OpenTelemetry Collector receiving the app's OTLP on pod loopback and exporting it to a configured backend (see [Observability](/guides/observability#workload-telemetry)). |

**The server-bound roles share one connection.** `mount`, `credential`,
`egress`, and `hub` all ride a single pod-scoped, always-on connection to the
server, multiplexed into streams. The session id travels inside each stream,
where it remains an unguessable capability. The `proxy` and `dns` roles are
self-contained data-plane roles between the app container and the cluster,
never touching the server.

## The Docker endpoint

The `docker` role gives a workload **loopback access to the cornus-managed
stack**: the caretaker runs the same Docker Engine API proxy that backs
`cornus daemon docker` on a pod-loopback endpoint, and the backend injects
`DOCKER_HOST` into the app container. Stock `docker` / `docker compose` inside
the pod then drive the very server that manages the pod's own stack — deploying
sibling workloads, running compose, exec — with no real Docker daemon in the
pod. It is opt-in via `DeploySpec.Docker` (Kubernetes only).

Unlike the other server-bound roles — which authenticate with a scoped
caretaker credential — the Docker proxy drives the full **client** deploy API,
which by design rejects the caretaker's scoped token. So the role carries its
**own** client-scoped bearer token, sourced from a dedicated Secret the
operator provisions (`CORNUS_CLIENT_TOKEN_SECRET`). Because this effectively
grants the workload deploy-engine access, the role is enabled only when that
token is configured, and it cannot share a pod with the enforcing proxy role
(which would redirect the endpoint's own dials).

## Client-side egress

A workload's outbound traffic normally leaves from wherever the runtime sits.
That breaks for **air-gapped clusters** (only the developer's machine reaches
the internet) and for **VPN / corporate-proxy / SASE** networks (the sanctioned
egress path lives on the client side). Client-side egress routes a remote
container's outbound traffic through a client-side vantage point, in three
modes of increasing transparency — `env` (propagate the caller's proxy
variables), `proxy` (the caretaker runs a real HTTP + SOCKS5 proxy on
loopback), and `transparent` (all app TCP captured by an nftables redirect).
See [client-side egress](/topics/egress) for modes, routes, and PAC usage; what
matters architecturally is the relay shape and its guarantees:

- **Reverse relay.** The caretaker classifies each connection's destination
  against the routing policy; a relayed route opens a stream on the pod-scoped
  server connection, and the server bridges it to the *client's* deploy-attach
  session. The client dials the destination through its **own** resolved proxy
  (corporate HTTP/SOCKS proxy or SASE gateway, honoring `NO_PROXY`) — the bytes
  physically leave from the client exactly as the client's own traffic would.
- **Policy is re-evaluated at every hop.** The server re-checks the policy
  (defense in depth — a compromised pod cannot upgrade its own routing) and the
  client re-checks it as a final guard. Destinations default to the `cluster`
  route, so enabling egress never silently diverts in-cluster traffic; a PAC
  script evaluates in a sandboxed JS engine with a bounded deadline, fail-closed
  to `deny`.
- **Two termini.** The `client` route needs a live session (the inner loop).
  The `gateway` route needs no client: the server itself is the egress node, so
  a `--detach` workload keeps egressing with nobody connected — gated by the
  operator opt-in `CORNUS_EGRESS_GATEWAY` and an optional policy ceiling, so a
  pod's request can never exceed what the operator permits.

On the host backends, the `proxy`/`transparent` modes run as a **companion
caretaker** container sharing the workload's network namespace.

## Related pages

- [Remote workflows](/topics/remote-workflows) — mounts and sessions in
  practice.
- [Client-side egress](/topics/egress) and the [egress guide](/guides/egress).
- [Credential brokering](/topics/credentials) and the
  [credentials guide](/guides/credentials).
