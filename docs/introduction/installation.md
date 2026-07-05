# Installation

Cornus is a single Go binary. The same binary serves the server (`cornus serve`) and drives it as a client (`cornus build`, `cornus deploy`, `cornus compose`, ...). You can install a prebuilt CLI, run the published container image, or build from source.

## Prebuilt CLI binary

Prebuilt static binaries for linux, darwin, and windows are attached to each [GitHub Release](https://github.com/moriyoshi/cornus/releases), with a `SHA256SUMS` manifest and keyless cosign signatures.

Each published binary includes the embedded web application used by [`cornus web`](/cli/web); no Node.js installation is required to run the UI.

Download the binary for your platform and put it on `PATH`:

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus
chmod +x cornus && sudo mv cornus /usr/local/bin/cornus
cornus version
```

For arm64, swap `amd64` for `arm64`.

## Container image

Pre-built multi-arch (amd64/arm64) images are published to GHCR by the release workflow:

* `ghcr.io/moriyoshi/cornus:<version>` on `v*` tags (also tagged `latest` and `<major>.<minor>`)

Third-party license attribution is bundled inside the image. The image is what the shipped Kubernetes manifests and Helm chart deploy; it also runs directly as a local Docker container.

### Run as a local Docker container

Run the server privileged for the in-process build engine, with the Docker socket mounted so the `dockerhost` deploy backend can run containers on this host:

```sh
docker run -d --name cornus --privileged -p 5000:5000 \
  -v cornus-data:/var/lib/cornus \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/moriyoshi/cornus:latest          # server on http://localhost:5000
```

Or with Compose:

```yaml
services:
  cornus:
    image: ghcr.io/moriyoshi/cornus:latest
    container_name: cornus
    privileged: true
    ports:
      - "5000:5000"
    volumes:
      - cornus-data:/var/lib/cornus
      - /var/run/docker.sock:/var/run/docker.sock
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "cornus", "version"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  cornus-data:
```

`privileged: true` is required by the in-process build engine (runc + overlayfs + user namespaces); for the rootless alternative and the full privilege model, see [Privilege posture](/reference/deploy-backends). Back `/var/lib/cornus` with a durable volume — see [Data directory and persistence](/reference/storage-backends).

## Run on Kubernetes

Deploy Cornus in-cluster as a StatefulSet so the registry CAS and build cache survive restarts.

```sh
# Recommended: Helm from the OCI registry (image tag tracks the chart version):
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus

# Or the raw manifest / a checked-out chart:
kubectl apply -f deploy/k8s/cornus.yaml
helm install cornus deploy/helm/cornus
```

- The manifest bundles a `StatefulSet` + PVC (data on `/var/lib/cornus`), a `Service`, a `ServiceAccount`, and `Role`/`RoleBinding` RBAC; both it and the chart set `CORNUS_DEPLOY_BACKEND=kubernetes` (Helm value `deployBackend`) so the server deploys into its own namespace. Liveness/readiness probe `/healthz` and `/readyz`.
- Chart values worth knowing: `storage` (`CORNUS_STORAGE`; empty keeps the CAS on the per-pod PVC), `replicas` (a multi-replica hub requires an `s3://` `storage` URL), and `auth.jwt.*` which wires the matching JWT-verification env. The full set is in the [Helm chart values](/reference/helm-values) reference.

::: tip
For the full serve → build → deploy walkthrough on a fresh single-node cluster, see the [quick start](/introduction/quick-start).
:::

## Building from source

Building requires Go 1.26. For a fully static, container-ready binary:

```sh
CGO_ENABLED=0 go build -tags "netgo osusergo" -o cornus ./cmd/cornus
```

To also enable the Google Cloud Storage (`gs://`) and Azure Blob (`azblob://`) registry storage backends, add the `cloudblob` build tag (the default build returns a clear "not supported in this build" error for those schemes):

```sh
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
```

::: warning
The in-process build engine is Linux-only and pulls in a large BuildKit dependency tree. A build compiles everywhere `go build` runs, but executing a build needs root or a rootless user-namespace stack. The registry and deploy subsystems need no special privileges. See the [architecture overview](/architecture/) for the privilege posture.
:::

## Next steps

* [Quick start](/introduction/quick-start) — serve, build, and deploy a Compose project.
* [What is Cornus?](/introduction/what-is-cornus) — the three subsystems and how they fit together.
