# Cornus Overview

Cornus brings the Docker development workflow — `docker compose`, the `docker` CLI,
and devcontainers — to a Kubernetes cluster (or a plain Docker host) from a single Go
binary (`module cornus`, Go 1.26, Apache-2.0). It collapses three tools an internal
platform usually runs separately into one self-contained service so a small team can
build → push → deploy a compose project onto a real cluster without standing up a
registry, a BuildKit daemon, and a GitOps controller individually.

## Subsystems

1. **Registry** — a tiny OCI Distribution v1.1 registry (`/v2/*`) backed by a persistent
   sha256 content-addressable store. Persistence is **pluggable** (`pkg/storage`):
   filesystem (default), in-memory, and S3 / S3-compatible object storage, selected with
   `--storage` (`gs://`/`azblob://` are available behind a `-tags cloudblob` build).
   Survives restarts; backed by a volume / PVC or an object bucket. A `/v2/*` miss can
   fall through to a pull-through mirror (`CORNUS_REGISTRY_MIRROR`), and on a host backend
   `/v2/*` **defaults to a view over the local runtime's image store**
   (`CORNUS_REGISTRY_SOURCE=host-native`, resolved per backend) so a developer reuses their
   existing images instead of a separate registry. Under `containerd` this is a full
   **read-write** view backed by the containerd content store (a push imports into it, via a
   `registry.Store` implementation); under `dockerhost` it is a **read-only** `docker save`
   view (a push 405s; `cornus build` routes through the server, which `docker load`s). With no
   `--storage` no separate CAS is kept; an explicit `--storage` layers a CAS (union) and
   `CORNUS_REGISTRY_SOURCE=off` keeps the classic push-able registry.
2. **Build engine** — an in-process BuildKit solver (no separate `buildkitd`) with
   `docker buildx` parity: Dockerfile builds, cache mounts (`RUN --mount=type=cache`),
   secret mounts (`RUN --mount=type=secret`), SSH agent forwarding
   (`RUN --mount=type=ssh`), remote cache (`--cache-to`/`--cache-from`), and remote builds
   that stream the caller's context over 9P (optionally lazily, on demand). Exposed via a
   CLI (`cornus build`) and HTTP endpoints (`/.cornus/v1/build`, `/.cornus/v1/build/attach`).
3. **Deploy engine** — an imperative, pluggable deploy backend with **four backends**:
   `dockerhost` (default) runs containers on a Docker host, `containerd` runs them
   natively on a bare containerd host (no dockerd; CNI bridge networking), `bare`
   runs them directly through an OCI runtime with no daemon at all, and `kubernetes`
   (client-go) deploys Deployments + Services into a cluster (e.g. kind),
   and — opt-in per deploy (`DeploySpec.Ingress`, kube only) — an auto-host-derived
   `networking.k8s.io/v1` Ingress for public HTTP(S) exposure.
   No git-watch / continuous reconciliation in v1. On top of the core, the deploy side
   also provides client-local bind mounts streamed to remote workloads over 9P
   (`/.cornus/v1/deploy/attach`), automatic client-side forwarding of published ports (a
   `host:` port is reachable at `127.0.0.1:<host>` on the client on every backend, and
   `cornus port-forward` reaches even unpublished ports), public exposure of a workload
   through a hosted tunnel (`cornus tunnel`, pluggable ngrok / ssh / cloudflare
   backends), and client-side egress that routes a remote workload through the caller
   network when it needs a VPN, corporate proxy, or SASE path. Compose user networks,
   a per-pod multi-role `caretaker` sidecar (including an opt-in pod-loopback Docker
   API endpoint), and a workload-to-workload hub overlay complete the runtime surface.

The subsystems integrate over OCI HTTP, not shared Go storage: the build engine pushes an image
reference to a registry and the target runtime pulls it. The registry CAS is private persistence
behind `pkg/storage`, so any subsystem can instead use an external OCI registry.

Security and observability are layered and **opt-in, zero-cost when off**: bearer auth
(static token / JWT / JWKS), mTLS identity, per-identity API/registry authorization, and
default-deny workload privilege policies on one axis; OpenTelemetry traces/metrics/logs plus
an optional Prometheus `/metrics` on the other. See the root `ARCHITECTURE.md` for both.

## Recent runtime capabilities

Kubernetes can realize a first-class Knative Serving descriptor as a native Service with autoscaling and scale-to-zero; other targets preserve its portable deployment form while warning that Knative behavior is unavailable. Workloads can also opt into an embedded OpenTelemetry Collector that exports application telemetry independently of the Cornus observability pipeline.

## Deployment model

Cornus ships as a container image (plus prebuilt static CLI binaries) and runs both as a **local Docker container**
(Compose file embedded in README.md "Local Docker") and as a first-class **Kubernetes service** (StatefulSet + PVC + Service +
RBAC; Helm chart under `deploy/helm/cornus`). Pre-built multi-arch images are released to
`ghcr.io/moriyoshi/cornus` (semver tags), with third-party license attribution
bundled inside the image; releases also attach static CLI binaries (linux/darwin/windows)
with a `SHA256SUMS` manifest, publish the Helm chart as an OCI artifact, and sign all of it
with keyless cosign. Both the image and downloadable binaries embed the built SolidJS web UI;
the frontend is built once before Go compilation so every artifact serves the same interface.
The shipped k8s manifests/chart preset the `kubernetes` deploy
backend. The README Quick start stands the whole thing up Docker-free on single-node k3s,
bootstrapping the Cornus image with Cornus itself. The registry and deploy subsystems need no
special privileges; the build engine needs root or a rootless user-namespace stack — see
the root `ARCHITECTURE.md` "Running with the right privileges".

## Interfaces

* HTTP: `/v2/*` (registry), `/.cornus/v1/build` + `/.cornus/v1/build/attach`,
  `/.cornus/v1/deploy[/{name}[/{action}]]` + `/.cornus/v1/deploy/attach`, `/.cornus/v1/caretaker/attach`
  (pod sidecar rendezvous), `/.cornus/v1/hub/catalog`, `/.cornus/v1/gc`, `/healthz`, `/readyz`, and an
  opt-in Prometheus `/metrics`.
* CLI (kong): `serve`, `setup`, `config`, `build`, `push`, `deploy`, `exec`, `port-forward`,
  `tunnel`, `socks5`, `compose`, `web`, `daemon`, `hub`, `token`, `health`, `version` (plus hidden sidecar-facing
  aliases such as `caretaker` / `caretaker-check`). `cornus config` manages
  kubeconfig-style connection profiles that can auto-port-forward to an in-cluster
  server and mint short-lived credentials from the caller's kube access, so every
  command works against a remote cluster with no manual tunnel or token.
* `cornus compose`: a Docker Compose-compatible command group (`up`/`down`/`ps`/`build`/
  `restart`/`stop`/`start`) that redirects Compose commands to a running Cornus server. It also
  reads a Dev Container definition (`.devcontainer/devcontainer.json`) natively — both the
  single-container and compose-based flavors, with lifecycle commands and the workspace mount — via
  `pkg/devcontainer` (see ARCHITECTURE.md "The Compose client and Dev Containers"). Implemented in
  `cmd/cornus/internal/composecli`.
* `cornus web`: loopback-only browser UI for workloads, Compose projects and dependency
  graphs, mounts, tunnels/forwards, configuration files, logs, and exec. It runs a
  client-side BFF because Compose structure and live agent sessions do not exist in the
  server's flattened workload API.
* `cornus daemon`: long-running client-side helpers — `daemon docker`, a local
  Docker Engine API proxy (point `DOCKER_HOST` at it and the stock `docker` CLI,
  `docker compose`, and even the official `@devcontainers/cli` drive a remote Cornus
  server; see ARCHITECTURE.md "The Docker API proxy"). The Docker frontend is hosted
  by the unified per-user client agent. `cornus compose up -d` uses that same agent to
  hold client-local mounts, forwarded ports, SOCKS5 aliases, and relay-backed egress;
  `daemon status` / `stop` inspect and stop it.
* `cornus-e2e`: the Starlark E2E runner used for testing (docker / kind / local targets);
  deliberately kept a separate binary (dev tooling, not part of the product surface).

## Documentation

User-facing documentation is one multilingual VitePress site under `docs/` (English at `/`, Japanese
at `/ja/`, Simplified Chinese at `/zh/`; Introduction, Guides, Cookbook, CLI, Reference, Topics),
adapted from `README.md` and the root `ARCHITECTURE.md`, built and deployed to
GitHub Pages by `.github/workflows/docs.yml`. `README.md` is a slim landing page that links into it.
When a change alters a user-facing flag, env var, config/spec/chart field, backend, or behavior,
update the matching `docs/` page too.

See [ARCHITECTURE.md](../../ARCHITECTURE.md) for the module layout and design decisions.
