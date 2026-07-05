# Guides

Task-focused, per-feature how-tos for everything Cornus can do. Each guide is a set
of short, copy-paste recipes with just enough context to run them — follow the
**See also** links into the [CLI reference](/cli/), [reference](/reference/deploy-spec),
and [topics](/topics/remote-workflows) pages when you need the full detail.

New to Cornus? Start with the [quick start](/introduction/quick-start), then come back
here to find the exact recipe for your task. For end-to-end scenarios that combine
several features, see the [Cookbook](/cookbook/).

## Find a guide

### [Building images](/guides/building-images)

Build a Dockerfile and push to the bundled registry, pass build args, use cache /
secret / SSH mounts, add named build contexts, build on a remote server (lazily),
import and export remote build cache, and build rootless.

### [Deploying workloads](/guides/deploying-workloads)

Deploy a Compose project or a raw deploy spec to a Docker host, a bare containerd
host, or Kubernetes; delete, detach, scale, and roll out; exec into a workload;
mount client-local directories; and reach published and unpublished ports.

### [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker)

Bring Compose projects up and down, inspect and rebuild services, use multiple
files / env files / profiles, run Dev Containers, and drive the stock `docker` CLI
against a Cornus server through the Docker API proxy.

### [Working with remote clusters](/guides/remote-clusters)

Point commands at a remote server, create connection profiles, auto port-forward
into an in-cluster server, mint short-lived credentials from your own kube access,
switch contexts, and route traffic through the server.

### [Networking recipes](/guides/networking)

Forward local ports, run a SOCKS5 split-tunnel proxy, choose a conduit, and wire
workloads together over the hub overlay.

### [Tunnels](/guides/tunnels)

Expose a workload port publicly through a hosted tunnel — step-by-step setup for
the ngrok, SSH, Cloudflare, and Tailscale backends.

### [Ingress](/guides/ingress)

Give a workload a public HTTP(S) hostname on the Kubernetes backend — auto-derived
or explicit hosts, TLS via cert-manager, and path/port/class routing.

### [Egress](/guides/egress)

Route a remote workload's outbound traffic through the caller network — for a VPN,
corporate proxy, or air-gapped cluster — with route rules or a PAC policy script.

### [Credentials](/guides/credentials)

Broker a caller-minted secret — including LLM API keys — into a workload without
baking it into the image, the spec, or the pod spec.

### [Registry and storage](/guides/registry)

Serve the registry on filesystem, in-memory, S3, or GCS / Azure storage; push and
pull images; allow anonymous pulls; advertise the registry to cluster runtimes; use
an external registry; and reclaim space with garbage collection.

### [Securing a server](/guides/security)

Require bearer tokens, mint JWTs, verify against JWKS, enable mTLS with per-identity
authorization, and allow anonymous pulls while protecting writes.

### [Observability](/guides/observability)

Enable OpenTelemetry traces, metrics, and logs; add a Prometheus `/metrics` endpoint;
configure logging; and wire up liveness and readiness probes.

### [Output modes](/guides/output-modes)

Choose how the CLI renders progress and results — `auto`, `fancy`, `plain`, or `json`
(NDJSON for agents and scripts) — and control color output.
