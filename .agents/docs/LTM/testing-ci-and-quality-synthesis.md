# Testing, CI, and Quality Assurance

## Summary

How cornus verifies itself. One Starlark E2E harness (`pkg/e2e`, `cmd/cornus-e2e`) runs `.star`
scenarios against pluggable targets (host Docker / kind Kubernetes / local privileged /
containerd), packaged into an all-in-one Docker-in-Docker runner image. GitHub Actions wraps that
harness in three workflows: a fast per-PR gate (`ci.yml`) that runs the standard build/vet/test
plus a lightweight scenario `--check`, a heavy privileged full-suite run (`e2e.yml`) scoped to
`main`, and a signed release pipeline (`release.yml`). The 2026-07-09 whole-codebase adversarial
audit is the third leg: a reusable slice-and-two-skeptic review method that both hardened the code
and extended this same harness with negative scenarios. The through-line: the E2E harness is the
one place cross-daemon behavior is proven, `go test ./...` stays hermetic, and CI is what runs both.

## Included Documents

| Document | Focus |
|----------|-------|
| [e2e-harness-and-coverage.md](./e2e-harness-and-coverage.md) | The Starlark harness: builtins, targets, `--check`, preflight, the dind runner, the full scenario inventory, the bugs the baselines caught, and per-environment run recipes. The canonical how-to-use surface is `.agents/docs/TESTING.md`. |
| [ci-github-actions.md](./ci-github-actions.md) | The three `.github/workflows/` files (`ci.yml`/`e2e.yml`/`release.yml`), their triggers and matrices, and cross-cutting hardening: per-ref concurrency guards, SHA-pinned actions, Dependabot. |
| [codebase-audit-2026-07.md](./codebase-audit-2026-07.md) | The whole-codebase adversarial audit as a reusable reference: the 40-slice fan-out + two-skeptic (correctness/reachability) verification method, the 73 confirmed findings, the fix approach, and the E2E coverage it added. (The finding-by-finding report `.agents/docs/AUDIT_2026-07.md` was retired 2026-07-21 and consolidated into `.agents/docs/TODO.md`; per-finding detail is recoverable via git history.) |

## Stable Knowledge

### The layered verification model

- **`go test ./...` is hermetic and always runnable.** No external daemons, no root, no network.
  The registry, deploy backends, and server APIs are covered by in-process servers / fakes. Any
  test needing root / a live daemon is gated (e.g. `os.Geteuid()==0`) and skips otherwise
  (`pkg/build/builder/engine_linux_test.go` is the template). The build engine is
  `//go:build linux` and pulls a large BuildKit tree — `go build ./...` compiles it, but EXECUTING
  a build needs privilege.
- **Cross-daemon behavior lives in the Starlark E2E harness**, opt-in and NOT part of
  `go test ./...`. It stays lean by `exec`ing the real `cornus`/`cornus compose` binaries (BuildKit
  is not linked into the e2e binary) and talking to the server via the client package.
- **`make e2e-check`** parses AND RESOLVES every scenario against the harness's predeclared builtins
  (no Docker/kind, no execution) — the cheap gate that catches structural errors and undefined-name
  typos on every PR. `--check` uses `starlark.SourceProgramOptions` (a bare `syntax.Parse` missed
  resolve errors like top-level `for`).
- **The dind runner image** executes the FULL suite — privileged local + remote builds, compose,
  deploy, lifecycle, registry, and kind-in-dind — from one `docker run --privileged`. This is the
  mechanism CI uses and the way to reproduce a CI failure locally.

### Harness invariants an author must respect

- **Adding a builtin: register it in BOTH `predeclared()` and `predeclaredNames()`** in
  `pkg/e2e/harness.go` (`TestPredeclaredNamesInSync` enforces equality). A builtin in
  `predeclared()` but missing from `predeclaredNames()` makes EVERY scenario using it fail to
  resolve under `--check`. Also add new scenarios to the Makefile `SCENARIOS` list.
- **Preflight is all-or-nothing fail-fast** over the union of target + scenario needs
  (`targetNeeds` + `scenarioNeeds`, the latter a token scan for `build(` / `build_upload(` /
  `ssh_agent(` / `devcontainer_cli(`). Gate a ROOT / environment requirement with a scenario
  SELF-SKIP (`id -u`, `"9p" in read_file("/proc/filesystems").split()`), NOT a preflight
  capability, or it aborts the whole suite on developer hosts. `build-lazy-9p.star` and
  `devcontainer-vscode.star` are deliberately NOT in `SCENARIOS` (their Cap9P/CapDevcontainerCLI
  preflights are all-or-nothing); they live in `EXTRA_CHECK_SCENARIOS` for `--check` only.
- **Progress markers are scenario-parsed contracts.** `CORNUS-9P served N bytes` /
  `CORNUS-9P-BACKING` are printed to `progressW`, not diagnostics — a slog sweep that converted one
  to `slog.Debug` silently broke `build-lazy-9p.star`. Logging sweeps must leave `progressW` prints
  alone.
- **Per-role data-dir isolation** (`Harness.dataDir(role)`): a `serve()` and a local `build()`
  sharing one data dir deadlock on BuildKit's boltdb lock. The product now also fails fast via
  `builder.New`'s non-blocking `<data-dir>/engine.lock` flock.

### The three GitHub Actions workflows

- **`ci.yml`** — push to `main` + every PR. `gate` job (`ubuntu-24.04`): gofmt-check, `go build
  ./...`, two rot-guard builds (`-tags cloudblob ./pkg/storage/...` and `cd e2e/scenarios/ftp && go
  build ./...` — a nested module), `go vet ./...`, `go test ./...`, `make e2e-check`. Separate
  `helm` job: `helm lint` + `helm template` (default TLS-off AND the cert-manager path).
- **`e2e.yml`** — full Starlark suite on push to `main` + `workflow_dispatch` only (NOT PRs; it is
  heavy and privileged — PRs get `e2e-check` instead). Matrix `target: [docker, kube, containerd]`,
  `fail-fast: false`, builds the runner image with a `type=gha` layer cache and runs `docker run
  --rm --privileged -e E2E_TARGETS=<target> cornus-e2e:ci` (the `make e2e-container` path). The kube
  leg passes `E2E_MULTUS=1`; docker/containerd legs keep it `0`.
- **`release.yml`** — `v*` tags + `workflow_dispatch`. Jobs: `image` (buildx multi-arch GHCR push,
  cosign-signed by digest, `main.version` stamped via `VERSION` build-arg), `chart` (OCI Helm push,
  cosign-signed), `binaries` (per-OS/arch static CLI with the SAME `CGO_ENABLED=0 -tags "netgo
  osusergo"` flags as the Dockerfile + `-X main.version`), `release` (one GitHub Release +
  SHA256SUMS + keyless-cosign bundle). Artifact/signing detail is owned by
  [[shipping-and-install-synthesis]] — this stack does not duplicate it.
- **Hardening, all three:** concurrency `group: ${{ github.workflow }}-${{ github.ref }}`
  (`cancel-in-progress: true` for ci/e2e, `false` for release — never kill a publish); every
  `uses:` pinned to a full 40-char SHA with a `# vX.Y.Z` comment; `.github/dependabot.yml`
  (github-actions, weekly, one grouped PR) bumps the SHA and the comment together.

### The reusable audit method (`codebase-audit-2026-07`)

1. Slice every non-test Go file in `pkg/` + `cmd/` into ~40 disjoint review slices (split big
   packages for balance).
2. One adversarial reviewer per slice at high effort, hunting concrete bugs (correctness,
   concurrency, resource leaks, error handling, security, API misuse, nil-deref). Every finding
   states a required failure scenario (concrete inputs/state -> wrong behavior).
3. Two independent skeptic verifiers per finding — a correctness lens and a reachability lens —
   each defaulting to "not a bug" and instructed to REFUTE. A finding survives only if BOTH agree
   it is real AND reachable. This gate turned 85 raw findings into 73 confirmed (14 high / 27
   medium / 32 low; 12 rejected).
4. Verifiers set severity, overriding the reviewer's rating.
5. One fix agent per owning package; high+medium first, then a low sweep with the bar "safe minimal
   fix or document/skip — never regress for a low-severity nit." 72 of 73 fixed.

Scale reference: 40 slices, 210 agents, ~6.3M tokens.

## Operational Guidance

- **Every Go change** runs the local gate before being called done: `gofmt -l <changed>` (empty),
  `go build ./...`, `go vet ./...`, `go test ./...` (or a focused package). This IS `ci.yml`'s
  `gate` job — passing it locally is passing the PR gate.
- **Changing anything under `pkg/e2e/`, `cmd/cornus-e2e/`, or `e2e/scenarios/`:** read
  `.agents/docs/TESTING.md` first; keep `predeclared()`/`predeclaredNames()` in sync; add scenarios
  to `SCENARIOS`.
- **Reproducing a CI E2E failure:** treat the legs as independent (`fail-fast: false`). Reproduce
  inside the containerized runner locally. For a kube subset, run `make e2e-image` and invoke
  `docker run --rm --privileged -e E2E_TARGETS=kube -e E2E_SCENARIOS="..." cornus-e2e:latest`
  directly; the `e2e-container` Make recipe does not forward `E2E_SCENARIOS`. For
  devcontainer flows run `devcontainer up --log-level trace` (it prints every docker invocation —
  far more useful than its minified stack traces). A representative run had BOTH legs red on
  unrelated real bugs (a kube `retry.RetryOnConflict` fix and docker-proxy `docker run` protocol
  gaps), not a flake.
- **Diagnose kube waits before changing timeouts:** `kubeWaitDiag` adds best-effort
  `kubectl describe pod`, caretaker logs (current and `--previous`), and app logs to a wait timeout.
  It distinguishes absent workloads, scheduling/image failures, probe failures, and sidecar crashes;
  it is a no-op on non-kube targets.
- **Running an audit-style pass:** use the slice + two-skeptic recipe above. When the code lives in
  uncommitted working-tree changes, git-worktree isolation is impossible — fix agents edit the real
  tree and self-check only their own package; the authoritative module-wide `go build/vet/test
  ./...` + `make e2e-check` gate runs afterward.
- **Prove new E2E scenarios live**, not just via `--check`. The audit's scenarios were validated by
  `--check` + reading the fixes only (no kind/docker available that session) — confirm in the dind
  runner before trusting them green.

## Files

- `pkg/e2e/harness.go` (builtins, per-role data dirs, `predeclared`/`predeclaredNames`),
  `preflight.go` (+ `_test.go`), `target.go`, `value.go`, `ftp_test.go`; `cmd/cornus-e2e/`;
  `e2e/scenarios/*.star` (+ fixture dirs, incl. the nested `e2e/scenarios/ftp/` module);
  `e2e/container/{Dockerfile,entrypoint.sh,appimage.Dockerfile}`; `Makefile` (`SCENARIOS`,
  `EXTRA_CHECK_SCENARIOS`, `e2e-*` targets).
- `.github/workflows/{ci,e2e,release}.yml`, `.github/dependabot.yml`; `Dockerfile` (release build
  stage the `binaries` job mirrors); `cmd/cornus/version.go` (`var version = "dev"`, the `-X
  main.version` stamp target).
- `.agents/docs/TESTING.md` — the how-to-use harness reference (builtins, targets, preflight, dind
  runner). The finding-by-finding audit report `.agents/docs/AUDIT_2026-07.md` was retired
  2026-07-21 (consolidated into `.agents/docs/TODO.md`; detail recoverable via git history).
- `pkg/build/builder/lock_linux.go` — the `engine.lock` fail-fast the data-dir-lock baseline
  produced.

## Tests

- Hermetic: `go test ./pkg/e2e/` includes `TestScenariosParse` (globs all scenarios),
  `TestPredeclaredNamesInSync`, the preflight tests, and the FTP client tests against an in-process
  fake. `TestHarnessRegistryScenario` runs a real round-trip when `CORNUS_BIN` is set.
- Suite run recipes (from `e2e-harness-and-coverage.md`): `make e2e-docker` / `e2e-kube`
  (`KEEP=1`) / `e2e-containerd` / `e2e-one TARGET=… SCENARIO=…` / `e2e-check`; the full CI shape is
  `make e2e-container E2E_TARGETS="docker kube"` (add `E2E_MULTUS=1` for the real-NAD path).
  To narrow the containerized kube run, build with `make e2e-image` and pass `E2E_TARGETS` plus
  `E2E_SCENARIOS` directly to `docker run --rm --privileged cornus-e2e:latest`.
- Negative surface (post-audit + the unhappy-path pass): `registry-errors`, `registry-auth`,
  `cli-errors`, `deploy-errors`, `build-fail`, plus the audit's `deploy-redeploy-network`,
  `exec` `/bin/false`, `deploy-network` port-dedup rows. The three harness enablers
  (`build(expect_fail,capture)` returns `{tag,log}`, `deploy(expect_fail)` returns the rejection
  string, `serve(env=…)`) add NO builtin, so `predeclaredNames`/`--check` are unaffected.

## Pitfalls

- **`make e2e-check` is resolve-only** — it never executes a scenario. Running the suite for real
  repeatedly paid for itself (locked-data-dir deadlock, dind published-port readiness race, async
  kube delete, `mktemp -d` 0700 nginx-403, privileged-deploy gap). `--check` green is necessary,
  not sufficient.
- **Concurrency group names MUST include `${{ github.workflow }}`** — groups are repo-global; a
  ref-only group makes CI and E2E on the same ref cancel each other.
- **A SHA-pinned action can still change behavior** — `sigstore/cosign-installer` is SHA-pinned but
  the cosign VERSION it installs is not; a cosign v2 -> v3 default flip broke `sign-blob` under an
  unchanged pin. Do not assume "pinned action" means "frozen behavior" for actions that download a
  separately-versioned binary. And SHA pins do NOT auto-update — Dependabot keeps them current; do
  not hand-pin to a moving major's current SHA and call it "latest" (verify against
  `releases/latest`).
- **The `cornus-linux-<arch>` release asset name is load-bearing** for the README install command —
  keep the `binaries` job `-o` output and `upload-artifact` name in lockstep with the documented
  download URL.
- **`command` vs `entrypoint` in scenarios:** `command` supplies ARGS to the image ENTRYPOINT (k8s
  `.args` / Docker `CMD`); `entrypoint` REPLACES it (k8s `.command`). Setting `command` on an
  ENTRYPOINT-bearing image (e.g. `cornus:e2e` ENTRYPOINT `["cornus"]`) doubles/mangles argv and
  crash-loops — the smoking gun behind six kube-only scenario failures that were a wrong mental
  model, not a backend bug.
- **Host backends (dockerhost/containerd) have no synchronous health wait** — a crash-on-start
  deploy "succeeds"; poll `Status` for `running==0`. Only kubernetes maps `Healthcheck` to probes.
- **`BUILD FAILED:` only appears on `POST /.cornus/v1/build`** (`build_upload`); the `build` builtin's
  CLI / build-attach paths fail with a non-zero exit and differently-formatted output. Assert
  failure TEXT via `build_upload`, only THAT it failed via `build(expect_fail=True)`.
- **Audit severities were verifier-corrected** (in the retired `AUDIT_2026-07.md`), not the reviewers' originals —
  do not re-derive severity from a reviewer's wording. Under working-tree (not worktree) isolation,
  fix agents self-check only their own package; cross-cutting regressions are caught only by the
  final module-wide gate.
- **Source docs may reference stale paths.** `e2e-harness-and-coverage.md`'s Files section lists
  `/home/moriyoshi/src/chimpose/...` from a pre-rename tree; current code is under this repo's
  `pkg/e2e`, `cmd/cornus-e2e`, `e2e/scenarios`.

## Recent Scenario And Reliability Work

The harness now covers SOCKS5 aliasing and isolation, Compose fidelity, client-side egress, caretaker Docker exposure, and AI credential proxy delivery. New helpers include SOCKS5-aware `http_get`, held-compose conduit and environment arguments, negative-routing support, and the recording `egress_proxy` builtin.

Distinguish resolve-checked, docker-live, and kube-live validation in reports. `make e2e-check` verifies scenario structure only; live relay, mount, and Kubernetes paths still need their intended target. Recent `exec_tty` and compose-up failures were harness/lifecycle races, so their regression coverage exercises cancellation and pod-recreation behavior rather than assuming a backend TTY defect.

Kubernetes wait failures now emit pod states, events, and current/previous sidecar logs. This made the
relay-egress failure diagnosable as a workload removed by detached Compose lifecycle rather than a
caretaker crash. The proxy and transparent egress scenarios are live-verified through the local
privileged dind/kind runner.
