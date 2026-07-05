# cornus Development Journal

This file retains only unconsolidated entries and the canonical long-term-memory audit.

---

## LTM Consolidation Record

Audited entry by entry against `.agents/docs/LTM/` and `.agents/docs/TODO.md` on
2026-07-26. Every substantive journal entry has durable coverage below, so the
consolidated narrative entries and superseded record blocks were removed.

### Journal sections -> durable memory

| Journal section group | Durable destination |
|-----------------------|---------------------|
| Activity flight recorder: server/caretaker/child lifecycle, unfinished work, follow/SSE/MCP reads, and crash-recovery E2E | `activity-flight-recorder.md` |
| Build delegation: raw relay, capability probing, auto-spawned privileged builder, self-built image, registry-mode propagation, docs, and diagnostics | `builder-delegation.md`, `maintenance-and-decisions.md` |
| Built-in workload observability: IMBH store, zero-touch multi-replica logs, query surfaces, Grafana APIs, OTLP gateway/re-export, caretaker mux, and E2E | `built-in-observability.md` |
| CI/CD and E2E reliability: Go caches, yamux race split, nested modules, Docker/plugin pins, license exception, scenario timeouts, fixture races, and real-controller testing | `ci-github-actions.md`, `e2e-harness-and-coverage.md`, `dependency-license-compliance.md` |
| Compose orchestration: orphans, parallel reconciliation, progress modes, watch reload, one-shot/PVC races, stable mount sessions, version skew, partial-apply cleanup, and provider plugins | `compose-cli.md`, `compose-provider-services.md`, `kubernetes-backend.md` |
| Completed follow-up archive and unresolved decisions from the TODO wrap-up | `.agents/docs/TODO.md` and the topic LTM documents referenced by each item |
| Dependency license audit, notices, scanner workflow, and published-module exceptions | `dependency-license-compliance.md` |
| Docker API proxy: deploy-attach lifecycle, stop/remove listener teardown, Dev Container coverage, and Docker 28/29 compatibility boundary | `dockerd-proxy.md`, `e2e-harness-and-coverage.md` |
| Embedded OpenTelemetry Collector: design, deployment surfaces, Kubernetes Secret hardening, docs, E2E, and CI | `workload-telemetry.md` |
| Host-backend companion caretaker: mounts, egress, port forwarding, agent relay, supervision, privilege union, leak recovery, and accepted blast radius | `remote-companion-and-agent-forwarding.md` |
| Host-native registry: Docker re-export/build loading, optional CAS, defaults, containerd read-write store, write gating, docs, and E2E | `host-native-registry.md` |
| In-container server topology: failure inventory, host-path translation, preflight, dockerhost implementation, containerd log-shim staging, setup flow, docs, and E2E | `in-container-server-mode.md`, `setup-wizard.md` |
| Incus deploy backend: client seam, OCI mapping, lifecycle/data plane, Debian runner, live E2E, documentation, and capability limits | `incus-backend.md` |
| Ingress configuration and native/emulated realization: field merging, certificates, RBAC/preflight, longest-path routing, 502 behavior, and controller-backed tests | `kubernetes-ingress.md`, `kubernetes-backend.md` |
| Ingress tunnels: shared route vocabulary, persisted mux/front door, backend gateway, client-held lifetime, provider behavior, teardown, reviews, docs, and E2E | `ingress-tunnels.md` |
| Knative Serving descriptor and speculative-probe Forbidden/NotFound least-privilege rule | `knative-serving.md` |
| Kubernetes E2E stats and backend-realized ingress constraints | `e2e-kubernetes-target-caveats.md`, `kubernetes-ingress.md`, `e2e-harness-and-coverage.md` |
| Local/remote image, storage, and disk-usage surfaces: OCI flow, object stores, host-native boundary, usage reporting, and quota decision | `registry-local-image-flow.md`, `registry-and-storage.md`, `host-native-registry.md` |
| MCP on the web BFF: shared core, Streamable HTTP, stdio process contract, tools/resources, and launched-client E2E | `web-bff-and-mcp.md` |
| Per-project connection context overrides and endpoint/credential trust model | `project-context-overrides.md` |
| Project and product reviews: scope pressure, abstraction/security/versioning decisions, first-user install findings, and diagnosability method | `maintenance-and-decisions.md`, `.agents/docs/TODO.md` |
| Remote 9P storage: yamux/tagged transport, block protocol, DiskStore, coherence, demand fill, prefetch, concurrent caller, writable export compatibility, and mmap boundary | `client-local-mounts-deploy.md`, `remote-9p-block-cache.md` |
| Remote connection ergonomics: Compose profile status, SSH profiles, ingress-through-SOCKS5, and conduit behavior | `july-2026-client-and-web.md`, `compose-cli.md`, `remote-cluster-connection-ergonomics.md` |
| User documentation and localization: storage diagrams, setup/remote guides, Topics-to-Guides, sidebar titles, anchor checker, NFKC, terminology, deploy-backend diagrams, and screenshots | `user-reference-docs-site.md`, `setup-wizard.md` |
| Web UI workspaces: Overview grouping, file explorer, terminal close semantics, tiled panes/stacks/editors, mock parity, and confined filesystem operations | `web-ui.md` |
| Workload lineage: structured origin, backend persistence, loaded-project join, non-project attribution, and mount-reporting boundary | `workload-lineage.md` |
| Yamux QoS fork, A/B methodology, netem bounds, batched send path, frame-size test invariant, and CI | `yamux-qos-performance.md`, `ci-github-actions.md` |

### Synthesis documents -> source LTM documents

| Synthesis document | Consolidates |
|--------------------|--------------|
| `build-engine-synthesis.md` | `remote-build-9p-transport.md`, `build-cache.md`, `lazy-bind-mounts.md`, build-worker facet of `containerd-backend.md` |
| `caretaker-transport-and-hub-synthesis.md` | `client-local-mounts-deploy.md`, `client-side-egress.md`, `hub-network-overlay.md`, caretaker facets of `user-networks-and-caretaker.md` |
| `client-connectivity-synthesis.md` | `client-daemon-and-conduit.md`, `client-side-egress.md`, `port-forwarding.md`, `public-tunnels.md`, `remote-cluster-connection-ergonomics.md` |
| `deploy-backends-synthesis.md` | `deploy-backend-contract.md`, `containerd-backend.md`, `kubernetes-backend.md` |
| `docker-compat-clients-synthesis.md` | `compose-cli.md`, `dockerd-proxy.md`, `dev-containers.md` |
| `kubernetes-deploy-synthesis.md` | `kubernetes-backend.md`, `user-networks-and-caretaker.md`, Kubernetes facets of `client-local-mounts-deploy.md` |
| `shipping-and-install-synthesis.md` | `release-and-packaging.md`, `local-k8s-quickstart.md` |
| `testing-ci-and-quality-synthesis.md` | `ci-github-actions.md`, `e2e-harness-and-coverage.md`, `codebase-audit-2026-07.md` |

### Intentionally standalone LTM documents

`activity-flight-recorder.md`, `auth-and-security.md`, `builder-delegation.md`,
`built-in-observability.md`, `compose-provider-services.md`,
`control-plane-api-namespace.md`, `dependency-license-compliance.md`,
`e2e-kubernetes-target-caveats.md`, `host-native-registry.md`,
`in-container-server-mode.md`, `incus-backend.md`, `ingress-tunnels.md`,
`kubernetes-ingress.md`, `maintenance-and-decisions.md`,
`observability-and-logging.md`, `project-context-overrides.md`,
`registry-and-storage.md`, `registry-local-image-flow.md`,
`remote-9p-block-cache.md`, `remote-companion-and-agent-forwarding.md`,
`setup-wizard.md`, `user-reference-docs-site.md`, `web-bff-and-mcp.md`,
`web-ui.md`, `workload-lineage.md`, `workload-telemetry.md`, and
`yamux-qos-performance.md`.

Open follow-up work is tracked in `.agents/docs/TODO.md`. See
`.agents/docs/LTM/INDEX.md` for the full index.

---

## Deep-Sleep Synthesis (2026-07-26)

Consolidated overlapping source LTM documents into broader orientations after a
topic-by-topic source and existing-synthesis review.

- Created registry/storage, observability, web/agent-surface, and ingress-routing
  syntheses.
- Refreshed build-engine synthesis for builder delegation, deploy-backend synthesis
  for all five backends and in-container topology, Docker-compatible client synthesis
  for Compose providers, and shipping/install synthesis for guided and containerized
  installation.

Source LTM documents were retained unchanged by the consolidation. The synthesis
table in `.agents/docs/LTM/INDEX.md` is the current navigation map.

---

## README landing-page rewrite (2026-07-27)

Replaced the 454-line root README with a 149-line project landing page. The long
k3s walkthrough and ecosystem comparison remain in the existing VitePress
documentation instead of being duplicated; README now links to those canonical
references.

- The new first-run path is a loopback-only Docker-host evaluation with an adjacent
  warning that authentication is opt-in and remote exposure needs security policy.
- The product summary now names all five deploy backends, managed builder delegation,
  Compose providers, the web/MCP surface, and activity/workload observability.
- Canonical overview and architecture references were updated so they no longer call
  README the k3s quick start, a container-Compose example, or the source of `docs/`.
- README-specific TODO findings D2, D3, U10, and U11 were closed. S4 and U9 remain
  open for the docs-site quick start and landing page respectively.

Validation passed `git diff HEAD --check`, repository punctuation checks, local
README target checks, `npm run docs:build`, and `npm run docs:check-anchors` (233
fragment links checked, 0 dead). The Docker example was source-verified against the
current kong flags, backend selection, and Compose path; it was not executed against
a live daemon in this documentation-only change.

## 2026-07-27 — Workload and server resource metrics on every backend

The store had three signals and producers for two of them. `cornus observe
metrics` worked and returned nothing, because nothing ever wrote a metric. This
adds the missing producer: every managed workload's CPU / memory / network /
disk, per replica, on all five backends, plus the server's own usage — all
through the same acceptance seam the other feeds use, so re-export gets them for
free.

Shipped: `api.ResourceSample` + `deploy.MetricsSampler` (a second optional
backend extension alongside `Preflighter`); `StatsOptions.Instance` threaded
client → server → all five backends; a metrics source for the kubernetes backend,
which previously had none; `obsstore.EncodeMetrics`; `pkg/server/metricsrecorder.go`;
`pkg/server/selfmetrics.go` + `pkg/observability/otlpbridge.go`; two config knobs;
`observe status` counters; trilingual docs; `observability-metrics.star`.

### Findings

**1. Verifying the query names found a defect nothing in Go could.** The plan
predicted `container_cpu_time_seconds_total` from the standard OTLP→Prometheus
translation. The store does no such thing: it maps dots to underscores and adds
no unit suffix, so the name is `container_cpu_time`. Worth more: it normalizes
metric NAMES and *not* attribute keys, and its PromQL cannot express a dotted
label under any quoting. So `cornus.replica` — the spelling the log recorder
correctly uses, and the one semconv specifies — made every per-replica series
**unfilterable**, and a matcher for it returns zero series *and no error*. The
whole point of sampling each replica was gone, silently. Fixed by emitting
underscored datapoint attributes (`cornus_replica`, `cpu_mode`, …), which is also
what any OTLP-to-Prometheus bridge does on the way in.

The general shape: when a component normalizes one half of an identifier, do not
assume it normalizes the other. And a store that answers a query language has a
name contract that only a live query can establish — no amount of unit testing
the encoder reaches it.

**2. Two bugs surfaced within seconds of the first live E2E run, both invisible
to unit tests.** A startup panic: both recorders were registered before
`s.newBackend` was assigned, so each child panicked on a nil func and the
supervisor restarted it 100ms later once the assignment landed. It self-healed,
which is exactly why it had gone unnoticed in the log recorder since that feature
shipped — every server start logged a nil-pointer panic for a one-line ordering
bug. And dockerhost's `SampleMetrics` counted a normal deploy as three backend
failures: Docker answers `?stream=0` on a not-yet-running container with an
**empty body**, not the zeroed frame I had guarded against, so the decode returned
`io.EOF`. Both are wiring facts between components, which is the category a fake
cannot testify to.

**3. `Backend.Stats` was the wrong thing to record through, and the reason
generalizes.** It renders Docker's stats JSON — a format shaped for the
`docker stats` CLI, where CPU is a pair of readings to difference, a one-shot read
zeroes the previous sample, and there is nowhere to put a backend that can only
report a rate. Recording through it would have meant re-parsing our own encoder's
output and inheriting all three compromises. A second interface returning the
*numbers* rather than the *format* costs ~10 lines per backend, because each
already had the sampler closure inside `Stats`.

**4. Backpressure and cadence diverge per feed, and each divergence is load-bearing.**
Logs hold a standing stream and resume from a watermark; metrics poll on an owned
ticker and never back-fill. That is not inconsistency: a missed log line is gone
forever, while a missed resource reading costs resolution, not information.
Likewise the server's own metrics ride the meter (one implementation, three
destinations) while workload metrics deliberately do not — an observable callback
runs during collection, so a wedged Docker socket would stall the whole cycle.

**5. Absent is not zero, and the type has to say so.** A backend that cannot see
network counters must produce no series, not a series of zeros: "moved no bytes"
and "cannot see whether it did" are different claims, and only the first should
render as a flat line. `ResourceSample` uses nil maps and pointer fields to carry
that distinction all the way to the encoder.

**6. The ja/zh guides were stale in a way the build could not catch.** Both still
carried a warning that only the *first* instance's logs are recorded — replaced in
the English source earlier in this session by the all-replicas behaviour. The
docs build passes, anchors resolve, and the translation contradicted the shipped
product in two languages. Fixed here. The structural audit cannot see this; only
reading the pair can.

Verification: the untagged gate, `make test-imbh` (cgo + `-race`), `make e2e-check`,
`observability-metrics.star` and the six sibling observability scenarios against a
live Docker target, `npm run docs:build`, and `docs:check-anchors` (233 fragment
links, 0 dead). PromQL names were confirmed against a live tagged server before
being written down, including a three-replica workload returning three series with
distinct values. Build-dependent scenarios (`deploy.star`, `deploy-stats.star`)
fail on this host for an unrelated pre-existing reason: the in-process BuildKit
push needs `--rootless` or privileges here.

---

## Animated logo (2026-07-27)

Added `assets/cornus-logo-animated.svg` as a separate one-shot animation while
leaving the existing SVG, PNG, and Illustrator assets unchanged. The original
six vector paths were preserved exactly and grouped into three semantic rotors:
the outer shell, the four workload tiles, and the central blossom.

The final motion keeps the initial energetic character but settles gradually:
the shell makes one clockwise turn over 6 seconds, the workload group makes one
counter-clockwise turn over 7.2 seconds, and the blossom makes two clockwise
turns over 8.4 seconds. Each uses its own deceleration curve and retains its
final transform, which is an integral number of turns and therefore exactly the
original logo orientation. A `prefers-reduced-motion` rule disables all three
animations.

### Findings

**1. The canvas center was not the visual center of every layer.** The first
version rotated all layers around `(150, 150)`. The workload and blossom artwork
is intentionally lower in the canvas, around `(151.5, 157.4)`, so using the
canvas center made those layers trace a small orbit. Giving both inner layers
their measured composition center made them spin in place.

**2. A path's bounding-box center can still be the wrong rotation pivot.** The
outer shell's bounds are centered at `(150, 150)`, but numerical integration of
the filled vector path placed its area centroid at approximately
`(150.03, 155.99)`. Rotating around the bounding-box center caused the shell's
visual mass to circle the pivot, which looked like a moving anchor even though
the CSS origin itself was fixed. Using the vector-area centroid removed that
perceptual drift.

**3. Rotation count and easing matter as much as duration.** Merely extending
the original 3.4-4.4 second timings would have left the two- and three-turn
inner animations with a high average angular velocity. The accepted version
both lengthens the motion and reduces the workload/blossom turn counts, with
gentler ease-out curves.

Validation parsed the result as XML and compared all six `d` attributes against
`assets/cornus-logo.svg`; the source geometry remained byte-for-byte identical.
The final rotation centers, speed, and settling motion were visually reviewed
and accepted.

---

## Memory consolidation, canonical docs, and README reset (2026-07-27)

### Work summary

Ran the full documentation-memory maintenance sequence and then repaired the
public project on-ramp:

1. **Journal to LTM.** Audited the journal's durable information against
   `.agents/docs/LTM/` and `.agents/docs/TODO.md`, filled uncovered topics, and
   collapsed the journal's older consolidation records into the canonical
   `## LTM Consolidation Record`.
2. **Deep-sleep synthesis.** Added broader registry/storage, observability,
   web/agent-surface, and ingress-routing syntheses; refreshed the build-engine,
   deploy-backend, Docker-compatible-client, and shipping/install syntheses; and
   updated `LTM/INDEX.md`. Source LTM documents remain intact.
3. **Canonical distillation.** Promoted missing durable design knowledge into
   `.agents/docs/OVERVIEW.md` and the root `ARCHITECTURE.md`: privileged builder
   delegation, Compose provider resources, the shared web/MCP operation core,
   workload-lineage trust boundaries, the four diagnostic planes, and the
   current public CLI inventory.
4. **README reset.** Replaced the 454-line README with a 149-line landing page.
   It now leads with product positioning, maturity, a source-verified
   loopback-only Docker-host evaluation path, an adjacent security warning, the
   five deploy backends, current client/agent/diagnostic surfaces, and links to
   the canonical VitePress pages. The detailed k3s walkthrough, direct-engine
   recipe, backend variants, and ecosystem comparison now live only in `docs/`.
5. **Record reconciliation.** Updated overview/architecture references that
   still described README as the k3s quick start or a container-Compose source.
   Removed resolved README TODOs D2, D3, U10, and U11; narrowed the remaining
   S4, U1, U3, U7, U9, and public-inventory items to the work still outstanding.

### Findings

**1. The user site was mostly ahead of the canonical summaries.** The VitePress
site already documented builder delegation, Compose providers, MCP modes,
activity, workload observability, ingress tunnels, and lineage. The principal
distillation gap was in agent orientation and architectural rationale, not in
new user-facing pages.

**2. README drift came from duplicating reference material.** Its embedded k3s
guide and comparison accumulated a transposed NodePort, a three-backend claim
after five backends shipped, an unauthenticated privileged NodePort on-ramp with
no adjacent warning, and roughly one hundred lines already maintained in the
comparison page. Making README a router into canonical pages reduces that drift
surface.

**3. The documentation surfaces now have explicit jobs.** README is the concise
repository landing page; `docs/` owns user tasks and interface reference;
`ARCHITECTURE.md` owns human-reader-ready system design; `OVERVIEW.md` orients
coding agents; JOURNAL/LTM/TODO retain findings, durable memory, and open work.

**4. Security and release/onboarding debt remains.** The docs k3s quick start
still exposes the privileged, authentication-off NodePort path without an
adjacent warning. The docs landing page still lacks the README's maturity
notice, and the tracked Helm-version, checksum-verification, pinned-manifest,
flag-consistency, and command/API-inventory follow-ups remain open in TODO.

### Validation

`git diff HEAD --check`, repository punctuation and Unicode scans, and local
README target checks passed. `npm run docs:build` completed successfully and
`npm run docs:check-anchors` checked 233 fragment links with 0 dead. The new
Docker example was verified against the current kong flags, connection-profile
resolver, dockerhost selection, Compose path, and builder-delegation code, but
was not executed against a live Docker daemon. No commit or push was made.

## 2026-07-27 — Metrics work: addendum (verification + a CLI footgun)

Follow-up to *Workload and server resource metrics on every backend* above. Two
things settled after that entry was written.

### `CORNUS_SERVER` does not reach `cornus compose`

While verifying the live PromQL names I ran `CORNUS_SERVER=http://127.0.0.1:18099
cornus compose up -d` against a throwaway test server — and deployed into a
DIFFERENT server entirely (the `demo` connection profile at `localhost:30500`).
The env var was ignored and nothing said so.

Verified, not inferred: `cornus compose up --help` mentions neither `--server`
nor `CORNUS_SERVER` (zero hits), while `observe`, `exec`, `port-forward`, `hub`,
`storage`, and `ingress-tunnel` all bind `env='CORNUS_SERVER'` on a `--server`
flag. Compose resolves the target purely through the connection profile /
current context (`cn.ViaServer`, `clientconn`), so an operator — or an agent —
who has internalized "`CORNUS_SERVER` picks the server" will silently deploy into
whatever context is current.

The failure is quiet in the worst way: `compose up` reported three replicas
running, and they *were* running, just not where intended. What exposed it was
the deployment being absent from the test server's `/.cornus/v1/deploy` list a
minute later. Worth knowing before the next agent debugs a metrics feed that is
sampling the wrong host's workloads. (The test deployment was removed.)

Not proposing a change here — compose's context-only resolution may well be
deliberate, since a compose project is a profile-scoped concept in a way a
one-shot `observe` query is not. Recording the asymmetry so it is not
re-discovered the same way.

### Final verification state

Everything green at hand-off: `gofmt -l` empty, untagged `build` / `vet` / `test`,
`make test-imbh` (cgo + `-race`, the leg that would catch an Arrow lifetime error
across the FFI boundary), `GOOS=darwin go build ./pkg/... ./cmd/...` (the reason
the portable half of `hostrun` had to be split out of `stats_linux.go` — the
kubernetes backend must compile where cgroups do not exist), `make e2e-check`,
`npm run docs:build`, and `docs:check-anchors` at 233 fragment links / 0 dead.
`observability-metrics.star` plus the six sibling observability scenarios pass
against a live Docker target.

135 files changed in the working tree; nothing committed, per the standing rule.

The one gap worth naming: `deploy.star` and `deploy-stats.star` fail on this host
and it is NOT from this change — the in-process BuildKit push needs `--rootless`
or privileges here, and an untouched scenario fails identically. That was checked
rather than assumed, because "my change broke the build scenarios" and "this host
cannot run build scenarios" look the same from the failure line.

## 2026-07-27 — Shipping observability out of the box (all-in-one release artifacts)

Goal: every prebuilt artifact carries the embedded OTel Collector (`otelcol`) AND
the built-in observability store (`imbh`), so a downloaded `cornus serve` records
workload telemetry with no flags. Previously the standalone binaries carried
neither and the image carried only `otelcol`.

### The finding that made it possible: musl-static cgo

The blocking assumption was "`imbh` means cgo means giving up the fully static
linux binary". That is false, and the fix is one linker shim.

`sable`'s `link_extern.go` hardcodes `-lgcc_s -lutil -lrt -lpthread -lm -ldl` for
linux with no musl variant, and upstream's own journal says musl-static "would
need the shipped flags dropped" — which is why they never tried it. But on musl,
`rt`/`util`/`dl`/`pthread` are already empty stub archives in libc; `-lgcc_s` is
the ONLY real gap, because Alpine ships no static `libgcc_s`. Pointing it at
`libgcc_eh.a` (which carries the unwinder symbols the Rust runtime actually wants
from it) closes it:

```sh
ln -sf "$(find /usr/lib/gcc -name libgcc_eh.a -print -quit)" "$shim/libgcc_s.a"
CGO_LDFLAGS="$CGO_LDFLAGS -L$shim" \
  go build -tags "netgo osusergo otelcol imbh sable_extern_lib" \
    -ldflags "-s -w -linkmode external -extldflags \"-static -L$shim\""
```

Verified on linux/arm64: a 244 MB **statically linked** binary that opens the
store on bare Alpine *and* on `distroless/static` (no libc present at all).
`netgo`/`osusergo` still hold under `CGO_ENABLED=1`, so DNS and user lookup stay
pure Go — the Rust runtime is the only reason cgo is on.

### Scope: the store is server-side only

`obsstore.Open`/`Compiled` have exactly one caller, `pkg/server/obsstore.go`.
`cornus observe` is a thin API client, so a CLI pointed at a cornus server always
had full observability; only the SERVING artifact ever needed the tag. Worth
knowing before assuming a change here affects client-side commands.

### Different artifacts, different linkage — on purpose

- **Image**: glibc cgo. The build stage (`golang:1.26-bookworm`) and the final
  stage (`debian:bookworm-slim`) are the same Debian 12, and the binary never
  leaves the image — the caretaker sidecar IS this image, the bare-host shim
  re-execs in place, and `builderctr`'s self-built image matches the host distro
  by design (`selfimage.go`). Static buys nothing here.
- **Released binaries**: musl static, because "runs on any distro" is their job.

### `--obs` is now tri-state

`Obs *bool` with `negatable`, resolved by `resolveObsEnabled` (cmd/cornus/serve.go):
unspecified follows `obsstore.Compiled()`, so it is ON in every released artifact
and OFF in a plain `go build` binary — which keeps a dev build from warning about
a feature nobody asked for, while an explicit `--obs` on a stub build still gets
the loud "not compiled in" message. Kong supports `*bool` tri-state cleanly
(verified: nil / true / false for unset / `--obs` / `--no-obs`).

### `cornus version --features` and why the release depends on it

A mistyped build tag still COMPILES — it just selects the stub — and exit status,
file size and `file` output all look fine. So the binary now reports its own
compiled-in feature set (`obsstore`, `otelcollector`), rendered as aligned text or
JSON via the existing `KVBlock`. The Dockerfile and
`.github/scripts/build-release-binary.sh` both grep `"obsstore":"yes"` and fail
the build otherwise. `TestVersionFeaturesJSONContract` pins the key names and the
compact JSON spelling, because they are now a build-pipeline contract.

This is also why the release legs went NATIVE (one runner per target) rather than
cross-compiled: a natively built artifact is runnable on the machine that made it,
so it can be asserted rather than trusted.

### Release lineup: five, not six

Dropped windows/arm64. Upstream imbh-go v0.1.0 publishes archives for linux
amd64/arm64 x glibc/musl, darwin amd64/arm64, and windows amd64 — no
windows/arm64 cell — and shipping the one binary without the store would defeat
"always all-in-one". (A `windows-11-arm` runner does exist; the archive is the
constraint, not the runner.)

`workflow_dispatch` now builds all five without publishing, as a dry run before
tagging.

### Risks accepted, and why they are named here

- **darwin and windows imbh linking has never been exercised.**
  `link_darwin.go` says the `-framework` set is "finalized empirically on a macOS
  runner"; `link_windows.go` calls Windows support "best-effort". Those three legs
  will link for the first time in CI. The Windows leg has a second unknown: the
  `eval` of `imbhgo-fetch -print-env` carries a backslashed Windows path into
  `CGO_LDFLAGS`. Both fail loudly by design.
- **Size**: linux/arm64 103 MB -> 244 MB (the archive alone is 608 MB
  decompressed); image 377 MB. Documented on the installation page.
- **Rust crate attribution gap**: `go-licenses` walks Go modules only, so the
  crates statically linked inside `libimbhgo.a` are not enumerated in
  `THIRD_PARTY_NOTICES.md`. Disclosed in `NOTICE`; needs an upstream notices
  manifest. TODO item added.

### Licenses

`LICENSE_TAGS` now matches what ships, in both the Makefile and the Dockerfile —
previously the bundle was generated with `netgo,osusergo` only, so it listed
neither the collector tree nor imbh-go/sable at all. Re-audited with the shipped
tags: **350** Go modules (was 228), zero strong-copyleft, zero needing review;
imbh-go Apache-2.0, sable MIT, arrow-go Apache-2.0, collector Apache-2.0. MPL-2.0
count is 8 — the six known HashiCorp ones plus `cyphar/filepath-securejoin` and
`hashicorp/go-version`, both unmodified, so notice retention suffices.

Note for future audits: `scan_licenses.py` runs `go list -deps` with whatever
`GOFLAGS` is in the environment, so it MUST be invoked with the shipped tags or it
silently under-reports. It also needs the go1.26 toolchain bin dir first on PATH
or go-licenses aborts with "Package X does not have module info".

### Verification state

`gofmt -l` empty; `go build ./...`, `go vet ./...`, `go test ./...` green;
`actionlint` clean on release.yml + ci.yml; `shellcheck` clean on the new build
script; the release script exercised end-to-end in `golang:1.26-alpine` producing
a verified-static binary; `docker build` green with the assertion firing and
`docker run cornus serve` (no flags) logging "observability store open";
`npm run docs:build` + `docs:check-anchors` at 233 links / 0 dead.

NOT verified, and only CI can: the darwin and windows legs.

## 2026-07-27 — All-in-one artifacts: addendum (the emulation trap, i18n, and gotchas)

Follow-on to the entry above. Everything here was found or finished after it was
written, so it is recorded separately rather than folded in.

### The risk I nearly shipped silently: the image's emulated arm64 leg

The binaries job was moved to native per-target runners precisely because cgo
needs a native toolchain. The `image` job was NOT, and that asymmetry is easy to
miss because it is unchanged code: it still builds `linux/amd64,linux/arm64` on a
single amd64 runner via QEMU, and the Dockerfile's build stage is deliberately not
`$BUILDPLATFORM`-pinned (that non-pinning is what gives each arch a native gcc and
the right libc cell — it is load-bearing, not an oversight).

Consequence: the arm64 image leg now links a ~600 MB Rust archive under qemu-user.
That was merely slow at `CGO_ENABLED=0`; it may now be very slow or OOM. Two
things make this worth writing down rather than just fixing:

- The fix is not local. Splitting into native legs (`ubuntu-24.04` +
  `ubuntu-24.04-arm`) plus `docker buildx imagetools create` also moves the cosign
  step, which signs by manifest-list digest. Doing that blind, to solve a problem
  not yet observed, would be worse than measuring first.
- It is cheaply measurable: the `image` job has no tag gate, so the new
  `workflow_dispatch` dry run exercises it. Measure, then decide.

Related upstream data point that says "measure, do not assume": sable's own journal
records that **rustc/LLVM segfault under qemu-user**, which is why their emulation
harness cross-compiles natively and only *runs* test binaries emulated. We do not
run rustc (the archive is prebuilt), so the failure mode here is the linker, not
the compiler — a different question, and one nobody upstream has answered.

### Dockerfile gotcha: `#` comments inside a continued RUN

The build assertion is a multi-line `RUN ... ; \` with `#` comment lines between
continuations. That works — BuildKit strips whole-line comments before joining
continuations, confirmed empirically (the build log shows the joined command with
the comments gone). Worth knowing, because the alternative reading (comment
swallows the rest of the joined line) would silently disable the assertion, which
is exactly the class of bug the assertion exists to catch.

Also switched the licenses step's `&&` chain to `;` under `set -eu`, so a
mid-sequence go-licenses failure still aborts the stage.

### `--obs` messaging follow-through

`pkg/server/obsstore.go`'s "not compiled in" remedy used to say "use a cornus image
variant that ships it". That advice inverted the moment the image started shipping
the store, so it now names the rebuild flags and points at released artifacts. The
comment above it also records WHY reaching that branch implies a hand-built binary:
an unspecified `--obs` resolves to `obsstore.Compiled()`, so only an explicit
`--obs` can get there in a stub build.

### Docs and translations

The doc claims that went stale were not just "requires the tag" — three were
subtler:

- The gateway paragraph said the store-less mode is what "a plain build with no
  `imbh` tag can do". With imbh in every release, "plain build" now means the
  opposite of what it meant. Rewritten around `--no-obs` in all three locales.
- `docs/introduction/*` advertised "prebuilt **static** binaries for linux,
  darwin, and windows". Still true for linux, no longer for darwin/windows (and
  arguably never was — Go always links libSystem on darwin). Installation now
  carries an explicit per-platform asset table, the batteries-included list, the
  static-linux guarantee scoped to linux, and an honest note that the download is
  now a few hundred MB with `--no-obs` as the escape hatch.
- `ARCHITECTURE.md`'s release-artifact section described cross-compilation and a
  six-binary lineup.

Translation notes: ja/zh mirrors updated for all three, terms taken from the
glossaries (`observability store` -> オブザーバビリティストア / 可观测性存储). Verified
my added lines specifically — not the whole files — for full-width parens/colons
and decomposed kana, since both pre-exist in the zh tree as established style and
a blanket scan only produces noise. The one decomposed-kana hit in
`docs/ja/guides/observability.md` is inside a `#fragment`, the documented
legitimate exception.

Found a PRE-EXISTING gap while there: neither translated `server-env-vars.md` has
any `CORNUS_OBS*` rows at all (315/274 lines vs 387 English). Not caused by this
change, but more visible now that the store defaults on. TODO item added rather
than silently backfilled, since a missing section is a translation task, not a
punctuation fix.

### Audit tooling caveat worth remembering

`scan_licenses.py` shells out to `go list -deps` inheriting the ambient
environment, so it reports whatever `GOFLAGS` says. Run it without the shipped
tags and it under-reports by 122 modules while still printing a confident green
VERDICT. That is a footgun for every future audit, not a one-off: the skill's
"known-good baseline" of 228 modules is now 350, and a future run that sees 228
again means the tags were forgotten, not that dependencies shrank.

### Final verification delta

Beyond the previous entry: `go test ./...` fully green; the new
`cmd/cornus/serve_obs_test.go` cases pass in BOTH build flavors (stub and
`imbh,sable_extern_lib,otelcol`) — which matters because
`TestResolveObsEnabledDefaultMatchesThisBuild` and `TestVersionFeaturesJSONContract`
deliberately assert different values per flavor, so passing in one proves little;
`make e2e-check` parses every scenario; `actionlint` clean across ALL workflows;
`shellcheck` clean on `.github/scripts/build-release-binary.sh`.

### TODO items opened

Four, all in `.agents/docs/TODO.md`: prove the darwin/windows legs via a dispatch
run before tagging; measure the emulated arm64 image leg; reproduce Rust crate
attribution for `libimbhgo.a`; backfill the translated env-var references. The
first two are release-blocking in the sense that they should be answered before
the next tag, not before merge.

## 2026-07-27 — E2E containerd leg: `compose.star` published port dies with EHOSTUNREACH

### Symptom

The `E2E (containerd)` job failed on `e2e/scenarios/compose.star`
(run 30208734659), and only there — docker, kube, and kube-ingress were green:

```
scenario failed scenario=e2e/scenarios/compose.star
  error="Get \"http://127.0.0.1:8080/\": dial tcp 127.0.0.1:8080: connect: no route to host"
```

`compose ps` had already reported both services `1/1 running`, and the 30s
`http_get` retry never got past the dial. EHOSTUNREACH on a loopback address is
the tell: portmap's DNAT rewrites the destination to the container's CNI IP, so
the failure is in routing to that IP, not in the workload.

### Reproduction

`make e2e-container E2E_TARGETS=containerd E2E_SCENARIOS=e2e/scenarios/compose.star`
**passes**. The failure only appears in the full containerd subset, so it is an
interaction with the scenarios that run before it. Dumping the runner's
networking mid-failure (poll `docker logs` for the scenario banner, then
`docker exec` `ip -o addr` / `ip route` / `iptables-save -t nat`) showed it
plainly:

```
3: crnsedab337a  inet 10.4.1.1/24 ... linkdown     <- mcpe2e_default, reaped scenarios ago
15: crnsfc008951 inet 10.4.1.1/24 ...              <- e2e_default, live
10.4.1.0/24 dev crnsedab337a proto kernel scope link src 10.4.1.1 linkdown
10.4.1.0/24 dev crnsfc008951 proto kernel scope link src 10.4.1.1
```

Two bridges, one `/24`, two routes — the dead one matched first, so the DNAT'd
packet was handed to a link-down interface.

### Root cause

`hostrun.RemoveNetwork` deleted a reaped network's conflist and released its `/24`
index, but never deleted the bridge. The CNI bridge plugin is create-only: it
brings the bridge up on first attach and leaves it there even after the last veth
leaves. So a freed index handed to the next network produced a second bridge on
the same subnet.

This was latent until `mcp-stdio-tools.star` (a Compose project, hence its own
`mcpe2e_default` network) was added to `CONTAINERD_SCENARIOS` ahead of
`compose.star`. Before that, nothing in the containerd subset created and reaped
a *user* network before compose ran, so index 1 was always fresh. Bridge names
are `crns` + `sha256(network)[:8]`, which is what identifies the corpses:
`crnsedab337a` = `mcpe2e_default`, `crnsfc008951` = `e2e_default`.

### Fix

`pkg/deploy/internal/hostrun/`:

- `RemoveNetwork` now deletes the network's bridge **before** releasing the
  subnet index, so a delete failure keeps the index allocated rather than
  handing it out over a live bridge. Shared by both host backends
  (containerd + bare).
- `EnsureNetworks` sweeps any *other* `crns*` bridge already carrying the
  gateway address of a newly allocated `/24` (new `bridge_linux.go`). Only a leak
  can hold it — the allocator never gives one index to two live networks — so
  this self-repairs hosts that leaked bridges under the old code, or where cornus
  died before reaping, or where the data dir was wiped while the host kept
  running. Best-effort: a repair, not a precondition.
- Both netlink calls sit behind package vars (`deleteBridgeLink`,
  `dropStaleBridges`) so `network_linux_test.go` can assert the lifecycle without
  root, and so unit tests never touch the host's real links.

### Regression coverage

`TestRemoveNetworkDeletesBridgeBeforeFreeingSubnet` in
`pkg/deploy/internal/hostrun/network_linux_test.go` asserts the three things that
actually matter, all without root: the reaped network's bridge is the one deleted,
the sweep runs for the index when it is handed out again, and a bridge that cannot
be deleted leaves the subnet allocated. The pre-existing allocator tests now stub
the same hooks so `go test` never enumerates or deletes the host's real links —
worth keeping in mind if these tests are ever run as root.

The end-to-end assertion stays in `compose.star`, which only catches this when it
runs after another Compose project has been reaped. That ordering is now the
containerd/bare subset's default, so the coverage is real but implicit: reordering
`CONTAINERD_SCENARIOS` to put `compose.star` first would silently retire it.

### Verification

`gofmt`/`go build ./...`/`go vet ./...`/`go test ./...` all clean. The full
containerd E2E subset — the exact CI invocation, `docker run --rm --privileged
-e E2E_TARGETS=containerd cornus-e2e:<tag>` — reproduced the failure before the fix
(`1 scenario(s) failed`, identical error string to CI) and reports
`all 9 scenario(s) passed` after it.

Not this bug, seen while reading run history: the docker leg of run 30210839159
failed on a Docker Hub `502 Bad Gateway` pulling `debian:bookworm-slim` during the
runner image build. Registry flake, unrelated to the containerd failure, and the
run was superseded by a newer push anyway.

### Worth remembering

- EHOSTUNREACH to `127.0.0.1:<published port>` on a CNI/portmap backend means the
  DNAT target is unroutable, not that the port is unpublished. A missing publish
  gives ECONNREFUSED instead.
- A scenario that passes standalone but fails in the suite is host-state
  contamination. The containerd/bare backends keep real host state (bridges,
  netns pins under `/run/cornus/netns`, iptables chains) that outlives the
  per-scenario server, so scenario order is part of the test surface.
- Bridge names are content-addressed (`crns` + `sha256(network)[:8]`), so a
  leaked interface can always be traced back to the network that created it —
  hash the candidate names and compare. That is what turned an anonymous
  `crnsedab337a` into "`mcpe2e_default`, from four scenarios ago".
- The technique generalizes: to inspect a failure inside the E2E runner, poll
  `docker logs` for the scenario banner and then `docker exec` diagnostics during
  the scenario's own retry window. The runner exits and is removed when the suite
  ends, so post-mortem inspection is not available.
- Adding a scenario to `CONTAINERD_SCENARIOS`/`BARE_SCENARIOS` changes what runs
  *after* it, not just what runs. `mcp-stdio-tools.star` was correct in isolation
  and correct where it was placed; it simply exposed a leak that had been dormant
  because nothing had ever reaped a user network mid-suite before.

## docker proxy: `volume rm` / `volume prune` now reach the deploy backend

`pkg/dockerproxy/volumes.go` treated named volumes as unbacked fakes: DELETE
`/volumes/{name}` just did `delete(p.volumes.byName, name)` and answered 204, and
POST `/volumes/prune` returned a hardcoded empty prune result. That was true when
the store was written, but `toDeploySpec` now translates named Docker volumes into
real `DeploySpec.Volumes`, so the backend really does provision persistent storage
— the proxy was reporting successful removals while the data survived.

- `deployAttacher` (attach.go) gained `DeleteVolume(ctx, name) error`. The only
  production implementer is `*client.Client` (`pkg/client/client.go:480`), which
  already had the method; the in-package fake (`fakeAttacher`, proxy_test.go)
  records the names it was asked to remove. `blockingAttacher` (state_test.go) is
  an `attachsession` attacher, not a `deployAttacher`, so it is unaffected.
- DELETE now 409s (`volume is in use - [<container ids>]`) when any container
  record — running or not, as in Docker; only `docker rm` releases the reference —
  references the volume, otherwise calls the backend and only then forgets the
  local name. A backend without `deploy.VolumeRemover` surfaces as 501 with an
  explicit "does not support removing volumes" message; any other failure is 500.
  Never 2xx while the data is still there.
- Prune follows the Engine API the proxy advertises (1.43): since 1.42 prune only
  reclaims ANONYMOUS volumes unless the `all` filter is set. The proxy tracks only
  named volumes, so a default `docker volume prune` truthfully reclaims nothing,
  while `--all` removes every unused tracked volume from the backend and reports
  them in `VolumesDeleted`. `SpaceReclaimed` stays 0 — no backend reports a removed
  volume's size.
- Removal is deliberately lenient about names the store never saw (the index is
  in-memory and does not survive an agent restart, and `docker run -v name:/path`
  never calls `/volumes/create`): the server's delete-if-exists contract makes an
  unknown name a no-op success, which is better than 404ing while real data sits
  in the backend.
- Tests: `pkg/dockerproxy/volumes_test.go` covers backend-reaching removal,
  in-use rejection plus removal after `docker rm`, prune with and without `all`,
  the unsupported-backend path, and a generic backend error.

## `deploy_attach` readiness is now the session's own event, not `Status(name)`

The E2E harness's `deploy_attach` waited by polling `client.Status(name)` until
`countRunning(st) >= replicas`. That condition is satisfied by ANY running
instance carrying the deployment's label — including one a previous FAILED run
left behind on the same daemon (host docker/containerd persist across harness
runs). The wait then returned instantly, before this run's `cornus deploy` had
applied anything, and the scenario asserted against a half-empty world. It cost
two flaky `activity-flight-record` failures; the per-scenario `docker rm -f`
guard there was a workaround, and Starlark has no `defer`, so every scenario
whose workload can outlive a failure inherited the same edge.

Fix (harness only, `pkg/e2e/harness.go`): wait for a RUN-SCOPED signal that the
deploy path already carries. `pkg/server/deploy_attach.go` sends
`Event{Ready: true, Log: "deployed <name>"}` only after IT applied this session's
spec and `awaitReady`-ed every desired instance, and `cmd/cornus/commands.go`
prints `deployed <name>: R/T instances running` off that same event. So the
harness tees the child's output (`io.MultiWriter(h.out, &lockedBuf)`) and gates
on `sawAttachReady(out, name)` — a marker only the child it just started can
produce. Nothing observable in `Status` is run-scoped: instance IDs are not
(a kube re-apply of an identical spec never recreates the pod, so an ID diff
would hang forever), and `Origin` carries host/user/dir/git/project, all
identical across runs of the same scenario.

Notes:
- The child gets `CORNUS_OUTPUT=plain NO_COLOR=1` so an ambient renderer setting
  cannot move the line the wait reads.
- After the marker, `Status` is read back once (bounded retry) purely for the
  returned dict — a report, not the gate.
- Timeout / early-exit errors now include the child's captured output, which is
  where the reason is.
- Readiness is now the SERVER's definition (all desired instances ready, and for
  a one-shot an instance that exited 0), which is stricter and correct for
  `restart="no"` workloads that finish before a poll.
- Tests: `pkg/e2e/deploy_attach_test.go` — a fake status server that always
  reports a leftover as running plus shell stubs standing in for `cornus deploy`
  (no daemon). Covers: leftover must NOT satisfy the wait, the session's own
  marker must, a sibling name (`app2`) must not satisfy `app`, an early exit
  reports the child's output, and the marker matcher's boundary rule.


## `depends_on: condition: service_healthy` on a backend that cannot report health

Symptom: on containerd/bare/incus a required `service_healthy` dependency was
unsatisfiable BY CONSTRUCTION — those backends drop `healthcheck:` at Apply time
with a warning, so `InstanceStatus.Health` is permanently `""` — yet the client
polled it for the full `completionWaitTimeout` (5m) before failing with a generic
"dependency %q of %q not service_healthy within 5m0s". A required dependency must
not spend the whole timeout on a state the backend can never produce.

Fix (option 1 of the triage: detect at planning time, not implement probes).

- New OPTIONAL backend capability `deploy.HealthReporter { ReportsHealth() bool }`
  (`pkg/deploy/deploy.go`), discovered by type assertion like the ~11 other
  capability interfaces. Implemented by `dockerhost` (Docker's own HEALTHCHECK
  engine; `Status` reads `State.Health.Status`) and `kubernetes` (`healthProbe`
  -> liveness/readiness, `healthFromReady` -> Health). containerd/bare/incus do
  not implement it. Deliberately POSITIVE: a backend that grows a probe engine is
  believed at once, and no client-side backend-name list exists to rot.
- Surfaced to clients as `api.BackendInfo{Name, ReportsHealth}` on
  `api.ServerInfo.Backend`, filled by `(*Server).advertisedBackend` in
  `GET /.cornus/v1/info`. Unlike `advertisedRegistry`/`advertisedIngress` it cannot
  short-circuit on the backend NAME (the whole point is asking the backend), so it
  constructs it — `getBackend` caches, so at most one construction per process.
  Best-effort: no factory / construction error -> nil -> client assumes capable.
- `cmd/cornus/internal/composecli`: `runtime.serverInfo` memoizes one `/info`
  fetch per command run (also now feeding `warnServerVersionSkew`, which lost its
  own fetch and takes the `api.ServerInfo`). `validateDependencyConditions` runs
  in `UpCmd.Run` BEFORE any build/deploy and fails a required `service_healthy`
  dependency; `waitForDependencies` repeats the check at the seam that would
  otherwise wait, so no caller can burn the timeout (optional deps warn and skip
  the poll instead of warning after it).

Contract kept: unknown capability (older server, unreachable server, backend that
would not construct) reads as CAPABLE, so nothing new fails on a fact we could not
learn. `service_started` / `service_completed_successfully` never consult Health
and are untouched; provider (`provider:`) dependencies are exempt (they gate on the
plugin's readiness channel, not container status).

Error the user now sees:

    service "web" depends on "db" with condition: service_healthy, but the
    "containerd" deploy backend does not report container health (it has no probe
    engine, so the healthcheck is dropped and "db" can never be observed healthy):
    use condition: service_started, or deploy to a backend that reports health

Tests: `reconcile_test.go` (fake backend without the capability -> actionable
error with ZERO polls and no timeout consumed; optional variant warns + skips;
same project on a health-reporting backend still polls to satisfaction;
`service_started` identical on both; `validateDependencyConditions` matrix incl.
"backend unknown" and "dependency not selected"), `server_info_test.go`
(`advertisedBackend` over a fake backend with/without the capability, an erroring
factory, and no factory).

## 2026-07-27 — TODO sweep: triage, then waves 1-2 (groups A-D)

Full triage of `.agents/docs/TODO.md` (1,120 lines / 123 open + 10 partial across 17 ad-hoc
sections) into nine groups, then execution of the first two waves. Groups E (English docs) and F
(ja/zh localization) were handed to a separate concurrent agent and are not covered here. The
triage document is at `.agents-workspace/tmp/todo-sweep-triage-2026-07-27.md`.

### The backlog was not trustworthy as written

45 claims were re-verified against the tree before any dispatch. Findings that changed the plan:

- **~11 items were already done**, including two marked BLOCKING. `docs:check-anchors` reports 233
  fragments checked / 0 dead, so both "dead localized fragment" items and the "8 broken locale
  anchors" item were stale. `release.yml` already had `workflow_dispatch` at `:26-29`. Three ja/zh
  sync items (bare backend, Compose `provider:`, `cli/setup.md` existence) were already synced.
- **3 items understated the problem.** `CORNUS_KUBE_QPS`/`BURST` is undocumented in *English* too,
  not just the translations; `IngressCertificate` is missing from zh as well as ja; `cli/index.md`
  omits five commands (`observe` too), not four.
- **1 duplicate**: the `CORNUS_OBS*` backfill appears at `:11-18` and `:79-83`, and the row count is
  10, not 12.
- The structural translation audit reports **9** issues across **7** files, not 10 —
  `guides/observability.md` and `reference/server-env-vars.md` have dropped to warnings.
- **Zero `// TODO` / `// FIXME` markers exist in the Go tree**, so the sweep is entirely TODO.md
  driven.

Lesson worth keeping: a backlog assembled from audit reports decays silently. Verify before
dispatching — roughly 1 item in 8 was already closed, and dispatching on them would have produced
confident no-op reports.

### One triage entry corrected during execution

The fresh-eyes review lists the empty JWT `scope` granting full access as a fail-open defect (S4).
It is fail-open, but `pkg/authtoken/authtoken.go:38-40` documents it as deliberate backward
compatibility ("a plain JWT with no scope is a full credential, as before scopes existed").
Changing it invalidates existing tokens, so it is a decision, not a mechanical fix. Moved back to
the parked group rather than shipped.

### Waves 1-2 — 18 items closed

Mechanical: `VolumeRemover` godoc reattached (it was rendering under `IngressGateway`); stale
"named volumes are skipped" comment in `dockerproxy/translate.go`; `ingress-tunnel-ssh.star` guard
narrowed to the kube target (it was dropping docker coverage whenever `E2E_INGRESS_NGINX=1`);
`deploy/k8s/cornus.yaml` off the never-published `:0.1.0` pin; the 7.9 MB tracked aarch64
`cornus-e2e-ftpd` binary removed.

CLI: `--version` as a flag; `ShortUsageOnError` (unknown-flag output 169 lines -> 4); `--server`
alias on `compose`; `mount-agent`/`mountcheck` deprecations dropped.

Correctness: Compose long-syntax port validation (`published: bogus` silently published the
*target* port); `docker volume rm`/prune now reach the real backend; `cornus push` falls through to
`authn.DefaultKeychain` for non-destination registries; `depends_on: condition: service_healthy`
fails fast on backends with no probe engine; `deploy_attach` readiness keyed on a run-scoped marker.

CI/release: `web` job (the SPA gate that had never run despite being embedded in every binary);
`image` job building the root Dockerfile with a `/healthz` smoke; containerd scenario-list drift
fixed and guarded by `TestScenarioSubsetsInSync`; release gated on tests and failing closed across
image+chart+binaries; `THIRD_PARTY_NOTICES.md` shipped as a release asset; `QUALITY_GATE.md`
section 4 resynced (it described two commands where CI runs seven jobs).

### Three agent results that needed correcting

1. **`serve.go` log ordering** was first implemented as a 250 ms race between `srv.Run` in a
   goroutine and a timer — nondeterministic under load, and a worse trade than the cosmetic bug it
   fixed. Replaced with a real `Server.Ready()` channel closed at the existing
   `s.ready.Store(true)` bind point, guarded by a `sync.Once` and a nil check (tests build bare
   `&Server{}` literals). Verified live: a second server on an occupied port prints only the bind
   error.
2. **`advertisedBackend()` introduced a hot-path regression.** It constructed the deploy backend
   inside `handleInfo`, and `getBackend` caches only on SUCCESS — so a registry-only server, or one
   whose daemon is unreachable, would retry construction (holding `backendMu`) on every `/info`
   call, which every CLI command makes for its version-skew check. Both sibling helpers
   (`advertisedRegistry`, `advertisedIngress`) deliberately gate first to avoid this, and
   `TestAdvertisedRegistryNonK8sBackendSkipsIntrospection` already encoded the rule. Fixed by
   memoizing the answer with `sync.Once`; a failure caches as nil, which the client reads as
   "unknown" and treats permissively. Regression tests assert one construction across five
   resolutions plus a `-race` concurrent case.
3. **`TestBearerForRegistry`** asserted `got != authn.Anonymous` for a non-destination host, which
   becomes environment-dependent once the keychain falls through to the developer's Docker config.
   Retargeted at the real invariants: the cornus token never reaches another host, and the result
   matches `DefaultKeychain`'s.

### Held back deliberately

- **bare/incus E2E CI legs** — S1 proposes dropping exactly those two backends. Wiring CI first
  could be wasted work; blocked on that decision.
- **The TODO.md restructure** (rewriting it around the nine groups) — a wholesale rewrite would
  clobber the concurrent docs agent's edits. Only surgical `- [x]` flips were applied.
- **Group F (ja/zh) is gated on S5**, which proposes freezing both locale trees until 1.0. It is
  the largest block in the backlog (~15 items) and the cheapest to cancel, so it should be decided
  before any localization work is scheduled.

### Gate

`gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` all green; `make e2e-check`
passes; both `ci.yml` and `release.yml` parse and their job graphs were dumped and reviewed.

## 2026-07-27 — S1 decided: keep all five backends, close the gap with CI

S1 ("decide what to CUT") is resolved: **cut nothing**. All five deploy backends and Knative stay;
the verification gap that motivated the proposal is closed with CI legs instead.

Measured before deciding:

| backend | prod LOC | test LOC | ratio | E2E scenarios | in CI |
|---|---|---|---|---|---|
| kubernetes | 6,491 | 7,049 | 1.09 | 127 | yes (2 legs) |
| barehost | 5,368 | 2,064 | **0.38** | 15 | **no** |
| containerdhost | 4,177 | 3,578 | 0.86 | 10 | yes |
| dockerhost | 2,818 | 2,904 | 1.03 | 127 | yes |
| incushost | 1,563 | 733 | 0.47 | 9 | **no** |

The review's instinct correlated with something real — the two backends it named for cutting are
exactly the two with no CI leg and the two worst test ratios. But its central "breadth is costing
depth" evidence turned out to be half stale, which is why the conclusion did not follow:

- "logs, stats, exec, and cp act on the first instance only" — **confirmed**,
  `pkg/deploy/deploy.go:255-280`.
- "kubernetes projects a ready count onto fabricated `<name>-<i>` slots so no real pod states (e.g.
  CrashLoopBackOff) are ever surfaced" — **outdated**. `pkg/deploy/kubernetes/kubernetes.go:4064-4073`
  zips the synthesized instances to name-sorted pods and sets `inst.Message =
  instanceDiagnostic(...)` specifically for crash-looping containers, image-pull failures, and
  unschedulable pods, plus a real `ExitCode`. Only the `State` field is binary running/pending.

So the surviving depth concern is the Docker wire-format constraint baked into the `Backend`
contract — which is **S2**'s subject, and an argument for narrowing the interface, not for deleting
backends.

Two further facts argued against the specific cut proposed. `barehost` is the second-largest
production surface AND the most entangled — it is the reference implementation for
`MountingBackend`, `EgressBackend`, `RemoteCapable`, and `AgentForwardCapable`, and the incus
backend was explicitly designed to mirror it; removing it means unpicking those integrations. It is
also the only daemonless backend, deliberately Moby/BuildKit-free in its deploy dependency tree, so
it is not a duplicate of the other two host backends.

Follow-ups recorded: bare + incus E2E CI legs (with the explicit requirement that an unavailable
daemon fails the leg rather than green-skipping), and a new open item to raise both backends' unit
test ratios — the CI legs prove happy paths, not the capability integrations.

## 2026-07-27 — Live E2E CI legs for the bare and Incus deploy backends

Closes the "HIGH: Add live E2E CI legs for the shipped bare and Incus backends" item recorded when
the maintainer decided to KEEP all five deploy backends and close the verification gap with CI.
`.github/workflows/e2e.yml` now has six legs: `docker`, `kube`, `containerd`, **`bare`**,
**`incus`**, `kube-ingress`.

**No host provisioning is needed for either new leg** — this was the open question for incus. Both
are one privileged `docker run` of the SAME all-in-one runner image the other legs use:
`e2e/container/Dockerfile` already stages runc + the CNI reference plugins (bare) and installs
**incus 7.2 from the Zabbly apt repo** with skopeo/umoci (incus), and `entrypoint.sh`'s
`start_incus` launches incusd in-container and initializes it with a NAT/firewall-free bridge
preseed. So the incus leg needs neither a self-hosted runner nor an incusd on the GitHub-hosted
host; nothing dials out of the container.

**Fail-closed (the explicit requirement: an unavailable CI daemon must be RED, not a green skip).**
Two audited green-skip paths existed, and both are now gated by a new `E2E_STRICT` entrypoint knob
(default 0, so the existing four legs are untouched):

1. `start_incus` returns 2 -> `continue` -> `rc=0` when incusd is older than 6.3 (no OCI
   application containers). On a dedicated incus leg that is a job that deploys nothing and goes
   green. Under `E2E_STRICT=1` it is a failure.
2. `setup_bare`'s `prepare_bare_agent_image` is best-effort; without the image,
   `deploy-egress-bare.star` and `deploy-mounts-sidecar-bare.star` print "skipped" and exit 0 —
   2 of the 15 bare scenarios. Under strict, a failed build fails the target (and `setup_bare` now
   propagates it explicitly, since its `if !` call site disables `set -e` inside the function).

A second knob, `E2E_PREFLIGHT_ONLY=1`, runs the real per-target setup and then
`cornus-e2e --preflight` INSTEAD of the scenarios; the new legs run it as a separate up-front step
so a missing daemon fails in ~1 minute with a legible message rather than deep inside the suite.
The harness preflight was already a hard gate (`CapIncus` = incus CLI + reachable daemon +
skopeo/umoci; `CapOCIRuntime` = runc on PATH as root). Both knobs are plumbed through
`make e2e-container` / `e2e-bare-container` and a new `make e2e-incus-container`, so a leg is
reproducible locally verbatim.

**Verification (all local; GitHub Actions cannot be executed here).** Fault injection against the
built image proved the leg reports failure rather than skipping:

| Injected fault | `E2E_STRICT=0` | `E2E_STRICT=1` |
| --- | --- | --- |
| incusd faked to report `6.0.0` | exit **0** (the hole) | exit **1** |
| incusd binary unusable | exit 0/2 path | exit **1** |
| `crane` masked (bare agent image unbuildable) | exit **0**, "preflight OK" | exit **1** |
| `runc` masked | — | exit **1** (`oci-runtime` preflight) |

Then the real suites, both fully green: `make e2e-bare-container E2E_STRICT=1` -> **15/15** with
zero "skipped" lines (the egress + sidecar-mount companion scenarios really ran), and
`make e2e-incus-container E2E_STRICT=1` -> **9/9**. Runtime ~5 min (bare) and ~4 min (incus) of
scenario time, well inside the job's 60-minute budget.

**Finding: `mcp-stdio-tools.star` never actually worked on incus.** It was in `SCENARIOS_INCUS` /
`INCUS_SCENARIOS` (the LTM's "7/7 green" predates the two mcp scenarios), and its `logs_tail`
assertion fails deterministically — byte-identical across two runs — because the incus backend
serves the instance CONSOLE (one raw PTY on PID 1), which for an OCI application container carries
the shell prompt `/ # `, never the workload's startup marker. That is the same structural
limitation that keeps `compose-logs.star` out of `SCENARIOS_INCUS`. Rather than drop the scenario
(losing graph/exec/file-read coverage on incus), the content assertion is now guarded by
`if TARGET == "incus"`; the tool call itself is still asserted to succeed. Re-verified after the
guard: passes on incus AND still passes on docker (the unguarded branch).

Scenario subsets were NOT changed, so `TestScenarioSubsetsInSync` stays green.

## 2026-07-27 — incushost unit coverage: 55.0% -> 94.3%

Follow-up to the decision to KEEP the incus backend: the point of keeping it was to close the
verification gap, so `pkg/deploy/incushost` was unit-tested for real rather than left at 55% with
26 zero-coverage functions.

**Key insight: `realConn` is testable without a daemon.** The 14 uncovered functions in
`backend_linux.go` were mostly the `realConn` adapter over `incus.InstanceServer`. Both
`incus.InstanceServer` and `incus.Operation` are interfaces, so a test struct that EMBEDS the
interface and overrides only the ~11 methods the adapter calls is enough — every unimplemented
method stays a nil panic, which makes an unexpected call loud instead of silent. That covers the
adapter's real logic: 404 -> `(nil, nil)`, 404 -> `deploy.ErrNotFound` on lifecycle actions (from
the request AND from `op.Wait`, which Incus can race), delete-if-exists, and "never wait on an
operation that was never started".

**`New` is testable too, over a unix socket.** `incus.ConnectIncusUnix` only needs a socket serving
`GET /1.0`; an `http.Server` on a `net.Listen("unix", ...)` in a temp dir (no daemon, no root, no
network) drives the REAL Incus client. This proves the thing that actually matters about `New`: the
`UseProject` scoping reaches the wire (`/1.0/instances?project=...`), so instances of another
project can never be seen as cornus deployments. Note `op.Wait` on that path needs a websocket
event listener, so async operations are NOT exercised through the real client — that is what the
`realConn` fake covers instead.

**Production seam (one, small).** `execSession.control` changed from `*websocket.Conn` to a
`wsControl` interface (`WriteJSON(any) error`, which `*websocket.Conn` satisfies), and the body of
`ExecStart`'s control callback moved into `(*execSession).attachControl`. No behaviour change; it
makes the documented resize/connect race fix (a resize arriving before the control channel is live
must be flushed on connect) assertable without a websocket peer.

**Deliberately left uncovered** (listed so a future reader does not "fix" it with theater): the
`Control`/`DataDone` closure plumbing that only a real `incus.InstanceExecArgs` exercises, and a
handful of `if err != nil` returns inside `copy_linux.go` that are pure pass-throughs of an already
tested failure mode.

Gate: `gofmt -l` clean, `go build ./...`, `go vet`, `go test -race`, and `go test ./...` all green;
package also vets clean for GOOS=darwin/windows (the new untagged `incushost_test.go`).

## 2026-07-27 — barehost unit coverage 39.5% -> 68.4%, and the `--tail 0` defect it exposed

Completes the S1 follow-up. Sibling entries cover the triage, the S1 decision, the CI legs, and the
incushost half; this one records the barehost half plus a real defect the work surfaced.

### Coverage

39.5% -> 68.4% of statements; zero-coverage functions 97 -> 12. That lands above dockerhost (63.0%)
and level with containerdhost (68.5%). Final state across all five backends:

| backend | before | after |
|---|---|---|
| incushost | 55.0% | **94.5%** |
| barehost | 39.5% | **68.4%** |
| dockerhost | 63.0% | 63.0% |
| containerdhost | 68.5% | 68.5% |
| kubernetes | 78.3% | 78.3% |

**No production code was changed for barehost.** The package already had the seams the tests needed
(`containerRuntime`, `newBackend`, `newImageStore`) — worth recording, because it means the low
number was a testing gap, not an untestable design. 115 new tests across 12 new files; the only
pre-existing file touched was `fake_linux_test.go`.

The highest-value additions drive REAL child processes, because faking them would proves nothing:
`shimStop`'s three outcomes (no shim -> not handled; accepted-and-exited -> handled; wedged ->
signalled dead but still not handled), `shimAlive` requiring BOTH a live pid and a responsive
socket, and the shim observing its child's true exit status (7 for a normal exit, 128+SIGKILL for a
signalled init, "exit detected, code unknown" for an adopted one). Reading the real exit code is the
entire reason the shim exists and none of it had a test.

Deliberately left uncovered, and why: `runcRuntime.{Create,Start,State,Kill,Delete,List,Exec,Stats}`
are one-line pass-throughs to go-runc needing the binary AND root (the `containerRuntime` seam
beneath them carries the behavior; E2E covers the rest); `imageStore.pull`/`resolver` are registry
network I/O; `createInstance`'s first real action is a `mount(2)`; `specClient.SnapshotService` is a
one-line accessor no spec opt cornus applies ever invokes.

### Defect found and fixed: `Attach` with `Logs:false` replayed the whole log

`seekToLastLines` (`logs_linux.go`) documented and implemented `n <= 0` as "keep the whole file".
But `Attach` (`exec_linux.go:392`) sets `opts.Tail = "0"` specifically to mean "no history" when
`cfg.Logs` is false. So attaching without logs dumped the ENTIRE backlog before streaming live
output, and a `Logs` call with `Tail:"0"` printed everything instead of nothing — the inverse of
docker's `--tail 0`.

The peer backend settles the intent rather than leaving it to interpretation:
`containerdhost/logs_linux.go:157-164` uses `tail = -1` as its "all" sentinel and rejects only
`n < 0` as invalid, so `0` there is a genuine tail of zero. barehost was conflating the two.

Fix: `n < 0` still means whole file; `n == 0` now seeks to EOF. The negative case was left as "whole
file" rather than also being rejected the way containerdhost rejects it — that would change behavior
for `Tail:"-1"` beyond the defect at hand, and is a separate call.

Regression tests (`logs_tail_linux_test.go`): the zero case; a table over the neighbouring
vocabulary (negative, 1, 2, at-line-count, beyond-line-count, empty file, no trailing newline) so
the zero case cannot be "fixed" by breaking a neighbour; and — the one that matters most — a test
proving that after a zero tail the offset sits at EOF so content appended AFTERWARDS is still
delivered. A fix that suppressed history by refusing to read at all would have silently broken live
attach, and nothing else in the suite would have caught it.

Process note worth keeping: the agent that found this wrote the test, saw it fail, and then DELETED
it rather than commit a test asserting the buggy behavior, reporting the defect instead. That is the
right call — a passing test pinned to a bug is worse than no test, because it converts a defect into
a specification.

### Session findings (whole sweep)

1. **A backlog assembled from audit reports decays silently.** Of 45 re-verified claims, ~11 were
   already fixed (including two marked BLOCKING), 1 was duplicated, and 6 had wrong scope or
   numbers — 3 of those *understating* the problem. Verify before dispatching: roughly 1 item in 8
   would have produced a confident no-op.
2. **Review findings age faster than the code they describe.** The fresh-eyes review's central
   "breadth costs depth" evidence was half stale within two days (kubernetes DOES surface
   crash-loop/image-pull/unschedulable diagnostics via `instanceDiagnostic`). A strategic
   recommendation resting on a stale observation should be re-grounded before it is acted on — S1
   would have deleted two backends on it.
3. **Low coverage was a testing gap, not an untestable design** — in barehost's case zero production
   changes were needed, and in incushost's the unlock was simply that the vendor client exposes
   interfaces (`incus.InstanceServer`, `incus.Operation`) that a test struct can embed and
   selectively override, leaving un-overridden methods as loud nil panics.
4. **Green-skips are the dangerous failure mode for opt-in E2E.** Two real holes were found where a
   dedicated leg would have reported success having deployed nothing; both were only provable by
   fault injection with measured exit codes, not by reading the code.
5. **Scenarios silently encode product limitations.** `mcp-stdio-tools.star` had never run on incus
   despite being in `SCENARIOS_INCUS`; making it pass required guarding an assertion behind the
   incus console-log limitation. That guard is now cross-referenced from the "Faithful incus `Logs`"
   TODO so it gets removed when the limitation goes, instead of outliving it.

### Gate

`gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` green, `-race` clean on both
backends, `make e2e-check` passes.

## 2026-07-27 — Group E/F documentation and localization sweep

The Group E English corrections and Group F locale synchronization are complete. The request to
execute Group F resolves S5 in favor of maintaining the Japanese and Simplified Chinese trees; the
freeze proposal is rejected. English remains the source of truth for both locales.

English source corrections:

- completed the security action/read-exception, environment-variable, deploy-spec, SSH-tunnel, and
  public command/API inventories;
- fixed the quick-start release references and documented checksum/Sigstore verification plus the
  release-binary size;
- disclosed pre-1.0 maturity and the Compose merge, Docker volume deletion, and Dev Container
  compatibility boundaries;
- added the combined documentation gate and a tested duplicate-list-target checker.

Localization:

- normalized 2,022 prohibited full-width punctuation characters across 57 Chinese pages while
  preserving four literal colons inside fenced shell comments;
- restored all missing reference, architecture, CLI, ingress-certificate, workload-lineage,
  Origin/GitOrigin, caretaker, remote-companion, and agent-forwarding material in both locales;
- cleared the measured baseline of 9 structural errors across 7 files in each locale;
- source-reviewed all 64 pages per locale and fixed confirmed omissions, stale contracts, wrong
  links, damaged Markdown/admonitions, mixed-language reader comments, and terminology drift.

The final editorial pass demonstrated that a structure-clean translation can still be materially
incomplete: the audit warnings exposed missing SSH/config flags, networking behavior, observability
transport, server environment tables, deploy-spec field semantics, and reader-facing comments.
Warnings were treated as a review queue rather than mechanically silenced; the surviving warnings
are localized fragment spellings or valid sentence-level reordering.

The documentation gate now checks authored punctuation before building, then validates routes,
fragments, and duplicate list targets. The punctuation checker ignores fenced/inline code and URL
destinations and has focused regression tests beside the existing anchor tests.

Gate: both full translation structural audits pass (64 pages each), the punctuation checker reports
