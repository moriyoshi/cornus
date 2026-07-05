# Project To-Dos

Items extracted from JOURNAL.md during `good-sleep` consolidation, plus open follow-ups. Each
item should be resolved or removed once addressed.

Completed items are cleared periodically into a "TODO wrap-up" entry in JOURNAL.md (the closure
index); the last sweep was 2026-07-26.

## Open Items

- [x] The translated `docs/ja/reference/server-env-vars.md` and
      `docs/zh/reference/server-env-vars.md` carry **zero** `CORNUS_OBS*` rows, while the English
      page has twelve. The observability store, log recording, metrics recording, and re-export are
      entirely undocumented in the two translated env-var references. The guides' own settings
      tables ARE translated and cover the common flags, so this is a reference-completeness gap
      rather than a total blackout — but a reader who goes to the reference page will conclude the
      variables do not exist. The docs build cannot catch it: both pages are valid and every anchor
      resolves.
      — *source: JOURNAL 2026-07-27 — Workload and server resource metrics on every backend*

- [ ] Histogram metrics are recorded but unreachable from PromQL. The SDK bridge delivers
      `http.server.request.duration` and friends into `metrics_histogram`, and the store's PromQL
      profile rejects them ("histogram selectors require canonical ..."; the `_bucket` / `_sum` /
      `_count` spellings do not resolve either). `cornus observe query` (SQL) is the documented
      workaround. Worth revisiting whether cornus should emit the canonical form the engine wants,
      or whether this is an upstream imbh-go gap to report.
      — *source: JOURNAL 2026-07-27 — Workload and server resource metrics on every backend*

- [ ] `observability-metrics.star` has never run against the kubernetes target, so
      `(*kubernetes.Backend).SampleMetrics` is covered only by unit tests against a canned REST
      transport. It needs a cluster with metrics-server installed plus the new
      `metrics.k8s.io` RBAC grant. The scenario's assertions would need gating on
      metrics-server's presence rather than failing where it is absent.
      — *source: JOURNAL 2026-07-27 — Workload and server resource metrics on every backend*

- [ ] Run `observability-telemetry-mux.star` for real. It has only ever SKIPPED: it needs
      `CORNUS_TEST_OTEL` plus an `otelcol`-tagged `cornus:e2e` sidecar image (`make e2e-image
      E2E_BUILD_TAGS="netgo osusergo otelcol imbh sable_extern_lib"`). Until it runs, the caretaker
      telemetry relay's loopback-shim-to-mux path is unexercised end to end — on a feature that is
      now ON BY DEFAULT whenever cornus is the telemetry destination. Both sides of the `'T'` stream
      are unit-tested and the framing round-trips, but nothing has proven a real Collector exports
      through the shim and the server ingests it.
      — *source: JOURNAL 2026-07-26 — Session summary: built-in workload observability*

- [x] ~~Decide whether `imbh` joins the Dockerfile default `BUILD_TAGS`.~~ DONE (2026-07-27):
      `imbh sable_extern_lib` is in the Dockerfile default `BUILD_TAGS` and in all five released
      binaries, the `obsstore` CI job is blocking (its first amd64 run went green 2026-07-26), and
      `--obs` defaults to on wherever the store is linked in. The linux binaries stayed fully static
      via a musl build plus a `libgcc_s`->`libgcc_eh` shim.
      — *source: JOURNAL 2026-07-27 — Shipping observability out of the box*

- [ ] Prove the darwin and windows release legs actually link `imbh`. Upstream says both are
      unverified: `link_darwin.go` calls its `-framework` set "finalized empirically on a macOS
      runner" and `link_windows.go` calls Windows support "best-effort". The Windows leg has a second
      unknown — `eval`-ing `imbhgo-fetch -print-env` carries a backslashed Windows path into
      `CGO_LDFLAGS`. Run the release workflow via `workflow_dispatch` (builds all five, publishes
      nothing) BEFORE cutting the next tag; the build script fails loudly rather than shipping a
      storeless binary, so a red leg is the expected failure mode, not a silent one.
      — *source: JOURNAL 2026-07-27 — Shipping observability out of the box*

- [ ] Watch the image job's emulated arm64 leg now that the image is a cgo build. `release.yml`'s
      `image` job builds `linux/amd64,linux/arm64` on ONE amd64 runner via QEMU, and the Dockerfile's
      build stage is deliberately not `$BUILDPLATFORM`-pinned (that is what gives each arch a native
      gcc and the right libc cell). So the arm64 leg now links a ~600 MB Rust archive under
      qemu-user, which was merely slow when it was `CGO_ENABLED=0` and may now be very slow or OOM.
      A `workflow_dispatch` run exercises it (the `image` job is not tag-gated), so measure before
      tagging. If it does not hold, split the job into native per-arch legs (`ubuntu-24.04` +
      `ubuntu-24.04-arm`) and join them with `docker buildx imagetools create` — note the cosign
      step signs by manifest digest, so it has to move after the merge.
      — *source: JOURNAL 2026-07-27 — Shipping observability out of the box*

- [ ] Reproduce the Rust crate attribution for `libimbhgo.a`. `go-licenses` walks Go modules only, so
      `THIRD_PARTY_NOTICES.md` lists imbh-go (Apache-2.0) and sable (MIT) but none of the Rust crates
      statically linked inside the archive we distribute. The gap is disclosed in the root `NOTICE`;
      closing it needs an upstream notices manifest published alongside the archive (ask imbh-go to
      emit one, e.g. via `cargo about`), then folding it into the bundle.
      — *source: JOURNAL 2026-07-27 — Shipping observability out of the box*

- [x] Backfill the observability section of the translated server-env-vars references.
      `docs/ja/reference/server-env-vars.md` and the `zh` counterpart contain no `CORNUS_OBS*` rows at
      all (315/274 lines vs 387 in English) — a pre-existing gap, now more visible since the store is
      on by default.
      — *source: JOURNAL 2026-07-27 — Shipping observability out of the box*

- [ ] E2E-cover traces and metrics with an instrumented workload. The observability suite proves the
      LOG path end to end, but the trace/metric surfaces are only asserted empty (`observe traces`
      reports "no matching traces"; an unresolvable PromQL metric returns a diagnostic). Nothing in
      the suite runs an app that actually exports spans, so the ingest-to-query path for those two
      signals — including the Tempo datasource shaping — rests on unit tests alone.
      — *source: JOURNAL 2026-07-26 — Session summary: built-in workload observability*

- [x] Make `deploy_attach`'s readiness wait robust to a leftover workload. It polls
      `Status(name)`, which is satisfied by ANY running container carrying that deployment's label —
      including one a PREVIOUS failed run left behind on the same daemon. The wait then returns
      instantly, before the current run's deploy has done anything, and the scenario asserts against
      a half-empty world. This cost two flaky failures in `activity-flight-record.star` before it was
      understood; that scenario now clears its workload up front, but every scenario whose workload
      can outlive a failure inherits the same edge (Starlark has no `defer`, so end-of-scenario
      cleanup does not run on failure). Options: have `deploy_attach` reap a same-named workload
      first, or match on something run-scoped rather than the name.
      — *source: JOURNAL 2026-07-26 — E2E for the flight recorder, and a scenario that lied*
      **DONE (2026-07-27 sweep): the wait now keys on the deploy-attach protocol's own run-scoped ready marker (emitted only after the server applied THIS session's spec) instead of polling `Status(name)`. Instance-ID diffing was rejected: on kube, re-applying an identical spec does not recreate the pod. 5 regression tests, no daemon needed.**

- [ ] Remove the root-owned `.agents-workspace/tmp/fr-data/` left by a manual repro (the
      containerized server runs as root, so its files are not removable by the unprivileged user).
      The three stranded 9P mountpoints alongside it were cleared by the user on 2026-07-26.
      — *source: JOURNAL 2026-07-26 — E2E for the flight recorder*

- [ ] Ship caretaker flight records off a **kubernetes** pod. The host backends are covered (their
      caretaker scratch dirs are binds of directories under the server's data dir, so records land
      where the server can read them), but a pod caretaker writes to its own filesystem and the
      stream is lost with the pod. Fixing it properly means shipping records over the caretaker
      connection. — *source: JOURNAL 2026-07-26 — Server/caretaker activity log*
- [x] DONE (2026-07-26): `--follow` and an MCP surface over the activity endpoint. `cornus activity
      --follow`/`-f` streams SSE from `GET /.cornus/v1/activity?follow=1` (and tails the files
      directly under `--local`); MCP gets an `activity_read` tool plus a `cornus://activity/unfinished`
      resource. `--follow` with `--unfinished` is refused on both surfaces — unfinished is resolved
      over the whole stream, so it is a snapshot, not a feed.
      — *source: JOURNAL 2026-07-26 — following the flight record, and reading it as an agent*

- [ ] Verify the wait-for-mounts-to-unwind now applied to `stopServer`'s HOST-PROCESS path actually
      helps. It was added by the same reasoning as the container path (SIGKILL with a live 9P mount
      strands it; clearing needs root) and costs nothing when no mount is live, but could not be
      exercised on the dev host, where client-local mounts need CAP_SYS_ADMIN the user lacks. Confirm
      under `make e2e-container`, where everything runs as root.
      — *source: JOURNAL 2026-07-26 — terminal deploy-attach errors were being lost on the wire*
- [ ] Finish containerized **containerd**: require `--network host` (its CNI plumbing builds workload
      networks in whatever netns cornus occupies) and ship the CNI plugins in the image or a variant.
      Path translation and the staged log shim are done; the guide carries a warning block until
      these land. A CNI-presence preflight was deliberately skipped — it would be a third copy of
      `hostrun`'s plugin-dir resolution, and a missing plugin already errors loudly at deploy.
      — *source: JOURNAL 2026-07-26 — In-container server mode*
- [ ] Extend host/container path translation to `barehost` and `incushost`. Both share `hostrun`'s
      exposure (volume backings, managed `/etc/hosts`, netns paths); bare is currently exempt because
      its OCI runtime is cornus's own child, which holds only while that stays true.
      — *source: JOURNAL 2026-07-26 — In-container server mode*

- [ ] Implement detached `cornus compose exec`: plumb Docker's detach option through the dockerhost backend and define a safe Kubernetes lifecycle rather than returning from an attached SPDY stream. — *source: JOURNAL 2026-07-12 — compose exec*
- [ ] Enable GitHub Pages with GitHub Actions as the repository Pages source so the `docs.yml` deployment can publish the VitePress site. — *source: JOURNAL 2026-07-12 — VitePress user-reference docs site*
- [ ] Design client-to-caretaker trace unification at the Apply/relay boundary, using propagated
      context or span links without falsely parenting the pod-scoped persistent caretaker connection
      under one CLI invocation. — *source: JOURNAL 2026-07-12 — Client-side distributed tracing and filled tracing gaps*
- [x] Complete source-checked review of the remaining Japanese and Simplified Chinese pages for
      inline English residue, calqued phrases, terminology drift, and prohibited full-width colons or
      parentheses; resolve the Japanese audit warnings against the English source. — *source: JOURNAL 2026-07-12 — Consolidated Japanese translation audit and home-page translation cleanup*
- [x] Rebuild generated `docs/.vitepress/dist/` before publishing the current API-path, architecture,
      and locale-source changes; do not hand-edit generated assets. — *source: JOURNAL 2026-07-12 — Docs sweep and home-page translation cleanup*

- [ ] Add a `deploy`/`deploy_attach` E2E scenario that interleaves a non-local (named/bare-name)
      volume between two client-local binds in the raw `spec.Mounts` list, to guard the sparse-index
      `m2`-gap regression — the existing `compose-mounts-multi.star` does not exercise it (compose
      routes `type: volume` into `spec.Volumes`, never producing a sparse index). — *source: JOURNAL
      2026-07-13 — Multi-mount caretaker investigation*
- [ ] Verify the Helm chart's opt-in `tailscaled` sidecar (`tailscale:` values block) against a live
      cluster — validated so far only via `helm lint`/`helm template`; whether Funnel actually works
      over the shared control-socket `emptyDir` in userspace mode is unconfirmed. — *source: JOURNAL
      2026-07-14 — Tunnels/hub docs restructuring... Tailscale Helm sidecar*
- [x] ja/zh doc sync for the `cornus tunnel --forward-agent` ssh-agent-forwarding feature
      (`docs/guides/tunnels.md`'s ssh section, `docs/cli/tunnel.md`'s new flag row/example) — English
      only so far. — *source: JOURNAL 2026-07-14 — SSH-agent forwarding for the `cornus tunnel` ssh backend*
- [x] ja/zh doc sync for the whole caretaker-sidecar mount relay / remote companion / agent-forwarding
      arc (dockerhost/containerdhost remote mode, `cornus exec --forward-agent`, kubernetes
      `AgentForward`) — English only across all of it so far. — *source: JOURNAL 2026-07-14 —
      Caretaker-sidecar mount relay... / Always-on remote companion... / Kubernetes `AgentRelayRole`...*
- [x] Backfill two pre-existing ja/zh translation gaps found by the structural audit (both locales,
      identical in each): `architecture/deploy-engine.md` is missing the `## Workload lineage` section,
      and `reference/deploy-spec.md` is missing `### Origin` + `#### GitOrigin` and orders
      `### TelemetrySpec` differently from the source. Until then
      `audit_markdown_translation.py` reports two heading-level errors for those files. — *source:
      JOURNAL 2026-07-25 — Incus backend: user-facing documentation coverage*
- [x] Fix two pre-existing inaccuracies in `reference/deploy-spec.md` (+ ja/zh): the `credentials`
      rows claim "host backends via a companion caretaker" when host backends still reject them (see
      the item below), and the `egress` `mode` row lists `transparent` as "kubernetes now" when every
      host backend supports it. Both predate the companion work and were noticed while correcting the
      neighbouring pages. — *source: JOURNAL 2026-07-25 — unified companion caretaker*
- [ ] Client-sourced credentials on the host backends. Now that dockerhost/containerdhost/barehost
      are `AttachingBackend`s, `ApplyWithAttachments` is the natural home, but delivering a
      credential is a feature port rather than a merge: endpoint deliveries bind a loopback port and
      inject resolved env into the APP container, file deliveries need a scratch directory shared
      with it, and deploy-time env deliveries have no Secret indirection to hide behind the way they
      do on kubernetes (a host backend's container env is readable by anyone who can talk to the
      daemon). Currently rejected with a per-backend message. — *source: JOURNAL 2026-07-25 —
      unified companion caretaker*
- [ ] Audit the `cornus daemon docker` client print path for the session-local SOCKS5 conduit banner. — *source: JOURNAL 2026-07-10 — Implemented: reach a compose service by its short name + session-local SOCKS5 tunnels*
- [ ] Add an `up -d` E2E that drives shared and session-local SOCKS5 conduit coexistence through the background agent. — *source: JOURNAL 2026-07-10 — Implemented: reach a compose service by its short name + session-local SOCKS5 tunnels*
- [ ] Reconcile a same-name background-agent project when its incoming connection or conduit configuration changes; currently the first conduit silently remains active, including stale ingress controller/CA settings. Include the full ingress configuration in the identity or reconcile it separately, and preserve shared conduit refcounts. — *source: JOURNAL 2026-07-20 — Known limitation: background-agent conduit configuration is first-writer per project*
- [ ] Design safe same-host detection so Compose can use permitted direct server-side binds instead of 9P for local configs, secrets, and bind mounts on unprivileged dockerhost. — *source: JOURNAL 2026-07-11 — Compose-spec fidelity: E2E coverage + a deferred mount-realization gap*
- [~] Declarative client-side conduit/session reconcile engine (design note, JOURNAL 2026-07-10).
      Replace the imperative "deploy + hold client resources" lifecycle — open-coded ~4 times
      (`runForeground`, agent `Project.StartService`, `DeployCmd.startConduit`, `pkg/dockerproxy`
      `Proxy.start`/`session`), plus `Socks5Cmd.Run` — with a single `apply(ProjectSpec)` +
      level-triggered `mountController` / `exposureController` shared by foreground and the agent.
      PARTIAL (2026-07-10, incremental steps 2+3 + most of 4): the agent's `Project` is now the
      reconcile engine — `mountController` + `exposureController` (`clientagent/controllers.go`)
      driven by `Project.Apply`/`Remove` over a desired map (`project.go`); `agent.go doUp` calls
      `Apply`. Per-dimension fingerprints (a ForwardPorts toggle keeps the 9P mount), request-order
      reconcile, and the alias-gap-gone-by-construction property, all regression-tested + race-clean.
      Step 4 (part): `runForeground` (composecli) now drives the SAME `Project` engine in-process
      (foreground == agent), deleting the open-coded mounted-session machinery; an operation ctx
      threaded through `Apply`/`ensure` preserves Ctrl-C pre-ready cancellation. See JOURNAL
      2026-07-10 "Implemented: declarative client-side reconcile engine (agent path...)" and "Step 4
      (part): foreground `up` now runs the same reconcile engine as the agent".
      Step 4 (dockerproxy): RESOLVED as a deliberate exception, NOT by applying the reconcile. The
      reconcile is a declarative->imperative adapter; docker's API is already imperative (edge-triggered
      create/start/stop/rm, immutable containers), so `Project` does not fit and is NOT applied. Instead
      extracted the shared imperative primitive both sides use — the per-workload deploy-attach hold —
      into new `pkg/attachsession` (`Open`/`WaitReady`/`Done`/`Stop`/`Context`/`Status`); `dockerproxy`'s
      `session` and the engine's `mountController` both build on it (dockerproxy keeps its own
      containerRecord state machine + verbs). Documented in ARCHITECTURE + proxy.go/project.go doc
      comments. See JOURNAL 2026-07-10 "dockerproxy: shared deploy-attach primitive (not the reconcile)".
      REMAINING (step 4 tail, lower value / higher risk): the single `cornus deploy` attach path
      (`runRemote`/`startConduit`) surfaces rich per-instance status from the DeployAttach event
      stream the engine doesn't expose (little dedup for real event plumbing); `Socks5Cmd.Run`
      holds zero services so the engine adds nothing. A PRE-EXISTING (unrelated) `-race` data
      race in composecli `TestStreamLogsFollowStopsOnCancel` (test polled its `bytes.Buffer` while
      `streamLogs` wrote) surfaced when running `-race` on the package and is now FIXED (mutex-guarded
      `syncBuffer` test helper); `cmd/cornus/...` is `-race`-clean.
      Guardrails + incremental path in the 2026-07-10 design note.
      (The mounted-alias fix is now E2E-covered by `socks5-mount.star` — kube-only, so it runs in CI
      but was not executed live during authoring.)

- [ ] Preflight node-side image-pull probe (deferred; plan *et-bright-dragon*). *source: Findings from
      the unhappy-path audit (2026-07-07).* The E2E preflight cannot yet confirm a cluster node can
      actually pull the pushed image; add a node-side pull probe so a bad registry-host/RBAC config
      fails fast instead of at deploy `wait`.

- [ ] In-cluster-server E2E target. *source: Auto-detect the in-cluster cornus Service (2026-07-07).*
      The harness runs cornus host-side, so it has no self Service to introspect; an in-cluster-server
      target is needed to E2E-cover the auto-advertise-from-Service path (NodePort/LB) and a full
      in-cluster deploy that pulls with no port-forward. The Service-introspection logic is only
      unit-tested (`advertise_test.go`) today.

- [ ] After committing the cosign fix, re-tag the release. *source: CI green-up (Release + E2E kube),
      2026-07-08.* A re-run of a failed `v0.0.0` Release uses the workflow at the tagged commit and
      won't pick up the fix; move/re-push `v0.0.0` or cut a new tag once the fix is committed.

- [ ] Docker `wait` reports StatusCode 0 regardless of the container's real exit code
      (`pkg/dockerproxy/containers.go` wait; audit 2026-07-09, LOW). The real exit code is not
      available in-package: neither `deploywire.Event` nor `api.InstanceStatus` carries it, and
      `session.done` only signals attach end. A KNOWN LIMITATION comment was added at `wait()`; a
      true fix must thread an exit code through the DeployAttach event stream + session across
      `pkg/deploywire`, `pkg/api`, and `pkg/server`. Deferred from the low-severity sweep as the only
      cross-package finding.

- [~] `gs://` (GCS) / `azblob://` (Azure) storage backends. FINDING + FIX (2026-07-05): they were NOT
      merely untested — the gocloud drivers were never blank-imported, so `Open` failed with "no driver
      registered" (non-functional). Now wired behind a `cloudblob` build tag (`drivers_cloud.go`; the
      Google/Azure SDKs stay out of the default lean binary), with a clear unsupported-scheme error in the
      default build (`drivers_nocloud.go` + `open.go`), gated round-trip tests (`cloudblob_test.go`,
      `CORNUS_TEST_GCS`/`CORNUS_TEST_AZBLOB`, self-skip), and a CI `go build -tags cloudblob` step so
      the path can't rot. Round-trips RUN + PASSED (2026-07-07): both gated tests pass against local
      emulators with ZERO code changes — fake-gcs-server (gocloud honors `STORAGE_EMULATOR_HOST`) and
      Azurite (`--skipApiVersionCheck` needed for the SDK's 2026-06-06 API version — emulator quirk,
      not a cornus bug; `AZURE_STORAGE_ACCOUNT/KEY/DOMAIN/PROTOCOL/IS_LOCAL_EMULATOR` envs). Exact
      repro commands documented in TESTING.md. `serve(storage=...)` E2E DONE (2026-07-07 wave 5):
      `registry-gcs.star` + `registry-azblob.star` (registry-s3.star pattern, env-gated self-skip)
      PASSED LIVE against the emulators through the full registry HTTP surface; `make e2e-cloudblob`
      builds the tag-gated `cornus-cloudblob` binary and runs both. STILL OPEN: a real-cloud
      (non-emulator) run has never happened — needs actual GCS/Azure credentials.
- [x] User-networks (remaining). NOTE (2026-07-05): the user-network machinery is VALIDATED in dind here
      (deploy-network + deploy-multus + the new ftp-usernet all pass under `E2E_TARGETS=kube E2E_MULTUS=1`
      in the e2e container) — my earlier "needs a live cluster, not runnable here" was wrong (privileged
      docker via `sg docker` + the pre-baked kind/Multus dind image). (a) [DONE 2026-07-07 sweep,
      wave 5 — matrix row A' SHIPPED + VALIDATED LIVE: plan-time deterministic IP allocator
      (`pkg/compose/usernet.go`, sha256-of-resource-name onto the subnet host range, salted-probe
      collision handling, `ipv4_address` compose field as explicit override, dynamic fallback for
      replicas>1/host-local); NAD renders `static` IPAM + ips capability and the annotation upgrades
      to Multus JSON selection form with pinned IPs; caretaker DNS OVERLAID mode serves peer
      SECONDARY IPs via `api.DNSSpec.RequireUserNet` (gracefully degrades to services DNS on
      non-Multus clusters); pinned specs use Recreate strategy; `static` CNI plugin staged in the
      runner. deploy-multus.star + ftp-usernet.star + deploy-network.star ALL PASSED under
      `E2E_TARGETS=kube E2E_MULTUS=1` on a real Multus kind cluster in dind, including the
      data-path assert that named traffic rides the user bridge.] (b) PARTIAL — ipvlan DONE
      (2026-07-07 wave 6: `deploy-multus-ipvlan.star`, triple-gated on kube + `E2E_MULTUS_IPVLAN=1`
      + the CRD, PASSED LIVE in kind-in-dind on parent eth0: ipvlan NAD with static IPAM, pinned
      secondary IPs live on net1, caretaker DNS answering them, named traffic riding the ipvlan
      network, NAD GC; one-command rerun via
      `make e2e-container E2E_TARGETS=kube E2E_MULTUS=1 E2E_MULTUS_IPVLAN=1`). macvlan DONE
      (2026-07-07 wave 7: `deploy-multus-macvlan.star`, gated on `E2E_MULTUS_MACVLAN=1`, PASSED
      LIVE in kind-in-dind — macvlan NAD on parent eth0 with static IPAM, pinned IPs on net1, DNS
      answering secondary IPs, named pod-to-pod traffic riding the macvlan network (slave-to-parent
      is impossible by kernel semantics — asserts are pod-to-pod only), NAD GC; single-node kind so
      bridge-mode switching stays in-driver — cross-node macvlan remains environment-sensitive,
      hence the dedicated gate). Detached-mode row D DONE (2026-07-07 wave 7:
      `deploy-multus-detached.star`, gated `E2E_MULTUS_DETACHED=1`, driven via
      `cornus deploy --detach` with `networks[].default: true`, PASSED LIVE in kind-in-dind — the
      user network IS the pod's primary interface, host-local IPAM on the derived subnet, name-only
      annotation, no net1/caretaker, direct-IP data path, NAD GC on last delete). The row flushed
      out and we fixed TWO real bugs: (1) `pkg/client.New` did not normalize ws://wss:// bases for
      plain HTTP calls, so the new `--detach` POST failed against WS-spelled endpoints; (2) the
      `default-network` annotation was emitted unqualified, but Multus resolves an unqualified
      reference in ITS default namespace (kube-system), not the pod's — now namespace-qualified
      (`<ns>/<nad>`, `Attachment.Namespace` threaded through the netdriver Engine). This item —
      the whole user-networks validation matrix (bridge/ipvlan/macvlan overlaid + detached) — is
      now CLOSED except cross-node macvlan (environment-sensitive, gated, no plan to validate in
      dind) — *source: approved plan 2026-07-03*
      **CLOSED (2026-07-27 sweep): the entry self-declares the validation matrix complete (bridge/ipvlan/macvlan overlaid + detached, all PASSED LIVE) except cross-node macvlan, which is a deliberate won't-do (environment-sensitive, gated, no plan to validate in dind). Retained here only as history.**
- [ ] Hub network overlay. Landed 2026-07-04: Phases 0-2, connection
      unification (`/.cornus/v1/caretaker/attach`), Phase 3 (synthetic-IP discovery + DNS + k8s `injectHub`),
      Phase 4 (reach + register policy, `CORNUS_HUB_POLICY` / `CORNUS_HUB_REGISTER_POLICY`), mTLS
      (cert-authoritative identity), the UNIFIED k8s sidecar (mounts+hub → one caretaker; proxy+hub
      rejected), the catalog (`GET /.cornus/v1/hub/catalog` + `Store.Catalog`), the cross-network spoke CLI
      (`cornus hub`), the `hub.Store` seam, and a kind scenario (`deploy-hub.star`, syntax-checked).
      REMAINING (infra-dependent): (1) [DONE 2026-07-05: multi-replica hub SHIPPED + VALIDATED —
      `hub.RedisStore` + `kubehub.KubeStore` (`CORNUS_HUB_STORE=kube`) with cross-replica delivery
      forwarding via `/.cornus/v1/hub/forward`; proven against real Redis + two real replicas and a real kind
      cluster in dind. Remaining sub-items are tracked as separate open items below]; (2) [DONE 2026-07-05: UDP support shipped + VALIDATED in dind — framed datagrams over the byte-agnostic relay, per-source reach flows; deploy-hub-udp.star passes on a real kind cluster]; (3) [DONE 2026-07-07 wave 5:
      reactive catalog push + dynamic caretaker rebind — `Registration.Watch` capability flag, server
      pushes `CatalogUpdate` frames over the existing control stream (kick on local register/disconnect
      + 3s hash-compare poll for cross-replica Redis/Kube convergence; poll goroutine exists only while
      watchers are subscribed), caretaker `HubRole.ReachDynamic` binds/unbinds synthetic-IP listeners
      with drain-not-kill semantics; old peers unaffected (unknown field ignored / no Watch = no
      frames)];
      (4) cert issuance/rotation wiring (mTLS mechanism exists; provisioning is ops/PKI); (5) [DONE 2026-07-05: `deploy-hub.star`
      RUN + PASSED on a real kind cluster in dind — exporter/importer register + reach "greeter" through the
      hub end to end; now registered in the Makefile SCENARIOS list]. Also unrelated hub-0 carry-overs: per-mount trace
      linking DONE (2026-07-07 wave 7: `cornus.mount.relay` span per relayed mount stream at the
      caretaker-facing edge — session digest (never the raw capability), mount name, transport
      local|forwarded, rx/tx bytes, error status — parented to the attach connection's otelhttp
      span which already links to the caretaker's `caretaker.conn` span; caretaker side stamps
      rx/tx on its existing `caretaker.mount` span; zero-cost when off (`span.IsRecording()` gate,
      original conn returned untouched); cross-replica linking landed too — `dialForward` now takes
      ctx and injects the W3C traceparent, so the owner replica's `/.cornus/v1/mount/forward` span links);
      version-skew fallback CLOSED
      AS MOOT (2026-07-07: the old endpoints were removed, both sides ship in one binary, and the
      new protocol additions since — catalog-push Watch, UDP port-forward ack, compose daemon
      Protocol stamp — each carry their own explicit skew story)
      — *source: JOURNAL 2026-07-04*.
- [ ] Embedded-gossip hub Store backend (deferred third option alongside Redis/KubeStore) — *source:
      JOURNAL 2026-07-05 — Multi-replica hub PoC (Redis) SHIPPED + VALIDATED*
- [ ] GHCR release follow-ups, blocked on repo creation: push the repo; adjust the hardcoded
      `ghcr.io/moriyoshi/cornus` defaults (Helm values, `deploy/k8s/cornus.yaml`, README) if the repo
      lands under an org (the workflow derives the name from `github.repository_owner`); tag `v0.1.0`
      so the pinned manifest ref and chart appVersion resolve; make the GHCR package public — *source:
      JOURNAL 2026-07-05 — Pre-built GHCR images for k8s installs*
- [ ] CI watch items: pin an explicit Helm `version:` in `ci.yml` if `azure/setup-helm@v5` ever fails
      its GitHub-API latest-version lookup; confirm the first Dependabot github-actions run is a no-op
      (everything is already at latest) — *source: JOURNAL 2026-07-06 — CI workflow hardening*

## Whole-codebase adversarial audit (2026-07-09, retired AUDIT_2026-07.md)

The standalone `AUDIT_2026-07.md` (finding-by-finding report of 73 confirmed findings) was RETIRED
on 2026-07-21 and consolidated here: the audit is fully resolved, so it carried no open work. Its
reusable method, highlights, and outcome live in the LTM synthesis
`.agents/docs/LTM/codebase-audit-2026-07.md` (indexed in `LTM/INDEX.md`); the finding-by-finding
detail remains recoverable via git history plus the landed fixes and their regression tests.

Method: 40 disjoint review slices over every non-test Go file in `pkg/`/`cmd/`, one high-effort
adversarial reviewer per slice, then two independent skeptic verifiers (a correctness lens and a
reachability lens) per finding -- confirmed only if BOTH agreed it was real and reachable against
the actual code. 85 raw findings distilled to 73 confirmed (14 high, 27 medium, 32 low; 12
rejected). 210 agents, ~6.3M tokens.

Resolution: all 41 high+medium findings fixed and 31 of 32 low fixed (most with a new unit
regression test; the 6 reachable only across a live daemon are covered by E2E scenarios). Module-wide
gate green after both passes (`gofmt -l` clean, `go build/vet/test ./...`, `make e2e-check`).

- The ONE deferred finding (docker `wait` always reports `StatusCode` 0 -- a cross-package change)
  is already tracked as its own open item above (see "Docker `wait` reports StatusCode 0 regardless
  of the container's real exit code"); not duplicated here.

## Production-hardening gaps (originally from GAP_ASSESSMENT.md, 2026-07-04)

The standalone `GAP_ASSESSMENT.md` was RETIRED on 2026-07-09 after a per-sub-claim re-verification
against current code: 6 of its 7 areas are materially closed and the 7th (rollback) was descoped,
so the doc was substantially stale and its live status is fully carried here. Most items were
tackled in the 2026-07-04 sweep (see JOURNAL). Remaining sub-items are noted inline; the three
low-severity residuals the re-verification found still open are listed first.

- [~] **No disk-usage reporting / quota surface** (residual from GAP §2). Reporting DONE
      (2026-07-21): new `storage.Backend.Usage` (lists `blobs/sha256/` + Stats each blob → count +
      total bytes; skips a blob deleted between List and Stat so it never fails alongside a
      concurrent GC; documented as O(blob count) — cheap on filesystem, expensive on S3) surfaced by a
      non-destructive `GET /.cornus/v1/storage` (the read-only counterpart to the destructive `POST
      /.cornus/v1/gc`): returns `{casBlobs, casBytes}` plus `{fileCacheBytes, fileCacheFiles}` when the
      block cache is enabled; zero CAS in pure re-export mode (nil store); authn-governed, no policy
      action, never mutates. Consumed end-to-end (2026-07-21): shared `api.StorageUsage` type,
      `client.StorageUsage`, and a `cornus storage usage` CLI command (`--format text|json`,
      `humanBytes` renderer). Tests: `storage.TestUsage`, `server.TestStorageUsageEndpoint` /
      `…MethodNotAllowed`, `cmd/cornus TestHumanBytes`. STILL OPEN: quota ENFORCEMENT — a separate
      policy decision (what to do when a ceiling is hit: reject which pushes? evict? warn only?),
      deferred as a design item, not autonomous. Low severity.
- [~] **Auth + TLS** — PARTIAL. DONE: TLS serving flags (`--tls-cert`/`--tls-key` + `CORNUS_TLS_*`)
      → `ServeTLS`; fail-open on malformed `CORNUS_HUB_POLICY`/`_REGISTER_POLICY` fixed (now a hard
      startup error); dockerhost **default-deny privilege policy** (`pkg/deploy/dockerhost/
      policy.go`) — rejects `Privileged` and host bind sources unless opted in via
      `CORNUS_ALLOW_PRIVILEGED` / `CORNUS_ALLOW_BIND_SOURCES`; deploy-attach `MountsDir` always
      permitted; local CLI stays permissive; k8s **also** default-denies `Privileged` (parity gate,
      same `CORNUS_ALLOW_PRIVILEGED` env; Cornus's own injected sidecars unaffected). Trust-boundary
      section in README + architecture auth section.
      **Step 2 (opt-in bearer auth) DONE (2026-07-04):** `pkg/server/auth.go` — a pluggable,
      off-by-default authenticator (pure pass-through when unconfigured) verifying an opaque static
      token (`CORNUS_AUTH_TOKEN`) and/or JWT (HS256 `CORNUS_JWT_HS256_SECRET`, RS256/ES256
      `CORNUS_JWT_PUBLIC_KEY`; optional `iss`/`aud`), algorithm-confusion-safe, protecting `/.cornus/v1/*`
      and `/v2/*` (health/readyz open; `CORNUS_REGISTRY_ANONYMOUS_PULL` opts GET/HEAD pull open); 401 +
      OCI `WWW-Authenticate`. Clients send `CORNUS_TOKEN` on `/.cornus/v1/*` HTTP + WS-attach handshakes;
      `cornus push` sends it as a crane bearer.
      Caretaker→server auth DONE (2026-07-04): `caretaker.Config.Token` carries a bearer token stamped
      onto server-bound sidecars (mount/hub, not dns/proxy) by the k8s backend's `caretakerConfigEnv`
      helper; the caretaker sets `Authorization: Bearer` on its `/.cornus/v1/caretaker/attach` handshake.
      Credential SPLIT DONE (2026-07-05): the caretaker uses a SCOPED `CORNUS_CARETAKER_TOKEN` that the
      server accepts ONLY on `/.cornus/v1/caretaker/attach` and rejects on the client API + registry
      (`authenticate(r, caretakerScope)`); full creds still work everywhere. So a sidecar credential
      leaked from a pod spec cannot deploy/build/exec/push. The k8s backend injects
      `CORNUS_CARETAKER_TOKEN` (no longer `CORNUS_AUTH_TOKEN`).
      Caretaker token via Secret DONE (2026-07-05): `CORNUS_CARETAKER_TOKEN_SECRET` ("name"/"name/key")
      sources the sidecar token from a k8s Secret via `secretKeyRef` (no pod-spec literal); the caretaker
      reads `CORNUS_TOKEN` at runtime (`applyEnvToken`, precedence over embedded `Config.Token`).
      In-process issuer + JWT scopes DONE (2026-07-05): `pkg/authtoken` (shared Claims + `scope` +
      `Issue`, used by `cornus token issue` AND the server verifier); scope `caretaker` restricts a JWT
      to `/.cornus/v1/caretaker/attach`, empty/`api` = full. Unlocks JWT-only k8s (a caretaker-scoped JWT in the
      sidecar Secret, no static token needed). HS256 or PEM private key (RS256/ES256) signing.
      Step 3 (mTLS identity + per-identity API authz) DONE (2026-07-05): `--tls-client-ca` /
      `CORNUS_TLS_CLIENT_CA` makes a verified client cert a full credential (identity = CommonName,
      `VerifyClientCertIfGiven` so probes/bearer still work); `Identity(r)` unifies mTLS CN + JWT `sub`.
      `CORNUS_API_POLICY` (JSON identity → actions, `*` = all; configure-to-enforce, empty identity
      denied) gates `deploy` (POST/DELETE + start/stop/restart/exec/attach/archive-write) and `build`
      with 403; reads stay open.
      Refinements DONE (2026-07-05): (a) HUB identity fold — `handleCaretakerUnified` declares
      `Identity(r)` (JWT `sub` or mTLS CN via the auth middleware; falls back to a direct verified-cert
      read for TLS-layer mTLS) as the authoritative hub identity, overriding the self-declared one so
      reach/register policy keys on an unforgeable credential (tested for both mTLS and JWT). (b) `/v2`
      registry PUSH authz — `registryAuthz` middleware gates registry writes (PUT/POST/PATCH/DELETE) on
      the `push` action; pull stays authn-governed (no conflict with anonymous pull).
      JWKS/rotation DONE (2026-07-05): `CORNUS_JWT_JWKS_FILE` (hot-reloaded on mtime) /
      `CORNUS_JWT_JWKS_URL` (cached with TTL, rate-limited refetch on unknown `kid`) verify JWTs by the
      token's `kid` (`jwks.go`), asymmetric-only (no HMAC confusion). `cornus token issue --kid` stamps
      the header; `pkg/authtoken.IssueOptions.KeyID` added.
      Final refinements (c)(d)(f) DONE (2026-07-07 sweep): (c) opt-in per-identity registry PULL
      authz — enforced only when some `CORNUS_API_POLICY` rule explicitly mentions the `pull` action
      (wildcard `*` does not count as mentioning), so existing policies can't lock out pulls;
      explicit pull policy wins over `CORNUS_REGISTRY_ANONYMOUS_PULL` (startup warning when both);
      also fixed anonymous-pull short-circuiting authentication so credentialed pulls now carry
      identity. (d) docker-login support — HTTP Basic on `/v2/*` with the token/JWT as the password
      (`docker login -u token -p $CORNUS_TOKEN`); registry 401 challenge is now `Basic
      realm="cornus"` (safe: crane clients send Bearer regardless); caretaker scoping preserved.
      (f) `exec` as its own API action — allowed iff policy allows `exec` OR `deploy` (deploy
      implies exec; enables exec-only identities), gated at every entry point incl. WS start/resize
      via leaked exec ids; BONUS fix: the deploy-attach WebSocket was previously ungated entirely —
      now gated on `deploy`. This item is CLOSED.
- [~] **Streaming failure surfacing** — PARTIAL. The `BUILD FAILED:`-trailer-after-200 convention is
      now documented in-code. Logs/Stats improved 2026-07-07 (lazy-header write: a backend error
      BEFORE the first output byte now returns a real 4xx/5xx instead of an empty 200, on both
      `/.cornus/v1/*` and the dockerproxy). Archive covered too (2026-07-07 sweep, second pass): archive
      GET uses the same lazy-header write (stat-header withdrawn on pre-byte error), StatPath and
      PUT errors classified (404/501 instead of blanket 500), `fs.ErrNotExist` → 404 for
      containerd's raw Lstat errors. Trailer convention DONE (2026-07-07 wave 5) — this item is
      CLOSED: mid-stream errors on logs/stats/archive-GET now ride the `X-Cornus-Stream-Error`
      HTTP trailer (`api.StreamErrorTrailer`, declared with the lazy 200, sanitized + capped);
      `pkg/client` Logs/Stats/CopyFrom check it after body EOF and return "stream error after
      partial output: ..." while still delivering partial bytes. dockerproxy is EXCLUDED by
      design: the docker CLI ignores HTTP trailers, so there is no consumer on that side.
- [~] **Remote-cluster connection ergonomics** — PARTIAL (2026-07-05). Phase 1 DONE: connection
      profiles (`pkg/clientconfig`, kubeconfig-style, XDG/cross-platform path), client-side TLS
      (custom CA / mTLS) through REST + all WebSocket dials, `--context`/`--config-file` global flags,
      `cornus config` command, and a `resolveConn`/`requireConn` resolver wired into deploy/exec/
      port-forward. Phase 2 DONE: automatic port-forward to the in-cluster cornus Service via embedded
      client-go SPDY (`pkg/svcforward`: kubeconfig load honoring `pf-kube-context`, Service->ready
      pod/targetPort resolution via Endpoints, `portforward.NewOnAddresses` to a local ephemeral port);
      `resolveConn` starts it for a pf-only profile, sets `http://<local>`, and tears it down via
      `cn.Cleanup`. Phase 2.5 DONE: share kube credentials with cornus by minting an audience-scoped
      ServiceAccount token via the TokenRequest API (`pkg/kubeauth` + `pkg/kubeclient`; `kube-auth`
      profile block + `--kube-auth-*` flags; token precedence CORNUS_TOKEN > kube-auth > static). Server
      needs no code change — validates via existing JWKS/audience env. See JOURNAL "Connection
      profiles...", "Automatic port-forward...", "Sharing kube credentials...". Resolver adoption DONE
      for `compose`/`daemon`/`build` (moved the resolver into `cmd/cornus/internal/clientconn`, kong-bound
      so the compose subpackage can share it; `Conn` now exposes Token/TLS so build honors profile
      CA/mTLS). See JOURNAL "Uniform resolver adoption...". STILL OPEN:
      (a) **Phase 3** — OAuth2 device authorization grant login (deferred by decision): server
      advertises an external OIDC IdP via discovery / a `WWW-Authenticate` extension, `cornus login`
      runs RFC 8628, resulting JWT validated by the existing JWKS path. (b) DONE — kube-target e2e for the
      SPDY forward + kube-auth TokenRequest (`incluster-portforward.star` + `incluster-kubeauth.star`,
      with in-cluster cornus manifests + `cornus()` `env=`/`expect_fail=` harness kwargs) is written AND
      has now passed LIVE on a real kind cluster (incl. the kube-auth JWKS chain and an
      unauthenticated-rejection negative control). See JOURNAL "In-cluster E2E — live kube run PASSED".
      `connection-profile.star` passes live on docker and kube.
      (c) DONE (2026-07-07 sweep) — `cornus hub` now resolves via the shared clientconn resolver
      (profiles, token precedence, pf-only profiles, explicit `--server` wins; `--server` no longer
      required). Deliberate guard: a profile carrying client-TLS material is REFUSED with a clear
      error because `caretaker.Config` has no TLS field and the hub WS dial uses the non-TLS
      `wire.DialControlHeader` — see the new "hub WS dial TLS plumbing" item below.
- [ ] containerd backend follow-ups (from the native containerd support work, JOURNAL 2026-07-07):
      (a) [DONE 2026-07-07 sweep: nerdctl-style hosts-file sync — per-instance hosts file under
      `<DataDir>/containerd/hosts/` bind-mounted at `/etc/hosts`, cornus-managed marker block
      (user edits outside survive), synced on Apply/Delete/repair from container labels
      (`cornus.netips`/`cornus.aliases`, restart-safe with no extra state file), names + aliases
      point at replica 0's IP, hostname = instance ID (`oci.WithHostname`), in-place single-write
      block updates (rename would detach the live bind mount). Aliases dropped from the
      unsupported-features warning; driver/driver-opts still warned.]
      (b) [DONE 2026-07-07 tackle-todos sweep: size-capped rotation on cornus-driven (re)starts (the
      only point where no shim holds the log fd) — rename to `<name>.log.1`, one old generation,
      default 16 MiB, `CORNUS_CONTAINERD_LOG_MAX_BYTES` override; reader concatenates `.1` + live for
      backlog/tail and resets its follow offset when the live file shrinks. Residual: within one
      uninterrupted run (incl. restart-monitor resurrections) the live file can still grow past the
      cap.]
      (c) [DONE 2026-07-07 sweep: `ensureReconciled` — one-shot (mutex + retry-on-enumeration-
      failure) reconcile kicked in `New()` and lazily from all backend entry points; nsfs-liveness
      check (`statfs`) detects both missing and dead pins, repairs via the same `repairNetns` path
      `Start` uses (netns + CNI + re-pin + label rewrite), does NOT start tasks (the restart monitor
      resurrects once the netns is live); skips restart=no and explicitly-stopped records;
      `repaired=N skipped=M` summary logged.]
      (d) [DONE 2026-07-07 sweep, wave 6: `lifecycle-restart.star` (boot-count via bind-mounted
      boot log; PID 1 = sh with a TERM trap) PASSED LIVE on both docker AND containerd-in-dind —
      the restart monitor resurrected the workload after `exec kill 1` (fresh boot recorded,
      running again) and an explicit stop stuck past a monitor interval (no resurrection, boot
      count unchanged). Registered in SCENARIOS + SCENARIOS_CONTAINERD + the entrypoint subset.]
      (e) [DONE 2026-07-07 sweep, wave 5-6: the dind e2e-container containerd leg now runs FULLY
      GREEN (deploy/lifecycle/exec/compose all pass). The first-ever live run flushed out four real
      bugs, all fixed: (1) docker-style short image names were passed unnormalized to containerd's
      resolver ("dummy://nginx:1.27-alpine" parse error) — now normalized via
      `reference.ParseDockerRef` at the single pull choke point; (2) the custom resolver built by
      `ConfigureDefaultRegistries` had NO Authorizer, so public-registry anonymous pulls died with
      a bare 401 — `docker.NewDockerAuthorizer()` added (anonymous bearer-token flow); (3) the
      overlay snapshotter cannot stack on an overlay-backed root (dind) — new
      `CORNUS_CONTAINERD_SNAPSHOTTER` knob threaded through pull/unpack/create/volume-seed, with
      /proc/mounts-based auto-detection in the runner entrypoint (busybox stat reports overlayfs as
      UNKNOWN); (4) an exec TTY resize arriving before process start was silently dropped (the
      initial window size always races start) — now buffered in the session and applied at start.
      A root-host (non-dind) `make e2e-containerd` run remains unexercised but the dind leg covers
      the same path; CI leg being added.]

## Compose CLI fidelity triage (2026-07-11)

Items from triaging the `cornus compose` CLI surface against the Docker Compose CLI
reference; see the JOURNAL entry "Compose CLI fidelity triage (2026-07-11)" for the
full categorized tables (Tier A bugs / B missing flags / C missing subcommands /
D intentional extensions). *source: compose CLI fidelity triage 2026-07-11.*

- [ ] logs `--follow` has no `-f` short (the group reserves `-f` for `--file`,
      logs.go:51-52); allow `-f` on logs or document the conflict (Tier A3, Med).
      DEFERRED (2026-07-11): the group owns `-f/--file`, which every subcommand
      inherits, so a `logs -f/--follow` short collides under kong. Left as a
      documented divergence (use `--follow`); revisit only if the group short is
      reworked.
- [ ] up `--no-attach` is a bool, but compose's is a stringArray of services;
      reconcile the semantic mismatch (Tier A2, Low). DEFERRED (risky semantics
      change, low value).
- [~] Lower-severity missing flags catalogued in the JOURNAL entry (Tier B, Low).
      PARTIAL (2026-07-11): DONE `build --no-cache` + `--build-arg` (thread
      NoCache/BuildArgs into the build request; parseBuildArgs supports KEY=VALUE
      and bare-KEY-from-env) and `logs --until` (api.LogOptions.Until through server
      + dockerhost/containerd; kubernetes warns as unsupported). DONE (2026-07-21):
      `up`/`down --remove-orphans` — orphan detection by workload lineage
      (Origin.Project stamped on every compose deploy) minus the full-project
      resource-name set; `up` warns and `--remove-orphans` removes on both up and
      down (findOrphans/removeOrphans/warnOrphans in composecli, unit-tested in
      orphans_test.go). STILL OPEN: up (--no-deps / --force-recreate / -t), down
      (--rmi / -t), logs (--no-color / --index), build
      (--pull / --push / -q), restart + stop (-t) — mostly no-ops on the
      deploy-to-server model or needing backend work.
- [ ] ps default columns differ from `docker compose ps` (SERVICE|NAME|IMAGE|STATUS
      vs NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS); document or revisit once
      `--format` lands (Tier A4, Low). PARTIAL (2026-07-11 sweep): `ps --format json`
      now exists for scripting; the default *table* column-set divergence is still
      open and left as a deliberate display choice.
- [ ] E2E follow-ups from the compose flag batch (2026-07-11). Live docker E2E
      added + passing for profiles, depends_on gating, and `down --volumes`
      (`compose-profiles.star`, `compose-dependson.star`, `compose-down-volumes.star`).
      STILL OPEN: (a) `build --no-cache` / `--build-arg` E2E is CI-only — the
      in-process build engine needs a rootless-userns / privileged / dind stack, so
      it cannot run in the plain docker sandbox (unit-tested meanwhile); (b)
      `down --volumes` volume-removal is E2E-covered on the docker target only — the
      kubernetes (PVC delete) and containerd (dir removal) `RemoveVolume` paths are
      unit-tested but not yet E2E-covered; (c) `--env-file` and `logs --until` are
      unit-tested only (no E2E).

## Client-side egress follow-ups (2026-07-11)

Core feature (Modes 1/2/3 on kubernetes) is DONE + unit-tested (see JOURNAL "Client-side egress
(2026-07-11)"). Remaining:

- [~] **E2E scenarios** — WRITTEN + resolve-checked (`make e2e-check` green, harness Check-all test
      passes). The proxy and transparent scenarios are LIVE-VERIFIED on real kube through the
      containerized dind/kind runner. Three added to Makefile SCENARIOS:
      `compose-egress-env.star` (Mode 1: assert HTTP_PROXY/NO_PROXY injected, cluster-rule folded into
      NO_PROXY; cross-target via `cornus exec`), `deploy-egress-proxy.star` and
      `deploy-egress-transparent.star` (kube-only: a three-app default-route DIFFERENTIAL — `cluster`
      reaches the in-cluster `web`, `client` cannot (relayed to the out-of-cluster harness → proves the
      traffic left the cluster), `deny` dropped; the proxy scenario also asserts `HTTP_PROXY` points at
      the caretaker). REMAINING: a dedicated PAC-`script` scenario; a
      positive client-reach proof would need a harness builtin to host a client-side listener (none
      exists today — see the explorer note in the E2E harness).
      NOTE (correctness fix found while writing E2E): the compose foreground AND agent paths deployed
      mount-free services fire-and-forget via a stateless POST, which bypasses the deploy-attach
      session — so a Mode 2/3 egress service would have had no session (relay dead) and the stateless
      Apply doesn't inject the caretaker. FIXED: both paths now hold a session when
      `spec.Egress.NeedsRelay()` (new `api.EgressSpec.NeedsRelay`), like a client-local mount.

## SSH-tunnel connection profiles (2026-07-16) — follow-ups

- [ ] **Higher-level, per-surface stream auto-resume.** The transport reconnection cannot resume an
      in-flight stream (`logs -f`, exec/attach) — the yamux/exec/pty state is lost on a drop. Making
      e.g. `logs -f` re-attach from the last offset must live at that command's layer, not the dialer.
- [ ] **ssh_config fidelity beyond common keywords + ProxyJump.** `Match` blocks and `ProxyCommand`
      are honored only via the `--ssh-use-binary` fallback (auto for ProxyCommand); kevinburke/ssh_config
      supports neither `Match` nor token expansion. Auto-detecting that a `Match` block *applies* (so
      the fallback is chosen without the flag) and the Windows `ProxyCommand`/`Match` story (the
      unix-socket fallback is Linux/macOS only) are open.

- [x] zh/ja doc sync for `cornus web --publish-in-conduit` (the "One browser proxy setting" section
      and the new flag rows in `docs/cli/web.md`) and for the `cornus socks5 --allow-non-loopback`
      flag + "Loopback only, by default" section in `docs/cli/socks5.md`. English only so far.
      — *source: JOURNAL 2026-07-18 — Serve `cornus web` through the SOCKS5 conduit*
- [ ] Add a `web.star` (or `agent.star`) E2E leg for `cornus web --publish-in-conduit`: publish into
      the shared conduit, `http_get(url="http://cornus.internal/.cornus/web/config", socks5=<addr>)`
      (the builtin already exists), and assert the name is withdrawn after the client exits. No new
      builtins needed. — *source: JOURNAL 2026-07-18 — Serve `cornus web` through the SOCKS5 conduit*

## `cornus setup` wizard (2026-07-16) — follow-ups

- [x] **ja/zh translations of `docs/cli/setup.md`** via the `translate-documents` skill (only the `en`
      page exists; the nav entry is registered for all three locales).
- [ ] **ssh_config Host-alias picker** in the setup SSH sub-flow (offer aliases parsed from
      `~/.ssh/config` instead of free-text entry).
- [ ] **PTY e2e for the rich wizard** via the `cliout` ptylive pattern (the tea models are unit-tested
      via direct `Update`/`View`, but no end-to-end raw-terminal drive exists).
- [ ] **`cornus setup --scenario <name>` presets** to skip the first Select and jump straight into a
      scenario's questions (and possibly a non-interactive `--set key=val` mode).

## July 2026 consolidation follow-ups

- [ ] Add a per-record flock shared by barehost server and shim read-modify-write cycles, then reconsider making CORNUS_BARE_SHIM the default. Also design companion reboot recovery against the rebuilt app netns. — *source: JOURNAL 2026-07-15 to 2026-07-17 barehost milestones*
- [ ] Investigate rshared to rslave sidecar-mount content propagation in nested DinD; current bare and containerd companion coverage proves wiring but not mounted-file content. — *source: JOURNAL 2026-07-17 barehost companion E2E*
- [~] Run socks5-ingress.star and socks5-ingress-tls.star live on docker and kube, then add a native ingress E2E with client KUBECONFIG and an ingress controller. Plain socks5-ingress.star passed on docker on 2026-07-20; TLS on docker and both kube/native legs remain. — *source: JOURNAL 2026-07-18 ingress via SOCKS5 conduit*
- [ ] Synchronize Japanese and Simplified Chinese docs for the bare backend and ingress-via-conduit pages. — *source: JOURNAL 2026-07-17 to 2026-07-18 documentation updates*

## Block-protocol DB write path (2026-07-18) — perf follow-ups

Context: `pkg/wire/sqliteab` runs a real SQLite workload over the block proxy in-process (SQLite ->
psanford VFS -> p9 client -> ServeBlockProxy -> yamux fork -> ServeBlockServer). See JOURNAL
2026-07-18 "real SQLite workload over the block proxy". The per-small-write allocation amplification is
FIXED (52 MB/op -> ~4 MB/op, +~75% insert throughput; `blockServer` scratch reuse + `MemStore`
in-place/cap-preallocated RMW). Remaining, evidence-backed:

- [ ] **Carry the alloc fix to DiskStore.** The production on-disk cache (`CORNUS_FILE_CACHE=1`) has the
      same RMW-per-small-write shape MemStore had. Add a DiskStore-backed `sqliteab` variant, measure,
      then apply the same in-place/scratch treatment if it profiles the same.

## Concurrent caller (2026-07-18) — reads DONE

- [ ] **Per-sub-block seq-gating (optional warmth).** Concurrent SAME-block writes currently drop+refetch that
      block (cold, correct). Keying the proxy `admitWrite` by `(block, subBlock)` would keep them warm. Warmth
      optimization only, not correctness.

## July 2026 consolidation follow-ups (second pass)

- [ ] Add an `x-cornus-knative` Compose extension, multi-revision traffic splitting/tags, and supported Knative sidecar/volume/network interoperation. — *source: JOURNAL Knative Serving descriptor*
- [ ] Vendor the Knative Serving/Kourier E2E installation so the opt-in Knative scenario does not require internet access. — *source: JOURNAL Knative Serving descriptor*
- [~] Add MPL Exhibit A headers to modified `third_party/yamux/*.go`, ship `THIRD_PARTY_NOTICES.md`
      beside released binaries, and wire the license scanner into CI. PARTIAL (2026-07-21 tackle-todos
      sweep): (a) DONE — the MPL 2.0 Exhibit A notice was added to all 14 `.go` source files in
      `third_party/yamux/` (they carried no license header; the dir is a separate module, MPL-covered,
      HashiCorp copyright confirmed in LICENSE); no build constraints affected; the submodule still
      builds/vets/tests green. STILL OPEN: (b) ship `THIRD_PARTY_NOTICES.md` as a release asset beside
      the binaries (release.yml wiring), and (c) wire the license scanner into CI (the `audit-licenses`
      skill regenerates the notices; CI enforcement is not yet wired). — *source: JOURNAL dependency
      license audit*

## Incus deploy backend follow-ups (2026-07-21)

The `incus` backend (`pkg/deploy/incushost`, `CORNUS_DEPLOY_BACKEND=incus`) landed
and is E2E-green (7/7 in the containerized runner, `make e2e-container
E2E_TARGETS=incus`). See [LTM/incus-backend.md](./LTM/incus-backend.md) for the
design, mapping, and the Debian-runner migration. Remaining:

- [ ] Implement `MountingBackend` / `EgressBackend` for incus (client-local 9P
      mounts + client-side egress) via a caretaker companion, mirroring
      `pkg/deploy/barehost`'s `companion_linux.go` / `mounts_linux.go` /
      `egress_linux.go`. Until then incus does not advertise those capabilities
      (client-local mounts fall back to unsupported, like dockerhost without
      remote mode). — *source: JOURNAL 2026-07-21 incus backend Phase 2*
- [ ] Realize `RemoteCapable` for incus: the `remote`/`agentImage`/`companions`
      fields and `CORNUS_INCUS_REMOTE` are carried, but the always-on
      remote-companion path (ForwardPort via a companion, exec ssh-agent
      forwarding) is not wired. — *source: same*
- [ ] Bump the `github.com/lxc/incus/v6` client library past `v6.18.0` once the
      vendored containerd is bumped: v6.19+ needs `runtime-spec v1.3.0`, which the
      pinned `containerd v1.7.24` `oci` package cannot compile against. (This is
      the client LIBRARY pin; the E2E runner daemon is incus 7.2 from Zabbly,
      independent.) — *source: same*
- [ ] Faithful incus `Logs`: the console log has no per-line timestamps and no
      stdout/stderr split, so `--since`/`--until`/`--follow`/`--tail` are warned
      and ignored. A follow implementation would need `ConsoleInstanceDynamic`;
      per-line timestamps have no Incus source. — *source: same*
      **CROSS-REF (2026-07-27 sweep): `e2e/scenarios/mcp-stdio-tools.star` now guards its `logs_tail` CONTENT assertion behind `if TARGET == "incus"` for exactly this reason (the console PTY carries the shell prompt, not app stdout — measured: `logs_tail` returns `/ # `). The scenario had never actually run on incus before the leg was added. When this item is fixed, REMOVE that guard so incus regains the assertion.**

## Docs site Topics→Guides restructure (2026-07-24) — pre-existing defects surfaced

The restructure itself is done and verified (see JOURNAL "Work summary: docs site
Topics→Guides restructure"). These are **pre-existing** defects it uncovered but
did not introduce — each sits in a file the restructure only link-edited (verified:
0 heading or fence lines touched in any of them).

- [x] 10 structural translation errors reported by
      `audit_markdown_translation.py` for **both** ja and zh (identical in each,
      which is itself evidence they predate this work): heading-level sequence
      drift in `cli/{compose,socks5,web}.md`, `guides/observability.md`,
      `reference/{connection-config,deploy-spec,server-env-vars}.md`,
      `architecture/deploy-engine.md`; fenced-block count/language drift in
      `cli/{tunnel,web}.md`, `guides/observability.md`,
      `reference/server-env-vars.md`. Reproduce with
      `python3 .agents/skills/translate-documents/scripts/audit_markdown_translation.py docs docs/ja --locale-prefix /ja --exclude ja --exclude zh --exclude README.md`.
      — *source: JOURNAL 2026-07-24 docs restructure*
- [ ] 8 broken cross-page anchors in the locale trees, invisible to
      `npm run docs:build` (it does not validate non-ASCII cross-page fragments):
      `{ja,zh}/reference/deploy-spec.md` → `guides/observability#workload-telemetry`;
      `{ja,zh}/architecture/caretaker.md` → `architecture/networking#the-workload-to-workload-hub`;
      `ja/architecture/security.md` → `architecture/caretaker#クライアント側-egress`
      and `architecture/networking#discovery-and-policy`;
      `zh/architecture/security.md` → `architecture/networking#发现和策略`;
      `ja/architecture/deploy-engine.md` → `architecture/build-engine#9p-経由のリモートビルド`.
      Detect by extracting every `id="…"` from `.vitepress/dist/**/*.html` and
      validating each `](/path#anchor)` in the Markdown against it. — *source: same*
- [x] `docs/ja/reference/connection-config.md` is missing the **IngressCertificate**
      section entirely (the English page has it). `docs/ja/guides/ingress.md` had to
      drop its `#ingresscertificate` anchor and link to the page instead. Translate
      the section, then restore the anchor. — *source: same*
- [x] Consider making the anchor validator and the duplicate-target check part of
      the documentation gate. Both caught real defects this session that
      `npm run docs:build` passed over; neither exists in the repo yet. See
      `.agents/docs/QUALITY_GATE.md`. — *source: same*
- [ ] `e2e/scenarios/deploy-ingress.star` can now be extended to fetch THROUGH the
      controller, not just assert the generated Ingress object: `E2E_INGRESS_NGINX=1`
      makes a real ingress-nginx available in the kube target (2026-07-25). This
      supersedes the older "Execute `deploy-ingress.star` against a kind cluster with an
      ingress controller" item, whose blocker is now removed.
      — *source: JOURNAL ingress tunnels closing the four remaining items*
- [ ] Two ja/zh translation gaps closed opportunistically on 2026-07-25 (the
      `guides/observability` Workload telemetry section) came from the standing sync
      debt; the remaining ja/zh sync items above are unaffected and still open.
      — *source: JOURNAL ingress tunnels five findings fixed*
- [x] **`ingress-tunnel-ssh.star` skip drops docker coverage.** It skips whenever
      `E2E_INGRESS_NGINX=1`, but that flag only installs a controller into the kind
      cluster — on docker the mux front still applies and the scenario would pass. So
      `make e2e-container E2E_TARGETS="docker kube" E2E_INGRESS_NGINX=1` silently loses
      docker coverage of ingress tunnels (verified). Guard on "kube AND the flag".
      — *source: same*
      **DONE (2026-07-27 sweep): the guard is now `TARGET == "kube" and getenv("E2E_INGRESS_NGINX") == "1"`, so docker keeps its mux-front coverage.**

## In-container server mode (2026-07-26) — implemented with remaining gaps

Dockerhost server-in-a-container support, host-path translation, preflight,
containerd log-shim staging, setup/docs, and Docker E2E are implemented. The
completed proposal-phase checklist was retired during journal/LTM reconciliation.
Containerd networking/CNI packaging, bare/Incus path translation, and privileged
host-process mount-unwind verification remain in `## Open Items` above.

- [ ] Guard against a deploy targeting the cornus container itself with a
      label-based check in the GC/reconcile pass. This was pre-existing but is more
      reachable in server-in-a-container mode.
      — *source: LTM/in-container-server-mode.md §Remaining limits*

## Fresh-eyes engineering review (2026-07-25)

An outside-perspective review of the project as a whole — scope, abstraction boundaries, process,
and default posture — rather than a feature-level audit. The strategic items (S1-S5) are judgement
calls that need a decision from the maintainer, not mechanical fixes; the defects (D1-D6) are
unambiguous and small. Baseline measured at review time: 194k LOC across 873 Go files and 52
`pkg/` packages, 82k lines of test code against 112k of production code, `go build` / `go vet` /
`go test ./...` all clean, 46 of 3,440 functions over 100 lines, exactly one TODO/FIXME comment in
the tree. The craft indicators are strong; the findings below are about judgement and process.

### Strategic — need a decision, not a patch

- [x] S1. Decide what to CUT. Five deploy backends (`dockerhost`, `containerdhost`, `kubernetes`,
      `barehost`, `incushost`), 22 CLI commands, and an own-registry + in-process-BuildKit + 9P +
      SOCKS5 + DNS + nftables + hub-overlay + OTel-collector + Knative + ingress-emulation + SPA +
      MCP surface, at version `dev` with zero releases and one maintainer. Nearly every item tracks
      an independently-moving upstream (BuildKit, containerd, client-go, the Docker API, Incus), so
      the maintenance bill compounds. The evidence that breadth is already costing depth is in the
      `Backend` contract's own admissions: logs, stats, exec, and cp act on "the first instance"
      only, and the kubernetes backend projects a ready count onto fabricated `<name>-<i>` replica
      slots so no real pod states (e.g. CrashLoopBackOff) are ever surfaced. Recommendation: drop
      `incushost` and `barehost`, and re-justify Knative pre-1.0. Two deep backends beat five
      shallow ones. — *source: fresh-eyes engineering review 2026-07-25*
      **DECIDED (2026-07-27): CUT NOTHING. All five deploy backends and Knative are kept; the verification gap is closed with CI instead. Grounding measured at decision time: barehost 5,368 prod / 2,064 test LOC (ratio 0.38) and incushost 1,563/733 (0.47) were the two worst-tested backends AND the only two with no CI leg, against 0.86-1.09 for docker/containerd/kubernetes. The review's central 'breadth costs depth' evidence was half stale: 'logs/stats/exec/cp act on the first instance only' is real (deploy.go:255-280), but 'kubernetes never surfaces real pod states e.g. CrashLoopBackOff' is outdated — kubernetes.go:4064-4073 zips synthesized instances to name-sorted pods and sets inst.Message = instanceDiagnostic(...) for crash-loop / image-pull / unschedulable, plus a real exit code; only the State field is binary. The remaining depth concern is the Docker wire-format constraint, which is S2's subject, not a reason to cut backends. FOLLOW-UP: bare + incus E2E CI legs (in progress), and raising both backends' test ratios remains open as a separate item.**
- [ ] S2. Re-examine the deploy abstraction. `Backend` is a 20-method mandatory interface plus
      **11 optional capability interfaces** (`RegistryAdvertiser`, `IngressAdvertiser`,
      `IngressGateway`, `VolumeRemover`, `MountingBackend`, `RemoteCapable`, `MountSessionReader`,
      `AgentForwardCapable`, `Preflighter`, `AttachingBackend`, `EgressBackend`) discovered by type
      assertion at 15 call sites in `pkg/server`. Worse, the contract mandates Docker's *wire
      formats* — stdcopy-multiplexed log frames, Docker's archive tar, Docker's stats JSON, all
      "passed through unchanged" — so every non-Docker backend must emulate Docker (kubernetes wraps
      its unframed stream in stdcopy framing purely to satisfy this). Docker fidelity is the product
      at the CLI edge, but it has been pushed all the way into the core interface, and the
      capability-sniffing means the server does know each backend's quirks, just diffusely. "Behind
      the same interface" oversells it: adding a backend is a large, subtle job. Consider narrowing
      the mandatory interface to the lifecycle core and moving the Docker-shaped IO
      (logs/stats/cp/exec framing) into an adapter layer the CLI owns. — *source: same*
- [ ] S3. Adopt incremental version control. The tree is a SINGLE commit (`81dc071 Initial.`,
      263,428 insertions across 1,433 files) with 158 changed files / roughly 10k lines uncommitted
      on top of it at review time. Consequences: no `git bisect`, no per-change rationale, no
      reviewable unit, no attribution — and a live risk of losing the working tree. The 6,669-line
      `JOURNAL.md` is currently doing the job git history should do, and doing it strictly worse
      because a hand-written journal cannot be bisected or verified against an actual diff. This is
      the highest-leverage process change available. — *source: same*
- [ ] S4. Flip the default security posture, or gate the quickstart. Auth is off unless configured
      (`authenticator.enabled()` is false with no env set, and `wrap` becomes a pass-through),
      `privileged: true` is the manifest and chart default, and the service is a NodePort on 30500.
      Composed, that is unauthenticated build / deploy / exec reachable on every node IP — effective
      cluster-wide RCE for anyone who can reach a node port. `docs/guides/security.md` states this
      plainly in its opening lines, and the rewritten README now keeps its evaluation server on
      loopback with an adjacent warning. The Docker-free docs quick start still walks a new user
      into the NodePort posture without a warning at the point of action, and documentation is the
      wrong layer for a safety control. Separately, an empty JWT `scope` grants FULL access
      (`CaretakerOnly` returns false for `""`), which is fail-open. — *source: same*
- [x] S5. Freeze the `docs/ja` and `docs/zh` trees until 1.0. 62 English pages are mirrored by 61
      Japanese and 61 Simplified Chinese pages (13.5k translated lines of a 22.8k-line doc set),
      serviced by two glossaries and a translation skill. Every English doc edit now owes two
      translation follow-ups — a standing tax on a solo, pre-release project that already carries 65
      open to-do items. This is capacity spent on reach the project cannot yet use. — *source: same*

### Concrete defects — small and unambiguous

- [x] D1. Delete `e2e/scenarios/ftp/cornus-e2e-ftpd`: a 7.9 MB unstripped **aarch64** ELF committed
      to the tree and referenced by nothing. `e2e/scenarios/ftp/Dockerfile` builds `/ftpd` from
      source in a multi-stage build, so the binary is a stray `go build` output named after the
      module. It is the largest tracked file in the repo and violates the project's own AGENTS.md
      rule against building binaries into the version-controlled tree. — *source: same*
      **DONE (2026-07-27 sweep): removed. Verified unreferenced first (only hits were this TODO and the coincidental `module cornus-e2e-ftpd` in the ftp fixture's go.mod). The deletion is unstaged in the worktree.**
- [x] D4. Repair the orphaned doc comment at `pkg/deploy/deploy.go:161`. The paragraph describing
      `VolumeRemover` ("an optional Backend capability: removing a named, project-scoped volume ...")
      runs straight into the `IngressGateway` paragraph with no blank line, so godoc attaches
      `VolumeRemover`'s text to `IngressGateway` and leaves `type VolumeRemover interface` (line 170)
      undocumented. Both types render the wrong contract. — *source: same*
      **DONE (2026-07-27 sweep): the VolumeRemover paragraph now sits above `type VolumeRemover interface`; `go doc` renders both types' contracts correctly.**
- [x] D5. Drop the pre-release deprecations. `cmd/cornus/main.go:120-121` carries `mount-agent` and
      `mountcheck` as DEPRECATED aliases (`cmd/cornus/mountagent.go`) while `version` is still
      `"dev"` with no published release. There are no users to break; carrying compatibility debt
      for a version that never shipped is pure cost. — *source: same*
      **DONE (2026-07-27 sweep): `mount-agent` and `mountcheck` removed and `cmd/cornus/mountagent.go` deleted; `caretaker-check` (the live replacement) unaffected.**
- [ ] D6. Force-rank the remaining TODO backlog. The 2026-07-26 sweep moved all 117 `[x]` entries
      into the JOURNAL closure index, leaving 95 open and 10 deferred items. A backlog this size is
      still where work can go to rest rather than get done; rank what remains. — *source: same*

### Scope of the review — what was NOT covered

- [ ] Not audited deeply enough to have an opinion on correctness: the BuildKit solver
      (`pkg/build/builder`), the 9P/block transport (`pkg/wire`, `pkg/blockcache`), and the Compose
      translator (`pkg/compose`, notably the 306-line `translateService`). These are three of the
      highest-risk subsystems and warrant their own adversarial pass. Note also that the tree was
      being edited concurrently during the review — an early `go build` failed on `barehost` mid-edit
      and passed moments later — so all measurements above are a snapshot. — *source: same*

## First-user product review (2026-07-25) — install path, CLI surface, docs

Findings from following the former `README.md` quick start and
`docs/introduction/quick-start.md` literally as a new user, with the shipped release artifacts,
rather than reading the code. Complements the engineering review above: that one asks what the
project should cut, this one asks whether a stranger can get it running. The README rewrite closed
D2, D3, U10, and U11; the pre-release deprecations are **D5**, the translation-tree tax is **S5**,
and the unauthenticated-quickstart posture is **S4**. U7 and U8 below are the mechanical, doc-layer
half of S4 and are worth doing regardless of how S4 is decided.

Verified working at review time, so the items below are onboarding problems and not engine
problems: `cornus compose up` against a local `serve --storage` with the dockerhost backend built
the former README demo image including a `RUN --mount=type=cache` and a `RUN --mount=type=secret` layer,
pushed it to the built-in registry, deployed it, served traffic on the published port, and
`compose down` removed it cleanly.

### Blocking — a new user hits these before anything works

- [x] U1. Fix the published Helm chart version in the docs. `helm show chart
      oci://ghcr.io/moriyoshi/charts/cornus --version 0.1.0` fails with
      `FetchReference ... :0.1.0: not found`; the only tag published to that repository is `0.0.0`.
      This is presented as the *recommended* install path and is wrong in three files:
      `docs/introduction/quick-start.md:40`, `docs/ja/introduction/quick-start.md:40`, and
      `docs/zh/introduction/quick-start.md:40`. Either
      publish a matching chart version or drop the `--version` pin from the documented command. —
      *source: first-user product review 2026-07-25*
- [ ] U2. Reconcile the version numbers. Release tag is `v0.0.0`; GHCR image tags are
      `0.0.0` / `0.0` / `latest`; `deploy/helm/cornus/Chart.yaml` carries `version: 0.3.0` with
      `appVersion: "0.1.0"`; the docs say `0.1.0`; a locally built binary prints `dev`. There is no
      single number a user can quote in a bug report, and U1 is a direct symptom. Pick one scheme
      and make the chart, image, tag, and `cornus version` agree. — *source: same*
- [x] U3. Add a global `--server` alias for `-H` / `--host`.
      `docs/introduction/quick-start.md:50` tells the reader that later commands would otherwise
      need `--server` or `CORNUS_HOST`, so the natural next command is
      `cornus compose ... --server URL` — which fails with `unknown flag --server`. Four spellings
      exist for one concept today: `--server` on `deploy` / `exec` / `port-forward` / `tunnel` /
      `socks5` / `hub` / `storage usage` / `config set-context`; `-H` / `--host` plus `$CORNUS_HOST`
      on `compose *` / `web` / `daemon docker`; `--addr` on `serve` / `health`; and neither on
      `build`, which has only `--builder` and `--registry`. `compose` is the headline surface and is
      the one that rejects the documented name. Accepting both names everywhere is near-zero cost;
      alternatively fix the two doc sentences, but the flag divergence will keep biting. —
      *source: same*
      **DONE (2026-07-27 sweep): kong v1.15 supports flag aliases, so `compose`'s `-H/--host` now carries `aliases:"server"`. Both spellings work.**

### Concrete defects — small and unambiguous

- [x] U4. Log `cornus serving` only after the listener is up. `cmd/cornus/serve.go:141-146` emits
      `cornus serving addr=... storage=...` and *then* calls `srv.Run(ctx)`, so on an occupied port
      the operator sees a success line immediately followed by
      `error: listen tcp ...: bind: address already in use`. Move the log after a successful
      `net.Listen`, or emit it from inside `Run`. — *source: same*
      **DONE (2026-07-27 sweep): new `server.Server.Ready()` channel closes at the existing bind point (`s.ready.Store(true)`); serve.go selects on it against Run's error. Verified live: an occupied port prints only the bind error.**
- [x] U5. Stop burying errors under the full command tree. An unknown flag prints 169 lines and a
      bare `cornus` prints 176, with the actual error as the final line — off-screen on an 80x24
      terminal. Configure kong to print a one-line usage hint plus the error instead of the whole
      help. — *source: same*
      **DONE (2026-07-27 sweep): `kong.ShortUsageOnError()` replaces `UsageOnError()`. Unknown-flag output went from 169 lines to 4, error last.**
- [x] U6. Accept `--version` as a flag, not only as a `version` subcommand. It is the first thing
      users try, and it currently fails with `unknown flag --version`. — *source: same*
      **DONE (2026-07-27 sweep): `kong.VersionFlag` + `kong.Vars`; the `version` subcommand (incl. `--features`) still works.**
- [x] U7. Document checksum verification in the install steps. The release already publishes
      `SHA256SUMS` (606 B) and `SHA256SUMS.bundle` (10 KB, sigstore), but the documented install is
      `curl -fsSL ... -o cornus && chmod +x && sudo mv /usr/local/bin/` with no verification — the
      hard part is done and unadvertised. Note there is no `checksums.txt`; a fetch of that name
      404s, so do not reference it. Fix the install and quick-start pages under `docs/`. —
      *source: same*
- [x] U8. Pin the manifest URL to a release tag. The quickstart runs
      `kubectl apply -f https://raw.githubusercontent.com/moriyoshi/cornus/main/deploy/k8s/cornus.yaml`
      — an unpinned moving branch — to install a `privileged: true` StatefulSet whose RBAC covers
      `secrets` (get/list/watch), `pods/exec`, `deployments`, and `services`, plus a `ClusterRole`
      for CRDs. Whatever S4 decides about the default posture, the URL should reference a tag so a
      user gets the manifest matching the image they pulled. — *source: same*
- [x] U9. State the project's maturity on the docs on-ramp. The rewritten README now says that
      Cornus is under active development and does not promise a stable CLI/API, but `docs/index.md`
      and `docs/introduction/*.md` still read like a 1.0 product while the reviewed artifacts said
      `v0.0.0`. Add the same expectation-setting near the top of the docs landing page. —
      *source: same*

### Editorial — onboarding sequence

- [x] U12. Mention the download size in the install docs. Published release binaries are 86-107 MB
      (linux-amd64 is 106.7 MB); a local `CGO_ENABLED=0 -ldflags="-s -w"` static build is 99 MB
      against 142 MB unstripped. Reasonable for an embedded BuildKit, but large enough that a user
      on a slow link will wonder whether the download stalled. One clause next to the `curl`
      command. — *source: same*

## Whole-codebase implementation and documentation gap audit (2026-07-26)

Read-only review of the current dirty worktree, deduplicated against every open item above.
Mechanical documentation baseline: the VitePress build passes (apart from its existing chunk-size
warning); locale file parity and nav-tree reachability pass; the anchor check fails on the two
localized links recorded below. The ja/zh structural translation audits still report the seven
already-tracked errors at line 1291 and are not duplicated here.

### Implementation correctness and reliability

- [ ] **HIGH: Preserve explicit zero values and Compose reset semantics during multi-file merge.**
      `pkg/compose/merge.go:10-28` documents that typed decoding loses key presence and the
      `!reset` / `!override` tags; `mergeService` at lines 37-85 consequently applies only nonzero
      scalar overrides. A later file therefore cannot turn inherited values such as
      `privileged: true`, `tty: true`, or `read_only: true` back off, or explicitly clear a scalar.
      Preserve YAML presence/tags before typed decode, cover false/empty/reset/override cases, and
      disclose the limitation until fixed. — *source: whole-codebase gap audit 2026-07-26*
- [ ] **HIGH: Forward client-sourced credential sessions across server replicas.**
      `pkg/server/caretaker_attach.go:207-224` explicitly consults only the local deploy-attach
      registry for credential streams. Unlike mount sessions, a credential session owned by another
      replica has no shared routing record or forward endpoint, so a caretaker landing on a peer
      replica cannot fetch a declared credential. Extend the capability-scoped hub-store forwarding
      design to credentials, retain session/name authorization at the owner, and add a two-replica
      E2E. — *source: whole-codebase gap audit 2026-07-26*
- [x] **MEDIUM: Make Docker-compatible volume removal operate on the real backend volume.**
      `pkg/dockerproxy/translate.go:13-62` now translates named Docker volumes into real
      `DeploySpec.Volumes`, but `pkg/dockerproxy/volumes.go:10-12,55-82` still describes them as
      unbacked: `docker volume rm` deletes only an in-memory name and prune always reports that
      nothing was removed. Plumb `client.DeleteVolume` (with attachment/use tracking) into the
      proxy, or return an explicit unsupported error; never report successful deletion while
      persistent backend data remains. Add remove/prune persistence tests. — *source: same*
      **DONE (2026-07-27 sweep): `deployAttacher` gained `DeleteVolume`; `docker volume rm` reaches the backend, 409s on an in-use volume, 501s on a backend without `VolumeRemover`, and prune honors the `all` filter per Engine API 1.43. No 2xx is returned while data survives.**
- [x] **MEDIUM: Fail fast or provide health status for `depends_on:
      condition: service_healthy` on containerd and bare.**
      `cmd/cornus/internal/composecli/reconcile.go:320-375` knowingly waits until the dependency
      timeout when a backend never reports health, while
      `pkg/deploy/containerdhost/lifecycle_linux.go:64-68` and
      `pkg/deploy/barehost/lifecycle_linux.go:69-71` drop healthchecks. Detect this impossible
      condition during planning/reconcile with an actionable error, or implement probe/status
      reporting; a required dependency should not consume the full timeout for a state the backend
      cannot produce. — *source: same*
      **DONE (2026-07-27 sweep): new optional `deploy.HealthReporter` capability (positive, not a backend-name list), implemented by dockerhost and kubernetes, advertised via `api.BackendInfo` on /info. composecli validates before any build/deploy and fails with an actionable error. Unknown capability reads as capable (fail-safe).**
- [x] **MEDIUM: Validate Compose long-syntax published ports instead of silently rewriting them.**
      `pkg/compose/types.go:1940-1964` ignores `strconv.Atoi` errors for string `published` values,
      accepts non-integral and unsupported types without an error, and defaults any resulting zero
      to the target port. Invalid input such as `published: bogus` can therefore become a valid but
      different mapping. Strictly validate `target`, `published`, and protocol; support valid range
      syntax or reject it explicitly; normalize omitted protocol to `tcp`; and add negative/boundary
      tests. — *source: same*
      **DONE (2026-07-27 sweep): `parsePortEntry` validates target/published/protocol, supports long-form ranges via a shared `expandPortPair`, normalizes protocol to tcp, and errors on unsupported types. The absent/zero `published` sentinel is preserved. 30 new subtests; no pre-existing test needed changing.**
- [ ] **MEDIUM: Surface and recover distributed hub-store write and heartbeat failures.**
      The `hub.Store` mutation interface in `pkg/hub/registry.go:48-55` cannot return errors.
      `pkg/hub/redisstore.go:111,154-161` discards heartbeat/HSET failures, while
      `pkg/kubehub/kubehub.go:286-319,492-501` drops CR create/update and Lease heartbeat errors.
      A replica can keep serving local lookups while peers silently lose its providers after the
      liveness TTL. Evolve the seam to report failures or add bounded retry plus
      readiness/log/metric signaling, and cover injected shared-store outages and recovery.
      — *source: same*
- [x] **MEDIUM: Preserve private-source credentials when `cornus push` authenticates to Cornus.**
      `cmd/cornus/commands.go:53-58,585-600` installs a destination-scoped bearer keychain when
      `CORNUS_TOKEN` is set, but that keychain returns anonymous credentials for every other
      registry instead of falling through to `authn.DefaultKeychain`. A cross-registry copy from a
      private source therefore fails even when Docker's credential store can authenticate it.
      Compose the destination credential with the default source keychain, add a two-registry auth
      test, and document precedence. — *source: same*
      **DONE (2026-07-27 sweep): `bearerForRegistry.Resolve` delegates to `authn.DefaultKeychain` for non-destination hosts. The token is still scoped to the destination; regression test asserts no leak plus delegation parity.**
- [x] Define and document the supported Dev Container schema subset.
      `pkg/devcontainer/devcontainer.go:108-115,234-266,349-382` recognizes but ignores
      `features`, `hostRequirements`, compose-based `runArgs`, `build.options`, and most
      single-container `runArgs`; the user guide does not enumerate this compatibility boundary.
      Prioritize expressible fields (especially `features` and runtime/build options), retain
      explicit warnings for the rest, and add compatibility fixtures proving every recognized but
      unimplemented field warns. — *source: same*

### User-facing and agent-facing documentation

- [ ] **BLOCKING: Fix the two dead localized client-local-mount fragments.**
      `docs/ja/guides/server-in-a-container.md:25` and
      `docs/zh/guides/server-in-a-container.md:25` use the English heading slug, so
      `docs:check-anchors` reports 2 dead links out of 218 checked fragments. Copy the exact emitted
      translated ids: JA
      `#クライアントローカルディレクトリをリモートワークロードにマウントする-local-mount、9p-でストリーム`
      and ZH
      `#将-client-local-directory-mount-到-remote-workload-local-mount-经-9p-stream`, then rerun
      the anchor checker. — *source: whole-codebase gap audit 2026-07-26*
- [x] **BLOCKING: Document the `activity` API-policy action and its read-endpoint exception.**
      `docs/guides/security.md:142-156` omits `activity` and says read/GET endpoints are not gated
      except registry pull, but `pkg/server/activity_http.go:17-38` gates
      `GET /.cornus/v1/activity` on `apiPolicy.Allow(identity, "activity")` because records expose
      deployment names, image refs, and identities. A least-privilege policy written from the guide
      gets an unexplained 403 from `cornus activity`. Correct the table/prose and sync ja/zh.
      — *source: same*
- [x] **BLOCKING: Repair the malformed VitePress warning directive in Chinese.**
      `docs/zh/guides/server-in-a-container.md:39` starts with two ASCII colons followed by
      U+FF1A and `warning`, so VitePress renders literal text instead of a warning container
      without failing the build. Change it to `::: warning` and scan localized directives for
      non-ASCII punctuation.
      — *source: same*
- [x] Document direct-deploy ingress tunnel fields.
      `docs/reference/deploy-spec.md:408-433` omits the public `IngressSpec.tunnel` and
      `IngressTunnelOpt.{enabled,hostMode,host}` fields declared at `pkg/api/deploy.go:710-729` and
      acted on by `cmd/cornus/commands.go:392`. The current material only documents the Compose
      `x-cornus-ingress.tunnel` spelling. Add the declarative YAML shape, foreground/client-only
      lifetime, credential and default semantics, then sync ja/zh; keep internal
      `clientEmulated` and transport-only managed certificates out of the schema. — *source: same*
- [x] Add `Mount.noCreateHostPath` to the deploy-spec reference.
      The public JSON/YAML field at `pkg/api/deploy.go:405-410` implements Compose
      `bind.create_host_path: false`, but the Mount table at
      `docs/reference/deploy-spec.md:158-169` omits it. Document the false default, the
      missing-caller-source failure behavior, and that server-host mounts do not use it; sync ja/zh.
      — *source: same*
- [x] Add the SSH tunnel schema to the connection-config reference.
      `pkg/clientconfig/clientconfig.go:71-76,123-163` exposes `Context.ssh-tunnel` and eleven
      `SSHTunnel` fields, while `docs/reference/connection-config.md:65-79` has neither a context
      row nor an `SSHTunnel` field table. CLI flags do not replace the raw schema needed to author,
      validate, or export project-context YAML. Document defaults, fields, and mutual exclusions,
      then sync ja/zh. — *source: same*
- [x] Document Kubernetes client rate-limit controls.
      `pkg/deploy/kubernetes/kubernetes.go:696-706` reads `CORNUS_KUBE_QPS` (default 50) and
      `CORNUS_KUBE_BURST` (default 100), but
      `docs/reference/server-env-vars.md:291-301` lists neither. Add both defaults and explain the
      readiness/reconcile impact of lowering them; sync ja/zh. — *source: same*
- [x] Refresh the public command and interface inventories.
      `docs/cli/index.md:39-57` omits the shipped public `setup`, `ingress-tunnel`, `activity`, and
      `storage` commands even though all four have pages and sidebar routes. The command inventories
      in README, architecture, and overview are current; the overview's HTTP inventory still omits
      newer endpoints such as `/.cornus/v1/activity`, `/.cornus/v1/storage`, and ingress tunnels.
      Reconcile the remaining inventories with `cmd/cornus/main.go:87-124` and
      `pkg/server/server.go:883-962`, including ja/zh CLI indexes. — *source: same*
- [x] Enforce the repository punctuation rule throughout Simplified Chinese docs.
      The audit found 2,026 prohibited punctuation characters across 57 `docs/zh` files (713
      full-width opening parentheses, 713 closing parentheses, and 600 colons); the existing
      translation-review TODO only names Japanese. Normalize prose while preserving URL fragments
      and code, then make the documented punctuation scan part of the repeatable docs gate.
      — *source: same*

### Tests, CI, packaging, and release

- [x] **HIGH: Run the web frontend quality gate in CI.**
      `web/package.json:8-10` defines the typechecked Vite build and `vitest run`, but
      `.github/workflows/ci.yml:19-70` runs no web step, and the release workflow at lines 188-192
      builds without testing. Add a web CI job that runs `npm ci`, `npm run build`, and `npm test`,
      and document it in `.agents/docs/QUALITY_GATE.md`. — *source: whole-codebase gap audit
      2026-07-26*
      **DONE (2026-07-27 sweep): new `web` job in ci.yml runs `npm ci`, `npm run build` (tsc --noEmit + vite build) and `npm test` (vitest). Verified locally: build clean, 104 tests pass.**
- [x] **HIGH: Restore native image-store coverage to the containerd CI subset.**
      `Makefile:231-239` includes `registry-host-native-containerd.star`, but
      `e2e/container/entrypoint.sh:41-49` omits it from `CONTAINERD_SCENARIOS`; the containerd
      workflow leg uses that entrypoint subset. Restore the scenario and add an invariant test or a
      single generated source so the Makefile and entrypoint lists cannot drift silently again.
      — *source: same*
      **DONE (2026-07-27 sweep): `registry-host-native-containerd.star` added to `CONTAINERD_SCENARIOS`, and `TestScenarioSubsetsInSync` (pkg/e2e) now enforces Makefile<->entrypoint parity for all three subsets (membership, order, non-vacuous parse, file existence).**
- [x] **HIGH: Add live E2E CI legs for the shipped bare and Incus backends.**
      The runner and scenario subsets already exist for bare
      (`e2e/container/entrypoint.sh:57-70,508`; `Makefile:247-261,546-550`) and Incus
      (`e2e/container/entrypoint.sh:76-84,511-523`; `Makefile:262-276,379-381`), but
      `.github/workflows/e2e.yml:40-62` selects only docker, kube, containerd, and kube-ingress.
      Add a privileged bare leg and provision an OCI-capable Incus leg; an intended CI daemon that
      is unavailable must fail rather than green-skip. — *source: same*
      **DONE (2026-07-27 sweep): `e2e.yml` now has six legs — `bare` and `incus` added, both one
      privileged `docker run` of the same all-in-one image (no host provisioning: the image ships
      runc + CNI for bare and its own incusd 7.2 from Zabbly for incus, so a GitHub-hosted
      `ubuntu-24.04` runner suffices). Fail-closed via two new entrypoint knobs, `E2E_STRICT=1`
      (turns the incus OCI-version self-skip and the bare companion-agent-image best-effort into
      hard failures) and `E2E_PREFLIGHT_ONLY=1` (an up-front gate step that does the real daemon
      bring-up and then runs `cornus-e2e --preflight`). Verified locally by fault injection
      (fake 6.0.0 incusd: exit 0 without strict, exit 1 with; masked runc / crane likewise) and by
      full local runs: bare 15/15, incus 9/9 green.**
- [x] **HIGH: Make tag releases independently gated and fail closed across artifacts.**
      CI triggers only branch pushes/PRs (`.github/workflows/ci.yml:3-7`), while release triggers
      tag pushes and runs no test gate (`.github/workflows/release.yml:17-19`). Image, chart, and
      binaries publish independently, and the GitHub Release depends only on binaries at lines
      293-296, so image/chart failure can still publish release assets. Gate the tagged commit,
      stage all artifact builds before publication, and require image, chart, and binaries to
      succeed before creating the GitHub Release. — *source: same*
      **DONE (2026-07-27 sweep): release.yml gained its own `gate` job (gofmt/build/vet/test on the tagged commit) that every artifact job depends on, and `release` now needs image + chart + binaries. workflow_dispatch stays a publish-nothing dry run.**
- [x] Build and smoke-test the production root Dockerfile before release.
      E2E CI builds only `e2e/container/Dockerfile` at `.github/workflows/e2e.yml:75-84`; the root
      release image first builds at `.github/workflows/release.yml:71-83`. Add an amd64 PR build
      and smoke checks for `cornus version` plus a basic server `/healthz` path. — *source: same*
      **DONE (2026-07-27 sweep): new `image` job in ci.yml builds the root Dockerfile (amd64, no push) and smokes `cornus version`, `version --features` (asserting the cgo obsstore linked into the runtime image), and `/healthz`.**
- [ ] Automate the real multi-replica hub checks.
      `Makefile:397-415` exposes Redis and KubeStore multi-replica scripts and notes that the
      Starlark harness is single-server, but no workflow invokes either target. Run both in CI or a
      required scheduled workflow so cross-process forwarding, shared-store liveness, and lease/TTL
      behavior do not remain manual-only. — *source: same*
- [x] Reconcile the release workflow's nonexistent manual trigger.
      `.github/workflows/release.yml:14,107` describes `workflow_dispatch` and manual edge rebuilds,
      but the trigger block at lines 17-19 contains only tag pushes. Add the documented dispatch
      path with safe non-release publication semantics, or remove the dead event branches/comments.
      — *source: same*
      **CLOSED STALE (2026-07-27 sweep): `workflow_dispatch` DOES exist — `release.yml:26-29`, documented at `:19-24` as the publish-nothing dry run. The claim was wrong; nothing to reconcile. (The real release gaps — no test gate, and `release` needing only `binaries` — were separate items and are now fixed.)**
- [x] Synchronize `.agents/docs/QUALITY_GATE.md` with actual CI and supported targets.
      Its CI summary at lines 138-146 omits the QoS, emulator-backed integration, and Helm jobs in
      `.github/workflows/ci.yml`; its target guidance omits the runnable bare and Incus targets.
      Document the complete gate and the per-target selection/requirements. — *source: same*
      **DONE (2026-07-27 sweep): section 4 now enumerates all seven `ci.yml` jobs (including the new `web` and `image`), the release gate and fail-closed graph, and all six live E2E legs, including the strict fail-closed `bare` and `incus` preflight/run paths.**

## Follow-ups extracted during 2026-07-26 good-sleep consolidation

- [ ] Decide whether writable client-local 9P mounts may use an mmap-capable cache
      mode for workloads such as Turbopack, and document the coherence trade-off;
      `cache=none` deliberately cannot support persistent-cache mmap.
      — *source: JOURNAL “Writable client-local 9P mount: missing Lock and O_APPEND write breakage”*
- [ ] Make the Docker API proxy compatible with Docker Engine/CLI 28 and 29 so the
      E2E runner can eventually lift its 27.5.1 pin; compare plugin dispatch and
      foreground attach protocols against a real daemon.
      — *source: JOURNAL “Docker leg: pin docker-ce to 27.5.1…”*
- [ ] Decide whether dockerhost's host-native registry should accept pushes by
      staging and loading them into the daemon, matching containerd's read-write
      native store.
      — *source: JOURNAL “Work summary: local image-store re-export → host-native registry”*
- [ ] Design backend-observed mount reporting for origin-attributed workloads not
      backed by a locally loaded project; there is no stored DeploySpec to read.
      — *source: JOURNAL “Follow-up: origin-based project attribution…”*
- [ ] Add automatic reconnect for a dropped client-local mount without requiring
      a full re-apply; stable session ids currently help only re-apply.
      — *source: JOURNAL “Follow-up: the still-terminating fix…”*
- [ ] Translate the Compose provider-services documentation into Japanese and
      Simplified Chinese.
      — *source: JOURNAL “Compose `provider:` service support”*

## Follow-ups from the S1 decision (2026-07-27)

- [ ] Raise the test ratios of the two backends S1 chose to keep rather than cut.
      `barehost` is 5,368 production LOC against 2,064 lines of test (ratio 0.38) and `incushost` is
      1,563/733 (0.47), against 0.86-1.09 for dockerhost/containerdhost/kubernetes. The new CI legs
      prove the happy paths end to end but do not close the unit-level gap — barehost in particular
      carries the most capability integrations of any backend (`MountingBackend`, `EgressBackend`,
      `RemoteCapable`, `AgentForwardCapable`) on the thinnest unit coverage.
      — *source: S1 decision, 2026-07-27 TODO sweep*
