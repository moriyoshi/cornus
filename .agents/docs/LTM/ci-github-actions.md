# GitHub Actions CI/CD Workflows

## Summary

Cornus's continuous integration and delivery run on three GitHub Actions workflows under
`.github/workflows/`: `ci.yml` (the fast per-PR gate: build/vet/test + E2E syntax check + helm
lint), `e2e.yml` (the heavy, privileged full Starlark E2E suite via the all-in-one containerized
runner over a docker/kube/containerd matrix, scoped to `main` + manual dispatch), and
`release.yml` (multi-arch GHCR image + prebuilt static CLI binaries + SHA256SUMS/keyless-cosign
signing + OCI Helm chart on `v*` tags). All three are hardened uniformly: per-ref concurrency
guards, every action pinned to a full commit SHA, and Dependabot keeping the pins current.

## Key Facts

- **`ci.yml`** runs on push to `main` + every `pull_request`. `gate` job (`ubuntu-24.04`):
  gofmt-check, `go build ./...`, `go build -tags cloudblob ./pkg/storage/...`,
  `cd e2e/scenarios/ftp && go build ./...`, `go vet ./...`, `go test ./...`, `make e2e-check`.
  Separate `helm` job: `helm lint` + `helm template` (default TLS-off and the cert-manager path).
- **`e2e.yml`** runs the FULL Starlark suite on push to `main` + `workflow_dispatch` only (NOT on
  PRs — it is heavy and privileged; PRs get the fast `e2e-check` gate instead). Matrix over
  `target: [docker, kube, containerd]`, `fail-fast: false`, `timeout-minutes: 60`. It builds the
  all-in-one runner image (`e2e/container/Dockerfile`) with a `type=gha` layer cache and executes
  it via `docker run --rm --privileged -e E2E_TARGETS=<target> cornus-e2e:ci` — the same path as
  `make e2e-container`. The kube leg additionally passes `E2E_MULTUS=1` and `E2E_KNATIVE=1`
  (`-e E2E_MULTUS="${{ matrix.target == 'kube' && '1' || '0' }}"`, same for `E2E_KNATIVE` — docker
  and containerd legs keep both explicitly `0`; Multus and Knative are kube-only).
- **`release.yml`** runs on `v*` tags + `workflow_dispatch`. Jobs: `image` (buildx multi-arch
  GHCR push, cosign-signed by digest, `main.version` stamped via a `VERSION` build-arg), `chart`
  (tag-gated `helm package`/`helm push` to `oci://ghcr.io/<owner>/charts`, cosign-signed),
  `binaries` (per-OS/arch static CLI, tag-gated), `release` (single GitHub Release attaching the
  binaries + SHA256SUMS + its keyless cosign signature, tag-gated). Artifact/signing detail lives
  in `release-and-packaging.md`.
- **Concurrency** on all three, group `${{ github.workflow }}-${{ github.ref }}`. `ci.yml` and
  `e2e.yml` use `cancel-in-progress: true`; `release.yml` uses `cancel-in-progress: false` (never
  kill a release mid-publish).
- **Every action is SHA-pinned** (full 40-char commit) with a trailing `# vX.Y.Z` comment.
- **`.github/dependabot.yml`** (`package-ecosystem: github-actions`, weekly, one grouped PR)
  bumps both the SHA and the version comment.
- `setup-go` uses `go-version-file: go.mod` so CI follows the `go 1.26.0` directive with no
  pinning; module + build cache enabled.

## Details

### `ci.yml` — the standard gate (per-PR)

Triggers: `push` to `main`, and `pull_request` (any branch). Two independent jobs on
`ubuntu-24.04`.

`gate` runs the full local gate every Go change must pass, in order: gofmt-check (fails if
`gofmt -l .` is non-empty), `go build ./...`, then two rot-guard builds, `go vet ./...`,
`go test ./...`, and `make e2e-check`.

- **`go build -tags cloudblob ./pkg/storage/...`** compiles the `gs://` / `azblob://` gocloud
  storage drivers, which live behind the `cloudblob` build tag (they pull the Google/Azure SDKs
  and stay out of the default lean binary). Compiling that path in CI keeps it from rotting
  without bloating the shipped binary.
- **`cd e2e/scenarios/ftp && go build ./...`** compiles the FTP server fixture the `ftp*.star`
  scenarios build. It is a nested Go module (its own `go.mod`, kept out of the main module), so
  the main `go build ./...` never touches it — it needs its own explicit compile step.
- **`make e2e-check`** is the lightweight E2E gate: it parses AND resolves every Starlark scenario
  against the harness's predeclared builtins (no Docker/kind, no execution), catching structural
  errors and undefined-name typos on every PR.

`helm` runs `helm lint deploy/helm/cornus` and `helm template` twice — the default (TLS off) and
the opt-in cert-manager path (`tls.enabled`/`tls.clientCA`/`tls.certManager.*`) — so a chart
template regression fails CI without needing a cluster.

### `e2e.yml` — full E2E via the containerized runner

Triggers: `push` to `main` + `workflow_dispatch`. Deliberately NOT on PRs: the suite is heavy and
must run privileged, so PRs stay fast on the `e2e-check` syntax gate and full execution is scoped
to merges and manual runs.

Each matrix leg builds the all-in-one runner image through `docker/build-push-action` (`context:
.`, `file: e2e/container/Dockerfile`, `tags: cornus-e2e:ci`, `load: true`) with a cross-run
GitHub Actions layer cache (`cache-from`/`cache-to: type=gha,...,scope=e2e-image`) so the golang
deps and staged tools (kind/kubectl/crane/multus) are not refetched each run; `load: true` drops
the built image into the local docker so the run step can start it. The run step is
`docker run --rm --privileged -e E2E_TARGETS="<target>" -e E2E_MULTUS=<0|1> -e E2E_KNATIVE=<0|1> cornus-e2e:ci`.
The image's `entrypoint.sh` starts an in-container dockerd, runs preflight, and for the kube target
pre-creates (and tears down) the in-container kind cluster.

The matrix has three legs: `docker`, `kube`, and `containerd` (the containerd leg runs the
containerd scenario subset against a standalone in-container containerd — the entrypoint's
`CONTAINERD_SCENARIOS` default). `fail-fast: false` reports the legs independently, so a kube
flake never masks a real docker regression (or vice versa).

The runner image also installs `nodejs`/`npm` plus a pinned `@devcontainers/cli`
(`ARG DEVCONTAINERS_CLI_VERSION=0.80.0` in `e2e/container/Dockerfile`, with a version smoke
check), so CI executes `devcontainer-vscode.star` — the scenario driving the official VS Code
devcontainer engine against the docker proxy — which developer hosts without a global npm
install skip.

Why the containerized runner is the right CI mechanism: TESTING.md documents it as the way to run
the full suite "from a generic CI or self-hosted host with only a privileged docker run".
GitHub-hosted `ubuntu-24.04` runners allow `--privileged` and nested Docker-in-Docker / kind, so
the runner maps directly onto Actions with no bespoke tool install on the runner itself. Runner
internals live in `e2e-harness-and-coverage.md`.

The kube path is designed to be green on kind:
- The kube leg sets `E2E_MULTUS=1`, so the entrypoint installs Multus into the kind cluster and
  the Multus scenarios (`deploy-multus.star` and friends) execute rather than self-skip. On the
  docker and containerd legs it is explicitly `0`.
- The kube leg also sets `E2E_KNATIVE=1`, so the entrypoint runs `install-knative.sh` (Knative
  Serving + Kourier from upstream manifests — the runner has internet, and the kind nodes already
  pull external images) and `deploy-knative.star` round-trips a real ksvc rather than self-skipping.
  Knative is kube-only; docker/containerd keep it `0`. The runner image bakes
  `e2e/container/install-knative.sh` (a `COPY` in `e2e/container/Dockerfile`).
- CNI-specific scenarios still self-skip when their CRD is absent: `deploy-cilium.star` (no
  Cilium CRD), and the extra-gated Multus rows (`E2E_MULTUS_IPVLAN`/`E2E_MULTUS_MACVLAN`/
  `E2E_MULTUS_DETACHED`) stay off unless their env gates are set.
- `deploy-netpolicy-enforce.star` does NOT self-skip; it relies on kindnet v1.31+ nftables
  NetworkPolicy enforcement, which the runner image's pinned `KIND_VERSION=v0.24.0` /
  `KUBECTL_VERSION=v1.31.0` provide — the same cluster `make e2e-kube` uses.

#### Diagnosing CI E2E failures (proven pattern)

When e2e.yml legs fail, treat them as independent (that is what `fail-fast: false` buys). A
representative failure (run 28779183420) had BOTH legs red on unrelated real bugs:

- **kube leg**: `lifecycle.star` restart returned 500 "the object has been modified" — the
  kubernetes backend's `Stop`/`Start`/`Restart` did a bare Get→Update while the deployment
  controller wrote to the Deployment concurrently, so the Update hit a 409 Conflict. Fixed with a
  shared `updateDeployment` helper wrapping Get→mutate→Update in
  `retry.RetryOnConflict(retry.DefaultRetry, ...)` (regression: `TestLifecycleRetriesOnConflict`
  with a fake-clientset reactor that 409s every first Update).
- **docker leg**: the first live `devcontainer-vscode.star` run exposed the docker proxy's
  foreground `docker run` protocol gaps (attach must park until start; `wait?condition=next-exit`
  must flush the 200 header immediately and send the JSON body at exit — dockerd's protocol;
  `/events` must actually emit start/die/stop/destroy events).

The diagnosis mechanism: reproduce inside the containerized runner locally
(`E2E_SCENARIOS=<scenario> make e2e-container`-style) and, for devcontainer flows, run
`devcontainer up --log-level trace` — the CLI prints every docker invocation it issues, which is
far more useful than its minified stack traces.

### `release.yml` — GHCR image + chart + prebuilt CLI binaries + signing

Triggers: `push` tags `v*` + `workflow_dispatch` (a manual dispatch rebuilds the image but only a
tag push cuts a GitHub Release, chart, or binaries). Base `permissions: contents: read,
packages: write`; `id-token: write` (for keyless cosign via GitHub OIDC) is scoped to exactly the
jobs that sign.

- **`image`**: buildx multi-arch (linux/amd64 + linux/arm64 via QEMU) push to
  `ghcr.io/<owner>/cornus`. Tags via `docker/metadata-action`: semver `{{version}}` and
  `{{major}}.{{minor}}` plus `latest` on semver tags, `edge` on the default branch, and
  `type=ref,event=pr`. Auth is the built-in `GITHUB_TOKEN` (no managed secrets). `type=gha` layer
  cache. Push is gated `github.event_name != 'pull_request'`. Passes
  `VERSION=${{ steps.meta.outputs.version }}` as a build-arg (the Dockerfile's `ARG VERSION=dev`
  stamps `main.version`), and cosign-signs the pushed image by the manifest-list digest taken
  from the build-push step's `digest` output.
- **`chart`** (tag-gated): `helm package` with `--version`/`--app-version` from the stripped tag,
  `helm push` to `oci://ghcr.io/<owner>/charts`, digest cosign-signed. The job runs a
  `docker/login-action` GHCR login (the same step as the `image` job) BEFORE the push, because
  `cosign sign` authenticates from the docker config (`~/.docker/config.json`) and `helm registry
  login` writes only helm's own registry config, which cosign never reads (see pitfalls).
- **`binaries`** (`if: startsWith(github.ref, 'refs/tags/v')`): matrix over
  linux/darwin/windows x amd64/arm64, cross-compiles the fully static CLI with the SAME flags the
  Dockerfile build stage uses (`CGO_ENABLED=0`, `-tags "netgo osusergo"`) plus
  `-ldflags "-s -w -X main.version=${version}"` where `version="${GITHUB_REF_NAME#v}"` — so the
  published binary's `cornus version` reports the tag instead of `dev`. Each is uploaded as an
  artifact named `cornus-<goos>-<goarch>[.exe]`.
- **`release`** (`needs: binaries`, tag-gated, `permissions: contents: write` +
  `id-token: write`): downloads all artifacts with `merge-multiple: true` (flattens them into
  `dist/`), generates `SHA256SUMS` (`sha256sum -c` compatible), signs it with
  `cosign sign-blob --yes --bundle dist/SHA256SUMS.bundle dist/SHA256SUMS` (attaching the single
  Sigstore `SHA256SUMS.bundle`, NOT the old `.sig` + `.pem` pair — see finding 7), and publishes
  ONE GitHub Release via `softprops/action-gh-release` with `fail_on_unmatched_files: true`,
  `generate_release_notes: true`. Verify with `cosign verify-blob --bundle SHA256SUMS.bundle
  SHA256SUMS` (plus identity/issuer flags).

The release image, chart-publishing mechanics, cosign verification commands, Helm/manifest image
conventions, and in-image third-party licensing are covered in [[release-and-packaging]]
(synthesis parent `shipping-and-install-synthesis.md`) — this doc does not duplicate that detail;
the bundle-vs-`.sig`/`.pem` asset contract lives there.

### Concurrency: never let workflows cancel each other

All three guard on `group: ${{ github.workflow }}-${{ github.ref }}`. The group name MUST include
`github.workflow`: concurrency groups are repo-global, not per-workflow, so a bare
`${{ github.ref }}` group would put CI and E2E for the same ref in one group and make them cancel
each other. Cancellation policy differs by workflow intent:

- `ci.yml`, `e2e.yml`: `cancel-in-progress: true` — a newer push supersedes the in-flight run so
  rapid successive pushes don't pile up redundant runs.
- `release.yml`: `cancel-in-progress: false` — a release must never be killed mid-publish (a
  half-pushed GHCR image or partial GitHub Release is worse than waiting), so a second run queues
  rather than supersedes. Tags are unique, so this only ever engages on a manual re-dispatch.

### Action pinning (supply-chain)

Every `uses:` is pinned to a full 40-char commit SHA with a trailing `# vX.Y.Z` comment. A
floating tag (`@v4`) lets a compromised or retagged action run with the workflow's token; a SHA
pin is immutable. Current pins across the three workflows:

| Action | Pin |
|--------|-----|
| actions/checkout | `9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0` |
| actions/setup-go | `924ae3a1cded613372ab5595356fb5720e22ba16 # v6.5.0` |
| azure/setup-helm | `9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310 # v5.0.1` |
| docker/setup-qemu-action | `96fe6ef7f33517b61c61be40b68a1882f3264fb8 # v4.2.0` |
| docker/setup-buildx-action | `bb05f3f5519dd87d3ba754cc423b652a5edd6d2c # v4.2.0` |
| docker/login-action | `af1e73f918a031802d376d3c8bbc3fe56130a9b0 # v4.4.0` |
| docker/metadata-action | `dc802804100637a589fabce1cb79ff13a1411302 # v6.2.0` |
| docker/build-push-action | `53b7df96c91f9c12dcc8a07bcb9ccacbed38856a # v7.3.0` |
| actions/upload-artifact | `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1` |
| actions/download-artifact | `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8.0.1` |
| softprops/action-gh-release | `718ea10b132b3b2eba29c1007bb80653f286566b # v3.0.1` |
| sigstore/cosign-installer | `6f9f17788090df1f26f669e9d70d6ae9567deba6 # v4.1.2` |

`.github/dependabot.yml` keeps them current: `package-ecosystem: github-actions`, `directory: /`,
weekly, with all bumps grouped into one PR (`groups.github-actions.patterns: ["*"]`) and
`commit-message.prefix: ci`. Dependabot understands the `uses: owner/repo@<sha> # vX.Y.Z` format
and bumps both the SHA and the version comment together.

### Durable findings / rationale

1. **A floating MAJOR tag pins the newest patch WITHIN that major, not the newest release.**
   `gh api repos/OWNER/REPO/commits/vN --jq .sha` faithfully follows whatever tag you hand it, so
   resolving `@v4` yields the latest `v4.x` even when upstream is already on `v7`. To catch that
   an action is a full major behind, query `releases/latest` per repo first, then resolve THAT
   tag to a SHA. Every action here was a full major behind the tag the workflows originally used.
2. **The latest action majors require the Node 24 runner** — fine on `ubuntu-24.04` (what every
   job uses).
3. **upload-artifact v7 and download-artifact v8 interoperate.** Both are on the v4+ artifact
   backend and `merge-multiple: true` still exists in v8, so the per-arch upload -> single
   download fan-in still flattens to `dist/` with the exact `cornus-linux-<arch>` basenames.
   That asset filename is a CONTRACT with the README, whose quick start curls
   `releases/latest/download/cornus-linux-<arch>` — renaming the build output silently breaks the
   documented install command (an inline comment in `release.yml` flags this).
4. **The CLI cross-compiles without QEMU.** It is `CGO_ENABLED=0` pure Go, so
   `GOARCH=arm64 go build` cross-compiles natively on the amd64 runner. QEMU (`setup-qemu-action`)
   is needed only by the `image` job, for arm64 container LAYERS.
5. **The version stamp point is `main.version`.** `cmd/cornus/version.go` declares
   `var version = "dev"`; the `binaries` job overrides it via `-ldflags -X main.version=...`, and
   the image build stamps it too via the Dockerfile's `ARG VERSION=dev` + the workflow's
   `VERSION` build-arg — so both released binaries and the released image report the tag, while
   local builds (no build-arg) stay `dev`.
6. **The e2e.yml matrix's two build steps share one gha cache scope (`e2e-image`).** Concurrent
   cache writes dedupe by blob digest (a duplicate key 409s and buildx continues), so whichever
   leg builds second is a fast cache-from load rather than a full rebuild.
7. **`sigstore/cosign-installer` is SHA-pinned, but the cosign VERSION it installs is not.**
   Pinning the action's commit does not pin the cosign binary: the pinned installer (v4.1.2) now
   installs cosign v3.0.6, whose `sign-blob` defaults to the Sigstore new-bundle format. Under
   that default, `sign-blob` IGNORES `--output-signature`/`--output-certificate` (it warns
   `--output-signature is deprecated when using --new-bundle-format and will be ignored`) and
   instead writes to the `--bundle` path — so the old `--output-signature dist/SHA256SUMS.sig
   --output-certificate dist/SHA256SUMS.pem` invocation failed with `create bundle file: open :
   no such file or directory` (empty default `--bundle`). This is a silent cosign v2 -> v3 default
   change that shipped through a pinned action. The same VERSION drift can hit the image/chart
   signing steps; keep an eye on cosign majors even though the installer SHA looks frozen.

## Files

- `.github/workflows/ci.yml` — per-PR gate (`gate` + `helm` jobs).
- `.github/workflows/e2e.yml` — full Starlark E2E via the containerized runner
  (`docker`/`kube`/`containerd` matrix, `E2E_MULTUS=1` and `E2E_KNATIVE=1` on the kube leg), on
  `main` + `workflow_dispatch`.
- `.github/workflows/release.yml` — GHCR multi-arch image + OCI Helm chart + static CLI binaries
  + SHA256SUMS/cosign + GitHub Release on `v*` tags.
- `.github/dependabot.yml` — github-actions ecosystem, weekly grouped bumps of the SHA pins.
- `e2e/container/Dockerfile`, `e2e/container/entrypoint.sh` — the all-in-one runner e2e.yml
  executes (details in `e2e-harness-and-coverage.md`).
- `Dockerfile` — the release image build stage whose static-build flags the `binaries` job
  mirrors.
- `cmd/cornus/version.go` — `var version = "dev"`, the `-ldflags -X main.version` stamp target.

## Test Coverage

- `ci.yml`'s `gate` job IS the standard build/vet/test gate plus `make e2e-check` (scenario
  parse+resolve), run on every push to `main` and every PR.
- `ci.yml`'s `helm` job guards the chart via lint + template (default and cert-manager configs).
- `e2e.yml` executes the full Starlark suite (docker + kube + containerd, Multus enabled on kube)
  on `main` + manual dispatch — the runtime coverage `e2e-check` cannot provide.
- `release.yml`'s `image` job rebuilds the Dockerfile on PRs touching it (via the `type=ref,
  event=pr` tag path, push-gated off), guarding against Dockerfile rot; the `binaries`/`release`
  jobs run only on `v*` tags.
- The workflow-only changes were verified by YAML-parsing all four files and grepping that no
  floating `@vN` refs remain. No Go source was touched, so the build/vet/test gate does not apply;
  the workflows were not committed (repo rule: no discretionary commits). The e2e.yml first real
  run happens on the next push to `main`.

## Pitfalls

- **Concurrency group names must include `${{ github.workflow }}.`** Groups are repo-global; a
  ref-only group would make different workflows on the same ref cancel each other.
- **`azure/setup-helm@v5` with no `version:` input** resolves the latest Helm release via the
  GitHub API (it reads `github.token` by default, so rate-limiting is unlikely). If `helm lint`
  ever fails on the version fetch, pin an explicit `version:`.
- **SHA pins do not auto-update.** Dependabot is what keeps them current; its first run should be
  a near no-op since everything is at the latest release. Do not hand-pin to a moving major tag's
  current SHA and assume it is "latest" — verify against `releases/latest` (see finding 1).
- **The e2e.yml first run is uncached and slow** — the runner image build downloads the golang
  toolchain, kind/kubectl, and a crane pull of the Multus image — until the `type=gha` layer cache
  warms on subsequent runs.
- **The `cornus-linux-<arch>` release asset name is load-bearing** for the README install
  command; keep the `binaries` job's `-o` output and the `upload-artifact` name in lockstep with
  the documented download URL.
- **A SHA-pinned action can still change tool behavior underneath you.** `sigstore/cosign-installer`
  is pinned, but the cosign version it fetches is not, so a cosign v2 -> v3 default flip broke
  `sign-blob` despite an unchanged pin (see finding 7). Do not assume "pinned action" means
  "frozen behavior" for actions that download a separately-versioned binary.
- **`cosign sign` reads only the docker config, not helm's registry config.** After `helm push`,
  signing the pushed chart digest fails with `UNAUTHORIZED: unauthenticated` if the job ran only
  `helm registry login` — that writes helm's own config, which cosign never reads. The `chart`
  job must run a `docker/login-action` GHCR login (`~/.docker/config.json`) so `cosign sign` can
  upload the signature layer.
