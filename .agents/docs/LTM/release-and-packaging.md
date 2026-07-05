# Release, Packaging, and Licensing

## Summary

How Cornus ships: a GHCR multi-arch image built by `.github/workflows/release.yml` (cosign-signed
by digest, `main.version`-stamped), prebuilt static CLI binaries for linux/darwin/windows with a
`SHA256SUMS` manifest and keyless cosign signature, the Helm chart published as an OCI artifact to
`oci://ghcr.io/<owner>/charts` (also cosign-signed), Helm/raw-manifest image defaults that follow
chart conventions, Apache-2.0 licensing with third-party attribution bundled inside the release
image, and the Go toolchain baseline (go 1.26). Attribution and license texts ship in the image
filesystem.

## Key Facts

- **Release workflow** (`.github/workflows/release.yml`): buildx multi-arch (linux/amd64 +
  linux/arm64 via QEMU) push to `ghcr.io/<owner>/cornus`. Tagging via `docker/metadata-action`:
  semver `v*` tags publish `<version>`, `<major>.<minor>`, and `latest`; default-branch pushes
  publish `edge`; PRs touching the Dockerfile build without pushing (Dockerfile rot gate).
  Auth is the built-in `GITHUB_TOKEN` (`packages: write`) — no secrets to manage. GHA layer
  cache (`type=gha`) speeds rebuilds.
- **Binary matrix**: linux/darwin/windows x amd64/arm64 (windows artifacts carry `.exe`). All
  targets cross-compile natively (pure-Go CLI, no QEMU) under Go 1.26; the linux-only build
  engine (`pkg/build/builder`, `//go:build linux`) is stubbed out on darwin/windows.
- **Checksums + signing**: a `SHA256SUMS` asset covers all binaries (`sha256sum -c` compatible);
  keyless cosign (GitHub OIDC) signs it via `cosign sign-blob --bundle` with a single
  `SHA256SUMS.bundle` uploaded as a release asset; the pushed image is signed by manifest-list
  digest via the build-push step's `digest` output. `id-token: write` is scoped to exactly the
  signing jobs; `sigstore/cosign-installer` is SHA-pinned.
- **Version stamping**: the Dockerfile has `ARG VERSION=dev` feeding
  `-ldflags "... -X main.version=${VERSION}"`; the workflow passes the stripped tag as a
  build-arg, so in-image `cornus version` reports the release (local builds stay `dev`).
- **Helm chart as OCI artifact**: a tag-gated `chart` job runs `helm package` with `--version` /
  `--app-version` set from the stripped tag (so the chart's default image tag = the released
  image, with no per-release Chart.yaml commits), `helm push` to `oci://ghcr.io/<owner>/charts`,
  and cosign-signs the pushed chart by digest.
- **Helm image convention**: `image.repository` defaults to `ghcr.io/moriyoshi/cornus`;
  `image.tag` is empty and falls back to `.Chart.AppVersion` in the StatefulSet template, so
  bare `helm install` pulls the release matching the chart. `deploy/k8s/cornus.yaml` pins
  `ghcr.io/moriyoshi/cornus:0.1.0`. The README Quick start's sed override matches
  `s#image: ghcr.io/.*#...#`.
- **License**: Apache-2.0 (holder: Moriyoshi Koizumi). `LICENSE` is the canonical text;
  `NOTICE` carries the Cornus copyright line — Apache-2.0 §4(d) obliges downstream
  redistributors to carry it forward.
- **Third-party attribution ships inside the image**: a Dockerfile build-stage step runs
  `go-licenses save` + `report`; the final stage carries
  `/usr/share/doc/cornus/third-party-licenses/` (license texts; full sources for reciprocal
  licenses like MPL-2.0; `THIRD_PARTY_LICENSES.csv` manifest) alongside `LICENSE`/`NOTICE` in
  `/usr/share/doc/cornus/`. `--ignore cornus` keeps our own module out;
  `GOOS`/`GOARCH`/`GOFLAGS` mirror the binary build so the module set matches what is linked.
- **`make third-party-licenses`** generates the same bundle locally into
  `bin/third-party-licenses/` (`GO_LICENSES_VERSION ?= v1.6.0`).
- **Dependency license survey** (204 modules): 127 Apache-2.0, 38 BSD-3, 28 MIT, 6 MPL-2.0
  (hashicorp), 4 BSD-2, 2 ISC — all compatible, no strong copyleft.
- **Go toolchain**: `go.mod` says `go 1.26.0` (bumped from 1.25.0 on 2026-07-05; no
  `toolchain` directive, no dependency changes — BuildKit, the heaviest dep, only requires
  `go 1.22.0`). Dockerfile build stage is `golang:1.26-bookworm`. CI needs no pinning —
  `setup-go` uses `go-version-file: go.mod`.
- `compose.yaml` deliberately keeps `build: .` + `cornus:dev` (the local dev path); the GHCR
  image is for k8s installs.

## Details

### Release tagging and repo-creation dependency

The workflow derives the image name from `github.repository_owner`, but the Helm/manifest/
README defaults hardcode `ghcr.io/moriyoshi/cornus`. The working tree had no git remote when
this was authored, so after the repo is created: adjust the hardcoded defaults if the repo
lands under an org, tag `v0.1.0` so the pinned manifest ref and chart `appVersion` resolve,
and make the GHCR package public (Settings → Packages) or pulls will need auth. Tracked in
TODO.md.

### go-licenses in the Dockerfile

The attribution step lives in the build stage so it runs under the same `golang:1.26-bookworm`
toolchain that compiles the binary — this is what avoids the version-mismatch pitfall below.
Verification at authoring time: `make third-party-licenses` produced the 204-module CSV +
license tree; `docker build --check` clean; a full local image build exercised the go-licenses
stage; a container run confirmed `/usr/share/doc/cornus/` holds LICENSE, NOTICE, and the
4.1 MB / 204-module bundle. The full image build has also been verified under the
`golang:1.26-bookworm` base: a fresh (non-cached) local `docker build` succeeded with the
go-licenses third-party output produced and copied into the final image.

### Verification state of the Helm/tagging changes

`helm lint` clean; `helm template` (default + tag override + TLS/cert-manager path, mirroring
CI) renders the expected image refs; release.yml parses as valid YAML. The release workflow
itself cannot run until the repo exists on GitHub.

### Prebuilt CLI binaries + clone-free install

`release.yml` does not ship only the image. Two tag-gated jobs cover binaries:

- **`binaries`** — matrix over linux/darwin/windows x amd64/arm64 (windows with `.exe` ext),
  cross-compiles the fully static CLI with the SAME flags the Dockerfile build stage uses
  (`CGO_ENABLED=0`, `-tags "netgo osusergo"`) plus
  `-ldflags "-s -w -X main.version=${GITHUB_REF_NAME#v}"` so the published binary's `cornus version`
  reports the tag instead of `dev`. No QEMU: the CLI is pure-Go so `GOARCH=arm64 go build`
  cross-compiles natively on the amd64 runner (QEMU is only for arm64 container LAYERS in the image
  job). The linux-only build engine (`pkg/build/builder`, `//go:build linux`) is stubbed out on
  darwin/windows so all targets compile; all combinations are verified to cross-compile under
  Go 1.26. Uploads each as artifact `cornus-<goos>-<goarch>[.exe]`.
- **`release`** — `needs: binaries`, downloads all artifacts (`merge-multiple: true` flattens into
  `dist/`), generates `SHA256SUMS` over every binary (basenames relative to `dist/`, so
  `sha256sum -c SHA256SUMS` works next to downloaded assets), cosign-signs it keyless
  (`cosign sign-blob --bundle dist/SHA256SUMS.bundle` — cosign v3 defaults sign-blob to the
  Sigstore bundle format and ignores the old `--output-signature`/`--output-certificate`),
  and publishes ONE GitHub Release via `softprops/action-gh-release`
  (binaries + SHA256SUMS + .bundle; `fail_on_unmatched_files: true`,
  `generate_release_notes: true`). Split from the matrix so arch jobs never race to create the
  same Release.

**Asset filename is a contract with the README.** The Quick start's
`curl .../releases/latest/download/cornus-linux-<arch>` resolves only if the attached asset name
matches byte-for-byte — keep it exactly `cornus-linux-amd64`/`cornus-linux-arm64`. The README Quick
start consumes prebuilt artifacts end to end: `curl` the release binary onto PATH, then
`kubectl apply -f .../deploy/k8s/cornus.yaml` (already points at `ghcr.io/moriyoshi/cornus`, node
pulls from GHCR).

**Triggers narrowed** to `push` on `v*` tags + `workflow_dispatch` (the default-branch / Dockerfile-PR
triggers were dropped); a manual dispatch still rebuilds the image but the tag-gated
binary/release/chart jobs only fire on a real `v*` tag. The `edge`/`type=ref,event=pr` entries in
`docker/metadata-action` are now largely vestigial given those events no longer trigger the
workflow. Full workflow mechanics (concurrency, SHA-pinned actions, Dependabot) live in
`ci-github-actions.md`; this doc stays focused on packaging/licensing.

### Keyless cosign signing

All release artifacts are signed keyless via GitHub OIDC (no long-lived keys):

- **Image**: signed by the manifest-list DIGEST taken from the build-push step's `digest` output
  (`cosign sign --yes "${image}@${DIGEST}"`), never by tag — cosign signs exactly what was pushed.
- **Binaries**: `cosign sign-blob --bundle SHA256SUMS.bundle` over `SHA256SUMS` (cosign v3's
  default Sigstore bundle carries signature + Fulcio cert + Rekor proof in one file); verify with
  `cosign verify-blob --bundle SHA256SUMS.bundle SHA256SUMS` (plus the standard identity/issuer
  flags). This single `SHA256SUMS.bundle` asset SUPERSEDES the old `.sig` + `.pem` asset pair
  produced by cosign v2's `--output-signature`/`--output-certificate` (those flags are ignored
  under cosign v3's default new-bundle format, so leaving them wired emitted no `.sig`/`.pem` and
  failed with `create bundle file: open : no such file or directory` on the empty default
  `--bundle` path). Some other/older docs may still reference the `.sig`/`.pem` pair; the
  `SHA256SUMS.bundle` contract here is canonical.
- **Chart**: the OCI chart digest captured from `helm push` output is cosign-signed the same way.
  The chart job MUST run `docker/login-action` against GHCR before the push, not only
  `helm registry login`: cosign authenticates from the docker config (`~/.docker/config.json`),
  which `helm registry login` does not write, so without the docker login the follow-up
  `cosign sign` of the pushed chart digest fails with `UNAUTHORIZED: unauthenticated`.
- `permissions: id-token: write` is granted ONLY to the jobs that sign (least privilege);
  `sigstore/cosign-installer` is pinned to a full commit SHA
  (`6f9f17788090df1f26f669e9d70d6ae9567deba6 # v4.1.2`). Note the action pin does NOT pin the
  cosign binary version it installs (currently cosign v3.0.6); a v2→v3 default change can still
  reach the signing steps despite the SHA-pinned action.

Full workflow-authoring conventions (concurrency, SHA-pinning, Dependabot, OIDC scoping) live
in [[ci-github-actions]]; this doc stays focused on the packaging/signing/verify contract.

### Image `main.version` stamping

The Dockerfile build stage declares `ARG VERSION=dev` and stamps
`-ldflags "-s -w -X main.version=${VERSION}"`; the release workflow passes
`VERSION=${{ steps.meta.outputs.version }}` as a build-arg. So the released image's
`cornus version` matches the tag while local `docker build` (no build-arg) stays `dev`. Both the
standalone release binaries AND the release image are therefore stamped.

### Helm chart as an OCI artifact

A tag-gated `chart` job in `release.yml` (same `startsWith(github.ref, 'refs/tags/v')` gate as
`binaries` — a `workflow_dispatch` must not overwrite a chart) publishes the chart to GHCR:

- `helm package deploy/helm/cornus --version <semver> --app-version <semver>` with the semver
  derived from the stripped tag (`${GITHUB_REF_NAME#v}`). Overriding BOTH means the chart's
  default image tag (the `.Chart.AppVersion` fallback) equals the released image, with NO
  per-release Chart.yaml commits — the checked-in `Chart.yaml` version only tracks chart-only
  changes.
- `helm push "dist/cornus-<version>.tgz" "oci://ghcr.io/<owner>/charts"`; the pushed digest is
  captured and cosign-signed keyless (`cosign sign --yes "ghcr.io/<owner>/charts/cornus@<digest>"`).
- Install path: `helm install oci://ghcr.io/<owner>/charts/cornus --version <semver>`.

### Chart version history (packaging-relevant)

The checked-in `deploy/helm/cornus/Chart.yaml` version increments on chart-only changes:

| Version | Change |
|---------|--------|
| 0.1.0 | initial chart |
| 0.2.0 | multi-replica hub values (`replicas>1` wires `CORNUS_HUB_STORE=kube`, headless Service, per-pod `CORNUS_HUB_FORWARD_URL`, anti-affinity, TLS SANs; requires shared s3 `storage`) |
| 0.2.1 | `CORNUS_K8S_NAMESPACE` fieldRef made unconditional |
| 0.2.2 | `caretakerTlsSecret` value (TLS Secret mount + scoped RBAC) |
| 0.3.0 | `gc.interval` / `gc.lease` values (lease-without-interval fails rendering) |

### Embedded web UI in release artifacts

The SPA must exist in `pkg/webui/dist` before the Go compiler evaluates `go:embed`. The image path
builds it in a Node 22 `webui` stage pinned to `$BUILDPLATFORM`, so multi-arch builds produce the
architecture-independent JS/CSS once without running npm under QEMU. The static-binary path uses a
dedicated workflow job to build and upload `webui-dist`; every OS/architecture matrix leg downloads
that artifact before `go build`. This prevents downloadable binaries from embedding only the
node-less 503 placeholder while the container image contains the real UI.

The E2E container mirrors the release Dockerfile staging. `.dockerignore` excludes `web/node_modules`
and `docs/node_modules`, keeping source-only contexts. See [web-ui.md](./web-ui.md) for the BFF,
frontend, and integrated test contract.

## Files

- `.github/workflows/release.yml` — multi-arch GHCR image + CLI binaries + SHA256SUMS/cosign +
  OCI chart publish pipeline.
- `Dockerfile` — `golang:1.26-bookworm` build stage; `ARG VERSION=dev` `main.version` stamp;
  go-licenses stage; final-stage doc copy.
- `deploy/helm/cornus/Chart.yaml` — checked-in chart version (release overrides
  version/appVersion at package time).
- `Makefile` — `third-party-licenses` target, `GO_LICENSES_VERSION`, toolchain-mismatch note.
- `LICENSE`, `NOTICE` — Apache-2.0 text and carry-forward notice.
- `deploy/helm/` — `image.repository`/`image.tag` (appVersion fallback), `deployBackend`.
- `deploy/k8s/cornus.yaml` — pinned GHCR image ref.
- `go.mod` — `go 1.26.0` directive.

## Test Coverage

CI's helm job (lint + template) guards the chart; the release workflow's PR trigger rebuilds
the Dockerfile without pushing, guarding against Dockerfile rot. No automated test covers the
in-image attribution bundle — it was verified by hand (container run, 2026-07-05).

## Pitfalls

- **go-licenses toolchain mismatch**: go-licenses aborts with
  `Package <stdlib pkg> does not have module info` when the `go` on PATH is older than the
  module's directive (GOROOT mismatch between the toolchain that compiled go-licenses and the
  auto-fetched one that loads packages). Fix: put the auto-downloaded newer toolchain's `bin`
  first on PATH. Inside the matching `golang:` image the versions agree, so image builds are
  unaffected. Documented in the Makefile.
- Semver tags must exist for the pinned manifest/chart refs to resolve; `edge` only tracks the
  default branch.
- The first REAL release run (image + binaries + SHA256SUMS/cosign + chart publish) has not
  happened yet — it is pending the GHCR repo creation and the first `v*` tag (tracked in
  TODO.md's GHCR follow-ups item). Everything is validated locally / by workflow parsing until
  then.
- The chart job must override BOTH `--version` and `--app-version`; forgetting `--app-version`
  ships a chart whose default image tag points at the checked-in `Chart.yaml` appVersion instead
  of the released image.
- **cosign reads docker config, not helm's**: signing a pushed OCI chart digest needs a
  `docker/login-action` GHCR login in the chart job; `helm registry login` alone leaves cosign
  unauthenticated (`UNAUTHORIZED: unauthenticated`).
- **A SHA-pinned `cosign-installer` does not pin the cosign binary version.** The v2→v3 default
  flip (sign-blob now defaults to the Sigstore bundle and silently ignores
  `--output-signature`/`--output-certificate`) reached the release run despite the action being
  SHA-pinned. Re-running a failed tag's Release uses the workflow at the tagged commit, so a fix
  committed after the tag requires moving/re-pushing the tag or cutting a new one to take effect.
