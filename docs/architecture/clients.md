# Docker-compatible clients and connection profiles

Three client surfaces funnel into the same server API and share one translation
pipeline: `cornus compose` (a docker-compose workalike), `cornus daemon docker`
(a Docker Engine API proxy for the stock `docker` CLI), and native Dev
Container support. All are subcommands of the single `cornus` binary.

## The Docker API proxy

`cornus daemon docker` serves a subset of the Docker Engine REST API on a unix
socket and translates container operations into Cornus deploys against a remote
server. Point `DOCKER_HOST` at its socket and the stock `docker` CLI runs
workloads on the remote Cornus, with the caller's local bind-mount directories
streamed over 9P.

Docker's create/start split is mapped onto Cornus's atomic apply by
**buffering**: `docker create` translates the request and stores a record
without contacting the server; `docker start` opens a long-lived deploy-attach
session that stays connected for the container's lifetime (so its 9P mounts
keep working). `docker ps`/`inspect` synthesize Docker-shaped responses from
the buffered record, echoing back create-time labels so label filters work.

`stop`/`start` round-trip at the **record level**: `stop` cancels the session
(tearing down the workload) but keeps the record marked `exited`, so
`docker ps -a` still lists it; `start` re-opens a session and re-deploys. This
is deliberately not a container-level pause — a client-served 9P mount cannot
outlive the caller's session, so the workload is recreated rather than paused,
consistent with Cornus's recreate-based deploy model. Coverage spans `run`,
`ps`, `inspect`, `stop`, `start`, `rm`, `logs`, `exec`, `attach` (including
interactive `-it`), `stats`, and `cp`, and stock `docker compose up/ps/down`
also works. `/build` is out of scope — builds belong to `cornus build`.

Foreground `docker run` works because the proxy replicates dockerd's exact
protocol, not just its routes: it parks an attach until the session goes live,
answers `wait?condition=next-exit` with the header immediately but the body
only at exit, and publishes lifecycle events in both encodings the CLI knows.
That fidelity is what lets the official `@devcontainers/cli` — the engine
behind VS Code's Dev Containers extension — work against the proxy unmodified.

## The Compose client and Dev Containers

`cornus compose` is a client, not a local driver: it parses the Compose file,
translates each service into a `DeploySpec` plus an optional build plan,
computes dependency order from `depends_on`, and drives a running server. `up`
builds (if a `build:` section exists), pushes to the registry, and deploys in
dependency order; `down` reverses it.

Because a client-served mount cannot outlive its client, a service with local
bind mounts needs a live session: `up` without `-d` runs those services in the
**foreground** until Ctrl-C, while `up -d` hands them to the unified client
agent (below), which holds the sessions in the background so the command
returns.

**Dev Containers** are read natively by translating
`.devcontainer/devcontainer.json` into the *same* compose project model the
Compose path uses — so every `up`/`down`/`ps`/`build` command is reused
unchanged. Both flavors are supported: single-container (`image` /
`build.dockerfile`) and compose-based (`dockerComposeFile` + `service`).
Lifecycle hooks (`onCreate`, `postCreate`, `postStart`, ...) run in the
container via server-side exec after the service is ready, which works in every
up path independent of who holds the 9P session.

## Declarative reconcile vs the imperative proxy

The two surfaces sit on opposite sides of a declarative/imperative line, by
design. A compose file *is* a desired-state description, so the compose path
runs a small **declarative reconcile engine**: callers apply a desired set of
services, and it drives the live resources — the 9P mount sessions and the
port-forward/SOCKS5 exposures — to match, with per-dimension fingerprints so an
exposure-only change does not tear down a healthy mount.

The Docker API is already imperative (`create`/`start`/`stop`/`rm` are discrete
edge events) and its containers are immutable, so the proxy does not reconcile;
a per-container state machine encodes the Docker API contracts instead. The two
share the layer *beneath*: the per-workload deploy-attach hold and the conduit
exposure primitive.

## The unified client agent

Every background client-held session lives in **one** long-lived process per
user — `cornus daemon agent` — reached over a single control socket
(`$XDG_RUNTIME_DIR/cornus/agent.sock`). `cornus compose up -d` and
`cornus daemon docker` are thin clients that ping-to-reuse or spawn the agent
and register work over the socket; `cornus daemon status` / `stop` inspect and
tear it down.

Clients pre-resolve the connection identity and send it with the work, because
the agent's process environment is frozen at spawn; the agent re-resolves
against the same profile logic, so a background compose session gets the
profile's token, TLS, and kube-auth. Work targeting the same server **shares
one connection and one conduit**, so a single SOCKS5 proxy spans docker
containers *and* compose services by name.

## The local web UI

[`cornus web`](/cli/web) is a client-side browser surface. Its BFF joins server
workload state with Compose project structure and the live background-agent
inventory, because those client-owned details do not exist in the server's
flattened API. The embedded SPA provides workload and project views, dependency
graphs, mounts, tunnels/forwards, allow-listed file editing, logs, and exec.

The UI has no authentication and is therefore loopback-only. Project apply reuses
`cornus compose ... up -d`, so the same reconcile engine and background-agent
lifetime rules govern both CLI and browser actions.

## Connection profiles and remote clusters

Reaching a Cornus server that lives *inside* a cluster used to take a hand-run
`kubectl port-forward` plus a hand-provisioned token. Connection profiles close
that gap client-side, with zero server changes:

- **Profiles** are kubeconfig-style contexts managed by `cornus config`:
  endpoint, TLS material, an optional port-forward target, and an optional
  kube-auth block. One resolver threads the selected context into every client
  command, with two precedence chains: explicit flag > context server > auto
  port-forward for the endpoint, and `CORNUS_TOKEN` > kube-auth mint > static
  profile token for the credential.
- **Auto port-forward**: a profile naming an in-cluster Service opens the
  `kubectl port-forward` equivalent for the command's lifetime and points the
  client at the local forwarded address. `cornus config set-context
  --namespace <ns>` discovers the client-facing cornus Service at config time;
  zero or multiple matches are a hard error listing the candidates.
- **Kube-auth**: a profile can mint a short-lived, audience-scoped
  ServiceAccount token via the Kubernetes TokenRequest API; the in-cluster
  server validates it through its existing JWKS verify path, so a developer's
  kube access doubles as a Cornus credential with no minting endpoint on the
  server.

A profile's TLS config applies to both the REST transport and every WebSocket
dial, so remote builds and deploy-attach sessions honor a custom CA or mTLS
client cert too. Note the two credentials are distinct: the kube credential
authenticates the port-forward *setup*, while the Cornus credential
authenticates *through* the tunnel — TokenRequest is the bridge.

## The pull-ref registry host is decoupled from the client endpoint

An image's identity is its **repository path**; the host is a per-vantage
rendezvous detail. This matters once the client, the build engine, and the node
no longer share one loopback: the build engine pushes from *inside* the pod
while the node's containerd pulls from the *host* network with node DNS — and a
port-forward endpoint (`127.0.0.1:<ephemeral>`) is unpullable by the node.

So the deploy image host is resolved separately from the control-plane
endpoint: an explicit override (`--registry` / `CORNUS_REGISTRY` / a profile
field) wins, else the server's auth-exempt info endpoint advertises one, else
the client endpoint's host. The advertised value comes from
`CORNUS_ADVERTISE_REGISTRY` or the kubernetes backend introspecting its own
Service — and only **NodePort / LoadBalancer auto-advertise**, because node
containerd uses host DNS, not cluster DNS, so a ClusterIP name would not
resolve at pull time. The server then **push-redirects** a build whose target
host equals the advertised host to the co-located registry over loopback,
keeping the repository path fixed so push and pull reach the same content
addressed differently.

## Related pages

- [Compose, devcontainers & docker](/guides/compose-devcontainers-docker) —
  the workflows.
- [Remote workflows](/topics/remote-workflows) — profiles and kube-auth setup.
- [Connection config](/reference/connection-config) — the profile file format.
- [Registry guide](/guides/registry) — advertising the registry to cluster
  runtimes.
- [cornus compose](/cli/compose) · [cornus daemon](/cli/daemon) ·
  [cornus config](/cli/config)
