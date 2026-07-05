# Shipping and Installing Cornus

## Summary

How Cornus itself is delivered and stood up — as opposed to what Cornus does to user
workloads. The release artifacts (a GHCR multi-arch image, prebuilt static CLI binaries with a
`SHA256SUMS` manifest, and the Helm chart as an OCI artifact — all keyless-cosign-signed and
`main.version`-stamped, the image carrying its own license attribution), two install surfaces
(Helm chart and raw manifest, which must agree on image refs and the deploy-backend env), and
one canonical local install flow (the k3s-first, Docker-free README Quick start that bootstraps
Cornus with Cornus). Changes to any one of these usually ripple into the others.

## Included Documents

| Document | Focus |
|----------|-------|
| [release-and-packaging.md](./release-and-packaging.md) | GHCR release workflow (image + binaries + SHA256SUMS + OCI chart, keyless cosign, version stamping), Helm/manifest image conventions, Apache-2.0 + in-image third-party attribution, Go 1.26 baseline |
| [local-k8s-quickstart.md](./local-k8s-quickstart.md) | The k3s Quick start flow, bootstrap self-build, `CORNUS_DEPLOY_BACKEND` fix, privileged-container verification workarounds |

## Stable Knowledge

- **Release artifacts**: `.github/workflows/release.yml`, triggered on `v*` tags +
  `workflow_dispatch` (the earlier default-branch/`edge` and Dockerfile-PR triggers were
  dropped), publishes: the multi-arch image `ghcr.io/<owner>/cornus` (amd64+arm64; semver tags
  yield `<version>` / `<major>.<minor>` / `latest`); prebuilt fully static CLI binaries
  (linux/darwin/windows x amd64/arm64, pure-Go cross-compile, no QEMU) as one GitHub Release;
  and the Helm chart. Auth is the built-in `GITHUB_TOKEN`.
- **Embedded web UI parity**: both the multi-arch image and downloadable static binaries build
  `web/` before Go compilation so `pkg/webui` embeds the real SPA rather than the node-less 503
  placeholder. Docker builds the architecture-independent assets once in a Node 22 stage on
  `$BUILDPLATFORM`; the release workflow builds `webui-dist` once and downloads it into every
  binary cross-compile matrix leg. The E2E image mirrors the same staging.
- **Checksums + keyless cosign signing** (GitHub OIDC, no long-lived keys): a `SHA256SUMS`
  release asset covers all binaries (`sha256sum -c` compatible; asset basenames like
  `cornus-linux-amd64` are a byte-for-byte contract with the README's
  `releases/latest/download/` curl), signed via `cosign sign-blob` (`SHA256SUMS.sig` /
  `SHA256SUMS.pem` assets); the image is signed by its manifest-list DIGEST (never by tag);
  the pushed chart digest is signed the same way. `id-token: write` is scoped to exactly the
  signing jobs; `sigstore/cosign-installer` is SHA-pinned.
- **Version stamping**: the Dockerfile declares `ARG VERSION=dev` feeding
  `-ldflags "... -X main.version=${VERSION}"`; the workflow passes the stripped tag, so both
  the release image's and the release binaries' `cornus version` report the tag while local
  builds stay `dev`.
- **Helm chart as an OCI artifact**: a tag-gated `chart` job runs `helm package` with
  `--version` AND `--app-version` set from the stripped tag (so the chart's default image tag
  equals the released image with no per-release Chart.yaml commits) and `helm push` to
  `oci://ghcr.io/<owner>/charts`; install via
  `helm install oci://ghcr.io/<owner>/charts/cornus --version <semver>`. The checked-in
  `Chart.yaml` version tracks chart-only changes: 0.1.0 initial; 0.2.0 multi-replica hub values
  (`replicas>1` wires `CORNUS_HUB_STORE=kube`, headless Service, per-pod
  `CORNUS_HUB_FORWARD_URL`, anti-affinity, TLS SANs); 0.2.1 unconditional
  `CORNUS_K8S_NAMESPACE` fieldRef; 0.2.2 `caretakerTlsSecret`; 0.3.0 `gc.interval`/`gc.lease`.
- **Image-ref convention across surfaces**: Helm `image.repository` defaults to
  `ghcr.io/moriyoshi/cornus` with `image.tag` empty → `.Chart.AppVersion` fallback;
  `deploy/k8s/cornus.yaml` pins an exact release tag; the README Quick start overrides the
  manifest image via `sed 's#image: ghcr.io/.*#...#'`. These four places (workflow, chart,
  manifest, README) must stay in sync.
- **An in-cluster server needs `CORNUS_DEPLOY_BACKEND=kubernetes`** (raw manifest env; Helm
  `deployBackend` value). Without it the server defaults to the dockerhost backend and every
  deploy fails — the shipped RBAC is for the kubernetes backend.
- **The local `cornus deploy` CLI hardcodes dockerhost** (`cmd/cornus/commands.go`
  `DeployCmd.Run`) and ignores `CORNUS_DEPLOY_BACKEND`; cluster deploys go through
  `--server` (a foreground deploy-attach session — Ctrl-C tears the workload down).
- **Licensing**: Apache-2.0 (holder: Moriyoshi Koizumi); `NOTICE` must be carried forward by
  redistributors (§4(d)). Third-party attribution ships inside the image at
  `/usr/share/doc/cornus/` (go-licenses build-stage step: license texts, MPL-2.0 sources,
  `THIRD_PARTY_LICENSES.csv`); `make third-party-licenses` reproduces it locally. Dep survey:
  204 modules, no strong copyleft.
- **Go 1.26 baseline**: `go 1.26.0` in go.mod, `golang:1.26-bookworm` in the Dockerfile; CI
  follows via `go-version-file: go.mod`, so toolchain bumps need no CI edit. A fresh
  (non-cached) local image build has been verified under the 1.26 base, including the
  go-licenses stage output landing in the final image; all binary-matrix combinations
  cross-compile under Go 1.26.
- **The Quick start is k3s-first and Docker-free**: a temporary root bootstrap server
  (`CORNUS_DATA=/tmp/cornus-bootstrap`) self-builds the Cornus image via
  `--builder ws://localhost:5000/.cornus/v1/build/attach`; k3s pulls it (with
  `/etc/rancher/k3s/registries.yaml` marking localhost:5000 plain-HTTP — containerd does not
  trust localhost the way Docker does); then localhost:5000 is handed over to
  `kubectl port-forward svc/cornus`. The single k3s node IS the host, so no image side-loading
  is ever needed; kind does not get this property (nodes are containers → `kind load` both
  images). Verified end to end 2026-07-05; reference outputs: `/healthz` →
  `{"status":"ok"}`, demo log `cornus demo`.
- `compose.yaml` deliberately keeps `build: .` + `cornus:dev` — the local dev path is not the
  release path.

## Operational Guidance

- **Changing image naming/tagging**: touch release.yml, the Helm values + StatefulSet
  template, `deploy/k8s/cornus.yaml`, and the README sed pattern together; verify with
  `helm lint` + `helm template` (default, tag-override, and cert-manager configs — mirroring
  the CI helm job).
- **Changing the Quick start**: re-verify inside a privileged debian container playing the
  host (the `sg docker` pattern). Container-only k3s workarounds: systemctl stub +
  `INSTALL_K3S_SKIP_ENABLE/START` then `k3s server` directly; tmpfs over `/var/lib/rancher`
  and `/var/lib/kubelet` (containerd refuses nested overlayfs); evacuate the root cgroup
  kind/k3d-style. Real hosts need none of these.
- **First release is gated on repo creation**: the first REAL release run (image + binaries +
  SHA256SUMS/cosign + chart publish) has not happened yet — everything is validated locally /
  by workflow parsing. Adjust the hardcoded `ghcr.io/moriyoshi` defaults if the repo lands
  under an org, tag `v0.1.0`, make the GHCR package public (tracked in TODO.md).
- **Chart release**: always override BOTH `--version` and `--app-version`; keep the asset
  names `cornus-linux-amd64`/`cornus-linux-arm64` byte-for-byte (README download contract).

## Files

- `.github/workflows/release.yml` — release pipeline: image + binaries + SHA256SUMS/cosign +
  OCI chart publish and shared `webui-dist` artifact (image name derived from
  `github.repository_owner`).
- `Dockerfile` — `golang:1.26-bookworm` build stage, `ARG VERSION=dev` `main.version` stamp,
  Node 22 web UI stage, go-licenses stage, final-stage doc copy.
- `deploy/helm/cornus/Chart.yaml` — checked-in chart version (release overrides
  version/appVersion at package time).
- `deploy/helm/`, `deploy/k8s/cornus.yaml` — install surfaces (image refs, `deployBackend` /
  `CORNUS_DEPLOY_BACKEND`).
- `Makefile` — `third-party-licenses` target; `LICENSE`, `NOTICE`.
- `README.md` — Quick start (k3s flow + kind/k0s/compose variants footnote).
- `cmd/cornus/commands.go` — `DeployCmd.Run` (hardcoded dockerhost for local deploys).

## Tests

CI helm job (lint + template) guards the chart; the raw manifest is YAML-parse-checked;
release.yml parses as valid YAML but cannot run until the GHCR repo and first `v*` tag exist.
The Quick start, the full image build under the 1.26 base, and the in-image attribution
bundle are hand-verified only (2026-07-05) — no automated E2E covers them.
The web UI stage and artifact dependency graph are build-validated; `e2e/scenarios/web.star`
proves an embedded SPA in the containerized docker/kube runner.

## Pitfalls

- go-licenses aborts (`Package <stdlib pkg> does not have module info`) when the PATH `go` is
  older than the go.mod directive — put the auto-downloaded toolchain's `bin` first on PATH.
  Inside the matching `golang:` image the versions agree (verified under the 1.26 base).
- Forgetting `--app-version` on `helm package` ships a chart whose default image tag points at
  the checked-in `Chart.yaml` appVersion instead of the released image.
- Semver tags must exist for the pinned manifest/chart refs to resolve; the `edge`/PR metadata
  entries are vestigial now that only `v*` tags and manual dispatch trigger the workflow.
- Do not suggest `CORNUS_DEPLOY_BACKEND=kubernetes cornus deploy` — the CLI ignores it; use
  `--server`.
- The bootstrap server must be stopped before `kubectl port-forward` can bind localhost:5000;
  the handover is what keeps every registry ref in the walkthrough stable.
- The Cornus build engine tolerates an overlayfs-backed data dir (BuildKit falls back), but
  k3s's containerd refuses nested overlay — the two behave differently on the same filesystem.
