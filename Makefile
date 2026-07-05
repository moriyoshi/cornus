# cornus build + E2E targets.
#
# The e2e-docker / e2e-kube targets run the Starlark E2E harness against a real
# Docker host or a kind cluster. They require:
#   - docker        (both targets; and the build engine, which needs root or a
#                    rootless user-namespace stack for the build scenario)
#   - kind, kubectl (kube target)
# See e2e/scenarios/*.star and README "E2E test harness".

GO ?= go
BIN := bin
SCENARIOS := \
	e2e/scenarios/registry.star \
	e2e/scenarios/registry-advertise.star \
	e2e/scenarios/compose-build-group.star \
	e2e/scenarios/registry-persistence.star \
	e2e/scenarios/registry-edges.star \
	e2e/scenarios/registry-mirror.star \
	e2e/scenarios/registry-host-native.star \
	e2e/scenarios/registry-s3.star \
	e2e/scenarios/registry-gcs.star \
	e2e/scenarios/registry-azblob.star \
	e2e/scenarios/cli.star \
	e2e/scenarios/mcp-stdio-protocol.star \
	e2e/scenarios/mcp-stdio-tools.star \
	e2e/scenarios/activity-follow.star \
	e2e/scenarios/deploy.star \
	e2e/scenarios/deploy-stats.star \
	e2e/scenarios/deploy-config.star \
	e2e/scenarios/deploy-portforward.star \
	e2e/scenarios/deploy-autoforward.star \
	e2e/scenarios/deploy-tunnel.star \
	e2e/scenarios/deploy-tunnel-tailscale.star \
	e2e/scenarios/ftp.star \
	e2e/scenarios/ftp-active.star \
	e2e/scenarios/ftp-usernet.star \
	e2e/scenarios/compose.star \
	e2e/scenarios/compose-down.star \
	e2e/scenarios/compose-logs.star \
	e2e/scenarios/compose-profiles.star \
	e2e/scenarios/compose-dependson.star \
	e2e/scenarios/compose-deps.star \
	e2e/scenarios/compose-down-volumes.star \
	e2e/scenarios/compose-redeploy-oneshot.star \
	e2e/scenarios/compose-up-signal-teardown.star \
	e2e/scenarios/compose-watch-reload.star \
	e2e/scenarios/connection-profile.star \
	e2e/scenarios/deploy-sshtunnel-docker.star \
	e2e/scenarios/config-from-file.star \
	e2e/scenarios/conduit-grammar.star \
	e2e/scenarios/socks5-aliasing.star \
	e2e/scenarios/socks5-coexist.star \
	e2e/scenarios/socks5-ingress.star \
	e2e/scenarios/ingress-front-door.star \
	e2e/scenarios/ingress-tunnel-ssh.star \
	e2e/scenarios/ingress-tunnel-controller.star \
	e2e/scenarios/ingress-settings-controller.star \
	e2e/scenarios/socks5-ingress-tls.star \
	e2e/scenarios/socks5-ingress-longest-match.star \
	e2e/scenarios/compose-conduit-mismatch.star \
	e2e/scenarios/web-conduit-join.star \
	e2e/scenarios/socks5-mount.star \
	e2e/scenarios/incluster-portforward.star \
	e2e/scenarios/incluster-kubeauth.star \
	e2e/scenarios/incluster-ingress.star \
	e2e/scenarios/incluster-preflight.star \
	e2e/scenarios/compose-build.star \
	e2e/scenarios/compose-build-ssh.star \
	e2e/scenarios/compose-mounts.star \
	e2e/scenarios/compose-mounts-multi.star \
	e2e/scenarios/devcontainer.star \
	e2e/scenarios/dockerd.star \
	e2e/scenarios/dockerd-attach-foreground.star \
	e2e/scenarios/dockerd-exit-code.star \
	e2e/scenarios/dockerd-exit-code-nonzero.star \
	e2e/scenarios/docker-push.star \
	e2e/scenarios/agent.star \
	e2e/scenarios/lifecycle.star \
	e2e/scenarios/lifecycle-restart.star \
	e2e/scenarios/deploy-mounts.star \
	e2e/scenarios/deploy-mounts-create-host-path.star \
	e2e/scenarios/deploy-mounts-multi.star \
	e2e/scenarios/deploy-oneshot.star \
	e2e/scenarios/deploy-crashloop.star \
	e2e/scenarios/credentials.star \
	e2e/scenarios/credentials-env-host.star \
	e2e/scenarios/credentials-endpoint-host.star \
	e2e/scenarios/credentials-sts.star \
	e2e/scenarios/credentials-ai.star \
	e2e/scenarios/credentials-ai-proxy.star \
	e2e/scenarios/credentials-github-proxy.star \
	e2e/scenarios/credentials-github-cli.star \
	e2e/scenarios/compose-credentials-proxy.star \
	e2e/scenarios/deploy-volumes.star \
	e2e/scenarios/deploy-named-volume.star \
	e2e/scenarios/deploy-network.star \
	e2e/scenarios/deploy-redeploy-network.star \
	e2e/scenarios/deploy-netpolicy.star \
	e2e/scenarios/deploy-netpolicy-enforce.star \
	e2e/scenarios/deploy-proxy.star \
	e2e/scenarios/deploy-proxy-coop.star \
	e2e/scenarios/deploy-proxy-mounts.star \
	e2e/scenarios/compose-egress-env.star \
	e2e/scenarios/compose-egress-project.star \
	e2e/scenarios/compose-egress-client-proxy.star \
	e2e/scenarios/deploy-egress-proxy.star \
	e2e/scenarios/deploy-egress-transparent.star \
	e2e/scenarios/deploy-dns.star \
	e2e/scenarios/deploy-remote-exec-agent.star \
	e2e/scenarios/deploy-multus.star \
	e2e/scenarios/deploy-multus-ipvlan.star \
	e2e/scenarios/deploy-multus-macvlan.star \
	e2e/scenarios/deploy-multus-detached.star \
	e2e/scenarios/deploy-cilium.star \
	e2e/scenarios/deploy-hub.star \
	e2e/scenarios/deploy-hub-udp.star \
	e2e/scenarios/caretaker-docker-endpoint.star \
	e2e/scenarios/deploy-shape.star \
	e2e/scenarios/deploy-ingress.star \
	e2e/scenarios/deploy-knative.star \
	e2e/scenarios/exec.star \
	e2e/scenarios/build-mounts.star \
	e2e/scenarios/build-cache.star \
	e2e/scenarios/build-invalidate.star \
	e2e/scenarios/build-upload.star \
	e2e/scenarios/build-lazy.star \
	e2e/scenarios/build-edge.star \
	e2e/scenarios/build-fail.star \
	e2e/scenarios/registry-errors.star \
	e2e/scenarios/registry-auth.star \
	e2e/scenarios/auth-build-deploy.star \
	e2e/scenarios/auth-ssh-key.star \
	e2e/scenarios/auth-ssh-key-session-cache.star \
	e2e/scenarios/auth-token-exchange.star \
	e2e/scenarios/hub-multireplica-redis.star \
	e2e/scenarios/hub-multireplica-kube.star \
	e2e/scenarios/hub-multireplica-credential.star \
	e2e/scenarios/deploy-errors.star \
	e2e/scenarios/cli-errors.star \
	e2e/scenarios/observability-trace.star \
	e2e/scenarios/observability-trace-otlp.star \
	e2e/scenarios/observability-store.star \
	e2e/scenarios/observability-export.star \
	e2e/scenarios/observability-compose-logs.star \
	e2e/scenarios/observability-observe-cli.star \
	e2e/scenarios/observability-mcp.star \
	e2e/scenarios/observability-replicas.star \
	e2e/scenarios/observability-metrics.star \
	e2e/scenarios/observability-telemetry-mux.star \
	e2e/scenarios/otel-collector.star \
	e2e/scenarios/compose-service-keys.star \
	e2e/scenarios/compose-configs-secrets.star \
	e2e/scenarios/compose-depends-completed.star \
	e2e/scenarios/compose-merge.star \
	e2e/scenarios/compose-env-file.star \
	e2e/scenarios/compose-exec.star \
	e2e/scenarios/compose-dns-resolution.star \
	e2e/scenarios/provider.star \
	e2e/scenarios/health-unhealthy.star

# build-lazy-9p.star is intentionally NOT in SCENARIOS: it needs the 9p kernel
# module (Cap9P), which preflight gates as an all-or-nothing fail-fast, so adding
# it here would abort the whole suite on hosts without 9p. Run it explicitly in a
# privileged, 9p-capable environment:
#   make e2e-one TARGET=local SCENARIO=e2e/scenarios/build-lazy-9p.star
#
# filecache-9p.star is likewise NOT in SCENARIOS for the same Cap9P reason: it
# drives the remote lazy path twice with CORNUS_FILE_CACHE=1 to prove the
# server-side block cache cuts the second build's 9P pull. Run it explicitly:
#   make e2e-one TARGET=local SCENARIO=e2e/scenarios/filecache-9p.star
#
# devcontainer-vscode.star is likewise NOT in SCENARIOS: it needs the official
# @devcontainers/cli binary (CapDevcontainerCLI, `npm install -g
# @devcontainers/cli`), which the same fail-fast preflight would demand of every
# `make e2e-docker` run. The containerized runner (e2e/container) bakes the CLI
# in and globs all scenarios, so CI executes it; locally:
#   make e2e-one TARGET=docker SCENARIO=e2e/scenarios/devcontainer-vscode.star
#
# deploy-mounts-sidecar-docker.star / deploy-mounts-sidecar-containerd.star
# exercise the caretaker-sidecar mount-relay path (CORNUS_DOCKER_REMOTE /
# CORNUS_CONTAINERD_REMOTE) added alongside pkg/deploy/{dockerhost,
# containerdhost}/mounts*.go — the dockerhost/containerd analogue of what
# deploy-mounts.star already covers for kube. deploy-remote-portforward-docker.star
# exercises the follow-on always-on remote companion (the same sidecar, now
# created for EVERY instance in remote mode regardless of --mount): it proves
# `cornus port-forward` (and, via the same ForwardPort bridge, `cornus tunnel`)
# reroutes through the companion instead of the server dialing the daemon's
# bridge network directly. All three self-skip without CORNUS_AGENT_IMAGE (a
# prebuilt cornus-embedding image; see entrypoint.sh's
# prepare_docker_agent_image for the docker target — the containerd target
# needs an equivalent image already present in containerd's OWN image store,
# e.g. via `ctr images import`, which is not yet wired into the containerized
# runner) and need privileged Docker/containerd (the companion runs with
# kernel-mount privilege), so they are kept out of the default
# SCENARIOS/SCENARIOS_CONTAINERD lists, same as build-lazy-9p.star. The
# containerized runner's docker-target glob picks them up automatically once
# CORNUS_AGENT_IMAGE is exported; run the containerd one explicitly once its
# image prerequisite is set up:
#   make e2e-one TARGET=containerd SCENARIO=e2e/scenarios/deploy-mounts-sidecar-containerd.star
#
# server-in-container.star exercises the server running AS A CONTAINER on the
# docker host it manages (pkg/hostenv, pkg/hostcheck): the one topology the rest
# of the suite cannot reach, since a host-run server shares the daemon's
# filesystem and never exercises path translation. It shares the same two
# prerequisites as the sidecar-mount pair above (CORNUS_AGENT_IMAGE, privileged
# Docker) and self-skips without them, so it lives here and is picked up by the
# containerized runner's docker-target glob once the image is exported.
#
# activity-flight-record.star exercises the flight recorder (pkg/activity) on the
# incident it exists for: a server killed while holding a live kernel 9P mount,
# and the next server recovering it from the record. It cannot be a unit test —
# a real mount needs root, so the Go tests only ever reach the "target already
# absent" path. Same prerequisites and self-skip as the two above.
#
# deploy-mounts-sparse-index.star is the sparse client-local mount-index ("m2
# gap") regression guard: a raw deploy-attach spec with a NON-local named volume
# interleaved between two client-local binds, which only a raw spec can express
# (compose and the docker proxy both route named volumes into spec.Volumes). It
# is docker-only and drives the dockerhost kernel-9P host-mount path, so — like
# async-write-docker.star — it needs root + the 9p module and stays out of the
# default SCENARIOS; the containerized runner's docker-target glob runs it.
#
# deploy-remote-exec-agent.star (default SCENARIOS, not here) exercises the
# same always-on companion's AgentRelayRole (`cornus exec --forward-agent`) on
# ALL THREE backends from one scenario body: it self-skips on docker/containerd
# without CORNUS_AGENT_IMAGE exactly like the pair above, but on kube — which
# needs no such gating (agent_forward is a plain per-deployment DeploySpec
# field, not an env-gated backend mode, and the kube target already loads a
# cornus:e2e sidecar image) — it runs for real unconditionally, so it lives in
# SCENARIOS rather than here.
#
EXTRA_CHECK_SCENARIOS := \
	e2e/scenarios/build-lazy-9p.star \
	e2e/scenarios/filecache-9p.star \
	e2e/scenarios/async-write-9p.star \
	e2e/scenarios/async-write-docker.star \
	e2e/scenarios/deploy-mounts-sparse-index.star \
	e2e/scenarios/devcontainer-vscode.star \
	e2e/scenarios/web.star \
	e2e/scenarios/web-fs.star \
	e2e/scenarios/web-terminal-introspect.star \
	e2e/scenarios/web-agent-detect.star \
	e2e/scenarios/deploy-mounts-sidecar-docker.star \
	e2e/scenarios/deploy-mounts-sidecar-containerd.star \
	e2e/scenarios/registry-host-native-containerd.star \
	e2e/scenarios/deploy-remote-portforward-docker.star \
	e2e/scenarios/server-in-container.star \
	e2e/scenarios/server-in-container-containerd.star \
	e2e/scenarios/server-in-container-incus.star \
	e2e/scenarios/server-in-container-podman.star \
	e2e/scenarios/deploy-portforward-rootless-podman.star \
	e2e/scenarios/activity-flight-record.star \
	e2e/scenarios/deploy-server-restart.star \
	e2e/scenarios/deploy-reboot-survival.star \
	e2e/scenarios/deploy-egress-bare.star \
	e2e/scenarios/deploy-mounts-sidecar-bare.star

# Opt-in web-UI scenario: stands up `cornus web` against a real deployed compose
# project and asserts its /.cornus/web/* BFF, plus the detached-frontend proxy.
# Kept out of the default SCENARIOS (run it with `make e2e-web`); still syntax-
# checked via EXTRA_CHECK_SCENARIOS above.
SCENARIOS_WEB := e2e/scenarios/web.star

# Opt-in web FILE-EXPLORER scenario: the /.cornus/web/fs* surface against a live
# workload with a client-local bind mount. Separate from SCENARIOS_WEB because it
# is the only web scenario that needs a bind-mounted project (so it deploys a
# different fixture and takes longer), and because the thing it proves is a
# different thing: not that the BFF reports the backend correctly, but that the
# explorer's view and the CONTAINER's view of the same bytes agree. Run it with
# `make e2e-web-fs`; syntax-checked via EXTRA_CHECK_SCENARIOS above.
SCENARIOS_WEB_FS := e2e/scenarios/web-fs.star

# Opt-in web TERMINAL-INTROSPECTION scenario: a persistent session's foreground
# program and working directory, read out of /proc INSIDE the container via the pid
# the server's launch wrapper announced. Separate from SCENARIOS_WEB because it is
# the only web scenario whose subject spans the server (which wraps the launch), the
# shell (which announces), and a second exec (which reads /proc) — and because it is
# worth running on kube specifically, where api.ExecState.Pid is 0 and the announced
# pid is the only anchor there is. Run it with `make e2e-web-terminal`;
# syntax-checked via EXTRA_CHECK_SCENARIOS above.
SCENARIOS_WEB_TERMINAL := e2e/scenarios/web-terminal-introspect.star

# Opt-in web AGENT-CLASSIFICATION scenario: does a session whose foreground program
# is an agent get classified from that agent's manifest, and does the same screen in
# a plain shell stay quiet? Separate from the introspection scenario because it
# proves a different thing — not the pid/cwd plumbing but the manifest engine on top
# of it. Run with `make e2e-web-agent`; syntax-checked via EXTRA_CHECK_SCENARIOS.
SCENARIOS_WEB_AGENT := e2e/scenarios/web-agent-detect.star

# Opt-in BENCHMARK suite: performance scenarios that time real work and report
# throughput/latency (via the now()/benchmark()/bench_record() builtins). They
# live under e2e/benchmarks/ (outside the e2e/scenarios/*.star suite glob), each
# self-skips unless CORNUS_E2E_BENCH is set, and they run via `make e2e-bench`
# (which sets it). Parse-checked below in e2e-check. Set CORNUS_E2E_BENCH_JSON to
# a path to also collect the numbers as JSONL.
SCENARIOS_BENCH := e2e/benchmarks/bench-mount-write.star \
	e2e/benchmarks/bench-mount-modes.star

# Scenario subset for the podman target: the backend-agnostic core. podman shares
# the dockerhost orchestration (one Backend type, two engines), so these exercise
# the libpod engine rather than a separate deploy path. Keep in sync with
# PODMAN_SCENARIOS in e2e/container/entrypoint.sh — TestScenarioSubsetsInSync
# (pkg/e2e/scenariolists_test.go) fails `go test ./...` if the two drift.
SCENARIOS_PODMAN := \
	e2e/scenarios/mcp-stdio-protocol.star \
	e2e/scenarios/mcp-stdio-tools.star \
	e2e/scenarios/deploy.star \
	e2e/scenarios/deploy-stats.star \
	e2e/scenarios/lifecycle.star \
	e2e/scenarios/lifecycle-restart.star \
	e2e/scenarios/exec.star \
	e2e/scenarios/compose.star \
	e2e/scenarios/compose-down-volumes.star \
	e2e/scenarios/compose-exec.star \
	e2e/scenarios/compose-dns-resolution.star \
	e2e/scenarios/deploy-portforward-rootless-podman.star \
	e2e/scenarios/server-in-container-podman.star \
	e2e/scenarios/credentials-env-host.star \
	e2e/scenarios/credentials-endpoint-host.star

# Subset for the ROOTLESS podman target. Smaller than SCENARIOS_PODMAN on purpose:
# rootless changes what is REACHABLE, not what the libpod engine encodes, so the
# wire-level coverage is already earned on the rootful leg and re-running it here
# would buy repetition rather than assurance. What is listed is what rootlessness
# can actually break — the deploy/lifecycle/exec/compose core, netavark DNS under
# a user namespace, the port-forward refusal that only exists because of it, and
# the two credential scenarios, which come out DIFFERENTLY here: a file delivery
# is refused (the daemon runs as an ordinary user and cannot read a credential
# directory this server owns) while an endpoint delivery works, because a listener
# carries no ownership. That divergence is exactly what this leg is for.
# The client-local mount scenario is here for the same reason: it is the only leg
# where the 9P mount is made by one user and consumed by another, and it failed
# SILENTLY in two different ways before it was covered (an 0700 session directory,
# then a mount that never propagated into podman's mount namespace, leaving /data
# empty with nothing reporting an error).
# Keep in sync with PODMAN_ROOTLESS_SCENARIOS in e2e/container/entrypoint.sh —
# TestScenarioSubsetsInSync (pkg/e2e/scenariolists_test.go) fails `go test ./...`
# if the two drift.
SCENARIOS_PODMAN_ROOTLESS := \
	e2e/scenarios/deploy.star \
	e2e/scenarios/lifecycle.star \
	e2e/scenarios/exec.star \
	e2e/scenarios/compose.star \
	e2e/scenarios/compose-dns-resolution.star \
	e2e/scenarios/deploy-portforward-rootless-podman.star \
	e2e/scenarios/deploy-mounts-local-podman-rootless.star \
	e2e/scenarios/deploy-mounts-idmap-podman-rootless.star \
	e2e/scenarios/credentials-env-host.star \
	e2e/scenarios/credentials-endpoint-host.star

# Backend-agnostic subset for the containerd target: full build->deploy->wait
# (deploy.star), lifecycle actions, restart-monitor semantics, interactive
# exec, and compose. The rest of SCENARIOS is docker- or kube-specific
# (docker/kubectl builtins, port forwarding, user networks, hub, mounts,
# proxies) or self-skips, so running it against containerd would add nothing
# yet; grow this list as the backend's coverage grows. compose-down-volumes.star
# is here because `down --volumes` reaps a DIFFERENT artifact per backend (a host
# directory under <CORNUS_DATA> for containerd/bare, a Docker volume, a PVC), so
# the containerd/bare hostrun RemoveVolume path needs its own live run. Keep in sync with
# CONTAINERD_SCENARIOS in e2e/container/entrypoint.sh — TestScenarioSubsetsInSync
# (pkg/e2e/scenariolists_test.go) fails `go test ./...` if the two drift.
SCENARIOS_CONTAINERD := \
	e2e/scenarios/mcp-stdio-protocol.star \
	e2e/scenarios/mcp-stdio-tools.star \
	e2e/scenarios/deploy.star \
	e2e/scenarios/deploy-stats.star \
	e2e/scenarios/lifecycle.star \
	e2e/scenarios/lifecycle-restart.star \
	e2e/scenarios/deploy-server-restart.star \
	e2e/scenarios/exec.star \
	e2e/scenarios/compose.star \
	e2e/scenarios/compose-down-volumes.star \
	e2e/scenarios/registry-host-native-containerd.star \
	e2e/scenarios/compose-exec.star \
	e2e/scenarios/server-in-container-containerd.star \
	e2e/scenarios/credentials-env-host.star \
	e2e/scenarios/credentials-endpoint-host.star \
	e2e/scenarios/compose-dependson.star \
	e2e/scenarios/health-restart-rearm.star \
	e2e/scenarios/health-unhealthy.star

# Scenario subset for the bare (daemonless OCI-runtime) target — now the full
# backend-agnostic set (identical to SCENARIOS_CONTAINERD): single-container
# deploy + lifecycle + restart monitor (cornus's in-process supervisor) + CNI
# networking / published ports / hosts-file DNS / server-hosted DNS / volumes +
# exec (TTY and non-TTY) / port-forward. Keep in sync with BARE_SCENARIOS in
# e2e/container/entrypoint.sh (enforced by TestScenarioSubsetsInSync).
SCENARIOS_BARE := \
	e2e/scenarios/mcp-stdio-protocol.star \
	e2e/scenarios/mcp-stdio-tools.star \
	e2e/scenarios/deploy.star \
	e2e/scenarios/deploy-stats.star \
	e2e/scenarios/lifecycle.star \
	e2e/scenarios/lifecycle-restart.star \
	e2e/scenarios/deploy-server-restart.star \
	e2e/scenarios/deploy-reboot-survival.star \
	e2e/scenarios/deploy-portforward.star \
	e2e/scenarios/deploy-egress-bare.star \
	e2e/scenarios/deploy-mounts-sidecar-bare.star \
	e2e/scenarios/compose.star \
	e2e/scenarios/compose-logs.star \
	e2e/scenarios/compose-down-volumes.star \
	e2e/scenarios/exec.star \
	e2e/scenarios/compose-exec.star \
	e2e/scenarios/credentials-env-host.star \
	e2e/scenarios/credentials-endpoint-host.star \
	e2e/scenarios/compose-dependson.star \
	e2e/scenarios/health-restart-rearm.star \
	e2e/scenarios/health-unhealthy.star

# Scenario subset for the incus target: single-container deploy + published
# ports, the Stop/Start/Restart lifecycle, stats, exec (TTY + non-TTY),
# port-forward, and compose deploy/exec — the data-plane paths implemented in
# Phase 2 (cp/logs/exec/forwardport). See .agents/docs/LTM/incus-backend.md.
# Deferred (need a live incusd or the caretaker companion): crash-restart
# supervision (lifecycle-restart), client-local mounts, egress. Keep in sync with
# INCUS_SCENARIOS in e2e/container/entrypoint.sh (enforced by
# TestScenarioSubsetsInSync).
SCENARIOS_INCUS := \
	e2e/scenarios/mcp-stdio-protocol.star \
	e2e/scenarios/mcp-stdio-tools.star \
	e2e/scenarios/deploy.star \
	e2e/scenarios/deploy-stats.star \
	e2e/scenarios/lifecycle.star \
	e2e/scenarios/deploy-server-restart.star \
	e2e/scenarios/exec.star \
	e2e/scenarios/deploy-portforward.star \
	e2e/scenarios/compose.star \
	e2e/scenarios/compose-exec.star \
	e2e/scenarios/server-in-container-incus.star \
	e2e/scenarios/credentials-env-host.star \
	e2e/scenarios/credentials-endpoint-host.star \
	e2e/scenarios/compose-dependson.star \
	e2e/scenarios/health-restart-rearm.star \
	e2e/scenarios/deploy-fsop-incus.star \
	e2e/scenarios/health-unhealthy.star

E2E_FLAGS := --cornus $(BIN)/cornus

# Default deploy target for the parameterized e2e-one / e2e-web targets; override
# on the command line (e.g. `make e2e-web TARGET=kube`).
TARGET ?= docker

.PHONY: build
build: web
	$(GO) build -o $(BIN)/cornus ./cmd/cornus
	$(GO) build -o $(BIN)/cornus-e2e ./cmd/cornus-e2e

# build-fast skips the frontend build; the binary embeds whatever pkg/webui/dist
# already holds (possibly only the "not built" notice).
.PHONY: build-fast
build-fast:
	$(GO) build -o $(BIN)/cornus ./cmd/cornus
	$(GO) build -o $(BIN)/cornus-e2e ./cmd/cornus-e2e

# web builds the SolidJS UI (web/) into pkg/webui/dist, the //go:embed root
# compiled into the cornus binary. Vite's emptyOutDir removes the committed
# .gitkeep, so restore it to keep the tree clean. Node is OPTIONAL: when npm is
# absent the build gracefully skips and the binary embeds whatever dist already
# holds (the "run make web" notice on a fresh tree) — so `make build` and the
# e2e suite run on node-less hosts. The Dockerfile's node stage guarantees a
# UI-embedded release image regardless.
.PHONY: web
web:
	@if command -v npm >/dev/null 2>&1; then \
		set -e; (cd web && npm ci && npm run build); touch pkg/webui/dist/.gitkeep; \
	else \
		echo "web: npm not found; skipping UI build (binary will not embed the web UI)"; \
	fi

.PHONY: test
test:
	$(GO) test ./...

# Regression tests for the Python tooling under .agents/skills/**/scripts/.
#
# These existed before this target did and NOTHING ran them — not `make test`,
# not CI, not the documented quality gate. A test nobody executes is worse than
# no test: it reads, in a review or an audit, as coverage that exists. The two
# doc-gate scripts and the translation tooling all gate real content checks
# (punctuation, anchors, duplicate targets, translation freshness), so a silent
# regression in one of them turns a green gate into an unchecked one.
#
# Discovery is per-directory because these are standalone scripts, not a package:
# each test file sys.path-inserts its own directory to import the script beside it,
# so a single rooted discovery would collide on same-named modules.
.PHONY: test-scripts
test-scripts:
	@set -e; \
	for dir in $$(find .agents/skills -name 'test_*.py' -exec dirname {} \; | sort -u); do \
		echo "==> $$dir"; \
		python3 -m unittest discover -s "$$dir" -p 'test_*.py'; \
	done

.PHONY: fmt
fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './.agents/*')

# Third-party attribution bundle: license texts (and sources for reciprocal
# licenses like MPL-2.0) of every module linked into the cornus binary, plus a
# CSV manifest. The Dockerfile runs the same commands at image build time so
# release images ship the bundle under /usr/share/doc/cornus/; this target is
# for local inspection or standalone-binary releases. Note: go-licenses must
# run under the same Go toolchain version the module resolves (go 1.26) — with
# an older local go, put the auto-downloaded go1.26 toolchain's bin dir first
# on PATH, or it aborts with "Package X does not have module info".
#
# LICENSE_TAGS must match what we actually SHIP (the Dockerfile's BUILD_TAGS and
# .github/scripts/build-release-binary.sh), because go-licenses reports what the
# given build configuration links. A shorter list silently omits real
# dependencies: without otelcol the whole collector tree goes missing, and
# without imbh/sable_extern_lib the store's bindings compile as stubs so neither
# imbh-go (Apache-2.0) nor sable (MIT) appears at all. imbh needs cgo and a
# libc-matched archive, hence CGO_ENABLED=1 and the imbhgo-fetch below.
#
# KNOWN GAP: go-licenses walks GO modules only, so the Rust crate tree statically
# linked inside libimbhgo.a is NOT represented here. Closing it needs an upstream
# notices manifest published alongside the archive; tracked in
# .agents/docs/TODO.md.
GO_LICENSES_VERSION ?= v1.6.0
LICENSE_TAGS ?= netgo,osusergo,otelcol,imbh,sable_extern_lib
.PHONY: third-party-licenses
third-party-licenses:
	rm -rf $(BIN)/third-party-licenses
	# go-proto ships no LICENSE file in its module zip (COPYING lives in the parent
	# repo, outside the submodule), so go-licenses can't classify it and aborts.
	# --ignore it here and re-inject its Apache-2.0 notice below; keep in sync with
	# the Dockerfile build stage.
	@LDFLAGS="$$(eval "$$($(GO) run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@$(IMBH_VERSION) -print-env 2>/dev/null)"; printf '%s' "$$CGO_LDFLAGS")"; \
	if [ -z "$$LDFLAGS" ]; then echo "third-party-licenses: could not resolve CGO_LDFLAGS (run 'make imbh-lib' to see why)" >&2; exit 1; fi; \
	set -x; \
	CGO_ENABLED=1 CGO_LDFLAGS="$$LDFLAGS" GOOS=linux GOFLAGS=-tags=$(LICENSE_TAGS) \
		$(GO) run github.com/google/go-licenses@$(GO_LICENSES_VERSION) \
		save ./cmd/cornus --save_path=$(BIN)/third-party-licenses \
		--ignore cornus \
		--ignore github.com/rootless-containers/proto/go-proto; \
	CGO_ENABLED=1 CGO_LDFLAGS="$$LDFLAGS" GOOS=linux GOFLAGS=-tags=$(LICENSE_TAGS) \
		$(GO) run github.com/google/go-licenses@$(GO_LICENSES_VERSION) \
		report ./cmd/cornus \
		--ignore cornus \
		--ignore github.com/rootless-containers/proto/go-proto \
		> $(BIN)/third-party-licenses/THIRD_PARTY_LICENSES.csv
	mkdir -p $(BIN)/third-party-licenses/github.com/rootless-containers/proto/go-proto
	{ printf 'rootlesscontainers-proto (github.com/rootless-containers/proto/go-proto)\nCopyright (C) 2018 Rootless Containers Authors\n\nLicensed under the Apache License, Version 2.0; the full license text follows.\n\n'; cat LICENSE; } > $(BIN)/third-party-licenses/github.com/rootless-containers/proto/go-proto/LICENSE
	printf 'github.com/rootless-containers/proto/go-proto,https://github.com/rootless-containers/proto/blob/f6ee952d53d9/COPYING,Apache-2.0\n' >> $(BIN)/third-party-licenses/THIRD_PARTY_LICENSES.csv

# Syntax-check every scenario without executing (no Docker/kind needed).
.PHONY: e2e-check
e2e-check: build
	$(BIN)/cornus-e2e --check $(SCENARIOS) $(EXTRA_CHECK_SCENARIOS) $(SCENARIOS_BENCH)

# Run all scenarios against the local Docker host.
.PHONY: e2e-docker
e2e-docker: build
	$(BIN)/cornus-e2e --target docker $(E2E_FLAGS) $(SCENARIOS)

# Run the backend-agnostic subset against the host's containerd daemon. Needs
# root (the containerd socket + CNI network setup) and the CNI reference
# plugins (bridge/portmap/host-local/loopback; CORNUS_CNI_BIN_DIR/CNI_PATH or
# /opt/cni/bin).
.PHONY: e2e-containerd
# Run the backend-agnostic subset against a podman API socket. Needs podman and
# CORNUS_PODMAN_SOCKET (or --podman-socket): the backend refuses to guess which
# daemon it drives, and neither does the harness.
.PHONY: e2e-podman
e2e-podman: build
	$(BIN)/cornus-e2e --target podman $(E2E_FLAGS) $(SCENARIOS_PODMAN)

# Run the rootless subset against a ROOTLESS podman API socket. Same flags as
# e2e-podman; what makes it the rootless leg is the socket you point it at (a
# service started by an unprivileged user), which is why the target still refuses
# to guess one.
.PHONY: e2e-podman-rootless
e2e-podman-rootless: build
	$(BIN)/cornus-e2e --target podman-rootless $(E2E_FLAGS) $(SCENARIOS_PODMAN_ROOTLESS)

e2e-containerd: build
	$(BIN)/cornus-e2e --target containerd $(E2E_FLAGS) $(SCENARIOS_CONTAINERD)

# Run the backend-agnostic subset against the daemonless bare (OCI-runtime)
# backend. Needs root (overlay snapshotter mount + netns + CNI) and an OCI
# runtime binary (runc by default; override with BARE_RUNTIME=crun) plus the CNI
# reference plugins (CORNUS_CNI_BIN_DIR/CNI_PATH or /opt/cni/bin) — the same
# networking the containerd backend uses.
.PHONY: e2e-bare
e2e-bare: build
	$(BIN)/cornus-e2e --target bare $(if $(BARE_RUNTIME),--bare-runtime $(BARE_RUNTIME),) $(E2E_FLAGS) $(SCENARIOS_BARE)

# Run the backend-agnostic subset against the host's Incus daemon (deploying
# cornus images as Incus OCI application containers). Needs Incus 6.3+ with
# skopeo and umoci on PATH (Incus flattens OCI images with them); the socket
# usually needs root or the incus group. Set CORNUS_INCUS_SOCKET/
# CORNUS_INCUS_PROJECT to override the daemon socket/project.
.PHONY: e2e-incus
e2e-incus: build
	$(BIN)/cornus-e2e --target incus $(E2E_FLAGS) $(SCENARIOS_INCUS)

# Run all scenarios against a kind-managed Kubernetes cluster (created/destroyed
# automatically; pass KEEP=1 to keep it). E2E_KNATIVE=1 installs Knative Serving
# into the cluster (via the shared e2e/container/install-knative.sh, needs
# network) so the deploy-knative scenario runs instead of self-skipping; override
# the release with KNATIVE_VERSION. Example:
#   make e2e-kube E2E_KNATIVE=1
# (E2E_KNATIVE is defined with the other E2E_* container knobs below.)
.PHONY: e2e-kube
e2e-kube: build
	E2E_KNATIVE=$(E2E_KNATIVE) E2E_INGRESS_NGINX=$(E2E_INGRESS_NGINX) E2E_METRICS_SERVER=$(E2E_METRICS_SERVER) $(BIN)/cornus-e2e --target kube $(if $(KEEP),--keep,) $(E2E_FLAGS) $(SCENARIOS)

# Multi-replica hub validation is no longer a pair of shell scripts and no longer
# has its own targets: it is `hub-multireplica-redis.star` (docker target, real
# Redis), `hub-multireplica-kube.star` (kube target, the API server as the
# store), and `hub-multireplica-credential.star` (kube target, cross-replica
# CREDENTIAL forwarding — kube-only because the kubernetes backend is the only one
# that realizes client-sourced credentials), all in SCENARIOS. They run with every
# other scenario on their target —
# `make e2e-docker` / `make e2e-kube`, or the containerized runner — and are
# parse- and resolve-checked by `make e2e-check`, which the scripts never were.
# The `serve(name=, addr=)` replica handle is what made them expressible; see
# .agents/docs/TESTING.md.

# Registry over gs:// / azblob:// object storage (cloud emulators or real
# accounts). The cloud drivers only exist behind the `cloudblob` build tag
# (pkg/storage/drivers_cloud.go), so this builds a SEPARATE tagged cornus
# ($(BIN)/cornus-cloudblob — the default $(BIN)/cornus stays lean) and runs just
# the two cloud scenarios on the local target. The scenarios self-skip unless
# CORNUS_TEST_GCS / CORNUS_TEST_AZBLOB (plus each SDK's emulator env, e.g.
# STORAGE_EMULATOR_HOST / AZURE_STORAGE_*) are set; see .agents/docs/TESTING.md
# "Cloud-storage backends (emulator runs)" for working fake-gcs-server/Azurite
# commands.
.PHONY: e2e-cloudblob
e2e-cloudblob:
	$(GO) build -tags cloudblob -o $(BIN)/cornus-cloudblob ./cmd/cornus
	$(GO) build -o $(BIN)/cornus-e2e ./cmd/cornus-e2e
	$(BIN)/cornus-e2e --target local --cornus $(BIN)/cornus-cloudblob \
		e2e/scenarios/registry-gcs.star e2e/scenarios/registry-azblob.star

# Client-sourced AWS credentials minted by the `aws-sts` backend, which only
# exists behind the `credaws` build tag (pkg/credential/awssts). Like
# e2e-cloudblob this builds a SEPARATE tagged CLIENT/server binary
# ($(BIN)/cornus-credaws — the default $(BIN)/cornus stays lean); the caretaker
# SIDECAR image (cornus:e2e) needs no tag since it only DELIVERS the credential,
# not mints it. The scenario is kube-only (sidecar delivery) and self-skips unless
# CORNUS_TEST_STS_ENDPOINT names a live STS server (start winterbaume first, same
# as the awssts unit test and registry-s3). Requires a reachable kube target.
.PHONY: e2e-credaws
e2e-credaws:
	$(GO) build -tags credaws -o $(BIN)/cornus-credaws ./cmd/cornus
	$(GO) build -o $(BIN)/cornus-e2e ./cmd/cornus-e2e
	$(BIN)/cornus-e2e --target kube --cornus $(BIN)/cornus-credaws \
		e2e/scenarios/credentials-sts.star

# Embedded OpenTelemetry Collector (the caretaker's workload-telemetry agent) only
# exists behind the `otelcol` build tag (pkg/otelcollector), which pulls in the
# collector-core dependency tree. The default `go build ./...` / `go test ./...`
# gate stays lean with a stub; this target compiles and tests the real collector
# path, so CI exercises it. The release image (Dockerfile BUILD_TAGS) ships with
# the tag so the caretaker sidecar carries the Collector.
.PHONY: test-otel
test-otel:
	$(GO) build -tags otelcol ./...
	$(GO) test -tags otelcol ./pkg/otelcollector/ ./pkg/caretaker/ ./pkg/deploy/... ./pkg/api/ ./pkg/compose/

# --- built-in observability store (imbh) ------------------------------------
# The store (pkg/obsstore) is IMBH, a Rust observability database reached over
# cgo, so unlike every other optional feature it cannot be part of the pure-Go
# CGO_ENABLED=0 build at all. It sits behind the `imbh` tag with a stub, exactly
# as the embedded Collector sits behind `otelcol`.
#
# Two things differ from a normal tagged build and both are load-bearing:
#   * `sable_extern_lib` must accompany `imbh` — it tells the Rust/Go runtime
#     fusion layer to link the one combined archive rather than its own.
#   * CGO_LDFLAGS must point at that archive. `make imbh-lib` fetches the
#     prebuilt one for this platform and prints the flags; IMBH_LDFLAGS below
#     resolves them automatically so the test target is a single command.
IMBH_VERSION ?= v0.3.0
IMBH_TAGS := imbh sable_extern_lib

# Download (and checksum-verify) the prebuilt static archive into the user cache.
# Needs no Rust toolchain. Re-running is cheap: the tool is a no-op once cached.
.PHONY: imbh-lib
imbh-lib:
	$(GO) run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@$(IMBH_VERSION) -print-env

# Build + test the real store path so it does not rot. -race is not optional
# here: it is the only thing that catches a mishandled zero-copy Arrow buffer
# crossing the FFI boundary, which is the one class of bug this binding can
# introduce that ordinary tests pass straight through.
.PHONY: test-imbh
test-imbh:
	@LDFLAGS="$$(eval "$$($(GO) run github.com/moriyoshi/imbh-go/cmd/imbhgo-fetch@$(IMBH_VERSION) -print-env 2>/dev/null)"; printf '%s' "$$CGO_LDFLAGS")"; \
	if [ -z "$$LDFLAGS" ]; then echo "test-imbh: could not resolve CGO_LDFLAGS (run 'make imbh-lib' to see why)" >&2; exit 1; fi; \
	echo "test-imbh: CGO_LDFLAGS=$$LDFLAGS"; \
	CGO_ENABLED=1 CGO_LDFLAGS="$$LDFLAGS" $(GO) build -tags "$(IMBH_TAGS)" ./... && \
	CGO_ENABLED=1 CGO_LDFLAGS="$$LDFLAGS" $(GO) test -tags "$(IMBH_TAGS)" -race \
		./pkg/obsstore/ ./pkg/server/ ./pkg/deploy/... ./pkg/api/ ./cmd/cornus/...

# Run a single scenario, e.g. `make e2e-one TARGET=kube SCENARIO=e2e/scenarios/deploy.star`.
.PHONY: e2e-one
e2e-one: build
	$(BIN)/cornus-e2e --target $(TARGET) $(E2E_FLAGS) $(SCENARIO)

# Opt-in web-UI E2E: `cornus web` against a real deployed compose project + the
# detached-frontend proxy. Defaults to the docker target; override with TARGET=.
.PHONY: e2e-web
e2e-web: build
	$(BIN)/cornus-e2e --target $(TARGET) $(E2E_FLAGS) $(SCENARIOS_WEB)

# Opt-in web file-explorer E2E: the /.cornus/web/fs* surface against a live
# workload with a client-local bind mount — the only place the explorer's view and
# the container's view of the same bytes are checked against each other. Defaults
# to the docker target; override with TARGET=.
.PHONY: e2e-web-fs
e2e-web-fs: build
	$(BIN)/cornus-e2e --target $(TARGET) $(E2E_FLAGS) $(SCENARIOS_WEB_FS)

# KEEP=1 preserves an existing kind cluster: the kube target REUSES a cluster it
# finds but deletes it on teardown regardless of whether it created it, so running
# this against a cluster somebody else set up destroys it without KEEP.
.PHONY: e2e-web-terminal
e2e-web-terminal: build
	$(BIN)/cornus-e2e --target $(TARGET) $(if $(KEEP),--keep,) $(E2E_FLAGS) $(SCENARIOS_WEB_TERMINAL)

.PHONY: e2e-web-agent
e2e-web-agent: build
	$(BIN)/cornus-e2e --target $(TARGET) $(if $(KEEP),--keep,) $(E2E_FLAGS) $(SCENARIOS_WEB_AGENT)

# Opt-in BENCHMARK suite: times real work and reports throughput/latency. Sets
# CORNUS_E2E_BENCH=1 so the benchmark scenarios run (they self-skip otherwise).
# Defaults to the docker target; override with TARGET=. Set BENCH_JSON=<path> to
# also collect the numbers as JSONL.
.PHONY: e2e-bench
e2e-bench: build
	CORNUS_E2E_BENCH=1 $(if $(BENCH_JSON),CORNUS_E2E_BENCH_JSON=$(BENCH_JSON),) \
		$(BIN)/cornus-e2e --target $(TARGET) $(E2E_FLAGS) $(SCENARIOS_BENCH)

# --- containerized runner ---------------------------------------------------
# Build the all-in-one E2E runner image (Docker-in-Docker + kind/kubectl + the
# build engine + binaries + scenarios). Context is the repo root.
#
# E2E_BUILD_TAGS overrides the cornus build tags baked into the runner (and the
# cornus:e2e sidecar image it derives, which copies the built binary). The
# otel-collector.star scenario needs the embedded collector in the sidecar, so run it
# with the `otelcol` tag plus CORNUS_TEST_OTEL on e2e-container (see below).
IMAGE ?= cornus-e2e:latest
E2E_BUILD_TAGS ?= netgo osusergo
.PHONY: e2e-image
e2e-image:
	docker build -f e2e/container/Dockerfile --build-arg BUILD_TAGS="$(E2E_BUILD_TAGS)" -t $(IMAGE) .

# Run the full E2E suite inside the all-in-one image (must be privileged). Select
# targets with E2E_TARGETS (default "docker"; add "kube" for the kind path and/or
# "containerd" for an in-container standalone containerd):
#   make e2e-container E2E_TARGETS="docker kube containerd"
# E2E_MULTUS=1 installs the staged Multus DaemonSet into the kind cluster (kube
# target) so the deploy-multus scenario runs; E2E_MULTUS_IPVLAN=1 /
# E2E_MULTUS_MACVLAN=1 additionally un-skip the ipvlan/macvlan variants
# (environment-sensitive in dind; parent NIC override via E2E_MULTUS_PARENT,
# default eth0), and E2E_MULTUS_DETACHED=1 un-skips the detached-primary
# (default-network) scenario:
#   make e2e-container E2E_TARGETS=kube E2E_MULTUS=1 E2E_MULTUS_IPVLAN=1 \
#     E2E_MULTUS_MACVLAN=1 E2E_MULTUS_DETACHED=1
E2E_TARGETS ?= docker
E2E_MULTUS ?= 0
E2E_MULTUS_IPVLAN ?= 0
E2E_MULTUS_MACVLAN ?= 0
E2E_MULTUS_DETACHED ?= 0
E2E_MULTUS_PARENT ?= eth0
# E2E_KNATIVE=1 installs Knative Serving + the Kourier networking layer into the
# kind cluster (kube target) so the deploy-knative scenario runs; otherwise it
# self-skips. The release manifests are VENDORED under e2e/container/knative/ and
# pinned to the version recorded in e2e/container/install-knative.sh, so the
# install needs no download (the cluster still pulls the digest-pinned images).
# Example:
#   make e2e-container E2E_TARGETS=kube E2E_KNATIVE=1 \
#     E2E_SCENARIOS=e2e/scenarios/deploy-knative.star
E2E_KNATIVE ?= 0
# E2E_INGRESS_NGINX=1 installs a real ingress controller into the kind cluster so
# the ingress scenarios exercise cornus dialling an actual controller instead of
# the server-side mux fallback. Example:
#   make e2e-container E2E_TARGETS=kube E2E_INGRESS_NGINX=1
E2E_INGRESS_NGINX ?= 0
# E2E_METRICS_SERVER=1 installs metrics-server into the kind cluster (kube
# target). It is what gives the kubernetes deploy backend a metric source at all:
# SampleMetrics reads the metrics.k8s.io aggregated API, which does not exist
# without it, so observability-metrics.star's kube leg keeps only its
# backend-independent assertions when it is absent — and HARD-FAILS when it is
# present but the readings never arrive. The scenario ALSO needs the built-in
# observability store, which is a cgo build (`imbh`), so pair the two:
#   make e2e-container E2E_TARGETS=kube E2E_METRICS_SERVER=1 \
#     E2E_BUILD_TAGS="netgo osusergo imbh sable_extern_lib" \
#     E2E_SCENARIOS=e2e/scenarios/observability-metrics.star
E2E_METRICS_SERVER ?= 0
# CORNUS_TEST_OTEL=1 opts the otel-collector scenario in; it also needs the runner
# built with the collector, so pair it with E2E_BUILD_TAGS on e2e-image, e.g.:
#   make e2e-container E2E_TARGETS=kube E2E_BUILD_TAGS="netgo osusergo otelcol" \
#     CORNUS_TEST_OTEL=1 E2E_SCENARIOS=e2e/scenarios/otel-collector.star
# Unset (the default), the scenario self-skips, so it is harmless in the full suite.
CORNUS_TEST_OTEL ?=
# E2E_STRICT=1 turns every "this target cannot run here" SELF-SKIP inside the
# entrypoint into a hard failure (the incus OCI-version gate, the bare companion
# agent image). This is what the dedicated bare/incus CI legs set, so it is also
# how you reproduce those legs locally:
#   make e2e-container E2E_TARGETS=incus E2E_STRICT=1
# E2E_PREFLIGHT_ONLY=1 does the per-target setup (starts/initializes the daemon)
# and then runs only the harness's capability probe, exiting non-zero if the
# daemon/runtime this target needs is missing — the cheap "can this host run the
# target at all?" gate the CI legs run before the suite:
#   make e2e-container E2E_TARGETS=incus E2E_STRICT=1 E2E_PREFLIGHT_ONLY=1
E2E_STRICT ?= 0
E2E_PREFLIGHT_ONLY ?= 0
.PHONY: e2e-container
e2e-container: e2e-image
	docker run --rm --privileged \
		-e E2E_TARGETS="$(E2E_TARGETS)" \
		-e E2E_STRICT="$(E2E_STRICT)" \
		-e E2E_PREFLIGHT_ONLY="$(E2E_PREFLIGHT_ONLY)" \
		-e E2E_MULTUS="$(E2E_MULTUS)" \
		-e E2E_MULTUS_IPVLAN="$(E2E_MULTUS_IPVLAN)" \
		-e E2E_MULTUS_MACVLAN="$(E2E_MULTUS_MACVLAN)" \
		-e E2E_MULTUS_DETACHED="$(E2E_MULTUS_DETACHED)" \
		-e E2E_MULTUS_PARENT="$(E2E_MULTUS_PARENT)" \
		-e E2E_KNATIVE="$(E2E_KNATIVE)" \
		-e E2E_INGRESS_NGINX="$(E2E_INGRESS_NGINX)" \
		-e E2E_METRICS_SERVER="$(E2E_METRICS_SERVER)" \
		-e CORNUS_TEST_OTEL="$(CORNUS_TEST_OTEL)" \
		-e E2E_SCENARIOS="$(E2E_SCENARIOS)" \
		-e CORNUS_E2E_BENCH="$(CORNUS_E2E_BENCH)" \
		-e CORNUS_E2E_BENCH_JSON="$(CORNUS_E2E_BENCH_JSON)" \
		$(IMAGE)

# Run the daemonless bare (OCI-runtime) target inside the existing all-in-one E2E
# image on any Docker host — no dockerd/kind/containerd needed, just the staged
# runc + CNI plugins the image already ships. A thin wrapper over e2e-container
# with E2E_TARGETS=bare; override the scenarios with E2E_SCENARIOS=... .
.PHONY: e2e-bare-container
e2e-bare-container: e2e-image
	docker run --rm --privileged \
		-e E2E_TARGETS="bare" \
		-e E2E_STRICT="$(E2E_STRICT)" \
		-e E2E_PREFLIGHT_ONLY="$(E2E_PREFLIGHT_ONLY)" \
		-e E2E_SCENARIOS="$(E2E_SCENARIOS)" \
		$(IMAGE)

# The incus counterpart of e2e-bare-container: runs the incus target inside the
# all-in-one image against the incusd the image itself ships (started and
# initialized by entrypoint.sh's start_incus) — no host Incus needed. This is
# what the `incus` CI leg runs; add E2E_STRICT=1 to match it exactly (a daemon
# too old for OCI then fails instead of self-skipping).
.PHONY: e2e-incus-container
# Run the podman leg inside the runner image, so no podman is needed on the host.
.PHONY: e2e-podman-container
e2e-podman-container: e2e-image
	docker run --rm --privileged \
		-e E2E_TARGETS="podman" \
		-e E2E_STRICT="$(E2E_STRICT)" \
		-e E2E_PREFLIGHT_ONLY="$(E2E_PREFLIGHT_ONLY)" \
		-e E2E_SCENARIOS="$(E2E_SCENARIOS)" \
		$(IMAGE)

# Run the ROOTLESS podman leg inside the runner image. The image carries an
# unprivileged user with subuid/subgid ranges and the entrypoint starts the API
# service as that user, so this needs nothing rootless on the host.
.PHONY: e2e-podman-rootless-container
e2e-podman-rootless-container: e2e-image
	docker run --rm --privileged \
		-e E2E_TARGETS="podman-rootless" \
		-e E2E_STRICT="$(E2E_STRICT)" \
		-e E2E_PREFLIGHT_ONLY="$(E2E_PREFLIGHT_ONLY)" \
		-e E2E_SCENARIOS="$(E2E_SCENARIOS)" \
		$(IMAGE)

e2e-incus-container: e2e-image
	docker run --rm --privileged \
		-e E2E_TARGETS="incus" \
		-e E2E_STRICT="$(E2E_STRICT)" \
		-e E2E_PREFLIGHT_ONLY="$(E2E_PREFLIGHT_ONLY)" \
		-e E2E_SCENARIOS="$(E2E_SCENARIOS)" \
		$(IMAGE)

.PHONY: clean
clean:
	rm -rf $(BIN)
