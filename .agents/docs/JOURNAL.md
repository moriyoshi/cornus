# cornus Development Journal

This file retains only unconsolidated entries and the canonical long-term-memory audit.

---

## LTM Consolidation Record

Audited against `.agents/docs/LTM/` and `.agents/docs/TODO.md` on 2026-07-18. Every substantive journal entry has durable coverage in the documents below; consolidated narrative entries and superseded record sections were removed.

### Journal sections -> LTM documents

| Journal section group | LTM document |
|-----------------------|--------------|
| Embedded OpenTelemetry Collector: design, deployment surfaces, Kubernetes Secret hardening, docs, E2E, and CI | workload-telemetry.md |
| Knative Serving descriptor | knative-serving.md |
| Per-project connection context overrides and trust model | project-context-overrides.md |
| Compose profile status fix, SSH profiles, and ingress-through-SOCKS5 work | july-2026-client-and-web.md, compose-cli.md, remote-cluster-connection-ergonomics.md |
| Remote 9P block cache: block protocol, coherence, demand fill, prefetch, DiskStore, and concurrent caller work | remote-9p-block-cache.md |
| Yamux QoS fork, A/B methodology, netem bounds, batched send path, and CI | yamux-qos-performance.md |
| Kubernetes E2E stats and emulated-ingress TLS constraints | e2e-kubernetes-target-caveats.md, kubernetes-ingress.md, e2e-harness-and-coverage.md |
| Dependency license audit, notices, and reusable scanner skill | dependency-license-compliance.md |

### Synthesis documents -> source LTM documents

| Synthesis document | Consolidates |
|--------------------|--------------|
| build-engine-synthesis.md | remote-build-9p-transport.md, build-cache.md, lazy-bind-mounts.md, build-worker facet of containerd-backend.md |
| caretaker-transport-and-hub-synthesis.md | client-local-mounts-deploy.md, client-side-egress.md, hub-network-overlay.md, caretaker facets of user-networks-and-caretaker.md |
| client-connectivity-synthesis.md | client-daemon-and-conduit.md, client-side-egress.md, port-forwarding.md, public-tunnels.md, remote-cluster-connection-ergonomics.md |
| deploy-backends-synthesis.md | deploy-backend-contract.md, containerd-backend.md, kubernetes-backend.md |
| docker-compat-clients-synthesis.md | compose-cli.md, dockerd-proxy.md, dev-containers.md |
| kubernetes-deploy-synthesis.md | kubernetes-backend.md, user-networks-and-caretaker.md, Kubernetes facets of client-local-mounts-deploy.md |
| shipping-and-install-synthesis.md | release-and-packaging.md, local-k8s-quickstart.md |
| testing-ci-and-quality-synthesis.md | ci-github-actions.md, e2e-harness-and-coverage.md, codebase-audit-2026-07.md |

### Intentionally standalone LTM documents

auth-and-security.md, control-plane-api-namespace.md, dependency-license-compliance.md,
e2e-kubernetes-target-caveats.md, kubernetes-ingress.md, maintenance-and-decisions.md,
observability-and-logging.md, project-context-overrides.md, registry-and-storage.md,
registry-local-image-flow.md, remote-9p-block-cache.md, remote-companion-and-agent-forwarding.md,
user-reference-docs-site.md, web-ui.md, workload-telemetry.md, and yamux-qos-performance.md.

Open follow-up work is tracked in `.agents/docs/TODO.md`. See `.agents/docs/LTM/INDEX.md` for the full index.

---

## CI green-up: yamux race timeouts, Knative RBAC on down, ingress 502 (2026-07-18)

Four CI failures on the `main` "Initial." commit, spanning three subsystems. Two were
deterministic (reproduced across consecutive runs), two were startup-timing flakes.

- **yamux `TestSession_PartialReadWindowUpdate` (off-by-one, `-race`, TCP/TLS).** The fork
  caps DATA frames at `defaultMaxDataFrame` (128 KiB, `priority.go`), so a 256 KiB flood
  ships as two frames. The test does one `Read(flood/2+1)` after the sender drains and
  asserts a window update of exactly that size — only true if the whole flood is buffered.
  On a real socket the 2nd frame lags the drain, the `Read` returns just the first 128 KiB,
  and the window update is off by one. Fix: pin `MaxDataFrame = MaxStreamWindowSize` in the
  test config so the flood is a single atomically-buffered frame (the assertion's premise).
- **yamux `TestSendData_VeryLarge` (120 s timeout, `-race`).** Moves 16 GiB; self-skips under
  `-short` ("may time out on the race detector") but CI ran `-race` without `-short`. Fix in
  `ci.yml`: run the yamux suite as `-race -short` (scheduler-concurrency coverage; slow
  throughput tests skipped) plus a companion non-race full run so the multi-GiB throughput /
  integrity tests still execute (finish in ~30 s without the detector).
- **Kube `incluster-portforward.star` (deterministic; 500/403 on `down`).** In a Knative-served
  cluster `Backend.Delete` always calls `deleteKnative`, and a `Delete` of a non-existent ksvc
  by an SA lacking `serving.knative.dev` RBAC returns 403 Forbidden (authz precedes existence),
  not NotFound — so `down` of a plain-Deployment workload fails. Production `rbac.yaml` /
  `k8s/cornus.yaml` have the same latent gap. Fix in the product, not RBAC: `deleteKnative`,
  `knativeStatus`, `knativeExists` now treat Forbidden like NotFound — an SA that can't touch
  ksvcs could never have created one, so "not mine / nothing here" is correct. Regression test
  in `knative_test.go` via a Forbidden reactor on the dynamic fake.
- **Docker `socks5-ingress.star` (transient 502).** The ingress-emulation reverse proxy answers
  502 while nginx starts behind a freshly published port; `http_get` returned any HTTP response
  verbatim and only retried dial errors, so the scenario's 30 s retry couldn't absorb it. Added
  an opt-in `retry_5xx` to `http_get` (bounded by `retry`; the last 5xx is still returned once
  the window passes, so a workload that never recovers fails honestly) and set it in the scenario.

The other kube red (`ftp-usernet` failing to load `docker.io` base-image metadata) did not recur
and is an upstream-registry/network flake, not a code defect.

**Files touched.** `third_party/yamux/session_test.go` (single-frame flood), `.github/workflows/ci.yml`
(split yamux job into `-race -short` + non-race full), `pkg/deploy/kubernetes/knative.go`
(Forbidden tolerance in the three speculative-ksvc probes), `pkg/deploy/kubernetes/knative_test.go`
(`TestDeleteToleratesKnativeForbidden`), `pkg/e2e/harness.go` (`http_get` `retry_5xx`),
`pkg/e2e/httpget_test.go` (new; two retry tests), `e2e/scenarios/socks5-ingress.star`.

**Verification.** `gofmt` clean; `go build ./...`, `go vet`, full `go test ./...` all pass. yamux both
passes pass (`-race -short` ~20 s; non-race full ~30 s incl. the 16 GiB transfer). `PartialRead`
survived 75 `-race` runs (was ~1-in-3 failing). No commit made (repo policy: commit only on request).

**Open decision.** The Knative-Forbidden tolerance is a *product* behavior change: cornus no longer
needs `serving.knative.dev` RBAC to `down` a plain Deployment in a Knative-serving cluster. The
alternative — keep `Delete` strict and instead grant Knative verbs in the e2e fixture
(`incluster-cornus.yaml`) *and* production (`rbac.yaml`, `k8s/cornus.yaml`) — was rejected as it
would force every deployer to grant permissions they may never use. If a future maintainer wants
cornus to manage ksvcs from in-cluster, that RBAC still needs adding regardless.

---

## Cache downloaded Go artifacts in CD: Dockerfile mounts + per-target matrix keys (2026-07-19)

Two independent Go-artifact caching gaps in the CD (release) path, fixed together.

- **Root `Dockerfile` build stage had no Go caches.** `go mod download`, `go build`, and the
  `go-licenses` `go run` all ran cold — modules refetched and every package recompiled from
  scratch on each build. Added BuildKit cache mounts to all three steps:
  `/go/pkg/mod` (`id=gomod`, default `sharing=shared`) and `/root/.cache/go-build`
  (`id=gobuild-${TARGETARCH}`). The module cache is architecture-independent so one shared
  mount serves every target; the compiler build cache is target-specific so it is keyed per
  `TARGETARCH`, letting the parallel amd64/arm64 legs of a multi-arch build run without
  contending on one mount. Left the module mount at default `sharing=shared` (not `locked`):
  Go's own module-cache lockfiles make concurrent access safe, and `locked` would needlessly
  serialize the two arch legs.
- **`release.yml` binaries matrix shared one poisoned build cache.** The 6-way GOOS/GOARCH
  cross-compile matrix used `setup-go`'s `cache: true`, whose key derives only from `go.sum`
  with no GOOS/GOARCH component — so all six legs raced on a single key and five of six
  restored a build cache compiled for the *wrong* target. Replaced with `cache: false` plus
  two explicit `actions/cache` steps: a shared `~/go/pkg/mod` (keyed on `go.sum`) and a
  per-target `~/.cache/go-build` (keyed `gobuild-<goos>-<goarch>-<go.sum>-<sha>` with
  `restore-keys` falling back to the newest cache for that same target, so only changed
  packages recompile).

**Key finding — BuildKit cache mounts are NOT cross-run in CI.** The `image` job's
`cache-to: type=gha` persists only image *layers*, not `type=cache` mount contents. So the
Dockerfile mounts deliver strong reuse *within* a single multi-arch build invocation (and for
local `docker build`), but give no run-to-run benefit in the CD `image` job — the existing
`go mod download` layer cache already covers the cross-run module case there. The cross-run
build-artifact reuse for the standalone release binaries comes entirely from the `release.yml`
change, not the Dockerfile.

**Runner path facts.** On the ubuntu-24.04 runner `setup-go` leaves Go's defaults:
`GOMODCACHE=~/go/pkg/mod`, `GOCACHE=~/.cache/go-build` — the two paths the manual caches target.
`cache: false` still installs Go at those default paths, so nothing else in the job changes.

**Files touched.** `Dockerfile` (cache mounts on the three Go steps in the `build` stage),
`.github/workflows/release.yml` (binaries job: `cache: false` + shared module cache + per-target
build cache). Reused the `actions/cache` SHA already pinned in `ci.yml` (v6.1.0) for consistency.

**Verification.** `docker buildx build --check .` — no warnings. `release.yml` validates as YAML.
No image build executed end-to-end (heavy multi-arch + QEMU) and no commit made (repo policy).

**Open follow-ups (offered, not done).** (a) Export the BuildKit cache mounts across runs
(experimental cache-mount export) if cross-run build-cache reuse in the `image` job is wanted;
(b) apply the same cache-mount treatment to `e2e/container/Dockerfile`.

---

## TODO sweep: e2e Dockerfile Go cache mounts + declined cross-run mount export (2026-07-19)

Swept the two follow-ups from the CD Go-artifact-caching entry above.

- **DONE — `e2e/container/Dockerfile` Go cache mounts.** Applied the root Dockerfile's pattern to the
  E2E runner image's `build` stage: `/go/pkg/mod` on a shared `id=gomod` mount (module cache is
  arch-independent, shared with the release image) and `/root/.cache/go-build` on
  `id=gobuild-e2e-${TARGETARCH}` (distinct from the release image's `gobuild-${TARGETARCH}` since the
  E2E binaries use different build tags). Declared `ARG TARGETARCH` for the per-arch key.
  `docker buildx build --check -f e2e/container/Dockerfile .` clean.
- **CLOSED (won't do) — cross-run cache-mount export for the CD `image` job.** Investigated and
  declined. Confirmed `cache-to: type=gha` exports only image *layers*, never `type=cache` mount
  contents, so persisting them across runs requires a "cache dance"
  (`reproducible-containers/buildkit-cache-dance`, latest v3.4.0). Rejected for this pipeline because:
  (1) it adds a third-party action to the job that does keyless cosign signing
  (`id-token: write`/`packages: write`); (2) dancing a multi-GB Go build cache x2 arches, on top of
  the existing `mode=max` layer cache, pressures the 10 GB per-repo GHA cache budget — LRU eviction
  would degrade every cache including CI's; (3) the payoff lands only on infrequent tag-triggered
  releases and is partly offset by the dance's own tar/upload wall-clock. Kept the image job on layer
  cache only (module downloads are already layer-cached). Recorded the better alternative for if
  arm64 release build time ever becomes the actual pain: a native `ubuntu-24.04-arm` runner (split
  matrix + manifest merge) removes QEMU emulation entirely, making cross-run build-cache reuse largely
  moot — a better attack than cache-dancing.

**Files touched.** `e2e/container/Dockerfile` (cache mounts + `ARG TARGETARCH`), `.agents/docs/TODO.md`
(both items closed with rationale).

**Verification.** `docker buildx build --check` clean on the e2e Dockerfile. No image build run
end-to-end; no commit made.

## dockerproxy: /stop and /rm could respond before the published port listener closed (2026-07-19)

**Context.** CI "Standard gate" was red on a flaky `TestContainerPortForward`
(`pkg/dockerproxy/proxy_test.go:418` "listener still accepting after stop", also seen at :429
"after rm"). Passed locally 5x but reproduced under `go test ./pkg/dockerproxy/ -run
TestContainerPortForward -race -count=50 -p 4`.

**Root cause — a real teardown race, not just a test-timing artifact.** On `/start` a self-exit
watcher goroutine blocks on `<-sess.Done()` and then calls `rec.setExited(sess)`
(`containers.go:161`). A `/stop` or `/rm` handler calls `sess.Stop()` — which does `cancel(); <-s.done`
(`attachsession.go:143`) — and then *also* calls `rec.setExited(sess)`. `sess.Done()` and the channel
`Stop()` waits on are the **same** `done` channel, so when it closes the handler and the watcher race
into `setExited`. Only the winner ran `cleanup()` (`portfwd.Group.Close`, which closes the published
TCP listener); the loser returned immediately. When the watcher won, the handler skipped cleanup and
wrote its `204` while the listener was still being torn down in the watcher goroutine — so a client
that reconnects the instant `stop`/`rm` returns can still reach the withdrawn port.

**Fix.** `setExited` now records the in-flight exit transition (`exitingSess`/`exitDone` on the
record, guarded by `mu`). The race winner runs cleanup then closes `exitDone`; a loser whose
`sess` matches the in-flight transition blocks on `exitDone` before returning. Every `setExited`
caller — `/stop`, `/rm`, `/wait`, the watcher, `Proxy.Close` — now observes port exposure as
withdrawn on return. "Exactly one returns true / publishes die" is preserved.

**Files touched.** `pkg/dockerproxy/state.go` (`setExited` + two record fields),
`pkg/dockerproxy/state_test.go` (new `TestSetExitedWaitsForCleanup`, a deterministic guard that
fails reliably — ~iter 197/200 — with the wait removed).

**Also diagnosed (not changed here).** The same CI run's second red job, `TestSendData_VeryLarge` in
the `third_party/yamux` "throughput/integrity (no race; full transfer sizes)" leg, is a load-induced
keepalive/write-timeout flake (`keepalive failed: i/o deadline reached`), not a logic error — passes
locally. And the Release "Build and push image" `go mod download` failure was already fixed at HEAD
(the Dockerfile now `COPY third_party/` — the yamux `replace` target — before `go mod download`).

**Verification.** `gofmt` clean; `go build ./...`, `go vet ./pkg/dockerproxy/` pass; full `go test
./...` green; `TestContainerPortForward` + `TestSetExitedWaitsForCleanup` survive `-race -count=100
-p 2`. Confirmed the new test catches the bug by temporarily reverting the `<-wait`. No commit made.

**Follow-up — yamux flake now skipped in CI (2026-07-19).** Rather than change the vendored fork's
timeouts, the `TestSendData_VeryLarge` runner flake is excluded from CI via the workflow only: the
"throughput/integrity" leg now runs `go test -skip '^TestSendData_VeryLarge$' ./...` (`-skip` is a
Go 1.20+ flag). The fork test file stays pristine, `TestSendData_Large` still exercises the bulk send
path in CI, and `TestSendData_VeryLarge` still runs locally under a plain `go test ./...` in
`third_party/yamux`. File touched: `.github/workflows/ci.yml` (step comment + `run` line). YAML
re-validated; `-skip` confirmed locally (VeryLarge excluded, Large runs).

---

## ARCHITECTURE.md — documented storage remoting / the wire transport + block protocol (2026-07-19)

**Ask.** ARCHITECTURE.md referenced "9P-on-WebSocket" throughout (build, lazy contexts, client-local
mounts) but never documented the shared substrate underneath it — the gap the user flagged. Filled it.

**What was missing.** The doc named the transport but never described `pkg/wire` itself, the mount-
serving modes, or the two pieces that make writable client-served mounts viable:
- the multiplexed transport (one WebSocket → forked yamux → 1-byte-tagged streams; the QoS classes,
  the 16 MiB window / batched-pipelined send path, the datagram framing);
- the three ways a mount is served (blind pipe / read-only caching proxy / writable block proxy) and
  how `LocalMount` properties select them;
- the **block protocol** (`pkg/wire/blockproto.go` et al.) — the bespoke block-indexed, hash-inline,
  reqID-muxed protocol on the `b` backing: framing, ops, HELLO negotiation, seq-gated coherence, and
  the `FeatSubBlockHash`/`FeatDeferHash`/`FeatSubBlockFill` feature bits + `CORNUS_BLOCK_*` knobs;
- the **server-side block cache** (`pkg/blockcache`) — the two `FileID` identity policies
  (content-version vs stable-bucket), the pluggable disk/mem `Store`, per-sub-block presence, and the
  RMW self-verify/drop.

**Change.** New top-level section "Storage remoting: the wire transport and block protocol" inserted
between Build engine and Deploy engine, plus a TOC entry. It formalizes the shared substrate and
cross-links (not duplicates) the existing "Remote builds over 9P", "Lazy build contexts", and
"Client-local bind mounts" prose. Terminology and measured figures aligned with LTM
`remote-9p-block-cache.md` and `yamux-qos-performance.md`. Docs-only change (no code); repo doc
punctuation rules honored (no full-width parens/colons). No commit made.

---

## User-facing docs — storage remoting / client-local mount caching (EN + ja + zh) (2026-07-19)

Follow-up to the ARCHITECTURE.md storage-remoting entry above, propagated to the VitePress site.
The usage surface (`--local-mount ...:cache/:async`, `CORNUS_BLOCK_*`, `asyncCache` spec field) was
already documented and translated; the gap was the *architecture-level* explanation of the mount
remoting and its caching modes.

- `docs/architecture/deploy-engine.md` — new "## Client-local bind mounts" section (caller-is-9P-server
  + source rewrite, the caretaker rendezvous, session-lifetime scoping) with a "### Read caching and
  writable mounts" subsection: the three serving modes (blind 9P pipe / `,cache` read-through /
  `,async` writable block protocol) as a 3-row table, cross-linked to server-env-vars and the build
  engine's 9P section. Added a Remote-workflows related-pages link.
- `docs/topics/remote-workflows.md` — appended a paragraph + example to "Remote deploys with
  client-local bind mounts" covering the `,cache`/`,async` performance modes.
- Translated both changes into `docs/ja/**` and `docs/zh/**` at the parallel locations, matching each
  locale's terminology (glossary consulted; no new terms needed).

**Anchor gotcha for future translations.** Intra-site fragment links must target the *translated*
heading's VitePress slug, written in composed CJK form — e.g. `#クライアントローカルバインドマウント`,
`#経-9p-的远程构建`, `#リモート-9p-ファイルキャッシュと書き込み可能マウント` (ASCII lowercased, spaces →
hyphens, CJK preserved). The English anchor (`#client-local-bind-mounts`) does NOT exist on a
translated page. `npm run docs:build` fails on a wrong anchor, so it is the real check — the audit
script only flags the localized-link heuristic as a warning.

**Verification.** `audit_markdown_translation.py` passed for ja + zh (only the expected
localized-link/inline-code review warnings); `cd docs && npm run docs:build` completed clean (no
dead-link/anchor errors); `git diff --check` clean; English pages verified free of full-width
punctuation. No commit made.

---

## Session summary — documenting the storage-remoting subsystem (2026-07-19)

Consolidated record for the two granular entries above (ARCHITECTURE.md gap-fill, then the
user-facing propagation). Task: the docs "mention virtually nothing about the storage remoting
architecture, its wire protocol" — fill that, then update the user-facing docs to match.

### What shipped
- `ARCHITECTURE.md` — new top-level section "Storage remoting: the wire transport and block protocol"
  (+ TOC entry), between Build engine and Deploy engine.
- `docs/architecture/deploy-engine.md` — new "Client-local bind mounts" + "Read caching and writable
  mounts" sections; `docs/topics/remote-workflows.md` — caching/`async` paragraph + example.
- Both user-facing changes translated into `docs/ja/**` and `docs/zh/**`.
- Docs-only; no code touched. `npm run docs:build` clean; ja/zh audit clean; no commit.

### Findings worth keeping

**1. "Storage remoting" in Cornus is filesystem remoting, not CAS/blob remoting.** What crosses the
wire is a POSIX subtree (9P), never `pkg/storage` objects — the content store is reached only as an
OCI client over `/v2/*`. Anyone asked to "document storage remoting" should look at `pkg/wire` +
`pkg/blockcache` + the `buildwire`/`deploywire` consumers, NOT `pkg/storage`.

**2. The gap was the shared substrate, not the features.** ARCHITECTURE.md already covered "remote
builds over 9P", lazy contexts, and client-local mounts at a feature level, and repeatedly said
"9P-on-WebSocket" — but never documented `pkg/wire` itself: the yamux tag table + QoS classes, the
three mount-serving modes (blind pipe / read-only caching proxy / writable block proxy), the bespoke
**block protocol** (`blockproto.go` et al.), or the **block cache** (`pkg/blockcache`). Those four
were the entire gap. The block-protocol/cache internals were already in LTM
`remote-9p-block-cache.md` and `yamux-qos-performance.md`; this work promoted them into the canonical
architecture docs (terminology/figures aligned to those LTM notes).

**3. Mount-mode selection is decided by `LocalMount` properties (verified in code).**
`deploywire/backing.go` + `cmd/cornus/commands.go` `parseLocalMount`: `--local-mount SRC:DST[:opts]`,
opts is the 3rd colon field, comma-separated among `ro`,`cache`,`async`. `cache` ⇒ immutable+ro ⇒
`ServeCachingProxy` (content-version FileID). `async` ⇒ writable, single-writer ⇒ `ServeBlockProxy`
(stable-bucket FileID, per-block hash+seq coherence), excludes ro/cache. Default ⇒ blind pipe. Both
cached modes fall back to the blind pipe unless the server file cache is enabled
(`--file-cache`/`CORNUS_FILE_CACHE` + `CORNUS_FILE_CACHE_DIR`) — a non-obvious operational footgun.
`CORNUS_BLOCK_COHERENCE`/`CORNUS_BLOCK_READAHEAD` must be set on BOTH server and deploy caller (HELLO
intersects the feature set) or the flag is silently dropped.

**4. Doc-placement decision.** The wire/block-protocol section sits between Build and Deploy engines
deliberately: build introduces read-only 9P + the caching proxy (lazy contexts), the section
formalizes the shared substrate, deploy then consumes all three modes incl. the writable block path.
The user-facing counterpart lives on the deploy-engine page (its main consumer) rather than a new
page, matching the existing site structure.

**5. Translation CJK-anchor gotcha (durable).** Intra-site fragment links on a translated page must
target the *translated* heading's VitePress slug in composed CJK form — e.g.
`#クライアントローカルバインドマウント`, `#经-9p-的远程构建`,
`#リモート-9p-ファイルキャッシュと書き込み可能マウント` (ASCII lowercased, spaces→hyphens, CJK kept).
The English anchor does not exist on a translated page. `npm run docs:build` fails on a wrong anchor,
so it is the authoritative check; the translation audit script only warns on the localized-link
heuristic.

---

## ARCHITECTURE.md — added block-protocol wire + on-disk structure detail (2026-07-19)

Extended the "Storage remoting" section (same session as the entries above) with concrete
structures, on request for "more details and diagrams describing the protocol and in-wire/in-disk
structures":

- **Block protocol on the wire** — a byte-layout of the frame header (`u32 len · op · flags · u16
  rsvd · u64 reqID`), the HELLO payload, and the request/response payloads of the data ops (READ,
  READRANGE, WRITE + its three coherence tails, FSYNC), plus a mermaid sequence diagram of a
  demand-fill read miss and a classic write-through showing where `seq` gates and `hash` verifies.
- **Block cache on disk** — the `DiskStore` layout (`<aa>/<key>.data` sparse file + `<key>.idx` JSON
  sidecar), the `FileID.Key()` = sha256(mount,path,size,mtime,writable) derivation, the `diskIndex`
  JSON shape, a chunk/sub-block bitmap diagram (256 sub-blocks per 1 MiB chunk), and the
  fsync-before-presence-bit + atomic-sidecar crash-safety contract.

Sources: `pkg/wire/blockproto.go`, `blockmsg.go`, `blockserver.go`, `blockproxy.go`,
`pkg/blockcache/diskstore.go`. Used bold lead-ins (not `h4`, matching the doc's convention) and
plain-ASCII byte-layout code blocks (portable on GitHub + no full-width punctuation). Verified:
mermaid message labels avoid stray colons (only the one separator per line), fenced blocks balanced,
no full-width parens/colons. Docs-only; no commit. User-facing site intentionally NOT touched — byte
layouts and on-disk formats are contributor-level detail, not user reference.

---

## Translation review — setup and remote SSH-host guides (2026-07-19)

Added Japanese and Simplified Chinese translations for `cornus setup` and the remote
docker/containerd-host SSH guide. Each preserves command/configuration interfaces and uses
locale-local absolute documentation links.

**Verification.** `npm run docs:build` passed and `git diff --check` was clean. The translation
audit now has page parity (65 source pages for each locale), but still fails on the same eleven
pre-existing structural mismatches in both locales: `cli/socks5`, `cli/tunnel`, `cli/web`,
`guides/ingress`, `guides/observability`, `guides/tunnels`, and `topics/ingress`. They omit source
sections or fenced examples and need deliberate section-by-section synchronization rather than
mechanical formatting changes. No commit made.

---

## User-facing docs — cache data-flow diagram + on-disk explainer (EN + ja + zh) (2026-07-19)

Follow-up to the ARCHITECTURE.md wire/on-disk detail: brought a *user-level* version to the VitePress
site's how-it-works page. Enhanced the "Read caching and writable mounts" subsection of
`docs/architecture/deploy-engine.md` (+ ja/zh) with:
- a mermaid sequence diagram of the shared server-side data path (cached read miss + `,async`
  write-through with seq/hash coherence), participants framed as container / server+cache / your
  machine — not the byte-level proxy/caller internals;
- a "What lives in the cache directory" paragraph (sparse data file + index sidecar under
  `CORNUS_FILE_CACHE_DIR`, sharded, sparse so only-read-chunks stored, survives restarts, capped by
  `CORNUS_FILE_CACHE_MAX_BYTES`) — operationally useful for sizing that volume.

Deliberately kept the raw frame/HELLO/op byte layouts OUT of the user site — those stay in
ARCHITECTURE.md. Followed the established convention (JA build-engine.md) of leaving mermaid diagram
labels in English while translating surrounding prose. Verified: `npm run docs:build` clean (mermaid
renders, no dead-link/anchor errors), ja+zh audit clean (only the expected heuristics), EN region free
of full-width punctuation. Docs-only; no commit.

---

## Documentation sidebar title reconciliation (2026-07-19)

Audited the single trilingual VitePress navigation tree in `docs/.vitepress/config.mts` against the
top-level H1 of every linked page in English, Japanese, and Simplified Chinese. Updated 91 labels
that differed from the document title; routes, section structure, and top navigation behavior were
unchanged.

**Finding.** The title drift was entirely in `TREE`, the shared source for every localized sidebar.
Most entries had intentionally shortened labels (for example, `Overview`, `Auth & TLS`, or
`Comparison`) while their pages used fuller H1 titles. Keeping `TREE` labels identical to those H1s
prevents the sidebar and document title from presenting competing names for the same page in any
locale.

**Verification.** A local audit parsed all 64 navigation items for all three locales (192 entries)
and found zero remaining label/H1 mismatches. `git diff --check` passed. `npm run docs:build` could
not be run in this environment because `npm` is not installed; no code or content pages were changed.

---

## Co-host an MCP server on the web BFF (2026-07-19)

Implemented `cornus-integration-zed/cornus-mcp-plan.md`: the `cornus web` BFF now co-hosts an MCP
(Model Context Protocol) server at `/.cornus/mcp`, and a new `cornus mcp` subcommand serves the same
surface over stdio for launch-a-command clients (Zed, Claude Desktop). SDK:
`github.com/modelcontextprotocol/go-sdk` v1.6.1 (Streamable HTTP + stdio transports).

**Crux — no duplicated operation logic.** Extracted every operation the handlers performed into
value-returning, context-taking methods on `webbff.Server` in a new `core.go` (`Workloads`,
`WorkloadDetail`, `WorkloadAction`, `Graph`, `Apply`, `Mounts`, `FileRead`, `FileWrite`, `LogsTail`,
`ExecRun`, ...). The existing `/.cornus/web/*` handlers were rewritten as thin adapters over these,
and the 17 MCP tools (`mcp.go`) are a second thin adapter — so the UI and MCP surfaces cannot drift.
Core methods signal HTTP-shaped failures with a `statusError{code, err}`; the HTTP adapters map it via
`writeErr` (defaulting to 502, the code the handlers already used for upstream failures), the MCP
adapters surface it as a tool error.

**Boundaries.** Streaming stays web-only. MCP gets a bounded, non-streaming `logs_tail` and a one-shot
`exec_run` (no TTY, output demuxed with `docker/pkg/stdcopy`, each stream capped at `maxToolCapture`
= 256 KiB). `file_write` reuses the exact-path `resolveEditable` allow-list unchanged.

**Security.** The MCP mount lives on the same mux inside the same `guardHost` wrap as the web routes,
so it inherits the loopback/no-auth + DNS-rebinding posture verbatim. The SDK's own localhost-only
rebinding guard is disabled (`DisableLocalhostProtection`) because it would reject the legitimate
published-conduit Host; guardHost's allow-list is the canonical (stronger) guard. On by default;
`cornus web --no-mcp` (and `Config.MCP` / `WebSpec.MCP` for the agent-hosted conduit path) opts out.

**Gotchas.** (1) MCP tool output types are returned as pretty-printed JSON `TextContent` with an `any`
output type, dodging JSON-schema inference over the deep `api.*` result structs (which would panic at
`AddTool` time). (2) A stdio peer closing stdin surfaces as the SDK's internal jsonrpc2 "server is
closing" error (cause `io.EOF`) — not an importable sentinel and it does not unwrap to `io.EOF` — so
`MCPRun` matches it by message to exit 0. (3) The e2e `http` builtin sets `req.Header`, which Go
ignores for `Host`; the foreign-Host rejection on `/.cornus/mcp` is covered by the unit test
`TestMCPGuardHost`, not the scenario.

**Verification.** New `cmd/cornus/internal/webbff/mcp_test.go` drives the server over the SDK's
in-memory transport (tools/list, parity with HTTP handlers, allow-list, guardHost, `--no-mcp` off).
`e2e/scenarios/web.star` extended to hit the live endpoint (Streamable-HTTP `initialize`, `--no-mcp`
serves nothing) via a new `mcp=` kwarg on the harness `web()` builtin. Live-verified the built binary:
stdio `initialize`+`tools/list` returns all 17 tools and exits cleanly; HTTP `initialize` returns 200
SSE with a session id; a foreign Host gets 421. Go gate (gofmt/build/vet/test on affected packages)
clean; `go mod tidy` added only the SDK + 4 transitive deps. Docs: `.agents/docs/LTM/web-ui.md`,
`docs/cli/web.md`, new `docs/cli/mcp.md` (+ sidebar). No commit.

### Follow-up: fold `cornus mcp` into `cornus web --stdio` (2026-07-19)

Consolidated the two commands into one. Removed the standalone `cornus mcp` subcommand
(`cmd/cornus/mcp.go` deleted, `MCPCmd` unregistered) and added a `--stdio` flag to `cornus web`:
`WebCmd.runStdio` builds the same `webbff.Server` (MCP forced on) and calls `bff.MCPRun(ctx)` over
stdin/stdout, binding no HTTP listener. `--stdio` and `--publish-in-conduit` are mutually exclusive.
The HTTP endpoint (`/.cornus/mcp`, `--mcp`/`--no-mcp`) is unchanged. Docs updated: removed
`docs/cli/mcp.md` and its sidebar entry, documented `--stdio` in `docs/cli/web.md`, and repointed the
LTM references. Verified the built binary: `cornus web --stdio` returns all 17 tools over stdio and
`--stdio --publish-in-conduit` errors out. Go gate clean; no dependency change. No commit.

Correction to the entry above: the flag was renamed `--stdio` -> `--mcp-stdio` (clearer that it selects
the MCP-over-stdio transport). All code and docs use `--mcp-stdio`.

---

## MCP on the web BFF — consolidated as-shipped summary (2026-07-19)

Supersedes the three incremental entries above (which reference an interim `cornus mcp` subcommand and
a `--stdio` flag that were both renamed/removed before landing). This is the final state.

### What shipped

`cornus web` co-hosts an MCP (Model Context Protocol) server backed by the same client-side BFF the
browser UI uses. Two transports, one implementation:

- **HTTP**: Streamable-HTTP handler mounted at `/.cornus/mcp` on the same mux as `/.cornus/web/*`,
  behind the same `guardHost`. On by default; `cornus web --no-mcp` opts out.
- **stdio**: `cornus web --mcp-stdio` serves the identical `mcp.Server` over stdin/stdout and binds no
  HTTP listener — for agent clients (Zed context servers, Claude Desktop) that launch a command rather
  than dial a URL. Mutually exclusive with `--publish-in-conduit`. There is NO standalone `cornus mcp`
  command; the capability lives entirely under `cornus web`.

SDK: `github.com/modelcontextprotocol/go-sdk` v1.6.1 (+ 4 transitive deps via `go mod tidy`). 17 tools:
`workloads_list`, `workload_get`, `workload_action`, `workload_delete`, `volume_delete`,
`tunnel_start`, `tunnel_stop`, `tunnels_list`, `projects_list`, `project_graph`, `project_apply`,
`mounts_list`, `files_list`, `file_read`, `file_write`, `logs_tail`, `exec_run`.

### Architecture (the crux — no duplicated operation logic)

Every operation the HTTP handlers performed was lifted into value-returning, context-taking methods on
`webbff.Server` in a new `core.go` (`Workloads`, `WorkloadDetail`, `Graph`, `Apply`, `FileRead`,
`FileWrite`, `LogsTail`, `ExecRun`, ...). The `/.cornus/web/*` handlers (`handlers.go`) and the MCP
tools (`mcp.go`) are both thin adapters over these, so the two surfaces cannot drift. Core methods
signal HTTP-shaped failures with a `statusError{code, err}`; HTTP adapters map it via `writeErr`
(defaulting to 502, the code the handlers already used for upstream failures), MCP adapters surface it
as a tool error. `WebCmd.runStdio` (in `web.go`) is the stdio entry point; `Server.MCPHandler()` /
`Server.MCPRun()` (in `webbff/mcp.go`) are the two transports over one `Server.MCPServer()`.

### Findings / gotchas (durable)

1. **Schema inference panics on rich types.** MCP tool outputs are returned as pretty-printed JSON
   `TextContent` with an `any` output type. Declaring typed `Out` structs makes the SDK's
   jsonschema-go inference walk the deep `api.*` result structs at `AddTool` time and PANIC. Returning
   `any` + explicit `TextContent` sidesteps inference entirely.
2. **stdio clean-shutdown isn't an importable sentinel.** A peer closing stdin surfaces from the SDK as
   its internal jsonrpc2 "server is closing" error (cause `io.EOF`), which is neither importable nor
   unwraps to `io.EOF`. `MCPRun` matches it by message (plus `io.EOF`/`ErrConnectionClosed`/
   `context.Canceled`) to exit 0 instead of erroring on a normal disconnect.
3. **guardHost vs the SDK's own rebinding guard.** The SDK's localhost-only DNS-rebinding protection
   would reject the legitimate published-conduit Host (`cornus.internal`), so it is disabled
   (`DisableLocalhostProtection`); the BFF's `guardHost` allow-list is the canonical, stronger guard,
   and it already wraps the whole mux including `/.cornus/mcp`.
4. **Streaming can't cross MCP.** Interactive exec/terminals and live logs/stats WebSockets don't fit
   request/response. MCP gets a bounded non-streaming `logs_tail` and a one-shot `exec_run` (no TTY,
   output demuxed with `docker/pkg/stdcopy`, each stream capped at `maxToolCapture` = 256 KiB).
   `file_write` reuses the exact-path `resolveEditable` allow-list unchanged.
5. **e2e `http` builtin can't set Host.** Go ignores a `Host` entry in `req.Header` (it uses `req.Host`),
   so the foreign-Host rejection on `/.cornus/mcp` can't be exercised from the Starlark `http` builtin;
   it's covered by the unit test `TestMCPGuardHost` instead.

### Security posture

Same threat model as the UI verbatim: loopback/no-auth + DNS-rebinding Host allow-list. With
`--publish-in-conduit` the HTTP MCP endpoint is published in the shared SOCKS5 conduit alongside the UI
(`clientagent.WebSpec.MCP` carries the flag), exposing `file_write`/`exec_run` to conduit users exactly
as the existing web surface already is; `--no-mcp` narrows that blast radius.

### Verification

`cmd/cornus/internal/webbff/mcp_test.go` drives the server over the SDK's in-memory transport
(tools/list, parity with the sibling HTTP handlers, allow-list, guardHost on `/.cornus/mcp`, `--no-mcp`
removal). `e2e/scenarios/web.star` hits the live HTTP endpoint (Streamable-HTTP `initialize`, and
asserts `--no-mcp` serves nothing) via a new `mcp=` kwarg on the harness `web()` builtin. Live-verified
the built binary: `cornus web --mcp-stdio` returns all 17 tools over stdio and exits cleanly; HTTP
`initialize` returns 200 SSE with a session id; a foreign Host gets 421; `--mcp-stdio
--publish-in-conduit` errors out. Go gate (gofmt/build/vet/test on affected packages) clean.

### Files

New: `cmd/cornus/internal/webbff/{core.go, mcp.go, mcp_test.go}`. Modified: `webbff/{handlers,webbff}.go`,
`cmd/cornus/{web.go, main.go}`, `cmd/cornus/internal/clientagent/{protocol,web}.go`, `pkg/e2e/harness.go`,
`e2e/scenarios/web.star`, `docs/cli/web.md`, `docs/.vitepress/config.mts`, `.agents/docs/LTM/web-ui.md`,
`go.mod`/`go.sum`. (The interim `cmd/cornus/mcp.go` and `docs/cli/mcp.md` were created then removed
during consolidation, so they don't appear in the final diff.) No commit. Follow-up: `docs/ja` and
`docs/zh` `cli/web.md` still lag the English canonical and need the `translate-documents` skill.

## Registry re-export of the local Docker daemon image store (Phase 1)

Added an opt-in mode where cornus's `/v2/*` registry re-exports the local Docker
daemon's image store instead of maintaining a separate persistent CAS, so a
developer working against a local Docker host does not need a second copy of
images pushed into a cornus-owned registry. Enabled with
`CORNUS_REGISTRY_SOURCE=docker-daemon`. This is Phase 1 (read-only re-export) of a
staged plan; build-export-into-daemon, the dockerhost self-pull skip, a native
containerd source, and docs are later phases.

### Design

Reuses the existing pull-through `Mirror` seam. Generalized `Registry.mirror
*Mirror` into `Registry.source imageSource` (a 2-method interface: `manifest` /
`blob`); `*Mirror` already satisfied it. Caching stayed Mirror-only via an
optional `cachingSource` interface (`cacheFetched() bool`), so the daemon source
is transparent (never persists) — the daemon is the single authoritative store.
`serve{Manifest,Blob}FromMirror` → `...FromSource`.

The daemon source (`pkg/registry/daemon_source.go`) fetches an image via
`docker save` (`GET /images/{ref}/get`) into a temp tar and reconstructs an
OCI-consistent `v1.Image` with go-containerregistry's `pkg/v1/tarball`
(`tarball.ImageFromPath`). On a manifest GET it serves `RawManifest`/`MediaType`/
`Digest` and warms a `digest → ref` index (config + every layer); a blob GET looks
up that index (warm because OCI clients fetch the manifest before its blobs) and
streams `RawConfigFile` or `LayerByDigest(...).Compressed()`. A short-TTL
(`60s`) cache keyed by daemon ref keeps one `docker save` per pull; the temp tar
lives for the entry's lifetime (tarball's opener re-opens by path) and is removed
on expiry (Linux unlink-after-open protects in-flight reads).

**Key dependency finding:** go-containerregistry v0.21.7's `pkg/v1/daemon`
package does NOT compile against the pinned `docker/docker v27.4.0-rc.2` (its
`daemon.Client` wants the newer moby `ImageSave`/`ImageInspect`/`Ping`
signatures). So the source hand-rolls a tiny Docker REST client
(`DockerImageAPI` + `NewDockerImageAPI`, transport mirrored from
`pkg/deploy/dockerhost/engine.go`) — no moby client, no docker bump. The interface
seam also fakes cleanly in tests.

### Gating / wiring (fail-closed)

`server.registrySourceMode()` validates `CORNUS_REGISTRY_SOURCE` in `server.New`:
accepted only as `docker-daemon`, only under the dockerhost backend
(`CORNUS_DEPLOY_BACKEND` unset/`dockerhost`; kubernetes/bare/containerd rejected),
never with `CORNUS_REGISTRY_MIRROR`. The `DockerImageAPI` is built in `New` (so a
bad `DOCKER_HOST` is a hard startup error) and stored on `Server.daemonImageAPI`;
`routes()` wires `registry.WithDaemonSource(api, cfg.UploadsDir())`. `serve.go`
defaults `StorageURL` to `mem://` in this mode when the operator set no explicit
storage, so no persistent CAS is created.

### Degraded surfaces (Phase 5 will document)

Over the empty mem store, `_catalog` / `tags/list` return empty (never 500);
referrers return the spec-compliant empty index; GC is a no-op (image lifecycle
is the daemon's `docker image prune`). Caveat: `docker save` recomputes
layer/config/manifest digests, so a client pulling by a manifest digest learned
elsewhere 404s — pulling by tag is internally consistent. Intended scope: local
dev, not a high-fanout registry.

### Verification

`pkg/registry/daemon_source_test.go`: a fake `DockerImageAPI` serves a golden
`docker save` tar (`tarball.Write` over `random.Image`); the real `Registry` is
driven with go-containerregistry `remote` and `validate.Image` (verifies manifest
+ config + every layer). Covers pull+validate, one-save-per-pull caching, manifest
HEAD (200 + digest header), absent-manifest and cold-blob 404, empty catalog,
ref-mapping. `pkg/server/registry_source_test.go` covers the gating matrix. Go
gate clean (gofmt/build/vet, `go test ./...`). No commit.

### Files

New: `pkg/registry/daemon_source.go`, `pkg/registry/daemon_source_test.go`,
`pkg/server/registry_source_test.go`. Modified: `pkg/registry/{registry.go,
mirror.go}` (source seam), `pkg/server/server.go` (gating + wiring + field),
`cmd/cornus/serve.go` (mem:// default). Follow-up phases: build export into the
daemon (`ExporterDocker` + `/images/load`), dockerhost skip-pull-if-local
(`GET /images/{ref}/json`), native containerd content-store source for
containerdhost, and user docs (`ARCHITECTURE.md` + `docs/`).

## Registry local-store re-export: Phases 2-5 (containerd source, build export, skip-pull, docs)

Completed the remaining phases of the local-image-store re-export feature (Phase 1
was the docker-daemon read path). The full feature lets a developer working
against a local Docker or containerd host reuse their existing image store instead
of a separate cornus registry.

### Phase 2 - containerd content-store re-export source

`CORNUS_REGISTRY_SOURCE=containerd` (gated to `CORNUS_DEPLOY_BACKEND=containerd`)
adds a second `imageSource` reading a host containerd's native content store —
cleaner than the docker-save path since blobs are already digest-addressable.
`pkg/registry/containerd_source_linux.go` (+ `_other.go` stub, + neutral
`containerd_source.go` holding `WithContainerdSource`). It dials containerd lazily
(`ctd.New`), stamps the managed namespace on every ctx (`namespaces.WithNamespace`
— the containerdhost pattern), and serves: a digest manifest read straight from
the content store; a tag manifest resolved by `resolveImage` (exact `repo:tag`
Get, then a `List`+`matchImageName` fallback that host-strips and is
`library/`-insensitive, so a cornus-built `127.0.0.1:<port>/repo:tag` and an
external `docker.io/library/repo:tag` both match a bare `/v2/<repo>` pull); blobs
streamed via `content.Store.ReaderAt`. The store logic is behind `imageGetter` /
`blobStore` interfaces (subsets of containerd's `images.Store` / `content.Store`)
so `manifestFromStores`/`blobFromStore`/`resolveImage` unit-test with in-memory
fakes — no live containerd. Note: the containerd build worker
(`CORNUS_BUILD_WORKER=containerd`) already lands builds in that store, so there is
no separate build-export work for this mode.

### Phase 3 - build export lands in the local Docker daemon

In docker-daemon mode a build now loads into the daemon instead of pushing.
`builder.SolveInput`/`Request` gained `DockerArchiveOutput
func(map[string]string)(io.WriteCloser,error)`; when set, `solve_linux.go` swaps
the `ExporterImage` push for `client.ExporterDocker` (docker-archive) with the
Target/Tags verbatim as the exporter `name`. Server-side, `Server.dockerLoadExport`
(build.go) returns an Output that pipes the archive into
`DockerImageAPI.ImageLoad` (`POST /images/load`, added to the registry docker
client) in a goroutine, plus a `wait()` that blocks for the load and surfaces its
error as the build error. Wired into both `handleBuild` (plain HTTP) and
`handleBuildAttach` (remote): in docker-daemon mode `push=false`, the loopback
target rewrite is skipped, and the archive is loaded. The goroutine is started
inside the Output func so a build that fails before export leaks nothing.

### Phase 4 - dockerhost skip-pull-if-local

`engineClient.imageExists` (`GET /images/{ref}/json`, raw ref in the path — moby
does not url-escape it) + a `dockerhost.WithSkipPullIfLocal(pred)` option.
`apply` skips `imagePull` when the predicate matches and the daemon already has
the image (an inspect error falls back to a normal pull, never a stale deploy).
The server installs the predicate `localRegistryRef` (bare or loopback-host refs)
only in docker-daemon mode via `defaultBackendFactory`. This closes the self-pull
loop: without it, deploying a cornus-served ref would ask the daemon to pull from
cornus's registry, which reads back from the daemon.

### Phase 5 - gating, docs, degraded surfaces

`registrySourceMode` now validates three modes (""/docker-daemon/containerd),
each gated to its backend and mutually exclusive with the mirror; `serve.go`
defaults storage to `mem://` for any re-export mode (no persistent CAS). Docs:
`ARCHITECTURE.md` "Registry and content store" gained a "Miss fallbacks" subsection
covering the mirror + both re-export sources; `docs/reference/server-env-vars.md`
documents `CORNUS_REGISTRY_SOURCE` (+ the previously-undocumented
`CORNUS_REGISTRY_MIRROR`/`_CACHE`) with a "Reusing a local image store" section.
Degraded surfaces (empty `_catalog`/tags over mem store, GC no-op, digest-recompute
caveat) are documented rather than special-cased — the handlers stay mode-agnostic.

### Verification

Unit tests, no live daemon/containerd: `containerd_source_linux_test.go` (manifest
by tag/digest, blob, list-fallback resolution, `matchImageName` matrix via
in-memory fakes); dockerhost skip-pull tests (present→skip, absent→pull,
external→pull); `build_export_test.go` (`dockerLoadExport` pipe/wait, error
propagation, no-archive no-leak); extended `registrySourceMode`/`localRegistryRef`
gating matrix. Full gate clean: `gofmt -l` empty, `go build ./...`, `go vet ./...`,
`go test ./...` all pass. No commit.

### Follow-ups

A live-daemon E2E scenario (build → daemon load → pull-through → deploy without a
registry round-trip) on the docker E2E target would exercise the whole path
end-to-end; deferred (opt-in harness, needs a real dockerd). containerd build in
this mode relies on `CORNUS_BUILD_WORKER=containerd` (documented). Translations
`docs/ja`/`docs/zh` of the env-var reference lag the English canonical.

## Writable client-local 9P mount: missing Lock and O_APPEND write breakage (2026-07-19)

### Context

A compose project (`/tmp/simple-scenario`) with a service (`frontend`, `next dev`
via Turbopack) bind-mounting `./frontend` writable, sharing that same dir with a
second service (`teaser`, a `sed -i` loop), crashed the `frontend` service on
k3s. Reported symptoms: `frontend` → *"An IO error occurred while attempting to
create and acquire the lockfile … Permission denied (os error 13)"*; `teaser` →
intermittent `sed: … Input/output error`. Reproduced live on the existing
k3s/cornus-server setup (kubernetes backend, caretaker-sidecar 9P mount, mount
served client-side by `cornus compose up`).

### Findings (two real bugs in `pkg/wire/writablefs.go`)

Both are in the writable confined 9P export that backs read-write client-local
deploy mounts. The export decorates `localfs` but embeds `templatefs.NoopFile`,
which silently stubs out ops the decorator does not override.

1. **File locking returned ENOSYS.** `writableConfinedFile` never overrode
   `Lock`, so it inherited `NoopFile`→`NotLockable.Lock` = ENOSYS. Turbopack
   flock()s `.next/dev/lock`; the `Tlock` came back ENOSYS → *"Function not
   implemented"*. Fix: delegate `Lock` to `f.inner` (localfs flock on the
   server-side fd). The lock is held on the host file, coherent across all 9P
   clients of the mount (intended single-writer semantics).

2. **O_APPEND writes failed with EIO.** `localfs.Open`/`Create` pass raw 9P flags
   (incl. `O_APPEND`) to `os.OpenFile`, then serve writes via `os.File.WriteAt`
   (pwrite) — which Go refuses on an O_APPEND fd (`errWriteAtInAppendMode`). Every
   append to a file in the mount → server error → kernel maps to EIO. This is what
   `next dev`'s *"Failed to flush logs to file: EIO"* was, and what `teaser`'s
   `sed` I/O churn traced to. Fix: `stripAppend()` clears `O_APPEND` on
   Open/Create — safe because a `cache=none` 9P client already positions each
   append write at EOF, so a plain pwrite there appends correctly.

Red herring that masked bug 1: the reported `Permission denied (os error 13)` was
a **stale root-owned `.next/dev`** on the host from a prior real `docker compose`
run. The 9P mount uses `access=client` and the export runs as the host user
(uid 1000), so neither the container (root, via 9P-as-1000) nor the host user
could write into that root-owned dir. Once cleaned, the true error underneath is
the ENOSYS of bug 1. Confirmed cornus itself creates files as uid 1000, not root.

### Verification

- Unit tests (`writablefs_test.go`): `TestWritableConfinedHonorsLock`,
  `TestWritableConfinedAppendWrites`. Each verified to fail before the fix (exact
  production errors: `function not implemented` / `input/output error`) and pass
  after. The Lock test is white-box (calls the server-side `Lock` directly): the
  hugelgupf/p9 **Go client** `clientFile.Lock` omits the fid on the `tlock` it
  sends, so it cannot drive a lock — the real client is the kernel 9P client.
- End-to-end through the real kernel 9P client (`repro9p_linux_test.go`, gated on
  `CORNUS_REPRO_9P=1`, run in a privileged container): serves the actual export on
  a unix socket, `mount -t 9p trans=unix`, runs a sed-rename + concurrent-reader
  workload, then flock()s a lockfile. flock failed with `function not implemented`
  before, passes after.
- Live k3s: fixed CLI took `frontend` from crash-loop → `2/2 Running`, past both
  the lockfile and the log-flush EIO; `teaser` clean. Full gate green.

The fix ships in the **CLI** (the export runs client-side in `cornus compose up`);
the in-cluster server image does not need rebuilding for it.

### Follow-up (unfixed, needs a design decision)

With both fixes in, `next dev` runs far enough to hit a *new* blocker it never
reached before: Turbopack's persistent-cache DB `mmap`s its files, and **`mmap` is
unsupported on a `cache=none` 9P mount** (writable mounts use `cache=none`
deliberately for coherence — see `pkg/deploywire/backing_linux.go`). Surfaces as
*"Failed to open database … Cannot allocate memory (os error 12)"*. Fixing it
means changing the writable-mount cache mode (`cache=loose`/`mmap`), trading away
coherence — deferred. Workarounds: disable Turbopack persistent caching, or keep
`.next` off the 9P mount (as the scenario already does for `node_modules`).

## Summary and findings: local Docker/containerd image-store re-export

This consolidates the two entries above ("Registry re-export ... (Phase 1)" and
"Registry local-store re-export: Phases 2-5"). One feature, shipped whole.

### What and why

Goal: a developer working against a **local Docker or containerd host** should be
able to reuse their existing image store instead of pushing a second copy into a
separate cornus registry. Opt in with `CORNUS_REGISTRY_SOURCE`:
`docker-daemon` (gated to the `dockerhost` backend) or `containerd` (gated to the
`containerd` backend). In these modes cornus's `/v2/*` becomes a read-only *view*
over the local store and keeps no persistent CAS (storage defaults to `mem://`).

End-to-end shape (docker-daemon): `cornus build` loads the image into the daemon
(docker-archive → `POST /images/load`) instead of pushing; a `dockerhost` deploy
of an image the daemon already has skips the registry pull; any other consumer
(a second daemon, a remote node, `docker pull`) can still pull it through `/v2/*`,
served on demand via `docker save`. containerd mode is the same idea reading the
native content store; builds land there via `CORNUS_BUILD_WORKER=containerd`.

### Durable findings (reusable beyond this feature)

1. **go-containerregistry `pkg/v1/daemon` does NOT compile against the pinned
   `docker/docker v27.4.0-rc.2`.** Its `daemon.Client` interface needs the newer
   moby `ImageSave`/`ImageInspect`/`Ping(PingOptions)` signatures. Anything that
   wants to read/write the local daemon must hand-roll a REST client (as
   `pkg/deploy/dockerhost` already does) and use the dependency-light
   `pkg/v1/tarball` for save/load, or accept a docker bump + license re-audit.
2. **`docker save` writes UNCOMPRESSED layers, so digests are recomputed.** A
   `v1.Image` reconstructed from a real daemon's save tar has its own
   layer/config/manifest digests (go-containerregistry gzips on the fly), which
   will not equal a prior BuildKit push's digests. Consequence: pull-by-tag is
   internally consistent; pull-by-manifest-digest-learned-elsewhere 404s. (Note:
   go-containerregistry's own `tarball.Write` writes *compressed* layers, so a
   round-trip in tests preserves digests — tests must not assert digest equality
   to stay honest about the real uncompressed path.)
3. **The pull-through `Mirror` was the right seam to generalize.** A miss
   fallback is now one `imageSource` interface (`manifest`/`blob`) with three
   impls (mirror, docker-daemon, containerd); caching stayed mirror-only behind an
   optional `cachingSource` interface. New sources cost ~one file, no handler
   changes.
4. **Self-pull loop is real and must be closed deploy-side.** When the deploy
   target is the same daemon backing the registry, `POST /images/create` asks the
   daemon to pull an image it already has from a registry that reads back from it.
   The `imageExists` (`GET /images/{ref}/json`, ref raw in the path — moby does not
   url-escape) + skip-pull predicate is the fix; it degrades safely to a normal
   pull on any inspect error or absent image.
5. **containerd reads need the namespace stamped per-ctx**
   (`namespaces.WithNamespace`, not a client default) or the store reads empty;
   name resolution must be host-stripped and `library/`-insensitive to match both
   cornus-built loopback-qualified names and external `docker.io/library/...`.

### Testing posture

Every layer is unit-tested with no live daemon/containerd: fake `DockerImageAPI`
serving a golden save tar driven through the real `Registry` with
go-containerregistry `remote` + `validate.Image`; in-memory `imageGetter`/
`blobStore` fakes for the containerd path; httptest-fake daemon for skip-pull;
pipe/wait orchestration for the build export. Follow-up: a live-daemon E2E
scenario (build → load → pull-through → deploy with no registry round-trip) on the
docker E2E target — deferred because it needs a real dockerd (opt-in harness,
outside `go test ./...`).

## Registry re-export: drop the CAS in pure re-export mode (optional store)

Follow-up to the local-store re-export feature. Previously re-export mode kept a
vestigial empty `mem://` content store just to satisfy the registry's
"store-required" assumption — every pull did a guaranteed-miss store lookup before
falling through to the source, and a stray `docker push` was silently swallowed
into a throwaway store. Made the content store **optional**.

- `registry.New(nil, ...)` is now valid. `Registry.readOnly()` (store == nil)
  gates the handlers: manifest/blob GET/HEAD serve straight from the source (no
  store lookup); blob upload, manifest PUT, and blob/manifest DELETE return `405`
  (`rejectReadOnly`); `_catalog`/tags return empty; referrers returns the empty
  OCI index. The mirror's cache-write path also guards `r.store != nil`.
- `pkg/server/gc.go` `runGC` skips the CAS sweep when `s.store == nil` (reports 0
  blobs freed; the localcache/filecache prunes still run).
- `cmd/cornus/serve.go`: for a re-export mode with no explicit `--storage`
  (`pureReexport`), storage is **not opened at all** — `server.New(cfg, nil)` — and
  the startup log reports `storage=none (re-export of <mode>)`. The earlier
  `mem://` default is removed. An explicit `--storage` still opens a CAS, giving a
  union view (store primary, source fills misses).

Net: pure re-export (docker-daemon/containerd with no `--storage`) genuinely has
**no content store** — the local runtime is the sole authority — while the union
configuration (explicit `--storage`) is preserved for anyone who wants a CAS
layered under the daemon/containerd view. Write verbs are now cleanly rejected
(405) instead of silently landing in a throwaway store, which subsumes the
optional "405 on writes" hardening item.

Verification: `daemon_source_test.go` now drives the registry with a **nil** store
(the real pure-reexport config) and adds `TestDaemonSourceRejectsWrites` (PUT
manifest / DELETE / blob upload → 405); existing pull/HEAD/miss/catalog tests pass
unchanged against the nil store. Full gate clean (`gofmt`/build/vet/`go test ./...`).
Docs updated: `ARCHITECTURE.md`, `docs/architecture/server-and-registry.md`,
`docs/reference/{server-env-vars,storage-backends}.md`, `.agents/docs/OVERVIEW.md`
— all now say "no content store" (not "mem://") for pure re-export and document the
explicit-`--storage` union. No commit.

Deferred (user-flagged): E2E scenario enhancement for the re-export path — still
open in TODO.md, to do later.

## Re-export follow-ups swept: E2E scenario + ja/zh translations + 405 hardening

Cleared the three "Local image-store re-export follow-ups" from TODO.md.

### E2E scenario

`e2e/scenarios/registry-source-docker-daemon.star` (registered in the Makefile
`SCENARIOS` list, next to the other `registry-*` scenarios). docker-only, so it
self-skips (`if TARGET != "docker"`) on every other target, matching
docker-push.star. Flow: `serve(env={"CORNUS_REGISTRY_SOURCE":"docker-daemon"})`
(composes with the docker target's `CORNUS_DEPLOY_BACKEND=dockerhost`) →
`build_upload(target="cornus-e2e-reexport:v1", context="e2e/scenarios/app")` which
goes through the server's `POST /.cornus/v1/build` so the docker-archive is
`docker load`ed into the daemon (the local `build()` path bypasses the server and
would push to a CAS instead — the load-bearing gotcha). Then asserts: `/v2/<repo>/
manifests/v1` → 200 (re-export via docker save), `/v2/_catalog` → `{"repositories":
[]}` (no CAS), a manifest `PUT` → 405 (read-only), and a `dockerhost` deploy that
reaches `running:1` — proving skip-pull, since a bare ref would otherwise be pulled
from docker.io and fail. Used a bare target on purpose: the daemon tags a
cornus-built image by the build target verbatim, and `daemonRef(repo,ref)` maps a
`/v2/<repo>` pull to `<repo>:<ref>`, so a bare target round-trips by the short path
(a registry-qualified target would not). No new builtin needed. Parses + resolves
under the e2e `--check`; a live run needs a real dockerd.

### Translations (ja + zh)

Propagated the re-export docs into both locales via the translate-documents skill:
5 English pages × 2 locales = 10 files (server-env-vars, storage-backends,
deploy-backends under reference; server-and-registry, build-engine under
architecture). Kept JA half-width parens/colons (the JA convention, per the
existing files + glossary) and ZH full-width (its convention); localized every
cross-page link (`/ja/...`, `/zh/...`) and gave the translated "Reusing a local
image store" headings an explicit `{#reusing-a-local-image-store}` anchor so the
inbound links resolve despite the translated heading text. Structural audit passed
(warnings are heuristic inline-code/link-order only), `npm run docs:build` clean.

### 405 hardening

Already delivered by the optional-store refactor (see the prior "drop the CAS"
entry) — `Registry.readOnly()` rejects write verbs with 405 — so the TODO item was
closed as done, not reimplemented.

### Note

`make e2e-check`/`make build` currently fail on `cmd/cornus/internal/webbff/
core.go` (`undefined: execCapture`) — a concurrent agent's in-progress MCP/webbff
work, unrelated to this change. My packages (registry, server, dockerhost, builder,
e2e) build and test green; `pkg/e2e` passes so `TestPredeclaredNamesInSync` holds.
No commit.

## Japanese translation: preserve Go schema types and reference-document terminology

Japanese Markdown schema tables must preserve Go type literals verbatim, including `string`, `[]string`, `map[string]string`, `map[string][]int`, named types such as `Mount` and `Healthcheck`, and `map[string][Context]`. Do not translate their identifiers in type cells. Use `リファレンス` for documentation senses of reference (for example, field references and page titles); retain `参照` for actual referential relationships such as image, owner, and Secret references. The canonical glossary term for deploy spec is `デプロイスペック`.

## Japanese translation correction pass

Reviewed the Japanese documentation for three terminology classes. Restored literal Go schema types in the deploy-spec and connection-config references: `string`, `[]string`, `map[string]string`, `map[string][]int`, `[][Mount]`, `[Healthcheck]`, and `map[string][Context]`. Normalized documentation-reference senses of reference to `リファレンス` while preserving `参照` for genuine relationships such as image, owner, and Secret references. Applied the glossary term `デプロイスペック` consistently, including the deploy-spec page title and links.

Verification: focused translation audit of the two updated schema pages passed with three heuristic inline-code/link-order warnings; `npm run docs:build` passed. The full translation audit reported 11 pre-existing structural mismatches in unrelated Japanese pages. `git diff --check` passed. No commit.

## Web file explorer (Finder/Explorer-style) for `cornus web`

Replaced the flat config-file editor (`web/src/views/Files.tsx`) with a two-pane file
explorer that browses two sources behind one unified surface: the developer's LOCAL
filesystem (confined to a set of roots — the compose project dir plus each external
bind-mount source) and a running WORKLOAD's CONTAINER filesystem. Full file
management: navigate, preview/edit (CodeMirror), new folder, upload, rename, delete,
download, with breadcrumb nav and keyboard selection.

New BFF surface `/.cornus/web/fs*` in `cmd/cornus/internal/webbff/fs.go` +
`fs_handlers.go` (a strict superset of the legacy `/files*`, which is untouched). Key
design decisions:

- Local containment ports `pkg/wire/confinedfs.go`'s `guard.within` (EvalSymlinks +
  root-prefix check) as `underRoot`; `resolveLocal` cleans `../` at the door. `..`/
  absolute spellings collapse to an in-root path (safe), escaping symlinks 403.
- Container directory LISTING has no `ReadDir`, so it execs a portable `sh` glob loop
  (`listScript`) that emits NUL-framed records — busybox+GNU safe, newline-in-name
  safe, injection-free because the dir rides as the exec `WorkingDir`. A shell-less
  image falls back to reading only the top-level tar headers of a recursive
  `CopyFrom` (`containerListTar`). Reads/writes/mkdir use single-entry tars
  (CopyFrom/CopyTo); rename/delete are direct `mv`/`rm` execs (no shell).
- Introduced a `containerFS` interface (`s.cfs`) so the container source is unit-
  testable with a `fakeContainerFS` — no live daemon. `ExecRun` was refactored onto a
  shared `execCapture(cl, name, workdir, cmd)` helper that adds `WorkingDir`.

Frontend: typed client in `web/src/api.ts` (`listDir`/`statPath`/`mkdir`/`renamePath`/
`deletePath`/`uploadFile`/`readFsContent`/`writeFsContent`/`fsContentURL`); a shared
stateful in-memory mock (`web/src/mock/fs.ts`) wired into both the test stub and the
dev server so `npm run dev:mock` is live.

Verification: `gofmt -l` clean; `go build/vet ./...` and `go test ./...` green
(new `fs_test.go` covers local round-trip + confinement + roots, and container
listing/read/write/rename/delete/tar-fallback via the fake seam); `npm run build`
(tsc+vite) and `npm test` (77 tests) green. Restored the vite-wiped
`pkg/webui/dist/.gitkeep`. No commit.

## Consolidated Workloads/Projects/Mounts/Tunnels into Overview (`cornus web`)

Merged four separate SPA screens into a single scrolling **Overview** dashboard.
Per the user's choices: stacked `<h2>` sections (not tabs), and the four screens
removed from the sidebar with their list routes dropped.

- `web/src/views/{Workloads,Projects,Mounts,Tunnels}.tsx` — demoted each screen's
  `<h1>` to `<h2 id="…">` (and their internal `<h2>` subheadings to `<h3>`), so
  they render as sections. They keep their own `pollResource`/state and remain
  self-contained components (still unit-tested standalone in `views.test.tsx`).
- `web/src/views/Overview.tsx` — composes `<Workloads/> <Projects/> <Mounts/>
  <Tunnels/>` after the summary cards; dropped the now-redundant inline Projects
  summary table and its poller; the Workloads/Compose-project cards link to
  in-page anchors (`#workloads`, `#projects`) instead of routes.
- `web/src/App.tsx` — `NAV` trimmed to Overview/Files/Terminal/Settings (this also
  removes the four palette "Go to" entries automatically).
- `web/src/index.tsx` — dropped the `/workloads`, `/projects`, `/mounts`,
  `/tunnels` routes/imports; kept `/workloads/:name` (WorkloadDetail), still linked
  from the workloads/mounts/tunnels sections.
- BFF endpoints and mocks unchanged (routes ≠ endpoints); the `/workloads` etc.
  JSON endpoints and `handler.ts`/`server.ts` cases stay.

Verified: `npm run build` (tsc+vite) and `npm test` (77 tests) green; restored the
vite-wiped `pkg/webui/dist/.gitkeep`. No commit.

## Made the Overview project-oriented (`cornus web`)

Follow-up to the consolidation above: instead of four flat whole-fleet sections,
the Overview now renders **one section per compose project**, each carrying that
project's own workloads, mounts, and port-forwards (plus its Apply control and
depends_on graph). Anything not attached to a loaded project falls into a trailing
"Other" section; conduit banners and the terminal-sessions table remain global.

Refactor (the four view files became prop-driven presentational pieces — no
orphans, no route/nav changes):
- `Workloads.tsx` -> `WorkloadTable({workloads, onChanged})`: renders one filtered
  set with per-row actions; drops the redundant Service/Project columns since the
  section header already names the project. Calls `onChanged` to refetch after an
  action.
- `Mounts.tsx` -> `MountTable({mounts})`; `Tunnels.tsx` -> `ForwardsView({tunnels,
  forwards})` (public tunnels + local forwards for the given subset).
- `Projects.tsx` -> `ProjectSection({title, project?, workloads, mounts, tunnels,
  forwards, onChanged})` composing the three tables + Apply + graph; also exports
  `slug()` for the section anchor. `project` omitted => the "Other" bucket (no
  Apply/graph).
- `Overview.tsx` polls config/projects/workloads/mounts/tunnels/terminals once and
  does the grouping: workload/mount `.project`, tunnel workload->project via a
  name map, forward service->project via a service map. Compose-project card links
  to `#<slug>`.
- `styles.css` — `.project` section rule (top rule + spacing).

Tests: dropped the now-prop-driven standalone describes; the Overview describe
asserts the project grouping (a `#project-shop` section, an `#project-other`
bucket holding project-less `legacy-cron`, shop's workloads/mounts/forwards/graph)
and that legacy-cron is *not* inside the shop section. `npm run build` + `npm test`
(74) green; `dist/.gitkeep` restored. No commit.

## Registry re-export becomes the default: the host-native token

Renamed the registry re-export knob to a single `CORNUS_REGISTRY_SOURCE=host-native`
token and made it the **default on host backends** (dockerhost and containerd).
Rationale: the two prior values (`docker-daemon`, `containerd`) were each redundant
with the deploy backend, so one auto-resolving token is cleaner, and for a local
host the redundant separate registry is a papercut.

### Resolution model (`resolveRegistrySource`, server.go)

`CORNUS_REGISTRY_SOURCE`: `host-native` | `off` | unset. host-native resolves to
the docker-daemon source under dockerhost and the containerd content-store source
under containerd (rejected on bare/kubernetes). It is the default when unset on a
host backend with no `--storage` and no `CORNUS_REGISTRY_MIRROR`. Store: pure (nil,
no CAS) with no `--storage`; union (CAS + source) with `--storage`; `off` (or a
non-host backend, an explicit `--storage`, or a mirror) keeps the classic CAS.
Exported `RegistryKeepsNoContentStore(cfg)` so cmd/cornus serve decides whether to
open a store without duplicating the logic.

### The load-bearing finding: pure default breaks in-process builds

A PURE default (no CAS) would 405 in-process `cornus build -t localhost:5000/app`
and the whole /v2/ push surface, because in-process builds push straight to /v2/
and only the server-routed build path can docker-load. My earlier "the engine lock
saves us" reasoning was WRONG — the server's build engine is lazy, so an in-process
build alongside a running server doesn't contend on engine.lock; it just pushes.
The user chose the pure default anyway (accepting the docker-save digest-recompute
limitation and that builds must route through the server). Verified the common
paths survive: `pkg/client.Build` goes through `/.cornus/v1/build/attach`
(docker-load), so `cornus compose build` / profile builds work; only a raw
in-process build to a local host-native server 405s (documented — use `off`,
`--storage`, or a server-routed build).

### containerd host-native build handling

Under containerd host-native, builds must land in the containerd image store to be
re-exportable — the containerd build worker's behavior. getEngine auto-selects
`WorkerContainerd` when the source is containerd and `CORNUS_BUILD_WORKER` is unset
(New warns if it is forced to a non-containerd worker), and both build handlers set
`push=false` for the containerd source (the worker's ImageStore lands the image;
`NewWorkerOpt` sets that store independent of push). docker-daemon keeps the
docker-archive → `/images/load` path.

### E2E fallout, contained

Because the docker/containerd/local E2E targets serve() with no storage, they would
all flip to the pure host-native default and break every /v2/-push scenario
(registry.star, deploy.star's in-process build(), etc.). Fix: the `local`/`docker`/
`containerd` targets' ServeEnv now sets `CORNUS_REGISTRY_SOURCE=off` (classic CAS),
so the existing suite is unchanged; the renamed `registry-host-native.star`
(from registry-source-docker-daemon.star) sets `host-native` explicitly via
serve(env=). `TestContainerdTargetServeEnv` updated for the new key. bare/kube
targets are non-host backends, unaffected.

### Verification

`resolveRegistrySource` unit test rewritten (defaults per backend, off, pure vs
union via --storage, host-native resolution, mirror exclusivity, non-host
rejection). Full gate green: gofmt, `go build ./...`, `go vet ./...`,
`go test ./...`, `make e2e-check` (all scenarios parse). Docs updated to
host-native/default/off/pure-vs-union across EN canonical (server-env-vars,
server-and-registry, build-engine, deploy-backends, storage-backends, ARCHITECTURE,
OVERVIEW) and ja+zh (5 pages each, via translate-documents subagent — audits pass,
JA half-width / ZH full-width preserved, anchors locale-prefixed); `npm run
docs:build` clean across all locales. No commit.

Follow-up: a live containerd-target E2E for host-native (the docker one is written;
containerd host-native build→store→re-export→deploy is only reasoned + unit-tested).

## Overview: project- vs workload-oriented grouping toggle (`cornus web`)

Added a segmented control (`By project` / `By workload`) to the Overview header that
pivots the dashboard between two groupings of the same data:
- **By project** (default, unchanged): one `ProjectSection` per compose project +
  an "Other" bucket.
- **By workload**: one `WorkloadSection` per workload — header (name link, status
  badge, service · project, start/stop/restart/delete) then that workload's own
  mounts and port-forwards (public tunnel where `t.workload===name`, local forwards
  where `svc===w.service`). Anchored `#workload-<name>`.

Refactor: extracted `WorkloadActions` (the shared start/stop/restart/delete buttons
with their own busy/error state) out of `WorkloadTable` so both the project-view
table rows and the workload-view section headers reuse it. `WorkloadSection` lives
in `Workloads.tsx` alongside `WorkloadTable`, composing `MountTable` + `ForwardsView`.
`Overview.tsx` holds a `mode` signal and renders one branch or the other; the global
conduit banners and terminal-sessions table stay below both. New `.seg` styles in
`styles.css`.

Tests: added an Overview case that clicks the `By workload` tab and asserts the
`#workload-*` sections appear (with each workload's mount + tunnel) and the
`#project-*` sections disappear. `npm run build` + `npm test` (75) green;
`dist/.gitkeep` restored. No commit.

## Work summary: Overview dashboard restructuring arc (`cornus web`)

This session reshaped the `cornus web` Overview from a flat multi-screen consolidation
into a project/workload-pivotable dashboard, over a sequence of user requests. Summary
of the end state and the findings worth keeping.

### What the Overview is now
- `<h1>Overview</h1>`, then a **cards row** (Server, Workloads count, Client agent).
  The former "Compose project" and "Terminal sessions" cards were dropped at the
  user's request (the loaded project is reachable via its own section; live sessions
  still drive the foot-of-page table).
- A **grouping toggle** (`.seg` segmented control, `role=tablist`) sits *below the
  cards*, above the sections it governs.
- **By project** (default): one `ProjectSection` per compose project — its workloads
  (with row actions), mounts, port-forwards, Apply, and depends_on graph — plus a
  trailing "Other" bucket for anything not attached to a loaded project.
- **By workload**: one `WorkloadSection` per workload — header (name link, status
  badge, `service · project`, start/stop/restart/delete) then that workload's own
  mounts and port-forwards.
- **Global tail** (both modes): conduit banners, then the terminal-sessions table.

### Component shape (no orphaned files, no route/nav churn beyond the intended)
The four ex-screens became prop-driven presentational pieces, all still imported:
- `Workloads.tsx` — `WorkloadActions` (shared start/stop/restart/delete + busy/error),
  `WorkloadTable` (default, project view), `WorkloadSection` (workload view).
- `Mounts.tsx` — `MountTable({mounts})`.
- `Tunnels.tsx` — `ForwardsView({tunnels, forwards})` (public tunnels + local forwards).
- `Projects.tsx` — `ProjectSection({title, project?, ...})` composing the three tables;
  exports `slug()` for anchors. `project` omitted => the "Other" bucket (no Apply/graph).
- `Overview.tsx` polls config/projects/workloads/mounts/tunnels/terminals once and owns
  the `mode` signal and all the grouping logic.

### Findings / insights
- **The membership joins live only on the client.** Workloads and mounts carry a
  `project` field, but tunnels do not: a public tunnel maps to a project only via its
  `workload` -> workload record -> `project`, and a local forward maps only via its
  `service` key -> a workload with that `service` -> `project`. Overview builds two
  Maps (name->workload, service->project) each render to resolve these. If a workload
  is missing (race, or an orphan), the tunnel/forward correctly falls through to the
  "Other" bucket via `orphan()`. No BFF change was needed — the `/workloads`,
  `/mounts`, `/tunnels`, `/projects` endpoints already carry everything.
- **Routes ≠ endpoints.** Dropping the `/workloads`, `/projects`, `/mounts`, `/tunnels`
  *client routes* (and their nav entries) required zero mock/BFF change — those names
  are still live JSON endpoints; only `index.tsx`/`App.tsx` changed. `/workloads/:name`
  (WorkloadDetail) stays and is still linked from every section.
- **Test collisions from co-location.** Once all sections render on one page, `findByText`
  for values like `shop-web` (workload table + mounts + tunnels) or `live` (agent badge
  + mount status) matches multiple nodes and throws. The durable fix is to assert against
  section containers by id (`#project-shop`, `#workload-shop-web`, `#project-other`) and
  use `findAllByText(...).not.toHaveLength(0)` for intentionally-repeated strings. The
  section-anchor ids (`slug()` / `workload-<name>`) double as both deep-link targets and
  test seams.
- **Toggle state is component-local** (a plain signal, default "project"); deliberately
  not persisted, partly to keep the two Overview render tests order-independent (no
  localStorage leakage between `it`s). Revisit if persistence is wanted.
- **Vite build wipes `pkg/webui/dist/`** including `.gitkeep` on every `npm run build`;
  restore it (`: > pkg/webui/dist/.gitkeep`) so `dist` stays git-clean. Also note
  Bash cwd persists across tool calls — a relative `.gitkeep` path resolved against
  `web/` (not the repo root) silently no-ops; use the repo root or an absolute path.

Verification throughout: `npm run build` (tsc + vite) and `npm test` (final: 75) green.
No commits (none requested).

## Workload lineage tracking (origin) — 2026-07-19

Added first-class **lineage** to every deployment: which project, and the client
host / OS user / launch directory / git repo it was spawned from, plus the
server-verified authenticated subject.

- **Wire type**: `api.Origin` + `api.GitOrigin` in `pkg/api/deploy.go`, hung off
  `DeploySpec.Origin` and `DeployStatus.Origin`. Trust model: client attests
  everything except `Subject`; the server always overwrites `Subject` and
  discards any client value.
- **Client capture**: new `cmd/cornus/internal/lineage` (`Collect(dir)` +
  best-effort `git` shell-outs, bounded by a 3s timeout, never fails a deploy).
  Wired into `DeployCmd.Run` (new `--project/-p` flag) and the compose `up`
  finalization loop (project = `rt.projectName`, dir = `rt.baseDir`; stamped onto
  `rt.plans[n].Spec` so both `runForeground` and `upDetached` inherit it).
- **Server stamp**: `stampOriginSubject(&spec, Identity(r))` in
  `handleDeployCollection` (POST) and `handleDeployAttach` (on `sess.Spec.Spec`,
  since the mount/egress/cred helpers re-read it).
- **Backends** (one shared `deploy.OriginToLabels`/`OriginFromLabels` pair, keys
  `cornus.origin.*`): dockerhost + containerd container labels; bare a new
  `instanceRecord.Origin` field (no daemon store); kubernetes Deployment
  **annotations** (values are not valid k8s label syntax — added a
  `mergeAnnotations` helper). `List`/`Status` read it back into
  `DeployStatus.Origin`.
- **CLI surface**: `deployResult` carries Origin; `summarizeOrigin` folds a terse
  `(origin: proj, user@host, auth:subj, git@abcdef1*)` suffix into the deploy line
  (avoided a hardcoded two-space indent per review feedback — Printer has only
  `Line`).
- **Tests**: lineage collector (git/no-git), origin label round-trip, dockerhost
  emit+read-back, server subject-discard (unauth) + `stampOriginSubject` unit.
- **Docs**: `docs/reference/deploy-spec.md` (Origin/GitOrigin nested types) and
  `docs/architecture/deploy-engine.md` (Workload lineage section).

Gate green: gofmt/build/vet/`go test ./...`. No commits (none requested).
Note: `pkg/registry/containerd_*` was being churned by concurrent work during
this task, causing transient build breaks unrelated to these changes.

## 2026-07-19 — Terminal panes: silent reattach vs. genuine "session ended"

Problem: a pane's `Term` (`web/src/components/Term.tsx`) fired `onExit` on *any*
WebSocket close, so `PaneView` always showed "Session ended. / Reconnect" — even when
the session was still alive server-side (a transient network drop, or a supersede when
the same session is open in another tab). The user should only be asked to
re-establish when the shell genuinely ended.

Key realization: the BFF (`cmd/cornus/internal/webbff/term.go`) already distinguishes
the three teardown causes internally (process exit -> `markDead`, network drop ->
`detach` keeps the session alive, explicit kill -> `shutdown`). The gap was purely on
the wire: the attach handler collapsed all of them into one close frame
(`StatusNormalClosure` / "session detached"), and the client treated every close the
same.

Fix (server): gave `subscriber` a `subCloseReason` (`subEnded` / `subSuperseded` /
`subDetached`), set once under the close-once guard (safe to read after `done` fires).
`attach`/`markDead`/`shutdown`/`detach` each record their reason; the forwarding loop
maps it via `closeFrame` to distinct WS close codes — `4000` ended, `4001` superseded,
normal-closure for a self-detach (RFC 6455 application range 4000-4999). Unit-tested
in `TestSubscriberCloseReasons` (reason per path + the `closeFrame` mapping) using the
existing `fakeExec`/net.Pipe seam — no daemon, no WebSocket.

Fix (client): `Term` now reports `{code, reason, opened}` and an `onOpen`. The policy
lives in a pure, unit-tested helper `web/src/views/terminal/reconnect.ts`
(`paneExitAction`): `4001` -> "elsewhere" (another tab; offer a manual "Reattach
here"), `4000` / never-opened / too-many-retries -> "ended" (prompt Reconnect),
anything else -> "reattach" silently. `PaneView` drives reattach by bumping a
`reconnectKey` nonce folded into the keyed `<Show>` value, remounting `Term` so it
re-attaches and replays the ring. A `failures` counter (kept off-signal so resetting
it never remounts) caps flapping; a 3s stable-connection timer resets it.

Insight worth keeping: `opened=false` is the clean discriminator for "the session is
gone" — a reattach to a killed/reaped session 404s before the socket upgrades, so it
never opens, which naturally bounds the auto-reconnect loop (and covers BFF restart,
since in-memory sessions don't survive it). No poll of `alive` needed on the close
path. This is a lifecycle signal, distinct from the herdr-parity *activity* detector
(`agentdetect.go`, working/idle/blocked) — that classifies a live session; this
classifies how one ended.

Gate green: gofmt clean, `go build ./...`, `go vet ./cmd/...`, `go test
./cmd/cornus/internal/webbff/`; web `npm run build` + `npm test` (80 tests, 5 new).
`pkg/webui/dist/.gitkeep` restored after the vite build. No commits (none requested).

## Containerd host-native becomes a read-WRITE registry (push imports into the store)

Turned the containerd host-native re-export from read-only into a full read-write
view: a `/v2/*` push now imports directly into the host containerd content store +
image service, so a `cornus build` that pushes to the registry lands the image
straight in the store the containerd deploy backend runs from. This is the
symmetric counterpart to the read (re-export) half, and it removes the earlier
containerd hacks (build-worker auto-select + push=false) and the 405-on-push
limitation — for containerd.

### The registry.Store interface

The registry handlers were bound to the concrete `*storage.Backend`. Extracted the
~15-method surface they use into a `registry.Store` interface (blob stat/get/put/
delete, chunked upload New/Get/Abort/Commit over `storage.Upload`, manifest
put/get/delete, tags/repos/referrers). `*storage.Backend` satisfies it
structurally; `Registry.store` and `New` now take `Store`. A nil interface still
means "no store" (docker-daemon pure): routes() assigns the concrete `*storage.
Backend` to the interface only when non-nil so a typed-nil never sneaks in.

### The containerd store

`pkg/registry/containerd_store_linux.go` (replaces the read-only
`containerd_source*`): a `containerdStore` implementing `registry.Store` against a
host containerd's content store + image service, in namespace
`CORNUS_CONTAINERD_NAMESPACE`, dialed lazily. Reads: blobs via `content.ReaderAt`,
manifests via image-name resolution (`resolveImage` + host-stripped
`matchImageName`) then `content.ReadBlob`, tags/repos via `ImageService.List`.
Writes: blobs via `content.Writer` (digest-verified at commit); chunked uploads
stage to temp files then stream into the content store; `PutManifest` writes the
manifest blob **with the containerd GC-ref labels** (`containerd.io/gc.ref.content.
{config,l.N,m.N}`, parsed from the manifest/index) so the config/layers stay
reachable, then records the `repo:tag` image so the deploy resolves it. Referrers
return empty. A `stores` field injects the (content, image) stores so the logic is
unit-testable against a real `content/local` store + an in-memory image store — no
daemon. Non-linux stub returns an error.

### Wiring simplification

host-native containerd now backs the registry with the containerd store directly
(`s.containerdStore`, wired in routes()); no `imageSource`, no nil store, no 405.
Removed: the containerd build-worker auto-select in getEngine and the `push=false`
special-casing in both build handlers — a containerd build just pushes to `/v2/*`
normally and the store imports it. docker-daemon is unchanged (read-only: nil
store + daemonSource, push 405s, builds docker-load via the server).

### Findings

- **`content/local` does not persist labels.** Labels are the metadata store's job
  (the containerd client's `ContentStore()` is metadata-wrapped). So the GC-label
  *persistence* can't be asserted with a bare `local.NewStore` in tests; the label
  *computation* is unit-tested separately (`TestManifestGCLabels`), and real
  persistence is an E2E concern.
- The image name a push creates is `repo:tag` (e.g. `app:v1`) from the `/v2/<repo>`
  path, not the registry-qualified name; the deploy still resolves it because the
  re-export read + the containerd backend's local-image fallback both host-strip.

### Tests / E2E / docs

Unit: `containerd_store_linux_test.go` drives the store against a real
`content/local` store + in-memory image store — blob round-trip, chunked
upload→commit, manifest push→create→get (by tag and digest)→tags/repos, GC-label
computation, name matching. E2E: `registry-host-native-containerd.star` (containerd
target, self-skips elsewhere; build+push→import, catalog lists it read-write,
deploy runs it) added to SCENARIOS_CONTAINERD + EXTRA_CHECK_SCENARIOS. Docs updated
to the per-backend read-write (containerd) vs read-only (dockerhost) split across
ARCHITECTURE.md, OVERVIEW.md, server-env-vars, server-and-registry, build-engine
(EN done; ja+zh via subagent). Full gate green (build/vet/`go test ./...`,
`make e2e-check`, docs build); a `-race` server run was flaky once and clean on
re-run (no data race reported; timing-sensitive server tests). No commit.

Follow-up: live containerd-target E2E run (the scenario is written but needs a real
root+containerd host); the docker-daemon push→load write path (make dockerhost
read-write too) remains a possible follow-up.

### Follow-up: mock dev server wasn't sending the new close codes

Manual test (`npm run dev:mock`) showed Ctrl-D still flickering "[session closed]"
then landing on Reconnect. Cause: the mock (`web/mock/faketerm.ts`,
`web/mock/server.ts`) closed persistent-session sockets with code 1000, and its
"no such session" path *accepted then closed* the socket (so opened=true). So the
client read a real end as a transient drop and reattached ~5× (each remount reprinting
the banner) before the failure cap surfaced "ended". The real BFF was already correct
(4000 on exit; 404 before upgrade). Fixed the mock to match: `MockSession.end`/`attach`
now send `4000`/`4001`, and the upgrade handler rejects an unknown session *before* the
WS upgrade (opened=false). Verified end-to-end with a throwaway `ws` probe: Ctrl-D ->
`{opened:true, code:4000, reason:"ended"}`; stale reattach -> `{opened:false, code:1006}`.
Lesson: when you add a wire signal, the mock BFF is part of the contract — update it
alongside the server or dev/manual testing silently diverges from production.

## Work summary: local image-store re-export → host-native registry (session arc)

This synthesizes an arc that grew across several requests, from "re-export the
local docker registry" to "the registry is a read-write view of the deploy
backend's own image store, on by default." The per-topic entries above have the
detail; this ties them together and collects the durable findings.

### The arc, in order

1. **Docker-daemon re-export (read path).** `/v2/*` serves a miss from the local
   Docker daemon via `docker save` + go-containerregistry `tarball`, reusing the
   existing pull-through `Mirror` seam (generalized to an `imageSource` interface).
2. **The rest of the first cut.** containerd source (native content store),
   build-export-into-the-daemon (`ExporterDocker` → `POST /images/load`), a
   dockerhost skip-pull to close the self-pull loop, and docs.
3. **Drop the CAS.** Made the registry's content store optional (nil): pure
   re-export keeps no CAS, serves reads from the local store, and rejects writes
   `405` (subsuming the "405 on writes" hardening). `off`/`--storage` opt back into
   a CAS.
4. **host-native + default.** Collapsed the two backend-specific values into a
   single `CORNUS_REGISTRY_SOURCE=host-native` that auto-resolves per backend, and
   made it the **default on host backends** (dockerhost + containerd). `off` forces
   the classic CAS; `--storage` gives a union.
5. **containerd read-write.** Turned containerd host-native from read-only into a
   full read-write registry: a `/v2/*` push imports into the containerd content
   store + image service. Required extracting the handlers' backend surface into a
   `registry.Store` interface and implementing a containerd-backed Store; deleted
   the containerd build-worker/push=false hacks (a build just pushes → import).

### Final shape

`CORNUS_REGISTRY_SOURCE`: `host-native` (default on dockerhost/containerd) | `off`
| unset. Per backend: **containerd** = read-write registry over the native content
store (push imports, digests preserved, catalog reflects pushes, no build-worker
config); **dockerhost** = read-only `docker save` view (push 405s, `cornus build`
docker-loads through the server, skip-pull on deploy, docker-save recomputes
digests → pull by tag). No `--storage` → no separate CAS; `off`/`--storage`/mirror/
non-host backend → classic CAS.

### Durable findings (reusable)

1. **go-containerregistry `pkg/v1/daemon` won't compile against the pinned
   `docker/docker v27.4.0-rc.2`** (needs the newer moby client API). Read/write the
   daemon with a hand-rolled REST client + `pkg/v1/tarball` instead.
2. **`docker save` writes uncompressed layers, so digests are recomputed** — a
   docker-daemon re-export can't be pulled by a digest learned elsewhere; pull by
   tag. containerd's native content store preserves digests (digest-addressable),
   which is why containerd got the clean read-write treatment and docker-daemon did
   not.
3. **A "pure" (no-CAS) default breaks in-process `cornus build` pushes.** In-process
   builds push straight to `/v2/*`, and only the server-routed build path can
   docker-load; a no-CAS registry 405s the push. The engine.lock does NOT prevent
   this — the server's build engine is *lazy*, so an in-process build alongside a
   running server doesn't contend. Verified the common paths survive: `cornus
   compose build` / profile builds go through `/.cornus/v1/build/attach`.
4. **The Mirror pull-through was the right seam to generalize** — first into
   `imageSource` (read-only fallback), then the handlers' whole surface into
   `registry.Store` (read-write) so a non-CAS backend can serve /v2/* entirely.
5. **containerd `content/local` does not persist labels** (that's the metadata
   store's job, provided by the client's `ContentStore()`); a bare `local.NewStore`
   in tests can't observe GC-ref labels, so verify label *computation* in a unit
   test and label *persistence* in E2E.
6. **The read/write symmetry insight**: re-export is the read half; a `/v2/*` push
   importing into the local store is the write half. For a digest-addressable store
   (containerd) it maps 1:1 and removes the read-only/405 limitation; for the Docker
   daemon it would need transient staging + `docker load`.

### State / follow-ups

Full gate green throughout (build/vet/`go test ./...`, `make e2e-check`, VitePress
build all locales); docs in EN + ja + zh. Not verified live: the containerd-target
E2E (`registry-host-native-containerd.star`, needs root+containerd) and the
docker-daemon build→load path in anger. Open follow-up: make dockerhost read-write
too (push → `docker load` with staging). No commit made this session; the pkg/wire
and cornus-web changes in the tree are a concurrent agent's, not part of this work.

### Follow-up: an explicit end should close the pane, not prompt

User feedback: prompting "Session ended / Reconnect" after the user *explicitly*
terminated a session (Ctrl-D / `exit`) is exactly the nag to remove — closing the pane
is the desired behavior (tmux-like). Reworked the pane lifecycle in
`web/src/views/terminal/panes.tsx` around a `PaneConn = "live" | "elsewhere" | "lost"`
signal, and made `paneExitAction` (reconnect.ts) the single source of the whole policy,
returning `"close" | "elsewhere" | "lost" | "reattach"`:
- `4000` (session process exited — the explicit end) -> **close the pane**, no prompt
  (`props.ctx.closePane`; `closePane` swaps in a fresh empty picker when it was the last
  pane, else re-tiles).
- `4001` -> "opened in another tab" + Reattach here.
- transient drop (opened, non-4000) -> silent reattach (backoff, capped).
- `opened=false` or cap exceeded -> **lost**: keep the pane, offer Reconnect.

Key correctness point: only a real `4000` closes the pane. An unexpected loss must NOT
close it — e.g. a BFF restart drops every socket at once, and auto-closing would delete
all panes from the persisted layout. "lost" keeps the pane so a reload (which prunes
dead session ids and re-creates) or a manual Reconnect recovers it. `4000` vs.
not-`4000` is a good proxy for explicit-vs-unexpected because the session is the shell
itself, which only exits on `exit`/Ctrl-D/kill — a child process crashing doesn't end
it. Verified the mock emits `4000` on Ctrl-D end-to-end (throwaway `ws` probe). Web
`npm run build` + `npm test` (80 tests) green; `dist/.gitkeep` restored.

## Work summary: terminal panes — detect explicit termination, stop nagging to reconnect

Goal (user): the tiled terminal workspace prompted "Session ended. / Reconnect" on
*every* socket close, including cases the user never intended as an end. Make the host
tell an explicit termination from an incidental drop, and stop asking the user to
re-establish a session they deliberately ended.

### End state — pane reaction on socket close

Driven by one pure function `paneExitAction` (`web/src/views/terminal/reconnect.ts`),
keyed off the WebSocket close code the BFF now sends:

- **`4000` (session process exited — the user typed `exit` / Ctrl-D)** -> **close the
  pane**, no prompt. tmux-like. `closePane` swaps in a fresh empty picker when it was
  the last pane, otherwise re-tiles.
- **`4001` (superseded by a newer attach)** -> "Session opened in another tab." +
  *Reattach here* (a deliberate takeover, not a nag).
- **transient drop** (socket had opened, code not 4000/4001, session still alive) ->
  **silently reattach** with capped backoff, replaying the ring — no user prompt.
- **unexpected loss** (a reattach that never opened, so the session is gone; or the
  flap cap is hit) -> keep the pane, offer *Reconnect*.

### Component shape

- Server `cmd/cornus/internal/webbff/term.go`: `subscriber` gained a `subCloseReason`
  (`subEnded` / `subSuperseded` / `subDetached`), set once under the close-once guard.
  `attach`/`markDead`/`shutdown`/`detach` each record their reason; the attach
  forwarding loop maps it via `closeFrame` to distinct WS close codes (`4000` / `4001`
  / normal-closure). Unit-tested by `TestSubscriberCloseReasons` on the existing
  `fakeExec`/net.Pipe seam — no daemon, no WebSocket.
- Client `web/src/components/Term.tsx`: reports `{code, reason, opened}` and an
  `onOpen`; `opened` is the clean "session is gone" discriminator.
- `web/src/views/terminal/panes.tsx`: `PaneView` tracks a `PaneConn` = `"live" |
  "elsewhere" | "lost"` plus a `reconnectKey` nonce folded into the keyed `<Show>` to
  remount `<Term>` for a reattach; an off-signal `failures` counter (reset by a 3s
  stable-connection timer) caps flapping. `paneExitAction` owns the whole decision;
  the component just dispatches.
- `web/src/views/terminal/reconnect.ts` (+ `reconnect.test.ts`): the policy and its
  close-code constants, unit-tested branch by branch.
- Mock BFF `web/mock/faketerm.ts` + `web/mock/server.ts`: send the same `4000`/`4001`
  and reject an unknown session *before* the WS upgrade, so `dev:mock` matches prod.

### Findings

- **The host already knew.** `term.go` distinguished process-exit / network-drop /
  explicit-kill internally; the only gap was the wire — all three collapsed into one
  `StatusNormalClosure` / "session detached" frame, and `Term` treated every close the
  same. The fix was to *surface* the existing distinction as close codes, not to add
  new detection.
- **`opened=false` is the free "session is gone" signal.** A reattach to a killed or
  reaped session 404s before the WS upgrades, so it never opens — which also bounds the
  auto-reconnect loop and covers BFF restart (in-memory sessions don't survive it). No
  polling of `alive` on the close path.
- **Only a real `4000` may close a pane.** An unexpected loss must NOT: a BFF restart
  drops every pane's socket at once, and auto-closing would delete the whole persisted
  layout. "lost" keeps the pane; a reload (prunes dead ids, re-creates) or Reconnect
  recovers it. `4000`-vs-not is a sound proxy for explicit-vs-unexpected because the
  session is the *shell*, which only exits on `exit`/Ctrl-D/kill — a crashing child
  doesn't end it.
- **The mock BFF is part of the contract.** The first manual test still flickered
  because the dev mock hadn't been updated to the new close codes; dev/manual testing
  silently diverged from prod until it was. When you add a wire signal, update the mock
  in the same change.
- **Lifecycle vs. activity are separate axes.** This ends-detection is distinct from
  the herdr-parity *activity* detector (`agentdetect.go`, working/idle/blocked): that
  classifies a live session; this classifies how one ended.

Gate green throughout: gofmt clean, `go build ./...`, `go vet ./cmd/...`, `go test
./cmd/cornus/internal/webbff/`; web `npm run build` + `npm test` (80 tests, 6 new
across server + web). `pkg/webui/dist/.gitkeep` restored after each vite build. No
commits (none requested).

## Workload lineage in the web BFF — 2026-07-19

Wired the origin lineage through the web BFF and SPA.

- **BFF (Go)**: added `Origin *api.Origin` to the `webWorkload` list DTO
  (`handlers.go`) and populate it from `st.Origin` in `Server.Workloads`
  (`core.go`, both the project-service and trailing `rest` branches). The detail
  DTO (`webWorkloadDetail`) already embeds `*api.DeployStatus`/`*api.DeploySpec`,
  so `origin` was already flowing through the detail JSON with no change.
- **SPA (TS)**: added `Origin`/`GitOrigin` interfaces in `web/src/api.ts`;
  `origin?` on `Workload` and on the inline `WorkloadDetail.status` shape.
  `WorkloadDetail.tsx` gained a **Lineage card** (`.kv` definition list: project,
  deployed-by user@host, directory, authenticated subject, git remote/branch/
  short-commit + dirty badge). `Workloads.tsx` shows a compact `user@host` muted
  hint in each workload section header.
- **Tests/fixtures**: extended `TestWebWorkloadsJoin` (origin on the deployed row,
  none on the uncreated row) and added `TestWebWorkloadDetailOrigin`; added an
  `origin` block to the `shop-web` mock fixture (flows into mock detail via the
  existing handler).

Gate green: gofmt/vet/`go test ./...`; web `npm run build` (tsc+vite) and
`npm test` (80) pass. No commits (none requested).

## Findings: workload lineage — the deploy/BFF join is "loaded-project-only" — 2026-07-19

Session arc: implemented workload lineage (origin) end-to-end (see the two
entries above: the deploy-engine plumbing, then the web BFF/SPA wiring). The
durable findings worth keeping:

### The trust boundary is the reason for a structured Origin
Origin fields are all client-attested except `Subject`, which the server stamps
from the authenticated request identity (`Identity(r)`) and always overwrites — a
client cannot forge who it is, only claim where it deployed from. This is why
`Subject` is a distinct field rather than just another client-supplied label, and
why the stamp happens in BOTH `handleDeployCollection` (POST) and
`handleDeployAttach` (on `sess.Spec.Spec`, since the mount/egress/credential
apply helpers each re-read it).

### Per-backend metadata has three storage shapes, already established by cornus.app
Reused the exact `LabelManaged`/`LabelApp` pattern, so lineage inherited the
same three shapes: container **labels** (dockerhost/containerd), a **record.json
field** (bare — no daemon store), and object **annotations** (kubernetes — path/
URL/subject values are not valid k8s label syntax, the same reason compose labels
ride as annotations there). One shared `deploy.OriginToLabels`/`OriginFromLabels`
pair keeps all four consistent; only bare stores the struct directly.

### The recurring architectural seam: BFF joins the LOCALLY-LOADED compose project
The web BFF is a join layer, not a proxy, and several of its views are bounded by
the single compose project it loaded — not by what the server actually runs:
- `webWorkload.Project` is `s.projectName` (the loaded project), while the
  workload's true project now lives in `origin.Project` (read back from the
  backend). They usually match, but a workload deployed from a different project
  shows the loaded name in the list and its real one only on the detail Lineage
  card.
- **Mount sources are entirely plan-derived.** `Server.Mounts` reads
  `plan.Spec.Mounts[].Source` / `plan.Spec.Volumes[].Name` from the loaded
  project's service plans; `client.List` is consulted ONLY to compute a
  live/running/inactive status, never the source. `api.DeployStatus` never
  carried mount info. Consequence: a workload deployed from elsewhere (no
  matching plan) appears in the list via `client.List` but shows NO mounts.

The seam is the same each time: list/status come from the server, but the
descriptive metadata (project name, mount sources) is read from the local plan.
Lineage now puts project + origin directory on the wire and into `DeployStatus`,
so surfacing non-project workloads' metadata (mounts included) from the
server-side stored spec — rather than the local plan — is a viable, deliberate
follow-up, not a free change.

No commits (none requested). Gates were green throughout:
gofmt/vet/`go test ./...`, plus web `npm run build` + `npm test` (80).

## Follow-up: origin-based project attribution for non-project workloads — 2026-07-19

Acted on the "loaded-project-only" finding above. Did the feasible, in-design
slice; deliberately did NOT do the mount half (see why below).

### Done — attribute non-project workloads to their recorded origin project
`Server.Workloads` (`core.go`) sets a non-loaded workload row's `Project` from
`st.Origin.Project` instead of leaving it empty. A workload deployed from a
project the BFF did not load now shows its real project name in the list and the
detail Lineage card, rather than appearing nameless. It still lands in the
Overview "other" bucket (the BFF only builds sections for projects it actually
loaded), which is correct — the name is now attributed, the structure is not
fabricated. SPA fix: `Workloads.tsx` header showed project only when `service`
was set, so a non-project workload (no service) would have hidden the attributed
project — regated to `service || project`. Test + mock fixture (`legacy-cron`
attributed to project `ops`) added.

### Deferred — mounts for non-project workloads (and WHY, so this isn't retried blindly)
The finding floated surfacing their mounts "from the server-side stored spec".
There is no such store, and it cannot be added cheaply:
- `api.DeployStatus` carries no mounts; only `DeploySpec` does, and the server
  does not persist specs (imperative, stateless-by-design — ARCHITECTURE: "not an
  operator", no revision store).
- Backends do NOT uniformly retain mounts: dockerhost/k8s could report realized
  mounts from live state (container binds / pod volumes), but bare's record and
  containerd's labels do not persist the mount set — so mount read-back would be
  uneven across backends.
- Client-local mounts have their source path rewritten to the server-side 9P
  mountpoint, so a backend-reported "source" would not even be the user's compose
  path.

So mount-for-any-workload is a real feature (add `Mounts` to `DeployStatus`,
populate from each backend's observed state, accept per-backend unevenness), not
a free follow-up. Left for an explicit decision.

Gates green: gofmt/vet/`go test ./...`; web tsc + `npm test` (80) + `npm run
build`. No commits (none requested).

## Work summary: workload lineage — end to end (session arc) — 2026-07-19

One synthesis pointer over the four entries above (deploy-engine plumbing, web
BFF/SPA wiring, the "loaded-project-only" findings, the attribution follow-up).

### What shipped
Every deployment now records its lineage — project + client host/user/dir/git —
and the server-verified authenticated subject. It travels on `DeploySpec.Origin`,
is persisted per backend (labels on dockerhost/containerd, a record field on
bare, annotations on kubernetes) via one shared `OriginToLabels`/`OriginFromLabels`
pair, read back onto `DeployStatus.Origin`, surfaced on the CLI deploy line, and
threaded through the web BFF (`webWorkload.Origin` + the detail's embedded status)
into a SPA Lineage card and a per-workload `user@host` hint. Non-project
workloads are attributed to their recorded `origin.Project`.

### The one finding that outlives this task
The web BFF is a JOIN over the LOCALLY-LOADED compose project, not a mirror of
what the server runs: list/status come from `client.List`, but descriptive
metadata (project name, mount sources) was read from the local plan — so a
workload deployed from elsewhere showed up bare. Lineage fixed the project half
by putting origin on the wire and reading it back. The mount half stays open on
purpose: cornus keeps NO server-side spec store (imperative/stateless by design),
`DeployStatus` carries no mounts, and backends do not uniformly persist them
(dockerhost/k8s could report realized mounts from live state; bare/containerd do
not) — and client-local sources are rewritten to the 9P mountpoint anyway. So
"mounts for any workload" is a real, uneven feature (add `Mounts` to
`DeployStatus`, populate from observed state), not a free follow-up. Recorded so
it is picked up as a decision, not attempted blindly.

### Guardrails learned this session
- Stamp the authenticated subject in BOTH deploy paths (POST + attach); the
  attach helpers re-read `sess.Spec.Spec`, so stamp the source.
- The web `Printer` has only `Line` — no indent primitive; fold sub-detail into
  the line, don't hardcode leading spaces (review feedback).
- `pkg/registry/containerd_*` was under concurrent churn all session; transient
  build breaks there were never from this work — verify by building the touched
  packages in isolation before blaming your own diff.

Gates green throughout: gofmt/vet/`go test ./...`, web tsc + `npm test` (80) +
`npm run build`. No commits (none requested).

---

## Web file explorer: virtual root namespace + cross-mount copy

Reworked the `cornus web` file explorer (`web/src/views/Files.tsx`) so its root is a
virtual directory listing the *mounts* — local roots (by id) and every workload —
instead of defaulting to the first local root behind a Source `<select>`. The dropdown
is gone; descending into a mount browses that source, and "up" from a mount returns to
the mount list. Breadcrumb root is "All".

Made the BFF fs surface (`cmd/cornus/internal/webbff/fs.go`) virtual-namespace aware
rather than doing it frontend-only, because the motivating use is copying files across
mounts. New `source=virtual` addresses everything by one slash path `/<mount>/<subpath>`:

- `resolveVirtual` splits the first segment (a local root id or, failing that, a
  workload name) and delegates to the existing `localXxx`/`containerXxx` code. Every
  `Fs*` method calls `virtualize` at entry; the bare root is list-only.
- `virtualRootListing` returns local roots first, then workloads sorted by name with a
  new `fsEntry.Running *bool` (omitempty) so the UI can grey stopped workloads.
- `FsList` echoes the virtual path back so client breadcrumbs stay virtual.
- Cross-mount rename is refused (400, "copy instead"); `FsCopy` (POST /fs/copy) copies a
  single file between any two mounts via read→write, bounded by maxEditableFileSize,
  landing inside dst when dst is a dir. Covered local→local and local→container.

Mock (`web/src/mock/fs.ts`) made virtual-aware + copy so the dev server and component
tests exercise the same paths. Gates green: gofmt/vet/`go test ./...`, web tsc +
`npm test` (82). No commits (none requested).

### Follow-up: making the BFF mock conform to the new scheme (findings)

After the above, the dev/test BFF mock didn't behave under the new scheme. Two real
findings, both worth remembering:

- **Single-source-of-truth drift becomes visible when a view's primary data changes.**
  `web/src/mock/fs.ts` hardcoded the file-explorer's workload list as `shop-web`
  (running) + `shop-db` (stopped) — which both diverged from and *contradicted* the
  shared `fixtures.workloads` (`shop-db/redis/web` running, `shop-worker/legacy-cron`
  stopped) used by every other view. Harmless in the old dropdown, but under the new
  scheme the virtual root *is* the workload list, so the mismatch reads as "broken."
  Also only `shop-web` had a container tree, so entering a running `shop-redis` 404'd.
  Fix: derive `fsRoots.workloads` from the `workloads` fixture and give every running
  workload a default container tree. Lesson: when a mount/list moves to being a
  screen's main content, re-check that its mock derives from the canonical fixture.

- **Dual-loaded mock files (native Node + tsc-bundler) can't statically import a
  VALUE from a sibling `.ts` without care.** `src/mock/fs.ts` is loaded both by Vite
  (tests) and by the native-Node dev server (`mock/server.ts` imports it). Node ESM
  resolves local *value* imports only with an explicit `.ts` extension; tsc's
  `moduleResolution: "bundler"` rejects `.ts` extensions unless
  `allowImportingTsExtensions` is set. The file had avoided this for years because its
  only local import was `import type … from "../api"` — type-only, erased at runtime,
  so Node never resolved it and tsc was happy extensionless. Adding
  `import { workloads } from "./fixtures"` broke both ways at once (extensionless →
  Node ERR_MODULE_NOT_FOUND; `.ts` → tsc TS5097). Resolution: `import … from
  "./fixtures.ts"` + enable `allowImportingTsExtensions` and `noEmit` in
  `web/tsconfig.json` (tsc stays a type-check-only gate; Vite/esbuild emits). Note:
  the other Node-loaded mock files (`mock/*.ts`) already use `.ts` but are outside
  tsconfig `include`, so they never hit the flag.

Verification gotcha: `npx vite build` emits to `pkg/webui/dist` with `emptyOutDir`,
which deletes the tracked `pkg/webui/dist/.gitkeep` (the emitted assets are gitignored).
Recreate the placeholder after building rather than `git restore` (concurrent-agent
rule). Gates green after the fix: web `tsc` + `vite build` + `npm test` (82), dev mock
server boots and serves the aligned virtual root; Go side unchanged. No commits.

---

## Tiled pane-splitting for the file explorer (reusing the terminal's model)

Gave the `cornus web` file explorer the same tmux-style tiling as the Terminal
workspace: split panes via edge overlays, drag a pane's title bar onto another to swap
(center) or re-tile (edge), drag dividers to resize, ✕ to close, focus tracking, and
localStorage persistence. Each pane is an independent explorer over the virtual
namespace, so you can browse two locations side by side and copy between them.
Frontend-only; no Go changes.

Approach — extract, don't refactor-in-place:
- New generic tiling module `web/src/views/tiling/`:
  - `layout.ts` — the terminal's pure binary-tree model generalized over the pane
    payload (`Pane<P>`, `Node<P>`). `splitPane`/`closePane` take payload factories
    (`makeData`/`freshData`) so each view decides what a new/last pane carries;
    `loadLayout`/`saveLayout` take a storage key + payload validator. Introduced a
    shared `detach` helper (remove leaf, promote sibling) that both `closePane` and
    `movePane` use — cleaner than the terminal's duplicated walks.
  - `panes.tsx` — the view-agnostic chrome (split-tree render, divider resize,
    edge-split overlays, drag-swap/move) with the pane title/body supplied via
    `TileCtx` render-props.
- `web/src/views/files/FilePane.tsx` — the former single-explorer body as a per-pane
  component; its current virtual path lives in the tree node so it survives
  splits/moves/persistence (selection + open editor stay transient, like the
  terminal's runtime state).
- `web/src/views/Files.tsx` — orchestrator mirroring `Terminal.tsx` (createStore +
  reconcile, drag state, edge-split inherits the current path, persistence key
  `cornus.files.layout`).
- CSS reuses every existing `.workspace`/`.split`/`.pane` rule; only added a
  `.file-pane` flex column so the listing/editor fill and scroll inside a pane.

Deliberately left the Terminal untouched (its `terminal/layout.ts` + `panes.tsx` stay
as-is) rather than migrating it onto the generic module — the terminal's `PaneView`
carries rich session/reconnect state and its `layout.test.ts` pins concrete
signatures, so an in-place refactor was higher risk than the task warranted. The
generic module is now the blessed reusable base; a future change could migrate the
Terminal onto it. Also skipped command-palette split bindings for Files (the `%`/`"`/`c`/`x`
prefix commands are tied to the terminal-specific prefix system); mouse parity is complete.

### Findings

- **Generic SolidJS components work in TSX without friction.** `function TreeNode<P>(props: { node: Node<P>; ctx: TileCtx<P> })` used as `<TreeNode node=… ctx=… />`
  type-infers `P` from the props at the call site (and through recursive
  `<TreeNode>` inside `SplitView<P>`), under `moduleResolution: "bundler"` + the solid
  vite plugin. No explicit type args or `unknown`-casts were needed — worth remembering
  before reaching for a non-generic escape hatch next time.
- **Persisted per-view layout needs `localStorage.clear()` in the test `beforeEach`.**
  Three Files component tests failed intermittently on `findByText("project")` not
  because of the tiling code but because the new layout persists to
  `cornus.files.layout`, and the Files `describe` (unlike the Terminal one) didn't
  clear storage between tests — so one test's navigation leaked into the next via a
  restored path. The failure *validated* persistence working; the fix was a one-line
  `beforeEach(() => globalThis.localStorage?.clear())`. Any view that persists to
  localStorage must clear it per-test, mirroring the Terminal/Settings blocks.
- **createResource source must stay truthy for a valid empty-string key.** A file
  pane at the virtual root has `path === ""`; keying `createResource` on the raw string
  would skip the fetch (Solid treats `""`/falsy sources as "not ready"). Keyed it on an
  object `() => ({ path })` instead so the root still lists. (Same trick the pre-tiling
  Files view used with its `loc` object.)
- **jsdom has no layout**, so the `.file-pane` flex CSS is unverified by tests — the
  split/close/navigate/copy behavior is covered, but the visual fit inside a pane would
  need a real browser (`npm run dev:mock` + screenshot) to confirm.

Gates green: web `tsc` clean, `vite build` succeeds, `npm test` 100/100 (16 new
`tiling/layout.test.ts` reducer tests + 2 new Files split/close component tests + the
`beforeEach` fix). Restored `pkg/webui/dist/.gitkeep` after building. Go side unchanged.
No commits (none requested).

## Work summary: `web-screenshot` agent skill — capture a page and embed it in a doc — 2026-07-19

Added a new user-invocable skill `web-screenshot` that captures a web screenshot
(primarily the `cornus web` SPA at `localhost:5000`, or any URL) and embeds it into a
document with the image placed in the right asset location for the target doc tree.
Confirmed scope/backend with the user: Cornus-web-focused, Playwright headless as the
default backend (with `claude-in-chrome` documented as the alternate for
authenticated/interactive pages).

### What shipped

- `.agents/skills/web-screenshot/SKILL.md` — frontmatter (`name`, trigger-rich
  `description`, `user-invocable: true`, `allowed-tools`) mirroring `audit-licenses`,
  plus a step-by-step body: bring the SPA up (`run` skill or `cornus serve --addr :5000`),
  capture, place the asset, embed, optional optimize.
- `.agents/skills/web-screenshot/scripts/shot.mjs` — a small Playwright-API helper:
  `--url/--out`, viewport `--width/--height`, `--scale` (deviceScaleFactor, default 2 for
  retina), `--full-page`, `--selector` (element crop), `--wait` (selector or ms),
  `--color-scheme light|dark`. Navigates with `waitUntil: 'networkidle'`.

### Findings (reusable)

- **`.claude/skills` is a symlink to `.agents/skills`** (`readlink .claude/skills` →
  `../.agents/skills`). So a skill authored under `.agents/skills/<name>/` is
  automatically discoverable via `.claude/skills/<name>/` with no hardlink/copy step —
  the harness picked up `web-screenshot` the moment the files existed. The matching
  inodes seen on `audit-licenses` are the same symlink, not maintained duplicates.
- **Playwright browsers are already cached** (`~/.cache/ms-playwright/chromium-1228`)
  and the npx-provisioned *latest* driver launches them fine — no version mismatch, no
  browser re-download. So the whole skill needs zero install: run via
  `npx --yes -p playwright node scripts/shot.mjs …`.
- **`npx -p playwright node script.mjs` does NOT make `playwright` importable to the
  script** — ESM bare-specifier resolution starts from the script's own directory and
  ignores the npx temp install, so `import { chromium } from 'playwright'` throws
  `ERR_MODULE_NOT_FOUND`. Fix in `shot.mjs`: try the plain import, then fall back to
  `createRequire(import.meta.url).resolve('playwright', { paths })` where `paths` is
  derived from every `PATH` entry ending in `node_modules/.bin` (npx puts its temp
  `.bin` there). This resolves to `~/.npm/_npx/<hash>/node_modules/playwright` and is
  dynamic-imported via `pathToFileURL`. Avoids polluting the tree with a `node_modules`
  (the root `.gitignore` only ignores `/docs/` and `/web/` node_modules, so a skill-local
  install would otherwise be committable).
- **playwright's entry is CommonJS**, so `chromium` may land on `mod.chromium` OR
  `mod.default.chromium` depending on Node's CJS interop. Take `mod.chromium ??
  mod.default?.chromium` — importing the resolved `index.js` gave it on `.default` here
  (a plain `.chromium` was `undefined` and crashed with "Cannot read properties of
  undefined (reading 'launch')").
- **The bundled `npx playwright screenshot` CLI is a good zero-arg fallback** (supports
  `--full-page`, `--viewport-size`, `--color-scheme`, `--device`, `--wait-for-selector`)
  but has **no explicit deviceScaleFactor and no element crop** — `--device "Desktop
  Chrome HiDPI"` is the only way to get 2x there. The custom `shot.mjs` exists precisely
  to cover retina scale + element crop, which docs screenshots want.
- **VitePress asset routing:** `docs/public/` is served at the site root and shared
  across `docs/`, `docs/ja`, `docs/zh` — so a screenshot at
  `docs/public/screenshots/<name>.png` is referenced from any locale as
  `/screenshots/<name>.png`.

### Verification

Smoke-tested against `https://example.com`: full viewport `1280x720 @2x` → valid
2560x1440 PNG (visually confirmed a real rendered page), `--selector h1` → cropped
1728x56 PNG, and the `npx playwright screenshot` fallback → valid 1280x720 PNG.
`node --check shot.mjs` clean. Did not run the Cornus-web path in-session (would need
the SPA served); the command is documented. Temp captures cleaned from
`.agents-workspace/tmp/`. No Go/web source touched; no commits (none requested).

---

## File explorer: actions to the command palette, breadcrumbs into the pane title

Decluttered the tiled file explorer per request: removed the on-screen action toolbar
(New folder / Upload / Rename / Copy / Delete / Download / Refresh) and re-homed
everything. Frontend-only; no Go changes.

- **Contextual palette commands.** Each `FilePane` publishes its actions to a per-pane
  registry via a `register` prop; `Files.tsx` registers ONE command-center provider
  (`registerCommands(fileCommands, true)`) that dispatches to the *focused* pane and
  returns only the commands that apply right now — inside a mount: New folder (bind
  `n`), Upload (`u`); with a row selected: Rename (`e`), Copy (`c`), Download (`w`),
  Delete (`x`); with an unsaved open file: Save (`s`). Providers are `Accessor<Command[]>`
  evaluated lazily by the palette, so the list tracks the current selection with no
  extra reactivity. tmux binds run them straight after the prefix.
- **Refresh onto the pane title.** A compact `⟳` button via the tiling chrome's
  `headerExtra` slot, styled like `.pane-close`.
- **Breadcrumbs into the pane title.** New `PaneCrumbs` renders the virtual-path
  breadcrumb in the header; the body's breadcrumb row is gone. Its links route through
  the pane's own `go()` (added to `PaneActions`) so the unsaved-guard + selection/editor
  reset still apply. To make the title render interactive content, `TileCtx.title`
  became `(pane) => JSX.Element` (was `=> string`) — only Files uses the tiling chrome,
  so this was safe.
- Kept the inline editor's Save/Close bar (the editor sub-view's own controls; removing
  Close would strand mouse users) — flagged to the user as a decision.
- Mock: added a deliberately deep `project/reports/2026/…/generated-manifests` chain
  (helper `deepChain`) purely to eyeball long-breadcrumb overflow.

### Findings

- **A single lazy command provider is the clean way to do multi-instance contextual
  commands.** Rather than each pane registering/unregistering its own commands, one
  provider reads `state.focused` + a plain `Map<paneId, PaneActions>` at eval time.
  The palette re-evaluates providers when shown, so `a.selected()`/`a.atRoot()` reads
  are fresh — no per-pane command churn, no stale entries.
- **`text-overflow: ellipsis` does not work across flex children.** A breadcrumb is
  separate `<a>`/separator elements, so the old string-title ellipsis just hard-clipped
  the right (hiding the *current* folder — the worst end to lose).
- **Left-truncation that stays left-aligned when it fits needs a scroll nudge, not
  CSS alone.** `justify-content: flex-end` keeps the tail visible but wrongly
  right-aligns short paths. The fix: leave it left-aligned, and in JS set
  `el.scrollLeft = el.scrollWidth` (re-run on path change via `createEffect` and on
  pane resize via a guarded `ResizeObserver`). scrollLeft is settable on an
  `overflow:hidden` element — programmatically scrollable, no visible scrollbar — and
  clamps to 0 when everything fits. A left `mask-image` fade, gated behind an
  `.is-clipped` class (`scrollLeft > 0`), gives the left-side "ellipsis" without
  fading the first crumb when the path fits. jsdom reports `scrollWidth === 0` and may
  lack `ResizeObserver`, so both paths are inert/guarded in tests.
- **Testing palette-driven actions without the palette UI:** the isolated view tests
  render only `<Files>` (no App-level prefix handler/overlay), so drive commands
  through the `command-center` singleton directly — `allCommands().find(c => c.id ===
  "files:new-folder").run()`. Rendering Files registers the provider; `cleanup()`
  disposes it between tests.

Residual trade-off (flagged, not yet done): when the path overflows, the "All" root
crumb scrolls out of view; up-nav still works via the listing `..` row / Backspace, but
there's no pinned jump-to-root.

Gates green: web `tsc` clean, `npm test` 101/101 (two toolbar-button tests rewritten to
drive palette commands; added a no-toolbar/refresh-on-title assertion), `vite build`
clean, `dist/.gitkeep` restored. No commits (none requested).

### Follow-up: flush pane listing + long-listing fixture

Two small cosmetic follow-ups on the tiled file explorer (frontend-only):

- **Listing flush to the pane frame.** Now that the breadcrumb (→ title bar) and the
  action toolbar (→ palette) are gone, the `.file-pane` body no longer needs padding.
  Dropped its `padding`/`gap` and removed the `.fs-list` (and `.editor-wrap`)
  `border`/`border-radius` inside a pane, so the pane's own border is the only frame —
  no double border, no inset gap. The occasional status line / editor bar keep their
  own small inset since the body no longer pads, and the editor bar gained a top border
  to separate it from the list now that they abut.
- **Long-listing fixture.** Added `manyEntries()` to `web/src/mock/fs.ts` — a
  `project/many-files` directory of 108 deterministic children (12 subdirs + 96 files
  across a dozen extensions and varied sizes) for eyeballing a long, scrolling listing:
  sticky header, flush frame, and independent per-pane scroll after a split. Sits
  alongside the earlier `deepChain` long-path fixture.

No findings beyond the earlier entry. Gates: web `tsc` clean, `npm test` 101/101,
`vite build` clean, `dist/.gitkeep` restored. Mock change verified by listing
`project/many-files` (108 entries) through the in-memory handler. No commits (none
requested).

---

## Stackable panes (tabs) + new-pane placement prompt (Terminal & Files)

Two related web-workspace features, frontend-only.

### 1. Stacks/tabs, swap→stack, terminal unified onto the shared tiling module

Reworked `web/src/views/tiling/` so a tile is a **stack** (`{ panes: Pane[]; active }`)
rendered as tabs, not a single pane:

- `layout.ts`: leaves became stacks. New/changed ops: `stackPane` (drag-onto-center —
  replaces the old "swap"; moves a pane into another tile as a tab), `activatePane`,
  `addTab` (append an active tab), and tab-aware `closePane`/`movePane` via a shared
  `detachPane` (removes a tab, collapses the stack when it empties, returns a natural
  next-focus). `splitPane` splits at the stack level.
- `panes.tsx`: the chrome renders a tab bar per tile (each tab draggable / click-to-
  activate / ✕-to-close), an optional sub-header row, and a body that keeps **all** panes
  mounted (`display:none` inactive) so background content stays alive. Drop-on-center →
  stack, drop-on-edge → move.
- **Migrated the Terminal onto the shared module** (reversing the earlier "kept separate"
  call, now justified): `Terminal.tsx` on tiling; session/reconnect logic moved verbatim
  into `terminal/TermPane.tsx`. Deleted the now-dead `terminal/layout.ts`,
  `terminal/panes.tsx`, `terminal/layout.test.ts` (after confirming no importers).
- Files gained tabs too; the breadcrumb moved to the sub-header (left of a compact
  refresh button), tab label = current folder / open file name.

### 2. "Where does the new pane go?" placement prompt

- New pref `newPaneSide` (`auto`|`left`|`right`, default `auto`; auto = RTL-aware via
  `getComputedStyle(documentElement).direction`) in `settings.ts` + a Settings "Workspace"
  card. The app had **zero** RTL awareness before — this introduces it fresh.
- `src/pane-placement.ts` singleton: `promptPanePlacement(): Promise<"stack"|"split"|null>`
  + `resolveSplitSide()`. `PanePlacementPrompt.tsx` overlay modeled on `CommandPalette`
  (backdrop cancel, `prevFocus` restore, ←/→/Enter/Esc), mounted once in `App.tsx` (which
  also suppresses its capture-phase prefix listener while the prompt is open).
- Terminal **New pane** (`c`) and Files **open file** now `await promptPanePlacement()` then
  `addTab` (stack) or `splitPane` (split, side from the pref).
- **Files: opening a file is now its own pane** (user chose "replace inline open").
  `FileData` gained `open?`; `FilePane` dropped its inline editor (opening a text file calls
  `openInNewPane`); new `FileEditorPane` edits one file. `PaneActions` became a
  `browse`/`edit` discriminated union so the palette offers the right commands.

### Findings

- **Command `run` is fire-and-forget, so a "prompt then act" flow lives in `run` itself.**
  The command center has no follow-up-choice mechanism. The clean pattern is a
  promise-returning module singleton (`promptPanePlacement()`) that a command's async `run`
  awaits — mirroring how `command-center`/`settings` own reactive state in a `createRoot`.
- **Keep ALL stacked pane bodies mounted (`display:none` inactive), never conditionally
  render only the active tab.** Background terminals must keep their sessions/scrollback;
  file panes keep listing/scroll. `Term`'s `ResizeObserver` refits when a hidden tab
  (0×0) is revealed, so no manual resize nudge is needed. This relies on `createStore` +
  `reconcile` (default `id` key) keeping pane object identity stable across commits so the
  `<For>` over `panes` preserves DOM.
- **A non-reactive registry can't drive live UI state.** The `paneActions` map is read at
  event time for actions (fine), but a Save button's `disabled`/badge need live `dirty()`.
  Solution: put the save bar in the editor pane's own body (it owns `dirty()`), not in the
  orchestrator-rendered sub-header. The palette `files:save` still dispatches via the map.
- **The CodeMirror `Editor` tolerates async-loaded content.** Its `createEffect` replaces
  the doc only when `props.content` (the *saved* text) changes — so loading a file after
  mount updates the view, while typing (which changes a separate `content` signal, not
  `savedContent`) never triggers a clobbering reset. So an editor pane can mount empty and
  fill on load.
- **Testing prompt/command-driven flows without App:** isolated view tests render only the
  screen (no App-level overlays), so drive the singletons directly — `allCommands().find(id).run()`
  for commands and `choosePlacement("stack"|"split")` to resolve the pending prompt promise,
  then `waitFor` the async continuation. jsdom hides `display:none` selects from
  `getByRole`, so assert stack/tab structure via `.stack`/`.tab` counts, not combobox counts,
  for stacked panes.

Gates green: web `tsc` clean, `npm test` 91/91 (added `addTab`/`newPaneSide` unit tests, a
prompt-driven open-file test, two terminal New-pane tests; rewrote the swap→stack tests when
tabs landed), `vite build` clean, `dist/.gitkeep` restored. jsdom has no layout, so the tab
bar / prompt overlay / editor-pane CSS remain visually unverified. No commits (none requested).

---

## Tiled workspace polish: modal service, editor panes, tile-edge overlays, whole-stack drag

A run of UI refinements on the tiled Terminal/Files workspaces, all frontend-only, all
green (tsc + `npm test` + `vite build`, `dist/.gitkeep` restored each time).

### What changed

- **Placement prompt → general modal service.** Generalized the pane-placement dialog
  into one app-wide modal singleton `src/modal.ts` (`promptText` / `confirmModal` /
  `promptChoice`, plus `submitModal`/`dismissModal`) with a single host `ModalHost.tsx`
  mounted once in App.tsx via a **keyed** `<Show>` (remounts per request). Folded the
  placement prompt onto `promptChoice` (deleted `PanePlacementPrompt.tsx`) and replaced
  FilePane's native `prompt()`/`confirm()` (New folder, Rename, Copy, Delete-with-danger).
- **New-pane placement prompt** (prior step, same theme): Terminal "New pane" (`c`) and
  Files "open file" now `await promptPanePlacement()` → stack (`addTab`) or split
  (`splitPane`, side from the RTL-aware `newPaneSide` pref).
- **Editor is its own pane type.** Opening a file (user chose "replace inline open")
  creates a new `FileEditorPane` (`FileData.open`); `FilePane` lost its inline editor.
  `PaneActions` is a `browse`/`edit` union. Then: **no sub-header on editor panes** (the
  filename is already in the tab + save bar; reload moved onto the save bar).
- **Split-edge overlays belong to the TILE, not the body.** Moved the split-zone buttons
  + drop indicator + drag `onDragOver`/`onDrop` out of `.stack-body` up to `.stack`
  (now `position: relative`), so the top-split overlay hugs the tile's top edge (over the
  tab bar) instead of sitting below the tabs/breadcrumb. Top zone thinned to 10px so it
  doesn't swallow tab clicks.
- **Whole-stack drag.** The tab bar is now a drag handle for the entire tile: dragging it
  begins a `"stack"` drag (vs a tab's `"pane"` drag). New ops `moveStack`/`stackStack`
  (+ private `detachStack`) re-tile or merge every tab of a tile at once. The drag now
  carries a kind; both orchestrators dispatch pane-vs-stack on drop.

### Findings

- **One modal service beats bespoke overlays.** `run` is fire-and-forget, so any
  "prompt then act" flow awaits a promise from a module singleton. Generalizing the first
  such dialog (placement) into `promptText/confirmModal/promptChoice` immediately
  retired four native `prompt()/confirm()` calls, and opening any modal auto-cancels the
  previous one (no dangling promises). Native `prompt/confirm` are also un-styleable and
  a pain to test (the old tests stubbed `globalThis.prompt`); the modal is driven in
  tests via `submitModal()`/`choosePlacement()`, no globals.
- **A render-prop that can return null needs the chrome to gate on its result, not its
  presence.** `subHeader` is always defined as a function, so `<Show when={ctx.subHeader}>`
  always rendered an (empty) row. Gating on the *return value*
  `<Show when={ctx.subHeader?.(pane)}>{(c) => <div>{c()}</div>}</Show>` lets a pane opt out
  of the sub-header entirely — used to drop the breadcrumb row on editor panes.
- **Absolute edge overlays must be positioned against the element whose edges you mean.**
  The split/drop overlays lived in `.stack-body` (offset below the tab bar), so "top" was
  the body's top, not the tile's. Positioning context is the fix: lift them to `.stack`
  (`position: relative`); `.stack-body` stays `relative` only for the `display:none`
  stacked panes. Moving the overlays also meant moving the `onDragOver`/`onDrop` there so
  `dropZone()`'s `currentTarget` rect is the tile — the drag tests had to switch from
  `.stack-body` to `.stack` targets to match.
- **Overlay-over-tab-bar is a genuine z/So tradeoff.** A hover-reveal split zone at the
  tile's top edge necessarily overlaps the tab bar; it can't be both visible/hoverable
  AND leave tabs fully clickable in the same pixels. Resolution: keep the zone above the
  bar (visible) but thin (10px) so tab labels (clicked center) stay reachable.
- **Nested HTML5-DnD draggables both fire `dragstart` via bubbling.** Making the tab bar
  draggable for whole-stack drags while tabs stay individually draggable required
  `e.stopPropagation()` in the tab's `dragstart` — otherwise the bar's handler also fires
  and overrides the pane drag with a stack drag. The innermost draggable initiates, but
  the event still bubbles to the outer draggable's handler.
- **No-op reducers should signal "don't touch focus".** `moveStack`/`stackStack` return
  `focus: ""` for missing/same-tile targets (a self-drop of a tile is an easy accidental
  gesture); the orchestrator only re-focuses when `focus` is non-empty, so a no-op drop
  doesn't yank focus to the first pane.

Gate each step: web `tsc` clean, `npm test` 100/100 (added modal, `addTab`, `newPaneSide`,
`moveStack`/`stackStack` unit tests, plus component tests driving the prompt/modal/whole-
tile-drag), `vite build` clean. jsdom has no layout, so tab bar / modal / overlay / editor
CSS stays visually unverified. No commits (none requested).

### Follow-up: placement prompt only for keyboard-induced opens

The stack-vs-split placement prompt on every mouse click to open a file was intrusive.
Gated it: `FilePane.activate(e, fromKeyboard)` threads the trigger — the `<a>` click and
row double-click pass `false`, the list's Enter handler passes `true` — up through
`openFile(name, fromKeyboard)`. `Files.openInNewPane` then does
`fromKeyboard ? await promptPanePlacement() : "stack"`: a mouse open just stacks the file
as a new tab without interrupting; a keyboard open prompts (you can't gesture a direction
from the keyboard). The Terminal already fit this model unchanged — its mouse gesture is
the edge-split overlay (never prompts) and its keyboard "New pane" command prompts.

Finding: **the "should this action prompt?" decision belongs at the call site that knows
the input modality, not in the shared helper.** `promptPanePlacement()` stays modality-
agnostic; the view passes a `fromKeyboard` flag derived from which DOM handler fired
(onClick/onDblClick vs onKeyDown). Testable by asserting `.modal-overlay` is absent after
a click but present after a keydown-Enter.

Gate green: web `tsc` clean, `npm test` 101/101 (file-open test split into a mouse case
asserting no modal + a keyboard case driving the prompt), `vite build` clean,
`dist/.gitkeep` restored. Mouse-open default = stack-as-tab (flagged to the user). No
commits (none requested).

### Follow-up: Files tab labels carry the mount name (parity with Terminal)

Terminal tabs read `workload  command` but Files tabs showed only the leaf (folder
basename / filename), so a Files tab didn't tell you which mount it was in. Brought Files
to parity: `Files.tabTitle` now renders `mount  detail` — the mount (first path segment: a
workload like `shop-web` or a local root like `project`) alongside the detail (the open
file for an editor pane, else the current folder). `All` at the virtual root; the mount
alone at a mount's own root (deduped so it isn't repeated). Mirrors Terminal's
`${workload}  ${cmd}`.

Finding: the mount/workload context for a Files pane is just `path.split("/")[0]` — the
virtual namespace already encodes it as the first segment, so no extra state was needed;
the tab label just wasn't surfacing it. When two tiled views share a chrome, keep their
`tabTitle` shape consistent (`context  detail`) so tabs read the same across pages.

Gate green: web `tsc` clean, `npm test` 102/102 (added a test asserting the mount name
stays in `.tab-label` at a mount root and after descending a folder), `vite build` clean,
`dist/.gitkeep` restored. No commits (none requested).

### Tiny image viewer for the file explorer (+ real-asset mock fixtures)

Added an image viewer to the tiled Files explorer and fixed the mock so it previews the
real Cornus logo.

- **Backend** (`cmd/cornus/internal/webbff/fs_handlers.go`): inline (non-download) reads
  now serve the real image content-type by extension (`imageContentType`:
  png/jpeg/gif/webp/avif/bmp/ico/**svg**) instead of `text/plain`, so a plain `<img>`
  renders any image (SVG in particular needs the right type). Editor/text reads stay
  text/plain; the 10 MB inline cap is fine for a viewer.
- **Frontend**: `isImageName` (raster + svg) routes an image open through the same
  `openFile` path as text; `Files.body` branches `open && isImageName → ImageViewerPane`
  (a minimal `<img src={inline content URL}>` centered/fit, with an onError fallback),
  else editor, else browser. Reuses the existing tab label / no-sub-header / mouse-vs-
  keyboard-open behaviour.
- **Mock**: the second local root (`assets`) now mirrors the repo `assets/` dir —
  `cornus-logo.svg` / `cornus-logo.png` referencing the real files. Added a `Node.asset`
  field + `imageAsset()`; the standalone dev server (`web/mock/server.ts`) reads
  `assets/<name>` off disk and serves the real bytes (a binary PNG can't live in the
  in-memory string tree), via an exported `contentAsset()` resolver + shared `imageMime()`.

### Findings

- **A binary asset can't round-trip through a string-body mock; serve it from disk in
  the Node dev server only.** The mock's `handleFs` is shared by the jsdom test stub
  (can't read files) and the Node dev server (can). So the fixture carries an `asset`
  reference; `handleFs` returns the (empty) string content for tests — jsdom doesn't
  render images anyway, the component test only asserts the `<img>` element + src — while
  `mock/server.ts` intercepts `GET /fs/content`, `readFileSync`s the real repo asset, and
  serves it with the right image type. SVG can also just be embedded as text, but routing
  both through the disk path keeps them consistent and truly "referencing" the asset.
- **`<img>` needs the correct content-type for SVG, but sniffs raster.** Browsers render
  raster in `<img>` regardless of content-type, but SVG requires `image/svg+xml` — so the
  BFF serving `text/plain` inline would have broken SVG previews. Serving the real image
  type fixes it for both real backends and the mock.
- **The Node mock server caches its in-memory tree at startup and does NOT hot-reload.**
  Repeatedly a fixture change (`many-files`, deep path, logos) "didn't show" until
  `npm run dev:mock` was restarted — Vite hot-reloads `src/`, but the fixtures are used by
  the separate `node mock/server.ts` process. Worth stating to the user every time a mock
  fixture changes.
- **Don't scatter demo fixtures; make the mount mean what its name says.** A placeholder
  `assets` root (`img/logo.svg`=`<svg/>`, `style.css`) plus logos dropped into the
  unrelated `project` mount read as misleading. Repointing the `assets` mount at the real
  repo `assets/` (cornus-logo.*) made the namespace self-consistent.

Gates green: Go gofmt/vet/`go test ./...` (new `TestExplorerImageContentType` asserts
image/png for `.png`, text/plain for `.txt`); web `tsc` clean, `npm test` 103/103 (image
viewer opens from the `assets` mount, no Save control), `vite build` clean, `dist/.gitkeep`
restored; dev server verified over HTTP (real PNG bytes, image/png). No commits (none
requested).

### File explorer: keyboard/focus-driven row navigation

The Files explorer rows were focusable (`<a href="#">`) but focus and the pane's
selection were decoupled: focusing a file did not select it, so command-palette actions
(rename/delete/download) never targeted the focused row, and the arrow keys lived on the
`.fs-list` container — moving `selected()` without moving DOM focus, so the focus ring and
selection drifted apart. Net effect: you couldn't actually drive the explorer by the
browser's focus.

Fix (all in `web/src/views/files/FilePane.tsx`):
- Roving tabindex: exactly one row link is in the tab order at a time (`rovingName()` =
  the selected row, else the first). Tab enters the list on that row and Tab leaves it —
  no stepping through every file.
- `onFocus` on each row link sets the selection, so focus *is* selection: the highlight,
  the palette's `selected()` target, and the focus ring all track together.
- Arrow Up/Down now move real DOM focus to the sibling row (`focusRow` → `rowRefs` map),
  which selects it via `onFocus`. Enter/Backspace unchanged (open-with-prompt / go up).
- The `.fs-list` container drops its permanent `tabindex="0"` (the rows are the stops); it
  keeps `tabindex=0` only while empty so Backspace-to-go-up stays reachable. Mouse row
  clicks also `focusRow` so click-then-arrow works.
- CSS: `.fs-list:focus-visible` → `:focus-within` (frame lights when a row inside is
  focused). Row focus ring comes from the global `:focus-visible`, suppressed for mouse
  focus by the browser's modality heuristic — keyboard shows it, clicks don't.

Findings:
- `element.focus()` works on a `tabindex="-1"` element (programmatic focus ignores the
  sign; only Tab-key reachability is gated) — this is what makes roving tabindex + arrow
  navigation compose: focus the -1 sibling, its `onFocus` promotes it to the 0 stop.
- Programmatic `.focus()` after a pointer vs. keyboard event inherits the last input
  modality for `:focus-visible`, so clicks don't draw the ring but arrows do — exactly the
  desired feel, for free.
- Keep the selection write on the row's own click handler (not only via `onFocus`) so it
  doesn't hinge on focus-event delivery under jsdom.

Gates green: web `tsc` clean, `npm test` 104/104 (new "navigates rows with the arrow keys
by moving the browser's focus" asserts focus lands on a row link, selection follows, and
stays single), `vite build` clean, `dist/.gitkeep` restored. No Go changes. No commits
(none requested).


### Ingress conduit configuration layers merge field-by-field

Completed the interrupted client-side conduit refactor. A resolved connection now owns one
immutable profile + environment configuration snapshot, and command options overlay it through
a shared `clientconn.Config` merge path. Precedence is profile < environment < CLI for
`via-server`, conduit mode/SOCKS5 settings, ingress mode, native controller, and emulated CA
certificate/key. Later layers replace only fields they set, so a bare `socks5` selector keeps
listen/suffix/resolve settings from lower layers while an ingress-mode override keeps its
controller and CA settings.

Durable details:
- `clientconfig.Conduit`, `Socks5`, `Ingress`, and `IngressController` now provide deep-copy,
  non-mutating merge helpers. Ordered SOCKS5 resolve rules replace wholesale; tri-state
  `bare-service-names` preserves explicit false. The project-context merge uses the same
  nested implementation, including ingress fields that it previously omitted.
- URL presence is retained separately from persisted zero values: `socks5://.shared` clears an
  inherited session-local listen address, `socks5://` selects an ephemeral session-local bind,
  and `?suffix=` explicitly clears an inherited suffix. Ingress `off` likewise remains distinct
  from an unset higher-precedence layer.
- Environment ingress settings are `CORNUS_INGRESS_CONDUIT`,
  `CORNUS_INGRESS_CONTROLLER`, `CORNUS_INGRESS_EMULATED_CA`, and
  `CORNUS_INGRESS_EMULATED_CA_KEY`. Deploy exposes matching controller and emulated-CA flags.
- The persisted controller `kube-context` remains supported and wins when explicitly set; the
  connection profile cluster context is the fallback.

Regression coverage exercises nested merge immutability, resolve-list replacement, every
profile/environment/CLI ingress field, explicit false/off, empty URL fields, malformed
controllers, and the pre-existing conduit grammar/session-local cases. Gates green: changed-file
`gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...`, `make e2e-check`, and the live
Docker `socks5-ingress.star` scenario.


### Compose up accepts explicit emulated-ingress CA files

Added `--ingress-emulate-ca` and `--ingress-emulate-ca-key` to `cornus compose up`.
Foreground and detached sessions now parse those flags with the same consolidated
`clientconn.Config` override path as `cornus deploy`, so CLI paths override environment/profile
CA values without discarding the selected ingress mode or suffix. Regression coverage starts from
profile CA values and verifies both compose flags win. Gates green: changed-file `gofmt -l`,
`go build ./...`, `go vet ./...`, `go test ./...`, and the live Docker
`socks5-ingress.star` scenario.


### Work summary and findings: ingress conduit configuration merge

Work completed:
- Replaced the separate profile/env/CLI decisions in `clientconn` with one layered,
  immutable configuration model. Precedence is profile < environment < CLI, and nested
  conduit, SOCKS5, ingress-controller, and emulated-CA fields merge independently.
- Added deep-copy merge helpers for `Conduit`, `Socks5`, `Ingress`, and
  `IngressController`, and reused them for project-context overlays.
- Added controller and emulated-CA overrides to `cornus deploy`, plus
  `--ingress-emulate-ca` and `--ingress-emulate-ca-key` to
  `cornus compose up`. Both foreground and detached compose sessions use the same
  validated override path.
- Added regression coverage for full source precedence, non-mutation, ordered resolve-rule
  replacement, malformed controller values, and compose CA flag plumbing.

Findings:
- Zero values are not always equivalent to an unset layer. Explicit `false`, ingress
  `off`, `socks5://.shared`, ephemeral `socks5://`, and `?suffix=` need
  presence metadata so they can clear or override inherited values.
- A bare `socks5` selector should change only the mode. It must preserve inherited
  listen, suffix, resolve rules, ingress controller, and CA material.
- SOCKS5 resolve rules are ordered configuration and therefore replace wholesale instead
  of appending during an overlay. Pointer booleans must be deep-copied to retain explicit
  false without aliasing a source profile.
- The persisted ingress-controller `kube-context` is compatibility-sensitive. Keep it
  when present; otherwise use the connection profile cluster context.
- Resolve environment configuration once with the connection, then add command overrides
  on demand. This prevents different call sites from applying subtly different precedence.

Verification: changed-file `gofmt -l`, `go build ./...`, `go vet ./...`,
`go test ./...`, `make e2e-check`, generated `compose up --help`, and the
live Docker `socks5-ingress.star` scenario all passed. No commit was created.


### Known limitation: background-agent conduit configuration is first-writer per project

A detached `compose up` sends its fully resolved conduit configuration to the background
agent; the agent process environment does not replace that request. The agent can host multiple
conduits on one server connection. Shared conduits key on their proxy configuration, while a
session-local conduit also keys on the project name, so different projects normally get separate
proxies and can coexist. An ephemeral `socks5://` bind is safe; two independent proxies that
pin the same listen address fail when the second listener binds.

The same-project case is not reconciled. `ensureProject` returns an existing project solely by
name before comparing the incoming `ConnSpec` or conduit configuration. Consequently, a later
`up -d` for that project silently keeps the first proxy configuration and banner. New CLI
listen/suffix/rule, ingress-mode, controller, or emulated-CA values do not take effect even though
service specs may still reconcile. Bringing the whole project down before the next `up -d`
releases the old conduit and is the current workaround.

A complete fix should compare the existing connection and conduit identities during
`ensureProject` and safely migrate or recreate the project conduit when they differ, preserving
reference counts and other projects that share the old connection/proxy. The identity/reconcile
logic must also account for ingress configuration: the current `conduitKey` includes mode,
listen, suffix, resolve rules, bare-name behavior, and the session discriminator, but not the
ingress controller/mode/CA fields.
### Conduit configuration identity and detached-session boundaries
Implemented typed runtime conduit and ingress modes; raw profile/environment/CLI strings are parsed and validated only at the clientconn conversion boundary.
Added canonical comparable conduit identity covering normalized SOCKS5 settings, session-local scope, ingress mode/CA, and native ingress controller details. The background agent now uses it for sharing and same-project mismatch warnings.
The agent protocol now uses an explicit WireConduitCfg DTO with conversion helpers. cornus deploy --detach rejects explicit conduit and ingress-conduit options; users can use foreground deploy or cornus port-forward.
Regression coverage includes identity, detached deploy rejection, agent warnings, and compose-conduit-mismatch.star. go test ./..., go vet ./..., and make e2e-check pass.


### Bring-your-own ingress certificates and native Kubernetes Secret materialization

Implemented one certificate-selection model shared by emulated and native ingress.
Connection profiles accept ordered certificate/key rules with an optional exact or
one-label wildcard pattern. Omitted patterns are derived from every DNS SAN; explicit
patterns must be SAN-covered. Exact matches win, then the longest wildcard suffix.

Emulated ingress now selects the served certificate by TLS SNI and retains its generated
or configured CA as the unmatched-host fallback. Native Kubernetes ingress resolves every
explicit concrete host before deploy, groups hosts by selected certificate, derives stable
DNS-safe Secret names without hashing key material, and carries transport-only PEM payloads
to the backend. The backend creates or rotates owned `kubernetes.io/tls` Secrets, refuses
to overwrite foreign Secrets, wires each host group into the Ingress TLS list, removes
obsolete managed Secrets, and relies on owner-reference garbage collection at teardown.

Security and lifecycle findings:
- Native certificate payloads are accepted only over HTTPS, a custom/SSH tunnel dialer, or
  loopback HTTP (including a local Kubernetes port-forward). Remote plaintext HTTP is
  rejected before JSON serialization, and errors/status/logging never include key bytes.
- Managed certificate content is excluded from the background agent's workload fingerprint,
  so rotation updates the Secret without recreating the workload. Host/Secret routing
  changes remain fingerprinted.
- Native managed certificates currently require explicit concrete ingress hosts. Empty
  auto-derived hosts and `@` are rejected client-side because the client cannot safely
  resolve the server's final domain before materialization.
- The durable native materialization works for detached deploys when mode/certificate rules
  come from the selected profile; the existing rejection of explicit conduit flags on
  `cornus deploy -d` remains intact because detached deploys still cannot hold a conduit.

Regression coverage spans SAN derivation, precedence and invalid rules, configuration merge
and agent identity, native grouping/stable naming, CLI-to-runtime path normalization,
transport rejection, Kubernetes create/update/refusal/cleanup and Ingress wiring, plus
fingerprint rotation semantics. E2E coverage upgrades `socks5-ingress-tls.star` to require a
verified handshake with the BYO certificate and extends `deploy-ingress.star` to verify the
actual Secret type/data, Ingress reference, detached CLI path, and garbage collection.

Verification passed: changed-file `gofmt -l`, `go build ./...`, `go vet ./...`,
`go test ./...`, `make e2e-check`, VitePress build, live containerized Docker
`socks5-ingress-tls.star`, and live containerized-kind `deploy-ingress.star`. No commit was
created.


### Review of the managed-ingress-certificate branch: mixed-case host bug, scenario registration, E2E gap

Reviewed the `feat/self-ca-cert-info-propagation` branch (BYO ingress certificates +
native Kubernetes Secret materialization + the layered conduit-config merge) that was
handed over as "not thoroughly E2E tested". The Go gate was already green, and re-running
the feature's scenarios against real backends confirmed the happy paths: live Docker
(`socks5-ingress-tls.star`, `socks5-ingress.star`, `compose-conduit-mismatch.star`) and
live containerized-kind (`deploy-ingress.star`) all pass. A deep read of the security
surface found no exploitable bug: key-material transport gating fails closed
(HTTPS / SSH-tunnel dialer / loopback only), foreign-Secret refusal uses optimistic
concurrency plus a `cornus.ingress-tls=true` + `cornus.app` label check, and key bytes never
reach logs, errors, annotations, or the workload fingerprint.

One real correctness bug found and fixed:

- Mixed-case ingress hosts were wrongly rejected when a managed certificate was attached.
  The client lowercases managed-cert hosts (`ingressemu.normalizeCertificateHost`), but the
  kube backend built its `resolved` host set and the Ingress rule hosts from the raw,
  un-lowercased `in.Hosts` (only `TrimSpace`d), so `resolved[host]` missed and the deploy
  failed with `ingress: managed TLS secret %q refers to host %q which is not an ingress host`.
  DNS is case-insensitive, so this is a wrong rejection. Fix: added `canonicalIngressHost`
  (trim + `ToLower` + strip trailing dot, matching the client normalization) in
  `pkg/deploy/kubernetes/kubernetes.go` and applied it at every host-collection site — the
  Ingress rule hosts, the domain-enforcement suffix check, and both the `resolved` map and
  the managed-cert host lookup now agree on case. Every E2E scenario used lowercase hosts,
  which is exactly why it slipped through.

Also fixed two coverage gaps:

- `compose-conduit-mismatch.star` was not in the Makefile `SCENARIOS` list, so the PR
  CI gate (`make e2e-check`, which iterates the explicit list) never parse-checked it and
  host `make e2e-docker`/`e2e-kube` skipped it (only the main-push `*.star` glob ran it).
  Registered it.
- Added regression coverage for the host bug: unit test
  `TestApplyManagedIngressTLSMixedCaseHost` (mixed-case ingress host + lowercase managed-cert
  host must Apply and canonicalize), and upgraded `deploy-ingress.star` section 4 to spell the
  native-BYO ingress host in mixed case (`App.Native-Cert.Example.Test`) and assert both the
  rule host and TLS host canonicalize to lowercase. The unit test fails against the pre-fix code.

Findings left as-is (documented, not fixed):

- Pure certificate rotation no-ops on a held mount-attach session. `mountFingerprintSpec`
  strips `CertificatePEM`/`PrivateKeyPEM` before hashing (intentional, so rotation does not
  restart the workload), so for a service whose deploy-attach is held by the background agent,
  rotating only the cert leaves the fingerprint unchanged, `DeployAttach` is not re-invoked,
  and the Secret is not rewritten until another spec field changes. The rotate-without-restart
  story holds for `cornus deploy` and non-mount services, not this path.
- Obsolete managed Secrets are deleted (in `applyManagedIngressTLSSecrets`) before the Ingress
  is re-applied, so a redeploy that drops/renames a managed cert has a brief window where the
  still-live old Ingress references a just-deleted Secret. It cannot delete a Secret referenced
  by the new Ingress, so this is a transient blip, not a persistent dangling reference.
- The compose CA CLI flags (`--ingress-emulate-ca[-key]`) have no E2E: the harness
  `compose_up` builtins do not pass arbitrary flags, so E2E would need a new builtin parameter.
  They are unit-tested for wiring, and the emulated-CA runtime path is exercised by
  `socks5-ingress-tls.star`, so this was flagged rather than expanded.
- Latent coupling (hardening note, not a bug): the transport gate treats a non-nil dialer as
  proof of an encrypted tunnel, correct today only because SSH tunneling is the sole dialer
  installer. A future plaintext-remote dialer would let key material through silently; gating
  on an explicit secure-transport flag would be more robust.

Gates green: changed-file `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...`,
`make e2e-check` (now including the registered scenario), live Docker
`socks5-ingress-tls.star` / `socks5-ingress.star` / `compose-conduit-mismatch.star`, and live
containerized-kind `deploy-ingress.star` (with the mixed-case host). No commit was created.

---

## Incus backend (Phase 1 skeleton): `CORNUS_DEPLOY_BACKEND=incus`

Added a fifth deploy backend, `pkg/deploy/incushost`, that deploys OCI images as
Incus application containers (Incus 6.3+ OCI support) via the official Go client
`github.com/lxc/incus/v6/client`. Design plan: `.agents/docs/INCUS_BACKEND_PLAN.md`.

**What landed (Phase 1, all behind `//go:build linux` with a `!linux` stub):**
- `incusConn` seam over `incus.InstanceServer` (real adapter runs `Operation.Wait`;
  a fake `incusConn` drives the unit tests, so `go test ./...` needs no live incusd).
- Fully implemented + unit-tested: `Apply` (recreate-on-Apply), `Start`/`Stop`/
  `Restart` (wrap `deploy.ErrNotFound`), `Delete` (delete-if-exists), `Status`/
  `List`, spec→`InstancesPost` mapping (env→`environment.*`, published ports→proxy
  devices on replica 0 only, labels/origin→`user.cornus.*` config keys, resources→
  `limits.*`, privileged gated by `hostpolicy`), OCI `imageSource` resolution, and
  `Stats` (reuses the shared `hostrun.StreamStats` Docker-JSON encoder).
- Honest stubs returning `errNeedsSpike` (interface satisfied, streaming paths
  deferred to Phase 2 pending a live-incusd spike): `Logs`, `Exec*`, `Attach`,
  `StatPath`/`CopyFrom`/`CopyTo`, `ForwardPort`.
- Selectors wired: `localBackend` (`cmd/cornus/commands.go`) and
  `defaultBackendFactory` (`pkg/server/server.go`); env knobs
  `CORNUS_INCUS_SOCKET`/`_PROJECT`/`_REMOTE`/`_INSECURE_REGISTRIES`. The two
  advertise-mirror selectors (~server.go:1060/1097) only special-case kubernetes,
  so incus correctly falls through — no change needed there.
- E2E harness wired: `IncusTarget` (pkg/e2e/target.go, drives the `incus` CLI for
  setup/teardown), `CapIncus` preflight probe (checks incus + skopeo + umoci +
  daemon reachability), `--target incus` runner enum, `make e2e-incus` +
  `SCENARIOS_INCUS`. Verified `--preflight --target incus` reports the missing
  capability with an actionable hint and the subset parses.

**Dependency constraint (non-obvious):** incus `v6.19.0+` needs `runtime-spec
v1.3.0`, which breaks the pinned `containerd v1.7.24` `oci` package. MVS settles
on **incus v6.18.0 + runtime-spec v1.2.1** (still OCI-capable). Bumping incus
past v6.18.0 requires bumping containerd first.

**Deferred to Phase 2:** the streaming shims (console-log→stdcopy `Logs`,
websocket `Exec`/`Attach`, file-API↔Docker-tar cp, bridge-IP `ForwardPort`),
`MountingBackend`/`EgressBackend` caretaker companion, and the OCI-registry-pull
Phase-0 spike (does incusd pull cornus's localhost/plain-HTTP registry directly,
or must `IncusTarget.PrepareImage` side-load?). Also pending: run `audit-licenses`
for the new Apache-2.0 module and grow `SCENARIOS_INCUS` as data-plane lands.

Gates green: changed-file `gofmt -l`, `go build ./...`, `go vet ./...`,
`go test ./...`, and `cornus-e2e --check` on the incus subset. No commit created.

### Findings (Incus client + OCI mapping — for the Phase 2 implementer)

- **Incus Go client seam.** The `incus.InstanceServer` interface is ~100 methods
  and returns async `Operation` objects. Wrapping it whole in a test fake is
  impractical, so the backend talks through a narrow `incusConn` seam
  (`backend_linux.go`) whose methods return already-waited plain values (the real
  adapter runs `Operation.Wait`). `Exec` is the sole method that must return the
  live `Operation` (it needs the control-channel websocket for TTY resize/signals).
  The fake implements only the seam — the pattern to keep for Phase 2 exec.
- **Not-found detection.** `incusapi.StatusErrorCheck(err, 404)` is the reliable
  test for a missing instance across `GetInstance`/`UpdateInstanceState`/
  `DeleteInstance`; the backend maps it to `deploy.ErrNotFound` (lifecycle) or a
  no-op (delete-if-exists). Do not string-match error text.
- **OCI image source.** An OCI pull is an `InstanceSource{Type:"image",
  Protocol:"oci", Server:<registry-url>, Alias:<repo:tag>}` — the API form of
  `incus remote add <r> <url> --protocol=oci` + `incus launch <r>:<alias>`. Incus
  needs `skopeo` + `umoci` on the DAEMON host's PATH (not the cornus host) to
  flatten the image; the `CapIncus` preflight checks both. Plain-HTTP/localhost
  registries (cornus's own default) are the open Phase-0 question — the mapping
  emits `http://` for localhost / `CORNUS_INCUS_INSECURE_REGISTRIES` hosts, but
  whether incusd honors that for an OCI remote is unverified without a daemon.
- **`name.ParseReference` quirk.** `go-containerregistry`'s parser (WeakValidation)
  rejects some very short refs like `localhost:5000/x:1` while accepting
  `localhost:5000/app:v1` — surfaced only in tests using toy image names. Real
  cornus refs are long enough; noted so a future test author uses realistic refs.
- **Config-key namespace.** Arbitrary instance metadata must live under Incus's
  `user.*` namespace, so cornus's `cornus.managed`/`cornus.app`/`cornus.origin.*`
  labels are stored as `user.cornus.*` config keys (and env as `environment.*`,
  limits as `limits.*`). `instanceApp`/`originFromConfig` invert this on read.
- **Published ports = proxy devices.** Each host-port mapping becomes an Incus
  `proxy` device (`listen tcp:HOSTIP:HOSTPORT` → `connect tcp:127.0.0.1:CPORT`,
  `bind: host`), attached to replica 0 only (cross-backend replica-0-publish
  contract). Phase 2 `ForwardPort` can instead dial the instance's bridge IP from
  `GetInstanceState().Network`.
- **Stats source.** `GetInstanceState` gives total CPU-ns, memory usage/total,
  process count, and per-interface counters — fed straight into
  `hostrun.StatsSample`/`StreamStats` for Docker-JSON parity. Incus reports NO
  host-wide system CPU total, so the docker CLI's CPU% reads low/zero (documented
  limitation); memory/pids/network are exact.
- **Advertise-mirror selectors need no incus case.** `advertisedRegistry`/
  `advertisedIngress` (server.go ~1060/1097) enumerate only `kubernetes` and
  default everything else to no-advertise — incus (a non-advertising host backend)
  falls through correctly, unlike what the plan initially assumed.

### Incus backend Phase 2a: cp (StatPath/CopyFrom/CopyTo)

Replaced the `errNeedsSpike` cp stubs with a real implementation
(`pkg/deploy/incushost/copy_linux.go`) riding the Incus instance file API through
the `incusConn` seam, translating to/from Docker's archive tar.

**What landed:** `StatPath` (GetFile → PathStat; size measured by draining the
body since the file API carries no length), `CopyFrom` (recursive tar pack — a dir
becomes a top entry `base/` with its tree underneath, a file a single `base`
entry, matching the tarcopy naming contract), `CopyTo` (tar unpack → per-entry
CreateFile with dir/file/symlink handling and `--archive` uid/gid via `ownerFor`).
Six new unit tests (StatPath ok + not-found, CopyFrom file, CopyFrom recursive
dir, CopyTo round-trip) drive an FS-modelling extension of the fake `incusConn`.

**Findings:**
- The Incus file API (`GetInstanceFile`) is lossy vs a real stat: the
  `InstanceFileResponse` has Type/Mode/UID/GID/Entries but **no size and no
  symlink target**. StatPath/CopyFrom drain the body to learn a file's size; a
  symlink's target is read as its content. A future refinement is the SFTP client
  (`GetInstanceFileSFTP`, sftp already a transitive dep) which stats cheaply, but
  it is a concrete type (harder to fake) and the file-API path keeps the testable
  seam. Recorded so Phase 2 doesn't silently ship expensive stats.
- Directory recursion is driven by `InstanceFileResponse.Entries` (child base
  names); `CreateInstanceFile` with `Type:"directory"` makes a dir. Integration
  (does incusd populate Entries / accept these creates) is E2E-gated (make
  e2e-incus); the tar translation is fully unit-tested here.
- `deploy.ErrNotFound` mapping needs a real Incus 404 — the fake produces one via
  `incusapi.StatusErrorf(404, ...)` so `isIncusNotFound` (StatusErrorCheck) fires.

Gate green: changed-file `gofmt -l`, `go build ./...`, `go vet`, `go test
./pkg/deploy/incushost/`. No commit created.

### Incus backend Phase 2b: ForwardPort + Logs

Replaced the `errNeedsSpike` stubs for `ForwardPort` (`forwardport_linux.go`) and
`Logs` (`logs_linux.go`).

**ForwardPort:** resolves the instance's global IPv4 from
`GetInstanceState().Network` (`pickIPv4`, skips `lo`, prefers `inet`/`global`),
dials `ip:port`, and splices with `deploy.Bridge` (tcp) or
`wire.BridgeDatagramStream` (udp) — the same shape as the bare backend. Unlike
kubernetes, udp is supported (Incus instances have real routable IPs).

**Logs:** streams the instance console log (OCI PID-1 stdout/stderr) wrapped in
`stdcopy.NewStdWriter(w, stdcopy.Stdout)` framing, satisfying the framing
contract. Added a `ConsoleLog` seam method (`GetInstanceConsoleLog`), replacing
the unused `Logfile`.

Four new unit tests: Logs stdcopy round-trip (demux via `stdcopy.StdCopy`), Logs
rejects a malformed `--since`, `pickIPv4` selection + loopback-only empty case.

**Findings:**
- **Incus console logs are lossy for `docker logs` semantics:** a single raw PTY
  byte stream with no per-line timestamps, no stdout/stderr split, and no tail.
  So `--since`/`--until` are validated with `deploy.ParseSince` (malformed = error,
  per contract) but cannot be honored, and `Follow`/`Tail`/`Timestamps` are
  warned-per-field and ignored. A future faithful `Logs` would need
  `ConsoleInstanceDynamic` (live console websocket) for follow, and there is no
  Incus source for per-line timestamps at all. Recorded so this limitation is not
  mistaken for a bug.
- **`GetInstanceConsoleLog` vs `GetInstanceLogfile`:** the app's output is the
  CONSOLE log, not the daemon logfiles (lxc.log etc.) — the seam uses the former.
- udp port-forward is viable here (routable instance IP), a capability the
  kubernetes backend rejects — worth a scenario when `SCENARIOS_INCUS` grows.

Gate green: changed-file `gofmt -l`, `go build ./...`, `go vet`, `go test
./pkg/deploy/incushost/`. No commit created.

### Incus backend Phase 2c: Exec + Attach

Replaced the exec stubs with a real implementation (`exec_linux.go`) and removed
the now-empty `dataplane_linux.go` (the `errNeedsSpike` sentinel is gone — every
Backend method is implemented).

**Exec:** an in-memory `execRegistry` (server-local; ExecCreate/Start/Inspect/
Resize share one process). ExecCreate registers `{instance, cfg}` → opaque id;
ExecStart runs Incus `ExecInstance` bridging conn⇄process (Stdin=conn; for TTY a
single stream, for non-TTY `stdcopy.NewStdWriter` stdout/stderr framing per the
contract), waits, then reads the exit code from the operation metadata `return`.
ExecResize pushes an `InstanceExecControl{Command:"window-resize"}` to the exec's
captured control websocket. ExecInspect reports running/exit; Pid stays 0 (Incus
does not surface the exec pid to the client — same as kubernetes).

**Attach:** a deliberate, documented not-supported error (not a deferred stub).
Incus exposes an instance CONSOLE (single PTY stream to PID 1) which does not
match docker attach's per-stream stdcopy framing / log-replay semantics for an
OCI app container; callers should use exec.

Six new unit tests: `buildExecPost` mapping (cmd/tty/env incl. malformed-entry
drop/cwd/uid), ExecCreate→Inspect lifecycle, ExecCreate on missing deployment
(ErrNotFound), ExecInspect/Resize unknown-id errors, resize-before-start no-op,
Attach not-supported.

**Findings:**
- **Exec exit code** lives in the finished operation's `Metadata["return"]` as a
  JSON number (decoded as `float64`); `execReturnCode` handles float64/int/int64.
- **Resize needs the live control websocket.** Incus's `InstanceExecArgs.Control`
  is a `func(*websocket.Conn)` callback; the session captures that conn under a
  mutex so ExecResize can `WriteJSON` a window-resize. This pulls
  `github.com/gorilla/websocket` into the DIRECT requires (it was already an incus
  transitive dep) — noted for the license audit.
- **`buildExecPost` maps only a numeric uid** (Incus exec `User` is `uint32`); a
  user NAME cannot be expressed and is warned about, matching the k8s backend's
  numeric-only exec-user limitation.
- The exec STREAM itself (websocket bridging, DataDone, op.Wait) can only be
  verified against a live incusd — E2E-gated via `make e2e-incus` once
  `SCENARIOS_INCUS` includes `exec.star`. The registry/mapping/inspect lifecycle
  is fully unit-tested here.

Gate green: changed-file `gofmt -l`, `go build ./...`, `go vet`, `go test
./pkg/deploy/incushost/`, `go mod tidy`. No commit created.

### Incus backend Phase 2d: docs + scenarios + license audit

Documentation, E2E scenario growth, and the dependency license audit for the
incus backend.

**Docs:** ARCHITECTURE.md updated (four→five backends, incus table row + notes,
`pkg/deploy/incushost` in the package map, CLI backend list). New LTM doc
`.agents/docs/LTM/incus-backend.md` (durable reference: seam, config-key scheme,
data-plane mapping, version pin, E2E, pitfalls, deferred items) with an INDEX.md
source-doc entry.

**Scenarios:** grew `SCENARIOS_INCUS` from the 3-scenario placeholder to
deploy / deploy-stats / lifecycle / exec / deploy-portforward / compose /
compose-exec (the paths implemented in Phase 2). Dropped `lifecycle-restart`
(crash-restart supervision is E2E-uncertain). Added a `boot.autorestart` mapping
from `deploy.RestartPolicy` so the lifecycle restart policy is expressed to Incus.
All seven parse-check clean.

**License audit (`audit-licenses` skill):** the incus subtree added ~23 shipped
Go modules (228→251). Verdict: **0 strong-copyleft (GPL/LGPL/AGPL)**, all
permissive except the weak-copyleft MPL set. Regenerated `THIRD_PARTY_NOTICES.md`
(251 Go modules, 11 npm, 130 license texts). Root `NOTICE` unchanged — no new
notice-bearing license category entered (Apache-2.0/MIT/BSD/MPL already listed).

**Findings:**
- Two new flagged Go items, both resolved: `cyphar/filepath-securejoin v0.6.1` is
  MPL-2.0 (weak-copyleft, compatible — a 7th MPL module beside the 6 HashiCorp
  ones); `rootless-containers/proto/go-proto` read as NO-LICENSE-FILE because it
  is a Go **submodule** whose LICENSE lives at the repo root — its source headers
  carry the full **Apache-2.0** grant (verified by hand). Taught the scanner a
  `KNOWN_MODULE_LICENSES` override (with the source-header evidence in a comment)
  so the scan reproducibly exits 0.
- The npm `caniuse-lite` CC-BY-4.0 flag is **pre-existing and out of scope**:
  browserslist build data under node_modules, not a `web/package.json`
  dependency, so it is not bundled into the embedded SPA. Unchanged by this work
  (which touched only Go deps).
- **incus v6.18.0 pin holds** (v6.19+ → runtime-spec v1.3.0 → breaks the vendored
  containerd oci package). Bumping incus needs a containerd bump first.

Gate green: `gofmt -l`, `go build ./...`, `go vet`, **`go test ./...`** (full
suite), `make e2e-check` (all scenarios parse), and the license scan exits 0. No
commit created.

**Incus backend — all planned+verifiable phases complete.** Phase 1 (backend +
selectors + E2E wiring) and Phase 2a–2d (cp, ForwardPort+Logs, Exec+Attach,
docs+licenses) are done, ~27 unit tests, every `deploy.Backend` method
implemented. **Still open, and genuinely blocked on a live incusd this
environment lacks:** the Phase-0 OCI-registry-pull spike (does incusd pull
cornus's localhost/plain-HTTP registry directly, else side-load in
`IncusTarget.PrepareImage`), the `MountingBackend`/`EgressBackend` caretaker
companion (client-local mounts + client-side egress; mirrors barehost's companion
machinery and needs the companion image + daemon to build/verify), and running
`make e2e-incus` against a real daemon. These are documented in
`.agents/docs/LTM/incus-backend.md` and `INCUS_BACKEND_PLAN.md`; not shipped as
unverified blind code.

### Incus backend: containerized E2E runner integration

Extended the all-in-one E2E runner image (`e2e/container/`) to run the incus
target in-container, mirroring how it already hosts containerd/bare on demand.

**Dockerfile:** installs `incus` + `incus-client` + `skopeo` via apk (Alpine
community) and stages a static `umoci` binary (opencontainers/umoci release) —
Incus's OCI mode needs skopeo + umoci on PATH. Added the incus backend marker
(`incus: connecting to daemon at`) to the compiled-in-backend assertion so a
stale build layer missing incus fails loudly instead of silently falling back to
dockerhost. Header/usage comments updated.

**entrypoint.sh:** `INCUS_SCENARIOS` (mirrors `SCENARIOS_INCUS`), a `start_incus`
that launches `incusd`, waits for the socket, and runs `incus admin init
--minimal` (dir pool + incusbr0 NAT bridge so instances get IPs ForwardPort can
dial), and an `incus` case in the target loop. Robust to a base image without
incusd/skopeo/umoci: reports and fails the target cleanly (never green-washes).
`E2E_TARGETS` doc updated; `bash -n` clean. Run with
`make e2e-container E2E_TARGETS=incus` (the target already threads `E2E_TARGETS`).

**Findings / caveats:**
- **Unverified end-to-end** — cannot build the image or run nested incusd here
  (no Docker host). The real risk is incusd-in-dind: cgroup2 delegation, `/dev`
  access, and the kernel features Incus OCI containers need. It relies on the
  outer `--privileged` run (as kind and the build engine already do).
- **Package availability** — `incus`/`incus-client`/`skopeo` are Alpine community
  packages (enabled on `docker:dind`); if a future base image drops community or
  renames the daemon binary (`incusd`), `start_incus`'s presence check fails the
  target with a clear message rather than hanging.
- `umoci v0.4.7` static binary is pinned via `ARG UMOCI_VERSION` for reproducible
  builds, matching the kind/kubectl/crane fetch pattern.
- No dockerd needed for the incus target (like bare/containerd): `PrepareImage`
  is a no-op — incusd pulls the built image from the co-located cornus registry
  over loopback (localhost is treated as insecure/http by the backend).

Gate: `bash -n e2e/container/entrypoint.sh` clean; Dockerfile build + a real
`make e2e-container E2E_TARGETS=incus` remain to be run on a privileged Docker
host. No commit created.

### Incus backend: containerized runner — tried it on a real Docker host

Actually built the E2E image and ran `make e2e-container E2E_TARGETS=incus` on a
privileged Docker host (the earlier "unverified" caveat is now resolved with real
results). Highly informative.

**Proven:**
- **Nested incusd works under the existing `--privileged` posture** — incusd
  starts, `incus admin init` runs, preflight detects skopeo+umoci + the daemon,
  the cornus incus backend connects and reaches the deploy path. Privilege/nesting
  was never the blocker (the image is already privileged for kind/dockerd/the
  build engine; incus needs no additional privilege — this settles the earlier
  question). All image wiring (apk install, backend-marker assertion, start_incus,
  target dispatch, INCUS_SCENARIOS, preflight) is correct.

**Two functional blockers found by running:**
1. **incus version.** Alpine stable community = incus **6.0 LTS**, which predates
   OCI application-container support (6.3+). Every deploy → `500 ... incus:
   creating instance ...: Unsupported protocol: oci`.
2. **incus ≥ 6.3 into an Alpine base.** Edge has 7.0.1 (has OCI), but edge incus
   on the stable dind base produces an nftables `libnftables`/`libnftnl` symbol
   skew (`Error relocating libnftables.so.1: nftnl_tunnel_* symbol not found`) that
   breaks `nft`, so incus's managed bridge fails: `Failed clearing nftables rules
   ... EOF`. Adding `nftables` then `libnftnl` from edge did not fix it; edge is a
   rolling target so any success would be non-reproducible. Rejected as brittle.

**Shipped resolution (clean + non-breaking):**
- Image builds with STABLE incus 6.0.4 (`apk add incus incus-client`) — no other
  target affected (verified: `E2E_TARGETS=local registry.star` still passes).
- `start_incus` **version-gates**: parses `incusd --version`, and self-skips the
  target (return 2 → `continue`, rc unchanged) with a clear message when < 6.3.
  Verified `make e2e-container E2E_TARGETS=incus` now exits 0 with
  "incus 6.0.4 lacks OCI application-container support (needs >= 6.3); skipping".
- `start_incus` init switched from `--minimal` to a firewall/NAT-disabled bridge
  preseed (our scenarios need no instance outbound; keeps nft surface minimal for
  a future 6.3+ image).
- **Shell gotcha fixed:** under the entrypoint's `set -euo pipefail`,
  `start_incus; s=$?` aborted on the return-2 before the skip check; use
  `s=0; start_incus || s=$?`.

**Follow-up for a runnable containerized incus target:** a Debian/Ubuntu runner
with the official **Zabbly incus apt repo** (current OCI-capable incus, no edge
skew) — a larger base-image change. Until then, `make e2e-incus` against a host
incusd ≥ 6.3 is the way to actually exercise the target. All recorded in
`.agents/docs/LTM/incus-backend.md`.

Gate: `bash -n e2e/container/entrypoint.sh` clean; image builds; incus target
self-skips (rc 0); `local` target unaffected. No Go code changed. No commit.

### Incus backend: E2E runner migrated Alpine → Debian; incus target 7/7 GREEN

Followed through on the containerized runner: migrated `e2e/container/` from the
Alpine `docker:dind` base to `debian:bookworm-slim` so a current OCI-capable
incus installs cleanly, then drove the incus target to a full green run on a real
privileged Docker host. **All 7 SCENARIOS_INCUS scenarios pass**
(deploy/deploy-stats/lifecycle/exec/deploy-portforward/compose/compose-exec,
`E2E_TARGETS=incus` → exit 0), and the docker target still passes (DinD
regression checked).

**Why Debian (the Alpine path is a dead end):** Alpine stable = incus 6.0 LTS
(no OCI); Alpine edge incus 7.x breaks `nft` via a non-reproducible
libnftables/libnftnl symbol skew. Debian has a consistent nftables + in-repo
skopeo/umoci, and Zabbly packages current incus.

**Migration, all verified by running:**
- Runner base → `debian:bookworm-slim`. incus **7.2** from the Zabbly apt repo
  (`pkgs.zabbly.com/incus/stable`). Zabbly's daemon lives at
  `/opt/incus/lib/systemd/incusd` (off PATH via a lib-setup wrapper); exposed as
  `/usr/local/bin/incusd` through a thin exec-wrapper (NOT a symlink — that
  breaks the wrapper's `$0`-relative lib lookup).
- **Docker-in-Docker rebuilt**: docker-ce + containerd.io + runc from Docker's
  Debian apt repo, plus the dind bootstrap scripts COPY-ed from `docker:27-dind`
  (shell; the musl-linked dind binaries are not reused). Fixes found by running:
  `docker-init` is at `/usr/libexec/docker/` on Debian (symlink onto PATH — the
  `dind` script execs it), and `VOLUME /var/lib/docker` must be declared or the
  nested dockerd's overlayfs fails overlay-on-overlay.
- **Registry pull (the Phase-0 answer):** incus's skopeo defaults to HTTPS and
  rejects cornus's plain-HTTP loopback registry. Fixed with a
  `registries.conf.d` insecure entry for `127.0.0.1`/`localhost` — a host-only
  entry matches ANY port (verified skopeo then dials http://), covering the
  harness's dynamic registry port. Public registries keep TLS.

**Backend fix (Go): exec TTY window-resize** (`exec_linux.go`). The client sends
the terminal size around exec-create, racing the control-channel setup, so the
size wasn't applying (`stty size` showed 25x80 vs 24x100). Now the requested size
is remembered on the `execSession` and applied both as the initial
`InstanceExecPost.Width/Height` at ExecStart and via a window-resize once the
control channel connects. New unit test `TestExecResizeBeforeStartStoresSize`.

Gate: `gofmt -l`, `go build ./...`, `go vet ./...`, **`go test ./...`** all green;
`make e2e-container E2E_TARGETS=incus` 7/7; docker target smoke test passes;
`bash -n entrypoint.sh` clean. No commit created.

### Consolidated INCUS_BACKEND_PLAN.md into the canonical docs

Removed the standalone `.agents/docs/INCUS_BACKEND_PLAN.md` (a working design doc,
now fully implemented) and consolidated its content:
- Durable design / mapping / Debian-runner migration → already in
  `.agents/docs/LTM/incus-backend.md` (now the canonical reference; its header
  notes it absorbed the plan).
- Backend catalogue → `ARCHITECTURE.md` (incus table row) and `OVERVIEW.md`
  (updated "four backends" → "five", incus added).
- Still-open follow-ups → `TODO.md` ("Incus deploy backend follow-ups"):
  MountingBackend/EgressBackend companion, RemoteCapable realization, the
  incus-client v6.18.0 pin (bump after containerd), and faithful Logs.

Repointed the live reference in `pkg/e2e/target.go` to the LTM doc; left the
historical JOURNAL entries (append-only) untouched. `go build ./pkg/e2e/` clean.

### Consolidated AUDIT_2026-07.md into TODO.md and retired it

Removed the standalone `.agents/docs/AUDIT_2026-07.md` (the 99 KB finding-by-finding
report of the 2026-07-09 whole-codebase adversarial audit) and folded its live
status into `TODO.md`, mirroring the earlier `GAP_ASSESSMENT.md` retirement pattern
(itself retired 2026-07-09).

Why it consolidated cleanly (the findings that drove the call):
- The audit had **no open work** to migrate: 72 of 73 confirmed findings were
  already fixed (all 14 high + 27 medium, 31 of 32 low; module-wide gate green),
  and the **one** deferred finding (docker `wait` always reports `StatusCode` 0 —
  a cross-package change) was **already** an open item in `TODO.md`. So the new
  TODO section references it rather than duplicating it.
- The reusable method + highlights + outcome already live in the LTM synthesis
  `.agents/docs/LTM/codebase-audit-2026-07.md` (indexed in `LTM/INDEX.md`), so
  retiring the report loses nothing durable — per-finding detail stays recoverable
  via git history plus the landed fixes and their regression tests.

Changes:
- `TODO.md`: new section "Whole-codebase adversarial audit (2026-07-09, retired
  AUDIT_2026-07.md)" — method (40 slices + two-skeptic verification), counts
  (73 confirmed: 14/27/32), resolution, and a pointer to the already-tracked
  docker-`wait` item.
- Deleted `.agents/docs/AUDIT_2026-07.md`.
- Repointed every reference so nothing dangles: `LTM/codebase-audit-2026-07.md`
  (summary, Files list, Pitfalls), `LTM/INDEX.md` (row 34), and
  `LTM/testing-ci-and-quality-synthesis.md` (row 21, Files list, Pitfalls) now
  describe the report as retired-and-consolidated. `grep -rI 'AUDIT_2026-07'`
  returns only retirement notes.

NOT LANDED: no commit created — the deletion + doc edits are staged in the working
tree only (per the no-discretionary-commits rule). Commit on request.

---

## CI-failure sweep: sqliteab go.mod, go-proto license, flaky GC test, ingress-TLS secrets RBAC, E2E scenario-timeout

Investigated a cascade of red CI on the single "Initial." commit (force-pushed /
amended repeatedly, so no diff is available — each fix surfaced the next failure).
Five distinct root causes across four workflows; four fixed and locally verified,
the fifth contained + made self-diagnosing.

### 1. QoS transport / sqliteab tests — nested-module go.mod drift (FIXED, confirmed green)
`cd pkg/wire/sqliteab && go test ...` failed with `go: updates to go.mod needed; to
update it: go mod tidy`. The nested module's indirect deps had drifted from what the
root module's MVS resolves. Fix: `go mod tidy` in `pkg/wire/sqliteab` (bumped
`klauspost/cpuid/v2` v2.2.10 -> v2.3.0 and `u-root/uio` to the newer pseudo-version;
go.sum updated). Validated with `GOFLAGS=-mod=readonly go vet ./...` in the module —
the exact thing CI does. Confirmed green in the next CI run (QoS transport passed).

### 2. Build and push image / go-licenses — go-proto ships no LICENSE file (FIXED)
`go-licenses save ./cmd/cornus` aborted on
`github.com/rootless-containers/proto/go-proto`: its module zip contains no license
FILE (the upstream Apache-2.0 COPYING lives in the parent repo dir, outside the
published `go-proto` submodule), and go-licenses only scans for a license file, not
per-file headers. It is the SOLE such module — verified by scanning all 344 linked
external modules' cache dirs for a license-file match; only go-proto (and the local
`cornus` main module, already `--ignore`d) lacked one. Fix (user chose "ignore +
re-inject"): `--ignore github.com/rootless-containers/proto/go-proto` on both
`save` and `report` in the Dockerfile build stage AND the Makefile
`third-party-licenses` target, then re-inject go-proto's Apache-2.0 LICENSE (canonical
text reused verbatim from the repo's own `LICENSE`, with a go-proto copyright header)
plus its CSV row so the shipped attribution bundle stays complete. Local go-licenses
can't run to completion here (the documented GOROOT-in-modcache toolchain limitation
under GOTOOLCHAIN=auto; `~/.local/go` is 1.24.4, too old for a go 1.26 module), so the
definitive confirmation is CI's clean `golang:1.26` GOROOT.

### 3. Standard gate / `go test` — flaky `TestPeriodicGCSupervisedAcrossPanic` (FIXED)
`pkg/server/gcschedule_test.go:142` intermittently failed ("gcRunning left true after
the panicking run"). The PRODUCTION code is correct — `runGCTick`'s
`defer s.gcRunning.Store(false)` clears the flag across a panic. The TEST raced: it
sampled `gcRunning` the instant `calls` reached 2, but the post-panic run increments
`calls` at the START of `gcRun`, before the defer clears the flag, so it read that
run's own legitimately-in-flight `true`. Reproduced under `-race` within 30 iterations.
Fix: poll `gcRunning` for `false` with a 5s deadline instead of a single sample. A
genuinely stuck flag can't hide — it would also stop `calls` ever reaching 2 (every
post-panic tick skips at the CompareAndSwap), so the existing deadline loop above
already catches that. Green 40x under `-race` after the fix.

### 4. kube E2E / `incluster-portforward.star` — deploy needs `secrets` RBAC it lacks (FIXED)
The in-cluster cornus ServiceAccount (`cornus-incluster`, restricted Role) got
`500 ... list managed ingress TLS secrets: secrets is forbidden`. Root cause:
`applyManagedIngressTLSSecrets` (pkg/deploy/kubernetes/kubernetes.go) runs on EVERY
deploy and its obsolete-secret prune `List` fired even when the spec declares no
ingress TLS — so every deploy demanded `secrets list`, which NONE of the canonical
RBAC grants (`deploy/helm/cornus/templates/rbac.yaml` scopes `secrets get` to one
named caretaker secret; `deploy/k8s/cornus.yaml` and `e2e/scenarios/incluster-cornus.yaml`
grant no secrets at all). Only this scenario surfaced it because only it runs cornus
IN-cluster under the restricted SA; `deploy-ingress.star` (which does use managed TLS)
runs cornus on the HOST with an admin kubeconfig, so its broad rights masked the gap.
This was a NEW regression: kube passed on 2026-07-19, broke on 2026-07-21.

Fix (least privilege, in code — NOT broadening RBAC): return early from
`applyManagedIngressTLSSecrets` when `spec.Ingress == nil || spec.Ingress.TLS == nil`,
before any Secret API call — a workload without ingress TLS must not require namespace
Secret access. The prune List still runs whenever a TLS block is present (so
`TestApplyManagedIngressTLSRotatesAndRemovesObsoleteSecret`, which keeps the TLS block
on redeploy, stays green). Added regression `TestApplyWithoutIngressTLSTouchesNoSecrets`
(asserts zero Secret actions via the fake clientset's action tracker). Deliberately did
NOT grant `secrets` in helm/k8s/fixture — that would give cornus namespace-wide secret
access on every install to support an opt-in feature; no current scenario exercises
in-cluster MANAGED ingress TLS, and if one is added, the grant should be values-gated
(like `caretakerTlsSecret`).

### 5. docker E2E / `devcontainer-vscode.star` 60-min hang — CONTAINED + made self-diagnosing
The docker leg was CANCELLED at the job's `timeout-minutes: 60` on the two latest
commits (previously ~7 min). Extracted the archived step log from the run's log zip
(the API `/logs` text endpoint truncates a cancelled step; the zip's per-step
`.txt` has the real output): the leg hung at the first `devcontainer_cli("up", ...)`
in `devcontainer-vscode.star` (docker-only, so containerd/kube skip it — why only
docker hangs), with ZERO cornus-server activity for the hour.

Read the whole proxy hot path (pkg/dockerproxy: images inspect/create, container
create/start/wait, exec, attach, `bridge`). Every path is BOUNDED — image inspect has
a 30s registry-fetch timeout with synthetic fallback, `startReadyTimeout=180s` caps
start and the pre-start attach park, create is proxy-local, `captureCtx` has a 5s
`WaitDelay`. A deterministic 60-min hang therefore cannot originate in the proxy code;
it is in the `@devcontainers/cli` itself or a backend hop that needs live trace output
to pin down. Could not repro locally (scenario self-skips unless root+9p; this host is
euid 1000 and has no `devcontainer` binary) and there is no diff to read (squashed
history). dockerproxy unit tests pass under `-race` (they use fakes, not the real CLI).

The real, fixable bug found while chasing it: the E2E RUNNER had NO per-scenario
timeout. `cmd/cornus-e2e/main.go` passed the top-level signal context straight to
every `RunFile`, and `captureCtx`'s `WaitDelay` only bites once that context cancels —
which only happens at the 60-min CI SIGTERM. So ANY hung child burns the whole job with
no captured output. Fix: added `--scenario-timeout` (default `10m`, env
`CORNUS_E2E_SCENARIO_TIMEOUT`, `0` disables). Each scenario runs under its own deadline;
a hang fails fast (children killed via the existing WaitDelay), the timeout branch
still logs the error so the killed child's partial output is preserved for diagnosis,
and signal-cancel is distinguished from a genuine per-scenario timeout (the former
stops the run). The containerized runner inherits the 10m default with no
`entrypoint.sh` change (well clear of every healthy scenario — the kube leg runs 119
scenarios in ~26 min, slowest ~1-2 min). Documented the flag in TESTING.md. Verified:
a `sleep(30s)` scenario fails in ~2s under `--scenario-timeout=2s`; a normal scenario
still passes under the default. This does NOT green the docker leg (devcontainer-vscode
still fails) but converts a 60-min silent burn into a ~10-min failure that captures the
CLI output — the concrete next step to root-cause the hang (add `--log-level trace` to
the `up` call and read the last docker API call off the next run).

### Gate
Per changed package: `gofmt -l` clean, `go build ./...`, `go vet`, and focused
`go test` (incl. `-race` for the GC and scenario-timeout paths) all green. Changed
files: `pkg/wire/sqliteab/go.mod` + `go.sum`, `Dockerfile`, `Makefile`,
`pkg/server/gcschedule_test.go`, `pkg/deploy/kubernetes/kubernetes.go`,
`pkg/deploy/kubernetes/ingress_test.go`, `cmd/cornus-e2e/main.go`, `.agents/docs/TESTING.md`.

NOT LANDED: no commit created (per the no-discretionary-commits rule). The
sqliteab/go-proto fixes are already in the pushed `5e9ca14`; the remaining fixes are
working-tree only. Commit on request.

FOLLOW-UP (TODO candidates): (a) root-cause the `devcontainer-vscode.star` hang via
`devcontainer up --log-level trace` in the containerized runner or live docker host;
(b) decide whether in-cluster MANAGED ingress TLS is a supported deploy mode and, if
so, add a values-gated `secrets` grant to the helm chart + an E2E covering it under the
restricted ServiceAccount.

## Docker leg: pin docker-ce to 27.5.1 (unpinned install drifted 27 -> 29) + host-native registry union read-only gate

Follow-on to the CI sweep above. After the kube RBAC fix landed and the E2E
`--scenario-timeout` contained the 60-min devcontainer hang (kube + containerd legs
now green, docker leg down from 60m to ~17m), the docker leg surfaced 4 concurrent
scenario failures. Root-caused two independent problems.

Diagnosis method: compared the failing run (29816335481, docker daemon 29.6.2) against
the last fully-green docker run (29676703391, 2026-07-19, docker daemon 27.5.1). In the
green run `devcontainer-vscode` passed in 3s, `dockerd` and `deploy-sshtunnel-docker`
passed, and `registry-host-native` did not yet exist (green had 116 scenarios; now 119).

Root cause 1 — unpinned Docker Engine drifted a major version. `e2e/container/Dockerfile`
installed `docker-ce docker-ce-cli containerd.io` with NO version pin, so the in-container
dind daemon+CLI silently jumped 27.5.1 -> 29.6.2 when Docker's stable apt repo published
29.x. That single environment change explains all three regressions:
  - `dockerd.star`: `docker compose -f ...` -> "unknown shorthand flag: 'f' in -f" with
    TOP-LEVEL docker usage. The CLI (docker-ce-cli 29) failed to dispatch `compose` to the
    plugin and reparsed `-f` at the top level. Pure CLI behavior, before any proxy call
    (the compose/buildx plugin versions were identical across both runs, so not a plugin
    change).
  - `devcontainer-vscode.star`: the devcontainer CLI's foreground `docker run
    --sig-proxy=false -a STDOUT -a STDERR ... alpine:3.20 -c echo Container started` hung
    (killed at the new 10m scenario timeout). Docker 29's attach/run protocol against the
    cornus proxy no longer completes the handshake the proxy implements for 27.
  - `deploy-sshtunnel-docker.star`: sshd bring-up timed out on its port under docker 29.
  Fix: pin `docker-ce`/`docker-ce-cli` to 27.5.1 via an `ARG DOCKER_VERSION=27.5.1` and an
  `apt-cache madison` exact-version select (prefix match `5:27.5.1-` so 27.5.10 can't
  sneak in; the build fails loudly if 27.5.1 leaves the repo). This reproduces the exact
  green env and makes the docker leg deterministic — an E2E suite must not install "latest
  docker". The dind bootstrap scripts were already pinned (`COPY --from=docker:27-dind`),
  so 27.5.1 is consistent. NOTE: pinning is a stopgap; making the cornus docker proxy
  compatible with docker 28/29 is a separate, larger follow-up (needs the actual
  attach/plugin-dispatch protocol diff).

Root cause 2 — host-native registry accepted writes in "union mode" (real bug, new
scenario, docker-version-independent). `registry-host-native.star` (added 2026-07-19,
after the green run) asserts `PUT /v2/<repo>/manifests/v2` returns 405; it got 201.
`Registry.readOnly()` keyed solely on `store == nil`, but the E2E harness always starts
the server with `--storage`, so `resolveRegistrySource` returns `pure:false` and a real
CAS is opened alongside the daemon re-export source (union mode) -> `store != nil` ->
writes accepted. The daemon re-export is inherently read-only (images are `docker load`ed
into the daemon, never pushed through `/v2/*`), so a co-resident CAS is meaningless and
all write verbs must 405 regardless. Fix (`pkg/registry`): added `Registry.sourceReadOnly`,
set it in `WithDaemonSource`, and OR'd it into `readOnly()` — the one existing choke point
already guarding manifest PUT/DELETE and blob upload/DELETE. `WithMirror` (pull-through)
deliberately leaves it false (it caches fetched content into the CAS, so that store stays
writable); the containerd host-native store is wired via `regStore`/`store`, not
`r.source`, so it is unaffected. Regression test `TestDaemonSourceUnionRejectsWrites`
(`pkg/registry/daemon_source_test.go`) builds a `mem://` store + fake daemon source and
asserts all four write verbs 405; verified it reproduces the E2E (201/202/404 without the
fix) and passes with it — runs in-process, no docker/root/network.

Gate: `gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all green;
Dockerfile RUN block shell + awk syntax validated (`sh -n`, madison prefix-match dry run).
The docker leg itself can only be confirmed by a CI run (needs root+dind); the pin
provably reproduces the green environment, and the registry fix is unit-verified.

Changed files: `e2e/container/Dockerfile`, `pkg/registry/registry.go`,
`pkg/registry/daemon_source.go`, `pkg/registry/daemon_source_test.go`.

NOT LANDED: no commit created (per the no-discretionary-commits rule); working-tree only.
Commit + push to confirm the docker leg goes green. FOLLOW-UP: docker 28/29 proxy
compatibility so the pin can eventually be lifted.

## 2026-07-21 — Compose `up`/`down --remove-orphans` (orphaned-workload handling)

Gap: `docker compose` detects orphaned containers (services removed/renamed out of
the Compose file) and removes them with `up`/`down --remove-orphans`; Cornus's
compose client had neither the detection nor the flag. Its `down` only deleted the
currently-defined services, so a workload for a deleted/renamed service kept running
indefinitely. Item was catalogued in TODO.md ("Lower-severity missing flags").

Key enabler already in place: every `compose up` stamps each workload's
`api.DeploySpec.Origin.Project` with the project name (`UpCmd.Run`, via
`lineage.Collect` + `projectOrigin.Project = rt.projectName`), and every backend
reads Origin back into `DeployStatus` (dockerhost/containerd via
`deploy.OriginFromLabels`, etc.). So orphans can be found by lineage, not by the
brittle `projectName + "-"` name prefix (which would misfire across projects like
`foo` vs `foo-bar`).

Design (`cmd/cornus/internal/composecli`):
- `findOrphans(list, projectName, known)` — pure predicate: a deployment is an
  orphan iff `Origin.Project == projectName` AND its resource name is not in
  `known`. `known` is the resource-name set of EVERY service in the FULL project
  (`rt.project.Project().PlanForStatus` — all profiles), so a profile-disabled
  service is NOT mistaken for an orphan. Nil-Origin or other-project deployments are
  never claimed (cornus only removes what it can prove it owns). Sorted result.
- `runtime.knownResources()` (compose.go) builds that set from the unfiltered project.
- `removeOrphans(ctx, cl, project, known, d, wait)` — lists, deletes each orphan, and
  when `wait` watches the teardown drain via `reportTeardown` (labelled by resource
  name; an orphan has no compose service name). List failure returns an error; a
  per-orphan delete failure warns and continues; honors Ctrl-C mid-wait.
- `warnOrphans(...)` — up's advisory when `--remove-orphans` is absent; best-effort
  (List failure swallowed so it never blocks up).

Wiring: `DownCmd.RemoveOrphans` runs `removeOrphans` after the service-removal loop
and BEFORE the `--volumes` sweep (so an orphan holding a named volume is gone first),
honoring `--wait`. `UpCmd.RemoveOrphans` runs before the deploy dispatch (so a reused
service name is free) on a bounded signal-cancellable context; without the flag `up`
calls `warnOrphans`. Project-wide even for a partial `up SERVICE` — matches docker;
safe because an orphan is by definition absent from the file, so no in-file service is
ever caught.

Tests: `orphans_test.go` — `findOrphans` predicate (nil-origin / other-project /
known-service exclusions, sort), `removeOrphans` (wait teardown, no-wait event,
no-op, List-failure error), `warnOrphans` (warns/silent/swallow), and flag parsing
for both subcommands. Fake `fakeOrphanClient` (List/Delete/Status), no daemon.

Docs: `docs/cli/compose.md` up + down flag tables + an orphan-detection-by-lineage note.

Gate: `gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all green.
Changed files: `cmd/cornus/internal/composecli/{commands.go,compose.go,orphans_test.go}`,
`docs/cli/compose.md`, `.agents/docs/TODO.md`. No commit (working-tree only).

## 2026-07-21 — tackle-todos sweep (catalog cache, yamux MPL headers, supervisor restart-tree, doc fix)

Source scan found no real `// TODO`/`// FIXME` comments in non-test Go (the grep hits are
`toDocument`/`XxxFromDocument` false positives); all actionable work lives in `TODO.md`. Triaged
the open items and dispatched four parallel agents with disjoint file sets, deliberately avoiding
`cmd/cornus/internal/composecli` (uncommitted `--remove-orphans` work in the tree). Landed:

1. **`_catalog` catalog-list cache** (`pkg/storage/cas.go`). `Backend.Repos` (backs `_catalog`) no
   longer re-walks the whole `repos/` tree per call: sorted list cached behind `catalogMu` with a 5s
   TTL + explicit invalidation on repo-mutating marker writes (`PutManifest` Put / `DeleteManifest`
   Delete; GC/DeleteBlob touch only blobs, so they don't invalidate). The walk runs without holding
   the lock (a slow S3 traversal never serializes unrelated requests); a generation counter drops a
   stale store-after-walk so a freshly pushed repo shows up immediately, not only after TTL. Returned
   slices are copies. Pagination (registry layer) unchanged. Tests: `TestCatalogCacheServesWithoutRewalk`
   (List-counting store seam), `…InvalidatedByPush`/`ByDelete`, `…TTLExpiry`, `…PaginationUnaffected`.

2. **MPL Exhibit A headers on `third_party/yamux/*.go`** — the vendored MPL-2.0 yamux fork carried no
   license header on any of its 14 `.go` files; added the canonical Exhibit A notice to all of them
   (no build constraints affected). This is part (a) of the license TODO; shipping
   `THIRD_PARTY_NOTICES.md` beside release binaries and wiring a CI license scanner remain open (item
   left `[~]`). The submodule (separate module, so gated in-dir) builds/vets/tests green.

3. **`pkg/supervisor` restart-tree for tunnelManager + hub loops** (`pkg/server`). Three loops brought
   under supervision mirroring the GC/caretaker adoptions: `tunnelManager.serve` accept loop under a
   manager-private supervisor with `RemoveOnExit` (panic-recovery only — per-tunnel resource-bounded;
   private supervisor avoids a shutdown deadlock, since these loops only unblock when their backend is
   closed in `closeAll`, after `s.sup.Wait()`; the old `tunnelSession.done` channel replaced by the
   supervisor `Token`); the six hub/mount/credential/egress/agent-relay stream handlers in
   `caretaker_attach.go` under a per-connection supervisor with `RemoveOnExit` (each stream consumed
   once — restart would desync the decoder; uniform panic isolation); the `catalogNotifier` poll loop
   under a per-notifier supervisor with `Restart` (process-lifetime within a subscription window, no
   per-iteration stream state — panic relaunches so watchers aren't stranded). server.go:90 comment
   updated. Tests: `TestTunnelAcceptLoopSupervisedAcrossPanic`, `TestCatalogNotifierSupervisedAcrossPanic`.

4. **Stale `.sig`/`.pem` doc fix** — only `LTM/shipping-and-install-synthesis.md` still described the
   old cosign `.sig`/`.pem` asset pair as current; corrected to the cosign v3 single `SHA256SUMS.bundle`,
   matching `release-and-packaging.md`. `ci-github-actions.md` (historical references) left untouched.

Also verified the README "registry CAS as shared substrate" TODO was already fixed (README.md:29 says
"The subsystems integrate over OCI HTTP") and closed it as a no-op.

Gate: main module `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` all green;
`third_party/yamux` submodule green in-dir. No commit (working-tree only). `TODO.md` updated (four
items `[x]`, the yamux item `[~]`).

## 2026-07-21 — Grant `secrets` RBAC for managed ingress TLS (helm + k8s manifest)

User hit, on a real in-cluster deploy of a compose config with ingress settings:
`deploy frontend: frontend: list managed ingress TLS secrets: secrets is forbidden:
User "system:serviceaccount:default:cornus-cornus" cannot list resource "secrets"
in API group "" in the namespace "default"`.

Root cause is the same code path documented in the earlier CI-failure sweep entry
("### 4. kube E2E / incluster-portforward.star"): `applyManagedIngressTLSSecrets`
(`pkg/deploy/kubernetes/kubernetes.go:1120`) manages the backing `kubernetes.io/tls`
Secrets for a workload whose ingress TLS block carries inline (managed) certificates.
It does Get/Create/Update on each cert Secret and a label-selector `List` +
per-item `Delete` to prune obsolete ones. The canonical RBAC granted none of this:
`deploy/helm/cornus/templates/rbac.yaml` scoped `secrets get` to the single pinned
`caretakerTlsSecret`; `deploy/k8s/cornus.yaml` granted no `secrets` at all. Any deploy
that ACTUALLY uses managed ingress TLS in-cluster therefore fails at the List.

Note the earlier fix (`spec.Ingress == nil || spec.Ingress.TLS == nil` early return)
only removed the demand for `secrets` from workloads WITHOUT ingress TLS. It did not,
and could not, help a workload that genuinely declares managed ingress TLS — that path
still needs the grant. This user's config exercises exactly that path.

### Fix
Added a namespace `secrets` grant (`get,list,watch,create,update,patch,delete`) to the
Role in both `deploy/helm/cornus/templates/rbac.yaml` (unconditional, inside the existing
`{{- if .Values.rbac.create }}`) and `deploy/k8s/cornus.yaml`. Kept the pinned caretaker
`get` rule (now subsumed) so that feature stays self-documenting. Verified with
`helm template` that the broad rule renders, and that both the broad rule and the pinned
caretaker rule render together when `--set caretakerTlsSecret=...`.

### DIVERGENCE from the prior recommendation — decision to revisit
The earlier CI-sweep entry (item 4) deliberately did NOT grant `secrets` and explicitly
recommended that if an in-cluster managed-ingress-TLS scenario ever arose, the grant
should be **values-gated** (like `caretakerTlsSecret`), to avoid giving cornus
namespace-wide secret access on every install for an opt-in feature. I granted it
**unconditionally** instead. Rationale and the honest trade-off:

- READ verbs (get/list) are not a real escalation: the Role already has pod `create` +
  `pods/exec`, so the server can already read any Secret in its namespace by mounting it.
- WRITE verbs (create/update/delete) ARE a genuinely new capability. Mitigation: the
  write path only ever touches Cornus-labelled Secrets and refuses to overwrite a Secret
  it does not own (`managedIngressTLSLabel` + app-label guard in kubernetes.go:1168).
- Chose unconditional for zero-config UX: the user asked for the chart to "just work,"
  and a gated grant would make them rediscover the same forbidden error before flipping
  a flag.

This is a legitimate least-privilege-vs-UX call and is the open decision here. If the
team prefers least privilege, the alternative is a values-gated version, e.g. add
`ingress.manageTLSSecrets: false` and wrap the rule in `{{- if .Values.ingress.manageTLSSecrets }}`,
mirroring the `caretakerTlsSecret` pattern. Flagged to the user in-conversation.

### Files
- `deploy/helm/cornus/templates/rbac.yaml` — new `secrets` rule + comment on the trade-off.
- `deploy/k8s/cornus.yaml` — matching `secrets` rule.
No code, no tests changed (RBAC/manifest only); no commit (working tree only).

## 2026-07-21 — Follow-up: ingress-TLS `secrets` grant is now values-gated (resolves the open decision above)

Resolved the open decision from the entry above per user direction: switched the Helm
chart from an unconditional `secrets` grant to a **values-gated** one, honoring the
least-privilege recommendation originally made in the CI-sweep entry (item 4).

- `deploy/helm/cornus/values.yaml` — new `ingress.manageTLSSecrets: false` (default off),
  documented: required only for workloads with inline (managed) ingress TLS certificates;
  cert-manager-issued TLS (`ingress.tlsIssuer`) needs no grant.
- `deploy/helm/cornus/templates/rbac.yaml` — the `secrets` rule is now wrapped in
  `{{- if .Values.ingress.manageTLSSecrets }}`, mirroring the `caretakerTlsSecret` pattern.
  Its comment notes the read-vs-write escalation nuance (read verbs add nothing over the
  existing pod create/exec grants; write verbs are new but label-guarded).
- `deploy/k8s/cornus.yaml` — the static all-in-one manifest keeps the grant unconditional
  (it has no values mechanism and is the batteries-included "just works" manifest).

User-facing consequence: an in-cluster deploy with managed ingress TLS now requires
`--set ingress.manageTLSSecrets=true` (or the values equivalent) at install/upgrade;
without it the deploy fails with the same `secrets is forbidden` error, which the value's
doc comment names explicitly.

Verified: `helm template` renders NO broad secrets rule by default, the broad rule when
`ingress.manageTLSSecrets=true`, and both the broad and pinned-caretaker rules when that
plus `caretakerTlsSecret` are set. `helm lint` clean (only the pre-existing icon INFO).
No commit (working tree only).

## 2026-07-21 — tackle-todos decision items (build defaults; non-loopback conduit refusal)

Resolved the two "decide" items flagged during the tackle-todos sweep.

**`/.cornus/v1/build` insecure/push defaults → document-as-intended (no flip).** Both defaults are
load-bearing for the build→push→deploy inner loop: `push` is the point of a build, and
`localPushTarget` redirects the target to Cornus's co-located registry which serves plain HTTP on
loopback — so `insecure=false` by default would break pushing to Cornus's own registry with no
benefit. The endpoint is gated by the `build` API policy + authenticator, so this is the
registry-transport default for AUTHENTICATED builds (target a remote TLS registry with an explicit
`insecure=false`), not an unauthenticated exposure. Documented the rationale in the `handleBuild` doc
comment (`pkg/server/build.go`); no behavior change.

**Non-loopback conduit bind refused early at the config layer.** The session conduit has no
non-loopback opt-in of its own (only `cornus socks5 --allow-non-loopback` sets
`Socks5AllowNonLoopback`), so a non-loopback conduit bind is always a misconfiguration; previously it
only surfaced at `socks5.Start`'s post-bind refusal. Added exported `socks5.LooksNonLoopback` (a cheap
string classifier: literal wildcard `:port`/`0.0.0.0`/`::`/`*` or literal non-loopback IP → true;
empty and hostnames deferred to Start's authoritative post-bind `loopbackAddr` check, which still
catches a poisoned `localhost`). Two guards reuse it: (a) `clientconduit.Start` refuses before binding
when `!Socks5AllowNonLoopback`, covering every conduit caller including a hand-edited profile
`Socks5.Listen`, while the opt-in flows through unchanged; (b) `ParseConduitSpec` rejects a
non-loopback host in a `socks5://` selector at parse time (the cited
`CORNUS_CONDUIT=socks5://0.0.0.0:1080` case) for the earliest error. Updated one pre-existing parse
test that had used `socks5://:1085` (a wildcard) merely to exercise empty-suffix parsing → loopback
host. Tests: `TestLooksNonLoopback`, `TestStartRejectsNonLoopbackConduit`/`…AllowsNonLoopbackWithOptIn`,
extended `TestParseConduitSpec`.

Files: `pkg/server/build.go` (doc only), `pkg/socks5/socks5.go` + `local_test.go`,
`pkg/clientconduit/clientconduit.go` + `local_test.go`,
`cmd/cornus/internal/clientconn/clientconn.go` + `clientconn_test.go`. Deliberately did NOT touch
`cmd/cornus/internal/composecli` (uncommitted `--remove-orphans` work), even though it is a
`clientconduit.Start` caller — the guard lives inside `clientconduit.Start`, so that path is covered
without editing it. Gate: main module `gofmt -l` clean, `go build ./...`, `go vet ./...`,
`go test ./...` all green. No commit (working-tree only).

## 2026-07-21 — Fix flaky TestPeriodicGCSupervisedAcrossPanic (load-dependent test artifact)

The long-standing flake (`gcRunning left true after the panicking run`, ~3/5 in the reporter's loaded
runs) was a TEST-HARNESS artifact, not a product bug. The test drove the supervised GC loop with a
free-running real 2ms ticker and polled `gcRunning`. Under CPU contention (the full suite running in
parallel) the loop fell perpetually behind on back-to-back buffered ticks, so `gcRunning` was true
nearly continuously and the 1ms poll never sampled a false window within the deadline. `runGCTick`'s
`defer s.gcRunning.Store(false)` always clears the flag (even on the panic path), so the production
guard was never actually stuck — the assertion was just racing a moving target.

Fix: added a test-only injectable tick source `Server.gcTicks` (`startPeriodicGC` uses it when set,
else the real interval ticker; captured once so the loop re-reads the same source on each supervisor
restart — production byte-identical). Rewrote the test to drive the loop with an unbuffered channel:
tick 1 panics (real `s.sup`/`supervisor.Restart` recovers + relaunches), tick 2 blocks until the
restarted loop is receiving again (natural synchronization on the restart backoff, no wall-clock
guessing) and takes the normal path signalling a `ran` channel, then — with no further ticks in
flight to re-set it — `gcRunning` deterministically settles to false. The test still exercises the
genuine supervisor panic→restart integration; it is just no longer at the mercy of a free-running
ticker vs. scheduler timing.

Verified: 50x under `-race`, 30x under full `nproc` CPU saturation (the original flake condition),
and the whole `pkg/server` package `-race`-clean. Files: `pkg/server/server.go` (gcTicks field),
`pkg/server/gcschedule.go` (startPeriodicGC tick-source seam), `pkg/server/gcschedule_test.go`. Full
module gate green. No commit (working-tree only).

## 2026-07-21 — Fix transient `compose up` deploy diagnostics leaking through slog instead of the user reporter

`cornus compose up` intermittently printed alarming "deploy error" lines for problems that turned out
to be fine. Root cause: while a mounted / egress-relay service holds a deploy-attach session, the
server *deliberately* streams non-terminal readiness diagnostics — an image still pulling, a brief
`CrashLoopBackOff`, a one-off `Status()` poll error that later succeeds — as `deploywire.Event{Err: …}`
frames WITHOUT `Done: true` (`awaitReady.stream`, `pkg/server/deploy_attach.go:187`). These resolve on
their own when the workload comes up. Terminal failures, by contrast, DO set `Done: true`
(`deploy_attach.go:113/122/138`) and additionally return as the session's error to a pre-ready waiter
(`serve.go:83` → `attachsession` `WaitReady`).

The client-side reconcile engine's event hook treated every error frame identically:
`log.ErrorContext(opCtx, "deploy error", …)` for any `e.Err != ""` (`clientagent/controllers.go`). So
transient, self-healing diagnostics were (a) logged as slog ERRORS and (b) sent to slog rather than the
`cliout.Driver` that the rest of `compose up` uses — bypassing the progress region. Hence the leak.

Fix (the engine `clientagent.Project` is UI-agnostic and shared with the background agent, so a reporter
was plumbed through rather than importing cliout):
- `project.go`: added `DeployNotice{Service, Message, Terminal}` (Terminal derived from `Event.Done`)
  and a `WithDeployReporter(DeployReporter)` functional option; `NewProject` is now variadic.
- `controllers.go`: the hook routes each diagnostic by severity via a new `mountController.report` —
  through the reporter when set, else a slog fallback that logs transient → **Warn**, terminal → Error
  (no more "deploy error" for a workload merely still starting). Terminal notices still flow so the
  post-ready HOLD phase — where no waiter is left to observe the returned error — stays visible.
- `commands.go` (`UpCmd.runForeground`): passes a reporter surfacing transient → `d.Warn`, terminal →
  `d.Error`, so both cooperate with the progress spinners. The background agent (`agent.go`) keeps the
  slog path (its output IS its slog stream) but now with the corrected warn/error severity.

Design note: terminal (`Done`) failures also flow through the interactive reporter. For a *pre-ready*
failure that mildly duplicates the error the session already returns to the caller, but it is the only
surfacing for a *post-ready* hold-phase death, so it is kept; the duplication only affects genuine
failures (rare) and pre-existed (old code: slog line + returned error).

Tests (both fail on the old code): `TestTransientDeployDiagnosticGoesToReporterNotSlog` (transient
diagnostic reaches the reporter as a non-terminal notice and does NOT appear in slog) and
`TestTransientDeployDiagnosticFallsBackToSlogWarn` (with no reporter it falls back to a slog WARNING,
never an error), driven by a new `diagnosticAttacher` that emits one `Done`-unset `Err` frame before
Ready. Files: `cmd/cornus/internal/clientagent/project.go`, `controllers.go`, `project_test.go`,
`cmd/cornus/internal/composecli/commands.go`. Full module gate green (`gofmt -l` clean, `go build ./...`,
`go vet ./...`, `go test ./...`). No commit (working-tree only).

## 2026-07-21 — Non-destructive disk-usage reporting surface (GAP §2 reporting half)

Closed the reporting half of the "no disk-usage / quota surface" gap. Previously nothing exposed
current storage consumption without running the DESTRUCTIVE `POST /.cornus/v1/gc` (which reports
`fileCacheBytes` as a side effect of pruning). Added a read-only counterpart.

`storage.Backend.Usage(ctx) (Usage{Blobs, Bytes})` lists `blobs/sha256/` and Stats each blob to sum
the CAS footprint. A blob that vanishes between the List and its Stat (a concurrent GC / blob DELETE)
is skipped rather than erroring — a usage snapshot need not be transactional. Documented as O(blob
count) store round-trips: cheap on the filesystem backend, notably more expensive on S3 (a HEAD per
object), so it is an occasional-operator-query surface, not a hot path (same caveat as the `_catalog`
walk).

`GET /.cornus/v1/storage` (`handleStorageUsage`, registered next to `/gc`) returns
`{casBlobs, casBytes}` plus `{fileCacheBytes, fileCacheFiles}` when the block cache is enabled. CAS
fields are zero in a pure re-export configuration (nil store). It is authn-governed (behind the auth
middleware like other `/.cornus/v1` reads) but gated on no policy action and never mutates state —
GET-only (405 otherwise). Deliberately NOT folded into `/.cornus/v1/info`, which must stay cheap for
probing; walking blobs is not.

Tests: `storage.TestUsage` (count/bytes track puts + a delete), `server.TestStorageUsageEndpoint`
(seeded CAS → correct report) / `…MethodNotAllowed`. Files: `pkg/storage/usage.go` (+ test),
`pkg/server/gc.go` (handler + response type), `pkg/server/server.go` (route + gcTicks field from the
earlier flake fix), `pkg/server/storage_usage_test.go`.

STILL OPEN (deferred, a design decision — flagged, not autonomous): quota ENFORCEMENT — what to do
when a ceiling is hit (reject which pushes? evict? warn only?). The reporting surface is the
prerequisite; the enforcement policy is a separate call. Full module gate green. No commit
(working-tree only).

## 2026-07-21 — Doc fix: Compose `x-cornus-ingress.tls` keys are snake_case, not camelCase

While answering "how do I enable TLS for a compose service with `x-cornus-ingress` in the emulated
ingress scenario," found a real key-casing bug in the user docs. The Compose extension struct
`compose.IngressTLS` (`pkg/compose/types.go:881-884`) tags its fields `json:"secret_name"` /
`json:"cluster_issuer"` (snake_case), verified by `pkg/compose/ingress_test.go:24-26`. But several
doc examples wrote the *native deploy spec's* camelCase `clusterIssuer` under `x-cornus-ingress`,
which the compose parser silently ignores (unknown key -> no TLS). Same snake_case convention applies
to the other compose ingress keys (`path_type`, `class_name`).

Root cause of the confusion: two layers with different casing. Compose `x-cornus-ingress.tls` uses
snake_case (`secret_name`, `cluster_issuer`); the native `api.IngressTLS` / deploy spec uses camelCase
(`secretName`, `clusterIssuer`). The `deploy-spec.md` / `helm-values.md` camelCase spellings are
correct (native spec / Helm values) and were left alone.

Fixed `tls: { clusterIssuer: ... }` -> `tls: { cluster_issuer: ... }` in five Compose examples:
`docs/topics/ingress.md`, `docs/guides/ingress.md`, and the ja/zh mirrors of both
(`docs/{ja,zh}/topics/ingress.md`, `docs/{ja,zh}/guides/ingress.md`). Added a casing note to
`docs/topics/ingress.md` after the Compose example clarifying snake_case vs camelCase and that a bare
`tls: {}` requests HTTPS with the server default issuer. Translation prose note was only added to the
English topics page; ja/zh got the code fix only (flagged for a later `translate-documents` sync).

Emulated-ingress TLS mechanics themselves were already documented correctly (BYO cert via the
profile's `conduit.ingress.certificates`, mkcert / self-signed CA fallback, `emulate` mode
terminating HTTPS locally). Reference E2E scenario: `e2e/scenarios/socks5-ingress-tls.{star,yaml}`
(compose uses `tls: {}`; cert supplied out-of-band via the `byo` profile's
`conduit.ingress.certificates`; run with `CORNUS_INGRESS_CONDUIT=emulate`). Docs-only change, no code
touched, working-tree only (no commit).

## 2026-07-21 — `cornus storage usage` client + CLI (consume the /storage endpoint end-to-end)

Completed the disk-usage surface: the `GET /.cornus/v1/storage` endpoint added earlier now has a
client method and a CLI consumer. Promoted the server-local response type to a shared
`api.StorageUsage` ({CASBlobs, CASBytes, FileCacheBytes, FileCacheFiles}); `pkg/server` uses it in
place of the unexported struct (behavior/JSON identical). Added `client.StorageUsage(ctx)` (GET,
modeled on `client.Info`) and a top-level `cornus storage` command group with a `usage` subcommand
(`--format text|json`; text renders via a new `humanBytes` binary-unit formatter). The command
resolves the connection through the standard `requireConn` resolver, so profiles/token/TLS all apply.

Chose a `storage` group (not a bare command) to leave room for future storage-admin subcommands;
`gc` intentionally stays server-side (endpoint + scheduler, no CLI) for now.

Tests: `cmd/cornus TestHumanBytes`; the endpoint/backend tests from the prior entry cover the wire
side. Verified `cornus storage --help` / `cornus storage usage --help` parse under kong. Files:
`pkg/api/deploy.go` (StorageUsage type), `pkg/server/gc.go` (use api type), `pkg/client/client.go`
(StorageUsage), `cmd/cornus/storage.go` (+ test), `cmd/cornus/main.go` (register), and the updated
`pkg/server/storage_usage_test.go`. Full module gate green (`gofmt -l` clean, `go build`, `go vet`,
`go test ./...`). No commit (working-tree only).

Follow-up tracked in TODO: user-reference docs (`docs/cli/storage.md` EN + sidebar wiring + ja/zh
localized pages) — deferred because the CLI pages are per-locale and the shared nav needs all three
to avoid dead-link build failures; the code and CLI `--help` are shipped.

## 2026-07-21 — `cornus storage` user-reference docs (EN + ja/zh)

Closed the docs follow-up from the previous entry. Wrote `docs/cli/storage.md` (modeled on
`token.md`/`version-health.md`: synopsis, description, flag table, a JSON-field table, examples, see
also) and the two localized pages via the `translate-documents` skill: `docs/ja/cli/storage.md`
(half-width parentheses/colons per the repo docs rule; ブロブ / コンテンツストア /
ガベージコレクション per the JA glossary) and `docs/zh/cli/storage.md` (full-width punctuation, `blob`
kept verbatim — matching the existing zh pages). Site-absolute links locale-prefixed
(`/ja/cli/config`, `/zh/cli/config`). Wired one `/cli/storage` sidebar entry in
`docs/.vitepress/config.mts` (per-locale text); all three pages exist so the nav has no dead links.

Checks: the skill's structural audit passed for both localized pages (0 warnings); `npm run
docs:build` clean; `git diff --check` clean; `docs/.vitepress/dist` is gitignored so the rebuilt
output is not a tracked diff. No commit (working-tree only).

## 2026-07-21 — `compose up` collapses per-service status into one live line (with a `--progress` status/stream switch)

Follow-up to the same day's "transient deploy diagnostics leaking through slog" fix. The user
observed that `cornus compose up` *streams* status changes and deploy diagnostics as scrolling
lines, when the natural surface is one in-place, mutating line per service keyed by name (the way
`docker compose up` renders a live status column). Two user decisions framed it: (1) both the deploy
diagnostics AND the per-instance transitions should collapse onto the one line; (2) on a fancy TTY
the scrolling status lines go away entirely (plain/json/non-TTY keep append-only output). A later
message added: make the visualization a user preference — `--progress=status|stream` plus
`CORNUS_PROGRESS` (flag + env, no profile persistence), names `status` (collapsed, default) /
`stream` (legacy scrolling).

Design — UI layer first (deliberate, at the user's suggestion). `cliout` now owns the
collapse-vs-stream decision as a single concept orthogonal to the plain/fancy/json Mode: a
`ProgressStyle` (`ProgressStatus` default, `ProgressStream`) on the `Driver`, resolved from
`CORNUS_PROGRESS` at `New` and overridable via `SetProgressStyle`. `Driver.Progress()` starts a live
bubbletea region only when `liveProgressEligible()` — fancy + real stderr TTY + `ProgressStatus`. So
`--progress=stream` suppresses the live region even on a capable terminal, and every existing caller
(`reportReconcile`, `reportTeardown`, build report) falls back to its append-only notices/events with
no per-caller plumbing. `ParseProgressStyle` maps the flag/env values (empty→default; unknown→default
with ok=false so a flag can reject while env silently defaults). Files: `cmd/cornus/internal/cliout/
progress.go` (ProgressStyle, ParseProgressStyle, liveProgressEligible), `driver.go` (field + env
resolve + Set/Get), `progress_test.go` (ParseProgressStyle, driver resolution, eligibility table).

Compose layer — one `serviceStatus` per service (`cmd/cornus/internal/composecli/servicestatus.go`).
It wraps a `*cliout.Task` and routes each update by `prog.Live()`: live → `task.Update("<svc>:
<state|msg>")` (collapse onto the one line); non-live → the pre-existing append-only `serviceEvent` /
`Warn` / `Error`. Methods: `transition`, `diagnostic(msg, terminal)`, `waiting(detail)`, `done`,
`fail`. The Task spans the whole bring-up (created up front, finished once by the owning goroutine's
`defer` from its outcome), so a mounted service's attach-phase diagnostics and its later
cluster-reconcile transitions share one continuous line.

Wiring in `runForeground`: create a `statuses` map of every selected service after the build's own
progress region is torn down (so only one live program owns the terminal at a time), captured by
reference in the engine's `DeployReporter` closure so mounted-service diagnostics fold onto the right
line. `pollTransitions` took an `onTransition(instance, state)` callback replacing its hardcoded
`d.Event`; `reportReconcile`/`reportCompletion` gained an optional `status *serviceStatus` (nil =
standalone Task, the pre-collapse behavior kept for tests and `down`); `waitForDependencies` gained
`status` and now shows the wait on the *waiter's* line and suppresses the dependency's duplicate
transitions (the dependency reports its own on its own line); `runMountedServiceHooks`/
`waitRunningThenHooks` thread `status` so the hook-gating reconcile updates the same line.

Scope note: `upDetached` (`up -d`) passes `nil` status — it hands mounts to the background agent and
returns quickly, so it keeps its standalone per-service tasks (still governed by `--progress`, just
not folded into a held foreground line). The `--progress` flag is on `UpCmd` only; `CORNUS_PROGRESS`
covers the other compose commands via `Driver.New`.

Tests: `cliout` ParseProgressStyle/resolution/eligibility; `composecli`
`TestServiceStatusStreamFallback` (non-live append-only contract byte-stable),
`TestReportReconcileWithStatusStreamsIdentically` (status path streams identically in a pipe),
`TestApplyProgressFlag`. Existing reconcile/lifecycle call sites updated for the new signatures. Full
module gate green: `gofmt -l` clean, `go build ./...`, `go vet ./cmd/cornus/...`, `go test ./...`. No
commit (working-tree only).

## 2026-07-21 — Doc enhancement: close three `x-cornus-ingress` usability gaps

Follow-up to the snake_case fix above. A real-world Compose file mis-used `x-cornus-ingress`
in three ways that all traced back to insufficient docs, not user error. Enhanced the canonical
English ingress docs so each mistake is now hard to make.

The three gaps and their fixes (all in `docs/topics/ingress.md`, with a lighter mirror in
`docs/guides/ingress.md`):

1. camelCase keys copied into Compose. The Routing field table listed only the deploy-spec camelCase
   names (`pathType`, `className`), so Compose authors wrote `pathType:` — an unknown key the parser
   silently drops (`json.Unmarshal` into the alias ignores unknowns; see `compose.IngressDocument`).
   Fix: added a second **Compose key** column to the table mapping each camelCase field to its
   snake_case spelling (`path_type`, `class_name`, `annotations`, and under `tls:` `secret_name` /
   `cluster_issuer`), plus a note that camelCase keys are silently ignored and values are
   case-sensitive (`Prefix`, not `prefix` — validated by `api.IngressSpec.Validate`, deploy.go:751).

2. `port` confused with the public HTTPS port. Users set `port: 443` thinking it is the external
   listen port. It is the **container** target port, matched against published container ports
   (kubernetes.go:3801-3812). Fix: the `port` table row now says explicitly it is the port the app
   listens on inside the container, not the public 80/443 (which come from `tls: {}`), and quotes the
   `ingress: port N is not among the deployment's published container ports` error.

3. Ingress enabled with no published port. Ingress fronts the workload's ClusterIP Service, which
   requires at least one published port (hard error at kubernetes.go:3737-3738, `ingress requires the
   deployment to publish at least one port`). The Compose example omitted `ports:`, modelling the
   broken shape. Fix: the Routing section now opens with the publish requirement and the exact error;
   both the topic and guide Compose examples now show a `ports:` entry plus a container `port:`, and a
   pitfalls list notes the long-form `- target: N` trick to publish a container port without binding
   a host port (avoids host-port clashes between services).

Docs-only change; no code touched. English canonical docs updated; the ja/zh mirrors now lag these
enhancements (earlier they received only the snake_case key fix) and need a `translate-documents`
sync. Working-tree only, no commit.

## 2026-07-22 — `compose up` re-apply of a running one-shot aborted the whole up (Job replace race + client fail-fast)

### Symptom

`cornus compose up` on a project that was already up would fail on the second (and subsequent) run
with `deploy <svc>: job "<name>" still terminating` (surfaced to the client as a `500 Internal Server
Error`), and — because the aborting up tore the client session down — the OTHER services' streamed
9P mounts died too. A mounted service whose working directory is a client-local bind mount then
started against an empty mount (its app failed on a missing file), and its in-pod caretaker logged
`caretaker mount: failed ... connection reset by peer` on every retry, ending in `context canceled`.
A fresh (nothing-deployed) up worked; the failure only appeared when a running one-shot service had
to be re-applied.

### Root cause 1 (server) — `waitJobGone` retry budget far too short

A `restart: "no"` / `on-failure` service deploys as a Job (`applyWorkload`). A Job's pod template is
immutable, so re-applying an existing one deletes the old Job foreground (so its pods go too) and
recreates it — `applyJob` at `pkg/deploy/kubernetes/job.go`. Between delete and recreate,
`waitJobGone` waited for the old Job to disappear, but used `retry.DefaultBackoff` (client-go): only
**4 attempts over ~0.31 s total** (sleeps 10ms, 50ms, 250ms), despite a doc comment claiming it was
"bounded by ctx". A foreground Job deletion cannot complete until its pod finishes its termination
grace period (tens of seconds), so the old Job was still present after 0.3 s → `waitJobGone` returned
`job %q still terminating` → the `Create` was skipped → the deploy errored, leaving the Job
**deleted-but-never-recreated**. A long-running server pod that ignores SIGTERM until its grace
period makes it reproduce every time; even a fast-exiting pod takes >0.3 s to clear on a real cluster.

Fix: `waitJobGone` now genuinely polls with `wait.PollUntilContextTimeout` (500 ms interval, bounded
by ctx, capped at a new `jobDeleteTimeout = 3 * time.Minute` that comfortably exceeds any pod grace
period) instead of quitting after ~0.3 s. Regression test `TestWaitJobGoneWaitsOutTermination` in
`job_test.go` installs a fake-clientset Get reactor that reports the Job present for more polls than
the old 4-attempt budget tolerated, then NotFound — fails on the old code, passes on the fix.

### Root cause 2 (client) — one deploy error fail-fast-cancels the shared conduit + every peer mount

`runForeground` / `upDetached` (`cmd/cornus/internal/composecli/commands.go`) deploy all selected
services concurrently in one `errgroup.WithContext(ctx)`. That is fail-fast: the first goroutine to
return a non-nil error cancels the shared `gctx`, which unblocks every other in-flight call with a
cancellation-shaped error (the informative `suppressCascaded` in `reconcile.go` keeps those cascade
errors from masking the real one). On a non-nil `groupErr`, `finish` → `teardown` calls
`project.Close()` (drops every mounted service's 9P deploy-attach hold) and `conduit.Close()`. So the
file server for a perfectly healthy mounted peer was torn down as **collateral** of a sibling's deploy
error — no health check of the mount was involved. Worse, the deploy call sites had no
transient-vs-terminal distinction: any error, including a retryable Job-replace race, tripped the
fail-fast.

Fix (defense in depth, complements RC1): retry transient deploy errors a bounded number of times
before letting them escape the goroutine, so a recoverable server-side hiccup can no longer cancel the
group.
  - `pkg/client/client.go`: `apiError` now returns a typed `*client.APIError{StatusCode,Status,Message}`
    (its `Error()` string is byte-identical to the old `fmt.Errorf`, so no message-matching/output
    regressions) with a `Transient()` helper — true for 5xx / 429, false for 4xx.
  - `cmd/cornus/internal/composecli/deployretry.go` (new): `transientDeploy(err)` classifies using two
    signals, because the two deploy transports fail differently — the stateless Deploy POST returns a
    typed `*client.APIError` (classified by status), while the deploy-attach path that mounted services
    use streams its failure back as free text with no status code, so the one known-retryable server
    message (`still terminating`) is matched directly. `retryTransientDeploy(ctx, deploy)` runs the
    deploy with a bounded, doubling, ctx-aware backoff (`deployRetryAttempts = 4`,
    `deployRetryBackoff = 500ms`, a var only so tests can shrink it); a success, a terminal error, the
    attempt cap, or a cancelled ctx all stop it immediately (so a group already doomed by another
    service does not sleep out the backoff).
  - Applied at the three synchronous deploy call sites in `commands.go`: foreground mount-free
    `client.Deploy`, foreground **mounted** `expose` (the deploy-attach path), and detached mount-free
    `client.Deploy`. The detached-agent mounted path (handed to the background agent) is out of scope
    for this change.
  - Tests `deployretry_test.go`: classifier truth table (5xx/429/4xx, wrapped errors via `errors.As`,
    `still terminating`, unrelated, nil); retry-succeeds-after-transient; caps at `deployRetryAttempts`;
    terminal-error-not-retried; cancelled-ctx-stops-after-one-try.

### Finding — the deploy engine runs in the in-cluster server, not the CLI

Reproducing live surfaced an important detail: replacing a running one-shot returned
`500 Internal Server Error: job "<name>" still terminating` in ~1 s — a **server-side** error. The
`pkg/deploy/kubernetes` backend (where `waitJobGone` lives) executes inside the cornus server, not the
`cornus compose` client; the client just forwards the deploy request. So a rebuilt client binary does
NOT change deploy behavior — verifying the fix end-to-end requires shipping the fixed binary into the
in-cluster server image and rolling it. (The server is typically a StatefulSet on the official image
with a persistent CAS/build-cache PVC that a pod roll preserves.) The bug was cleanly reproduced
against the live server before the roll; the roll itself is a cluster-write step.

### E2E

New kube-only scenario `e2e/scenarios/compose-redeploy-oneshot.{star,yaml}` (registered in the
Makefile `SCENARIOS`): first `up` brings up a one-shot Job whose pod stays Running (it sleeps, ignores
SIGTERM, `stop_grace_period: 8s`), then a second `up` re-applies it — the exact replace-a-running-Job
path, exercised against a real server (so it covers the server-side `waitJobGone` path, not just the
unit-level fake). It asserts the service comes back up and the Job is recreated rather than aborting
with `still terminating`. Parse-validated via `cornus-e2e --check`; a full run needs the kube harness.

### Gate

`gofmt -l` clean, `go build ./...`, `go vet ./...`, and `go test ./pkg/client/ ./pkg/deploy/kubernetes/
./cmd/cornus/internal/composecli/` all green. Files touched: `pkg/deploy/kubernetes/job.go` (+test),
`pkg/client/client.go`, `cmd/cornus/internal/composecli/deployretry.go` (+test) and three call sites in
`commands.go`, plus the E2E scenario/fixture and Makefile registration. Working-tree only, no commit.

## 2026-07-22 — Follow-up: the still-terminating fix was not the whole story — stale mount-session id (the deferred TODO), and scoping back the mounted retry

After the above landed and a fixed SERVER image was rolled onto a live cluster, a mounted service
(a client-local bind mount streamed over 9P) STILL failed: its caretaker logged
`caretaker mount: failed ... connection reset by peer` on every retry, then `context canceled`, and its
app started against an empty mount. The `still terminating` abort was genuinely gone (its peers came
up), so this was a DIFFERENT failure.

### Diagnosis — the deferred "stable mount-session id" bug, now confirmed

The server WARN `mount relay: reset stream (no live backing for this deploy-attach session)` named the
session. Its logged value is a DIGEST (`mountServiceName` = `sha256(sessionID)[:16]`), and computing
that digest over the pod's OWN baked caretaker session id matched exactly — so the pod's own
deploy-attach session was dead on the server while the pod's caretaker kept presenting it. This is the
failure mode documented in `LTM/caretaker-transport-and-hub-synthesis.md` §"stale mount-session ids"
and tracked as a deferred decision in `TODO.md`: the session id is minted fresh per deploy-attach
connection (`newSessionID()`) and baked FIXED into the pod, while `s.mounts` is in-memory per process,
so any connection replacement (server restart OR a deploy-attach reconnect / a re-run of `up`) orphans
the id the pod presents. The TODO deferred the fix "pending confirmation via the new `logMountReset`
WARN of whether the trigger is a server restart or a reconnect." That confirmation is now in hand: the
server had `RESTARTS=0` and the workload was created AFTER it, so the trigger is a **deploy-attach
reconnect**, not a restart. An amplifier made it terminal here: the one-shot services are Jobs with
`backoffLimit=0`, so a single mount reset fails the pod permanently (no retry).

### Fix 1 — stable mount-session id on re-apply (resolves the deferred TODO)

Reuse the id already baked into the running workload instead of minting a new one, so a reconnecting /
re-running client re-registers under the id the pod already presents:
  - `deploy.MountSessionReader` (`pkg/deploy/deploy.go`): optional Backend extension,
    `ExistingMountSession(ctx, name) (string, error)`.
  - Kubernetes `Backend.ExistingMountSession` (`pkg/deploy/kubernetes/kubernetes.go`): reads the
    `CORNUS_CARETAKER_CONFIG` JSON off the `cornus-caretaker` container of the running Deployment (or
    Job) and returns the shared session id; "" (never an error) when absent / no caretaker.
  - `Server.mountSessionID` (`pkg/server/deploy_attach.go`): reuses the baked id when the backend can
    report one AND no live session currently holds it (`mountRegistry.has`, added), else mints fresh —
    so reuse can never clobber a still-connected session, and a first apply / non-reader backend / a
    read-back error all fall back to a fresh id. Wired into all three attach paths
    (`applyWithSidecarMounts`, `applyWithAttachments`, `applyWithEgress`).
  - The id remains an unguessable capability: reuse only re-adopts an id already baked into a workload
    the same authenticated deployer is re-applying. NB: it takes effect on a client RE-APPLY —
    `attachsession.Open` does not itself auto-reconnect, so the payoff is the "re-run `compose up`
    keeps already-running mounts alive" path (and any future reconnect-and-re-apply). A client that
    auto-reconnects a dropped mount without re-applying is a possible follow-up.
  - Tests: `pkg/deploy/kubernetes/mount_session_test.go` (read-back from Deployment/Job, "" when
    absent/no-caretaker); `pkg/server/mount_session_test.go` (reuse when baked+not-live, fresh when
    live/absent/error/no-reader).

### Fix 2 — scope the client transient-deploy retry back to the mount-free path

The earlier `retryTransientDeploy` wrapped the MOUNTED `expose` path too. Retrying a deploy-attach
re-runs the reconciler, which mints a fresh mount session id and can re-apply the workload — itself a
"connection replaced under a fresh id" event, i.e. the very trigger of Fix 1's bug. Removed the wrap
on the mounted path (`commands.go`); it now guards only the two mount-free `client.Deploy` POSTs. The
server-side `waitJobGone` fix already covers `still terminating` on the mounted path, so no safety is
lost. Comments in `deployretry.go` updated to say so.

### Gate

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all green. Also rebuilt the static
server image and side-loaded it into the k3d node for live verification; rolling the in-cluster server
StatefulSet onto it is a cluster-write step left to the operator. Working-tree only, no commit.

## 2026-07-22 — Live-cluster debugging of a real multi-service compose up: five more root causes, one wrong turn, and an E2E coverage post-mortem

Rolling the fixed server onto a live cluster and re-running a real, multi-service `compose up` (mounted
services, one-shots with volumes, `depends_on: service_completed_successfully` chains, and an
emulate-mode ingress) surfaced a chain of further bugs — each unmasked by fixing the one before it.
The server-side deploy-attach lifecycle logging added here (see below) was decisive: without it these
were near-invisible.

### Bug — terminating-PVC race wedged one-shot re-apply (Unschedulable "persistentvolumeclaim not found")

Once one-shot re-applies actually completed (the `waitJobGone` fix), a one-shot with an anonymous
managed volume failed to reschedule: `pod: Unschedulable: persistentvolumeclaim "<name>-vol-0" not
found`. An anonymous-volume PVC is owned by its workload (Job/Deployment), so the foreground Job delete
on re-apply cascades a GC of the PVC. `applyDependents` created the PVC with `Create` and silently
swallowed `AlreadyExists` — so a re-apply that raced the GC ADOPTED the still-terminating claim, which
then vanished, wedging the new pod. Fix: `ensurePVC` (`pkg/deploy/kubernetes/kubernetes.go`) now
inspects an AlreadyExists claim — a live one is reused (spec is immutable), a terminating one is waited
out (bounded by `pvcDeleteTimeout`, ctx) and recreated fresh. Tests `pvc_test.go`.

### Bug — kube API client self-throttling starved the whole bring-up

Server logs were saturated with client-go `client-side throttling ... Waited ~1.2s` on nearly every
call. The backend's rest.Config used client-go defaults (QPS 5 / Burst 10) — far too low when one
`compose up` deploys ~10 services and polls each one's readiness every second. Raised to QPS 50 /
Burst 100 in `loadConfig`, overridable via `CORNUS_KUBE_QPS` / `CORNUS_KUBE_BURST` (`envFloat32` /
`envInt` helpers). This is the api-server's priority/fairness to enforce, not the client's to
self-limit.

### Wrong turn (reverted) — gating the app on a 9p mount broke a workload with an optional failing mount

Hypothesis: the app container's startup probe (`cornus caretaker-check` -> `Ready`) used `IsMountpoint`,
which is true the instant the pod starts because the mount target (/cornus/mounts/<i>) is an emptyDir
base — so the app could start against the empty base before the 9P mount attached. Changed `Ready` to
require the 9p fstype (`is9PMount`). This REGRESSED a real workload: a one-shot with FOUR mounts, one of
which (a read-only bind of a client dir that does not serve) never attaches — the app never needed it
and completed fine before, but the stricter gate blocked the app FOREVER on the one unattachable mount
(`mount m2 not live ... (no 9p mount attached)`, startup probe failing 120x). Lesson: the lenient
`IsMountpoint` gate was load-bearing — real compose files carry optional/failing binds, and readiness
must be best-effort, not all-or-nothing. Reverted entirely (`Ready` back to `IsMountpoint`; `is9PMount`
/`hasMountOfType` removed). No net change to the tree from this excursion.

### Fix — client completion/dependency wait must match the server's readiness patience

The client's `waitForDependencies` / `reportCompletion` used a flat 120s (`reconcileWaitTimeout`) while
the server's own deploy-attach readiness wait is 5m (`readyTimeout`). A one-shot that legitimately
restarts before completing (an init retrying until its dependencies are ready) could still be coming up
server-side when the client abandoned it and failed the up. Added `completionWaitTimeout = 5m` for the
completion and dependency waits (a one-shot completing / a depends_on gate); `reportReconcile` (a
long-lived service reaching Running, expected to be quick) keeps the tighter 120s. Not a regression from
this session — the earlier fixes just let one-shots get far enough to hit it.

### ROOT CAUSE of the headline failure — emulate-mode ingress wrongly created a real cluster Ingress

The mounted service that kept failing (`npm ENOENT` on an empty bind mount) was NOT a mount bug. Timeline
from the new lifecycle logs: its deploy-attach session was registered then TORN DOWN ~0.8s later with no
"caller disconnected"/"not reach ready" line — i.e. `Apply` itself errored AFTER the Job/pod were
created, orphaning the pod (the pod then ran against an unbacked session -> reset mount -> empty dir ->
npm ENOENT). Every OTHER service succeeded; the failing one was the ONLY one with an `x-cornus-ingress`.
`kubectl auth can-i create ingresses` -> **no**: the server Role grants Deployments/Jobs/Services/PVCs/
Secrets but never `networking.k8s.io/ingresses`, so `applyDependents` -> `ings.Create` was forbidden.

But — as the user noted — with `--ingress-conduit=emulate` the ingress is a CLIENT-SIDE reverse proxy;
the server should not create a real cluster Ingress at all. The bug was that emulate mode still shipped
`spec.Ingress` to the server. Fix: `api.IngressSpec.ClientEmulated` — client sets it for emulate mode
(`MarkClientEmulatedIngress`, wired beside `MaterializeNativeTLS` in both up paths); server's
`ingressEnabled` (the single gate `b.ingress` and `applyManagedIngressTLSSecrets` funnel through)
returns false for it, so no Ingress object and no TLS Secret. The client-side `AddIngress` still reads
the spec and builds the emulated proxy — unchanged (native keeps the real server Ingress; off/plain
deploys are untouched). Tests: server (emulated -> no Ingress object, Deployment still created), client
(emulate flags; native/off/none do not).

### Fix — the missing ingress RBAC is a real gap for NATIVE ingress too

The emulate fix sidesteps the RBAC gap, but a native/real ingress (or a plain `cornus deploy` with an
x-cornus-ingress) genuinely needs the server to create the Ingress. Added the
`networking.k8s.io/ingresses` grant to both `deploy/k8s/cornus.yaml` (unconditional — it is the
batteries-included example, which likewise grants ingress-TLS Secrets unconditionally) and
`deploy/helm/cornus/templates/rbac.yaml` (GATED behind a new `ingress.manageIngresses` value, default
false, mirroring the `ingress.manageTLSSecrets` opt-in — least privilege, since the common emulate
workflow needs it not at all; only operators wanting real cluster Ingresses enable it). Verified with
`helm template` off/on.

### Diagnostics added (kept) — server-side deploy-attach session lifecycle logging

`pkg/server/deploy_attach.go` now logs the full session lifecycle keyed by deployment + the session
DIGEST (the same digest the mount-reset WARN uses, so a pod's reset lines up with its session's
teardown): reuse, register, apply-failed (with the error — previously only sent to the client),
not-reach-ready (with ctx_err to distinguish client-disconnect from timeout), workload-ready,
caller-disconnected, torn-down. `sessionDigest` helper in `mount_relay.go`. These turned a class of
"connection reset by peer / no live backing" mysteries into a readable timeline and were essential to
finding the emulate-ingress root cause.

### E2E coverage post-mortem (the important lesson)

The `compose-redeploy-oneshot` scenario added earlier CLAIMED to cover "the case" but did not: it was a
single bare `alpine sleep` one-shot — no mounts, no volume, no ingress, no dependencies — so it
exercised only `waitJobGone` timing and missed every shape that actually broke. Two structural blind
spots compounded it:
  1. The E2E `kube` target runs `cornus serve` as a HOST process with the kind ADMIN kubeconfig, so it
     is never subject to any RBAC Role — an RBAC-restriction bug (the missing ingresses grant) is
     invisible by construction there.
  2. The one emulate-ingress scenario (`socks5-ingress-tls`) asserted only the client-side proxy, never
     the server-side "no real Ingress" behavior — and its `CORNUS_INGRESS_TLS_ISSUER` workaround existed
     to make the WRONGLY-created real Ingress validate, papering over the bug.
  3. The only RBAC-realistic path (`incluster-*`) used a hand-copied Role that had drifted from
     production (no PVCs/Secrets/leases/ingresses) and no incluster scenario deployed an ingress.

Coverage improvements (all parse-checked, all in the Makefile SCENARIOS list):
  - `socks5-ingress-tls.star`: now asserts on kube that an EMULATED ingress creates NO real cluster
    Ingress (fails against the old behavior in either direction); dropped the issuer workaround.
  - `compose-redeploy-oneshot`: the fixture now carries an anonymous managed volume (a Job-owned PVC),
    so the re-apply exercises BOTH `waitJobGone` AND `ensurePVC` (the terminating-claim race) together;
    asserts the PVC is Bound after re-apply.
  - `incluster-ingress.star` (new): deploys a NATIVE ingress + volume through an in-cluster server
    running under the restrictive `cornus-incluster` Role (its grants now mirror production, with a
    comment demanding they stay in sync), and asserts the server created the real Ingress + PVC — so a
    missing production RBAC grant fails E2E, closing the admin-kubeconfig blind spot for this path.

### Gate

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` green (incl. `pkg/e2e`
predeclared-sync). `helm template` renders the ingresses rule only when `ingress.manageIngresses=true`.
All new E2E scenarios `cornus-e2e --check`-parse. TODO.md updated (stable-session-id item resolved).
Working-tree only, no commit. The RBAC-realism improvement to `incluster-*` closes the E2E gap for the
incluster path only — the host `kube` target remains admin-kubeconfig by design, so RBAC realism there
would need a separate harness change (a candidate follow-up).

## 2026-07-22 — Startup RBAC permission preflight (proactive missing-grant warnings)

Follow-up to the ingress-RBAC gap: the server had NO proactive signal for a missing cluster
permission. A gap surfaced only reactively — a `forbidden` at reconcile time — and on the stateless
`POST /.cornus/v1/deploy` path it was not even logged server-side (only returned to the client), so an
operator watching the server saw nothing. That is exactly why the missing `ingresses` grant cost so
much debugging.

Added a startup permission preflight:
  - `deploy.Preflighter` (`pkg/deploy/deploy.go`): optional Backend extension,
    `Preflight(ctx) []PermissionGap`. A `PermissionGap` carries verb, group-qualified resource, an
    optional Feature name (empty = core capability), and a human Impact hint. Best-effort — a backend
    that cannot self-check returns nil, never errors (a diagnostic must never block serving).
  - Kubernetes `Backend.Preflight` (`pkg/deploy/kubernetes/preflight.go`): runs a
    SelfSubjectAccessReview per grant the deploy paths need (one representative verb per resource,
    mirroring the production Role) against the target namespace, reporting the denied ones. SSAR needs
    no special RBAC (default `system:basic-user` grants it to every authenticated identity). The two
    feature-gated grants — `ingresses` (native ingress) and `secrets` (inline ingress TLS) — are
    reported as FEATURE gaps, since a default install legitimately omits them (emulate needs neither).
  - Server startup hook (`Server.Run` -> `preflightBackend`, `pkg/server/server.go`): for a configured
    kubernetes backend (`CORNUS_DEPLOY_BACKEND` in {kubernetes,k8s}), best-effort in a goroutine (never
    blocks serving; registry-only servers do no cluster discovery). A core gap logs a WARN
    ("missing a required permission"), a feature gap an INFO ("missing an optional-feature permission"),
    each carrying the verb/resource/impact so an operator can match it straight to an RBAC rule.
  - Also closed the reactive gap: the stateless deploy handler (`pkg/server/deploy.go`) now WARN-logs an
    apply failure server-side, not just returns it to the client.

On the live cluster (whose Role still lacks the ingresses grant, harmless now that the workflow is
emulate) this prints, at startup:
`deploy-backend missing an optional-feature permission feature="native ingress" verb=create
resource=ingresses.networking.k8s.io impact="... Grant via ingress.manageIngresses (Helm)."`

Tests `preflight_test.go`: all-allowed -> no gaps; missing ingresses -> one FEATURE gap; missing
deployments -> one CORE gap (empty Feature). Uses a fake-clientset SSAR reactor. `gofmt`/`go build
./...`/`go vet ./...`/`go test ./...` green. Working-tree only, no commit.

E2E: `incluster-preflight.star` (+ `incluster-cornus-restricted.yaml`, registered in SCENARIOS) runs an
in-cluster cornus under a Role granting every deploy resource EXCEPT ingresses, and asserts the server's
OWN startup logs carry the optional-feature ingresses gap AND no required-permission WARN (no
false-positive on the present core grants). Validated live against the k3d cluster with the
preflight-enabled image: the log line
`level=INFO msg="deploy-backend missing an optional-feature permission" feature="native ingress"
verb=create resource=ingresses.networking.k8s.io impact="..."` appeared, with zero
"missing a required permission" lines — matching every scenario assertion. It complements
`incluster-ingress.star` (a native-ingress deploy actually fails forbidden without the grant); this
guards the WARNING that points at it first.

## 2026-07-22 — Delicacy hardening: client/server version skew, one-shot transient retries, apply-error cleanup

After the full live bring-up finally worked, the takeaway was "it works but only if a lot of things line
up." Three fragilities got hardened (the final blocker was itself a symptom: a STALE CLIENT binary —
built before the emulate fix — silently never set ClientEmulated, so the server treated the ingress as
real and failed on a TLS-issuer error five layers down, with nothing saying "your client is out of
date").

  1. **Client/server version-skew warning.** `api.ServerInfo.Version` now carries the server's build
     version (`Server.Version`, set in `serve.go`, stamped in `handleInfo`). The compose client warns
     up front when its build differs from the server's (`warnServerVersionSkew` in composecli, called at
     the top of `UpCmd.Run`, reusing the existing Info fetch path). Predicate `versionSkew` flags only a
     real mismatch — both versions known and different; two identical builds, INCLUDING two "dev" builds
     from the same tree, are indistinguishable and left alone (documented limitation: it catches release
     skew and dev-vs-release, not dev-vs-dev, which is what bit us — the real fix there is committing +
     a stamped release). Tests: `TestInfoAdvertisesVersion` (server advertises it), `TestVersionSkew`
     (predicate truth table).

  2. **One-shot transient retries.** `jobBackoffLimit` gave a restart:"no" one-shot backoffLimit=0 — a
     SINGLE pod attempt, so a transient infra race (scheduling, a not-yet-attached mount, a PVC still
     provisioning) was permanently fatal. Now a small budget (`oneShotTransientRetries = 3`) lets those
     self-heal while a genuinely-broken one-shot still fails quickly; pod restartPolicy stays Never.
     restart:"no" is about not RESTARTING the app after it exits, not a one-shot's retry-on-infra-failure
     budget; init/migration tasks are ~universally idempotent, so the extra attempts are safe.
     `TestJobFromDeployment` updated.

  3. **Apply-error cleanup (no orphaned pods).** The deploy-attach apply-error path dropped the SESSION
     but not the workload — so a backend that created the Job/Deployment before a later dependent step
     failed (PVC/Service/Ingress) left the pod running against a torn-down mount session; it then started
     against an empty mount and failed confusingly downstream (the exact chase this session). It now
     `backend.Delete`s the partial workload before dropping the session, mirroring the not-ready path
     (`pkg/server/deploy_attach.go`). Also fixed the reactive gap on the stateless POST /deploy path
     (now WARN-logs apply failures server-side). Test `TestDeployAttachApplyErrorRemovesWorkload`
     (fakeBackend gained an `applyErr` seam).

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all green (the apply-error test
verified deterministic across `-count=3`). Working-tree only, no commit.

## 2026-07-22 — Emulated ingress: informative HTTP 502 when the upstream is unreachable

The client-side emulated ingress (`--ingress-conduit=emulate`) is an `httputil.ReverseProxy`
(`pkg/ingressemu/ingressemu.go`, `Handler`) that terminates HTTP(S) and reverse-proxies to the workload
over the conduit's port-forward dialer. It had NO `ErrorHandler`, so when the upstream could not be
reached (the workload down / not yet ready, so `DialContext` -> `d.PortForward` fails) it fell through to
net/http's default: a bare 502 with an EMPTY body. To a browser that reads as a blank page / a dropped
connection, giving no clue what went wrong.

Fix: since the emulated ingress always terminates HTTP(S), the client is by definition HTTP-aware here,
so answer with a proper, informative 502 instead of relying on the transport-level failure. Added an
`ErrorHandler` on the ReverseProxy that writes `text/plain` 502 naming the exact target the conduit tried
to reach and the underlying dial error, plus the most common cause:

```
502 Bad Gateway

cornus emulated ingress could not reach workload "<name>" on container port <p>: <err>

The service may not be running or ready yet.
```

Test `TestHandlerUpstreamUnreachableReturns502` (`pkg/ingressemu/ingressemu_test.go`): a `failingDialer`
whose `PortForward` always errors; asserts status 502, `text/plain` content-type, and that the body
carries the workload name, port, the underlying error string, and the readiness hint.

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./pkg/ingressemu/` green. Rebuilt `bin/cornus`
(the emulated ingress runs in the `compose up` client). Working-tree only, no commit.

## 2026-07-22 — E2E flakiness triage: compose plugin, sshd privsep dir, async PVC races

Triaged the failing E2E CI (run 29906110375 and 5 prior failing runs, all on the same squashed HEAD
`17910cc`). The suite is FLAKY, not hard-broken: the identical commit passes on some runs (e.g. 2026-07-18,
2026-07-19) and fails on others, with a different subset of scenarios failing each run. Pulled per-job logs
via `gh api repos/moriyoshi/cornus/actions/jobs/<id>/logs` (the `gh run view --log` path returned empty here)
and diffed "started but never printed `✓ … passed`" across runs to isolate the culprits. Four distinct
clusters, two of them non-hermetic image/runtime issues, two async-timing races.

1. **`dockerd.star` — `docker compose -f …: unknown shorthand flag: 'f' in -f` (exit 125).** That error is
   the docker root CLI parsing `compose -f …` itself, i.e. the `compose` plugin was NOT resolved. Root cause:
   `docker-ce` only *Recommends* `docker-compose-plugin` / `docker-buildx-plugin`, and the runner image
   installs with `--no-install-recommends`, so the plugins are never named explicitly. A cold buildx cache
   rebuild therefore drops them entirely, while a warm cache still serves an older layer that happened to
   carry them — exactly the intermittency observed (the docker-install step `#38` was `CACHED` in the failing
   run). The existing `DOCKER_VERSION=27.5.1` pin does NOT cover the separately-versioned plugin packages.
   Confirmed real available versions against Docker's Debian repo `Packages.gz`: compose is at v5 (5.3.1
   latest), buildx 0.35.0. The scenario genuinely targets Compose **v5** (`convergence.go` / `checkExpectedNetworks`,
   dockerd.star ~line 148), so the fix is to ensure a v5 plugin is present, not to downgrade.
   FIX (`e2e/container/Dockerfile`): new `ARG DOCKER_COMPOSE_VERSION=5.3.1` / `ARG DOCKER_BUILDX_VERSION=0.35.0`,
   resolved with the same fail-closed `apt-cache madison` pattern as `docker-ce` and installed EXPLICITLY in
   the pinned apt line, plus a `docker compose version; docker buildx version` smoke check. This also busts the
   stale cache (RUN text changed), forcing a deterministic rebuild. Only verifiable via a CI run (the
   containerized runner cannot be built locally here).

2. **`deploy-sshtunnel-docker.star` — `sshd did not come up …: Missing privilege separation directory: /run/sshd`.**
   Modern OpenSSH `sshd` aborts at startup when its compiled-in privsep dir `/run/sshd` is absent. In the
   containerized runner `/run` is a fresh tmpfs at container start, so the `openssh-server` package's `/run/sshd`
   does not survive; unpinned openssh versions differ on whether they self-create it (the intermittency).
   FIX (`pkg/e2e/harness.go`, `bSSHD`): `os.MkdirAll("/run/sshd", 0o755)` best-effort just before starting sshd
   — a no-op on a systemd dev host, and if it cannot be created (unprivileged) sshd fails exactly as before, so
   it only ever helps. Works for both the containerized runner and a direct `make e2e-docker`.

3. **`compose-redeploy-oneshot.star` + `incluster-ingress.star` (kube) — `kubectl get pvc …-vol-0`:
   `NotFound` (exit 1).** Both query a PVC by name immediately after a deploy / `wait(running=1)`. The PVC is
   created asynchronously during the backend reconcile, so the object can lag the deploy call returning, and a
   bare `kubectl get` exits 1 on a transient NotFound — which the hard-failing `exec` builtin surfaces as a
   whole-scenario failure. FIX: added an opt-in `retry=<duration>` kwarg to the shared `exec` builtin
   (`pkg/e2e/harness.go`) that re-runs the command until exit 0 up to a deadline (500ms poll, honors `h.ctx`);
   default behavior unchanged (single attempt, fail-hard), so no `predeclaredNames` churn. Both scenarios now
   pass `retry = "30s"` on the PVC get. A Running pod implies its claim is already Bound once the object is
   visible, so retry-until-exit-0 is sufficient for the `Bound` assertion too.

4. **`registry-host-native.star` — `got 201, want 405` (NOT fixed; no code defect).** Traced the whole
   read-only path: `Registry.readOnly()` = `store == nil || sourceReadOnly`; `WithDaemonSource` always sets
   `sourceReadOnly = true` (its only early-return is `api == nil`, and `NewDockerImageAPI` never returns
   `(nil, nil)` — it returns a client or an error); `resolveRegistrySource` maps `host-native` + `dockerhost`
   to the docker-daemon source deterministically; and the env precedence that makes `serve(env=
   {CORNUS_REGISTRY_SOURCE: host-native})` beat `DockerTarget.ServeEnv()`'s `…=off` is Go `exec`'s documented
   last-wins dedup. Every path yields 405 in host-native docker mode. The stray 201 has no deterministic code
   path and is runner-level flakiness; left as-is rather than papering over the read-only enforcement.

Verification: `gofmt -l` clean on `pkg/e2e/harness.go`; `go build ./...`, `go vet ./...`, `go test ./...` all
green; `cornus-e2e --check e2e/scenarios/*.star` parses all 122 scenarios. The Dockerfile change needs a CI
run to confirm. Working-tree only, no commit. NOTE: `pkg/deploy/kubernetes/job.go`, `kubernetes_test.go`, and
`pkg/server/deploy_attach.go` showed as modified by a concurrent session and were left untouched.

---

## compose `up --watch`: auto-reload + re-reconcile

Added an opt-in `--watch` flag to `cornus compose up` that watches every file the
project loaded from and, on edit, reloads the whole configuration and
re-reconciles the running project (recreate changed, start added, tear down
removed via reconcile). Covers both run modes per the design decision.

Design and moving parts:

- **File-set collection** (`pkg/compose`): added `LoadOptions.OnFileRead func(path
  string)`, threaded alongside the existing `warn` callback through
  `loadFile`/`parseFile`/`envMapping`/`applyEnvFiles`, `resolveExtends`, and
  `processInclude`. It reports every compose YAML, sibling `.env`/`--env-file`,
  per-service `env_file:`, and `include:`/`extends` target — including absent
  OPTIONAL files (so creating one later triggers a reload). The loader previously
  read every file via `os.ReadFile` and discarded the paths.
- **Watcher** (`pkg/filewatch`, new): hybrid event-driven-then-poll. At idle it
  blocks on fsnotify events over the watched files' PARENT directories (no busy
  polling); the first event opens a short coalescing window during which it
  stat-polls to gather the full change set before firing once. Watching
  directories + only needing the first event makes it immune to editors'
  atomic write-temp-then-rename saves (which silence an inode-bound watch). Falls
  back to a pure poll loop if fsnotify is unavailable. Promoted `fsnotify` from an
  indirect to a direct dependency.
- **Engine** (`clientagent.Project.ApplyExact`, new): sets the desired set to
  EXACTLY the given services and reconciles, so services dropped from the set are
  torn down — the reconcile-to-declared-state entry point both the foreground
  reload and the agent use. (`Apply` still merges, preserving `up SERVICE`.)
- **Foreground** (`composecli`): `runForeground`'s terminal hold became a loop
  that also selects on a `filewatch` watcher; on change `reloadAndReconcile`
  re-plans into a temp runtime (committed onto `rt` only on success, so a
  parse/build error keeps the old set), reuses the live client/conduit/engine,
  re-deploys only mount-free services whose spec hash changed, `ApplyExact`s the
  set, and deletes removed mount-free deployments server-side.
- **Detached agent** (`clientagent` + `daemonize`): chose "agent re-execs the CLI"
  over an in-agent planner (the `composecli` → `clientagent` import edge blocks
  the agent from importing the planner, and re-exec reuses build+plan for free).
  A watched `up` carries `Watch`/`WatchFiles`/`Reload` (protocol v6); the agent
  pins the project (`watching`), runs a supervised watch loop, and on change
  re-execs the original argv in the original cwd/env via new
  `daemonize.SpawnAt` (the existing `Spawn` set neither, and cwd/env fidelity —
  especially `CORNUS_AGENT_DIR` — decides which agent the re-exec targets). A
  `Watch` up is the complete held desired set (prune-on-reload); full `down`
  stops the watch before teardown.

Documented limitations: detached reload does not delete removed pure
fire-and-forget services (same as a plain re-`up -d`); server/conduit changes need
`down`+`up`; foreground reload re-runs `build:` builds and restarts the follow-log
stream.

Tests: `pkg/compose` (OnFileRead full set incl. missing optional), `pkg/filewatch`
(fire/cancel/coalesce/create/normalize), `clientagent` watch state machine
(arm/pin/prune/partial-vs-full-down), plus E2E `compose-watch-reload.star`
(add a service by editing the file under `up -d --watch`; registered in the
Makefile SCENARIOS). Gate green: `gofmt -l`, `go build ./...`, `go vet ./...`,
`go test ./...` (and `-race` on the touched packages), `make e2e-check` parses all
scenarios. Working-tree only, no commit.

## Emulated ingress: longest-match path routing across ingresses sharing a host

The emulated ingress (`--ingress-conduit=emulate`) could not route two ingresses that
shared a host by longest path match the way a real Kubernetes ingress does. Root cause
was structural, not a bad comparison: the SOCKS5 router keys locals purely on
`host:port` (`pkg/socks5` `localSubject`), and path is not — cannot be — a routing
dimension there, because SOCKS5 dispatches at CONNECT time before any HTTP is read.
`addEmulatedIngress` called `ingressemu.Serve` per ingress and `RegisterLocal`'d each
listener at `host:80`, so a second ingress on the same host clobbered the first
(last-write-wins, registration-order-dependent). One path was silently shadowed —
either 404 (surviving handler gates on the wrong path) or proxied to the wrong backend
(a surviving `/` prefix swallows everything).

Fix: path selection must live at the HTTP layer, so added `ingressemu.Mux` — one shared
listener/server per `host:port` owning a set of `(path, pathType, backend)` rules, with
`entryHandler`/`longestMatch` dispatching each request to the longest matching path
(Exact beats Prefix at equal length; `matchLen` trims trailing slashes so `/` ranks
lowest). The Mux stays router-agnostic via `register`/`unregister` callbacks; the first
rule for a `host:port` creates+publishes the listener, the last removed closes+withdraws
it (withdraw uses the entry's stored host so case-variant callers can't unpublish the
wrong key). First TLS ingress on a shared `host:443` establishes the cert (a shared host
has one TLS identity on the wire). `clientconduit` holds one Mux per conduit (lazy
`mux()`, wired to `router.RegisterLocal/UnregisterLocal`); `Serve`/`Emulated` kept as a
thin single-ingress adapter over Mux so existing callers/tests are unchanged. Regression
tests `TestMuxLongestMatchRouting` (root registered FIRST, `/api` SECOND — the order the
old code broke on) and `TestMuxExactBeatsPrefixOnEqualLength` drive real `http.Client`
requests over the memlisten listener. Gate green: `gofmt -l`, `go build ./...`,
`go vet ./...`, `go test ./...` (and `-race` on `pkg/ingressemu` + `pkg/clientconduit`).
Working-tree only, no commit.

Follow-up: added E2E scenario `socks5-ingress-longest-match.star` (+ `.yaml`,
registered in Makefile `SCENARIOS`) for the emulate-mode fix above. Two services
(`web-root` at `/`, `web-api` at `/api`) share one ingress host `shared.example.com`;
`traefik/whoami` backends print `Hostname:` (set via compose `hostname:`) so the
response reveals which backend served. Asserts `/api`+`/api/v1/x` -> backend-api,
`/`+`/other`+`/apix` -> backend-root, proving longest match (and element-boundary
prefix). Portable docker+kube (reverse proxy dials through the server proxy). Parses
under `cornus-e2e --check`.

## Work summary: emulated-ingress longest-match routing (consolidated)

Consolidates the two entries above. Reported symptom: in emulate mode the internal
router did not honor the longest path match among ingresses sharing a host (unlike a
real Kubernetes ingress).

Findings:
- Not a bad comparison — a structural gap. The SOCKS5 router (`pkg/socks5`,
  `localSubject`/`RegisterLocal`) keys published locals on `host:port` only. Path
  cannot be a routing dimension there because SOCKS5 dispatches at CONNECT time, before
  any HTTP request line is read. So path selection must live at the HTTP layer.
- The bug was last-write-wins, not shortest-match. Each `AddIngress` built its own
  listener via `ingressemu.Serve` and `RegisterLocal`'d it at `host:80`; a second
  ingress on the same host replaced the first in the map. Outcome was
  registration-order-dependent: one path 404s (surviving handler gates on the other
  path) or misroutes (a surviving `/` prefix swallows everything).
- Precondition: only manifests with 2+ ingress rules sharing a `host:port`. A single
  path per host always worked, which is why it went unnoticed.

Fix: `ingressemu.Mux` — one shared listener/server per `host:port` owning a rule set
`(path, pathType, backend)`; `longestMatch` picks the longest matching path, Exact
beating Prefix at equal length (`matchLen` trims trailing slashes so `/` ranks lowest).
Router-agnostic via `register`/`unregister` callbacks; first rule creates+publishes the
listener, last-removed closes+withdraws it (withdraw uses the entry's stored host).
First TLS ingress on a shared `host:443` sets the cert. `clientconduit` holds one Mux
per conduit (lazy `mux()`); `Serve`/`Emulated` kept as a thin single-ingress adapter so
existing callers/tests are unchanged.

Files touched:
- `pkg/ingressemu/ingressemu.go` — new `Mux`/`NewMux`/`Add`, `muxEntry`/`ingressRule`,
  `attachLocked`/`detachLocked`/`entryHandler`/`entryKey`/`longestMatch`/`matchLen`,
  extracted `buildTLSConfig`; `Serve`/`Emulated` re-expressed over `Mux`.
- `pkg/clientconduit/clientconduit.go` — `ingressMux`+`ingressMuxOnce` fields, lazy
  `mux()` wired to `router.RegisterLocal/UnregisterLocal`, `addEmulatedIngress` now
  calls `mux().Add` with cleanup on ctx.Done (removed the per-listener register loop);
  import `cornus/pkg/memlisten`.
- `pkg/ingressemu/ingressemu_test.go` — `TestMuxLongestMatchRouting`,
  `TestMuxExactBeatsPrefixOnEqualLength` (+ `recordingMux`/`getVia` helpers).
- `e2e/scenarios/socks5-ingress-longest-match.{star,yaml}` — new scenario; added to
  Makefile `SCENARIOS`.

Verification: `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...`, `-race` on
`pkg/ingressemu`+`pkg/clientconduit`, and `cornus-e2e --check` on the new scenario — all
green. The E2E scenario was not executed here (no Docker/kind in this environment); run
`make e2e-docker` (or the kube target) for a live pass. Working-tree only, no commit.

## Compose bind mount to a missing host source: auto-create (Docker create_host_path parity)

Symptom: `cornus compose up` against a docker-backend server failed the `init`
service with a server-side `deploywire: kernel-9p mount "m2": connection reset by
peer`. `m2` was the `~/.aws/` read-only bind, and that directory did not exist on
the client host (the other three init binds did).

Root cause (client-side, backend-agnostic): for a caller-local bind source that
does not exist, `client.DeployAttach` registered the absent path as the 9P export
root. `serveOne9P` -> `confinedAttacherCounted` calls `filepath.EvalSymlinks(root)`
(`pkg/wire/confinedfs.go`), which returns ENOENT, so the caller closed the 'L'
stream and the server's `mount(2)` surfaced the closed peer as ECONNRESET — an
opaque failure. Docker never hit this because the daemon (and Compose, via
`bind.create_host_path`, default true) auto-creates a missing bind source.

Fix: match Docker. Extracted the localDirs/LocalMounts construction into
`resolveLocalMounts` (`pkg/client/client.go`); a missing bind source is now created
as an empty directory before it is served, unless the mount carries
`NoCreateHostPath` (compose `bind.create_host_path: false`), in which case it is
left absent and the server-side attach fails as Docker does. Plumbed
`create_host_path` end to end: compose `Volume.NoCreateHostPath` (parsed via a
`*bool` so an explicit `false` is distinguishable from absent) ->
`api.Mount.NoCreateHostPath` -> `resolveLocalMounts`.

Files touched:
- `pkg/client/client.go` — new `resolveLocalMounts`; auto-create gated on
  `!m.NoCreateHostPath`.
- `pkg/api/deploy.go` — `Mount.NoCreateHostPath`.
- `pkg/compose/types.go` — `Volume.NoCreateHostPath` + long-form `bind.create_host_path` parse.
- `pkg/compose/project.go` — carry `NoCreateHostPath` into `api.Mount`.
- `pkg/client/client_test.go` — `TestResolveLocalMountsCreatesMissingBindSource`,
  `TestResolveLocalMountsNoCreateHostPath`.
- `pkg/compose/compose_test.go` — `TestVolumeCreateHostPath`.
- `e2e/scenarios/deploy-mounts-create-host-path.star` — new kube scenario (missing
  local_mount source is auto-created, served over 9P, pod write propagates back);
  added to Makefile `SCENARIOS`.

Verification: `gofmt -l`, `go build ./...`, `go vet ./pkg/{api,compose,client}`,
`go test ./...`, and `make e2e-check` — all green. The E2E scenario was not executed
here (no kind cluster in this environment); run `make e2e-kube` for a live pass.
Working-tree only, no commit.

## Unprivileged `cornus serve` cannot build; delegate to a builder over a raw relay

Reported symptom: with `CORNUS_DEPLOY_BACKEND=docker`, a non-root `cornus serve
--data-dir /tmp/data` failed every build with `failed to read dockerfile: lstat
<data>/buildkit/runc-stargz/snapshots/snapshots/5/Dockerfile: permission denied`,
suggesting "the docker backend requires root".

Two independent causes, neither related to the deploy backend (the in-process
engine is constructed the same way for every backend):

1. **Stale root-owned snapshots.** `/tmp/data/.../snapshots/snapshots/` held 72
   dirs owned `root:root` mode `0700` from an earlier privileged run. The
   native/overlay snapshotter creates these `0700`, so once root populates a data
   dir an unprivileged server can no longer traverse it — the reported `lstat:
   permission denied`. A data dir is effectively owned by the uid that created it.

2. **The engine cannot run unprivileged at all.** Reproduced on a fresh data dir:
   - BuildKit's `client/solve.go` `prepareSyncedFiles` unconditionally rewrites every
     local mount's uid/gid to `0/0` (`resetUIDAndGID`).
   - The receiver (`source/local/source.go`) only maps them back when the worker has
     a non-nil `IdentityMapping`; `engine_linux.go` passes `nil`.
   - So fsutil's `rewriteMetadata` calls `os.Lchown(path, 0, 0)` -> EPERM. Verified
     the syscall semantics directly: `lchown(f, self, self)` succeeds, `lchown(f, 0, 0)`
     does not.
   - Supplying an `IdentityMapping` (spike) removed the `lchown` failure and then hit
     `failed to mount ...: operation not permitted` — `mount(2)` needs `CAP_SYS_ADMIN`.
     **An idmap alone is therefore not a viable unprivileged mode**; only a user
     namespace is, and this host blocks it (`kernel.apparmor_restrict_unprivileged_userns=1`,
     Ubuntu 24.04; `newuidmap`/`newgidmap` absent though `/etc/subuid` is configured).
   - `--rootless` does not help: it sets BuildKit's rootless flag and `processMode` but
     never creates a namespace. Upstream rootless works because rootlesskit puts the
     process in a userns where it *is* uid 0.

Fix implemented: delegate builds to a privileged cornus (typically a local
container) via `--builder-url` / `CORNUS_BUILDER_URL`.

The key simplification: the entire buildwire protocol — the yamux session, the
control stream, and the caller's 9P export — rides one WebSocket, so the server
relays the **raw** connection (`wire.AcceptConn` + `wire.DialConn` + `wire.Pipe`)
before yamux. No 9P proxy is needed, and the builder terminates 9P against the
caller's own export, so the caller's context never lands on the delegating host.
Authorization is enforced before any delegation.

Trap worth remembering: a builder container started without `--storage` defaults
to host-native re-export and tries to load the image into a Docker daemon it does
not have — the build fully succeeds and then dies at export with `failed to copy to
tar: read/write on closed pipe`.

Files touched:
- `pkg/server/build_relay.go` — new; `builderAttachURL`/`builderHTTPBase`
  normalization, `relayBuildAttach` (raw splice), `relayBuildPost` (streaming forward).
- `pkg/server/build_attach.go` — delegate before the upgrade (after authz).
- `pkg/server/build.go` — delegate before extracting the context tar.
- `pkg/config/config.go` — `Config.BuilderURL`.
- `cmd/cornus/serve.go` — `--builder-url` / `CORNUS_BUILDER_URL`.
- `pkg/server/build_relay_test.go` — URL normalization, raw-splice round trip,
  authz-denied-before-upstream, POST forwarding.
- `docs/reference/server-env-vars.md` — "Delegating builds to a builder".

Verification: `gofmt -l`, `go build ./...`, `go vet`, `go test ./...` all green.
End-to-end proven live: a non-root (uid 1000) `cornus serve --builder-url` relayed
to `docker run --privileged --network host <cornus image> serve --addr 127.0.0.1:5099
--storage ...`; `RUN` steps executed and the image pushed. Working-tree only, no commit.

Not done: builder-container lifecycle management (`cornus builder create/rm`,
auto-spawn). Deliberately left out — auto-spawning a `--privileged` container as a
silent fallback is a security decision, not just UX, and should be opt-in.

## Transparent containerized builder: auto-spawn on first build

Follow-up to the delegation work above. The relay made an unprivileged server
able to build, but only if the operator stood a builder up by hand. This closes
that gap: `cornus serve` now spawns and manages the builder itself.

Design points worth keeping:

- **Capability is probed, not inferred.** `builderctr.CanMount()` attempts a real
  bind mount of a temp dir onto itself and undoes it. euid is the wrong question:
  a process can be root yet blocked (seccomp, restrictive container) or non-root
  yet capable (CAP_SYS_ADMIN, or root inside a userns — exactly how rootless
  BuildKit works). This probes the very syscall BuildKit needs.
- **Lazy, and latched.** Resolution happens on the first build, not at startup, so
  a server that never builds never starts a container. The outcome — including
  failure — is cached, so a broken Docker setup is not retried per request.
- **Adopt before create.** `Ensure` first probes the builder's address; a builder
  already serving (from a previous run, or hand-started) is reused without
  touching Docker. Verified: restarting the server reused the container in ~1ms
  and did not create a second one, keeping the build cache warm.
- **Cannot regress a working host.** The auto path engages only when
  `CanMount()` is false, i.e. only where every build would otherwise fail.
- **Zero-value config stays off.** `--builder-auto` defaults to true at the CLI,
  but `config.Config{}.BuilderAuto` is false. That asymmetry is deliberate: tests
  construct servers from a bare `config.Config`, and `go test ./...` must never
  reach a Docker daemon (`TestZeroConfigNeverStartsContainer` pins this).

The builder is the same cornus image (`--builder-image` overrides), named
`cornus-builder`, run `--privileged` with host networking (so refs like
`localhost:5000/app` mean the same inside and out) and a dedicated
`cornus-builder-cache` volume (never the server's data dir — the builder runs as
root and would leave root-owned 0700 snapshots behind).

Files touched:
- `pkg/build/builderctr/` — new package: `Ensure`/`Remove`, a minimal Docker
  Engine API client (unix + tcp DOCKER_HOST), `CanMount` probe (linux + stub).
- `pkg/server/build_relay.go` — `resolveBuilder` (explicit URL > auto > in-process).
- `pkg/server/{build.go,build_attach.go}` — resolve before upgrade / before tar extract.
- `pkg/server/server.go` — latched builder resolution state.
- `pkg/config/config.go`, `cmd/cornus/serve.go` — `BuilderAuto`, `BuilderImage`.
- `pkg/server/build_relay_test.go`, `pkg/build/builderctr/builderctr_test.go`.
- `docs/reference/server-env-vars.md`.

Verification: `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...`,
`make e2e-check` all green. Live: a non-root server started with NO builder flags
spawned the container on first build, relayed, `RUN` steps executed, image pushed;
a restart adopted the same container. Working-tree only, no commit.

## 2026-07-24 — Docs site: Topics folded into Guides (one page per feature)

The VitePress site had grown a 1:1 shadow taxonomy: every one of the seven
`docs/topics/*` pages had a `docs/guides/*` counterpart on the same feature, so a
reader saw both "Tunnels" and "Public tunnels" in the same sidebar. The
recipe-vs-concept boundary had also stopped being maintained and had inverted
between pairs (`guides/tunnels.md` 12 KB vs `topics/tunnels.md` 2.5 KB, while
`topics/ingress.md` 12 KB vs `guides/ingress.md` 5 KB). Topics is gone; the site
is now one page per feature, six sidebar sections instead of seven.

Merge map: tunnels/ingress/egress/credentials folded into their `guides/`
namesakes; `topics/auth-and-tls.md` into `guides/security.md` (retitled "Security
and authentication"); `topics/hub.md` plus the two hub sections lifted out of
`guides/networking.md` became a new `guides/hub.md`; `guides/networking.md`
retitled "Networking and conduits". Each merged page is now
`# Title` -> intro -> `## How it works` -> recipe H2s -> See also.

`topics/remote-workflows.md` was a **redistribution, not a merge** — its ~46
inbound links cited it for four different reasons (general remote model, session
conduits, remote builds, client-local mounts), each needing a different target
(`guides/remote-clusters`, `guides/networking`, `guides/building-images`,
`guides/deploying-workloads`). A blanket sed would have been wrong.

Three findings worth keeping:

1. **Self-links.** ~15 inbound links lived in pages that were themselves merge
   targets, so after the merge they pointed at the page they were on. They had to
   be deleted, not rewritten. A second variant showed up only after the sed pass:
   `architecture/security.md` and `architecture/networking.md` each listed two
   differently-named links that collapsed onto the same merged target, leaving
   visible duplicates in their "Related pages" lists.

2. **`npm run docs:build` is NOT a complete anchor oracle.** It passed green while
   `ja/cookbook/remote-dev-environment.md` linked to a non-existent Japanese
   anchor. VitePress did not flag the bad non-ASCII cross-page fragment. A
   throwaway checker that extracts every `id="…"` from `.vitepress/dist/**/*.html`
   and validates every `](/path#anchor)` in the Markdown against it found 3 real
   breakages this change introduced (plus 8 pre-existing ones in ja/zh
   `architecture/*`, `guides/observability`, and `reference/deploy-spec` that are
   still open). Worth rebuilding that check whenever a page with translated
   headings is moved or renamed.

3. **`public/` stub ordering.** The retired `https://cornus.dev/topics/*` URLs are
   preserved by static meta-refresh files under `docs/public/<locale>/topics/`,
   which keeps them out of the sidebar, the local search index, and the dead-link
   check. But `public/ja/topics/x.html` and `docs/ja/topics/x.md` render to the
   same `dist` path — add a locale's stubs only after deleting that locale's
   markdown, or they collide.

Also fixed en/ja/zh drift found while merging: the tunnels guide's SSH backend
was missing `--forward-agent` in all three locales and still carried a stale "no
SSH agent integration" bullet. `sidebarMap()` was additionally scoped so each
section prefix expands only its own section (`SECTIONS` must stay index-aligned
with `TREE`; `NAV` indices are positional and needed renumbering).

Verification: `npm run docs:build` green; no residual `/topics/` links and no
self-links in any locale; 21 redirect stubs present in `dist`; sidebar confirmed
collapsed-except-active in en and zh; `audit_markdown_translation.py` passes for
all 12 changed pages in both ja and zh (review warnings only). The full-tree audit
still reports 12 pre-existing structural errors in files this change only
link-edited (verified: 0 heading/fence lines touched in any of them).

## Builder image is built from the running binary, not pulled

Refinement of the auto-spawn work above. The builder image was the published
`ghcr.io/moriyoshi/cornus:latest`, which has two problems: it needs registry
access, and it can be a different cornus than the server that spawned it. The
builder image is now built from the RUNNING binary.

How: the server streams a build context (a generated Dockerfile plus its own
executable, read via `os.Executable`) to the Docker daemon's `POST /build` and
tags the result `cornus-builder:<binary-sha256[:12]>`. Verified that endpoint
still works on Docker 29 despite the classic builder's deprecation.

Details that matter:

- **Content-addressed tag.** The tag is the binary's hash, so an upgraded cornus
  builds a new image and an unchanged one reuses it. Verified: the tag equalled
  `sha256sum <binary> | cut -c1-12`, and a second run reused the identical image
  ID (3s vs ~22s for the first).
- **Base image matches the HOST distribution** (parsed from `/etc/os-release`,
  e.g. `ubuntu:24.04`). A locally built cornus is dynamically linked against the
  host glibc — confirmed with `file` — so a fixed minimal or musl base would fail
  to exec. `--builder-base-image` overrides; `FallbackBaseImage` covers unknown
  distros (fine for a static release binary).
- **runc is installed into the base if absent.** BuildKit shells out to it for
  every RUN; the published image installs it for the same reason.
- **The context is streamed, not buffered** (`io.Pipe` + `archive/tar`): the
  binary is >100MB. `io.CopyN` bounds the copy to the header's size so a binary
  replaced mid-copy cannot desynchronize the tar.
- **A failed build returns HTTP 200.** The failure appears only inside the JSON
  build stream, so `buildStreamErr` parses it; skipping that would treat a broken
  image as ready.
- **Recursion guard is an ENV, not a flag.** The builder gets
  `CORNUS_BUILDER_AUTO=false`. A flag would break a pinned OLDER published image,
  which would reject an unknown flag and fail to start; an unknown env var is
  ignored. (It is belt-and-braces anyway: the builder is privileged, so its own
  mount probe succeeds and auto never engages.)
- `--data-dir` is passed explicitly in the container's Cmd, before the
  subcommand (it is a global flag), so a self-built image works without relying
  on the published image's `ENV CORNUS_DATA`.

Files touched:
- `pkg/build/builderctr/selfimage.go` — new; self-image build, host base
  detection, content-addressed tag, streamed context, build-stream error parsing.
- `pkg/build/builderctr/builderctr.go` — `Options.BaseImage`; `Image` now means
  "pin a published image" and empty self-builds; `PublishedImage` (was
  `DefaultImage`) is no longer a default.
- `pkg/config/config.go`, `cmd/cornus/serve.go` — `BuilderBaseImage` /
  `--builder-base-image`.
- `pkg/build/builderctr/builderctr_test.go`, `docs/reference/server-env-vars.md`.

Verification: full gate green. Live: a non-root server with NO builder flags
built `cornus-builder:8c23738f1f71` from itself, started it, relayed the build,
`RUN` executed, image pushed; restart reused the image.

Process note: a malformed kong struct tag (missing closing quote) panicked only
at CLI parse time. `go test ./cmd/cornus/` DOES catch it (those tests construct
the kong parser) — it slipped through because the suite was not re-run after that
particular patch. Re-run the gate after every edit, not just at the end.

## Translation sync: builder-delegation docs (ja + zh)

Synced `docs/ja/reference/server-env-vars.md` and `docs/zh/reference/server-env-vars.md`
with the English builder-delegation additions (four `CORNUS_BUILDER_*` rows and the
"Delegating builds to a builder" subsection).

Findings worth keeping:

- **Translating surfaced two defects in the English source**, both fixed there
  first so the translations stayed faithful: the log excerpt still read "starting
  a containerized builder" after the code was changed to "using", and a "Two
  things to get right" lead-in sat above three bullets. Re-reading a page in
  order to translate it is a decent proofreading pass in its own right.
- **Explicit heading anchors keep cross-locale links stable.** The subsection uses
  `{#delegating-builds-to-a-builder}` in all three locales, so the table row's
  `(#delegating-builds-to-a-builder)` link works identically everywhere instead of
  depending on a slugified Japanese or Chinese heading.
- **Punctuation convention is per-locale, not global**: the ja page uses
  half-width parens with surrounding spaces, the zh page full-width `（）` with
  none. Both were matched rather than normalized.
- **The audit's two warnings on this page are pre-existing and legitimate.** The
  first inline-code divergence is at index 0 — the intro paragraph, untouched by
  this change — where ja/zh naturally reorder `cornus serve` and `CORNUS_*`.
  Confirmed the added sections match the source exactly (25 inline-code spans, 2
  fenced blocks, 3 bullets, 1 heading, identical sequence). Do not "fix" that
  warning by contorting the intro.

Both locale pages are otherwise mid-translation by concurrent work in this tree
(many untranslated fragments remain, e.g. a `Meaning` table header and
`## 認証 and API ポリシー` in ja, `## Build engine` in zh). Those were left alone.

Verification: structural audit passed for both locales; `npm run docs:build`
completed with no dead links; `git diff --check` clean. New terms recorded in
`.agents/docs/JA_TRANSLATION_GLOSSARY.md`.

## Work summary: unprivileged builds via a containerized builder (consolidated)

Consolidates the four entries above — "Unprivileged `cornus serve` cannot build",
"Transparent containerized builder", "Builder image is built from the running
binary", and "Translation sync" — into one arc. Those entries keep the detail;
this one records what is worth carrying forward.

### The reported problem was not the reported problem

Symptom: `CORNUS_DEPLOY_BACKEND=docker` + a non-root `cornus serve` failed every
build with `lstat .../snapshots/5/Dockerfile: permission denied`, read as "the
docker backend requires root". The deploy backend was irrelevant — the in-process
engine is constructed identically for every backend. There were two independent
causes, and the visible one was the less interesting:

1. `/tmp/data` held 72 snapshot dirs owned `root:root` mode `0700` from an earlier
   privileged run. **A build data dir is effectively owned by the uid that created
   it**; mixing uids in a shared path like `/tmp/data` poisons it silently.
2. The engine cannot run unprivileged at all (below).

### Why unprivileged builds are impossible without a user namespace

- BuildKit's `client/solve.go` `prepareSyncedFiles` unconditionally rewrites every
  local mount's uid/gid to `0/0`; the receiver only maps them back when the worker
  has a non-nil `IdentityMapping`, and cornus passed `nil` — so fsutil called
  `os.Lchown(path, 0, 0)` and got EPERM.
- **Supplying an `IdentityMapping` fixes only that first EPERM.** A spike proved
  the build then dies at `mount(2): operation not permitted`. This is the load-
  bearing result: an idmap is not a viable "unprivileged mode", it just moves the
  failure. Only a user namespace grants both.
- `--rootless` is a misnomer: it sets BuildKit's rootless flag and `processMode`
  but never creates a namespace. Upstream rootless works because rootlesskit puts
  the process in a userns where it genuinely is uid 0. On hardened hosts
  (`kernel.apparmor_restrict_unprivileged_userns=1`, Ubuntu 24.04) a namespace is
  unavailable anyway, so this cannot be solved in-process on such a host.

### The design that worked, and the insight that made it cheap

Delegate to a privileged cornus container. The expensive-looking part — forwarding
the caller's build context — turned out to be free: **the entire buildwire
protocol (yamux, control stream, and the caller's 9P export) rides one WebSocket**,
so the server splices the RAW connection (`wire.AcceptConn` + `wire.DialConn` +
`wire.Pipe`) before yamux. No 9P proxy, and the builder terminates 9P against the
caller's own export, so contexts never land on the delegating host. Roughly 130
lines. Authorization is enforced before delegation, with a test proving a denied
identity never reaches the builder.

The builder image is built from the running binary (`POST /build`, tagged
`cornus-builder:<binary-sha256[:12]>`), so it is byte-identical to the server,
needs no registry, and cannot drift in version.

### Durable, reusable findings

- **Probe capabilities, do not infer them from uid.** A process can be root yet
  blocked (seccomp, restrictive container) or non-root yet capable (CAP_SYS_ADMIN,
  or root in a userns). `CanMount()` attempts a real bind mount.
- **A fallback that only engages where the feature is already impossible cannot
  regress anything.** That property is what made auto-spawn safe to default on.
- **Prefer an ENV over a flag for cross-version instructions.** An older pinned
  image rejects an unknown flag and fails to start; it ignores an unknown env var.
- **`POST /build` returns HTTP 200 for a FAILED build** — the error is only inside
  the JSON stream. Still true on Docker 29, where the classic builder also still
  works.
- **A builder container needs explicit `--storage`.** Otherwise it defaults to
  host-native re-export and tries to load into a Docker daemon it does not have:
  the build fully succeeds and dies at export with `failed to copy to tar:
  read/write on closed pipe`.
- **Match the self-built image's base to the host distro** (`/etc/os-release`): a
  locally built cornus is dynamically linked against the host glibc.
- **Zero-value config must stay inert.** `--builder-auto` defaults true at the CLI
  but `config.Config{}.BuilderAuto` is false, because tests build servers from a
  bare config and `go test ./...` must never reach a Docker daemon.

### Process lessons

- **A local `cornus build` that "succeeds" may not be local.** Early runs appeared
  to disprove the bug; they were silently routed to a remote server by the config's
  `current-context`, and `--data-dir`/`CORNUS_DATA` were ignored on that path. Only
  an explicit `--builder` at a known-unprivileged server reproduced it. Verify
  which engine actually ran before drawing conclusions.
- **Re-run the gate after every edit, not just at the end.** A malformed kong
  struct tag (missing closing quote) panicked only at CLI parse time;
  `go test ./cmd/cornus/` does catch it, but the suite had not been re-run after
  that patch.
- **Translating a page proofreads it.** The ja/zh sync surfaced two defects in the
  English source (a stale log excerpt, and "Two things" above three bullets).

### State and what is deliberately not done

Working tree only, no commits. Full gate green throughout: `gofmt`, `go build
./...`, `go vet ./...`, `go test ./...`, `make e2e-check`, plus `npm run
docs:build` for the docs. Verified live end to end, including auto-spawn from a
server with no builder flags and image reuse across a restart.

Not done, on purpose:

- **No `cornus builder create/ls/rm` CLI.** Lifecycle is implicit (lazy start,
  adopt on restart). A management surface is the obvious next increment.
- **No cleanup of stale `cornus-builder:<hash>` images.** Every upgraded binary
  leaves its predecessor's image behind; a prune path is missing.
- **`docs/ja` and `docs/zh` remain mid-translation** by concurrent work; only the
  builder sections were translated here.

## Docs site follow-up: propagating the *prose* half of a link restructure

(Follow-up to "Docs site: Topics folded into Guides" above — not to the builder
work immediately preceding it; concurrent appends landed between the two.)

The first pass propagated link **targets** into ja/zh correctly but missed the
**prose and list-structure** edits made alongside them. Both trees ended up with
link text still naming retired pages, and with "Related pages" lists where two
differently-labelled entries had silently collapsed onto the same target.

Two defects survived in **English** too, so this was not purely a translation gap:
`cli/tunnel.md` still said `[Public tunnels — Backends](/guides/tunnels)` (retired
page name, and the `#backends` anchor had been dropped), and
`cookbook/ai-agent-egress.md`'s See-also listed `/guides/egress` and
`/guides/credentials` twice each — the topic link and the recipe link had become
the same page.

Same shape in `architecture/{caretaker,build-engine,deploy-engine}.md`, whose
"Related pages" lists each carried two bullets pointing at one merged page
(`- [Egress](/guides/egress) and the [egress guide](/guides/egress).` reads as
nonsense once both halves resolve identically). Also `reference/helm-values.md`,
`cookbook/microservices-hub.md`, `cookbook/preview-environments.md`,
`guides/registry.md`, and five ja reference pages still labelled the security
guide `[Auth and TLS]` — the retired title, left untranslated.

**Two cheap checks that catch this whole class, neither of which the VitePress
build performs:**

1. *Duplicate-target check* — parse every contiguous bullet list and every single
   line, collect `](/path#anchor)` destinations, flag any that repeat. Counting
   the anchor as part of the identity avoids false positives on legitimate
   "page + page#section" pairs. This found all the collapsed-list defects.
2. *Cross-locale entry-count parity* — for each `## Related pages` / `## See also`
   section, compare the `^- [` bullet count across en/ja/zh. A mismatch means a
   list was restructured in one locale only. This is what surfaced the ja/zh
   lists still holding the pre-merge entry count.

Also worth grepping after any page rename: the retired page's **title string** as
link text (`[Public tunnels]`, `[Remote workflows]`, `[Networking recipes]`, …)
across all locales. Repointing the href is only half the edit.

Final state: 0 residual `/topics/` links, 0 duplicate targets, 0 stale
retired-page link text in any locale; Related-pages entry counts match across
en/ja/zh for all six restructured architecture/reference pages; `docs:build`
green. The full-tree translation audit is down from 12 pre-existing structural
errors to 10 (two were fixed as a side effect); the remaining 10 and the 8 broken
cross-page anchors are pre-existing and untouched by this work.

## Two silent-failure fixes: unrecognized deploy backend, unwritable registry store

Both came out of diagnosing a `500 Internal Server Error` on every blob PUT after
a delegated build finally succeeded. Neither was a regression from the builder
work — the build was fine; the push was not.

### The diagnosis

`/tmp/data/blobs/sha256/` was `drwxr-xr-x root root` from an earlier privileged
run, and the failing digests needed shard dirs that did not exist yet, so the
registry had to `mkdir` inside a root-owned directory as uid 1000. Confirmed
directly: `mkdir /tmp/data/blobs/sha256/zz-probe` → `Permission denied`. Same
root-owned-data-dir problem as the original build failure, just moved downstream
from the snapshotter to the registry once builds started working.

### Fix 1: unrecognized `CORNUS_DEPLOY_BACKEND` is now a startup error

The reporter ran `CORNUS_DEPLOY_BACKEND=docker`. That is not a backend name.
`defaultBackendFactory`'s switch falls through to dockerhost for anything
unrecognized, so the RIGHT backend was selected — but `isHostBackend` matches
only the exact names `""`, `dockerhost`, `containerd`, so the typo ALSO flipped
the registry from host-native re-export to a classic CAS, with no diagnostic.

That combination is the dangerous part: right backend, wrong registry semantics.
In proper `dockerhost` mode the server sets `push=false` and `docker load`s the
result straight into the daemon, never touching the blob store; in CAS mode it
pushes blobs — which is why the root-owned shard dirs were reached at all. A
misspelled backend must not silently change what the registry does, so
`validateDeployBackend` now rejects unknown values. It is called from
`resolveRegistrySource` because both startup entry points (`server.New` and
`RegistryKeepsNoContentStore`, which `cmd/cornus serve` calls first) funnel
through it, so nothing can slip past either.

### Fix 2: an unwritable registry store reports itself

A permission failure surfaced as a bare `500 UNKNOWN` carrying only a raw syscall
string — and push clients retry 5xx with backoff, forever, since the condition is
permanent. `writeStoreUnwritable` now detects `fs.ErrPermission` on the write
paths (blob chunk write, blob commit, manifest PUT) and answers `503 UNAVAILABLE`
naming the effective uid, plus a server-side ERROR log. 503 rather than 500
because this is a server-state problem, not a failed operation, and it stays
distinguishable in logs and metrics from genuine internal errors.

Verified the error chain first: the storage layer returns these bare
(`return err`), so the `*os.PathError` wrapping `EACCES` survives and
`errors.Is(err, fs.ErrPermission)` fires. Had any layer wrapped with `%v` the
check would have silently never matched.

Live output now:

```
503 {"errors":[{"code":"UNAVAILABLE","message":"registry storage is not writable
by this server (uid 1000): mkdir .../blobs/sha256: permission denied — the data
directory is likely owned by another user; ..."}]}
```

Files touched:
- `pkg/server/server.go` — `knownDeployBackends`, `validateDeployBackend`, called
  from `resolveRegistrySource`.
- `pkg/registry/registry.go` — `writeStoreUnwritable`, wired into `blobPutError`,
  the upload write paths, and `PutManifest`.
- `pkg/server/deploy_backend_name_test.go`, `pkg/registry/unwritable_store_test.go`
  (the latter reproduces the root-owned data dir WITHOUT root, by making the blob
  root read-only for its own owner — same EACCES from the same MkdirAll; it skips
  under root, which ignores directory permissions).
- `docs/reference/server-env-vars.md` — documented `CORNUS_DEPLOY_BACKEND`.

Verification: full gate green. Live: `CORNUS_DEPLOY_BACKEND=docker` now exits with
`unknown CORNUS_DEPLOY_BACKEND "docker" (want one of: ...)`; valid names still
start; a real push against an unwritable store returns the 503 above.

Follow-up not done: a startup writability preflight would catch this before the
first push rather than at it. Deliberately skipped — a server that only ever
serves reads should not fail to start.

## Work summary: docs site Topics→Guides restructure (consolidated, 2026-07-24)

Consolidates the two entries above — "Docs site: Topics folded into Guides (one
page per feature)" and "Docs site follow-up: propagating the *prose* half of a
link restructure". Those keep the detail and the reasoning; this is the scope,
the verification, and what is left open.

### What and why

The VitePress site had a 1:1 shadow taxonomy: all seven `docs/topics/*` pages had
a `docs/guides/*` counterpart on the same feature, so the sidebar showed both
"Tunnels" and "Public tunnels". The recipe-vs-concept boundary had stopped being
maintained and had inverted between pairs. Topics is gone; the site is one page
per feature, six sidebar sections instead of seven, and the sidebar now expands
only the reader's current section (it previously rendered all ~60 links on every
page).

### Scope

- **Merged**: `topics/{tunnels,ingress,egress,credentials}` into their `guides/`
  namesakes; `topics/auth-and-tls` into `guides/security` (retitled "Security and
  authentication"); `topics/hub` + the hub sections of `guides/networking` into a
  new `guides/hub`; `guides/networking` retitled "Networking and conduits".
- **Redistributed** (not merged): `topics/remote-workflows` across
  `guides/{remote-clusters,networking,building-images,deploying-workloads}`.
- **Config**: `.vitepress/config.mts` — Topics section dropped from `TREE`, new
  `SECTIONS` array (must stay index-aligned with `TREE`), `NAV` indices
  renumbered, `sidebarFor()` takes an active-section argument.
- **Retired URLs**: 21 meta-refresh stubs under `docs/public/{,ja/,zh/}topics/`.
- **All of the above ×3 locales**, plus `README.md`, `docs/README.md`,
  `.agents/skills/distill-memories/SKILL.md`, and
  `.agents/docs/LTM/user-reference-docs-site.md`.

Roughly 150 files. No commits made; working tree only.

### Verification performed

| Check | Result |
| --- | --- |
| `npm run docs:build` | green |
| Residual `/topics/` links, all locales | 0 |
| Self-links (page linking to itself) | 0 |
| Duplicate link targets in bullet lists / See-also lines | 0 |
| Stale retired-page link *text* | 0 |
| Related-pages entry-count parity en/ja/zh | matches on all 6 restructured pages |
| Redirect stubs present in `dist` | 21/21 |
| Sidebar collapsed-except-active | confirmed in en and zh |
| `audit_markdown_translation.py`, changed pages | passes, ja and zh (review warnings only) |

### Cross-cutting lesson

Every defect that survived a pass was of one shape: **a link whose destination was
correct but whose surrounding text or list position was not.** Repointing an href
is only half the edit. The three checks that actually caught things — none of
which the VitePress build performs — are the anchor validator, the
duplicate-target check, and cross-locale entry-count parity; all three are
described in the two entries above. Worth reaching for on any page rename or
merge, not just this one.

### Deliberately not done

Pre-existing defects found while working, all in files this change only
link-edited (verified: 0 heading or fence lines touched in any of them), now
tracked in TODO.md: 10 structural translation errors, 8 broken cross-page
anchors, and a missing IngressCertificate section in
`docs/ja/reference/connection-config.md`.

## Work summary: diagnosability fixes after the builder work (consolidated)

Continues "Work summary: unprivileged builds via a containerized builder
(consolidated)" above. That entry ends with builds working on an unprivileged
host; this one covers what happened next and what it taught.

### One poisoned data dir, three failures at three layers

A single earlier `sudo cornus serve` against `/tmp/data` produced three distinct
symptoms, each of which read like a separate bug:

1. `lstat .../buildkit/.../snapshots/5/Dockerfile: permission denied` — the
   snapshotter's `0700` dirs, root-owned.
2. Once builds were delegated to a container and started succeeding, the SAME
   cause reappeared one layer down as `500 Internal Server Error` on every blob
   PUT: `blobs/sha256/` was root-owned, so creating a shard failed with EACCES.
3. It will keep resurfacing at any layer that writes under the data dir.

The durable rule: **a cornus data dir is effectively owned by the uid that
created it.** One privileged run permanently poisons it for later unprivileged
use, and nothing in cornus detects or warns about the mismatch. Fixing the
visible symptom just moves the failure downstream.

### The session's real cost was misdiagnosis, not difficulty

Nearly every step was slowed by a failure that pointed confidently in the wrong
direction. Collected, because the pattern is the lesson:

| Symptom | What it suggested | What it was |
| --- | --- | --- |
| `lchown ...: operation not permitted` | the docker deploy backend needs root | BuildKit resets context uid/gid to `0/0`; unrelated to the backend |
| `CORNUS_DEPLOY_BACKEND=docker` accepted | a valid backend name | not a name at all; right backend, registry silently degraded to a CAS |
| `--rootless` changes nothing | the flag is broken | it sets BuildKit's flag but creates no user namespace |
| `failed to copy to tar: closed pipe` | the build failed | the build fully succeeded; the builder had no `--storage` and no Docker daemon to load into |
| a local `cornus build` succeeding | the bug was not real | the CLI silently routed it to a remote server via `current-context` |
| `500 UNKNOWN` on blob PUT | a server bug or a relay regression | EACCES on a root-owned directory |

Two of these were fixed rather than merely understood (see the entry above):
an unrecognized `CORNUS_DEPLOY_BACKEND` is now a startup error, and an unwritable
registry store answers `503 UNAVAILABLE` naming the uid instead of a bare `500`.

### Principle worth carrying forward

**When a component can be misconfigured into a silently-degraded mode, fail
closed at startup rather than degrade.** The backend-name fix is the clearest
instance: the old behavior was not "reject" or "accept", it was "accept the
backend but quietly change the registry's semantics", which is the worst of the
three and produced a failure four layers away from its cause.

The corollary for error paths: an error a client RETRIES must say whether
retrying can ever help. The `500` on a permanent permission failure had push
clients backing off forever against a condition no amount of waiting fixes.

### Verification and state

Full gate green throughout (`gofmt`, `go build ./...`, `go vet ./...`,
`go test ./...`, `make e2e-check`), plus live checks of both fixes: a typo'd
backend now exits at startup with the valid list, valid names still start, and a
real push against an unwritable store returns the 503 with its uid. Working tree
only, no commits.

### Open items, in rough priority order

- **`/tmp/data` on the reporter's host is still root-owned** — needs
  `sudo rm -rf /tmp/data`; not something to do on their behalf.
- **No startup writability preflight.** Would catch the poisoned-data-dir case
  before the first push rather than at it. Skipped deliberately: a read-only
  server should not fail to start over it, so it wants to be a warning.
- **No guard for a uid-mismatched data dir** generally — the rule above is
  documented nowhere in the product, only here.
- **Builder images are never pruned**; each upgraded binary orphans its
  predecessor's `cornus-builder:<hash>`.
- **No builder lifecycle CLI** (`cornus builder ls/rm`).
- **ja/zh reference pages lag the English one** by the `CORNUS_DEPLOY_BACKEND`
  row added here.

## Relay regression: the builder must mirror the delegating server's registry mode

`405 Method Not Allowed` on `POST /v2/<repo>/blobs/uploads/` after a delegated
build. Unlike the earlier `500`, this one WAS a regression from the relay work.

### Cause

The relay splices a build session to the builder verbatim, so the BUILDER decides
how to export the result. The delegating server's own decision —

```go
if s.registrySource == registrySourceDockerDaemon {
    push = false
    dockerArchiveOut, loadWait = s.dockerLoadExport(ctx)  // load into the local daemon
} else {
    target, tags = s.localPushTargets(...)                // push at the registry
}
```

— is bypassed entirely. With a host backend and no `--storage` (the DEFAULT), the
server's registry is a pure host-native re-export: read-only, `405` on any write.
The builder, resolving its own mode and holding its own `--storage`, took the push
branch and pushed straight at that read-only registry.

Notably this only surfaced once the reporter fixed `CORNUS_DEPLOY_BACKEND=docker`
to `dockerhost` — the typo had been keeping them in CAS mode, where pushing is
correct. Fixing one bug exposed the next.

### Why the tests missed it

Every relay test tagged images at `localhost:5099` — the BUILDER's own registry —
so the builder always pushed to itself and the delegating server's mode never
mattered. The real flow tags the delegating server's registry. A test that
exercises delegation but not the delivery destination proves less than it looks.

### Fix

`Options.DockerExport` mirrors the server's resolved mode into the container:

- **re-export mode**: no `--storage`, `CORNUS_DEPLOY_BACKEND=dockerhost`, and the
  host's Docker socket bind-mounted — so the builder resolves to the same
  docker-daemon source and `docker load`s the result into the SAME daemon the
  server would have.
- **CAS mode**: `--storage` as before, which also pins the builder out of
  host-native re-export so it pushes.

Two supporting pieces were needed:

- **A config fingerprint label** (`cornus.builder.config`). A builder is now only
  reused if it was created with the configuration we want; a leftover from the
  other mode is recreated. Without this a stale container silently exports the
  wrong way — exactly the failure being fixed. This also forced reordering
  `Ensure`: the "already serving" fast path now runs only when no MANAGED
  container exists, otherwise it would happily adopt our own stale one.
- **A refusal path.** A `tcp://` DOCKER_HOST cannot be handed to a container as a
  bind mount, and a containerd re-export store cannot be written by a
  containerized builder at all. Both now fail with an explanation naming the
  alternatives (`--storage`, `--builder-url`) instead of starting a builder whose
  results would go nowhere.

Files touched: `pkg/build/builderctr/builderctr.go` (Options.DockerExport,
fingerprint, containerLabel, dockerSocketPath, mode-dependent container spec,
reordered Ensure), `pkg/server/build_relay.go` (pass the mode, refuse containerd),
`pkg/build/builderctr/builderctr_test.go`, `docs/reference/server-env-vars.md`.

Verified live in the reporter's exact configuration (`dockerhost`, no
`--storage`): the build now reports `exporting to docker image format`, finishes
without a 405, and the image is present and runnable in the HOST Docker daemon.
Switching that server to `--storage` recreated the builder (different container
id) with `--storage` and no socket, and pushed. Full gate green.

### Lesson

**Transparent relaying is not free: it moves a decision.** The raw splice was
chosen precisely because it forwards everything without interpretation — but the
export strategy is a decision the delegating server used to make, and handing the
session over hands that decision over too. Any state the original decider used
(here, the registry mode) has to be mirrored explicitly onto the delegate, or the
delegate silently decides differently.

## Work summary: the builder-delegation saga, end to end (consolidated)

Ties together the whole arc — the three "Work summary" consolidations above plus
the relay-regression entry — into the one shape worth remembering. The feature
(an unprivileged `cornus serve` transparently delegating builds to a privileged
container) shipped and works; getting there was a chain of five failures, each
uncovered only by fixing the previous one.

### The failure chain

1. Non-root build → `lchown 0/0: EPERM` (BuildKit zeroes context uid/gid; the
   receiver can't chown to root). Root cause: no user namespace, and none is
   possible on this host.
2. Delegated to a container → `mount(2): EPERM` one layer down (an idmap fixes
   only #1; mount needs CAP_SYS_ADMIN). Resolved by a PRIVILEGED builder.
3. Builder had no `--storage` → build succeeds, dies at export with
   `failed to copy to tar` (no daemon to load into).
4. `CORNUS_DEPLOY_BACKEND=docker` typo accepted → registry silently degraded to a
   CAS; combined with a root-owned `/tmp/data` → `500` on every blob PUT.
5. Typo fixed to `dockerhost` → `405` on push: the read-only re-export registry
   rejects the write the relayed builder was making.

Each symptom pointed at the wrong subsystem (the deploy backend, a broken flag, a
relay regression). Only #5 was actually a regression from this work; #1–#4 were
pre-existing sharp edges the feature merely routed traffic into.

### The two findings that generalize

**Finding A — a data dir is owned by the uid that first created it.** One
privileged run permanently poisons `/tmp/data` for later unprivileged use, and
the failure resurfaces at every layer that writes there (snapshotter, then
registry blob store). Fixing the visible symptom moves it downstream; the only
real fix is `chown`/`rm` the dir, or never mix uids on it. Cornus now names the
uid in the error instead of a bare 500, but does not detect the mismatch up front.

**Finding B — transparent relaying moves a decision.** The raw WebSocket splice
was chosen because it forwards the whole buildwire protocol without interpreting
it — which is exactly why it also forwards away the delegating server's *export
decision*. Whatever state the original decider used (registry mode: push vs
docker-load) must be mirrored onto the delegate explicitly, or the delegate
silently decides differently. This is the general hazard of "just proxy it
through": a proxy that adds no logic still relocates every decision the endpoint
used to make locally.

### The meta-lesson for this kind of work

**Fail closed on misconfiguration; degrade nowhere.** Two of the five failures
(#4's silent CAS downgrade, #5's silently-miswired builder) were "accepted the
input but quietly did something different", which is strictly worse than either
rejecting or honoring it, because the consequence lands far from the cause. Both
are now hard errors or fingerprint-driven recreations. When a component can enter
a silently-degraded mode, that mode is a bug, not a feature.

### Net state

Feature complete and verified live in the reporter's own configuration.
Everything landed working-tree-only, no commits, gate green throughout. Standing
open items (unchanged from the prior summary): the reporter's `/tmp/data` still
needs `sudo rm -rf`; no startup writability/uid preflight; no builder-image
pruning; no builder lifecycle CLI; ja/zh reference pages lag the English one by
the builder sections and the `CORNUS_DEPLOY_BACKEND` row.

## 2026-07-24 — Compose `provider:` service support (external provider plugins)

Added full runtime support for the compose-spec `provider:` feature to `cornus
compose`. Previously a `provider:` block was an unrecognised service key: it
parsed, warned (`field "provider" ... ignored`), and left an image-less spec
that only failed downstream at deploy. Now a provider service delegates its
lifecycle to an external plugin instead of being built/deployed.

- **Model** (`pkg/compose`): `Provider`/`ProviderOptions`/`ProviderOption` in
  `types.go` with a custom `UnmarshalJSON` that flattens the `options:` map into
  sorted, deterministic `--key=value` flags (scalar -> one flag, list -> repeated;
  numbers stringify without a trailing `.0`). Added the field to
  `ServiceDocument`/`Service` (+ `serviceFromDocument`, `toDocument`, `mergeService`),
  `"provider"` to `supportedServiceFields`, `ProviderPlan` on `ServicePlan`, and a
  `translateService` short-circuit that rejects provider+image/build/deploy and
  requires `provider.type`.
- **Runner** (`composecli/provider.go`): `resolveProviderBinary` (docker-<type>
  then <type> on PATH), `providerRunner.run` (invokes `<bin> compose
  --project-name=<p> up|down [flags] <svc>` with an injectable exec seam), and the
  pure `parseProviderStream` decoding the newline-delimited JSON protocol
  (`info`/`debug`/`error`/`setenv`/`rawsetenv`). `setenv` is prefixed with the
  upper-cased, `[^A-Z0-9_]`->`_`-normalised service name; `rawsetenv` passes through.
- **Wiring** (`composecli`): per-`up` state behind `runtime.providers *providerState`
  (pointer so `reloadAndReconcile`'s `tmp := *rt` shallow copy stays lock-free /
  vet-clean). `runForeground`/`upDetached` run the plugin `up` inside the
  per-service errgroup; dependents gate on a per-provider readiness channel via a
  new `waitForDependencies` branch; `serviceSpec`->`injectProviderEnv` folds each
  provider's env into its dependents (dependent env wins). `DownCmd.Run` invokes
  plugin `down`; `psRows` renders `provider:<type>`; provider services are excluded
  from the `watchGone` self-exit watch and skipped by `--watch` reload.
- **Tests**: `pkg/compose/provider_test.go` (parse, sorted flags, scalar options,
  mutual-exclusion, type-required) and `composecli/provider_test.go` (stream
  protocol incl. `=`-in-value and non-JSON lines, env prefix normalisation,
  `injectProviderEnv` precedence + no shared-map mutation, and a helper-process
  fake plugin exercising `run` up/error paths). Full gate green: gofmt/vet clean,
  `go test ./...` 84 ok / 0 fail.

Known follow-ups: the docs ja/zh translations of the new `docs/cli/compose.md`
"Provider services" section are not done.

### 2026-07-24 follow-up — provider stop verb + reload parity

- Added the compose-spec provider `stop` verb: `runAction` now routes a provider
  service through `providerLifecycle` (`stop`->plugin `stop`, `start`->`up`,
  `restart`->stop+up) instead of the server `Action` API. `Provider.MarshalJSON`
  renders the block back to its `{type, options}` Compose shape so `compose config`
  round-trips it (asserted by `TestProviderConfigRoundTrip`).
- Corrected an earlier inaccuracy: the detached `up -d --watch` reload re-execs
  the whole CLI (`clientagent.reexecReload` -> `daemonize.SpawnAt`), so `upDetached`
  re-runs provider `up` on every reload — there was never a detached gap. For
  parity the foreground in-process `reloadAndReconcile` now calls `runProviderReload`
  for each provider before re-deploying dependents, so an edited provider config
  takes effect in both paths (providers are required to be idempotent). Gate green.
- Translated the new "Provider services" section into `docs/ja/cli/compose.md`
  (プロバイダーサービス) and `docs/zh/cli/compose.md` (Provider service, matching zh's
  code-switching register), inserted after the Devcontainer subsection.
- Then fully reconciled `cli/compose.md` ja/zh to the English source: the ja/zh
  pages had trailed it (254 vs 360 lines) and were missing the `--watch`,
  `--remove-orphans`, and `--ingress-conduit` `up` rows, the entire `### Auto-reload`
  subsection (translated with an explicit `{#auto-reload}` anchor so the in-page
  link resolves under a translated heading), the `down` `--remove-orphans` row +
  orphan-detection paragraph, and the `exec` `--forward-agent` row. All 17 headings
  now align across EN/JA/ZH; the translation audit passes (only inline-code/link
  heuristic warnings, expected under the code-switching register), site-absolute
  links are locale-prefixed, and VitePress `docs:build` passes.

### 2026-07-24 follow-up — translation glossary maintenance

Reconciled the two glossaries with the terms settled during the provider/compose
work, and fixed the drift my new prose had introduced.

- Terminology survey (grep over `docs/ja` and `docs/zh`) settled the canonical
  renderings: reconcile → JA 収束/収束処理 (dominant 11; katakana リコンサイル was
  my outlier and is codified as 収束させる), ZH keeps `reconcile` in English (12
  usages) despite the glossary's 调谐; tear down → JA 削除, ZH 拆除 (with 移除
  reserved for "remove"); provider/plugin/lifecycle/idempotent/prefix/auto-reload/
  dependent all consistent across the tree.
- Fixed `docs/ja/cli/compose.md`: my 3 リコンサイル → 収束 and 2 撤去 → 削除
  (rephrasing "removed" as 取り除く to avoid a 削除…削除 repetition). ZH needed no
  prose fix — its code-switching register already matched.
- JA glossary: added provider/provider service, provider plugin/plugin, lifecycle,
  idempotent, dependent service, discovery(探索), prefix(接頭辞), auto-reload.
- ZH glossary: added provider/plugin (kept English), lifecycle(生命周期),
  idempotent(幂等), dependent(依赖方), prefix(前缀), auto-reload(自动重载); refined
  `clean up / tear down` to 清理 / 拆除 and annotated that `reconcile` is usually
  kept in English in prose.

Audit passes for both locales, `docs:build` passes, `git diff --check` clean.

### 2026-07-24 follow-up — provider E2E scenario

Added `e2e/scenarios/provider.star` (in the Makefile `SCENARIOS` list, so it runs
on the docker and kube targets and is glob-picked by the containerized CI runner).
It drives the real `cornus compose` client against a stub provider plugin:

- The stub is a POSIX-sh script written to a `temp_dir()`; the provider `type:` is
  its absolute path, so `resolveProviderBinary`'s `exec.LookPath(typ)` branch finds
  it without touching PATH (no harness change needed). The stub records the argv +
  service name of each call under a second `temp_dir()` and, on `up`, emits the
  provider stdout protocol (`info` + two `setenv`).
- Backend-agnostic assertions (every target, incl. `local`): `up`/`stop`/`down`
  invoke the plugin with the sorted `--engine=mysql --version=8` flags and
  `database` as the final arg (read back via `read_file`), and `compose ps` renders
  `provider:<type>` with a `provider` status.
- kube-only: a dependent `app` (busybox, `depends_on: database`) is deployed and
  `pod_exec(printenv DATABASE_URL/DATABASE_TOKEN)` proves the `setenv` output is
  injected into the dependent as `<SERVICE>_<VAR>`.

Validated live on `--target local` (passes; provider-only branch), and
`make e2e-check` resolves the whole suite including the new scenario. The kube
env-injection branch runs under `make e2e-kube` / the CI kube matrix.
