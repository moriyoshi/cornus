# Project To-Dos

Items extracted from JOURNAL.md during `good-sleep` consolidation, plus open follow-ups. Each
item should be resolved or removed once addressed.

Completed items are cleared periodically into a "TODO wrap-up" entry in JOURNAL.md (the closure
index); the last sweep was 2026-07-04.

## Open Items

- [x] Export the BuildKit cache mounts across CI runs so the CD `image` job gets cross-run Go
      build-cache reuse — CLOSED, won't do (2026-07-19, investigated). `cache-to: type=gha` exports
      only image layers, never `type=cache` mount contents, so persisting them needs a "cache dance"
      (`reproducible-containers/buildkit-cache-dance`). Declined: it adds a third-party action to the
      cosign-signing release job, and dancing a multi-GB Go build cache x2 arches on top of the
      existing `mode=max` layer cache pressures the 10 GB GHA cache budget (LRU eviction degrades all
      caches, incl. CI's), for a payoff only on infrequent tag releases. Kept the image job on layer
      cache only. If arm64 release build time ever becomes the real pain, the better fix is a native
      `ubuntu-24.04-arm` runner (kills QEMU emulation) rather than cache-dancing. — *source: JOURNAL
      2026-07-19 — Cache downloaded Go artifacts in CD*
- [x] Apply the same Go cache-mount treatment to `e2e/container/Dockerfile` as the root `Dockerfile`
      — DONE (2026-07-19). Added `/go/pkg/mod` (shared `id=gomod`) + `/root/.cache/go-build`
      (`id=gobuild-e2e-${TARGETARCH}`) mounts to the `go mod download` / `go build` steps in the E2E
      runner image's build stage; `ARG TARGETARCH` declared for the per-arch build-cache key.
      `docker buildx build --check` clean. — *source: JOURNAL 2026-07-19 — Cache downloaded Go artifacts in CD*

- [x] Parallelize independent services' deploy+reconcile in `cornus compose up` — DONE (2026-07-15).
      `runForeground`/`upDetached` now launch one goroutine per selected service (errgroup); each still
      calls the existing `waitForDependencies` first, which already polls its `depends_on` targets'
      live status independently, so firing every service at once and letting each block on its own
      condition IS the topology resolution (no separate graph/level pass needed). See JOURNAL
      2026-07-15 — "Parallelize compose up's deploy+reconcile loop" for the full design (shared
      `cliout.Progress`/`LineGroup`, `suppressCascaded`, mountFree reordering). REMAINING: mounted
      (client-local-mount) services still serialize on `clientagent.Project.Apply`'s internal
      `reconcileMu` — that engine was not touched, so only their `waitForDependencies`/reportReconcile
      polling phases actually parallelize, not the attach-readiness step itself. Revisit only if that
      proves to matter in practice (mount-free services are the common case).
- [ ] `pkg/server/gcschedule_test.go` `TestPeriodicGCSupervisedAcrossPanic` is flaky, unrelated to any
      change here — observed failing ~3/5 runs in isolation (`gcRunning left true after the panicking
      run`) on an otherwise clean tree, both with and without `-race`. Looks like a race between the
      supervisor's restart-after-panic and the test's own readback of `gcRunning`. Noted during the
      2026-07-15 full-suite gate run for the compose reconcile-parallelization change (which does not
      touch `pkg/server`); needs its own investigation.
- [ ] Implement detached `cornus compose exec`: plumb Docker's detach option through the dockerhost backend and define a safe Kubernetes lifecycle rather than returning from an attached SPDY stream. — *source: JOURNAL 2026-07-12 — compose exec*
- [x] Route Compose extra `build.tags` through the server's `localPushTarget` loopback redirect when the advertised registry host is remote. — DONE (2026-07-15). New `Server.localPushTargets` redirects the primary `Target` and every additional `Tags` entry together (one `advertisedRegistry` resolution for the whole build); `pkg/server/build_attach.go` now calls it instead of redirecting only `Target`. See JOURNAL 2026-07-15 — "Compose build-group additional tags now redirected to the co-located registry" (also the fix for a live user-reported bug, not just the tracked gap).
- [ ] Enable GitHub Pages with GitHub Actions as the repository Pages source so the `docs.yml` deployment can publish the VitePress site. — *source: JOURNAL 2026-07-12 — VitePress user-reference docs site*
- [ ] Execute `e2e/scenarios/deploy-ingress.star` against a kind cluster with an ingress controller. — *source: JOURNAL 2026-07-12 — Automatic ingress creation*
- [ ] Design client-to-caretaker trace unification at the Apply/relay boundary, using propagated
      context or span links without falsely parenting the pod-scoped persistent caretaker connection
      under one CLI invocation. — *source: JOURNAL 2026-07-12 — Client-side distributed tracing and filled tracing gaps*
- [ ] Complete source-checked review of the remaining Japanese and Simplified Chinese pages for
      inline English residue, calqued phrases, terminology drift, and prohibited full-width colons or
      parentheses; resolve the Japanese audit warnings against the English source. — *source: JOURNAL 2026-07-12 — Consolidated Japanese translation audit and home-page translation cleanup*
- [ ] Rebuild generated `docs/.vitepress/dist/` before publishing the current API-path, architecture,
      and locale-source changes; do not hand-edit generated assets. — *source: JOURNAL 2026-07-12 — Docs sweep and home-page translation cleanup*

- [ ] Add a `deploy`/`deploy_attach` E2E scenario that interleaves a non-local (named/bare-name)
      volume between two client-local binds in the raw `spec.Mounts` list, to guard the sparse-index
      `m2`-gap regression — the existing `compose-mounts-multi.star` does not exercise it (compose
      routes `type: volume` into `spec.Volumes`, never producing a sparse index). — *source: JOURNAL
      2026-07-13 — Multi-mount caretaker investigation*
- [ ] Design decision needed: make a deployment's mount session id stable across server/client
      reconnects (e.g. reuse the id already baked into a running Deployment's caretaker env on
      re-apply, instead of minting a new one via `newSessionID()`), so a reconnecting client
      re-registers under the id the pod already presents — deferred pending confirmation via the new
      `logMountReset` WARN of whether the real-world trigger is a server restart or a deploy-attach
      reconnect. — *source: JOURNAL 2026-07-13 — Silent caretaker mount resets*
- [ ] Verify the Helm chart's opt-in `tailscaled` sidecar (`tailscale:` values block) against a live
      cluster — validated so far only via `helm lint`/`helm template`; whether Funnel actually works
      over the shared control-socket `emptyDir` in userspace mode is unconfirmed. — *source: JOURNAL
      2026-07-14 — Tunnels/hub docs restructuring... Tailscale Helm sidecar*
- [ ] ja/zh doc sync for the `cornus tunnel --forward-agent` ssh-agent-forwarding feature
      (`docs/guides/tunnels.md`'s ssh section, `docs/cli/tunnel.md`'s new flag row/example) — English
      only so far. — *source: JOURNAL 2026-07-14 — SSH-agent forwarding for the `cornus tunnel` ssh backend*
- [ ] ja/zh doc sync for the whole caretaker-sidecar mount relay / remote companion / agent-forwarding
      arc (dockerhost/containerdhost remote mode, `cornus exec --forward-agent`, kubernetes
      `AgentForward`) — English only across all of it so far. — *source: JOURNAL 2026-07-14 —
      Caretaker-sidecar mount relay... / Always-on remote companion... / Kubernetes `AgentRelayRole`...*
- [ ] Combine mounts+egress into one companion on the host backends (dockerhost/containerdhost) —
      kubernetes' `AttachingBackend` already does this; the host backends keep them as two
      independently-gated companions. — *source: JOURNAL 2026-07-14 — Caretaker-sidecar mount relay
      for dockerhost/containerdhost*
- [ ] Apply the `pkg/supervisor` restart-tree treatment to `tunnelManager`'s per-tunnel accept loop and
      the hub connection loops in `pkg/server` — flagged as natural next candidates using the same
      primitive already adopted by the GC loop and `pkg/caretaker`. — *source: JOURNAL 2026-07-14 —
      Caretaker-sidecar mount relay for dockerhost/containerdhost*

- [ ] Correct README wording that still describes the registry CAS as the shared integration substrate; Cornus subsystems integrate over OCI/HTTP. — *source: JOURNAL 2026-07-09 — ARCHITECTURE.md "big picture" reframed*
- [ ] Audit the `cornus daemon docker` client print path for the session-local SOCKS5 conduit banner. — *source: JOURNAL 2026-07-10 — Implemented: reach a compose service by its short name + session-local SOCKS5 tunnels*
- [ ] Add an `up -d` E2E that drives shared and session-local SOCKS5 conduit coexistence through the background agent. — *source: JOURNAL 2026-07-10 — Implemented: reach a compose service by its short name + session-local SOCKS5 tunnels*
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

- [x] Tailscale Funnel tunnel backend — DONE (2026-07-10) via the `tailscale` CLI subprocess route
      (the tsnet in-process listener was NOT viable: adding `tailscale.com` to go.mod forces
      `k8s.io/*` v0.32.1 -> v0.34.0 across the whole module — Go MVS is build-tag-agnostic, so a
      build tag gates compilation but not the version graph). Shipped `pkg/tunnel/tailscale`: an
      `UpstreamProvider` (like cloudflare) that shells out to `tailscale funnel <shim-port>`, parses
      the `*.ts.net` public URL, and kills the subprocess (tearing the Funnel config down) on Close.
      `CredentialOptional` (node joins the tailnet out-of-band); binary overridable via
      `CORNUS_TUNNEL_TAILSCALE_BIN`; blank-imported in `cmd/cornus/main.go`; URL/target parsing
      unit-tested; opt-in E2E `deploy-tunnel-tailscale.star` (gated on `CORNUS_TUNNEL_TAILSCALE_E2E`,
      in SCENARIOS so `make e2e-check` parse-validates it). README/ARCHITECTURE/TESTING backend
      enumerations updated. LIMITATION: Funnel is single-config-per-node on port 443, so concurrent
      tailscale tunnels on one node conflict (documented, not a cornus bug). A tsnet in-process
      backend remains possible only via a separate Go module/plugin. Live run never exercised (needs
      a joined, Funnel-enabled tailnet node). See [[public-tunnels]].

- [ ] Purge stale `.sig`/`.pem` release-asset references from older LTM/synthesis docs. *source: CI
      green-up (2026-07-08).* cosign v3 sign-blob now emits a single Sigstore `--bundle`; some
      LTM/synthesis docs still describe the old `.sig`/`.pem` outputs. Reconcile in a future
      consolidation pass. See [[release-and-packaging]], [[ci-github-actions]].

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
- [x] Dev Container follow-ups — DONE. (a) 2026-07-05 threaded `build.target`/`cacheFrom` through
      `BuildRequest`->`BuildSpec`->`SolveInput` (`build.target` -> `SolveInput.TargetStage` -> frontend
      `target` attr; `cache_from` folded into `type=registry` cache imports at the client); (b) 2026-07-05
      gated Starlark scenario `e2e/scenarios/devcontainer.star` (kube-only self-skip) + `devcontainer_up/
      ps/down` harness builtins driving `cornus-compose --devcontainer`; (c) 2026-07-05
      `postStartCommand`/`postAttachCommand` re-run on `start`/`restart` (`runStartHooks`, guarded on a
      devcontainer lifecycle), not only `up`
- [x] `stop`-and-keep semantics — DECIDED + documented (2026-07-05). The code already does record-level
      stop-and-keep: `dockerproxy` `stop` cancels the session (workload torn down) but keeps the record
      as `exited` so `docker ps -a` lists it; `start` re-opens the session and re-deploys from the kept
      spec; `rm` deletes the record. NOT a container-level pause (a client-served 9P mount can't outlive
      the caller, so the workload is recreated, not paused — ephemeral in-container state doesn't survive,
      consistent with the recreate model). Documented in ARCHITECTURE.md dockerproxy section.
- [x] Observability follow-ups — DONE (2026-07-05). (a) Backend-client spans: the kubernetes client-go
      transport (`rest.Config.WrapTransport`) and the dockerhost Docker-socket `http.Client` transport are
      wrapped with `otelhttp.NewTransport`, gated on `observability.Enabled()` (no wrap when off). (b)
      Opt-in Prometheus pull `/metrics`: `CORNUS_METRICS_PROMETHEUS` (requires telemetry enabled) adds a
      Prometheus exporter as an ADDITIONAL metric reader over its own registry (OTLP push untouched);
      `/metrics` is registered on the mux only when active and is auth-exempt. Zero-cost when off (no
      reader/handler/route/goroutine). Added deps: `prometheus/client_golang`,
      `otel/exporters/prometheus`.
- [ ] User-networks (remaining). NOTE (2026-07-05): the user-network machinery is VALIDATED in dind here
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
- [x] Multi-replica hub: k8s manifests — DONE (2026-07-07 sweep). Helm chart `replicas` value:
      when >1 it wires `CORNUS_HUB_STORE=kube`, POD_NAME/POD_NAMESPACE/CORNUS_K8S_NAMESPACE downward
      API, per-pod `CORNUS_HUB_FORWARD_URL` via a new headless Service (also the StatefulSet
      serviceName), preferred hostname anti-affinity, and wss + hub SANs under TLS. Fails template
      rendering unless `storage` is shared s3 (per-pod PVC CAS would split-brain). Chart 0.1.0 →
      0.2.0; default render byte-identical. Static `deploy/k8s/cornus.yaml` stays single-replica
      with a pointer comment. NEW follow-ups recorded below (namespace gap, forward-dial CA).
- [x] Multi-replica hub: mount-relay forwarding — DONE (2026-07-07 sweep, wave 6). Deploy-attach
      mount sessions publish routing records through the EXISTING hub store (additive: delivery-mode
      record under reserved `cornus.mount/<sha256(sessionID)[:16]>` names — digest only, the raw id
      stays an unguessable capability; nil mux so a hub relay can't open ingress onto the session;
      records filtered out of `/.cornus/v1/hub/catalog` AND catalog-watch pushes). A replica that doesn't
      hold the session forwards via new `GET /.cornus/v1/mount/forward` (shared `dialForward` helper with
      the hub forward: same bearer, same `CORNUS_HUB_FORWARD_CA` TLS). Local-session fast path
      consults no store (single-replica byte-identical, asserted by a counting-store test);
      teardown unregisters; crash-safety rides the stores' TTL/Lease/ownerRef GC. Two-replica
      end-to-end test over miniredis (9P read through the non-owning replica).
- [ ] Embedded-gossip hub Store backend (deferred third option alongside Redis/KubeStore) — *source:
      JOURNAL 2026-07-05 — Multi-replica hub PoC (Redis) SHIPPED + VALIDATED*
- [x] Harness: kube `compose_up` kind-load — DONE (2026-07-07 sweep). `prepareComposeBuildImages`
      in `pkg/e2e/harness.go`: on the kube target, `compose("up")` enumerates `build:` services via
      the compose model (`composeBuildImageRefs`, `<registry>/<project>-<service>:latest` — the
      exact tag composecli pushes), pre-runs `cornus compose build`, and `PrepareImage`s each ref
      (the same kind-load path `build()` uses). No-op on other targets and build-free files.
      Cleanup candidate: `ftp-usernet.star:37-44`'s pre-build workaround is now redundant.
- [x] `cornus compose` `up -d` mounts daemon: spec change on re-`up` — DONE (2026-07-07 tackle-todos
      sweep). The daemon fingerprints each service (sha256 of the canonical JSON of the resolved
      `daemonService`: DeploySpec + forward shape); re-`up` keeps an unchanged service (`up-to-date`),
      tears down + recreates a changed one (`recreated: configuration changed`), and recreates
      fingerprint-less records. Responses are stamped `Protocol: 2`; the CLI warns when an older
      daemon build (Protocol < 2) cannot detect changes (keep + warn, since killing it would drop all
      held mounts).
- [x] Live `docker compose up --scale` E2E — DONE (2026-07-07 sweep): `dockerd.star` grew a scale
      section (project `dscale`, `up -d --scale web=2`, both instances asserted server-side and via
      `docker ps`, `down` convergence) and PASSED LIVE against docker 29.2.1 / compose v5.0.2.
      NOTE: re-`up --scale web=1` (the recreate-diff path) is NOT live-testable yet — blocked on two
      real dockerproxy gaps, recorded as a new item below.
- [x] Scheduled/periodic storage GC — DONE (2026-07-07 tackle-todos sweep). `CORNUS_GC_INTERVAL`
      (Go duration; unset = fully off, zero cost; malformed/non-positive = hard startup error, matching
      the fail-closed policy-env precedent) runs the same `runGC` core as `POST /.cornus/v1/gc` on a ticker
      (`pkg/server/gcschedule.go`): first run after one full interval, non-overlapping (skip + log),
      errors logged not fatal, stopped-and-drained before `closeResources` returns. Documented in
      README + ARCHITECTURE GC sections.
- [x] Native stateless remote deploy verb — DONE (2026-07-07 sweep, wave 4). The server `POST
      /.cornus/v1/deploy` and `pkg/client.Deploy/Delete` already existed; the CLI now uses them:
      `cornus deploy --detach`/`-d` POSTs the spec and exits (workload persists; client-local mount
      sources rejected up front via `client.LocalMountSources` — they need the attach session's 9P;
      ports not auto-forwarded, note logged; local backend = documented no-op), and remote
      `--delete` now works as the matching one-shot teardown (previously hard-rejected). (The other
      half of this item — the local CLI hardcoding dockerhost — was fixed with the containerd
      backend: `localBackend()` honors `CORNUS_DEPLOY_BACKEND` for the host-level backends.)
- [ ] GHCR release follow-ups, blocked on repo creation: push the repo; adjust the hardcoded
      `ghcr.io/moriyoshi/cornus` defaults (Helm values, `deploy/k8s/cornus.yaml`, README) if the repo
      lands under an org (the workflow derives the name from `github.repository_owner`); tag `v0.1.0`
      so the pinned manifest ref and chart appVersion resolve; make the GHCR package public — *source:
      JOURNAL 2026-07-05 — Pre-built GHCR images for k8s installs*
- [x] Verify the Docker image build under Go 1.26 — DONE (2026-07-07): a full local `docker build`
      succeeded (build stage ran fresh, 86s, not cached; go-licenses third-party output produced and
      copied into the final image, sha256:9ebfaaa0df0f...).
- [x] Move `caretaker`/`caretaker-check`/`net-redirect` under `cornus daemon` — DONE (2026-07-07
      sweep). The three commands mount under `DaemonCmd` (shared structs, kong scopes flags per
      node) while the old top-level spellings remain as HIDDEN aliases (`kong:"cmd,hidden"` — the
      `LogShimCmd` pattern), so pod-spec argv keeps working and the generators were NOT changed.
      Parse-equivalence + visibility tests; smoke-ran both spellings.
- [x] UDP `cornus port-forward` — DONE (2026-07-07 sweep, wave 6) for dockerhost + containerd:
      `5353:53/udp` CLI suffix; the tunnel reuses the hub's 2-byte length-framed datagram convention
      (`wire.BridgeDatagramStream`); one tunnel per client source address with a 60s idle GC
      (mirrors the hub's per-source reach flows); backends dial a udp-connected socket to the
      workload IP the same way their TCP forward does. New newline-JSON `api.PortForwardAck` on udp
      tunnels only (TCP wire format unchanged) lets an incapable backend reject the dial cleanly —
      kubernetes stays TCP-only by design (`pods/portforward` cannot carry datagrams) and
      `pkg/portfwd` probe-detects it and warns-and-skips as before. Race-clean tests throughout.
- [x] Live kube-target run of `deploy-portforward.star` — DONE (2026-07-07): PASSED on a real kind
      cluster in dind (containerized runner, `E2E_TARGETS=kube`): unpublished :80 reached end to end
      through `cornus port-forward`, concurrent connections served.
- [x] Live kube-target run of `deploy-autoforward.star` — DONE (2026-07-07): PASSED on the same kind
      run — deploy-attach session auto-forwarded the published port (local 9P mounts served),
      concurrent connections served.
- [x] Helm JWKS/audience values — DONE (2026-07-07 sweep). `auth.jwt.{jwksURL, jwksConfigMap,
      jwksSecret, jwksKey, audience, issuer}` render the `CORNUS_JWT_*` envs; file-based sources
      mount the ConfigMap/Secret read-only and point `CORNUS_JWT_JWKS_FILE` at it; conflicting
      combinations fail template rendering (mirrors the server's "set only one" rule). Defaults
      render nothing.
- [x] Release CLI binaries — enhancements — DONE (2026-07-07 sweep). darwin/windows targets turned
      out to ALREADY exist in the matrix (all four cross-compile verified under Go 1.26);
      added: `SHA256SUMS` asset (generated over all binaries in the release job, `sha256sum -c`
      compatible), keyless cosign (`sign-blob` of SHA256SUMS with `.sig`/`.pem` uploaded; image
      signed by manifest-list digest via the build-push step's `digest` output; `id-token: write`
      scoped to exactly the two signing jobs; cosign-installer SHA-pinned), and `main.version`
      stamped into the image (`ARG VERSION=dev` in the Dockerfile ldflags + workflow build-arg;
      local builds unchanged). Validated against a real release run: PENDING the next tag.
- [x] Enable `E2E_MULTUS=1` on the `e2e.yml` kube matrix leg — DONE (2026-07-07 wave 5): the kube
      leg's docker run now passes `E2E_MULTUS=1` (docker leg explicitly `0`, behaviorally
      unchanged); confirm green on the next CI run.
- [ ] CI watch items: pin an explicit Helm `version:` in `ci.yml` if `azure/setup-helm@v5` ever fails
      its GitHub-API latest-version lookup; confirm the first Dependabot github-actions run is a no-op
      (everything is already at latest) — *source: JOURNAL 2026-07-06 — CI workflow hardening*
- [x] Publish the Helm chart as an OCI artifact to GHCR — DONE (2026-07-07 wave 5): `release.yml`
      gained a tag-gated `chart` job — `helm package` with `--version`/`--app-version` from the
      stripped tag (chart default image tag = released image, no per-release Chart.yaml commits),
      `helm push` to `oci://ghcr.io/<owner>/charts`, digest captured and cosign-signed keyless.
      First real run still depends on the GHCR repo landing (see the GHCR follow-ups item).
- [x] First LIVE run of `devcontainer-vscode.star` (@devcontainers/cli vs the docker proxy): DONE
      2026-07-06 — the first CI run failed exactly on watch item (a), plus two more foreground
      `docker run` gaps (attach-before-start closed, no /events start event). All three fixed in
      pkg/dockerproxy (condition-aware wait with dockerd's flush-header-early protocol, parked
      attach, real event stream); scenario now passes hands-off in the containerized runner.
      Watch item (b) was a non-issue. See JOURNAL 2026-07-06 — CI E2E failures fixed — *source:
      JOURNAL 2026-07-06 — E2E for the real VS Code devcontainer toolchain*

## New follow-ups from the 2026-07-07 sweep (wave 3)

- [x] Hub WS dial TLS plumbing — DONE (2026-07-07 sweep, wave 5). `caretaker.Config` gained
      `TLSClientConfig *tls.Config` (json:"-", in-process CLI path) and `TLS *TLSFiles`
      (serializable ca/cert/key file paths for sidecars, fail-fast load at Run); dials switched to
      `wire.DialControlHeaderTLS` (nil config = byte-identical); the `cornus hub` client-TLS
      refusal guard is gone (profile TLS now flows through); `CORNUS_HUB_FORWARD_CA` (PEM appended
      to system roots, fail-closed parse) secures `dialHubForward` inter-replica dials. REMAINDER
      tracked below: the k8s backend does not yet emit `Config.TLS`/`ReachDynamic` into sidecar
      configs.
- [x] Helm chart `CORNUS_K8S_NAMESPACE` gap — DONE (2026-07-07 sweep, wave 5). The fieldRef env is
      now unconditional in the chart StatefulSet (0.2.0 → 0.2.1) and added to the static
      `deploy/k8s/cornus.yaml` (whose ClusterRoleBinding-subject-pinned-to-default caveat is noted
      in a comment).
- [x] dockerproxy: compose v5 scale-reconverge gaps — DONE (2026-07-07 sweep, wave 5). Network
      labels stored at create (create-time-only, docker semantics) and echoed by `networkJSON`;
      `containerSummary` gained `NetworkSettings.Networks` (factored `networkEndpoints` shared with
      inspect). `dockerd.star` now drives `up -d --scale web=1` reconverge and PASSED LIVE against
      docker 29.2.1 / compose v5.0.2 (no third gap).
- [x] Multi-replica + shared s3 CAS concurrent GC — DONE (2026-07-07 wave 7). `CORNUS_GC_LEASE`
      (`kube`, `kube:<name>`, `kube:<ns>/<name>`; requires `CORNUS_GC_INTERVAL`; fail-closed on
      malformed value or unloadable kube config) gates each GC tick behind a per-tick CAS
      acquire/renew on a `coordination.k8s.io` Lease (duration 2x interval clamped [30s, 1h];
      holder identity POD_NAME/hostname; 409/AlreadyExists = clean refusal; gate errors skip the
      tick — a missed sweep beats a concurrent one). Deliberately NOT client-go leaderelection
      (leadership only matters at tick instants; no background renew loop). Chart 0.3.0:
      `gc.interval` + `gc.lease` values (lease-without-interval fails rendering); the multi-replica
      caveat now points at `gc.lease`; Lease RBAC already covered by the kube-hub-store rule.
      Nuance: intervals > 2h can let far-apart replicas each sweep once per round — never
      concurrently, which is the invariant.
- [x] e2e cleanup: ftp-usernet redundant pre-build — DONE (2026-07-07 wave 5), removed; the
      kube `compose_up` auto-build/kind-load covers it.
- [x] Hub caretaker sidecar wiring — DONE (2026-07-07 sweep, wave 6). `CORNUS_CARETAKER_TLS_SECRET`
      (Secret name; k8s TLS-secret key conventions) mounts read-only at `/cornus/tls` in
      server-bound sidecars (hub + mounts caretakers only; dns/proxy untouched) and stamps
      `Config.TLS` paths into the embedded config — CA-only Secrets supported, unreadable Secrets
      assume the full layout with a loud warning (intended TLS never silently degrades to
      plaintext); unset = byte-identical pod specs. `api.HubSpec.ImportDynamic {Ports, Protocol}`
      opts a deploy's hub sidecar into dynamic imports (maps to caretaker `ReachDynamic`). Chart
      `caretakerTlsSecret` value renders the env + `resourceNames`-scoped `secrets get` RBAC
      (chart 0.2.2, since bumped). The dynamic-DNS nicety LANDED (2026-07-07 wave 7): the caretaker
      dns role gained a concurrency-safe `DynamicRecords` overlay (RWMutex map, allocation-light
      lookups, static records always win, positive A answers only — NODATA/upstream semantics
      unchanged); `runDynamicReach` publishes `name -> synthetic IP` after a successful bind and
      withdraws on unbind/teardown, joined via a process-wide rendezvous (one caretaker process =
      one pod, so no Config plumbing needed). Plain `dial(peer)` now works for dynamically
      discovered names when the pod runs both roles.

## Production-hardening gaps (originally from GAP_ASSESSMENT.md, 2026-07-04)

The standalone `GAP_ASSESSMENT.md` was RETIRED on 2026-07-09 after a per-sub-claim re-verification
against current code: 6 of its 7 areas are materially closed and the 7th (rollback) was descoped,
so the doc was substantially stale and its live status is fully carried here. Most items were
tackled in the 2026-07-04 sweep (see JOURNAL). Remaining sub-items are noted inline; the three
low-severity residuals the re-verification found still open are listed first.

- [ ] **`/.cornus/v1/build` defaults `insecure=true` and `push=true`** (residual from GAP §1;
      re-verified open 2026-07-09). `pkg/server/build.go` reads `q.Get("push") != "false"` and
      `q.Get("insecure") != "false"`, so a build with no explicit query pushes over an insecure
      registry connection by default. Acceptable for the dev inner-loop posture but never explicitly
      resolved; decide whether to flip the defaults or document it as intended.
- [ ] **No disk-usage reporting / quota surface** (residual from GAP §2; re-verified open
      2026-07-09). GC + the build-cache cap prevent the unbounded-growth outage, but nothing in
      `pkg/storage`/`pkg/server` reports current disk usage or enforces a quota. Low severity.
- [ ] **`_catalog` walks the full `repos/` tree per call with no cache** (residual from GAP §5;
      re-verified open 2026-07-09). `pkg/storage/cas.go` `walkRepos` re-walks `repos/` on every
      `_catalog` request (pagination was added, but not caching); "expensive at scale, especially on
      S3". Low severity.

- [x] **Storage GC / lifecycle** — DONE (sweep 2026-07-04). Blob `DELETE` + `Backend.DeleteBlob`;
      mark-and-sweep `Backend.GC` (roots = tag + manifest markers, BFS through config/layers/index/
      subject, sweeps unreachable `blobs/sha256/**`); `Backend.SweepStaleUploads(ttl)` (24h default,
      prefix-restricted, called at Backend open); BuildKit worker now carries a disk-derived
      `GCPolicy` (buildkitd's `DefaultGCPolicy`) with `CORNUS_BUILD_CACHE_KEEP_BYTES` override.
      Follow-ups DONE (2026-07-05): `localcache`-dir prune (`builder.PruneLocalCache`, subtree-newest-mtime
      TTL, root-confined) and an on-demand `POST /.cornus/v1/gc` endpoint (runs CAS `Backend.GC` + localcache
      prune, engine-absent-safe) gated on a new `gc` API-policy action. Scheduled/periodic GC wired
      2026-07-07 (`CORNUS_GC_INTERVAL`, see the closed item above).
- [x] **CI** — DONE. `.github/workflows/ci.yml` runs gofmt-check / build / vet / test / `make
      e2e-check` on push + PR (`setup-go` via `go-version-file: go.mod`, module+build cache).
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
- [x] **Operational hardening** — DONE. Build-concurrency semaphore (`CORNUS_BUILD_CONCURRENCY`,
      default NumCPU); per-deploy-name lock in the server layer (apply + delete); shutdown Closes the
      lazily-built engine + backend (frees the BuildKit data-dir lock); `readyz` is a real
      `atomic.Bool` gate (503 until serving, false on shutdown); `http.MaxBytesReader` on blob PUT
      (10 GiB) and build-context tar (2 GiB, `CORNUS_MAX_BUILD_CONTEXT_BYTES`).
- [x] **Registry spec holes** — DONE. Referrers API, `Range` on blob GET (206 + `Content-Range` +
      `Accept-Ranges`, 416 unsatisfiable), pagination on tags/`_catalog` (`?n=`/`?last=` + `Link` next).
      Transactional-manifest fix (2026-07-05): `PutManifest` write ORDERING is now documented + defended
      (blob -> marker -> tag, tag last, so a tag never precedes its data); `GetManifest`-by-tag returns a
      clean `ErrNotFound` (OCI `MANIFEST_UNKNOWN`) when a tag resolves to a missing/corrupt manifest, so a
      crash-induced dangling tag reads self-consistently. No active repair (avoids racing a live write); GC
      reclaims orphan markers/blobs.
- [x] **Deploy/Compose fidelity** — DONE. `pkg/compose` WARNS on unsupported service/`deploy.*`
      fields. Healthchecks + CPU/mem limits DONE (2026-07-05): `api.DeploySpec.Healthcheck` (`Test`/
      `Interval`/`Timeout`/`Retries`/`StartPeriod`, CMD/CMD-SHELL/NONE) + `Resources` (`CPULimit` cores,
      `MemoryLimit` bytes), parsed from compose `healthcheck:` + `deploy.resources.limits`, mapped by
      dockerhost (`Config.Healthcheck` + `NanoCpus`/`Memory`) and kubernetes (exec liveness/readiness probes
      + `resources.limits`); both removed from the warned set. Deploy rollback DESCOPED (2026-07-05, see
      ARCHITECTURE.md "Deploy rollback — descoped"): NOT a parity gap — Compose/Docker have no rollback; the kubernetes
      backend updates Deployments in place with native ReplicaSet history intact (`RevisionHistoryLimit`
      unset -> default 10), so `kubectl rollout undo` already works. A bespoke cross-backend revision store
      would invent stateful history for dockerhost against the imperative model — not planned.
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
- [x] dockerhost: `replicas>1` + published ports — DONE (2026-07-07 tackle-todos sweep). Host ports
      publish on replica 0 only (containerd parity; documented in `Apply`); replicas 1+ get a copy of
      the create body with `PortBindings` niled (ExposedPorts kept). The test fake now models
      dockerd's port lifecycle (allocate at start, conflict = 500 "port is already allocated",
      release on remove); regression `TestApplyReplicasPublishHostPortsOnFirstOnly`.
- [x] dockerhost: Delete anonymous-volume leak — DONE (2026-07-07 tackle-todos sweep).
      `containerRemove` now sends `?force=1&v=1` (`docker rm -v` parity per the `Backend.Delete`
      contract); fake records the flag; `TestDeleteRemovesAnonymousVolumes`.
- [x] kubernetes: `spec.Restart` — DONE (2026-07-07 tackle-todos sweep). `warnUnsupportedRestart` in
      the `deployment()` funnel warns (slog, containerd-healthcheck style) for `no`/`on-failure[:N]`/
      unknown; silent for ``/`always`/`unless-stopped` (`unless-stopped` counts as honored — Stop
      scales to zero, so stopped stays stopped). Deploy always proceeds.
- [x] kubernetes: command-only spec preserving ENTRYPOINT — DONE (2026-07-07 tackle-todos sweep).
      `spec.Command` now always rides `container.Args`; `container.Command` set only from
      `spec.Entrypoint` — image ENTRYPOINT preserved, docker create semantics on all backends.
- [x] kubernetes: exec/attach contract — DONE (2026-07-07 tackle-todos sweep). Non-TTY exec AND
      attach output now stdcopy-framed (`muxWriters`, same wrapping Logs used); `ExecCreate` warns
      per-field for unsupported Env/WorkingDir/User/Privileged (not honored — `PodExecOptions` can't
      express them; deliberately no `sh -c` wrapping; exec still runs so the devcontainer flow is
      preserved); ExecInspect lifecycle fixed (Running = started && !done; Pid stays 0, documented —
      pods/exec never surfaces the remote PID).
- [x] Cross-backend contract tightening — DONE (2026-07-07 tackle-todos sweep). (a) new
      `deploy.ErrNotFound` sentinel; all three backends wrap it from Stop/Start/Restart on a missing
      name (dockerhost was silent nil, k8s a raw apierror); Delete stays delete-if-exists,
      documented on the `Backend` interface; caller audit found no nil-for-missing reliance;
      `handleDeployAction` now maps it to 404, and `streamErrStatus` classifies via `errors.Is`
      first with substring fallback. (b) shared `deploy.ParseSince` (`pkg/deploy/since.go`, docker
      `GetTimestamp` grammar: unix[.nanos] / RFC3339 / durations-ago; `"0"` = epoch) wired into all
      three backends — garbage `since` is now an error everywhere (k8s previously returned ALL
      logs), durations now work on containerd. (c) state vocabulary documented on `Backend.Status`
      (docker 7 verbatim / containerd 4 / k8s running|pending with fabricated `<name>-<i>` IDs;
      common subset = `running` + the Running bool); no normalization layer, no spelling
      misalignments found. Per-backend `TestLifecycleMissingDeployment` + since-validation tests.
- [x] Server stream-error surfacing (Logs/Stats) — DONE (2026-07-07 tackle-todos sweep). Lazy-header
      writer in `pkg/server/deploy.go` + `pkg/dockerproxy/containers.go` (deliberate small
      duplication): 200 + flush happen on the backend's FIRST write; a pre-output backend error now
      yields a real status (ErrNotFound/"no such" → 404, "not supported" → 501, invalid since → 400,
      else 500) with a JSON/docker-style error body. Mid-stream-after-bytes unchanged (nothing can
      be done post-200). Behavioral note: on quiet follow-mode logs/stats the client now receives
      headers only at first output. Attach/wait flush-header-early protocol untouched.
- [x] containerd: StatsJSON fidelity — DONE (2026-07-07 tackle-todos sweep). `memory_stats.stats`
      (cg1: `total_inactive_file` etc.; cg2: verbatim `memory.stat` keys — docker CLI MEM now
      correct), `networks` (read `/proc/<task.Pid()>/net/dev` — the task's netns view, no setns;
      excluding `lo`; best-effort), `blkio_stats.io_service_bytes_recursive` (cg1 passthrough / cg2
      `io.stat` expansion). Pure mapper `sampleFromMetrics` factored out + unit-tested rootless.
- [x] containerd: unsupported-network-feature warning — DONE (2026-07-07 tackle-todos sweep). One
      `slog.Warn` per deploy in `Apply` (healthcheck-warning style) when any attachment uses
      Aliases / non-bridge Driver / DriverOpts.
- [x] Doc fixes from the audit — DONE (2026-07-07 tackle-todos sweep). README + ARCHITECTURE "both
      backends" claims corrected (user networks = dockerhost + kubernetes; containerd limitation
      stated); `pkg/api/deploy.go` Replicas doc (all backends honor it; replica-0-only port publish
      noted) and Command doc (uniform args-to-ENTRYPOINT semantics — the k8s divergence was fixed,
      so no divergence note); `localBackend()` doc comment + `slog.Warn` on unrecognized
      `CORNUS_DEPLOY_BACKEND` values falling through to dockerhost. New env knobs documented:
      `CORNUS_GC_INTERVAL` (README + ARCHITECTURE GC sections), `CORNUS_CONTAINERD_LOG_MAX_BYTES`
      (ARCHITECTURE containerd logs bullet + README containerd paragraph).

## Compose CLI fidelity triage (2026-07-11)

Items from triaging the `cornus compose` CLI surface against the Docker Compose CLI
reference; see the JOURNAL entry "Compose CLI fidelity triage (2026-07-11)" for the
full categorized tables (Tier A bugs / B missing flags / C missing subcommands /
D intentional extensions). *source: compose CLI fidelity triage 2026-07-11.*

- [x] up `-d/--detach` help says "Detached mode (default; accepted for
      compatibility)" but the default is foreground (`runForeground`,
      commands.go:202); reword to match `docker compose` (Tier A1, High / trivial).
      DONE (2026-07-11 sweep): help reworded to state the default is foreground
      (stream logs, hold mounts/forwards until Ctrl-C) and `-d` detaches to the
      background helper. Text-only, no behavior change.
- [x] down: add `-v/--volumes` to remove named volumes (Tier B, High). DONE
      (2026-07-11 sweep): `down -v/--volumes` removes the project's non-external
      named volumes after the workloads are gone, like `docker compose down -v`.
      New optional `deploy.VolumeRemover` backend capability (`RemoveVolume`) —
      dockerhost (`DELETE /volumes/{name}`), kubernetes (delete the `cornus.volume`
      PVC via `namedPVCName`), containerd (`os.RemoveAll` the named volume dir); a
      `DELETE /.cornus/v1/volume/{name}` server endpoint (gated on the `deploy` action,
      501 when the backend lacks the capability); `client.DeleteVolume` +
      `client.ErrVolumeRemovalUnsupported`; exported `compose.VolumeResourceName`
      so the CLI targets exactly the provisioned volumes. External volumes are
      never touched; unsupported backend is a soft skip. Regression tests across
      all five packages.
- [ ] logs `--follow` has no `-f` short (the group reserves `-f` for `--file`,
      logs.go:51-52); allow `-f` on logs or document the conflict (Tier A3, Med).
      DEFERRED (2026-07-11): the group owns `-f/--file`, which every subcommand
      inherits, so a `logs -f/--follow` short collides under kong. Left as a
      documented divergence (use `--follow`); revisit only if the group short is
      reworked.
- [x] ps: add `-q/--quiet`, `--format` (json/table), and `--services` for scripting
      parity (Tier B, Med). DONE (2026-07-11 sweep): `PsCmd` gained `-q/--quiet`
      (resource ids of created services), `--services` (service names in dependency
      order), and `--format table|json`; rendering factored into pure `psRows` /
      `renderPs` helpers with unit tests in commands_test.go.
- [x] depends_on conditions (service_healthy/started/completed) are honored
      (Tier A5, Med). DONE (2026-07-11): conditions are parsed onto
      `compose.Dependency` and the `up` path gates on them via
      `waitForDependencies` / `dependencySatisfied` (reconcile.go) — service_started
      (all instances running), service_healthy (InstanceStatus.Health == healthy),
      service_completed_successfully (exited 0); required deps abort on timeout,
      optional deps warn and proceed; ctx-cancellable. Wired into both the
      foreground and detached up paths; tested in reconcile_test.go.
- [x] Global `--env-file` and `--profile` (Tier B, Med). DONE (2026-07-11):
      `--env-file` (repeatable) threads through `compose.LoadOptions.EnvFiles` into
      interpolation, replacing the default `.env` (process env still wins; missing
      explicit file errors). `--profile` (repeatable) + `COMPOSE_PROFILES` feed
      `LoadOptions.Profiles`; `filterProfiles` drops services whose profiles are
      inactive, pulling in enabled services' depends_on transitively. `Service.Profiles`
      added + in `supportedServiceFields`. Tests: envfile_test.go, profiles_test.go.
- [ ] up `--no-attach` is a bool, but compose's is a stringArray of services;
      reconcile the semantic mismatch (Tier A2, Low). DEFERRED (risky semantics
      change, low value).
- [~] Lower-severity missing flags catalogued in the JOURNAL entry (Tier B, Low).
      PARTIAL (2026-07-11): DONE `build --no-cache` + `--build-arg` (thread
      NoCache/BuildArgs into the build request; parseBuildArgs supports KEY=VALUE
      and bare-KEY-from-env) and `logs --until` (api.LogOptions.Until through server
      + dockerhost/containerd; kubernetes warns as unsupported). STILL OPEN: up
      (--no-deps / --force-recreate / --remove-orphans / -t), down
      (--remove-orphans / --rmi / -t), logs (--no-color / --index), build
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
- [x] Cheap missing subcommands worth considering: `compose config` and
      `compose version` (rest of Tier C is architectural) (Low). DONE (2026-07-11
      sweep): added `compose version` (`--short`, `--format pretty|json`; real build
      version propagated from `main.version`) and `compose config` (`--services`,
      `--volumes`, `--images`, `-q/--quiet` validate-only, and a default `--format
      yaml|json` dump of the parsed/merged project — cornus's parsed view, not a
      byte-faithful reserialization, documented on the command).

## Client-side egress follow-ups (2026-07-11)

Core feature (Modes 1/2/3 on kubernetes) is DONE + unit-tested (see JOURNAL "Client-side egress
(2026-07-11)"). Remaining:

- [x] **G1 — client dials through its OWN proxy** — DONE (2026-07-11). The client-side egress backing
      previously dialed direct (`serve.go` passed `nil` dial → bare `net.Dialer`), bypassing the
      caller's corporate/SASE proxy. New `pkg/clientproxy/dialer.go` (`Dialer()` / injectable
      `DialerFor(cfg)`): NO_PROXY bypass, `ALL_PROXY` via SOCKS5 (socks5 local-DNS vs socks5h remote-DNS
      preserved), `HTTP(S)_PROXY` via HTTP CONNECT, direct fallback. Wired at `serve.go:54`. Unit tests
      (`dialer_test.go`, real in-process SOCKS5 + CONNECT proxies) + integration proof
      (`deploywire.TestServeEgressDialsThroughClientProxy`, real relay→backing→client-proxy path).
      ARCHITECTURE.md updated (the claim is now true).
- [x] **G2 — reject a distinct gateway URL** — DONE (2026-07-11). `EgressSpec.Gateway` (a separate
      gateway node) was accepted but silently ignored (only the `gateway` ROUTE — server-as-gateway —
      is implemented). New `(*EgressSpec).Validate()` rejects a non-empty `Gateway`; the
      `--egress-gateway` flag is removed; Validate is called from `egressflags.Apply`, compose
      `translateService`, and all three backends' egress apply paths. Reserved for a future
      distinct-gateway-node release.
- [x] **Gap sweep** — DONE (2026-07-11). (a) k8s detached-network guard now also rejects relay-mode
      egress (proxy/transparent) on a `Default` network — the caretaker needs the cluster network to
      reach the relay (env mode stays allowed); regression added. (b) egress + DNS/Hub coexistence
      VERIFIED sound (all folded into one caretaker; the transparent redirect exempts loopback + TCP-only,
      so DNS/hub loopback traffic is never captured) — no change needed. (c) plain-HTTP forward path
      (`forwardHTTP`) now byte-metered via a `countWriter` (all three egress paths covered).
- [x] **Project-level compose egress default** — DONE (2026-07-11). Top-level `Project.Egress`
      (compose `x-cornus-egress:` at the document root) is the default for every service with no egress
      block of its own; a service-level block FULLY overrides it (no field merge). `Plan` gives each
      inheriting service a fresh copy (no aliasing); the default is validated once up front; `Load`
      carries it across multi-file merges. Also RENAMED the compose key `egress:` → `x-cornus-egress:`
      at BOTH levels (the `x-` extension prefix keeps a cornus file valid for standard compose tooling
      — `egress` was the only non-standard bare key cornus added). New E2E `compose-egress-project.star`
      (env mode; inherit + full-override differential). Tests: `TestProjectEgressDefaultAndOverride`,
      `TestProjectEgressDefaultValidated`.
- [x] **G1 Starlark E2E** — DONE (2026-07-11). `compose-egress-client-proxy.star` (kube-only): points
      the HELD client session's `ALL_PROXY` at a new in-process harness recording proxy and asserts the
      proxy captured the exact relayed destination — proof the client dialed via its OWN proxy. Built
      the two enabling harness capabilities: `egress_proxy()` / `egress_proxy_hits(addr)` (in-process
      SOCKS5 recording proxy, `pkg/e2e/egress_proxy.go`, closed on teardown, Go-tested against the
      production `clientproxy` dialer) and an `env=` kwarg on `compose_up_bg` (sets the session-holder's
      env). Recording (not reachability) is the assertion, so no reachable target / proxy image is
      needed. Check-validated; not yet run live in dind/kind.
- [x] **Host companion caretaker** — DONE (2026-07-11): dockerhost AND containerd, PROXY and
      TRANSPARENT modes, and REPLICAS>1 (each replica gets its own companion sharing that instance's
      netns; companions named `cornus-<name>-egress-<i>`, all reaped before the app instances).
      Transparent: the companion runs with NET_ADMIN and programs
      the nftables redirect ITSELF in the shared netns (new `caretaker.EgressRole.SetupRedirect` +
      shared `pkg/netredirect` — `setupRedirect` moved out of `cmd/cornus` so both the `net-redirect`
      subcommand and the caretaker use it), marking its own sockets (`cfg.Mark`) so its dials escape
      the redirect. dockerhost adds `HostConfig.CapAdd:[NET_ADMIN]`; containerd appends
      `oci.WithAddedCapabilities(["CAP_NET_ADMIN"])`. No proxy env injected in transparent mode. New `deploy.EgressBackend` interface (`ApplyWithEgress`);
      the server routes an egress-only host deploy to it (`applyWithEgress` in
      `pkg/server/deploy_attach.go`, replacing the rejection). Each backend runs a companion
      `cornus caretaker` sharing the workload's network namespace: dockerhost
      (`pkg/deploy/dockerhost/egress.go`) via `HostConfig.NetworkMode: "container:<app-id>"` (reaps
      the companion FIRST — Docker's netns-provider constraint); containerd
      (`pkg/deploy/containerdhost/egress_linux.go`) via a second task joining the app's PINNED netns
      (`/run/cornus/netns/...`, no netns-provider constraint since the pin is an independent bind
      mount) with restart-monitor labels (session-scoped deploys make reboot-resurrection moot). Both
      label the companion `cornus.role=egress-caretaker`, inject the proxy env (shared
      `egresspolicy.ProxyEnv`), and filter it out of Status/List. Scope: proxy mode, replicas=1
      (rejected otherwise). A local deploy with a relay mode is rejected (needs --server).
      REMAINING: (a) **transparent** on hosts (`net-redirect` + NET_ADMIN in the shared netns);
      (b) replicas>1 (per-instance companions); (c) dockerhost Stop/Start/Restart act on the app
      instance only — decide whether the companion should follow; (d) live validation. This path
      also unblocks the long-planned host client-sourced credentials.
- [x] **Gateway terminus for `--detach`** — DONE (2026-07-11). Durable egress with no live client:
      the cornus server is the egress node. The egress stream now carries the caretaker's route
      (`wire.OpenEgress` writes session/route/dest); the server (`pkg/server/egress_relay.go`) splits
      into `relayEgressSession` (session held → the session's policy is authoritative, unchanged) and
      a sessionless GATEWAY path (`pipeGatewayEgress`) that dials the destination directly, gated by
      operator opt-in `CORNUS_EGRESS_GATEWAY` and an optional ceiling `CORNUS_EGRESS_POLICY` (a JSON
      EgressSpec; malformed = fail-closed startup error). The stateless `POST /.cornus/v1/deploy`
      (`applyDetachedEgress`) injects the egress caretaker with a SESSIONLESS `AttachEgress`
      (Session="") for a relay-mode spec, on k8s (ApplyWithAttachments) or host (ApplyWithEgress).
      `checkDetachable` now permits `--detach` for a policy that routes only to gateway/cluster/deny
      (a `client` route, or a script, still needs a session → rejected). Security: a pod's gateway
      request can never exceed the operator ceiling; a client route with no session is dropped. Unit
      tests: gateway round-trip, disabled-drops, operator-deny, and the checkDetachable matrix.
      REMAINING: a distinct operator gateway NODE (forward to another cornus egress node) beyond
      "the server itself"; live validation.
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
- [x] **Metrics** for the egress role — DONE (2026-07-11). `caretaker.egress.connections` (by route
      client/gateway/cluster/deny/error + protocol, recorded in `routeUpstream`) and
      `caretaker.egress.bytes` (inbound/outbound, at the CONNECT/SOCKS5/transparent splice sites),
      mirroring the proxy role's OTel instruments (`pkg/caretaker/observability.go`). Zero-cost when
      telemetry is off. The plain-HTTP forward path is counted by the connection counter (no byte
      metering on that per-request path yet).

## SSH-tunnel connection profiles (2026-07-16) — follow-ups

- [x] **E2E scenario for the SSH tunnel** — DONE (2026-07-16). Added the `sshd()` harness builtin,
      `CapSSHD` preflight capability, `openssh-server` in the E2E Dockerfile, and
      `e2e/scenarios/deploy-sshtunnel-docker.star` (in Makefile `SCENARIOS`), verified live on the
      docker target. See JOURNAL 2026-07-16 "automated E2E scenario". REMAINING: the scenario does
      NOT yet exercise the unix-socket **binary fallback** (`--ssh-use-binary` / ProxyCommand) or a
      **ProxyJump** chain end-to-end — both are covered only by Go tests today; extending the
      scenario (e.g. a second sshd as a bastion, or a ProxyCommand host) would close that.
- [ ] **Higher-level, per-surface stream auto-resume.** The transport reconnection cannot resume an
      in-flight stream (`logs -f`, exec/attach) — the yamux/exec/pty state is lost on a drop. Making
      e.g. `logs -f` re-attach from the last offset must live at that command's layer, not the dialer.
- [ ] **ssh_config fidelity beyond common keywords + ProxyJump.** `Match` blocks and `ProxyCommand`
      are honored only via the `--ssh-use-binary` fallback (auto for ProxyCommand); kevinburke/ssh_config
      supports neither `Match` nor token expansion. Auto-detecting that a `Match` block *applies* (so
      the fallback is chosen without the flag) and the Windows `ProxyCommand`/`Match` story (the
      unix-socket fallback is Linux/macOS only) are open.

- [ ] zh/ja doc sync for `cornus web --publish-in-conduit` (the "One browser proxy setting" section
      and the new flag rows in `docs/cli/web.md`) and for the `cornus socks5 --allow-non-loopback`
      flag + "Loopback only, by default" section in `docs/cli/socks5.md`. English only so far.
      — *source: JOURNAL 2026-07-18 — Serve `cornus web` through the SOCKS5 conduit*
- [ ] Add a `web.star` (or `agent.star`) E2E leg for `cornus web --publish-in-conduit`: publish into
      the shared conduit, `http_get(url="http://cornus.internal/.cornus/web/config", socks5=<addr>)`
      (the builtin already exists), and assert the name is withdrawn after the client exits. No new
      builtins needed. — *source: JOURNAL 2026-07-18 — Serve `cornus web` through the SOCKS5 conduit*
- [ ] Decide whether a non-loopback shared conduit bind should be refused at the CLI/config layer too
      (not only at `socks5.Start`): `CORNUS_CONDUIT=socks5://0.0.0.0:1080` and a hand-edited
      `Socks5.Listen` still reach `socks5.Start`, which now refuses them — a clear error, but the
      earlier the better. — *source: JOURNAL 2026-07-18 — Serve `cornus web` through the SOCKS5 conduit*

## `cornus setup` wizard (2026-07-16) — follow-ups

- [ ] **ja/zh translations of `docs/cli/setup.md`** via the `translate-documents` skill (only the `en`
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
- [ ] Run socks5-ingress.star and socks5-ingress-tls.star live on docker and kube, then add a native ingress E2E with client KUBECONFIG and an ingress controller. — *source: JOURNAL 2026-07-18 ingress via SOCKS5 conduit*
- [ ] Synchronize Japanese and Simplified Chinese docs for the bare backend and ingress-via-conduit pages. — *source: JOURNAL 2026-07-17 to 2026-07-18 documentation updates*

## Block-protocol DB write path (2026-07-18) — perf follow-ups

Context: `pkg/wire/sqliteab` runs a real SQLite workload over the block proxy in-process (SQLite ->
psanford VFS -> p9 client -> ServeBlockProxy -> yamux fork -> ServeBlockServer). See JOURNAL
2026-07-18 "real SQLite workload over the block proxy". The per-small-write allocation amplification is
FIXED (52 MB/op -> ~4 MB/op, +~75% insert throughput; `blockServer` scratch reuse + `MemStore`
in-place/cap-preallocated RMW). Remaining, evidence-backed:

- [x] **Sub-block hashing + hash-at-fsync — DONE (2026-07-18), off by default.** Both implemented behind
      negotiated HELLO feature bits (`FeatSubBlockHash`, `FeatDeferHash`; `WithBlockFeatures`). Store gained
      `WriteThrough` (hash-free in-place RMW) + `HashRange` (in-place sub-range hash) in both stores. A/B on
      the SQLite insert workload: sub-block +21%, defer +19%, alloc-neutral (after fixing a WriteThrough
      full-chunk-copy regression). Correct across all modes (`TestBlockProxyFeatureModes`,
      `TestSQLiteCoherenceModes`, `-race`). See JOURNAL 2026-07-18 "block-protocol coherence rework".
      REMAINING before a production flip: (1) prefer `FeatSubBlockHash` (keeps the classic per-write
      coherence guarantee; defer relaxes it to fsync boundaries); (2) wire an env/config knob so BOTH the
      server and the `cornus deploy` caller (ninep_backing.go ServeBlockServer) advertise the bit — today
      only tests/sqliteab enable it; (3) measure a DiskStore-backed sqliteab variant (production uses the
      on-disk cache, the bench uses MemStore).
- [ ] **Carry the alloc fix to DiskStore.** The production on-disk cache (`CORNUS_FILE_CACHE=1`) has the
      same RMW-per-small-write shape MemStore had. Add a DiskStore-backed `sqliteab` variant, measure,
      then apply the same in-place/scratch treatment if it profiles the same.
- [x] **Sub-block demand-fill — DONE (2026-07-18), off by default.** `FeatSubBlockFill` (implies
      FeatSubBlockHash): keep the 1 MiB block as the addressing unit but track presence per sub-block and
      fill only the touched sub-range on a read miss (Store `GetSub`/`PutSub` + presence bitmaps in both
      backends; `opReadRange`). Measured: random point reads fetch ~130x less (7 KiB/query vs 1 MiB/query),
      beating even a 16 KiB fixed chunk, with the block/coherence granularity unchanged. Correct across all
      modes (`TestWritableDemandFill`, `TestBlockProxyFeatureModes`, `TestSQLiteCoherenceModes`, `-race`).
      See JOURNAL 2026-07-18 "sub-block demand-fill".
- [x] **Readahead window for demand-fill — DONE (2026-07-18).** `WithReadahead(w)` (proxy-side): a ranged
      read miss fetches the touched range extended forward by w. Measured: `+ra64` beats the 1 MiB baseline on
      BOTH random (~10x less fetched) and scan (faster) even without kernel readahead. See JOURNAL 2026-07-18
      "demand-fill readahead window + production enable-knob". REMAINING (optional): adaptive
      sequential-detection sizing instead of a fixed window.
- [x] **Production enable-knob — DONE (2026-07-18).** `wire.BlockEnvOpts()` (env `CORNUS_BLOCK_COHERENCE` =
      subhash/defer/subfill, `CORNUS_BLOCK_READAHEAD` = e.g. 64k) wired into every ServeBlockProxy /
      ServeBlockServer production call site; both endpoints must set the same coherence bits (HELLO
      negotiation intersects). Empty env = classic default. `TestBlockEnvOpts` covers parsing. Recommended DB
      starting point: `CORNUS_BLOCK_COHERENCE=subhash,subfill` + `CORNUS_BLOCK_READAHEAD=64k` on server AND
      the deploy caller.
- [x] **DiskStore-backed sqliteab measurement — DONE (2026-07-18).** Threaded a store factory through the
      harness; `TestSQLiteCoherenceModes` runs mem+disk x all 6 modes (`-race`, all pass). Findings: demand-fill
      fetch reduction is IDENTICAL on disk (~130x, caching works); coherence write throughput is disk-I/O-bound
      so the modes give only +3-5% (vs +20% on mem) but still cut ~5x allocations; DiskStore + pure demand-fill
      + page-by-page scan is pathologically slow (fsync-per-page) so readahead matters even more on disk. See
      JOURNAL 2026-07-18 "DiskStore validation". Benchmarks: `BenchmarkCoherenceFullDisk_*`; disk rows in
      `TestReadAmplification`.

## Sequential read/write optimization (2026-07-18) — DONE

- [x] **Adaptive readahead.** `WithReadahead` is now an adaptive CAP: `blockProxyFile` grows the demand-fill
      prefetch window on a sequential read and resets it on a jump. Random reads keep the full 7 KiB/query
      demand-fill win AND scans get the readahead round-trip collapse — no tradeoff, no per-workload tuning.
      See JOURNAL 2026-07-18 "sequential read/write optimization".
- [x] **Sequential-write read-back removal.** A full-block write hashes the write buffer directly
      (`unitHashCovering`) instead of reading the block back from the authoritative file. Always on (pure win);
      removes a read syscall + whole-block page-cache read per full-block write. (For sequential-write
      THROUGHPUT the batched send path is the main lever, already shipped; writes are transport-bound
      ~750 MB/s.)
- [x] **Concurrent caller / write pipelining — DONE (2026-07-18).** Reads and writes use separate bounded
      16-slot paths; fsync/setattr drain writes, and shared sequence/handle state is locked. See
      `remote-9p-block-cache.md` for the completed implementation and tests.

## Speculative read-ahead (2026-07-18) — DONE

- [x] **Speculative (background) prefetch.** Demand path is pure (touched range only); a sequential read
      launches a bounded BACKGROUND prefetch of the next range so the reader never blocks on the round-trip.
      Measured 27x faster sequential scan under 2 ms link latency (16.9s -> 0.62s), random reads unaffected.
      `CORNUS_BLOCK_READAHEAD` is now the prefetch-distance cap. See JOURNAL 2026-07-18 "speculative
      (background) read-ahead". REMAINING (optional): classic whole-block prefetch; single-flight to dedup a
      demand-catches-up duplicate fetch.

## Prefetch follow-ups (2026-07-18) — DONE

- [x] **Single-flight dedup + classic-mode prefetch.** `fetchSF` collapses a demand read catching up to an
      in-flight prefetch (exact for classic, best-effort in demand-fill; a ~20 MiB scan prefetched 24.8 MiB,
      not ~2x). Prefetch now works in classic mode too (whole-block), though the win there is small (~1.1x —
      classic already batches into whole blocks). See JOURNAL 2026-07-18 "prefetch: classic-mode + single-flight".

## Concurrent caller (2026-07-18) — reads DONE

- [x] **Concurrent READ handling at the caller.** blockServer dispatches opRead/opReadRange/opStatBlock to
      bounded goroutines (16); mutating ops stay serial (coherence unchanged). Guarded handle/seq maps (`mu`),
      serialized wire writes (`writeMu`), pooled read scratch. Measured 11.6x on 32 concurrent cold reads at
      500us/read storage cost; neutral on fast local storage. Coherence green mem+disk `-race`. See JOURNAL
      2026-07-18 "concurrent caller".
- [x] **Concurrent WRITE path (2026-07-18).** `opWrite` now dispatches to bounded goroutines (`writeSem`, 16);
      fsync/setattr barrier-drain in-flight writes (`writeWG`); `dirty` map `mu`-guarded; write-hash path uses a
      pooled scratch. Coherence is enforced at the proxy (seq-gate + verify + drop), which already tolerated
      concurrent writes, so this is pure parallelism — no per-block locks needed. Measured 11.1x on 32 concurrent
      distinct-block writes at 500us/write; neutral on fast storage. Coherence green mem+disk `-race`. See JOURNAL
      2026-07-18 "concurrent caller, part 2".
- [ ] **Per-sub-block seq-gating (optional warmth).** Concurrent SAME-block writes currently drop+refetch that
      block (cold, correct). Keying the proxy `admitWrite` by `(block, subBlock)` would keep them warm. Warmth
      optimization only, not correctness.

## July 2026 consolidation follow-ups (second pass)

- [ ] Add an `x-cornus-knative` Compose extension, multi-revision traffic splitting/tags, and supported Knative sidecar/volume/network interoperation. — *source: JOURNAL Knative Serving descriptor*
- [ ] Vendor the Knative Serving/Kourier E2E installation so the opt-in Knative scenario does not require internet access. — *source: JOURNAL Knative Serving descriptor*
- [ ] Add MPL Exhibit A headers to modified `third_party/yamux/*.go`, ship `THIRD_PARTY_NOTICES.md` beside released binaries, and wire the license scanner into CI. — *source: JOURNAL dependency license audit*

## Local image-store re-export follow-ups (2026-07-19)

- [x] Live-daemon E2E scenario for `CORNUS_REGISTRY_SOURCE=docker-daemon`: build → `docker load` → pull-through `/v2/*` → deploy with no registry round-trip, on the docker E2E target. DONE (2026-07-19) — `e2e/scenarios/registry-source-docker-daemon.star` (docker-only self-skip; `build_upload` through the server → daemon load; asserts /v2 manifest re-export 200, empty `_catalog`, write→405, and a dockerhost deploy that runs without a pull), registered in the Makefile `SCENARIOS` list. Parses + resolves under the e2e `--check`; a live run needs a real dockerd (opt-in harness, outside `go test ./...`). — *source: JOURNAL "local Docker/containerd image-store re-export"*
- [x] Translate the new `CORNUS_REGISTRY_SOURCE` / "Reusing a local image store" reference material into `docs/ja` and `docs/zh`. DONE (2026-07-19) — propagated across all 5 changed English pages × 2 locales (server-env-vars, storage-backends, deploy-backends reference; server-and-registry, build-engine architecture) via the `translate-documents` skill; audit passed, `npm run docs:build` clean, JA half-width / ZH full-width conventions preserved, cross-page anchors locale-prefixed. — *source: same*
- [x] Optional hardening: return 405 for registry write verbs (blob/manifest PUT/DELETE, uploads) while a re-export source is active. DONE (2026-07-19) — subsumed by making the content store optional (nil) in pure re-export mode: `Registry.readOnly()` rejects all write verbs with 405, and no throwaway store is created. See JOURNAL "Registry re-export: drop the CAS in pure re-export mode". — *source: same*
