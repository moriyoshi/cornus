# Maintenance, Hardening, and Closed Decisions

## Summary

Catch-all reference for durable project decisions, the current package layout, GC/lifecycle
mechanisms, operational hardening knobs, and CI setup for Cornus — a single Go binary combining a
tiny OCI registry, an in-process BuildKit build engine, and an imperative deploy engine. Records
why certain features were deliberately NOT built (deploy rollback, overlay-differ warning
suppression, manifest crash-repair) so they are not re-litigated.

## Key Facts

- Deploy rollback is DESCOPED: not a Compose/Docker parity gap, and `kubectl rollout undo` already
  works natively on Cornus-created Deployments. Rationale kept in ARCHITECTURE.md.
- The recurring `failed to compute blob by overlay differ (ok=false): <nil>` build warning is a
  benign BuildKit optimization fallback — confirmed, closed, no code change.
- `stop` in the dockerd proxy is record-level stop-and-keep: the workload is torn down but the
  `exited` record survives; `start` re-deploys; `rm` deletes. No container-level pause.
- Package layout: everything lives under `pkg/` per subsystem; the only compiler-enforced private
  packages are `pkg/build/internal/lazyctx` and `pkg/deploy/kubernetes/internal/netdriver`.
- Storage GC is mark-and-sweep (`Backend.GC` in `pkg/storage/gc.go`), triggered on demand via
  `POST /.cornus/v1/gc` (which also runs a 7-day localcache prune); stale uploads are swept at backend
  startup (`SweepStaleUploads`, 24h TTL).
- Scheduled GC: `CORNUS_GC_INTERVAL` (Go duration) runs the same `runGC` core on a ticker
  (`pkg/server/gcschedule.go`); unset = fully off at zero cost; malformed/non-positive = hard
  startup error (the fail-closed policy-env precedent). Multi-replica coordination:
  `CORNUS_GC_LEASE` (`kube`, `kube:<name>`, `kube:<ns>/<name>`) gates each tick behind a
  per-tick CAS on a `coordination.k8s.io` Lease; requires `CORNUS_GC_INTERVAL`; fail-closed.
- Stream-error surfacing on Logs/Stats/archive-GET: lazy-header write turns pre-output backend
  errors into real 4xx/5xx; mid-stream errors ride the `X-Cornus-Stream-Error` HTTP trailer
  (`api.StreamErrorTrailer`), which `pkg/client` checks after EOF. dockerproxy is excluded by
  design (the docker CLI ignores trailers).
- `cornus deploy --detach`/`-d` is the stateless remote deploy verb (POST the spec and exit);
  remote `--delete` is the matching one-shot teardown. The local CLI's `localBackend()` honors
  `CORNUS_DEPLOY_BACKEND` (unrecognized values fall through to dockerhost with a `slog.Warn`).
- `caretaker`/`caretaker-check`/`net-redirect` live under `cornus daemon`; the old top-level
  spellings survive as HIDDEN kong aliases (`kong:"cmd,hidden"`) so baked pod-spec argv keeps
  working.
- BuildKit cache growth is capped by `CORNUS_BUILD_CACHE_KEEP_BYTES` (overrides `MaxUsedSpace` of
  buildkitd's default disk-derived GC policy; accepts human sizes via go-units).
- Server limits: `CORNUS_BUILD_CONCURRENCY` semaphore (default NumCPU), per-deploy-name mutex,
  build-context tar capped at 2 GiB (`CORNUS_MAX_BUILD_CONTEXT_BYTES`), blob PUT capped at 10 GiB
  (`WithMaxBlobSize`, 413 + session abort).
- CI (`.github/workflows/ci.yml`): gofmt-check / build / vet / test / `make e2e-check` on push+PR,
  plus a `helm` job (lint + template, both TLS-off default and cert-manager config).
- CLI parser is `github.com/alecthomas/kong` (explicit user choice, not cobra).
- Single product binary since 2026-07-05: `cornus compose <up|down|ps|build|restart|stop|start>`
  and `cornus daemon <docker|mounts>` replaced the standalone `cornus-compose` / `cornus-dockerd`
  binaries; `cornus-e2e` deliberately stays separate (dev tooling, not product surface).
- Go toolchain baseline is 1.26 (`go 1.26.0` in go.mod; `golang:1.26-bookworm` in the
  Dockerfile; local install at `~/.local/go` is 1.26). Nothing pins the project lower — BuildKit
  only requires `go 1.22.0`.
- Debugging heuristics (from a CI green-up): trust the API contract and suspect the stale
  caller/fixture first; a subagent's root-cause theory still needs primary-source checks per
  specific (e.g. `docker image inspect` for a real ENTRYPOINT); a SHA-pinned installer can still
  install a floating tool version (cosign v2 -> v3 under a pinned `cosign-installer`).
- Project identifier `cornus.io` -> `cornus.dev`: the project does not own `cornus.io`, so
  `cornus.dev` is canonical for any new Kubernetes-facing group/label/annotation key going
  forward (k8s specifics in [[kubernetes-backend]]); the one non-k8s site was the internal
  containerd build label in `pkg/build/internal/lazyctx/labels.go`, renamed to
  `cornus.dev.lazy.ref` (the `containerd.io/snapshot/` prefix is containerd's own namespace and
  stayed untouched).

## Details

### Closed decisions (do not reopen without new evidence)

**Deploy rollback / revision history — descoped.** Compose and plain Docker have no rollback
concept, so there is nothing to be at parity with; it was mis-lumped into the deploy-fidelity TODO
next to healthchecks/resource limits (which WERE real parity gaps and shipped). The kubernetes
backend does `Deployments.Update(...)` in place and does not zero `RevisionHistoryLimit`, so
native ReplicaSet history is intact and `kubectl rollout undo` works on Cornus-created
Deployments for free. The only delta a bespoke revision store would add is dockerhost rollback,
which has no parity precedent and would introduce persistent stateful history against Cornus's
imperative, stateless-by-design deploy model. The descope rationale plus a compressed sketch of
the reviewed design live in ARCHITECTURE.md ("Deploy rollback — descoped").

**Overlay-differ export warning — benign, no code change.** Cornus uses the `overlayfs`
snapshotter; BuildKit (v0.18.2, `cache/blobs.go:183-218`) sets `enableOverlay,fallback=true` with
`logWarnOnErr=true` and probes the fast overlay differ via `tryComputeOverlayBlob`. When the probe
returns `ok=false, err=nil` (overlay mounts not in the optimizer's expected form), BuildKit warns
and falls through to the standard containerd differ, which computes the correct blob. Empirically
corroborated: every build → push → pull/consume E2E round-trips identical digests. Suppressing the
warning in Cornus's progress drain would risk hiding genuinely useful warnings.

**stop-and-keep semantics — resolved as record-level.** The dockerproxy's `stop` tears down the
workload but keeps the `exited` record; `start` re-attaches/re-deploys; `rm` deletes the record.
A container-level pause is impossible for services with client-served 9P mounts (the mount cannot
outlive the caller). Separately, `cornus compose` `stop`/`start`/`restart` fail loud with guidance
("use `down`") for a service whose client-local mount is held by a live `up -d` supervisor;
mount-free services are unaffected. Documented in ARCHITECTURE.md.

**No manifest crash-repair.** `PutManifest` writes blob → manifest marker → tag, in that order
(tag last), so a crash leaves at worst a GC-reclaimable orphan, never a tag pointing at missing
data. `GetManifest`-by-tag returns a clean `ErrNotFound` when a tag resolves to a missing/corrupt
manifest. Active repair was rejected — it would race a live write. (`pkg/storage/cas.go`)

**Single-binary consolidation — no argv[0] dispatch.** When `cornus-compose` and
`cornus-dockerd` were folded into `cornus` (2026-07-05), a busybox-style `docker-compose`
symlink mode was considered and REJECTED as unsound: copied binaries, exec wrappers with
arbitrary argv[0], Windows naming, and two CLI grammars in one binary. Drop-in Docker tooling
compatibility is served by `cornus daemon docker` + `DOCKER_HOST` (the stock docker CLI /
compose plugin is the faithful layer); interactive users can `alias docker-compose='cornus
compose'`. Naming: `daemon mounts` was chosen over `supervisor` (too general), `mountd`,
`exports`, and `attach` — it names what the process holds. The compose subcommands live in
`cmd/cornus/internal/composecli` to avoid `package main` symbol collisions (e.g. two
`BuildCmd`s). The mounts-daemon spawn (`spawnDetached`) re-execs `<self> daemon mounts ...` via
`os.Executable()`, never trusting `os.Args[0]`. The supervisor state dir stays
`$XDG_RUNTIME_DIR/cornus-compose/` (runtime state, not worth a migration).

**caretaker family lives under `cornus daemon`, with hidden top-level aliases.** The
`caretaker`/`caretaker-check`/`net-redirect` commands mount under `DaemonCmd` (shared command
structs, kong scoping flags per mount point). Because the old top-level argv spellings are baked
into generated pod specs (six-plus sites in `pkg/deploy/kubernetes/kubernetes.go`), they remain
as HIDDEN kong aliases — `kong:"cmd,hidden"`, the `LogShimCmd` pattern — so existing pod-spec
argv keeps working and the spec generators were NOT changed. Covered by parse-equivalence and
visibility tests; both spellings smoke-ran. (This supersedes the earlier decision to keep the
family top-level; the alias mechanism removed the skew concern.)

**Documentation lifecycle.** JOURNAL.md consolidates into topic documents under
`.agents/docs/LTM/` (`good-sleep`), which merge into synthesis documents (`deep-sleep`, see
LTM/INDEX.md) and promote into OVERVIEW.md/ARCHITECTURE.md (`distill-memories`). A `deep-sleep`
pass (2026-07-07) created `deploy-backends-synthesis.md` (consolidates `deploy-backend-contract.md`,
`containerd-backend.md`, `kubernetes-backend.md` at contract level only; k8s object-shape depth
stays in `kubernetes-deploy-synthesis.md`, which `kubernetes-backend.md` continues to feed — the
established two-parent precedent) and refreshed five syntheses in place with new source material
(`docker-compat-clients-synthesis.md`, `build-engine-synthesis.md` now covering the
`CORNUS_BUILD_WORKER=containerd` build-worker facet of `containerd-backend.md`,
`shipping-and-install-synthesis.md`, `caretaker-transport-and-hub-synthesis.md`,
`kubernetes-deploy-synthesis.md`). `port-forwarding.md` and `remote-cluster-connection-ergonomics.md`
are deliberately NOT merged (reaching workloads vs. reaching the cornus server are different
concerns).
README.md and ARCHITECTURE.md were structurally reorganized on 2026-07-05 (content preserved;
ARCHITECTURE.md gained a numbered reading order, subsystem-grouped module list, and a "Closed
decisions" tail section; README.md follows a user-journey order with a contents line). Four
pre-restructure LTM source docs still say `internal/...` where code now lives under `pkg/...`.

**ARCHITECTURE.md is now the repository-ROOT, human-reader-ready canonical doc.** It was promoted
from `.agents/docs/ARCHITECTURE.md` (which is now a pointer stub to `../../ARCHITECTURE.md`) as a
narrative rewrite (~810 lines from the ~1050-line agent-oriented source; two mermaid diagrams,
backend/role/network/auth tables, links to `pkg/...`). `distill-memories` targets the root file and
is the reconciliation path against drift; `AGENTS.md`, `README.md`, and `OVERVIEW.md` were rewired to
it. Section headings differ from the old doc, so any future "ARCHITECTURE.md <section>" citation must
use the new names. `distill-memories` promotes LTM into OVERVIEW.md and the root ARCHITECTURE.md;
`deep-sleep` also produced `shipping-and-install-synthesis.md` (release-and-packaging +
local-k8s-quickstart), giving both of those source docs a synthesis parent — a note the next
`reconcile-journal-ltm` pass should fold into the canonical record's synthesis/standalone tables.
A `distill-memories` pass (2026-07-07) promoted durable LTM into both canonical docs: OVERVIEW.md
gained the deploy engine's THREE backends (dockerhost / containerd / kubernetes), orientation-level
port reachability, signed static binaries + `SHA256SUMS` + OCI Helm chart, `config`/`port-forward`
CLI verbs + connection profiles, and `@devcontainers/cli` compatibility with `-d/--daemon`. Root
ARCHITECTURE.md gained module-map additions (`clientconn`, `daemonize`, `pkg/clientconfig`,
`pkg/svcforward`, `pkg/kubeauth`), the stream-error surfacing mechanism (lazy 200 +
`X-Cornus-Stream-Error` trailer), `CORNUS_GC_LEASE` coordination, a cross-backend contract block
(`deploy.ErrNotFound`, `ParseSince`, stdcopy framing, Entrypoint/Command semantics, replica-0
publish, volume reaping, `deploy.Bridge`) + `cornus deploy --detach`, Multus static IPAM + the
fabric matrix, the proxy's foreground `docker run` protocol, the compose daemonize mechanism, and a
new "Connection profiles and remote clusters" section. Deliberately NOT promoted (already covered or
too fine-grained): containerd backend internals, hub TLS/catalog-push/mount-relay, auth completions,
emulator validation recipes, chart version history, fingerprint `Protocol: 2` internals, and
per-bug patch history.

**Project name is "Cornus" in prose.** The whitespace-delimited standalone word naming the project in
an English sentence is capitalized (including possessive `Cornus's` and sentence-start, and
attributive compounds `Cornus-owned`/`-side`/`-managed`); it STAYS lowercase when adjacent to
`/ . - _ :` (command `cornus build`, path `cmd/cornus`, image `ghcr.io/moriyoshi/cornus`, env
`CORNUS_*`, object `svc/cornus`, label `cornus.app`), inside code/backticks/mermaid fences, and for
literal CLI-subcommand subjects (`cornus compose`, `cornus daemon docker`). `JOURNAL.md` is exempt
(append-only). Note `CLAUDE.md` is a symlink to `AGENTS.md` (one physical file). Repo-authored docs
also forbid full-width parens/colons; large block deletions via anchored `perl -0777 -pi` mark the
file modified-since-read, so re-Read before any subsequent Edit.

**Project identifier renamed `cornus.io` -> `cornus.dev` (the project does not own `cornus.io`).**
The Kubernetes API-group / annotation-label prefix used to be `cornus.io`, a domain owned by an
unrelated entity; the project's actual domain is `cornus.dev`. Since Kubernetes group and
label/annotation keys are conventionally DNS names the project controls, this was renamed
everywhere as a clean cut with no compatibility shim for the old group (the repo held only the
"Initial." commit at the time, so there was no deployed-CRD/annotation backward-compat concern to
preserve). This is the durable naming decision: `cornus.dev` is the project's canonical
domain/identifier prefix going forward for any new Kubernetes-facing group/label/annotation key.
See [[kubernetes-backend]] for the Kubernetes-specific sites changed (the CRD group in
`pkg/kubehub/kubehub.go`, RBAC `apiGroups` in the Helm chart and static manifest, the annotations
in the kubernetes deploy backend, and the E2E scenario/script updates) — not duplicated here.

One non-Kubernetes-specific site also carried the string and was fixed in the same pass: the
internal build label in `pkg/build/internal/lazyctx/labels.go` (`LazyLabel`) was
`"containerd.io/snapshot/cornus.io.lazy.ref"`. The load-bearing part is the
`containerd.io/snapshot/` prefix — containerd's `FilterInheritedLabels` only forwards keys under
that prefix to `Snapshotter.Prepare` — so the trailing token is arbitrary; it was renamed to
`cornus.dev.lazy.ref` for consistency, leaving the `containerd.io/snapshot/` prefix untouched since
that namespace belongs to containerd, not Cornus. No user-facing docs (VitePress `docs/`,
README.md, ARCHITECTURE.md) ever referenced `cornus.io` — nothing to change there. Verified via
`grep -rn "cornus\.io"` across the whole tree (excluding `.git`) returning zero hits after the
change, with a full green Go gate (gofmt/build/vet/test).

**Standalone design docs are consolidated into ARCHITECTURE.md.** `HUB_MULTIREPLICA_DESIGN.md`
and `DEPLOY_ROLLBACK_DESIGN.md` were folded into the relevant ARCHITECTURE.md sections and the
originals parked under `.agents-workspace/tmp/consolidated-design-docs/`. Do not resurrect or cite
the standalone files. `GAP_ASSESSMENT.md` (a 2026-07-04 assessment) was RETIRED on 2026-07-09 once
a per-sub-claim re-verification confirmed it was substantially stale (6 of 7 areas closed, the 7th
descoped); its live status is carried in the "Production-hardening gaps" section of TODO.md, which
also holds the three low-severity residuals the re-verification left open.

**Foundational choices (from the initial build).** In-process BuildKit solver (no separate
buildkitd, no buildx shell-out): the controller is registered on a gRPC server bound to an
in-memory bufconn listener and driven by BuildKit's own client, which reuses all client-side
machinery (sessions, secrets, authprovider, filesync, exporters) — buildx parity for free.
Registry `/v2/*` handlers are hand-written over the CAS because go-containerregistry's
`pkg/registry` stores manifests in memory only (go-containerregistry is still used client-side).
The dockerhost deploy backend speaks the Docker Engine REST API directly over the unix socket and
never imports `docker/docker/client` — this sidesteps the docker/docker ↔ go-connections ↔
go-containerregistry dependency diamond. Deploy model is imperative apply (one spec in →
converge), no git-watch/reconciliation. Fully static build: `CGO_ENABLED=0 go build -tags "netgo
osusergo"`.

**Transport/build decoupling.** The 9P/yamux transport (yamux-over-WebSocket session,
`DirServer`, confined 9P attachers, `Serve9PBacking`) lives in `pkg/wire` precisely so that
`pkg/deploywire` and `pkg/dockerproxy` link zero BuildKit packages (verified via
`go list -deps | grep -c buildkit` == 0). `docker logs` was deliberately built as a REST stream
(`GET /.cornus/v1/deploy/{name}/logs`) rather than over the deploywire attach path to preserve this.
Contract: `Backend.Logs(ctx, name, opts, w)` writes stdcopy-framed output (dockerhost passes
Docker's frames through; kubernetes wraps `GetLogs` in a stdout framer — k8s cannot split
stdout/stderr).

### Package layout (post-restructure)

The former single global `internal/` tree is dissolved: every package is a subsystem under `pkg/`
(`api`, `authtoken`, `caretaker`, `client`, `compose`, `config`, `deploy`, `deploywire`,
`devcontainer`, `dockerproxy`, `e2e`, `hub`, `kubehub`, `logging`, `observability`, `registry`,
`server`, `storage`, `wire`), with per-subsystem `internal/` only where the importer set is
single-subsystem:

- Build subsystem: `pkg/build/builder` (engine, `//go:build linux`), `pkg/build/buildwire`, and
  `pkg/build/internal/lazyctx` (only importers are builder + buildwire — compiler-enforced
  build-private).
- `pkg/deploy/kubernetes/internal/netdriver` (only importer is the kubernetes backend).

Everything else is shared across subsystem boundaries (mostly by `pkg/server` and the cmds) and
stays exported. Package names were unchanged in the move — only import paths.

### GC and lifecycle

- **Registry/storage GC** (`pkg/storage/gc.go`): mark-and-sweep `Backend.GC`. Roots are every
  repo's tags plus manifest markers; the mark phase BFS-parses image manifests AND indexes,
  following config/layers/nested `manifests[]`/`subject`; sweep removes unreachable
  `blobs/sha256/**`. Blob `DELETE` / `Backend.DeleteBlob` exist as well.
- **Stale upload sweep**: `SweepStaleUploads(ttl)` (24h default), prefix-restricted so it only
  touches known staging files, called best-effort from `NewBackend`.
- **HTTP trigger**: `POST /.cornus/v1/gc` runs CAS `Backend.GC` plus a 7-day localcache prune
  (`builder.PruneLocalCache` — subtree-newest-mtime TTL, root-confined, never over-deletes a
  nested-key sibling; engine-absent-safe). Gated on the `gc` API-policy action (403 on denial,
  405 non-POST).
- **Scheduled GC** (`pkg/server/gcschedule.go`): `CORNUS_GC_INTERVAL` (Go duration) runs the
  same `runGC` core as `POST /.cornus/v1/gc` on a ticker. Unset = fully off, zero cost (no
  goroutine/ticker). Malformed or non-positive = HARD startup error, matching the fail-closed
  policy-env precedent. First run happens after one full interval (not at startup); ticks are
  non-overlapping (a tick that finds GC still running skips and logs); tick errors are logged,
  never fatal; the scheduler is stopped and drained before `closeResources` returns.
- **GC leader election** (`CORNUS_GC_LEASE`): accepts `kube`, `kube:<name>`, or
  `kube:<ns>/<name>`; requires `CORNUS_GC_INTERVAL`; fail-closed on a malformed value or
  unloadable kube config. Each tick is gated behind a per-tick CAS acquire/renew on a
  `coordination.k8s.io` Lease with duration 2x the interval clamped to [30s, 1h]; holder
  identity is POD_NAME/hostname; a 409/AlreadyExists is a clean refusal; gate errors skip the
  tick (a missed sweep beats a concurrent one). Deliberately NOT client-go's `leaderelection`
  package — leadership only matters at tick instants, so there is no background renew loop.
  Nuance: intervals > 2h can let far-apart replicas each sweep once per round — never
  concurrently, which is the invariant. Chart values `gc.interval`/`gc.lease` render these
  (lease-without-interval fails template rendering); Lease RBAC is already covered by the
  kube-hub-store rule.
- **BuildKit cache cap** (`pkg/build/builder/engine_linux.go`): `wopt.GCPolicy` wired from
  buildkitd's `config.DefaultGCPolicy` (disk-derived: reserve <= 10 GB, keep >= 20% free, cap used
  at min(80%, 100 GB)); `CORNUS_BUILD_CACHE_KEEP_BYTES` overrides `MaxUsedSpace` — the knob that
  actually caps growth.
- **Builder crash recovery** (`pkg/build/builder/sweep_linux.go`): at engine `New`, a startup
  sweep reaps stale `cornus-9p*`/`cornus-ssh-*` temp dirs older than 5 min (mtime heuristic,
  best-effort, never blocks startup). `remoteSnapshotter.releaseAll()` is wired into
  `Engine.Close` via a `snapshotterRegistry`, bounding `committed`/`views` growth on the
  process-singleton engine.
- **Server shutdown** Closes the lazily-built engine and deploy backend, freeing the BuildKit
  data-dir lock.

### Operational hardening (server)

- `readyz` is a real `atomic.Bool`: 503 until serving, flipped false on shutdown; `healthz` stays
  pure liveness.
- Malformed `CORNUS_HUB_POLICY` / `CORNUS_HUB_REGISTER_POLICY` is a hard startup error (was
  fail-open); `server.New` returns `(*Server, error)`.
- TLS: `--tls-cert`/`--tls-key` flags (+ `CORNUS_TLS_*` env) → `ServeTLS`.
- Build concurrency semaphore: `CORNUS_BUILD_CONCURRENCY`, default NumCPU.
- Per-deploy-name mutex map covering apply + delete; kept in the server layer, not dockerhost.
- Request-size caps: `http.MaxBytesReader` on the build-context tar — 2 GiB default,
  `CORNUS_MAX_BUILD_CONTEXT_BYTES`; blob PUT cap `WithMaxBlobSize` — 10 GiB default → 413 +
  upload-session abort.
- Build failures after streaming has begun use the in-band `BUILD FAILED:` trailer-after-200
  convention (documented in code). Deploy-side streams use the lazy-header + HTTP-trailer
  mechanism below instead.
- Registry API completeness: Referrers API (`GET /v2/{name}/referrers/{digest}`, `?artifactType=`
  + `OCI-Filters-Applied`), `Range` on blob GET (206/`Content-Range`/`Accept-Ranges`/416),
  pagination on tags and `_catalog` (`?n=`/`?last=` + `Link` header).
- Observability: kubernetes client-go and dockerhost socket HTTP clients wrapped with `otelhttp`
  (gated on `Enabled()`); opt-in Prometheus `/metrics` via `CORNUS_METRICS_PROMETHEUS` (its own
  registry/reader; OTLP push untouched; route and auth-exemption exist only when active).
  Zero-cost when telemetry is off.
- Data-dir flock makes a second Cornus instance fail fast instead of hanging.

### Stream-error surfacing (Logs / Stats / archive-GET)

Two complementary mechanisms make backend errors on streaming endpoints visible instead of
swallowed by an already-committed 200:

- **Lazy-header write** (in `pkg/server/deploy.go` and, as deliberate small duplication,
  `pkg/dockerproxy/containers.go`): the 200 + flush happen only on the backend's FIRST output
  byte. A backend error BEFORE any output returns a real status with a proper error body:
  not-found → 404, unsupported → 501, invalid `since` → 400, else 500 (classification via
  `errors.Is(deploy.ErrNotFound)` first, substring fallback). Archive GET uses the same
  mechanism, with the stat header withdrawn on a pre-byte error. Behavioral note: quiet
  follow-mode clients (e.g. `logs -f` on a silent container) receive headers only at first
  output. The attach/wait flush-header-early protocol is untouched.
- **Mid-stream errors ride an HTTP trailer**: errors AFTER output has started are stamped into
  the `X-Cornus-Stream-Error` trailer (`api.StreamErrorTrailer`, declared together with the lazy
  200; value sanitized and length-capped). `pkg/client` `Logs`/`Stats`/`CopyFrom` check the
  trailer after body EOF and return "stream error after partial output: ..." while still
  delivering the partial bytes. dockerproxy is EXCLUDED by design — the docker CLI ignores HTTP
  trailers, so there is no consumer on that side.

### Stateless remote deploy verb (`cornus deploy --detach`)

`cornus deploy --detach`/`-d` POSTs the spec via the pre-existing `POST /.cornus/v1/deploy` +
`pkg/client.Deploy` and exits — the workload persists with no client session. Constraints and
behavior:

- Client-local mount sources are rejected up front via `client.LocalMountSources` (the same
  classification `DeployAttach` serves over 9P) — they need an attach session to exist.
- Ports are NOT auto-forwarded; a note is logged.
- Remote `--delete` works as the matching one-shot teardown (previously hard-rejected against a
  remote server).
- Against a local (serverless) invocation, `--detach` is a documented no-op; `localBackend()`
  honors `CORNUS_DEPLOY_BACKEND` for host-level backends and logs a `slog.Warn` for unrecognized
  values before falling through to dockerhost.
- `pkg/client.New` normalizes `ws://`/`wss://` bases for plain HTTP calls (the detach POST must
  work against WS-spelled endpoints the attach surfaces pass around).

### Compose / deploy fidelity and dev containers

- `api.DeploySpec.Healthcheck` (Test/Interval/Timeout/Retries/StartPeriod; CMD/CMD-SHELL/NONE)
  and `Resources` (CPULimit cores, MemoryLimit bytes) parse from compose `healthcheck:` and
  `deploy.resources.limits.{cpus,memory}`; dockerhost maps to `Config.Healthcheck` +
  `NanoCpus`/`Memory`, kubernetes to exec liveness/readiness probes + `resources.limits`.
- Compose warns (via `slog.Warn`, deduped per `(service, field)`) on unknown service fields and
  non-`replicas` `deploy.*` keys instead of hard-erroring — format:
  `compose: service "web": field "healthcheck" is not supported and was ignored`.
- `compose.Service.Privileged` maps to `api.DeploySpec.Privileged`; the E2E harness exposes
  `privileged=` on `deploy`/`deploy_attach`.
- Compose `secrets:` (file-based top-level defs referenced by `build.secrets`) and
  `additional_contexts` forward through `client.BuildRequest.BuildContexts`/`Secrets` into
  buildwire's `NamedContexts`/`SecretIDs`.
- Dev containers: `build.target` threads to the dockerfile frontend `target` attr via
  `SolveInput.TargetStage`; `cache_from` folds into `type=registry` cache imports at `pkg/client`
  (no server change). `postStartCommand`/`postAttachCommand` re-run on `start`/`restart`
  (`runStartHooks`); once-per-create hooks do not.

### CI

`.github/workflows/ci.yml`: gofmt-check, build, vet, test, and `make e2e-check` on push + PR;
`setup-go` uses `go-version-file: go.mod` (honors the `go 1.26.0` directive, and follows future
bumps automatically) with module + build caching. A separate `helm` job runs `helm lint` and `helm template` against both the default
(TLS off) and the opt-in cert-manager configurations, so chart regressions fail CI without a
cluster.

As of 2026-07-06 the workflow surface grew to three files with cross-cutting hardening (concurrency
guards, SHA-pinned actions, Dependabot) and a second execution workflow (`e2e.yml`) that runs the
full Starlark suite via the containerized runner on push-to-main / dispatch. The authoritative
reference for all of that is `ci-github-actions.md`; the ci.yml gate summary above remains the quick
view. The gate also compiles the `cloudblob` storage drivers and the nested FTP-fixture module so
neither rots.

### Cross-cutting engineering lessons

Durable debugging heuristics that generalize beyond the incident that surfaced them (a CI green-up
where the failures were stale callers/fixtures against correct implementations; the CI mechanics
themselves live in [[ci-github-actions]]).

- **Trust the API contract, verify the fixtures.** When a self-consistent implementation is failing
  an external caller, suspect the caller/fixture before the code. Recurring shape: the kube backend,
  `api.DeploySpec`, and dockerhost were internally consistent; the outliers were the stale callers
  (deprecated cosign flags under a bumped installer; scenarios written against a wrong
  command-replaces-entrypoint mental model). The fix was to correct the fixtures, not the engine.
- **A subagent's plausible theory still needs primary-source checks.** An investigator can correctly
  identify a systematic root cause yet be wrong on a specific it "asserted" without checking — e.g.
  claiming a given importer image was "fine" when `docker image inspect` showed `curlimages/curl`
  ships its own ENTRYPOINT, so a partial fix would only move the failure to the next `wait`.
  Inspect each image's real ENTRYPOINT (or the equivalent primary source) before editing.
- **A SHA-pinned installer can still install a floating tool version.** `cosign-installer` is pinned
  by SHA, but the cosign VERSION it installs is not, so a cosign v2 -> v3 default change (new-bundle
  `sign-blob` output format) landed under a "pinned" action. Pinning the action is not pinning the
  tool; the same caveat applies to the image/chart signing steps.

### Privilege posture (durable environment fact)

Build *execution* requires root or a rootless user-namespace stack. On hardened Ubuntu hosts,
`kernel.apparmor_restrict_unprivileged_userns=1` blocks unprivileged user namespaces even with
`unshare --map-root-user`; builds fail at snapshotter `lchown`. Everything works as root inside a
container (the intended deployment) or `--privileged`. The build integration test
(`pkg/build/builder/engine_linux_test.go`) is gated on `os.Geteuid()==0` and skips otherwise.

## Files

- `/home/moriyoshi/src/chimpose/pkg/storage/gc.go` — mark-and-sweep `Backend.GC` (+ `gc_test.go`)
- `/home/moriyoshi/src/chimpose/pkg/storage/cas.go` — blob→marker→tag write ordering, `ErrNotFound` on corrupt tag resolution
- `/home/moriyoshi/src/chimpose/pkg/build/builder/engine_linux.go` — GC policy wiring, overlayfs snapshotter
- `/home/moriyoshi/src/chimpose/pkg/build/builder/sweep_linux.go` — crash-recovery temp-dir sweep
- `/home/moriyoshi/src/chimpose/pkg/server/` — readyz, concurrency semaphore, per-deploy locks, size caps, `/.cornus/v1/gc`
- `pkg/server/gcschedule.go` — scheduled GC ticker (`CORNUS_GC_INTERVAL`) + kube Lease gate (`CORNUS_GC_LEASE`)
- `pkg/server/deploy.go` / `pkg/dockerproxy/containers.go` — lazy-header writers for Logs/Stats/archive
- `pkg/api` — `api.StreamErrorTrailer` (`X-Cornus-Stream-Error` trailer name)
- `cmd/cornus/commands.go` — `cornus deploy --detach`, `localBackend()`
- `cmd/cornus/daemon.go` — `DaemonCmd` mounts of the caretaker family; hidden top-level aliases
- `/home/moriyoshi/src/chimpose/cmd/cornus/serve.go` — TLS flags, env plumbing
- `/home/moriyoshi/src/chimpose/pkg/compose/` — unknown-field warnings, privileged/healthcheck/resources parsing
- `/home/moriyoshi/src/chimpose/pkg/wire/` — BuildKit-free 9P/yamux transport
- `/home/moriyoshi/src/chimpose/.github/workflows/ci.yml` — CI + helm jobs
- `/home/moriyoshi/src/chimpose/.agents/docs/ARCHITECTURE.md` — canonical home of the rollback descope, stop-and-keep, and hub multi-replica rationale
- `pkg/build/internal/lazyctx/labels.go` — `LazyLabel`, renamed trailing token `cornus.io.lazy.ref` -> `cornus.dev.lazy.ref` (kept under the untouched `containerd.io/snapshot/` prefix)

## Test Coverage

- `pkg/storage/gc_test.go` and `cas_consistency_test.go` — GC mark-and-sweep, manifest ordering.
- `pkg/server` tests pass under `-race` (semaphore, per-deploy mutex, readyz, size caps).
- `pkg/compose/compose_test.go` — `TestPrivileged`, unknown-field warnings, secrets/contexts.
- `pkg/e2e/preflight_test.go` — `TestScenarioNeedsComposeBuild` (compose-build capability
  detection); harness `TestDaemonHeldService` (compose lifecycle guard).
- Privileged E2E scenarios: `build-cache.star`, `build-invalidate.star`, `build-lazy.star`
  (default host-bind lazy path), `build-lazy-9p.star` (gated on `lazy_9p` token / `Cap9P`, out of
  the default suite), `devcontainer.star` (kube-only self-skip).
- `pkg/build/builder/engine_linux_test.go` `TestBuildAndPush` — root-gated full build→push→pull.

## Pitfalls

- Mechanical import rewrites: a sed rule like `s#internal/lazyctx#pkg/build/internal/lazyctx#` is
  NOT idempotent — re-running matches the `internal/lazyctx` suffix of the new path and produces
  `pkg/build/pkg/build/internal/lazyctx`. Anchor the pattern (e.g. exclude a preceding `/`) or
  grep for the corrupted form after every pass.
- E2E cache-hit assertions must not embed markers in `RUN` command text: `drainProgress` prints
  the RUN vertex NAME even on a cache hit, so an `echo CACHE-RUN-EXECUTED` marker matches when the
  step was served from cache. Emit markers by `cat`-ing a committed file so only actual execution
  produces the output.
- Preflight capability detection cannot rely on the literal `build(` token — compose files driven
  purely via `compose_up(` need `scenarioDrivesComposeBuild` (YAML-parses referenced files for
  `build:` sections). Read/parse errors conservatively yield no capability.
- `docker logs` limitations: first instance/pod only; kubernetes cannot split stdout/stderr
  (everything is framed as stdout).
- Do not suppress upstream BuildKit warnings in the progress/log drain — the overlay-differ case
  showed the same channel carries genuinely useful warnings.
- The `BUILD FAILED:` trailer arrives after an HTTP 200 on the build stream; clients must scan the
  stream, not trust the status code.
- On Logs/Stats streams the lazy-header write means quiet follow-mode clients get NO response
  headers until the first output byte — do not treat a headerless open stream as a hang.
- Mid-stream errors surface only via the `X-Cornus-Stream-Error` HTTP trailer; raw-HTTP consumers
  that ignore trailers (like the docker CLI, hence dockerproxy's exclusion) see silent
  truncation.
- `CORNUS_GC_LEASE` without `CORNUS_GC_INTERVAL` is a startup error; and lease-without-interval
  fails Helm template rendering too.

## OCI Is The Integration Boundary

The registry CAS is private registry implementation detail, not a shared Go subsystem substrate. BuildKit exports with `push=true` to an OCI registry reference; deploy backends receive that reference and target runtimes pull it over OCI/HTTP. `pkg/deploy` does not import `pkg/storage`, and `pkg/build` reaches storage only by speaking the registry protocol. Keep README and architecture wording aligned with this loose OCI coupling.

## Unified CLI Output

`cmd/cornus/internal/cliout` is the client-facing output boundary. It resolves plain, fancy, or NDJSON mode once from flag, environment, and TTY state; command results go to stdout, progress and notices go to stderr. Structured `Emit` and `Event` calls keep service state machine-readable, while `LineGroup` preserves concurrent line boundaries.

Build progress is neutral `pkg/build/buildprog.Event` data, not BuildKit-rendered bytes. `BuildSink` projects it into the selected output mode without pulling BuildKit into remote clients. Foreground-only user notices should use the driver; server, daemon, shared-library, and raw-passthrough diagnostics remain `slog`.
