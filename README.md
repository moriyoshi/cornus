# Cornus

<p align="center">
<img src="./assets/cornus-logo.png" alt="Cornus logo" />
</p>

Bring your Docker workflow — `docker compose`, the `docker` CLI, and
devcontainers — to a Kubernetes cluster (or a plain Docker host), from a single
Go binary that bundles the registry, build engine, and deploy engine it all runs
on:

1. **Registry** — a tiny OCI Distribution v1.1 registry (`/v2/*`), backed by a
   persistent content-addressable store with pluggable persistence (filesystem,
   in-memory, S3, and — behind a build tag — GCS/Azure Blob).
2. **Build engine** — an **in-process BuildKit solver** (no separate `buildkitd`)
   with `docker buildx` capabilities: Dockerfile builds, cache mounts
   (`RUN --mount=type=cache`), secret mounts (`RUN --mount=type=secret`), SSH
   agent forwarding (`RUN --mount=type=ssh`), named build contexts / bind mounts,
   and remote cache. Builds run locally or on a remote Cornus server, with the
   caller's directories, secrets, and SSH agents streamed over 9P-on-WebSocket —
   optionally lazily, so only the bytes a build actually reads cross the wire.
3. **Deploy engine** — an imperative, pluggable deploy backend. A
   **dockerhost** backend, a native **containerd** backend (CNI bridge
   networking, no dockerd needed), and a **client-go kubernetes** backend ship
   behind the same interface, with client-local bind mounts, Compose user
   networks, client-side egress through the caller network, a multi-role caretaker
   sidecar, and a workload-to-workload hub overlay on top.

The subsystems integrate over OCI HTTP: the build engine pushes an image reference
to a registry and the target runtime pulls it. The registry content store is private
persistence behind `pkg/storage`, so Cornus can also use an external OCI registry.

**Contents:**
[Quick start](#quick-start) ·
[Comparison](#comparison-with-similar-tools) ·
[Documentation](#documentation) ·
[Architecture](#architecture) ·
[Tests](#tests) ·
[License](#license)

## Quick start

Cornus is primarily meant to run **inside a local Kubernetes cluster**. This
walkthrough goes from nothing installed to a workload running in a single-node
[k3s](https://k3s.io/) cluster, using the **prebuilt `cornus` binary** from
[GitHub Releases](https://github.com/moriyoshi/cornus/releases) and the
**multi-arch (amd64/arm64) image** published to
[GHCR](https://github.com/moriyoshi/cornus/pkgs/container/cornus)
(`ghcr.io/moriyoshi/cornus`). No clone, no Go toolchain, and **no Docker**: k3s
runs containerd natively, Cornus's own in-cluster build engine builds the demo
image, and `cornus compose` talks to the server directly — so there is no Docker
daemon anywhere in the loop. The whole thing is an ordinary `compose.yaml` and one
command. (Variants for k0s, kind, and plain Docker are at the end.)

### 1. Install the Cornus CLI

Download the prebuilt static binary for your platform and put it on `PATH`:

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

(For arm64, swap `amd64` for `arm64`.)

### 2. Install k3s and Cornus, then point the CLI at it

Cornus is exposed on a fixed **NodePort** (`30500`) so both your CLI and the
node's containerd reach it at a real service endpoint — nothing here depends on a
`kubectl port-forward`. First tell k3s's containerd that `localhost:30500` is a
plain-HTTP registry — the demo image you build in step 3 is served from there —
then install k3s (single-node, kubeconfig readable without sudo):

```sh
sudo mkdir -p /etc/rancher/k3s
sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  "localhost:30500":
    endpoint:
      - "http://localhost:30500"
EOF
curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
```

(`kubectl` below is the standalone binary; `sudo k3s kubectl` works without
installing one.) Now install Cornus. The shipped manifest — a privileged
StatefulSet (the build engine needs it) with a PVC, RBAC for deploying into the
cluster, and a **NodePort `Service`** (`30500` -> container `5000`) — already points
at the published GHCR image, so there is **nothing to build**: the node's
containerd pulls `ghcr.io/moriyoshi/cornus` straight from GHCR.

```sh
kubectl apply -f https://raw.githubusercontent.com/moriyoshi/cornus/main/deploy/k8s/cornus.yaml
# or, the recommended Helm path — install the chart straight from the OCI registry:
#   helm install cornus oci://ghcr.io/moriyoshi/charts/cornus --version 0.1.0
# (or, with the chart checked out: helm install cornus deploy/helm/cornus)
kubectl rollout status statefulset/cornus --timeout=300s
```

Check if the server is up and ready to serve at the NodePort `:30050`:

```sh
curl http://localhost:30500/healthz        # -> {"status":"ok"}
```

Store it as your default **connection profile** so no later commands need `--server` or `CORNUS_HOST`:

```sh
cornus config set-context demo --server http://localhost:30500
cornus config use-context demo
```

### 3. Build and deploy with a Compose file

The Cornus CLI speaks Compose. Write an ordinary `compose.yaml` — the same file
`docker compose` would read — whose `build:` section exercises a cache mount and a
secret mount, with one published port:

```sh
mkdir -p demo
tee demo/Dockerfile >/dev/null <<'EOF'
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl busybox-extras
RUN --mount=type=secret,id=token \
    test -f /run/secrets/token && echo "secret present (not stored in image)"
RUN mkdir -p /www && echo 'cornus demo' > /www/index.html
# Serve the page on :80 with busybox httpd (bundled in alpine) so the forwarded
# 127.0.0.1:8080 actually answers. The startup echo keeps `logs` showing the banner.
CMD ["sh", "-c", "echo cornus demo && exec httpd -f -v -p 80 -h /www"]
EOF
echo -n s3cret > /tmp/token

tee demo/compose.yaml >/dev/null <<'EOF'
name: demo
services:
  web:
    build:
      context: .
      secrets:
        - token
    ports:
      - "8080:80"
secrets:
  token:
    file: /tmp/token
EOF
```

Now bring it up. **One command** builds the image in the cluster (the context and
the secret stream over 9P-on-WebSocket to the Cornus pod, so your host never needs
build privileges or Docker), pushes it into Cornus's in-cluster registry, deploys
it, and forwards the published port back to your machine:

```sh
cd demo
cornus compose up
```

The service is built and deployed as `localhost:30500/demo-web:latest`. That
registry host is the one the server advertises at `GET /.cornus/v1/info` (the NodePort
address a node can pull from), not your CLI endpoint; the rest of the ref is
`<project>-<service>`, so a `build:` service sets no `image:` of its own. The build
engine, running inside the pod, pushes into its own co-located registry, and the
node's containerd pulls `localhost:30500/demo-web:latest` back through the NodePort
under the plain-HTTP rule from step 2 — no port-forward in the loop. The command
holds the session in the foreground — streaming any client-local mounts and
tunneling the workload's published port — and prints `forwarding 127.0.0.1:8080 ->
:80`. The demo container serves a page on `:80`, so `curl http://127.0.0.1:8080`
returns `cornus demo` even though the workload runs in the cluster. Leave it
running.

### 4. Inspect and clean up

From another terminal (the workload is named `<project>-<service>`):

```sh
kubectl get deployment,service demo-web
kubectl logs deployment/demo-web           # -> cornus demo
cornus compose logs demo-web               # same logs, no kubectl needed
```

`cornus compose logs` streams each service's logs — add `--follow` to follow,
`--tail`, `--since`, or `-t` for timestamps, and name services to filter (default:
all). For a cluster profile it reads the pod's logs directly with your kubeconfig
(the same credentials `kubectl` uses, so it works even when the Cornus server's
ServiceAccount lacks log access), falling back to fetching them through the server
only if that direct read is unavailable; other profiles always go through the
server.

Then tear it down — Ctrl-C the foreground `cornus compose up` to release the
published-port tunnel, remove the services, and remove the cluster:

```sh
cornus compose down
/usr/local/bin/k3s-uninstall.sh
rm -rf demo /tmp/token
```

**Variants.**

* **k0s** works the same way — a single binary running containerd natively
  (`k0s install controller --single`); configure its containerd for the
  plain-HTTP `localhost:30500` registry (same `registries.yaml` as step 2), point a
  profile at `http://localhost:30500`, and the rest of the flow is identical.
* **kind**, if Docker is available, also works — kind pulls the Cornus image from
  GHCR directly, but kind nodes are Docker containers, so a node port on the kind
  node is not reachable at your host's `localhost` without a mapping. Either map the
  node port when creating the cluster (`extraPortMappings` for `30500`) so the flow
  above works unchanged, or load the image into the node **between** build and
  deploy with the two-step [native flow](#driving-the-engine-directly): after
  `cornus build ... -t localhost:30500/demo:v1 demo`, run
  `docker pull localhost:30500/demo:v1 && kind load docker-image localhost:30500/demo:v1`,
  then `cornus deploy -f demo.yaml`.
* **Plain Docker, no Kubernetes** — run the published image as a privileged
  container with the Docker socket mounted, deploying workloads to the local
  daemon via the `dockerhost` backend:

  ```sh
  docker run -d --name cornus --privileged -p 5000:5000 \
    -v cornus-data:/var/lib/cornus \
    -v /var/run/docker.sock:/var/run/docker.sock \
    ghcr.io/moriyoshi/cornus:latest          # server on http://localhost:5000
  cornus config set-context demo --server http://localhost:5000
  cornus config use-context demo
  # step 3 as above; `cornus compose up` builds and deploys to the local daemon.
  ```
* **Bare containerd host, no Docker and no Kubernetes** — deploy straight onto a
  host's containerd (k3s/k0s/nerdctl/plain containerd), with no dockerd in the
  loop. Run the server as root with the `containerd` deploy backend, and pair it
  with the containerd **build worker** so a just-built image is runnable from the
  host's own image store without a registry round trip:

  ```sh
  sudo CORNUS_DEPLOY_BACKEND=containerd CORNUS_BUILD_WORKER=containerd \
    cornus serve --storage /var/lib/cornus      # server on http://localhost:5000
  # Needs the containerd socket (CORNUS_CONTAINERD_ADDRESS, default
  # /run/containerd/containerd.sock), root (it creates netns + runs CNI), and the
  # CNI plugins (bridge, portmap, host-local, loopback) under /opt/cni/bin.
  cornus config set-context demo --server http://localhost:5000
  cornus config use-context demo
  # step 3 as above; `cornus compose up` builds and deploys onto the host's containerd.
  ```

  See [Deploy backends → The containerd backend](https://cornus.dev/architecture/deploy-engine#the-containerd-backend) for the
  full configuration (socket, root, CNI plugins, snapshotter, insecure registries).

From here: the rest of [Using the CLI](https://cornus.dev/cli/) (remote builds, caches,
client-local mounts, exec, the hub), [Docker-compatible clients](https://cornus.dev/architecture/clients)
for `docker compose` / `docker` drop-in use,
[Connecting to a remote server](https://cornus.dev/guides/remote-clusters) to point at a
remote or ingress-less cluster (a stored profile, an auto-forward to the in-cluster
Service by name or by namespace, and minting the token from your kube credentials),
and — before exposing a server to anyone else —
[Security](https://cornus.dev/architecture/security).

### Driving the engine directly

`cornus compose up` is sugar over two primitives — the **build engine** and the
**deploy engine** — that you can drive directly when you want explicit control,
have no Compose file, or need to interleave a step (the kind variant above loads
the image into the node between build and deploy):

```sh
# Build in the cluster and push to the registry. --builder streams the context and
# the secret over 9P-on-WebSocket to the Cornus pod, so the host needs no Docker
# and no build privileges:
cornus build --builder ws://localhost:30500/.cornus/v1/build/attach \
  -t localhost:30500/demo:v1 \
  --secret id=token,src=/tmp/token demo

curl http://localhost:30500/v2/demo/tags/list    # -> {"name":"demo","tags":["v1"]}

# Deploy from a native spec — the schema every higher-level surface translates
# into. It uses the current connection profile (an explicit --server overrides):
tee demo.yaml >/dev/null <<'EOF'
name: demo
image: localhost:30500/demo:v1
replicas: 1
restart: unless-stopped
ports:
  - { host: 8080, container: 80 }
EOF
cornus deploy -f demo.yaml
```

The spec's fields (`name` / `image` / `replicas` / `restart` / `env` / `ports` /
`mounts`) are what `cornus compose`, `cornus daemon docker`, and the devcontainer
support all ultimately produce. Reach for the primitives when you want them; reach
for `compose` when you want the familiar workflow.

## Comparison with similar tools

Cornus overlaps with a crowded ecosystem, but its combination is unusual: a
**single self-contained binary** that is *at once* the registry, the image
builder, and the deploy engine, and that drives all three from your **existing**
`compose.yaml`, `docker` commands, or `devcontainer.json` — with **no new config
DSL** and **no pre-existing registry, `buildkitd`, or GitOps controller** to
stand up first. Most tools in this space orchestrate components you already run;
Cornus *is* those components. The tools it is most often compared to fall into
three groups.

### Inner-loop "dev on Kubernetes" orchestrators

[Skaffold](https://skaffold.dev/), [Tilt](https://tilt.dev/),
[DevSpace](https://www.devspace.sh/), [Garden](https://garden.io/), and
[Okteto](https://www.okteto.com/) automate the build -> push -> deploy loop
against a cluster. They are **orchestrators**: they shell out to your builder
(`docker` / BuildKit / kaniko), push to a registry you provide, and apply
manifests / Helm / kustomize you write, all driven by a tool-specific config file
(Skaffold YAML, a `Tiltfile`, `devspace.yaml`, Garden's project graph). Cornus
differs on two axes: it **bundles** the builder and registry rather than calling
out to them, and it consumes the **Docker artifacts you already have** — a
Compose file or a devcontainer — instead of a new DSL. Where Okteto and DevSpace
sync your source into a dev container running in the cluster, Cornus keeps your
files on your machine and streams only the bytes a build or bind mount actually
reads over 9P.

### Local <-> remote-cluster bridges

[Telepresence](https://www.telepresence.io/), [mirrord](https://mirrord.dev/),
and [Gefyra](https://gefyra.dev/) run a process **locally** while making it
behave as if it were **in** the cluster — intercepting a running pod's traffic,
environment, and file reads down to your laptop. Cornus solves the adjacent
problem from the other direction: it **deploys the workload into the cluster**
and brings the cluster back to you — published ports auto-forward to
`127.0.0.1`, `cornus exec` / `cornus port-forward` reach any container port, a
SOCKS5 conduit resolves `*.cornus.internal` to services by name, and the
workload-to-workload hub connects services across NAT and cluster boundaries. If
your goal is "run my code locally against cluster dependencies," reach for
mirrord or Telepresence; if it is "get my Compose project *running* in the
cluster with the inner-loop conveniences of local Docker," that is Cornus.

### Remote file-sync tools

A whole category of remote-dev tooling exists just to keep a local directory and a
remote one in step. Almost all of it reduces to **two sync engines**:
[Mutagen](https://mutagen.io/) (with its
[mutagen-compose](https://mutagen.io/documentation/orchestration/compose/)
integration; acquired by Docker in 2024 and now the basis of Docker Desktop's
synchronized bind mounts) and [Syncthing](https://syncthing.net/), descendants of
the classic [Unison](https://github.com/bcpierce00/unison) and `rsync`
(+ `lsyncd`). The Kubernetes dev tools mostly wrap one of the two —
[ksync](https://ksync.github.io/ksync/) and [Okteto](https://www.okteto.com/) drive
Syncthing, [Garden](https://garden.io/)'s code-sync drives Mutagen — while
[DevSpace](https://www.devspace.sh/) ships its own and
[Skaffold](https://skaffold.dev/docs/filesync/) / [Tilt](https://tilt.dev/) copy
changed files into the running container on change. All of them share one model:
**copy** your tree to the far side, then continuously reconcile the two copies —
buying local-speed remote reads and offline tolerance, at the cost of a second
materialized copy, an initial full transfer, and bidirectional conflict resolution.

Cornus is not in that camp at all. It does not sync, it **serves**, so it is really
the network-filesystem family — **sshfs**, **NFS**, **virtiofs** (Docker Desktop's
VM bind path), 9P — that it belongs to. During a remote build or a client-local
bind mount, the caller runs a read-through 9P server and the workload reads the
caller's files **in place** — a single source of truth, so no divergence, no
conflict resolution, and no upfront copy. What distinguishes it from a plain
network mount is the transport and the scoping: 9P tunneled over one WebSocket
(so it works through NAT with no mount daemon on either side), confined to the
context / named-context / mount directories and filtered through `.dockerignore`,
and — with `--lazy` — served on demand, so only the bytes a build or mount actually
touches ever cross the wire (a 20 MB context whose build reads 11 bytes transfers
11 bytes). The trade-off is the mirror image of sync's: an uncached read depends on
the link rather than on a resident local copy, so Cornus aims at the inner-loop /
dev case, not long-lived offline work. If your workflow is "edit here, run there,
keep both sides converged," a dedicated syncer like Mutagen is purpose-built for it;
Cornus folds the equivalent capability into its own transport, with nothing extra
to run. (Mutagen also forwards network ports, which Cornus covers with its own
per-connection tunnels — see [Deploy workloads](https://cornus.dev/guides/deploying-workloads) above.)

### The components Cornus subsumes

| You would otherwise run | Cornus's take |
| --- | --- |
| [BuildKit](https://github.com/moby/buildkit) / `buildkitd` as a daemon | embeds the **same** BuildKit solver in-process — full `buildx` feature set, no daemon |
| [Docker Registry](https://github.com/distribution/distribution) (`distribution`), [Zot](https://zotregistry.dev/), [Harbor](https://goharbor.io/) | a built-in tiny OCI Distribution v1.1 registry with a pluggable content store |
| [Kompose](https://kompose.io/) / [Docker Compose Bridge](https://docs.docker.com/compose/bridge/) | those convert Compose to manifests **once**; Cornus keeps Compose as the live control surface |
| [nerdctl](https://github.com/containerd/nerdctl) (Docker CLI over containerd) | the containerd deploy backend runs Compose projects natively on a bare containerd host, and also targets Docker and Kubernetes |
| stock `docker` / `docker compose` against a local daemon | the same commands, redirected to a remote Cornus server (`cornus daemon docker`, `cornus compose`), with files streamed from your machine |

The closest single-binary analogue is [Werf](https://werf.io/), which also builds
and deploys to Kubernetes from one binary — but Werf is Git-driven and still
relies on an external registry and a Helm-based apply, whereas Cornus is
Compose / devcontainer-driven, ships its own registry, and reconciles a
`DeploySpec` imperatively across Docker, containerd, and Kubernetes alike.

## Documentation

📖 **Full documentation: <https://cornus.dev/>**

The complete user reference is a VitePress site built from [`docs/`](./docs) and
published to GitHub Pages. It is searchable and organized into:

- **[Introduction](https://cornus.dev/introduction/what-is-cornus)**
  — what Cornus is, installation, and a quick start.
- **[Guides](https://cornus.dev/guides/)** — one page per feature, each opening
  with how the feature works and then the recipes for using it: building images,
  deploying workloads, Compose and devcontainers, remote clusters, networking and
  conduits, the workload hub, tunnels, ingress, egress, credentials, registry and
  storage, security and authentication, observability, and output modes.
- **[Cookbook](https://cornus.dev/cookbook/)** — end-to-end
  scenarios such as running an AI agent with client egress routing, ephemeral
  preview environments, Docker-free CI, and shipping a Compose project to Kubernetes.
- **[CLI reference](https://cornus.dev/cli/)** — every command and
  flag, with the global flags and output modes.
- **[Reference](https://cornus.dev/reference/deploy-spec)** — the
  deploy spec, connection config, server environment variables, and storage and
  deploy backends.
- **[Architecture](https://cornus.dev/architecture/)** — the reader-facing
  architecture section: subsystems, wire protocols, and the security model.

To preview the docs locally, see [`docs/README.md`](./docs/README.md).

## Architecture

The full design document — subsystem boundaries, wire protocols, security layers, and
closed decisions — is [ARCHITECTURE.md](./ARCHITECTURE.md).

## Tests

```sh
go test ./...
```

The registry, deploy backends (dockerhost + kubernetes via a fake clientset), and
server APIs are covered without external daemons. The full build-engine integration
test (`pkg/build/builder`) and the S3 storage test are opt-in (root / a rootless userns
stack / a live S3 endpoint) and skip otherwise.

Beyond the Go suite, a Starlark-powered end-to-end harness drives a real Cornus
server against a Docker host, a kind-managed Kubernetes cluster, or a build-only
local target. See [`.agents/docs/TESTING.md`](./.agents/docs/TESTING.md) for the
full testing guide — scenarios, `make` targets, preflight, and the containerized
runner.

## License

Cornus is licensed under the [Apache License, Version 2.0](./LICENSE).
Copyright 2026 Moriyoshi Koizumi. See [NOTICE](./NOTICE) for details.

Release images ship third-party attribution under
`/usr/share/doc/cornus/third-party-licenses/` — the license text of every Go
module linked into the binary (generated with
[go-licenses](https://github.com/google/go-licenses) at image build time),
plus a `THIRD_PARTY_LICENSES.csv` manifest. Regenerate locally with
`make third-party-licenses` (output in `bin/third-party-licenses/`).
