# What is Cornus?

Cornus brings the Docker development workflow — `docker compose`, the `docker` CLI, and devcontainers — to a Kubernetes cluster (or a plain Docker host) from a single Go binary. It collapses three tools an internal platform usually runs separately into one self-contained service, so a small team can build, push, and deploy a Compose project onto a real cluster without standing up a registry, a BuildKit daemon, and a GitOps controller individually.

The project is a single module (`module cornus`, Go 1.26, Apache-2.0).

## The three subsystems

Cornus bundles the registry, build engine, and deploy engine it all runs on into one binary.

1. **Registry** — a tiny OCI Distribution v1.1 registry (`/v2/*`) backed by a persistent sha256 content-addressable store. Persistence is pluggable: filesystem (default), in-memory, and S3 / S3-compatible object storage, selected with `--storage` (`gs://` / `azblob://` are available behind a `-tags cloudblob` build). It survives restarts, backed by a volume / PVC or an object bucket. See [storage backends](/reference/storage-backends).
2. **Build engine** — an in-process BuildKit solver (no separate `buildkitd`) with `docker buildx` parity: Dockerfile builds, cache mounts (`RUN --mount=type=cache`), secret mounts (`RUN --mount=type=secret`), SSH agent forwarding (`RUN --mount=type=ssh`), named build contexts / bind mounts, and remote cache. Builds run locally or on a remote Cornus server, with the caller's directories, secrets, and SSH agents streamed over 9P-on-WebSocket — optionally lazily, so only the bytes a build actually reads cross the wire. Exposed via the [`cornus build`](/cli/build) CLI and the `/.cornus/v1/build` HTTP endpoints.
3. **Deploy engine** — an imperative, pluggable deploy backend with four backends: `dockerhost` (default) runs containers on a Docker host, `containerd` runs them natively on a bare containerd host (CNI bridge networking, no dockerd), `bare` runs them directly through an OCI runtime with no daemon, and `kubernetes` (client-go) deploys Deployments + Services into a cluster. There is no git-watch / continuous reconciliation in v1. On top of the core, the deploy side also provides client-local bind mounts streamed to remote workloads over 9P, automatic client-side forwarding of published ports, public exposure of a workload through a hosted tunnel, and client-side egress that routes a remote workload through the caller network. See [deploy backends](/reference/deploy-backends).

The subsystems integrate over OCI HTTP, not shared Go storage: the build engine pushes an image reference to a registry and the target runtime pulls it. The registry content store is private persistence behind `pkg/storage`, so Cornus can also use an external OCI registry.

## The build → push → deploy flow

A workload reaches a cluster in three steps that map directly onto the subsystems:

1. **Build** an image with the build engine.
2. **Push** it to a registry (Cornus's own, or an external one).
3. **Deploy** it by applying a spec to a deploy backend, which pulls the image and runs it.

[`cornus compose up`](/cli/compose) is sugar over these primitives; you can also drive [`cornus build`](/cli/build), [`cornus push`](/cli/push), and [`cornus deploy`](/cli/deploy) directly when you want explicit control. See the [quick start](/introduction/quick-start) for a full walkthrough.

## Deployment model

Cornus ships as a container image (plus prebuilt static CLI binaries) and runs both as a local Docker container and as a first-class Kubernetes service (StatefulSet + PVC + Service + RBAC; a Helm chart is provided). Pre-built multi-arch images are released to `ghcr.io/moriyoshi/cornus` (semver tags + `edge`), with third-party license attribution bundled inside the image. Releases also attach static CLI binaries (linux/darwin/windows) with a `SHA256SUMS` manifest, publish the Helm chart as an OCI artifact, and sign all of it with keyless cosign.

The registry and deploy subsystems need no special privileges; the build engine needs root or a rootless user-namespace stack. See [installation](/introduction/installation) to get the binary, and the [architecture overview](/architecture/) for the privilege posture.

## Interfaces

* **HTTP:** `/v2/*` (registry), `/.cornus/v1/build` + `/.cornus/v1/build/attach`, `/.cornus/v1/deploy[/{name}[/{action}]]` + `/.cornus/v1/deploy/attach`, `/.cornus/v1/caretaker/attach` (pod sidecar rendezvous), `/.cornus/v1/hub/catalog`, `/.cornus/v1/gc`, `/healthz`, `/readyz`, and an opt-in Prometheus `/metrics`.
* **CLI (kong):** [`serve`](/cli/serve), [`setup`](/cli/setup), [`config`](/cli/config), [`build`](/cli/build), [`push`](/cli/push), [`deploy`](/cli/deploy), [`exec`](/cli/exec), [`port-forward`](/cli/port-forward), [`tunnel`](/cli/tunnel), [`socks5`](/cli/socks5), [`compose`](/cli/compose), [`daemon`](/cli/daemon), [`hub`](/cli/hub), [`token`](/cli/token), [`health`](/cli/version-health), and [`version`](/cli/version-health). [`cornus config`](/cli/config) manages kubeconfig-style connection profiles that can auto-port-forward to an in-cluster server and mint short-lived credentials from the caller's kube access, so every command works against a remote cluster with no manual tunnel or token. See [working with remote clusters](/guides/remote-clusters).
* **`cornus compose`:** a Docker Compose-compatible command group (`up` / `down` / `ps` / `build` / `restart` / `stop` / `start`) that redirects Compose commands to a running Cornus server. It also reads a Dev Container definition (`.devcontainer/devcontainer.json`) natively — both the single-container and compose-based flavors, with lifecycle commands and the workspace mount.
* **`cornus daemon`:** long-running client-side helper daemons — `daemon docker`, a local Docker Engine API proxy (point `DOCKER_HOST` at it and the stock `docker` CLI, `docker compose`, and even the official `@devcontainers/cli` drive a remote Cornus server), and `daemon mounts`, the per-project background mounts daemon spawned by `cornus compose up -d`.

## Where to go next

* [Comparison](/introduction/comparison) — how Cornus relates to Skaffold, Tilt, Telepresence, Mutagen, Werf, and friends.
* [Installation](/introduction/installation) — get the CLI, the container image, or build from source.
* [Quick start](/introduction/quick-start) — a serve → build → deploy walkthrough.
* [Output modes](/guides/output-modes) — `auto` / `plain` / `fancy` / `json` rendering.
* [Architecture](/architecture/) — the module layout and design decisions.
