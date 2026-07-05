# Docker-free build and deploy from CI

## The scenario

A CI pipeline (or a k3s / containerd node) needs to build an image and roll it
out to a cluster, but there is no Docker daemon anywhere — no `dockerd`, no
`buildkitd`, and no separately provisioned registry. An in-cluster Cornus server
does all three jobs: its in-process BuildKit engine builds the image, its bundled
OCI registry stores it, and the runtime (containerd / Kubernetes) pulls it. The
CI runner only carries the `cornus` binary and the source tree.

## What you'll use

- The in-process build engine, driven over 9P from the runner — see [Building images](/guides/building-images) and [Remote workflows](/topics/remote-workflows).
- The bundled OCI registry that stores the built image — see [Registry and storage](/guides/registry).
- The imperative deploy engine on the `kubernetes` (or `containerd`) backend — see [Deploying workloads](/guides/deploying-workloads) and [Deploy backends](/reference/deploy-backends).
- A connection profile so pipeline steps need nothing on the command line — see [`cornus config`](/cli/config).

## Walkthrough

1. **Point the runner at the in-cluster server once.** Store a connection
   profile so every later step resolves the endpoint (and, for an ingress-less
   cluster, opens its own port-forward to the Service) with nothing on the
   command line. A profile that names a server also routes remote builds there
   automatically.

   ```sh
   cornus config set-context ci \
     --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
   cornus config use-context ci
   ```

   For a reachable URL, `--server http://cornus.example:5000` works just as
   well. On CI, mint the bearer token from the runner's own Kubernetes access
   (`--kube-auth-service-account` / `--kube-auth-audience`) or pass a static
   `--token`.

2. **Build in the cluster and push into the bundled registry.** The runner
   streams the build context and any secrets to the server over
   9P-on-WebSocket; the in-process BuildKit engine builds, and the result is
   pushed into Cornus's co-located registry. The runner needs no Docker and no
   build privileges.

   ```sh
   cornus build --builder ws://cornus.example:5000/.cornus/v1/build/attach \
     -t cornus.example:5000/app:$CI_COMMIT_SHA \
     --secret id=npmrc,src=$HOME/.npmrc \
     --rootless ./context
   ```

   `--rootless` (or the server-wide `CORNUS_ROOTLESS`) runs the build inside user
   namespaces. Because a profile already names the server, you can drop
   `--builder` and let the build route remotely on its own. To pull only the
   bytes a build actually reads, add `--lazy` on the named build contexts.

3. **Deploy the freshly built image.** Apply a native deploy spec against the
   same server. On the `kubernetes` backend this renders a Deployment plus a
   Service; the node's containerd pulls the image back from the bundled
   registry.

   ```yaml
   # deploy.yaml
   name: app
   image: cornus.example:5000/app:$CI_COMMIT_SHA
   replicas: 3
   restart: unless-stopped
   ports:
     - { host: 8080, container: 80 }
   updateConfig:
     parallelism: 1
     order: start-first
   healthcheck:
     test: ["CMD", "curl", "-f", "http://localhost/healthz"]
     interval: 30s
     retries: 3
   ```

   ```sh
   envsubst < deploy.yaml > deploy.rendered.yaml
   cornus deploy -f deploy.rendered.yaml --server http://cornus.example:5000 --detach
   ```

   `--detach` POSTs the spec and returns, leaving the workload running with no
   client session — the right mode for a fire-and-forget pipeline step. Tear a
   deployment down later with `cornus deploy -f deploy.yaml --delete --server ...`.

4. **Or do both in one command with Compose.** If the project already has a
   `compose.yaml` with a `build:` section, `cornus compose up --build` builds
   every service image in the cluster, pushes it, and deploys it — the same
   daemonless path, one command.

   ```sh
   cornus compose up --build -d
   ```

## How it works

The three subsystems integrate purely over OCI HTTP: the build engine pushes an
image reference to the registry, and the target runtime pulls it. Nothing in the
loop is a Docker daemon. The build engine is the **same** BuildKit solver
`docker buildx` uses, embedded in-process, so cache mounts, secret mounts, SSH
forwarding, and named build contexts all work unchanged — see
[Building images](/guides/building-images). On a remote build the runner runs a
read-through 9P server over one WebSocket and the engine reads the context in
place, so a private CI runner behind NAT never has to expose anything.

The registry that stores the result is Cornus's own bundled OCI Distribution
registry; the image reference you tag with is the registry host the server
advertises to cluster nodes, resolved via `--registry` / `CORNUS_REGISTRY`, then
the server's `GET /.cornus/v1/info`, then the endpoint host. On a multi-node cluster the
node's containerd must resolve and trust that host (mark it plain-HTTP or serve
TLS) — see [Registry and storage](/guides/registry).

The deploy engine applies the spec imperatively against the selected backend.
`kubernetes` renders Deployments and Services; `containerd` runs workloads
natively on a bare containerd host with CNI bridge networking and no dockerd. On
the `containerd` backend, pairing the deploy backend with the containerd **build
worker** (`CORNUS_BUILD_WORKER=containerd`) lands a just-built image straight in
the host's image store, so it deploys without a registry round trip at all. The
full backend matrix is in [Deploy backends](/reference/deploy-backends).

## Variations

- **Bare containerd node, no Kubernetes.** Run the server as root with
  `CORNUS_DEPLOY_BACKEND=containerd CORNUS_BUILD_WORKER=containerd`, and `cornus
  compose up --build` builds and deploys onto the host's own containerd.
- **External registry.** Tag builds for a registry you already run and set
  `CORNUS_REGISTRY` so the deploy pull refs point at it; the flow is otherwise
  identical.
- **Registry cache across runs.** Add `--cache-to` / `--cache-from
  type=registry,ref=...` so a cold CI runner reuses the previous build's cache.

**See also:** [Cookbook](/cookbook/) · [Building images](/guides/building-images) · [Deploying workloads](/guides/deploying-workloads) · [Registry and storage](/guides/registry) · [Remote workflows](/topics/remote-workflows) · [Deploy backends](/reference/deploy-backends)
