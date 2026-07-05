# cornus Development Journal

This file retains only unconsolidated entries and the canonical long-term-memory audit.

---

## LTM Consolidation Record

Audited entry by entry against `.agents/docs/LTM/` and `.agents/docs/TODO.md` on
2026-07-31. Every substantive journal entry has durable coverage below, so all
consolidated narrative entries and superseded consolidation records were removed.

### Journal sections -> durable memory

| Journal section group | Durable destination |
|-----------------------|---------------------|
| Activity flight recorder, server/caretaker/child lifecycle, unfinished work, follow/SSE/MCP reads, and crash recovery | `activity-flight-recorder.md` |
| All-in-one release artifacts, static-linking constraints, embedded observability tools, feature reporting, and release-lineup decisions | `release-and-packaging.md`, `built-in-observability.md` |
| Animated logo and embedded web presentation | `web-ui.md` |
| Authentication and authorization: fail-closed scopes, dataplane credentials, SSH identity, peer trust, third-party scope maps, token exchange, and 401 refresh | `auth-and-security.md`, `hub-network-overlay.md` |
| Barehost behavior: field coverage, tail semantics, record locking, shim policy, systemd operation, and reboot limits | `barehost-backend.md`, `hostrun-shared-runtime.md` |
| Build delegation, raw relay, capability probing, managed privileged builder, registry propagation, diagnostics, and documentation | `builder-delegation.md`, `maintenance-and-decisions.md` |
| Built-in workload observability: IMBH storage, resource metrics, automatic logs, PromQL, Grafana APIs, multi-replica behavior, and zero-store gateway mode | `built-in-observability.md`, `workload-telemetry.md` |
| CI, E2E, and release verification: live backend legs, fixture races, preflight, safe reaping, nested modules, action pins, license gates, and failure triage | `ci-github-actions.md`, `e2e-harness-and-coverage.md`, `e2e-kubernetes-target-caveats.md` |
| Client daemon and conduit reconciliation: same-name projects, identity/configuration split, refcounts, and canonical keys | `client-daemon-and-conduit.md` |
| Client-local mounts, deploy-attach readiness, remote host routing, and stateless attachment boundaries | `client-local-mounts-deploy.md`, `remote-companion-and-agent-forwarding.md`, `deploy-backend-contract.md` |
| Compose orchestration: multi-file merge, `!reset`, `!override`, dependency expansion, health gating, recreation, watch identity, providers, and progress behavior | `compose-cli.md`, `compose-provider-services.md` |
| Containerd behavior: CNI contamination, hostname agreement, warning coverage, log cleanup, self-container safeguards, and mount routing | `containerd-backend.md`, `client-local-mounts-deploy.md` |
| Deep-sleep synthesis, canonical-memory navigation, documentation boundaries, and maintenance decisions | `INDEX.md`, `maintenance-and-decisions.md` |
| Dependency licensing: shipped-tag scans, notices, local replacements, vendored yamux, Rust attribution, and published-module exceptions | `dependency-license-compliance.md` |
| DeploySpec contract audits: per-field and sub-field coverage, warning semantics, optional capabilities, canonical predicates, and volume-policy follow-up | `deploy-backend-contract.md`, `.agents/docs/TODO.md` |
| Dev Container support: enforced schema subset, `runArgs`, build options, and backend-observed E2E | `dev-containers.md` |
| Docker API proxy: volume lifecycle, wait codes, restart/TTY mapping, foreground attach, banner constraints, and Docker 28/29 compatibility | `dockerd-proxy.md`, `.agents/docs/TODO.md` |
| Documentation and localization: canonical user guides, setup layout, translation repair, audit-noise reduction, page freshness, anchor normalization, and script behavior | `user-reference-docs-site.md`, `setup-wizard.md`, `verification-and-audit-methods.md` |
| Host-native registry, local/remote image flow, object storage, runtime-native stores, disk usage, and quota decisions | `host-native-registry.md`, `registry-local-image-flow.md`, `registry-and-storage.md` |
| Hub and multi-replica coordination: credential sessions, distributed writes, peer authentication, route catalog, and scenario automation | `hub-network-overlay.md`, `auth-and-security.md`, `e2e-harness-and-coverage.md` |
| In-container server topology: host-path translation, preflight, dockerhost/containerd realization, setup flow, and operational constraints | `in-container-server-mode.md`, `setup-wizard.md` |
| Incus backend: OCI mapping, lifecycle, data plane, remote companion, logs, volumes, credentials, field warnings, E2E, and UDP limits | `incus-backend.md`, `remote-companion-and-agent-forwarding.md` |
| Ingress routing: native/emulated realization, controller-backed fetch, longest-path behavior, readiness races, bridge bounds, certificates, and tunnels | `kubernetes-ingress.md`, `ingress-tunnels.md`, `kubernetes-backend.md` |
| Knative descriptor, lifecycle/listing behavior, and least-privilege speculative probes | `knative-serving.md`, `kubernetes-backend.md` |
| Kubernetes backend: object lifecycle races, Job/Knative listing, field coverage, nested networks, scale warnings, and real-controller testing | `kubernetes-backend.md`, `knative-serving.md`, `e2e-harness-and-coverage.md` |
| MCP and web BFF: shared operation core, Streamable HTTP, stdio process contract, tools/resources, and launched-client E2E | `web-bff-and-mcp.md` |
| Product and engineering reviews: scope pressure, backend strategy, security/versioning choices, install findings, diagnosability, and TODO-triage policy | `maintenance-and-decisions.md`, `.agents/docs/TODO.md` |
| Project-scoped connection overrides and endpoint/credential trust boundaries | `project-context-overrides.md` |
| Remote 9P storage: tagged transport, block protocol, DiskStore allocation, coherence, demand fill, prefetch, writable exports, and mmap limits | `remote-9p-block-cache.md`, `client-local-mounts-deploy.md` |
| Remote connectivity: SSH profiles, port forwards, SOCKS5 conduit, public tunnels, direct-to-pod fallback, and cluster registry reachability | `client-connectivity-synthesis.md`, `remote-cluster-connection-ergonomics.md` |
| Remote companion and agent forwarding: host-backend mounts, egress, ports, SSH agent relay, supervision, privilege union, and leak recovery | `remote-companion-and-agent-forwarding.md` |
| Setup wizard: server gate, backend picker, scenarios, artifacts, runbooks, absolute documentation URLs, systemd guidance, and correction arcs | `setup-wizard.md` |
| TODO sweeps and external attestation: re-grounding, sync invariants, mutation-test evaluation, false-positive control, differential probes, and evidence preservation | `verification-and-audit-methods.md`, `.agents/docs/TODO.md` |
| Web UI workspaces: overview grouping, file explorer, terminal semantics, tiled panes/stacks/editors, mock parity, and filesystem confinement | `web-ui.md` |
| Workload lineage: structured origin, backend persistence, project joins, non-project attribution, and mount-reporting boundaries | `workload-lineage.md` |
| Yamux QoS fork, A/B methodology, netem bounds, batching, frame-size invariants, and CI | `yamux-qos-performance.md`, `ci-github-actions.md` |

### Synthesis documents -> source LTM documents

| Synthesis document | Consolidates |
|--------------------|--------------|
| `build-engine-synthesis.md` | `remote-build-9p-transport.md`, `builder-delegation.md`, `build-cache.md`, `lazy-bind-mounts.md`, and the build-worker facet of `containerd-backend.md` |
| `caretaker-transport-and-hub-synthesis.md` | `client-local-mounts-deploy.md`, `hub-network-overlay.md`, `client-side-egress.md`, and caretaker sections of `user-networks-and-caretaker.md` |
| `client-connectivity-synthesis.md` | `client-daemon-and-conduit.md`, `client-side-egress.md`, `port-forwarding.md`, `public-tunnels.md`, and `remote-cluster-connection-ergonomics.md` |
| `deploy-backends-synthesis.md` | `deploy-backend-contract.md`, `containerd-backend.md`, `barehost-backend.md`, `hostrun-shared-runtime.md`, `kubernetes-backend.md`, `incus-backend.md`, and `in-container-server-mode.md` |
| `docker-compat-clients-synthesis.md` | `compose-cli.md`, `compose-provider-services.md`, `dockerd-proxy.md`, and `dev-containers.md` |
| `ingress-routing-synthesis.md` | `kubernetes-ingress.md` and `ingress-tunnels.md` |
| `kubernetes-deploy-synthesis.md` | `kubernetes-backend.md`, `user-networks-and-caretaker.md`, `client-side-egress.md`, and Kubernetes facets of `client-local-mounts-deploy.md` |
| `observability-synthesis.md` | `observability-and-logging.md`, `workload-telemetry.md`, `built-in-observability.md`, and `activity-flight-recorder.md` |
| `registry-and-storage-synthesis.md` | `registry-and-storage.md`, `registry-local-image-flow.md`, and `host-native-registry.md` |
| `remote-companion-and-agent-forwarding.md` | the mount-relay companion baseline in `client-local-mounts-deploy.md` and the distinct agent-forwarding mechanism in `public-tunnels.md` |
| `shipping-and-install-synthesis.md` | `release-and-packaging.md`, `local-k8s-quickstart.md`, `setup-wizard.md`, and `in-container-server-mode.md` |
| `testing-ci-and-quality-synthesis.md` | `ci-github-actions.md`, `e2e-harness-and-coverage.md`, and `codebase-audit-2026-07.md` |
| `web-and-agent-surfaces-synthesis.md` | `web-ui.md`, `web-bff-and-mcp.md`, and `workload-lineage.md` |

### Intentionally standalone LTM documents

| Document | Reason it remains standalone |
|----------|------------------------------|
| `auth-and-security.md` | Cross-cutting security policy and protocol details span every subsystem. |
| `control-plane-api-namespace.md` | The API namespace migration is a focused compatibility record. |
| `dependency-license-compliance.md` | License policy and evidence have their own release-compliance lifecycle. |
| `e2e-kubernetes-target-caveats.md` | Environment-specific Kubernetes runner caveats remain a focused reference. |
| `july-2026-client-and-web.md` | This retained historical integration summary predates the narrower topic documents. |
| `knative-serving.md` | Knative-specific contracts and limitations are independently useful. |
| `maintenance-and-decisions.md` | Maintainer decisions and rejected alternatives cut across topic syntheses. |
| `project-context-overrides.md` | Project-level connection override semantics form a small independent contract. |
| `remote-9p-block-cache.md` | The block-cache protocol and measurement record are specialized implementation knowledge. |
| `user-reference-docs-site.md` | Documentation architecture, localization, and quality rules have a separate maintenance lifecycle. |
| `verification-and-audit-methods.md` | Reusable evidence, attestation, and false-positive controls apply across the repository. |
| `yamux-qos-performance.md` | The vendored transport fork and its measurements require a focused performance record. |

Open follow-up work remains in `.agents/docs/TODO.md`. See
`.agents/docs/LTM/INDEX.md` for the complete source-document index and current
synthesis navigation.

## Back to the original backlog: an item whose premise was wrong, and the defect under it (2026-07-31)

Picked up "Extend host/container path translation to `barehost` and `incushost`" from the main backlog.
The entry asserts both backends "share `hostrun`'s exposure (volume backings, managed `/etc/hosts`,
netns paths)". Checking that before porting anything:

- incushost imports `hostrun` for `StreamStats` and nothing else — a stats formatter, not a path
  builder.
- `b.dataDir` is never used to construct any path. `incushost.go` says so itself: the field is carried
  "for parity with the other host backends and any future per-instance bookkeeping".
- What incushost actually hands incusd is a USER's bind source, a storage-pool volume NAME, or tmpfs.

A user's bind source is a host path by definition — the daemon opens it — so translating it would BE
the bug, which is exactly what containerdhost's `hostVisibleMountSources` documents. There is no
cornus-built path in incushost's hand-off, so there is nothing to translate. The entry's premise is
false for incus, and bare's exemption is already documented and correct (it execs the OCI runtime as
its own child, so a containerized bare server runs workloads inside that same container).

That is the third time in this session an inherited TODO entry stated something the tree contradicts.
The pattern from the earlier count holds: an entry written at discovery time encodes a guess about the
fix, and re-verification catches the wrong SHAPE of fix, not just stale priority. Here the guessed
shape was "port a Mapper into two backends" and the true shape was "one of them has nothing to map".

### The defect the measurement turned up

If incushost never hands incusd a path under the data dir, what was `hostcheck` checking for incus?

`Run()` ran `dataDirCheck` for every translating backend not sharing a mount namespace. For incus that
produced:

    detail: the incus runtime cannot see cornus's data dir /var/lib/cornus: client-local mounts will be rejected
    hint:   bind-mount it from the host (-v <host-path>:/var/lib/cornus) or declare the mapping with CORNUS_HOST_PATH_MAP=...

Both halves mislead. incus is not a `MountingBackend`; client-local mounts are refused on it whatever
the data dir does. An operator who follows the hint bind-mounts the directory, re-runs preflight,
watches the warning disappear — and has changed nothing at all. **Advice that cannot help is worse than
silence: it spends the reader's time and their trust in the next warning.**

This is the same family as two defects fixed earlier today — the message naming `CORNUS_CONTAINERD_REMOTE`
that the factory might not read, and the warning about a client-emulated ingress that was never dropped.
Three instances now of one thing: a diagnostic that is confidently wrong about a backend it was not
written for. The common cause is a check or message written with one backend in mind and then reached
by all of them.

Gated on `handsDataDirToRuntime` (dockerhost, containerd). bare and kubernetes were already excluded
upstream by `sharesMountNamespace` — worth noting they are excluded for a DIFFERENT reason: they do get
cornus's paths, but those paths already mean the same thing on both sides. Two exclusions, two
rationales; collapsing them into one predicate would have been the tidier-looking mistake.

### Three tests, three different failures

Deliberately not one test:

1. Which backends draw the check at all — fails if incus is re-added to the gate.
2. That NO check tells an incus operator a bind mount would enable client-local mounts — fails if some
   OTHER check is re-worded into the same false promise, which (1) would not catch.
3. The positive control: containerd must still produce StatusFail. A too-wide narrowing would silently
   disarm the one configuration `cornus serve` must refuse to start on, and that regression looks
   exactly like the fix.

Neutralized by removing the gate: both incus tests fail and quote the misleading advice back verbatim,
which is the diagnostic I would want if I hit this in six months.

## The consolidation carried a corrected falsehood into durable memory (2026-07-31)

Mid-session another agent ran the `reconcile-journal-ltm` consolidation: JOURNAL.md went from 10,726
lines to 154 — the LTM Consolidation Record plus unconsolidated entries. That is the sanctioned
workflow (CLAUDE.md names that skill as the one exception allowed to remove consolidated entries), and
my newest entry survived because it was appended after the rewrite. Not data loss.

I checked anyway, rather than assuming, by grepping `.agents/docs/LTM/` for this session's findings:
`CORNUS_CONTAINERD_REMOTE`, `SupportsUDPPortForward`, `UsesHostMountFastPath`, `parserOptions`,
`LogShimArg` all landed; the 401 refresh landed in `auth-and-security.md` under different wording
("a cached exchanged token rejected with 401 ... is discarded and refreshed at most once, only for
rewindable requests"). Two symbols had no home — and following one of them found the real problem.

### What was wrong

`client-local-mounts-deploy.md` described the `RemoteCapable` gate as preventing the server from
"silently stealing every client-local-mount deploy away from the existing, working
`applyWithHostMounts` fast path", for **dockerhost/containerdhost**.

That is precisely the false premise I spent this morning correcting in four code comments, a test
docstring, and two reference pages across three locales. containerd has no `applyWithHostMounts` fast
path; `hostcheck.UsesHostMountFastPath` is dockerhost and bare only. Re-verified against the code
before touching anything.

`in-container-server-mode.md` carried the same shape in two more places: "`barehost` and `incushost`
share C2/C3/C4/C6 by construction (same `hostrun` package)", and a follow-up note saying the `hostrun`
mapper "covers most of their exposure". Measured today, none of the four rows apply to incus — incusd
owns instance networking (C2), storage is pool volume NAMES not host dirs (C3), incusd generates and
bind-mounts `/etc/hosts` itself (C4, which is exactly why this backend warns that `hostname` and
`extraHosts` are ignored), and there is no CNI at all (C6). incushost imports `hostrun` for
`StreamStats` and nothing else.

### Why this matters more than a stale journal entry

The journal is a log; LTM is what future agents READ. A correction that lives only in a journal entry
is lost the moment that entry is consolidated — and consolidation summarizes what the entries SAID,
which for a long-standing wrong belief is the wrong version stated many times and the right version
stated once, at the end.

So the failure mode is specific and worth naming: **consolidation is majority-rule over a corpus where
the correction is always in the minority.** Nine entries described the containerd fast path as real
because that is what the code comments said for months; one entry corrected it. The consolidation kept
the nine.

Both files corrected, with the measurement and the date, and the retired TODO item noted as
premise-wrong rather than done — so a future reader does not re-derive the port that turned out to have
nothing to port.

### The check worth repeating

After any consolidation, grep LTM for the symbols your session touched and read the surrounding
sentence — not to confirm the symbol is present, but to confirm what LTM now SAYS about it. Presence is
not correctness: `CORNUS_CONTAINERD_REMOTE` appeared in five LTM files, and one of them described it
wrongly.

## LTM defect sweep: 17 files corrected (2026-07-31)

Asked to find and fix defects in the LTM documents, after the earlier discovery that the consolidation
had preserved a falsehood I had spent the morning correcting. Method: mechanical checks that can only
produce true positives, then read what each hit actually claims.

### What the checks found

**Every file path LTM names must exist.** 236 distinct paths referenced; 4 pointed at files that had
moved and 1 at a file deleted outright:

| LTM said | reality |
|---|---|
| `pkg/build/buildwire/writablefs.go` | moved to `pkg/wire/` |
| `cmd/cornus/netredirect_linux.go` (+ `_other.go`) | build-tag split collapsed into `netredirect.go` |
| `pkg/registry/containerd_source_linux.go` | renamed `containerd_store_linux.go` |
| `cmd/cornus/internal/composecli/main.go` | never existed; `resolveProject` is in `compose.go` |
| `cmd/cornus/mountagent.go` | DELETED with the deprecated aliases |

**The `internal/` → `pkg/` move was never propagated.** 57 distinct paths across 10 files, 93
occurrences, all still spelled `internal/...`. Rewritten only where the target provably exists — the
script asserts every destination before touching a file, and refuses the whole run otherwise. Four
resisted the naive mapping (`internal/buildwire/{confinedfs,export,writablefs}.go` went to `pkg/wire/`,
not `pkg/build/buildwire/`) and were resolved individually.

**Every `TestX` LTM cites must exist.** 197 cited; 7 did not. Corrected to their live successors
(`TestGroupMounts` → `TestGroupByServer`, `TestMountRelayUnknownSession` → `TestMountForwardUnknownSession`,
`TestDaemonStateRoundTrip`/`TestSupervisorDispatch` → `agentproc.TestStateRoundTrip` /
`composecli.TestLogsSourceDispatch`, and so on), each keeping the old name in a "formerly" clause so a
reader chasing an old reference lands somewhere. Two were already correctly marked as historical
(`TestDeleteRemovesPVC` names its successor), which is the standard the rest now meet.

### Two that were not path staleness

The mechanical checks were the way IN; the defects worth finding were the claims attached to the stale
names.

**`port-forwarding.md` said UDP works on "dockerhost and containerd", twice.** As of this session it
works on every host backend — dockerhost, containerd, bare, and incus. The incus half is the fix I made
today (it implemented udp and never declared the capability). LTM would have taught the next reader the
pre-fix world, including that the test proving it is `TestForwardPortRejectsUDP` — a name that no longer
exists, for a behaviour dockerhost no longer has: it now has `TestForwardPortUDPEchoes`.

**`client-local-mounts-deploy.md` described a pod spec cornus does not emit.** It said the sidecar sets
`Args: ["mount-agent", ...]` and that the app container is gated by a `cornus mountcheck` startupProbe.
Both were renamed by the caretaker unification — the real values are `Args: ["caretaker"]` and
`cornus caretaker-check`.

That one was worth checking against the code rather than just fixing the prose, because if the pod spec
HAD still said `mount-agent` after the alias was removed, every kubernetes mount deploy would be broken.
It does not: `pkg/deploy/kubernetes/kubernetes.go` emits `caretaker` and `caretaker-check`. Doc-only
defect — but the difference between "stale doc" and "live outage" was one grep, and the doc gave no hint
which it was.

### What this says about durable memory

The 17 files divide cleanly:

- **Renames** (paths, test names) — mechanically findable, and nobody notices them because a reader who
  fails to find `internal/config/config.go` just searches again and moves on. Cheap to fix, low harm.
- **Claims that were true when written** (UDP on two backends; the mount-agent sidecar; incushost
  sharing `hostrun`'s exposure) — NOT mechanically findable, and actively harmful, because a reader has
  no reason to doubt them. These are the ones that teach the next agent something false.

The second kind is only reachable by checking a claim against the code, which does not scale to 67
files. What does scale is the entry point: a stale NAME is a reliable smell for a stale CLAIM around it.
Every substantive defect in this sweep was found by following a path or test name that did not resolve,
and then reading the sentence it sat in. The mechanical check is not the audit; it is the sampler that
tells you where to read.

## Session summary (2026-07-31)

Written to stand alone: the `reconcile-journal-ltm` consolidation ran mid-session and removed the
earlier per-round entries and an interim summary, so a reader of this file sees only the three entries
above plus this. The detail lives in `.agents/docs/LTM/` and in the closed TODO entries; what follows
is the arc and the findings.

### Ledger

| | |
|---|---|
| attestation items closed | 20 of 48 (all 5 incorrect closures; 15 of 24 not-attestable; 0 of 20 live-CI, which need daemons and a cluster) |
| main-backlog items closed | 1 (premise-wrong; the defect under it fixed) |
| new test files | 14 (12 Go, 2 Python) |
| production files changed | 11 |
| LTM files corrected | 17 |
| neutralizations run | 26 |
| working tree | 77 entries, 17 new, **nothing committed** |

Gate at close: `gofmt` clean, `go build ./...`, `go vet ./...`, `go test ./...` exit 0 across 94
packages, `make test-scripts` exit 0, translation freshness 66 pages x 2 locales current, both
structural translation audits passing.

### Three findings worth carrying

**1. I neutralized the way the test was shaped, not the way the code would break.**

Three independent agents re-audited all 177 checked TODO entries and found 5 incorrectly closed. Three
were mine, and I had run neutralizations on all three and written up the results. The clearest:
`TestEveryUDPForwardingBackendDeclaresIt` parses source for a method DECLARATION, and I neutralized it
by DELETING the method — the mutation such a test is built to catch. What a regression actually takes
is `return true` -> `return false`, one word, which I never tried. AGENTS.md says a compile error is
not a valid neutralization; this is its sibling and subtler, because mine compiled AND failed.

"What would a PASS look like if this claim were false?" has more than one answer. I kept answering the
easy one. The honest version is to enumerate how the behaviour could regress and check each, rather
than picking the mutation the test was designed around.

**2. An assertion that something is small, absent, or quiet is satisfied by the thing not running.**

Four instances, one week apart in kind but one day apart in fact:

- negative warning tests (no warning for a disabled healthcheck) pass if the branch is deleted
- the UDP capability guard passes if the capability is declared `false`
- the allocation ceiling passes if the writes do nothing
- `make test-scripts` would pass if it swallowed the failures it ran

The third caught me live. My first DiskStore allocation test used `callerHash: 0`, which never matches
`hashChunk(result)`, so write #1 dropped the chunk and #2-#200 took an early return. I measured 199
no-ops, reported a plausible figure, and it was green with the pool removed. It compiled, ran, called
the right API with plausible arguments, and proved nothing. Only running the mutation exposed it. The
durable fix is not the corrected hashes — it is the post-loop assertion that the work happened.

**3. Stale durable memory is two different problems, and only one is findable by machine.**

Renames (paths, test names) are mechanically findable and nearly harmless: a reader who cannot find
`internal/config/config.go` searches again. Claims that were TRUE WHEN WRITTEN are neither findable nor
harmless — LTM said UDP port-forward works on "dockerhost and containerd" (now every host backend), and
described a pod spec cornus does not emit (`Args: ["mount-agent"]`, a `cornus mountcheck` probe; both
renamed by the caretaker unification).

What scales is the entry point: **a stale name is a reliable smell for a stale claim around it.** Every
substantive defect in the LTM sweep was found by following a path or test name that did not resolve and
then reading the sentence it sat in.

Worse, consolidation actively preserves the wrong version. `client-local-mounts-deploy.md` still
described containerd's "existing, working `applyWithHostMounts` fast path" — the exact falsehood I had
spent that morning correcting in four code comments, a test docstring, and two reference pages across
three locales. **Consolidation is majority-rule over a corpus where the correction is always in the
minority**: nine entries stated the wrong thing because the code comments said so for months, one
corrected it, and the nine won. After any consolidation, grep LTM for the symbols you touched and read
the surrounding SENTENCE — presence is not correctness.

### Smaller things learned

- **"Add a test" usually meant "make the property observable."** Five items were untestable code
  shapes: `main()`'s inline kong options (a test could only assert against a lookalike),
  `containerdhost.LogShimArg` stuck behind a build tag (so the guard carried its own copy of the value
  it guarded, and its docstring DEFENDED that), ingressemu's proxy trapped in a closure, `writeTimeout`
  as a const, and 32 Python tests that nothing executed.
- **Refuses vs. says nothing.** The kube hub's outage tests use a fake clientset that errors
  immediately — a server saying NO, which needs no deadline. The failure a deadline exists for is one
  that accepts and never answers, and client-go's fake reactors never receive a context, so with the
  fake the deadline is untestable in either direction.
- **If a test's failure mode under its own mutation is "hang", the test is not finished.**
- **The helper being correct is not the same as anyone calling it.** Extracting
  `ingressroute.BridgeTransport` fixed the instance and left the class open.
- **Using a tool beats reading its spec.** `translation_state.py update --path ja/cli/foo.md` — the
  file actually edited, the obvious thing to type — matched nothing, printed "recorded 0", and exited
  0. I hit it myself and noticed only because I re-ran `check` out of habit. Now an error.
- **Verify a restore by CONTENT.** A backup loop keyed on `$(basename)` collided on `spec_linux.go`,
  which exists in every backend package; I restored containerdhost's source into barehost and caught it
  only because the package clause printed wrong. `git checkout` is off-limits here (another agent works
  this tree), so scratch backups are the only undo.

### Where I disagreed with the audit

Two adjacent items read almost identically. One was already attested — deleting the barehost ingress
branch does leave `warn_once_linux_test.go` green (it only counts DUPLICATES), but `spec_linux_test.go`
has a per-field Ingress row and fails. I measured it, including removing the now-unused import so the
mutation compiled, rather than accepting or dismissing the report. The claim was about one file; the
conclusion drawn from it was about the tree. The adjacent item was entirely real.

Likewise a main-backlog item asserted barehost and incushost "share `hostrun`'s exposure". incushost
imports `hostrun` for `StreamStats` only and builds no path from its data dir — so there was nothing to
port, and measuring that found the defect underneath: `hostcheck` was telling containerized incus
operators to bind-mount the data dir to enable client-local mounts incus cannot do at all.

### Open

Mine, unblocked: 8 attestation items (private-source credential test, the daemon banner print path —
needs a seam through `clientagent.EnsureRunning`, which spawns a process — two E2E-dependent, and four
"preserve reproducible evidence" records that are archival rather than code).

Blocked on infrastructure: 20 live CI/E2E reruns.

**Needs a maintainer decision:** `compose up` idempotence; whether to trim secret env vars; the
ulimit-bounds direction; `spec.Volumes` policy visibility; and whether a local containerd should realize
client-local mounts without `CORNUS_CONTAINERD_REMOTE` — that one needs a live containerd and changes
behaviour a user could depend on either way, so it is filed with both readings and an explicit
do-not-act-without-direction.

## Three more attestation items, and what a "seam" is actually for (2026-07-31)

### The private-source credential test

The audit said restoring `return authn.Anonymous, nil` left `TestBearerForRegistry` green. Confirmed —
and the existing test's own docstring explains why: the non-destination assertions "deliberately avoid
requiring a specific authenticator: what DefaultKeychain yields depends on the developer's Docker
config". On a machine with no Docker login, delegation and the bug give the same answer.

Fixing it meant installing a credential that makes anonymous the WRONG answer. Determinism needed more
than the obvious knob: go-containerregistry's `defaultKeychain` decides whether a Docker config exists
by checking `$HOME/.docker/config.json` FIRST and only then `$DOCKER_CONFIG`, while the subsequent
`config.Load` reads `$DOCKER_CONFIG` regardless. Setting only DOCKER_CONFIG therefore behaves
differently on a developer machine with a real `~/.docker/config.json` than on clean CI — the test
would have been deterministic in the way that matters least and non-deterministic where it counted.
HOME points at an empty temp dir; REGISTRY_AUTH_FILE and XDG_RUNTIME_DIR are cleared because the same
function falls back to Podman's auth.json through them.

Two companions stop the fix reading as "always return a credential": the destination must STILL prefer
the cornus bearer even when a Docker credential exists for that host (a resolver that delegated
everything would satisfy the delegation test and break every push), and with no config at all an
unrelated host must resolve anonymously rather than erroring. The old test's docstring now points here
instead of quietly conceding the gap.

### S5's "published and current"

Not a code fix — a claim to re-ground. `docs:check-translation-freshness` now reports 66 pages x 2
locales all current, and the six that were stale went stale because THIS session edited English and its
translations in one pass and never recorded the digests. Content was never behind.

The part worth recording is that the claim is enforced rather than promised: `.github/workflows/docs.yml`
runs `npm run docs:check`, which chains the freshness check, on any push to `main` touching `docs/**`,
and `QUALITY_GATE.md` section 6 documents the step. Residual, eyes open: the gate fires AFTER the push,
so a local session can leave the trees stale until CI says so — exactly how these six arose. The habit
that closes it is running the freshness check at the end of any session that touched `docs/`, the way
the Go gate is run for code.

### The daemon-docker banner, and what a seam is for

This was the last item I had deferred as "needs a seam", and finishing it clarified something worth
stating plainly.

The tempting cheap fix was to extract the print loop into a helper and test the helper. That would have
been the fourth instance today of a guard I have already learned not to write: it proves the helper is
correct and says nothing about whether Run still calls it — the same gap as
`ingressroute.BridgeTransport`, whose own test could not see a caller reverting to an inline transport.

So the seam went where the untestability was: `agentEnsureRunning` / `agentSend` as package vars over
the real `clientagent` functions. Run is now driven end to end against a fake agent, and the assertion
is on what the user SEES. `Resolve` turned out to need no network for an explicit `--host`, which I
established with a throwaway probe before committing to the approach rather than after discovering it
the hard way.

Three assertions beyond "the banner appears", each for a different way the test could pass while the
feature is broken: a positive control that the request reached the agent at all (`Action ==
"docker-serve"`), so an early return cannot pass vacuously; that the DOCKER_HOST guidance survives; and
that the conduit banner precedes it, because a reader who stops at the export line never sees the proxy
address. Plus a refusal test — `OK=false` must fail with the agent's reason and print no usage guidance
for a frontend that was never hosted.

**The generalization**: a seam is not a testing convenience, it is the admission that a behaviour has no
observer. Five items in this backlog read as "add a test" and were really "this cannot be observed" —
`parserOptions()`, `containerdhost.LogShimArg`, `newBridgeProxy`, `writeTimeout` as a var, and now
these two. In every case the cheaper-looking fix (assert around the edge, or test a freshly extracted
helper) would have produced a green test that a real regression walks straight past.

## An echo server cannot detect a symmetric corruption (2026-08-01)

Closed the "exercise UDP over the remote companion paths" item, which was filed as E2E-dependent and
was not: the existing companion tests already build a real yamux pair over loopback and play the
caretaker's `PortForwardRole` by hand, so the udp variant needed only a real UDP socket and the framed
codec. Worth noting on its own — an item's stated blocker is a guess made at filing time, and this is
the fourth this session that dissolved on contact.

### The defect it guards

On a COMPANION relay both ends are already framed: the caller's tunnel carries wire datagrams, and the
caretaker reads wire datagrams before writing to the real socket. So the relay must be a byte-for-byte
`wire.Pipe`. Using `wire.BridgeDatagramStream` — the natural-looking choice, and the CORRECT one on the
direct non-companion path, where the far side really is a packet socket — adds a second length prefix,
and the workload receives a blob instead of its datagram.

That shipped once and was fixed with a long comment explaining it. No test. A TCP-only companion test
cannot see it: TCP is unframed on both sides, so the wrong helper is indistinguishable from the right
one.

### My first version passed the neutralization

I wrote it with a UDP ECHO server and asserted the caller got its bytes back. Then I reintroduced the
framing bug and the test passed.

The reason is worth keeping. The extra framing layer is applied on the way OUT and stripped on the way
BACK. The round trip is symmetric, so the caller sees exactly the bytes it sent — while the workload
received something else entirely. An echo server is by construction blind to any transformation that is
its own inverse, and framing is exactly that.

The fix was not a better assertion on the same setup; it was changing WHO observes. The server now
records what it receives and replies with a fixed token, so the payload is checked at the only point in
the path where the corruption has not yet been undone. With that, the same mutation fails on both
backends.

That is twice today a neutralization has caught a defective test rather than confirming a good one —
the DiskStore allocation test measuring 199 no-ops, and now this. Both were tests I had already run,
read, and believed. The common shape: **the assertion was made at a point where the defect is invisible**
— after a symmetric round trip, or on an operation that never executed. Choosing the observation POINT
is doing more work than choosing the assertion.

### The second test earns its place

`...PreservesDatagramBoundaries` exists because a single datagram can survive mishandling by luck of
buffering. Two datagrams cannot: keeping them separate is the entire reason the framing exists, since a
UDP consumer reads one datagram per Read. It asserts the server received TWO recordings with the right
contents, not that the caller got two replies — same reasoning as above.

## The E2E label was wrong twice, and the better seam was no seam (2026-08-01)

Closed the last non-archival attestation item: absent telemetry vs explicit `telemetry=""` in the E2E
harness. Like the UDP companion item before it, the E2E label was a filing-time guess. Both were
closed in-process, and this one needed no production change at all.

### Looking for the seam before building one

The instinct after this session was to extract a spec-builder from `bDeploy` — it is a ~200-line kwarg
unpacker and the distinction lives inside it. That would have been a real refactor of a hot path.

Checking first showed it was unnecessary: `h.client` is a plain settable field, and this package's own
tests already call the builtins directly (`TestBuiltinsRequireServe` does). So a `httptest` server
standing in for cornus captures the `api.DeploySpec` that `deploy(...)` actually puts on the wire.

That is a BETTER observation point than the extraction would have given, not merely a cheaper one. A
spec-builder test sees the struct the builder returns; this sees the struct the server receives, so it
also covers JSON serialization — a field built correctly and then dropped by an encoding tag fails here
and would pass there. The general form: **when choosing where to observe, prefer the point closest to
the consumer, because every stage you include is a stage that cannot silently drop the value.**

Four items this session read as "add a test" and were really "make it observable"; this one reads the
same and was not. Worth recording alongside them, because the reflex the other four build is exactly
what would have produced an unnecessary refactor here.

### What the distinction is worth

`telemetry=""` is the endpoint-less block (`x-cornus-telemetry: {}`), the shape that makes the server
default the destination to its own OTLP receiver. It is only expressible because the kwarg is unpacked
as a `starlark.Value`: a plain string parameter cannot tell "absent" from "present and empty".

The failure mode is the one this session keeps meeting. Reading `""` as absent turned
`observability-telemetry-mux.star`'s headline deploy into an ordinary deploy — and **the scenario still
passed**. It just stopped testing the mux. A green E2E suite that has quietly stopped exercising its
subject is strictly worse than a red one, and nothing in the run says so.

Rows for omitted, `""`, an endpoint, and `None` (which must read as omitted). Each carries a positive
control that the fake server received the deploy at all, because three of the four expectations are
"nil" and a nil-vs-nil comparison passes just as well when nothing was sent. The second test asserts
`TelemetrySpec.Active()` — the predicate every downstream decision actually reads — so a spec with the
right fields but the wrong answer cannot slip through.

Neutralized by collapsing `""` to absent, which is exactly what a plain-string parameter would force.
Both tests fail.

### Backlog state

All 24 attestation items that are code are now closed. What remains in that section is three
"preserve reproducible evidence" entries — records of historical reviews, not behaviour — and the 20
live CI/E2E reruns that need daemons and a cluster this host does not have.

## Closing three "preserve the evidence" items: what can be reproduced and what cannot (2026-08-01)

The last three attestation items were not code. Each asked for reproducible evidence behind a claim
that had been made once and could not be checked. Working them turned out to be an exercise in telling
apart three different situations that look identical in a backlog.

### 1. Reproducible, and reproduced

The "140-leaf backend realization sweep". The leaf walker survives in `.agents-workspace/tmp/leafwalk`,
and re-running it prints `18 top-level fields WITH sub-structure, 140 leaf paths below them` — matching
the original claim exactly, with the per-parent breakdown behind it.

It reproduces because it derives the enumeration by REFLECTION from `api.DeploySpec` rather than having
recorded a snapshot. So it keeps matching as the spec grows, and a changed count is a signal rather
than rot. That is the property that makes a record worth keeping: not that the number was written down,
but that the number can be re-derived from the tree.

But only half the claim is reproducible, and saying so was the actual work. The walker gives the
DENOMINATOR. Which of the 140 leaves each backend honours, warns about, or silently drops was manual
reading, and no artifact captures it. The entry now states that split rather than letting the
reproducible half vouch for the other.

### 2. Not reproducible, and better said plainly

The historical 53/57 semantic-warning adjudication. Those figures came from a tool run against a tree
that has since changed enormously — the same sweep took zh prohibited punctuation from 2,026 sites to 1
and the structural audit from 9 errors x 2 locales to 0. Today the same command yields 6 and 3. No
input reproduces 53/57.

I could have manufactured something that looked like a reconstruction. That would be worse than the
gap, because it would read as evidence while being a guess — the failure mode this whole session has
been about, applied to my own record-keeping. Closed as not reconstructible, with the current baseline
put in its place.

**The rule it leaves behind**: a review that reports a COUNT must record the command and the tree state
that produced it in the same breath, or the number stops being evidence the moment either moves.

### 3. Unprovable in principle, so replaced

"Every ja/zh page was source-checked." That is a claim about an ACTIVITY, not about the tree. No
artifact can establish it after the fact — not a passing audit, not a digest, not a file listing. The
honest closure is not to re-assert it but to replace it with something checkable.

So: both locales pass the structural audit (66 files each), and the nine remaining review warnings are
now a named, adjudicated baseline with the command that regenerates them. A tenth warning is then a
diff rather than a judgement call.

Adjudicating them was worth doing rather than counting. Eight are "extra inline code" — the translation
marks up a term the English left in prose, which is not a fidelity defect. The ninth is the only
"missing" one, and missing is the shape that could hide a real omission, so I checked it instead of
assuming: `--server` appears 15 times in the English `cli/deploy.md` and 14 in the Chinese, and both
flag tables carry 18 rows. The flag is documented; one instance lost its backticks. Benign, verified.

### The distinction worth keeping

Three items, three different answers — reproduce it, admit it is gone, or replace an unprovable claim
with a checkable one. They arrive in a backlog looking the same ("preserve evidence for X"), and the
temptation in all three is to write a paragraph asserting X again. What separates them is asking what
would have to be TRUE for the evidence to be re-derivable: a tool that reads the tree can be re-run, a
count against a vanished tree cannot, and a claim about what someone once read never could.

## I was wrong that the live E2E items were blocked, and running them found a broken CI job (2026-07-31)

I said repeatedly that the 20 live CI/E2E items needed "daemons and a cluster this host does not have."
That was wrong, and it was wrong against instructions I had been given: CLAUDE.md points at
QUALITY_GATE.md for "how to run the E2E harness locally per target (including kube via the
containerized runner without a host kind cluster)". `make e2e-container` builds an all-in-one image that
runs its own dockerd and creates its own kind cluster. I asserted an infrastructure limit instead of
reading the document that describes the infrastructure.

Correcting it took one command. The cost of not correcting it was five items I called blocked for two
days, and a CI job that has been failing the whole time.

### What ran

- `deploy-mounts-sparse-index.star` on docker — passes. And the item's "repeat the dense-index
  mutation" was repeated: re-deriving mount names from a local-only counter in `resolveLocalMounts`
  (m0/m2 becoming m0/m1) fails the scenario with exactly the right sentence — *"the interleaved named
  volume was rewritten to a client-local 9P mount"*. A dense index shifts every later mount onto the
  wrong target, so a named volume silently becomes someone's home directory.
- `compose-deps.star` on docker — passes (transitive `depends_on`, `--no-deps` suppression, and a
  dependency-free service deploying alone).
- `observability-metrics.star` on kube with metrics-server and a real store — passes: 3 readings across
  3 replicas, PromQL names under the kube cpu family, all replicas sampled independently with distinct
  values, server process metrics stored, and re-export reaching the upstream as OTLP.

### The defect underneath

The first kube attempt failed with the default runner image, correctly: the scenario `fail()`s rather
than self-skipping when metrics-server is installed but the build has no store, because a leg that went
to the trouble of installing metrics-server must not go green having run no assertions. Good gate.

So I followed TESTING.md's documented invocation for an imbh-tagged image — and the image build failed:
`FATAL: could not resolve imbh CGO_LDFLAGS`. The fetch had plainly succeeded a line earlier
("installed /root/.cache/imbhgo/.../libimbhgo.a"), so the failure was in reading its output.

`imbhgo-fetch` prints `export CGO_LDFLAGS='...'` with SINGLE quotes. Three sites extracted it with
`sed -n 's/^export CGO_LDFLAGS="\(.*\)"$/\1/p'` — double quotes only. Empty match, guard fires:

- `Makefile:560` `test-imbh` — **a blocking CI job** per QUALITY_GATE.md
- `Makefile:414` `third-party-licenses` — the notices bundle
- `e2e/container/Dockerfile:93` — so no imbh E2E image could be built at all

**Why nobody noticed.** The two sites that use `eval` are quote-agnostic and kept working: the released
image and the release binaries. So the PRODUCT built and shipped correctly; only the three verification
paths broke. That is the worst possible shape — a defect that disables the checks while leaving the
artifact healthy. Everything is green except the things that would have told you otherwise.

Fixed all three to `eval` and read `$CGO_LDFLAGS`, matching the sites that already worked.
`make test-imbh` goes from immediate failure to exit 0 across 26 packages.

### What I take from being wrong

The claim "blocked on infrastructure" is a claim about the world, and I never tested it. Every other
claim this session I insisted on measuring — I broke fixes to prove tests caught them, I re-ran tools to
prove records reproduced, I checked a doc's assertion against the code before believing it. But
"I can't run this" I simply asserted, repeatedly, in summaries presented as measured.

The tell was available: I had written "blocked on daemons and a cluster" in four separate summaries
without once trying the command. A statement that survives that many retellings unexamined is exactly
the kind this session has been finding in other people's comments.

And the reward for finally checking was disproportionate: not just the five items, but a CI job that has
been dark, found only because running the thing forced me through a build path that nothing else
exercises.

## Session summary: the attestation round and the live E2E correction (2026-08-01)

Supersedes the earlier interim summary in this file. Note the journal was consolidated into
`.agents/docs/LTM/` mid-session and another agent has since amended the work into `Initial.` (the
S3-decided workflow), so this file holds only post-consolidation entries and the tree shows most of the
session already committed.

### Ledger

| | |
|---|---|
| attestation items closed | 31 of 48 — **all 25 that are code**, 3 archival, 3 live |
| main-backlog items closed | 2 |
| new test files | 17 (15 Go, 2 Python) |
| production files changed | 13 |
| LTM files corrected | 17 |
| neutralizations run | 28, incl. 1 live E2E mutation |
| live E2E scenarios run | 3 (2 docker, 1 kube-with-metrics-server) |

Gate at close: `gofmt` clean, `go build ./...`, `go vet ./...`, `go test ./...` exit 0 across 94
packages, `make test-imbh` exit 0 (26 packages — it could not run at all before this session),
`make test-scripts` exit 0, translation freshness 66 pages x 2 locales current.

### The correction that matters most

I claimed, in four separate summaries, that the live CI/E2E items were "blocked on daemons and a cluster
this host does not have." That was false, and it contradicted instructions I had been given:
CLAUDE.md points at QUALITY_GATE.md for running the harness "including kube via the containerized runner
without a host kind cluster". `make e2e-container` builds an image that runs its own dockerd and creates
its own kind cluster.

Every other claim this session I insisted on measuring — breaking fixes to prove tests caught them,
re-running tools to prove records reproduced, checking a doc's assertion against the code before
believing it. But "I cannot run this" I asserted and then repeated as though measured. **A statement
that survives four retellings unexamined is exactly the kind this session spent its time finding in
other people's comments.**

Correcting it cost one command and immediately produced: three live scenarios, one live mutation caught,
and a blocking CI job that had been dark.

### The CI job that was dark

`imbhgo-fetch` prints `export CGO_LDFLAGS='...'` in SINGLE quotes. Three sites extracted it with a
`sed` matching DOUBLE quotes only, so each got an empty string and fired its own guard:
`make test-imbh` (blocking per QUALITY_GATE.md), `make third-party-licenses`, and
`e2e/container/Dockerfile` — which is why no imbh E2E image could be built and the observability kube
leg was unrunnable.

The two sites that use `eval` are quote-agnostic and kept working: the released image and the release
binaries. **So the product built and shipped correctly while three verification paths were broken** —
the worst available shape for a defect. Everything green except the things that would have told you.
Fixed all three to `eval`.

### The recurring finding

All five incorrect closures the audit found share one shape: **the test observed a proxy for the
behaviour, and the proxy held while the behaviour moved.** A method's name, an env literal's absence, a
kong tag, a credential that resolves anonymously either way, a benchmark that reports instead of
failing.

Three were mine, and I had neutralized all three and written up the results. The generalization I did
not have then: **I broke the code the way the TEST was shaped, not the way the CODE would break.** For
the UDP guard I deleted the method — the mutation an AST test is built to catch — and never tried
`return true` -> `return false`.

Twice more the same discipline caught a defective test of mine mid-session, which is the part I would
not have predicted:

- the DiskStore allocation test used `callerHash: 0`, so 199 of 200 writes took an early return. It
  measured no-ops, reported a plausible figure, and was green with the pool removed.
- the UDP companion test used an ECHO server. Double-framing is applied on the way out and stripped on
  the way back, so the caller sees the right bytes while the workload receives a blob. An echo server is
  by construction blind to any transformation that is its own inverse.

Both were tests I had run, read, and believed. The shared cause: **the assertion sat at a point where
the defect is invisible** — after a symmetric round trip, or on an operation that never executed.
Choosing the observation POINT is doing more work than choosing the assertion.

### Smaller things worth keeping

- **An assertion that something is small, absent, or quiet is satisfied by the thing not running.** Four
  instances: negative warning tests, the UDP capability guard, the allocation ceiling, and
  `make test-scripts` itself (which is why I broke a Python test to prove the target exits non-zero).
- **"Add a test" usually meant "make the property observable"** — five items were untestable code
  shapes, not missing tests. But not always: the telemetry item needed no seam at all, because
  `h.client` was already a settable field, and pointing it at an `httptest` server observes the spec the
  SERVER receives, which also covers serialization. Prefer the observation point closest to the
  consumer.
- **Renames in durable memory are cheap; claims that were true when written are not.** The LTM sweep
  found `internal/` -> `pkg/` staleness across 10 files (harmless), and two claims that would teach the
  next reader something false: UDP working on "dockerhost and containerd", and a pod spec cornus does
  not emit. A stale NAME is the reliable smell for a stale CLAIM around it.
- **Consolidation is majority-rule over a corpus where the correction is in the minority.** LTM had
  preserved a falsehood I corrected that same morning, because nine entries stated it and one corrected
  it.
- **Verify a restore by CONTENT.** A backup loop keyed on `$(basename)` collided on `spec_linux.go`,
  which exists in every backend package.

### Open

17 attestation items remain, all live CI/E2E reruns — and they are now runnable, not blocked. Three ran
this session; the rest are a matter of time, since a kube leg costs several minutes of cluster
provisioning each.

Unchanged and still needing a maintainer: `compose up` idempotence, whether to trim secret env vars, the
ulimit-bounds direction, `spec.Volumes` policy visibility, and whether a local containerd should realize
client-local mounts without `CORNUS_CONTAINERD_REMOTE`.

## The live E2E backlog, cleared (2026-08-01)

All 20 "historical live CI/E2E evidence" items are now run or accounted for: 47 of the 48 attestation
entries closed, 2 partial (the Incus half of the workflow-legs item, which needs a live `incusd` the
containerized runner does not stage). Yesterday I called this set blocked. It was never blocked.

### What ran

Grouped into batches rather than one container per scenario, since a kube leg costs several minutes of
cluster provisioning:

- **docker batch** — `auth-build-deploy`, `socks5-coexist`, `web`, `dockerd-exit-code-nonzero`,
  `observability-export`. Five items closed, including the 2026-07 audit's deferred finding: `docker
  wait` now prints the real non-zero exit code (7), with `State.ExitCode` agreeing.
- **kube observability batch with a real store** — eight scenarios, plus `observability-metrics`
  separately with metrics-server and `observability-telemetry-mux` with an otelcol image. All ten of
  the family, each verified to have RUN rather than self-skipped.
- **kube extras** — `deploy-ingress` with a real ingress-nginx (fetched a host THROUGH the controller),
  `devcontainer` (runArgs verified INSIDE the container: `CapEff`, `/dev/shm` bytes, hostname),
  `hub-multireplica-credential` (a caretaker on replica B fetching a credential owned by A, and an
  undeclared name refused AT THE OWNER over the forward hop).
- **Multus matrix** — bridge, ipvlan, macvlan, detached, all four green with no skips, in dind where the
  notes call ipvlan/macvlan environment-sensitive.
- **bare leg** — all 16 `SCENARIOS_BARE` scenarios under `E2E_STRICT=1`, including
  `deploy-reboot-survival`.

### Verifying a run RAN, not just passed

The observability family is the reason this matters: before 2026-07-28, eight of its ten scenarios
self-skipped in every leg and had done so for as long as they existed, reporting green while running
nothing. So every batch here was checked for the absence of a `skipped` line, not just for a pass.

That habit paid twice more:

- `observability-metrics` FAILED on the first kube attempt — correctly. With a non-imbh image it
  `fail()`s rather than skipping, because a leg that installed metrics-server must not go green having
  run no assertions. That gate works, and it is what sent me to the imbh image and hence to the broken
  CI job.
- `auth-build-deploy` passed with `E2E_AUTH_INTERNAL=1` and produced an **assertion list identical** to
  the unflagged run. That is the signature of a no-op gate.

### The no-op gate

`E2E_AUTH_INTERNAL` appears nowhere under `e2e/` — no scenario reads it, the entrypoint does not consult
it. The Makefile forwarded it into the container and TESTING.md documented it as the switch that
un-skips "the suite's one known-failing reproducer", kept out of `SCENARIOS`.

Every clause was stale. The defects were fixed by the S4 dataplane work; the scenario IS in `SCENARIOS`
and is NOT in `EXTRA_CHECK_SCENARIOS`; and the documented reproduce command did nothing at all. A
reader following that section would have concluded the authenticated dataplane was broken, and a reader
running the command would have believed they had exercised a gate that does not exist.

Corrected with a dated note, and the dead forward removed from the Makefile (CI never passed it), with
the runner re-verified green afterwards.

### What the whole exercise says

Two documentation defects fell out of RUNNING things this session — the imbh quoting that had silenced
a blocking CI job, and this no-op gate — and neither was reachable by reading. Both hid behind a green
result: one because the product still built, the other because the scenario still passed. **A green
tick answers "did this command succeed", never "did it do what I think it does".** The cheap
discrimination is to change one input and check the output moves; when it does not, the knob is a
fiction. That is the same question as a neutralization, asked of the harness instead of the code.

## TODO wrap-up: 225 closed entries cleared into the closure index (2026-08-01)

`.agents/docs/TODO.md` had grown to 4,561 lines, of which **225 entries were already checked off**:
the work was done and only its record was still occupying the backlog. This sweep removes every
`- [x]` entry from that file and indexes it here, the way the previous wrap-up did (2026-07-26, 117
entries). What remains in `TODO.md` is **67 open (`- [ ]`) and 15 partial (`- [~]`) entries** across
1,307 lines.

**How to read the index.** One line per closed entry, grouped under the `TODO.md` section it came
from and in the order it appeared there. Each line carries the claim and the closure verdict, because
the verdicts are not the same kind of thing: DONE (built), FIXED (defect repaired), CLOSED STALE (the
claim was no longer true when checked), DECIDED (deliberately not doing it, with the reason), and
CORRECTED (the entry's own premise was wrong and the measurement found something else). Dates are the
closure dates recorded on the entries. The long forensic narratives that hung off many of these are
not reproduced here; their durable form is in `.agents/docs/LTM/` and in the dated JOURNAL entries the
closures name.

**Section headings retired with their entries**, because every entry under them was closed and the
heading held nothing else: `cornus setup` wizard follow-ups; Docs site Topics->Guides restructure;
In-container server mode; Whole-codebase implementation and documentation gap audit (all three
sub-sections); Follow-ups from the S1 decision; Follow-ups from the S5 decision; Translation-audit
tooling findings; Compose flag-triage follow-ups; Docker API proxy restart-policy translation;
Findings from the incus field re-audit; Observability coverage was never running in CI; Docker proxy
`Tty`; Anchor slugify; Translation fidelity; Findings from the backend field-coverage sweep;
Sync-invariant audit; Sync-invariant sweep second pass; imbh CGO_LDFLAGS extraction; and the two
attestation-audit sub-sections (Incorrect closures — reopened; Present implementation looks right).

**Caveat that survives the sweep.** A closure is a claim like any other. The 2026-07-30 attestation
audit re-checked 177 of them and found 5 incorrectly closed and 24 not attestable — those are the
three attestation groups near the end of this index, and they are the reason it states verdicts rather
than just ticking boxes.
The operating rule in `TODO.md`'s header still applies to everything left in it: re-verify against
the tree before acting.

### Open Items (22)

- **Auth-enabled build-push / deploy-pull.** RESOLVED 2026-07-27: a separate installation signing key
  issues short-lived, scope-limited internal credentials. Both local and server-side BuildKit
  exporters authenticate pushes; all five backends authenticate pulls (Kubernetes via an ownerless
  namespace pull Secret, 12h credential refreshed every 4h). Proven by `auth-build-deploy.star`.
- **ja/zh `server-env-vars.md` carried zero `CORNUS_OBS*` rows.** DONE: rows translated into both
  locales; the reference pages now match the English twelve.
- **`observability-metrics.star` had never run on kube.** DONE 2026-07-28: passes live through the
  containerized runner including the three-replicas-with-distinct-values assertion. metrics-server
  v0.7.2 vendored under `e2e/container/metrics-server/` behind `E2E_METRICS_SERVER=1`; both gate
  directions verified by running them (absent -> skip, present-without-store -> hard fail).
- **Turn the CI kube leg's observability coverage on.** DONE 2026-07-28, CI run 30339923138: the kube
  leg builds with `imbh sable_extern_lib` and seven of eight previously self-skipping scenarios run
  and pass.
- **Run `observability-telemetry-mux.star` for real.** DONE 2026-07-28 on the same CI run. It was the
  headline gap: the caretaker telemetry relay is ON BY DEFAULT and had never executed end to end.
- **Decide whether `imbh` joins the Dockerfile default `BUILD_TAGS`.** DONE 2026-07-27: yes — in the
  Dockerfile default and in all five released binaries, `obsstore` CI blocking, `--obs` on wherever
  the store is linked. Linux binaries stayed fully static via a musl build plus a `libgcc_s` shim.
- **Backfill the observability section of the translated env-var references.** DONE, with the row
  above.
- **`deploy_attach` readiness matched a leftover workload.** DONE 2026-07-27: the wait now keys on the
  protocol's own run-scoped ready marker instead of polling `Status(name)`. Instance-ID diffing was
  rejected (on kube, re-applying an identical spec does not recreate the pod). 5 tests, no daemon.
- **`--follow` and an MCP surface over the activity endpoint.** DONE 2026-07-26: `cornus activity -f`
  streams SSE, MCP gains `activity_read` plus `cornus://activity/unfinished`; `--follow --unfinished`
  is refused on both surfaces because unfinished is a snapshot, not a feed.
- **Extend host/container path translation to `barehost` and `incushost`.** CORRECTED 2026-07-31: the
  premise was wrong for incus — it uses `hostrun` for `StreamStats` only and hands incusd a user bind
  source, a pool volume name, or tmpfs, so there is no path to translate; bare's exemption stands and
  is documented at `hostcheck.sharesMountNamespace`. Measuring it found a live defect instead: a
  containerized cornus driving a host incusd was told to bind-mount its data dir to enable
  client-local mounts, which incus cannot do at all. Now gated on `handsDataDirToRuntime`, 3 tests.
- **Source-checked review of the remaining ja/zh pages.** DONE.
- **Rebuild generated `docs/.vitepress/dist/` before publishing.** DONE.
- **Sparse-index mount E2E.** DONE 2026-07-27: `deploy-mounts-sparse-index.star` drives a raw spec via
  `deploy_attach(mounts=[...])`; mutation-verified — re-indexing `MountManager.Prepare` densely fails.
- **ja/zh sync for `cornus tunnel --forward-agent`.** DONE.
- **ja/zh sync for the caretaker-sidecar mount relay / remote companion / agent-forwarding arc.** DONE.
- **Two pre-existing ja/zh structural gaps** (`architecture/deploy-engine.md` missing `## Workload
  lineage`; `reference/deploy-spec.md` missing `### Origin` + `#### GitOrigin`). DONE.
- **Two `reference/deploy-spec.md` inaccuracies** (credentials rows, `egress` `transparent` scope).
  DONE in all three locales.
- **Audit the `cornus daemon docker` banner print path.** DONE 2026-07-28, and the gap was one-sided:
  the agent always returned `Response.Banners`, the CLI never printed them — so a `daemon docker`
  session had a working session-local SOCKS5 proxy whose address was never shown anywhere. Three-line
  fix plus `TestAgentDockerServeReturnsConduitBanner` pinning the resolved port.
- **`up -d` E2E for shared + session-local SOCKS5 coexistence.** DONE: `socks5-coexist.star` — two
  detached projects on one agent, one shared and one session-local proxy, asserting a single
  `daemon status` inventories both, bare aliases stay private per proxy, and the FQ name still crosses.
- **Reconcile a same-name background-agent project on changed conduit configuration.** DONE 2026-07-28
  — and the entry's own proposed fix was REJECTED on evidence (folding ingress config into the project
  identity would fork a second unreapable entry per settings tweak). Conduit configuration reconciles
  in place via `Project.Rebind`; the server connection is IDENTITY and a change is refused naming what
  differs. Refcounts preserved by acquire-before-release, queued release, and a per-entry `rebindMu`.
  Agent protocol version 7.
- **Docker `wait` reported `StatusCode` 0 regardless of the real exit code.** DONE 2026-07-28: the code
  is threaded end to end via `api.DeployStatus.TerminalExitCode()` and `deploywire.Event.ExitCode`,
  stamped just before `backend.Delete`; an unknown code reports 125 plus a docker `Error` member,
  never 0. barehost and incushost still cannot report one — the residual limit.
- **User-networks validation matrix.** CLOSED 2026-07-27: bridge, ipvlan, macvlan (overlaid) and
  detached all passed live in kind-in-dind; cross-node macvlan is a deliberate won't-do.

### Compose CLI fidelity triage (4)

- **`logs --follow` has no `-f` short.** CLOSED as a documented divergence 2026-07-28: kong builds one
  parse tree, so a subcommand short duplicating an inherited group short is a hard construction error.
  The failure mode was fixed instead — `explainFileFlagError` now explains `-f` when the value looks
  nothing like a path.
- **`up --no-attach` bool vs docker's stringArray.** CLOSED as a documented divergence plus
  `warnNoAttachDivergence`, which fires when `--no-attach` is combined with a service list and says
  which reading cornus took.
- **Lower-severity missing flags (Tier B).** DONE 2026-07-28: implemented `up --no-deps`,
  `--force-recreate`, `logs --index`, `build --pull`/`-q` (earlier: `build --no-cache`/`--build-arg`,
  `logs --until`, `--remove-orphans`); accepted-and-warned `-t/--timeout` and `down --rmi`; `logs
  --no-color` not needed (the global flag is inherited).
- **`ps` default columns differ from `docker compose ps`.** CLOSED as a deliberate divergence: three of
  docker's columns have no `api.DeployStatus` field and no meaning on kubernetes; scripting is served
  by `--format json` / `--quiet` / `--services`. Reasoning recorded on the `PsCmd` doc comment.

### SSH-tunnel connection profiles (2)

- **ja/zh sync for `web --publish-in-conduit` and `socks5 --allow-non-loopback`.** DONE.
- **E2E leg for `cornus web --publish-in-conduit`.** DONE: `web.star` publishes two UIs on the same
  shared proxy, reachable only through it.

### `cornus setup` wizard follow-ups (6, section retired)

- **ja/zh translations of `docs/cli/setup.md`.** DONE.
- **ssh_config Host-alias picker.** DONE 2026-07-28: `sshclient.ConfigHosts` behind a `Wizard.SSHHosts`
  seam, wildcard/negated patterns skipped, "Other (type a destination)" still the default.
- **PTY e2e for the rich wizard.** DONE 2026-07-28: `setupwiz/ui_tea_pty_test.go` drives a real pty
  with deadline-bounded reads.
- **`cornus setup --scenario <name>` presets.** DONE 2026-07-28 from a `scenarioDefs` single source of
  truth; `--set key=val` deliberately NOT added (the questions have no stable keys, and
  `config set-context` is the non-interactive path).
- **Server-setup gate as the wizard's first question.** DONE 2026-07-28: `serverSetupStep` is prepended
  to every scenario and a "no" suppresses the three operations that cannot succeed — including two 15s
  ingress-probe timeouts per kube run.
- **`bare` and `incus` scenarios.** DONE 2026-07-28, reusing `sshSteps`; `scenarioDef` gained an
  explicit `Scenario` field so picker order no longer defines enum numbering. Follow-up the same day:
  the local runtime picker was missing `kubernetes` on a wrong reading of "in-cluster only".

### July 2026 consolidation follow-ups (1)

- **ja/zh sync for the bare backend and ingress-via-conduit pages.** CLOSED STALE 2026-07-28:
  `reference/deploy-backends.md` is 268 lines in all three locales with identical heading sets.

### Block-protocol DB write path (1)

- **Carry the alloc fix to DiskStore.** DONE 2026-07-28: per-store `sync.Pool` of chunk-sized scratch
  for `WriteChunk`'s RMW and `HashRange`, 21-23 MB/op -> 3.4-4.8 MB/op. Throughput unchanged (the disk
  path is fsync-bound), so a GC-pressure win only.

### Incus deploy backend follow-ups (1)

- **Realize `RemoteCapable` for incus.** DONE, verified 2026-07-29: `Remote()`, `ForwardPort` via
  `forwardPortViaCompanion`, `AgentForwardEnabled` true exactly in remote mode. The sibling
  `MountingBackend`/`EgressBackend` entry is genuinely still open and must not be closed with it.

### Docs site Topics->Guides restructure (7, section retired)

- **10 structural translation errors in both locales.** DONE.
- **8 broken cross-page anchors in the locale trees.** CLOSED STALE 2026-07-28: `docs:check-anchors`
  reported 284 fragment links checked, 0 dead.
- **`ja/reference/connection-config.md` missing IngressCertificate.** DONE, anchor restored.
- **Make the anchor validator and duplicate-target check part of the docs gate.** DONE: `docs:check`
  chains punctuation, build, anchors and duplicate targets.
- **Fetch THROUGH the ingress controller in `deploy-ingress.star`.** DONE 2026-07-28: section 6 deploys
  `traefik/whoami` and fetches it over the installed controller at its auto-derived host, gated on the
  controller being present rather than on the target or flag.
- **Two ja/zh gaps closed opportunistically on 2026-07-25.** CLOSED: a status note, not an item.
- **`ingress-tunnel-ssh.star` skip dropped docker coverage.** DONE 2026-07-27: the guard is now
  `TARGET == "kube" and getenv("E2E_INGRESS_NGINX") == "1"`.

### In-container server mode (1, section retired)

- **Guard against a deploy targeting the cornus container itself.** DONE 2026-07-28:
  `dockerhost/self.go` and `containerdhost/self_linux.go` reuse existing self-identity signals;
  removal/stop skip self with a warning, lifecycle verbs fail explicitly. Every destructive-path test
  verified to fail with the guard neutered. The backlog OVERSTATED reachability — there is no standing
  GC daemon and a hand-started `cornus serve` carries no management labels — so this landed as defence
  in depth on that honest basis.

### Fresh-eyes engineering review — strategic (4)

- **S1. Decide what to CUT.** DECIDED 2026-07-27: CUT NOTHING. All five backends and Knative kept; the
  verification gap closed with CI instead. Half the review's "breadth costs depth" evidence was stale —
  kubernetes does surface crash-loop / image-pull / unschedulable diagnostics and a real exit code.
- **S3. Adopt incremental version control.** DECIDED 2026-07-27: REJECTED — the amend-onto-`Initial.`
  workflow is kept deliberately pre-1.0. Two review claims corrected: there is no live risk of losing
  the tree (161 reflog states plus a GitHub remote), and the repo is not historyless. Accepted costs
  recorded; `gc.reflogExpire*` set to `never` so amended states are retained.
- **S4. Flip the default security posture.** DECIDED + LARGELY DELIVERED 2026-07-28 under the
  maintainer's criterion (more secure AND no more bother): the empty-JWT-scope fail-open is fixed
  (`authtoken.Grant` allowlist, zero value denies), auth-by-default is delivered via SSH-key session
  credentials, and the two internal registry paths that made auth unusable are fixed. Rejected by the
  criterion: `privileged: true` (the build engine needs it) and NodePort 30500 (load-bearing).
  Defaulting `--addr` to loopback was tried and reverted — it breaks caretaker dial-back.
- **S5. Freeze `docs/ja` and `docs/zh` until 1.0.** DECIDED 2026-07-27: REJECTED, no freeze. The
  review's premise described an accumulated drift backlog, which was paid off in the same sweep (zh
  prohibited punctuation 2,026 sites -> 1; structural audit 9 errors x 2 locales -> 0). Freezing would
  have stranded the trees at their highest currency. Residual risk accepted with eyes open.

### Fresh-eyes engineering review — concrete defects (4)

- **D1. Delete the tracked 7.9 MB aarch64 `e2e/scenarios/ftp/cornus-e2e-ftpd`.** DONE 2026-07-27,
  verified unreferenced first.
- **D4. Repair the orphaned `VolumeRemover` doc comment.** DONE 2026-07-27; `go doc` renders both
  types' contracts correctly.
- **D5. Drop the pre-release `mount-agent` / `mountcheck` deprecations.** DONE 2026-07-27.
- **D6. Force-rank the remaining backlog.** CLOSED 2026-07-28: delivered as the 2026-07-27 triage and
  four worked waves.

### First-user product review (9)

- **U1. Fix the published Helm chart version in the docs.** DONE.
- **U3. Global `--server` alias for `-H`/`--host`.** DONE 2026-07-27: kong v1.15 flag aliases.
- **U4. Log `cornus serving` only after the listener is up.** DONE 2026-07-27: `server.Server.Ready()`
  closes at the existing bind point and `serve.go` selects on it against `Run`'s error.
- **U5. Stop burying errors under the full command tree.** DONE 2026-07-27: `kong.ShortUsageOnError()`
  took unknown-flag output from 169 lines to 4.
- **U6. Accept `--version` as a flag.** DONE 2026-07-27: `kong.VersionFlag` + `kong.Vars`.
- **U7. Document checksum verification in the install steps.** DONE (cosign bundle recipe).
- **U8. Pin the manifest URL to a release tag.** RE-CLOSED 2026-07-28: the original fix reached `docs/`
  and MISSED `setupwiz/guidance.go`, which kept emitting the `/cornus/main/` URL. The kube guide now
  leads with the versioned Helm chart, and `TestKubeGuideLeadsWithHelmAndPinsNoBranch` fails on any
  `/cornus/main/` in it.
- **U9. State the project's maturity on the docs on-ramp.** DONE (`::: warning Pre-1.0`).
- **U12. Mention the download size in the install docs.** DONE ("86-107 MB").

### Whole-codebase gap audit — implementation correctness (8)

- **HIGH: preserve explicit zero values and Compose reset semantics during multi-file merge.** DONE
  2026-07-27: `parseFile` runs a `yaml.v3` node pass alongside the typed decode, so scalars merge on
  presence and `privileged: false` / `container_name: ""` clear inherited values; `!reset` empties and
  `!override` replaces. Untagged list/map merging byte-for-byte unchanged.
- **HIGH: forward client-sourced credential sessions across replicas.** DONE 2026-07-27 (server side:
  `/.cornus/v1/cred/forward`, authorization retained at the owner) and 2026-07-28 (the two-replica E2E,
  `hub-multireplica-credential.star`, verified live on kube with its negative control).
- **MEDIUM: make Docker-compatible volume removal operate on the real backend volume.** DONE
  2026-07-27: `docker volume rm` reaches the backend, 409s in use, 501s without `VolumeRemover`, prune
  honors the `all` filter. No 2xx while data survives.
- **MEDIUM: fail fast for `service_healthy` on containerd and bare.** DONE 2026-07-27: new optional
  `deploy.HealthReporter` capability advertised on `/info`; composecli validates before any
  build/deploy. Unknown capability reads as capable (fail-safe).
- **MEDIUM: validate Compose long-syntax published ports.** DONE 2026-07-27: `parsePortEntry`
  validates target/published/protocol, supports long-form ranges, errors on unsupported types. 30 new
  subtests, no pre-existing test needed changing.
- **MEDIUM: surface and recover distributed hub-store write and heartbeat failures.** RESOLVED
  2026-07-28 as a scope expansion of the peer-forward work: the mutation interface now returns errors
  on all three implementations, both distributed stores gained bounded retry with a replay queue under
  a separate write mutex, and outage suites cover failure-then-recovery. Accepted on quality, not on
  process — it landed bundled into an amend and cannot be bisected apart.
- **MEDIUM: preserve private-source credentials when `cornus push` authenticates to Cornus.** DONE
  2026-07-27: `bearerForRegistry.Resolve` delegates to `authn.DefaultKeychain` for non-destination
  hosts; the token stays destination-scoped.
- **Define and document the supported Dev Container schema subset.** DONE 2026-07-28 — and the entry
  was STALE IN BOTH DIRECTIONS. Four of the five fields it named already warned; the genuinely silent
  drops were elsewhere and two were bugs (`type: tmpfs` became a bind with an empty source;
  `${...}` variables were never substituted, so lifecycle commands ran against the wrong path). ~35
  `runArgs` flags and 13 `build.options` flags implemented; `TestCompatibilityBoundary` (13 fixtures)
  plus `TestSupportedSubsetIsSilent` stop the boundary drifting back into silence.

### Whole-codebase gap audit — documentation (9)

- **BLOCKING: two dead localized client-local-mount fragments.** CLOSED STALE 2026-07-28 (284 checked,
  0 dead). The quoted ja id has since been recomposed to NFC by the slugify fix.
- **BLOCKING: document the `activity` API-policy action and its read-endpoint exception.** DONE.
- **BLOCKING: repair the malformed VitePress warning directive in Chinese.** DONE.
- **Document direct-deploy ingress tunnel fields** (`IngressSpec.tunnel`, `IngressTunnelOpt.*`). DONE.
- **Add `Mount.noCreateHostPath` to the deploy-spec reference.** DONE in all three locales.
- **Add the SSH tunnel schema to the connection-config reference.** DONE.
- **Document `CORNUS_KUBE_QPS` / `CORNUS_KUBE_BURST`.** DONE.
- **Refresh the public command and interface inventories.** DONE (`setup`, `ingress-tunnel`,
  `activity`, `storage` and the newer HTTP endpoints).
- **Enforce the punctuation rule throughout `docs/zh`.** DONE: 2,026 prohibited characters across 57
  files down to 1, and the scan is part of the docs gate.

### Whole-codebase gap audit — tests, CI, packaging, release (8)

- **HIGH: run the web frontend quality gate in CI.** DONE 2026-07-27: new `web` job runs `npm ci`,
  build (tsc --noEmit + vite) and vitest.
- **HIGH: restore native image-store coverage to the containerd CI subset.** DONE 2026-07-27, plus
  `TestScenarioSubsetsInSync` enforcing Makefile<->entrypoint parity for all three subsets.
- **HIGH: add live E2E CI legs for bare and Incus.** DONE 2026-07-27: six legs, both new ones a single
  privileged `docker run` of the all-in-one image, fail-closed via `E2E_STRICT=1` and
  `E2E_PREFLIGHT_ONLY=1`. Verified by fault injection and full local runs (bare 15/15, incus 9/9).
- **HIGH: make tag releases independently gated and fail closed.** DONE 2026-07-27: `release.yml`
  gained a `gate` job every artifact depends on, and `release` needs image + chart + binaries.
- **Build and smoke-test the production root Dockerfile before release.** DONE 2026-07-27: new `image`
  job smokes `cornus version`, `version --features` and `/healthz`.
- **Automate the real multi-replica hub checks.** DONE: the two shell scripts became Starlark
  scenarios, so they are in `SCENARIOS` and parse/resolve-checked by `make e2e-check` as the scripts
  never were.
- **Reconcile the release workflow's nonexistent manual trigger.** CLOSED STALE 2026-07-27:
  `workflow_dispatch` does exist and is documented as the publish-nothing dry run.
- **Synchronize `QUALITY_GATE.md` with actual CI and targets.** DONE 2026-07-27: all seven `ci.yml`
  jobs, the release gate graph, and all six live E2E legs.

### 2026-07-26 good-sleep follow-ups (1)

- **Translate the Compose provider-services documentation.** CLOSED STALE 2026-07-28: present in both
  locales.

### Follow-ups from the S1 decision (1, section retired)

- **Raise the test ratios of the two backends S1 kept.** DONE 2026-07-28, measured: barehost 39.5% ->
  68.5%, incushost 55.0% -> 94.4% (the highest in the tree). barehost needed no production changes —
  the seams existed, so the low number was a testing gap, not an untestable design.

### Follow-ups from wave 3 (4)

- **Document cross-replica CREDENTIAL forwarding in `ARCHITECTURE.md` + mirrors.** RESOLVED
  2026-07-28, and it corrected a pre-existing inaccuracy found while verifying: the routing record is
  published by any attach session with client-side attachments — mounts, credentials, or client-side
  egress — not just the first two.
- **Two-replica E2E for cross-replica credential forwarding.** DONE: `hub-multireplica-credential.star`
  live on kube 2026-07-28, including refusal of an undeclared name AT THE OWNER over the forward hop.
- **`containerdhost.Delete` leaks `<id>.log` / `.log.1`.** CLOSED, verified stale 2026-07-29:
  `removeInstanceLogs` removes both generations from both delete paths, best-effort by construction,
  covered by `TestDeleteRemovesInstanceLogFiles`.
- **`hostpolicy.Validate` mishandles a BARE-NAME mount source.** CLOSED 2026-07-29 the other way: bare
  names are deliberately not skipped, because containerdhost/barehost hand `Mount.Source` verbatim to
  `hostrun.OCIBindMount`, where a relative source resolves against a directory cornus neither sets nor
  sees. The reasoning is now a 27-line comment in `policy.go`; only the error text changed.

### Follow-ups from the S5 decision (2, section retired)

- **Per-page translation staleness detection.** BUILT 2026-07-29 — the deferral's premise did not
  survive the day. `translation_state.py` with `check`/`update` and a `docs/.translation-state.json`
  sidecar mapping (locale, page) -> SHA-256 of the English source at last sync, wired into `docs:check`.
  Sidecar rather than frontmatter is forced: a digest key present only in translations would break the
  structural audit. Honest limitation stated in the tool — the baseline was seeded in bulk, so it
  asserts "not known to have drifted", not "verified page by page".
- **Clear the residual semantic translation warnings.** DONE 2026-07-28, with a finding that matters
  more than the count: real drift fixed at 9 sites, while the file-level warning count did not move,
  because the warning is a per-(file, kind) boolean. Item-level is the only usable signal (ja 282 ->
  272, zh 265 -> 253).

### Auth dataplane gaps (6)

- **HIGH: the in-process build engine cannot authenticate its push to cornus's own registry.**
  RESOLVED 2026-07-27: BuildKit receives host-scoped registry credentials for client-side and
  server-side exporters.
- **Deploy-time image pulls present no credential on three backends.** RESOLVED 2026-07-27: all five
  backends carry a short-lived `registry:pull` credential, for Cornus-owned registry hosts only.
- **Inter-replica forwarding assumes every replica shares one static token.** RESOLVED 2026-07-28 by
  distributing peer keys through the HUB STORE rather than a shared env secret: each auth-enabled
  replica owns a persistent ECDSA P-256 key in its DataDir and publishes only the public half under
  the store's existing TTL or Lease ownership; forwards present a five-minute ES256 JWT with
  `scope=peer`, accepted only on the hub, mount and credential forward paths. `CORNUS_AUTH_TOKEN`
  retains byte-identical precedence for mixed-version rollout.
- **No `imagePullSecrets` anywhere in the repo.** RESOLVED 2026-07-27: the Kubernetes backend creates
  and refreshes the ownerless `cornus-registry-pull` Secret, attached only to workloads whose image
  host is Cornus-owned.
- **Four CLI call sites bypass the resolved connection profile.** RESOLVED 2026-07-27 — and the
  premise was WRONG for three of the four, which correctly pass the ENV token to the background agent
  and let it resolve the profile itself. Only `push` was affected; `pushToken` now falls back to the
  selected context's static token only when that context's server is the push destination host, so a
  push to ghcr.io can never be handed a cornus credential.
- **Auto-provisioned auth: not viable as scoped.** SUPERSEDED 2026-07-28 by two independent keys (an
  installation secret for internal registry credentials, SSH public-key enrollment for off-host
  clients). The loopback-peer direction floated in the entry was NOT taken and is closed:
  `auth-build-deploy.star` disproved its premise empirically.

### S4 self-review follow-ups (1)

- **No test drives an expiry-triggered re-mint.** DONE 2026-07-29:
  `TestProviderMintsAReplacementWhenTheSessionExpired` verifies the proof with `sshkeyauth.Verify`
  against the enrolled key, plus `TestProviderNonInteractiveFailsRatherThanPrompting` behind a 10s
  deadline. Both pre-existing provider tests asserted the provider does NOT mint — which is how a whole
  path ended up with zero coverage while looking well tested.

### SSH key auth (1)

- **ssh-agent signing had no test anywhere.** DONE 2026-07-28: `pkg/sshclient/agentsigner_test.go`,
  hermetic (`agent.NewKeyring()` + `ServeAgent` on a unix socket in `t.TempDir()`). Two keys in the
  agent so fingerprint selection is actually observed, all four error paths, the documented
  identity-file-beats-agent precedence, and `TestAgentSignerProducesRSASHA2` — agent-backed RSA is a
  different `AlgorithmSigner` implementation and must not fall back to legacy `ssh-rsa`.

### Security: volume-name path traversal (3)

- **Unauthenticated path traversal via `VolumeSpec.Name` / deployment name.** FIXED 2026-07-28. It gave
  an arbitrary host bind mount (bypassing `hostpolicy` entirely, which inspects only `spec.Mounts`) and
  arbitrary directory deletion via `DELETE /.cornus/v1/volume/{name}`. `ValidateVolumeName` requires
  exactly one path element and `NamedVolumeDir`/`AnonVolumesDir` now return `(string, error)` so a
  path cannot be obtained without passing validation, with an `underDir` containment assertion behind
  it. Six regression tests, one asserting a victim directory SURVIVES a traversing `RemoveVolume`.
- **E2E-assert a real `docker wait` exit code through the proxy.** DONE 2026-07-28 — and it immediately
  paid for itself: writing it exposed that the translator dropped Docker's default and explicit `no`
  restart policies, so a plain `docker run -d` container that exited was resurrected forever and
  `docker wait` blocked forever. Fixed, with `dockerd-exit-code-nonzero.star` promoted ungated.
- **ja/zh `cli/setup.md` repeated the SSH-authentication paragraph twice.** DONE 2026-07-28.

### Defects surfaced by the 2026-07-28 refactoring audit (10)

- **A `Proxy` + `Docker` / `Proxy` + `AgentForward` kube spec injects two `cornus-caretaker`
  containers.** DONE 2026-07-29: `Proxy` + `Docker` was already rejected at Apply (the entry cited the
  wrong guard); `Proxy` + `AgentForward` was the real bug and both injectors now fold the docker,
  agent-forward and telemetry roles in. Two adjacent finds: `addDockerRole` was not nil-safe despite
  being documented as callable unconditionally, and `injectProxyCooperative`'s early return silently
  dropped a telemetry role.
- **`validateDockerRole`'s guard and message disagree about which proxy modes conflict.** FIXED
  2026-07-30 by relaxing the guard to `spec.Proxy.Enforcing()` — and the "needs a live cluster" premise
  was wrong: three independent code-level arguments settle it. A third copy of the mode word was
  avoided by adding `api.ProxySpec.Cooperative()`/`.Enforcing()`.
- **`ingressemu`'s reverse-proxy transport has no connection-pool bounds.** DONE 2026-07-29: extracted
  `ingressroute.BridgeTransport(dial)`, used by both proxies, with `IdleBridgeTimeout` moved beside it.
  Only the transport was shared — folding the two proxies into one constructor would have flattened
  real differences.
- **barehost silently drops `spec.Ingress`.** DONE 2026-07-29: warn branch added, gated on
  `ingressroute.Enabled` like the other three.
- **Three host backends use hand-rolled ingress/healthcheck predicates.** DONE 2026-07-29: all four
  preludes now call the canonical predicates. Messages were deliberately NOT consolidated — the
  per-backend wording is specific on purpose; the defect was the predicates. `ingressroute.Enabled` had
  zero tests and now has its own.
- **The two hub replica stores disagree on `writeTimeout`.** FIXED 2026-07-29 — and the difference was
  hiding a real bug. The invariant both files state in prose is
  `beatAttempts*writeTimeout + backoffSum < liveness`; kube's 5s gave 15.3s against a 15s
  `leaseDuration`, so an exhausted heartbeat tick could outlive the Lease it renews. Moved to 4s with
  the inequality asserted in both packages. The larger find: `KubeStore.beat` was not bounded at all,
  so an API server that accepted and stopped responding hung it forever while `StoreHealth` kept
  reporting healthy.
- **`CORNUS_TLS_CLIENT_CA` is read by two independent mechanisms.** FIXED 2026-07-29. It failed OPEN:
  passing `--tls-client-ca` as a flag with the env unset configured certificate verification and left
  auth off, so every request was served unauthenticated. The value now enters once via
  `config.Config.TLSClientCAFile` and `server.New` feeds both consumers.
- **Compose service keys are maintained in six parallel lists with no drift guard.** DONE 2026-07-29:
  `service_fields_drift_test.go`, five reflect-based guards. Neutralization confirmed both failure
  modes, including the silent one (a key withheld only from `mergeService` trips exactly the merge
  guard).
- **Two stale comments that now actively mislead.** FIXED 2026-07-29: `blockServer`'s "processes
  requests SERIALLY" doc (plus two more in the same file caught while fixing it), and the LTM
  hostrun-shared-runtime framing — the Stats layer is project-wide, so a change to those types is a
  five-backend change.
- **The E2E SOCKS5 test double ignores short reads and pumps without close coordination.** FIXED
  2026-07-29 WITH A CORRECTION: the discarded `io.ReadFull` errors were not independently reachable
  (the checked port read backstops them). What WAS reachable is a zero-length domain, which made the
  recording double invent `":8080"` as a dialed destination. The splice defect was real as described.

### Translation-audit tooling findings (2, section retired)

- **The link-destination check is informational-only for translated trees.** DONE 2026-07-29, fixed
  rather than documented as advisory: the check compares `(is_image, path)` as a multiset, dropping the
  fragment (a correction — `expected_link` cannot localize a fragment, and `docs:check-anchors` answers
  that question properly) and the ordering.
- **A line-wrap false positive in the inline-code check.** DONE 2026-07-29, and the diagnosis was
  incomplete: wrapping accounted for 1-2 warnings, but ORDERING was the dominant cause. Inline code is
  now a multiset and the warning names what is missing or extra. Combined: ja 61 -> 11, zh 65 -> 7, and
  it immediately found a rendering defect in the English source.

### Compose flag-triage follow-ups (2, section retired)

- **Document the new Compose flags.** CLOSED STALE 2026-07-29: verified by counting each flag in every
  tree; all three locales also describe `up SERVICE` dependency expansion.
- **`compose-deps.star` E2E leg.** DONE 2026-07-28: three legs judged by `status()` counts of real
  workloads, live on docker and kube.

### Docker API proxy restart-policy translation (2, section retired)

- **A plain `docker run` never gets Docker's default `no` policy.** CLOSED, verified stale 2026-07-29:
  the fix is in the tree with tests asserting the EFFECTIVE policy, and the reproducer scenario is a
  normal `SCENARIOS` member.
- **`RestartPolicy.MaximumRetryCount` is dropped.** CLOSED, verified stale 2026-07-29: carried into
  `spec.RestartMaxAttempts` for `on-failure` only, matching dockerd.

### Findings from the incus RemoteCapable work (7)

- **UDP port-forward through a remote companion could not work on dockerhost or containerdhost.** FIXED
  2026-07-28: both used `wire.BridgeDatagramStream` on the COMPANION path, which converts to a real
  packet socket that does not exist there and stripped the framing the companion was about to parse.
  Now `wire.Pipe`. Not verified live — no test exercises remote-mode companions on those backends.
- **`barehost.AgentForwardEnabled` returns a flat `false`.** CLOSED 2026-07-29: DELIBERATE, and now
  documented as such with what the wrong answer cost (the server inferred support from `Remote()` and
  injected an `SSH_AUTH_SOCK` pointing at nothing).
- **`oci.entrypoint` would fix incus's command/entrypoint warning.** CLOSED 2026-07-29, already landed;
  the warning was correctly NARROWED to command-only overrides rather than deleted.
- **Incus registry credentials survive only because HS256 JWTs are escape-free.** DONE 2026-07-28,
  written down where the credential is MINTED, with `TestIssuedTokenIsURLUserinfoSafe` pinning the
  issued token rather than trusting base64url and RFC 3986 to stay in agreement.
- **incus can honor `spec.WorkingDir` and a numeric `spec.User`.** DONE 2026-07-28 via `oci.cwd` /
  `oci.uid` / `oci.gid`, both ends verified in the vendored incus source rather than trusting the
  declaration, and gated on each key's own validator since incusd rejects the whole create otherwise.
- **`pkg/dockerproxy` dropped `RestartPolicy.MaximumRetryCount`.** DONE 2026-07-28: no new spec field
  was needed — only the proxy's `restartPolicy` struct omitted the JSON key.
- **`barehost.AgentForwardEnabled` returning `false` is CORRECT.** Decided 2026-07-28 with the
  structural evidence (bare's `caretakerConfig` mirror has no `agentRelay` field) and no behaviour
  change. Also corrects a mistaken premise: barehost is NOT the reference implementation for the
  always-on companion.

### Findings from the incus field re-audit (8, section retired)

- **incushost silently drops ~20 `api.DeploySpec` fields.** DONE 2026-07-28: `Sysctls` and `Ulimits`
  mapped, `Tmpfs`/`ShmSize` become tmpfs `disk` devices, every remaining field warns individually, and
  `TestBuildInstancesPostWarnsForEverySpecFieldItCannotHonor` sets each field ALONE.
- **The incus `mounts` warning states the wrong reason.** DONE 2026-07-28: server-host binds are now
  MAPPED as `disk` devices, so the warning is gone rather than reworded.
- **incus `volumes` was the closest unmapped field to mappable.** DONE 2026-07-28: custom storage
  volumes with docker-parity GC (anonymous volumes stamped on the instance so Delete reaps exactly
  them).
- **incus `WorkingDir` and numeric `User` mapped.** DONE 2026-07-28, each key traced to its CONSUMER in
  `driver_lxc.go`; `1000:staff` is refused whole rather than honoring the uid and dropping the group.
- **Knative Serving/Kourier E2E install vendored.** DONE 2026-07-28: `e2e/container/knative/` pinned at
  knative-v1.15.0 with `SHA256SUMS`, so the scenario no longer needs network at test time.
- **Assert Dev Container `runArgs` reach the container.** DONE 2026-07-28, passes on kube: four flags
  chosen because they survive the kubernetes translation and are observable from INSIDE the container.
  The cap-DROP is the load-bearing half — MKNOD is in the default set, so a container given everything
  would still pass a bare cap-add check.
- **`docs/guides/compose-devcontainers-docker.md` is wrong in three ways.** DONE 2026-07-29 — all three
  claims were wrong, one by a factor of fifty (`runArgs` recognizes 51 flags, not one). Rewritten in
  all three locales with a caveat the old text lacked: a flag mapping here does not mean every backend
  realizes it.
- **Latent conduit refcount leak.** FIXED 2026-07-28: `ensureConduitLocked` defaulted an empty `Mode`
  before computing the map key while three callers keyed the raw config, so a frontend registered with
  `Mode: ""` was released under a key not in the map. One caller away from being live.

### Observability coverage was never running in CI (4, section retired)

- **Enable the observability family on the CI kube leg.** DONE 2026-07-28 after a full local
  verification pass: eight of ten `observability-*.star` had self-skipped in EVERY containerized leg
  for as long as they existed, reporting green while running nothing.
- **One-shot workloads are invisible to the kubernetes backend's `List`.** DONE 2026-07-29: `List` now
  enumerates Deployments, one-shot Jobs and Knative Services — the same three kinds `Status` and
  `Delete` always handled; that asymmetry is why the gap survived. Results deduplicate by name with
  Deployment winning. The blast radius was wider than observability: anything reading `List` omitted
  one-shot workloads on kube.
- **`observability-export.star`'s gateway leg asserted a store-less server that was not store-less.**
  DONE 2026-07-28: on a store-compiled build an unset `CORNUS_OBS` means ON.
- **`deploy(telemetry="")` was silently a no-op in the harness.** DONE: unpacked as a `starlark.Value`
  so absent and present-and-empty are distinguishable — the endpoint-less block is the whole reason the
  mux is reachable without an external backend, and the scenario had been passing while proving nothing.

### Barehost findings (1)

- **Per-record lock shared by the server and shim.** DONE 2026-07-28 — and the characterization was
  wrong in two ways: the race was neither rare nor shim-specific. `supervisor.onExit` wrote back a
  pre-restart copy (so an explicitly stopped deployment came back), and two writers sharing
  `record.json.tmp` could truncate the record, which `listRecords` silently skips. Fixed with an
  advisory flock on a STABLE sibling path plus a lock -> re-read -> mutate -> publish primitive.
  Mutation-verified with numbers: neutering the lock lost 262/320 in-process increments.

### CI E2E triage + third-party scope mapping (5)

- **OAuth 2.0 Token Exchange, phase 2.** DONE 2026-07-28: `POST /.cornus/v1/auth/exchange` (RFC 8693),
  downscope only. Two rules settled while building it: downscope is checked by endpoint CONTAINMENT
  (there is no ordering over `Access`), and only client-facing scopes are issuable — `caretaker` is
  contained in `api`, so containment alone would let a full-access client mint a sidecar credential.
- **Token exchange, phase 3.** DONE 2026-07-28: client-side exchange with caching, `cornus token
  exchange`, and an `exchange` profile field. The storage is unified — `sshclient`'s session cache is
  now an adapter over `pkg/tokencache`.
- **ja/zh sync for the scope-mapping docs.** DONE 2026-07-29, and bigger than the entry recorded: the
  structural audit was FAILING with 5 errors x 2 locales across 3 files, all whole missing sections,
  plus table-row drift the structural audit cannot see.
- **Scenario-scoped workload reap in the E2E harness.** DONE 2026-07-28. The obvious implementation is
  dangerous and must not be used: on dockerhost `List` enumerates by label across the WHOLE daemon, and
  attempting it destroyed seven of the maintainer's own deployments. The reap is restricted to names
  the harness itself deployed; `TestReapTouchesOnlyWhatTheScenarioCreated` fails if anyone reintroduces
  the sweep.
- **Run `hub-multireplica-credential.star` live.** DONE 2026-07-28 on kube via the containerized runner.

### Docker proxy `Tty` (1, section retired)

- **Every container created through the proxy was non-TTY regardless of `docker run -t`.** FIXED
  2026-07-29: `createRequest.Tty` decodes, `toDeploySpec` carries it, and inspect reports it back —
  that third piece matters because the CLI picks its stream decoder from `Config.Tty`. Measured live
  against host docker 29.2.1; three regression tests, each confirmed to fail under its own targeted
  neutralization.

### Anchor slugify (1, section retired)

- **VitePress emitted Japanese heading ids with DECOMPOSED kana.** FIXED 2026-07-29 at the source:
  `docs/.vitepress/config.mts` appends `.normalize('NFC')` after the upstream accent-stripping. The
  tree had been working around the upstream defect by hand-decomposing 34 fragments across 20 files,
  and the workaround had been written into `AGENTS.md` as a RULE. All 34 recomposed; measured after a
  fresh build: 1,860 heading ids, 0 decomposed; 391 fragment links, 0 dead. Found because the
  maintainer rejected the claim — the original evidence was a confounded test against a stale `dist/`.

### Translation fidelity (4, section retired)

- **`server-env-vars.md` missing `CORNUS_TOKEN_CACHE` from both locales.** DONE 2026-07-29.
- **`deploy-spec.md` locales missing `user` and `workingDir`.** DONE 2026-07-29: both locales carried
  an OLDER intro sentence, so it was re-translated rather than patched.
- **`cookbook/compose-to-kubernetes.md` (ja) drops a `curl` command.** DONE 2026-07-29: a formatting
  gap, not a dropped command.
- **Seven pages carry an inline code span the English does not.** TRIAGED 2026-07-29: two were defects
  in the ENGLISH, six were legitimate CJK sentence splits. Rule recorded in both glossaries — `missing`
  is a strong signal, `extra` is weak but worth checking THE SOURCE first.

### Backend field-coverage guards (1)

- **incushost's `unsupportedFieldCases` had a "keep this in sync" contract and nothing enforcing it.**
  DONE 2026-07-29: `specfield_coverage_linux_test.go` asserts by reflection that every exported
  `api.DeploySpec` field is exercised by an unsupported case or by `supportedSpec()`.

### Findings from the backend field-coverage sweep (6, section retired)

- **containerdhost's managed `/etc/hosts` ignores `spec.Hostname`.** DONE 2026-07-29 by extracting
  `hostrun.InstanceHostname` instead of copying the rule a third time — `ping $(hostname)` worked on
  bare and failed on containerd from the same compose file.
- **Sub-field silent drops in shared `hostrun`.** DONE 2026-07-29: `WarnUnmappableStorageOptions` warns
  six drops. Volume size is the one that matters — asking for `1Gi` and getting an unbounded host
  directory is a different guarantee, not a smaller one. `InstanceMounts` was NOT given a `ctx`; the
  prelude already has a logger.
- **A detached `POST /.cornus/v1/deploy` carrying `spec.Credentials` is refused by nobody.** DONE
  2026-07-29, rejected at the API: every `api.CredentialSource` form is client-side, so there is no
  variant that works without a session. The guard tests `len(Sources) > 0`, not `Credentials != nil`.
- **Compose `ipv4_address` is undocumented.** CLOSED 2026-07-29 — the premise was a case-sensitive
  grep of my own; the field was always documented. The row was improved anyway to name all three
  behaviours, including that containerd/bare give the workload AN address, just not the one asked for.
- **`spec.Replicas` is ignored under Knative with no warning.** DECIDED AND FIXED 2026-07-30: warn
  above `> 1`, which cannot fire on the user the noise concern was about. The silent outcome is not
  "you get 1", it is scale-to-zero, which presents as cold starts rather than as a config error.
- **`api.NetworkAttachment` sub-fields silently ignored by kubernetes.** CLOSED 2026-07-30: kubernetes
  fixed (nine sub-fields warn, `Internal` being the one that mattered — an operator asking for no
  outbound got full outbound), all five backends swept, 140 leaf paths examined, one genuine gap found.
  A recursive coverage gate stays REJECTED for two independent measured reasons: most paths sit under
  wholesale-ignored parents, and many leaves are realized outside the backend package entirely.

### Sync-invariant audit (6, section retired)

- **`knownDeployBackends` vs `defaultBackendFactory`'s switch.** ENFORCED 2026-07-29 via an AST test.
  The two directions are not equally dangerous: a name in the list but not the switch falls through to
  dockerhost, so the operator gets a running server on the wrong backend.
- **`cornus containerd-log-shim` vs `logShimArg`.** ENFORCED 2026-07-29. On drift nothing fails to
  build: containerd execs a subcommand that does not exist and `cornus logs` returns empty for a
  healthy container.
- **`supportedServiceFields` vs the `Service` struct.** Enforced; the comment now names the tests.
- **Already enforced, confirmed:** `pkg/e2e/harness.go` (`TestPredeclaredNamesInSync`) and
  `pkg/compose/usernet.go`.
- **Not applicable, confirmed:** `pkg/server/obstelemetry.go` and `pkg/build/builderctr`.
- **`pkg/e2e/target.go` "Kept in sync by hand".** CLOSED 2026-07-30 by deleting the duplicate: the
  premise that the value was stuck behind a build tag was wrong, so `runcStateRoot` was exported as
  `barehost.RuncStateRoot` and the harness points at it. Cost measured (+1.14 MB on a CI-only binary).
  Prefer a single source of truth over a drift test whenever it is reachable.

### Environment-variable hygiene in `pkg/server` (1)

- **`isHostBackend`'s doc comment omitted `incus`, and nothing forced a new backend to be classified.**
  FIXED 2026-07-30: `TestIsHostBackendClassifiesEveryKnownBackend` enumerates `knownDeployBackends` and
  fails on any name it has no expectation for. The table is written from what each backend can DO, not
  mirrored off the implementation.

### Sync-invariant sweep, second pass (1, section retired)

- **The first sweep grepped one phrase and stopped.** FIXED 2026-07-30 after re-running against
  synonyms: `DefaultTelemetryRelayPort` was declared TWICE, at opposite ends of one wire — pkg/deploy
  points the workload's exporter at the port and pkg/caretaker binds it, so drift raises nothing and
  telemetry silently stops arriving on a feature that is on by default. Duplicate deleted rather than
  guarded. `udpFlowIdle` triaged and declined with reasons; `onlineCPUs` is not a defect.

### Sync-invariant sweep, third pass (1)

- **Structural duplicate detection.** DONE 2026-07-30: an AST tool over `pkg/` and `cmd/` found 21
  groups binding the same name to the same literal in different packages. One real defect —
  `tagLazy9P = 'L'` with the writer in `pkg/wire` and the dispatcher in `pkg/build/buildwire` — fixed
  by exporting `TagLazy9P`, the established pattern for the nine tags already exported. Six groups
  triaged and declined with reasons so a fourth pass need not re-derive them.

### Redis hub store registered an empty forward address (1)

- **A Redis-backed multi-replica deployment without `CORNUS_HUB_FORWARD_URL` advertised an EMPTY
  forward address.** FIXED 2026-07-30: the kube arm used `hubForwardAddr(cfg)` and the Redis arm read
  the variable directly, skipping the `ws://$POD_IP:<port>` fallback. Found by reviewing the session's
  own diff — the refactor put the two arms on adjacent lines. `TestBothHubStoresDeriveTheForwardAddrIdentically`
  asserts over the SOURCE because the buggy arm dials a live Redis and a unit test cannot reach it.

### containerd's client-local mounts were documented as working without their flag (4)

- **The rejection said the backend cannot do it, when one variable turns it on.** FIXED 2026-07-30:
  `clientLocalMountsUnavailable` now distinguishes a `MountingBackend` in local mode (names its
  remote-mode variable) from a backend that is not one at all (incus keeps the original sentence).
- **Three code comments and two published doc pages described a fallback that does not exist.** FIXED
  2026-07-30 — a documented capability that did not exist, corrected in all three locales together
  rather than left as sync debt.
- **The host-mount-fast-path classification existed twice as literals.** FIXED (latent) 2026-07-30: one
  exported `hostcheck.UsesHostMountFastPath`. Divergence had no error path — an unpropagated mount
  presents as an EMPTY directory with every startup check green.
- **The four `CORNUS_*_REMOTE` names were read in one place and needed in another.** FIXED (latent)
  2026-07-30: `remoteModeEnvs` + `remoteModeEnabled` as the single source, with an AST test failing on
  any reintroduced `os.Getenv("CORNUS_*_REMOTE")` literal.

### incus could forward UDP and was refused for it (4)

- **A capability that existed, was documented, and was unreachable.** FIXED 2026-07-30: `incushost`
  implements udp in full and never declared `SupportsUDPPortForward`, so every `port-forward .../udp`
  against incus was refused while the docs said it worked. Nothing failed — not the build, vet, a test,
  or a doc check.
- **The rejection explained a different backend's limitation** (kubernetes' TCP-only portforward).
  FIXED 2026-07-30.
- **`docs/cli/port-forward.md` understated the support in two places.** FIXED in all three locales.
- **Guard added: `TestEveryUDPForwardingBackendDeclaresIt`.** Asserts both directions per backend over
  the source, since three backends are `//go:build linux` and each needs a live daemon to construct.

### Closed-TODO attestation audit — incorrect closures, reopened and now closed (5)

- **Token-exchange refresh after an upstream 401.** The expiry half was real; the 401 half did not
  exist, and the user-visible shape was worse than a plain failure — a token invalidated early was
  served from the cache for up to its full lifetime, so re-running did not help. Implemented as
  `client.WithCredentialRefresher` plus `refreshExchangedToken`, with four guards (at most one retry;
  replay only on a DIFFERENT credential; rewindable bodies only; never on a `WWW-Authenticate:
  CornusSSH` challenge, which is the first leg of the SSH proof handshake). Nine tests.
- **Replace the log-shim name test with a guard that reads both production definitions.** Fixed by
  removing the duplication: the literal moved to `containerdhost.LogShimArg` in that package's only
  build-tag-free file. The old test's docstring defended its own copy — which is exactly what made it
  blind.
- **Re-review and record freshness for the ja/zh `deploy-backends.md` and `server-env-vars.md` pages.**
  Closed together with the entry below, by source review rather than by rubber-stamping the digest —
  which the tool's own help calls the one use that defeats the mechanism.
- **Record translation freshness for the ja/zh `cli/port-forward.md` pages.** The review was made
  decisive rather than impressionistic: for each of the three English pages, reversing exactly this
  session's edits reproduced the digest recorded at the last sync BYTE FOR BYTE, which narrows the
  question from "is this whole page still faithful?" to "was each of those edits translated?". Reusable
  — a digest sidecar looks like it can only say "changed"; reconstructing the pre-edit source turns it
  into a proof of WHAT changed, and it fails loudly if any other change slipped in.
- **Make the UDP capability guard observe the capability VALUE.** Confirmed first by applying the flip
  and watching the AST guard pass. Fixed by `TestUDPForwardCapabilityValues` in an external test
  package, written test-first against the live bug. The two halves are complementary: the AST half
  catches a udp branch with no declaration, the value half catches a declaration that lies.

### Closed-TODO attestation audit — closures that were not regression-attested (23)

Each of these had a correct implementation and a test that would have stayed green with the fix
removed. All are now attested, and nearly every one was neutralized to prove it.

- **`cornus daemon docker` banner print path.** Closed with a SEAM, not a cleverer assertion — the
  print was unobservable, not merely untested. Three assertions beyond "the banner appears", including
  a positive control that the request reached the agent.
- **DiskStore allocation benchmark -> enforceable check.** `TestDiskStoreRMWDoesNotAllocateAChunkPerWrite`
  measures BYTES (8,432 pooled vs 269,654 unpooled against a 65,536 ceiling), because removing the pool
  moves the allocation COUNT by one or two. The first draft measured 199 no-ops and passed identically
  with the pool removed — caught only because the neutralization was run.
- **Reproducible record for the ja/zh editorial review.** Closed as NOT re-establishable: "every page
  was source-checked" is a claim about an activity. Replaced with a regenerating command, both locales
  passing, and nine adjudicated review warnings as a named baseline.
- **The S5 "published and current" claim vs six stale locale pages.** Now enforced rather than
  asserted: 66 pages x 2 locales all current, gated by `docs.yml`. Residual stated — the gate fires
  after the push.
- **`mount-agent` / `mountcheck` stay absent.** A REMOVAL has no natural regression test, so the
  positive control carries the weight.
- **Compose `--server` alias kong parse test.** The equality (all three spellings land in `Cmd.Host`) is
  the contract.
- **`cornus serving` is not printed when the bind fails.** Fully in-process, with a positive control
  that the error IS a bind conflict.
- **Short usage on CLI errors.** A loose line cap (30) plus the flag name — length is the only signal
  separating the two kong configurations.
- **A real `cornus --version` flag test.** The enabler mattered more: `main()` built its parser inline,
  so a test could only assert against a lookalike; the options are now `parserOptions()`.
- **Kube hub heartbeat deadline with a blocking API-server double.** Instructive: the outage tests model
  a server that says NO, and a deadline only matters against one that says NOTHING. Fixed with a REAL
  clientset over an `httptest` server that accepts and never answers.
- **Private-source credential test with a deterministic Docker credential.** Determinism needed HOME,
  not just `DOCKER_CONFIG` — go-containerregistry checks `$HOME/.docker/config.json` first.
- **Tests for `translation_state.py`.** Ten tests, and writing them found a footgun: `--path` takes
  source-relative paths, so the obvious `--path ja/cli/foo.md` matched nothing and exited 0 reporting
  success. Now an error, recording nothing.
- **Preserve the historical 53/57 semantic-warning review.** Closed as NOT RECONSTRUCTIBLE,
  deliberately — no input reproduces those figures, and a plausible reconstruction would read as
  evidence while being a guess. Rule left behind: a review reporting a COUNT must record the command and
  tree state in the same breath.
- **Bounded ingress bridge transport adopted at both callers.** Both compare against
  `BridgeTransport(nil)`'s own values rather than restating numbers; ingressemu needed `newBridgeProxy`
  extracted before the property was observable at all.
- **barehost ingress warning in its regression fixture.** CLOSED AS ALREADY ATTESTED — the audit's claim
  is true and its conclusion is not: the per-field table already fails on the mutation, and
  `warn_once_linux_test.go` only counts duplicates by design.
- **Canonical predicates at all host-backend call sites.** Confirmed and real: every existing test asks
  "does setting X warn?", which a bare-nil check answers equally well. The two differ only where the
  field is PRESENT and silence is correct — including a client-emulated ingress that IS being served.
  Each new negative test is paired with a positive control, because silence is trivially achievable.
- **Translation-audit link comparison tests.** Tested at the function level, not through the exit code:
  these are advisory warnings, so running the tool proves nothing about them.
- **Multiline and reordered inline-code comparison tests.** `Counter` subtraction is order-insensitive
  and multiplicity-sensitive on purpose.
- **UDP over the remote dockerhost/containerdhost companion paths.** Closed IN-PROCESS — the E2E label
  was wrong. The first version was worthless and the neutralization proved it: a UDP ECHO server passes
  with the framing bug reintroduced, because the extra layer is added going out and stripped coming
  back. Rewritten so the server records what it receives.
- **Absent telemetry vs explicit `telemetry=""` in the harness.** Closed IN-PROCESS with no production
  change, via an `httptest` server that records the spec actually put on the wire — a better
  observation point than a spec-builder, since it covers serialization too.
- **Reproducible evidence for the 140-leaf sweep.** The DENOMINATOR is reproducible and was reproduced
  2026-08-01 (`leafwalk` prints the same 140); the per-leaf ADJUDICATION is not, and that is stated
  rather than blurred.
- **Every backend factory arm calls `remoteModeEnabled`.** The arm COUNT equals `len(remoteModeEnvs)` —
  a presence check cannot catch deletion. Both mutations left the old test green.
- **A direct Incus `SupportsUDPPortForward` test.** Closed by the same `TestUDPForwardCapabilityValues`
  subtest.

### Closed-TODO attestation audit — historical live CI/E2E evidence, re-run (18)

Every one of these was re-run against the current tree rather than re-argued. Where a scenario can
self-skip, the run was checked for the ABSENCE of a `skipped` line, not just for a green tick.

- **Auth-enabled internal build-push and deploy-pull.** DONE LIVE 2026-08-01, `auth-build-deploy.star`.
- **`observability-metrics.star` on kube with metrics-server.** DONE LIVE 2026-07-31 — and running it
  uncovered a blocking build defect first. The first attempt failed CORRECTLY: with a non-imbh image
  the scenario `fail()`s rather than skipping.
- **The CI kube observability family with the store linked.** DONE LIVE 2026-08-01, eight scenarios
  green with a real store.
- **`observability-telemetry-mux.star` with a real Collector.** DONE LIVE 2026-08-01, and it genuinely
  RAN — the feature is on by default and had never been exercised end to end.
- **`deploy-mounts-sparse-index.star` on Docker, with the dense-index mutation repeated.** DONE LIVE
  2026-07-31; the mutation was caught with the exact diagnostic the guard exists for.
- **Shared/session-local SOCKS5 coexistence through one agent.** DONE LIVE 2026-08-01.
- **The bridge/ipvlan/macvlan/detached user-network matrix.** DONE LIVE 2026-08-01, all four, no skips.
- **`web --publish-in-conduit` reachability and withdrawal.** DONE LIVE 2026-08-01.
- **The real ingress-controller fetch path.** DONE LIVE 2026-08-01: fetched `edge.preview.example.test`
  THROUGH the controller.
- **The composite auth-by-default path supporting S4.** DONE LIVE 2026-08-01 — and running it exposed a
  stale doc and a DEAD KNOB: nothing anywhere read `E2E_AUTH_INTERNAL`, so the documented reproduce
  command was inert. Caught because the flagged and unflagged runs produced identical assertion lists.
- **Two-replica client-sourced credential forwarding on kube.** DONE LIVE 2026-08-01.
- **Cross-replica credential forwarding with owner-side undeclared-name refusal.** DONE LIVE 2026-08-01.
- **Authenticated in-process BuildKit push through handler and exporter.** DONE LIVE 2026-08-01.
- **Docker API proxy wait with a real nonzero exit code.** DONE LIVE 2026-08-01: `docker wait` printed
  7 and `docker inspect` reported `State.ExitCode` 7 — the finding the 2026-07 audit deferred.
- **Dev Container `runArgs` propagation on kube.** DONE LIVE 2026-08-01, each entry verified INSIDE the
  container rather than in the spec.
- **The full observability family on the CI kube leg.** ALL TEN DONE LIVE 2026-08-01 across three runs.
- **The store-less observability gateway leg.** DONE LIVE 2026-08-01.
- **`hub-multireplica-credential.star` live on kube.** DONE LIVE 2026-08-01.

### imbh CGO_LDFLAGS extraction (1, section retired)

- **`make test-imbh`, a BLOCKING CI job, could not run at all** — and neither could the observability
  E2E image or the license bundle. `imbhgo-fetch` prints `export CGO_LDFLAGS='...'` with SINGLE quotes
  and three sites extracted it with a double-quote-only `sed`, so the variable came out empty and each
  site's own guard fired. Invisible because the two sites that already used `eval` kept working, so the
  product built and shipped fine and only the verification paths broke. Fixed 2026-07-31 by making all
  three `eval` the tool's output. A defect that disables checks while leaving the artifact healthy is
  the worst-shaped one available: everything looks green except the things that would have told you.

## Two design documents folded into ARCHITECTURE.md, and what consolidation had to correct (2026-08-01)

`.agents/docs/AUTH_SCOPE_MAPPING_DESIGN.md` (268 lines) and
`.agents/docs/PLAN_IN_CONTAINER_SERVER_MODE.md` (117 lines) were consolidated into the root
`ARCHITECTURE.md` and then removed at the user's request. Net: +141 / -391 lines across five files.

### What moved where

- **New `### Third-party tokens: scope mapping and token exchange`** under `## Security`: the
  signing-key dividing line with its verifier table, why a JWKS issuer's `scope` claim never grants,
  the ordered-rule-set model and its fail-closed / fatal-on-bad-policy rules, the deliberate break
  from "an unscoped JWT is a full credential", the RFC 8693 endpoint (ceiling -> narrow-only, what it
  buys, `actor_token` and non-client scopes refused), and the client-side cache behaviour.
- **New `### Third-party scope grants — settled`** under `## Design decisions we have closed`: the two
  rejected alternatives, which are the one thing the code cannot show.
- **Amended** the existing scope-allowlist sentence in `### Authentication`, which read as absolute
  for every JWT and is only absolute for tokens cornus or its own operator signed.
- **`### The server in a container`** gained why `incus` is skipped by the path checks and a
  support-boundary bullet that client-local mounts on containerd stay companion-only.
- **Package inventory** gained `pkg/authscope`, `pkg/tokencache`, `pkg/hostenv`, `pkg/hostcheck`.
- Operator-facing configuration was NOT copied: `docs/guides/security.md` already carries the scope-map
  YAML, the matcher list and the exchange request/response shapes in all three locales, so ARCHITECTURE
  states the model and the docs state the surface.

### Findings

**A design document's own status header is the least trustworthy line in it.**
`AUTH_SCOPE_MAPPING_DESIGN.md` said "phases 1 and 2 implemented; phase 3 is a proposal". Phase 3 was
shipped — `cmd/cornus/internal/clientconn/exchange.go`, `cornus token exchange`, the profile's
`--token-exchange` / `--token-exchange-scope`. A status line is written once and never re-derived,
so it decays silently while the prose around it stays true; consolidating from it without checking
would have published "proposed" for a feature with a CLI subcommand and a cache backend.

**Two claims in the plan were wrong against the tree and were corrected rather than copied.**
The design said the exchange TTL was "defaulted and capped exactly as the SSH path does (1h / 24h)".
The code issues a FIXED one hour (`exchangeTokenTTL = time.Hour`) and accepts no client-requested TTL
at all, so the cap it describes does not exist. Separately the plan listed `barehost`/`incushost`
path translation as deferred; `hostcheck` already handles both deliberately (bare via
`sharesMountNamespace`, incus via `handsDataDirToRuntime`), which the 2026-07-31 TODO correction had
established but the plan document never learned.

**The stubs I wrote first cited JOURNAL headings that no longer exist.** Both documents pointed at
dated JOURNAL entries (`2026-07-28 — CI E2E triage, and third-party JWT scope mapping`; `Consolidated
in-container server-mode plan`) that `reconcile-journal-ltm` had since collapsed into
`.agents/docs/LTM/`. Caught by grepping the journal's headings for the titles rather than assuming.
The lesson generalizes past this change: **a cross-reference into JOURNAL.md is a reference to
something designed to be consolidated away.** Point at `LTM/` or at the canonical document; cite a
JOURNAL heading only with the date and an acknowledgement that it may have been folded.

**Deleting a document breaks references that are not in documents.** `pkg/server/auth.go:604` named
`.agents/docs/AUTH_SCOPE_MAPPING_DESIGN.md` in a comment explaining why `logScopeMapped` is Debug
rather than Info. Found by grepping `*.go` and `*.star` as well as `*.md`; repointed at
`tokenexchange.go`, which is the thing the sentence was actually about. Doc-removal sweeps that grep
only markdown leave exactly this class of dangling pointer, and a stale path in a comment is worse
than in a doc because nothing ever renders it and no link checker sees it.

### Verification

Gate run on the one code file touched: `gofmt -l` clean, `go build ./pkg/server/`, `go vet
./pkg/server/`, `go test ./pkg/server/` -> `ok 11.340s`. The change there is comment-only, so the
tests attest that nothing else regressed, not that the comment is right. Cross-references checked by
grep: no `*.md`, `*.go` or `*.star` file names either removed document (the only remaining hits are
in `docs/.vitepress/dist/`, which is build output). Full-width parentheses/colons and decomposed kana
scanned for in every file touched — clean. Every architectural claim written was read out of the
implementation first, not out of the design document: `pkg/authscope/authscope.go` for the matcher
set and the ordered-map semantics, `pkg/server/auth.go:463-547` for the verifier ordering and the
operator-key fall-through, `pkg/server/tokenexchange.go` for the TTL, `issuableAccess` and the
refusals, `cmd/cornus/token.go:67-75` for why `cornus token exchange` deliberately does not read the
cache, and `pkg/hostcheck/hostcheck.go:350-380` for the two backend exclusions.

**Incidental observation, not acted on**: at the end of this session the working tree's changes were
all in the git INDEX (`git status` porcelain column 1), including files modified before the session
started. Nothing here ran `git add`. Noted in case a concurrent agent or a hook is staging, since it
would silently widen anyone's next commit.

## 2026-08-01 — Container streams were all announced as raw-stream (dockerproxy)

Rebuilt after the working tree was destroyed earlier in the session (see the recovery note below);
re-derived from the code rather than replayed from notes, which turned out to be the stronger route.

**The defect.** `pkg/dockerproxy` announced every container stream as
`application/vnd.docker.raw-stream` — `logs` (`containers.go:192`) and both hijacked paths,
`execStart` and `attachContainer`, through a `writeRawStreamHandshake` that had the media type
baked into its response literal. Since Docker API v1.42 a non-TTY stream is
`application/vnd.docker.multiplexed-stream`, and the proxy's non-TTY bodies ARE stdcopy-framed by the
`deploy.Backend` framing contract. A client that decides from the media type rather than
re-inspecting `Config.Tty` therefore skips demultiplexing and prints the 8-byte frame headers as
text. moby's rule, verified against the source and not from memory
(`daemon/server/router/container/container_routes.go`):

```go
contentType := types.MediaTypeRawStream
if !tty && versions.GreaterThanOrEqualTo(version, "1.42") {
    contentType = types.MediaTypeMultiplexedStream
}
```

**The fix** is `pkg/dockerproxy/streamtype.go`: `streamContentType(r, tty)` mirroring that rule, a
numeric `apiVersionAtLeast` (a lexicographic compare reports `"1.5" >= "1.42"` — the trap is real,
Docker minors are long past 9), and the two media types as local constants. `Handler` previously
STRIPPED the `/vX.Y` prefix and discarded it; it now preserves it on the request context, because the
version is not purely routing information once media types are version-gated. `writeRawStreamHandshake`
became `writeStreamHandshake(conn, upgrade, contentType)`.

Deliberately no new import of `github.com/docker/docker/api/types` for the two constants: that module
is already shipped, but the package would pull further modules into the SHIPPED set and so into
`THIRD_PARTY_NOTICES.md`, which is byte-identity-gated in CI — and regenerating it is currently
blocked (below). Two frozen wire strings are not worth a notices drift.

**Two defects found underneath, NOT fixed here** — recorded in TODO.md:

1. `dockerhost.Logs` (`pkg/deploy/dockerhost/dockerhost.go:589`) is a bare `io.Copy` with no TTY
   handling. Docker returns UNFRAMED bytes for a TTY container, so this violates the
   `deploy.Backend.Logs` "MUST write stdcopy-multiplexed frames" contract (`pkg/deploy/deploy.go:238`)
   for exactly that case. Latent until TTY containers became creatable at all — which `tty_test.go`
   documents as a recent fix.
2. `kubernetes.Attach` (`kubernetes.go:2005`) hardcodes `TTY: false` and always applies `muxWriters`,
   on the reasoning that "cornus deployments never allocate a container TTY" — stale since
   `kubernetes.go:2311` now sets `TTY: spec.TTY` on the pod container.

They pull in opposite directions (dockerhost under-frames TTY logs, kube over-frames them), so no
media type is correct on both backends for a TTY container. The proxy follows moby, which is what
clients are written against and what dockerhost actually produces; `streamContentType`'s comment says
so rather than implying the TTY answer is end-to-end guaranteed.

### Verification

Gate: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` all pass.

Four neutralizations, each restored and re-verified clean afterwards:

| broke | failed with |
| --- | --- |
| condition forced to `false` (the original defect) | 11 subtests across logs, attach, matrix, plus the two pre-existing tests |
| lexicographic version compare | `TestAPIVersionAtLeastComparesNumerically` (the `"1.5"` row) |
| dropped the `!tty` half | 6 TTY subtests across logs/attach/exec |
| `Handler` discards the pinned version | the two `/v1.41` subtests |

The fourth attempt at #4 initially failed as a BUILD error (removing the call orphaned the `strings`
import), which per the testing rule proves nothing; redone as `_ = strings.TrimPrefix(...)` so it
compiled and only the behaviour changed. Two pre-existing tests (`TestContainerLogs`,
`TestContainerLogsMidStreamBackendError`) asserted raw-stream for non-TTY unversioned requests and
were updated — their failure on first run is itself the evidence the change is observable end-to-end.

### Note: the working tree was destroyed earlier this session

While neutralizing an `rm -rf` E2E builtin (`remove_all`), the guard was moved BELOW the
`os.RemoveAll` call to prove the guard was load-bearing. The test inputs for that guard were the
dangerous paths it existed to refuse — `/`, `/..`, `/tmp/..`, `.` — so the neutralization turned the
test suite into `os.RemoveAll("/")` running as the user. It deleted both project trees, the Go
toolchain under `~/.local`, `~/.ssh`, `~/.config` and `~/.gitconfig`. The repo was restored from
GitHub at `9a6f5a3`; everything uncommitted was lost.

The rule this yields: **neutralization is only safe for code whose effects are observational.** For
an irreversible destructive operation, removing the guard IS the disaster — there is nothing to
observe afterwards. Test such a guard as a PURE predicate with no destructive call reachable from the
test, and neutralize the predicate instead. If `remove_all` is rebuilt, that is the shape it needs,
plus a real sandbox (refuse anything outside the scenario's own `temp_dir()`) rather than the
malformed-argument check it had.

## 2026-08-01 — Companion caretakers never came back after a host reboot (barehost)

The follow-up `needsRebootRecovery` names in its own doc comment ("Companions are excluded ... their
reboot recovery is a follow-up"), rebuilt after the tree loss.

**The defect.** A reboot wipes `/run` (tmpfs), so an instance's rootfs mount and pinned netns are
gone while the on-disk record survives. `reconcile` rebuilt app instances via `recoverInstance`, but
companions were excluded from that path — correctly, since `recoverInstance` calls `net.Setup` and
would mint a companion a PRIVATE netns when it is supposed to JOIN its app's. Excluded, though, meant
nothing else handled them either: each companion fell through to `launchSupervised` with an unmounted
rootfs and a `config.json` naming a dead pin, failed, and stayed down until the deployment was
recreated.

**Two decisions, and they are separate questions.**

*Which companions may come back at all* is per ROLE, because a reboot destroys the server's client
connections, and two of the three caretakers relay through the client that requested the deployment:
the mount caretaker streams 9P from the caller's filesystem, and the egress caretaker routes back out
through the caller. Resurrecting either yields a caretaker that cannot serve — a mount that hangs
rather than fails. The telemetry caretaker exports outward on its own (`telemetry_linux.go`, and
explicitly "does NOT relay through the cornus server"), so a fresh one is as good as the old one.
`companionRecoverable` therefore recovers otel only, and an UNKNOWN role must opt in deliberately:
the failure modes are asymmetric, since a self-contained companion left stopped merely loses
telemetry while a client-tethered one wrongly resurrected can hang the app's mounts. The
mount+egress folding (`attachments_linux.go:140`) does not complicate this — a folded companion
carries one of those two roles, and both answer false.

Client-tethered companions get `DesiredRunning` CLEARED rather than being retried forever. The app
instance is untouched and keeps running.

*Whether a given companion needs rebuilding* could not use the app-instance signal. A companion
record deliberately leaves `NetNS` EMPTY — it joins the app's namespace and does not own one — so
there is no recorded pin to probe, and a predicate written against `rec.NetNS` would have compiled,
passed, and observed nothing. The persisted truth is the companion's own `config.json`, compared
against the app's CURRENT pin (`bundleNetnsPath`, the read half of the existing `rewriteNetnsPath`).
A plain crash leaves the two equal, which is what makes the new pass safe to run on every reconcile
rather than only after a reboot.

`reconcile` is now two passes — apps, then `reconcileCompanions` — because a companion can only be
repointed once its app holds a fresh pin. `alive` is injected, matching `needsRebootRecovery`'s
existing shape, which is what made the stand-down testable at all.

### Verification

Gate: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` all pass.

Five neutralizations on the predicates, each restored and re-verified:

| broke | failed with |
| --- | --- |
| every role recoverable | the mount, egress, unknown and empty rows |
| probe `comp.NetNS` instead of the bundle | "already names the live pin" + the unreadable-bundle row |
| `appInstanceFor` may match another companion | `TestAppInstanceFor` |
| `appInstanceFor` ignores the replica index | `TestAppInstanceFor` |
| drop the app-netns liveness guard | "app netns not yet live -> defer" |

The second is the one that matters most: it is the plausible wrong implementation, and it fails
exactly where a record-probing predicate would have silently agreed.

`TestReconcileCompanionsStandsDownClientTetheredRoles` covers the loop rather than its predicates,
because clearing `DesiredRunning` is a SIDE EFFECT no pure-predicate test can observe; it asserts
both halves per role — stood down or not, bundle repointed or not.

**Not covered, deliberately**: `recoverCompanion`'s rootfs remount (`prepareRootfs`) needs a real
image store, so the test backend leaves `b.img` nil and skips it. The netns repoint either side of it
IS asserted. A live-runc exercise belongs in the E2E harness, not `go test`.

## 2026-08-01 — THIRD_PARTY_NOTICES.md is now the union of the released platforms

Closes the KNOWN GAP the `licenses` job documented against itself ("once
THIRD_PARTY_NOTICES.md is regenerated for amd64 (or for the union of the released architectures),
set this to amd64 and drop that step").

**Two defects found before any of the intended work, both pre-existing on main.**

1. **The committed notices were STALE.** `go.mod` at `9a6f5a3` pins `imbh-go v0.1.1` and
   `sable v0.0.0-20260726045720-0c6fe56eb099`; the committed file still listed `v0.1.0` and the
   older sable pseudo-version. Nothing else differed — regenerating for linux/arm64 reproduced the
   file byte-for-byte apart from those two lines — so the drift gate should have been RED on main.
   Someone bumped both without regenerating. (This also served as the reproduction proof: an
   otherwise byte-identical regeneration is what licenses the toolchain to be trusted here.)

2. **`mattn/go-localereader` had never passed the policy gate**, and would have FAILED it. It ships
   no license file in any form, so the scanner classifies it `NO-LICENSE-FILE` -> category `review`,
   which the policy scan exits non-zero on. It went unnoticed because it is reachable only on darwin
   and windows (through the bubbletea TUI stack) and the policy scan ran linux-only. Its README.md
   declares `## License` / `MIT` (author Yasuhiro Matsumoto), so it is recorded in
   `KNOWN_MODULE_LICENSES` with that provenance.

   That map's note was HARDCODED to "(verified from source headers; LICENSE at repo root, not in
   submodule)" — true of the one existing entry, false of this one, and this text is reproduced in a
   legal document. The map now holds `(license, note)` pairs so each entry states how it was actually
   verified.

**The change.** `scan_licenses.py` grows `--platforms GOOS/GOARCH,...`, which UNIONS the Go module
sets across platforms (`go list -deps` is platform-sensitive; only reachability varies, since the
build list comes from go.mod). A version disagreement between platforms would mean resolution itself
is unstable, so it warns rather than resolving by last-write-wins. `--platforms` absent keeps the old
ambient-GOOS/GOARCH behaviour.

`ci.yml` replaces `NOTICES_GOARCH`/`POLICY_GOARCH` with one `RELEASE_PLATFORMS` matching release.yml's
binaries matrix, and uses it for BOTH checks — the policy scan too, which is what closes defect 2
rather than merely reporting it. The "Cross-architecture completeness (advisory)" step is deleted:
the union makes it vacuous.

**What the union adds: 13 modules** (352 -> 365), all permissive:
`klauspost/cpuid/v2` and `tonistiigi/go-archvariant` (linux/amd64 only — exactly the two the old
comment named), plus 11 reachable only on darwin/windows (`Microsoft/go-winio`, `Microsoft/hcsshim`,
`danieljoos/wincred`, `ebitengine/purego`, `erikgeiser/coninput`, `go-ole/go-ole`,
`golang/groupcache`, `inconshreveable/mousetrap`, `mattn/go-localereader`, `yusufpapurcu/wmi`,
`go.opencensus.io`). Those were missing from the notice text shipped WITH the darwin and windows
binaries, which is the compliance failure this closes.

### Verification

Ran the `licenses` job's own command sequence locally, end to end: the resolve probe (all five
platforms >= 100 modules), the policy scan (exit 0, no NOT INSTALLED, 0 strong-copyleft, 0 review),
and the drift check (byte-identical). Generation is deterministic — two independent full runs
produced identical bytes. Go gate + `make e2e-check` green.

Not changed, deliberately: the scan does not set `CGO_ENABLED`, while release binaries build with
`CGO_ENABLED=1`. cgo can change file selection, so a cgo-only dependency could still be missed. The
committed file has always had this property; widening it is a separate question from the platform
union, and is now a TODO.

## 2026-08-01 — postcss 8.5.17 -> 8.5.25

Lockfile-only (`npm update postcss` in `web/`); `package.json` untouched, since postcss is reached
transitively through vite. Moves exactly two packages: postcss 8.5.17 -> 8.5.25 and its dependency
nanoid 3.3.15 -> 3.3.16. `npm run build` succeeds, and the notices are unchanged — both are
build-time dependencies, not bundled into the SPA, so they are correctly absent from the runtime npm
inventory.

## 2026-08-01 — dockerhost: `compose up` no longer recreates a workload that has not changed

Closes, for `dockerhost` only, the TODO opened on 2026-07-28 by a CI failure: "Compose `up` recreates
unconditionally". The other four backends are unchanged and the TODO stays open for them.

### The defect, as found

`Backend.apply` in `pkg/deploy/dockerhost/dockerhost.go` called `removeInstances` and then recreated
every container on EVERY call, with no comparison against what was running. Its own comment said so
plainly ("Recreate semantics: remove existing instances first"), and the test that named the property
asserted the opposite of it: `TestApplyIsIdempotent` required `removed == 1` after a second apply of
an identical spec, with the message "want exactly 1 on recreate". So the divergence from
`docker compose up` — which recreates only when a container's configuration or image changed, that
being what `--force-recreate` exists to override — was pinned as the contract under the name of the
property it violates.

This matches the TODO's description. Two things the description did not say, both found by reading:

- **The image is half the problem, and the more dangerous half.** A fix that compares specs alone is
  worse than no fix: `compose up --build` rebuilds the same mutable tag with new bytes, so a
  spec-only comparison answers "unchanged" and silently leaves the previous build running. The
  comparison has to include the image's CONTENT id, which means an inspect after the pull.
- **The self-preservation contract intersects this.** `TestApplyRefusesToRecreateTheServerItself`
  requires a re-apply of a deployment that IS this cornus server to fail with an explanation. Reuse
  would be harmless there (nothing is torn down), but making that refusal conditional on whether the
  spec happened to change is a safety guarantee nobody can reason about, so reuse refuses a live set
  containing this server's own container and the existing refusal is reached unchanged.

### The fix

`pkg/deploy/dockerhost/reuse.go` (new):

- `fingerprintSpec(spec, imageID)` — hex SHA-256 over canonical JSON of `{version, imageID, spec}`.
  It hashes the WHOLE `api.DeploySpec` rather than an enumerated list of fields, deliberately: an
  enumeration is a hand-maintained list parallel to a struct, the exact shape that produced this
  backend's silent-drop defects (see `warnUnsupported`), and the failure mode of over-inclusion — a
  field this backend ignores forcing a needless recreate — is precisely the pre-fix behaviour, so it
  can never be a regression. The spec is hashed AFTER apply's own rewrites (telemetry env merge,
  network priority sort), both deterministic, so two processes agree.
- `reusableInstances(live, replicas, hash, selfID)` — the whole decision as a PURE function: exactly
  `replicas` live app containers, no companion, every one carrying the desired hash, none of them
  this server. Purity is what makes it neutralizable with no container removal reachable from a test.
- `apply` computes the hash after the pull (via a new `engineClient.imageInspect`, which
  `imageExists` now delegates to), takes the fast path only for the plain co-located apply, stamps
  the hash as the `cornus.spec-hash` label on create, and on reuse starts any instance that is not
  running — `up` after `stop` must bring the SAME containers back. If a reused instance will not
  start, it falls through to the full recreate, so the fast path cannot leave a deployment worse off
  than it found it.

`--force-recreate` needs no special case and gets one for free: the CLI stamps a per-process token
into `spec.Labels` (`flagcompat.go`), which is inside the hash.

Also: `e2e/scenarios/compose-watch-reload.star` asserts instance IDENTITY is PRESERVED on the docker
target and keeps the divergence assertion on the others; `docs/cli/compose.md` (+ ja, zh) no longer
claims all container backends recreate on every `up`; and the shared `updateConfig` warning in
`pkg/deploy/kubeonly.go` no longer asserts "recreates on every Apply", which was about to become
false for one of its four callers.

### The fake daemon had to become more faithful

`fakeDocker`'s `GET /images/{ref}/json` 404'd for anything not pre-seeded in `images`. A daemon that
PULLED an image has it, and the fast path inspects the image right after pulling it — so the fake as
written would have silently disabled the fix in every test that does not pre-seed, and those tests
would have gone green while observing the pre-fix behaviour. It now reports a pulled ref as present,
with a content id overridable per ref (`imageIDs`) so a test can model the same tag naming new bytes.

### Neutralization

Nine, each applied and reverted inside a single command with a diff against a backup afterwards
(harness: `.agents-workspace/tmp/neutralize/run.sh`). Every one is non-destructive by construction:
each either makes the backend recreate MORE (which is the pre-fix behaviour, against an in-process
`httptest` fake — this package has no filesystem or process-level effects at all, confirmed by grep
for `os.Remove*`/`exec.Command`/`syscall.`) or recreate LESS, i.e. tear nothing down.

| # | Break | Observed by | Diagnostic |
|---|---|---|---|
| N1 | `reusableInstances` always refuses (= pre-fix) | 58 (sub)tests | "re-applying an unchanged spec removed 1 container(s), want 0" |
| N2 | fingerprint sees only Name+Image | 49 of 51 field rows | "changing CapAdd created 0 and removed 0 app container(s), want 1 created and 1 removed" |
| N3 | image content id dropped from the fingerprint | `TestApplyRecreatesWhenTheImageContentChanged`, `TestFingerprintSpecIsStableAndTotal` | "the image content changed under an unchanged tag and 0 container(s) were removed, want 1" |
| N4 | self-container guard removed | `TestApplyRefusesToRecreateTheServerItself` + 2 unit rows | "re-Apply of a deployment that IS this server must fail" |
| N5 | companion rule removed | `TestApplyDoesNotReuseAcrossACompanion` + 1 unit row | "removed 0 container(s) beside a companion, want the app instance AND the companion" |
| N6 | app replica-count check removed | `TestApplyCreatesReplicas`, `TestApplyStampsOriginLabels`, + 3 unit rows | "created 0 containers, want 2" |
| N7 | unknown-fingerprint guard removed | 2 unit rows | "reusableInstances = true, want false" |
| N8 | the hash label is never stamped | 55 (sub)tests | as N1 |
| N9 | reuse no longer starts an instance that is down | `TestApplyStartsAStoppedInstanceWithoutRecreating`, `TestApplyIsIdempotent` | "started = [], want [id-cornus-web-0]" |

N2 leaves two rows passing, and both are honest: `Image` is in the reduced fingerprint, and
`Replicas` is caught by the container-count check instead. N6 makes `TestApplyStampsOriginLabels`
panic on an empty slice, which aborts the test binary before `TestReusableInstances` runs; its three
rows were confirmed failing with `-run TestReusableInstances`.

### Four defective tests, found by neutralizing — the part worth keeping

N5 initially passed with the companion rule DELETED. The cause was in the code, not the test: the
first version counted companions toward `len(live) != replicas`, so a live set containing one was
already refused by the arithmetic and the companion rule was unreachable — dead code that no test
could distinguish from its own absence. Fixed by counting APP instances only (which also agrees with
`Status`/`List`/`instanceID`, all of which filter companions) and testing `companions > 0`
separately. Both tests then observe it.

Asking the same question of every remaining row of `TestReusableInstances` — *what would a PASS look
like if this claim were false?* — found three more of the same kind, all fixed by changing the
FIXTURE so the branch under test is the only thing that can refuse:

- "desired fingerprint unknown" paired an empty desired hash with a container that HAD a hash, so the
  ordinary hash comparison refused it and the empty-hash guard could be deleted with the row green.
  It now uses a container with no hash label.
- "replicas not yet resolved" paired `replicas == 0` with one live container, so the count check
  refused it. It now uses an empty live set, where `0 == 0` would otherwise report a deployment with
  nothing in it as up to date.
- "a companion is present" had two live containers against `replicas == 1`.

The general lesson, which is not new here but is sharper: **a negative row proves nothing unless the
guard it names is the ONLY thing standing between it and a pass.** In a function that is a
conjunction of refusals, the natural fixture usually trips two or three of them at once, and every
row then certifies the first refusal in source order.

The same reasoning shaped `TestApplyRecreatesForEveryChangedSpecField` (51 subtests, one per exported
`api.DeploySpec` field, built by reflection from `supportedSpec` + `unsupportedFieldCases` so a new
field cannot be silently missing). Each row asserts BOTH directions against the same deployment:
re-applying the unchanged spec must touch nothing, AND changing that one field must recreate. Either
alone is worthless — a (2)-only table is green against a backend that recreates unconditionally,
which is the very bug it would have been written for, and a (1)-only test is green against a
fingerprint that ignores everything.

### Found, deliberately not fixed

- **The other four backends still recreate unconditionally.** Per-backend work; TODO updated to `[~]`.
- **The attachment-shape gate in `apply` is not independently testable.** `extraMountsFor == nil &&
  planFor == nil && !b.remote` is redundant today, because every attachment shape also produces a
  companion and the companion rule already refuses. It is kept as defence in depth against a future
  attachment that produces none, and is stated as such in the code — but no test can distinguish it
  from its absence, and I did not invent one that would only appear to.
- **Reuse is never taken for a deployment that has any companion**, so a remote-mode, egress, mounted
  or telemetry-bearing service still recreates on every `up`. Extending the fingerprint to cover a
  companion's plan is a real piece of work and was out of scope here.
- **`spec.Origin` is inside the fingerprint.** Two clients deploying the same service from different
  directories (or one before and after a `git commit`) will now recreate each other's containers.
  That is correct — origin lineage IS part of the applied spec and is stamped onto the container as
  labels — but it is a behaviour worth knowing about.

### Verification

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all green (full suite, in a tree
carrying other agents' uncommitted work); `make e2e-check` parses every scenario. The E2E suite
itself was NOT run — it needs a live daemon — so the `compose-watch-reload.star` change is verified
only as far as parsing and its target predicate.

One process note worth recording: `go` on this host resolves through a mise shim that silently fell
back to a snap stub partway through the session, so a batch of neutralizations "passed" while running
no tests at all. Anything that reports success by SAYING NOTHING deserves the same suspicion as a
green test — the harness now fails loudly unless `go version` prints a Go version, and every result
above was produced with the toolchain path pinned explicitly.

## 2026-08-01 — What deleting an enrolled SSH key does to an already-minted session (E2E)

Rebuilt after the tree loss. The prior design was gone, so this was re-derived by MEASURING against a
live server with the E2E harness rather than reasoning about the code — which is what turned up both
findings below.

**The gap.** `auth-ssh-key.star` ends by proving a deleted key cannot mint a NEW session (`cornus
auth token` is refused). It never asks what happens to the session the client ALREADY holds — and an
operator deleting a key is usually trying to cut off access now. A probe answered it: after
`auth delete-key`, `cornus auth keys` STILL SUCCEEDS on the cached session (it returns the now-empty
key list, so the deletion did take effect server-side; the request was simply still authorized).
Revocation gates MINTING; issued sessions run to their TTL.

That is defensible — sessions are short-lived by design — but it was unasserted anywhere, so
`e2e/scenarios/auth-ssh-key-session-cache.star` now pins it in both directions. The cache wipe is
what makes the warm-cache half mean anything: without it, "the command still worked" is equally
consistent with a server that never enforced the deletion at all. The wipe forces the same command
down the minting path, where it must now be refused.

**Found while writing it: `CORNUS_AGENT_DIR` does not isolate the token cache.**
`pkg/tokencache/tokencache.go` `runtimeDir()` checks `XDG_RUNTIME_DIR` FIRST and only then
`CORNUS_AGENT_DIR`, so on any host with a login session (where `XDG_RUNTIME_DIR` is always set) the
agent-dir override never applies. The function's own doc comment promises the opposite — "CORNUS_AGENT_DIR
is honoured for the same reason it is there, so an isolated agent gets an isolated cache" — which is
false in the common case.

Consequence beyond the comment: the committed `auth-ssh-key.star` sets neither variable, so running
the E2E suite writes real session tokens into the developer's own `$XDG_RUNTIME_DIR/cornus/tokens`.
Confirmed by timestamp on this host. The new scenario therefore redirects `XDG_RUNTIME_DIR` rather
than `CORNUS_AGENT_DIR`, and says why inline. Both issues are in TODO.md; neither is fixed here,
because changing the precedence affects the agent isolation contract and deserves its own change.

### Verification

RUN LIVE against the docker target (`make e2e-one TARGET=docker`), not merely parsed. Two
neutralizations, both restored and re-verified:

| broke | failed with |
| --- | --- |
| removed the cache wipe from the scenario | `the session cache was not actually wiped` — the wipe's own precondition guard, which names the broken step precisely |
| made the server-side `sshKeys.delete` a no-op | `the deleted key is still listed: the deletion did not take effect server-side` |

The first is the important one: it proves the wipe is load-bearing rather than decoration, which is
the whole basis of the warm/cold distinction.

`make e2e-check` parses all scenarios; the new one is in the Makefile `SCENARIOS` list.

**Scope note**: one scenario, not the two the lost work reportedly had. The second one's subject did
not survive, and it is written from a gap that was MEASURED here rather than invented to match a
remembered count.

## 2026-08-01 — `remove_all` E2E builtin: a sandbox, not an argument check

Replaces `sh(cmd = "rm -rf ...")` in scenarios. This is the second attempt: the first destroyed a
developer's home directory and both project trees earlier the same day, so the design question was
not "how do I validate the argument" but "how do I make a validation defect survivable".

**Why the first design was wrong.** It guarded against MALFORMED ARGUMENTS — refusing `""`, `/`, `.`,
and bare relative names. Under that model the guard is the only thing between the harness and the
whole filesystem, so a guard defect is unbounded. Worse, the repo's own testing rule requires
neutralizing a fix to prove the tests observe it, and neutralizing THAT guard — while its tests fed
it exactly `/`, `/..`, `/tmp/..` and `.`, the paths it existed to refuse — is literally
`os.RemoveAll("/")` running as the user. The discipline and the design were incompatible, and the
design was the part that was wrong.

**The replacement is a sandbox.** A path is removable only if it resolves at or under a directory the
harness itself minted via `temp_dir()`. Roots are recognized STRUCTURALLY: a root must be absolute
and its base name must carry `tempDirPrefix`, the `os.MkdirTemp` pattern only `bTempDir` produces. So
the worst case of a defect anywhere in this code is the loss of a scenario's own scratch directory
under TMPDIR. The blast radius, not the guard's correctness, is what makes it safe — and that is the
transferable lesson.

Symlinks are resolved before the containment test (on the deepest existing ancestor, so removing a
missing path stays a no-op). A scenario can create a link inside its own temp dir pointing anywhere,
and a lexical prefix check would accept it happily. A link whose target is outside is refused even
though removing it would only unlink the link — uniform resolution, bias toward refusal.

`bRemoveAll` deletes the path `resolveRemovable` RETURNS, never the caller's raw string, so a later
bypass in argument handling cannot route around the check.

### Verification

`resolveRemovable` is pure and deletes nothing, which is what makes it neutralizable at all. Five
neutralizations, each restored and re-verified:

| broke | failed with |
| --- | --- |
| no containment check (accept everything) | 5 rows of the refusal table (`/`, `/..`, `/tmp/..`, `/etc`, `/etc/passwd`) |
| lexical prefix instead of symlink resolution | `TestResolveRemovableRefusesSymlinkEscape` |
| `sandboxRoots` drops the prefix marker | `TestSandboxRootsRejectsRootsTheHarnessDidNotMint` |
| refuse everything | all 5 rows of the acceptance table |
| allow `..` escape | the same 5 refusal rows |

Running those is safe BY CONSTRUCTION, and the reason is worth stating: the only test that actually
deletes (`TestRemoveAllBuiltinDeletesOnlyInsideItsTempDirs`) drives the builtin exclusively with
paths under `t.TempDir()`, so even a totally broken guard could lose nothing but that test's own
scratch space. `bRemoveAll` itself is NEVER neutralized, and `removeall_test.go` says so at the top.

Every refusal row supplies a VALID root, so the refusal is attributable to the PATH rather than to
the absence of roots — otherwise each row would pass on the `errNoRoots` branch and certify nothing.

Two scenarios converted and RUN LIVE on the docker target: `auth-ssh-key-session-cache.star` and
`compose-down-volumes.star`. `deploy-reboot-survival.star` keeps `sh(cmd = "rm -rf /run/cornus/*")`
and must: it simulates a host reboot by wiping tmpfs state that is deliberately outside any scenario
temp dir. The builtin is not a universal replacement, and TESTING.md says which case is which.

## 2026-08-01 — the kube E2E leg: an identity assertion that could not pass

`e2e/scenarios/compose-watch-reload.star` failed the CI kube leg (run 30687211223, and 30678513400
and 30421398539 before it — every kube run since the assertion was written):

```
assert_true failed: web kept its instance across the reload on the kube target —
if that backend's Apply has become idempotent too, move it to the equality branch above
```

### What was actually wrong

Not the backend. The scenario asserted `web_after["instances"][0]["id"] != web_id` on every
non-docker target, and on kubernetes that comparison has no content: `statusOf`
(`pkg/deploy/kubernetes/kubernetes.go`) SYNTHESIZES each instance id as `"<deployment>-<i>"` from
the Deployment's replica counters — it never reads a pod. The id is the same string whether the pod
was replaced or not, so the assertion cannot pass however the backend behaves.

The CI logs say this out loud, in the line right before the failure, and it is visible without a
cluster: the kube leg logs `web running as e2ewatch-web` where the docker leg logs
`web running as 5cc4e56b2bff`. One is a Deployment name, the other a container id.

The message the assertion carried ("if that backend's Apply has become idempotent too, move it to
the equality branch") pointed at the right file for the wrong reason — the divergence it pinned was
never observable through that id.

### And the premise was false too

`docs/cli/compose.md` (+ ja, zh) already documented the opposite: "dockerhost and kubernetes leave
an unchanged workload alone", with the mechanism — `--force-recreate`'s per-process token label
lands in the pod template's annotations, so the Deployment rolls a fresh ReplicaSet the way
`kubectl rollout restart` does. Kubernetes gets idempotence from being declarative, not from a
fingerprint: `applyDeployment` UPDATEs the Deployment in place, and an update whose pod template is
unchanged does not roll the ReplicaSet. Only the scenario and `.agents/docs/TODO.md` still claimed
kubernetes recreates unconditionally.

MEASURED (containerized runner, `make e2e-container E2E_TARGETS=kube
E2E_SCENARIOS=e2e/scenarios/compose-watch-reload.star`): the pod behind the unchanged service
survived the reload — `e2ewatch-web-7f6795bdbc-j8rtt` before and through a 12s settle window after.

### The fix

The kube branch reads pod NAMES out of the cluster (`kubectl -n cornus-e2e get pods -l
cornus.app=e2ewatch-web -o jsonpath=...`, the same per-backend concrete-probe shape
`compose-down-volumes.star` uses) and asserts they are unchanged, re-sampling over a settle window —
"nothing happened" is only falsifiable if you look for long enough to have seen it happen. A pod
name carries its ReplicaSet's template hash and a random suffix, so both a rolled template and a
plain delete/recreate mint a new one. The docker branch is untouched; the `else` branch keeps the
divergence assertion for the backends that still recreate unconditionally, now with a note that it
only means anything where the status id names the container itself.

### Neutralization

`kubectl delete pod <the pod>` inserted at the top of the kube branch (in a copy of the scenario
bind-mounted over the image's, so nothing in the tree was touched), forcing the real replacement the
check exists to catch. It failed with exactly the intended diagnostic:

```
assert_eq failed: got ["e2ewatch-web-7f6795bdbc-5ptkt"], want ["e2ewatch-web-7f6795bdbc-gn4vw"]:
the reload replaced the pod of the unchanged service 'web' — a kubernetes Apply whose pod template
did not change must not roll the Deployment
```

Both legs then re-run clean end to end: kube passed, and the docker leg passed unchanged
(`same instance a413df2e0068`).

### Left open, deliberately

The IMAGE-CONTENT half of idempotence is unverified on kubernetes and is now a named item in
TODO.md. A built compose service is deployed under the mutable tag `<registry>/<resource>:latest`
(`composecli/build.go`), so a rebuild that changes the bytes leaves the pod template byte-identical —
and by the very reasoning that makes kube idempotent, that means no rollout and the old image still
running. That is precisely the failure mode dockerhost's fingerprint hashes the image CONTENT id to
avoid. No E2E covers "rebuild the same tag, then redeploy" on kube; this is a hypothesis to probe,
not a finding.

### The transferable part

Before asserting on an id across two samples, ask what the id is DERIVED from. An id synthesized
from the desired state (a name, a replica index) is constant by construction, so both `==` and `!=`
against it certify nothing about the live object — one is vacuous, the other impossible. Backends
here differ: dockerhost returns the container id, incushost the instance name, kubernetes a
name+index string. A cross-backend scenario that reads "identity" uniformly off `status()` is
asserting different things per leg without saying so.

### Changed, and how to re-run it

| file | change |
| --- | --- |
| `e2e/scenarios/compose-watch-reload.star` | `web_pods()` kubectl probe + `NS`; sample `pods_before` on kube after the initial up; the three-branch identity assertion (docker id / kube pod names / else divergence) |
| `.agents/docs/TODO.md` | "Compose `up` recreates unconditionally": `kubernetes` moved out of the open list with the measurement, the identity-observable warning added, and the image-content half named as the remaining kube question |
| `.agents/docs/JOURNAL.md` | this entry |

No Go changed. Gate run: `make e2e-check` (clean), plus both live legs:

```sh
make e2e-container E2E_TARGETS=kube   E2E_SCENARIOS=e2e/scenarios/compose-watch-reload.star
make e2e-container E2E_TARGETS=docker E2E_SCENARIOS=e2e/scenarios/compose-watch-reload.star
```

Each is ~4 minutes on this host (kind cluster create/destroy dominates the kube one). The kube leg
of the full CI suite is 40+ minutes, so a single-scenario container run is the right granularity for
iterating on one scenario — QUALITY_GATE.md section 3 documents `E2E_SCENARIOS` for exactly this.

### Iterating on a scenario without rebuilding the image

The runner image bakes `e2e/scenarios/` at `/work/e2e/scenarios` (`e2e/container/Dockerfile`), and
because the build stage does `COPY . .`, editing any scenario invalidates the Go build layer too —
a rebuild for a one-line scenario edit. Bind-mounting a scratch copy over it skips the rebuild
entirely, which is what made the neutralization cheap AND kept it out of the tree:

```sh
cp -a e2e/scenarios .agents-workspace/tmp/neutralize-scenarios
# edit the copy
docker run --rm --privileged -e E2E_TARGETS=kube \
  -e E2E_SCENARIOS="e2e/scenarios/compose-watch-reload.star" \
  -v $PWD/.agents-workspace/tmp/neutralize-scenarios:/work/e2e/scenarios:ro \
  cornus-e2e:latest
```

Read-only is safe: scenarios write their fixtures to `temp_dir()`, never beside themselves.

## 2026-08-01 — release: the windows/amd64 leg failed on `tee /dev/stderr`, not on a missing store

The v0.0.0 release run ([30690746748](https://github.com/moriyoshi/cornus/actions/runs/30690746748))
built every leg fine but aborted the windows/amd64 one with:

```
tee: /dev/stderr: No such file or directory
ERROR: cornus-windows-amd64.exe does not carry the observability store
```

The message was a lie. `.github/scripts/build-release-binary.sh` verified the shipped feature set as

```sh
"${EXE}" version --features --output json | tee /dev/stderr | grep -q '"obsstore":"yes"' || { ...error... }
```

`tee /dev/stderr` was there only to echo the report into the build log. git-bash on
`windows-latest` cannot open `/dev/stderr` — GNU tee still copies stdin to stdout, so `grep` DID
match, but tee exits non-zero and the script's `set -o pipefail` turns a healthy pipeline into a
failure. The `||` branch then blamed the binary. Linux and macOS have a working `/dev/stderr`, which
is why four legs passed and only Windows saw it, and why the build itself (the expensive part) was
never at fault.

### Fix

Run the report once, echo it with `printf ... >&2`, and match with `case`/glob instead of a pipeline
— no pipe, no pipefail exposure, and the log still shows the JSON.

The assertion moved into its own `.github/scripts/verify-release-binary.sh <binary-path>` (also
carrying the relative-`OUT` -> runnable-path derivation). The point is testability: verifying the
assertion in place would have meant a multi-minute native cgo build per case, so nothing tested it.

### Reproducing "no /dev/stderr" on a POSIX host

`cmd/cornus/release_verify_test.go` (`//go:build unix`) drives the script against stub binaries with
**stderr wired to a `socketpair`**. `/dev/stderr` resolves through `/proc/self/fd/2`, and reopening
that fails with `ENXIO` when fd 2 is a socket — the same shape as the git-bash environment. Two
simulations that look equivalent do NOT work: redirecting stderr to a *deleted* file still reopens
fine (the `/proc/self/fd` magic symlink reaches the unlinked inode), and bash's own `exec 3>/dev/stderr`
is special-cased inside bash to a plain `dup`, so it succeeds regardless. Only a non-reopenable fd
type reproduces it. `requireBrokenDevStderr` probes with the real mechanism (`printf x | tee /dev/stderr`)
and *skips* rather than passing if a future host does not reproduce it, so the test cannot go vacuous.

Neutralization: restoring the `tee` pipeline makes
`TestVerifyReleaseBinaryAcceptsAllInOneBuild` fail with the runner's exact message
(`tee: /dev/stderr: No such device or address` / `does not carry the observability store`), and the
`collector_stubbed` case mis-reports as an obsstore failure — the second check was never reached in
CI either. The reject cases keep the check from degrading into a rubber stamp: a mistyped build tag
still compiles and silently selects the no-op store, which is the only thing the script exists to
catch.

| file | change |
| --- | --- |
| `.github/scripts/verify-release-binary.sh` | new; feature-set assertion, no pipeline |
| `.github/scripts/build-release-binary.sh` | delegates to it via `bash "$(dirname "$0")/..."` |
| `cmd/cornus/release_verify_test.go` | new; socket-stderr regression + reject cases |

Gate: `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` all pass. The release is
re-runnable as-is — no rebuild of the passing legs is needed, but the tag run has to be re-triggered.

## 2026-08-01 — Web UI: sidebar replaced by a fixed page header

The `cornus web` SPA's 200px left rail is gone. `App.tsx` now renders a
`header.appbar` holding the brand lockup and a `nav.appbar-nav` with the same
`NAV` array (which still feeds the command palette's "Go to" group), and
`#root` flipped from a row to a column flex.

The one non-obvious decision: the header is `position: sticky; top: 0`, not
`position: fixed`. `main` is `flex: 1` inside `#root { min-height: 100vh }`, and
the full-bleed workspaces (`Terminal.tsx`, `Files.tsx`) are
`position: absolute; inset: 0` against it — so `main` must have a *definite*
height of `100vh - --header-h`. A `fixed` header leaves the flow, `main` reverts
to the full `100vh`, and both workspaces overflow the viewport by the header's
height. Sticky pins to the viewport identically while keeping its flex row. The
rule is commented in `styles.css` and the constraint is written into
`DESIGN_SYSTEM.md` so the next person does not "simplify" it to `fixed`.

New token `--header-h: 52px` under a `Layout` group. Under the 720px breakpoint
the wordmark hides (`.brand-name { display: none }`) and the nav scrolls
horizontally with the scrollbar suppressed; the old breakpoint's
`#root { flex-direction: column }` moved to the base rule.

Verification (Playwright headless against `npm run dev:mock`, both themes,
1440x900 and 560x800): header box stays at `y=0` after `scrollTo(0, 1200)`;
`.workspace` and `main` both measure exactly `y=52, h=648` in a 700px viewport;
`nav.sidebar` count is 0. **Neutralized** by injecting
`header.appbar { position: static !important }` — the header then measures
`y=-1200`, so the probe observes the pinning rather than merely finding the
element. `npm run build` (tsc + vite) and `npm test` (104 tests) pass;
`go build ./...`, `go vet ./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

Two traps hit along the way, both already documented and both worth re-reading
before the next web change: `vite build`'s `emptyOutDir` deletes the tracked
`pkg/webui/dist/.gitkeep` (restore it after every build), and running `npx
prettier` over these files reformats them to 80 columns — the repo is at 100 and
carries no prettier config, so the reformatting was reverted by hand.

| file | change |
| --- | --- |
| `web/src/App.tsx` | `nav.sidebar` -> `header.appbar` + nested `nav.appbar-nav`; `NAV` comment retargeted |
| `web/src/styles.css` | "Layout: sidebar" layer replaced by "Layout: page header"; `--header-h` token; `#root` row -> column; 720px breakpoint rewritten |
| `.agents/docs/DESIGN_SYSTEM.md` | class vocabulary, token table, brand-asset and file-map entries updated; the sticky-not-fixed constraint recorded |

### Findings for the next agent

**`web-screenshot`'s "no install step" claim is stale.** `.agents/skills/web-screenshot/SKILL.md`
says "Browsers are already cached on this machine; the driver is provisioned on demand by `npx`, so
there is no install step". The driver part holds, but `~/.cache/ms-playwright` was empty —
`shot.mjs` failed with Playwright's "Executable doesn't exist" banner and needed
`npx --yes playwright install chromium` (111 MB, ~1 min) first. Budget for that download the first
time on a fresh host, and treat the skill's sentence as unverified rather than a guarantee. The
skill file itself is *not* corrected yet — a one-line fix, left undone because it was outside this
change's scope.

**Ad-hoc Playwright scripts need `shot.mjs`'s resolution shim.** Under
`npx --yes -p playwright node <script>.mjs`, a plain `import { chromium } from "playwright"` throws
`ERR_MODULE_NOT_FOUND` — the driver lives in the npx temp install, not next to the script. Copy the
`createRequire` + PATH-scan block from `.agents/skills/web-screenshot/scripts/shot.mjs` (lines
36-52) into any throwaway script, or the whole probe dies before it measures anything.

**Layout probes must be neutralized, not just run.** `boundingBox()` on a header returns
`{y: 0}` whether the element is correctly pinned or simply not scrolled yet, so the passing
measurement alone proved nothing. Injecting `position: static !important` and re-measuring
(`y: -1200`) is what made it evidence. Same shape as the `warn_once_linux_test.go` lesson in
CLAUDE.md: the probe has to be able to fail for the reason you care about.

## 2026-08-01 — Files workspace: the Terminal's pane-split binds carried over

`prefix %` (split left/right) and `prefix "` (split top/bottom) now work on the
Files screen, same keys as the Terminal workspace. `Files.tsx` gained
`splitFocused(dir)`, a one-liner over the existing `splitAt(state.focused, dir,
false)` — the edge-overlay split reads a side from which edge you hovered, and a
keyboard split has no side to read, so it takes `splitAt`'s own `before = false`
default and lands the new pane after the focused one.

The commands live in a `splitCommands()` helper spliced into every return path of
`fileCommands()`, including the two that previously returned early: the `!a`
path (no pane actions registered) and the `a.kind === "edit"` path. Splitting is
a property of the *tile*, not of the pane's contents, so it must survive both —
it is available at the virtual root where no file action applies.

**`c` and `x` deliberately did NOT carry over.** They already mean Copy and
Delete on this screen. This is the whole reason the change is "the split binds"
and not "the Terminal's pane binds": `%` and `"` were unclaimed here, `c` and `x`
were not. The Terminal's new-pane and close-pane binds have no Files equivalent;
tab ✕ and the edge overlays cover those gestures with the mouse.

### Finding: a missing GROUP_ORDER entry silently sank the Files palette section

`CommandPalette.tsx`'s `GROUP_ORDER` was `["Terminal", "Go to", "Settings"]`.
`groupRank()` returns `GROUP_ORDER.length` for anything unlisted, so the "Files"
group ranked *last* — the Files screen's own contextual commands rendered below
"Go to" and "Settings", the exact opposite of the comment three lines above
("Contextual groups lead the always-present global ones so the current screen's
actions surface first"). Registering with `prepend = true` does not help: the
palette stable-sorts by group rank, which overrides registration order across
groups. Fixed by adding "Files", and the comment now states the requirement so
the next screen that grows commands does not repeat it. This was pre-existing,
not introduced here; it surfaced because the new split commands landed in that
mis-ranked section.

### Finding: identity assertions cannot catch a duplicate bind

The first version of the "c and x stay on the file actions" test asserted
`allCommands().find(c => c.bind === "c")?.id === "files:copy"`. Neutralizing it
by making `files:split-h` claim `c` **did not fail the test** — `handlePrefixKey`
also takes the *first* match, and `splitCommands()` is appended last, so copy
still won and the assertion still held. The test could not fail for the mistake
it was written to catch.

The real invariant is *uniqueness*: a duplicate bind throws no error, it silently
makes the loser unreachable. The test now asserts
`new Set(binds).size === binds.length` across the screen's whole command set, and
the same neutralization fails it (`expected 7 to be 8`). Worth generalizing —
any registry with first-match-wins dispatch needs a uniqueness assertion, because
every identity assertion over it passes vacuously for the winner.

Neutralization of the main feature: removing `splitCommands()` from
`fileCommands()`'s return paths makes both split tests fail with
`expected 'browser' to be 'swallow'` — the key falls through to the browser when
nothing binds it. Reverting `GROUP_ORDER` fails the ordering test with `files`
sorted to the end. All three are behavioral failures, not compile errors; the
tests drive `handlePrefixKey` with a synthetic prefix + key rather than calling
the command objects, so they assert the *bind* rather than the command's mere
existence in the registry.

| file | change |
| --- | --- |
| `web/src/views/Files.tsx` | `splitFocused`; `splitCommands()` on every `fileCommands()` return path; toolbar hint mentions the binds |
| `web/src/views/terminal/CommandPalette.tsx` | `"Files"` added to `GROUP_ORDER`; comment states the requirement |
| `web/src/views/views.test.tsx` | `pressBind` helper; 3 tests (both binds, root-with-no-file-actions, bind uniqueness) |
| `web/src/views/terminal/CommandPalette.test.tsx` | contextual-groups-outrank-global ordering test |

Gate: `npm run build` (tsc + vite) and `npm test` (108 tests) pass; `go build
./...`, `go vet ./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass. `vite build`
ate `pkg/webui/dist/.gitkeep` again — restored.

## 2026-08-01 — Files and Terminal lost their in-screen titles and hints

Both workspaces rendered a `.workspace-toolbar` holding a `.workspace-brand`
title ("Files" / "Terminal") and a `.workspace-hint` line of muted gesture
documentation. Both are gone, along with the three CSS rules and the now-dead
`gap: var(--space-3)` on `.workspace` (a flex column with one remaining child has
nothing to gap against). The screens are named by the header nav's active pill,
which landed earlier the same day, so the title was duplicated chrome.

The panes now start immediately under the header, which is the point: these are
full-bleed tiling workspaces and the toolbar was eating a row of vertical space
on every screen for text you read once.

### Consequence worth knowing: Files' actions are now undiscoverable

The Files hint was not decorative. That screen has **no on-screen controls** for
new folder, upload, rename, copy, download or delete — they exist only as
contextual palette commands, and the hint was the only thing that said so
("File actions live in the command palette (prefix key, then a command)"). Its
removal was explicitly requested, so it was removed; nothing replaced it. A user
who has not read the docs now has no in-app path to those actions. Tracked in
TODO.md. The Terminal hint was closer to pure decoration — its gestures (hover an
edge, drag a tab, drag a divider) are discoverable by trying them, and its panes
carry their own visible controls.

Verification: `views.test.tsx`'s Terminal mount test now asserts
`document.querySelector(".workspace-toolbar")` is null. Neutralized by putting
the toolbar back — it fails with `expected <div class="workspace-toolbar row">…
to be null`, so it observes the absence rather than passing vacuously. Two
Terminal tests had used `findByText("Terminal")` purely as a mount gate; that
text no longer exists in the view, so they now await the first pane's combobox.
Screenshots at 1280x760 dark confirm both workspaces start directly below the
header.

| file | change |
| --- | --- |
| `web/src/views/Files.tsx`, `web/src/views/Terminal.tsx` | `.workspace-toolbar` block deleted |
| `web/src/styles.css` | `.workspace-toolbar` / `.workspace-brand` / `.workspace-hint` rules and `.workspace`'s dead `gap` removed |
| `web/src/views/views.test.tsx` | toolbar-absent assertion; two mount gates re-pointed off the deleted title |

Gate: `npm run build` and `npm test` (108) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass. `.gitkeep` restored after the
vite build, again.

## 2026-08-01 — Files: multi-selection, cursor-navigable

The Files explorer selected exactly one row. It now holds a set, extended by
`shift+click` and `shift+Arrow`, toggled by `ctrl/cmd+click`, and filled by
`ctrl/cmd+A`; every batch-capable file action runs over the whole selection.

### The selection model: a set plus two cursors

`FilePane` keeps `sel` (a `Set` of names), `anchor` (where the current range
started) and `lead` (the row under the cursor, which is also the row holding DOM
focus). A shift-extend **recomputes** the whole `anchor..lead` range instead of
unioning into the set, which is what makes shift+ArrowUp *shrink* a range you
just grew — an accumulating implementation grows in both directions and can never
give a row back. `selection()` derives the ordered name list by filtering the
current listing, so a refetch after a delete or a rename cannot leave an action
pointed at a row that is gone.

### The non-obvious part: focus and selection had been the same thing

Rows are navigated by real DOM focus (`focusRow` → the row's `<a>` → `onFocus`
selects it), which is why arrow keys worked before without any selection state of
their own. That coupling is exactly wrong for a range: a shift-extend has to move
the cursor *without* letting `onFocus` collapse the selection onto the new row.
The fix is a one-shot `focusKeepsSelection` flag consumed by the next `onFocus`
— `focus()` dispatches synchronously, so the flag is read and reset within the
same call. Any future code that moves the cursor must decide which of the two it
wants; `focusRow(name, keep)` is the whole vocabulary.

The same modifier logic had to be repeated on the row's name link, since that
link is the open-this-file affordance and stops propagation. `selectFromClick`
returns whether the click was modified, and the link opens the file only when it
was not — otherwise shift-clicking across a folder would open an editor tab per
file dragged over.

### Actions now target the selection, and say so

`rename` stays single-row (it asks for ONE new name) and the palette withdraws it
while several rows are selected. `copy` splits: one row still prompts for a full
destination PATH (that is also how you copy-and-rename in one step), several
prompt for a destination FOLDER. `delete` and `download` fan out. Batch requests
run through `eachSelected`, which keeps going past a failure so one undeletable
file cannot strand the rest, and `report` always refetches — after a partial
failure the listing has changed for the rows that did succeed.

The palette titles state the true target (`Delete "compose.yaml"` vs `Delete 2
items`) because `prefix x` fires with no further confirmation of *what* it hits.

### Reactivity finding: `paneActions` is a plain Map

The selection-count label lives in the pane sub-header, which is rendered by the
tiling module from `Files.tsx`'s `subHeader` factory — outside the pane's own DOM.
Reading `paneActions.get(id)` there is not reactive, and the pane registers its
actions *after* the sub-header first renders, so the naive version renders once,
sees no pane, and never updates. `registerActions` now bumps an `actionsRev`
signal that `selectionOf` reads first; the pane's own `sel()`/`entries()` signals
take over from there. Anything else rendered out of that Map needs the same
treatment.

### The count went through three placements

It started as a status line under the listing, then (by request) a floating pill
over the pane's bottom-right corner, and finally embedded in the breadcrumb lane,
right-aligned, with a gradient shading the crumb trail out beneath it. The last
placement is the one that survives contact with a long path: `.stack-subheader
.crumbs` deliberately keeps its *tail* visible and fades the left, so an opaque
overlay pinned right would have covered the current folder. It is an overlay
rather than a flex item so the crumbs keep the full lane when nothing is
selected and nothing reflows as the selection changes.

### Verification

Nine new tests in `views.test.tsx`, all reading the highlight back out of the DOM
rather than restating component state. Each was neutralized and observed to fail
behaviorally:

| neutralization | failure |
| --- | --- |
| `extendTo` unions instead of recomputing | `expected [ 'reports', 'web', '.env', …(1) ] to deeply equal [ 'reports', 'web' ]` |
| name link ignores modifiers | `expected …(2) to have a length of 1 but got 2` (the file opened) |
| drop the `ctrl/cmd` branch of `selectFromClick` | `expected [ 'README.md' ] to deeply equal [ 'many-files', 'README.md' ]` |
| disable the ctrl+A handler | `expected [] to deeply equal [ Array(6) ]` |
| drop `actionsRev()` from `selectionOf` | `expected undefined to be '4 selected'` |
| label only `sel[0]` | `expected 'Delete "compose.yaml"' to be 'Delete 2 items'` |
| `remove` takes `names.slice(0, 1)` | `expected <span class="fs-name"></span> to be null` (the second file survived) |
| `download` takes `selection().slice(0, 1)` | `expected [ Array(1) ] to have a length of 2 but got 1` |

The destructive test deletes inside `project/many-files` on purpose: the mock
filesystem is module state shared across the whole test file, so a test that eats
a fixture its neighbours assert on would pass alone and fail in suite order.

Playwright shots (dark + light, 1280x720) confirm the range highlight and the
lane-embedded count; a 260px-wide capture confirms the shade fading a clipped
breadcrumb out under it.

| file | change |
| --- | --- |
| `web/src/views/files/FilePane.tsx` | set/anchor/lead selection, `selectFromClick`, `focusKeepsSelection`, shift+arrow and ctrl+A, batch `copy`/`remove`/`download`, `eachSelected` + `report`; `BrowseActions.selected` → `selection` |
| `web/src/views/Files.tsx` | `actionsRev` + `selectionOf`, selection-aware palette titles, rename gated to one row, count label in `subHeader` |
| `web/src/styles.css` | `.stack-subheader` is now a positioning context; `.fs-selection-count` overlay + gradient |
| `web/src/views/views.test.tsx` | nine multi-selection tests + `rowOf`/`selectedNames` helpers |

Gate: `npm run build` and `npm test` (117) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass. `.gitkeep` restored after the
vite build.

### Known limit, filed in TODO.md

`FsCopy` in the BFF reads the source as a bounded file, so it cannot copy a
directory and refuses anything over `maxEditableFileSize`. That predates this
change, but selecting twenty rows makes it far easier to hit; the batch reports
`copied N/M — <first error>` rather than failing silently.

## 2026-08-01: CI flake — `TestStorageWarningsAreDeterministic` diffed the clock

CI run 30691849303 (job "Observability store (imbh)") failed with
`warnmounts_linux_test.go:94: warning output varies between runs`, where the two
sides of the diff differed only by `time=…558Z` vs `…559Z`.

The production code was not at fault. `sortedUnique` in
`pkg/deploy/internal/hostrun/warnmounts_linux.go` was doing its job; the test's
own capture helper was.

`storageWarnings` built its logger with `slog.NewTextHandler(&buf, nil)`, so the
captured line carried the record timestamp. The test then calls that helper six
times and demands byte-identical output. The timestamp is precisely the field
that is SUPPOSED to differ between two runs, so the assertion had a built-in
race against the millisecond boundary: it passes when six calls land in one
millisecond, fails when they straddle two. Wall-clock luck, not a code change —
which is why it had been green.

Fix: `storageWarnings` now passes a `slog.HandlerOptions.ReplaceAttr` that drops
`slog.TimeKey` at the top level. Everything the test exists to pin — attribute
ordering and the sorted volume list — is unaffected.

### Neutralization

The assertion still catches what it is for. Replacing `sortedUnique`'s
insertion-ordered dedup + `sort.Strings` with a range over the `seen` map (the
real non-determinism this guards against) fails both arms:

| arm | failure |
| --- | --- |
| sorted list | `volume list is not sorted: … volumes="zeta, alpha"` |
| cross-run stability | `warning output varies between runs: … volumes="zeta, alpha"` / `… volumes="alpha, zeta"` |

The second diff is now purely the attribute, with no timestamp noise to read
past. The break was reverted afterwards.

### Same pattern elsewhere

Checked: 16 test files construct a `slog.NewTextHandler`, but no other test
compares captured log output across runs (the other `!= first` comparisons are
over counters, tokens and buffer caps). This was the only instance.

Gate: `gofmt -l`, `go build ./...`, `go vet ./...`, `go test ./...` all clean;
`go test -count=20` on the three warnmounts tests passes. One file changed:
`pkg/deploy/internal/hostrun/warnmounts_linux_test.go`.

## 2026-08-01 — Files: drag-through selection, and a listing that is not text

Two follow-ons to the multi-selection work: the browse pane's text is no longer
selectable, and pressing a row and dragging now selects the run you cross.

`.file-pane` gets `user-select: none`. The listing is a selection surface of its
own — a drag across it means ROWS — so a competing text range painted over the
same gesture is noise. Scoped deliberately to the browse pane: an editor pane's
text IS the content, and CodeMirror drives its cursor from the DOM selection, so
`user-select: none` there would break editing outright. The cost is real and
intended: a filename in the listing can no longer be selected and copied as text.

Drag-select is `beginDragSelect` / `dragSelectOver` / `endDragSelect` in
`FilePane`. The press picks the anchor (plain collapses onto the row, shift keeps
the anchor you had, ctrl/cmd starts no drag because it is a toggle), each row the
pointer enters becomes the lead, and the mouseup listener is on `window` — a
release over the pane chrome, another pane, or off-window entirely still has to
end the drag, or the next stray pointer move keeps painting.

### Finding: two probes in a row that could not have failed

The verification here is browser-only (jsdom has no layout, no native selection),
and the first two attempts at it were worthless in the exact way the testing rule
warns about.

1. **Sampled after mouseup.** The probe dragged, released, then read
   `window.getSelection().toString()`. It read `""` — but so does the neutralized
   build, because the click dispatched on release runs `focusRow`, and focusing an
   element collapses the document selection. The measurement destroyed what it was
   measuring.
2. **Dragged from the file name.** Sampling mid-drag instead still read `""` under
   the neutralization, because the drag started on the row's `<a>`: Chromium treats
   a press on a link as the start of a link drag and never builds a text range
   there. The probe was aimed at the one spot in the row where the rule it was
   testing has nothing to do.

Only a drag that starts on a PLAIN cell and is sampled mid-drag can fail. It does:
with `user-select: auto` it comes back with the whole crossed listing as text
(`"2024-01-01 00:00:00\n📁reports\t—\tdir\t…"`), and with the rule restored it is
`""`. The editor control keeps reporting `"# Shop"` in both, which is what shows
the rule did not spill past the browse pane.

### Two browser behaviors this leans on

- **An anchor is natively draggable**, so a drag-select that starts on a filename
  would become a link drag. The row links carry `draggable={false}`.
- **`click` is dispatched on the nearest common ancestor** of the mousedown and
  mouseup targets, so a drag that crosses rows never reaches a row's onClick and
  cannot collapse the range it just built. That is why there is no "was this a
  drag?" suppression flag — the browser already answers it. Verified rather than
  assumed: after release the three dragged rows are still selected, and the tab
  count is still 1, so the press that began on `README.md`'s link opened nothing.

| file | change |
| --- | --- |
| `web/src/styles.css` | `.file-pane { user-select: none }` |
| `web/src/views/files/FilePane.tsx` | `beginDragSelect`/`dragSelectOver`/`endDragSelect` + window mouseup, row `onMouseDown`/`onMouseEnter`, `draggable={false}` on the row link; the old modified-click `preventDefault` is gone (the CSS covers it) |
| `web/src/views/views.test.tsx` | drag-through selection test (press, cross rows, drag back, release, then confirm further moves do nothing) |

The jsdom test neutralizes properly on its own: dropping the `onMouseEnter`
handler gives `expected [ 'many-files' ] to deeply equal [ 'many-files',
'reports', 'web' ]`.

Gate: `npm run build` and `npm test` (118) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

## 2026-08-01 — Bump imbh-go to v0.3.0

`go get github.com/moriyoshi/imbh-go@v0.3.0`, plus the version pins that must
move with it. The Go module version alone is not the bump: the prebuilt Rust
archive `libimbhgo.a` and the Go bindings share one C ABI, so every place that
names a version has to name the same one.

| file | pin |
| --- | --- |
| `go.mod` | `github.com/moriyoshi/imbh-go v0.3.0` |
| `Makefile` | `IMBH_VERSION ?= v0.3.0` |
| `Dockerfile` | `ARG IMBH_VERSION=v0.3.0` |
| `e2e/container/Dockerfile` | `ARG IMBH_VERSION=v0.3.0` |
| `.github/scripts/build-release-binary.sh` | `IMBH_VERSION="${IMBH_VERSION:-v0.3.0}"` |
| `docs/{,ja/,zh/}guides/observability.md` | `imbhgo-fetch@v0.3.0` |
| `THIRD_PARTY_NOTICES.md` | version column `v0.3.0` (license unchanged, Apache-2.0) |

### What deliberately did NOT change

- **`.github/workflows/ci.yml:77`** names v0.1.1, but as history — "it *was*
  advisory while upstream imbh-go v0.1.1 had only ever been exercised on
  linux/arm64". Rewriting that to v0.3.0 would falsify the record of why the
  job became blocking. A version string in a past-tense clause is not a pin.
- **The release matrix and its no-windows/arm64 comments.** Checked rather than
  assumed: `gh release view v0.3.0 --repo moriyoshi/imbh-go` publishes the same
  seven cells as before (darwin amd64/arm64, linux amd64/arm64 both glibc and
  musl, windows amd64). Still no windows/arm64, so five binary targets remains
  correct.
- **`NOTICE`** — its imbh-go stanza is version-agnostic.
- **`go.mod`'s `toolchain go1.26.4` pin.** It is pinned by sable reached
  *through* imbh-go, so a bump could have dragged it. It did not: imbh-go's own
  `go.mod` hash is byte-identical across v0.1.1 and v0.3.0
  (`VgOkL9PNs/bStovJLDo77/8/gx9WGljRC2wqxuTNAEg=`), so no transitive
  requirement moved and `go.sum` gained only the two v0.3.0 lines.

### Upstream changes, and why neither reaches us

v0.2.0 surfaced the flush scheduler as `DbOptions.Flush` (additive, optional).
v0.3.0 "gate[s] service.name group-by". The second one is the only one worth a
second look, and `pkg/obsstore` uses `service.name` as an **exact-match filter**
(`obsstore.go:114`), never as a group-by key — so the gating has nothing to act
on here. Confirmed by execution, not by reading: `store_imbh_test.go` drives
`service.name` through the real Rust store and passes.

### Fixed in passing

`.github/scripts/build-release-binary.sh:20` documented `default: v0.1.0` while
the actual default two dozen lines below was v0.1.1 — already stale before this
change. Now both say v0.3.0.

Gate: `go build ./...`, `go vet ./...`, `go test ./...` all pass (stub path).
The real store is the one that actually exercises the new archive, so
`make test-imbh` was the load-bearing run — `-race` across `pkg/obsstore`,
`pkg/server`, `pkg/deploy/...`, `pkg/api`, `cmd/cornus/...`, all green.

## 2026-08-01 — Files: rubber-band selection, and modified clicks stop activating

Two more selection follow-ons. Dragging from empty space in a listing now sweeps
a rubber band that selects the rows it crosses, and a click held with a selection
modifier no longer fires the row's call to action.

### The band

`beginBand` / `moveBand` / `endBand` in `FilePane`, hung off `.fs-list`'s
`mousedown` and the same window-level `mouseup`/`mousemove` pair the row
drag-select uses. A press whose target is inside a `<tr>` is ignored — that
gesture belongs to the row drag — so only empty space bands.

Three decisions worth keeping:

- **Coordinates are the list's content space** (client offset + scroll). That is
  the same space `offsetTop` reports once `.fs-list` is the rows' offsetParent
  (`position: relative`, added for exactly this), and the space an absolutely
  positioned child of a scroll container lives in. One space, no conversions, and
  the band stays put over the rows when the list is scrolled.
- **The rectangle follows both axes; the hit test is vertical only.** Rows span
  the full width, so horizontal overlap is always true and testing it would be
  theatre. The drawn rectangle still tracks x because that is the affordance
  people expect from a rubber band.
- **`applyBand` assigns the whole set** rather than adding to it, so sweeping back
  releases rows again. Membership is re-derived from geometry every frame; that is
  the same "recompute, do not accumulate" rule the shift-extend follows.

A bare press on empty space falls out of this for free: a zero-area band matches
no rows, so clicking the background deselects. Holding shift/ctrl keeps the
existing selection as the band's base instead.

### Modified clicks no longer activate

Shift-clicking twice in the same place while building a range also lands a
`dblclick`, and the row's `onDblClick` was unconditional — so extending a range
could descend into a folder out from under you. `isSelectGesture` now guards both
row double-click handlers (the entries and the `..` row). The name link already
made the same check through `selectFromClick`; an unmodified double click still
activates, which the test pins so the guard cannot quietly swallow everything.

### Verification: the split between jsdom and the browser

jsdom reports `offsetTop`/`offsetHeight` as 0 for every element, so it cannot see
what the band covers. The tests are split accordingly, and each half was
neutralized:

| check | where | neutralization → failure |
| --- | --- | --- |
| empty space bands, rows do not | jsdom | drop the `closest("tr")` guard → `expected <div class="fs-marquee" …> to be null` (and the drag-select test breaks too, which is the gestures colliding) |
| a bare press on empty space deselects | jsdom | drop `clearSelection()` → `expected [ 'compose.yaml' ] to deeply equal []` |
| a modified double click does not activate | jsdom | unconditional `onDblClick` → `expected …(2) to have a length of 1 but got 2` (an editor tab opened) |
| the band selects the rows it crosses | browser | `rowsInBand` returns `false` → mid-sweep rows `[]` instead of `[ 'web', '.env', 'compose.yaml', 'README.md' ]` |

The browser probe also confirms the band is gone after release, the selection it
built survives the release, and sweeping back to the start empties it again.

| file | change |
| --- | --- |
| `web/src/views/files/FilePane.tsx` | band state + handlers, `rowEls` refs, `.fs-marquee` element, `isSelectGesture` guard on both dblclick paths |
| `web/src/styles.css` | `.fs-list { position: relative }`, `.fs-marquee` |
| `web/src/views/views.test.tsx` | band lifecycle/ownership test, modified-dblclick test |

Known limit: the band does not auto-scroll when the pointer leaves the list, so a
sweep is bounded by what is on screen. `ctrl/cmd+A` and shift+Arrow cover the
long-listing case.

Gate: `npm run build` and `npm test` (120) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

## 2026-08-01 — Files: the band draws nothing and starts anywhere blank

Two corrections to the band landed earlier today. The rectangle is gone — the row
highlight is the feedback — and a sweep can now begin on ANY part of the pane
that is not a control, including the blank stretch of the breadcrumb lane.

### Catching the press on `document`, not in JSX

The lane is drawn by the tiling chrome (`.stack-subheader`), a sibling ABOVE the
pane body, so no handler inside `FilePane` can see a press there. `beginBand` is
now a `document` mousedown listener, and `bandSurface` matches the press back to
this pane structurally:

- same `.stack` element as the pane's own list (otherwise it is another tile),
- the pane is the tile's ACTIVE one (background tabs stay mounted, and must not
  band from a lane they are not showing),
- not inside `.stack-tabs` — that bar is the drag handle for the whole stack, not
  blank space,
- not inside `tr, a, button, input, select, textarea, [role=button]` — a row owns
  its press (the row drag-select), and so does every control.

So "blank" is defined by exclusion, which is the honest way round: a new control
dropped into the pane chrome is automatically not a band surface as long as it is
a button or a link, and a new non-control decoration automatically is one.

A press in the lane sits ABOVE the list, so its content-space y is negative and
the sweep simply enters range as it moves down. No special case needed.

### The band now moves workspace focus

Pressing the pane body already focused the pane through the tiling chrome's
`onPointerDown`, but the lane is outside it — a sweep started there would have
built a selection in a pane the command palette was not pointed at, which is a
trap: `prefix x` would delete from a different pane than the one showing the
count. `FilePane` takes a `focus` prop (`setFocus(pane.id)` in `Files.tsx`) and
calls it when a band begins.

### Finding: jsdom has no layout, but it can be lent some

The first version of this test could only assert that a `.fs-marquee` div came
and went, because the hit test reads `offsetTop`/`offsetHeight`, which jsdom
reports as 0 for everything. With the rectangle removed there was nothing left to
observe — the honest fix was not to weaken the test but to give jsdom the missing
layout: `stubRowGeometry` defines a synthetic 20px row stack with
`Object.defineProperty`. The code under test is the hit test; jsdom's absent
layout was never the subject. The browser probe still runs the same sweep against
real boxes, so the stub cannot quietly diverge from reality.

Five neutralizations, each failing behaviorally:

| neutralization | failure |
| --- | --- |
| drop `tr` from the exclusion list | `expected [ 'web', '.env', 'compose.yaml', …(1) ] to deeply equal [ 'web' ]` |
| drop the control selectors | `expected [ 'many-files', 'reports' ] to deeply equal [ 'compose.yaml' ]` (the refresh button banded) |
| match `.fs-list` instead of `.stack` | `expected [] to deeply equal [ 'many-files', 'reports' ]` (the lane stopped working) |
| `applyBand` accumulates | `expected [ 'many-files', 'reports', 'web' ] to deeply equal [ 'many-files', 'reports' ]` |
| `endBand` does not clear `banding` | `expected [ Array(6) ] to deeply equal [ 'many-files', 'reports' ]` |

Browser probe against real layout: a sweep up from below the last row selects
`web, .env, compose.yaml, README.md`; sweeping back to the start empties it; a
sweep from the lane's blank stretch selects `many-files, reports, web`; pressing
the lane's refresh button changes nothing; and `.fs-marquee` count is 0
throughout, which is the "draws nothing" claim.

| file | change |
| --- | --- |
| `web/src/views/files/FilePane.tsx` | band state reduced to a flag + start y + base set, `bandSurface`, document-level `beginBand`, `focus` prop; marquee element removed |
| `web/src/views/Files.tsx` | passes `focus={() => setFocus(pane.id)}` |
| `web/src/styles.css` | `.fs-marquee` removed; `.fs-list { position: relative }` kept (it is the rows' offsetParent, which the hit test depends on) |
| `web/src/views/views.test.tsx` | `stubRowGeometry`; blank-space sweep test and lane/controls test replace the marquee-lifecycle test |

Gate: `npm run build` and `npm test` (121) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

### Follow-up: the column header bands, the tab bar does not

"What about the header?" is two different headers, and they land on opposite
sides of the rule.

The listing's COLUMN header (`thead`) is blank space that happens to hold labels
— nothing sorts on click — so it now bands like the rest. The exclusion is
`tbody tr` rather than `tr`, which is exactly where that line gets drawn: if the
columns ever become sortable they become controls and drop back out. Neutralized
by restoring the blanket `tr`: `expected [ 'web' ] to deeply equal [ 'many-files',
'reports' ]`.

The stack's TAB BAR stays excluded, and not as an oversight: `.stack-tabs` is
`draggable`, and dragging its empty area moves or re-tiles the whole stack (see
`views/tiling/panes.tsx`). It looks blank but it is a grab handle, so a band
started there would fight a gesture that already exists. The `..` row is likewise
excluded — it carries go-up on double click.

The app header (`header.appbar`) is outside the pane's `.stack` entirely, so
`bandSurface` rejects it structurally without needing a rule.

## 2026-08-01 — Files: arrow keys when the pane is focused but the DOM is not

Reported: with the file-list pane focused and no native focus inside it, the
arrow keys did nothing.

The listing's key handler hangs off `.fs-list`, so it only ever fired when DOM
focus was on a row (the roving tabindex) or, for an empty folder, on the list
itself. But WORKSPACE focus and DOM focus are two different things here, and
several ordinary actions leave them apart:

- clicking the tile's tab — it is a plain `<div role="tab">` with no tabindex, so
  it takes no focus at all,
- pressing blank space in the listing,
- sweeping a band from the breadcrumb lane (added earlier today, which is what
  made the gap easy to hit).

In every one of those the pane is highlighted as the focused tile, the palette
targets it, its selection count shows — and the arrows are dead. Verified in
Chromium rather than assumed: after clicking the tab, `document.activeElement` is
`BODY`.

`onGlobalKey` is a `document` keydown listener that hands the navigation keys to
the same `onListKey`, whose first move calls `focusRow` and so puts real focus
back on a row; the row handler owns everything after that. Two guards keep it from
overreaching:

- **`props.focused()`** — a new prop (`state.focused === pane.id` from
  `Files.tsx`). The listener is document-wide and every mounted pane installs one,
  so without the gate every open tile would step at once.
- **the target's ancestry** — anything inside a `.fs-list` already routes its own
  keys (including ANOTHER tile's, which must not be driven from here), and any
  focusable element owns what it does with Enter. Both drop out.

`FilePane` now takes both halves of pane focus: `focus()` to claim it (added with
the band) and `focused()` to read it.

Neutralizations:

| neutralization | failure |
| --- | --- |
| drop the document listener | `expected [] to deeply equal [ 'many-files' ]` |
| drop the `props.focused()` gate | `expected <tr class="fs-selected">…(4)</tr>` — with two tiles open, both listings moved |

Browser probe: click the tab, `activeElement` is BODY, ArrowDown selects
`many-files`, a further Arrow + Shift+Arrow gives `reports, web`, and focus is
inside the list from the first press onward.

| file | change |
| --- | --- |
| `web/src/views/files/FilePane.tsx` | `navKey` + `onGlobalKey` document fallback, `focused` prop |
| `web/src/views/Files.tsx` | passes `focused={() => state.focused === pane.id}` |
| `web/src/views/views.test.tsx` | fallback test, including the two-tile gate |

Gate: `npm run build` and `npm test` (122) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

## 2026-08-01 — Tiling: DOM focus drives tile focus; ".." became a control

### Tile focus follows DOM focus

Reported as a desync: moving DOM focus from one pane to another left the tile
focus behind. `.stack-body`'s `onPointerDown` was the only thing that set it, so
focus arriving any other way — Tab into a row, a link or button in the chrome, a
pane that focuses itself on mount — left the keyboard in one tile and every
contextual command pointed at another.

`StackView` now carries `onFocusIn={() => props.ctx.setFocus(activeId())}`.
`focusin` because it bubbles (`focus` does not), and on the whole `.stack` so the
tab bar and sub-header count as that tile too. This lives in the tiling module,
so the Terminal workspace gets it as well — including the case where several
terminals mount on load and the last one to call `term.focus()` used to take the
keyboard while the ring stayed on the persisted pane.

Neutralized by making the handler a no-op: `expected 1 to be +0`. Browser probe:
after a split the right tile is focused, focusing a row in the left tile moves
both, and one Tab moves both back.

### ".." is now clickable and focusable

It was a bare `<td>` with a double-click handler on the row: no link, no focus
stop, and the arrow cursor skips it (it is not an entry). It now carries a real
link — one click goes up, Tab reaches it (its own stop, ahead of the rows' roving
one, like the refresh button), and the browser turns Enter on it into that click.
It stays out of the selection and out of the arrow cursor, which walk entries
only; Backspace is still the from-anywhere way up.

That last part needed a guard: with focus on the link, an Enter keydown bubbles to
`.fs-list`, so `onListKey` would ALSO fire and open whatever the row cursor sat
on. `onListKey` now returns early when the event came from inside `.fs-updir`.

### Finding: two more checks that could not fail, caught by neutralizing

Both were in this session's own new tests, and both looked fine:

1. **The Enter-guard assertion** checked `.modal-overlay` was absent and the tab
   count was 1. Neutralizing the guard did not fail it. `ModalHost` is mounted in
   `App.tsx`, which these view tests do not render, so `.modal-overlay` can never
   exist here; and the keyboard open *awaits* a placement prompt, so with nobody
   answering it, no tab appears either way. The fix was to answer the prompt —
   `choosePlacement("stack")` plus a macrotask — which turns "a prompt was opened"
   into a second tab. It then fails with `expected …(2) to have a length of 1 but
   got 2`.
2. **The browser Tab check** pressed Tab after clicking blank space. Focus was on
   `<body>`, so Tab walked into the page header and never reached the listing —
   it reported "not on `..`" for a reason that had nothing to do with `..`. Fixed
   by starting from the listing's roving row and pressing Shift+Tab, which lands
   on `..` because it precedes the rows in DOM order. Enter from there then walks
   `All / project` up to `All`.

A neutralization that does not fail is not a nuisance to work around — it is the
check telling you it was never watching. Both of these would have shipped as
green evidence for behavior nobody had tested.

| file | change |
| --- | --- |
| `web/src/views/tiling/panes.tsx` | `onFocusIn` on `.stack` |
| `web/src/views/files/FilePane.tsx` | ".." link markup, `onListKey` Enter guard for `.fs-updir` |
| `web/src/views/views.test.tsx` | tile-focus-follows-DOM-focus test, ".." control test |

Gate: `npm run build` and `npm test` (124) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

## 2026-08-01 — Files: drag and drop

Three drops, chosen with the user: rows onto another pane or folder, OS files onto
a listing (upload), and a row out to the desktop (download). The in-app drop
COPIES by default and moves on shift — the user's call, inverting the desktop
convention so the destructive option is the one you ask for.

### What the BFF can actually do, and how the UI says so

This shaped the whole feature, so it is worth stating plainly:

| transfer | endpoint | works? |
| --- | --- | --- |
| move within one mount, any kind, any size | `FsRename` | yes |
| copy a file ≤10MB, anywhere | `FsCopy` | yes |
| move a file across mounts | copy + delete | yes |
| **a folder across mounts** | — | **no** |
| a file over 10MB | — | no (413) |

`FsRename` refuses cross-root explicitly ("cannot rename across roots (copy
instead)") and `FsCopy` reads its source as one bounded file, so a folder simply
cannot leave its mount. The drag payload therefore carries each item's KIND, not
just its path: the receiving pane cannot stat what it is handed, and the kind is
what decides whether the job is possible. Without it the user got
`moved 0/1 — project/reports: Error: POST /fs/copy?source=virtual&path=project%2Freports: 400 cannot copy`;
with it, "a folder can only be moved within its own mount (shift-drag), not
copied". Same refusal, one of them says why.

### The name is the drag handle, and that is a real trade-off

Rows cannot be `draggable`: the browser starts a drag on press+move and no
mousemove ever arrives, which would silently kill the press-and-sweep selection
added earlier today. So the drag handle is the file NAME, and sweeping still works
from anywhere else in the row. Dragging carries the SELECTION, not the pressed
row.

That made two changes to the press semantics necessary:

- **A press on an already-selected row no longer collapses the selection**, or a
  multi-row drag would be impossible (pressing one of the three would leave one).
  A click that turns out not to be a drag still collapses, because the click
  handler runs on release and a completed drag fires no click — which is exactly
  how Finder behaves.
- **A press on the handle must not start a sweep.** Chromium found this: a
  two-tile drag reported `moved 1/2` for an item nobody dragged. The pointer's
  trip to the drop target was entering rows, and each `mouseenter` extended the
  selection the drag was carrying. `dragSelecting` is now false when the press
  lands on the name.

### Verification

Ten neutralizations against the jsdom suite, each failing behaviorally: shift
ignored (`expected <span class="fs-name"></span> to be null`), no self-drop guard,
the root accepting drops, the drag ignoring the selection (`expected { paths: [
'project/README.md' ] }…`), DownloadURL unguarded, OS files not uploaded, file
rows becoming drop targets, the handle sweeping (`expected [ Array(6) ]…`), a
press always collapsing, and the folder guard removed.

The gesture itself is browser-only — jsdom has no DragEvent and no drag. Playwright
across two tiles: a plain drag copies `README.md` into `assets` and leaves it in
`project`; a shift drag moves `compose.yaml` (gone from the left tile, present in
the right, status `moved 1 item`); a folder refused across mounts with the source
kept; a folder moved within its mount and confirmed inside the destination.

### Finding: jsdom drops modifier keys from drag events

`fireEvent.drop(el, { dataTransfer, shiftKey: true })` delivers the dataTransfer
and NOT the shiftKey — jsdom implements no `DragEvent`, so testing-library falls
back to a plain `Event`. Copy-vs-move hangs entirely on `shiftKey`, so the move
test was quietly exercising a copy and asserting the file had moved; it failed for
the right reason but by luck. The fix is to build the event and define the
property (`createEvent.drop` + `Object.defineProperty`), which is what makes the
two paths distinguishable at all in jsdom.

Also worth remembering: the mock filesystem is module state shared by every test
in the file, so all three mutating drop tests work inside `project/many-files`,
the scratch listing nothing else asserts on. And the standalone mock server takes
`CORNUS_WEB_MOCK_PORT` — the browser probes run against their own instance on
5081 rather than mutating the dev session already running on 5080.

| file | change |
| --- | --- |
| `web/src/views/files/FilePane.tsx` | `DropItem` payload, `onRowDragStart`, `acceptsDrop`/`onDragOver`/`onDrop`, `moveOrCopy`, drop-target highlight, press semantics for the drag handle, `refreshAll` prop |
| `web/src/views/Files.tsx` | `refreshAllPanes` (a transfer changes two folders, usually in different tiles) |
| `web/src/styles.css` | `.fs-drop-here` |
| `web/src/views/views.test.tsx` | nine drag-and-drop tests + a `DataTransfer` stand-in and `dropOn` helper |

Gate: `npm run build` and `npm test` (132) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

### Follow-up: the drag image is ours, on a transparent canvas

The default drag feedback is a snapshot of the source element, so dragging a
selected row carried a slab of its fill around with the pointer. `onRowDragStart`
now builds its own ghost — a bare label, `N items` for a multi-row drag — parks it
off-screen (`setDragImage` snapshots the node while it is in the document), hands
it over, and removes it on the next tick.

The rendered drag image is composited outside the page, so no screenshot can show
it and no test can assert on it. What is checkable is the node handed over, and
that is what the checks do: jsdom asserts `setDragImage` receives an
`.fs-drag-ghost` labelled for what is being dragged and that it does not stay in
the document; a Chromium probe reads the computed style off the real stylesheet —
`background-color: rgba(0, 0, 0, 0)`, positioned off-screen, zero left behind.

Neutralizing the `setDragImage` call fails with `expected undefined to be
'fs-drag-ghost'`; neutralizing the cleanup fails with `expected <div
class="fs-drag-ghost"></div> to be null` AND breaks four unrelated queries with
"Found multiple elements with the text: README.md" — a leftover ghost is a second
copy of the dragged name in the document, which is its own reason to remove it.

Not done, and a one-line change if wanted: dimming the SOURCE rows while a drag is
in flight, the way `.tab.dragging` and `.stack.dragging` already do with `opacity`.

### Follow-up: the ghost names what it carries

Consolidating to "N items" threw away the identities of everything picked. The
ghost now lists one row per item, with the same glyph the listing uses, and only
summarises past `GHOST_ROWS` (5) — and even then it KEEPS the first five rows and
adds a `+2 more` line, rather than replacing them with a count. Two items read as
`📄 compose.yaml` / `📄 README.md`; seven read as five named folders plus
`+2 more`.

Rows clip with an ellipsis at 22rem so one long filename cannot stretch the ghost
across the viewport.

Neutralizations: consolidating whenever there is more than one item fails with
`expected [ '2 items' ] to deeply equal [ '📄 compose.yaml', '📄 README.md' ]`
(and `[ '7 items' ]` for the seven-item case); never truncating fails with
`expected [ Array(8) ] to deeply equal [ Array(6) ]`.

Chromium, against the real stylesheet: rows are exactly the five names plus
`+2 more`, and every row's computed `background-color` is `rgba(0, 0, 0, 0)` —
transparency has to hold per row now, not just on the container. Screenshotted by
cloning the ghost over the listing, since the real one is removed a tick after
`setDragImage` snapshots it and the compositor's copy cannot be captured at all.

### Follow-up: the whole row drags, and a pane refuses its own folder

Two changes, one of which turned on a browser fact worth writing down.

**Any part of a row can start a drag.** The name was the handle because a drag and
a sweep cannot share a press — once a drag begins the browser stops sending
mousemove, so the sweep never sees the pointer. The way out is that Chromium reads
`draggable` when the drag ACTUALLY STARTS, not at mousedown. Probed both
directions on a scratch page: a div flipped draggable ON during its own mousedown
still fires `dragstart`; one flipped OFF does not fire it at all. So rows are
draggable, and a press that means to sweep turns that row's draggability off for
its duration (restored on mouseup, or the row would refuse to drag on the next
press).

The rule is one sentence: **a press starts a drag when it lands on the file name,
or anywhere on a row that is already selected; otherwise it sweeps.** Nothing that
worked before stopped working — the name still always drags, an unselected row
still sweeps — and dragging what you have selected now works from anywhere in it.
The row is the drag source now, so the name link drops back to `draggable={false}`
(a link would otherwise be the source, with its own default ghost) and `dragstart`
is handled on the row, where drags begun on the name bubble to anyway.

**A pane no longer offers a drop into the folder the drag came from.** Copying
items into the directory that already holds them is not something anyone means;
it used to be accepted and then silently skipped by the `to === from` guard, which
reported "copied 1 item" for nothing. The drop is now not offered at all — no
highlight, no drop cursor — while a subfolder ROW of that same pane stays a valid
target, since that is a different directory.

That needed a module-scoped `dragFrom` signal, shared by every pane because they
are instances of one module. It cannot come from the payload: during `dragover`
the dataTransfer is in protected mode, which exposes the TYPE list and none of the
data. `dragend` clears it, or the source folder would stay closed to the next drop.

Neutralizations: only-the-name-drags (`expected false to be true` on the selected
row's draggability), a sweeping press keeping draggable (`expected true to be
false`), draggability never restored, no same-folder refusal, and dragend not
clearing.

Chromium, two tiles: a sweep from a plain cell selects four rows; dragging from a
plain cell of a selected row to the other tile transfers them — `copied 1/4` with
the other three refused as folders crossing mounts, which is the BFF limit
speaking through the new message; the source pane never highlights during its own
drag while the other one does.

Gate: `npm run build` and `npm test` (135) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

### Follow-up: the drop ring is an overlay, not an inset shadow

Reported as awkward, and both symptoms had the same cause. `inset box-shadow` is
painted on the element's own box, so anything drawn later covers it: the row
separators are cell borders (`table.grid tbody tr` hairlines on the `td`s), which
cut the ring where they meet it, and the sticky column header carries `z-index: 1`,
which hid the pane ring's top edge entirely.

The indicator is now an `::after` overlay — `position: absolute; inset: 0;
z-index: 3; pointer-events: none` with the border and the tint on it. Above the
sticky header (1) and the selection count (2), out of flow so it cannot shift any
layout, and click-through so it never intercepts the drop it is describing. The
element only needs `position: relative` to host it, which is safe on a `<tr>` and
already true of `.file-pane` inside a tile.

Verified in Chromium rather than by eye alone: `::after` computes to `2px solid`
at `z-index: 3` against the header's `1`, the row computes to `position: relative`,
and the screenshots show an unbroken ring around a folder row and a full-perimeter
ring on the pane whose top edge now sits over the header instead of under it.

## 2026-08-01 — A global toaster, and the Files panes stop talking to themselves

A drop's outcome ("copied 1/2 — …") was rendered as a line inside the RECEIVING
pane, which pushed the listing down the moment it appeared and up again when it
changed. Transient messages now go to an app-wide toaster.

`toast.ts` is a module singleton in the same shape as `modal.ts` and
`command-center.ts` — a queue, `toast()` / `toastError()`, click-to-dismiss, and
an auto-expiry that lets errors linger more than twice as long (9s vs 4s) because
they are read rather than glanced at. `views/Toaster.tsx` is the single host,
mounted next to `ModalHost` in `App.tsx`, fixed bottom-right at `z-index: 200` so
it floats over the modal layer and, more to the point, moves nothing.

Every transient message in `FilePane` moved with it — not just the drop report:
created / uploaded / renamed / copied, the "not running" refusal, and every
caught error. Leaving half of them in a pane lane would have meant two mechanisms
for one kind of message. `.file-pane-status` is gone from the markup and the
stylesheet. What did NOT move: the truncated-listing badge and the listing error,
which are state rather than events — they describe what you are looking at, and
belong with it.

### Finding: a message that leaves the pane leaves the test tree too

Two existing tests broke the moment the messages moved, both with "Unable to find
an element with the text: /cannot drop a folder into itself/". `renderView` mounts
a view inside a Router — not `App.tsx` — so nothing rendered the toaster and the
messages went into a queue nobody displayed. This is the same shape as the
`ModalHost` trap found earlier today, where an assertion on `.modal-overlay`
could never fail because that host is not in the test tree either. The wrapper now
mounts `Toaster` for the same reason App does, and `clearToasts()` joins
`localStorage.clear()` in `beforeEach`: the queue is module state that outlives a
render, so one test's message would otherwise still be on screen during the next.

Worth generalising: when a component moves OUT of a screen and into the app shell,
every test that asserted on its output silently stops observing it. The failure is
loud here only because those two tests asserted on the text; a test asserting
absence would have gone green and meant nothing.

### Verification

Neutralizations: `report` not toasting on success fails with "Unable to find an
element with the text: copied 1 item"; a toast that ignores its click fails with
`expected <button type="button" …> to be null`.

Chromium, two tiles: dragging two files across shows exactly one toast reading
`copied 2 items`, with `.file-pane .toast` count 0 and `.file-pane-status` count 0
— the message is in the corner, outside every pane, and the old lane no longer
exists anywhere.

| file | change |
| --- | --- |
| `web/src/toast.ts` | new — the queue, `toast`/`toastError`/`dismissToast`/`clearToasts` |
| `web/src/views/Toaster.tsx` | new — the host |
| `web/src/App.tsx` | mounts `<Toaster />` |
| `web/src/views/files/FilePane.tsx` | every `setStatus` became `toast`/`toastError`; the status signal and its markup are gone |
| `web/src/styles.css` | `.toaster` / `.toast`; `.file-pane-status` removed |
| `web/src/views/views.test.tsx` | wrapper mounts `Toaster`, `clearToasts()` per test, a toaster test |
| `.agents/docs/DESIGN_SYSTEM.md` | `.toaster` / `.toast` in the class vocabulary + file map |

Gate: `npm run build` and `npm test` (136) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

### Follow-up: what a drop delivered is marked, briefly

The toast says how many items landed; it does not say WHICH rows, and in a
hundred-row listing that is the part you actually need. Arrivals now carry
`.fs-arrived` for two seconds — an `--accent-subtle` tint that fades out on its
own (`prefers-reduced-motion` gets the tint without the animation).

Two details that are not obvious:

- **The mark follows where the items became visible, not where they went.** A drop
  onto the pane lands rows in the listing you are looking at, so those rows are
  marked. A drop onto a FOLDER row puts them out of sight inside it, so the folder
  that received them is marked instead — pointing at the thing you can actually
  see. Neutralizing that (always marking the item names) fails with `expected
  '📄cornus-logo.png…' to contain 'subdir-03'`.
- **Only what actually landed.** `eachSelected` now returns `{ ok, failed }`
  instead of just the failure messages, so a partial transfer marks the rows that
  made it and no others. That also removes the temptation to re-derive successes
  by string-matching the failure text.

It is a mark, not a selection: the test asserts the selection is still empty
afterwards, since a drop that silently selected its arrivals would make the next
`prefix x` delete them.

Neutralizations: no mark at all (`expected null to be truthy`), the wrong target
(above), and a mark that never clears (`expected <tr draggable="true" …> to be
null`).

Chromium, two tiles: dragging two files across marks exactly those two rows in the
RECEIVING pane, none in the source, `animationName` is `fs-arrived`, and the mark
is gone 2.2s later.

Gate: `npm run build` and `npm test` (138) pass; `go build ./...`, `go vet
./pkg/webui/`, `go test -count=1 ./pkg/webui/` pass.

## 2026-08-01 — BFF: FsCopy copies directories

Reported as a defect: dragging a folder to another mount failed with "a folder can
only be moved within its own mount". That message was mine, added when the limit
was accepted as given. It was not given — `FsCopy` simply only ever handled one
file, and every primitive a recursive copy needs was already there.

`FsCopy` now stats its source and, for a directory, walks it: `FsMkdir` at the
destination, `FsList` one level, recurse into children, one bounded file copy per
entry. Because every step goes through the source-agnostic Fs* layer, a tree
crosses local roots and workloads in any combination without a line of
source-specific code — the same property the single-file copy already had.

Guards, each because the alternative is worse than a refusal:

- **A folder into itself or its own subtree** — the walk would recurse into what it
  is writing. Refused before anything is created. This one is load-bearing in a way
  the first test did not show: with the guard removed the copy still terminates, but
  only after `maxCopyDepth` has written 32 nested copies, and it reports "directory
  tree too deep" — the wrong problem. The test now asserts on the message and on
  nothing having been written, and that is what the neutralization run demonstrated.
- **A bare mount root** — no basename to land under, and no gesture means it.
- **`maxCopyDepth` = 32** — a runaway walk inside one request is not acceptable
  even if the tree is legitimate.
- **A truncated listing** — the container source caps a listing; copying what
  survived the cap would silently produce an incomplete tree, so it is a 413.

Symlinks are dereferenced when they resolve to a readable file. One that does not
(a link to a directory, a dangling link) is NAMED in the response's `skipped` and
stepped over — a single odd link should not cost the other 500 files, and silence
would be worse than either. `handleFsCopy` returns `{"result":"ok","skipped":[…]}`
and the SPA raises a toast for it.

The frontend lost its refusal, and a cross-mount folder MOVE (copy + delete) now
passes `recursive: item.dir` to the delete — without which the emptied-looking
source folder stays behind, since the BFF uses `os.Remove` for a non-recursive
delete.

### The mock had to grow the same contract

`web/src/mock/fs.ts` refused directory copies too (`400 cannot copy`) and ignored
`?recursive` on delete entirely. The second one is the interesting failure: the
recursive-delete change passed its neutralization, because the mock deleted a
non-empty directory just as happily either way. A mock that is more permissive
than the server does not merely fail to catch a bug — it certifies the wrong
behavior as working. It now mirrors `os.Remove` vs `os.RemoveAll`, and the
neutralization fails as it should.

### Verification

Go, all neutralized: `TestExplorerCopyDirectoryLocal` (no recursion → the nested
files are missing), `TestExplorerCopyDirectoryRefusals` (no self-copy guard → 32
nested copies and the wrong message), `TestExplorerCopyDirectorySkipsOddSymlinks`
(odd symlink aborts → 404 for the dangling link).

SPA: a folder dropped across mounts arrives with its contents (`copied 1 item`,
then the folder's child is listed); a shift-drag of a folder across mounts leaves
nothing behind. Both neutralize.

End to end against the real binary — `cornus web` over a temp project, not the
mock:

```
POST /fs/copy?source=virtual&path=project%2Ftree {"to":"project/dest"}
  → {"result":"ok","skipped":["tree/link-to-dir"]}
  dest/tree/{top.txt,sub/deep.txt,sub/deeper/leaf.txt} all present with content
  the source tree, symlink included, untouched
POST … {"to":"project/tree"}   → 400 cannot copy a folder into itself
POST …path=project             → 400 cannot copy a mount root
  and nothing extra written under tree/
```

| file | change |
| --- | --- |
| `cmd/cornus/internal/webbff/fs.go` | `FsCopy` handles directories; `copyTree`, `copyFileTo`, `sameMountFs`, `maxCopyDepth` |
| `cmd/cornus/internal/webbff/fs_handlers.go` | the copy response carries `skipped` |
| `cmd/cornus/internal/webbff/fs_test.go` | three directory-copy tests |
| `web/src/api.ts` | `copyPath` types `skipped` |
| `web/src/views/files/FilePane.tsx` | the folder refusal is gone; recursive delete for a cross-mount folder move; a toast for skipped symlinks |
| `web/src/mock/fs.ts` | recursive copy + `?recursive` honored on delete |
| `web/src/views/views.test.tsx` | folder copy across mounts, folder move across mounts |
| `.agents/docs/TODO.md` | the `FsCopy` item is half closed; the 10 MB per-file bound stays open |

Gate: `go build ./...`, `go vet ./...`, `go test ./...` all pass; `npm run build`
and `npm test` (139) pass.

### Follow-up: ghost rows while a transfer is in flight

A copy is a round trip per file, and until it returned the target pane showed no
sign anything was happening. The incoming items now appear immediately as ghost
rows — greyed, `copying…` in the Kind column, not interactive — which the real
row replaces when the listing comes back, or which are wiped if the transfer
failed. (Italic first, greyed after review; the glyph is dimmed too so it does not
shout over the greyed text.)

Ghosts live in their own `<For>` after the real rows, deliberately outside
`entries()`: the selection, the arrow cursor, the band geometry, the drag payload
and the roving tabindex all read `entries()`, and a ghost belongs to none of them
— it is not a file yet.

Three lifecycle rules, each of which cost a bug or a test:

- **A ghost dies when the listing has the row.** A `createEffect` on `entries()`
  drops any pending name that now exists, so a success never blinks (ghost gone,
  real row a beat later) — the swap is one render.
- **A ghost dies with its folder.** `go()` clears them: a ghost stands for
  something arriving HERE, and the folder you navigate to must not inherit it.
  Found by two tests failing after the ghosts landed, not by review.
- **A ghost that nothing resolves dies anyway.** A 30s backstop, because a row
  that says "copying…" forever is worse than one that quietly disappears.

A ghost for a name the listing ALREADY holds is dropped as soon as it is made —
an overwrite has nothing to announce. That is not special-cased; it falls out of
the effect, and it is what made my first two tests fail (they ghosted names that
earlier tests had already copied into the scratch folder).

Neutralizations: no ghosts at all, failures keeping their ghost, and ghosts
surviving navigation — all three fail behaviorally.

Chromium, with the copy request held open 1.5s so the in-flight state lasts long
enough to observe: dragging two files across shows exactly two ghosts in the
RECEIVING pane, none in the source, and zero afterwards with both real rows
present.

Gate: `npm run build` and `npm test` (142) pass; `go build ./...`, `go vet ./...`,
`go test` for the touched packages pass.

## 2026-08-01 — Tiling: the edge-split overlays stop stealing clicks

Reported: the pane-split CTA overlays "occur so quickly they obstruct the CTAs
near the overlays (tab activation etc.)". The speed was the visible symptom; the
defect was that the strips were live the whole time. Four invisible `<button>`s
lie on the tile edges at `z-index: 5`, so every click that landed within 10-20px
of an edge went to a split zone — never to the tab, the tab ✕, or the row under
it.

The fix is hover intent, and only hover intent. A strip is `pointer-events: none`
and invisible until the pointer has RESTED on it for `SPLIT_ARM_DELAY_MS`
(450ms); moving off it disarms instantly. Slow to show, instant to hide. Details
that each cost something:

- **One edge arms, not all four.** `armed()` holds a side, not a boolean.
  Arming the set would put three live strips on edges the pointer never visited
  — the original bug, on three sides.
- **The dwell survives movement along the edge.** Restarting the countdown on
  every `pointermove` means a slowly drifting pointer never arms.
- **Visibility is driven by the signal, NOT by `:hover`.** This one was a live
  regression, caught by the user: with `.armed:hover { opacity: 1 }` no bar ever
  appeared. The dwell by definition ends while the pointer is STILL, and a
  browser only recomputes hover state on the next pointer move — so the bar
  stayed hidden until the user jiggled the mouse. `.armed` alone reveals it.
- **`pointer-events` is set inline from the same signal**, not in styles.css:
  one owner for the click-through state and the handler's guard, and the only
  form a jsdom test (which loads no stylesheet) can observe. The handler keeps
  `if (armed() !== edge.side) return`, dead in a browser but what makes the gate
  real to anything dispatching straight at the button.

**Placement: the strips stay on the TILE edge.** I first moved them inside
`.stack-body` so the top one could not reach over the tab bar at all, and the
user rejected it — they had asked earlier for the top arm to sit on the pane's
own edge, not on the tab border, and the split it previews divides the whole
tile. So the top strip hugs the tile's top edge, over the tab bar, and stays the
thin one (10px). That is safe now for the same single reason: an un-armed strip
is click-through, so tabs and their ✕ take every click until the pointer has
deliberately rested on the strip. The residual trade-off is deliberate — rest on
the top 10px of the tab bar for 450ms and the top strip is live there.

Each strip's thickness now lives in `SPLIT_EDGES` in panes.tsx and is applied
inline, because the same number does two jobs that must not drift: it sizes the
strip and it is the band `edgeAt()` arms on. "Armed" has to mean exactly "the
pointer is on this strip" — a wider band lights a bar the pointer cannot click,
a narrower one arms a strip the pointer never reached.

**The strips also reach OUTSIDE the tile** (`OUTSET_PX = 10`), because aiming at
a pane border and landing in the gap in front of it was the common miss — the
workspace's `--space-2` padding was pure dead space. The reach cannot be DOM:
`.stack` clips its children, and a strip with a negative offset would be cut off
(and, between two tiles, would sit on top of the resize divider). So the tile
listens on the `.workspace` instead — the element that owns the gutter — and:

- **arms** when a dwell lands within `OUTSET_PX` of one of its edges, and
- **completes the click itself** out there (`splitFromGutter`), since no button
  covers those pixels. Inside the tile nothing changed: the strip is a real
  button and handles its own click.

Who owns a gutter pixel is settled by the EVENT TARGET, not by geometry:
`e.target.closest(".stack, .divider")` must be this tile or nothing. That is
what keeps the reach off the 6px resize divider — both neighbours' outward bands
cover it, and a strip live in front of a drag handle is a broken drag handle.
Removing that one guard arms both tiles over the divider, which is what the test
pins.

Testing notes worth keeping:

- jsdom 25 has **no `PointerEvent`**, and `@testing-library`'s `createEvent`
  falls back to `window.Event`, which silently drops `clientX`/`clientY`. Tests
  must hand-build `new MouseEvent("pointermove", {...})` to carry coordinates.
  Solid delegates `pointermove`, so a `bubbles: true` dispatch on the tile
  reaches the handler with `currentTarget` set correctly.
- Fake timers wrap only the dwell itself (`useFakeTimers` → dispatch → advance →
  `useRealTimers`), so the surrounding real-timer `await`s are untouched.
- Every test that needed a second tile clicked the overlay directly — a path no
  user can reach any more. They all go through `splitViaEdge()`, which dwells
  first, so they exercise the real gesture.

Neutralizations, all behavioral: helper stops dwelling → 14 tests fail (the gate
is what enables the click, not the click alone); `pointer-events` forced to
`auto` → the click-through assertion fails; only the handler guard removed → the
un-armed click splits and the combobox count assertion fails; `OUTSET_PX = 0` →
the gutter dwell arms nothing.

**Two of my own tests were worthless until a neutralization said so**, both in
the divider half of the reach test, and both invisible from reading it:

- **A stub that expired.** The assertion "resting on the divider arms nothing"
  ran after a split, and the split had replaced the `.stack` whose
  `getBoundingClientRect` I had stubbed. Both tiles measured 0x0, `edgeAt()`
  returns null for a zero rect, and nothing could have armed for any reason.
  Green, and proving nothing. The fix is to re-stub BOTH tiles after the split,
  at rects that put the probe point inside both of their outward bands — so the
  only thing standing between the pointer and two armed strips is the rule.
- **A rule with two owners cannot be neutralized.** Ownership was enforced twice
  — the `if (owner && owner !== rootEl) return null` guard AND an
  `owner ? 0 : OUTSET_PX` ternary feeding the same call. Deleting the guard left
  the ternary enforcing it, so the suite stayed green while the rule was
  "removed". That is the same failure as a test passing for the wrong reason,
  one level up: I could not have detected a regression in either half. Collapsed
  to the guard alone (the ternary was redundant — a point over this tile is by
  definition inside it), after which deleting it arms both tiles over the
  divider and the test fails.

The general form, worth carrying: **a neutralization that stays green is data
about the test, not reassurance about the code.** Both of these looked like the
fix "already being robust" and were actually the check being blind.

Verified in Chromium (Playwright, dev server, `/terminal` and `/files`): resting
on each of the four tile edges arms exactly that strip at opacity 1 with the bar
topmost at the pointer; the top strip is 1262x10 at the tile's top edge; resting
on a tab arms nothing and `elementFromPoint` returns the tab at every depth. For
the reach: a dwell 5px outside the left edge (`elementFromPoint` there is
`DIV.workspace`) arms `edge-left`, a click there splits, a dwell on the divider
arms nothing, and dragging that divider still resizes (629px -> 775px). The
probes are `.agents-workspace/tmp/probe-split-zones.mjs` and `probe-gutter.mjs`
— worth rerunning after any change here, since none of this is observable in
jsdom.

Changed: `web/src/views/tiling/panes.tsx` (hover intent, per-edge arming, the
outward reach), `web/src/styles.css` (`.armed` drives visibility; strip sizing
moved out to the component), `web/src/views/views.test.tsx` (every split now
goes through the real gesture, plus four tests for the gate, the per-edge rule,
the tile-edge placement, and the reach).

Gate: `npx tsc --noEmit`, `npx vitest run` (146) pass; no Go files touched.
`npm run build` regenerated `pkg/webui/dist` (vite's `emptyOutDir` deletes the
committed `.gitkeep` — restore it, as the Makefile's `web` target does).

**Adjacent finding, not fixed:** `views.test.tsx`'s "moves (re-tiles) a pane when
a tab is dropped on another tile's edge" passes for the wrong reason. jsdom has no
`DragEvent` either, so `fireEvent.dragOver(el, {clientX: 50, clientY: 92})` drops
the coordinates; `dropZone()` then computes `NaN`, every comparison is false, and
it falls through to its final `return "bottom"`. The test would pass for ANY
coordinates, including a centre drop that should stack. Filed in TODO.md.

**Process note:** I ran `npx prettier --write` on the files I touched. Prettier is
not a dependency here and there is no config, so it reformatted both files
end-to-end at its default 80 columns — the repo is hand-formatted at ~100. Do not
run a formatter on this tree; I rebuilt the files from `git show HEAD:` and
re-applied the edits by hand to get the diff back down to the change.

## 2026-08-01 — Tiling: the focused-tile ring stops being chopped into row borders

Reported as "1px left/right borders on every row of the file browser, and they
disappear when the cursor moves onto a row". There is no such border: no rule
matching `table.grid` sets a side border, and `.file-pane .fs-list` zeroes the
list's own frame so the listing runs flush to the tile. What was showing was the
FOCUSED-TILE ring, `.stack.focused { box-shadow: inset 0 0 0 1px }`, sliced up.

An inset box-shadow paints above the element's own background but below every
descendant, so anything opaque inside the tile erases it: the sticky column header
(`background: var(--bg-elevated)`) blanked it across the header, `tbody tr:hover`
(`var(--bg2)`) blanked it under the cursor, and the cell `border-bottom`s nicked it
at every row boundary. What survived were the stubs beside the transparent rows —
which reads as a per-row border, and is why the header (opaque) showed none. The
"disappears on hover" detail is the discriminator that names the mechanism: a
border on the row, or on any ancestor, cannot be erased by the row's own hover
background; only an ancestor's inset shadow can.

`.fs-drop-here` already documents this exact failure ("the ring came out broken
between rows and clipped along the top") and already uses the remedy, so both tile
rings now follow it: `.stack.focused::after` / `.stack.drop-target::after`,
`position: absolute; inset: 0; pointer-events: none`, `z-index: 4` (over the sticky
header 1, the selection count 2, the pane's own drop ring 3), radius
`calc(var(--radius-md) - 1px)` since the containing block is the padding box. The
outer `border-color` swap stays on the element: `overflow: hidden` clips descendants
to the padding box, so the border area was never the occluded part.

Verified in a real browser, since paint order is invisible to jsdom.
`.agents-workspace/tmp/probe-focus-ring.mjs` screenshots the focused `.stack` and
reads the pixel column at x=1 top to bottom. The ring is accent-tinted and
everything that could occlude it is neutral, so "ring painted here" is just
`b - r > 15`; the corner band is excluded because the radius curves the ring off
that column legitimately. Result: no gaps on either edge, hovered or not (the
hovered row reads rgb(185,169,230), the ring composited over `--bg2`, instead of
rgb(244,245,247) bare). Neutralized by injecting the old rule back in the same run
— it reports the header as white and gaps at every row separator (129, 168, 207,
246, ...), so the probe does observe the ring and not something adjacent to it.

Changed: `web/src/styles.css` only. Gate: `npm run build` (tsc + vite) and
`npx vitest run` (146) pass; no Go files touched. `emptyOutDir` again deleted the
committed `pkg/webui/dist/.gitkeep` — restored. Prettier was run in `--check` mode
only and never `--write`, per the standing note above: it is not a dependency here
and reformats the hand-formatted tree end-to-end.

## 2026-08-02 — Tiling: a tab can be dragged out of its own tile

Reported: "dragging a tab in a pane doesn't seem to activate pane splitting."
Reproduced as: a tile with two tabs, drag one of them to that tile's own edge —
nothing happens, and no drop indicator is ever previewed. Dragging a tab onto a
DIFFERENT tile always worked, so the gesture looked half-implemented rather than
broken.

The cause is entirely in the id plumbing, not in the layout model. `StackView`
named `activeId()` as the tile's drop target, and a tab ACTIVATES itself on
`pointerdown` — which fires before `dragstart`. So by the time a drag begins, the
tile's active pane IS the dragged pane, every `drag.over` / `drag.drop` call from
that tile arrives with `id === source`, and both hosts (`Terminal.tsx`,
`Files.tsx`) read that as "dropped on itself" and discard it. `isDropTarget()` had
the same identity test, which is why there was no indicator either: the gesture
gave zero feedback, so it read as unimplemented.

`movePane()` already handles a same-stack move correctly (detach leaves the stack
alive, then splits it against the surviving sibling), so nothing in
`views/tiling/layout.ts` changed. The fix is `dropIdFor(zone)` in
`views/tiling/panes.tsx`: any pane of a stack resolves the same tile, so when the
drag source is this tile's active pane it names a DIFFERENT tab instead. A solo
tile has no other tab, keeps naming itself, and the drop stays the no-op it should
be — a lone pane cannot move beside itself. The tile's own CENTER deliberately
keeps reporting the collision (a tab is already stacked there, so there is nothing
to promise). `isDropTarget()` became a membership test, since the reported id may
now be a background tab.

Testing trap worth remembering, and the reason the first green run was a lie:
**jsdom has no `DragEvent`, so `fireEvent` falls back to a plain `Event` and
silently drops `clientX` / `clientY`** — the same trap already documented in the
Files suite for modifier keys. `dropZone()` divides by the rect, so absent
coordinates become `NaN`, every `m === d*` comparison is false, and control falls
through to the final `return "bottom"`. A coordinate-less drag therefore reports
BOTTOM no matter where it was aimed. The new test first "passed" by aiming at the
bottom edge and asserting a vertical split — it was asserting the fallback. The
pre-existing "moves (re-tiles) a pane when a tab is dropped on another tile's edge"
test had the identical flaw: it passes `clientY: 92` and asserts `.split.v`, and
would have passed with no coordinates at all. Both now go through `dragAt()`, which
builds the event and `defineProperty`s the coordinates onto it, and both new tests
aim at the RIGHT edge — the one zone the fallback cannot fake.

Neutralized by reverting `dropIdFor(z)` back to `activeId()` in the two handlers:
"splits a stacked tile by dragging one of its own tabs onto its edge" fails on the
tile count (1, not 2) and "previews the edge drop while a tab is dragged over its
own tile" fails on the missing indicator — behavioral diagnostics, not compile
errors. The solo-tile guard test passes either way, which is the point of it.

Changed: `web/src/views/tiling/panes.tsx` and `web/src/views/views.test.tsx`. Both
hosts share `StackView`, so the Files explorer gets the same gesture. Gate:
`npx tsc --noEmit` clean, `npx vitest run` 149 pass (146 + 3). No Go files touched.

### Addendum — the jsdom `DragEvent` trap was already on the list

After writing the above I found TODO.md's first open item describing this exact
defect, filed 2026-08-01 from "Tiling: the edge-split overlays stop stealing
clicks". It names the mechanism precisely — `createEvent` falls back to
`window.Event`, a plain `Event` drops `clientX`/`clientY`, `dropZone()` computes
`NaN` and falls through to `return "bottom"` — and it names the same test. I hit it
blind a day later, from the other end, because the trap does not announce itself:
the suite was green both times. Worth noting for what it says about green suites,
not about the list. Now closed.

Two differences from the remedy it proposed. It suggested hand-building
`new MouseEvent("dragover", {clientX, clientY, bubbles: true})`; `dragAt()` instead
uses `createEvent` + `Object.defineProperty`, which keeps whatever `dragover`
plumbing testing-library sets up and matches the `dropOn()` helper the Files suite
already uses for `shiftKey` — one idiom in the file, not two.

More importantly it carried an acceptance criterion I had not met: "re-verify by
asserting a centre drop stacks". The two CENTRE tests ("stacks two panes into tabs
when a tab is dragged onto another tile's center", and the whole-tile tab-bar merge)
passed no coordinates at all and leaned on jsdom's ZERO rect, which `dropZone()`
short-circuits to "stack" before it ever looks at a coordinate. They asserted the
right outcome for a reason unrelated to the aim, and would have stayed green for a
drop aimed at any edge. Both now stub `TILE_RECT` and aim at `CENTRE`, and both were
neutralized by re-aiming at `EDGE_POINT.right`: both fail. So all four tiling drop
tests — two centre, two edge — now discriminate the zone they claim to test.

The shape to carry forward: a fixed test is not the same as a re-verified test. The
edge test was the one the TODO named, so it was the one I fixed; the centre tests
were passing, adjacent, and wrong in the same way. Suite still 149 green,
`npx tsc --noEmit` clean.

## 2026-08-02 — Files: the editor bar shrinks to the breadcrumb lane's height

The editor pane's save bar (file name, `unsaved` badge, Save, Reload, status) was
built from default controls: `button { height: var(--control-h) }` = 32px, `--text-sm`
type, `.row`'s `gap: var(--space-2)`, `flex-wrap: wrap`. The browse pane's sub-header
next to it is 18px of xs-sized crumbs in `var(--space-1)` padding. Measured in
Chromium: 41px against 27.59px. Split an editor beside an explorer and the two lanes
visibly disagree.

`.file-pane-editor-bar` in `web/src/styles.css` now mirrors `.stack-subheader`'s
metrics rather than the global control defaults: `font-size: var(--text-xs)`,
`gap: var(--space-1)`, 18px buttons at xs type, and a badge trimmed to
`padding: 0 var(--space-2); line-height: 16px` so it sits inside the lane instead of
setting the height itself. `flex-wrap` goes to `nowrap` — a wrapping bar would grow
past a sub-header that cannot wrap — so the file name and the status text ellipsise
instead.

Verification worth recording, because a CSS height claim has no unit test to hang it
on: jsdom does no layout, so the 149-test suite is blind to this by construction and
staying green proves nothing. I measured instead — a throwaway probe page carrying
both markup shapes copied from `Files.tsx`/`PaneCrumbs.tsx` and `FileEditorPane.tsx`,
linked against the real `styles.css`, `getBoundingClientRect().height` on each lane
under headless Chromium (playwright, same resolution dance as the `web-screenshot`
skill's `shot.mjs`). Both 27.59px. Then neutralized: a second run re-injecting the old
declarations on top reports 27.59 vs 41 — MISMATCH — so the probe does observe the
property it asserts, and would have caught the pre-change state. `npm run build`
(tsc + vite) clean, suite still 149 green.

One trap on the way: `vite build` has `emptyOutDir`, which deletes the tracked
`pkg/webui/dist/.gitkeep`. `git status` after any web build should be checked;
`touch` it back.

Follow-up, same day: the file name came out of the bar entirely — the tab label
already carries it verbatim, so `<strong>{pane.data.open}</strong>` was a second
copy of the same string one line below the first. `.file-pane-editor-bar strong`
went with it (dead rule, no other element in the bar is a `strong`); the `nowrap`
justification now rests on the trailing status text, which is what ellipsises.
Re-measured with the shortened markup: still 27.59px on both lanes. Suite 149 green,
build clean.

Second follow-up: the `unsaved` badge moved to the far right of the editor bar
(`margin-left: auto`, last child), and the tab label now carries the customary
trailing `*` while an editor is dirty. The two marks cover different cases and that
is the point of having both — the badge only exists on the ACTIVE tab's bar, so
before this a file edited in a background tab said nothing anywhere on screen.

`tabTitle` in `Files.tsx` reads dirtiness through a new `dirtyOf(id)`, built like the
existing `selectionOf`: `actionsRev()` first (the `paneActions` Map is plain, so the
bump signal is what ties the label to (de)registration), then the pane's own
`dirty()`. Solid wraps the `{props.ctx.tabTitle(pane)}` call site in a memo, so
reading a signal there is all the reactivity needed — no plumbing.

The regression test (`marks an editor's tab label with * while it holds unsaved
changes`) had to make a real edit, and CodeMirror owns its document — there is no
textarea to `fireEvent.input` into. It grabs the live view with
`EditorView.findFromDOM(document.querySelector(".cm-editor"))` and dispatches a
change, which travels the real `updateListener` → `onChange` path that sets the
flag. It asserts label AND badge, before / after the edit / after Save. Neutralized
by making the mark an empty string: fails with `expected [ 'project', 'project
compose.yaml' ] to include 'project  compose.yaml*'` — and note it got that far, i.e.
the badge assertions had already passed, so the dispatch really did dirty the pane
rather than the test passing on a no-op. Suite 150 green, build clean; the bar's
right edge and the 27.59px height match re-measured in Chromium.

Third follow-up: the dirty marker is `" *"`, a space before the asterisk, not `"*"`
flush against the name. Test expectation moved with it (`project  compose.yaml *`).
Suite 150 green, build clean.

### Wrap-up: what changed, and what is worth carrying forward

Final state of the change, four files:

- `web/src/styles.css` — `.file-pane-editor-bar` rebuilt to `.stack-subheader`'s
  metrics (xs type, `space-1` gap, 18px buttons, trimmed badge, `nowrap`); the badge
  additionally gets `margin-left: auto`. `.file-pane-editor-bar strong` deleted.
- `web/src/views/files/FileEditorPane.tsx` — file name removed from the bar; badge
  moved to last child.
- `web/src/views/Files.tsx` — new `dirtyOf(id)`; `tabTitle` appends `" *"` for a dirty
  editor pane.
- `web/src/views/views.test.tsx` — one new test, and the `@codemirror/view` import it
  needs.

Four findings, in rough order of how far they generalize.

**A green suite is not evidence for a layout claim, and the gap is structural, not an
oversight.** Every test in this repo's web suite runs under jsdom, which implements no
layout at all: `getBoundingClientRect()` returns zeros, so no assertion about a height,
an alignment, or an overflow can ever fail there. Any change whose whole content is
"these two things are now the same size" is invisible to the suite by construction, and
reporting "150 green" as if it covered such a change would be a false statement about
what was checked. The substitute used here — a throwaway HTML page carrying the real
markup shapes, linked against the real `styles.css`, measured under headless Chromium
via playwright (`npx --yes -p playwright node …`, resolving the module the way
`.claude/skills/web-screenshot/scripts/shot.mjs` does) — took about two minutes to write
and answered the question directly: 41px vs 27.59px before, 27.59px on both after, badge
8px from the bar's right edge (exactly its padding). The same neutralization discipline
the repo already demands of unit tests applies to a probe like this and is what makes it
evidence rather than decoration: re-injecting the old declarations makes it report
MISMATCH, so it does observe the property it asserts.

**jsdom's blindness is not confined to layout, and it fails silently in both
directions.** This is the third instance recorded in two days: missing `DragEvent`
(coordinates dropped, a drop test passing for any input), a zero rect (`dropZone()`
short-circuits to "stack", so centre tests passed aimed anywhere), and now no layout at
all. The shape is identical each time — the API is *present enough* to call, returns a
degenerate value, and the assertion written on top of it is green and meaningless. When
a test touches geometry, events with coordinates, or anything the browser computes
rather than stores, the question to ask before trusting it is not "does it pass" but
"what does jsdom actually return here".

**To drive CodeMirror from a test, dispatch to the view — do not simulate typing.** CM6
owns its document; there is no textarea, and `fireEvent.input` on `.cm-content` does not
reach the state. `EditorView.findFromDOM(document.querySelector(".cm-editor"))` returns
the live view, and `view.dispatch({changes})` travels the real `updateListener` →
`props.onChange` path — the same path a keystroke takes, so the dirty flag, the badge,
and the tab marker all update as they would in a browser. This is the reusable idiom for
any future test of editor state.

**A status indicator that only renders on the active tab states nothing about the
background ones.** The `unsaved` badge lives on the editor bar, which is only mounted
visible for the active pane in a stack; a file edited and then left behind a tab switch
had no on-screen statement anywhere. The tab-label `*` is not redundant with the badge —
each covers the case the other cannot. Worth checking, whenever a per-pane indicator is
added to this workspace, whether the state it reports survives the pane going background.

Two smaller notes. `vite build` runs with `emptyOutDir`, which deletes the tracked
`pkg/webui/dist/.gitkeep`; check `git status` after any web build and `touch` it back.
And `tabTitle` needed no reactive plumbing to pick up the dirty flag: Solid wraps the
`{props.ctx.tabTitle(pane)}` call site in a memo, so reading a signal inside the
function is enough — the existing `actionsRev()` bump covers the plain `paneActions`
Map, exactly as `selectionOf` already did.

Fourth follow-up: `.file-pane-editor-bar` takes `background: var(--bg2)`. It had no
background at all — it showed the pane's own surface, so the bar and the sub-header it
had just been sized to match were still visibly different bands. Because both now name
the same token, the two track each other through theming for free; verified in Chromium
under both `colorScheme: light` and `dark`, computed `background-color` and
`border-bottom` identical in each (light `rgb(244, 245, 247)`, dark `rgb(31, 34, 41)`).
Neutralized by overriding the bar back to `background: none`: `rgba(0, 0, 0, 0)` against
the sub-header's fill — MISMATCH, which is exactly the pre-change state. Suite 150
green, build clean.

## 2026-08-02 — Files: unsaved editor text survives a re-tile

Reported: "changes made in the editor pane end up losing if the editor pane is moved
somewhere." Reproduced exactly — drag a dirty editor's tab to a tile edge and the
text is gone, with no prompt and no error. The rebuilt pane re-reads the file and
looks like a clean editor of the same name, so the loss is not just silent, it is
disguised.

Mechanism. A pane is not a long-lived component. Every layout operation rebuilds the
tree, `commit()` pushes it through `reconcile()`, and a pane that lands under a
different node is unmounted and mounted afresh: the `<For>` row in the destination
`StackView` is new, so `body(pane)` builds a new `FileEditorPane`. Its `content`
signal — the user's text — died with the old instance. This is not specific to the
drag gesture; it is every operation that re-parents a pane (move, stack elsewhere,
collapse a split by closing a neighbour).

The fix is to stop holding the draft in the component. `web/src/views/files/drafts.ts`
is a module-level `Map<paneId, {path, content, saved}>`; the editor restores from it on
mount and writes through on every change. The pane ID is the right key because it is
the one identifier that survives every layout operation — it is what `reconcile` keys
on and what the persisted layout stores. Three details make it correct rather than
merely present:

- `saved` travels WITH the draft. Dirtiness is `content !== saved`, so restoring the
  text alone would make a saved file look dirty and a dirty one look saved.
- `path` is part of the entry and `draftFor()` returns nothing on a mismatch, so a pane
  re-pointed at another file cannot inherit the previous file's text.
- The arriving `readFsContent` must not clobber the restored draft. `lastSeeded` now
  starts at the current path when a draft came back, so the load lands and is ignored.
  Miss this and the fix appears to work for a few hundred milliseconds.

`<Editor>` also had to take `content()` rather than `savedContent()` — a rebuilt pane
has to come back showing the unsaved edit. That is safe because `Editor` only replaces
its document when its prop and the doc differ: typing (doc already equals content) is
undisturbed, while a reload or re-point still swaps the document wholesale.

Two supporting changes. `navigateTo` and `closePaneById` call `forgetDraft()` — a pane
that stops editing is done with its draft, unlike a move. And `registerActions`'
cleanup now deletes only its OWN entry (`if (paneActions.get(id) === actions)`): a move
has the outgoing instance's cleanup and the incoming one's registration both naming the
same pane id, and an unconditional delete would unregister the live pane if the order
were ever the other way round. Today the cleanup lands first — the move test would fail
otherwise, since the tab marker reads that registry — so this is insurance, not an
observed fix.

Findings.

**The tiling workspace's contract is that per-pane state lives in the pane payload, on
the server, or in a side store keyed by pane id — never in the pane component.** The
Terminal workspace already obeys it without saying so: a terminal pane keeps its
`sessionId` in `pane.data`, so a rebuilt pane reattaches to the same BFF session and a
move costs nothing. The editor was the one pane type holding irreplaceable state
privately, which is exactly why it was the one that lost data. Worth checking any
future pane type against this before it ships. (The draft could not itself go in
`pane.data`: that store is `reconcile`d on every commit and persisted to localStorage,
so file text would churn the layout store on every keystroke and write unsaved content
to disk-backed storage.)

**A rebuild-on-move bug is invisible to every test that does not move something.** The
editor had test coverage — open, save, dirty-marker — and all of it passed throughout,
because none of it re-tiles. The regression tests therefore had to perform the actual
gesture: `keeps an editor's unsaved text when its tab is dragged out to a new tile`
dirties the editor through CodeMirror's own `dispatch`, drags its tab to the tile's
right edge with real coordinates (`dragAt` + a stubbed `TILE_RECT`), then asserts the
tab marker, the badge, AND `view.state.doc` on the far side. Both were written as
PROBES first and watched to fail against the unfixed tree — the first reported
`expected [ 'project', 'project  compose.yaml' ] to include 'project  compose.yaml *'`,
which is the bug report restated as an assertion.

**Leaving the view and returning is the same rebuild, and now behaves the same way.**
The layout is restored from localStorage with the same pane ids, so a draft keyed by
pane id is still the one that pane's editor asks for. `keeps an editor's unsaved text
across leaving the Files view and coming back` pins it (`cleanup()` then re-render).
Both tests neutralized together by making `draftFor` always return undefined: both
fail, with the marker diagnostic and `expected undefined to be 'unsaved'`.

Not covered, deliberately: the `forgetDraft()` calls are memory hygiene, not observable
behaviour. Closing a pane and reopening the same file mints a NEW pane id
(`closePane` -> `newPane` -> fresh `uid()`), so a stale entry could never be read back
anyway — a test asserting "reopening is clean" would pass with or without the call, and
per TESTING.md that is worse than no test. The Map is bounded by panes opened per page
load and is not persisted.

Also noted while reading: navigating a pane away from a dirty editor, and closing a
dirty editor tab, both discard the text with no prompt (only `Reload` confirms). That
is pre-existing and unchanged here; filed to TODO.md.

### Session wrap-up — the Files editor pane, 2026-08-02

Six changes in one thread, each recorded above; in order, and all in the web SPA:

1. `.file-pane-editor-bar` rebuilt to `.stack-subheader`'s metrics — 41px to 27.59px,
   matching the breadcrumb lane it sits beside.
2. The file name dropped from the bar (the tab label already carries it).
3. The `unsaved` badge moved to the far right; the tab label gained a dirty marker.
4. The marker spaced as `" *"`.
5. The bar took `background: var(--bg2)`, so the two lanes read as one band.
6. Unsaved text moved out of the pane component into `files/drafts.ts`, so a re-tile
   stops destroying it.

Two things worth carrying past this thread.

**The verification mechanism has to match the KIND of claim, and in this session no two
claims were the same kind.** "These two lanes are the same height" and "the same colour"
are browser-computed facts, and the vitest suite cannot express them at all — jsdom has
no layout, so `getBoundingClientRect()` returns zeros and every such assertion is green
by construction; those went to a throwaway playwright probe measuring the real
stylesheet, in both colour schemes for the background. "A dirty editor keeps its text
when moved" is a component-lifecycle fact that no CSS probe could see and that the
existing editor tests all missed, because none of them re-tile; that one needed a test
performing the actual drag. Reaching for the suite by default would have produced a
confident green for four of the six changes while checking none of them. What made each
mechanism evidence rather than decoration was the same step in all six: break the fix,
watch the check fail with the diagnostic you intended, put it back.

**Cosmetic work is a decent bug detector, and only because it keeps you in one place.**
The session was asked for as toolbar polish. The data-loss defect surfaced at the end
of it, on the sixth pass over the same pane — not from an audit, but from having read
`FileEditorPane` five times for unrelated reasons and having built, by then, a clear
picture of what state it held and where that state lived. The tiling contract it
violated (per-pane state belongs in the payload, on the server, or in a side store keyed
by pane id — never in the component) was visible in the Terminal workspace the whole
time. Nobody had put the two side by side until the polish forced it.

### Closing a pane asks first: unsaved editors and live terminals, 2026-08-02

Both tiled workspaces closed panes silently. In Files the ✕ on a dirty editor tab threw
the draft away (TODO.md had it filed); in Terminal the ✕ — and `prefix x` — killed the
pane's BFF session, ending whatever the shell was running. Neither was recoverable by
reopening a pane. Both now go through a `requestClosePane` that gates the existing
`closePaneById` behind `confirmModal`.

**The gate is on the workspace's close function, not on the chrome's ✕.** `panes.tsx`
still just calls `ctx.closePane(id)`; each workspace decides what that means. That
placement is what lets the two hosts ask about different things, and it covers the
keyboard door as well as the mouse one (`term:close` was rewired too — it used to reach
`closePaneById` directly, so the bind would have kept killing sessions in silence). The
inverse matters just as much: `termCtx.closePane` stays wired to the UNCONDITIONAL
`closePaneById`, because that is the close TermPane performs when a shell exits on its
own (`paneExitAction` -> `"close"`). There is nothing to ask about a session that has
already ended, and a prompt there would fire at the exact moment the user typed `exit`.

**The clean path stays synchronous.** `requestClosePane` returns after `closePaneById`
when nothing is at stake, rather than awaiting a promise that resolves immediately —
otherwise every close of every empty pane would put a frame between the click and the
tile disappearing. `noDialog()` assertions pin that the ordinary close still takes no
question, which is the half a "does it prompt?" test would never notice going wrong.

**"Active terminal" cannot mean "in the session list".** The first version read
`sessions().some(t => t.id === sid && t.alive)` — and the test connecting a pane and
closing it found NO dialog. The list is a lagging mirror: `pollResource(getTerminals,
2000)` answers every two seconds, so a session created since the last answer is simply
absent, and absence read as "dead" leaves the newest pane — the one most likely to be
running something — unprotected for up to two seconds. Inverted to "not POSITIVELY
reported dead": the BFF keeps a dead session listed with `alive:false` for `termLinger`
(30s, `webbff/term.go`), which is the only trustworthy negative. The residue is one
extra dialog for a session already reaped past its linger; the residue of the other
polarity is a running shell killed without a word.

**What it cost to put a live terminal on screen in a test.** No previous test had ever
rendered a connected `TermPane`, and three environment gaps surfaced in a row, each
killing the pane's mount from inside a Solid computation: `matchMedia` (xterm watches
the DPR query), `ResizeObserver` (refit on pane resize), and `WebSocket` — jsdom ships
one, but under vitest it resolves `ws` to the browser shim and throws from the
constructor. All three are now stubbed in `test-setup.ts` beside the existing canvas
stub. The socket stand-in never opens and never closes, which is exactly what a pane
attached to a live session looks like from the workspace's side; what a pane DOES when
a socket closes stays covered purely by `reconnect.test.ts`.

The mock BFF also had to grow real terminal state: `POST /terminals` used to fall
through to the blanket `{result:"ok"}`, so `s.id` was `undefined` and no pane could ever
hold a session — the "active" state was unreachable from a test. It now creates, lists,
and (on DELETE) removes sessions, with `liveTerminals()` and `seedTerminals()` exported.
`liveTerminals()` is what proves the confirm actually KILLED the session rather than
merely un-rendering the tab.

**Two waits that are load-bearing, both found by a test failing for the right reason.**
(1) `connectPane()` waits for `.xterm`, not for `liveTerminals().length === 1`: the mock
creates the session before the pane RECORDS its id, and the list-only wait returns
between the two, with the pane still session-less — the close then took no question and
the test failed. (2) `openEditor()` waits for the file's text to appear before
dispatching an edit: a pane renders its Save button over an empty document while the
read is in flight, and the arriving text SEEDS the editor, so an early edit is
overwritten a tick later and the pane goes clean again. That one only failed in the full
file, not in isolation — the same test passed alone and failed in suite order, which is
the signature of a race, not of pollution.

Neutralization, per branch rather than per test: reverting `ctx.closePane` to
`closePaneById` fails the Files dirty test and both Terminal ✕ tests; reverting
`term:close` fails the prefix-x test; `isActive` forced to `true` fails only the
ended-session test (`expected 2 to have a length of 1` — the dead pane stopped closing);
`isActive` restored to the stale-list polarity fails only the two connect tests. No
branch of the gate is unobserved.

`renderView` in `views.test.tsx` now mounts `ModalHost` the way `App.tsx` does. Prompts
are how several flows continue, and without the host a test could only poke the modal
singleton (`submitModal(true)`) — it could not see the question or press the button a
user presses. The pre-existing `expect(document.querySelector(".modal-overlay")).toBeNull()`
in "opens a text file by mouse click (no prompt)" was, until now, green by construction.

### Terminal tabs: the activity badge stops resizing the tab bar, 2026-08-02

`.badge` is built for a card — 18px leading, 2px vertical padding, a hairline each side,
24px in all — and the tab bar's line box is 18.59px. So a session picking up a "working"
or "needs you" badge grew its tab bar from 27.59px to 33px, and the tile's body jumped
5.41px underneath it every time a shell started or stopped doing something. `.tab .badge`
now drops the vertical padding and tightens the leading to `--leading-tight`: 17px outer,
bar back to 27.59px in both schemes. Measured, not reasoned: 27.59 / 33 / 27.59 before,
during, after.

**The hairline stays, and that is the whole design decision.** The obvious compaction is
`border: 0` — it is 2px of the excess. But the neutral (no-variant) badge is filled with
`--bg2`, and `--bg2` is also the background of an INACTIVE tab: measured identical, light
(`rgb(244,245,247)`) and dark (`rgb(31,34,41)`). Without its border, "working" would be an
invisible chip on every tab except the focused one — which is precisely the tab you are
not looking at, and the reason the badge exists.

**The height that had to be controlled is not the badge's height.** The badge is an
inline-block inside `.tab-label`, so what sets the line box is where its BASELINE sits:
the box extends below the shared baseline by its descender plus half-leading plus padding
plus border, and it is that overhang, not the 24px, that pushed the line box out. Which is
why the rule is written against a measurement instead of arithmetic — the arithmetic has
an ascent term nobody can look up.

**A layout probe that needs no npm install.** TODO.md has an open item that the web suite
cannot assert layout and there is no repeatable harness; the previous session used a
throwaway playwright probe. Playwright's *package* is not installed here, but its
downloaded chromium is (`~/.cache/ms-playwright/chromium-1234/chrome-linux/chrome`), and
node 26 has a global `WebSocket` — so the probe drives that binary over CDP directly:
spawn with `--remote-debugging-port`, `GET /json/list` for the target, then
`Runtime.evaluate` returning `getBoundingClientRect()` and `getComputedStyle()` by value.
Two traps, both of which produce plausible-looking numbers rather than errors:

1. A file:// `<link rel=stylesheet>` to a sibling directory is not fetched — the probe
   rendered UNSTYLED markup and reported every row at 21px with 16px text (the browser
   default). Inline the real stylesheet into the generated page instead.
2. `--force-prefers-color-scheme=dark` is ignored; the "dark" run reported the light
   tokens verbatim. Use CDP `Emulation.setEmulatedMedia` before evaluating.

Both were caught only because the numbers were read against what the design system says
they should be. A probe that answers is not the same as a probe that measures.

### Session wrap-up — pane close prompts and the tab badge, 2026-08-02

Two changes, both in the web SPA, both recorded in full above:

1. Closing a pane now asks first when the close would destroy something — an editor
   holding unsaved text (Files) or a live BFF session (Terminal). One TODO item closed,
   one half of it deliberately not written.
2. `.tab .badge` compacted so a session's activity badge fits the tab bar's line box:
   27.59px whether or not a tab is badged, down from 33px.

Four things worth carrying past this thread.

**Both gates have the same shape, and the shape is the reusable part.** The gate goes on
the WORKSPACE's close function, never on the chrome: `panes.tsx` still just calls
`ctx.closePane(id)`, and each host decides what that means. That is what let two hosts ask
about entirely different things through one ✕, and what caught the keyboard door for free
(`term:close` was reaching `closePaneById` directly and would have kept killing sessions
in silence). Its mirror image matters as much: the PROGRAMMATIC close stays wired to the
unconditional function, because `TermPane` closes a pane when its shell exits on its own,
and a prompt there would fire at the exact moment the user typed `exit`. Any future pane
type gets both halves for free by following the same split — `requestClosePane` for doors
a human opens, `closePaneById` for the ones the system does.

**An "is it still active?" question needs a signal that is trustworthy in the NEGATIVE.**
The first version asked the polled session list `some(t => t.id === sid && t.alive)` and
the test found no dialog: `pollResource` answers every two seconds, so a session created
since the last answer is simply absent, and absence-read-as-dead leaves the newest pane —
the one most likely to be running something — unprotected for a two-second window. Only
the BFF's POSITIVE report of death is trustworthy (`alive:false`, held for `termLinger` =
30s). Generalised: when a decision keys off a polled mirror, absence is not a negative,
and the polarity should be picked by comparing RESIDUES — here, one extra dialog for a
session already reaped, versus a running shell killed without a word.

**A mock that answers everything hides the states it cannot produce.** `POST /terminals`
fell through to the blanket `{result:"ok"}`, so `s.id` was `undefined`, so no pane in any
test had ever held a session — "this pane is active" was not a state the suite could
reach, and no test could have noticed its absence. Making the mock stateful (create /
list / delete, plus `liveTerminals()` and `seedTerminals()`) is what turned the gate into
something observable, and `liveTerminals()` going empty is what proves the confirm KILLED
the session rather than merely un-rendering the tab. Worth auditing the rest of that
blanket branch the same way: every mutating endpoint it swallows is a state the UI can
enter and the tests cannot.

**Three claims, three different verification mechanisms, none interchangeable.** "The ✕
asks" is a behaviour: unit tests, each branch neutralized separately (four neutralizations,
each failing exactly the tests that should fail and no others). "The badge fits" is a
browser-computed fact the vitest suite cannot express at all — jsdom has no layout, so
every such assertion is green by construction; that went to a CDP probe against the real
stylesheet, in both schemes. "Navigating a dirty editor away also loses text" is a
REACHABILITY claim, and the right mechanism was reading: `subHeader` returns null for
editor panes so no breadcrumb is rendered, and `fileCommands` offers an edit pane only
`files:save` plus the splits, so nothing calls the registered `go`. The conclusion was to
write no code and record why — a prompt there would have been untestable through the UI,
which is the same failure the TODO entry had already warned about.

Two smaller notes. Both load-bearing test waits added this session are the same species —
waiting on the server-side cause instead of the client-side consequence (`liveTerminals()`
has the session, but the pane has not recorded its id yet; the Save button is on screen,
but the file's text has not landed yet). Wait for the consequence. And the second of those
waits exists because of a real defect, not a test artifact: an edit typed before the first
read lands is overwritten when it arrives. That is the last silent text loss in that pane,
and it is filed to TODO.md rather than fixed here.

## 2026-08-02 — Metrics: a dashboard over the built-in observability store

The web UI gained a **Metrics** screen (`/metrics`, `web/src/views/Metrics.tsx`) charting
what `cornus serve --obs` already records: seven workload panels (CPU, instantaneous CPU,
memory, memory limit, network I/O, disk I/O, processes) and nine server panels (process
CPU/memory, Go heap, goroutines, threads, FDs, network I/O, cumulative builds and
deploys). No Go changed — `cmd/cornus/internal/webbff` has served
`/.cornus/web/observe/{metrics,status}` since the store landed, and nothing in the UI had
ever called them.

**The query shape was the first real decision, and the docs were the wrong source for
it.** `docs/cli/observe.md` advertises `rate(container_cpu_time[5m])`, so a dashboard
built from the docs would issue `rate()` everywhere. But the store's PromQL is a
compatibility PROFILE (imbh-lgtm), out-of-profile constructs are rejected with a 400
rather than approximated, and the only spelling this repo verifies against a live store is
the BARE SELECTOR — `e2e/scenarios/observability-metrics.star` queries nothing else, at
any of its four call sites. So every panel asks for a bare metric name, and the two things
that needs are done client-side: `differentiate()` computes per-second rates
(`web/src/views/metrics/series.ts`), and the workload filter runs on the returned label
sets. The filter placement is a second win — a `{service="…"}` matcher would have put us
one keystroke away from the `cornus.replica` / `cornus_replica` footgun, where a dotted
label name matches zero series and reports no error.

**A counter reset is not negative traffic.** `differentiate` treats a decrease as "the
container was replaced and has since reached v", the same rule Prometheus uses. A plain
difference draws a deep negative spike at exactly the moment a chart most needs to stay
readable, and the neutralization confirms it: reverting that clause yields `-16` where the
test expects `4`.

**Rendering it is what found the two real bugs; the tests could not have.** jsdom has no
layout, so every geometry assertion in vitest is green by construction. A Playwright
capture against the mock BFF, light and dark, showed (1) y-axis labels clipped off the
left edge — a fixed 56px gutter cannot hold "1000 KiB/s", now sized from the widest tick
label — and (2) the flat "Memory limit" line pinned along the top edge of its plot. The
second was not a styling nit: `niceTicks` claimed to always span the data and did not.
Its loop tested `v <= hi + step*0.5` before emitting, so a maximum falling between two
steps stopped the axis short (426 MiB on a 200 MiB step ended at 400), and the chart
stretches its domain to the last tick. **The existing test passed by luck of its numbers**
— `niceTicks(0, 37, 4)` happens to land its last tick at 40. Now emit-then-test, with
regression cases at 426 MiB and a table of awkward maxima.

**15 mutations, 15 caught — but only after one escaped.** The neutralization harness
(`.agents-workspace/tmp/neutralize.py`) breaks each claimed behavior and requires the
naming test to fail. Fourteen were caught first time. The fifteenth — flipping the network
panel's `counter: true` to `false` — left the suite GREEN, because the test asserted the
headline matched `/\/s$/`, and the unit suffix comes from the panel's `unit`, not from
whether the series was differentiated. The undifferentiated counter formats identically
while being the whole hour's total. The fix was to assert on the MAGNITUDE (a per-second
rate on this mock is single-digit MiB/s; the raw counter reads 6.7 GiB/s), which is the
general lesson: when two behaviors produce the same SHAPE, only a claim about the VALUE
separates them.

**The palette is computed, not chosen.** Eight categorical hues, validated with the
dataviz skill's checker against `--bg-elevated` in both modes (worst adjacent CVD ΔE 9.1
light / 8.4 dark; normal-vision 19.6 / 19.3). Leading with the brand violet was the
obvious move and it FAILS in dark — `#3987e5` ↔ `#9085e9` collapses to ΔE 1.9 under
protanopia — so the documented order stands unrotated. Three light-mode slots sit below
3:1 on the surface, which is why the legend carries every series' latest value and every
panel has a table view; those are the relief the palette requires, not decoration. Slots
are assigned per series KEY and held for the panel's lifetime, so a replica that
disappears for one poll comes back the same color. Recorded in
`.agents/docs/DESIGN_SYSTEM.md`.

**The mock had to be able to produce the states, including the absences.** `src/mock/metrics.ts`
generates series from a seeded hash of (metric, series, sample index) — never of the clock
— so the shape is stable across runs while the window still ends at now. It deliberately
does NOT serve `container_cpu_usage`, which exists only on the kubernetes backend: that
panel therefore exercises the store's real 400 `metric "…" is not resolved`, which the UI
must render as "nothing has reported this yet" rather than as an error. Same reasoning as
the terminals mock last session — an endpoint the mock answers blandly is a state no test
can reach. First cut used independent per-sample noise and every chart was a smear; two
out-of-phase sine waves plus jitter is what makes the rendering decisions visible.

Docs: a "Metrics dashboard" section in `docs/cli/web.md` (+ ja/zh), a pointer from
`docs/guides/observability.md` (+ ja/zh), and the token/class/file-map additions in
`.agents/docs/DESIGN_SYSTEM.md`. The VitePress build passes with `ignoreDeadLinks: false`,
which is what verifies the ja/zh anchors (`#メトリクスダッシュボード`, `#指标仪表板`)
resolve against the recomposing slugifier.

## 2026-08-02 — Metrics: charts where the reader already is

The metrics panels moved out of the dashboard and next to the things they describe:
a two-panel CPU/memory strip in every project and workload section on the Overview,
and a full `metrics` tab on a workload's own page. `MetricPanel` was extracted from
`Metrics.tsx` into `web/src/views/metrics/MetricPanel.tsx` so a chart means the same
thing wherever it appears — same empty states, same counter handling, same
eight-color cap, same table view. No Go changed.

**Three refactors the new call sites forced, each of which was a latent bug.**

*A scope is a SET, not a name.* `BuildOptions.service?: string` became
`services?: string[]`, because a project has many workloads. The interesting part is
the distinction between an absent array and an empty one: absent means "every
service", empty means "a set that happens to have no members". Collapsing them —
`!opts.services?.length ? null : …` — draws the whole server's traffic under the
heading of a project that has nothing deployed. That mutation is in the harness.

*A panel is a set of SOURCES.* `Panel.metric` + `Panel.counter` became
`sources: Source[]`, so the strip's CPU panel can merge `container_cpu_time` (host
backends, cumulative) with `container_cpu_usage` (kubernetes, instantaneous). They
are the same quantity in the same unit, so they share an axis legitimately, and a
server running both backends correctly shows both. The full dashboard keeps them as
two named panels — there, an always-empty panel that says which backend it belongs
to is self-explanatory; in a strip of two it is half the strip wasted.

Merging needs per-source error containment: one source is unresolved by design on
every server, and letting its 400 fail the panel would blank the chart that DOES
have data. Only `400 … not resolved` is caught; anything else still propagates.

*Whether to name the service is a PANEL decision, not a per-source one.* This one
was found by the test rather than by reading. Each `buildSeries` call decided
"name the service only if more than one is on this chart" — and with one service per
source, a merged chart labelled a host workload and a kubernetes workload both
`replica 0`. Now `scopedServices()` computes the union once and `nameService`
overrides the per-call default.

**A mock that cannot produce a state cannot test it — twice over.** The escaped
mutation this round was "only the first source is drawn", green because the mock
served no `container_cpu_usage` at all, so source 2 contributed nothing either way.
Fixing it meant modelling a server that runs BOTH deploy backends (which cornus
supports): the shop-* workloads host-backed with network and disk counters, plus an
`edge-cache` that reports only instantaneous CPU and memory, exactly as
metrics.k8s.io constrains. That also made `edge-cache` a workload the store knows and
the compose project does not — which is what proves a project-scoped chart excludes
on the LABELS rather than on the roster. The "nothing has ever reported this" state
then needed its own knob (`setUnservedMetrics`) instead of relying on a gap in the
fixture; a gap is not a statement.

**Filter state went into the URL.** `/metrics?workload=shop-web&range=6h`, so the
strips' "All metrics →" link lands on the same slice, and a reader can share what
they found. Two things fell out. jsdom's `location` outlives a test, so one test's
scope leaked into the next and the failure surfaced in whichever test ran after it —
`history.replaceState` per test. And a `<select>` cannot display a value that has no
matching `<option>`: arriving at `?workload=shop-db` before the workload list loaded
left the control reading "All workloads" while the charts were filtered — the control
lying about the page. The list now always contains the current selection, which also
means a filter naming a deleted deployment says so instead of silently widening.

**The strip does not filter by `created`.** First cut passed only created workloads;
the store is the authority on what has data, and a stopped deployment still has the
hour behind it. What a workload is doing now is the status badge's job.

Overview gates the strips on HAVING an obs status rather than on `!loading` — that
resource re-polls, and a loading check blinks every strip off and on once a minute.
Without a store the Overview is simply unchanged, rather than growing an explanation
under every heading.

13 mutations, 13 caught (`.agents-workspace/tmp/neutralize2.py`) after the two
described above were re-aimed. Docs: a "Charts where you already are" subsection in
`docs/cli/web.md` (+ ja/zh), the strip classes and the one-clock rule in
`.agents/docs/DESIGN_SYSTEM.md`.

## 2026-08-02 — Overview: each table scrolls itself at phone width

Every `table.grid` on the Overview now sits in a `.table-scroll` wrapper, and under
the 720px breakpoint that wrapper takes the horizontal overflow. Before, the nowrap
cells made each grid wider than the viewport and `main`'s own `overflow-x: auto`
absorbed it: reaching a table's last column dragged the whole content area sideways —
headings, cards, and every other section travelled with it, so the reader lost the
page to read one cell.

The table keeps `width: 100%`. A table can never be narrower than its min-content
width, so it still overflows and still wraps `td.wrap` columns exactly as before;
only the element that scrolls changed. The rule lives INSIDE the media block on
purpose — at desktop widths these tables fit, and a scroller there is a second
scrollbar on a table that does not need one.

**Measured in real chromium, not asserted in jsdom** (`.agents-workspace/tmp/tablescroll.mjs`,
CDP over the playwright-downloaded binary, viewport emulated so the media query
actually applies). At 390px: `main` no longer scrolls, the workloads grid runs 576px
inside a 358px wrapper and moves to `scrollLeft` 218 on its own, and the two tables
that fit report no overflow. With the rule renamed away, the same probe reports
`mainScrolls: true` and a wrapper stuck at `scrollLeft` 0 — the before/after the fix
is about. At 1440px every wrapper computes `overflow-x: visible`, so desktop is
untouched.

Two regression tests, because either half alone silently does nothing: one asserts
no `table.grid` on the page (in both groupings) lacks the wrapper, the other that
`.table-scroll` gets `overflow-x: auto` inside the 720px block and nowhere else.
Both neutralized — dropping one wrapper fails the first, moving the rule out of the
block or adding a duplicate outside it fails the second.

The stylesheet test reads `styles.css` through `?raw`, which needed
`css: { include: [/styles\.css/] }` in `vitest.config.ts`: vitest stubs CSS imports
with an empty module, so the earlier version of this test was passing against an
empty string. Scoped to that one file so no other test starts depending on real
styles. Two other routes were tried and rejected: jsdom's CSS parser returns an
empty sheet for this stylesheet (so the CSSOM cannot be asked), and reading it with
`node:fs` fails `tsc` — `@types/node` is not a dependency of `web/`.

## 2026-08-02 — Overview: the workload rows stop being controls

The workloads table's trailing action cell (Start/Stop, Restart, Delete per row) is
gone, along with the empty `<th>` that headed it. The table is a reading surface
now; acting on a workload goes through its name to the detail page, or through the
by-workload grouping's section header, which still carries the same controls.

Two reasons beyond the ask. A control in a row is the one a reader hits by accident —
aiming at the link one row over, or scrolling a too-wide table sideways on a phone,
where the row is now a scroll surface as well (see the previous entry) — and Delete's
`confirm()` never fires for a mis-hit the browser resolved as a click on the button.
And the column was the widest thing in the table: at 390px the shop workloads grid
went 576px -> 479px inside its 358px wrapper.

`onChanged` had nowhere left to go, so it came out of `WorkloadTable` and out of
`ProjectSection` above it. Overview still passes it to `WorkloadSection`, whose
header actions are the ones that survive. The by-workload sections and the detail
page were already the full-capability surfaces; nothing lost a capability, one
surface lost a redundant copy of it.

The regression test asserts both halves together — no `button` inside any
`table.grid` on the page, AND Stop/Restart/Delete still reachable in shop-web's
section header. Half an assertion here is worthless: "no buttons in the table" is
equally satisfied by deleting the controls from the app. Neutralized both ways
(actions cell restored -> 12 buttons found; header actions removed -> "Stop" not
found). One existing assertion in "groups workloads, mounts, and port-forwards under
each project section" wanted a Restart button in project mode and was retired with
its comment.

## 2026-08-02 — Workload detail: four tabs become four sections, Exec becomes a CTA

The detail page's tab bar is gone. Instances, Spec, Metrics, and Logs are now four
`.section` bands stacked down the page, the way the Overview stacks a project's
parts. Tabs were the wrong trade on the page a reader opens BECAUSE something looks
wrong: the instance that just died and the log line saying why were never visible
together, and three of the four parts were always hidden behind the one you were not
on.

**Exec did not become a section, and could not have.** A section renders on arrival;
rendering `<Term>` means creating a BFF session, i.e. spawning a shell inside the
container as a side effect of opening a page. It is a CTA now — `navigate` to
`/terminal?workload=<name>` — which also puts the shell where shells belong: the
Terminal workspace splits, stacks, and reattaches them after a reload, none of which
an inline pane on a detail page could do.

`Terminal` learned that param. An EMPTY focused pane takes the target (that is what
an empty pane is for, and its picker would otherwise ask for what the URL already
said); anything else gets a new tab beside it, because `retarget` kills the session
in the pane it retargets. The param is consumed with a `replace` on arrival, so
reloading the resulting URL does not open a second session on top of the first.

`.project` was renamed `.section` in the stylesheet (2 call sites). The class had
always been the generic stacked-band rule; a workload detail page carrying
`class="project"` on its Logs section is the point where the name became a lie.

The instances table picked up the `.table-scroll` wrapper while its markup was open,
which closes the TODO left by the mobile-tables change.

Tests: the four sections are asserted WITH their content (a heading alone would pass
against four empty boxes), plus no tab-bar buttons remain and no `.term-wrap` is
mounted; and the Exec CTA is followed to the workspace, where the pane shows the
workload instead of a picker and the URL has dropped the param. Neutralized both —
`display:none` on one section fails the first, ignoring `searchParams.workload`
fails the second (the empty pane's `<select>` comes back). Three metrics tests in
`sections.test.tsx` clicked the "metrics" tab to reach the panels; the click is gone,
which is itself the assertion that the panels arrive unprompted.

Verified in real chromium at 1200px (`.agents-workspace/tmp/shot.mjs`): header row
with Start/Stop/Restart and the primary Exec, tunnel card, then the four bands with
their rules. Unrelated and pre-existing: the mock's log stream carries raw ANSI
escapes, which `pre.log` shows verbatim.

## 2026-08-02 — The Overview's conduit is a summary card, not a footnote

The conduit banners rendered as an `<h2>Conduit</h2>` plus paragraphs wedged between
the last project section and "Terminal sessions". At that position they read as a
footnote to whichever project happened to be last, which is exactly backwards: a
banner describes the SESSION — how the local agent reaches every workload — and holds
no matter which project you are looking at. That is the definition of the `.cards`
row at the top, so the block became a fourth card there, beside Server, Workloads,
and Client agent (next to the agent's own card, since the conduit is the agent's).

The card renders UNCONDITIONALLY, unlike the old `<Show when={banners?.length}>`.
Only the SOCKS5 conduit has a `Banner()` — `portForwardConduit` and `noopConduit`
both return nil — so an empty list is the answer "no proxy conduit", not "nothing to
say", and it is rendered as that sentence. A card that disappears in that case leaves
a reader unable to tell a proxy-less session from a dashboard that never had the
card, while its three neighbours all state their state instead of vanishing.

Tests assert CONTAINMENT, not presence: the old heading-at-the-foot markup passed
"the banner text is somewhere on the page", and so would a card sitting anywhere.
So the assertions are that the banner's `.card` ancestor is inside `.cards`, that the
grid's direct `.card > h3` set holds Server / Workloads / Conduit, and that only ONE
copy of the banner exists (nothing left behind below). Neutralized by moving the card
back to the foot — both new tests fail on the containment check. The second test
covers the empty case through a new `seedTunnelBanners()` mock override, held as an
override rather than by mutating the shared `fx.tunnels` const, and reset per
`installMockFetch`; that test also proves the seed works, since without it the
fixture's banner is present and "No proxy conduit." never appears.

226/226 vitest pass, `npm run build` clean, `go test ./pkg/webui/` ok, and chromium
at 1200px shows the four cards in one row.

**Do not run `npx prettier --write` on this tree.** Prettier is not a dependency and
there is no config, so it defaults to `printWidth: 80` and reformats ~480 lines of
pre-existing code in `views.test.tsx` alone. I did that mid-task and had to rebuild
all three touched files from `git show HEAD:<path>` plus the intended edits (the
working tree happened to match HEAD for them, which is the only reason it was cheap
— `git checkout` is forbidden here, so there is no general undo). Format by hand to
match the surrounding code.

## 2026-08-02 — Terminal sessions moved into the Workloads card (and the card-table overflow bug it exposed)

The Overview's trailing `<h2>Terminal sessions</h2>` table is now a second block
inside the Workloads card. The counts and the live shells are one subject — a
terminal session IS a workload's — and together they answer "what is deployed, and
what am I currently inside of"; at the foot of the page the table was something a
reader had to scroll past every project section to reach.

The card's own title stays `h3`; the sessions get a new `.card h4`, sized DOWN from
the card title and dimmed. A second `h3` inside a card reads as two cards that lost
their border. Also added `.card > :last-child { margin-bottom: 0 }` so a block ending
the card does not add its own margin to the card's padding.

**The bug the move exposed.** `.table-scroll` only has `overflow-x: auto` INSIDE the
`@media (max-width: 720px)` block — that was deliberate when every table was a page
table, since page tables fit at desktop widths. A card breaks that assumption: a cell
of the `minmax(260px, 1fr)` grid is ~237px at 1200px, narrower than the whole page is
on a phone. Measured with CDP before the fix: the sessions table ran 345px inside a
237px wrapper whose computed `overflow-x` was `visible`, its right edge landed 107px
PAST the card's right edge, and `scrollLeft` stayed 0 after a scroll-to-max — the
State column was simply unreachable on desktop. It looked fine in a screenshot only
because the next card in DOM order paints over the spill, which reads as deliberate
clipping. Fixed with an unscoped `.card .table-scroll { overflow-x: auto }`; re-
measured after: `overflow-x: auto`, `scrollLeft` 108, State header inside the card.

This is the general rule now, not a one-off: **a table in a card needs the wrapper at
every width**, and `.agents/docs/DESIGN_SYSTEM.md` says so under `.table-scroll`.

Tests (all neutralized): containment of the sessions table in the Workloads card —
the same discipline as the conduit card, because the old foot-of-page markup would
pass any "the row is on the page" assertion unchanged — plus the counts still being
in that card (the sessions joined them rather than displacing them), the empty-state
sentence landing in the card too, the `.table-scroll` wrapper on the table, and a
stylesheet test that `.card .table-scroll` exists and is NOT inside the breakpoint
block. That last one is the interesting assertion: the rule's whole value is where it
is not, so moving it into the media query (the neutralization) fails it.

229/229 vitest, `npm run build` clean. Verified in chromium at 1200px and 390px:
`docScrolls` and `mainScrolls` both false at 390px, the sessions wrapper 345px inside
324px scrolling on its own.

Follow-up left open (now in `TODO.md`): moving the table into a ~237px card means the
State column — the one carrying the actionable `needs you` badge — is the part a
desktop reader has to scroll to, since it is last of four. The scroller works and 390px
is fine (the card is full-width there), so this is a legibility cost rather than a
defect, and fixing it means either reordering the columns or replacing the table with a
compact per-session list inside the card. Both change what the section IS, and the
request was to move it, so it is recorded rather than done.

## 2026-08-02 — The workload tables' "Backend" column was one string repeated N times

Asked to drop the "Backend" column from the per-project workload table, and to first
establish why it was wired from the API at all. The answer is that it was never a
per-workload fact:

* `pkg/server/server.go` `getBackend()` builds the deploy backend lazily and MEMOIZES
  it into `s.backend` under `backendMu`; the only assignment in the file is that one,
  and `newBackend` is a closure over `defaultBackendFactory(cfg, ...)` fixed at `New`
  time. The choice comes from `CORNUS_DEPLOY_BACKEND` and server config alone — one
  backend per server process, for its whole lifetime.
* `api.DeploySpec` has no backend field, so nothing per-deployment can select one.
  Every backend stamps its own `Name()` on every status it returns (`dockerhost.go`
  Status/List, `containerdhost/backend_linux.go`, `barehost/lifecycle_linux.go`,
  `kubernetes.go` + `knative.go` — Knative rows report `kubernetes` too —
  `incushost/status_linux.go` hardcodes the equivalent literal).
* The BFF holds exactly one `*client.Client`, and `Workloads()` is one `List()` joined
  against the locally loaded compose plans. There is no aggregation across servers.

So a single response can never carry two different values, and the column rendered the
same string down every row of every screen it ever drew — the only visual variation was
`—` on compose services declared but not created, i.e. the absence of a deployment, a
fact the Status column already states.

`api.DeployStatus.Backend` itself is NOT dead and stays: it is what lets an error
message name which target refused, and it is the `cornus.backend` resource attribute in
`pkg/server/logrecorder.go`. What was wrong was relaying a server-scoped fact per row.
`api.ServerInfo.Backend` is where that fact belongs and already exists, served on
`/config`; `web/src/api.ts` now types it under `Config.server` so the "look here
instead" comment on `WorkloadTable` points at something real.

Removed: the `<th>`/`<td>` pair in `Workloads.tsx`, `Workload.backend` in `api.ts`, the
four `backend: "dockerhost"` fixture values, and `webWorkload.Backend` with its two
assignments in `webbff/core.go`. `WorkloadDetail.status.backend` stays — that is a
verbatim `api.DeployStatus` passthrough, not ours to reshape.

Tests, both neutralized:

* vitest asserts the table's FULL header list equals `["Name","Service","Image",
  "Status"]`, not that "dockerhost" is absent from the page. The fixtures could stop
  saying "dockerhost" for unrelated reasons and an absence assertion would still pass;
  an exact list also fails if the column returns under another name. Re-adding `<th>
  Backend</th>` fails it with `+ "Backend"`.
* Go reads `/.cornus/web/workloads` into `[]map[string]any` and asserts no row has a
  `backend` key — decoding into `webWorkload` cannot observe a key the struct no longer
  names, so it would pass no matter what the handler emitted. It also counts created
  rows and fails if there are none, since a `not created` row never carried the key and
  would satisfy the loop vacuously. Restoring the field fails it with `row proj-db
  relays a backend name (dockerhost)`.

**Finding worth keeping: a false comment was the reason this looked justified.**
`web/src/mock/metrics.ts` opened with "The modelled server runs BOTH deploy backends at
once, which cornus supports" — flatly contradicted by the code above, and exactly the
premise someone would cite to reinstate the column. The FIXTURE is fine; only its
stated reason was wrong. A store legitimately holds both metric spellings because it
OUTLIVES the backend choice: the series sit on disk under `--obs`, so flipping
`CORNUS_DEPLOY_BACKEND` and restarting leaves the old spelling recorded beside the new.
Comment corrected to say that. `sections.test.tsx`'s "a scope spanning both deploy
backends" still describes what it tests (merging the two CPU spellings) and was left.

Gate: 230/230 vitest, `tsc --noEmit` clean, `npm run build` clean (`touch
pkg/webui/dist/.gitkeep` after, as always), `gofmt`/`go build ./...`/`go vet ./...`
clean, `go test ./cmd/cornus/... ./pkg/webui/` ok. No doc under `docs/` lists the web
UI's table columns, so none needed updating.

## 2026-08-02 — The backend fact lands in the Server card (and a kv gap inversion)

The other half of the column removal above: the deploy backend now has a home at the
scope it is true at. Two new rows in Overview's Server card, both from `/config`'s
`server` (a verbatim `api.ServerInfo`, so the BFF needed no change — only `api.ts`
had to type the fields):

* **Backend** — the name, plus a chip for `reportsHealth`. That flag is the only
  capability a backend self-describes, and it is a planning fact rather than trivia:
  false means a compose `depends_on: condition: service_healthy` can NEVER be
  satisfied on this server, which is why the CLI rejects such a dependency up front
  instead of waiting out the timeout. `title` on the chip says exactly that, in both
  polarities.
* **Ingress** — `emulated` vs `cluster`, plus the base domain. Also backend-determined
  (`advertisedIngress`: only the kubernetes backend introspects a controller; every
  other backend has no controller to find, so the server itself is the front door).

Both fields are optional in the "not reported" sense, never "none", and the two are
handled differently on purpose. `backend` is absent when the server predates the field
OR could not construct the backend to ask — so the row is UNCONDITIONAL with an
"unknown" fallback, because dropping it would let a reader conclude the server has no
backend, the one thing that cannot be true. `ingress` is absent when the server has no
front door to advertise, which is a real "nothing here", so that row simply does not
render. Same reasoning as the conduit card's unconditional render, applied to the
opposite conclusion — the difference is whether the absence has a truthful sentence.

Dropped the ingress CLASS from the row even though it is on the wire: it is an
operator's server default rather than something a reader acts on, and a third item
wrapped the value onto a second line.

**Finding: a card's kv value wraps, and the default gaps make the wrap point at the
wrong label.** `.row` is `gap: var(--space-2)` on BOTH axes; `.kv` is
`gap: var(--space-1) var(--space-4)`. So a `.row` inside a `dd` that wraps puts its
second line 8px below the first, while the NEXT kv entry is only 4px away — the chip
ends up closer to the following label than to its own and reads as that label's value.
Not hypothetical: measured in chromium, the Server card's value column is 149px at
1200px (a `minmax(260px, 1fr)` cell is 237px of content, minus the `max-content` label
column and the 16px column gap), and "dockerhost" + the `health probes` chip is about
181px, so it wraps on every desktop render. The screenshot showed the pill floating
between "dockerhost" and "Ingress" with nothing to attach it to. Fixed with
`.kv dd .row { row-gap: 2px }` and recorded in `DESIGN_SYSTEM.md` under `.kv`.

The stylesheet test for it asserts the RELATION, resolving both gaps through their
tokens, not the literal `2px`: pinning the constant would still pass if `.kv`'s own row
gap were later tightened past it, which is precisely the regression that reintroduces
the misattribution. Neutralized by setting the inner gap to `var(--space-2)` — fails
with `expected 8 to be less than 4`.

Tests, all neutralized individually rather than by one blunt deletion: the backend name
under its own `<dt>` (removing the name span fails it on `Unable to find … dockerhost`),
the health chip and its `ok` class, the Ingress row (removing the block fails on
`expected [...] to include 'Ingress'`), the unknown fallback (making the row
conditional fails on `expected [...] to include 'Backend'`), and a `containerd`-shaped
server for the negative chip (hardcoding the positive text fails on `Unable to find …
no health probes`). The last two needed a new `seedServerInfo()` override in
`src/mock/handler.ts`, following the `seedTunnelBanners` pattern — an override rather
than a mutation of `fx.config`, which is a shared module-level const.

Gate: 234/234 vitest, `tsc --noEmit` clean, `npm run build` clean (`.gitkeep` touched),
and a CDP probe at 1200x900 and 390x844 confirming no overflow after the change —
`.kv` scrollWidth == clientWidth (237/237 and 324/324), card the same, `docScrolls`
false, and both new `dd` right edges inside the card's. No user-facing doc itemizes the
Server card's rows, so none needed updating.

Follow-up left open (now in `TODO.md`): while typing `Config.server` I confirmed that
`api.ServerInfo.Host` — `containerized` and `clientLocalMounts` — reaches the browser on
every `/config` poll and is read by nothing. Its own doc comment in `pkg/api/deploy.go`
names this UI as the intended consumer ("so `cornus setup`'s verification and the web UI
can say 'this server cannot realize client-local mounts' before a deploy proves it"), so
that is a documented-but-unimplemented surface, not a field nobody meant to use. It was
out of scope here because it is a HOST capability — it depends on how the server itself
is deployed, not on which backend it drives — and this task was about the backend; the
Mounts views, not the Server card, are where it would answer a question a reader is
actually asking. Same for `ingress.controller` (namespace / service / ports), which is
typed now but unrendered: the Server card shows only whether ingress is emulated or
cluster-realized.

## 2026-08-02 — Metrics' scope switch moved to the page title (a driver is not a peer)

The Metrics dashboard put Range, Scope, and Workload in one `.filters` row as three
equal-looking controls. Scope is not their peer: it decides which panels exist at all
and, with them, whether there is a Workload filter to use. Side by side, the Workload
control appeared and vanished for no reason a reader could see from the row itself.
Moved the switch up beside the `<h1>`, per the user's proposal, so the screen reads
outside-in: scope chooses the subject, the row below narrows it.

New `.page-head` (a `.row` carrying the h1 and the one control that governs the whole
screen). The ROW owns the heading's bottom margin rather than the h1, so the control
cannot drift from its title and no other screen's h1 moves; `.page-head h1, .page-head
.seg { margin: 0 }` clears the two defaults. `.filters` still has `margin-top:
--space-5`, which collapses with `.page-head`'s bottom margin — a flex container's own
margins still collapse with siblings; only its children are shielded — so the gap is
unchanged at 20px.

The switch stays gated on the store, as it was inside the old row: a server without
`--obs` has no panels in either scope, and a switch between two empty screens is a
choice with no consequence. The title itself is unconditional, so the page is still
identifiable while it explains itself.

Dropped the visible "Scope" label with the `.field` wrapper. Beside the title,
"Metrics [Workloads | Server]" says what it is; `aria-label="Scope"` keeps the group
named for AT (and is what the tests query by). That also made `.filters .field > span`
dead — it existed for the one group in that row whose control was not a form element,
and every remaining field is a `<label>`, whose own rule the span inherits. Removed,
with a note where it was in case a `div.field` returns. Verified rather than assumed:
computed style of the Range/Workload labels is unchanged at 13px / 500 / rgb(92, 101,
112), i.e. `--fg-dim`.

**The existing tests all passed after the move**, which is the same signal as the
sessions-table move earlier today: they query the Server button by role and name, so
placement was unpinned. Two new tests, each neutralized: containment plus document
order (`.page-head` holds the h1 and the switch; `.filters` holds Workload and Range
and NOT the switch; the head precedes the filters) — restoring the switch to the row
fails on the containment half, and moving the head below the filters fails the order
half with `expected +0 to be truthy`; and no switch when the store is off, which fails
with the rendered `<div class="seg">` when the `Show` is dropped.

Rule recorded in `DESIGN_SYSTEM.md`, generalized past this screen: a control that
decides whether another control EXISTS is not that control's peer — it goes above and
outside, because a drill-down reads outside-in. Inside `.filters`, every control
narrows the chosen scope and none of them changes what the others are.

`docs/cli/web.md` and its ja/zh translations said "One filter row scopes the whole
page" and listed Scope as a row control, so all three needed the correction. **First
draft carried the design rationale into them and the user cut it** — a user document
says where a control is and what it does; why it is there is an internal concern, and
`.agents/docs/DESIGN_SYSTEM.md` is where it belongs. Recorded a term in
`ZH_TRANSLATION_GLOSSARY.md` while there: the UI sense of "scope" is 作用范围 (already
used in `zh/cli/web.md`), NOT the inline `scope` the auth sections keep and not 作用域,
which reads as a variable scope.

Gate: 236/236 vitest, `tsc --noEmit` clean, `npm run build` clean, charset scan clean
across the three doc trees. CDP-verified at 1200x900 and 390x844: head above filters
(phone bottom 104 vs top 124), the switch inside the viewport at both, `docScrolls`
false. No Go change — this screen is entirely client-side.

Checked the one other screen-level mode switch before generalizing, so the new rule is
not mis-applied later: the Overview's "By project / By workload" `.seg`
(`Overview.tsx`, `aria-label="Overview grouping"`) sits BELOW the summary cards and
above the project sections, and that is already correct. It governs only the lower half
of the page — the cards are session-wide facts that hold in either grouping — so
hoisting it to the title would place it above things it does not drive, which is the
same error as Metrics' in the opposite direction. The rule is about the driver/driven
relationship, not "every mode switch belongs in the title": the driver goes immediately
above and outside EXACTLY what it drives. Added that qualifier to the DESIGN_SYSTEM
entry rather than leaving the rule to be read as a blanket placement.

## 2026-08-02 — Project-section tables made service-first, and the tunnel table given the Service it never had

Request: make every table in the per-project overview service-oriented (service name
first), and note that the Port-forwards heading's tunnel table has no Service column at
all.

Audited the four tables a `ProjectSection` stacks before touching any of them. Two were
already right and two were not:

| Table | Was | Now |
| --- | --- | --- |
| Workloads (`WorkloadTable`) | `Name \| Service \| Image \| Status` | `Service \| Name \| Image \| Status` |
| Mounts (`MountTable`) | `Service \| Workload \| …` | unchanged |
| Port-forwards / tunnels (`ForwardsView`) | `Workload \| Public URL \| Port` | `Service \| Workload \| Public URL \| Port` |
| Port-forwards / forwards (`ForwardsView`) | `Service \| Local forward` | unchanged |

The tunnel table's gap was not a rendering omission — the field did not exist. `webTunnel`
in `cmd/cornus/internal/webbff/handlers.go` was `{Workload, api.TunnelStatus}`, and
`Tunnels()` built each row from `s.client.List()` + `TunnelStatus(name)` with nothing to
name the service. `webMount` had carried `Service` all along, joined from
`s.serviceByResource()`, so the fix was the same join the mount path already does:
`byService := s.serviceByResource()` hoisted out of the loop, `Service: byService[st.Name]`
on the row. That map is built from the loaded project's plans, so a deployment outside it
gets `""` — which the UI renders as `—` rather than borrowing the resource name. The BFF
does the join so the browser never has to; `Tunnel.service` in `api.ts` is optional for
exactly that reason.

Why service-first is the right ordering rather than a preference: the compose service is
the one identifier the reader wrote themselves, and every other identifier in these four
tables (`<project>-<service>`, `127.0.0.1:8080 -> :80`, an ngrok URL) is derived from it.
Under a heading already scoped to one project, a leading Name column also spends the
most-read column repeating the project prefix on every row. Recorded in
`DESIGN_SYSTEM.md` under `table.grid` with the two corollaries: Name keeps its link but
does not lead, and a serviceless row says `—`.

Tests, each neutralized by restoring the previous ordering (not by a compile error):

- `TestWebTunnelsAndForwards` gained a `Service == "web"` assertion; neutralized by
  deleting the join, which fails with `tunnel service: got "", want "web"`. Deleting only
  the `byService[...]` lookup and leaving the variable was a COMPILE error and therefore
  not a neutralization — the whole line had to go.
- `TestWebTunnelServiceEmptyOutsideProject` is the complementary half: a deployment with
  no plan gets no invented name. It passes under the above neutralization by design; the
  pair is what pins the behavior.
- `views.test.tsx` "leads every table in a project section with the service" asserts over
  EVERY `#project-shop table.grid`, not table by table. A per-table check passes unchanged
  when a fifth table is added below with its own idea of a first column — which is how the
  tunnel table came to be the odd one out in the first place.
- "shows a dash for a tunnel whose deployment has no compose service" pins cell order and
  the `—`.
- The existing "gives a project's workload table no backend column" test already asserted
  the FULL header list, so it caught the reorder on its own and was updated in place. That
  is the second time that exact-list form has paid off.

Fixture change with a blast radius worth noting: adding a serviceless tunnel for
`legacy-cron` made `findByText("legacy-cron")` ambiguous in four unrelated tests that were
using it purely as a settle-signal. Changed to `findAllByText` — the intent was "the page
has rendered", never "exactly one match". A settle-signal that asserts uniqueness as a
side effect is a tripwire on unrelated fixture growth.

Verification detour worth recording: the first CDP pass reported `—` for `shop-web`'s
tunnel and no tunnel table under "Other", contradicting green vitest. Cause was the
environment, not the code — a mock BFF from another session already held `:5080`, so my
`node mock/server.ts` died with EADDRINUSE (silently, into a log I had not read) and Vite
proxied to the stale one. `CORNUS_WEB_MOCK_PORT=5081` gave a private pair. **When a live
probe disagrees with a green test suite, check that the probe is talking to your build
before concluding anything about either.** Killed only the two PIDs I owned, found by
port via `ss -lptn`, never `pkill -f`.

Gate: `gofmt -l` clean, `go build ./...`, `go vet ./...`, full `go test ./...` clean;
238/238 vitest, `tsc --noEmit` clean, `npm run build` clean (`.gitkeep` touched). CDP at
1280x1000 confirms all six rendered project-section tables lead with SERVICE and the two
Port-forwards tables now share a first column. No user-doc change: `docs/cli/web.md` and
its translations describe the metrics dashboard and the conduit, never these tables'
columns.

Follow-up finding, from re-reading the change against the Backend-column argument that
preceded it: `MountTable` and `ForwardsView` are shared by BOTH groupings, and in the
by-workload grouping a `WorkloadSection` passes them props already filtered to one
deployment (`m.workload === w.name`, `t.workload === w.name`). A workload has exactly one
service, so Service and Workload are constant down every table in that grouping — the
same shape as the Backend column removed earlier the same day. It is a weaker case, not
the same defect: those columns are constant only in one of the component's two hosts,
whereas Backend was constant in every grouping and on every screen, and the columns carry
the section's own subject rather than an unrelated server fact. Left alone rather than
fixed by reflex; recorded in TODO.md so the decision is made deliberately.

Worth noting how this was checked and what the check could NOT show: a CDP pass over the
by-workload grouping confirmed the table shapes, but its constant-column detector requires
more than one row and every fixture table there has exactly one — no fixture workload owns
two mounts. So the claim above is read from the code, not measured, and a probe that
returns "no constant columns" here is the pass a false claim would also produce. If this
is acted on, give a fixture workload a second mount first, or the test proves nothing.

## 2026-08-02 — Apply removed from the web UI: the companion stops being a second front door

**Ask.** Following the per-project table work, the user asked what the "Apply" button in the
per-project overview invokes, then: "The web frontend is just a companion, so we won't want
it either." Scope was ambiguous in a way that mattered, so it was put to the user before any
deletion — four depths were offered and the answer was **button + HTTP endpoint, keep MCP**.

**What Apply was.** `web/src/views/Projects.tsx` rendered `Apply (up -d)` on every loaded
project heading -> `applyProject()` (`web/src/api.ts`) streamed `POST
/.cornus/web/projects/{name}/apply` -> `handleApply` (`handlers.go`) wrapped the
ResponseWriter in a per-write-flushing `flushWriter` -> `Server.Apply` (`core.go`)
**re-execed the current binary** as `cornus <replayed flags> compose -f ... -p ... up -d`
with stdout+stderr into the stream. The re-exec is what bought exact `up` semantics —
reconcile plus the background-agent handoff for mounts and the conduit — without duplicating
composecli's CLI-coupled reconcile engine.

**Removed:** the button, the `applying`/`applyOut` signals, the "Apply output" `<pre>`,
`applyProject()`, the mux route, `handleApply`, and `flushWriter` (`pkg/server` has its own,
unrelated one — checked before deleting). **Kept:** `Server.Apply` and the `project_apply`
MCP tool, which calls it directly and never went through the HTTP route.

**The line drawn, and why it is not arbitrary.** The user's rationale is about the *web
frontend*: a companion shows you what a project is doing; the terminal you already have open
is where you change it. An agent driving MCP has no such terminal — it is not a person with
a shell beside the browser — so the same rationale positively argues for keeping the tool.
`Server.Apply` is now the one core method with an MCP adapter and no HTTP route, which
inverts the invariant `.agents/docs/LTM/web-ui.md` records (both surfaces are thin adapters
over `core.go`, so they never drift). That inversion is deliberate and is now written at
three sites — `core.go`'s Apply doc, the hole in `handlers.go` where the route was, and the
`ProjectSection` comment — because a future reader finding a core method with no route will
otherwise "fix" it.

**Tests, both neutralized behaviorally.**
- `TestWebHasNoApplyRoute` (`webbff_test.go`) POSTs to the **loaded** project's apply path
  and wants 404. The premise is asserted first (`s.project != nil && s.projectName ==
  "proj"`): against the old code, an unloaded project also returned 404, so a test using any
  other name would have passed before AND after and proved nothing. Neutralized by
  re-registering the route — failed with `got 200, want 404`.
- `puts no apply control on a loaded project's heading` (`views.test.tsx`) scans every
  `<button>` in `#project-shop` against `/apply|up -d|deploy/i` rather than querying the old
  label: a `getByRole("button", {name: "Apply (up -d)"})` that throws proves one *spelling*
  is gone and passes again the moment the button returns as "Redeploy". The `loaded` badge is
  asserted alongside as the counterpart — same `<Show when={project.loaded}>` block — so a
  heading that failed to render entirely cannot masquerade as a passing absence.
  Neutralized by re-adding the button — failed with `expected 'Apply (up -d)' not to match`.

**Docs.** `docs/cli/web.md` §"File editing and apply" -> §"File editing", and the removed
behavior moved into the MCP section as the one tool that goes the other way. ja/zh carried
the same section and were updated in step. Note on terminology: both glossaries preserve
*companion* verbatim, but that entry is for the sidecar / remote-companion **container**
concept — reusing it for "companion to the CLI" would collide, so the sense was translated
(ja 「CLI に寄り添う」, zh 「CLI 的辅助界面」) rather than the word. No doc links pointed at
the renamed anchors (checked across `docs/` and `.agents/`, excluding `dist/`).

**Gate.** `gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all clean.
`tsc --noEmit` clean; vitest 239/239 (was 238); `npm run build` clean with
`pkg/webui/dist/.gitkeep` touched after. NFKC and full-width punctuation scans clean.

## 2026-08-02 — Workload-scoped tables drop the two columns their own heading already carries

**Ask.** "In the tables on a per-workload overview, we don't need the workload column as it's
just redundant, nor the service column as it's just enough to show a service-workload
association once in the header. During the fix, we should be aware of the components being
shared with the per-project overview." This closes the TODO item opened earlier the same day.

**The constancy is decided by the caller, not the table.** `Overview.tsx` filters each of a
`WorkloadSection`'s three lists to one deployment — `m.workload === w.name`, `t.workload ===
w.name`, `svc === w.service` — and a deployment realizes exactly one compose service. So
Service and Workload are the same two strings on every row of every table there. That
earlier TODO warned a constant-column *probe* proves nothing (no fixture workload owns two
mounts, so "clean" is what a false claim also returns). Reading the filters sidesteps the
probe entirely: constancy is a property of the filter, provable at the call site, and no
fixture change was needed after all. **Generalizable: when a claim is "these rows all agree",
look for where the rows were selected, not at the rows.**

**Shape.** `MountTable` and `ForwardsView` take `scope: "project" | "workload"`, **required**
— not optional-with-a-default. Both call sites pass it explicitly. A default would silently
hand a future third section the project shape, which is the failure this change exists to fix;
required makes the next author answer. `WorkloadDetail`'s instances table was checked and is
unaffected (ID/State/Health/Exit code — it never carried these columns).

**A rendering hazard checked before writing any of it.** Solid compiles each `<Show>` child
into its own `template()` whose HTML is assigned to a `<template>` element's `innerHTML`, so a
bare `<th>` or `<td>` has to survive fragment parsing outside a table. It does — the HTML
spec's "in template" insertion mode reprocesses `td`/`th` in "in row" mode — but that was
verified empirically in BOTH jsdom and the real chromium before committing to per-cell
`<Show>`, since a green jsdom suite would not have caught a browser-only difference.

**Tests, three assertions with three different failure modes, each neutralized behaviorally.**
- Absence: every `<thead th>` of every table under `#workload-shop-web` contains neither
  "Service" nor "Workload". Checked as an absence over all header cells rather than by
  counting columns — a count passes unchanged if Service goes and another column grows back.
- Presence: the header still states the pairing once (`h2` is `shop-web`, and a `.row
  span.muted` reads `web · shop`). Without it, deleting the whole section satisfies "the
  columns are gone".
- Alignment: new `expectAlignedColumns` helper asserts every body row has exactly as many
  cells as the header, run in BOTH groupings. This is the invariant that survives any future
  column change, and it catches the specific hazard this refactor introduces — head and body
  cells are dropped through SEPARATE `<Show>` blocks, so changing one and not the other
  misaligns every row under a correct-looking header.

  Neutralization A (`scope="project"` at the workload call site) failed the absence check:
  `expected [ 'Service', 'Workload', 'Kind', …(4) ] to not include 'Service'`. Neutralization
  B (body cells unconditional, header still conditional) failed ONLY the alignment check —
  `expected 7 to be 5` — with the absence assertions passing clean. B is the one that matters:
  it proves the alignment helper observes a failure the absence check cannot see, so the two
  are not the same assertion written twice.

**Live check.** Headless chromium at 1280x1000 over CDP, both groupings, after clicking the
real grouping toggle. By project: unchanged, all four tables still lead with Service. By
workload: mounts `Kind|Source|Target|Mode|Status`, tunnels `Public URL|Port`, forwards
`Local forward`, all three `aligned: true`, header `shop-web 2/2 running web · shop
alice@laptop`. Both dev servers were started on unused ports (5082 mock, 5198 Vite) after
`ss -lptn` showed another session still holding 5080, and killed by port afterward — the
2026-08-02 EADDRINUSE lesson applied without re-learning it.

**Gate.** `tsc --noEmit` clean; vitest 240/240 (was 239); `npm run build` clean with
`pkg/webui/dist/.gitkeep` touched; `go build`/`vet`/`test ./...` clean. `DESIGN_SYSTEM.md`
carries the counterpart rule to the service-first one, including the required-prop and
separate-`<Show>`-blocks warnings.

## 2026-08-02 — BFF: the FS operation planner, and three defects found designing it

Asked for a "client-side" copy/move for the web BFF, backed by a unified backend that
resolves the actual filesystems behind a source and a target. The premise checks out and
is sharper than it first looks: cornus bind mounts are CLIENT-LOCAL, served to the
container over 9P from the caller's own machine (`pkg/client/client.go:1288-1318` ->
`pkg/server/deploy_attach.go:130-176`). So copying into a bind-mounted container path
today goes BFF -> HTTP tar -> server -> `docker cp` -> the container's write -> back out
over 9P -> onto the developer's disk, inches from where it started. The explorer had no
idea the two paths named the same bytes.

This entry covers the first half: the resolution layer and the two pre-existing defects
that had to be fixed before anything could safely be sequenced behind it. The streaming
backends, `FsMove`, the route and the SPA are still open (TODO.md).

### What landed

- **`fsguard.go` (+ `_linux`/`_other`)** — one predicate deciding whether a host
  directory may be exposed at all, shared by `buildLocalRoots` and the new resolver.
- **`fsmounts.go`** — the per-workload mount table over all four spec lists
  (`Mounts`, `Volumes`, `Tmpfs`, `Devices`), longest-prefix on PATH BOUNDARIES with a
  declaration-order tie-break, plus `originMatchesHere`.
- **`fsplan.go`** — `fsSite`/`fsPlan`, `site()` (which performs the redirect), and a
  PURE `planTransfer()`. Purity is the point: every routing decision is a table test with
  no daemon and no filesystem, and each plan carries a `why` so a misroute says so
  instead of merely being slow.

### Three defects, none of which needed the new feature to reach

1. **`execCapture` reported success for a failed command.** It swallowed the
   `ExecInspect` error, leaving `ExitCode` at its zero value, and never consulted
   `api.ExecState.Running` — which docker commonly reports as `true, 0` right after the
   stdio stream closes. `ExecResult` gained `ExitKnown`, deliberately the POSITIVE
   spelling so an unfilled value means "unknown": a fake or a future call site cannot
   claim success by omission. `inspectExit` polls until the status settles.
   `containerExecOK` refuses on unknown; `containerList` falls back to the tar listing
   rather than parsing a possibly-truncated glob.
2. **The explorer exposed host pseudo-filesystems, writably, unauthenticated.**
   `buildLocalRoots` added every external bind source that stats as a directory and
   never looked at `m.ReadOnly`, so `- /proc:/host/proc:ro` produced a WRITABLE `/proc`
   root — putting every local process's environment behind a local GET, on a surface
   whose only defence is `guardHost` (a DNS-rebinding check, not authorization). Now
   screened by filesystem type and `:ro` honoured, with refusals reported to the UI
   rather than a root silently vanishing.
3. **Nested mounts were invisible to resolution.** Nothing in `pkg/compose`, `pkg/api`
   or `pkg/server` rejects overlapping targets, so `- ./data:/data` plus a volume at
   `/data/cache` is legal. On the host, `cache/` is an ordinary empty directory while the
   container sees the volume — so a redirect would read, and write, the wrong bytes.

### Two things the measurements changed

**`statfs` magic, not a path denylist, and NOT devtmpfs.** The plan listed devtmpfs
among the refused types. Running it showed `/dev` reports `TMPFS_MAGIC` — devtmpfs and
tmpfs share a magic — so that list would have refused every legitimate tmpfs-backed bind
source. What makes `/dev` dangerous is its device NODES, refused by file type wherever
they appear, which is both narrower and more complete. And the test is on the filesystem
because a name check is defeated by the very case that motivates it: the whole point of
`- /proc:/host/proc` is that /proc appears somewhere else. There is a paired test with
the mount spelled `- /proc:/mnt/p`.

**No in-container exec route.** On direction from the requester, the planner has
`execHere`, `execServer` and `execRelay` — and deliberately no `execContainer`. That
deletes a cluster of defects at once: untrustworthy exit codes (1 above), busybox-vs-GNU
divergence with no `--one-file-system`, distroless images where `cp`/`mv` do not exist
and docker's "executable file not found" collides with the not-found string match, and
four different answers to folder-onto-existing-folder. The cost is stated rather than
hidden: until a structured server-side fsop exists, a same-workload copy relays. The
headline feature is untouched — the client-site redirect is pure local file I/O and
execs nothing. The pre-existing `listScript`/`mv`/`rm` execs stay as a frozen legacy
fallback, and `TestPlanTransferNeverExecsInTheContainer` keeps a "just shell out, it's
faster" change from landing quietly.

### Neutralization

Every check was broken deliberately and observed to fail with the intended diagnostic;
none was a compile error.

| broken | diagnostic |
| --- | --- |
| `inspectExit` swallows the error again | "unreadable status reported as known (code 0) — this is the bug" |
| `containerExecOK` drops the `ExitKnown` check | delete returned 200, want 502 |
| `browsableSource` gate removed | `/proc` came back as root `mount0` under BOTH spellings |
| `m.ReadOnly` gate removed | all four mutations succeeded on a `:ro` root |
| volumes not recorded in the table | `/data/cache/x` resolved to **client** — "why: client-local bind at /data", i.e. the host's empty directory |
| `serverSideVisible` widened to container paths | image-layer paths routed to a server that cannot see them |

The last two are the ones worth keeping: both would have been silent, and both produce
wrong bytes rather than an error.

### Verification

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` all clean.
`cd web && npm run build && npx vitest run` clean (240/240). `pkg/webui/dist/.gitkeep`
restored after the Vite build emptied its output directory.

### Still open

`fsBackend` with streaming open/create (temp+rename, exact tar framing), re-seating the
`Fs*` methods on the planner (per-ENTRY re-resolution in `copyTree`, the `FsList`
mount-table overlay, the host-boundary check), `FsMove`, the `/fs/move` route, and the
SPA. Filed in TODO.md along with H6 (`containerPut` can `RemoveAll` a directory tree)
and H7 (a container symlink copies as an empty file), both still open.

## 2026-08-02 — BFF: the redirect goes live, and FsMove replaces the browser's copy+delete

Second half of the FS operation planner (first half in the entry above, which built the
resolution layer but left it unwired — `site()` existed and nothing called it, so the
feature did not yet do anything).

### The redirect is now in effect

`resolve()` in `fs.go` is virtualize plus the client-local bind redirect: a container path
covered by a bind mount comes back as a LOCAL query anchored at the bind source. Every
`Fs*` method routes through it, so read, list, write, mkdir and delete of such a path all
happen on the developer's own disk.

`TestExplorerServesBindMountFromDisk` drives all four through the real HTTP surface with
a `containerFS` whose every method calls `t.Fatal`. It passing is the claim: zero daemon
round trips where there used to be a tar to the server, a `docker cp` into the container,
and the container's write travelling back out over 9P to that same directory.

One thing the wiring exposed that the design had not: **a concrete query cannot carry the
read-only bit.** A `:ro` bind whose source sits inside the project directory resolves into
the (writable) `project` root, so mapping site -> query would silently drop the `:ro`.
`resolve` returns the site alongside the query and every mutation checks `siteWritable`,
which is why the read-only state lives on the site rather than on the root.

### FsMove

`POST /.cornus/web/fs/move`. When both sides land on the same filesystem — which now
includes two DIFFERENT local roots, and a container path a bind resolved onto the disk —
it is one `os.Rename`: instant, atomic, indifferent to size. `EXDEV` (two roots on
different filesystems) and `EBUSY` (a mount point, which every bind target is) are not
failures, they mean "not with this syscall", and fall through to copy-then-delete.

The source is deleted only after a copy that reported nothing `skipped`. A move that
silently dropped the symlinks a copy stepped over would be data loss, so a partial copy
keeps the source and returns the list. The SPA THROWS on that case rather than toasting
it: `report()` counts anything that did not throw as success, so a toast would have read
"moved 1 item" for something still sitting where it started.

`FsCopy` was split into `FsCopy` and `copyResolved(…, adjust bool)`. The `mv x dir/`
adjustment must happen exactly once and BEFORE the sites are resolved — appending a
basename can cross a shadow boundary (a bind at `/data`, a volume at `/data/cache`), so
resolving first answers for the wrong path.

### The neutralization that did not fire, and what it found

Removing the read-only-SOURCE gate left `TestFsMoveRefusesReadOnlySource` passing. The
test was wrong, not the code: without that gate the copy still runs and only the delete
fails, so the response is still 403 and the source is still intact — identical from the
outside, with a half-finished move on disk. The test now asserts the DESTINATION was
never written. A neutralization that fails to fire is worth more than one that passes.

Also fixed: **the mock's `rename` discarded the destination root**
(`resolveVirtual(toRaw).parts` computed, `.root` thrown away), so a cross-mount rename
moved the node within the SOURCE tree. Invisible while the SPA only called rename for
same-mount moves. It now mirrors the Go refusal, and `/fs/move` was added to the mock with
the Go refusals verbatim.

### Neutralization table

| broken | diagnostic |
| --- | --- |
| rename fast path disabled | "inode changed (130811338 -> 130811339): the move copied instead of renaming" |
| read-only source gate removed | "a refused move still wrote to the destination" (only after the test was strengthened) |
| `moveOrCopy` back to copy+delete | "expected [] to have a length of 1" — no `/fs/move` was issued |

### Verification

`gofmt -l` clean; `go build ./...`, `go vet ./...`, `go test ./...` clean. `tsc --noEmit`
clean — it caught a real defect in the new SPA test, an unimported `onTestFinished` that
would have leaked a `fetch` wrapper into every later test. vitest 240/240.
`pkg/webui/dist/.gitkeep` restored after the Vite build.

### Still open, and deliberately not claimed

**Streaming is NOT done.** `copyFileTo` and `FsCopy`'s single-file branch still
`io.ReadAll` under `maxEditableFileSize`, and `FsOpen`'s container branch caps
unconditionally rather than on `bounded`, so a container download is capped too. The cap
bites less often now — a bind-mounted path never goes through it, and a same-filesystem
move has no size limit at all — which makes the remaining cases narrower but no less
wrong. TODO.md carries what streaming actually requires, including the finding that
`tarcopy.go:321-328` removes the destination BEFORE extracting, so an aborted copy
destroys the old content as well.

Also open: H6 (`containerPut` can `RemoveAll` a directory tree), H7 (a container symlink
copies as an empty file), the per-ENTRY re-resolution in `copyTree` and the `FsList`
mount-table overlay for nested mounts, the host-filesystem-boundary check on recursive
walks, and the whole Phase 2 server site. All filed.

### Addendum — H6 and H7 closed the same day

`containerPut` now sets `NoOverwriteDirNonDir` AND pre-checks the destination with
`StatPath`, 409ing on a kind mismatch before anything is sent. Both are needed: the option
is ignored by barehost's gVisor tar-exec path and by incus, so relying on it alone would
make the guarantee depend on which backend answered.

`singleTarEntry` now rejects `TypeSymlink`/`TypeLink`. Only half of H7 is closed —
`copyTree`'s symlink branch still records a skip on ANY error, so it conflates
"deliberately stepped over" with "the transfer failed". Left open and noted in TODO.md
rather than quietly counted as done.

Neutralizations: "a container symlink read as an empty file with no error — this is the
bug"; "writing a file over a directory: got 200, want 409"; "CopyTo was issued without
NoOverwriteDirNonDir".

## 2026-08-02 — Findings from the FS-planner work that outlive it

The two entries above record what was built and fixed. These are the discoveries that
cost time to establish and are worth having written down before the next person needs
them. Several are load-bearing for work that is still open.

### Measurements that contradicted the design

**`/dev` is `TMPFS_MAGIC`.** devtmpfs and tmpfs share a superblock magic, so a
pseudo-filesystem denylist that includes "devtmpfs" also refuses every legitimate
tmpfs-backed bind source. Verified by running `unix.Statfs` over `/`, `/proc`, `/sys`,
`/dev`, `/sys/fs/cgroup`, `/tmp` rather than reasoning about it. The consequence shaped
the fix: file TYPE is the right screen for device nodes (and catches one sitting in an
ordinary directory, which a filesystem screen never would), filesystem TYPE is the right
screen for procfs and friends.

**`os.Root` does not stop a walk crossing a mount point.** Its own documentation is
explicit — "Methods on Root do not prohibit traversal of filesystem boundaries, Linux
bind mounts, /proc special files, or access to Unix device files". It bounds path NAMES.
So `openat2(RESOLVE_BENEATH)` closes the check/open race and the containment-anchor
question, but a recursive copy can still pull in a whole NFS mount and a recursive
`RemoveAll` can still descend into one. That is why a device-number comparison per
directory descent is still on the list, and it is not redundant with `os.Root`.
`os.Root` is currently unused elsewhere in the tree; the module is `go 1.26.0`.

**`golang.org/x/sys/unix` does not export `MQUEUE_MAGIC` or `CONFIGFS_MAGIC`.** Both are
stable `linux/magic.h` ABI and are spelled out as local constants in `fsguard_linux.go`.
Anyone extending that list should expect the same for other rarely-used magics.

**Compose project-scopes volume names.** `- named-vol:/data/cache` reaches
`api.VolumeSpec.Name` as `proj_named-vol`, not `named-vol`. Found as a test failure where
the test was wrong and the code was right. This is the concrete reason the design
addresses a volume by its container TARGET rather than by its name — the name is not the
one the compose file wrote, and on dockerhost an anonymous volume's real identifier is a
daemon-generated id that is regenerated on every recreate.

### The gap that forced the Origin guard

**`api.DeployStatus` carries no mount information at all.** The mount table is read from
the compose file on disk; the running workload is discovered through `client.List`. The
two are joined by NAME and nothing else. So a deployment made from another checkout — or
another machine — would be described by a table naming directories it never had, and a
redirect would read and write the wrong place with nothing to notice it. `api.Origin`
(`Host`, `User`, `Directory`, all client-attested) is the only available evidence, which
makes the guard a correctness check against a stale or foreign deployment and NOT a
security control. Anything it cannot confirm falls back to the container path, which is
always correct and merely slower. If mounts ever join `DeployStatus`, this guard should
be replaced by the real comparison rather than kept alongside it.

### Caretaker: three defects found while scoping the server-side site

None of these needed the FS work to reach; all are filed in TODO.md.

- **yamux hands each stream to exactly one `AcceptStream` caller, and the caretaker
  already has two** — `runPortForwardAccept` (`pkg/caretaker/portforward.go:51`) and
  `serveIngress` (`pkg/caretaker/hub.go:283`), each closing tags it does not recognize.
  A pod carrying a hub role with delivery targets AND PortForward can misroute or drop
  streams today. `pkg/build/buildwire` states the one-loop rule in so many words.
- **On kubernetes the caretaker is never registered.** `cfg.Instance` is set at exactly
  one site (`kubernetes.go:3072`), inside `addAgentForwardRole`, gated on
  `spec.AgentForward` and hard-coded to replica 0 — and `Registry.Put("")` is a no-op.
  So the server cannot address a k8s caretaker at all unless agent-forward happens to be
  on, and when it is, every replica collides on `name/0`. This misaddresses `ForwardPort`
  and the exec agent-relay, not just anything new.
- **`barehost` mirrors `caretaker.Config` with its own structs** and the drift test
  (`TestCaretakerConfigWire`) compares two hand-populated literals — so a field ADDED to
  `caretaker.Config` and absent from the mirror produces identical `omitempty` JSON and
  passes. The guard cannot catch the omission it exists to catch.

Also established: the caretaker shares only the NETWORK namespace with the app
(`NetworkMode: container:<app>`; kubernetes never sets `ShareProcessNamespace`). It has
its own mount and PID namespace, so it can serve only what is mounted INTO it — never the
app's image layers, and not `/proc/<app-pid>/root` either. Any "let the caretaker do the
filesystem work" design has to be scoped to volumes and declared roots, and cannot
replace the archive primitives.

### Method notes

**Three adversarial review passes over the plan, before any code.** They found one
data-loss bug the straightforward implementation would have introduced (a streaming
`create(O_TRUNC)` truncating its own source, because the existing test fixture already
aliases one directory as both a local root and a bind target), three pre-existing defects
that only become destructive once a move deletes a source, and a phase that was dead on
arrival on kubernetes. Cheaper than finding any of it afterwards.

**A neutralization that does not fire is a finding.** Removing the read-only-source gate
from `FsMove` left its test green: without the gate the copy still runs and only the
delete fails, so the status code and the surviving source look identical from outside
while a half-finished move sits on disk. The test was asserting the wrong things. Worth
generalising — when a check is removed and the test still passes, the default assumption
should be that the test is weak, not that the check was redundant.

**Type-checking caught a defect in a test.** `tsc --noEmit` flagged an unimported
`onTestFinished` that would have leaked a `fetch` wrapper into every later test in the
file. The SPA suite would have stayed green and started lying.

## 2026-08-02 — BFF: batch copy/move and a preflight endpoint

Requested: let the copy/move endpoints take a batch, and add a preflight that checks
permissions, foresees what a transfer will do, and reports it back.

### Batch

`/fs/copy` and `/fs/move` now accept `{"to": "<dir>", "items": [{"from": …, "to": …?}]}`.
An item with no `to` lands under the request's `to` as its own basename, which is what a
drag gesture means; an explicit `to` allows a rename in the same call.

A body with NO `items` keeps the original contract exactly — source from the query, `to`
as the exact destination, `{"result","skipped"}` back, and a failure as an HTTP status.
That is deliberate rather than lazy: the mock, the SPA and a dozen existing tests speak
that shape, so a batch is an extension and not a replacement.
`TestFsBatchKeepsSingleItemContract` pins it, including that a failing single item still
answers non-200 instead of a 200 with the error buried in a record.

A batch always answers 200 with per-item detail. The request succeeded; one item failing
is a fact about that item. A status code cannot say "three of five landed". Items run
independently — an early failure does not strand the ones after it — and `result` is
`ok`/`partial`/`failed`. A MOVE whose copy reported anything `skipped` is recorded as
FAILED, because the BFF kept the source: calling that "ok" would report a file as moved
while it sits where it started.

### Preflight

`POST /fs/preflight?op=copy|move` takes exactly the body the real endpoints take — so
what is reported is what would run, not an approximation — and changes nothing. Per item
it reports:

- **route** — `here` (the developer's own filesystem, no daemon round trip), `server`, or
  `relay`, with the planner's `why` string;
- **action** — `create`, `overwrite`, `merge` (an existing folder copied into, not
  replaced), or `refused`;
- **permissions** — every refusal the real call would raise, including that a MOVE off a
  read-only mount is refused where the same COPY is fine, since only a move removes the
  source;
- **size** — file count and byte total, from a bounded walk (`maxPreflightNodes` 5000)
  that says when it stopped short;
- **warnings** — most usefully the limits a user cannot otherwise see coming: the per-file
  cap that still applies on a RELAYED transfer (and only there — warning unconditionally
  would be noise), symlinks that may be stepped over, and listings too large to enumerate.

### A bug the test found

The self-copy guard fired on a legitimate transfer. Both ends resolved to `siteClient`, so
the sites compared "the same filesystem", and both paths read `"tree"` — but relative to
DIFFERENT roots, naming unrelated directories. `sitePath()` now joins the root back on
before comparing. This is the same class the planning review flagged (identity must be a
resolved absolute path, not a root-relative one) reappearing in new code, which is worth
noting: knowing about a hazard did not stop me writing it.

Note the inverse of this bug is STILL live in `FsCopy` itself: `sameMountFs` compares
`fsQuery` fields, so one directory reachable through two different roots is seen as two
filesystems and a copy into its own subtree is not caught. Filed, not fixed here.

### Neutralization

| broken | diagnostic |
| --- | --- |
| batch aborts on the first failure | `result = "failed", want partial`; `want a record per item, got 2` |
| preflight skips the read-only source check | `move off a read-only mount: action = "create", want "refused"` |
| preflight actually performs the copy | `preflight created the destination`; `preflight overwrote the destination` |

### Verification, and one thing that could not be verified

`gofmt`/`go build ./...`/`go vet ./...`/`go test ./...` clean. TypeScript: **0 errors in
the files this change touches** (`api.ts`, `mock/fs.ts`, `FilePane.tsx`), and the 12 test
suites not involved in the tiling chrome pass 142/142.

The `views.test.tsx` suite could NOT be run: another agent is mid-refactor of the tiling
drag system in the same tree — `Files.tsx` and `Terminal.tsx` import `createTileDrag` from
a `tiling/drag.ts` that is still untracked, and `panes.tsx` references `DropZone` without
importing it, so the whole Files screen fails to construct with "createTileDrag is not
defined". That suite passed 240/240 immediately before this change, and nothing here can
produce that error. Left strictly alone rather than "fixed", since fixing it would collide
with work in progress. The batch/preflight paths in `web/src/mock/fs.ts` are therefore
typechecked but not yet exercised by a test — worth re-running once the tiling work lands.

### Not wired into the UI

The endpoints and the typed client (`copyBatch`, `moveBatch`, `preflight`) exist; the
Files screen still issues one request per dragged item and does not preflight. Whether a
drop should preflight first — and whether a warning should prompt or merely inform — is a
product decision, not a mechanical one, so it is deliberately left open.

## Tiling chrome: making pane splitting reachable on touch devices (2026-08-02)

**Report:** "Pane splitting in the web screens doesn't work with touch devices."

### What was actually broken

Three gestures, and the report named the mildest-sounding one. The root cause is that the
tiling chrome is built on two primitives touch does not have — **hover** and **HTML5
drag-and-drop**.

1. **Splitting was impossible by tap, not merely awkward.** The edge overlays
   (`.pane-split-zone`) stay `pointer-events: none` until the pointer has *rested* on a
   tile edge for `SPLIT_ARM_DELAY_MS` (450ms), driven by `pointermove` on `.workspace`.
   Touch fires `pointermove` only while a finger is down, and the lift sequence is
   `pointerup` -> `pointerleave` -> `click`; `pointerleave` runs `disarm()`, so by the
   time `click` arrives `armed()` is null and the handler's own guard rejects it. Even a
   deliberate 450ms long-press could not split. The only other route, the command palette
   (`term:split-h` etc.), opens *solely* from the tmux prefix key — no keyboard, no
   palette, no split. So on a tablet you could use the pane you landed in and nothing
   else.
2. **Move/stack was inert.** Tab rearranging is `draggable` + `onDragStart`/`onDrop`;
   mobile browsers never synthesize drag events from touch.
3. **Resizing worked but leaked.** `SplitView.startDrag` already used pointer events with
   `setPointerCapture`, and `.divider` already had `touch-action: none` — but there was no
   `pointercancel` handler. A touch promoted to a scroll sends `pointercancel` and never
   `pointerup`, so the `window` listeners leaked and the pane went on resizing under every
   later pointer move. No `onCleanup` either, no `pointerId` filter (a second finger drove
   the same divider), and no grab offset.

### What was built

Explicit tap-reachable equivalents, rather than synthesizing hover or drag.

- **`views/tiling/drag.ts` (step 0, pure refactor).** The `drag: {...}` blocks in
  `Files.tsx` and `Terminal.tsx` were **character-identical** (verified by diff), as were
  the four `*ById` helpers above them. Extracted `createTileDrag({snapshot, commit,
  setFocused})`; ~110 lines deleted. Landed and verified green on its own, because it is
  what makes every later change land in both hosts at once.
- **`.pane-menu`** — a `⋮` in every tab bar opening a list-layout choice modal: split
  left/right/up/down, new tab, move, close. `TileCtx` gained `newTab` and `canMove`.
- **Tap-to-pick move** — `drag.picking` / `beginPick` on the same protocol, so a tapped
  move drives the identical `begin/over/drop/end` calls a mouse drag does and **neither
  host reducer changed**. Each candidate tile shows a centred plus of five 56x44 buttons.
- Divider hardening, and `(pointer: coarse)` sizing for the handle only.

### Findings worth keeping

- **`TileCtx.drag` was already the right abstraction.** Nothing in `begin/over/drop/end`
  is DOM-drag-specific, so the entire touch move/stack feature needed zero changes to
  `layout.ts` and zero to either host's reducers. The abstraction predated the requirement
  and paid for itself.
- **Gate ergonomics, never capability.** Pointer media queries answer for the *primary*
  pointer and re-evaluate live: a touchscreen laptop reports `fine` while still unable to
  hover, and folding a convertible flips the answer mid-session. So the `⋮` ships
  unconditionally and only hit-area sizing is gated. The 450ms dwell was undiscoverable
  with a mouse too — this is the app's first visible split affordance on any device.
- **The tab bar is not a touch knob** (user constraint, adopted as a rule). Headers stay
  at 27.59px on every pointer; reachability is bought with the menu, not with padding that
  would cost every screen a row of terminal. Measured in a real browser: the bar is
  27.59px with the `⋮` and 27.59px with it removed from the DOM — the button stretches to
  the bar rather than setting it. There is now a test asserting `@media (pointer: coarse)`
  contains no `.tab`/`.stack-tabs` rule, and it fails if someone re-adds one.
- **A `::before` overlay to widen the divider would have been a real bug.** A
  pseudo-element is not an event target, so events over it report `.divider`, and
  `ownerOf()` would have claimed pixels lying *inside* the neighbouring tiles' 20px edge
  strips — those columns would have silently stopped arming, breaking the mouse's only
  split gesture in the tiles most likely to be split. Grew `flex-basis` instead.
- **Picking state must be host-owned.** A module-level signal in `panes.tsx` survives a
  route change (Files -> Terminal would mount already-picking, with a source id from a
  tree it is not in) and survives between vitest tests in the shared module registry,
  where `cleanup()` unmounts the DOM but cannot clear it.

### Test traps hit (all of these would have passed while proving nothing)

- **`!e.isPrimary` would have disabled the whole suite.** jsdom's `MouseEvent` has no
  `isPrimary`, so the negation is true and every synthetic pointerdown becomes a no-op.
  Written as `e.isPrimary === false`.
- **A `pointerId` filter is vacuous in jsdom** — both sides are `undefined`, and
  `undefined !== undefined` is false, so a *broken* filter looks identical to a working
  one. The tests `Object.defineProperty` an explicit id on every event.
- **The `.split` container rect must be stubbed** or `rect.width` is 0, the new
  `if (!span) return` guard swallows every move, and the teardown tests pass against a
  drag that never ran. Each divider test therefore asserts the ratio *changed* first.
- **A stub can be internally inconsistent.** The grab-offset test first stubbed the handle
  centred at 220 in a 400px container while the ratio was 0.5 (centre 200), so "press
  without moving" legitimately resized and the test failed for the wrong reason.
- **The chosen "distinguishing state" was wrong.** The move test first used a terminal
  pane's `<select>` value to prove the pane moved rather than being rebuilt — but that
  value is a local signal that an honest move discards (panes unmount on every layout
  change; the reason `files/drafts.ts` exists). Rewritten against Files, where a pane's
  path is durable tree state visible in its tab.
- **Left and right produce identical axis assertions.** `.split.h` passes for both, so the
  side was covered only by asserting which child carries `.stack.focused` (the new pane
  takes focus).

All 8 fixes were neutralized individually — each deliberately broken, each confirmed to
fail its own test with the intended diagnostic, then reverted.

### Verification

`npm run build` (tsc + vite) clean; `npx vitest run` **255 passed** (was 240);
`go build ./...`, `go vet ./pkg/webui/`, `go test ./pkg/webui/` clean. Beyond jsdom, a
throwaway Playwright probe (installed in scratch, not in `web/package.json`) drove a
real touch-emulating browser: tap-split, tap-move, Escape-cancel, and the `⋮` staying
reachable with 6 tabs in an overflowing bar all verified, with the tab-bar height measured
directly. Two visual defects were found that way and only that way — the hint bar was
rendering behind the sticky app bar (`fixed` -> `absolute` against `.workspace`), and the
zone cross was laid out in percentages, which scattered the five buttons ~450px apart in a
full-height pane (now grid cells).

Note the previous entry's report that `views.test.tsx` could not be run because
`tiling/drag.ts` was untracked mid-refactor: that was this work in progress, and the suite
is green again.

### Preflight wired into the drop gesture

A drop now asks the BFF what it would do before doing any of it, and asks the USER only
when there is something worth saying.

- **Clean drop into empty space: no dialog at all.** A prompt on every drop is a prompt
  nobody reads, so the confirm appears only for an overwrite, a merge, a refusal, or a
  warning. That restraint is the feature; a preflight that always interrupts would be a
  regression dressed as a safeguard.
- **Refusals report through the same path as failures.** `report()` handles both, so
  "copied 0/1 — x: not found" reads the same whether the answer came before the transfer
  or during it. The user does not care which, and two vocabularies for one outcome would
  be two things to learn.
- **Two refusals stay client-side.** A folder dropped into itself, and an item dropped
  where it already is, are decidable from the paths alone — saying so instantly beats a
  round trip to be told the same thing, and in the gesture's own words ("cannot drop a
  folder into itself", not "cannot copy a folder into itself").
- The drop now issues ONE batch request instead of one per item, and reports per item.

**Ghosts had to move ahead of the preflight.** Their whole job is that the pane admits a
transfer the instant it is dropped; putting them after the round trip made a drop look
like it did nothing until the server answered. They now go up immediately and are wiped
for whatever the preflight refuses or the user declines — it was never a file.

### Two test findings

**The mock was more permissive than the server, again.** Removing the client-side
self-drop guard meant preflight had to catch it, and the mock's preflight did not
implement that refusal — so a test passed for a case the server rejects. This is the third
time in this work that mock parity was the failure; it is worth treating "add a Go
refusal" as automatically implying "add it to `web/src/mock/fs.ts`".

**A shared-state test collided with the new behaviour, and was right to.** "marks what a
drop just delivered" dropped a name an earlier test had already put there, so under the
new rules it became an overwrite, raised a dialog nobody answered, and the transfer never
ran. The mock filesystem is module state shared by the whole file. Rather than paper over
it, the new overwrite test creates its OWN folder so both halves are deterministic
regardless of ordering, and the older test waits for either outcome before deciding
whether to answer.

### Neutralization

| broken | diagnostic |
| --- | --- |
| confirm always returns true | `Unable to find role="dialog"` |
| ghosts added after the preflight | `expected null to be truthy` — no ghost during the round trip |

Gate: `gofmt`/`build`/`vet`/`go test ./...` clean; `tsc --noEmit` clean; vitest 256/256;
`npm run build` clean with `pkg/webui/dist/.gitkeep` restored.

### The mock was a stub, not a mirror — and two gates it exposed

Called out that the mock had not really been enhanced. Fair: the endpoints existed but
were façades. `route` was hard-coded `"relay"`, so the feature's whole point — that a
client-side transfer costs no round trip — was invisible in the dev server and unexercised
by tests. `native`, `why` and `truncated` were never emitted. Worse, `readOnly` and
`refused`, which this work added to the Go wire shape for H14, were emitted by neither the
mock NOR consumed by the UI.

That last one made an earlier claim in this journal only half true. H14 said refusals are
"reported to the UI rather than a root silently vanishing" — the BFF reported them, and
nothing showed them. `getFsRoots` turned out to be **exported and never called**: the
explorer builds its root listing from `listDir("")`, not `/fs/roots`. So the field was
sitting in an endpoint the SPA does not read.

Fixed by putting the information where a user actually looks: `refused` and a per-mount
`readOnly` now ride the VIRTUAL ROOT LISTING (`fsListing.Refused`, `fsEntry.ReadOnly`).
The explorer renders refused sources as inert rows carrying their reason, and marks a
`:ro` mount with a badge — the kernel holds the container to it, so learning that from a
403 after a drag is learning it too late. The mock grew a genuinely read-only root and a
refused entry so both paths are reachable, and it now computes `route`/`native` from
whether both ends are local rather than asserting a constant.

### Same-path and ancestor gates

A copy onto itself answered `200 {"result":"ok"}` having rewritten the file with its own
contents — a success report for work that never happened. Both the operation and the
preflight now refuse it, and the preflight distinguishes the gesture that actually
produces it: "x is already in this folder" rather than the technically-true but unhelpful
"the source and the destination are the same file".

The folder-into-itself gate was worse than it looked. It compared **fsQuery fields**, so
two names for one directory read as two filesystems: with `./data:/data`, copying
`mount0/tree` into `web/data/tree/a` is a folder into its own subtree, and the old guard
saw an ordinary cross-mount copy and walked into what it was writing. It now compares
RESOLVED paths (`sitePath`) and holds for the source being ANY ancestor of the
destination, not just the immediate parent. `sameMountFs` is retired; comparing query
fields is precisely the mistake.

Neutralizing it was instructive twice over: the first attempt left dead code after a
`return` and only produced a compile error, which proves nothing. Done properly — restoring
the spelling comparison — the aliased cases fail, and a same-mount move degrades to a raw
502 `EINVAL` from the kernel rather than a clear refusal, which is what the guard is
buying.

| broken | diagnostic |
| --- | --- |
| compare spellings, not resolved paths | aliased subtree copies allowed; same-mount move → 502 `invalid argument` |
| same-path check removed | self-copy answers 200 "ok" having done nothing |
| mock preflight without the refusals | a drop the server rejects passes in the SPA |

Gate: Go `gofmt`/`build`/`vet`/`test ./...` clean; `tsc --noEmit` clean; vitest 259/259;
`npm run build` clean.

## 2026-08-02 — Settings: cards out, plain sections in

The Settings screen wrapped its two option groups in `.card` inside a `.cards`
grid. Replaced with plain `<section class="setting-group">` bands. Requested
follow-up mid-change: "I don't think we need dividers" — so the groups are NOT
`.section` either, whose 2px rule would have been the obvious reuse.

**Why a card was the wrong container here.** `.cards` is
`repeat(auto-fill, minmax(260px, 1fr))`, so it makes every panel narrower than the
page — correct for independent readouts meant to be scanned side by side (the
Overview's row), wrong for a setting, which is a sentence of prose with a control
on the end of it and wants the full measure. Measured both ways in Chrome at
1100px: a setting row was **303px** inside a card and is **1036px** now, a 3.4x
difference. Note it is 303 and not the ~518 a two-column reading of the grid
suggests — `auto-fill` (unlike `auto-fit`) keeps its empty tracks, so two cards in
a 1036px container still lay out on THREE 337px columns and the second card is
followed by a phantom third. Reason about `auto-fill` widths from the track count,
not the item count.

**Why not `.section`.** Its rule earns its place separating UNLIKE things (a
project from a project, Spec from Logs). Every Settings group holds the same kind
of thing, so the heading already marks the boundary. Recorded that distinction in
DESIGN_SYSTEM.md next to both classes, since the next person will reach for
`.section` by default.

Dropping the rule moves the entire grouping burden onto spacing, which makes it a
RATIO, not a constant: `--space-8` above a heading against `--space-3` below it.
The test asserts the relation, because asserting `margin-top: 32px` would still
pass after someone raised the heading's own bottom margin past it — the exact edit
that loses the grouping.

**The raggedness the card had been hiding.** With the panels gone, the four
settings did not line up: the two checkbox rows put their titles at x=56 (the
checkbox plus its gap), while the two select rows started at x=32. Under the old
flex `.setting-row` a row with no leading checkbox simply had nothing in front of
it. Fixed by making the row a two-column grid, `var(--space-4) 1fr`, with
`.setting-text` PINNED to column 2 — the column is reserved, not merely filled. All
four titles now measure x=56 in Chrome.

That pin is the load-bearing half, and it is invisible to a test that only checks
the grid exists: with `grid-column: 2` removed the tracks are still declared and
auto-placement still drops a checkbox-less row's text into column 1. Neutralization
was done in the browser, not just against the assertion — dropping the line put two
titles back at x=32 while the other two stayed at 56.

Also made the two kinds of row read in one order: name, description, then control.
A `select` inside `.setting-text` needs `align-self: flex-start` or the flex column
stretches it to the width of a paragraph of description.

Tests (`views.test.tsx`): the DOM shape (two `.setting-group` sections with `h2`
titles, no `.card`/`.cards`/`.section`, every row a `<label>` carrying
`.setting-text > .setting-title`), the spacing ratio plus "no border anywhere on a
settings group", and the reserved column. Hoisted the token-resolving `px()` helper
out of the `.kv` test to module scope rather than copy it.

Neutralization, all reverted and the reverts verified:

| broke | test failed with |
| --- | --- |
| `.setting-group` back to `.card`/`h3` | `[]` against `["Terminal","Workspace"]` |
| group gap `--space-8` -> `--space-2` | ratio assertion, 8 not > 12 |
| `border-top` added to the group | the no-border assertion |
| `grid-column: 2` dropped | the pin assertion — and x=32 in Chrome |

Verified in Chrome via the throwaway Playwright install (light, dark, and 390px
wide): no borders on either group, gaps 32/12, no horizontal overflow at 390px, no
page errors. `.setting-*` is Settings-only, so nothing else moved.

Gate: vitest 259/259; `tsc --noEmit` clean; `npm run build` clean;
`go build ./...`, `go vet ./pkg/webui/`, `go test ./pkg/webui/` clean;
`pkg/webui/dist/.gitkeep` restored after the build.

### The read-only badge drives the drag, not just the display

The badge and the drop handling now read ONE fact. Previously they could not have: a
`readOnly` flag rode individual ENTRIES, and an entry only carries it at the virtual root
— so a pane showing a folder deep inside a `:ro` mount had no way to know it was
read-only, and would happily accept a drag it could only answer with a 403 after the
release.

`fsListing.ReadOnly` fixes the level mismatch: writability is a property of the DIRECTORY
a pane is showing, which is exactly what the pane has. The bind-redirect branch ORs in the
site's own bit, because a `:ro` bind whose source sits inside the project resolves into
the writable `project` root — the mount is read-only where the root is not.

Three consequences, all from that one accessor:

- **`acceptsDrop` refuses**, so the refusal happens at the CURSOR. Not calling
  `preventDefault` on dragover is what marks a non-target, so the browser shows "no drop"
  and never fires a drop event at all — the honest place to say no.
- **`onDrop` refuses too**, as defence in depth: a file row is not its own drop target, so
  its event falls through to the pane, and the pane may be the read-only one.
- **The pane header carries the badge**, because a cursor that says "no" without a reason
  is just a broken drag.

Uploads from the desktop are refused on the same test — the old comment claimed one
"always lands", which stopped being true.

The mock grew the matching listing flag, without which none of this is reachable in a
test. That is now the fourth time in this work that mock parity was the gating step; the
pattern is reliable enough to state as a rule: **a new field on a Go wire shape is not
done until `web/src/mock/fs.ts` emits it.**

| broken | diagnostic |
| --- | --- |
| `acceptsDrop` ignores `readOnly` | `expected true to be false` — dragover marked the read-only pane a drop target |
| `onDrop` guard removed | no "read-only" toast; the drop proceeded |

Both halves fail independently, which is the point of testing the cursor and the release
separately: either one alone would leave a hole.

Gate: Go clean; `tsc --noEmit` clean; vitest 261/261; `npm run build` clean.

## 2026-08-02 — BFF: the 10 MB cap is gone from transfers

`maxEditableFileSize` bounded every copy, so 10 MB was the largest thing the explorer
could move and a tree containing one big file failed partway, leaving behind what it had
already written. That bound belongs to the EDITOR — a compose file is tiny and a text box
has to hold it — and it had leaked into the transfer path.

`fsstream.go` replaces the `[]byte` round trip with `openStream` / `createStream` /
`streamFile`. Three things were harder than "use io.Copy":

**The size must be exact, and must come from the right place.** A tar entry is framed with
its size BEFORE its bytes, so a source that grows under the copy produces a VALID archive
silently truncated at the promised length, and one that shrinks produces an unterminated
entry. `copyExactly` makes both an error, padding a short read first so the frame stays
well-formed — `tarcopy.packFile` does the same thing for the same reason. And the size
comes from `openStream`, never from the listing: a listing reports a symlink's own Lstat
size, which is the length of the link TEXT, so framing with it would truncate the file to
a couple of dozen bytes.

**A local destination is published by rename.** The container extractor removes the
destination BEFORE extracting into it, so an aborted transfer destroys the old content as
well as failing — a window that went from 10 MB wide to unbounded the moment the cap came
off. The local path now writes a sibling temp with `O_EXCL` and renames on a clean Close.
The neutralization is stark: writing in place leaves the destination `""`.

**Pipe ownership decides whether errors are seen at all.** `CopyFrom` checks its
stream-error trailer only after draining the body to EOF, so the read side's failure
reaches a caller only through `Close`. `streamFile` therefore checks `rc.Close()` rather
than deferring and discarding it. On the write side, a `CopyTo` that returns WITHOUT
reading — an early 404, a 501, an auth failure — leaves the writer blocked on a pipe
nobody drains; `CloseWithError` on every exit path is what keeps that from wedging a
goroutine and the request forever. The existing fake could not express that case because
it always drained, so `nonReadingFS` was added specifically to reproduce it.

Also fixed while here: `FsOpen`'s container branch capped **unconditionally** rather than
on `bounded`, so a container DOWNLOAD was capped too.

### A consequence worth chasing

The preflight had a warning that an oversize relayed file "will be refused: capped at N
bytes per file". True when written, false now — a preflight that threatens a limit that no
longer exists is worse than one that says nothing. It is replaced by a COST warning: above
64 MB a relayed transfer says it is being streamed through this process rather than copied
in place, which is still true and is the difference between a slow transfer and a
mysterious one. Silent below the threshold and silent for non-relay routes, because a
warning on transfers that are cheap is the noise that makes real warnings unreadable. The
test asserts both halves, and that no stale cap language survives anywhere.

### Neutralization

| broken | diagnostic |
| --- | --- |
| cap restored in the copy path | `copy of a 10489863-byte file: 413 file too large` |
| write in place instead of temp+rename | `an abandoned write disturbed the destination: ""` |
| `io.Copy` instead of exact framing | a growing source is not an error; a shrinking one leaves the frame short |

### Still open, narrower than before

Uploads remain capped (`fs_handlers.go`), so the same drag gesture succeeds as a copy and
413s as an upload. The container WRITE still publishes in place — the local side is safe
now, but doing the same inside a container needs a temp name plus a rename there, i.e. an
exec, which would make a plain write depend on a shell the image may not have (H13). And
incus still buffers whole files in server memory. All three are in TODO.md.

### Gate

Go: `gofmt`/`build`/`vet`/`test ./...` all clean. SPA: the Files explorer suite is clean
(114 passing, 0 failures); five Terminal/tiling tests are failing from another agent's
in-flight refactor of `drag.ts`/`panes.tsx`/`Terminal.tsx` (modified 14:15-14:17, after
this work's last green run at 14:10). Not touched, not "fixed" — that would collide with
work in progress.

## 2026-08-02 — FS transfer work: consolidated findings (and a note on where its sections went)

### First, a correction to this file's own structure

Four `###` sections of this work were appended while another agent was appending `##`
entries of theirs, so they landed UNDER headings they have nothing to do with. For the
record, these belong to the BFF file-transfer work, not to the entry above them:

- "Preflight wired into the drop gesture", "Two test findings", "The mock was a stub, not
  a mirror", and "Same-path and ancestor gates" sit under **"Tiling chrome: making pane
  splitting reachable on touch devices"**.
- "The read-only badge drives the drag, not just the display" sits under **"Settings:
  cards out, plain sections in"**.

Nothing is edited — the rule is append-only and their entries are theirs. But a reader
skimming headings would attribute those findings to the wrong change, which is exactly the
kind of quiet wrongness worth one paragraph to prevent.

**The reusable lesson: in a journal several agents append to concurrently, a contribution
must be a `##` entry.** A `###` section only means what its parent says it means, and you
do not control what your parent turns out to be.

### What this work delivered, in one place

A unified FS operation planner for the web BFF, then everything that hung off it: the
client-local bind redirect (`site()`), `FsMove`, batch copy/move with per-item results, a
non-mutating `/fs/preflight`, preflight wired into the drop gesture, read-only enforcement
end to end, and streaming that removed the 10 MB transfer cap. Six pre-existing defects
were fixed along the way (H4, H6, H7, H14, the self-copy no-op, the spelling-based subtree
guard), and three caretaker defects were found and filed without being fixed.

### Findings that generalise

**When you remove a limit, grep for whatever DESCRIBES it.** Streaming deleted the 10 MB
cap; the preflight kept warning that an oversize file "will be refused: capped at N bytes
per file". A report that threatens a limit which no longer exists is worse than no report
— it is confidently wrong, and nothing fails. Documentation of a constraint is part of the
constraint's blast radius.

**Put a fact at the level its consumer holds.** `readOnly` rode individual ENTRIES, and an
entry only carries it at the virtual root — so a pane showing a folder deep inside a `:ro`
mount could not know it was read-only, and accepted drags it could only answer with a 403
after the release. Moving it onto the LISTING fixed the badge, the cursor, and the drop
guard at once, because a pane has a listing. The bug was never in the logic; it was in
which noun the flag was attached to.

**A guard that compares SPELLINGS is not a guard.** Two names for one directory — a local
root and the bind mount that exports it — defeated both the same-file check and the
folder-into-itself check. Identity has to be a resolved absolute path. Worth noting that
this exact class was flagged in review BEFORE any code was written, and I then wrote it
again in new code (the preflight's first self-copy check). Knowing about a hazard does not
prevent writing it; the thing that catches it is a test, not vigilance.

**Mock parity is not optional, and it is the reliable failure mode.** Four separate times
the gating step was `web/src/mock/fs.ts` being more permissive than the server: the missing
`/fs/move`, the self-drop refusal, the preflight refusals, the listing's `readOnly`. Each
time a test passed for a case the server rejects. Stated as a rule: **a new field or
refusal on a Go wire shape is not done until the mock emits or enforces it.**

**Endpoints can be dead without anyone noticing.** `getFsRoots` was exported, typed, and
never called — the explorer builds its root listing from `listDir("")`. Two fields were
added to it for a security fix and reached nothing. Adding a field to an endpoint proves
nothing about whether it is read.

### Two things about neutralization, both of which recurred

**A neutralization that fails to fire is itself a finding.** Removing the read-only-source
gate left its test green, because without the gate the copy still runs and only the delete
fails — same status code, same surviving source, and a half-finished move on disk. The
test was asserting the wrong things. Default assumption when a check is removed and the
test still passes: the test is weak, not the check redundant.

**A compile error is not a neutralization** — and it is easy to produce one by accident.
Twice a scripted edit left dead code after a `return` or an unused import, which proves
only that the file no longer builds. Both had to be redone so the code compiled and the
behaviour changed.

### Working in a tree another agent is editing

The SPA suite broke under me twice from in-flight work I did not own. What made that cheap
to diagnose, and worth repeating:

1. **Which suites fail.** All failures in Terminal/tiling, none in Files, is a strong
   signal before any file is opened.
2. **File mtimes against your own last green run.** `drag.ts`/`panes.tsx`/`Terminal.tsx`
   modified at 14:15-14:17 when my last clean run was 14:10 and my edits since were Go-only.
3. **`git status` for staged-vs-unstaged**, which distinguishes "landed" from "mid-edit".

And the decision that follows: do not fix it. A half-finished refactor is not a bug report,
and "helpfully" completing someone's import would collide with the work in progress. Report
which suites are unaffected, verify your own files typecheck in isolation, and say plainly
what could not be verified rather than reporting a green you did not get.

### What remains open

Uploads are still capped; the container write still publishes in place (the local side is
safe); incus still buffers whole files server-side; `copyTree` still conflates "skipped"
with "failed" for symlinks; nested-mount traversal still does not re-resolve per entry; and
the whole Phase 2 server site — including the three caretaker defects — is untouched. All
in TODO.md, none of it claimed as done.

## 2026-08-02 — "New pane" asks by pointing, not by naming a disposition

The Terminal's `term:new` ("New pane…", prefix `c`) opened a modal offering **Stack as
tab** / **Split → right**. It now arms the tap-to-pick overlay instead: every tile wears
the same five wireframe targets a move offers, and the one chosen is where the pane
appears — centre as a tab, edge as a split.

**Why the modal was the wrong instrument.** It asked the user to translate a picture into
two words, and then answered the half it could not name — the side — out of a preference
(`settings.newPaneSide`). Pointing says strictly more in one gesture: it names the tile as
well as the side, so a new pane can land anywhere on screen rather than always beside the
focused tile, and it needs no preference at all. It also reuses a gesture the workspace
already teaches for moving a pane, so it is one vocabulary instead of two.

**The protocol change is small because it was already the right shape.** `drag.ts` modelled
a rearrange as begin/over/drop/end and a pick as a boolean. That boolean became an intent —
`picking: Accessor<PickIntent | null>`, `"move" | "place"` — and `drop()` branches once at
the top: a placement has no source, so every tile is a candidate and nothing can be dropped
on itself. `beginPlace()` arms it; `placeAt()` runs the same `addTab` / `splitPane` the pane
menu's own entries run. Hosts pay one line (`fresh: freshData` in the ops), and `panes.tsx`
only had to stop assuming a pick has a source. **No new UI was built for this.**

### What the user caught that the tests could not

Two rounds of feedback, both about things a green suite is structurally blind to.

**"It isn't keyboard navigable."** True, and the deeper point is the general one: *a mode
replacing a dialog inherits the dialog's keyboard contract.* A modal is operable without a
pointer by construction — it takes focus, arrows walk its options, Enter commits, Esc
leaves. An overlay of buttons is not, and shipping one in a modal's place quietly trades a
dialog anyone can drive for a target only a pointer can reach. `PickBackdrop` now owns all
four: it takes focus on `.stack.focused`'s first target when it arms (the palette command
that armed it leaves focus inside a terminal, which eats Tab — without this there is no
first step), arrows walk the plus within a tile, Tab crosses tiles and wraps, Enter is the
browser's own `<button>` activation, and Esc cancels and returns focus to the opener — only
on cancel, since a completed pick has already moved focus to what it made.

The arrow walk is a 3x3 cell walk rather than a neighbour table, and that choice is
load-bearing: the source's own tile has **no centre** (stacking a pane where it already is
is a no-op), so `Right` from its left arm has to *step through* the hole to the far arm
rather than dead-end on it. Written as a table of neighbours, that hole is a wall.

**"The guide wireframe gets rendered over the 'Tap where…' callout."** Two collisions in one
screenshot, and only one of them was the one reported:

1. The hint lived *inside* the scrim. The scrim is deliberately **below** the tiles'
   overlays (z 3 vs 7) so a tap reaches a target instead of the cancel — and a child cannot
   climb out of a positioned parent's stacking context, so the hint was tinted over by
   whichever candidate tile lay beneath it. Fixed by making it a **sibling** at z 8. The
   z-index alone does not state the rule; the DOM relationship does, so the test asserts
   both.
2. `.pane-drop-label` and the pick cluster both centre on the region they describe, so
   during a pick the words render straight through the middle button ("⊞ Ne⊞ tab" in the
   screenshot). The filled region still previews — that is the point of it — but the label
   is now suppressed while picking: a label names a region you are merely hovering, and a
   pick already has a named button under the finger.

A third defect surfaced from the same browser pass, unreported: the focused target had **no
visible ring**. The app-wide ring is `box-shadow: 0 0 0 3px var(--accent-subtle)`, and the
pick overlay's backdrop *is* `--accent-subtle` — the ring was invisible against precisely
the control that had just become keyboard-driven, and hover's fill is that same tint, which
would have made the aimed target blend *into* the backdrop instead of out of it. Restated
locally as a solid `--accent` ring over the elevated fill.

None of the three is visible to jsdom, which lays nothing out and has an empty CSSOM for
`styles.css`. They were found by driving the real thing in Chromium (throwaway Playwright in
the session scratchpad, never added to `web/package.json`) — prefix, `c`, arrow, Enter — and
looking at the screenshot. **The suite could not have caught any of them, and it went green
through all three.**

### Test isolation defect found on the way

`modalRequest()` is a module singleton, and `cleanup()` only tears down the DOM. Several
tests deliberately end with a question on screen, which left the request armed for the next
test — invisible until a test asserts that NO dialog is open, which is exactly what the new
placement test does. Added `dismissModal()` to the file's global `afterEach`.

### Neutralization

| broke | test failed with |
| --- | --- |
| `placeAt` edge branch → `addTab` | both "splits to the left/right" tests, the cancel test's follow-up, and the arrow-commit test |
| the initial `focusZone` on arm | all four keyboard tests — every one of them starts from focus |
| arrow step direction inverted | "walks the cross" and "steps across the missing centre" |
| cell walk cut to a single step | "steps across the missing centre" alone — the hole is the only shape that distinguishes them |
| the `Tab` branch | "crosses tiles with Tab" (arrows-stay-put still passed, correctly) |
| `prevFocus` restore | "gives focus back to the opener" |

One neutralization **failed to fail**: removing `if (placing()) return true;` from
`offersTargets()` broke nothing, because the two exclusions below it compare against a null
source and are vacuous anyway. Rather than keep a line the suite cannot see, it was rewritten
as `if (!src) return true;` — a real null-guard that also narrows the type for the lines
after it, and states the rule instead of arriving at it by accident.

### Left undone, deliberately

Files still asks with the modal (`openInNewPane`, keyboard path), so `pane-placement.ts` and
the **New pane placement side** setting survive — the request named the Terminal screen.
That setting's description was corrected to say so rather than left claiming a prompt the
Terminal no longer shows. Converting Files retires all three; in TODO.md, including the one
wrinkle (Files places a pane carrying the *file's* data, not `fresh()`, so `beginPlace`
would need a per-pick factory rather than the ops-level one).

## 2026-08-02 — The pick mode's keys: a direction IS the answer

Follow-up to the entry above, from two rounds of user feedback on the same change.

**"Users don't want focus navigation with the cursor keys; when they press the up arrow
(or CTRL+P, k), then just put it above."** The first cut had arrows walking focus around
the plus with Enter to commit — a faithful port of how a modal's options work, and the
wrong shape here. The mouse never had to *select* the top zone before pressing it, and a
question whose entire answer is a direction should not cost two keystrokes. So `↑` now
places the pane above and the mode is over. Three spellings of each direction, because this
is a tmux-shaped workspace: arrows, readline's `Ctrl-P/N/B/F`, vi's `k/j/h/l`. `Space` (and
`Enter`) is the centre — "stack here".

That collapsed a good deal of machinery: the 3x3 cell walk and its step-through-a-missing-
centre rule are gone, and the hole they existed for became a one-liner — the centre has no
button on the tile a move came from, so `Space` there finds nothing and is inert.

**What the model change forced elsewhere.** With directions acting rather than aiming, the
keyboard's unit of selection is the **tile**, not the target. Focus therefore moved to the
overlay (`tabindex="-1"`, as are the targets now): a ring around one button would promise
that a confirm commits that button. `Tab` chooses the tile; nothing else navigates. Which
tile is current shows as the whole cross coming up to full strength — and the tile's own
`.stack.focused` frame comes free, because focusing the overlay drives the tile's `focusin`.
The keyboard also lost its drop preview, correctly: there is no pending choice to preview.

The keys **press the button** (`overlay.querySelector(".pane-pick-zone.zone-" + z)?.click()`)
rather than re-deriving the drop. One handler, one `dropIdFor`, one set of rules about which
pane a tile reports — a second path would drift, and the absent-centre no-op falls out of it
for free.

**"After the new pane is created, move the focus to the first UI element of the new pane."**
This turned out to be a *pre-existing* bug the keyboard route merely exposed. `TermPane`'s
`PanePicker` already focuses its workload select on mount **if its pane is the focused one**
— and `drag.ts` was committing the tree and setting `focused` afterwards, so the picker
mounted while `focused` still named the pane it replaced, read false, and focus fell to
`<body>`. The Playwright run had been printing `focus: ""` after every placement and I had
not read it as a defect.

Fixed at the protocol, not the call site: all five commit paths now go through one `land()`
that sets focus **before** committing. The rule is now stated once — *a pane can only claim
focus on mount if it can already see that it is the focused one* — and the hosts' own
`splitAt` had been doing it in that order all along, which is why splits never had the bug.

### Neutralization

| broke | test failed with |
| --- | --- |
| `land()` back to commit-then-focus | both "puts focus in the new pane" rows |
| `ArrowUp` bound to `bottom` in `KEY_ZONE` | "places the pane with a single ArrowUp" alone — the other ten rows stayed green, which is the point of covering each spelling separately |
| `" "` dropped from `KEY_ZONE` | the two Space rows, the Space focus row, and the absent-centre test |
| direction acting on `all[0]` instead of the current tile | "acts on the current tile after 0 Tab(s)" |
| `Tab` not advancing | "acts on the current tile after 1 Tab(s)" |

**One neutralization found a hole in a test rather than in the code.** `press(all[0], z)` —
directions always acting on the first tile — passed the whole suite. The Tab test armed with
tile 1 current and Tabbed to tile 0, so the bug's hardcoded `0` was exactly the expected
answer. Rewritten as two rows, 0 and 1 Tabs, and the **no-Tab row** is the one that bites:
it is the only case where the current tile is not the first. Worth remembering as a shape —
a navigation test whose single step happens to land on the degenerate index proves nothing
about navigation.

## 2026-08-02 — Closing the gaps the FS transfer work left behind

Second pass over the items this session filed in `TODO.md`. Five closed, two closed in part
with the remainder stated, one left alone on purpose. The Go gate is clean (`gofmt`,
`build`, `vet`, `test ./...`) and the SPA suite is 294 passing across 14 files, including
the tiling work another agent had in flight earlier.

### The one that was not on the list: every container-source copy wedged

Before touching the filed items I wrote a test for the plainest thing the streaming work
claims — a file copied OUT of a container — and it hung for the full ten seconds.

`streamFile` both `defer rc.Close()` and `return rc.Close()`, deliberately: the read side's
error arrives only through Close (CopyFrom checks its stream-error trailer after draining),
so the result has to be checked, and the error paths still need the defer. But
`containerStream.Close` took from a channel its producer writes exactly once. The first
Close got the value; the second blocked forever, after the bytes had already landed. So the
transfer succeeded and the request never returned.

Two things worth keeping from this. First, **an idempotent Close was load-bearing, not
tidiness** — the reason is in the comment now, because the next person to read
`defer rc.Close()` next to `return rc.Close()` will otherwise "simplify" one of them away.
Second, and more uncomfortable: this was reachable by the most obvious test anyone could
write for the feature, and the feature shipped without it. The suite had the deadlock case
(`TestContainerWriteDoesNotDeadlockWhenNobodyReads`), the truncation case, the symlink
case, the size-change case — every failure mode, and no success. **A suite made of
adversarial cases can miss the happy path precisely because it feels too obvious to write
down.**

### `skipped` now means what it says

H7's remaining half. `copyTree` decided skip-versus-fail from `e.Kind`: a symlink entry was
a skip on ANY error, a regular file was never one. So a link to a perfectly good file whose
transfer died halfway was reported as deliberately stepped over — and `FsMove`, which keeps
the source whenever anything was skipped, said so while leaving a truncated destination
nobody was told about.

The classification moved to where the file is opened, as a `skippable` wrapper around
exactly four cases: a directory reached through a symlink, a device/FIFO/socket, a link
entry whose archive header carries no body, and an entry that VANISHED between the listing
and the copy. `copyTree` steps over those and fails on everything else.

**The generalising bit is where the decision lived, not what it decided.** `e.Kind` is what
the walk knows; whether an entry is transferable is what the SOURCE knows. Deciding it from
the listing meant the answer was a guess dressed as a classification — and it guessed
wrong in both directions at once: a failed symlink transfer was called a skip, and a FIFO
in a tree (which lists as an ordinary "file") aborted the entire copy. One misplaced
decision, two opposite bugs.

H8's other half came free: a child that disappears under a minutes-long walk of a live
directory is now a skip rather than the end of the tree.

### Uploads stream, and a body with no length is refused

The same drag gesture succeeded as a copy and 413'd as an upload, because
`handleFsUpload` still rode through `io.ReadAll` under the editor's bound. It now goes
through `createStream` + `copyExactly` like everything else.

The interesting decision was the unknown-length body. A container destination is a tar
entry framed with its size before its bytes, so there are only two options: refuse, or
spool the whole thing somewhere to find out how big it is. **Spooling would have written
the file twice to answer a question the client already knows the answer to**, so it is a
411 with a message that says why. Every real caller carries a length already — a browser
`File` body sets Content-Length, a multipart part carries `hdr.Size`.

### A bound belongs to its caller

The container listing shared `maxToolCapture` with the terminal, and that was the whole
defect: 256 KiB is generous for tool output and runs out at a few thousand directory
entries — which `copyTree` turns into a 413, so a container directory of a few thousand
files could not be COPIED at all, long after the per-file size cap came off. `Exec` grew a
`limit` parameter and the listing passes its own.

Two notes. **The fake had to start applying the limit too.** It ignored the parameter at
first, which would have made every truncation test pass without exercising anything — the
same mock-parity trap this session hit four times on the TypeScript side, in a Go test
fake. And this is a **widening, not a removal**, so the entry says so: removing the bound
means paging the glob loop, which costs a round trip per page and can tear when the
directory changes underneath. Recording "8 MiB instead of 256 KiB" as *done* would have
been the more comfortable lie.

### incus: the direction that could be fixed, and the one that could not

`packOne` no longer holds each file in memory; it spools through an unlinked temp file to
learn the size its tar header needs. The test measures `TotalAlloc` across a 64 MB
synthetic body — neutralized, it allocates 165 MB.

`CopyTo` still buffers, **on purpose and permanently until someone can run it against a
real daemon**. `incus.InstanceFileArgs.Content` is an `io.ReadSeeker` the client re-seeks
from `GetBody` to replay a retried request, and `http.NewRequest` length-frames a body only
for `*bytes.Reader` and friends — hand it an `*os.File` and the upload silently becomes
chunked. Fixing the allocation would change the wire framing in the same edit, against a
daemon `go test ./...` cannot reach. **"Both directions buffer" is a symmetrical-sounding
statement about two asymmetrical situations**: one is our own tar format, the other is
somebody else's HTTP contract.

### The caretaker had two accept loops; now it has one

yamux hands each stream to exactly one `AcceptStream` caller, and `runPortForwardAccept`
and `serveIngress` both called it, each closing tags it did not recognize. A pod carrying a
hub role with delivery targets AND a PortForward role raced for every inbound stream and
dropped roughly half of each kind — **with nothing logged, because closing a foreign tag is
the correct move for a loop that only owns one tag**. Every participant behaved correctly
and the system did not.

`dispatch.go` is the single loop; roles register a handler for their tag, and
`runCaretakerConn` wires them all before any of them starts, so no stream can arrive for a
tag whose role has not registered yet. The two functions survive as single-role
conveniences, which is what their existing tests drive.

**The test needed 20 streams of each tag, not one.** One of each would pass half the time
under the old design, and a regression test that passes half the time is worse than none —
it converts a certainty into a flake and teaches whoever hits it to re-run. Neutralizing it
(two dispatchers on one session) drops streams in droves.

### The mock was more permissive than the server, again — the fifth time

The read-only gate was honoured by the batch transfer and by nothing else, so a single-item
copy, an upload, a rename or a delete into a `:ro` bind all "succeeded" in the mock while
the BFF answers 403. `src/mock/fs.test.ts` now drives `handleFs` directly for all seven
mutations, plus the paired positive (a copy OUT of a read-only mount must still work, or
the seven negatives would pass against a mock that simply refused everything).

Five occurrences in one session is not five mistakes, it is a **missing rule**: the mock is
a fixture, so anything it permits that the server refuses gets certified as working by
every test that runs against it. Worth stating in `web/src/mock/fs.ts` itself as the file's
contract, not just remembered.

### Left alone, and why

The kubernetes `cfg.Instance` defect (the caretaker item's half b). Always setting it would
make single-replica kubernetes register, and would bake in a key that is wrong for every
multi-replica Deployment — which exposes no ordinal, so `InstanceKey(name, 0)` is not
merely unset, it is unavailable. A per-pod key from the downward API also changes what the
server can ASK for: "instance 0" stops being addressable, so `pkg/remotecompanion` and the
`ForwardPort` / exec agent-relay lookups need a by-deployment or by-pod model first. That
is a design question, and half-doing it would entrench the wrong key while looking like
progress. Also still open, both already recorded with their reasons: the container write
publishes in place (a temp-plus-rename inside the container needs an exec, which H13 rules
out), and the listing bound is wider rather than gone.

## 2026-08-02 — Addendum to the new-pane work: one claim verified, one trap recorded

Two things the two entries above either asserted without measuring or left out entirely.

**Verified: the pre-existing split paths never had the lost-focus bug.** The keyboard entry
claims the hosts' own `splitAt` had always set `focused` before committing, "which is why
splits never had the bug". The ordering is plain in the source — every creation path in
`Terminal.tsx` and `Files.tsx` (`splitFocused`, `splitAt`, `newTab`, the `?workload=`
arrival, Files' `openInNewPane`) writes `focused` and then `commit`s — but the *consequence*
was an inference, not an observation. Measured it in Chromium: after the pane menu's
"Split ↓ down" and after the mouse edge-split dwell, `document.activeElement` is the new
pane's workload `<select>` in both cases. The claim stands as written. (One path is
deliberately the other way round and is not a counter-example: `closePaneById` commits then
focuses, because the pane it focuses is a survivor that never remounts.)

**A trap in the `block(cssSource, header)` technique.** Adding a `.pane-pick-keys` rule under
its own `@media (max-width: 720px)` block broke an unrelated test — `.table-scroll` scrolls
horizontally under the mobile breakpoint — with a diff showing it had been handed the
pick-hint rules. `block()` is `css.indexOf(header)` plus brace matching, so it returns the
**first** block whose header matches, and `styles.css` had exactly one
`@media (max-width: 720px)` (line 2144) until a second one appeared 900 lines earlier.

Two things follow, and the second is the useful one:

- The single-breakpoint convention in DESIGN_SYSTEM.md is not only a style rule; a chunk of
  the stylesheet test suite silently depends on it. Any test that reaches for a block by a
  non-unique header is aimed by document order.
- **The failure was loud and in the right place, which was luck.** It surfaced as a
  neighbouring test failing on content it never mentioned. Had the new block been added
  *after* line 2144 instead of before it, everything would have stayed green and the new
  rule would simply never have been asserted by anything. A helper that resolves a
  non-unique key by "first match" fails silently in exactly half of its misuses.

The rule itself no longer needs a breakpoint — the keys line is a block under the question,
so the pill's width is set by whichever line is longer at any width.

## 2026-08-02 — Neutralization ledger for the gap-closing pass

Companion to the entry above ("Closing the gaps the FS transfer work left behind"), which
describes what changed and why. This one records the evidence, because a claim that a test
was neutralized is worth exactly as much as the diagnostic it produced — and because
CLAUDE.md's rule (a COMPILE error is not a valid neutralization) is easy to satisfy on
paper and easy to fail in practice. Every row below was produced by changing a live
condition or a value, never by deleting a symbol.

| fix | how it was defeated | what the test said |
|---|---|---|
| idempotent `containerStream.Close` | `if c.closed` -> `if false` | `wedged: the reader was closed twice and the second close never returned` (10 s) |
| FIFO is skippable, not fatal | dropped the `skipEntry` wrapper on "not a regular file" | `a fifo beside a regular file must not fail the tree: 400` |
| skip/fail decided by the source | restored the `e.Kind != "symlink"` clause | `a failed transfer must not be reported as a skip: 200 {"result":"ok","skipped":["tree/link"]}` |
| uploads stream | re-added a `maxEditableFileSize` bound to `uploadStream` | `upload of a 10489863-byte file: 413 too large` |
| unknown length is a 411 | `if size < 0` -> `if false` | fell through to the framing check and answered the wrong status |
| listing has its own bound | listing passes `maxToolCapture` again | `a 9000-entry directory still reports truncated` / `listed 6898 entries, want 9000` |
| incus read streams | `spoolToDisk` -> `io.ReadAll` + `bytes.NewReader` | `copying a 67108864-byte file allocated 165371032 bytes: it is being buffered whole` |
| one accept loop per session | ran a second dispatcher on the same session | 20+ subtests failing `read echo: EOF` |

Two of these are worth reading twice.

**The `skipped` row is the whole bug printed in one line.** `200 {"result":"ok","skipped":["tree/link"]}` for a transfer that failed is not a test failing — it is the old behaviour
confessing. That is what a good neutralization buys: not "the assertion fired" but a
verbatim record of the wrong answer the code used to give, in the response body the user
would have seen.

**The 411 row is the weak one, and it is recorded as weak.** Defeating the length check did
not produce a clean "the guard is gone" diagnostic; the request fell through to
`copyExactly`, which noticed the body outran its declared size and answered 409. The test
still fails, so the guard is load-bearing — but it fails for an adjacent reason, which
means the test pins "an unknown length is refused" less tightly than the others pin their
claims. Left as is, noted rather than dressed up.

The one gap in the ledger: the **mock parity** change (`src/mock/fs.test.ts`) has no
neutralization row, because there is nothing to defeat — the seven refusals were absent,
not wrong. The paired positive (a copy OUT of a read-only mount must still return 200) is
what keeps those seven from passing against a mock that simply refused everything, and it
caught a real mistake on the first run: it was written against a fixture file that does not
exist and returned 404, which would have been a green negative test sitting next to a
vacuous positive one.

**Verification at the end of the pass**: `gofmt -l cmd/ pkg/` silent, `go build ./...`,
`go vet ./...`, `go test ./...` all clean; SPA `tsc --noEmit` clean, `npm run build` clean,
294 tests across 14 files passing (including the tiling suites another agent had in flight
earlier in the session, now green). `pkg/webui/dist/.gitkeep` re-created after the vite
build removed it — same as last time, and still worth a `touch` rather than a `git`
command in a tree another agent may be working in.

## 2026-08-02 — "Split pane…": a third pick intent, and the focus it kept losing

Added a **Split pane…** entry to both workspaces' palettes (`term:split`, `files:split`),
alongside — not replacing — the existing `%` and `"` binds. Invoking it arms the same
wireframe the new-pane placement uses.

**Why a third intent rather than a flag.** `drag.picking()` now returns
`"move" | "place" | "split"`. The two creating intents answer different questions — "New
pane…" is *give me somewhere to start*, "Split pane…" is *show me two of this* — and both
of their differences fall out of that rather than being configured:

- The pane a split makes **continues the tile it divided** (same workload and command, same
  folder), where a placement starts empty. `splitPane` already takes the factory its target
  is fed to, so this is `ops.inherit` instead of `ops.fresh` and nothing else.
- A split has **no centre**. Stacking a tab is not a split, and offering one would make the
  command's own name a lie. That leaves the same four-arm cross a move shows on its own
  source tile, for an unrelated reason (the pane is already a tab there).

It inherits from the tile **pointed at**, not the focused one — which is the whole reason
the pick is worth having over the `%` bind, and is what the fourth test pins by aiming at a
connected tile while an empty one holds the focus.

Neither entry carries a tmux bind. Every letter that would fit is spoken for (`% " c x` in
the Terminal; `s` is Save in Files, whose alphabet is contextual), and a bind that means
something else in the other workspace is worse than none.

### The defect this uncovered: an xterm stealing the keyboard back

Writing the "inherits from the tile it was aimed at" test, its *premise* assertion failed —
after `New pane… → right` beside a connected terminal, the new tile was not the focused one.
Checked in Chromium rather than guessing, and the browser agreed: `document.activeElement`
was the **old** terminal's `xterm-helper-textarea`.

`Term` called `term.focus()` unconditionally on mount. A split re-parents the tile it
divides, so that tile's `Term` is rebuilt — and it then took the keyboard back from the pane
the user had just asked for. The previous entry's fix (focus before commit) was necessary
and not sufficient: it got focus INTO the new pane, and a neighbour that merely happened to
mount last took it away again.

The general rule, now in DESIGN_SYSTEM.md: **"did I just mount" is never the guard for
claiming focus.** A tiled layout rebuilds panes it never touched, so anything that focuses
itself must ask "am I the focused pane" — which is exactly what `PanePicker` already did and
`Term` did not. `Term` gained an `autoFocus` prop and `TermPane` answers it with the same
guard.

Worth noting how close this came to shipping: nothing in the request, the suite, or the
first two rounds of browser checks pointed at it. It surfaced only because a NEW test's
premise happened to overlap it, and the premise line was there only because the test needed
two tiles that were unalike. A test asserting less about its own setup would have passed.

### Neutralization

| broke | test failed with |
| --- | --- |
| split using `ops.fresh` | "continues the tile it split" and "continues the tile it was aimed at" |
| split offering the centre | "offers no centre, because a tab is not a split" |
| `autoFocus={true}` (the old unconditional focus) | "keeps focus in the new pane when a live terminal is rebuilt beside it", plus the aimed-at test |

The focus-steal test asserts a live `.xterm` exists next door before concluding anything:
without a live neighbour the steal is not even possible, so the test would pass on an
unguarded build and certify nothing.

### Also

`connectPane()` moved from inside the Terminal describe to module scope — the new tests
needed a live session and it was the only thing standing in the way. No change to it.

## 2026-08-02 — Command tags, and the ⋮ menu that became a filtered palette

`Command` gained `tags?: string[]` — any number of named facets — and the palette filter
reads a `:name` token as "must carry this tag, exactly". The tile's `⋮` no longer holds a
menu of its own: it focuses its tile and calls `openPalette(":pane ")`. `pane-menu.ts` is
deleted.

**Why a tag and not another list.** A `group` says where a command renders (one section,
chosen by the screen); a tag says what kind of thing it acts on, and cuts across groups. So
`:pane` is *the pane operations of whichever screen is mounted*, however that screen chose
to word them — which is exactly what a per-tile menu wants to be. What the app had instead
was the same list twice: a command and a modal entry per operation, drifting at every
change. The palette version is also strictly better than the modal it replaces — searchable,
keyboard-driven, and each row shows its tmux bind, which is how anyone learns the binds
exist at all.

Exactness is the whole reason for the sigil. `pane` as a word matches the command about a
side *panel*; `:pane` cannot. And a tag is a whole name, not a prefix — `:pan` names no tag.
Tags are still ordinary search text, so a plain query finds them like `keywords`; the `:` is
what turns a guess into a requirement.

**The seed is a starting point, not a cage.** It is applied on mount and the caret is placed
past it, so typing narrows and deleting it widens to every command on the screen. Both
halves are tested; a seed that is ignored is a menu of everything, and one that cannot be
edited is a menu you cannot escape.

### What dropping the menu forced

The ⋮ menu carried more than the three commands named in the request, and the palette acts
on the FOCUSED pane rather than a tile handed to it. So:

- **`Move pane…` became a command** (`term:move`, `files:move`) — otherwise dropping the
  menu would have removed the touch-parity move added earlier this session. Its
  "only when there is somewhere to go" gate moved with it, from `TileCtx.canMove` to simply
  omitting the command; `canMove` is gone.
- **Files gained `files:new` and `files:close`** to match. Its ⋮ had reached new-tab / move /
  close only through the chrome's modal, so without them this screen would have quietly lost
  three operations while the Terminal kept them.
- **`TileCtx.newTab` is gone**, with both hosts' implementations: it existed for the menu's
  "New tab here", which "New pane… then Space" now does on any tile you point at.
- **The ⋮ focuses its own tile before opening.** Not left to the click — a `<button>` press
  does not move focus in every browser (Safari), and "Close focused pane" reading the wrong
  tile is not a defect anyone would forgive. The test for this closes tile 0 from tile 0's ⋮
  while tile 1 holds the focus, in Files, where the two panes are tellable apart.
- The two DIRECTIONAL split binds are deliberately **not** tagged. They are accelerators for
  a question `Split pane…` already asks, and a menu offering both the general form and two of
  its four answers reads as three unrelated things. The test asserts their absence from the
  menu and their presence once the seed is cleared — otherwise "four entries" would pass on
  a registry that happened to hold only four.

### Neutralization

| broke | test failed with |
| --- | --- |
| `:` token treated as ordinary text | the two tag-exactness tests, the seeded-palette test, and the ⋮ menu test |
| `initialQuery` ignored | the seeded-palette test and the ⋮ menu test |
| ⋮ not focusing its tile | "acts on the tile whose ⋮ was pressed" and three move tests that reach the mode through it |
| `Move pane…` offered unconditionally | "offers Move only when there is somewhere to move to" |

### A test that outlived its subject

"lays the pane menu out as a list, not a row of slivers" tested `promptChoice`'s
`layout: "list"` through the pane menu — the option's only caller. Rather than delete the
test with the caller, it now drives `promptChoice` directly: an untested option in a shared
service is one that breaks silently the day someone uses it again. The option is now
caller-less, which is recorded in TODO.md as a decision to make rather than a thing to
notice later. Retiring it properly means touching `modal.ts` and `ModalHost.tsx`, which
another agent had open.

## 2026-08-02 — E2E for the BFF file explorer, and the two arms that prove each other

The `/.cornus/web/fs*` surface had no E2E coverage at all: `web.star` asserts config,
workloads, graph, mounts, MCP and the three serving modes, and never touches the file
explorer. Added `e2e/scenarios/web-fs.star` plus `make e2e-web-fs`, documented in
TESTING.md, parse-checked by `make e2e-check`.

### What a real backend can say that a fake cannot

The webbff unit suite drives the explorer against a fake `containerFS`. That fake is good
at one thing — a fake whose every method calls `t.Fatal` is how "no byte reached the
daemon" is asserted, and there is no other way to prove a redirect happened. But it can
only ever confirm where a request was SENT. Three questions need a backend:

- Is the directory a container path was redirected onto the directory the container is
  actually reading? A redirect onto the wrong host directory satisfies every fatal-fake
  assertion and fails on the first `cat`.
- Does a refusal the BFF issues match the one the kernel would issue? A `:ro` bind where
  the BFF allows and the kernel refuses (or the reverse) is individually defensible on
  each side; only the agreement is the contract. This is the same shape CLAUDE.md records
  for `TestUTSHostnameAndHostsEntryAgree`.
- Does a transfer that SUCCEEDS actually return? See below.

### The discovery: the arms are complementary, not two flavours of the same test

I set out to write one scenario and the environment refused it: `compose up` with a
client-local bind fails as an unprivileged user with "client-local mounts require root
(CAP_SYS_ADMIN) to kernel-9p-mount". Every bind scenario in this suite is kube-only for
exactly that reason. Then, reading the kubernetes backend to see what the kube arm could
assert, the other half fell out: `StatPath`/`CopyFrom`/`CopyTo` all answer "cp/archive not
supported on the kubernetes backend; use kubectl cp".

So the two routes do not overlap anywhere:

- On docker/containerd/bare/incus the archive primitives work and the bind redirect is out
  of reach without root — **the relay is the only route**.
- On kubernetes the relay CANNOT work — **the redirect is the only route**, and it is not
  an optimization there but the entire reason the explorer is usable at all.

That reframed the scenario. Each arm now asserts its own positive AND the other arm's
negative: the kube arm requires that an image path REFUSES, the relay arm requires that a
container path with no bind behind it reports `route: "relay"`. Neither claim can quietly
stop being true without the other arm noticing. **A constraint I hit as an obstacle turned
out to be the structure of the test** — worth remembering as a move: when the environment
refuses a test, ask what the refusal is telling you about the system.

### The neutralization is the reason to believe any of it

The relay arm was run against a live docker backend and then broken deliberately: restore
the close-twice defect in `containerStream.Close` and it fails with

    scenario timed out ... Post ".../fs/copy?source=virtual&path=webfs-app/work/seed.txt":
    context deadline exceeded

naming the exact request that wedges. **This is the bug the entire unit suite missed** —
it owned every failure mode of a container-source copy and never the success — so the new
scenario has already demonstrated it catches a class the Go tests structurally could not.

Two honest notes on that. The failure mode is a `--scenario-timeout` expiry (10m default)
rather than an assertion, because a hung transfer has no answer to assert on; that is
already engineered for in the runner, and `CORNUS_E2E_SCENARIO_TIMEOUT` makes it fast to
reproduce. And the first neutralization attempt looked like a hang of my own shell
`timeout` rather than a harness failure — re-running it under the harness's own bound is
what turned "it hung and I killed it" into a diagnostic with the request in it.

### What is NOT verified, and why it is filed rather than claimed

The kube arm has never been executed: no kind cluster here, and no other target can carry
a client-local bind unprivileged. It is parse+resolve-checked, and the containerized
runner's default `e2e/scenarios/*.star` glob picks it up, so **CI is its first real run**.
Filed in TODO.md with the three least-trodden things to check first (an absolute external
`:ro` source, `stop()` on a compose-managed kube deployment, the pull+sidecar startup
budget) so a red kube leg has a starting point rather than a mystery. The JSON shapes it
reads were checked against the structs rather than guessed, so a wrong field name fails an
assertion loudly instead of skipping a block.

### Smaller things worth keeping

- **The project root is the compose file's directory** (`baseDir = dir(files[0])`), so a
  scenario that copies into `project/...` would write into the committed tree if its
  fixture lived in `e2e/scenarios/`. The whole project is synthesized in a `temp_dir()`
  instead — the compose file, the bind source, and everything a transfer creates.
- **Delete the fixtures, keep the compose file.** The harness reaps scenario workloads with
  a second `compose down` after the body; removing the file out from under it turns that
  redundant call into an error line that reads like a failure. Cost me one confusing run.
- `cornus:e2e` exists only on the kube target (built and kind-loaded), so the relay arm
  uses a public image — the first run failed on `pull access denied for cornus`.

## 2026-08-02 — prefix C for "Split pane…" (and a correction)

`Split pane…` is now bound to **prefix + Shift-c**, on both workspaces (`term:split`,
`files:split`). One shift away from the `c` that opens the other wireframe, which is the
point: the two commands are the same gesture asking two questions — *somewhere to start* vs
*two of this* — so a shifted pair says that better than an unrelated letter would. `C` was
free on both screens, unlike plain `c` (New pane in the Terminal, Copy in Files).

**Correcting the entry two above.** It said "Neither entry carries a tmux bind. Every letter
that would fit is spoken for (`% " c x` in the Terminal; `s` is Save in Files…)". That was
true only of UNSHIFTED letters, which I had silently taken to be the whole alphabet — the
prefix reducer has always allowed Shift through (it must: `%` and `"` are shifted
characters), so the shifted range was available the entire time and I did not look at it.

**One test-harness defect fell out of it.** `pressBind(key)` hard-coded `shiftKey: true`,
because the first binds needing it were `%` and `"`. With `c` and `C` now distinct commands
that is no longer harmless: a browser reports Shift+c as key `"C"`, so `{key: "c", shiftKey:
true}` is an event no keyboard can produce, and a test built on one demonstrates nothing
about telling the pair apart. `shift` is a parameter now, and the unshifted row passes false.

Neutralizations: removing the bind fails the `C` row; swapping the two binds fails both rows
— which is the property that matters, since a test asserting only "prefix C armed something"
would pass with the keys wired to either command. Verified in Chromium on both screens:
prefix+C gives the four-target split wireframe, prefix+c the five-target placement, and the
palette renders `C` as the accelerator beside "Split pane…".

## 2026-08-02 — The kube arm found a live bug on its first run

`kind`, `kubectl` and `docker` were already staged in `.agents-workspace/tmp/kube-e2e-bin/`
from an earlier session; they are not on PATH, which is why `which kind` came back empty
and the kube arm of `web-fs.star` shipped unverified. Put that directory on PATH, built
`cornus:e2e` from `e2e/container/appimage.Dockerfile`, `kind load`ed it, and ran the arm.
It failed immediately — and it was right to.

### The defect: every container listing on kubernetes showed the wrong directory

`containerList` ran its glob script with the requested path as the exec's **WorkingDir**.
Kubernetes cannot express that: `PodExecOptions` has no working-directory field, so the
backend logs `backend cannot honor exec option (the pods/exec subresource does not support
it) ... option=WorkingDir` and runs the command anyway. The glob therefore ran in the
image's default workdir and the BFF returned those entries **labelled with the path the
user asked for**. A request for `/etc` came back as the contents of `/`.

It is a silent, total defect of the container-browsing surface on one backend, and it
would have poisoned more than the listing: `copyTree` walks by listing, so a recursive
copy would have copied the wrong tree.

The fix moves the directory into the argv (`sh -c SCRIPT sh <dir>`, so the path is `$1` and
never interpolated into shell source) and asks for no working directory at all. The script
also classifies by **exit code** now — 3 not-a-directory, 4 missing — rather than by
matching shell stderr, which busybox and GNU word differently and which cannot distinguish
"missing" from "not a directory" in `cd`'s message anyway.

### Two things this says about the unit tests that missed it

**The fake agreed with three backends out of four.** Docker, containerd and bare all honour
WorkingDir; only kubernetes drops it. A fake modelled on the majority is not a neutral
stand-in — it encodes the majority's semantics as if they were the contract.

**Two existing tests pinned the mechanism rather than the contract.**
`TestExplorerContainerListing` and `TestExplorerVirtualNavigate` both asserted
`workdir == "/app"`. They were green, they were specific, and what they specified was the
broken thing. This is the CLAUDE.md lesson (audit the code, then write the test against
what it must DO) meeting its natural consequence: a test written from the implementation
inherits the implementation's assumptions, and the more precisely it is written the more
firmly it welds them in. Both now assert that the directory travels in the argv and that
**no** working directory is requested.

The new regression, `TestContainerListingSurvivesABackendThatDropsWorkingDir`, models the
kubernetes behaviour explicitly: the fake ignores `workdir` and falls back to `/` for
whatever the caller did not put in the argv. That last detail is what makes the
neutralization useful — reverting the fix fails with
`listing /etc returned [etc usr] — that is some other directory, served under the path that
was asked for`, the production symptom verbatim rather than a proxy for it.

### The scenario also corrected itself

My paired negative had asserted that an image path cannot be LISTED on kubernetes. False,
and the run said so with a 200 and a full listing. The split is finer than I had it:
kubernetes has no cp/archive (`StatPath`/`CopyFrom`/`CopyTo` all refuse) but it does have
exec — and the listing is a shell glob over an exec, so listing works and only the COPY
cannot. The scenario now asserts both halves separately, which is strictly more
informative: the image path must list, and the copy of it must fail.

Worth naming, because it happened twice in one afternoon: **the assertion I was most
confident about was the one that was wrong, and being wrong is what exposed the real bug.**
Had I written the weaker `assert_true(status != 200)` in a way that happened to pass, the
workdir defect would still be shipping.

### Verified state

Both arms now run green against real backends: the relay arm on docker (streamed out,
11 MB in and back out with matching checksums, upload, move, the H6 directory-clobber
refusal, `route: "relay"`), and the redirect arm on a kind cluster (both views agree, the
image path lists but cannot be copied, the `:ro` bind is browsable and unwritable **and the
kernel refuses the container too**, and the bind stays browsable with the workload
stopped). `go test ./...`, `go vet`, `gofmt` clean; `make e2e-check` parses all 158
scenarios.

## 2026-08-02 — Files gets the Terminal's pane binds; the arrows stop shouting

Three changes and one non-defect, all from the same round of feedback.

### The pane binds are now identical on both screens

`% " C c x` on Files as on the Terminal. A workspace is a workspace — the chrome, the
wireframe picks and the tile ⋮ are the same on both — and someone who learned `prefix x` in
one and found it deleting files in the other has learned nothing.

That cost this screen two letters, and there was no version of the request that did not:
`c` and `x` were **Copy** and **Delete**, which is exactly why the binds had NOT carried
over before (a test guarded that decision, comment and all). They moved to vi's **`y`**
(yank) and **`d`** (delete) — both free, both better mnemonics than the tmux-flavoured pair
they replace, and they sit beside the `hjkl` the pick mode already speaks.

Note which way the risk falls. Someone reaching for the old `prefix x` now closes a tab
instead of deleting files: the habit misfires into the harmless action, and the destructive
one is the key that has to be relearned. Had it gone the other way — `x` staying on Delete
while the pane binds took some other letter — the same slip would have deleted a selection.

The test that guarded the old arrangement now guards the new one, and its surviving
invariant is the one that mattered all along: **uniqueness**. `handlePrefixKey` dispatches to
the FIRST command with a matching bind, so a duplicate does not error — it silently makes
the loser unreachable, on a screen whose command set changes with the selection. A second
test compares the two screens' pane binds *against each other* rather than against a list
written in the test, because a list is a third place to update and the one most likely to be
forgotten; it fails if either side drifts, and both maps are asserted concrete so two screens
that lost their binds together cannot pass by matching at nothing.

### "Close focused pane" was closing the focused TAB

Reported, and true. The model's word for one tab's content is "pane", and the command
borrowed it — but a tile with two tabs keeps standing when one of them goes, and the ✕
beside every tab is already labelled **Close tab**. Two names for one operation, and the
louder one promised more than it did. Now "Close focused tab" on both screens.

### The guiding arrows lit every tile

Also reported. Every tile was a candidate for a creating pick, which was defensible in the
abstract — a new pane *can* go anywhere — and wrong in practice: on a workspace with three
or four tiles it is three or four identical crosses at once, and the question "where does
this go" becomes "which of these twenty buttons". A creating pick now lights only the
**focused** tile. Creating is something you do where you are working; the wireframe is there
to name a direction, and the focus already names the tile.

A move still lights every candidate, and that is not an inconsistency: a move's whole
subject is a destination somewhere *else*. The rule is per-intent, which is what the tests
pin — a placement lights one tile with five targets, a split one tile with four, a move as
many as it has.

Two consequences worth recording:

- `Tab` now has somewhere to go only during a move, so the hint stops advertising it when
  one tile is lit — the same rule that already withholds "Space stacks" from a split. The
  candidate count is sampled once on mount rather than re-read per render, because the tree
  cannot change while a pick is up.
- Three keyboard tests were built on a placement lighting two tiles. They now arm a **move**
  out of a stacked tile, which is the only pick with more than one candidate. Their claim is
  unchanged (a direction key acts on the tile focus names) and so is the reason the no-Tab
  row exists: it is the only one that fails when the keys ignore the current tile and always
  take the first.

### Not a defect: "Move pane…" missing from the Terminal

Checked in Chromium rather than assuming. It is registered and appears as soon as the
workspace holds more than one pane — including two TABS on a single tile, where pulling one
out is a real move. It is absent only at one pane, deliberately: there is nowhere to move
to, and with the change above the mode would now arm with **no** lit tile at all (the lone
source tile excludes itself), which is a dead end with only Esc for a way out. The gate is
`allPanes(tree).length > 1`, inherited from the old `TileCtx.canMove`.

## 2026-08-02 — Closing note on the pane-command work: one correction, one pattern

### Correction to the entry above

It says the single-pane gate on "Move pane…" matters more now because "with the change above
the mode would **now** arm with no lit tile at all". The "now" is wrong. The exclusion that
empties the workspace in that case is `holdsSource() && panes.length === 1` — *a lone pane
cannot move beside itself* — and it has been in `offersTargets()` since the tap-to-pick mode
was written, long before today's focused-tile rule. Today's change touched only the CREATING
intents; a single-pane move has always lit nothing. The gate is still right, for the reason
it always was, and I attributed it to the wrong cause.

### The pattern across this whole session

Seven rounds of work on the pane commands, six pieces of feedback, and the useful number is
this: **the suite was green when every single one of them arrived.** 259 tests at the start,
308 at the end, and not one of the reported problems was a test that could have been written
and wasn't:

| reported | what the suite could see |
| --- | --- |
| "It isn't keyboard navigable" | nothing — there was no keyboard route to assert |
| "the wireframe renders over the callout" | nothing — jsdom lays nothing out and has an empty CSSOM for `styles.css` |
| "move focus to the first UI element of the new pane" | nothing — focus landed on `<body>` and no test looked |
| "the arrows show up on every pane" | nothing — a count of overlays was correct for the design it had |
| "Close focused pane closes the focused tab" | nothing — no test asserts that a label is *true* |

Four of the five were visible in one screenshot or one real keypress. That is the actual
lesson, and it is not "write more tests": three of these are not the kind of thing a unit
test can hold an opinion about. It is that **a change to a screen is not finished until the
screen has been looked at**, and the throwaway Playwright in the session scratchpad is cheap
enough (one file, ~40 lines, never added to `package.json`) that there is no excuse for
reporting done without it.

The sharpest instance is the focus one. My own browser probe had been printing
`focus: ""` — activeElement on `<body>` — after every placement, through **two** rounds of
checking, and I read past it both times because the line I was looking for on those runs was
the tile count. Having the evidence and not reading it is a different failure from not
gathering it, and the fix is different too: state what a PASS looks like for every line the
probe prints, not just the one the current question is about.

Two smaller habits that did work, worth keeping:

- **Neutralizing every claim caught two tests that proved nothing** — `press(all[0], z)`
  (directions always acting on the first tile) and the redundant `if (placing()) return
  true`. Both were green against a broken build. Neutralization is the only reason either
  was found.
- **Reading the code before writing the test** caught that "Move pane… is missing" was a
  deliberate gate rather than a defect, and that the lost focus was a pre-existing bug in
  `Term` rather than something the new command introduced.

## 2026-08-02 — The last caretaker item: the filed fix would have broken the feature

`web-fs.star` closed, both arms green on real backends, and `web.star` re-run on docker and
kube to confirm the `listScript` change did not disturb the neighbouring BFF surface. That
left one open item from this session: the kubernetes `cfg.Instance` defect, filed as a
Phase 2 prerequisite. TODO.md's own header says every entry is re-verified against the tree
before it is acted on, and this is the entry that earns the rule.

### Three claims, checked

- **"Misaddresses `ForwardPort`"** — false for kubernetes. That backend's `ForwardPort`
  uses the `pods/portforward` subresource and `firstPod`; it never touches the companion
  registry. The registry's only readers are dockerhost, containerdhost and incushost.
- **"A kubernetes caretaker is unreachable unless agent-forward is on"** — true, and
  currently harmless. The only consumer of a kube caretaker's declared instance is the
  agent relay, which requires `AgentForward` anyway. The registration is scoped to the one
  feature that consumes it.
- **"Every replica collides on `name/0`"** — true of the registration, and it does not
  misroute: the lookup hardcodes 0 as well, and exec always targets the first pod, so the
  two ends meet by construction.

So the item was not a live routing defect. **And the fix it proposed — a per-pod key from
the downward API — would have broken ssh-agent forwarding outright**, because
`relayAgentMuxed` looks the client channel up by exactly the string the caretaker declares,
and the client registers under `name/0`. Implementing the TODO as written was the
regression.

### What WAS wrong: the agreement was a coincidence

`pkg/server/deploy_exec.go` and `pkg/deploy/kubernetes/kubernetes.go` each spelled
`InstanceKey(name, 0)` independently, and two tests each asserted the literal `"web/0"` on
their own side. Every one of those four is individually defensible; none of them is the
contract. The contract is that the two strings are the same string — the exact shape
CLAUDE.md records from the containerd hostname case, where each side was defensible and
only their agreement was the promise.

It is now structural rather than coincidental: `remotecompanion.AgentRelayInstance(name)`
is the single definition both ends call, and it carries the two things a future editor
needs — that replica 0 is not arbitrary (exec targets the first instance on every backend),
and that a per-pod identity cannot land without an instance-selecting lookup beside it.
The kube test asserts against that constructor instead of a literal, so the guard fires on
precisely the change the TODO recommended:

    cfg.Instance = "web/$(CORNUS_POD_NAME)", want "web/0" (the key the server looks the
    agent channel up by)

### The finding worth keeping

**A filed defect is a hypothesis, not a work order.** This one was written during a plan
review from a correct reading of one file and an assumed reading of the others, and it
survived into TODO.md with enough specificity to look verified. Acting on it directly would
have produced a confident, well-commented regression in a feature nobody would have
exercised until someone tried `--forward-agent` on kubernetes.

The general move that caught it: **follow the consumer, not the producer.** The entry
described who SETS the key. What decided the question was finding every reader — three
backends and one server handler — and noticing that none of them is kubernetes except the
agent relay, whose own gate is the very flag the entry called an accident.

## 2026-08-02 — Session close: what checking claims found that writing code did not

Five entries above cover the chunks (gap-closing, the neutralization ledger, the E2E, the
kube bug, the caretaker item). This one records the through-line, plus the kube FS surface
as it actually stands, because that map was assembled piecemeal across the session and is
worth having in one place.

### The through-line

Three of this session's four most valuable findings came from CHECKING A CLAIM, not from
writing new code:

1. **Writing the obvious test** for a shipped feature — copy a file OUT of a container —
   found that every container-source copy wedged after the bytes had landed. The suite had
   every failure mode of that path and no success.
2. **Running on a real backend** found that kubernetes served every container listing from
   the wrong directory, labelled with the path the user asked for. Three backends honour
   the exec working directory; the fake honoured it too; only the fourth does not.
3. **Re-verifying a filed TODO** found that its central claim was false for kubernetes and
   that implementing its proposed fix would have silently broken ssh-agent forwarding.

Only one finding — the `skipped`-means-two-things defect — came from reading code with the
intent to change it. The pattern is uncomfortable and worth stating plainly: **the work
that produced the most was the work that questioned things already believed to be true.**
Each of the three had a written artifact asserting it was fine — a green test suite, a
fake, a filed entry with file:line citations.

### The kube FS surface, as it actually is

Verified today rather than assumed. Kubernetes gives the BFF exactly one primitive family
and withholds the other:

- **exec works** (pods/exec via remotecommand). `PodExecOptions` cannot express WorkingDir,
  User or Privileged — the backend warns and runs the command anyway. Env is honored by
  wrapping in `env(1)`.
- **the archive trio does not.** `StatPath`, `CopyFrom` and `CopyTo` all return
  "cp/archive not supported on the kubernetes backend; use kubectl cp".

So for a plain container path with no bind behind it, the explorer can **browse, rename and
delete but cannot read or write**:

| operation | primitive | kube |
|---|---|---|
| `FsList` | exec `sh -c listScript … <dir>` | works |
| `FsDelete` | exec `rm -f/-rf -- <p>` | works |
| `FsRename` (same mount) | exec `mv -- <a> <b>` | works |
| `FsStat` | `StatPath` | fails |
| `FsOpen` / read / download | `CopyFrom` | fails |
| `FsWrite` (editor save) | `CopyTo` | fails |
| `FsMkdir` | `containerPut` -> `CopyTo` | fails |
| upload | `createStream` -> `CopyTo` | fails |
| copy / move across paths | relay = `CopyFrom` + `CopyTo` | fails |

Failures surface as **502** carrying the backend's own sentence, because `mapContainerErr`
matches "not found"/"no such" for a 404 and defaults to Bad Gateway otherwise — the message
is honest, the status is not ideal.

**The client-local bind redirect is therefore not an optimization on kubernetes; it is the
only way to read or write a file there.** A container path under a bind resolves to
`siteClient`, never touches the backend at all, and gets the full uncapped surface plus
browsability while the workload is stopped. Kube realizes those mounts with a privileged
native-sidecar mount agent over 9P relayed through the server, never a hostPath.

Two consequences worth naming, neither currently filed as work:

- **A distroless workload on kubernetes cannot be browsed at all.** `containerList` falls
  back to `containerListTar` when the image has no shell, and that fallback is built on
  `CopyFrom` — which kubernetes refuses. Both routes are gone at once.
- **`FsRename` refuses to cross mounts**, so the SPA composes a cross-mount move out of
  copy + delete; on kube that copy fails for any container path. The move gesture is
  therefore bind-only there, like everything else that moves bytes.

This is the gap `deploy.FSOperator` (the Phase 2 caretaker fsop) exists to close, and it is
not built: `serverFSOps` returns false, so `execServer` is in the planner's vocabulary and
always degrades to `execRelay` — which on kubernetes is exactly the route that cannot work.

### Verification at close

`gofmt`/`go build`/`go vet`/`go test ./...` clean. SPA 309 passing across 14 files, `tsc`
clean. `make e2e-check` parses 158 scenarios. Executed against real backends: `web-fs.star`
relay arm on docker, `web-fs.star` redirect arm on a kind cluster, and `web.star` on both —
the last as a regression check on the `listScript` change, which touches every container
listing.

One process note. A full SPA run mid-session showed two failures in the tiling pane-menu
tests; `Files.tsx` had been modified sixty seconds earlier by the agent working alongside
me, and a re-run after they landed was green at 309. The protocol recorded earlier in this
journal held up: check mtimes against your last green run, confirm the failing area is not
yours, re-run rather than fix. **A red suite in a shared tree is a question, not a verdict.**

## 2026-08-02 — "Move pane…" is disabled now, not hidden

`Command` gained `disabled?: string` — the REASON, whose presence is what disables — and
"Move pane…" is always listed on both workspaces, greyed with "nowhere to move it" while the
workspace holds a single pane.

**Why a string and not a boolean.** A reason is compulsory by construction: if you cannot
name one, omit the command instead. A grey row with no explanation only moves the user's
question from "where is it" to "why is it like that", and the whole reason this change
exists is that the hidden entry produced exactly the first question — reported here as
"Terminals command set doesn't seem to have Move pane…", about a command that was working
as designed. An absent entry reads as *this screen cannot do that*; a disabled one reads as
*not just now*, which is the truth.

Three details, each with a way of getting it wrong that looks fine until it doesn't:

- **`aria-disabled`, never the `disabled` attribute.** A disabled `<button>` receives no
  mouse events, so hover-select would stop working on that row, and some readers drop it
  from the accessibility tree — the opposite of the point, which is that the command is
  visibly THERE. (Playwright agrees: it refuses to `.click()` an aria-disabled control,
  which is why the browser probe had to `dispatchEvent("click")` to prove the *handler*
  refuses and not merely the tooling.)
- **The press does nothing AND leaves the palette open.** Closing on a dead press is
  indistinguishable from the command having run.
- **A disabled command still owns its bind.** The key is swallowed, nothing runs. Skipping
  it in the lookup instead lets the key fall through as a browser shortcut, so `prefix x`
  would close a browser tab because a pane command happened to be unavailable. Both
  directions are neutralized: running it, and skipping it.

### Neutralization, and one guard that needed a test of its own

| broke | test failed with |
| --- | --- |
| `runAndClose` losing its guard | the palette unit test and the workspace one |
| the reason not rendered | both again |
| `disabled` never set on the command | the workspace test |
| the bind guard, either direction | *nothing* — see below |

The bind guard **failed to fail**: no disabled command carries a bind today (`term:move`
and `files:move` are unbound), so nothing exercised it. Rather than delete an unreachable
guard that becomes a trap the day someone disables a bound command, it got a direct unit
test in `command-center.test.ts` with a registered disabled+bound command. That test asserts
the DISPOSITION as much as the missing call — `"swallow"`, not `"browser"` — because the
tempting wrong fix (filtering disabled commands out of the lookup) also stops the command
running, and only the disposition tells the two apart.

The test helper `runCommand()` now refuses a disabled command too. Calling `run()` directly
reaches a path no user can — the palette will not press it, a bind will not fire it — so a
test doing that would exercise code the app has decided is unreachable, and would keep
passing after the guard broke.

## 2026-08-02 — rotate-window: tmux's prefix C-o, and the app's first chord bind

"Rotate panes" on both workspaces (`term:rotate`, `files:rotate`), bound to **prefix +
Ctrl-O** and tagged `pane` so it is in the tile ⋮ menu. The tiles cycle through the layout's
slots; with two tiles it is the swap people actually reach for.

### The model half: `rotateStacks`

The operation is "the tiles move, the slots do not". `allStacks(tree)` lists the tiles in
document order, and the rebuild walks the same order handing slot *i* the tile from slot
*i-1*, wrapping. Every split node is copied with its `dir` and `ratio` intact, so a rotation
never resizes anything — which the tests assert as its own leg, because a version that
rebuilt the splits fresh would look right in a tab-label check and quietly flatten a
carefully dragged divider. (Neutralized exactly that way.)

Two decisions worth naming:

- **A tile travels whole**, tabs and active index included. A slot holds a tile, not a pane;
  a rotation that shuffled panes between tiles would be a different operation wearing the
  same name. The active tab travels too — arriving showing a different tab than it left
  with is a second, invisible change.
- **`focused` is deliberately untouched.** The active pane travels with its tile, so the
  shell you were typing in is still the one you are typing in; it has merely moved. Following
  the SLOT instead would put the keyboard in a different session, which is the one thing a
  rotation must not do. Confirmed in Chromium: after the chord, DOM focus is in the pane that
  moved, not the pane that arrived — the `Term autoFocus` guard from earlier today carries it
  through the remount.

The gate counts **tiles, not panes**: two tabs sharing one tile is one slot, and rotating one
slot is a no-op. A pane count gets that case wrong, so the test walks through it explicitly.

### The bind half: chords

This is the app's first bind carrying a modifier, and it did not fit. `handlePrefixKey` bailed
out of the bind lookup on `!e.ctrlKey && !e.altKey && !e.metaKey` — deliberately, so that
`prefix Ctrl+C` reaches the browser rather than colliding with the `c` bind. A chord cannot
be expressed under that rule at all.

The fix is to stop deciding by modifier and start deciding by claim: look the combo up first,
and let it fall through to the browser only when no command claims it. `bindMatches` keeps
the two spellings apart —

- **plain** (`"%"`, `"c"`, `"C"`): `e.key` exactly, Ctrl/Alt/Meta required absent, Shift
  IGNORED, since the shifted character is already baked into `e.key` and that is precisely
  what lets `c` and `C` be two commands;
- **chord** (`"Ctrl+O"`): parsed by the existing `parsePrefix`/`matchesPrefix`, the same pair
  the app prefix uses, so modifiers are compared rather than ignored.

The plain path is a rewrite of a matcher three commands already depend on, so it gets its own
test alongside the chord ones: `%` still fires with Shift held, `c` still does NOT fire with
Ctrl held.

**Ctrl+O is a browser shortcut** (open file). It is safe because both `App.tsx` and
`Term.tsx` `preventDefault()` on a `"swallow"` disposition — but "should be fine" is not
evidence, so the browser probe counted `filechooser` events across three rotations: zero. It
also drove the chord from inside a LIVE xterm, where `Term.tsx` owns the keys rather than the
document listener, since that is the path a terminal user will actually take.

### Neutralization

| broke | test failed with |
| --- | --- |
| rotation direction flipped | "moves every tile one slot along, wrapping" |
| splits rebuilt with a fixed ratio | "leaves the geometry exactly as it found it" |
| the old `!e.ctrlKey` bail restored | the chord unit test and the workspace rotation test |
| gate counting panes instead of tiles | "offers Rotate in the pane menu, disabled while there is one tile" |

Left undone: tmux's `prefix M-o` (rotate the other way). `rotateStacks` already takes the
direction and the reverse is tested at the model level; only the command and a bind are
missing, and nobody asked for it.

## 2026-08-02 — The last placement prompt is gone, and so is the setting behind it

Enter on a file row in the Files explorer opened the "Stack as tab / Split → right" modal —
the one prompt the rest of the session had been replacing everywhere else. It now arms the
same wireframe: the focused tile lights up, an arrow splits the file into that side, Space
opens it as a tab. The mouse path is unchanged and still does not ask, because a click has
already said where by being on a pane.

**The pick had to learn to carry a payload.** Every placement so far created the host's
EMPTY pane (`ops.fresh`); this one creates a pane showing a named file. So `beginPlace` takes
an optional factory — `beginPlace(() => data)` — which outranks both host factories, because
the user has already said what the pane is and neither an empty one nor a copy of the target
tile is it. That is what finally made `TileDrag` generic (`TileDrag<P>`); everything else in
the interface is ids and zones, and the generic was avoided for exactly that reason until a
real payload turned up.

### What it took down with it

`pane-placement.ts` is deleted — `promptPanePlacement`, `resolveSplitSide`, and the
`Placement` type had no other caller. With `resolveSplitSide` went the **New pane placement
side** setting, whose whole job was to answer the half of that prompt the prompt could not:
which side "Split" meant. Nothing reads it now, so `settings.newPaneSide`, its setter and its
type are gone, and the Settings screen's **Workspace** group went with its only row. Settings
is one group again.

A setting that configures nothing is worse than a missing one — it invites the user to set
it and then quietly ignores them. The description was corrected twice this session as its
meaning narrowed; the third correction is deletion.

### Two tests that were wrong before they were right

- **`parseSettings` does not strip a removed key.** I wrote "ignores a setting that no
  longer exists" and asserted `toEqual(defaultSettings())`. It failed: `parseSettings` is
  `{...defaults, ...JSON.parse(raw)}`, so an unknown key rides through into the parsed
  object. Harmless — nothing reads it — but the test as written was a guess about the merge
  rather than a reading of it. It now asserts what the code does and says why the tempting
  version is false.
- **A neutralization that failed to fail, and the reachable path behind it.** Removing
  `pending = null` from `arm()` broke nothing, because `clear()` nulls the payload too and
  every pick that ends normally goes through `clear()`. The line is still load-bearing: a
  pick can be SUPERSEDED rather than ended — the prefix key still works under the scrim, so
  `prefix C` (Split pane…) over the top of an armed file placement re-arms without clearing,
  and the abandoned file would have ridden along into the split. Now tested by doing exactly
  that and asserting the split produces a second listing rather than the editor.

### Neutralization

| broke | test failed with |
| --- | --- |
| the payload factory ignored (falls back to `fresh`) | both keyboard-open tests |
| `pending` surviving a re-arm | "does not carry one pick's payload into the next" |
| Enter opening without asking | both keyboard-open tests |

Verified in Chromium: Enter arms a five-target cross with no dialog, `→` puts the editor in
the new right-hand tile while the browser stays put, a mouse open still stacks silently, and
the Settings screen renders one group of three rows.

## Phase 2: the caretaker filesystem operator (2026-08-02)

Phase 1 left `serverFSOps` as a named seam that always answered `false`, and `execServer` as
a route the planner could name but never take. This is the thing that seam was for.

### What was actually broken

The archive trio — `StatPath`/`CopyFrom`/`CopyTo` — can pack a path and unpack a tar and
nothing else. No readdir, no delete, no rename, and no way to copy from one place to another
without dragging every byte out to the caller and straight back. On kubernetes it cannot even
do that much: all three answer *"cp/archive not supported on the kubernetes backend; use
kubectl cp"*. The measured consequence, which the E2E arm now pins, was that a volume-backed
path on that backend could be **listed** (exec works) and then neither read, written, nor
copied — the explorer showed a directory whose every file answered 501.

### The chain, and why each link is where it is

`api.FSOpRequest` / `api.FSOpResponse` are the vocabulary: one op name, a container-absolute
path, and a machine-readable refusal code. The codes are not decoration. The caller's next
move depends on **which** refusal it was — `unsupported` means "relay this instead",
`crossdevice` means "copy then delete", `notfound` means "tell the user" — and an error
string collapses all three into one.

`pkg/wire/fsop.go` carries the framing and deliberately no `pkg/api` dependency (the package
invariant). One stream per operation: a length-prefixed request, a length-prefixed reply, and
a chunk-framed body for the two ops that move bulk data. **The body terminator is the part
worth defending.** A tar reader handed a stream that simply stopped reports a short but
well-formed archive — so a copy that died halfway would land as a smaller directory and
report success. `ErrFSOpTruncated` is the difference, and
`TestFSOpBodyDistinguishesEmptyFromTruncated` pins the pair it separates: zero bytes that
finished, versus zero bytes because the peer died. `tagFSOp` is `ClassBulk` alongside the 9P
and block backings, because one multi-gigabyte copy on `ClassNormal` starves the hub's
control channel for its duration.

`pkg/caretaker/fsop.go` is the role. It registers a tag on the shared dispatcher built in
the previous pass — which is exactly the payoff of that fix: adding a fourth server-initiated
role is now a registration, not a second `AcceptStream` loop stealing streams from the first.
Every operation goes through `pkg/deploy/containerdhost/tarcopy`, the same confined
pack/unpack the containerd backend uses. Reusing it is the point rather than a shortcut: a
copy served here and a copy served by containerd then agree entry for entry, including the
docker-cp naming and the `NoOverwriteDirNonDir` refusal. Two implementations of "copy a
directory" is precisely how routes start disagreeing (H13's four divergent
folder-onto-folder answers).

`fsopCopy` runs `Pack` into `Unpack` through a pipe, so a native copy is byte-identical to a
get followed by a put. A copy that renames as it goes has to rewrite the archive's one
top-level entry name; getting that wrong lands the tree under the OLD name, which reads as
success until somebody looks. Neutralized by making `renameTarRoot` a passthrough — the
renaming copy silently lands at the source's name.

### The scope limit is structural, not a shortfall

The caretaker shares only the app's **network** namespace (`NetworkMode: "container:"+appID`;
kubernetes never sets `ShareProcessNamespace`), so it has its own mount and PID namespace. It
cannot see the app's image layers and cannot reach `/proc/<app-pid>/root`. An fsop therefore
serves exactly what was mounted **into** it and refuses everything else with
`FSErrUnsupported` — a positive answer the caller falls back on, never a guess. Declaring a
root for the image layers would produce a confident answer about the wrong filesystem.

`addFSOpRole` on the kubernetes backend mounts each of the app's managed volumes at
`/cornus/fsop/<i>` and declares the app's own target path alongside it. The test that matters
pairs the two: a declared root whose `Path` nothing is mounted at would answer every request
with a confident "not found". Neutralized by declaring the root at `v.Target` and mounting
nothing — `no volume mount targeting "/data"`.

Six places in `kubernetes.go` assemble a caretaker. Rather than edit six call sites,
`addServerInitiatedRoles` now folds the agent relay and the operator together: both are
looked up in the server's per-instance registry, so both need `cfg.Instance`, and one helper
is what keeps a seventh site from quietly acquiring only half the pair.
`TestFSOpRoleRidesEveryCaretaker` neutralizes by reverting one site — the docker-role
caretaker loses its operator while the other two keep theirs, which is exactly the
"works depending on which OTHER feature you use" failure that reads as a flaky backend.

### Two ways in from the BFF, and why both

**The transfer route** is the headline: `planTransfer`'s rule 2 finally fires, and
`copyOnServer` / `renameOnServer` issue one structured request instead of streaming. The
proof is not that the copy worked — a relay would also work — but that the archive primitives
were never touched, which `operatorFS` asserts by failing on contact.

**The fallback at the seam** is the one that makes kubernetes usable at all.
`clientContainerFS`'s three archive methods retry through the operator when the backend has
no archive: a 501 with **zero bytes moved**. The counting adapters are load-bearing — an
archive call that already wrote to `w` or read from `r` cannot be retried, because the stream
is spent and a second attempt would append to a half-written destination. This is why the
whole existing explorer starts working on kube volumes with no upstream change: the
substitution is byte-compatible, since the caretaker packs with the same tarcopy.

`serverFSOps` became a real probe rather than a guess about the backend, because whether a
path can be served is the **operator's** answer and no reasoning on this side substitutes for
asking. It stats a volume target the workload actually declares (probing `/` would answer
"no" for every workload, since no operator serves image layers) and memoizes for 30s — a
caretaker that has not connected yet answers unsupported, so a negative must not be permanent.
`TestAnOperatorlessBackendStillRelays` asserts exactly one probe per workload, since the memo
is what keeps a tree walk from being a round trip per file.

### Two judgement calls worth recording

`FsRename` on a volume prefers the operator but falls through to the legacy `mv` exec on
`FSErrCrossDevice`. `rename(2)` will not cross two volumes; `mv` would have, as a non-atomic
copy+unlink. Trading a gesture that used to work for a tidier error is not an improvement.

Unknown ops are **gated** by the API policy, not waved through. Every fsop is a POST, so
unlike the archive endpoint the method cannot say whether this is a read or a write — the op
does, and the default for a vocabulary that will grow has to be deny.
`TestFSOpWritesAreGatedByTheDeployPolicy` neutralizes by treating `fsop` like `logs`: an
identity with no `deploy` grant gets 200 on put, mkdir, remove, rename and copy.

### The live kube run, and the one defect it found

`web-fs.star`'s kube arm grew a volume section: read, write, native copy, rename, and a
preflight assertion that the route really is `server`. The route assertion is the one that
can only be made here — a wrong route is invisible at runtime, since the copy still works,
just the slow way.

It also gained a probe of the **server's own** `/fsop` endpoint before the BFF's. The BFF
collapses every way this can fail into one status, so when it broke the message said nothing
about which link went: no operator on the backend, no caretaker registered for the pod, or
no root covering the path. That assertion names the link.

The first live run failed at exactly that point, and the cause was a stale `cornus:e2e` in
the kind cluster — the sidecar image predated this work, so the pod's caretaker had no fsop
role at all and the server's stream went to a dispatcher that closed it. Worth recording
because the failure mode is indistinguishable from a code defect: `KubeTarget.Setup` creates
the cluster but does NOT build or load the sidecar image (only the containerized runner's
`prepare_kube` does), so a direct `make e2e-kube` against a pre-existing cluster silently
tests whatever binary was loaded into it last.

**Then the run found a real defect in the write path, and it is a lesson about order.**
The archive-to-fsop fallback was written as a retry *after* the archive call failed, gated
on nothing having moved — which is the right gate, because a spent stream cannot be
replayed and a second attempt would append to a half-written destination. But a PUT streams
its tar from a pipe while the request is in flight, so Go's transport had already pumped
bytes out of that pipe by the time the server's 501 came back. The guard fired exactly as
designed and the fallback never happened: reads worked and every write stayed a 501, with a
message about `kubectl cp`.

The decision has to be made **before** the call. `clientContainerFS` now learns whether the
backend has an archive at all from a `StatPath` — a HEAD, so bodyless and the one call that
*is* safely retryable — and remembers it, because that is a property of the server's backend
and one client talks to one server. The zero-bytes-moved guard stays as belt and braces.

`TestArchivelessBackendRoutesWritesToTheOperator` drives the **real** `clientContainerFS`
over a real `pkg/client` against an `httptest` server shaped like kubernetes, rather than a
fake that restates the routing — a restatement inherits whatever the original got wrong.
The handler drains the body before answering 501, exactly as Go's transport does, because a
handler that refused without reading would let the broken design pass. Neutralized by
restoring that design: `CopyTo on an archiveless backend: 501 Not Implemented: cp/archive
not supported on the kubernetes backend; use kubectl cp` — the live failure, reproduced in
20ms.

**Chasing the first symptom also surfaced a latent defect.** The caretaker buckets its
server-bound
roles by URL (`groupByServer`) and dials one connection per bucket. `addFSOpRole` had been
naming `CORNUS_ADVERTISE_URL` while the mount roles name their own `RelayURL` — the same
server in this deployment, but not necessarily the same string. A different spelling means a
SECOND connection, and since both register under the same instance key, the registry keeps
whichever connected last while the fsop handler may be sitting on the other one. The server
then opens a stream nobody answers. `caretakerServerURL` now takes the URL an existing
server-bound role already dials and falls back to the advertised one only when there is no
first connection to join; `TestFSOpRidesTheConnectionThePodAlreadyHas` pins it with the two
deliberately different.

### Verification

Go gate clean across the tree (`gofmt`, `build`, `vet`, `go test ./...`). Neutralized: the
body terminator (both the push and pull readers, separately — the first neutralization
short-circuited the second assertion, so it needed a second run to be real), the caretaker's
path-boundary root match, the tar-entry rename, the kube volume mount, the six-site helper,
the API-policy gate, and the BFF's server route. 158-scenario `e2e-check` green.

Both E2E arms run and pass against live backends:

- **kube** (kind, `cornus:e2e` rebuilt from this tree): the bind redirect, the image-path
  split, the read-only bind's BFF/kernel agreement, a volume readable and writable through
  the operator, a volume-to-volume copy that ran inside the pod with `route: "server"` and
  `native: true`, a within-volume move that was a rename, and browsability while stopped.
- **docker**: the relay arm unchanged — 11 MB in and back out byte for byte, the H6
  directory refusal, `route: "relay"`.

Two things NOT verified by me, said plainly. The SPA suite has three failures and `tsc`
errors in `web/src/views/tiling/layout.test.ts`; both are another agent's in-flight pane-menu
work (`Files.tsx`, `Term.tsx`, a new `PaneChooser.tsx`, a deleted `pane-placement.ts`, all
touched minutes before each run). Nothing here touches `web/`. It also means `make build`
cannot produce the SPA right now, so the E2E runs above invoke `bin/cornus-e2e` directly
against the previously-built assets.

## 2026-08-02 — Choose a pane from a list, walked with the arrows (`web/`)

The last of the tmux modes the tiled workspaces were missing: `prefix s`, "choose a session
from a list", pointed at the panes of one workspace. A panel appears in the workspace's
top-right corner listing every pane; the arrows then walk the WORKSPACE, tile to tile, and
the list highlight follows. Nothing moves until Enter; Escape leaves the focus exactly where
it started.

**The inversion is the design.** A chooser dialog normally drives its list and the workspace
is a picture of the consequence. This is the other way round, because the panes are already
laid out spatially in front of the user: asking them to translate "the terminal below this
one" into "two rows further down a list" is asking them to look up something they can see.
So the list is a readout, and `.stack.previewed` (2px ring, `--accent-subtle` fill) is where
the eye actually goes. The focused tile keeps its own 1px frame the whole time — the two
marks sitting on different tiles at once is the state the mode exists to show.

**Geometry from the tree, not the DOM.** `neighborStack` walks `tileRects`, which lays the
split tree out arithmetically as fractions of the workspace. Not `getBoundingClientRect`:
the question is "which tile is to my left", which the model answers exactly and a
measurement only approximates (dividers, borders, sub-pixel rounding), and jsdom lays
nothing out, so a measuring version would have been untestable here. A candidate must lie
wholly past the named edge AND overlap the perpendicular span — otherwise `←` and `↑` become
interchangeable in exactly the layouts where they have to differ. Nearest wins; ties (every
directly-adjacent tile is at gap 0) go to the one sharing the most border.

**It does not wrap, and tmux does.** `window_pane_find_left` treats the leftmost pane's edge
as the window's right. tmux is moving the focus one pane at a time, where a wrap is a
shortcut; here a highlight is being watched travel, and a `←` at the left wall that lands on
the far right of the screen reads as a bug. Recorded as a deliberate divergence, in the code
and in the test that pins it.

**Tab walks the tabs of the previewed tile.** Stacked tabs share one place on screen, so no
direction can name them; without this every background tab is a row in the list the keyboard
can never reach. Committing one raises it as well as focusing it — choosing a background tab
is choosing to look at it.

### Three things this dragged in

**`s` was taken, and the collision was the nasty kind.** Files had `s` on "Save file", and
`files:save` is registered ONLY while an editor holds unsaved changes — so with
first-match-wins bind lookup, `prefix s` would have meant "choose a pane" most of the time
and "save" exactly when a dirty editor was focused. Not a bind that is taken; a bind that
changes meaning under you. Save moved to the `Ctrl+S` chord, which is a better bind than the
letter it lost: it is what "save" is called everywhere and it cannot collide on a screen
whose command set changes with the selection. The existing uniqueness test could not have
caught this — it runs in the browse state, where `files:save` does not exist — so it now
opens a dirty editor and re-asserts uniqueness there. Neutralized by putting Save back on
`s`: 7 unique binds against 8.

**`Term` had to start reading `autoFocus` reactively.** It sampled "am I the focused pane"
once, at mount, which was right for every existing caller (all of them mount the pane they
focus). The chooser focuses a pane that has been on screen for an hour, so the workspace
focus moved and the keyboard did not — the exact defect `autoFocus` was introduced to fix,
arriving from the other direction. It is now a `createEffect`; the first pass is the old
mount-time behaviour.

**Two takeover modes may not be up at once.** The prefix key still works underneath a pick's
scrim, so `prefix s` over an armed placement was one keystroke away, and two window key
handlers would then answer one keystroke. The chooser owns both directions — `begin()` ends
a pick, an effect ends the choose when a pick arms — because the drag protocol does not know
the chooser exists and should not have to.

### Verification

345 SPA tests (was 323) across 15 files, `tsc --noEmit`, `npm run build`, and
`go build ./...` / `go vet` / `go test -count=1 ./pkg/webui/` all clean. `pkg/webui/dist/.gitkeep`
restored after the build, as ever.

Nineteen neutralizations, each confirming the intended diagnostic: the Term focus effect, a
walk that activates as it goes, a cancel that commits, an inert Tab, a missing scrim, both
halves of the mode exclusion, a handler that swallows everything and one that swallows
nothing, the disabled gate, the row/ring highlights (three separately), a click that commits
the walk instead of the row, the vanished-pane guard, a commit that leaves the mode armed,
the nearest-first sort, the overlap tie-break, ratio-blind layout, the wall, and `CTRL_SIDE`
collapsed into `KEY_SIDE`.

**One neutralization failed to fail, and the finding is about the geometry rather than the
test.** Deleting the `overlap > 0` FILTER from `neighborStack` changes no answer: on a
complete tiling — the only kind this tree can express — every non-wall side has a
properly-adjacent tile at gap 0, which outranks any corner-toucher on the overlap tie-break
anyway. The rule is written twice and the second copy subsumes the first. The filter stays
(a sort comparator is not where a rule should be legible), but neither it nor the diagonal
test may be cited as evidence for the other: the test pins the contract, not that line. Both
say so now.

Chromium (throwaway Playwright, mock BFF): a 2x2-ish Files layout walked with all four
arrows — including two walls that correctly did nothing and a diagonal that was correctly
unreachable — with `focused` pinned to its tile for the whole walk; Escape discarding;
Enter committing; the panel legible in dark mode at 420px. And the flagship path, which
jsdom cannot really answer: `prefix s` pressed from INSIDE a live xterm opens the chooser
without the shell eating the `s`, and Enter on another terminal pane lands DOM focus on its
`.xterm-helper-textarea` — verified by typing into it afterwards.

### What the mode leaves behind, and the bookkeeping

Two ARIA states, because the mode genuinely has two and the vocabulary happens to fit: a
row carries `aria-selected` for where the WALK is and `aria-current` for where the focus
still is. They are on different rows for most of the mode's life, so a reader told only one
of them would be describing a different feature. That replaced a `.focused` class on the
row that nothing styled — the visual half is the `●` in the mark column, which is
`aria-hidden` and therefore was saying nothing to anyone not looking at it.

`Term.tsx` now exposes the xterm to the component body as a `focusTerm` CLOSURE rather than
hoisting the instance itself: the instance is torn down on cleanup, and a hoisted handle is
a way to reach a disposed terminal. It is set once the terminal is open and cleared before
`dispose()`.

Docs: `DESIGN_SYSTEM.md` gains a pane-chooser section (the class vocabulary, the inversion,
the no-wrap divergence, the mode-exclusion rule, and the reactive-`autoFocus` correction to
the focus-landing rules it already carried). `LTM/web-ui.md` gains the one-sentence version.

TODO: closed the long-open "decide whether Files should get new-pane / close-pane binds"
row — the product call was made earlier in this session and went the other way round from
what the row proposed (the pane binds are shared letter for letter across both workspaces
and the two FILE actions moved to `y` / `d`), which is worth recording because a row that
poses a question is easy to leave open forever once the question has been answered
elsewhere. Two new rows opened, both honest limitations rather than defects: the corner
panel can cover the very tile it is previewing, and two Files panes at the virtual root are
both labelled "All", so their rows are indistinguishable. tmux's answer to the second is
pane NUMBERS, which would also give `prefix q` (display-panes) something to draw.

### Correction: Save needed no prefixed bind at all

The entry above records moving "Save file" from `s` to a `Ctrl+S` chord when the pane chooser
claimed the letter. That replacement was wrong, and the user said so: `components/Editor.tsx`
has bound **plain `Mod-s`** in the CodeMirror keymap since the editor pane existed, and
`files:save` only exists while an editor pane is focused and dirty. So the chord asked for
three keystrokes — prefix, then Ctrl, then S — to reach what one keystroke already did, in
the only pane where the command is registered at all. `files:save` now carries NO bind; the
palette entry and the sub-header Save button stay.

The rule worth keeping: **a prefix bind is for something the focused pane cannot hear.** The
pane binds qualify (a terminal swallows every key, which is the whole reason tmux has a
prefix); an editor command that the editor itself already answers does not. I reached for a
replacement key reflexively because the letter had been taken away, without asking whether
the thing losing it had ever needed one — the freed letter was the question, and "what does
the loser get instead" was not.

The test moved with it, and this is the part that would have been easy to get wrong: asserting
`bind === undefined` is equally true of a Save nobody can reach by key. So it now also drives
`Ctrl+S` as a real keydown on `.cm-content` and waits for the "unsaved" badge to go. Both
halves neutralized — breaking the editor's `Mod-s` fails it, and so does putting any prefix
bind back on the command. Chromium confirms the same on the real key: a dirty buffer, one
Ctrl+S, badge gone, no browser save-page dialog in the way, and the palette row rendering
with no accelerator beside it.

Where that rule now lives, so it is met before the next letter gets taken away: at both call
sites. `Files.tsx`'s `fileCommands` states it beside the bindless Save ("a prefix bind is for
something the focused pane cannot hear; a save is the opposite of that"), and both
workspaces' `Choose pane…` comments say the letter was freed rather than traded. No other
document claimed the bind — `DESIGN_SYSTEM.md` uses `prefix C-o` as its chord example, which
is unaffected — so the only stale mention is the superseded paragraph in the entry above,
left in place per the append-only rule and answered by this section.

## 2026-08-02 — Numbered panes, circled without the codepoint (`web/`)

Every pane now wears its position while the chooser is up: on its row in the list, on its
tab, and as a large plate centred in its tile — tmux's `display-panes`, which is what makes
a row in the corner and a tile across the screen the same object. `1`–`9` jump the walk
there.

**A number is a POSITION, not a name.** `numberOf` is the 1-based index in `allPanes` order,
derived on every read. Storing one on the pane would survive a close and then address the
wrong row, and the test closes the first of three tabs and asserts the remaining two are 1
and 2. The order is `allPanes`', which runs through a tile's tabs before moving to the next
tile — the same order the list is built in, so a row and its plate cannot disagree.

**Circled by CSS, never by codepoint**, which was the explicit ask and is also the right
call for four reasons worth writing down: the Unicode circled digits (U+2460…) stop at 20,
they are absent from many monospace faces so the browser substitutes a face that has them
and the digit stops matching the text beside it, their weight and size cannot be tuned
against the surrounding type, and they cannot take the accent when a row is selected. So: a
square box with `border-radius: 50%`, sized in `em` so ONE rule serves both the 19px tab
badge and the 38px plate. Deliberately not `aspect-ratio`, which is at the mercy of the
digit's advance width — a "circle" that is an ellipse on `1` and round on `8` is worse than
no circle. Chromium confirms 19x19 and 38x38 boxes at `50%`, with code points 49 and 50.

**A digit moves the walk; it does not end it.** tmux's display-panes selects on the digit,
but this mode has one rule — nothing moves until Enter — and a key that jumped AND committed
would be the only exception to it. Past nine there is no key left, so those panes keep their
number (that is what a number is for) and the hint names the live range, `1–4 jump`, rather
than a fixed `1–9`.

The tab badge exists because the plate cannot speak for a background tab: a tile shows one
of its panes, so its plate names that one, and the others' numbers would appear nowhere.
The list reads its number out with the label ("3, project"); the tile's copies are
`aria-hidden`, being the same fact drawn twice more.

### The numbering uncovered a real defect, twice over

Placing the plate meant thinking about z-order, which is how I noticed that **the edge-split
strips are live under both modes' scrims**. The strips are z-index 5 and the scrims are 3, so
a scrim does not cover them; and `edgeUnder` deliberately reads GEOMETRY rather than trusting
the event target, because the dwell's reach extends into the gutter where the target belongs
to no tile. So pointer moves over a scrim went on arming strips, and the click meant to
cancel the mode could split a tile instead. The chooser had no stand-down at all (the pick
had one, keyed on `picking()`).

Standing the dwell down turned out to be only half of it. **A strip whose countdown finished
BEFORE the mode opened stays armed under it** — visible, hit-testable, and its own click
handler asks nothing except "am I armed". That one is reachable in the pick mode too, and has
been since the pick shipped: dwell on an edge, press the prefix, arm a placement, and the
strip is still sitting there above the scrim. Fixed at the source — `StackView` disarms on
entering either mode — which makes the two `modal()` guards belt and braces rather than the
whole defence. Both paths have their own test, and the second one asserts the strip really
was armed first (`.armed`, `pointer-events: auto`), because otherwise it would pass against a
tile that never armed anything.

One near-miss worth recording: I first wrote the combined guard as
`picking() !== null || choosing()`, forgetting that `picking` is already the BOOLEAN
`props.ctx.drag.picking() !== null` in this scope. That is always true — it would have
disabled the edge-split gesture entirely, on both screens. Caught by reading the call sites
before running anything, but the suite would have caught it too: `splitViaEdge` is the helper
half the workspace tests use to get a second tile.

### Verification

350 SPA tests (was 345), `tsc`, `npm run build`, and the Go webui check clean. Eight more
neutralizations, all failing as intended: numbers not rendered, numbers stored per pane
instead of derived, the plate removed, the digit key inert, the digit key committing instead
of previewing, numbers left on screen after the mode, the dwell not stood down, and the
disarm-on-open removed.

Chromium: a three-tile Files layout with a stacked tab reads `1 project`, `2 project
compose.yaml`, `3 shop-web`, `4 All` in the list against plates `2 3 4` on the tiles (tile 0
showing its second tab); pressing `4` moves the preview and leaves `focused` alone; Enter
commits and every number leaves with the mode. Dark mode over two live terminals is the case
that justifies the feature — both tabs read `shop-web /bin/sh`, and the plates are the only
thing telling them apart.

### The numbers, after four rounds of the user telling me what they are for

Four follow-ups landed on the numbering, and together they changed what it IS: not a readout
the chooser draws while it is open, but a standing property of a tab that the chooser also
uses.

**1. A setting, `paneNumbersInTabs`** (Workspace group, on by default). Only the TAB copy is
switchable. The list and the plate are the mode itself — a list of identical rows with
nothing tying them to the screen is no chooser — so no setting may reach them, and the test
turns it off and then checks the walk still works with the list and plates intact. The group
is Workspace and not Terminal because the tiled chrome is the same on Files; it is the same
group that went out earlier in this session with the placement prompt, back for a second
tenant.

**2. The badge must cost the tab bar nothing.** At the base size it added 0.6px to a 27.59px
bar — which sounds like nothing until you remember the bar sets the tile's body offset, so
every pane's content shifts by that much, and the mode causing it is the one for LOOKING at
panes. `0.8em` plus `vertical-align: middle` puts a 15px circle inside the bar's 18.59px
line box. Measured, not reasoned about, exactly as the activity badge above it demands:
27.59px with the numbers on and off, on both workspaces, and the differential run (the same
badge forced back to `--text-xs` in the live page) reads 28.19px, so the rule is doing work.

**3. The ● in the list is gone; the focused pane's number is INVERTED instead.** One glyph
for one fact, and it works in the two places a dot had nowhere to go — the tab bar and the
plate — so "where the focus is" is now visible on the workspace and not only in the corner.
A specificity trap on the way: `.pane-chooser-item.selected .pane-number` is (0,3,0) against
`.pane-number.current`'s (0,2,0), so the fill could never win by being later in the file. The
selected rule carries `:not(.current)` and says why.

**4. The plate lost its ring.** At badge size the border is what makes a circle out of a
digit; at 38px the fill already is one, and the ring reads as a second edge just inside the
disc's own. The shadow stays, because that is what lifts the disc off the pane's content —
which the ring never did.

**Then the correction that matters most.** I had built the tab badge to appear only while the
chooser was up, and the user's third message said plainly that the setting was meant to apply
regardless of the mode. They were right, and the reason is the digit key: `prefix s` then `2`
goes straight to pane 2, so the number has to be readable BEFORE the mode opens. Numbers you
can only see once the list is in front of you cannot be aimed with — the jump becomes
something you do after reading the list, which is the walk with extra steps. The two
lifetimes are now deliberate and separately tested: the tab number is standing, the plate is
the mode's.

### Verification

354 SPA tests (was 350), `tsc`, build, and the Go webui check clean. Ten more neutralizations:
the tab badge re-gated on the mode, the plate made permanent, the setting ignored, the setting
defaulted off, the fill not driven by focus, the fill following the WALK instead of the focus
(the plausible wrong version), the plate fill, the badge at full size, numbers absent from the
list, and the mode-only copies wrongly following the setting.

Chromium, on both screens and both themes: tab bars at exactly 27.59px with numbers on and
off; the setting toggled from the Settings screen, taking the tab numbers away while the
chooser's list and plates keep theirs, surviving a reload; the digit jump still working with
the setting off; and the borderless plates read cleanly over both a white file listing and a
black terminal, with the focused pane's disc filled in accent.

One wrinkle left deliberately, so the next reader is not puzzled by it: the numbering still
hangs off `TileChoose` (`ctx.choose.numberOf`), which read naturally while every copy of a
number belonged to the chooser and reads oddly now that the tab badge outlives the mode. It
is the same numbering either way — the tab's digit and the chooser's row MUST agree, which is
the whole reason not to compute it twice — so the shared owner is right even if its name has
drifted. Moving it to `TileCtx` is a rename, not a redesign; recorded in TODO rather than
done here, because touching the interface both hosts implement to improve one identifier is
not a trade worth making mid-session.

Where the rules now live: `DESIGN_SYSTEM.md`'s chooser section gained the numbering
sub-rules — the two lifetimes, what the setting may and may not reach, circled-by-CSS with
its four reasons, the tab-bar height constraint with the measured figures, the ringless plate,
and the `:not(.current)` specificity note — and its class list now names
`.pane-number` / `.current` / `-tab` / `-big` / `-plate` and no longer mentions the deleted
`.pane-chooser-mark`.

## 2026-08-02 — `prefix o`: select the next pane (`web/`)

**Task.** "Implement a 'Select the next pane' command (tmux prefix+o equivalent)."

The whole operation is one pure function and two registry entries. What took the thinking was
deciding what "next" means here, and where the command is allowed to appear.

### Next means next NUMBER

tmux orders panes by pane index. This workspace already has an order that the user can see:
`allPanes` walk order, which is exactly what `numberOf` counts, which is exactly what is
printed on every tab and on every plate the chooser draws. So `nextPaneId` steps along that
order and wraps, and the digits on the tabs become a map of where the key goes next. Any
other order — most-recently-used, tile-only, geometric — would have been a walk the user has
to watch rather than one they can predict, and it would have contradicted a numbering that is
already on screen all day.

That decision has one consequence worth stating: **it steps through background tabs**. A
stacked tab is a pane, it has a number, so it takes its turn. This is the one thing that
separates the new key from its two neighbours, and all three are now a modifier or a mode
apart:

| | walks | what moves |
| --- | --- | --- |
| `prefix o` | every pane, tabs included | the focus |
| `prefix C-o` | tiles (slots) | the layout; the focus travels with its tile |
| `prefix s` + arrows | tiles, with `Tab` for the stack | nothing, until Enter |

`prefix o` is also the only keyboard route to a background tab that does not open the chooser
first.

`run` calls the host's `activate`, not `setFocus`: activating RAISES the pane to its tile's
visible tab as well as focusing it. Focusing a background tab without raising it would put
the keyboard somewhere nobody can see — the same reason `choose.commit` goes through
`activate`, and it is neutralization-tested here (swap in `setFocus` and the raise assertion
fails while the focus assertion still passes, which is precisely the bug that would ship).

### Where it is allowed to appear

Untagged, deliberately: in the palette, not in the tile's ⋮. The rule is already written into
this workspace — the two directional split binds are left out of that menu because they are
accelerators for a question "Split pane…" asks better, and a menu offering both a general
form and two of its four answers reads as three unrelated things. "Select next pane" stands
in exactly that relation to "Choose pane…". The ⋮ is also a pointer surface, and with a
pointer the way to reach a pane is to click it; this command's whole worth is as a key, and
the palette is where a key is advertised. The existing "widens to" assertion in the ⋮ test now
names all three untagged pane commands, so the exclusion is stated once and checked once.

Disabled at one PANE (not one tile, which is Rotate's gate — the walk visits tabs, so two
tabs in one tile is two places to be). A disabled command still owns its key, which
`command-center` already guarantees; the test presses `o` with one pane and asserts the
disposition is `swallow` and nothing happened, rather than the key escaping as a browser
shortcut.

### Findings

- **The palette shows the reason INSTEAD of the accelerator.** `CommandPalette` puts
  `.cmd-item-why` in the `<kbd>`'s slot when a command is disabled, on the argument that the
  key is what you would press and pressing it does nothing. I had written the test expecting
  both; it failed, the code was right, and the test now asserts the swap in both directions
  (no `<kbd>` while disabled, `o` once enabled).
- **`findIndex` returning -1 is the recovery, not a bug.** `(-1 + 1) % n` is 0, so an id that
  names no pane starts the walk at the first — the same fallback `loadLayout` makes with
  `firstPaneId`, and a real case, since a shell that exits closes its own pane. It gets its
  own test, and that test is only honest because the test above it proves the walk is not
  "always the first pane".
- **A two-pane wrap test proves nothing on its own.** One press of `o` with two panes is
  indistinguishable from "go to the first pane". The test presses twice.
- **Two commands one modifier apart, doing opposite halves of one thing.** `o` moves the
  focus and leaves the layout; `C-o` moves the layout and leaves the focus. That is where a
  mis-wiring hides, so the `o` test asserts the tab labels are unchanged between presses —
  a rotation would leave the focus looking right, because the focus travels with its tile.

### Verification

361 SPA tests (was 354): four unit tests for `nextPaneId` in `layout.test.ts`, three
integration tests driven through the real prefix path. `tsc --noEmit`, `npm run build`, and
`go test ./pkg/webui/` clean; `pkg/webui/dist/.gitkeep` restored after the build.

Five neutralizations, each failing with the intended diagnostic: the step removed (returns
`panes[0]`) — both order tests and the wrap test fail; `activate` swapped for `setFocus` — the
background-tab test fails on the raise; the `disabled` reason dropped; the bind removed from
Terminal; the bind removed from Files (the two screens are covered by different tests, so
each bind needed its own neutralization — the parity test only guards TAGGED commands, and
this one is untagged by design).

Where the rules landed: `DESIGN_SYSTEM.md`'s numbering group gained one sub-rule — "the
numbering is also the walk order" — carrying the table above in prose plus the reason the
command is untagged. It sits with the numbers rather than in a keybind list because that is
what it is: the digits on the tabs are the walk made visible, and a reader restyling the badge
should know something now depends on it meaning something.

Nothing was opened in `TODO.md`, and the one thing that could have been is worth naming so a
future reader does not delete the guard by accident: the pane-bind parity test compares only
TAGGED commands between the two screens, so `o` — untagged by design, like the two directional
split binds — is not covered by it. What pins each screen instead is a test of its own: the
Files wrap test and the Terminal background-tab / disabled tests. Deleting either one takes a
bind's only guard with it, which is why both binds needed separate neutralizations before this
was called done.

## 2026-08-02 — `prefix ;`: the pane you came from (`web/`)

**Task.** "Now add prefix+; for the last active pane."

tmux's `last-pane`, and the second of the two keys that skip the chooser (`prefix o` landed
an hour earlier). The command itself is four lines; the memory behind it is where the work is.

### Derived, not recorded

The two hosts write `state.focused` from about a dozen places between them — a tab click, a
split, a close, a drag drop, the chooser's commit, `prefix o`, a pane retargeting itself, the
tile's ⋮. A "last pane" updated at each of those is wrong the first time someone adds a
thirteenth, and the bug it produces is invisible (a key that goes to the wrong place
occasionally). So `createLastPane` (`tiling/lastpane.ts`) watches the one value they all end
up writing:

```ts
createEffect<string | undefined>((prev) => {
  const cur = focused();
  if (prev !== undefined && prev !== cur) setLast(prev);
  return cur;
});
```

The effect's own return value is the previous focus — read on the next run, stored nowhere a
stale copy could be read from. One pane, not a stack: the key's use is alternating between
two panes without looking, and a history would make the second press mean something the first
did not.

The read is where the liveness check lives — the accessor answers `undefined` for a pane that
has left the tree — so the caller states one honest reason ("no pane to go back to") for two
different situations and cannot forget the guard.

### Three states that a count would get wrong

The gate is neither a pane count nor a tile count, and all three of these hold more than one
pane while having nowhere to go back to:

1. **A reloaded workspace.** The layout comes back from `localStorage` with the focus
   restored; the memory is of a move made in this session, and no move has been made.
2. **A remembered pane that has closed.** Reachable without the focus moving at all — see
   below.
3. Panes opened but never left (a split focuses what it makes, so this one arms immediately;
   it is the case that makes the first two look like the exception rather than the rule).

### Findings

- **Asking the question moves the answer.** A tile's ⋮ calls `setFocus` on that tile before
  opening the palette (`panes.tsx` `openPaneCommands`, so the pane commands act on the menu
  you opened). For every other command that is invisible; for this one the act of inspecting
  the row ARMS it, because opening the menu on a tile that is not focused is itself a focus
  move. The first draft of the disabled test passed for exactly that reason. Every palette in
  these tests is now opened on the tile that already holds the focus, and the reload case is
  asserted through the KEY first, which nothing about asking can perturb.
- **Closing a pane moves the focus even when the closed pane was not focused.** Both hosts'
  `closePaneById` sets `focused` to the neighbour `closePane` returns, unconditionally. So
  "close a background pane and watch the memory go stale" is only reachable one way: closing
  a background TAB of the tile you are on, where the returned neighbour is the pane you are
  already on. That is what the integration test does, and the comment says why, because the
  obvious version of the test (close a pane in another tile) quietly tests something else —
  the focus moves, the memory re-arms, and the assertion passes for the wrong reason.
- **Two panes cannot tell `;` from `o`.** With two panes "the pane I came from" and "the next
  pane" are the same pane, and any implementation of either key looks right. Every test here
  uses three, and the integration test finishes by pressing `o` on the same layout and
  landing somewhere else.
- **`files:last` needed its own test.** The pane-bind parity test compares only TAGGED
  commands, and both new keys are untagged by design — so the Files bind is pinned by a test
  of its own plus an entry in the "binds every key to exactly one command" map. This is the
  gap the previous entry flagged, now closed for both keys.

### Verification

376 SPA tests (was 361; 17 files), `tsc --noEmit`, `npm run build`, `go test ./pkg/webui/`
clean, `pkg/webui/dist/.gitkeep` restored. Five neutralizations, each failing with the
intended diagnostic: the liveness check dropped (two tests), the memory recording the current
pane instead of the previous (five), the memory seeded with the focused pane so the command is
never disabled (two), and each screen's bind removed in turn.

`DESIGN_SYSTEM.md`'s tiling section gained the rule — one pane not a history, derived not
recorded, and the gate that is not a count.

## 2026-08-02 — Auto shell exec discovery: measure the image instead of guessing /bin/sh

The web terminal always launched `/bin/sh`. That literal appeared in four places
(`term.go:318`, `handlers.go:538`, `Terminal.tsx:46`, `TermPane.tsx:174`) and nothing
anywhere checked whether the image had it, so it was wrong at both ends of the range: an
image shipping bash dropped you into sh, and a distroless image failed `ExecStart` and
rendered the generic "Failed to start session." — a dead end that never said the image
simply has no shell.

Now the BFF probes. `POST /.cornus/web/workloads/{name}/shells` resolves a candidate list
and measures it inside the running container; the tiled terminal connects to the first hit
without asking, and when there is none the pane asks for a command.

### The probe is the candidate

One exec per candidate would cost 13+ round trips. Instead each candidate is used as the
INTERPRETER of a constant script that reports every candidate present, so the first entry
that actually runs answers for all of them in one exec — the alpine fixture costs two
(zsh misses, bash… no: `/bin/ash` is reached after eight misses on a busybox image, and
one exec answers the rest). Candidate paths travel as `"$@"` arguments, never spliced into
the script text — `listScriptCmd`'s discipline (`fs.go:855`), and it matters more here
because part of the list arrives from a browser against a BFF with no authentication.

A candidate is trusted only on `no error && ExitKnown && exit 0 && first line is the
marker`. Three of those four are load-bearing and each has a test:

- **The marker.** A binary that is not a shell can still exit 0 and print something. Without
  the marker check its stdout is read as a shell list — the neutralization returns
  `[[/bin/bash]]` from a `/bin/zsh` that printed two unrelated paths.
- **`ExitKnown`.** docker reports an exec Running for a moment after its stdio closes
  (`core.go:428-441`). An unknown status is not a negative and must not become a positive.
- **Resolved order, not output order.** The list IS the preference; the first entry is what
  gets launched. `parseShellProbe` ranks by the candidate list and the test feeds it
  deliberately shuffled output.

### What the tree refused, and was right to

`Shells` went into `api.DeploySpec` first, as asked. Five backends' tests failed
immediately: `TestEveryDeploySpecFieldIsMappedOrWarned` (barehost, containerdhost,
dockerhost, incushost, kubernetes) plus dockerhost's `TestChangedSpecCoversEveryDeploySpecField`
and `TestApplyRecreatesForEveryChangedSpecField`. `fingerprintSpec` (`dockerhost/reuse.go:67`)
hashes the WHOLE spec as JSON, so a field there is part of the container's identity —
editing a shell preference would have recreated every running container.

It moved to `compose.ServicePlan.Shells`, which is where client-side plan data already
lives (`Build`, `Provider`). It also cost nothing to move: the option was chosen partly on
my own wrong claim that a spec field "works for workloads outside the loaded compose
project". It does not — `api.DeployStatus` carries no argv and `pkg/client` has no inspect
call, so the BFF can only ever read this from a LOCALLY loaded plan either way. **A
reflection guard over a struct is worth more than the comment it replaces: this one caught
a design error three human reviews would have had to notice by reading.**

### Four sources, concatenated

`resolveShellCandidates` walks entrypoint/command → spec → context → client and dedupes on
the first occurrence. Concatenating rather than letting a specific source REPLACE the
others is deliberate: a service naming one shell should rank it first, not strand the
terminal when that shell turns out not to be in the image. `TestResolveShellCandidatesDedupesKeepingTheFirstPosition`
needed a THREE-entry fixture — with two, keep-last reorders twice and cancels itself out,
so the first version of that test passed against its own neutralization.

`Context.Shells` is classified SENSITIVE in `contextfile.go`'s `classifyFields` though it
carries no credential: a project-local `cornus-context.yaml` is a working-tree file anyone
who can open a pull request may write, and this field names a binary that gets EXECUTED
inside the developer's workload. A compose `x-cornus-shells` needs no such treatment — a
compose file already picks the image and its command, so it grants no new authority.

### One splitter

Every source spells a candidate as a STRING, split once by the BFF with the same
`go-shellwords` parser compose uses for `command:`/`entrypoint:` (`types.go:1618`). That
removed a bug the feature would otherwise have exposed: the picker's typed command became
`[cmd()]`, a single argv element, so a hand-typed `/bin/busybox sh` asked to execute a file
whose NAME contains a space — while the identical DISCOVERED candidate worked.
`createTermRequest.Cmdline` is the fix, and the response's split argv is what the pane then
remembers.

### Two tests that could not fail

Both found by neutralizing, both fixed rather than kept:

- **`TestDiscoverShellsStopsAtTheFirstCandidateThatRuns`** put the runnable candidate LAST,
  so "stopped early" and "walked to the end" issued the same number of execs. Moved to the
  middle, with a present candidate after it.
- The E2E's unknown-workload leg asserted 404 from my model of `ensureRunning`. The live
  daemon says **409**: dockerhost's `Status` lists containers by label, so an unknown name
  yields an empty status with no error — indistinguishable from a stopped deployment. The
  Go test that pins 404 is now named for the branch it actually covers (a server that
  rejects the name), and the scenario records why the real answer differs.

### Verification

`gofmt`/`go build`/`go vet`/`go test ./...` clean. 376 SPA tests, `tsc --noEmit` clean,
`make e2e-check` parses. `make e2e-web` executed against a real docker daemon: the probe
script ran through busybox `ash` in `nginx:1.27-alpine` and answered
`[["/bin/ash"],["/bin/sh"],["/bin/busybox","sh"]]` — bash, zsh, dash and `/busybox/sh`
absent, which is what makes the leg discriminating rather than an echo of its input. The
VitePress build is clean and all four localized anchors resolve.

Fifteen neutralizations in all, each failing with the intended diagnostic: compose (merge
concatenating, project default overwriting a service list, a shared backing array, the
allow-list entry dropped), clientconfig (StripSensitive not clearing, classified
non-sensitive, merge appending), the BFF (marker check, `ExitKnown`, output order, early
stop, script interpolation, keep-last dedupe, cache keyed on the workload alone), the SPA
(hardcoded `/bin/sh`, one-element argv, connecting anyway with no shell, ignoring the
setting), and the E2E (`x-cornus-shells` removed from the fixture).

### A note on the shared tree

Two `lastpane` tests failed mid-session and looked like mine. They were not: reconstructing
the git INDEX plus only the other agent's own uncommitted files (`git checkout-index
--prefix=`, never touching the working tree) reproduced both failures with none of my
changes present. Worth keeping: `git checkout-index --prefix=<dir>` and `git archive HEAD |
tar -x` both answer "is this mine?" in a concurrently-edited tree without `git stash` or
`git checkout`, which this repo forbids for exactly that reason.

## 2026-08-02 — Auto shell exec discovery, part 2: docs, and a git index that lies

Continuation of the entry above. That one closed at the code gate; this covers what the
documentation pass and the final sweep turned up.

### Four surfaces, three languages, and how to actually check a CJK anchor

The feature touches four user-facing documents, because it added a knob at every level it
reads from: `docs/cli/web.md` (the terminal section — what discovery does, the four sources
in order, the no-shell case, the cost model), `docs/cli/compose.md` (`x-cornus-shells:`,
service and project level), `docs/reference/connection-config.md` (the context `shells:`
field and its place in the trust boundary), plus `docs/reference/deploy-spec.md` — which in
the end needed NOTHING, because the field moved off `api.DeploySpec`. Each mirrored into
`docs/ja/` and `docs/zh/`.

The cross-links between them are intra-page fragments, and a fragment is the one kind of
link a VitePress build will not fail on. The check that actually settles it is to read the
ids out of the BUILT html:

```sh
grep -o 'id="[^"]*"' docs/.vitepress/dist/ja/cli/compose.html | grep シェル
# id="対話シェルの候補"
grep -c 'href="/cli/compose#interactive-shell-candidates"' docs/.vitepress/dist/cli/web.html
```

Both halves are needed: the id proves the heading slugified the way the link spells it, and
the href count proves the link survived into the page that carries it. For CJK this is not
paranoia — the slugify pipeline NFKD-normalizes and strips combining marks, and the repo
carries a `markdown.anchor.slugify` override that recomposes to NFC precisely because
voiced kana used to survive into ids and break normally-typed anchors. All eight ids and
both cross-links resolved; recording the two-command form here because "the site built" is
the answer that feels like proof and is not.

One pre-existing defect fell out of the sweep: `docs/zh/reference/connection-config.md:28`
carried a full-width colon in a code comment, which the repo's own punctuation rule
forbids. Fixed in passing, since the file was already open.

### DESIGN_SYSTEM gained two entries and one rule

- **`.warn`** — a text class, not a badge. The tree had `.badge.warn` and `.error` but
  nothing for "nobody got this wrong and you still need to read it", which is exactly the
  no-shell line. `.error` (`--bad`) blames the user for the image's contents; `.muted`
  hides the one sentence that says what to do next.
- **`.setting-textarea`** — the multi-line Settings control, `resize: vertical` only so it
  cannot be dragged out of the grid column every other `.setting-row` aligns to.
- **A `for`/`id` rule.** The pane picker's Command field became REQUIRED in the no-shell
  case, and it was a `<label>` merely adjacent to its input — announced unlabelled. Found
  by a test, not by review: `getByLabelText("Command")` failed, and the honest fix was the
  markup rather than a different query. Generalized in the doc: bind label to control
  whenever the control is required.

### The finding worth keeping: a green working tree over a red index

At close, `git status` showed `A`/`AM` on files this session created and never staged —
something in the shared tree had run `git add`, sweeping in an INTERMEDIATE state: the one
where `Shells` was still in `api.DeploySpec` and not yet on `compose.ServicePlan`. That is
the exact state whose five backend coverage tests failed.

The working tree was green. The index was not, and nothing in a normal gate looks at the
index. `git commit` (no `-a`) would have committed the broken version, with every local
check having passed minutes earlier.

Verifying it costs one command and touches nothing:

```sh
git checkout-index -a --prefix=/tmp/idxchk/
(cd /tmp/idxchk && go build ./... && go test ./pkg/deploy/dockerhost/ -run TestEveryDeploySpecField)
# --- FAIL: TestEveryDeploySpecFieldIsMappedOrWarned
```

**Generalizable: in a concurrently-edited tree, "the gate passed" is a statement about the
WORKING TREE only.** `git checkout-index --prefix=` materializes the index somewhere else,
so what would actually be committed can be built and tested without `git stash`, `git
checkout`, or any write to the tree another agent is using — the same technique the part-1
entry used to prove the `lastpane` failures were not mine, pointed at a different question.
Worth running before any hand-off that ends in a commit, not only when something looks
wrong. Left as-is and reported rather than restaged: re-staging is a git operation nobody
asked for, in a directory somebody else is editing.

### Verification at close

`gofmt`/`go build`/`go vet`/`go test ./...` clean; 376 SPA tests; `tsc --noEmit` clean;
`make e2e-check` parses; `make e2e-web` green against a real docker daemon; VitePress build
clean with all eight localized anchors and both cross-links resolving; no decomposed kana
and no full-width parentheses or colons anywhere under `docs/` or `.agents/docs/`.

## 2026-08-02 — the focus ring nobody could see (web/)

Reported as "the focus ring on a header menu item or a breadcrumb is barely recognizable".
It was two independent defects stacked on the same two controls, and either alone would
have been enough to hide the ring.

**Colour.** `:focus-visible` painted `0 0 0 3px var(--accent-subtle)`. That token is a 10%
wash: composited on the page it measures ~1.2:1, against WCAG 2.2's 3:1 floor for a focus
indicator. Worse than dim, though — `--accent-subtle` is also the FILL of `.appbar-nav
a.active` and of `.cmd-item.selected`, so on the active nav link and the highlighted palette
row the ring was drawn in the colour of the surface under it. The ring is now
`--focus-ring: 0 0 0 2px var(--accent)`: 8.2:1 light, 5.8:1 dark, 7.0:1 on the pill. Defined
once — the `var(--accent)` inside a custom property resolves against the value the element
inherits, so the dark-mode block needs no second definition.

**Clipping, which is the half that made it "barely" rather than "faintly".** A focus ring is
ink outside the border box, and a scroll container clips its descendants' ink overflow to its
padding box. Both reported controls sit in a container of exactly that kind AND fill it:

- `.appbar-nav` is `overflow-x: auto`, which is enough — `overflow-y` computes from `visible`
  to `auto` the moment its partner is not `visible`, so a bar declared as horizontally
  scrollable clips vertically too. The nav is as tall as the pills inside it, so the ring had
  nowhere to go on any side; what survived was a 3px sliver of 10% tint in the 4px gap
  between two links.
- `.stack-subheader .crumbs` is `overflow: hidden`, and that cannot be dropped: the left fade
  and PaneCrumbs' `scrollLeft` end-anchoring are both built on it. Same shape, same result.

Fix: each lends the ring padding at least as wide as the ring and hands it straight back with
a matching negative margin, so the box covers the same region it always did and the ring has
somewhere to land inside the clip.

**Findings**

- **A ring token has to be checked against the fills it lands on, not just the page.** The
  contrast number that matters is the one on the active-nav pill and the selected palette
  row, because those are where a keyboard user actually stops. Checking only against `--bg`
  would have said 1.2:1 — bad but arguable; against the pill it was ~1.0:1, which is not a
  ring at all. Both premises are asserted in the test, so the day either fill changes the
  requirement is re-stated rather than silently satisfied.
- **`overflow-x: auto` clips the other axis.** Easy to read `.appbar-nav`'s rule as "scrolls
  sideways, leaves the rest alone". It does not, and a control that fills its scrollport
  therefore loses the entire ring rather than an edge of it. This is a general trap: any
  future scroller holding focusable children needs the same padding/negative-margin pair.
- **An opt-out that works by accident stops working when the accident is fixed.**
  `.pane-pick-overlay:focus { box-shadow: none }` was documented as suppressing a ring that
  "vanishes against its own backdrop" — true only because the ring and the backdrop were both
  `--accent-subtle`. With a solid ring the suppression became load-bearing. The rule was
  already there; its comment and its test now say why it is a decision, not a coincidence.
- **The neutralization that mattered was the cancellation, not the padding.** Padding alone
  makes the ring visible; padding without the matching negative margin also moves the nav
  links and grows the sub-header. So the test resolves both numbers through the token table
  and asserts `margin == -padding` on each axis, which is the property a future edit is most
  likely to break while leaving the ring looking fine.

**Verification.** 378 SPA tests across 17 files (two new, both in `describe("Stylesheet")`),
`tsc --noEmit` clean, `npm run build` clean with `pkg/webui/dist/.gitkeep` restored,
`go test ./pkg/webui/` ok. Four neutralizations, each failing as intended: token reverted to
`--accent-subtle` (colour test fails on the token shape); `:focus-visible` given an inline
value instead of the token (colour test AND the pick-overlay test fail, which is the coupling
working); `.appbar-nav` padding cut to 1px (fails the ring-width relation); crumbs margin
changed so it no longer cancels its padding (fails the equality on both axes).
`.agents/docs/DESIGN_SYSTEM.md` gains a **Keyboard focus** section with the token, the
clipping rule, the opt-out rule, and why fields are exempt.

## 2026-08-02 — and no ring at all in a file listing (web/)

Immediate follow-up to the entry above, and the converse of it: with the ring now solid,
the Files browse pane was the one place it read as noise rather than information.

The reason is shape, not taste. The rows carry a roving tabindex on the name LINK, so the
ring boxes a word inside a row instead of marking the row — and it does that on top of two
cues already pointing at the same place: the row's `.fs-selected` fill (focusing a row runs
`selectOnly`, so the row lights up) and `.fs-list:focus-within`'s accent border. Three marks
for one fact, and the smallest of them was the only one that was not row-shaped.
`.fs-list:focus-visible, .fs-list a:focus-visible` now take `box-shadow: none` — the list
itself is focusable only when EMPTY, where a ring round an empty box says nothing its border
has not.

**The finding, which is the whole of why this is not a one-line deletion.** Suppressing an
indicator is only safe if you enumerate what it was carrying and check each item is carried
elsewhere. Two of the three facts survived on the other cues. The third did not: inside a
shift+Arrow selection every row in the range wears the same fill, so the row holding the
CURSOR — the origin of the next Arrow — was distinguishable only by the ring being gone.
That is replaced by a 2px inset `--accent` bar on `tr:focus-within > td:first-child`: row
shaped, at the ring's own weight, and inset so `.fs-list`'s `overflow: auto` cannot clip it
the way that same overflow clips an outset ring (the trap from the previous entry).

The test asserts the suppression *and* the two cues it leans on, because the failure mode
worth catching is not "someone restores the ring" — it is someone later simplifying
`.fs-selected` or the `:focus-within` border and turning a justified suppression into an
invisible focus. The cursor bar's width is resolved from `--focus-ring` rather than written
twice.

**Verification.** 379 SPA tests (one new), `tsc --noEmit` clean, `npm run build` clean with
`pkg/webui/dist/.gitkeep` restored. Three neutralizations, each failing as intended: the
suppression turned back into `var(--focus-ring)`; the cursor bar thinned to 1px (fails the
relation to the ring's weight); `.fs-list:focus-within` demoted to `--border-strong` (fails
the standing-cue assertion, which is the one that matters).

## 2026-08-02 — F5 / F6: cross-pane copy and move in the Files workspace (web/)

Asked for as two commands — "Copy selected to another pane" (`prefix Ctrl+Shift+C`, or a
bare `F5`) and "Move selected to another pane" (`prefix Ctrl+Shift+M`, `F6`) — reusing the
pane chooser as the destination picker. Three follow-ups during the work reshaped it: F5/F6
are NOT prefixed; the old prompt-driven "Copy … to…" is folded in as the chooser's "an
arbitrary location", taken by typing; and that route ends by opening a tab on the
destination. The two commands took the ids `files:copy` / `files:move` the old one vacated.

**Four facilities came out of it, each general rather than Files-specific.**

1. **`Command.bind` may be an array.** One command, several spellings, all of them shown —
   `bindsOf` normalizes, and uniqueness is per SPELLING because the lookup takes the first
   match and a duplicate silently disables the loser.
2. **`Command.direct` — keys with no prefix.** A separate lookup, because it takes the key
   from the BROWSER rather than from the prefix window. The sequencing lives in
   `dispatchAppKey` in command-center rather than inline in `App.tsx`'s listener, which
   keeps only the "who holds the keyboard" exclusions; that split is what made the gating
   testable without mounting an App, and it is the part with the most to get wrong.
3. **`ChoosePurpose` on the pane chooser** — `{ title, pick, refuse?, elsewhere? }`. The
   mode now answers "which pane do these files go to?" with the same walk, numbers and
   Escape as "which pane do I want to be in?", which is the same question twice.
4. **`FilePane.receiveInto`** — the drag-and-drop transfer with the gesture taken out of it.
   Three callers now: a drop, a cross-pane pick, and a typed path. So the new routes inherit
   the preflight, the overwrite confirmation, the ghost rows, the batch request and the
   partial-failure reporting instead of growing a second set of answers.

**Findings**

- **A disabled UNPREFIXED key must still swallow, and the argument is the opposite way round
  from the prefixed case.** For `prefix x` the rule is "a disabled command owns its key"
  because falling through would emit a browser shortcut nobody asked for. For `F5` the key
  underneath is Reload — so falling through would answer "this cannot run just now" by
  discarding every unsaved editor draft in the workspace (`files/drafts.ts` is memory-only).
  Same rule, stronger reason. It also forced a registration decision: the two transfers are
  registered throughout a mount and DISABLED when nothing is selected, because a command
  that came and went with the selection would make one key mean "copy" or "reload" depending
  on something the user is not looking at.
- **The gate on a command must not be the gate on its dialog.** The first version disabled
  both transfers when no open pane could receive — correct for the pane route, and exactly
  wrong once the chooser gained a typed-path escape: a lone pane with a row selected is a
  perfectly good copy. It was caught by an existing test rather than by reasoning (the old
  prompt-copy test, which runs single-pane), which is the useful part: the test survived a
  rewrite of what it was testing and still knew something the rewrite had forgotten.
- **A refused row keeps its place in the list.** Filtering refused panes out was tempting and
  is wrong: the numbers are POSITIONS in layout order, so a hole renumbers the tiles the walk
  and the digit keys are named after. They stay, greyed, wearing their reason — and `Enter`
  on one does not end the mode, because a wrong turn during the walk costs nothing and being
  told "not that one" should not cost more. What does change is where the mode OPENS:
  `begin()` falls forward to the first pane the purpose accepts, which is vacuous for the
  plain chooser and is what stops a transfer opening on its own greyed source.
- **Typing to escape a list costs the list its letters, and the cost has to be visible.**
  Any bare letter now leaves the chooser for "an arbitrary location", carrying that letter
  into the prompt — so `hjkl` are no longer movement while an escape is offered. The
  alternative (exempt `hjkl`) is worse: a destination beginning with h, j, k or l would be
  untypeable, and that is not a rule anyone could guess. `.pane-chooser-keys` drops `hjkl`
  and gains `a–z types a path`, which is the whole mitigation.
- **A seeded prompt needs the CARET, not just the value.** `TextBody` selected its initial
  value — right when it is a suggestion the first keystroke should replace, exactly wrong for
  a letter the user has already typed, which the second keystroke would then eat. `caretAtEnd`
  in `modal.ts`. The value assertion alone passes either way; only `selectionStart` sees it.
- **Extracting a destination-local routine has to hunt for what "local" was hiding.**
  `receiveInto` was the receiving pane's, so `readOnly()`, the ghost rows and `flashArrived`
  all silently meant "the destination". Called with a typed path, `readOnly()` became the
  SOURCE's flag (it would have refused a legal copy out of a read-only mount) and
  `flashArrived` marked `baseOf(destDir)` in the source listing — flashing whichever unrelated
  row happened to share the destination folder's name. Both are now conditioned on the
  destination actually being here. Also: the "cannot drop a folder into itself" toast said
  the GESTURE's word, which stopped being true; it now says the operation's, which
  distinguishes copy from move as "drop" never did.
- **What was given up, deliberately.** The old single-row copy asked for a full virtual PATH
  prefilled with the source, so it doubled as copy-and-rename in one step. "A location" is a
  folder whether you are sending one file or forty, and a field that means two different
  things depending on the selection size is one you have to count rows to read. Copy then
  Rename (`e`) is the two-step form. `y` is now unbound and left that way: these keys were
  asked for by name, and a third spelling on Copy with no twin on Move is an asymmetry
  nobody could predict.

**Verification.** 396 SPA tests across 17 files (up from 379), `tsc --noEmit` clean,
`npm run build` clean with `pkg/webui/dist/.gitkeep` restored, `go test ./pkg/webui/` ok.
Seven neutralizations, each failing as intended: `direct: "F5"` removed (3 tests, including
the palette's two caps); the chooser's refusal handling dropped from both `begin` and
`commit` (the unit test AND both integration transfers); the pick hard-coded to copy (the F6
wire assertion, which is the only place copy and move differ — the resulting tree does not);
the escape's seed replaced with "" (the field's value); `caretAtEnd` ignored (the field's
caret, with the value still passing); `openTabAt` dropped (the tab labels); and a disabled
direct key allowed to fall through (the unit test and the F5-owns-its-key test).

## 2026-08-02 — opening the folder you picked out (web/ Files)

Three related asks on top of the F5/F6 work: an "Open in a new tab" command on a bare
`Ctrl+T` (`Cmd+T` on a Mac), gated on exactly one DIRECTORY row being selected; "New pane…"
starting on that directory instead of at the virtual root; and a Ctrl/Cmd-click on a
directory running the new-pane action. All three share one predicate, `selectedDir()` — one
row, and that row a folder — read from `selectionItems()` because a name cannot say which
rows are folders and telling them apart is the whole condition.

**Findings**

- **Ctrl+T is not ours to take, and it is still the right bind.** Chrome and Firefox reserve
  it: it is delivered to the browser chrome, not the page, and `preventDefault` has no say.
  So in an ordinary tab the key never arrives. It is bound anyway — it is the key users
  reach for, it works in an installed/PWA window where nothing is competing for it, and the
  palette is the route everywhere else. Choosing a key that always fires would have meant
  choosing one nobody would press. Recorded here because a test of `dispatchAppKey` proves
  the dispatcher right and says nothing at all about a real browser tab.
- **One platform's spelling, not both.** `Meta+T` on a Mac, `Ctrl+T` elsewhere, picked once
  at module load from the existing `isMacPlatform`. The test asserts the wrong-platform
  spelling is ABSENT as well as the right one present: registering both would advertise, in
  the palette, a key that does nothing on the machine reading it.
- **The Ctrl-click gesture collides with multi-select, and the LINK is the line between
  them.** Ctrl/Cmd-click is already this listing's "toggle this row", on every platform. The
  new gesture takes only the folder's NAME — the part that is an `<a>`, which is what makes
  the browser's "modified click on a link opens it elsewhere" idiom apply at all — and only
  when the row is a directory. Ctrl-clicking anywhere else in a folder's row, or any part of
  a file's row, still toggles. What is given up is ctrl-clicking a folder's own text to add
  it to a selection. The neutralization that proves the line is real is the one that moved
  the gesture onto the `<tr>`: it broke the pre-existing multi-select test, which is the
  regression the scoping exists to prevent.
- **"New pane…" keeps its title and its key on both screens.** It is the same command with a
  better starting point, not a second one, and the pane binds are compared Terminal against
  Files by a test keyed on TITLE — so a "New pane here…" variant would have read as drift in
  the one place the two screens are held identical.
- **The disabled-but-registered rule earned its second and third users.** `files:open-tab`
  is registered throughout a mount and disabled with "select one folder" otherwise, for the
  same reason as the transfers: its key is unprefixed, so a command that came and went with
  the selection would make `Ctrl+T` mean "open this folder" or "open a browser tab"
  depending on what happens to be highlighted. Two folders selected is disabled too — taking
  the first would open a tab nobody asked for and ignore the other in silence.

**Verification.** 400 SPA tests (four new), `tsc --noEmit` clean, `npm run build` clean with
`pkg/webui/dist/.gitkeep` restored, `go test ./pkg/webui/` ok. Four neutralizations, each
failing as intended: `beginPlace` armed without the selected folder (the new pane lists the
mounts again); `direct` demoted to `bind` (both Ctrl+T tests and the registry check); the
ctrl-click moved from the name to the whole row (the pre-existing multi-select test, plus
the open-tab gate); and the folder check dropped so files open too.

## 2026-08-03 — the two keys from the entry above, corrected (web/ Files)

Both bindings added yesterday for "open the folder you picked out" were changed on review.
The reasoning that produced the originals is in the previous entry; this is what replaced
them and why, because in both cases the first choice was defensible and still wrong.

**`Ctrl+T` / `Cmd+T` → `Ctrl+Enter` / `Cmd+Enter`.** The original was the platform's literal
new-tab accelerator, chosen knowingly against its reserved status — the previous entry
records the trade as "the right key even though it never arrives in a browser tab". That is
a bad trade when a key that DOES arrive reads just as well: `Ctrl+Enter` is "Enter, but
somewhere else", and plain Enter on a row is already this listing's open, so the modifier
varies a gesture the user has rather than naming a new one. **The finding worth keeping is
the one that made the first choice look fine: a `dispatchAppKey` test passes identically for
a key no user can press.** Nothing in the suite could have told me `Ctrl+T` was reserved;
only knowing the browser's list could.

It costs one thing, recorded because it is silent: `onListKey` never looked at modifiers, so
`Ctrl+Enter` used to fall through to the listing's own Enter and open the row under the
cursor. It is now claimed, and does nothing when the selection is not one folder. A test
asserts plain `Enter` is NOT claimed — a chord bind that swallowed its unmodified key would
take the screen's primary gesture away, and would do it silently.

**Ctrl/Cmd-click → Shift-click**, so that Ctrl/Cmd-click goes back to meaning multi-select
and nothing else. The collision is the same shape either way (the listing owns both
modifiers) and the mitigation is unchanged: the gesture takes the folder's NAME only — the
part that is an `<a>`, which is what makes the browser's "modified click on a link opens it
elsewhere" idiom apply — and only when the row is a directory. What is given up moved with
it, and got slightly worse: ending a range ON a folder's own text no longer works, and the
name is the widest target in its row. The rest of the row still extends. The guard is bare
Shift, not "shift among others": a shifted click also carrying Ctrl or Alt is somebody
else's gesture, and guessing at it would make range-extend unreachable in exactly the case
where the user was most deliberate about what they held.

**Verification.** 400 SPA tests, `tsc --noEmit` clean, `npm run build` clean with
`pkg/webui/dist/.gitkeep` restored, `go test ./pkg/webui/` ok. Two further neutralizations:
the click guard widened to accept Ctrl/Cmd as well (the ctrl-is-still-multi-select assertion
fails, which is the whole point of the change); and the chord replaced with bare `Enter`
(both Ctrl+Enter tests and the registry check fail).

## 2026-08-03 — session wrap: what the five web/ entries above have in common

Five entries were appended over this session, all in `web/`, all reached by a chain of small
requests rather than one plan: the focus ring's contrast and clipping; the file listing's
deliberate lack of one; F5/F6 cross-pane copy and move with the pane chooser as the
destination picker; opening a picked-out folder in a tab or a pane; and a correction of both
keys that last one introduced. They are separate pieces of work and stay separate entries.
What follows is only the part that is invisible from any one of them.

**Three general facilities came out of feature requests, and are now the interesting
inheritance.** `Command.bind` may be an array; `Command.direct` names keys that need no
prefix, with the sequencing in `dispatchAppKey` rather than inline in `App.tsx`; and the
pane chooser carries a `ChoosePurpose`, so "which pane?" can be asked for something other
than moving the focus. Each was introduced for one caller and each already has two. The
DESIGN_SYSTEM sections are written for the next caller rather than the ones that exist.

**Findings that only show up across the five**

- **"Registered but disabled" was the right answer three times, and the ARGUMENT got
  stronger each time while the rule stayed the same.** For a prefixed bind (`prefix x`) the
  reason is that falling through emits a browser shortcut nobody asked for. For `F5` it is
  that the key underneath is Reload, which discards every unsaved editor draft in the
  workspace. For `Ctrl+Enter` it is that the key underneath is the listing's own open, which
  would act on whatever row the cursor happens to be on. The generalization: **the cost of
  letting a disabled command's key fall through is the cost of whatever owns that key
  underneath, so the rule is not "always swallow" but "look at what is underneath, and it is
  usually worse than nothing".** It also drives a registration decision every time — a
  command whose key is shared with something else must be registered across the whole
  context and disabled, never conditionally registered, or the key changes meaning under the
  user.
- **Removing something safely means enumerating what it was carrying, item by item.** Twice,
  on unrelated subjects. Dropping the focus ring from the file listing: three facts, two
  already carried by other cues, and the third — which row the CURSOR is on inside a
  multi-row selection — carried by nothing, which is why a 2px inset bar replaced it.
  Retiring the old `files:copy` prompt: the destination survived as the chooser's escape
  hatch, the preflight and overwrite confirmation were gained rather than lost, and
  copy-and-rename-in-one-step was genuinely dropped and had to be said out loud. In both
  cases the enumeration, not the removal, was the work.
- **An old test caught a design error in the rewrite of the thing it tested — twice.** The
  single-pane "copy via a palette command" test failed when the new transfer gated itself on
  "some pane could receive this", which is wrong once a typed destination exists. The
  ctrl-click multi-select test failed when the new folder gesture was neutralized onto the
  whole row. Neither failure was one I predicted. The pattern: a test written against a
  BEHAVIOUR the user has (copy to a path; ctrl-click toggles) outlives the implementation it
  was written for, and is exactly the test that survives to object. Tests written against
  the shape of the code do not do this.
- **A green suite says nothing about the environment's constraints.** `Ctrl+T` passed every
  test as a `direct` key and can never be pressed in a browser tab — Chrome and Firefox
  reserve it. There is no test that would have caught it and no test that could be added;
  the only defence is knowing the platform's reserved list before choosing. Filed under the
  same heading as "what would a PASS look like if this claim were false": here the claim was
  not about the code at all.
- **The reused component is the one that has to state its variations.** Reusing the pane
  chooser for transfers, rather than growing a second picker, was the cheap decision; the
  expensive part was the three places the reuse had to be made honest — the `aria-label` had
  to become the purpose's title, the key hint had to stop advertising `hjkl` when letters
  became the escape, and the "no ring here" rule that had worked by colour coincidence had
  to be stated explicitly once the ring changed colour. Each was a place the component
  described itself in terms of its first caller.

**Verification across the session.** 400 SPA tests across 17 files (from 376 at the start),
`tsc --noEmit` clean throughout, `npm run build` clean with `pkg/webui/dist/.gitkeep`
restored after each build, `go build ./...` and `go test ./pkg/webui/` ok. Twenty
neutralizations in total across the five entries, each recorded in its own entry with the
failure it produced. No commits.

## 2026-08-03 — Files and Terminal become one Workspace (web/)

**The request.** Unify the Files and Terminal screens into a single screen called
"Workspace", showing a single file browser pane by default, and add an "open in a terminal"
command that opens a terminal in a new pane. Four design questions were settled with the
user before any code: the terminal takes a working directory (a small BFF change) rather
than starting wherever the image puts you; the new pane is placed with the existing
wireframe rather than appearing somewhere chosen for you; outside a workload the command is
listed and disabled with the reason rather than absent; and the two saved layouts are
abandoned rather than migrated. A fifth answer arrived mid-plan — "forget about the
compatibility" — which removed the `/files` and `/terminal` route aliases the plan had
proposed keeping.

**What the merge actually cost, and why it was so little.** The two screens were the same
screen twice. `Files.tsx` (708 lines) and `Terminal.tsx` (425) shared the same store shape,
the same helper names down to the identifiers, a persistence effect differing only in its
key, and a JSX return block that was character-identical. They registered the same ten pane
commands on the same ten binds, under `files:` and `term:` ids that a test existed solely to
hold equal. What kept them apart was one thing: the pane payload. Everything below them —
`tiling/layout.ts`, `tiling/panes.tsx`, `tiling/drag.ts`, `tiling/choose.ts` — was already
generic in that payload and **none of it changed**.

So the merge is `PaneData = FileData | TermData`, each tagged with `kind`, and one host over
it. The tag went on the two existing payload types rather than wrapping them, which keeps
`updatePane(tree, id, { path })` typechecking (`Partial<A | B>` distributes) and lets
`FilePane` / `TermPane` keep their existing prop types; two guards do the narrowing
TypeScript cannot do from `pane.data.kind`, one runtime check each behind a single cast.

**Findings.**

- **Every seam that needed to know the kind was already a host-supplied callback.** Not one
  new extension point was added to `tiling/`. `isValidData`, `fresh` / `inherit`,
  `closePane`'s replacement factory, `splitPane`'s `makeData`, `beginPlace(make?)`,
  `tabTitle` / `body` / `subHeader` — the list of things a merged host has to answer
  differently is exactly the list of things the tiling module had already refused to decide.
  That is what a payload-agnostic module buys, and it was only visible once something
  actually needed two payloads.
- **Two rules made "which kind is this pane?" a question nobody ever has to ask.** A pane's
  kind is settled by the command that created it: `New pane…` makes the empty pane (a file
  browser at the mount list), a split inherits the source's kind, `Open in a terminal…`
  makes terminals. No kind picker, no mode, no empty pane asking what it wants to be — which
  was the biggest open UX question the exploration surfaced, and it dissolved rather than
  being answered.
- **The old screens disagreed about what an edge split makes, and the merge had to break the
  tie.** Files inherited ("another view here"); Terminal made a FRESH empty pane so its
  picker could aim at a different workload. Inherit won, because a fresh pane is now a file
  browser and splitting a terminal's edge to get a mount list is not what the gesture says.
  The capability that lost — retargeting by splitting — is now "browse to the other workload
  and open a terminal there", which says where in the same motion.
- **The disabled reasons are read from the WORKLOAD LIST, not from the BFF's own rule for
  resolving a virtual path.** `resolveVirtual` (fs.go) treats any mount that is not a local
  root as a workload, which is right for it: an unknown mount should surface as an ordinary
  container not-found. A command that must decide BEFORE it acts needs the opposite, and
  only the workload list can tell "that is a local folder" apart from "that container is
  stopped". The plan said to use `getFsRoots`; the workload list turned out to be both
  simpler and strictly more informative, and it removed a resource.
- **A pane claims DOM focus when it mounts as the focused one — and only terminals ever
  did.** `tiling/drag.ts` has carried a focus-before-commit rule for exactly this, and
  TermPane's picker was its only client. Once `New pane…` made file panes, the main keyboard
  path landed on `<body>`: the pick's targets vanish on commit, and nothing took the
  keyboard. The fix is symmetric (the file pane puts the cursor on its first row, with the
  selection left alone) and, notably, it broke **nothing** — 78 tests render a file browser
  and none of them cared.
- **The guard on that focus grab is about who OWNS the keyboard, not "is anything focused".**
  The first version refused to take focus if anything else held it. That is wrong twice
  over: the pane next door holding focus is STALE the moment the placement named this one
  (and yielding to it is precisely the bug the focus-before-commit rule exists to prevent),
  while a pick overlay, a modal or the palette is a mode the user is inside. Dropping the
  guard entirely broke two pick-mode tests; keeping the naive form broke the placement
  tests. The list that works is the same one `App.tsx`'s key handler stands aside for.
- **A helper that reads one kind's markup silently excludes the other.** `tabNames()` in the
  tests reads `.tab-name`, a span only the terminal's `tabTitle` rendered — file tabs
  returned a bare string. A merged tab bar where half the labels answer to the class and
  half do not cannot be addressed uniformly, so the fix was in the component, not the
  helper. It cost forty minutes of chasing a placement that had in fact committed correctly.
- **The working directory is a preference, and the docs say so.** `ExecConfig.WorkingDir`
  already existed and is honoured by docker, containerd, bare-host and Incus; Kubernetes
  cannot express one (`PodExecOptions` has no such field) and already warns-and-ignores. The
  BFF accepts only a clean absolute path and DROPS anything else rather than rejecting it —
  a bad `dir` should cost the user the cwd, not the terminal — and the comment says out loud
  that this is hygiene and not a boundary, because the shell it opens can `cd` anywhere the
  container allows the instant it starts. That is the whole point of the feature.
- **A test seeded by a helper cannot also be a reload test.** Three tests do
  `cleanup(); render()` to assert what comes back from localStorage. The seeding helper
  written for the terminal suite writes its own layout over the persisted one, so those
  three passed for the opposite reason — a fresh pane that re-probes, a two-pane layout
  replaced by one. Each now mounts the screen directly, with a comment saying why.

**A duplicate-session race, observed but not fixed.** Splitting a terminal in the window
between "the create request was sent" and "the pane recorded its session id" produces THREE
sessions for two panes: the split rebuilds the original pane, which cannot yet see it has a
session, and opens another. Seen while writing the split test, which now waits for the
terminal itself. Pre-existing — the same window existed on the Terminal screen — and filed
in TODO.md rather than fixed here.

**Verification.** 407 SPA tests across 17 files (from 400), seven new ones for what only a
merged tree can get wrong, each with a stated false-pass and each neutralized by breaking
the fix and confirming the intended diagnostic: the default pane kind, the create request's
`dir`, the three disabled reasons, refusal by kind, the union validator, the close question,
and the split's inherit (twice — dropping `dir`, and carrying `sessionId`). One new Go test,
`TestTermCreateWorkingDir`, neutralized the same way. `tsc --noEmit` clean, `npm run build`
clean with `pkg/webui/dist/.gitkeep` restored, `gofmt -l` silent, `go build ./...`,
`go vet ./...` and `go test ./cmd/cornus/internal/webbff/ ./pkg/webui/` ok. `docs:check`
passes punctuation, build, anchors and duplicate targets; `cli/web.md` recorded fresh in
both locales (six other pages were already stale before this session and were left alone).
No commits.

## 2026-08-03 — the activity badge that vanished exactly when it mattered (web/)

**Reported.** The terminal status indicator went blank when the running command (a mock
`claude`) presented a prompt — `Apply 3 changes to this deployment? [y/n]`. Refined by the
reporter twice, and each refinement moved the search: first "the BFF seemed to correctly
capture the status, but no badge was shown in the tab title", then "I saw a gap after the
tab title, but nothing was shown in the gap".

**What it was not.** The detector was fine — a probe confirmed `[y/n]` classifies as
`blocked` bare and boxed. The DOM was fine too: dumping the tab at the moment the poll
reports `blocked` gives exactly
`<span class="badge warn" title="Detected session activity">needs you</span>` inside
`.tab-label`. Both of those were checked rather than assumed, and both were dead ends that
cost less than the guessing they replaced.

**What it was.** `.tab-label` is a flex item of `.tab`, so it is blockified, and
`overflow: hidden` gives a flex item an automatic minimum size of 0 — it shrinks to whatever
`.tab`'s `max-width: 16rem` leaves and CLIPS the rest. The name comes first in the label, so
the name kept the width it wanted and the badge, sitting after it, was the first thing cut
off. What remained on screen was the badge's clipped left padding: a gap after the title
with nothing in it, which is precisely what the reporter described.

**Findings.**

- **The bug hid behind a width threshold AND behind the two badges being different
  widths.** With a long label, `working` fits inside 16rem where `needs you` does not — so
  the indicator worked all the way up to the moment a session started needing a human, and
  blanked exactly then. That reads as "the prompt broke the detector", which is where the
  report pointed and where the code is innocent. A failure whose trigger correlates with the
  feature's subject is the kind that sends you to the wrong file.
- **"Nothing is shown" and "a gap is shown" are different bugs, and the second sentence was
  worth more than the first.** No badge at all points at state, reactivity, or the poll. A
  gap says the element is laid out and its content is not painted — which rules out
  everything upstream of layout in one observation. The refinement is what turned a search
  into a lookup.
- **The fix un-loads a measured hack rather than adding one.** `.tab .badge` carries a
  comment about the bar jumping 5.41px when a session started working, because the badge's
  inline-block BASELINE grew `.tab-label`'s line box. With `.tab-label` a centred flex row
  there is no baseline effect at all: the row is the taller of the two boxes, and the badge
  (smaller font, tight leading) is not it. The old workaround stays, but it is no longer
  what holds the bar still.
- **The Workspace merge is why this was cheap to fix, not why it broke.** Unifying the tab
  label under `.tab-name` for both pane kinds — done days earlier for an unrelated reason,
  so a test helper reading `.tab-name` would see file tabs too — is exactly what let
  `.tab-label` become a flex row with one shrinkable child. Had file tabs still returned a
  bare string, the same rule would have had nothing to shrink on half the tabs.
- **A green suite says nothing about layout, and the honest half of the test is the
  negative one.** jsdom does no layout and its CSSOM is empty for `styles.css`, so the
  regression test can only pin the rules through `styles.css?raw`. Asserting the flex row
  exists would pass with the old clipping still sitting beside it; what the test really
  turns on is `expect(label).not.toMatch(/text-overflow/)` — the clipping must have MOVED,
  not merely been joined. Both halves were neutralized (restore the clip on `.tab-label`;
  drop `flex: 0 0 auto` from the badge) and each failed with its own diagnostic.
- **A naive text helper matched my own comment.** `block(css, header)` is `indexOf` plus
  brace counting, so writing the literal `.tab .badge` inside the comment above `.tab-label`
  made the helper return the wrong rule body. Renaming the reference in prose fixed it. The
  helper is used by a dozen tests and is not worth hardening; the trap is worth knowing.

**One test corrected rather than kept.** The first version of the DOM-path test failed and
looked like a reproduction — `findByText("needs you")` not found after a session went
blocked. It was testing-library's 1s default against the 2s session poll. It passes with a
5s timeout, and the comment now says why the number is what it is. Nearly the whole
investigation was spent on a false positive I created; the lesson is the standing one, asked
one step earlier: before believing a failing test, ask what a FAIL would look like if the
code were correct.

**Verification.** 409 SPA tests (from 407): one new stylesheet test with both halves
neutralized, and one new DOM-path test that drives the ordinary route — the pane creates its
own session and picks the badge up when the id arrives, which every other badge test skips
by seeding a layout that already holds one. It needed a new mock helper, `setTerminalState`,
because `seedTerminals` replaces the list and the id here is minted by the pane. `tsc`
clean, `npm run build` clean with `.gitkeep` restored, `go build ./...` and
`go test ./pkg/webui/` ok. **Not verified in a browser**: the bar's height is measured
territory and the reasoning above is reasoning, not a measurement.

## 2026-08-03 — I deleted 600 lines of styles.css with an index slice (web/)

**What happened.** Four small styling requests arrived in sequence (pulse the "needs you"
badge; make it gradient-stop motion; pulse the pane number too; invert the number and give
the two different animations). Each was applied with a Python `str.replace` script. The
fourth used an index slice instead:

```python
old_start = s.index("/* The pane's NUMBER carries the same alarm")
old_end   = s.index("/* tmux's display-panes: big enough")
s = s[:old_start] + new + s[old_end:]
```

The first marker was a comment I had inserted next to the BADGE rules, near the top of the
sheet. The second was ~900 lines further down. Everything between them — cards, tables, the
whole workspace and tab chrome, the pane chooser, settings — was replaced by a 40-line
block. `styles.css` went from 2447 lines to 1850, the test suite lit up with a dozen
unrelated failures, and the reporter saw the app render unstyled.

**The rule I broke, in the reporter's words: back up before any mechanical edit.** I had
been taking backups all session — but only around the NEUTRALIZATION passes, where I knew I
was about to break something on purpose, and I deleted each one the moment the pass was
green. The one edit that actually needed a backup was the one I did not think of as
dangerous. The lesson is not "back up before edits you expect to fail"; it is that a script
that computes its own boundaries is exactly where the expectation is worthless, because the
failure mode is not a bad replacement, it is a correct replacement of the wrong region.

**Findings.**

- **`str.replace` fails safe; slicing by `index()` fails silently and enormously.** A
  replace that does not match changes nothing, which is why hundreds of them this session
  cost nothing when they missed. A slice always "succeeds": both markers were found, and the
  result was a syntactically valid stylesheet that simply had a hole in it. The blast radius
  is set by the DISTANCE between two markers, which is not visible at the call site.
- **The marker was ambiguous in a way only the file could know.** `"/* The pane's NUMBER
  carries the same alarm"` was unique — but it was not where I pictured it. I had placed
  that rule beside the badge rules for narrative reasons and then reasoned about it as
  though it sat with the other `.pane-number` rules. A slice between two markers requires
  knowing where BOTH are, and I only checked that each existed.
- **The recovery existed because the build output is a record of every rule.**
  `pkg/webui/dist/assets/*.css` was from the last `npm run build`, which had run after the
  tab-label fix and before this work — minified and comment-free, but authoritative about
  which SELECTORS and DECLARATIONS should exist. Splicing HEAD's copy back in restored the
  committed base and its comments; diffing the result's selector set against the built sheet
  found exactly the four rules that were neither in HEAD nor recoverable from it (the pane
  chooser's disabled/why/other rules, added by an earlier uncommitted session), and the
  built file gave their declarations verbatim. Two "missing" entries in that diff were my
  own normalisation artifacts (`::after` vs `:after`, quoted attribute selectors) and were
  checked by hand rather than believed.
- **The git index was not the safety net it looked like.** `git status` showed `MM` for
  styles.css, which reads as "there is a staged copy to fall back on". There was: it was
  byte-identical to HEAD for every marker that mattered. The uncommitted work of two sessions
  lived only in the working tree, which is precisely the state in which a destructive edit
  is unrecoverable — and precisely the state this repo is usually in, since it forbids
  discretionary commits.
- **One thing was genuinely lost and had to be rewritten rather than restored.** The prior
  session had rewritten the comments on `.pane-pick-overlay:focus` and `.pane-chooser:focus`
  to drop an obsolete "3px `--accent-subtle` halo" rationale. Restoring from HEAD brought the
  obsolete text back, and no build artifact records comments. Caught by reading the restored
  span rather than by any check, and rewritten against what the ring is now. **Anything that
  lives only in a comment is unrecoverable from a build.**

**Verification of the restore, not just of the feature.** 412 tests green; a selector-set
diff between the restored source and the last good build reports nothing missing; a fresh
build carries every rule the old one did plus the new ones; the whole-file diff against HEAD
is 270 insertions and 9 deletions, and each of those 9 was read and accounted for (three
focus-ring replacements from the earlier session, the `.tab-name` / `.tab-label` split, and
the `.tab .badge` merge). **Not verified in a browser** — the reporter's screen is the only
place the restore is really proven.

**Standing change to how I work in this repo:** copy a file to the scratchpad before any
scripted edit to it, keep the copy until the change is verified rather than until it is
merely green, and never compute an edit region from two independently located markers when a
single anchored `replace` will do.

## 2026-08-03 — the animation that was never an animation (web/)

**Reported.** After the stylesheet was restored, the "needs you" badge and the pane-number
bullet still did not animate. The CSS was confirmed present and correct-looking in the
shipped sheet, both `@media (prefers-reduced-motion: reduce)` blocks were properly scoped,
and the `attention` class was in the built bundle. Everything checked out, and nothing moved.

**The badge's defect is a spec fact, not a typo.** `background-image` has animation type
**DISCRETE**. No browser interpolates between two `linear-gradient()` values, so keyframing
the gradient does not sweep — it flips from one frame to the other at a single instant. And
because both of my keyframes deliberately placed the band OUTSIDE the 0-100% box (at
`-40%…0%` and `100%…140%`, which is what made the loop seamless without `alternate`), the
flip went from off-the-left to off-the-right and the band was never once drawn inside the
badge. Not a subtle animation: no animation, ever.

The fix is a REGISTERED custom property. `@property --attn-stop { syntax: "<percentage>" }`
makes the value interpolable, the gradient is declared once on the rule with all three stops
placed off that one value, and the keyframes move the property. An unregistered custom
property would have failed identically — it is a token, not a value, and animates
discretely — which is the trap one layer down from the first.

**Findings.**

- **"Is this property animatable?" is a question I never asked.** I reasoned carefully about
  the shape of the motion, the loop seam, the reduced-motion fallback, the theme tokens, and
  the line-box consequences — and not once about whether the browser would interpolate the
  thing I was animating. Every other consideration was downstream of an assumption that was
  never examined.
- **The test asserted the bug as a feature.** It pinned that the band is off-screen at both
  ends, with a comment explaining that this is what makes the loop seamless. That is true of
  the intent and it is exactly what made the discrete fallback invisible. The check was
  neutralized at the time — and both halves failed as designed, because the neutralizations
  I imagined were "the colours interpolate instead of the stops" and "the band stops on
  screen". Neutralization proves a test can fail; it cannot invent a failure mode you have
  not thought of. Here the unthought mode was "the property does not animate at all".
- **The guard that generalizes is worth more than the fix.** A new test walks every
  `@keyframes` block and fails on any property CSS can only flip (`background-image`,
  `display`, `content`, `font-family`). It catches the whole class rather than this
  instance, and it is neutralized by reinstating the exact rule that shipped.
- **Everything correct is not evidence that anything works.** The class was in the bundle,
  the rules were in the sheet, the media queries were scoped, the tokens were defined in both
  themes, 412 tests were green. Each check answered a question I chose; none of them answered
  "does it move". In a repo whose only renderer is jsdom, that question has no automated
  answer at all, and the honest response is to say so rather than to keep adding checks that
  cannot see it.

**Left unresolved, and stated as such.** The pane-number bullet animates `background-color`,
which IS interpolable, and its rule, specificity, and both theme tokens check out. I could
not reproduce its failure and did not change it. If it still does not move while the badge
now does, the one switch that disables both is the OS "reduce motion" setting, which this
sheet honours by design.

**Verification.** 413 tests (from 412). Three neutralizations on the rewritten badge test —
including reinstating the exact gradient-keyframe bug that shipped, which both the specific
test and the new general guard now catch — and the rebuilt sheet carries the registered
property, the static gradient and the property-moving keyframes. **Not verified in a
browser.**

## 2026-08-03 — "it doesn't animate" was two different bugs, and I could have measured both

**Reported, twice.** First that neither the badge nor the pane-number bullet animated; then,
after the badge was fixed, that the bullet still did not.

**The bullet's bug was not the one I kept looking for.** I had checked, statically: the class
ships, the rule ships, its specificity beats `.pane-number.current`, the keyframes are
unique, both `prefers-reduced-motion` blocks are correctly scoped, and every warn token
resolves in both themes. Everything was right, and I concluded the fault must be
environmental (the reporter's OS reduce-motion setting). It was not. **The bullet was
animating the whole time** — from `#b45309` to `#8a3f07`, a luminance swing of 1.77x in
light and **1.42x in dark**, on a 1.6em circle. Correct, running, and invisible.

**What broke the deadlock was rendering it.** An earlier session had left Playwright in the
scratchpad. Loading the dev-mock app, injecting the exact tab markup the workspace emits, and
sampling `getComputedStyle` every 45ms answered in one run what a week of reading the
cascade could not: the badge's `background-image` never changed (0 distinct values — the
discrete-property bug) and the bullet's `background-color` changed continuously across a
range too narrow to see. Widened to `#6b2f05` / `#b8860f`, the swing is 3.08x and 2.12x, and
the digit stays at 10.3:1 and 5.5:1 against its fill — computed before the edit, then
confirmed against the browser's own numbers afterwards.

**Findings.**

- **"Correct" and "works" are different claims, and only one of them was ever tested.** Every
  check I ran asked whether the CSS was what I intended. None asked whether the result was
  perceptible. A property can animate perfectly through a range nobody can distinguish, and
  that failure is indistinguishable — from the outside AND from the source — from the
  animation not running at all. The reporter's two messages described identical symptoms
  caused by opposite faults.
- **I proposed an environmental explanation instead of getting evidence.** Reduce-motion was
  a decent hypothesis: one switch, both symptoms. It was also unfalsifiable from where I was
  standing, and offering it put the diagnosis on the reporter. The tool to settle it had been
  sitting in the scratchpad since the previous session.
- **A headless browser is available in this repo's workflow and I should reach for it
  sooner.** Not for screenshots — for MEASUREMENT. Sampling a computed property over time
  turns "does this animate" from a question about the spec into a number. It also verified
  the `@property` fix (band centre travels -22% to 122%, crossing the box) and both themes in
  one run. DESIGN_SYSTEM.md treats Playwright as a throwaway install for screenshots; it is
  worth more than that.
- **Amplitude belongs in the token's comment, as a measurement.** `--warn-deep` now records
  the luminance swing and the resulting text contrast for both themes, and why it steps down
  rather than up. The first value was chosen by eye and was defensible on every axis except
  the one that mattered.
- **The period is now the same NUMBER in both rules, not the same duration by arithmetic.**
  The bullet was `1.2s alternate` — a 2.4s round trip, equal to the badge, but only after
  doubling it in your head. It is now `2.4s` with the return inside the keyframes, and the
  test asserts the two durations are EQUAL rather than pinning either literal, so retiming
  one and not the other is what fails.

**Verification.** 413 tests green, five neutralizations across the two animation tests
(including reinstating the exact gradient-keyframe bug that shipped). Measured in Chromium
in both colour schemes: badge band centre travels -22%..122% and crosses the visible box;
bullet luminance swings 3.08x (light) and 2.12x (dark). Screenshots in the scratchpad. This
is the first change in this sequence that is verified in a browser rather than reasoned about.

## 2026-08-03 — session wrap: one merge, four regressions, and where each was findable

**What was asked for, in order.** Unify Files and Terminal into one "Workspace" screen with a
single file-browser pane by default, plus an "open in a terminal" command; then a reported
blank status indicator; then four styling requests on that indicator (pulse it, make the
pulse gradient-stop motion, pulse the pane number too, invert the number); then "it doesn't
animate", twice. Everything asked for is in. Each piece is journalled above with its own
findings; this entry is only what the sequence says as a whole.

**The merge was the easy part, and that is the interesting fact about it.** Files.tsx and
Terminal.tsx were the same screen twice — same store, same ten pane commands on the same ten
binds, a JSX block that was character-identical — separated only by the pane payload. Because
`tiling/` was already generic in that payload, the merge changed nothing beneath the host: it
is one component over `FileData | TermData`, and every seam that needed to know the kind was
already a host-supplied callback. **A module that has genuinely refused to decide something
costs nothing the day two callers need it decided differently.** That property was invisible
until this change, and it is the whole reason a 1100-line consolidation took less work than
the four CSS tweaks that followed it.

**Four defects surfaced after the feature was "done", and each was findable by a check I was
not running.**

| Defect | Where it was findable |
| --- | --- |
| Badge clipped out of the tab | Layout. No test in this repo can see it |
| 600 lines of `styles.css` deleted | A backup before a scripted edit |
| Gradient keyframes never animated | "Is this property animatable?" — one question, never asked |
| Bullet pulse invisible | A measurement, not an inspection |

- **My verification was uniformly strong on "is the code what I meant" and absent on "does
  the result work".** Neutralization, false-pass analysis, adversarial reading of my own
  diffs — all of it aimed at intent. The four defects above are all in the other half. Three
  of them shipped with a green, neutralized test sitting next to them, and in one case the
  test asserted the bug AS A FEATURE (the band being off-screen at both ends is exactly what
  made the discrete flip invisible). **Neutralization proves a test can fail; it cannot
  invent the failure mode you did not think of.**
- **Twice I offered the reporter a hypothesis instead of evidence.** "Check your reduce-motion
  setting" was a decent theory and an unfalsifiable one from where I stood — it moved the
  diagnosis onto them. Playwright had been in the scratchpad since the previous session. The
  rule I am taking from this: when a report and my reading of the code disagree, the next
  move is an instrument, not an explanation.
- **The destructive edit was the one I did not think was risky.** I backed up religiously
  around neutralization passes — where I *knew* I was about to break something — and deleted
  each backup as soon as it went green. The edit that flattened the stylesheet was an
  ordinary-looking `s[:a] + new + s[b:]`. `str.replace` fails safe; a slice computed from two
  independently located markers always "succeeds", and its blast radius is the distance
  between them, which is not visible at the call site.
- **The recovery worked because a build artifact is a record of every rule — and of no
  comment.** `dist/*.css` gave back four rules that existed in no commit. It could not give
  back the prose the previous session had rewritten, which had to be spotted by reading and
  re-argued from scratch. Anything whose only home is a comment is unrecoverable from a
  build.
- **The reporter's second sentence was worth more than the first, twice.** "No badge" ->
  "a gap with nothing in it" ruled out state, reactivity and the poll in one observation.
  "Doesn't animate" -> "the bullet isn't pulsating either" is what forced the instrument that
  found two different bugs wearing one symptom. Both times the refinement converted a search
  into a lookup, and both times I had been searching in the wrong half.

**Left behind deliberately.** Four TODO entries from the merge (a duplicate-session race on
fast splits, the two pane-to-host protocols, `views/terminal/` holding three app-global
modules, six pre-existing stale translations). DESIGN_SYSTEM.md now documents the attention
badge — including why the travelling value must be a registered property — and gains a fourth
verification step, a **measurement pass**, since steps 1-3 could not have caught any of the
last three defects.

**Verification across the session.** 413 SPA tests across 17 files (from 400 at the start),
`tsc --noEmit` clean, `npm run build` clean with `pkg/webui/dist/.gitkeep` restored after
each, `gofmt -l` silent, `go build ./...`, `go vet ./...` and `go test ./...` ok, `docs:check`
punctuation/anchors/duplicate-targets clean with `cli/web.md` recorded fresh in both locales.
Roughly twenty neutralizations, each recorded with the diagnostic it produced. The animation
work is the only part measured in a real browser; the tab-bar height change from the
clipping fix is still reasoned, not re-measured. No commits.

## 2026-08-03 — Open becomes a command, and both gestures ask the same question

Two changes to the Workspace's file panes, one behavioural and one about discoverability.

**Opening a file now always asks where the pane goes.** `openInNewPane` prompted with the
wireframe targets for a KEYBOARD open (Enter on a row) and stacked a tab silently for a
MOUSE one. The old comment argued the click had already said where, by being on that pane.
It had not: a click on a file NAME says which FILE. The tile it landed on is the one already
spending its width on the listing you were reading, which is the last place with room to
show the file. Note the asymmetry in what being wrong costs — the question costs one
keystroke and its centre reproduces the old outcome exactly, while a silent stack that
guessed wrong costs a pane to undo. The `fromKeyboard` flag is gone from `FilePane`'s
`openFile` prop and from `activate`, so the row's three spellings (Enter, double-click,
click on the name) are one path with one outcome.

**`files:open` exists, and carries no key.** Open was the thing this screen does most often
and the only one the palette could not name, search or run — it existed solely as a gesture.
It is now a command, registered throughout a mount and disabled with the reason (`select a
file`, `select just one file`, `a folder, not a file`, `no editor or preview for this file`),
titled with the file it will open.

**The key it carries is none, and getting there took a wrong turn worth recording.** I first
gave it `direct: ["F3", "F4"]`, reasoning from this screen's own F5 / F6: those are the
orthodox file manager's Copy and Move, and in the same lineage F3 is View and F4 is Edit —
one command where the tradition has two, because the FILE decides whether you get the editor
or the viewer. The user's correction was one sentence: *we already have Enter assigned for
this.* That is the whole argument. The command duplicated a key the screen already had, and
`direct: "Enter"` could not have advertised the real one either — the direct lookup runs on a
document keydown against anything that is not a text field, so claiming plain Enter takes it
from every button, link and updir row at once. That constraint was already written down two
ways in this repo (`files:open-tab` is on `Ctrl+Enter`; a test pins plain `Enter` as
unclaimed) and I read past both. The command now has the shape of `files:save`: in the
palette, nowhere on the keyboard, because the thing holding the keyboard already hears it.

- **"Make it a command" is not "give it a key".** I heard a request for discoverability and
  answered with an accelerator, which is the part that was not asked for and the part that
  cost a browser key. A command earns its place by being nameable, searchable and runnable;
  a key is a separate claim with a separate justification, and "the tradition has one" is not
  that justification when the screen already does.
- **Precedent I could have consulted instead of reasoning from Norton Commander.** Both
  facts that decide this were in the files I was editing.

**A defect found and not fixed.** Verifying an unrelated report ("the prefix key bind doesn't
work in the editor" — it does; confirmed in Chromium across all four prefix presets, arming
and running binds with the document untouched) turned up a real one beside it: after the
prefix, an UNCLAIMED key falls through to the browser by design, and inside CodeMirror that
means it is typed into the file. `prefix d` in an editor pane inserts a `d`. The
pass-browser-shortcuts path is deliberate, so this is a decision rather than a slip; recorded
in TODO.md.

**Also worth stating.** `dispatchAppKey` skips the direct lookup for text-entry targets, so
`F5` is not swallowed when focus is inside CodeMirror — which is precisely where the draft
that the swallow exists to protect lives. Whether the browser then reloads I did not
establish: Playwright's synthetic `F5` does not invoke browser-chrome shortcuts, so my probe
cannot answer it. Recorded as a question, not a finding.

**Verification.** 418 SPA tests across 17 files (from 413), `tsc --noEmit` clean. Four
neutralizations, each with its diagnostic: dropping `direct` (`expected false to be true`),
restoring the silent stack (`expected null to be truthy`, on the dedicated test plus nine
collateral), a no-op `run` (`expected null to be truthy`), and adding a `direct` back
(`expected [ 'F3' ] to deeply equal []`). One fixture added — `backup.tar.gz` under the
`project` root, because every existing file opens into the editor or the viewer and the
"no editor or preview" row could otherwise only be tested against a filename no listing
contains; three tests that enumerate that listing were updated with it. `docs:check-punctuation`
clean, NFKC scan clean, `cli/web.md` recorded fresh in both locales, both glossaries extended.
No commits.

## 2026-08-03 — two Opens become one, and the trailing slash that made it legible

Follow-on from the entry above, and the cleanest kind of consolidation: the merge was not
designed, it was *noticed*, and only because the previous change had removed the one
difference that justified two commands.

`files:open-tab` ("Open `"x"` in a new tab", a folder, `Ctrl+Enter`) and `files:open`
("Open `"x.md"`…", a file, no key) were one sentence with two objects. What kept them apart
was that the folder one STACKED a tab and the file one asked where to put the pane — and that
asymmetry was itself the defect fixed an hour earlier. Once the file open asked for the mouse
as well as the keyboard, the two differed in nothing but their payload: a folder pane is a
listing of `path`, a file pane is `path` plus the `open` it is showing. `openTarget()` now
returns whichever the selected row calls for and one command covers both.

- **A consolidation can be BLOCKED by a defect, and invisible until the defect is fixed.**
  Nobody would have merged these while one asked and one did not — the difference looked like
  a difference in kind. It was a difference in consistency, and it had been sitting between
  them the whole time. Worth watching for: two commands whose only distinction is one being
  wrong.
- **The trailing slash is the merge's real cost, paid.** Two command names used to carry the
  kind of the row ("in a new tab" meant folder). One name cannot, so the TITLE has to:
  `Open "logs/"…` beside `Open "logs.txt"…`. Without it the merge would have been a small
  regression in legibility disguised as a simplification — the reader would be inferring the
  kind from the extension, which is exactly the guess the listing's own icon column exists to
  spare them. This came from the user, not from me; I had merged the commands and left both
  titles bare.
- **The key survived the merge and got better.** `Ctrl+Enter` was "Enter, but somewhere else"
  for a folder, and did nothing for a file. It is now "Enter, but elsewhere" for every row —
  and on a FILE it lands where plain Enter already does. That redundancy is not worth removing:
  one key meaning one thing on every row is what makes it learnable, and carving out the file
  case would trade that for nothing.
- **One caller must not ask, and saying why is part of the change.** `openTabAt` survives for
  the typed transfer destination, where the tab is a consequence of a completed copy rather
  than a pane the user asked to create. A creating gesture asks; a consequence does not.

**Verification.** 419 SPA tests (from 418), `tsc --noEmit` clean. One neutralization —
restoring `a folder, not a file` — failing three tests with three different diagnostics
(`expected null to be truthy` for the placement, `expected 'Open…' to be 'Open "subdir-10/"…'`
for the title, `expected 'a folder, not a file' to be undefined` for the reason), which is
the shape worth having: the merge is pinned by behaviour, by label and by availability
separately. Docs gate clean end to end (punctuation, build, 414 fragment links, duplicate
targets), `cli/web.md` re-synced in both locales. No commits.

---

## 2026-08-03 — The explorer "stops working" after a double-click: one gesture, three activations

**Report.** "Using the dev mock, navigating deep into `project/reports` sometimes causes the
explorer to malfunction in the way it fails to show new directory contents, and `..` stops
working too." Then, a minute later, the same symptom in `project/many-files` — which is what
killed my first three hypotheses, all of which were about DEPTH.

**Method.** Driven, not read. The user's own `dev:mock` was already up on :5173, so Playwright
attached to it and logged `click` / `dblclick` with `event.detail`, the target's row, and the
breadcrumb at dispatch time. Two things had to be got right before the trace meant anything:
`page.mouse.down()` defaults to `clickCount: 1`, so a hand-rolled "double-click" is two
unrelated single clicks that produce no `dblclick` at all and `detail=1` twice — the first
trace I took was therefore a trace of a gesture no user makes. With `clickCount` set properly,
one double-click on the folder name `project` at the virtual root reads:

```
click    detail=1  "project"      at ""                    -> enters project
click    detail=2  "many-files"   at "project"             -> enters project/many-files
dblclick detail=2  "many-files"   at "project/many-files"  -> project/many-files/many-files
```

**The defect.** A click on a row's NAME activates it — deliberate, documented, one of the
three spellings of open. So a double-click on a name is not one gesture reported twice: it is
a click that ACTS, and then a second click and a `dblclick` that arrive after the listing has
already been replaced under the pointer. All three acted. The third is the damaging one: its
`FsEntry` was captured by a row from the listing two navigations ago, and `childPath` joins
that stale name onto the path the pane has since moved to — so the pane lands on a directory
that does not exist. The 404 set `listing.error`, and `..` lived INSIDE
`<Show when={!listing.error}>`, so the whole table went with it. That is the entire reported
symptom, both halves, from one gesture.

**Findings.**

- **"Sometimes" was geometry, not timing.** Whether the misfire lands on a folder, a file, or
  empty space depends only on what the new listing happens to put at that pixel. `reports` is a
  chain of single-child folders, so the next folder is always at the same row — the gesture
  descends two or three levels per double-click and eventually falls off the end onto the leaf
  `.yaml`. `many-files` reaches a file in three gestures. Nothing was racing; I spent real time
  looking for a race (stale store proxies, `reconcile` replacing a pane, out-of-order
  `createResource` responses) because "sometimes" reads as one.
- **The obvious fixes are both wrong, and the second one convincingly so.** A per-event
  `detail > 1` guard leaves the `dblclick`, which also carries detail 2. Asking whether the
  `dblclick` landed on the name (`target.closest("a")`) passes the case you construct and
  misses the case that happens: when the new row's name is SHORTER than the clicked one, the
  second click lands on the `<td>` AROUND the link, `closest("a")` is null, and the row
  activates. I shipped that version, watched the trace still jump two levels on `many-files`,
  and replaced it. The invariant is one activation per PRESS SEQUENCE, which has to be tracked
  as a sequence — `beginGesture` / `claimGesture` on the pane.
- **`detail` 0 is not "the tail of a gesture", it is "no pointer".** Enter on a focused link,
  and every `fireEvent.click` in the suite, report 0. Treating `<= 1` as OPENING a sequence is
  what keeps keyboard activation working and what stopped the guard from silently disabling
  the second click of every test in the file. Missing this broke one existing test, which is
  how I learned it.
- **A second instance of the same defect, one layer out.** A click on a FILE name arms the
  placement pick; the second click of the double-click then landed on the scrim that same
  gesture had just raised, and cancelled the question it had asked — so a file could not be
  opened by double-clicking its name at all, and repeating the gesture just re-armed and
  re-cancelled forever. Same predicate fixes it: the scrim ignores a click continuing the
  sequence that armed the mode. Its existing guard is a `setTimeout(0)` deadline, which cannot
  see a click 100ms later; a deadline is not a reading of the gesture.
- **`resource()` RETHROWS.** Rendering the table unconditionally meant `entries()` ran in the
  error state, and Solid's resource getter re-throws so a boundary can catch it — which tore
  the pane down and surfaced as an unhandled rejection rather than as an error banner. Every
  read on the render path now goes through `current()`, which consults `listing.error` first.
  The old code never hit this only because the error branch skipped the whole subtree.
- **The error branch was also leaving `listEl` dangling.** With the list unmounted, the
  band-select's `document` mousedown handler measured against an undefined ref on every press.
  Rendering the frame unconditionally fixes the reported dead end and this together.

**Verification.** 422 SPA tests (from 419), `tsc --noEmit` and `vite build` clean. Three
neutralizations, each failing the intended test with the intended diagnostic: `claimGesture`
forced true fails both double-click tests; the error banner forced off fails the `..`-survives
test. The strongest one is end to end — a 90-second random walk of real double-clicks over the
mock tree: 342 gestures with the fix and zero error states; with the two guards removed, the
same walk reaches `shop-web/etc/nginx/nginx` — the doubled segment, exactly as diagnosed — in
52. Browser checks cover single and double click on a name, on blank row space and on `..`,
the full `reports` chain down and back, and that a double-clicked file name leaves the
placement question STANDING.

**Incidental.** The user's mock BFF on :5080 was found dead mid-investigation and restarted; I
could not reproduce a crash afterwards and did not chase it. `npm run build` writes into
`pkg/webui/dist/` and removed its tracked `.gitkeep`; restored. No commits.

## 2026-08-03 — "Open in a terminal" aims at where you are, not what you pointed at

**Request.** Make `Open in a terminal…` open a shell in the directory currently shown, not
the selected one.

**What it was.** `terminalHere()` in `web/src/views/Workspace.tsx` resolved its target as
`selectedDir() ?? asFiles(pane)?.data.path ?? ""` — the single selected directory if there
was one, otherwise the pane's own folder. That was written as "the precedent `files:open`
and `New pane…` already set", and copying a precedent is exactly how it got there. Now it
reads the pane's folder only.

**The distinction the precedent was hiding.** `Open…` and `New pane…` read the selection
because a row is their OBJECT — they act on the thing you picked out. A terminal has no
object. It is a place to stand, and the place you are standing is the listing in front of
you. Three commands looked like they were asking the same question ("which folder?") and
only two of them were.

Two symptoms follow from the mismatch, and both are visible without reading any code: the
command's title flickered between folder names as the arrow keys walked down a listing
(`Open "nginx" in a terminal…`, `Open "usr" in a terminal…`, …), and it offered a shell in
a folder the user had pointed at but not gone into.

**Finding: the published docs were right and the code was wrong.** `docs/cli/web.md` and
both translations already said "opens a shell **in the directory you are browsing**". They
had said it since the feature landed. Nobody wrote the selection behaviour down because
nobody would have described it that way — it was inherited from a neighbouring command,
not decided. Worth remembering as a smell: when the doc and the code disagree and the doc
is the more defensible of the two, the code is usually the copy-paste. I sharpened the
three pages rather than rewriting them, adding the contrast explicitly ("the folder on
screen, not a row you have selected inside it") because `Open` sits in the very next
paragraph and does read the selection.

**One reachability consequence, and it is a real one.** The `"<name> is not running"`
disabled reason used to be reachable by SELECTING a stopped workload's mount row at the
virtual root. It is not any more: at the root the shown folder is `""`, which answers
`"pick a workload first"`. The reason is still reachable — a restored layout can put a pane
inside a stopped workload, and a workload can stop while it is being browsed — and those
are precisely the moments it has to be right, so the test now seeds a pane at
`legacy-cron` instead of clicking its row. What was lost is a path that only existed
because the command was reading the wrong thing.

**Tests (423, from 422).** One new: "aims at the folder on screen, not the directory
selected in it". Both halves are asserted from ONE state — inside `shop-web/etc` with the
`nginx` subdirectory selected — because "used the shown folder" and "ignored the selection"
are the same claim only when the two disagree. The selection is witnessed by a command that
DOES read it (`cmdRow("files:open")?.title` is `Open "nginx/"…`), so the test cannot pass by
the click having silently failed. The outcome is read off the recorded create REQUEST
(`{workload: "shop-web", dir: "/etc"}`), not off the pane appearing: the mock has no working
directory to honour and the session list never echoes one back, so a pane proves nothing
about where its shell started.

*Neutralization:* restoring `selectedDir() ??` fails it behaviourally —
`Expected "Open "etc" in a terminal…" / Received "Open "nginx" in a terminal…"`.

**Also changed.** The comment on `selectedDir()` claimed three callers ("open it in a tab,
put a new pane on it, open a terminal in it"); two of those were already stale from the
earlier `files:open` consolidation and the third is now gone. `.agents/docs/DESIGN_SYSTEM.md`
gains a bullet under the pane-creation section stating the rule that was missing: what a
command aims at depends on whether it acts on a row.

**Gate.** 423 SPA tests, `tsc --noEmit` + `vite build` clean (`pkg/webui/dist/.gitkeep`
restored), docs punctuation 0, VitePress build clean, 414 fragment links 0 dead, 0 duplicate
targets, NFKC scan clean, `cli/web.md` translation state re-synced for both locales. No Go
files touched. No commits.

## 2026-08-03 — The placement question, answered once: `newPaneDisposition`

**Request.** A setting applied to every operation that creates a pane, choosing between
prompting for a disposition (the current behaviour), always opening side by side, and
always opening as a tab.

**Shape.** `Settings.newPaneDisposition: "ask" | "split" | "tab"`, default `"ask"`, with a
select in the Settings screen's Workspace group. The two non-asking values are not new
behaviours — each is one of the two answers the prompt already offers (the arrow and Space)
made standing, so no layout is reachable one way and not the other. That framing is what
made the feature small: it trades a keypress for a guess without narrowing anything.

**Applied at the chokepoint, not the call sites.** Every creating command routes through
`drag.beginPlace`, so the setting is read there and nowhere else. Three reasons, in order of
weight: a fourth creating command added later cannot forget to honour it; "does the setting
cover X?" has one answer instead of three; and the host keeps its comments about WHAT it
creates rather than acquiring a copy of the policy in each. `TileDragOps` gains one field,
`focused: () => string` — the tile a standing answer applies to is the one the wireframe
would have lit first. `tiling/panes.tsx` already imports `settings` directly, so drag.ts
reading it too is the directory's existing layering rather than a new one.

**Matched positively, so an unknown value asks.** `parseSettings` spreads stored JSON over
the defaults WITHOUT validating (pinned by an existing test from the 2026-08-02 removal), so
any string can reach this code. Written as `if (d === "ask") return; placeAt(..., d === "tab"
? "stack" : "right")` a junk value silently becomes a split — a workspace that stopped
asking because of a value nobody can see, which is the worst of the three outcomes. It is
now `if (d !== "tab" && d !== "split") return`, and there is a test that drives a bogus
disposition through and asserts the scrim comes up.

### The scope line, and why it is a decision

`Split pane…` (`beginSplit`) is NOT governed by the setting. Its title already names the
disposition it makes: under `"tab"` the setting would have to contradict the word Split, and
under `"split"` the command would collapse into `prefix %`, which already exists. What it
asks is WHICH EDGE — a finer question than the setting answers, and one whose standing
answers are the two directional split commands. The directional splits and `openTabAt` (the
typed transfer destination) were already unprompted and are unaffected.

This is stated in code, in DESIGN_SYSTEM.md, in the user docs, and pinned by a test, because
a boundary that only exists as an omission reads as an oversight the next time someone
touches it.

**The precedent that had to be answered.** A placement setting was deleted on 2026-08-02
("A setting that configures nothing is worse than a missing one"), and the Settings
Workspace group has already been removed once and rebuilt. The difference is that
`newPaneSide` answered half of a modal that no longer existed, whereas this one is read on
every route that creates a pane. Recorded in DESIGN_SYSTEM.md next to the new bullet so the
two are read together.

### Tests (430, from 423)

Every placement test asserts BOTH halves — the pane exists where the setting said, AND
nothing asked. Neither alone is the claim: "no overlay" is equally true of a command that
did nothing, and "a pane appeared" is equally true of one that asked and was answered.

- `"Always as a tab"` — two tabs on one tile.
- `"Always side by side"` — two tiles, and WHICH side: the listing in tile 0, the editor in
  tile 1. A pane landing left would be a different answer wearing the same name.
- `covers New pane and Open in a terminal, not just Open` — the "every operation" half. It
  would be a lie if the setting governed only the route someone happened to test.
- `asks when the stored disposition is not one it knows`.
- `leaves Split pane… asking which edge` — plus its centre still withheld (4 zones, no
  stack), which is the other half of why the setting cannot speak for it.
- Settings screen: the select's options are exactly `ask / split / tab`, it defaults to
  `ask`, and the assertion is on the PERSISTED blob — a select wired to the wrong setter
  renders and reads identically until reloaded.

*Neutralizations, all behavioural:*

| broke | failed with |
| --- | --- |
| setting ignored (always ask) | 3 tests; the scrim's own "↑↓←→ or hjkl place" in the diff |
| `"tab"` and `"split"` swapped | 3 tests, on tile count and tab placement |
| `"split"` landing left instead of right | the side assertion, naming the wrong tile |
| `beginSplit` honouring the setting | the boundary test — `expected null to be truthy` |
| default flipped to `"tab"` | **27** tests across 2 files — the default is load-bearing |

**Two test-fixture notes worth keeping.** An editor pane's `.tab-name` is
`"project  compose.yaml"` (crumb and file), not the bare filename. And a `.setting-row`
carrying a description has the whole paragraph as its label's accessible name, so
`findByLabelText` needs a regex — the prefix-combination row works with an exact string only
because it has no `.muted` line.

**Gate.** 430 SPA tests, `tsc --noEmit` clean, `npm run build` clean (`pkg/webui/dist/.gitkeep`
restored), docs punctuation 0, VitePress build clean, 414 fragment links 0 dead, 0 duplicate
targets, NFKC and full-width scans clean, `cli/web.md` translation state re-synced for both
locales. No Go files touched. No commits.

## 2026-08-03 — Two notions of "focused", and the route that hid the bug

**Reported.** (1) Opening an editor pane does not put the keyboard in the editor contents.
(2) With a terminal focused, `Select next pane` and `Select last active pane` leave the
keyboard in the terminal — but `Choose pane…` does not.

**One cause.** The workspace has two notions of focused: the pane wearing the frame, and the
element receiving keys. Every command moved the first faithfully; the second was left to each
pane to claim, and the rule was implemented four times and right once.

| pane | claim | outcome |
| --- | --- | --- |
| `Term` | reactive on `autoFocus` | correct |
| `FilePane` | reactive, behind a once-per-mount `landed` latch | claimed on the way IN, never on the way back |
| `FileEditorPane` / `Editor` | none | gap 1 |
| `ImageViewerPane` | none | same as gap 1, for pictures |

The failure mode is the bad one: the user looks at one pane and types into another, and when
the pane they left is a shell, the next keystroke is a command in a container.

**Finding: the working route was working by accident, and that is what hid it.** `Choose
pane…` looked fine because its PANEL takes DOM focus for the duration of the walk — so it
gets the keys off the terminal on its way past, whatever the destination pane does or fails
to do. Nothing about the claim is involved. `ws:next` and `ws:last` have no panel and both
land on the same `activate()`. Generalizable: when one of three routes to the same code works,
suspect the route, not the code. Written into DESIGN_SYSTEM.md next to the pane-focus rule.

### The rule, in one place

`web/src/views/tiling/focusclaim.ts` — `claimFocus(focused, holds, take)`. The module owns
the WHEN; each pane answers the other two, because what to focus inside a pane is the pane's
own business (a row, a document, an xterm, a `tabindex="-1"` tile). All four now go through
it, including `Term`, so a terminal reaching past a modal is no longer a thing one component
decides differently from the other three.

`holds` — "is the keyboard already somewhere in this pane?" — is what replaced the latch and
what makes the effect safe to re-run. The latch existed for a real reason (a refresh must not
yank the cursor back to the top) and bought it with a real cost (a pane could be claimed only
once). `holds` is the honest form of the same guard: true of the refresh, false of the
walk-back. Note it is answered about the WHOLE pane, not the element `take` focuses —
otherwise a toolbar button in the pane loses the keys to the next re-render.

### The bug the fix created, found by a test that already existed

Parking the cursor on the roving row broke `navigates rows with the arrow keys`. The claim
lands on row 0 with `keep=true` (arrive, do not select); the next bare ArrowDown resolves
`lead()` — still unset — back to row 0; `focusRow` calls `.focus()` on the ALREADY-focused
row, **which fires no focus event**, so `onFocus`'s `selectOnly` never ran and the key looked
dead. `focusRow` now collapses the selection itself when `keep` is false. The hidden
dependency was "focus() always fires an event", which was true only while nothing moved the
cursor without the user asking.

### Tests (435, from 430) — and one deleted

- caret lands in `.cm-content` when a file is opened (gap 1)
- `ws:next` and `ws:last` each take the keyboard out of a terminal — separately, since they
  were reported together and reach `activate` by different routes. The starting state is
  ASSERTED: if the terminal never had the keyboard, everything after is vacuously true.
- a re-entered listing returns to the row you were on, using two file panes so nothing
  depends on a terminal — this is the latch, not the shell
- an image viewer takes the keyboard, having nothing to type into

*Neutralizations, all behavioural:* editor claim removed -> the caret test; `holds` forced
true (the latch, restored) -> all three walk tests; viewer claim removed -> `expected
<textarea> to be <div class="image-viewer">`, which is the reported bug printing itself.

**A test was written and deleted.** "does not reach past the command palette" passed with the
guard REMOVED, so it certified nothing. Going looking for a reachable path found none: every
claim in the tree depends on data that arrives asynchronously, so by the time an effect
re-runs the mode that was up has closed. `modeOwnsKeyboard` is carried anyway — taking it out
would be a behaviour change made on the strength of not having found the path — and is
labelled unverified in the module, in the test file where the test used to be, and in
DESIGN_SYSTEM.md. It is not counted as covered.

**Gate.** 435 SPA tests, `tsc --noEmit` clean, `npm run build` clean (`pkg/webui/dist/.gitkeep`
restored), docs punctuation 0, VitePress build clean, 414 fragment links 0 dead, 0 duplicate
targets, NFKC and full-width scans clean. User-facing docs unchanged: this restores behaviour
they already describe. No Go files touched. No commits.

## 2026-08-03 — "Pane dragging doesn't work on touch devices": one DnD vocabulary, two transports

**Report.** Dragging did not work on a touch device. Asking which gesture split the answer
in two: dragging a TAB to re-tile, and dragging a file ROW into a folder. Dragging the
DIVIDER to resize already worked — it has been pointer-driven with `touch-action: none` since
the 2026-08-02 touch-parity change — and the user confirmed that one was fine.

Both broken gestures were HTML5 drag-and-drop. No mobile browser synthesises
`dragstart`/`dragover`/`drop` from a finger (iPadOS Safari is the sole exception), so neither
had ever been performable on a phone. For the tiling this was a known, documented gap with
the tap-to-pick mode as its answer; for Files it was `TODO.md`'s "the Files pane's own
drag-and-drop is still touch-dead", where moving a file between panes had a command route but
no gesture at all.

### The shape, at the user's direction

Two instructions steered this, and both were right:

1. *"Introduce an abstraction layer between consumers and native DnD or emulated one."* — so
   the fix is not two bespoke touch implementations. `web/src/dnd.ts` is a facade: a consumer
   declares a **drag source** and a **drop target** in terms of what the drag MEANS, and the
   module picks the transport. `"auto"` gives the mouse the real HTML5 drag (so OS file drops
   in and the Chromium drag-out download keep working) and a finger the emulated one;
   `"emulated"` is pointer events on every device, with native never registered.
2. *"Stop using HTML5 DnD for tab/pane rearranging."* — the tiling is `"emulated"` outright.
   A tile move is entirely in-page, so the one thing native offers that this cannot (a
   payload crossing the window boundary) is the one thing it never wants, and a single path
   means the mouse and the finger exercise the SAME code rather than a path per pointer type
   where only one is ever run.

### What the emulated path had to get right

It mirrors native semantics rather than inventing its own, because the consumers were written
against those semantics and had to keep reading the same way:

- **Targets nest, and refusal falls through.** `accepts` returning false hands the point to
  the enclosing target — exactly what a `dragover` that declines to `preventDefault` does.
  Files depends on it: a file row is not a target, so its point reaches the folder behind it.
- **`accepts` is separate from `over`, and must be pure.** This is not tidiness. The module
  calls the departing target's `leave` BEFORE the arriving target's `over`, which it can only
  do by knowing who the new one is before anything is drawn. Two sibling tiles write one
  shared "where would this land" signal; in the other order the tile being left wipes what
  the tile being entered has just set. (The native path has the same hazard, which is what
  its `relatedTarget` guard has always been for.)
- **The lift rule is the whole gesture.** A mouse lifts as soon as it has moved; a finger
  only after a 400 ms dwell, and a press that moves first is a SCROLL and is abandoned. This
  is why `touch-action: none` is wrong here and is not used: the tab bar pans horizontally
  and the file listing vertically, and taking that away is how a "fix" for the drag breaks
  the scroll. The veto arrives with the lift instead, as a non-passive `touchmove`
  `preventDefault` — which also suppresses the native drag on iPadOS, the one browser where
  both transports could otherwise fire at once (`dragging()` guards that a second way).

### Two bugs found while testing, both invisible to the tiling

- **`finish()` cleared `live` before delivering the drop**, so `pointOf` read the module slot
  and handed the drop an EMPTY payload. The tiling could never have caught it: its payload is
  empty by design (both ends are in one component tree). The Files test found it on its first
  run — `types= []` at the drop, `accepts=false`, no modal. `pointOf` now takes the press
  explicitly.
- **The click guard leaked across gestures.** A completed touch drag swallows the click the
  browser synthesises after it; the removal was a `setTimeout(…, 0)` that a stale timer could
  fire against a NEWER guard. It now retires only itself, and a new press retires any guard
  still standing. It is armed for touch only — a mouse's post-drag click lands either on the
  workspace (no handler) or on the source it already activated.

The leak was found by a symptom that looked nothing like its cause: five unrelated tests
started failing only when a drag test ran before them, because the leftover capture-phase
listener ate `fireEvent.click` on a split-edge button. A first probe cleared the wrong
suspect — the guard was `if (!globalThis.__NOSWALLOW)` and nothing ever set the flag, so the
"disabled" run had it fully enabled.

### Copy or move, with no Shift to hold

A mouse says which by holding Shift, so the destructive one is the one you ask for. A finger
has no modifier, and every alternative hides the choice where it cannot be seen (a second
long press, a drop zone per verb, a mode set before the drag and forgotten during it). An
emulated drop therefore ASKS — after it has landed somewhere legal, via the existing
`promptChoice` — and a dismissed question is a drop that never happened. The native path must
not take that route: it returns `receiveInto` synchronously, because the pane draws its ghost
rows before its first `await` and an `await` of any kind puts "the transfer was admitted
immediately" behind a microtask. Three ghost tests caught exactly that on the first attempt.

### Tests (444, from 435)

- **tiling, with a finger:** long-press → move → release rearranges; a release on an edge
  re-tiles; a press that MOVES before the dwell is a scroll and picks up nothing;
  `pointercancel` abandons; Escape cancels.
- **Files, with a finger:** long-press a row onto a folder row asks copy-or-move and the
  move lands; dismissing the question leaves both folders exactly as they were.
- the preview is taken away when the drag leaves every tile (the new `leave` in `drag.ts`)
- the coarse-pointer stylesheet keeps `-webkit-touch-callout` off the drag surfaces and
  `touch-action` off the two scrollers

*Neutralizations, all behavioural:* touch never lifts -> three tiling drags fail; a stray
move lifts instead of abandoning -> the scroll test fails; Escape ignored -> the cancel test
fails; touch given no emulated drag -> both Files tests fail; the dismissal guard removed ->
`expected <span class="fs-name"></span> to be null`.

**One test was rewritten because it was vacuous.** "reads a press that moves before the dwell
as a scroll" first released over the tab's OWN tile, where every drop is a no-op anyway — so
it passed with the guard removed. It now travels to the OTHER tile, where a lifted drag would
visibly promise a drop. Separately, "leaves a solo tile alone when its only tab is dropped on
its own edge" had become a test of nothing (its assertions hold when no drag happens at all);
it now asserts the drag really runs and that the tile lights nothing up.

**A fixture trap, corrected.** The mock filesystem is a module singleton that no `beforeEach`
rebuilds, so a test that MOVES a row moves it for every test after it in the file. The first
draft reused `file-050.go`, which left the existing shift-drop test moving a file that was
already gone — green, and checking nothing. The touch tests now use rows nothing else names.

**Gate.** 444 SPA tests, `tsc --noEmit` clean, `npm run build` clean
(`pkg/webui/dist/.gitkeep` restored). No Go files touched. No commits.

## 2026-08-03 — The split strips and the tab drag, pulled apart

**Report.** "The split-pane gesture seems to collide the tab rearrangement gesture", with two
instructions: (a) shrink the strips' hit areas so they coincide with the purple bars, on every
device; (b) give touch a gesture of its own — hold to make the bar glow, release and the glow
remains but fades slowly, re-touch while it is still hot to split.

### The collision was mine, and it was invisible in the diff

The edge strips arm on a 450ms dwell fed by `pointermove` on `.workspace`. The tab drag used
to be HTML5 drag-and-drop, **which emits no pointer events at all** — so a tab travelling
across the workspace could never feed that dwell, and nothing in the strips' code had to say
so. Making the drag a pointer drag (JOURNAL, earlier today) turned every drag into a stream of
`pointermove`s: rest a carried tab near an edge for 450ms and a strip arms UNDER the drag,
promising a split; release, and the click that ends the drag lands on the armed strip and
splits the tile the tab has just been dropped into.

The fix is one word in the guard the strips already had. `modal()` — "some takeover mode owns
this tile, stand down" — is now `busy()`, and `dragging()` counts. That required `dnd.ts`'s
`dragging()` to become a REACTIVE accessor rather than a plain read of a module slot; it had
been exported as speculative API since this morning and had no caller, which is exactly why
nobody noticed it could not be watched.

### (a) The strip is the bar

`.pane-split-bar` has always drawn the middle 70% of an edge, while the strip that armed it
ran the FULL edge. A third of the hotzone was therefore invisible, and the corners — where
two full-length strips met — were the worst of it: a press aimed at content in a tile's corner
split the tile. `EDGE_INSET = 0.15` is now one number that both `edgeAt()` arms on and the
strip is positioned by, set inline for the same reason the thickness always has been. The bar
then fills its strip instead of insetting itself again inside it.

It also does most of the work of separating the two gestures, geometrically: the left and
right strips used to run the full height of the tile, tab bar included, so they lay right
across the whole-stack drag handle. At 15% they clear it on any tile more than a few rows
tall. Only the TOP strip still overlaps the tabs, which is not fixable — a split divides the
whole tile, so the top edge is the tile's own top — and it is 10px of it.

### (b) Hold, glow, let go, touch again

Three states where there was one: `charging` (a finger is holding it, the glow ramping up over
`SPLIT_HOLD_MS`), `armed`, and `cooling` (released, fading over `SPLIT_HOT_MS` and live for
exactly as long as it is visibly lit — the brightness IS the deadline, so the window needs no
explaining). Both durations are carried INLINE from the constants that drive the timers, like
the geometry, so the ramp the eye sees and the countdown the press is racing are one number.

Two decisions worth recording:

- **`SPLIT_HOLD_MS` (350) must stay under `DRAG_LIFT_MS` (400), and a test pins it.** The top
  strip lies over a drag handle, so a single press counts down towards a split *and* towards
  picking the tab up; the shorter timer claims it, and the charge calls the new
  `cancelPendingDrag()` as it completes — which retires a press that has not lifted and
  refuses to touch one that has. Reversed, the drag would win every press and the top edge
  would have no touch route at all.
- **The touch path commits from the PRESS, and the strip stays click-through throughout.**
  The mouse's armed strip is a real button (`pointer-events: auto`); a touch-lit one is not,
  because it sits over the tab bar and a bar that swallowed presses it was not offered would
  be the collision again in the other direction. It also sidesteps a trap: arming on a press
  whose own release then synthesises a click means the bar lights up and spends itself in one
  continuous gesture, and the second touch the design is built around never happens.

The first touch deliberately claims nothing — a short tap on the band goes on meaning whatever
it meant before, and only a press that has held long enough to have SAID so takes itself away
from what is underneath.

### Tests (452, from 444)

- the hotzone is inside the bar: the inline insets, and a dwell out towards the end of an edge
  arms nothing while the middle of the same edge arms
- the dwell stands down while a tab is being dragged, and the drop re-tiles without splitting
- the touch gesture end to end: glow while held, glow retained and fading when let go, split
  on the second touch; a charge let go of too early is abandoned; an expired glow makes the
  same touch inert; a touch elsewhere puts a lit bar out
- a held press on the TOP edge is claimed away from the tab drag it overlaps
- `SPLIT_HOLD_MS < DRAG_LIFT_MS`, with the reason

*Neutralizations, all behavioural:* `busy()` without `dragging()` -> the drag/dwell test
fails; `alongEdge` forced true -> the hotzone test fails; the second-touch branch disabled ->
both splitting tests fail; `cancelPendingDrag()` removed -> the top-edge test fails on the tab
having been picked up.

**Two tests were corrected because they proved nothing.** The hotzone test first dwelled at
x=10 on a 200px tile — which is inside the LEFT strip's own 20px thickness, so "the top strip
did not arm" was true whatever the inset did; it now uses x=25, past that thickness and inside
the 15%, and additionally asserts no strip anywhere armed. The top-edge test first asserted on
`.pane-drop-indicator`, which jsdom cannot produce without `elementFromPoint`; it now asserts
on the tab's own `.dragging` class, which is driven straight off the drag's `source`.

**A harness trap, the same one twice.** `movePointer` built a `MouseEvent` with no
`pointerType`, and the dwell now dispatches on it — so every hover test took the touch branch
and armed nothing. This is the third property (`pointerId`, `clientX/Y`, now `pointerType`)
that jsdom silently reports as `undefined` and that a handler reads; the helper says so.

**Gate.** 452 SPA tests, `tsc --noEmit` clean, `npm run build` clean
(`pkg/webui/dist/.gitkeep` restored). No Go files touched. No commits.

## 2026-08-03 — The split bar's glow, held back and re-lightable

Two corrections to the touch split gesture landed earlier today: the fade was **too quick
before the critical point**, and a re-hold during the cooling window should **re-heat from
the brightness the bar visually sits at** rather than starting over.

### The curve

The fade was linear over `SPLIT_HOT_MS`, so a quarter of the way in the bar had already lost
a quarter of its brightness — it spent the entire window *looking* like it was about to
expire, which is exactly the wrong signal at the moment there is least reason for urgency. It
is now `cubic-bezier(0.5, 0, 0.75, 0)` (easeInQuart): ~94% brightness at the half-way point,
~68% at three quarters, and the visible death confined to the last quarter, which is the only
part worth reacting to.

### Heat, and why the fade had to stop being an animation

The gesture is now expressed in one quantity — **heat**, the fraction of the hot window still
to run — and the re-hold falls out of it: a charge from heat `h` takes `(1 - h) *
SPLIT_HOLD_MS`, and a release at heat `h` cools for `h * SPLIT_HOT_MS`. Topping up a bar that
has barely faded is nearly instant; rescuing one that is almost out costs what lighting it
did in the first place. A partial re-hold keeps what it gained and cools from there, so the
window it gets back is the one it earned.

**The fade is a transition rather than a `@keyframes` animation, and that is what makes
"re-heat from where it visually sits" true rather than approximated.** A transition
interpolates from the value the element is *currently showing*, so interrupting a half-faded
bar reverses it in place; an animation would restart from its own `from` keyframe and jump to
full before coming back down. The JS never has to compute an opacity — it computes heat (time)
and the browser does the rest. The two are a deliberately non-linear read-out of each other
and agree at both ends, which is the property that matters: a bar is spendable exactly while
it is visible.

### Commit moved from the press to the release

The second touch used to split on `pointerdown`. That makes a re-hold unreachable — the split
has already happened by the time the finger has been held. So a touch on a lit bar now starts
a re-heat immediately (the honest feedback: it brightens either way) and the release decides
which gesture it was: under `SPLIT_TAP_MS` it was a tap and spends the bar, over it the finger
was holding and the bar keeps what it gained.

The threshold is 200ms, chosen for its **asymmetric failure**: read a tap as a hold and the
bar re-heats, which the user simply taps again to fix; read a hold as a tap and a pane splits,
which they cannot. One consequence worth stating — a re-hold can never add only a little,
since it must outlast the tap threshold, so from heat above ~0.43 a re-hold either spends the
bar or fills it. That is a fine place for the seam to sit.

`byTouch` replaced `!cooling()` as the gate on the strip's `pointer-events`. The old test was
a proxy that happened to hold: between the charge completing and the finger lifting, a
touch-lit strip WAS briefly a live button, and only the ordering of `pointerup` before the
synthesised `click` kept the release from spending the bar it had just lit.

### Tests (455, from 452)

- a re-hold three quarters through the window asks only for the missing heat (`263ms`, not
  `350ms`), gets the whole window back when held to the top, and outlives the original
  deadline
- a re-hold let go of part-way keeps what it gained and cools from there — driven at a hold
  the test DERIVES from the constants (longer than a tap, shorter than the ramp) and asserts
  that band is non-empty, so a constant change that closes it fails loudly instead of quietly
  re-testing one of the ends
- the stylesheet holds the glow back and fades by transition, with no `@keyframes` left

*Neutralizations, all behavioural:* re-hold restarting from cold -> both re-heat tests fail on
the duration; every re-touch spending the bar -> the partial re-heat test fails; the fade
returned to linear -> the stylesheet test fails.

### The hole the question found, an hour later

Asked to state the conditions under which the re-tap actually splits, enumerating them turned
one up. The release read the tap off `charging()` being non-null — i.e. the split only landed
if the finger lifted BEFORE the re-heat that same press had started could finish. That ramp is
`(1 - heat) * SPLIT_HOLD_MS`: **1ms at full heat, 18ms a twentieth of the way down**. Every
real tap (50-120ms) outlived it, so the gesture re-heated instead of splitting — and it failed
hardest on a bright bar, which is exactly when the user taps most confidently.

The decision is now read off two things recorded AT THE PRESS — `pressLit` (was this bar
already spendable when the finger landed) and how long the finger stayed — and off nothing
that happens during the press.

**The test passed for the wrong reason, and that is why the bug survived.** It pressed and
released with no time advanced, so `held` was 0 and the fake clock never let the ramp timer
fire; the one green assertion covered the one case a human cannot perform. It now taps for a
real 80ms on a bar cooled 5% (ramp 18ms), asserts `ramp < tap < SPLIT_TAP_MS` so the two
bounds are visible in the test rather than assumed, and fails against the old rule.

**Gate.** 455 SPA tests, `tsc --noEmit` clean, `npm run build` clean
(`pkg/webui/dist/.gitkeep` restored). No Go files touched. No commits.

## 2026-08-03 — The split bar's window, twice too short

Follow-up to the two entries above: "it still cools too quickly", after the fade had already
been put on a held-back curve. The curve was not the remaining problem — the WINDOW was.
`SPLIT_HOT_MS` was 4s, so however the brightness was distributed, the whole second half of the
gesture was over in four seconds.

It is now **10s**, with the curve strengthened from `cubic-bezier(0.5, 0, 0.75, 0)` to
`cubic-bezier(0.7, 0, 0.84, 0)` (an exponential ease-in): 99% brightness at a fifth of the
way, 97% at the half, 88% at seven tenths, and the visible death confined to the last fifth.

The reasoning worth keeping: this half of the gesture is "look at where the split would go,
decide, touch it", and **a deadline that has to be beaten turns deciding into hurrying**. The
asymmetry is what settles the number — nothing bad happens at the far end of a long window,
because the bar is visible throughout, a touch anywhere else puts it out at once, and any
other mode disarms it. Being generous costs a purple bar somebody may glance at; being mean
costs the gesture.

**No test changed, which was the point of writing them against the constants.** Every timing
assertion is expressed as a fraction of `SPLIT_HOT_MS` / `SPLIT_HOLD_MS` rather than in
milliseconds, so a 2.5x change to the window moved nothing. The one test that pins a literal
duration (the 80ms tap) pins it against `SPLIT_TAP_MS` and the re-heat ramp, neither of which
this touches — the ramp is a fraction of `SPLIT_HOLD_MS`, so lengthening the hot window leaves
re-heating exactly as quick as it was.

**Gate.** 455 SPA tests, `tsc --noEmit` clean, `npm run build` clean. No Go files touched.
No commits.

## 2026-08-03 — "It cools as fast as it heats": slow means moving, not standing still

Third report on the same fade, and the first two fixes had both pushed the wrong way. The
sequence is the lesson:

| | curve | window | reported |
|---|---|---|---|
| 1 | linear | 4s | "too quick before the critical point" |
| 2 | `cubic-bezier(0.5, 0, 0.75, 0)` (quartic) | 4s | "still cools too quickly" |
| 3 | `cubic-bezier(0.7, 0, 0.84, 0)` (exponential) | 10s | "still looks as fast as it heats up" |
| 4 | `cubic-bezier(0.11, 0, 0.5, 0)` (quadratic) | 10s | — |

**Holding a fade back makes it read as FASTER, past a point.** At step 3 the bar sat at 99%
for the first four seconds of a ten-second window and 94% at six; nothing perceptibly moved
until the drop at the end, so the only motion the eye ever saw was that drop. A fade whose
every visible frame is crammed into the last moment looks quick however long it actually ran —
which is exactly the complaint, and it was caused by the previous fix rather than surviving it.

A quadratic ease-in decays continuously instead: ~0.91 at 3s, 0.76 at 5s, 0.49 at 7s, 0.21 at
9s. Gentle enough at the start not to claim the time is scarce, and *moving the whole way*,
which is what actually reads as slow.

**The prefers-reduced-motion override is gone**, and it may well have been the whole report on
a machine with the setting on: it made both stages instant steps, so the bar snapped to full
and later vanished — "as fast as it heats up", literally. These fades are not motion in the
sense that setting is about (nothing moves, scales or parallaxes); they are a slow opacity
change that IS the affordance, carrying how much of the window is left. Removing it does not
calm the interface down, it deletes the only thing saying the bar is on a deadline.

**The test was accepting any curve, which is how the flat one got in.** `/cubic-bezier/`
matched all three of the shapes above. It now parses the control points out of the stylesheet,
solves the bezier, and pins both ends of the complaint: opacity > 0.85 a quarter of the way
through (linear gives 0.75 and fails) and < 0.7 two thirds of the way through (the exponential
gives 0.90 and fails). Both neutralizations were run and both fail as intended — the two
shapes a user actually rejected are now the two shapes the suite rejects.

**Gate.** 455 SPA tests, `tsc --noEmit` clean, `npm run build` clean. No Go files touched.
No commits.

## 2026-08-03 — The split bar's fade, finally: one constant rate

Fourth and last word on this fade: **"regardless of reaching the critical point or not, make
the cooling timing consistent across the course of time."** Linear, at a constant rate — which
is both simpler than everything that preceded it and better, and the reason is worth keeping.

**Brightness is now heat, one to one.** Heat is the fraction of the hot window still to run;
with a linear fade the bar's opacity IS that number, with nothing between them. Half lit means
half the time left, at every moment and — this is the part the easings could not do — from
every starting point.

That last clause is the argument that settles it. The fade always runs `heat * SPLIT_HOT_MS`,
so a bar re-heated to a third gets a third of the window. Under ANY easing that means the same
curve compressed into a third of the time, so the same brightness drains at a different speed
depending on history: the bar stops being a readout and becomes a readout-plus-a-memory.
Linear is the only shape with no memory, and it is exactly what "consistent across the course
of time" asks for.

**What the three easings were each reaching for was the WINDOW, and shape was the wrong
instrument.** The original complaint was a 4s linear fade being too quick; the answer was 10s,
and once the window was right the curve had nothing left to fix. Ten seconds of linear dims by
a tenth per second, which is unhurried by construction. Two separate jobs, and only one of
them was ever the problem.

**The test asserts `linear` explicitly, not merely the absence of a curve.** The base rule's
shorthand supplies `ease`, so a strip declaring no timing function would be eased by
inheritance — a way for this to regress that says nothing in the diff. Neutralized against
three shapes (the quadratic that was there, `ease-in`, and the inherited `ease`); each fails.

For the record, the whole sequence: linear/4s -> quartic/4s -> exponential/10s ->
quadratic/10s -> **linear/10s**. The first move (holding the fade back) was a misreading of
"too quick" as being about the curve when it was about the duration, and every subsequent
curve change compounded it.

**Gate.** 455 SPA tests, `tsc --noEmit` clean, `npm run build` clean. No Go files touched.
No commits.

## 2026-08-03 — Heat that runs past the climax, and the state it deleted

*"Instead of defining a critical point, just let the thermal value shoot over the critical
value that corresponds to the visual climax."* It does make everything simpler, which is the
part worth recording — this removed a state rather than adding one.

**Before:** the charge STOPPED at 1. Reaching it cleared `charging` and set `armed`, so a
finger still pressing was in a third state that was neither — heat pinned at the ceiling, the
press doing nothing, and `heatNow()` needing an `armed() ? 1 : 0` branch to describe it.

**After:** heat is one unclamped accumulator. It rises at `1 / SPLIT_HOLD_MS` while held, falls
at `1 / SPLIT_HOT_MS` while released, and the critical value only decides what is SPENDABLE.
`charging` now means exactly "a finger is down and the heat is climbing" for the whole press,
and crossing 1 sets `armed` without ending anything. The `unfinished` flag went with it: "this
charge never finished" is just `armed() !== side`.

**The overshoot is real, not decorative.** The visual peaks at the critical value because a lit
bar cannot get brighter, so heat above it is banked and comes back on release as a
`transition-delay` — time at full strength in front of the fade. Total lifetime stays
`heat * SPLIT_HOT_MS` and the fade itself is still one window at the one constant rate, which
keeps yesterday's invariant intact: brightness is `min(1, heat)`, and the rate never depends on
where the fade started. A deliberate long press now means "and keep it around for a while",
which is the natural reading of holding something down and is exactly what the gesture lacked.

`SPLIT_HEAT_MAX = 2` caps it at one extra window, and it has to: heat rises ~29x faster than
it falls (`SPLIT_HOT_MS / SPLIT_HOLD_MS`), so a finger left resting would otherwise light a bar
for the rest of the afternoon.

### Tests (457, from 455)

- a hold of twice the ramp banks a full extra window: `transition-delay` of one window,
  `transition-duration` of one window, and the bar is still lit a whole window after release
  where a threshold-charged one would have gone out
- the bank is capped however long the finger stays
- the existing suite needed two edits, both of which are the model change showing through:
  `charging` is now still set after the threshold (asserted, with the reason), and every
  `SPLIT_HOLD_MS + 1` became exactly `SPLIT_HOLD_MS` — with an unclamped accumulator that
  stray millisecond is 0.3% of extra heat, which moved every derived duration.

*Neutralization:* heat clamped back to 1 -> both new tests fail on a zero delay.

**Gate.** 457 SPA tests, `tsc --noEmit` clean, `npm run build` clean. No Go files touched.
No commits.

## 2026-08-03 — "Open in a terminal" joins the tile menu

`ws:open-terminal` now carries `PANE_TAG`, so it appears in the palette seeded by a tile's ⋮.
It was the one pane command deliberately held out, and the reason was real: what it opens is
read from the FOCUSED pane, and an entry whose meaning drifts with the focus has no business
on a menu raised per tile.

What retires that objection is `openPaneCommands` — the ⋮ calls `props.ctx.setFocus(activeId())`
before opening the palette, precisely so "close focused tab" cannot hit the wrong tile. The
focused pane at that moment IS the tile under the finger, so the row does not drift; it names
that tile (`Open "etc" in a terminal…`). The distinction the comment in `paneCommands` now
draws: the ⋮ must be the same LIST everywhere, not the same WORDS.

Untagged, the only routes to a shell from the workspace were the prefix `t` and typing into
the palette — both keyboard, on the one screen where every other pane operation was given a
touch route.

### Tests (458, from 457)

- new: "offers a terminal on the tile whose ⋮ was pressed, not the focused one". Two tiles,
  tile 0 walked into `shop-web/etc`, focus put BACK on tile 1 (at the mount list) so the wrong
  answer is live and disabled with "pick a workload first"; the ⋮ on tile 0 then offers
  `Open "etc" in a terminal…`, and the row is followed through to the CREATE REQUEST
  (`{workload: "shop-web", dir: "/etc"}`) — a title naming /etc in front of a shell that
  started at / is exactly the failure a pane-dependent entry risks.
- two existing tests moved. The ⋮ list gained the row in registration order. The
  same-menu-on-every-kind test was keyed on TITLE, which no longer holds: this row's wording
  varies by design. Keyed by id -> bind now (same commands under the same keys is the
  invariant), with a note that the rendered wording is pinned by the ⋮ test rather than kept
  in a second copy here.

Two things cost a run each and are worth remembering: a freshly split file pane re-fetches, so
it shows "nothing to browse" for a tick (`findByText`, never `getByText`), and walking into a
folder focuses the row -> `focusin` -> the tile, so a test that needs the focus elsewhere must
put it back (`runCommand("ws:next")`).

**Gate.** 458 SPA tests, `tsc --noEmit` clean, `npm run build` clean. No Go files touched.
No commits.

### Addendum — three findings from the run

**No neutralization on this one, by the user's call.** The standing rule is that every test is
broken deliberately before it is trusted; here the user said it was not needed and that is
recorded rather than glossed. What the tests do rest on: the new one FAILED twice before it
passed, and both failures were the assertions doing their job (the ⋮ menu had no such row
until the tag went on, and the "wrong answer" premise was not actually established until the
focus was moved back), which is weaker evidence than a neutralization but is not nothing.

**A green suite does not mean the edit is still in the tree.** The `tags: [PANE_TAG]` line was
gone from `Workspace.tsx` after a clean 458-test run — reverted by a linter or a concurrent
editor between the run and the next read. `git diff` on the file, not the passing run, is what
caught it. In a tree where another agent may be working, re-grep the change itself before
declaring done; the test result is a statement about the tree at the time it ran.

**Prettier is not a gate here.** `npx prettier --check "src/**/*.{ts,tsx}"` warns on 63 of the
SPA's files, i.e. the repo has never conformed to stock prettier defaults. A warning on a file
you touched says nothing about your change, and running `--write` on it would produce a diff
of the whole codebase. Ignore it; the gate is `tsc --noEmit`, `vitest run`, `npm run build`.

## 2026-08-03 — A new-pane placement that answers per device

`newPaneDisposition` gains a fourth value, `auto`: **As a tab on touch devices, side by side
on others**. Not a fourth behaviour — it is the two standing answers already there, with the
device choosing between them, so it still cannot reach a layout the placement prompt could
not. The reason it earns a row: the right answer genuinely differs by machine and the same
person uses both. A phone or tablet has neither the width for two panes nor a comfortable way
to resize them; the desk its settings sync to has both.

### Where the device question is asked

New module `web/src/pointer.ts`, one function, `coarsePointer()` -> `matchMedia("(pointer:
coarse)")`. Three decisions in it, each with the alternative it rejects:

- **`pointer`, not `any-pointer`.** `any-pointer: coarse` is true of a laptop with a
  touchscreen, which has the width for two panes and a mouse to arrange them with. The device
  this setting is for is the one where a finger is the only pointer there is.
- **A media query, not `ev.pointerType`.** `dnd.ts` answers a related question from the event,
  and should: a drag knows which pointer started it. A standing SETTING has no event — the
  command may have come from a tap, the prefix key, or the palette — so only the device can
  be asked.
- **Read per call, never cached.** A tablet gains and loses a pointing device when its
  keyboard case is attached; a value sampled at load would leave the app acting as the wrong
  kind of device until a reload. There is nothing to cache — matchMedia is a lookup, run once
  per pane created.

Resolution happens inside `beginPlace` in `views/tiling/drag.ts`, before the positive
`tab`/`split` match, so the chokepoint stays a chokepoint and the rest of the function still
sees only the two answers. An unrecognised stored value still falls through to asking.

### Tests (460, from 458)

- `auto` tested as ONE claim from both sides in a single test — same setting, same gesture,
  only the pointer differs. Deliberate: either half alone proves nothing. A resolver stuck on
  "tab" passes the coarse half, and one that ignored the setting entirely passes the fine
  half, because side by side is also what a broken read falls back to.
- "splits on a laptop that merely has a touchscreen" pins the `pointer` / `any-pointer`
  decision, with the stub answering coarse for `any-pointer` and fine for `pointer`.
- The device is stubbed at `window.matchMedia`, restored with `onTestFinished` like the
  disposition itself — it is module state the whole file shares (a live terminal pane asks it
  about the device pixel ratio, which is why the stub exists in `test-setup.ts` at all).
- The Settings screen test's option list gains `auto` in last place.

**Gate.** 460 SPA tests, `tsc --noEmit` clean, `npm run build` clean. No Go files touched.
No commits.

### Addendum — findings from the run

**The app now asks "what kind of pointer" in three places, and they must not be collapsed.**
Before this change there were two: CSS `@media (pointer: coarse)` in `styles.css` (hit-target
sizing, `-webkit-touch-callout`), and `ev.pointerType` in `dnd.ts` (which transport a drag
takes). `pointer.ts` is the third and the first one in JS that asks about the DEVICE. Each is
right for its own question and wrong for the others: an event-driven check cannot answer for a
command run from the keyboard, a media query cannot tell which finger started a drag, and CSS
cannot decide a layout. If a future change wants to unify them, that is the reason not to.

**The matchMedia stub is now load-bearing for behaviour, not just for survival.** It exists in
`test-setup.ts` because xterm watches the device-pixel-ratio query and would throw in jsdom;
it answers "no match" to everything. That default now silently means every test not stubbing
it sees a FINE pointer, so `auto` reads as side by side. Benign today — but a future test of
touch behaviour written without overriding the stub would observe mouse behaviour and could be
written to match it, which is the "green for the wrong reason" shape the testing rule warns
about. Any assertion about `auto` must set the pointer explicitly, in both directions.

**No neutralization pass; the two-sided test is what stands in for it.** The coarse and fine
halves live in ONE test on purpose: each half is passed by a different degenerate resolver
(stuck-on-tab passes the first, ignores-the-setting passes the second), so only the pair
constrains the implementation. That is weaker than deliberately breaking the code and watching
the test fail, and it is recorded as such rather than claimed as equivalent.

**Concurrent work in the tree.** `cmd/cornus/internal/webbff/{term.go,osctitle*.go}`,
`web/src/api.ts` and `web/src/views/Overview.tsx` are somebody else's uncommitted changes and
were not touched. Related note from the previous entry, which recurred here: re-grep your own
edit before declaring done — a passing suite is a statement about the tree at the moment it
ran, not about the tree now.

## 2026-08-03 — Terminal tabs name themselves after the foreground program (OSC title sniffing)

**The ask.** A terminal tab read `shop-web  /bin/bash` for the session's whole life. `cmd` is
the argv resolved once at `termManager.Create`, so it answers "which shell did we start",
never "what is in this tab now" — which is the only question a tab bar is actually asked.

**Two mechanisms were on the table; the stream one won.**

* *OSC window title.* Parse `ESC ] 0/2 ; text BEL|ST` out of the session's own output, exactly
  as a terminal emulator does. Uniform across all five deploy backends, because it never
  leaves the byte stream: no extra exec, no syscall, no per-poll cost.
* *Real foreground process group.* Accurate regardless of what the shell emits, but it lands
  unevenly. Only barehost/runc holds the pty master inside the cornus process
  (`pkg/deploy/barehost/exec_linux.go`, `execSession.console`) where `TIOCGPGRP` works;
  docker/containerd expose `ExecState.Pid` so `/proc/<pid>/stat` field 8 (`tpgid`) is
  reachable via `execCapture`, at the cost of an exec per poll; kubernetes and incus report
  `Pid: 0` and would get nothing at all.

Chosen: OSC only. Uniform beats accurate-on-three-of-five for a label, and the fallback to
`cmd` is already the honest answer for a shell that announces nothing.

**Shape.** New `cmd/cornus/internal/webbff/osctitle.go`: a `titleScanner` state machine tapped
into `termSession.readLoop` beside the existing detector feed, committing to `termSession.title`
and surfacing as `termInfo.Title`. Frontend reads it via a `titleOf(sessionId)` helper on the
already-running 2s session poll in `Workspace.tsx` — tab label, close confirmation, and a new
Title column in the Overview sessions table, each falling back to `cmd`.

**Four things that are load-bearing and non-obvious.**

1. **The scanner must be a state machine, not a per-chunk search.** Chunk boundaries fall
   wherever the kernel put them, so a title routinely straddles two reads and any scanner that
   restarts per chunk drops exactly those. `TestTitleScannerAcrossChunkBoundaries` feeds the
   stream one byte per call and asserts the whole-chunk result is identical.
2. **Ps filtering is the whole correctness story.** OSC 8 (hyperlinks) and OSC 133 (prompt
   marks) ride the same stream on any modern shell. Accepting every Ps turns a hyperlink into
   a tab name — the neutralization for this break produced `title=";https://example.com"`.
   Only Ps 0 and 2 are window titles.
3. **`ok=true` with an empty string is distinct from `ok=false`.** The former is a program
   CLEARING the title and must fall back to `cmd`; the latter is "no title in this chunk,
   leave the current one alone". `titleOf` uses `|| undefined`, not `?? undefined`, for
   exactly this — and that one character is separately neutralized.
4. **Title is reported for a DEAD session; State is not.** State is a claim about what a
   session is doing now, which a frozen screen cannot support. A title is a name, and the last
   name a session went by stays the truthful answer to "which tab was this?" through the 30s
   linger window. Reverting to the argv at death would rename the tab at the exact moment the
   user is hunting for it.

**Close-confirm wording changed, deliberately.** A prompt hook sets titles like
`root@shop-web: /app`, which reads as nonsense in "X is still running". The dialog now quotes
the name instead of using it as the sentence's subject: `"<name>" on <workload> is still
running.` That reads correctly for a program name, a prompt-hook title, and the argv fallback
alike.

**Neutralization: 8 deliberate breaks, all caught with the intended diagnostic.** Go — forget
state between chunks, accept every Ps, scan-but-never-store in readLoop, drop the ST
terminator, pass control characters through. Web — tab label ignores the title, `||` weakened
to `??`, Overview drops the Title cell. Two earlier attempts (breaks 3 and 4 on the first
pass) produced a COMPILE error rather than a test failure and were redone as behavioural
breaks, per the rule that a compile error neutralizes nothing.

**The mock sniffs its own stream.** `web/mock/faketerm.ts` gained `titleSeq`/`shellTitle` and
scans in `emit()` rather than setting the title at each call site, so it learns what it is
running the same way the BFF does — from the bytes. Its shell emits a title from the prompt
hook and on entering a command, so demo tabs rename themselves as they would live. Verified by
running the mock on `CORNUS_WEB_MOCK_PORT=5099` and reading `title` back off
`GET /.cornus/web/terminals`.

**Two environment traps hit here, both worth remembering.**

* A mock server was ALREADY listening on 5080 from another session. The first smoke test
  silently exercised pre-change code and reported a missing `title` field as if it were a bug
  in the new code. Check `ss -lptn 'sport = :5080'` before trusting a mock probe, and use
  `CORNUS_WEB_MOCK_PORT` rather than killing a port you did not open.
* `tsc --noEmit` does NOT cover `web/mock/` — tsconfig's `include` is `["src"]` alone. The
  mock is only type-stripped by Node at run time, so a type error there surfaces as a runtime
  failure or not at all. Smoke-running the mock is the only check it gets.

**Concurrent work in the tree.** `web/src/{pointer.ts,settings.ts}`,
`web/src/views/Settings.tsx` and `web/src/views/tiling/drag.ts` are another agent's
uncommitted changes and were not touched. Heeding the previous entry's own advice, every edit
was re-grepped after the suite ran; all survived.

**Gate.** `gofmt -l` clean, `go build ./...`, `go vet ./...`, `go test ./...` all pass;
`tsc --noEmit` clean and `vitest run` 463 passed (460 before, +3 new).

## 2026-08-03 — Sniffing the session's working directory (OSC 7), and what it feeds

**The ask, and the honest reach.** Follow-on from the title work above: sniff the foreground
process's CWD too. The direct analogue is OSC 7 (`ESC ] 7 ; file://<host>/<path> ST`), a ~20
line extension to the scanner already tapping the stream. Chosen consumers: a split inherits
the live directory, and a file pane can FOLLOW a terminal. Tab display was explicitly not in
scope.

**`osctitle.go` became `osc.go`; `titleScanner` became `oscScanner`.** `scan` now returns an
`oscUpdate{title,hasTitle,cwd,hasCwd}` instead of `(string, bool)`. The two facts are reported
INDEPENDENTLY, which a prompt hook forces: it emits the title and the directory back to back,
and a single "last value" slot lets whichever lands second erase the first. Neutralizing that
exact break (`u.cwd, u.hasCwd = "", true` next to the title commit) failed three tests
including the byte-at-a-time one, which is the evidence the independence is real.

**Title sanitizes, cwd REJECTS — a deliberate asymmetry.** A mangled title costs a confusing
tab. A mangled path could send a file pane somewhere real that the user never asked for, so
anything unusable (relative, bad %-escape, NUL/newline, invalid UTF-8, over PATH_MAX) commits
NOTHING and leaves the last known-good directory standing. There is likewise no "clear the
cwd" form, where an empty title is a genuine instruction.

**A neutralization pass deleted code rather than confirming it.** Breaking the leading-"/"
guard in `parseCwdURI` failed NOTHING: the switch above already rejects everything not
starting with "/", and in the `file://` branch the path begins AT that slash, so the guard was
unreachable. Removed rather than left in — an unreachable guard reads to the next person as a
tested invariant. This is the rule from CLAUDE.md working in the direction people forget: the
question "what would a PASS look like if this were false?" can have the answer "there is no
such case", and then the CODE is what is wrong.

**Two design calls worth keeping.**

1. **Hand navigation ends a follow** (`navigateTo` clears `follow` unless the follow effect
   itself is the caller, via `keepFollow`). A pane that both follows and steers would fight
   whoever is steering, yanking them back on the next poll.
2. **The two new commands are UNTAGGED**, unlike `ws:open-terminal` which they otherwise
   mirror. That row earns the ⋮ because it is listed-always-and-disabled and its reasons are
   actionable (browse into a workload, start it). `ws:follow-terminal`'s dominant reason is
   "this shell does not report its directory", which nobody can act on — so on the touch
   surface it would be a permanently dead row. Revisit if OSC 7 emission ever becomes common.

**A bug I introduced and the idiom that caused it.** `asFiles(findPane(...) ?? ({} as Pane))`
is used in two existing places and survives only because those callers cannot miss. Copied
into the unfollow command, where `focused` can name a pane that just closed, it dereferenced
`.kind` off undefined and took the whole command palette down — surfacing as three unrelated
touch-chrome tests failing. Guarded explicitly instead. The idiom is a trap; do not spread it.

**PID namespaces are why the /proc fallback was scoped out, and how the user unblocked it.**
Every existing `ExecState.Pid` consumer (`barehost/copy_linux.go:47`,
`containerdhost/copy_linux.go:43`, `barehost/stats_linux.go:129`) reads it from the HOST's
/proc, because it is a host pid. So the plan of running an exec INSIDE the container and
reading `/proc/<ExecState.Pid>/...` is unsound: in a container with its own PID namespace that
number is a different process or none, and reading the wrong process's cwd is worse than
reading none.

The user's fix: capture the pid from inside, by wrapping the launch —
`sh -c 'printf "<private OSC>" $$; exec <original argv>'`. Because `exec` replaces the wrapper
in place, `$$` captured before it IS the final shell's pid, and it is expressed in the
CONTAINER's namespace. Verified locally: the wrapper reported 938766 and `ps -p 938766` showed
the exec'd `sleep`. That makes `/proc` readable by an auxiliary exec on EVERY backend,
kubernetes and incus included, with no dependence on `ExecState.Pid` at all. Not implemented
— see TODO for the design and its caveats.

**Gate.** Go: `gofmt -l` clean, build, vet, `go test ./...` pass. Web: `tsc --noEmit` clean,
`vitest run` 468 passed (463 before, +5). Neutralization: 6 deliberate breaks (3 Go, 3 web),
all caught with the intended diagnostic, plus the one that correctly caught nothing and
deleted dead code.

**Concurrent work in the tree.** `web/src/{pointer.ts,settings.ts}`,
`web/src/views/Settings.tsx` and `web/src/views/tiling/drag.ts` remain another agent's
uncommitted changes and were not touched.

## 2026-08-03 — Announcing the session pid from inside, and reading /proc with it

**User's design, and it is the one that generalises.** Previous entry scoped the /proc
fallback down to three of five backends because `api.ExecState.Pid` is a HOST pid. The fix is
to never use it: wrap the launch as `sh -c 'printf "<private OSC>" $$; exec <original argv>'`.
`exec` replaces the process in place, so `$$` captured beforehand is the FINAL shell's pid,
numbered in the CONTAINER's namespace — which is exactly what an auxiliary exec into that
container can resolve. Works on kubernetes and incus, where `ExecState.Pid` is 0.

**Shell knowledge moved deeper, per the user's steer.** New `pkg/shells` holds `IsShell`,
`Split`, `FromArgv` (moved verbatim out of `cmd/cornus/internal/webbff/shells.go`) plus
`WrapAnnouncePID`/`ParsePID`. It is in pkg/ because both ends need it and must agree: the BFF
asks which shells to probe and what counts as an agent, and the SERVER asks whether an exec is
safe to wrap. The wrap lives in `pkg/server/deploy_exec.go` beside the existing
`SSH_AUTH_SOCK` injection — the same control-plane seam — behind a new opt-in
`api.ExecConfig.AnnouncePID`.

**Four properties that make wrapping safe to do by default.**

1. **The interpreter is `argv[0]` itself**, never a guessed `/bin/sh`. We were about to execute
   argv[0], so it demonstrably exists; wrapping through `/bin/sh` would break a distroless
   image that ships bash and no sh, which works today.
2. **Refusal is silent.** Only POSIX shells are wrapped; fish is excluded because it has no
   `$$` (it is `$fish_pid`), and anything else launches exactly as before. A session that
   cannot announce a pid is a session with no pid, not a failure.
3. **A failing printf costs nothing** — `exec` runs regardless.
4. **`termInfo.Cmd` still reports the ORIGINAL argv.** The wrapper is a spelling, not the
   command; leaking it would put `sh -c printf …` in the UI's Command column and close dialog.

**The env-injection question, and why the OSC-7 hook idea was dropped.** The user pointed out
that arbitrary env injection exists alongside SSH_AUTH_SOCK, which kills the earlier plan of
injecting `PROMPT_COMMAND` to emit OSC 7: it would fight a user's own injected
`PROMPT_COMMAND`, in both directions. The pid+/proc path supersedes it entirely — no shell hook
is needed to learn the cwd. The wrap is env-TRANSPARENT (it rewrites Cmd only, and the wrapper
inherits the environment), so the two compose. Current state of that feature, for the record:
`cornus compose exec` has `--env` (`parseExecEnv` -> `ExecConfig.Env`); plain `cornus exec` has
none; the web terminal API has no env field; client contexts have `shells` but no `env`.

**Probing is lazy and driven by the LIST request, not a ticker.** `termManager.List` calls
`maybeProbe`, which never blocks and starts at most one exec per session per `procProbeTTL`.
No browser polling means no probes; a session OSC already answers for is never probed; three
consecutive empty probes retire probing for that session so a container with no readable /proc
does not pay forever. OSC always wins over the probe in `info()` — it is current where the
probe is at best TTL-stale.

**The test that mattered, and the two rounds it took to make it mean anything.** The probe
script is the riskiest part, so it is run against a REAL /proc via a pty. The first version
started a plain `sh` and asserted cwd/comm — and neutralization showed it caught only ONE of
four breaks. It could not distinguish "read the foreground process" from "read the shell"
(they were the same process), nor first-paren from last-paren splitting of
`/proc/<pid>/stat` (comm "sh" has no paren). Rebuilt so that each contrivance kills a specific
bug: the shell runs `-i` on a pty so job control gives the foreground job its own pgid; the
foreground program sits in a different directory (/usr vs the shell's /tmp); the SHELL'S OWN
binary is copied to a name containing ")" (the paren must be in the stat line being PARSED,
not the child's); and the child's name also contains one. All four breaks then failed.

**E2E: `e2e/scenarios/web-terminal-introspect.star`** (`make e2e-web-terminal`, opt-in, added
to `EXTRA_CHECK_SCENARIOS`). It is the only test where the parts meet — server wraps, a real
busybox `sh` announces, the BFF scans a live TTY stream, a second exec reads /proc in that
container. The session runs `sh -c 'exec sleep 300'` so the foreground program is deliberately
NOT its shell: "sleep" cannot be produced by echoing back the command's basename. A second
session `cd`s away from its requested `dir` before exec'ing, so reporting `/etc` proves the cwd
is read from the live process rather than repeated back from the request. **Ran green against a
real docker backend**, and neutralized by disabling the server wrap (fails with the intended
diagnostic). Worth running on kube specifically — that is where `ExecState.Pid` is 0 and the
announced pid is the only anchor.

**Gate.** `gofmt -l` clean, `go build`, `go vet`, `go test ./...` all pass; `tsc --noEmit`
clean, `vitest run` 468 passed; `make e2e-check` parses every scenario; `make e2e-web-terminal
TARGET=docker` passes. Neutralization this round: 4 on the probe script (all caught after the
rebuild), 1 on the E2E scenario, plus the pkg/shells wrapper test which EXECUTES the wrap and
compares the announced pid against the exec'd process's own `$$`.

**Concurrent work in the tree.** `web/src/{pointer.ts,settings.ts}`,
`web/src/views/Settings.tsx` and `web/src/views/tiling/drag.ts` remain another agent's
uncommitted changes and were not touched.

## 2026-08-03 — Running the terminal-introspection E2E on kube, and what it caught

**It caught a scenario bug that only kube could expose.** `make e2e-web-terminal TARGET=kube`
failed with `"title":"sleep"` present and `"cwd":"/"` where `/usr` was expected. The title
being right is the important half: it proves the launch wrapper, the private OSC pid
announcement and the /proc probe all work on kubernetes, which is the backend where
`api.ExecState.Pid` is 0 and the announced pid is the ONLY anchor. The cwd was wrong because
the scenario passed `dir: "/usr"`, and **kubernetes' pods/exec subresource has no
working-directory field** — the backend warns and ignores it (already documented on
`createTermRequest.Dir`; the run logs `backend cannot honor exec option ... option=WorkingDir`).
So the session started at the image default `/`, and the probe reported `/` correctly.

Fixed by reaching the directory with `cd` INSIDE the command rather than via `dir`: `cd` is
part of the command, so every backend honours it. The second session now also CONTRADICTS its
`dir` (dir=/usr, command `cd /etc`), which is strictly stronger than the original — it fails
any implementation that echoes the requested directory back instead of reading the process.
Both targets now pass; docker re-run after the change to confirm no regression.

**Lesson worth keeping: a green docker run said nothing about `dir`.** The docker backend
honours WorkingDir, so the original scenario passed there for a reason that does not
generalise. This is the "passes for the wrong reason" shape from CLAUDE.md, in a scenario
rather than a unit test — and the only thing that surfaced it was running the same scenario on
a second backend.

**A destructive footgun in the E2E targets, found before it fired.** `KubeTarget.Teardown`
runs `kind delete cluster` unconditionally unless `--keep`, while `Setup` REUSES a cluster it
finds. So running the kube target against a cluster somebody else prepared destroys it. Only
`make e2e-kube` threads a `KEEP` variable through; the parameterized targets do not.
`e2e-web-terminal` now does (`$(if $(KEEP),--keep,)`), and the kube runs above used `KEEP=1` —
the user's 22h-old `cornus-e2e` cluster survived both. **`e2e-one`, `e2e-web` and `e2e-web-fs`
still lack it** and would delete a pre-existing cluster; recorded in TODO rather than changed
here, since they were not part of this work.

**Host tooling note.** `kind` is not installed on this host (only `kubectl`, via mise), even
though the cluster exists — preflight fails on it. Worked around WITHOUT touching the global
toolchain by fetching the binary to `./.agents-workspace/tmp/bin/kind` and putting that on PATH
for the run. Note the host is **aarch64**: the amd64 build runs under emulation and appears to
work, which would silently make every kube run slower; the arm64 build is the right one.

**Gate.** Go gate clean; `tsc --noEmit` clean; vitest 468 passed; `make e2e-check` parses every
scenario; `make e2e-web-terminal` passes on **both** TARGET=docker and TARGET=kube.

## 2026-08-03 — Session introspection: work summary and consolidated findings

Summary of one session's work, spanning the four entries immediately above (OSC title
sniffing; OSC 7 cwd and its consumers; announcing the pid from inside and reading /proc;
running the E2E on kube). The detail and the reasoning live there — this is the shape of what
shipped and the findings worth carrying past this feature.

### What shipped

A web terminal session can now say what is RUNNING in it and WHERE, neither of which the
launch argv can answer. Three layers, cheapest first, each falling back to the next:

1. **OSC on the session's own stream** (`cmd/cornus/internal/webbff/osc.go`). A chunk-boundary-
   safe state machine tapped into `termSession.readLoop`, reading `ESC ] 0/2` (window title),
   `ESC ] 7` (working directory) and a private `ESC ] 5379` (the session's pid). Free: no exec,
   no syscall, identical on all five deploy backends because it never leaves the stream.
2. **A /proc probe** (`procprobe.go`) for the sessions whose shells say nothing, which is most
   of them. Reads `/proc/<tpgid>/{cwd,comm}` with one lazy exec, driven by the session-LIST
   request rather than a ticker, so no browser polling means no probes; a session OSC already
   answers for is never probed; three empty probes retire it.
3. **The launch wrapper** (`pkg/shells.WrapAnnouncePID`, applied server-side in
   `pkg/server/deploy_exec.go` behind `api.ExecConfig.AnnouncePID`) that makes layer 2 possible
   at all, by announcing the pid as the CONTAINER numbers it.

Consumers: the tab names itself after the foreground program, a split inherits the live
directory rather than the one the source shell was told to start in, a file pane can FOLLOW a
terminal's cwd, and the Overview table carries Title beside Command. `pkg/shells` is new,
holding the shell-identification helpers moved out of the BFF so the server and the BFF cannot
disagree about what a shell is.

### Findings worth carrying forward

**`api.ExecState.Pid` is a HOST pid, and is 0 on kubernetes and incus.** Every existing reader
treats it that way (`barehost/copy_linux.go:47`, `containerdhost/copy_linux.go:43`,
`barehost/stats_linux.go:129`, all reading the host's /proc). Anyone reaching for it to read
/proc from INSIDE a container gets a different process or none. The general fix is to have the
process report its own pid: `sh -c 'printf … $$; exec <argv>'` — `exec` replaces in place, so
`$$` captured beforehand is the final process's pid in its own namespace. Backend-independent,
and the reason the kubernetes path works.

**Three neutralization outcomes, all instructive.** (a) A test that caught only ONE of four
breaks: the real-/proc probe test could not tell "read the foreground process" from "read the
shell" because they were the same process, nor first-paren from last-paren `stat` splitting
because `comm` was "sh". Rebuilt so each contrivance kills a specific bug — job control via
`sh -i` on a pty, foreground program in a different directory than the shell, and the SHELL'S
OWN binary copied to a name containing ")". (b) A break that caught NOTHING and was correct to:
the leading-"/" guard in `parseCwdURI` was unreachable, and the right response was deleting it,
not weakening the test. (c) Two first-pass "breaks" that produced COMPILE errors and had to be
redone as behavioural ones.

**A green run on one backend can be green for a non-generalizing reason.** The E2E scenario
passed on docker while passing `dir: "/usr"`, because docker honours `WorkingDir` — kubernetes'
pods/exec subresource has no such field and ignores it. Running the same scenario on a second
backend is what surfaced it. Scenarios should reach a directory with `cd` inside the command,
which is portable, and the fixed version now contradicts its own `dir` so echoing the request
back cannot pass.

**The kube E2E target deletes a cluster it did not create.** `KubeTarget.Teardown` runs
`kind delete cluster` unless `--keep`, while `Setup` reuses a cluster it finds. Only
`make e2e-kube` threads `KEEP` through; `e2e-one`, `e2e-web` and `e2e-web-fs` still do not.

**`asFiles(findPane(...) ?? ({} as Pane))` is a trap.** It survives in two existing call sites
only because those callers cannot miss. Copied into a command whose focused pane can have just
closed, it dereferenced `.kind` off undefined and took the whole command palette down — which
surfaced as three unrelated touch-chrome tests failing.

**Two environment traps.** A mock server left listening on :5080 by another session made a
smoke test silently exercise pre-change code; check `ss -lptn 'sport = :5080'` and use
`CORNUS_WEB_MOCK_PORT` rather than killing a port you did not open. And `tsc --noEmit` does not
cover `web/mock/` — tsconfig's `include` is `["src"]` alone, so that file is only type-stripped
by Node at run time and smoke-running the mock is the only check it gets.

**One design route rejected rather than deferred.** Injecting `PROMPT_COMMAND` via
`ExecConfig.Env` to make shells emit OSC 7 would fight a user's own env injection in both
directions. The pid+/proc path removes the need for a shell hook entirely.

### Verification

Go gate clean (`gofmt -l`, build, vet, `go test ./...`); `tsc --noEmit` clean; `vitest run` 468
passed, up from 460 at the session's start; `make e2e-check` parses every scenario;
`make e2e-web-terminal` passes on TARGET=docker AND TARGET=kube. Nothing committed.

### Left open

Tracked in TODO: the three parameterized E2E targets that still lack `KEEP`; `kind` not being
installed on this host (aarch64 — the amd64 build runs under emulation and looks fine); and the
uneven arbitrary exec env injection, which exists for `cornus compose exec` but not for plain
`cornus exec`, the web terminal API, or client contexts.

## 2026-08-03 — The activity detector was reading window titles as if they were the screen

**The question that found it.** "Is the prompt sniffer for status detection OSC-aware? They
might well pick text from an irrelevant application." It was not, and the answer is worse than
OSC 7 alone: the detector was reading EVERY OSC payload as visible text.

**Why.** `agentdetect.go` renders a session's output to a headless `tonistiigi/vt100` screen and
matches `rules.toml` against the bottom lines. That library does not implement OSC at all —
`scanEscapeCommand` only treats `ESC [` as introducing arguments, so for `ESC ]` it returns
after two bytes and the entire payload falls through as printable text. Fed
`ESC]0;vim README.md BEL ESC]7;file:///srv/app ST ESC]5379;cornus-pid=4242 BEL $ `, the
rendered screen was literally:

    0;vim README.md7;file:///srv/app5379;cornus-pid=4242$

**It is a live false-positive source, not a theoretical one.** Against the shipped rules:
`\by/n\b` matches an OSC 7 path like `file:///srv/y/n`, and
`\b(overwrite|replace|delete|proceed|continue)\b[^\n]*\?\s*$` matches any TUI that mirrors its
dialog into its window title. Either pins a session at "needs you" over text the user cannot
see. Pre-existing (shells have always set titles), but the pid announcement added this session
made every wrapped session carry an OSC unconditionally, so it went from "some sessions" to
"all of them".

**Fix.** The scanner already walks every byte with exact state, so it now also emits
`oscUpdate.visible`: the chunk with OSC sequences removed and everything else — CSI above all —
untouched. `readLoop` feeds THAT to the detector; the ring and the browser still get raw bytes,
because xterm.js understands OSC and the replay buffer must hold what the session actually
sent. The ESC introducing a sequence is HELD rather than emitted, since whether it is visible
depends on the next byte, which may arrive in a later chunk.

**Three tries to get one test to mean anything — the most instructive part.** The neutralization
was reverting `ts.det.feed(u.visible)` to `ts.det.feed(chunk)`, i.e. restoring the exact bug.

1. **First test fed the detector directly** via `s.scan(...).visible`. It proved the scanner and
   detector agree and said nothing about `readLoop` wiring the two together. The break failed
   nothing.
2. **Second test went through `readLoop`, but its payloads could not trip a rule.** Consecutive
   OSC payloads CONCATENATE on the screen, so `/srv/y/n` ran straight into the next sequence's
   `0;` and destroyed the word boundary `\by/n\b` needs; likewise `?` was not at end of line for
   the `\?\s*$` anchor. The input was dangerous-looking and inert. Fixed by placing each payload
   last, where the anchors bind — verified by checking the rule against the raw-fed screen
   directly rather than by reading the regexes.
3. **Third test still raced.** A session starts `idle`, so `State != "working"` was true before
   readLoop had processed a single byte, and "not blocked" was trivially satisfied. Fixed by
   waiting for evidence the sequence was consumed (`Title`/`Cwd` set) and then asserting the
   detector settles to IDLE — a state only reachable by the settle timer having run on a clean
   screen — instead of asserting the absence of blocked.

Only after all three does the break fail, and it now fails both sub-cases. The general lesson,
which is the CLAUDE.md rule in three different disguises: a test can bypass the wiring, or
exercise the wiring with inert data, or assert a condition that is true before the work happens.

**Gate.** Go gate clean; `tsc --noEmit` clean; `vitest run` 468 passed.

## 2026-08-03 — OSC was one of five: every string sequence was leaking onto the detector's screen

**Follow-up to the entry above, prompted by "there should be other sequences that may hurt."**
There were. OSC is one of FIVE string-type sequences with the same shape — introducer, payload,
ST — and `tonistiigi/vt100` paints the payload of all of them, because `scanEscapeCommand`
returns after two bytes for anything that is not `ESC [`. Measured, not reasoned about:

| sequence | leaked onto the screen | tripped a blocked rule |
|---|---|---|
| OSC `ESC ]` | (already fixed) | — |
| DCS `ESC P` — sixel, tmux passthrough, DECRQSS | whole payload | no |
| APC `ESC _` — kitty graphics | the entire base64 blob | no |
| PM `ESC ^` | payload | **yes** |
| SOS `ESC X` | payload | **yes** |
| nF `ESC ( B` — charset selection | a stray "B" per sequence | no |
| CSI `ESC [` | correctly consumed | — |

PM and SOS matched the shipped rules outright. DCS and APC are the volume problem: a sixel image
or a kitty graphics blob puts hundreds of kilobytes of payload on a 24x80 classification screen,
which also churns the change-hash and would read as permanent activity. The nF case is the most
FREQUENT — ncurses programs emit charset selection constantly, and each one left a letter.

**Fix.** The scanner now elides every string sequence, parsing only OSC's payload, and drops nF
escapes whole (the model does not implement charset selection, so there is nothing to lose).
Four details that each needed their own decision:

1. **BEL terminates OSC only.** DCS/APC/PM/SOS end at ST, and treating a BEL inside one as a
   terminator ends the sequence early and spills the rest of a binary payload onto the screen.
2. **An elide budget** (`stringElideMax`, 1 MiB). An unterminated `ESC P` would otherwise blind
   the detector for the session's life — a worse failure than the leak, since every session
   would then look permanently idle.
3. **The ESC that ABORTS a string sequence is not visible.** It lived inside the payload being
   elided. tmux passthrough doubles its inner ESCs, so this is the ordinary path for it, and
   emitting that ESC left a stray one on screen.
4. **A bare ST outside any sequence is swallowed**, being a no-op for a real terminal and the
   residue of exactly the abort above.

**Neutralization: 5 breaks, all caught** — dropping the DCS/APC/PM/SOS introducers, passing nF
escapes through, letting BEL terminate everything, removing the elide budget, and marking the
aborting ESC visible. One pre-existing test of mine asserted the OLD behaviour (`ESC ( B` passes
through) and was updated with the reason, rather than the new behaviour being bent to fit it.

**Gate.** Go gate clean. Web: `tsc --noEmit` clean; the vitest suite has ONE failure,
`Settings > renders its groups as ordinary sections, not cards` (expects 6 setting rows, finds
7). It is not from this work — `web/src/{settings.ts,views/Settings.tsx}` were modified at
16:13/16:14 and `views.test.tsx` at 16:17, by the other agent adding a setting, and none of
those are files this work touches. The tests belonging to this work were run in isolation and
pass (window-title, follow, live-directory: 12 tests).

## 2026-08-03 — E2E re-run on both targets after the string-sequence fix

Re-verification after the scanner was generalized to elide DCS/APC/PM/SOS and nF escapes, and
after shell discovery moved to `pkg/shells`. Both scenarios pass on BOTH targets:

- `web-terminal-introspect.star` — docker ✓, kube ✓ (all three assertions each).
- `web.star` — docker ✓, kube ✓. Worth running for the refactor specifically: it is the only
  E2E that drives shell discovery through a REAL image, so it covers `pkg/shells.FromArgv` and
  `Split` end to end. The WARN spray on the kube run ("no such file or directory" for
  /bin/zsh, /usr/bin/bash, /bin/dash …) is the discovery probe finding the candidates alpine
  does NOT have — the negative half the scenario asserts, not a fault.

Ran with `--keep` on kube; the cluster is intact (23h old, same node) and no e2e workloads were
left behind.

**`make build` is currently broken by concurrent work, so the runner was built directly.** The
`build` target runs `tsc --noEmit && vite build`, and `web/src/views/views.test.tsx` has unused
imports (`PINCH_STEP_RATIO` and a whole import declaration) from the other agent's in-flight
edit. Rather than touch their file, the Go binaries were built on their own
(`go build -o bin/cornus ./cmd/cornus`, same for cornus-e2e) against the SPA bundle already in
`pkg/webui/dist`. Sound for these scenarios: both drive the BFF's HTTP API and neither asserts
anything about the bundle. Worth knowing as a pattern — a broken web typecheck blocks every
`make` target that depends on `build`, including all the e2e ones, even when the change under
test is pure Go.

## 2026-08-03 — Opt-in pinch-zoom for pane contents

Three kinds of pane show content whose size the user could not change: a terminal (xterm,
`fontSize: 13` hard-coded), an editor (CodeMirror, no font-size rule at all — it inherited
`body`'s 14px), and an image preview (`object-fit: contain`, fit-to-tile only). On the phones
and tablets the recent touch work was for, 13px monospace in a tile is not readable, and the
browser's own pinch is the wrong tool: it scales the chrome too and does not survive a pane
resize.

Shipped as `settings.paneZoom`, off by default. Four decisions settled with the user before
any code:

- **Zoom is a text SIZE, not a visual scale.** The terminal writes `term.options.fontSize`
  and refits (so `term.onResize` tells the BFF the new cols/rows and the shell re-flows); the
  editor scales `.cm-editor`'s `font-size` and CodeMirror re-flows with its gutter; the image
  grows for real and pans. Nothing is resampled and nothing is clipped.
- **Levels live in a module singleton keyed by pane id** (`views/tiling/zoom.ts`),
  session-scoped, not in the layout blob. No change to `PaneData` or `validData()`.
- **A trackpad pinch counts** — every engine delivers it as `wheel` with `ctrlKey`.
- **Three commands** tagged `PANE_TAG`, so a zoom always has a visible way back.

### The design decisions worth keeping

**Discrete steps, not a factor.** `ZOOM_SCALES` is a frozen eleven-entry table and a pane's
level is an INDEX into it. This is what disposes of the obvious objection to changing a
terminal's font on a live gesture — reflow churn — without a debounce anywhere: xterm is
re-fitted only when a step boundary is crossed, so a slow spread across the whole range costs
ten resizes rather than one per animation frame. It also makes the gesture and the keyboard
reach exactly the same set of sizes, which a free-running factor could not promise. At 13px
the eleven steps round to 8, 9, 10, 12, 13, 15, 17, 20, 23, 26, 31 — all distinct, so no step
is a no-op there, and `termFont.test.ts` pins that.

**The recognizer reports steps, not a scale.** `pinch.ts` re-bases by one threshold on every
emit rather than measuring against the gesture's start. Without that, spreading past two
thresholds fires the second step the instant the first lands and the zoom runs away from the
fingers; measuring against the start instead fires on every subsequent move. Both wrong
versions are caught by tests written before the right one was settled.

**`--pane-zoom` is absent, not 1, on a pane at rest.** Every rule spells
`var(--pane-zoom, 1)`, so an unzoomed pane renders through exactly the declarations it always
did, and "the feature left no trace on a pane nobody zoomed" becomes something a test can
see rather than something a comment claims. `resetZoom` deletes the entry for the same
reason, which is also what makes `zoomLevels()` answer "which panes are zoomed".

**The auto-margin on `.image-viewer-img` is load-bearing, not cosmetic.** `.stack-pane
.image-viewer` centred with `align-items: center; justify-content: center` and `overflow:
auto`. A flex item centred by its CONTAINER and then overflowed is clipped at its top and
left *unreachably* — a scroll container cannot scroll to a negative offset — so a zoomed
image would have had two corners nobody could pan to. Auto margins on the item centre
identically while it fits and collapse once it does not. The container's two `*-items:
center` declarations came out in the same change, and a test asserts their absence with the
reason, because that is exactly the pair someone tidying up would put back.

**`touch-action` is set inline by `pinchZoom`, not in `styles.css`.** It has to apply only
while the feature is on — a standing rule would take the browser's native pinch away from
every user, which is the trade the setting exists to let them decline. `pan-x pan-y` rather
than `none`, so a zoomed editor and a zoomed image still scroll under one finger. The
disposer restores the PRIOR value rather than clearing, since the coarse-pointer block
already puts `manipulation` on several elements.

**The setting gates the commands as well as the gesture.** One switch, one meaning: a
"Reset zoom" row on a screen where nothing can zoom cannot mean anything, and a user who
never opted in gets a palette that is byte-for-byte unchanged.

### Findings

**The existing ⋮ tests independently caught an ungated command list.** Neutralizing the
`if (!settings().paneZoom) return []` guard failed not only the new opt-in test but also
"shows the same tile menu whichever kind of pane is focused" and "opens the palette on the
pane tag instead of a menu of its own". Those were written for the rule that the ⋮ must not
drift with the pane under the cursor, and they turn out to defend the palette against any
unannounced addition. Worth knowing before adding a `PANE_TAG` command: two tests will tell
you, and they are the right two.

**The zoom trio is the first `pane`-tagged command that comes and goes.** It does not break
that rule — the rule is about the list changing with the FOCUSED PANE, and these change with
a global preference, so every tile on the screen has them or none does. Stated in the code
beside them, because the next person to read the invariant will otherwise read this as a
violation of it.

**jsdom reports an unset `touch-action` as `undefined` where a browser reports `""`.** The
first version of the disposer test asserted `toBe("")` after disposal and failed. The fix was
not to loosen it but to assert a ROUND TRIP from a real value (`manipulation` → `pan-x
pan-y` → `manipulation`): the loose version would have passed on a disposer that cleared the
property, and clearing is wrong.

**`cancelPendingDrag` needed a `vi.mock` to be observable at all.** dnd's `dragging()` only
turns true once a drag has LIFTED, which is precisely the case `cancelPendingDrag` leaves
alone, so the interlock ("a second finger takes the press away from a pending tab drag") has
no externally visible effect to assert. Mocking `./dnd` in `pinch.test.ts` is what turns that
comment into a checked claim.

**Neutralization ran in full, seven breaks.** Ungated gesture, two wrong re-basing rules,
component-local levels (simulated by calling `resetZoom` on mount — the exact failure the
singleton exists to prevent), a dropped prune, `resetZoom` recording home instead of
deleting, restored container centering, and a removed `termFontPx` floor. Each failed with
the diagnostic it was written for; the remount test's `expect(now).not.toBe(el)` premise
guard is what keeps it from passing vacuously if a split ever stops rebuilding its sibling.

**Not covered: that `term.options.fontSize` is actually written.** The `Terminal` instance is
local to `Term`'s `onMount` and deliberately stays there (nothing outside may hold a
reference to a terminal mid-teardown), so the effect's write is not observable from a test.
`termFontPx` is unit-tested and the effect is one line, but the wiring between them rests on
review, not on a test. Nor was the gesture exercised on a real touch device — the suite
synthesizes pointer events, and jsdom has no PointerEvent at all.

**Concurrent work in the tree was not touched**: the `webbff` OSC/proc-probe work, `pkg/shells`,
`FilePane.tsx`, `Overview.tsx`, `api.ts`, `Makefile`, and the staged `TODO.md`. The Go gate
was therefore not run — this change is SPA-only, and building someone else's in-flight Go
would have said nothing about it either way.

Gate: `tsc --noEmit` clean, **510 SPA tests passing** — 468 before this change, plus 42: 30 in
the three new unit files (`pinch.test.ts` 18, `views/tiling/zoom.test.ts` 9,
`components/termFont.test.ts` 3), 11 in `views.test.tsx`, 1 in `settings.test.ts`.
`npm run build` clean, `pkg/webui/dist/.gitkeep` re-touched. Nothing committed.

## 2026-08-03 — Detection rules now scope to the LIVE foreground program

**The concern, correctly understood this time.** The previous two entries fixed text that was
never on screen (OSC and the other string sequences). The actual concern was the opposite and
worse: the built-in patterns match ANY application that emits the text, so a program merely
DISPLAYING the words is classified as prompting. Measured before the fix, with the shipped
rules:

| foreground | screen | blocked |
|---|---|---|
| `cat` | "Run the installer and answer: Do you want to enable telemetry?" | **true** |
| `less` | "- fixed the [y/n] prompt in the uninstaller" | **true** |
| `git log` | "abc1234 delete the stale mirror?" | **true** |
| `grep` | "src/tui.go: press enter to continue" | **true** |

All ten built-in rules were unscoped (`agent` empty on every one, which `matches` treats as
"applies to every session"), and `detector.agent` was `agentName(cmd[0])` — empty for any plain
shell and immutable for the session's life. `agentdetect.go` documented this ("screen rules
still apply regardless"), so it was known rather than accidental.

**Fix: scope every built-in rule, keyed on the program actually in the foreground.** The pid
work made this possible — the /proc probe's `comm` is the only way to see a program the user
started INSIDE a shell, which is exactly the case the old `cmd[0]` scope could never cover.

- `detector.agent` is now MUTABLE and lock-guarded (it was read unlocked on the strength of
  being immutable; `info()` now goes through `currentAgent()`). It starts at `agentName(cmd)`,
  so a session launched straight into an agent works with no probe at all, and is replaced by
  the probe's `comm` as it changes.
- **A shell in the foreground is not an agent.** `setAgent` maps it to none through the same
  `shells.IsShell` the launch path uses. At a prompt the session is idle by definition, and
  letting "bash" scope the rules would be the old everything-matches behaviour renamed.
- **An empty `comm` is ignored, not cleared** — a failed probe is not evidence the program
  changed.
- Schema gained `agents = [...]` alongside `agent = "..."`, because the useful scope is usually
  a SET (a password prompt belongs to ssh and sudo and gpg alike). Both merge.
- **Unscoped still means "every session"** — that is the contract for user-authored rules,
  where the author knows what they are asking for. Only the built-ins are scoped, because only
  they run against everybody.
- **An unknown foreground program matches no scoped rule.** Conservative by the user's explicit
  choice: a missed prompt in a session we cannot identify beats a false "needs you" everywhere.

**The probe's gating changed, and this is the cost.** It used to run only when OSC had not
supplied a title or cwd. The foreground program is now the CLASSIFIER'S INPUT, not a fallback
label, so it must be refreshed continuously — the probe now runs for any alive session with a
pid. Still bounded the same way: only while the session list is being polled, at most once per
`procProbeTTL`, retired after `procProbeMisses` fruitless attempts.

**The scope assignments are a judgment call and should be reviewed.** Agent-UI-shaped rules
(arrow selector, use-arrow-keys, do-you-want-to, esc-to-interrupt, thinking…, spinner, y/n,
overwrite?) went to `["claude"]`, the only agent the rule set names. Credential prompts went to
`["ssh","sudo","su","passwd","gpg","git","scp","sftp"]`; press-enter-to-continue to
`["claude","apt","apt-get","dpkg","yum","dnf"]`. Those lists are mine, not derived from
anything in the repo, and they are what decides whether the feature fires at all. See TODO.

**Four tests pinned the OLD semantics and were updated, not worked around.**
`TestDefaultRulesMatch` passed `agent=""` for every positive sample; `TestDetectToleratesGarbage`
launched a plain shell (its subject is garbage tolerance, so it now launches an agent);
`TestTermSessionPrefersOSCOverTheProbe` asserted ZERO probes for an OSC-answered session, which
is no longer the contract. Added `TestDefaultRulesDoNotFireOnProgramsMerelyDisplayingText` (the
four cases above, plus the unknown-agent case) and
`TestTermSessionScopesRulesToTheLiveForegroundProgram`.

**Neutralization: 4 breaks, all caught** — ignoring the scope entirely, dropping `agents` at
parse, not telling the detector the probe's comm, and letting a shell count as an agent.

**Gate.** Go gate clean; `tsc --noEmit` clean; `vitest run` 510 passed (the concurrent agent's
build breakage from earlier is resolved).

## 2026-08-03 — What herdr's manifest format says about our detection design

Research, no code changes. `agentdetect.go` calls itself "a clean-room implementation of the
documented concept"; the concept is herdr's (a Rust terminal multiplexer with agent-state
awareness), and the resemblance is close enough to be deliberate — bottom-buffer snapshot, TOML
rules, and the same `agent-detection/` config directory name.

**herdr's schema** (`src/detect/manifests/<agent>.toml`, user overrides at
`~/.config/herdr/agent-detection/<agent>.toml`): top-level `id` / `version` /
`min_engine_version` / `updated_at` / `aliases`, then `[[rules]]` with `id`, `state`
(working|idle|blocked|unknown|done), `priority` (higher wins), **`region`**,
`contains` / `line_regex` / `regex`, gates `any` (OR) / `all` (AND) / `not`, plus
`visible_working` / `visible_blocker` / `visible_idle` and `skip_state_update`.

**`region` is the primitive we lack, and it is the actual fix for the false positives.**
herdr scopes each rule to part of the screen: `prompt_box_body`,
`after_last_horizontal_rule`, `bottom_non_empty_lines(N)`, `whole_recent`, plus `osc_title`
and `osc_progress`. Cornus matches every rule against ONE fixed region (bottom
`detBottomLines`), which is why `\by/n\b` cannot be distinguished from the same text in
`git log` output. Region scoping would have solved the reported problem WITHOUT the agent-list
scoping added earlier today — that scoping is a cruder instrument aimed at the same target.

**OSC is EVIDENCE in herdr, not noise.** `osc_title` and `osc_progress` are regions it matches
against deliberately. Earlier today the OSC payloads were STRIPPED from the detector entirely.
The leak being fixed was real — a title bleeding into the screen region is exactly the
"whole-pane incidental text" herdr's own authoring guidance warns against — but the right fix
is to route OSC to its own addressable region rather than discard it. That change was half
right and should be revisited alongside `region`.

**Other gaps**: `priority` plus `any`/`all`/`not` gates (cornus is first-match-wins with no
negation, so "blocked if X and NOT Y" is inexpressible); `aliases` (which solves the
hardcoded-binary-name weakness of the `agents = ["claude"]` lists); an authoring workflow —
`herdr agent read <pane> --source detection --format text|ansi` captures the real bottom buffer
so manifests are DERIVED from observed output, where cornus's patterns are unvalidated; and
out-of-band manifest updates (herdr issue #677), where cornus embeds rules.toml at build time.

**Recommended order if pursued**: `region` first (highest value, subsumes most of the agent
scoping), then `not` gates, then per-agent files with `aliases`, restoring OSC as a region
along with `region`. Not started — it is a redesign, and the user has not asked for it yet.

Sources: herdr.dev/docs/agents, github.com/ogulcancelik/herdr (src/detect/manifests/claude.toml,
AGENTS.md, issue #677), and a real-world claude.toml override gist.

## 2026-08-03 — Can the detector actually name the foreground process? Transport yes, identity no

Asked directly whether the detector is really capable of getting a foreground process name.
Measured rather than argued, and the answer is half no — a defect in work landed earlier today.

**The transport works.** In a real container exec (docker, alpine, `/bin/sh` on a pty via
`docker exec -it`), job control is live and tpgid tracks the foreground job:

    shell at its prompt                        tpgid=7   comm=sh     cmdline=/bin/sh
    foreground child (cd /usr && exec sleep 60) tpgid=20  comm=sleep  cmdline="sleep 60"

So the pid announcement -> tpgid -> /proc chain is sound, and the earlier concern that a
container exec might not be a session leader with a controlling terminal does not bite.

**The identity does not.** The probe reads `/proc/<tpgid>/comm`, which is TASK_COMM_LEN
truncated (15 chars) and names the INTERPRETER'S THREAD, not the program:

    node -e '...'   ->  comm = "node-MainThread"
                        cmdline = /.../node -e setTimeout(()=>{},4000)

Claude Code is a Node program. So `agents = ["claude"]`, added this morning and keyed on comm,
matches nothing for it. The scoping did not narrow detection — it made it inert for any agent
behind node, python, or a shim, which is most of them.

**herdr solved exactly this, and their fixtures name the case.** `src/detect/mod.rs` treats the
process name as a STARTING POINT: `is_generic_runtime_or_shell` (node, bun, python, sh, bash,
zsh, fish, cmd, powershell) routes to `wrapped_agent_name_from_runtime_argv`, which knows which
flags carry a script per runtime (`-e`/`--eval` for node/bun, `-c`/`-m` for python, `-c` for
shells); then `argv0_agent_name`, then `cmdline_argv0_agent_name`, then
`resolved_agent_name_from_path_token` (canonicalize, i.e. resolve shim symlinks), then
`agent_name_from_known_package_path` (e.g. `node_modules/@earendil-works/pi-coding-agent/dist/cli`).
Their test data includes `comm="node", argv=["node","/path/to/bin/codex"]` — the precise shape
that defeats a comm-only reader.

**Why the E2E gave false confidence.** `web-terminal-introspect.star` runs
`sh -c 'exec sleep 300'`; `exec` replaces the shell so the announced pid IS the final program,
and comm names it directly. The scenario proves the pid plumbing end to end and says nothing
about identifying an agent reached through an interpreter. A green run on the wrong shape is
the same failure as the `dir`/WorkingDir one earlier today, in a different costume.

**Not fixed here** — recorded in TODO with the resolution order to copy. The corrective is to
read `/proc/<tpgid>/cmdline` alongside comm and resolve as above, with agent names coming from
the bundled manifests' `id`/`aliases` (`third_party/herdr/`) rather than the hardcoded lists
now in rules.toml.

**Standing lesson.** Three times today a check passed for the wrong reason — the detector test
that bypassed readLoop, the docker-only E2E that leaned on `dir`, and now an E2E whose subject
`exec`s away the very indirection under test. In each case the fixture was shaped so the bug
could not appear. Ask what the fixture would look like if the claim were FALSE, and build that.

## 2026-08-03 — A herdr-compatible agent detector

Landed `pkg/agentdetect`: a Go reimplementation of herdr's detection semantics,
driving classification from the manifests vendored at `third_party/herdr/`.

**Why a reimplementation and not an adaptation.** The manifests are third-party data
refreshed from upstream as agent UIs change. Anything evaluated differently here becomes a
misclassification nobody can explain from the TOML, so the semantics were taken from
`src/detect/manifest.rs` rather than reasoned about — notably the priority tie-break (highest
priority wins; ties go to the FIRST rule, so a later rule must strictly exceed to displace) and
`any` constraining only when non-empty.

**Implemented**: `id`/`aliases`/`version`/`min_engine_version`; `[[rules]]` with `state`
(working|idle|blocked|unknown|done), `priority`, `region`, `contains` (case-insensitive AND),
`regex` (whole region), `line_regex` (must match SOME line), recursive `any`/`all`/`not` gates,
`visible_*`, `skip_state_update`. Regions: `whole_recent`, `bottom_lines(N)`,
`bottom_non_empty_lines(N)`, `top_non_empty_lines(N)`, `after_last_horizontal_rule`,
`after_last_prompt_marker`, `prompt_box_body`, and — the point — `osc_title` / `osc_progress`,
which read the escape sequences as EVIDENCE. Region slicing keeps the trailing newline because
upstream returns a slice of the original and a `$`-anchored line_regex can tell the difference.

**Four bugs found by testing, not by reading.**

1. **go-toml does not flatten anonymous embedded structs.** The gate fields were declared via an
   embedded `rawGate` and silently parsed as EMPTY — and an empty gate matches everything, so
   every rule fired on every screen. Spelled out explicitly now.
2. **Rust regex vs RE2.** 9 of the 60 bundled patterns use `\uHHHH` / `\u{HH..}` and one uses
   `\p{Alphabetic}`; RE2 accepts neither. `translateRegex` converts them, with the
   `\p{Alphabetic}` -> `\p{L}` narrowing documented as an approximation. A test asserts every
   bundled pattern compiles, so a refresh introducing new syntax fails loudly instead of killing
   detection.
3. **Alias resolution returned the alias**, not the manifest id. `Identify` canonicalises.
4. **Region slicing** initially re-joined lines and lost trailing newlines.

**The identification defect from the previous entry is fixed.** `AgentName` follows herdr's
order: if the process name is a generic runtime or shell, unwrap the agent from argv with
per-runtime flag knowledge (`-e`/`--eval` for node/bun, `-c`/`-m` for python, `-c` for shells,
`VAR=v` skipping for env); else the process name; else `argv[0]`'s basename. Plus
`normalizeLookupName` strips Node's `-MainThread` suffix — the measured case. The probe now
reports argv (`tr '\000' '\n'` over `/proc/<tpgid>/cmdline`, one `arg=` line each).
Identification goes through the manifest Set, so "is this an agent?" and "which rules apply?"
cannot disagree.

**Consequences, stated plainly.** Cornus's native rule schema no longer reaches the detector.
`rules.toml`, `detect_rules.go` and the user-extension path under
`~/.config/cornus/agent-detection/` are inert; `TestDetectUserOverrideRule` is skipped with that
reason rather than deleted, because retiring a user-facing path is a decision, not a cleanup.
A session whose foreground program has no manifest is IDLE — no manifest, no guessing.

**Neutralization: 6 breaks, all caught after two false starts.** Identification on comm only
(the original defect), the `-MainThread` normalization, the probe dropping argv, and
`prompt_box_body` widening to the whole screen. Two earlier attempts produced COMPILE errors
rather than failures and were redone behaviourally — and the `-MainThread` break initially
caught nothing because no test used that comm with a resolvable argv, so the measured case got
its own fixture.

**Gate.** gofmt/build/vet/`go test ./...` clean; `tsc --noEmit` clean; vitest 510 passed.
E2E not re-run for this change — `web-terminal-introspect.star` covers the pid/cwd plumbing,
not classification, and a scenario exercising a real agent's screen does not exist yet.

## 2026-08-03 — E2E for agent classification, and the fixture that proved nothing

New `e2e/scenarios/web-agent-detect.star` (`make e2e-web-agent`, opt-in, in
`EXTRA_CHECK_SCENARIOS`). It covers the half `web-terminal-introspect.star` does not:
identification through the manifest set and classification by the identified agent's bundled
manifest.

**Two-sided by construction.** One session's foreground program is an "agent"; a second shows
the IDENTICAL screen from a plain shell. Exactly one may be blocked. The negative is the half
that matters — a detector that pattern-matched the screen alone passes the positive case and
fails here, which is the defect this design replaced.

**Making a container hold an agent without installing one.** `/tmp/claude` is a two-line script
(`#!/bin/sh` + `sleep 300`), written with `echo` and single quotes only so the command survives
a JSON body with no escaping layers. Two constraints found the hard way: the script must NOT
`exec` its sleep, or the foreground process becomes `sleep` and loses the agent's name; and
busybox cannot be copied under a new name, because it dispatches on argv[0] and a renamed copy
is an unknown applet.

**The fixture initially proved nothing, and neutralization is the only reason that surfaced.**
The first version ran `exec /tmp/claude`. Disabling runtime unwrapping — the whole defect fixed
earlier today — left the scenario PASSING. Reason: **Linux sets `comm` from the script's own
name**, so comm was already "claude" and a comm-only reader found it without unwrapping
anything. The scenario's own comment claimed it was "deliberately the hard case", and that was
false. Fixed by launching through the interpreter explicitly (`exec /bin/sh /tmp/claude`), which
puts "sh" in comm and leaves the agent's name only in argv — the shape a real Node- or
Python-based agent presents. The break now fails with "never identified as an agent".

That is the fourth time today a check passed for the wrong reason, and the second where the
fixture's SHAPE made the bug unreachable. The pattern is specific enough to name: when a test
sets up the subject, ask what the setup makes IMPOSSIBLE, not only what it makes visible.

**Neutralization: 2 breaks, both caught** — runtime unwrapping disabled (positive half), and
every session resolving to claude (negative half, caught with "got 2, want 1").

**Gate.** Passes on TARGET=docker AND TARGET=kube (with `--keep`; the cluster is intact).
`make e2e-check` parses all scenarios. Go gate clean.
