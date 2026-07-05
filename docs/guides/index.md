# Guides

One page per feature. Each guide opens with a short **How it works** section
explaining the model, then a set of copy-paste recipes for the tasks that model
supports — follow the **See also** links into the [CLI reference](/cli/) and the
[reference](/reference/deploy-spec) pages when you need the exhaustive flag and
field lists.

New to Cornus? Start with the [quick start](/introduction/quick-start), then come back
here to find the exact recipe for your task. For end-to-end scenarios that combine
several features, see the [Cookbook](/cookbook/).

## Find a guide

### [Setting up a server](/guides/server-setup)

Get a server running in the arrangement you want: local (Docker, containerd,
bare, Incus, or driving a cluster), on a remote host over SSH, inside the
cluster, or as a container. One short runbook each — prerequisites, the command,
and how to confirm it worked.

### [Building images](/guides/building-images)

Build a Dockerfile and push to the bundled registry, pass build args, use cache /
secret / SSH mounts, add named build contexts, build on a remote server (lazily),
import and export remote build cache, and build rootless.

### [Deploying workloads](/guides/deploying-workloads)

Deploy a Compose project or a raw deploy spec to a Docker host, a bare containerd
host, an Incus host, or Kubernetes; delete, detach, scale, and roll out; exec into a workload;
mount client-local directories; and reach published and unpublished ports.

### [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker)

Bring Compose projects up and down, inspect and rebuild services, use multiple
files / env files / profiles, run Dev Containers, and drive the stock `docker` CLI
against a Cornus server through the Docker API proxy.

### [Compose extensions and compatibility](/guides/compose-support)

The `x-cornus-*` fields a Compose service can declare and how project-level
defaults apply, compose-spec `provider:` services, what `up --watch` reloads, and
every flag where cornus and `docker compose` are not identical — implemented,
accepted but not honored, and deliberately different.

### [The browser UI](/guides/web-ui)

What `cornus web` puts in the browser: the file explorer's local roots, the tiled
workspace of browsers and terminals, the metrics dashboard, the ingress section, the
MCP endpoint agent clients drive, and how to reach the UI from another machine or
through one browser proxy setting.

### [Working with remote clusters](/guides/remote-clusters)

Point commands at a remote server, create connection profiles, auto port-forward
into an in-cluster server, mint short-lived credentials from your own kube access,
switch contexts, and route traffic through the server.

### [Running the server in a container](/guides/server-in-a-container)

Run the server itself as a container on the docker host it manages: the bind
mounts it needs and why, how it works out the correspondence between its paths
and the host's, checking a configuration before you commit to it, and reaching
workloads from a containerized server.

### [Networking and conduits](/guides/networking)

How a session's conduit mode picks between per-port forwarding and one SOCKS5
split-tunnel proxy; then forward local ports, run the proxy, reach a whole Compose
stack through one browser setting, and reach a workload's ingress host with no DNS.

### [The workload hub](/guides/hub)

The star-hub relay model that connects workloads sharing no routable network, its
policy matrices and multi-replica registry; then join as a spoke from the CLI and
export / import services in the deploy spec.

### [Tunnels](/guides/tunnels)

How the server hosts a tunnel and what each backend needs; then step-by-step setup
for the ngrok, SSH, Cloudflare, and Tailscale backends.

### [Ingress](/guides/ingress)

How hosts resolve, how routing and the server-side domain policy work; then give a
workload a public HTTP(S) hostname on the Kubernetes backend — auto-derived or
explicit hosts, TLS via cert-manager, your own certificates, and path/port/class
routing.

### [Egress](/guides/egress)

The three egress modes and four routes; then route a remote workload's outbound
traffic through the caller network — for a VPN, corporate proxy, or air-gapped
cluster — with route rules or a PAC policy script.

### [Credentials](/guides/credentials)

The source backends, delivery kinds, and trust model; then broker a caller-minted
secret — including LLM API keys and AWS STS credentials — into a workload without
baking it into the image, the spec, or the pod spec.

### [Registry and storage](/guides/registry)

Serve the registry on filesystem, in-memory, S3, or GCS / Azure storage; push and
pull images; allow anonymous pulls; advertise the registry to cluster runtimes; use
an external registry; and reclaim space with garbage collection.

### [Security and authentication](/guides/security)

How bearer verification and caller identity work; then require bearer tokens, mint
JWTs, verify against JWKS, enable mTLS with per-identity authorization, and allow
anonymous pulls while protecting writes.

### [Observability](/guides/observability)

Enable OpenTelemetry traces, metrics, and logs; add a Prometheus `/metrics` endpoint;
configure logging; and wire up liveness and readiness probes. Or turn on the
built-in store and get your *workloads'* logs, traces, and metrics — searchable,
surviving the container, and queryable from Grafana — with no backend to run.

### [Output modes](/guides/output-modes)

Choose how the CLI renders progress and results — `auto`, `fancy`, `plain`, or `json`
(NDJSON for agents and scripts) — and control color output.
