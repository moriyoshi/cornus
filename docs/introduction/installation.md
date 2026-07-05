# Installation

Cornus is a single Go binary. The same binary serves the server (`cornus serve`) and drives it as a client (`cornus build`, `cornus deploy`, `cornus compose`, ...). You can install a prebuilt CLI, run the published container image, or build from source.

::: warning Pre-1.0
Cornus is under active development and does not yet promise a stable CLI or
API. Pin release artifacts and review release notes before upgrading.
:::

## Prebuilt CLI binary

Prebuilt binaries are attached to each [GitHub Release](https://github.com/moriyoshi/cornus/releases), with a `SHA256SUMS` manifest and keyless cosign bundle (`SHA256SUMS.bundle`):

| Platform | Asset |
| --- | --- |
| linux | `cornus-linux-amd64`, `cornus-linux-arm64` |
| macOS | `cornus-darwin-amd64`, `cornus-darwin-arm64` |
| Windows | `cornus-windows-amd64.exe` |

Every binary is self-contained and batteries-included:

- The embedded web application used by [`cornus web`](/cli/web) — no Node.js needed to run the UI.
- The embedded OpenTelemetry Collector and the [built-in observability store](/guides/observability), so `cornus serve` records your workloads' logs, traces and metrics with no extra flags and nothing to install. Run `cornus version --features` to see what a binary carries.

The linux binaries are **fully static**, so they run on any distribution — Alpine and distroless included. Shipping the observability store makes the release downloads substantially larger than a bare CLI: currently about 86–107 MB depending on platform. `--no-obs` disables recording at runtime but does not shrink the downloaded binary.

Download the binary and verification files, verify the signed checksum manifest,
then put the binary on `PATH`:

```sh
curl -fsSL https://github.com/moriyoshi/cornus/releases/latest/download/cornus-linux-amd64 -o cornus-linux-amd64
curl -fsSLO https://github.com/moriyoshi/cornus/releases/latest/download/SHA256SUMS
curl -fsSLO https://github.com/moriyoshi/cornus/releases/latest/download/SHA256SUMS.bundle
cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity-regexp '^https://github\.com/moriyoshi/cornus/\.github/workflows/release\.yml@refs/tags/v[0-9].*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
grep ' cornus-linux-amd64$' SHA256SUMS | sha256sum -c -
mv cornus-linux-amd64 cornus
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
