# Project To-Dos

Items extracted from JOURNAL.md during `good-sleep` consolidation, plus open follow-ups. Each
item should be resolved or removed once addressed.

Completed items are cleared periodically into a "TODO wrap-up" entry in JOURNAL.md (the closure
index); the last sweep was 2026-08-01, which moved 225 checked entries into
"TODO wrap-up: 225 closed entries cleared into the closure index (2026-08-01)" and retired the
section headings that held nothing else. What remains here is 67 open and 15 partial entries.

> **Before acting on any entry here, re-verify it against the tree.** This list is stale in BOTH
> directions and measurably so: on 2026-07-29, 42 of 45 spot-verified rows were already done,
> including every row of the tier a previous triage called "verified live, all small". Two of the
> three that were still open turned out to understate the defect. Verify by RUNNING, not by
> re-reading; verify the proposed REMEDY separately from the observation, because the remedy decays
> faster; and scrutinize hardest the rows you are about to CLOSE, since a false "still open" costs
> one re-check while a false "done" retires real work and nobody looks again. The reusable form of
> this is `.agents/docs/LTM/verification-and-audit-methods.md`.

## Open Items

- [x] `WorkloadDetail`'s instances table is the one page `table.grid` still outside a
      `.table-scroll` wrapper. **DONE 2026-08-02**: wrapped while restructuring that page into
      sections. The Overview-scoped "in its own scroller" test was not extended to the detail
      page — the detail page's own test asserts the section's content, not the wrapper.

- [x] A tiling drag/drop test passes for the wrong reason: jsdom has no `DragEvent`.
      `web/src/views/views.test.tsx` "moves (re-tiles) a pane when a tab is dropped on another
      tile's edge" calls `fireEvent.dragOver(el, {clientX: 50, clientY: 92})`, but
      `@testing-library`'s `createEvent` falls back to `window.Event` when the constructor is
      missing, and a plain `Event` silently drops `clientX`/`clientY`. `dropZone()` then computes
      `NaN`, every comparison is false, and it falls through to its final `return "bottom"` — so
      the test would pass for ANY coordinates, including a centre drop that must stack instead.
      Fix by hand-building `new MouseEvent("dragover", {clientX, clientY, bubbles: true})` (jsdom
      does carry coordinates on `MouseEvent`), then re-verify by asserting a centre drop stacks.
      Same trap applies to any future pointer-coordinate test: `PointerEvent` is missing too.
      — *source: JOURNAL 2026-08-01 — Tiling: the edge-split overlays stop stealing clicks*
      **DONE 2026-08-02** (rediscovered independently while fixing the same-tile tab drag; the
      diagnosis here was exact). Remedy differs in form: `dragAt()` in `views.test.tsx` uses
      `createEvent` + `Object.defineProperty` for the coordinates rather than a hand-built
      `MouseEvent`, matching the `dropOn()` helper the Files suite already uses for `shiftKey`.
      All four tiling drop tests now carry real coordinates against a stubbed rect, and the two
      CENTRE tests were the acceptance criterion named above: they had been relying on jsdom's
      zero rect (`dropZone()` returns "stack" for it), so they would have passed aimed anywhere.
      Neutralized by re-aiming both at `EDGE_POINT.right` — both fail. See
      JOURNAL 2026-08-02 — Tiling: a tab can be dragged out of its own tile.

- [ ] `CORNUS_AGENT_DIR` does not isolate the token cache, contradicting its own doc comment.
      `pkg/tokencache/tokencache.go` `runtimeDir()` checks `XDG_RUNTIME_DIR` first, so the agent-dir
      override only applies when `XDG_RUNTIME_DIR` is unset — never true in a login session. The
      comment claims "an isolated agent gets an isolated cache". Decide which wins (probably
      `CORNUS_AGENT_DIR`, since it is the explicit override) and fix the code or the comment.
      — *source: JOURNAL 2026-08-01 — What deleting an enrolled SSH key does to an already-minted session*

- [ ] E2E scenarios write real session tokens into the developer's `$XDG_RUNTIME_DIR/cornus/tokens`.
      `e2e/scenarios/auth-ssh-key.star` sets neither `XDG_RUNTIME_DIR` nor `CORNUS_TOKEN_CACHE`, so
      it pollutes the ambient cache (confirmed by timestamp). `auth-ssh-key-session-cache.star` shows
      the fix: redirect `XDG_RUNTIME_DIR` at a `temp_dir()`. Apply the same to every scenario that
      mints a session, and consider having the harness default it for all scenarios.
      — *source: JOURNAL 2026-08-01 — What deleting an enrolled SSH key does to an already-minted session*

- [ ] The license scan does not set `CGO_ENABLED`, but every released binary is built with
      `CGO_ENABLED=1` (`.github/scripts/build-release-binary.sh`). cgo changes Go file selection, so
      a dependency reachable only under cgo would be absent from both the policy scan and
      THIRD_PARTY_NOTICES.md. The platform union (2026-08-01) fixed the GOOS/GOARCH axis but not this
      one. Check whether `CGO_ENABLED=1 go list -deps` resolves additional modules for the five
      released targets, and if so fold it into `--platforms` (it needs no C toolchain: `go list`
      does not compile).
      — *source: JOURNAL 2026-08-01 — THIRD_PARTY_NOTICES.md is now the union of the released platforms*

- [ ] `dockerhost.Logs` violates the `deploy.Backend.Logs` framing contract for TTY containers.
      `pkg/deploy/dockerhost/dockerhost.go:589` is a bare `io.Copy` of Docker's log stream with no
      TTY handling, but Docker returns UNFRAMED bytes for a TTY container, while the contract at
      `pkg/deploy/deploy.go:238` says implementations MUST write stdcopy-multiplexed frames. The
      kubernetes backend frames unconditionally (`kubernetes.go:1739`), so the two backends disagree
      about the same container and no `Content-Type` is correct on both (see `streamContentType`).
      Latent until `docker run -t` actually produced a TTY container. Decide which side is right —
      re-frame in dockerhost, or narrow the contract to "non-TTY only" and make the proxy ask the
      backend — and make the other side match.
      — *source: JOURNAL 2026-08-01 — Container streams were all announced as raw-stream*

- [ ] `kubernetes.Attach` ignores the container's TTY. `kubernetes.go:2005` hardcodes `TTY: false`
      and always applies `muxWriters`, justified by a comment saying "cornus deployments never
      allocate a container TTY" — stale, since `kubernetes.go:2311` now sets `TTY: spec.TTY` on the
      pod container. So `docker attach` to a TTY deployment on kube gets stdcopy-framed bytes where
      the client expects a raw PTY stream.
      — *source: JOURNAL 2026-08-01 — Container streams were all announced as raw-stream*

- [ ] Histogram metrics are recorded but unreachable from PromQL. The SDK bridge delivers
      `http.server.request.duration` and friends into `metrics_histogram`, and the store's PromQL
      profile rejects them ("histogram selectors require canonical ..."; the `_bucket` / `_sum` /
      `_count` spellings do not resolve either). `cornus observe query` (SQL) is the documented
      workaround. Worth revisiting whether cornus should emit the canonical form the engine wants,
      or whether this is an upstream imbh-go gap to report.
      — *source: JOURNAL 2026-07-27 — Workload and server resource metrics on every backend*

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

- [ ] E2E-cover traces and metrics with an instrumented workload. The observability suite proves the
      LOG path end to end, but the trace/metric surfaces are only asserted empty (`observe traces`
      reports "no matching traces"; an unresolvable PromQL metric returns a diagnostic). Nothing in
      the suite runs an app that actually exports spans, so the ingest-to-query path for those two
      signals — including the Tempo datasource shaping — rests on unit tests alone.
      — *source: JOURNAL 2026-07-26 — Session summary: built-in workload observability*

- [ ] Remove the root-owned `.agents-workspace/tmp/fr-data/` left by a manual repro (the
      containerized server runs as root, so its files are not removable by the unprivileged user).
      The three stranded 9P mountpoints alongside it were cleared by the user on 2026-07-26.
      — *source: JOURNAL 2026-07-26 — E2E for the flight recorder*

- [ ] Ship caretaker flight records off a **kubernetes** pod. The host backends are covered (their
      caretaker scratch dirs are binds of directories under the server's data dir, so records land
      where the server can read them), but a pod caretaker writes to its own filesystem and the
      stream is lost with the pod. Fixing it properly means shipping records over the caretaker
      connection. — *source: JOURNAL 2026-07-26 — Server/caretaker activity log*

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
      **REFRAMED (2026-07-28) after investigation — this item is NOT about `e2e/container/` and the premise is wrong in two of three parts. (1) The runner image ALREADY carries the CNI reference plugins (`Dockerfile:257-270` extracts bridge/macvlan/ipvlan/portmap/host-local/static/loopback into `/opt/cornus/cni`; the Multus staging step doubles as the containerd/bare plugin dir). Verified by `ls` inside the built image. (2) The containerd E2E leg does NOT need host networking: cornus, containerd, the shims and the workloads all share one netns, so `hostrun`'s CNI bridge and portmap DNAT land exactly where the harness dials from — and `pkg/hostcheck/hostcheck.go:194` only raises the `host-network` check when `Env.Translating` is set, i.e. only in the SEPARATE-container topology. The leg passes: `make e2e-container E2E_TARGETS=containerd` -> `all 11 scenario(s) passed`. (3) `--network host` on the runner would actively BREAK the other legs — the nested dockerd would land in the host netns and collide with the host daemon's `docker0`, its `kind` bridge and its DOCKER/DOCKER-USER chains. That experiment was deliberately NOT run, because it would have damaged the dev host's Docker. WHAT REMAINS, in different files: the RELEASED root `Dockerfile` installs no CNI plugins, so `cornus serve` in its own container against a HOST containerd still needs them shipped (or a `-containerd` image variant); decide whether hostcheck's `host-network` warning becomes fatal there; and extend `e2e/scenarios/server-in-container.star` (docker-only today, its header says why) to containerd. The runner image is already equipped to host that scenario once written.**

- [ ] Implement detached `cornus compose exec`: plumb Docker's detach option through the dockerhost backend and define a safe Kubernetes lifecycle rather than returning from an attached SPDY stream. — *source: JOURNAL 2026-07-12 — compose exec*
- [ ] Enable GitHub Pages with GitHub Actions as the repository Pages source so the `docs.yml` deployment can publish the VitePress site. — *source: JOURNAL 2026-07-12 — VitePress user-reference docs site*
- [ ] Design client-to-caretaker trace unification at the Apply/relay boundary, using propagated
      context or span links without falsely parenting the pod-scoped persistent caretaker connection
      under one CLI invocation. — *source: JOURNAL 2026-07-12 — Client-side distributed tracing and filled tracing gaps*

- [ ] Verify the Helm chart's opt-in `tailscaled` sidecar (`tailscale:` values block) against a live
      cluster — validated so far only via `helm lint`/`helm template`; whether Funnel actually works
      over the shared control-socket `emptyDir` in userspace mode is unconfirmed. — *source: JOURNAL
      2026-07-14 — Tunnels/hub docs restructuring... Tailscale Helm sidecar*
- [ ] Client-sourced credentials on the host backends. Now that dockerhost/containerdhost/barehost
      are `AttachingBackend`s, `ApplyWithAttachments` is the natural home, but delivering a
      credential is a feature port rather than a merge: endpoint deliveries bind a loopback port and
      inject resolved env into the APP container, file deliveries need a scratch directory shared
      with it, and deploy-time env deliveries have no Secret indirection to hide behind the way they
      do on kubernetes (a host backend's container env is readable by anyone who can talk to the
      daemon). Currently rejected with a per-backend message. — *source: JOURNAL 2026-07-25 —
      unified companion caretaker*
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
      **NARROWED (2026-07-28): sub-items 1, 2, 3 and 5 are all marked DONE inline above; only **(4) cert issuance/rotation wiring** remains, and that is ops/PKI provisioning rather than code — the mTLS mechanism already exists. Treat this entry as that one item.**
- [ ] Embedded-gossip hub Store backend (deferred third option alongside Redis/KubeStore) — *source:
      JOURNAL 2026-07-05 — Multi-replica hub PoC (Redis) SHIPPED + VALIDATED*
- [ ] GHCR release follow-ups, blocked on repo creation: push the repo; adjust the hardcoded
      `ghcr.io/moriyoshi/cornus` defaults (Helm values, `deploy/k8s/cornus.yaml`, README) if the repo
      lands under an org (the workflow derives the name from `github.repository_owner`); tag `v0.1.0`
      so the pinned manifest ref and chart appVersion resolve; make the GHCR package public — *source:
      JOURNAL 2026-07-05 — Pre-built GHCR images for k8s installs*
      **PREMISE CORRECTED (2026-07-28): the repo is NOT blocked on creation — `moriyoshi/cornus` exists on GitHub with ~9.4 MB pushed and `origin/main` live. What actually remains: tag `v0.1.0` so the pinned manifest ref and chart appVersion resolve, make the GHCR package public, and adjust the hardcoded `ghcr.io/moriyoshi/cornus` defaults only IF the repo ever moves under an org (it has not).**
- [ ] CI watch items: pin an explicit Helm `version:` in `ci.yml` if `azure/setup-helm@v5` ever fails
      its GitHub-API latest-version lookup; confirm the first Dependabot github-actions run is a no-op
      (everything is already at latest) — *source: JOURNAL 2026-07-06 — CI workflow hardening*

- [ ] `.agents/skills/web-screenshot/SKILL.md` claims "Browsers are already cached on this machine
      ... there is no install step". The npx-provisioned driver part holds, but
      `~/.cache/ms-playwright` was empty on 2026-08-01 and `shot.mjs` died on Playwright's
      "Executable doesn't exist" banner until `npx --yes playwright install chromium` ran (111 MB).
      Either soften the sentence to "run `npx playwright install chromium` once per host" or have
      `shot.mjs` detect the missing browser and self-install. One-line fix; left undone because it
      was outside the change that found it.
      — *source: JOURNAL 2026-08-01 — Web UI: sidebar replaced by a fixed page header*

- [ ] Give the Files screen an in-app affordance for its file actions. New folder, upload, rename,
      copy, download and delete exist ONLY as contextual command-palette entries — there are no
      buttons — and the one line that said so (the `.workspace-hint`) was removed on 2026-08-01 by
      request. A user who has not read the docs now has no in-app path to those actions. Options: a
      small "⌘" / "?" affordance in the pane sub-header that opens the palette, a right-click context
      menu on a row, or per-row action buttons like the Overview workload table already has.
      — *source: JOURNAL 2026-08-01 — Files and Terminal lost their in-screen titles and hints*

- [x] Decide whether the Files workspace should get new-pane / close-pane binds. The Terminal's
      `prefix %` / `prefix "` split binds were carried over on 2026-08-01, but its `c` (new pane) and
      `x` (close pane) could NOT be: `c` and `x` already mean Copy and Delete on the Files screen.
      **DONE 2026-08-02**: the product call went the other way round — the PANE binds are shared
      across both workspaces letter for letter (`% " C c x`, plus `s` and `Ctrl+O`), and the two
      file actions moved to vi's `y` (yank) and `d`. Note which way the risk falls: an old
      `prefix x` now closes a tab instead of deleting files, so the stale habit misfires into the
      harmless action. `views.test.tsx` compares the two screens' bind maps against each other
      rather than against a list, so either side drifting fails.
      — *source: JOURNAL 2026-08-01 — Files workspace: the Terminal's pane-split binds carried over*

- [~] **Directory copy: DONE. Large-file copy: still open.** `Server.FsCopy`
      (`cmd/cornus/internal/webbff/fs.go`) now walks a directory tree server-side —
      `FsMkdir` + `FsList` per level, one bounded file copy per entry — so folders copy
      anywhere in the virtual namespace and the drag-and-drop refusal is gone. Guards: a
      folder into its own subtree, a bare mount root, `maxCopyDepth` 32, and a truncated
      listing (copying what survived truncation would silently produce a partial tree).
      Symlinks that do not resolve to a readable file are named in the response's
      `skipped` and stepped over. Covered by `TestExplorerCopyDirectoryLocal`,
      `TestExplorerCopyDirectoryRefusals`, `TestExplorerCopyDirectorySkipsOddSymlinks`.
      STILL OPEN: every file still rides through memory bounded by `maxEditableFileSize`
      (10 MB), so a tree containing one big file fails partway and leaves what it already
      wrote. Fixing that means streaming (`FsWrite` takes `[]byte`; the container side
      builds a tar in memory), which is a wider change than the recursion was.
      — *source: JOURNAL 2026-08-01 — Files: multi-selection, cursor-navigable; closed in
      part by JOURNAL 2026-08-01 — BFF: FsCopy copies directories*
      **DONE 2026-08-02** (streaming landed; see JOURNAL "the 10 MB cap is gone").
      `cmd/cornus/internal/webbff/fsstream.go` carries `openStream`/`createStream`/
      `streamFile`: no buffering, exact tar framing via `copyExactly`, a local destination
      published by temp-plus-rename, and pipe ownership that surfaces the upstream error
      through `Close`. `FsOpen`'s container branch now gates its cap on `bounded`, so a
      container DOWNLOAD is no longer capped either. The editor keeps the 10 MB bound,
      which is where it belongs.
      **UPLOADS DONE 2026-08-02** (second pass). `handleFsUpload` streams through
      `uploadStream` -> `createStream` + `copyExactly`, so the drag gesture answers the
      same way whichever side of the window the file came from. Both body shapes carry a
      length already (a browser `File` body sets Content-Length; a multipart part carries
      `hdr.Size`), and a body of UNKNOWN length is a 411 rather than a silent spool — the
      destination is framed with the size before it sees a byte. Multipart spills past
      1 MiB to net/http's temp file instead of RAM.
      `TestUploadIsNoLongerCapped`, `TestUploadIntoContainerIsFramed`,
      `TestUploadWithoutContentLengthIsRefused`; the read-only loop in
      `TestExplorerHonoursReadOnlyBind` gained the upload row. Neutralized both halves.
      **INCUS: the READ direction is DONE 2026-08-02**, the write direction is not and is
      not a "not yet". `packOne` streams through an unlinked temp file
      (`spoolToDisk`), so a CopyFrom no longer allocates each file whole in the server
      (`TestCopyFromDoesNotBufferWholeFiles` measures `TotalAlloc`; neutralizing it
      allocated 165 MB for a 64 MB file). CopyTo still buffers ON PURPOSE:
      `incus.InstanceFileArgs.Content` is an `io.ReadSeeker` the client re-seeks from
      `GetBody` to replay a retried request, and `http.NewRequest` length-frames a body
      only for `*bytes.Reader` and friends — handing it an `*os.File` silently turns the
      upload chunked. That is a wire change against a daemon `go test ./...` cannot reach,
      so it belongs behind `make e2e-incus`, not behind a guess.
      STILL open: the container WRITE publishes in place, so an aborted transfer there
      loses the previous content (the local side is safe) — fixing it needs a temp name
      plus a rename INSIDE the container, i.e. an exec, which would make a plain write
      depend on a shell the image may not have.
      Superseded note follows.
      **STILL OPEN after 2026-08-02.** The FS operation planner landed and did NOT close
      this: `copyFileTo` and `FsCopy`'s single-file branch both still `io.ReadAll` under
      `maxEditableFileSize`, and `FsOpen`'s container branch caps unconditionally rather
      than on `bounded`, so a container DOWNLOAD is capped too. What did change is that
      the cap now bites less often — a bind-mounted path is served straight off the
      developer's disk, and a same-filesystem move is an `os.Rename` with no size limit at
      all — which makes the remaining cases narrower but no less wrong. Streaming needs
      `create(path, size, mode) (io.WriteCloser, error)` with the tar framed from a size
      the reader itself obtained (a symlink's `Lstat` size is the link text's length, so
      the listing's size is the wrong number), `io.CopyN` plus an EOF check so a growing
      file is an error rather than a silently truncated tar, a zero-pad for a shrinking
      one (`tarcopy.packFile` is the in-tree model), and a temp-sibling-plus-rename so an
      aborted transfer never publishes a half-written file — `tarcopy.go:321-328` removes
      the destination BEFORE extracting, so today an aborted copy destroys the old
      content too.
- [x] A dirty Files editor is discarded without a prompt when its tab is CLOSED. `Reload`
      asks (`confirm("Discard unsaved changes?")` in `FileEditorPane.tsx`), but the tab ✕
      (`closePaneById` -> `closePane`) does not, and neither does `navigateTo` — which is
      why both call `forgetDraft()`. Unsaved text now survives moves and view changes
      (`web/src/views/files/drafts.ts`), so closing is the remaining silent loss, and it is
      more visible than before precisely because everything else stopped losing it. Guard
      it with the same confirm, reading `paneActions.get(id)?.dirty()`. Note `navigateTo`
      may be unreachable for an editor pane from the current UI (`PaneCrumbs` renders only
      for browse panes) — verify before writing a test for that half, or the test will pass
      for the wrong reason.
      — *source: JOURNAL 2026-08-02 — Files: unsaved editor text survives a re-tile*
      **DONE 2026-08-02.** `requestClosePane` in `Files.tsx` now gates the tab ✕ behind
      `confirmModal` (the in-app dialog, not the native `confirm()` `Reload` still uses),
      keyed on `dirtyOf(id)` — so a clean pane still closes on the click itself. The
      `navigateTo` half was VERIFIED UNREACHABLE as the entry suspected and deliberately
      left alone: `subHeader` returns null for `pane.data.open`, so no `PaneCrumbs` is
      rendered for an editor, and `fileCommands` offers an edit pane only `files:save`
      plus the splits — nothing calls the registered `go`. A prompt there would be
      untestable through the UI. See JOURNAL 2026-08-02 — Closing a pane asks first.
- [ ] Check the other pane types against the "no irreplaceable state in the pane component"
      rule. A pane is rebuilt by every re-tile, so anything held in component state is lost
      on a move. Terminal panes are fine by construction (`sessionId` lives in `pane.data`,
      so a rebuild reattaches); the editor was not, and lost the user's text
      (fixed 2026-08-02). Unverified: the browse pane's multi-selection and scroll position,
      and the image viewer's state — all live in their components. Selection loss is cheap
      compared to text loss, but it is the same defect. Verify by DOING the move, not by
      reading: `keeps an editor's unsaved text when its tab is dragged out to a new tile` in
      `views.test.tsx` is the pattern.
      — *source: JOURNAL 2026-08-02 — Files: unsaved editor text survives a re-tile*
- [ ] The web suite cannot assert layout, and there is no repeatable harness that can.
      jsdom implements no layout, so every height/alignment/overflow claim about the SPA is
      unfalsifiable under `npx vitest run` — `getBoundingClientRect()` returns zeros. Aligning
      the Files editor bar to the breadcrumb lane (both must be 27.59px) was therefore verified
      with a THROWAWAY playwright probe: a hand-written page carrying the real markup shapes,
      linked against `web/src/styles.css`, measured in headless Chromium, then neutralized by
      re-injecting the old declarations. It worked and it was cheap, but it left no guard — the
      next edit to `.file-pane-editor-bar` or `.stack-subheader` can silently desynchronize the
      two lanes and nothing will say so. Consider a small opt-in layout-metrics check (playwright
      against the built `pkg/webui/dist`, a handful of paired-element assertions, out of the
      default `vitest` path the way the E2E harness is out of `go test ./...`). Verify any
      proposal by breaking a metric on purpose first; a layout check that cannot fail is the
      exact failure mode being described here.
      — *source: JOURNAL 2026-08-02 — Files: the editor bar shrinks to the breadcrumb lane's
      height*
      **SECOND OCCURRENCE 2026-08-02** (terminal tab badges resizing the tab bar, same 27.59px
      number). Still open, but the probe no longer needs an install: playwright's *package* is
      absent here while its downloaded chromium is present
      (`~/.cache/ms-playwright/chromium-1234/chrome-linux/chrome`), and node 26 has a global
      `WebSocket`, so ~90 lines drive it over CDP — spawn with `--remote-debugging-port`, `GET
      /json/list`, `Runtime.evaluate` returning rects/computed styles by value. Any harness built
      from this must avoid two silent failures, both measured: a file:// `<link>` to a sibling
      directory is NOT fetched (the page renders unstyled and reports browser defaults — 21px
      rows, 16px text — instead of erroring), so inline the stylesheet; and
      `--force-prefers-color-scheme` is ignored (the dark run returns the light tokens), so use
      CDP `Emulation.setEmulatedMedia`.
      — *also: JOURNAL 2026-08-02 — Terminal tabs: the activity badge stops resizing the tab bar*
- [ ] A Files editor CLOBBERS text typed before its first read lands. `FileEditorPane` renders
      the editor (and an enabled-looking bar) immediately over an empty document, while
      `createResource` is still fetching; when the text arrives, the seeding effect fires
      because `filePath() !== lastSeeded` and calls `setContent(text)` over whatever the user
      has typed. The window is one `/fs/content` round-trip — negligible on a local root,
      not on a workload exec FS over a slow link. Same family as the draft loss fixed on
      2026-08-02, and now the LAST silent one in that pane. Likely remedy: treat "the user has
      touched this document" as a reason not to seed (a dirty flag set by the first `onChange`
      that is not the seed itself), or hold the editor read-only until the load settles — the
      first keeps the pane responsive, the second is honest about what it is showing. Verify by
      DISPATCHING into the editor before the resource resolves, not by reasoning about the
      window; the race is real enough that it changed the result of a test written without it
      (`openEditor()` in `views.test.tsx` waits for the file's own text for exactly this
      reason, and the test failed in suite order and passed in isolation before that wait).
      — *source: JOURNAL 2026-08-02 — Closing a pane asks first*

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
  was fixed on 2026-07-28 and proven live through the proxy on 2026-08-01; see the 2026-08-01 TODO
  wrap-up in JOURNAL.md for its closure record.

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

- [ ] E2E follow-ups from the compose flag batch (2026-07-11). Live docker E2E
      added + passing for profiles, depends_on gating, and `down --volumes`
      (`compose-profiles.star`, `compose-dependson.star`, `compose-down-volumes.star`).
      STILL OPEN: (a) `build --no-cache` / `--build-arg` E2E is CI-only — the
      in-process build engine needs a rootless-userns / privileged / dind stack, so
      it cannot run in the plain docker sandbox (unit-tested meanwhile); (b) DONE
      (2026-07-27): `compose-down-volumes.star` is now backend-parametric and was run
      live on ALL FOUR targets (docker volume / kube PVC / containerd + bare host dir),
      and is registered in `SCENARIOS_CONTAINERD` + `SCENARIOS_BARE` (and their
      `entrypoint.sh` twins); (c) PARTIAL (2026-07-27): `--env-file` now has live E2E
      (`compose-env-file.star`: repeatable + later-wins, process env overriding it,
      sibling-`.env` discovery and its REPLACEMENT by `--env-file`, and a missing file
      failing loudly). `logs --until` is still unit-tested only.

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

## July 2026 consolidation follow-ups

- [~] Add a per-record flock shared by barehost server and shim read-modify-write cycles, then reconsider making CORNUS_BARE_SHIM the default. Also design companion reboot recovery against the rebuilt app netns. — *source: JOURNAL 2026-07-15 to 2026-07-17 barehost milestones*
      **LOCK DONE (2026-07-28): `pkg/deploy/barehost/recordlock_linux.go` — an flock on `<recordDir>/record.lock` (a stable path; the record itself is rename-published) plus an `updateRecordAt` read-modify-write primitive both the server and the shim go through. Two races closed, both reachable in the DEFAULT in-process mode, not only under the shim: a Stop landing inside `restartInstance` was clobbered by the supervisor writing back its pre-stop copy (an explicitly stopped workload came back on the next reconcile), and two writers sharing `record.json.tmp` could truncate `record.json`, which `listRecords` silently skips — the instance disappears from Status/List. `CORNUS_BARE_SHIM` default: NOT flipped, see JOURNAL 2026-07-28 for the remaining prerequisites (no shim E2E leg, unmonitored shim liveness, no stable-run backoff reset, mixed companion supervision). Companion reboot recovery is still open and is the sub-item below.**
- [ ] Companion reboot recovery against the rebuilt app netns (split out of the item above). After a host reboot `recoverInstance` mints a FRESH random netns pin (`netns.NewNetNS(netnsDir)`) for the app instance, but a companion's `config.json` still names the dead pin and nothing rewrites it, so `reconcile` fails its relaunch and the deployment comes back without egress policy, client-local mounts, or telemetry export until the user re-applies. Shape: two-phase reconcile (recover apps, then per companion resolve `instanceName(rec.App, rec.Replica)`'s fresh `NetNS`, `rewriteNetnsPath` its bundle, relaunch), skip companions whose app failed, and decide per role — a mount caretaker's 9P peer is a client that no longer exists after a reboot, so it likely should NOT be resurrected. — *source: JOURNAL 2026-07-28 barehost record lock*
- [ ] Investigate rshared to rslave sidecar-mount content propagation in nested DinD; current bare and containerd companion coverage proves wiring but not mounted-file content. — *source: JOURNAL 2026-07-17 barehost companion E2E*
- [~] Run socks5-ingress.star and socks5-ingress-tls.star live on docker and kube, then add a native ingress E2E with client KUBECONFIG and an ingress controller. Plain socks5-ingress.star passed on docker on 2026-07-20; TLS on docker and both kube/native legs remain. — *source: JOURNAL 2026-07-18 ingress via SOCKS5 conduit*

## Block-protocol DB write path (2026-07-18) — perf follow-ups

Context: `pkg/wire/sqliteab` runs a real SQLite workload over the block proxy in-process (SQLite ->
psanford VFS -> p9 client -> ServeBlockProxy -> yamux fork -> ServeBlockServer). See JOURNAL
2026-07-18 "real SQLite workload over the block proxy". The per-small-write allocation amplification is
FIXED (52 MB/op -> ~4 MB/op, +~75% insert throughput; `blockServer` scratch reuse + `MemStore`
in-place/cap-preallocated RMW). Remaining, evidence-backed:

- [ ] **DiskStore fsync-per-write.** Follow-on found by the above: each `writeData` and `writeIndex`
      fsyncs, making the on-disk cache ~17x slower per insert transaction than MemStore. Batching or
      deferring the sidecar sync (without weakening the fsync-before-presence-bit invariant) is the next
      real lever, but it is a durability redesign, not an allocation fix.

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

## Fresh-eyes engineering review (2026-07-25)

An outside-perspective review of the project as a whole — scope, abstraction boundaries, process,
and default posture — rather than a feature-level audit. The strategic items (S1-S5) are judgement
calls that need a decision from the maintainer, not mechanical fixes; the defects (D1-D6) were
unambiguous and small and are all closed (see the 2026-08-01 TODO wrap-up in `JOURNAL.md`), as are
S1, S3, S4 and S5 — only S2 below survives. Baseline measured at review time: 194k LOC across 873 Go files and 52
`pkg/` packages, 82k lines of test code against 112k of production code, `go build` / `go vet` /
`go test ./...` all clean, 46 of 3,440 functions over 100 lines, exactly one TODO/FIXME comment in
the tree. The craft indicators are strong; the findings below are about judgement and process.

### Strategic — need a decision, not a patch

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
      **DEFERRED (2026-07-27) — to be planned separately; NOT decided and NOT rejected. Left open deliberately. Measurements taken during the TODO sweep, so a planner need not re-derive them:
      - `Backend` has **20 mandatory methods**, splitting almost exactly in half by nature: **lifecycle core (9)** = Name, Apply, Status, List, Delete, Start, Stop, Restart, Close; **Docker-shaped IO (10)** = Logs, Stats, StatPath, CopyFrom, CopyTo, ExecCreate, ExecStart, ExecInspect, ExecResize, Attach; plus ForwardPort (networking).
      - **13** optional capability interfaces now (the review said 11; `HealthReporter` was added by this sweep), discovered by type assertion at **17** call sites in `pkg/server`.
      - The Docker wire-format claim is CONFIRMED and self-documented in the contract, not inferred: "implementations MUST write stdcopy-multiplexed frames (Docker's 8-byte per-chunk stream header) ... backends with an unframed stream (kubernetes) wrap it in stdcopy stdout framing", and stats JSON / archive tar / exec output are each "passed through unchanged".
      - A MIGRATION PATTERN ALREADY EXISTS in-tree: `MetricsSampler` is the backend-neutral counterpart to Docker-shaped `Stats`, and the contract already tells callers to prefer it. `HealthReporter` follows the same shape. So neutral capabilities can be added incrementally without a big-bang refactor.
      - TWO CAUTIONS for the plan: (1) making the 10 Docker-shaped methods optional RELABELS rather than removes — all five backends already implement all ten, so the benefit accrues to a future backend, and S1 decided not to add one; Docker fidelity is also genuinely the product at the CLI edge (`pkg/dockerproxy` serves real docker clients), so something must speak that format. (2) S3 kept the amend-onto-one-commit workflow, so a subtle regression from a cross-cutting refactor of 5 backends + `pkg/server` + the CLI would be **un-bisectable**. Prefer small, individually-verifiable steps over one large change.**

### Scope of the review — what was NOT covered

- [ ] Not audited deeply enough to have an opinion on correctness: the BuildKit solver
      (`pkg/build/builder`), the 9P/block transport (`pkg/wire`, `pkg/blockcache`), and the Compose
      translator (`pkg/compose`, notably the 306-line `translateService`). These are three of the
      highest-risk subsystems and warrant their own adversarial pass. Note also that the tree was
      being edited concurrently during the review — an early `go build` failed on `barehost` mid-edit
      and passed moments later — so all measurements above are a snapshot. — *source: same*
      **REFRAMED (2026-07-28): this is a scope note from the fresh-eyes review, not a defect. It is a real proposal — an adversarial pass over the three highest-risk unaudited subsystems (`pkg/build/builder`, `pkg/wire`+`pkg/blockcache`, `pkg/compose`). Weak corroboration since: two independent defects were found in `pkg/compose` during this sweep (the published-port silent rewrite and the multi-file merge reset semantics), which is the area the note flagged.**

## First-user product review (2026-07-25) — install path, CLI surface, docs

Findings from following the former `README.md` quick start and
`docs/introduction/quick-start.md` literally as a new user, with the shipped release artifacts,
rather than reading the code. Complements the engineering review above: that one asks what the
project should cut, this one asks whether a stranger can get it running. The README rewrite closed
D2, D3, U10, and U11; the pre-release deprecations were **D5**, the translation-tree tax is **S5**,
and the unauthenticated-quickstart posture is **S4**. Every U item except U2 is closed (see the
2026-08-01 TODO wrap-up in `JOURNAL.md`), including U7 and U8, which were the mechanical, doc-layer
half of S4.

Verified working at review time, so the items below are onboarding problems and not engine
problems: `cornus compose up` against a local `serve --storage` with the dockerhost backend built
the former README demo image including a `RUN --mount=type=cache` and a `RUN --mount=type=secret` layer,
pushed it to the built-in registry, deployed it, served traffic on the published port, and
`compose down` removed it cleanly.

### Blocking — a new user hits these before anything works

- [ ] U2. Reconcile the version numbers. Release tag is `v0.0.0`; GHCR image tags are
      `0.0.0` / `0.0` / `latest`; `deploy/helm/cornus/Chart.yaml` carries `version: 0.3.0` with
      `appVersion: "0.1.0"`; the docs say `0.1.0`; a locally built binary prints `dev`. There is no
      single number a user can quote in a bug report, and U1 is a direct symptom. Pick one scheme
      and make the chart, image, tag, and `cornus version` agree. — *source: same*

## Follow-ups extracted during 2026-07-26 good-sleep consolidation

- [ ] Decide whether writable client-local 9P mounts may use an mmap-capable cache
      mode for workloads such as Turbopack, and document the coherence trade-off;
      `cache=none` deliberately cannot support persistent-cache mmap.
      — *source: JOURNAL “Writable client-local 9P mount: missing Lock and O_APPEND write breakage”*
- [ ] Make the Docker API proxy compatible with Docker Engine/CLI 28 and 29 so the
      E2E runner can eventually lift its 27.5.1 pin; compare plugin dispatch and
      foreground attach protocols against a real daemon.
      **MEASURED 2026-07-28 — the blocker is now ONE named thing, not three.** A runner image built
      with `--build-arg DOCKER_VERSION=29.2.1` ran the full docker leg as root: **153 of 154
      scenarios pass**. The single failure is `devcontainer-vscode.star`, which times out at the 10m
      cap. The Dockerfile's pin comment cites three 29.x regressions — compose flag parsing, the
      foreground `docker run` attach the devcontainer CLI drives, and sshd bring-up — and only the
      middle one still reproduces: `dockerd.star` passes in FULL against 29.2.1 (run/ps/inspect/
      logs/stats/cp/exec/stop/rm, compose up/ps/down, interactive TTY exec with window size, and
      `--scale` both directions), as do `dockerd-exit-code{,-nonzero}.star` and `docker-push.star`.
      **The mechanism.** The devcontainer CLI runs `docker run --sig-proxy=false -a STDOUT -a STDERR
      --mount ... --entrypoint /bin/sh`; the server logs `deploy-attach: workload ready; holding
      session until caller disconnects` and `docker run` never returns. A minimal reproducer is
      smaller than that: a PLAIN foreground `docker run --rm alpine echo` against the proxy also
      hangs on 29.2.1 (probe, 2026-07-28), so the trigger is foreground ATTACH itself, not the
      explicit `-a` stream selection or the mount.
      **Why nothing else catches it.** `dockerd-exit-code{,-nonzero}.star` use `run -d` plus `docker
      wait`; `dockerd.star` uses `run -d`. NO scenario drives a foreground attached `docker run`
      except through the devcontainer CLI, which is why one third-party CLI is the whole signal.
      Worth adding a direct foreground-attach scenario regardless of the version question — it would
      have made this a one-line failure instead of a 10-minute timeout in someone else's tool.
      A/B: the same scenario PASSES at 27.5.1 (CI run 30359492738) and hangs at 29.2.1, same image
      otherwise.
      **ROOT-CAUSED AND HALF-FIXED 2026-07-28.** Request tracing on `Proxy.Handler` showed the CLI's
      sequence: create -> attach (NEVER RETURNS) -> `wait?condition=removed` -> start -> wait
      returns. `/wait` delivers the exit code; the ATTACH handler never returns, because `bridge`
      blocks in `io.Copy(client, backendStream)` and the backend tunnel does not EOF once the
      workload is gone. The CLI will not exit while stdout is open (it must not truncate output), so
      it waits on a dead container. FIXED (`pkg/dockerproxy/exec.go`, 27 lines): end the attach on
      `sess.Done()` — the SAME signal `/wait` resolves on in `awaitExit`, so the exit code and the
      output stream can never disagree. `docker run` now exits 0 against 29.2.1; dockerd,
      dockerd-exit-code{,-nonzero} and docker-push still pass.
      **STILL OPEN — a SECOND defect.** In a hand-built harness, attach delivers NO OUTPUT at all,
      with or without a TTY, and output written 3s BEFORE the close never arrived either, so it is
      not truncation from the fix. stdcopy framing was the obvious suspect and `-t` disproved it
      (both modes silent). Unresolved tension: `devcontainer-vscode.star` PASSES at 27.5.1, so attach
      must work there. A runner image at 29 WITH the fix is the deciding measurement.
      SUB-ITEM worth doing regardless: add a scenario that drives a foreground attached `docker run`
      and asserts its OUTPUT and exit code. Nothing does today — `dockerd*.star` all use `run -d` —
      which is why this surfaced as a 10-minute timeout inside a third-party CLI. Also: the attach
      handler swallows `Attach` errors (`if err != nil { return }`, no logging), which is why the
      no-output case gives an investigator nothing.
      **DECIDED 2026-07-28 by measurement: the fix is NOT sufficient and the pin STAYS.** A runner
      image at 29.2.1 WITH the `sess.Done()` fix still times out `devcontainer-vscode.star` at 10m
      (dockerd.star and dockerd-exit-code.star pass). The CLI's full command explains why:
      `docker run --sig-proxy=false -a STDOUT -a STDERR ... --entrypoint /bin/sh alpine:3.20 -c echo
      Container started`. The devcontainer CLI does NOT wait for that run to exit — the container is
      long-lived — it waits to SEE the string `Container started` ON THE ATTACHED STDOUT. So missing
      attach output is THE blocker, the hand-built harness was representative, and the `sess.Done()`
      fix is orthogonal to this case (a long-lived container correctly holds its session).
      LEADING HYPOTHESIS, NOT YET PROVEN: a framing/content-type mismatch. `dockerhost` sends a
      stdcopy-MULTIPLEXED stream for a non-TTY container (`dockerhost.go:535`, `engine.go:684`) and
      the proxy passes it through while always announcing
      `Content-Type: application/vnd.docker.raw-stream` (`writeRawStreamHandshake`); real dockerd
      announces `application/vnd.docker.multiplexed-stream` for a non-TTY attach, which is what that
      content type is for. COUNTER-EVIDENCE that must be explained before acting: plain non-TTY
      `docker exec` DOES stream stdout correctly through the same handshake (dockerd.star). Next step
      is a measurement of the bytes on the wire for attach vs exec at both docker versions, not more
      reasoning.
      **MEASURED 2026-07-29 — framing was the wrong question, and the fault is now localized.**
      Instrumenting the backend stream shows attach on the foreground-run path delivers **0 BYTES**:
      `failed to get reader: failed to acquire lock: context canceled`. It fails at OPEN. Not
      mis-framed, not truncated. The obvious explanation — the handler passes the HIJACKED request's
      `r.Context()` to `Attach`, and `execStart` escapes it by opening immediately while
      `attachContainer` waits for start first — is WRONG: `context.WithoutCancel` changed nothing
      (still 0 bytes). Recorded because it survives on plausibility unless measured.
      **THE NARROWING THAT HOLDS**: `docker attach --no-stdin` on an ALREADY-RUNNING container WORKS
      (78 bytes, clean output, correct framing). So attach is broken only in the
      **create -> attach -> start** ordering a foreground `docker run` uses — the branch where
      `attachContainer` finds `rec.session() == nil` and waits on `rec.started()` before opening the
      tunnel. Attaching to an established session is fine; attaching around its establishment is not.
      Still unknown: whether the server rejects the tunnel opened at that instant, the client cancels
      it, or the session hand-off closes it. That is where the next attempt starts.
      **RETRACTION 2026-07-29 — the `sess.Done()` fix was HARMFUL and is REVERTED.** Server log during
      a failing foreground run: `deploy attach failed error="failed to write msg: use of closed
      network connection"` — the server was writing container output into a tunnel the PROXY had
      already closed, and that closer was the watchdog. For a short-lived container the session ends
      within milliseconds of start, so it closed the stream before any output flowed: it TRUNCATES,
      converting a visible hang into a silent empty success. Worse, every measurement taken after it
      was added was made THROUGH it — the "0 bytes", the "context canceled" errors, and the
      conclusion that framing was irrelevant were partly properties of the patch, not the system.
      `pkg/dockerproxy/exec.go` is back at HEAD; the patch is kept at
      `.agents-workspace/tmp/exec.go.withfix` for reference only.
      **CLEAN BASELINE (no patch), Docker 29.2.1**: foreground `docker run` HANGS with no output, and
      the server logs NO attach error — `backend.Attach` neither errors nor returns, so the server is
      blocked having written nothing while the client blocks reading nothing. `docker attach` on an
      already-running container still works (78 bytes, correct framing), so the create -> attach ->
      start narrowing holds. ALSO DISPROVED: "attach opens too late so early output is lost" —
      output produced THREE SECONDS after start, long after attach is open, is lost too.
      Any future fix that closes the tunnel on session end has the same truncation flaw; the shape
      must instead be "deliver everything, then end", e.g. draining before close or serving the
      stream from the follow-from-start log feed.
      **ROOT CAUSE FOUND 2026-07-29, unpatched tree, host docker 29.2.1. It is TWO independent
      defects, and neither is a cancelled context.** Staging one echo every 500 ms from a container
      (`run-b`) delivered `LINE-1 (t+500ms)` through `LINE-8 (t+4000ms)` live over the attach tunnel
      and lost EXACTLY `LINE-0 (t+0)`. A 58-second run (`run-a`) streamed its t+3s and t+58s lines
      and exited rc=0. So the attach stream works; only the HEAD is missing.
      **Defect 1 — head loss (this is what breaks the devcontainer CLI).** Real dockerd registers the
      attach BEFORE the container's process starts, so `logs=0` loses nothing. Cornus inverts the
      order: `startContainer` deploys and waits for readiness, and `attachContainer` only opens the
      tunnel after `rec.started()` (`exec.go:228-241`), with `Logs` taken from the CLI's query — which
      a foreground `docker run` sets to 0. Everything written between container start and tunnel open
      is gone forever. The window measured under 500 ms, which is ALL of the output for an `echo`
      workload. The devcontainer CLI runs `... -c 'echo Container started'` and waits to SEE that
      string on the attached stdout, so it waits on output that was discarded ~200 ms in.
      **Defect 2 — no EOF on workload exit.** `docker run --rm alpine echo` (`run-c` case 1) produced
      empty stdout AND never returned: 281 s, unkillable by SIGTERM (`--sig-proxy` default forwards
      the signal to a container that is already gone), killed with SIGKILL at rc=137. When the
      workload exits, the backend attach tunnel does not EOF, so `bridge` blocks in
      `io.Copy(client, backendStream)` forever. INDEPENDENT of defect 1: a long-lived container
      (case 3) loses its head but correctly keeps the run open.
      **CORRECTIONS to entries above, both from measuring through the reverted patch.** (a) "output
      produced THREE SECONDS after start is lost too" is FALSE — t+500 ms onward is delivered.
      (b) "the session terminates abruptly / caller disconnected 43 s in" is FALSE — the session
      never terminated on its own; the 43 s was the E2E harness's own command timeout killing the
      docker CLI, and `deploy-attach: caller disconnected` is the correct downstream consequence.
      Reproduced at `.agents-workspace/tmp/attach-dbg/run-{a,b,c}.sh` (each isolates its own agent via
      `CORNUS_AGENT_DIR` and names containers `cornus-attach-probe-*`, so a developer's own workloads
      are never touched).
      **PRIMITIVES MEASURED 2026-07-29 (`attach_probe.py`, raw hijack against the host daemon, no
      cornus in the path).** `logs=1` replays the head at t+0.00s and then streams live
      (`HEAD-AT-T0` + `LIVE-AT-T3`); `logs=0` delivers only `LIVE-AT-T3`. So replay is available and
      correctly framed. Two side findings: (a) an earlier "0 bytes" reading was a CURL artifact —
      without `Upgrade: tcp` the daemon answers 200 + `raw-stream` and curl buffers it away, so the
      probe must speak the hijack itself; (b) dockerd announces
      `Content-Type: application/vnd.docker.multiplexed-stream` on a non-TTY attach, confirming the
      long-standing suspicion that the proxy's unconditional `raw-stream` handshake
      (`writeRawStreamHandshake`) is wrong — INDEPENDENT of the head loss, since the CLI evidently
      copes today. Also relevant to any freeze-based fix: cornus never sets `LogConfig`, and
      dockerhost already has SEPARATE `containerCreate` (`engine.go:368`) and `containerStart`
      (`:386`).
      **FIXED 2026-07-29 (`pkg/dockerproxy/exec.go`), both defects, verified.** Defect 1: on the
      create -> attach -> start branch ONLY (`lateAttach`), the tunnel is opened with `Logs: true`, so
      the backend replays from the container's first byte and then continues live. An attach to an
      already-established session is untouched — it missed nothing, and replaying there would
      re-print history the caller never asked for. Defect 2: `endWhenWorkloadDrains` closes the
      tunnel once the workload has exited AND the stream has been quiet for 300ms (cap 10s). The exit
      signal is `awaitExit`, NOT `sess.Done()` — the server deliberately holds the session open after
      a self-exit (see the `exitPollInterval` comment in `containers.go`), so `Done()` never fires for
      exactly the short-lived containers this unblocks, which is why the reverted patch could not have
      worked. Also fixed: the swallowed `Attach` error now logs.
      MEASURED AFTER: `LINE-0` through `LINE-8` all delivered exactly once (was: `LINE-0` lost);
      plain foreground `docker run --rm alpine echo` rc=0 in 3.1s with its output (was: empty, 281s,
      SIGKILL-only); the devcontainer command shape rc=0 in 3.0s with `Container started` (was:
      empty, hung). Regression tests: `pkg/dockerproxy/attach_replay_test.go` (3 tests; the two
      behavioural ones were confirmed to FAIL with the fix neutralised) and a new E2E scenario
      `e2e/scenarios/dockerd-attach-foreground.star` (passes at 29.2.1; fails with the fix
      neutralised at `rc 137 ... the attach stream never ended`). Nothing in the suite drove a
      foreground attached run before it.
      **THE PIN STAYS — a THIRD, unrelated blocker.** `devcontainer-vscode.star` in a purpose-built
      29.2.1 runner image (`docker build --build-arg DOCKER_VERSION=29.2.1`) still times out at 10m,
      but it now gets FURTHER: the CLI's own log prints `Container started`, the string it used to
      wait forever for, so the attach defects really are gone. It then hangs before issuing its next
      `docker` command — no `docker exec` is ever spawned, and its `docker events --filter event=start`
      watcher plus `docker run` are the only live processes. NOT the event stream: subscribing to the
      proxy's `/events` inside that same runner and starting a labelled container delivers a correct
      `start` event with the devcontainer labels intact. Next step is to find what the CLI waits on
      between seeing the marker and its next command.
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

## Follow-ups from wave 3 (2026-07-27)

- [ ] **`THIRD_PARTY_NOTICES.md` represents the arm64 dependency set only — the amd64 release
      binaries ship an incomplete notice.** `go list -deps` is architecture-sensitive: linux/amd64
      additionally links `github.com/klauspost/cpuid/v2` and `github.com/tonistiigi/go-archvariant`
      (both MIT), neither of which appears in the committed file. Since the notices are now attached
      as a release asset beside all five binaries, the amd64 artifacts carry a notice that omits two
      modules they actually link. Pre-existing, found while wiring the CI drift check
      (2026-07-27). The new `licenses` job pins its drift gate to `NOTICES_GOARCH: arm64` so it is
      green and still catches every real dependency change, runs the POLICY scan on amd64 (the
      superset, so nothing hides), and reports the two missing modules from an advisory
      `continue-on-error` step every run. Closing it is a content decision — regenerate for amd64,
      or emit the union of both arches — then flip that one env var.
      — *source: wave 3, license scanner CI wiring*
- [ ] Reproduce the Rust crate tree inside `libimbhgo.a` in the notices (the scanner walks Go
      modules only). Pre-existing and already tracked above under the release-notices item; noted
      here only because the new `licenses` CI job does NOT cover it and should not be mistaken for
      doing so.
      — *source: same*

## Follow-ups from the S3 decision (2026-07-27)

- [ ] **Force-push the re-pointed `v0.0.0` tag, or delete it.** The tag was orphaned — it pointed at
      `9ab8057`, an amend state `main` had moved past, so nothing built from it was reproducible from
      the tree. It was re-pointed LOCALLY to the then-current HEAD (`4c30b80`) on 2026-07-27, but
      **`origin` still has `v0.0.0 -> 9ab8057`**; syncing it needs `git push --force origin v0.0.0`,
      deliberately not done (outward-facing, and SSH auth to `origin` is currently broken —
      `Permission denied (publickey)`; `gh` is authenticated over HTTPS if that route is preferred).
      Verified via the API at decision time: there are **no** GitHub Releases at all, so no published
      release assets depend on the old target — this is safe to move.
      — *source: S3 decision, 2026-07-27 TODO sweep*
- [ ] **Decide the tagging convention under the amend workflow.** Because S3 kept
      amend-onto-`Initial.`, EVERY tag on `main` is orphaned by the next amend — re-pointing `v0.0.0`
      is a one-time fix for a recurring condition, not a cure. Two workable conventions: (a) stop
      amending once a release tag is cut, so the tagged commit stays an ancestor; or (b) accept that
      the tag must be re-pointed immediately before each release-workflow run, and add that to the
      release checklist so a `v*` run never builds a detached tree. This matters directly to the open
      release items, which assume a tagged commit corresponds to a state of `main`.
      — *source: same*
- [ ] Fix SSH auth to `origin` (`git@github.com:moriyoshi/cornus.git` returns
      `Permission denied (publickey)`). `gh` is authenticated over HTTPS, so the API path works, but
      `git push`/`git ls-remote` do not — meaning `origin/*` remote-tracking refs can go stale
      without any error surfacing. Noticed 2026-07-27 while checking the tag; local `origin/main`
      happened to match the real remote head (`12d5c71`), but that was luck, not freshness.
      — *source: same*

## Auth dataplane gaps (2026-07-27) — found scoping S4's auto-provisioning

Scoping "auto-provision a token on first run" mapped every party that connects to the server. The
auto-provisioning verdict is below, but the more important result is that **cornus's own dataplane
presents no credential on several paths**, so enabling auth AT ALL — which `guides/security.md`
tells operators to do before exposing the server — likely breaks core flows today. These are
current defects, not blockers invented by the auto-provisioning idea.

- [ ] **The auto-started builder container cannot be authenticated to.** Its env is constructed
      explicitly and does not inherit the parent's (`pkg/build/builderctr/builderctr.go:416,428`), so
      it would hold a different credential. Worse, the relay is inconsistent: the attach path sends
      NO header (`pkg/server/build_relay.go:184`, though `wire.DialConnControlHeader` exists) while
      the POST path forwards the CALLER's token (`build_relay.go:143-145`) — a credential minted for
      the parent, presented to the builder. Same applies to a `--builder-url` upstream.
      **DELIBERATELY OUT OF SCOPE for the S4 dataplane work (2026-07-28), with reasons:** the
      container's env is fixed at `POST /containers/create` (`builderctr.go:416`) under
      `RestartPolicy: unless-stopped`, so it cannot hold a credential short-lived enough to be worth
      minting; and it only engages when `!builderctr.CanMount()` (`build_relay.go:61`). The relay
      asymmetry was re-examined and is NOT the hole it reads as: `relayBuildAttach` splices the
      hijacked connection before any yamux handshake, so the caller's own WebSocket `Authorization`
      header reaches the builder on that path too — the difference is code shape, not credential
      flow. The rule to preserve when this is picked up: **never substitute a server-minted internal
      credential on a relay**, or a third-party `--builder-url` receives one.

## S4 self-review follow-ups (2026-07-28)

The 2026-07-28 self-review of the S4 implementation raised six items. Four were fixed outright, one
was retracted, and the two below survive as real work. Full detail in `JOURNAL.md` under
"S4 self-review follow-ups, and one finding retracted".

Fixed: the Helm installation Secret now renders env-safe text (`randAlphaNum 32 | b64enc`, verified
by decoding a render) instead of `randBytes 32`, which Kubernetes decoded to raw binary with roughly
a 12% chance of a NUL that a process environment cannot carry; `CORNUS_INSTALLATION_SECRET` is held
to the same 32-byte minimum as the file; the multi-replica peer-forward warning landed and was later
superseded by hub-store peer keys (closed 2026-07-28, see the TODO wrap-up in `JOURNAL.md`); the SSH
session cache is keyed on a stable profile identity rather than the dialed
endpoint; and the background agent's bearer token is late-bound so a cached client no longer pins an
expiring JWT.

RETRACTED — do not re-open as an S4 regression: the review flagged the fail-closed JWT scope model
as a compatibility break introduced outside the behaviour-preserving Stage 1. It predates that work
by a day (`JOURNAL.md` "2026-07-27: The empty JWT scope now fails closed"). The compatibility
question itself is real and is the first item below.

- [ ] **Decide the migration story for externally-issued JWTs that carry no cornus scope.** A stock
      OIDC/JWKS token whose `scope` is absent or names only foreign values (`openid profile`) now
      401s, where it used to be a full credential. This is deliberate hardening and correct on the
      merits, but it is a breaking change for any existing JWKS deployment and there is currently no
      migration path and no upgrade note. Options: document it as breaking in the release notes;
      add an opt-in compatibility flag mapping "no cornus scope" to `api` for a deprecation window;
      or map a configured external scope onto `api` (`CORNUS_JWT_SCOPE_ALIAS`-style). Decide
      explicitly rather than shipping it as an incidental S4 side effect.
- [ ] **The Incus registry-credential path is unit-tested only.** All four other backends have live
      Docker E2E coverage of authenticated pulls; Incus does not. `SCENARIOS_INCUS`
      (`Makefile`) carries no auth scenario, and the incus target self-skips below incus 6.3
      (`e2e/container/entrypoint.sh`), which is what Alpine stable ships — so a scenario added
      naively would silently skip in CI. Add an incus arm registered in BOTH `SCENARIOS_INCUS` and
      `INCUS_SCENARIOS` (`TestScenarioSubsetsInSync` enforces the pair and its ordering) and run it
      via `make e2e-incus` with `E2E_STRICT=1` against a host incusd >= 6.3, where the skip becomes
      a failure. Until that leg runs, the userinfo-in-`InstanceSource.Server` mechanism is verified
      by reading upstream source, not by execution.

## SSH key auth: coverage and design gaps (2026-07-28)

Found by auditing what `e2e/scenarios/auth-ssh-key.star` and the Go tests actually cover. The
primary flow is well covered — the scenario drives enroll -> session -> list -> delete plus
code-rotation replay rejection, and the RSA/SHA-1 hazard is covered TWICE in Go
(`pkg/sshkeyauth`: `TestProofRejectsRSASHA1`, `TestSignRSAUsesSHA2`; `pkg/server`:
`TestSSHTokenRejectsRSASHA1EndToEnd`), alongside purpose binding, declared-algorithm mismatch,
keystore-off-by-default, routes-absent-when-unconfigured, the `keystore=none` 409, and pre-seeded
keys. The items below are what is left.

- [ ] **Decide the second-machine enrollment story.** The design called for adding a further machine
      by having an ALREADY-ENROLLED key countersign the new one. That was not implemented:
      `AuthEnrollCmd.Code` is `kong:"required"` (`cmd/cornus/auth.go:46`) and the server validates a
      code (`pkg/server/sshkeyauth_http.go:80,95`) with no enrolled-key proof path. Consolidating on
      one mechanism is defensible, but the consequence is NOT free: enrolling a second machine
      requires server-side access all over again to read the rotated code — precisely the friction
      the countersignature was meant to remove for remote and unattended servers. Either implement
      the countersignature, or record the code-only model as the decision and say so in
      `docs/cli/auth.md` so operators do not plan around a capability that does not exist.
- [ ] **Three narrower E2E coverage gaps in `auth-ssh-key.star`.** (a) No key-auth scenario exercises
      the BACKGROUND AGENT, so the read-only session-cache consumer — `compose up -d` and the web
      and dockerproxy front ends — is unproven end to end; this is the path the stable cache
      identity and the renewal provider were built for. (b) Only ed25519 is driven through the CLI;
      RSA and ECDSA are unit-covered but never exercised across the full client-server path. (c) **DONE 2026-07-28** — fixed at the
      PREFLIGHT rather than per-scenario: `scenarioNeeds` now flags `CapSSHTools` when a scenario
      shells out to `ssh-keygen`/`ssh-add`, not only when it calls `ssh_agent(`. auth-ssh-key.star
      slipped past the check entirely because it generates its keys with `sh(cmd = "ssh-keygen ...")`,
      so a host without openssh-client failed MID-RUN where preflight exists to say so up front with a
      remedy. Covered by `TestScenarioNeedsSSHToolsWhenShellingOut`. (a) and (b) remain open.

## Security: volume-name path traversal (found + fixed 2026-07-28)

- [ ] Consider whether `spec.Volumes` should be policy-visible at all. The traversal above was
      possible partly because `hostpolicy` inspects only `spec.Mounts`, so the entire volume path
      was ungoverned. The name is now safe, but the asymmetry is worth a deliberate decision rather
      than an accident: should a host-backed volume backing be subject to the same default-deny
      posture as a host bind?
      — *source: same*

### Defects surfaced by the 2026-07-28 refactoring audit

A three-way parallel audit of `pkg/` and `cmd/` for refactoring opportunities incidentally
surfaced the defects below. Each was re-verified against the working tree before being filed.
Common shape: a duplicated code path where the copies have since DRIFTED, so one copy is now
wrong. The audit found no dead code, no unread kong flags, and no unconsumed `api.DeploySpec`
fields.

- [~] **`CORNUS_ADVERTISE_URL` and `CORNUS_AGENT_IMAGE` are re-read from the environment per
      request, not resolved once at startup.** **PARTIAL 2026-07-29 — and the prescription was
      wrong, while a real defect sat beside it.**
      - *The prescription does not survive contact.* "Memoizing these two in `New`" would break the
        attach paths: the advertised URL is the address OTHERS reach this server at, which is not
        known when a Server is built ahead of its listener — exactly what `httptest` does and what
        `credential_multireplica_test.go:35-40` and the other two-replica relay tests depend on
        (server constructed, THEN `t.Setenv` with the now-known URL). Memoizing would capture `""`
        and reject every attachment regardless of the environment. Neither variable has a flag and
        nothing in the tree mutates either at runtime (the only production `os.Setenv` is
        `CORNUS_OTEL`), so in a real server the value is fixed before exec and the per-request read
        costs one getenv on a path already doing a deploy. Reading per use is correct here; the
        reason is now written down in `pkg/server/advertise.go` so it is not "fixed" again.
      - *The real defect: the ten sites did not agree on what "set" means.* Two (`obstelemetry.go`)
        `TrimSpace`d; the other eight did not. A value with a trailing newline — a YAML folded scalar,
        a hand-edited env file — was therefore set for every consumer but well-formed for only some:
        the telemetry endpoint came out clean while the mount and egress paths handed the caretaker a
        `RelayURL` with a newline in it, failing at dial time far from the cause.
      - *Fixed:* `pkg/server/advertise.go` adds `advertiseURL()` / `agentImage()`, both trimming;
        all ten `pkg/server` sites now go through them, including the backend construction in
        `server.go`. Also hoisted the `CORNUS_AGENT_IMAGE` read out of `applyWithSidecarMounts`'s
        per-mount loop (`deploy_attach.go:471`), where it was re-read once per mount while its
        sibling `applyWithAttachments` correctly resolved it once — every sidecar in one deploy must
        be told the same image. Regression:
        `TestAdvertiseURLAndAgentImageAreTrimmedOnTheAttachPath` drives a real deploy-attach with
        whitespace-wrapped env and asserts what the backend receives, plus
        `TestAdvertiseAccessorsTreatBlankAsUnset` for the blank-is-unset half. Both confirmed to fail
        with the trim removed (`RelayURL = " ws://127.0.0.1:42509\n"`).
      - *Still open:* the broader pattern the entry names — `pkg/server` reads 36 distinct `CORNUS_*`
        names via ~58 scattered `os.Getenv` calls across 14 files, in parallel with ~25 kong flags,
        with env-vs-flag precedence accidental rather than specified. Two names are now accounted
        for; the precedence question is untouched and is the part that actually needs a decision.
      *(original entry follows)* `CORNUS_ADVERTISE_URL` is `os.Getenv`'d at 11 sites
      (6 in `pkg/server` alone: `egress_relay.go:33`, `obstelemetry.go:60/143`,
      `deploy_attach.go:450/488/538`) and `CORNUS_AGENT_IMAGE` at 8. A value that changes after boot
      is therefore observed inconsistently between requests, and the four `applyWith*` functions in
      `deploy_attach.go` each re-derive it with their own error message. Part of a broader pattern:
      `pkg/server` reads 36 distinct `CORNUS_*` names via 58 scattered `os.Getenv` calls across 14
      files, in parallel with the ~25 kong flags, and the env-vs-flag precedence is currently
      accidental rather than specified. Memoizing these two in `New` is the cheap first step.
      — *source: refactoring audit 2026-07-28*

## Findings from the incus RemoteCapable work (2026-07-28)

- [ ] **Nothing in the incus companion path has run against a live `incusd`.** Every Incus-facing
      claim in that work is grounded in the vendored v6.18.0 source rather than observed behaviour,
      and the companion path (sibling instance addressed by IP, shared `security.shifted` custom
      volume as the agent-socket carrier) is the piece most in need of a real daemon before it is
      trusted. The incus E2E leg exists (`make e2e-container E2E_TARGETS=incus`) but this host has no
      incusd. Exercise it before relying on incus remote mode.
      — *source: same*
- [ ] **`e2e/scenarios/mcp-stdio-tools.star`'s `if TARGET == "incus"` guard is believed obsolete.**
      It skips the `logs_tail` CONTENT assertion because incus served only the console (shell prompt,
      no app stdout). `--tail`/`--follow` now have a real source and return app output, so the guard
      should be removed and incus should regain the assertion — but that cannot be verified without a
      live incusd, and removing it blind would redden the incus CI leg. Remove it in the same change
      that first runs the incus leg for real.
      — *source: same*

## Barehost findings (2026-07-28)

- [ ] **`CORNUS_BARE_SHIM` should stay OFF — four prerequisites remain**, judged 2026-07-28 after the
      lock removed one objection (the lock was necessary, not sufficient):
      (1) **No E2E leg exercises the shim at all.** `BareTarget.ServeEnv` never sets it and CI's bare
      leg does not either, so every green bare run to date is the in-process path — verified: zero
      `CORNUS_BARE_SHIM` hits in `pkg/e2e/`. A variant leg is cheap (env propagates via
      `append(os.Environ(), ServeEnv()...)`); it simply has to run.
      (2) **Shim liveness is unmonitored.** `ensureShim` is called only from `createInstance`,
      `Start`, and `reconcile` — and `reconcile` runs only from `New`. A shim that dies while the
      server stays up leaves its container unsupervised indefinitely with no detector.
      (3) **The restart contract differs.** The in-process path resets the backoff tally past
      `stableRunThreshold`; the shim has no equivalent (~100ms vs up to 30s for a long-lived workload
      that finally crashes).
      (4) **Companion supervision is mixed.** `startCompanion` always uses `b.super.watch`, but
      `reconcile` routes companions through `launchSupervised`, which spawns a shim for them in shim
      mode — so the supervisor identity CHANGES across a server restart, and the "companions are never
      shim-supervised" comments in `shim_control_linux.go` are wrong after the first reconcile.
      — *source: same*
- [ ] **Companion reboot recovery is broken and silent.** Recovery mints a FRESH random netns pin
      (`netns.NewNetNS(netnsDir)` -> `cni-<uuid>`), but the companion's `config.json` still carries
      the dead path baked in at `startCompanion`, and nothing rewrites it. After a host reboot the app
      returns and `reconcile`'s companion relaunch fails, so the deployment silently loses egress
      policy, client-local mounts, and telemetry export until it is re-applied. SHAPE OF THE FIX:
      two-phase reconcile — recover apps, then per companion resolve `instanceName(rec.App,
      rec.Replica)`'s fresh `NetNS`, `rewriteNetnsPath` its bundle, relaunch, skipping companions
      whose app failed. Needs a per-ROLE decision too: a mount caretaker's 9P peer is a client process
      that no longer exists after a reboot, so resurrecting it yields a container with a dead relay.
      — *source: same*
- [ ] Add a shim-supervision E2E variant leg for the bare target (see prerequisite 1 above), so the
      shim path is exercised before any thought of making it default. Note
      `deploy-reboot-survival.star:49` does `pkill -9 -f 'bare-shim'`, which has always been a no-op
      because no shim ever runs — that scenario would become meaningful under such a leg.
      — *source: same*

## CI E2E triage + third-party scope mapping follow-ups (2026-07-28)

Opened by the triage of run 30339923138 and the `pkg/authscope` work that came out
of it. Findings and the verification record are in JOURNAL.md under
`## 2026-07-28 — CI E2E triage, and third-party JWT scope mapping` and its addendum.

- [ ] **`observability-metrics.star` failed in CI in a way that has not
      reproduced.** CI: 7 replicas, 608 samples, 0 failed, 180s of polling, no
      `container_memory_usage` series. Locally (containerized kube + metrics-server +
      imbh): 3 replicas, resolves on the first poll, all four sections pass. The
      visible difference is that CI's kind cluster is shared with the whole suite, so
      the recorder is also sampling replicas belonging to other deployments — a
      hypothesis, not a finding. The gate now issues one probe on the failure path
      and prints what the store actually answered, so a recurrence should explain
      itself; re-check the next kube leg.

## Compose `up` recreates unconditionally (found 2026-07-28 by CI)

- [~] **`compose up` recreates every service even when nothing changed, diverging from Docker
      Compose.** **PARTIALLY CLOSED 2026-08-01: fixed for `dockerhost` only** (`pkg/deploy/dockerhost/reuse.go`,
      a spec+image-content fingerprint stamped as the `cornus.spec-hash` label; guard tests in
      `reuse_test.go`, one subtest per `api.DeploySpec` field). Still open for `containerdhost`,
      `barehost` and `incushost`, each of which is its own change and its own review.
      `e2e/scenarios/compose-watch-reload.star` now asserts instance IDENTITY on the docker target
      and keeps the divergence assertion on the others; each leg flips as its backend lands.
      **`kubernetes` needs no fingerprint and never did — MEASURED 2026-08-01.** It is declarative:
      `applyDeployment` UPDATEs the Deployment in place, and an update whose pod template is
      unchanged does not roll the ReplicaSet, so the pod is not replaced. Verified on the kube
      target (containerized runner, single scenario): the pod behind the unchanged service survived
      the reload byte-for-byte (`e2ewatch-web-7f6795bdbc-j8rtt`), and the check was neutralized by
      forcing a real replacement, which failed it. `docs/cli/compose.md` (+ ja, zh) already said so
      ("dockerhost and kubernetes leave an unchanged workload alone"); only this entry and the E2E
      scenario still claimed otherwise. See the 2026-08-01 JOURNAL entry on the kube leg.
      REMAINING for kubernetes, and NOT verified either way: the IMAGE-CONTENT half. A built compose
      service is deployed under the mutable tag `<registry>/<resource>:latest`
      (`composecli/build.go`), so a rebuild that changes the image bytes leaves the pod template
      byte-identical — which by the reasoning above means no rollout, i.e. the old image keeps
      running. That is the failure mode `dockerhost`'s fingerprint hashes the image CONTENT id to
      avoid. No E2E covers "rebuild the same tag, then redeploy" on kube; probe it before assuming
      either answer.
      Original entry: `docker compose up` recreates a container only when its configuration or image
      changed (that is what `--force-recreate` exists to override); cornus's Apply recreates
      unconditionally. MEASURED: a repeat `compose up` against a byte-identical file mints a new
      container id (probe, docker target, 2026-07-28), so this is Apply's behaviour and not
      something `--watch` does.
      **Why it matters more than it looks.** `compose up --watch` reloads the whole configuration on
      every file save, so editing ONE service bounces every service in the project — in the tool
      whose whole purpose is the dev loop. It also means any repeat `up` drops connections and
      restarts work that had no reason to stop.
      **How it stayed hidden.** `e2e/scenarios/compose-watch-reload.star` claimed the opposite in a
      comment ("it did not restart the unchanged service") and asserted it as
      `status(...)["running"] == 1` sampled the instant after an ASYNCHRONOUS reload reported the new
      service up. That passes whenever the replacement happens to be up already, which is most of the
      time — and it failed on the CI docker leg (run 30359492738) when the sample landed inside the
      down-window instead. The scenario now asserts WORKLOAD IDENTITY, which is stable whenever it is
      sampled, and currently pins the divergence so it is stated rather than assumed.
      **And identity is not one observable.** Reading it off `status()` uniformly is what then broke
      the kube leg for three runs (30421398539, 30678513400, 30687211223): the kubernetes backend
      SYNTHESIZES the instance id as `"<deployment>-<i>"` from the Deployment's replica counters
      (`statusOf`), so it never changes and an `id != before` assertion cannot pass there whatever
      the backend does. The kube branch reads pod NAMES out of the cluster instead. Any backend
      added to this scenario needs its identity probe checked the same way — first ask what the id
      is derived from.
      SHAPE OF THE FIX: make Apply skip the recreate when the desired spec and image digest match
      what is running, per backend, with `--force-recreate` as the override. Touches every backend's
      Apply, so it needs its own change and its own review. When it lands, the identity assertion in
      compose-watch-reload.star flips back to `assert_eq` — the comments there say so.

## Backend field-coverage guards (2026-07-29)

- [~] **The other four backends have no equivalent field-coverage guard.** **PARTIALLY CLOSED
      2026-07-29, and looking found a live defect in the DEFAULT backend.** A differential probe
      (set each `api.DeploySpec` field alone, compare `toCreateBody` output) showed 13 fields with no
      effect on dockerhost's container body. Ten are handled elsewhere or warned about; the rest were
      not referenced anywhere in the backend. Checked for an upstream guard first — nothing in
      `pkg/server` gates these by backend — so they were reachable:
      **`spec.Proxy`, `DNS`, `Hub`, `Docker`, and `AgentForward` were accepted and dropped in TOTAL
      SILENCE by `dockerhost` (the default backend), `containerdhost`, and `barehost`.** Only
      incushost warned. Writing an `x-cornus-hub` block and deploying to the default backend gave a
      successful deploy and no feature.
      Fixed with `deploy.WarnKubernetesOnlyFields`, now called from all four host preludes. Shared
      rather than copied because these fields are kubernetes-only for the SAME reason everywhere, so
      a field added to the list warns everywhere at once — the divergence is what caused this. The
      wording is incushost's, which had it right and alone; incushost was migrated onto the helper,
      removing 773 characters of duplicated text.
      Still open: the full per-field coverage guard (a `supportedSpec` plus a row per unsupported
      field) for `dockerhost`, `containerdhost`, `barehost`, and `kubernetes`. That is per-backend
      domain work and a map-vs-warn decision per field; the five kubernetes-only fields above are now
      handled, but the systematic guard incushost has is not yet reproduced. Order: dockerhost, then
      kubernetes. Original entry: Each needs the same two-part story
      incushost has (a `supportedSpec` the backend must be silent for, plus a row per field it
      cannot honor) before a reflection guard can be added, which is real per-backend domain work
      rather than a mechanical copy. Worth doing highest-traffic first: `dockerhost`, then
      `kubernetes`.
      — *source: generalizing the incus guard, 2026-07-29*

## Dependency advisory found incidentally (2026-07-29)

- [ ] **`postcss` high-severity advisory in the web build toolchain — assess, then decide.**
      GHSA-r28c-9q8g-f849, "Path Traversal in Previous Source Map Auto-Loading (sourceMappingURL)
      leads to Arbitrary .map File Disclosure", affects `postcss <= 8.5.17`. The tree has
      `postcss@8.5.17`, reached transitively: `cornus-web@0.0.0 -> vite@7.3.6 -> postcss@8.5.17`.
      **Scope, measured rather than assumed**: `npm audit --omit=dev` in `web/` reports **0
      vulnerabilities**, so nothing that ships inside the cornus binary is affected. It is a
      BUILD-TIME exposure only, and the advisory needs postcss to process CSS carrying an attacker-
      controlled `sourceMappingURL` — first-party stylesheets do not. Real severity here is well
      below the headline "high".
      `npm audit fix` claims a non-breaking fix, but the fix path runs through `vite`, which builds
      the SPA embedded in every release binary. That is a toolchain bump, wants its own verification
      (`npm run build` + `vitest` + a look at the built bundle), and should not ride along in an
      unrelated sweep.
      Found incidentally while running `make e2e-check` (which installs web deps). Noting the
      provenance because nothing in the repo currently watches npm advisories — the licence scanner
      covers Go modules and notices, not JS CVEs. Worth deciding whether CI should run `npm audit`
      at all, and at what threshold, rather than only noticing this by accident.

## Ulimit bounds are validated in exactly one backend (found 2026-07-30 by the sub-field sweep)

- [ ] **An inverted `ulimits` entry behaves four different ways across the five backends, and nothing
      upstream normalizes it.** `api.Ulimit` is `{Name string; Soft, Hard int64}` and **no validation
      exists anywhere above the backends** — grepped `pkg/api`, `pkg/compose` and `pkg/server` for a
      bounds comparison and found none. So each backend invents its own answer to
      `ulimits: {nofile: {soft: 4096, hard: 1024}}`:
      - **incushost** validates and SKIPS with a warning — `incusUlimit` (`spec_linux.go:260-272`)
        rejects both `Soft > Hard` and the negative spelling of the same inversion (`Soft < 0 &&
        Hard >= 0`, an unlimited soft bound under a finite hard bound).
      - **containerdhost / barehost** pass it straight through: `hostrun.withRlimits`
        (`spec_linux.go:233-247`) casts `uint64(u.Soft)` / `uint64(u.Hard)` into `Process.Rlimits`
        with no comparison at all.
      - **dockerhost** forwards it to the Docker API (`engine.go:1194`) and inherits whatever the
        daemon decides.
      - **kubernetes** warns that ulimits are unsupported wholesale, so the question never arises.
      **What is verified vs. not.** Verified: the four code paths above, and that no shared validation
      exists. NOT verified: what containerd/bare actually do at run time — that needs a live OCI
      runtime and root, which this host cannot provide, so the entry deliberately does not claim
      "the container fails to start". `setrlimit(2)` returns EINVAL when soft exceeds hard, which
      suggests a start failure with an opaque runtime error rather than a silent wrong value, but
      that is an inference and is marked as one.
      **Why this is a decision and not a patch.** The obvious fix — validate once at the API boundary
      so all five agree — is a behavior CHANGE: a spec that deploys today on containerd would start
      being rejected. The alternatives (teach `withRlimits` incushost's skip-with-warning, or leave
      the divergence and document it) trade a clear diagnostic against not silently dropping what the
      operator asked for, which is the same trade `hostrun` and `incushost` already resolved in
      opposite directions. Decide the direction first.
      Note the shape: this is NOT the silent-drop pattern the sweep was looking for — it is the
      inverse, a loud but undiagnosable failure. It was found only because the sweep read the mapping
      code rather than counting coverage, which is the same lesson recorded on 2026-07-29.

## Environment-variable hygiene in `pkg/server` (swept 2026-07-30)

- [~] **The two trimming accessors added on 2026-07-29 are the only ones; ~34 other `CORNUS_*` names
      are still read raw, and several are whitespace-sensitive.** **STRUCTURAL HALF DONE 2026-07-30;
      the SECRET half is still a decision.** I first declined this whole item as needing a per-name
      call, which over-bundled two questions that have different answers. Trimming a URL, a host, a
      path, a namespace or an identifier is not a judgement call — a trailing newline makes those
      malformed and nobody configures one deliberately. Only the credentials are contested.
      `pkg/server/env.go` now carries accessors for the nine structural names — `CORNUS_HUB_REDIS`,
      `CORNUS_HUB_STORE`, `CORNUS_HUB_FORWARD_URL`, `CORNUS_REGISTRY_MIRROR`, `CORNUS_K8S_NAMESPACE`,
      `CORNUS_REPLICA_ID`, `CORNUS_JWT_ISSUER`, `CORNUS_JWT_JWKS_URL`, `CORNUS_JWT_JWKS_FILE` — and
      every read site in the package goes through them. `CORNUS_AUTH_TOKEN` and
      `CORNUS_JWT_HS256_SECRET` are deliberately excluded and still read raw.
      **The concrete bug this closes, which the original entry did not identify**: `CORNUS_HUB_STORE`
      is read by two consumers asking DIFFERENT questions — `multiReplicaHubConfigured()` asks
      `!= ""` ("is this clustered?") and `newHubStore` asks `== "kube"` ("which backend?"). Untrimmed,
      `"kube\n"` answers YES to the first and NO to the second, so the server reports a multi-replica
      hub and quietly builds the replica-local in-memory registry instead. Nothing errors; every
      replica serves its own private hub and the only symptom is peer names that never resolve.
      `CORNUS_HUB_REDIS` has the same three-site split.
      **Three tests, one of which I had to throw away and rewrite.** `TestStructuralEnvAccessorsTrim`
      (nine subtests, each checking both the trim and that whitespace-only reads as unset),
      `TestHubStoreSelectionAgreesAcrossPredicates` (the concrete split above, not a property of the
      helper), and `TestSecretEnvIsNotTrimmed`. The first version of that last one asserted
      `envTrimmed("CORNUS_AUTH_TOKEN") != "s3cret\n"`, which only proves `envTrimmed` trims — it would
      have passed unchanged if someone added a trimming accessor for the token, i.e. it certified
      nothing about the policy it claimed to pin. Rewritten to AST-parse `env.go` and fail if that
      file ever mentions a secret name. **Neutralized**: removing the `TrimSpace` fails the accessor
      tests; adding an `authToken()` accessor to `env.go` fails the policy test with the reasoning
      quoted back. Both still compile.
      **Still open — the secret half.** *(original entry follows)* Background: the `CORNUS_ADVERTISE_URL` work established that a value with a
      trailing newline (YAML folded scalar, hand-edited env file) was "set" for every consumer but
      well-formed for only the two that happened to `TrimSpace`, and the untrimmed path handed a
      caretaker a `RelayURL` with a newline in it that failed at dial time far from the cause. That
      fix added `advertiseURL()` / `agentImage()` and routed ten sites through them.
      **MEASURED 2026-07-30 — the pattern was not extended.** Every other whitespace-sensitive name in
      `pkg/server` is still read raw: `CORNUS_AUTH_TOKEN` (2 sites), `CORNUS_HUB_REDIS` (3),
      `CORNUS_HUB_FORWARD_URL` (2), `CORNUS_REGISTRY_MIRROR` (2), `CORNUS_JWT_HS256_SECRET`,
      `CORNUS_TLS_CLIENT_CA`, `CORNUS_K8S_NAMESPACE`, `CORNUS_JWT_JWKS_URL`, `CORNUS_JWT_ISSUER` —
      **zero `TrimSpace` between them**.
      **Why this is filed rather than fixed: trimming is not uniformly correct.** For a URL, a path or
      a namespace it plainly is. For a SECRET it is a silent change to the credential —
      `CORNUS_AUTH_TOKEN` and `CORNUS_JWT_HS256_SECRET` could in principle carry trailing whitespace
      deliberately, and quietly trimming means cornus authenticates with a value the operator did not
      configure. The two classes need opposite defaults, so a blanket sweep would trade one silent
      wrong-value bug for another.
      Also verified while sweeping, so it does not need re-checking: the eight names read at MULTIPLE
      sites in `pkg/server` are otherwise mutually consistent — all four `CORNUS_DEPLOY_BACKEND`
      kubernetes arms accept both `"kubernetes"` and `"k8s"`, and `isHostBackend`,
      `hostNativeSourceFor` and `runtimeForBackend` all treat `""` and `"dockerhost"` together. The
      inconsistent-normalization defect found in the advertise-URL work does NOT have a second
      instance in the multi-site set.
      This is the concrete half of the still-open precedence question in the `CORNUS_ADVERTISE_URL`
      entry above; that entry's broader ask (env-vs-flag precedence across ~58 reads and ~25 kong
      flags) remains a maintainer decision and is untouched.

## Sync-invariant sweep, third pass: structural instead of lexical (2026-07-30)

- [ ] **`hostcheck.normalizeBackend` mirrors the server's backend-alias handling and nothing enforces
      it.** Found by the same sweep. `pkg/hostcheck/hostcheck.go:361` maps a raw
      `CORNUS_DEPLOY_BACKEND` to a canonical name — `""` to dockerhost, `"k8s"` to `"kubernetes"` —
      and its own comment says it is "mirroring the server's own selector". The server's vocabulary
      lives in `pkg/server.knownDeployBackends` plus the alias arms of several switches. There are now
      THREE copies of this vocabulary: `pkg/server`, `pkg/hostcheck`, and
      `cmd/cornus/internal/setupwiz/answers.go` (which spells the docker case `""` rather than
      `"dockerhost"`).
      **Consequence if they drift**: `cornus daemon preflight` runs its checks against the wrong
      runtime. A server-side alias that hostcheck does not know falls to its default arm and is
      treated as dockerhost, so preflight would probe Docker for a containerd/bare host and report a
      confident verdict about the wrong thing — on the command whose entire job is telling the
      operator whether their host can run the configured backend.
      Note the default arm itself is NOT the bug: it carries an explicit comment ("An unknown value
      is the server's problem to reject; treat it as the default here rather than silently skipping
      every check"), which is a defensible call. The gap is that a NEW alias is silently absorbed by
      that same arm.
      **DRIFT IS NOW GUARDED (2026-07-30); the CONSOLIDATION is still a decision.** I first filed this
      as needing a choice between two designs — hoist the vocabulary into a shared package, or export
      `hostcheck.NormalizeBackend` plus an agreement test — and both do change a package surface. On
      re-reading there is a third option that commits to neither, and it is the pattern
      `TestKnownDeployBackendsMatchTheFactorySwitch` already uses in this very file: parse the other
      package's SOURCE. `TestHostcheckNormalizerKnowsEveryDeployBackend` AST-parses
      `../hostcheck/hostcheck.go`, resolves its string consts (the switch arms are a mix of literals
      and identifiers, so resolving them is required — without it every backend would report as
      unhandled, a louder wrong answer than the drift being guarded), and asserts every
      `knownDeployBackends` entry plus `""` reaches an EXPLICIT arm rather than the default.
      **Neutralized twice, both behaviourally**: adding a `"kube"` alias to `knownDeployBackends` alone
      fails naming it; removing `backendIncus` from normalizeBackend's case still compiles and fails
      naming incus. The second doubles as proof the identifier resolution works — `backendIncus` is an
      identifier, and had the test not been resolving those it would have failed before the edit too.
      **What remains open is the three-copy vocabulary itself** (`pkg/server.knownDeployBackends`,
      `pkg/hostcheck`, `cmd/cornus/internal/setupwiz/answers.go`). The guard makes drift loud; it does
      not make the copies one. Consolidating into a shared package both `pkg/server` and
      `pkg/hostcheck` can import — and folding in setupwiz, which spells the docker case `""` rather
      than `"dockerhost"` — is still worth doing and still a design decision, now without a silent
      failure riding on it.

## Redis hub store registered an empty forward address (found + fixed 2026-07-30)

- [ ] **The ulimit-bounds divergence does NOT split into a guard-shaped half — re-checked 2026-07-30.**
      Two other deferrals filed as "needs a decision" turned out to bundle an uncontested half with a
      contested one (the `hostcheck` mirror, and the env-trimming item), so this one was re-examined on
      the same suspicion. It does not. For env-trimming the split was structural — trimming a URL is
      unambiguously right and trimming a secret is unambiguously cornus's business to stay out of. Here
      every available action on an inverted or unknown ulimit — forward it, warn and forward, warn and
      skip, reject at the API — is defensible, changes behaviour, and picking one IS the decision.
      There is no sub-question whose answer is obvious. Recording the negative result so a fourth pass
      does not re-derive it: the item stands as filed, and it needs a direction, not a decomposition.

## containerd's client-local mounts were documented as working without their flag (found + fixed 2026-07-30)

- [ ] **DECISION NEEDED: should a local containerd realize client-local mounts without the flag?**
      Naming the knob makes the failure honest; it does not make the capability available by default,
      and there is a real argument that it should be. `containerdhost.ApplyWithMounts` does not consult
      `b.remote` on the mount path itself (`mounts_linux.go` gates only the always-on companion and
      `Privileged`, which a mount role sets anyway), so routing a local containerd onto the sidecar path
      when it has no fast-path alternative would plausibly just work — the server would stop refusing a
      deploy it can serve. Two things stop this being a code change to make now:
      1. It cannot be verified here. It needs a live containerd (`make e2e-containerd`) to prove the
         companion comes up with `remote=false`, and asserting it from a fake proves only that the
         router changed.
      2. It is a behaviour change a user could depend on either way. Today `CORNUS_CONTAINERD_REMOTE`
         is an explicit privilege/topology opt-in; making mounts turn the companion on implicitly means
         a deploy with `--mount` starts a privileged companion the operator did not ask for.
      The alternative reading is that containerd should get the real kernel-9P fast path
      (`hostcheck.UsesHostMountFastPath` would gain it, plus the propagation check that comes with it),
      which is more work and more risk than it removes. **Do not act on this without a direction and a
      containerd E2E run.**

## incus could forward UDP and was refused for it (found + fixed 2026-07-30)

- [ ] **E2E gap: no scenario forwards UDP against incus.** The fix is verified by construction
      (identical code shape to the three declared backends) and by the unit-level guard, not by a live
      datagram round trip. `make e2e-incus` on a host with incusd could carry one — the existing UDP
      coverage pattern is a `dns`-style workload plus a datagram probe. Until then the claim "incus
      forwards UDP" rests on reading, which is exactly the standard the rest of this file holds itself
      to, so it is recorded rather than assumed closed.

## Closed-TODO attestation audit (2026-07-30)

All 177 checked entries were re-audited against the current tree by three independent agents, then
reconciled against the local quality gates and targeted neutralizations. Result: 128 attested, 24
not attestable with the present evidence, 20 dependent on historical live CI/E2E evidence, and 5
incorrectly closed. The exposed work was reopened as its own entries; all of it has since been closed
and cleared into the 2026-08-01 TODO wrap-up in `JOURNAL.md` (the five incorrect closures and the 23
unattested ones each have a line there). What survives here is the live-evidence group below.

Four named regression tests stayed green with the repaired behavior deliberately broken in an
isolated copy:

- `TestBearerForRegistry` passed after restoring the old hardcoded anonymous source credential.
- `TestHeartbeatRetryFitsInsideLivenessWindow` passed after removing the kube heartbeat deadline.
- `TestLogShimCommandNameMatchesTheContainerdURIKey` passed after drifting production `logShimArg`.
- `TestEveryUDPForwardingBackendDeclaresIt` passed after making Incus return `false`.

### Historical live CI/E2E evidence — rerun against the current tree

The local attestation ran the daemon-free parse/resolve gate. Treat these closures as historical
evidence until their named live targets pass against the current tree. Raw logs from run 30359492738
confirm that credential-forwarding and observability scenarios passed individually, although that
workflow was red for another scenario.

- [~] Re-verify the strict bare and Incus workflow legs on GitHub-hosted runners. — *former line 1356*
      **BARE LEG DONE LIVE 2026-08-01**: `make e2e-bare-container E2E_STRICT=1` — the daemonless OCI
      runtime target, all 16 scenarios of `SCENARIOS_BARE` green with STRICT preflight (so a missing
      runtime would have failed rather than skipped): mcp-stdio protocol+tools, deploy, deploy-stats,
      lifecycle, lifecycle-restart, deploy-server-restart, **deploy-reboot-survival**,
      deploy-portforward, deploy-egress-bare, deploy-mounts-sidecar-bare, compose, compose-logs,
      compose-down-volumes, exec, compose-exec.
      **Incus leg still outstanding** — it needs a live `incusd`, which the containerized runner does not
      provide (unlike bare, whose runtime the image stages). That half remains genuinely blocked here,
      and separately tracked by the incus companion-path item.
      Note the ORIGINAL item is about GitHub-hosted RUNNERS specifically; this run proves the legs pass
      in the containerized runner, which is the same code path CI uses but not the same machine.
- [~] Re-run `compose-deps.star` on Docker and kube. — *former line 2475*
      **Docker leg DONE LIVE 2026-07-31**:
      `make e2e-container E2E_TARGETS=docker E2E_SCENARIOS=e2e/scenarios/compose-deps.star` passes —
      `up web` deploys the transitive `depends_on` set (api, cache, and db via api), `--no-deps`
      suppresses the expansion so web deploys alone, and a dependency-free service deploys with no
      announcement. Kube leg still to run.

- [ ] The Overview's Workloads card hides the sessions table's **State** column behind a
      horizontal scroll at desktop widths. Measured 2026-08-02 at 1200px: the card's grid cell
      is ~237px, the four-column table is 345px, so `Workload | Agent | Command` are visible and
      `State` — the column carrying the actionable `needs you` badge for a blocked session — is
      the 108px a reader has to scroll to. Not a correctness bug (the scroller works, and at
      390px the card is full-width so nearly all of it fits), but it buries the one cell worth
      glancing at. Two remedies, both cheap, pick one: reorder to `Workload | State | …` so the
      badge is never the clipped column, or drop the table inside the card for a compact
      two-line-per-session list (workload + state badge, command as muted secondary text) and
      leave the full table to the Terminal workspace. Decided against doing either unasked, since
      the request was to MOVE the section, not to redesign it.
      — *source: JOURNAL 2026-08-02 — Terminal sessions moved into the Workloads card*

- [ ] `api.ServerInfo.Host` reaches the web UI on every `/config` poll and nothing reads it.
      `pkg/api/deploy.go` documents this UI as the intended consumer: "so `cornus setup`'s
      verification and the web UI can say 'this server cannot realize client-local mounts'
      before a deploy proves it". Today a client-local `--mount` against a server that cannot
      realize one fails at deploy time with no warning anywhere in the UI. Two fields:
      `clientLocalMounts` (false = the server lacks the privileges for the 9P mount, or is
      containerized such that a mount it makes never reaches the runtime) and `containerized`.
      Put it where the question is asked — the Mounts table / a project's Mounts section — not
      in the Server card, since a bare capability line there is read by nobody about to write a
      `volumes:` entry. Not done 2026-08-02 because it is a HOST capability, not a backend one,
      and that task was scoped to the deploy backend.
      — *source: JOURNAL 2026-08-02 — The backend fact lands in the Server card*

- [x] In the Overview's **by-workload** grouping, `MountTable`'s Service and Workload columns
      and `ForwardsView`'s Service and Workload columns are constant down each table: a
      `WorkloadSection` filters both to one deployment, and a workload has exactly one service.
      Same shape as the Backend column removed on 2026-08-02, but weaker — constant in one of
      the component's two hosts rather than everywhere, and naming the section's own subject
      rather than an unrelated server fact. Options: pass a `dense`/`scope` prop so a
      workload-scoped render drops the two identifying columns, or accept the repetition as the
      price of one shared component. Not done unasked: the 2026-08-02 request was to reorder the
      per-project tables, and dropping columns in the other grouping is a different change.
      Verify before acting — no fixture workload owns two mounts, so a constant-column probe
      returns "clean" today whether or not the claim holds; add a second mount to a fixture
      workload first.
      **Done 2026-08-02 on user request**, via the `scope` option (made REQUIRED, not defaulted).
      The fixture caveat turned out not to gate it: the claim was confirmed by reading the
      filters in `Overview.tsx` (`m.workload === w.name`, `t.workload === w.name`,
      `svc === w.service`), which is where constancy is decided, rather than by probing rows.
      — *source: JOURNAL 2026-08-02 — Project-section tables made service-first*

## Explorer defects found designing the FS operation planner (2026-08-02)

Found while planning the client-side copy/move work (the unified FS operation planner for the
web BFF). All five are reachable in the tree **as it stands** — none needs the planner — and
each was re-verified against the source before filing. The planner work fixes them as it
lands; they are filed separately because they stand on their own account, and because the
first one should probably not wait for a refactor.

- [ ] **The file explorer exposes host pseudo-filesystems, writably, on an unauthenticated
      surface.** `buildLocalRoots` (`cmd/cornus/internal/webbff/fs.go:217-230`) adds EVERY
      external bind-mount source that stats as a directory as a browsable root, and it never
      looks at `m.ReadOnly`. So the standard monitoring idiom `- /proc:/host/proc:ro` yields a
      **writable** `/proc` root, and `- /:/host` yields a writable `/`. The BFF has no
      authentication at all — `guardHost` (`webbff.go:212`) is a DNS-rebinding defence, not an
      authorization check — so `/proc/*/environ` and `/proc/*/cmdline` put the environment of
      every process on the developer's machine, API tokens included, behind a local HTTP GET.
      This is a confidentiality problem first and a foot-gun second, and the `:ro` half of the
      bug means the read-only declaration people rely on is not honoured either.
      **DONE 2026-08-02** (see JOURNAL). Note the fix does NOT list devtmpfs: `/dev` reports
      `TMPFS_MAGIC`, so refusing that type would reject every legitimate tmpfs bind source;
      device nodes are refused by file type instead.
      Fix: one shared predicate, applied in `buildLocalRoots` and in the planner's site
      resolution — refuse pseudo-filesystems by `unix.Statfs` magic (proc, sysfs, debugfs,
      tracefs, bpf, cgroup/cgroup2, devtmpfs), refuse a bind source that is a filesystem root,
      and honour `m.ReadOnly`. **A path denylist is the wrong mechanism**: the whole point of
      `- /proc:/host/proc` is that it appears under another name, so matching the string
      `/proc` catches nothing. Test it with the mount spelled `- /proc:/mnt/p`.
      **Decide whether this ships ahead of the planner work** rather than on its timeline.
      — *source: plan review 2026-08-02 — unified FS operation planner, hardening item H14*

- [ ] **`execCapture` reports success for a failed in-container command.**
      `cmd/cornus/internal/webbff/fs.go:160-162` swallows the `ExecInspect` error, leaving
      `ExitCode` at its zero value, and never consults `api.ExecState.Running`
      (`pkg/api/deploy.go:1591-1595`) — which docker commonly reports as `true, 0` right after
      the stdio stream closes. `containerExecOK` (`fs.go:919`) therefore cannot distinguish a
      successful `mv`/`rm` from one whose status it failed to read. Harmless today only
      because nothing destructive is sequenced behind it; a move implemented as copy-then-delete
      would delete a source whose copy never landed.
      **DONE 2026-08-02.** `ExecResult.ExitKnown`, `inspectExit` polls until the status
      settles, `containerExecOK` refuses on unknown and `containerList` falls back to the tar
      listing. Neutralized both halves.
      — *source: plan review 2026-08-02 — unified FS operation planner, hardening item H4*

- [ ] **`containerPut` can delete a directory tree.** It passes an empty
      `api.CopyToOptions{}` (`fs.go:821`), so `NoOverwriteDirNonDir` is false and
      `pkg/deploy/containerdhost/tarcopy/tarcopy.go:316-320` runs `os.RemoveAll` when an
      existing directory meets a non-directory entry. Writing a one-byte file over a same-named
      non-empty directory wipes it, with no confirmation and no error.
      Fix: set `NoOverwriteDirNonDir: true` on every BFF-originated `CopyTo` AND pre-check the
      destination with `StatPath` — barehost's gVisor tar-exec path
      (`pkg/deploy/barehost/copy_exec_linux.go:34-37`) and incus ignore the flag entirely, so
      the option alone is not a guarantee.
      **DONE 2026-08-02.** Both: `NoOverwriteDirNonDir` set, plus a `StatPath` pre-check that
      409s on a kind mismatch before anything is sent. `TestExplorerContainerPutRefusesKindMismatch`,
      `TestExplorerContainerPutSetsNoOverwriteDirNonDir`.
      — *source: plan review 2026-08-02 — unified FS operation planner, hardening item H6*

- [ ] **A container symlink copies as an empty file, silently.** `tarcopy.Pack` `Lstat`s and
      emits a `TypeSymlink` header with no body (`tarcopy.go:196-206`); `singleTarEntry`
      (`fs.go:719-729`) rejects only `TypeDir`, so `io.ReadAll` yields zero bytes with **no
      error**. Every symlink in a copied container tree becomes an empty regular file, and
      `copyTree`'s symlink branch (`fs.go:1049-1054`) records nothing in `skipped` because
      nothing failed. Note the local source is fine — `FsOpen` uses `os.Stat`, so a dangling
      link errors and is skipped; it is the container path that is untested and wrong.
      **DONE 2026-08-02** (completed in a second pass; the first pass fixed only the read
      path, which rejects `TypeSymlink`/`TypeLink` so a symlink can no longer become an
      empty file — `TestExplorerContainerSymlinkIsNotAnEmptyFile`).
      The remaining half was the classification, and it is now made by the SOURCE rather
      than by `e.Kind`: `skippable` wraps the three things that are not a transferable file
      (a directory reached through a symlink; a device, FIFO or socket; a link entry whose
      archive header carries no body) and the one thing that VANISHED between the listing
      and the copy. `copyTree` steps over exactly those and fails on everything else, so a
      link to a good file whose transfer died is no longer reported as tidily handled —
      which mattered because `FsMove` keeps the source whenever anything was skipped and
      said so while leaving a truncated destination. It also picked up H8's other half: a
      child that disappears under a minutes-long walk is a skip, not the end of the tree,
      and a FIFO in a copied tree no longer aborts the whole copy.
      `TestCopyTreeFailsWhenALinkedFileFailsToTransfer`,
      `TestCopyTreeSkipsNonRegularEntries`. Neutralized both: the first returned
      `{"result":"ok","skipped":["tree/link"]}` for a transfer that failed.
      — *source: plan review 2026-08-02 — unified FS operation planner, hardening item H7*

- [ ] **Two caretaker defects, independent of any FS work.** (a) yamux hands each stream to
      exactly one `AcceptStream` caller, and the caretaker already has **two** loops —
      `runPortForwardAccept` (`pkg/caretaker/portforward.go:51`) and `serveIngress`
      (`pkg/caretaker/hub.go:283`) — each closing tags it does not recognize. A pod carrying a
      hub role with delivery targets AND PortForward can therefore misroute or drop streams
      today. Fix: hoist a single tag dispatcher into `runCaretakerConn`
      (`pkg/caretaker/caretaker.go:474-517`). (b) On kubernetes `cfg.Instance` is set at exactly
      one site (`pkg/deploy/kubernetes/kubernetes.go:3072`), inside `addAgentForwardRole`, gated
      on `spec.AgentForward` and hard-coded to replica 0 — and `Registry.Put("")` is a no-op
      (`pkg/remotecompanion/registry.go:66-69`). So a kubernetes caretaker is unreachable from
      the server unless agent-forward happens to be on, and when it is, every replica collides
      on `name/0`. This misaddresses `ForwardPort` and the exec agent-relay on multi-replica
      pods. Fix needs the downward API (`metadata.name`) — a Deployment exposes no ordinal.
      **(a) DONE 2026-08-02.** `pkg/caretaker/dispatch.go` holds the session's one accept
      loop; roles register a handler for their tag (`registerPortForward`,
      `registerIngress`) and `runCaretakerConn` wires them onto a single dispatcher before
      any of them starts, so there is no window in which a stream can arrive for an
      unregistered tag. `runPortForwardAccept` and `serveIngress` survive as
      single-role conveniences (each builds its own dispatcher) because the existing tests
      drive them directly. `TestOneDispatcherServesEveryRoleTag` runs 20 streams of each
      tag — one of each would pass half the time under the old design — and
      `TestDispatcherClosesUnknownTags` keeps the negative honest. Neutralized by running
      two dispatchers on one session: streams are dropped in droves.
      **(b) RE-VERIFIED 2026-08-02 AND LARGELY WITHDRAWN — the entry overstated it, and the
      fix it proposed would have caused a regression.** Following this file's own rule that
      every entry is re-checked against the tree before it is acted on:
      - *"misaddresses `ForwardPort`"* is **false for kubernetes**. That backend's
        `ForwardPort` (`kubernetes.go:2071`) uses the `pods/portforward` subresource and
        `firstPod`; it never consults the companion registry. The registry's only readers
        are dockerhost, containerdhost and incushost.
      - *"a kubernetes caretaker is unreachable unless agent-forward is on"* is **true and
        currently harmless**: the only consumer of a kube caretaker's declared instance is
        the agent relay (`relayAgentMuxed` -> `execAgentChannels.Get`), which requires
        `AgentForward` anyway. The registration is scoped to the one feature that uses it.
      - *"every replica collides on `name/0`"* is true of the REGISTRATION and does not
        misroute, because the lookup hardcodes 0 too and exec always targets the first pod.
        Both ends agree by construction. The residue is a scoping wrinkle, not a routing
        failure: a process in a non-first replica can reach a forwarded agent while a
        session is open.
      **What was actually wrong was that the agreement was a coincidence.** `pkg/server`
      and the kubernetes backend each spelled `name/0` independently, so changing either —
      exactly what this entry proposed — would have left agent forwarding silently relaying
      nothing. Now structural: `remotecompanion.AgentRelayInstance(name)` is the single
      definition both call, carrying why replica 0 is not arbitrary (exec targets the first
      instance) and why a per-pod identity is not a drop-in change. Neutralized by making
      the kube backend declare `name/$(CORNUS_POD_NAME)`: the guard fires with
      `cfg.Instance = "web/$(CORNUS_POD_NAME)", want "web/0" (the key the server looks the
      agent channel up by)`.
      STILL OPEN, narrower and only as a PREREQUISITE for the Phase 2 caretaker fsop work:
      if a second consumer of the caretaker registry appears on kubernetes, per-pod
      identity becomes necessary, and it must land together with an instance-selecting
      lookup in `pkg/remotecompanion` — not before.
      **That second consumer HAS now appeared (2026-08-02, Phase 2), and the residue is
      unchanged rather than worse.** `kubernetes.Backend.FSOp` looks the pod's caretaker
      up by `remotecompanion.AgentRelayInstance(name)` — the same replica-0 key the agent
      relay uses, and the same one `addFSOpRole` declares — so both ends still agree by
      construction. What it means concretely: on a multi-replica Deployment every pod's
      caretaker registers under `name/0`, so the registry holds whichever connected last
      and a filesystem operation lands in an arbitrary replica. For a NAMED volume that is
      harmless (one PVC, shared) and for an anonymous one it is not, which is why the
      planner refuses anonymous volumes at `siteServer` in the first place. Making this
      correct still needs the downward API plus an instance-selecting lookup, together.
      — *source: plan review 2026-08-02 — unified FS operation planner, Phase 2 prerequisites*

- [ ] Deferred by the planner plan, filed so they are not lost: the container listing's
      **entry-count** cap survives even after the size cap is lifted (`maxToolCapture` truncation
      at `fs.go:497` becomes a 413 in `copyTree`, `fs.go:1037-1039`, so a container directory of
      a few thousand entries still cannot be copied); **uploads stay capped** at
      `maxEditableFileSize` (`fs_handlers.go:111,137,155`), so the same drag gesture will succeed
      as a copy and 413 as an upload; and the **incus backend buffers every file in server
      memory** in both directions (`pkg/deploy/incushost/copy_linux.go:120-138`, `:207`), so an
      uncapped relay through it allocates the whole file server-side.
      **Uploads and the incus READ direction are done** — see the streaming entry above for
      both, including why the incus write direction deliberately still buffers.
      **Entry-count cap: WIDENED 2026-08-02, not removed, and the distinction is the
      point.** The listing had been sharing the terminal's `maxToolCapture`; it now has its
      own `maxListCapture` (8 MiB, roughly 100k entries), because the two callers want
      different things — a terminal command is tool output, a directory listing is data
      whose size is set by how many files someone happens to have. `containerFS.Exec` grew
      a `limit` parameter so the bound is the caller's, and the test fake applies it, since
      a fake that ignored it would make every truncation test pass for the wrong reason.
      `TestContainerListingHoldsThousandsOfEntries`; neutralized by restoring the shared
      bound, which loses 2102 of 9000 entries.
      STILL OPEN: it is a bigger number, not the absence of one. Removing the bound means
      paging the glob loop, which costs an exec round trip per page and can tear when the
      directory changes between pages — a trade worth making deliberately, not silently.
      A listing past the bound still reports `Truncated`, and a copy still refuses rather
      than producing a partial tree.
      — *source: plan review 2026-08-02 — unified FS operation planner, corner-case register*

## `web-fs.star`: the kube arm has now been executed (2026-08-02)

- [x] **The redirect arm of `e2e/scenarios/web-fs.star` was unverified; it has since been
      RUN against a real kind cluster and passes.** It found a live kubernetes defect on
      its first run (the exec working directory the container listing depended on — see the
      JOURNAL entry "The kube arm found a live bug on its first run") and corrected a false
      assertion of its own: kubernetes cannot do cp/archive but CAN exec, so an image path
      still lists and only the copy fails. What follows is the original entry, kept for the
      record of what was checked. The relay arm
      (docker/containerd/bare/incus) was written, run and NEUTRALIZED against a live docker
      backend — restoring the close-twice defect in `containerStream.Close` makes it fail
      with `scenario timed out ... Post ".../fs/copy?source=virtual&path=webfs-app/work/seed.txt":
      context deadline exceeded`. The `TARGET == "kube"` arm was not run: there is no kind
      cluster on the machine it was written on, and client-local bind mounts need root to
      kernel-9p-mount on every other target, so there was nowhere else to exercise it.
      It IS parse+resolve-checked (`make e2e-check`), and the containerized runner's
      default `e2e/scenarios/*.star` glob picks it up, so **CI is its first real run** and
      a mistake surfaces as a red kube leg rather than as a silent pass.
      Check these first, in order of how unproven they are:
      (a) an ABSOLUTE external `:ro` bind source in a compose file — every proven bind
      scenario (`compose-mounts.star`, `deploy-mounts.star`) uses a repo-relative source,
      so the `%s:/ro:ro` line is the least-trodden path in the file;
      (b) `stop(name = app)` on a compose-managed kube deployment mid-scenario, then
      browsing the bind through the BFF;
      (c) `wait(..., timeout = "300s")` — the pod pulls `cornus:e2e` and starts a mount
      sidecar, and 240s is what `compose-mounts.star` uses for a simpler pod.
      The JSON shapes it reads were checked against the source rather than guessed
      (`fsRoots.roots`, `fsRoot.id`, `fsRoot.readOnly`), so a wrong field name fails the
      `assert_true(ro_id != "")` loudly rather than skipping the block.
      — *source: JOURNAL 2026-08-02 — E2E for the BFF file explorer*

## Touch parity in the tiled workspace: what was deliberately left out (2026-08-02)

The tap-reachable pane menu, tap-to-move, and the divider hardening landed
(JOURNAL 2026-08-02 — "Tiling chrome: making pane splitting reachable on touch devices").
These three were scoped out of that change on purpose and are each independently useful.

- [ ] **Whole-stack tap-move.** *Partly overtaken, 2026-08-03: the tab drag is now a POINTER
      drag on every device (`web/src/dnd.ts`), so dragging `.stack-tabs`'s empty area moves a
      whole tile with a finger too. What is still missing is the TAP route — a menu entry, for
      anyone who cannot perform a drag at all.* The `⋮` menu offers `beginPick(id, "pane")`
      only. The protocol already supports the rest: `createTileDrag` takes a `DragKind`, and
      `drop()` routes `"stack"` to `stackStackById`/`moveStackById`. The work is one more menu
      entry ("Move tile…", offered only when the tile has >1 tab or there is somewhere to put
      it) plus `beginPick(props.node.id, "stack")`, and `offersTargets()` already handles the
      stack-onto-itself case. *source: scoped out of the touch-parity change, 2026-08-02*

- [ ] **Debounce the layout `localStorage` write during a divider drag.** Every
      `pointermove` runs `snapshot()` (a parse∘stringify), `setRatio`, `reconcile`, then
      the persist effect's own `JSON.parse(JSON.stringify(state.tree))` **and** a second
      `JSON.stringify` inside `saveLayout` — three full serializations plus two deep clones
      at pointer rate, into a synchronous disk-backed store. On a phone this is the most
      expensive thing in the drag. Two separable pieces: (a) the `JSON.parse` in both
      persist effects (`Files.tsx`, `Terminal.tsx`) is **pure waste** — it detaches a tree
      that is immediately re-stringified, and deleting it removes a third of the work with
      no timing change, because `JSON.stringify` over the store proxy still deep-tracks;
      (b) a `scheduleSaveLayout`/`flushLayoutSaves` pair in `layout.ts` coalescing the
      burst into one trailing write, flushed on `onCleanup`, at the end of a drag, and on
      `pagehide` — without that last part a reload within the debounce window silently
      loses the resize, which is worse than the jank. *source: scoped out of the
      touch-parity change, 2026-08-02*

- [x] **The Files pane's own drag-and-drop is still touch-dead.** *Done 2026-08-03 (JOURNAL —
      "Pane dragging doesn't work on touch devices"): rows are drag sources and folders are
      drop targets through `web/src/dnd.ts`, whose `"auto"` transport keeps the real HTML5
      drag for the mouse (so the OS-file drop and the Chromium drag-out download are
      untouched) and emulates one from pointer events for a finger. A touch drop asks
      copy-or-move, since there is no Shift to hold.*

- [ ] **The Files pane's marquee band-selection is still mouse-only.** What was left of the
      item above. `beginDragSelect`/the band sweep are `mousedown`/`mousemove` on
      `document`/`window` (`FilePane.tsx`, "Band-select"), so a finger cannot sweep a range;
      selection itself has a tap route via rows, and shift/ctrl-click has no touch equivalent
      either. Note the two gestures deliberately cannot share a press — the row's `draggable`
      is toggled per press to keep them apart — so a touch sweep would need a different
      discriminator from the drag's dwell (a handle, or a selection mode). Worth doing only
      if selecting ranges on a phone turns out to matter. *source: split out of the Files
      touch-DnD item when the drag half landed, 2026-08-03*

- [ ] **`promptChoice`'s `layout: "list"` has no production caller.** It existed for the
      tile ⋮ menu, which is now the command palette seeded with `:pane`
      (`views/tiling/panes.tsx`), so nothing in the app passes it — only the test that keeps
      the capability covered (`views.test.tsx`, "lays a list-layout choice out as a
      column"). Either give it a caller or retire the whole path together: the field in
      `modal.ts`, the `classList`/hint branches in `ModalHost.tsx`, `.modal-options.list` in
      `styles.css`, and that test. Deliberately not done in the same change, because another
      agent had `modal.ts` and `ModalHost.tsx` open at the time.
      *source: dropped the ⋮ context menu, 2026-08-02*

## Phase 2 fsop: what it deliberately does NOT cover (2026-08-02)

The caretaker filesystem operator landed (see JOURNAL 2026-08-02 — "Phase 2: the caretaker
filesystem operator"). These are the edges it consciously left, each with the reason, so a
later pass does not rediscover them as bugs.

- [ ] **A kubernetes pod with volumes but NO other caretaker reason gets no operator.**
      `addFSOpRole` folds into whichever caretaker the pod already has; it never creates
      one. So a plain `Deployment` with a PVC and no mounts/hub/docker/agent-forward/proxy
      still cannot be read or written through the explorer. Making the operator
      unconditional means an extra container in EVERY volume-carrying pod, which is a real
      cost and a change to every user's pod spec — the kind of decision that should be
      made deliberately, not inherited from a file-browser feature. Options: a spec opt-in
      (`api.DeploySpec` flag, plumbed from compose), or accept the current
      "rides an existing caretaker" scope and say so in the docs.
      — *source: Phase 2 implementation, 2026-08-02*

- [ ] **Only kubernetes implements `deploy.FSOperator`.** dockerhost/containerdhost/
      barehost/incushost all register caretakers in remote mode and could serve the same
      role, and containerd/bare could do better still: their named volumes are plain
      directories under the server's `DataDir`
      (`hostrun/volumes_linux.go:98-111`, with `ValidateVolumeName` and `underDir`
      containment already enforced), so an IN-PROCESS realization would work **while the
      workload is stopped** — something no sidecar can match. Not done because those
      backends already have working archive primitives, so the operator is an
      optimization there rather than the difference between usable and not.
      Whatever lands must run a shared conformance suite against the caretaker
      realization, or the two will disagree about directory-onto-directory.
      — *source: Phase 2 implementation, 2026-08-02*

- [ ] **The container LISTING still goes through exec, even on a volume the operator
      serves.** `containerList` runs the NUL-framed `listScript` glob loop; `api.FSOpList`
      exists, is implemented in the caretaker, and is not consulted. Wiring it would
      retire one of the three legacy execs on volume-backed paths and give the listing
      real errnos plus a `Truncated` flag that means something. Not done in the same
      change because the listing has its own tar fallback (`containerListTar`) and its own
      truncation semantics, and changing all three at once is how a subtle
      entry-classification regression gets in. The `mv` and `rm -rf` execs ARE now
      bypassed on operator-served paths (`fsopUnary`).
      — *source: Phase 2 implementation, 2026-08-02*

- [ ] **The zero-bytes-moved guard on the archive-to-fsop fallback is now belt-and-braces,
      and it is untested.** This entry was filed describing that guard as the PRIMARY
      mechanism; the live kube run proved that design broken and it was replaced the same
      day, so what follows is the corrected, narrower residue.
      What happened: a retry after the archive call fails has to be gated on nothing having
      moved, because a spent stream cannot be replayed. But a PUT streams its tar from a
      pipe, so Go's transport had already consumed bytes by the time the server's 501
      arrived — the guard fired correctly and the fallback never happened. Every write on
      kubernetes stayed a 501. `clientContainerFS` now LEARNS whether the backend has an
      archive from a bodyless `StatPath` (a HEAD, hence the one safely retryable call) and
      routes accordingly before touching a stream;
      `TestArchivelessBackendRoutesWritesToTheOperator` drives the real type over a real
      `pkg/client` and neutralizes back to the broken shape.
      STILL OPEN: `cw.n > 0` / `cr.n > 0` survive as a second line of defence for a backend
      that streams some bytes and only then answers 501. Nothing exercises that path —
      kubernetes fails before touching the stream, and the learned answer normally makes it
      unreachable. A fake that writes and then 501s would pin it.
      — *source: Phase 2 implementation, 2026-08-02; corrected the same day*

- [ ] **The kube E2E arm needs a hand-loaded `cornus:e2e` and nothing enforces it.**
      `KubeTarget.Setup` creates the kind cluster but does not build or load the sidecar
      image; only the containerized runner's `prepare_kube` does. So a direct
      `make e2e-kube` against a pre-existing cluster tests whatever binary was loaded into
      it last, and a caretaker-side regression surfaces as an inexplicable server-side
      failure — which is exactly how the Phase 2 run burned two cycles. Options: have
      `KubeTarget.Setup` build and load it (costs a docker build per run), or have it
      compare the loaded image's digest against the binary under test and fail loudly.
      Doing nothing is also defensible, but then it belongs in the preflight output rather
      than only in TESTING.md.
      — *source: JOURNAL 2026-08-02 — Phase 2: the caretaker filesystem operator*

- [ ] **The pane chooser's panel can cover the tile it is reporting on.** It is anchored to the
      workspace's top-right corner (as asked for), so previewing the top-right tile hides part of
      the thing being previewed. Tolerable at the sizes tested and no worse than tmux, which takes
      over the pane outright. If it bites: flip the panel to the opposite corner while the
      previewed tile is the one underneath it, or shrink it to the tile labels alone. Do not
      centre it — the middle of the workspace is where a split's divider usually is.
      **Sharper since the numbers landed (2026-08-02)**: the number plate is centred in each
      tile, so for the top-right tile the plate is exactly what the panel hides — and a plate is
      only worth drawing because it is glanceable. Not lost (that tile's number is also on its
      tab, which is nowhere near the panel), but the fix is worth more now than it was.
      — *source: JOURNAL 2026-08-02 — Choose a pane from a list, walked with the arrows*

- [x] **Two panes at the virtual root are indistinguishable rows in the pane chooser.** The list
      labels each pane with `ctx.tabTitle`, which is right (it is the name the user already knows
      it by) but at the Files root that name is "All" for every such pane.
      **DONE 2026-08-02**: every pane now wears its 1-based position — on its row, on its tab, and
      as a large plate centred in its tile (tmux's display-panes) — and `1`–`9` jump the walk
      there. Drawn as a digit in a CSS circle, deliberately not the Unicode circled forms. What
      remains of the original thought is `prefix q` itself: the plates exist now, so a
      display-panes command that shows them WITHOUT the list would be a small addition.
      — *source: JOURNAL 2026-08-02 — Choose a pane from a list, walked with the arrows*

- [ ] **`numberOf` lives on `TileChoose` but is no longer only the chooser's.** The tab badge
      is standing (`settings.paneNumbersInTabs`), so `props.ctx.choose.numberOf(pane.id)` now
      renders a permanent affordance through the interface of a transient mode. The SHARING is
      correct — the tab's digit and the chooser's row have to agree, so computing it twice is
      the thing to avoid — only the home is wrong. Moving it to `TileCtx` is a rename touching
      both hosts and the two call sites in `panes.tsx`; do it next time that interface is open
      for another reason, not on its own.
      — *source: JOURNAL 2026-08-02 — Numbered panes, circled without the codepoint*

- [ ] **The effective (image-derived) entrypoint is unreachable, so shell discovery is
      blind for a workload outside the loaded compose project.** `resolveShellCandidates`
      reads the shell a workload's `entrypoint:`/`command:` names from `s.plans[svc].Spec`
      — the LOCAL compose plan. There is no read-back: `api.DeployStatus` carries no argv,
      `pkg/client` has no inspect call, and no backend persists the applied spec. So a
      workload deployed from another machine (or by `cornus deploy`) contributes neither
      its entrypoint's shell nor its `shells` list, and discovery starts at the connection
      context. Two ways to close it, both larger than the feature that exposed it: a new
      `api.DeployStatus` field populated per backend, or a client-side image-config fetch
      over the registry seam `pkg/client` already exposes (`RegistryToken` /
      `RegistrySecure` / `RegistryTransport`), which is how `pkg/dockerproxy/images.go`
      already reads `Config.Entrypoint`. Note `/proc/1/cmdline` is NOT a substitute: it is
      the flattened live argv with no ENTRYPOINT/CMD split, and reading it needs either an
      exec (the chicken-and-egg this feature exists to break) or an archive read that
      kubernetes refuses.
      — *source: JOURNAL 2026-08-02 — Auto shell exec discovery*

- [ ] **`Server.plans` is a start-up snapshot, so an edited `x-cornus-shells` needs a
      `cornus web` restart.** `loadProject` runs once in `New` (`webbff.go:162`) and is
      never re-run. Pre-existing and shared by every project-shaped view — the shell list
      is simply the first field a user is likely to edit and immediately re-test, so it is
      the one that makes the staleness felt. Fixing it means deciding what invalidates the
      snapshot (an mtime check on the compose files at request time is the cheap answer)
      and is a change to the BFF's project lifecycle, not to discovery.
      — *source: JOURNAL 2026-08-02 — Auto shell exec discovery*

- [ ] **Copy-and-rename in one step is gone from the Files screen.** The retired
      `files:copy` prompt asked a single selected row for a full virtual PATH, prefilled
      with the source, so editing the last segment renamed while copying. Its replacement —
      the pane chooser's "an arbitrary location" — asks for a FOLDER, because a field that
      means "path" or "folder" depending on the selection size is one you have to count rows
      to read. Copy then Rename (`e`) is the two-step form. Worth reconsidering only with a
      design that does not overload one field: a second "Copy as…" command for the
      single-row case, or a rename field beside the destination in the same prompt.
      — *source: JOURNAL 2026-08-02 — F5 / F6: cross-pane copy and move*

- [ ] **`prefix y` is free on the Files screen and nothing says so to a future author.**
      It was Copy until the cross-pane transfers absorbed that command, and it was left
      unbound deliberately: `Ctrl+Shift+C` / `F5` were asked for by name, and a third
      spelling on Copy with no twin on Move would be an asymmetry nobody could predict.
      `views.test.tsx` asserts `bind("y")` is undefined, so the day something claims it that
      line fails and this decision gets re-made rather than forgotten — which is the intent,
      not a defect. Listed so the failure is recognized as a prompt and not as a bug.
      — *source: JOURNAL 2026-08-02 — F5 / F6: cross-pane copy and move*

- [ ] **Ending a selection range ON a folder's own name no longer works in the Files
      listing.** Shift-click on a folder's NAME opens it in a new pane (the browser's
      "modified click on a link opens it elsewhere"); the name is the widest target in its
      row, so this is a real cost, not a theoretical one. Shift-clicking any other part of
      that row, or any part of a file's row, still extends the range. If it bites, the fix
      is not to narrow the gesture — it is to give the row a larger non-link hit area, or to
      move the gesture onto a modifier the listing does not already own (Alt is free).
      — *source: JOURNAL 2026-08-03 — the two keys from the entry above, corrected*

- [ ] **The pane chooser's "an arbitrary location" row is reachable by typing and by
      clicking, but not by the walk.** It is deliberately outside the arrow/digit walk —
      it has no tile, no number and no place on screen, and a row between two tiles would
      renumber them. That leaves no keyboard route for a user who wants it but does not want
      to start typing a path yet. A dedicated key (Tab is taken; `/` would read as "type a
      path") would close it without touching the numbering.
      — *source: JOURNAL 2026-08-02 — F5 / F6: cross-pane copy and move*

- [ ] **Splitting a terminal in the first moments of its life opens a session too many.**
      Between "the create request was sent" and "the pane recorded the session id", the pane
      holds a workload and command with no `sessionId`. A split in that window rebuilds the
      original pane, whose create effect cannot see a session and opens another — three
      sessions for two panes, and the extra one is never attached to or killed. Pre-existing
      (the Terminal screen had the same window); surfaced while writing the split test,
      which now waits for `.xterm` rather than for the create. A fix probably belongs in
      TermPane: the `creating` guard is an instance-local boolean, so it does not survive
      the remount that a split performs, and the pane needs a way to know a create is
      already in flight for its id.
      — *source: JOURNAL 2026-08-03 — Files and Terminal become one Workspace*

- [ ] **Two pane-to-host protocols still live side by side in the Workspace.** A terminal
      pane talks back through a static `TermCtx` prop; a file pane REGISTERS its actions in
      a `Map` + revision signal, because which actions exist depends on what it is doing
      right now. Both are defensible and the merge deliberately kept both — unifying them
      would touch every file action for no user-visible gain — but one host now owns two
      answers to "how does a pane reach the workspace", and a third kind of pane would have
      to pick one without a stated rule for which.
      — *source: JOURNAL 2026-08-03 — Files and Terminal become one Workspace*

- [ ] **`views/terminal/` holds three modules that are not the terminal's.** `CommandPalette`
      (the whole app's palette), `prefix` (the app-wide key machine) and `shells` (read by
      `settings.ts`) live under a directory named after one pane kind, and are imported from
      `App.tsx`, `command-center.ts` and `settings.ts` respectively. The merge left them
      alone on purpose — moving them is three unrelated edits and filing them under
      `workspace/` would be a worse name, not a better one — but the directory currently
      misdescribes a third of its contents.
      — *source: JOURNAL 2026-08-03 — Files and Terminal become one Workspace*

- [ ] **Six translated pages were already stale before the Workspace merge and were left
      that way.** `docs:check-translation-freshness` reports `ja`/`zh` for `cli/compose.md`,
      `guides/observability.md` and `reference/connection-config.md` behind their English
      sources; none of those files were touched by this change. Recording them without
      reading them is the one use that defeats the mechanism, so they were not recorded.
      Each needs a read against the current English.
      — *source: JOURNAL 2026-08-03 — Files and Terminal become one Workspace*

- [ ] **After the prefix, an unclaimed key is typed into the editor.** The post-prefix
      escape hatch hands an unbound second key to the browser (that is the whole "pass
      browser shortcuts" path, and `dispatchAppKey` documents it), but inside CodeMirror
      "the browser" means text insertion: `prefix d` in an editor pane puts a `d` in the
      file. Confirmed in Chromium — the prefix itself works there, across all four presets;
      it is only the fall-through that misfires. The fix is probably to swallow an unclaimed
      post-prefix key when the target is a text-entry context, since a browser shortcut is
      not what a letter means there — but the escape hatch is deliberate, so it is a
      decision and not a repair.
      — *source: JOURNAL 2026-08-03 — Open becomes a command*

- [ ] **`F5` is not swallowed when focus is inside CodeMirror, and that is where the draft
      is.** `dispatchAppKey` skips the direct lookup for `input, textarea, select,
      [contenteditable]`, so the transfers' keys do nothing in an editor pane — and the
      reason a disabled direct command still swallows its key (falling through to Reload
      discards every unsaved draft, which `files/drafts.ts` holds in memory only) applies
      hardest in exactly the pane the guard does not cover. Whether the browser actually
      reloads was NOT established: Playwright's synthetic `F5` does not invoke browser-chrome
      shortcuts, so the probe cannot answer it. Needs one real keypress in a real browser
      before anything is changed.
      — *source: JOURNAL 2026-08-03 — Open becomes a command*

- [ ] **The mock BFF on :5080 was found dead mid-session and no crash was reproduced.** While
      driving the user's running `dev:mock`, the mock server had exited; every `/.cornus/web/*`
      call proxied through Vite answered 500, which the file pane renders as its error state.
      Restarting it restored the environment, and ~500 further double-click gestures plus the
      whole SPA suite did not kill it again, so this is unexplained rather than fixed. The
      suspicious paths, none confirmed: `mock/server.ts` `server.on("upgrade")` calls
      `socket.destroy()` on a raw socket with no `error` listener attached (an ECONNRESET there
      is an uncaught exception), and `readBody(req).then(...)` swallows nothing — a throw inside
      `handleFs` becomes an unhandled rejection, which Node has treated as fatal since v15. If
      it recurs, capture the mock's stderr (`node mock/server.ts 2>&1 | tee`) rather than
      inferring: the process prints its own stack, and the 500s the SPA shows say nothing about
      which of the two it was.
      — *source: JOURNAL 2026-08-03 — The explorer "stops working" after a double-click*

- [ ] **A click on the tile while the placement pick is armed cancels it silently.** By design
      (`.pane-pick-overlay` is `pointer-events: none`, and the scrim underneath takes the tap),
      but the feedback is only the overlay disappearing, so a user who did not notice the
      overlay reads the click as "nothing happened" and clicks again — which now acts on the
      row. That is the shape the double-click bug wore before it was diagnosed, and it will
      wear it again for anyone who arms the mode by accident. Worth considering whether a
      cancelling tap should say so (a toast, or the hint pill fading out from where it was)
      rather than just vanishing. Not a defect; a legibility decision.
      — *source: JOURNAL 2026-08-03 — The explorer "stops working" after a double-click*

- [x] **Session title and cwd: OSC first, /proc probe as the fallback.** `osc.go` sniffs
      `ESC ] 0/2` (title), `ESC ] 7` (cwd) and a private `ESC ] 5379` (the session's pid) off
      the stream. Where the shell says nothing, `procprobe.go` reads
      `/proc/<tpgid>/{cwd,comm}` with one lazy exec, anchored on the pid the server's launch
      wrapper announced (`pkg/shells.WrapAnnouncePID`, opt-in via
      `api.ExecConfig.AnnouncePID`). **Done 2026-08-03**; the env-var `PROMPT_COMMAND` route
      was REJECTED rather than deferred, because it fights a user's own env injection.
      — *source: JOURNAL 2026-08-03 — Announcing the session pid from inside*

- [ ] **Arbitrary exec env injection is uneven across the clients.** `cornus compose exec` has
      `--env` (`cmd/cornus/internal/composecli/exec.go`, `parseExecEnv` -> `ExecConfig.Env`,
      honoured by every backend), and the server injects `SSH_AUTH_SOCK` itself on
      `ForwardAgent` (`pkg/server/deploy_exec.go`). Nothing else can set it: plain
      `cornus exec` has no `--env` flag, `createTermRequest` (the web terminal API) has no env
      field, and a client context declares `shells` but not `env`. Noted by the user on
      2026-08-03 while reviewing the pid wrapper. The wrapper itself is env-TRANSPARENT — it
      rewrites `Cmd` only and inherits the environment — so this is a gap to fill rather than
      a conflict to resolve. If filled, keep it cooperative with the two existing injectors:
      appending rather than replacing is what stops a caller's `SSH_AUTH_SOCK` or a future
      hook from being silently clobbered.
      — *source: JOURNAL 2026-08-03 — Announcing the session pid from inside*

- [x] **`make e2e-web-terminal` verified on kube as well as docker.** **Done 2026-08-03.** The
      kube run caught a scenario bug docker could not: it passed `dir: "/usr"`, but
      kubernetes' pods/exec subresource has no working-directory field, so the backend warns
      and ignores it and the session started at `/`. The scenario now reaches its directory
      with `cd` inside the command (portable), and its second session deliberately contradicts
      its `dir` so echoing the request back cannot pass.
      — *source: JOURNAL 2026-08-03 — Running the terminal-introspection E2E on kube*

- [ ] **`e2e-one`, `e2e-web` and `e2e-web-fs` delete a pre-existing kind cluster.**
      `KubeTarget.Teardown` (`pkg/e2e/target.go`) runs `kind delete cluster` unconditionally
      unless `--keep`, while `Setup` REUSES a cluster it finds — so running any of these with
      `TARGET=kube` against a cluster somebody else prepared destroys it. Only `make e2e-kube`
      threads `KEEP` through (`$(if $(KEEP),--keep,)`); `e2e-web-terminal` was given the same
      treatment on 2026-08-03. The other three are a one-line fix each and were left alone
      only because they were outside that change. Verify before acting: check whether the
      Makefile recipes still lack the `$(if $(KEEP),--keep,)` fragment.
      — *source: JOURNAL 2026-08-03 — Running the terminal-introspection E2E on kube*

- [ ] **`kind` is not installed on this host, so the kube E2E target cannot run unaided.**
      Preflight fails with "kind not on PATH" even though the `cornus-e2e` cluster exists and
      `kubectl` (via mise) reaches it. Worked around on 2026-08-03 by fetching the binary to
      `./.agents-workspace/tmp/bin/kind` for the duration of a run. Note the host is aarch64:
      the amd64 build runs under emulation and LOOKS fine, so grabbing the wrong one silently
      slows every kube run. A permanent fix is `mise use -g kind@latest`, matching how kubectl
      is already managed — not done unasked, since it changes the global toolchain.
      — *source: JOURNAL 2026-08-03 — Running the terminal-introspection E2E on kube*

- [ ] **Pane pinch-zoom has never been exercised on a real touch device.** The gesture
      (`web/src/pinch.ts`, opt-in via `settings.paneZoom`) is covered by synthesized pointer
      events in jsdom, which has no PointerEvent at all — the tests define `pointerId` /
      `pointerType` onto MouseEvents. Two things only a device can settle: whether iOS Safari
      honours the inline `touch-action: pan-x pan-y` for pinch (the `gesturestart`
      preventDefault is belt-and-braces for exactly this doubt), and whether a two-finger
      pinch that starts near a tile edge also arms a split bar — `panes.tsx`'s capture-phase
      `pointerdown` sees both fingers, and the second one landing off an edge is what is
      relied on to `disarm()`.
      — *source: JOURNAL 2026-08-03 — Opt-in pinch-zoom for pane contents*

- [ ] **Nothing tests that a zoomed terminal actually changes `term.options.fontSize`.** The
      `Terminal` instance is local to `Term`'s `onMount` and stays there on purpose, so the
      effect's write is unobservable. `termFontPx` is unit-tested and the wiring is one line,
      but it rests on review. A test would need a seam — e.g. `Term` reporting its applied px
      through a callback — which is worth weighing against adding a prop only tests use.
      — *source: JOURNAL 2026-08-03 — Opt-in pinch-zoom for pane contents*

- [ ] **Review which programs the built-in detection rules are scoped to.** As of 2026-08-03
      every rule in `cmd/cornus/internal/webbff/rules.toml` carries an `agents` list, and that
      list decides whether the feature fires at all. The assignments were chosen by the agent,
      not derived from anything in the repo: agent-UI-shaped rules to `["claude"]` (the only
      agent the rule set names), credential prompts to
      `["ssh","sudo","su","passwd","gpg","git","scp","sftp"]`, press-enter-to-continue to
      `["claude","apt","apt-get","dpkg","yum","dnf"]`. Two consequences worth a decision:
      a coding agent other than claude currently gets NO detection, and a generic installer
      prompting `[y/n]` is missed unless its binary is listed. Users can extend via
      `~/.config/cornus/agent-detection/*.toml`, and an unscoped rule there still applies to
      every session — but the shipped defaults are what most people will run.
      — *source: JOURNAL 2026-08-03 — Detection rules now scope to the LIVE foreground program*

- [ ] **DEFECT: agent scoping keys on `/proc/<tpgid>/comm`, which cannot name an interpreted
      agent.** Introduced 2026-08-03 with the rule scoping. Measured: `node -e ...` reports
      `comm = "node-MainThread"` (comm is TASK_COMM_LEN-truncated to 15 chars and names the
      interpreter's main thread), while `cmdline` is the full argv. Claude Code is a Node
      program, so the shipped `agents = ["claude"]` scopes match nothing for it — the detection
      feature is not merely narrowed, it is inert for any agent behind node/python/a shim.
      The transport underneath is fine and was verified in a real container exec: tpgid tracked
      the foreground job (7 -> 20) with `comm=sleep cmdline="sleep 60"`.
      Fix, following herdr's `src/detect/mod.rs` (vendored manifests are already in-tree at
      `third_party/herdr/`): have `procProbeScript` also read `/proc/<tpgid>/cmdline`
      (NUL-separated), then resolve the name as herdr does — if `argv[0]`'s basename is a
      generic runtime or shell, unwrap the agent from the remaining argv using per-runtime flag
      knowledge (`-e`/`--eval` for node/bun, `-c`/`-m` for python, `-c` for shells); else use
      `argv[0]`'s basename; else canonicalize the path to resolve shims; else match known
      package paths. Agent names should come from the bundled manifests' `id`/`aliases`, not
      from the hardcoded lists currently in rules.toml.
      **The E2E does not cover this**: `web-terminal-introspect.star` runs
      `sh -c 'exec sleep 300'`, where `exec` makes the announced pid BE the final program, so it
      exercises the pid plumbing and never a runtime-wrapped agent. Any fix needs a scenario
      where the foreground program is reached through an interpreter.
      — *source: JOURNAL 2026-08-03 — Can the detector actually name the foreground process?*

- [ ] **Retire or migrate the cornus-native detection rule schema.** As of 2026-08-03
      classification runs on `pkg/agentdetect` against the vendored herdr manifests, so
      `cmd/cornus/internal/webbff/rules.toml`, `detect_rules.go`, `detector.rules` and the user
      extension path `~/.config/cornus/agent-detection/*.toml` are inert. `TestDefaultRulesMatch`
      and friends still exercise the dead schema; `TestDetectUserOverrideRule` is skipped with a
      pointer here. Decide: port the user-extension path to the manifest schema (upstream reads
      overrides from the same directory shape, `<agent>.toml`), or remove it and say so in the
      docs. Do not leave it looking live.
      — *source: JOURNAL 2026-08-03 — A herdr-compatible agent detector*

- [x] **E2E covers agent CLASSIFICATION.** **Done 2026-08-03** —
      `e2e/scenarios/web-agent-detect.star`, passing on docker and kube. Two-sided: an agent
      session and a plain shell showing the identical screen, exactly one blocked.
