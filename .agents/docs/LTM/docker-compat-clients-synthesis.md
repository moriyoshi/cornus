# Docker-Compatible Client Surfaces Synthesis (compose CLI, dockerd proxy, devcontainers)

## Summary

Cornus exposes three Docker-compatible client surfaces that all funnel into the same server API
and share one translation pipeline: `cornus compose` (a docker-compose-workalike CLI),
`cornus daemon docker` (a local Docker Engine REST proxy that lets stock `docker`/`docker compose`
drive Cornus), and native devcontainer.json support (a loader that produces the same compose
project model). Merged because they share `pkg/client`, `pkg/compose`, the deploy-attach mount
path, the build-wire fields, and a common set of Docker-parity semantics (naming, labels,
lifecycle, exec).

## Included Documents

| Document | Focus |
|----------|-------|
| [compose-cli.md](./compose-cli.md) | Compose parsing/interpolation, dependency-ordered deploy, `up -d` supervisor, named volumes |
| [dockerd-proxy.md](./dockerd-proxy.md) | Docker Engine API proxy: run/compose/logs/stats/cp/exec/attach, hijack tunnel |
| [dev-containers.md](./dev-containers.md) | devcontainer.json loader, lifecycle hooks via server-side exec, JSONC parsing |

Note on paths: these docs predate the `internal/` -> `pkg/` restructure; read `internal/X` as
`pkg/X` (`pkg/client`, `pkg/compose`, `pkg/dockerproxy`, `pkg/devcontainer`).

## Stable Knowledge

### Shared pipeline

- `pkg/compose` is the common model: the devcontainer loader (`pkg/devcontainer`) produces the
  SAME `*compose.Project` the Compose path drives (devcontainer-specific state rides a `Result`
  side channel), so every command works unchanged. `pkg/client` is the one HTTP client
  (Deploy/List/Status/Delete/Action/Build/DeployAttach/ExecResize).
- Build threading is identical for all surfaces: multi-stage target rides
  `client.BuildRequest.Target` -> `buildwire.BuildSpec.TargetStage` -> `SolveInput.TargetStage`
  -> `FrontendAttrs["target"]`; each `cache_from` ref folds into a `type=registry` cache import
  client-side (no server mapping). `SolveInput` already had `Target` for the image ref — the
  stage field is deliberately `TargetStage`.
- Bind mounts: relative sources are absolutized client-side (`ResolveMounts(baseDir)`); services
  with client-local mounts deploy via `client.DeployAttach` (9P streaming), never stateless
  `Deploy`. On k8s the server realizes them as sidecar mounts, never hostPath.
- Named volumes: `api.VolumeSpec.Name` empty == anonymous (ephemeral, owner-reaped); non-empty ==
  shared and persistent across deployments. Name resolution mirrors compose: explicit `name:` >
  `external: true` literal > `<project>_<vol>`. The dockerd proxy classifies bare-name `-v`
  sources as named volumes via `isHostPath` (a non-path source like `cache:/data` is a volume,
  not a host bind).

### cornus compose specifics

- Standalone ~7.4M static binary (no BuildKit/gocloud). Deployment names are
  `<project>-<service>`; deploys run in `depends_on` topological order (cycle detection).
- Full `${VAR}` interpolation (all forms incl. nested `${A:-${B}}`) over process env overlaid on
  a sibling `.env`; `env_file` merged with inline `environment:` precedence.
- `up -d` self-daemonizes into ONE per-project supervisor with a unix control socket at
  `$XDG_RUNTIME_DIR/cornus-compose/<project>.sock` (JSON up/down/ping protocol); repeated
  `up -d` reuses it. A per-invocation helper design orphans prior helpers — don't regress this.
  The supervisor also holds port-forward listeners for port-publishing services
  (`daemonService.ForwardPorts`/`ForwardOnly`; `daemonResponse.Forwards` reports bound
  addresses — engine semantics in [port-forwarding.md](./port-forwarding.md)).
- `needsBackgroundAgent(specs, mode)` centralizes detached client-resource lifetime. It returns true
  for client-local mounts, `Egress.NeedsRelay()` proxy/transparent policies, SOCKS5 conduit use, or
  published-port forwarding. Relay egress is session state just like a mount: omitting it sends a
  detached service through a temporary foreground session whose teardown removes the workload.
- Re-`up` fingerprints each service (sha256 of the canonical resolved `daemonService` JSON,
  forward shape included): unchanged services are kept (`up-to-date`), changed ones go through
  the normal teardown path (`recreated: configuration changed`). Daemon replies are stamped
  `Protocol: 2`; against an older daemon the CLI warns and keeps it running — killing it would
  drop held mounts.
- Foreground `up` BLOCKS whenever any selected service has client-local mounts OR publishes
  ports (a client-served mount/forward cannot outlive the caller), releasing on Ctrl-C; scripts
  must pass `-d` or `--no-forward-ports`. `stop`/`start`/`restart` fail loud for services whose
  mounts a live supervisor holds.
- `cornus daemon docker` and `cornus daemon mounts` run in the FOREGROUND by default;
  `-d`/`--daemon` re-execs a detached setsid child via `cmd/cornus/internal/daemonize`
  (`Spawn(args, logPath)` + `SelfArgs()`, which strips only the exact `-d`/`--daemon` token from
  the RAW argv so all other flags — including root-level globals — survive; a combined short
  group like `-dp proj` is not rewritten). `daemon mounts -d` writes `daemonState` so
  `compose up -d`/`down` discovers a hand-daemonized supervisor; `daemon docker -d` prints the
  pid + `export DOCKER_HOST=...`.
- Build progress uses BuildKit `progressui.AutoMode` — degrades to append-only text on non-TTY,
  so E2E substring assertions survive.

### cornus daemon docker specifics

- Persistent daemon by necessity: docker's create/start split maps onto Cornus's atomic Apply by
  buffering at create and opening a long-lived deploy-attach at start, held for the container
  lifetime. One Docker container == one single-replica Cornus deployment
  (`deploymentName` sanitization maps compose's `<project>-<service>-1` verbatim).
- Wire structs are hand-rolled (no moby types); `deployAttacher` seam makes handlers
  unit-testable with fakes. `/build` is intentionally unimplemented (would relink BuildKit).
- Compose-compat requirements found only live: synthetic 200 image inspect (compose treats 404 as
  fatal), always-populated `NetworkSettings`/`HostConfig`, fake networks/volumes endpoints,
  create-time label echo (`com.docker.compose.*` filters and the `config-hash`
  recreate key round-trip verbatim). Compose v5 scale reconverge (`up --scale web=N` then a
  smaller N) additionally needs `NetworkSettings.Networks` on container-LIST entries
  (`containerSummary`) — v5 nil-derefs without it; validated live (docker 29.2.1 / compose
  v5.0.2, `dockerd.star` scale sections).
- Foreground `docker run` protocol (attach -> wait -> start) is fully implemented: attach PARKS
  on the record's started-channel and opens the backend tunnel only once the session is live
  (never attach a not-yet-started deployment); `wait?condition=next-exit` flushes the `200` +
  Content-Type header immediately and sends the JSON body at exit — dockerd's actual protocol
  (holding the header deadlocks `docker run`, and replying `{"StatusCode":0}` before a session
  exists breaks it); `/events` is a real eventHub publishing start/die/stop/destroy in BOTH the
  legacy (`status`/`id`/`from`) and modern (`Type`/`Action`/`Actor`) forms with filter parsing.
- That trio is exactly what the official `@devcontainers/cli` needs: it blocks on a filtered
  `docker events` stream, runs a FOREGROUND keepalive `docker run`, and treats that process
  exiting — even with status 0 — as fatal. Three general proxy fixes complete devcontainer
  compat: create-time `Entrypoint` threading (`DeploySpec.Entrypoint`; dockerhost create slot,
  kubernetes `Command` with `spec.Command` demoted to `Args`), the REAL image config on
  `GET /images/{name}/json` via go-containerregistry (per-ref success cache, `name.Insecure`
  retry, synthetic empty-config fallback preserved for offline compose), and modern MAP-form
  label-filter encoding in `parseLabelFilters` (docker CLIs at API >= 1.22 send
  `{"label":{"k=v":true}}`; a list-only decoder silently matches everything).
- Published ports auto-forward on the client: `Proxy.start` publishes each container's
  PortBindings through `pkg/portfwd` after `waitReady`; the group lives in
  `containerRecord.fwd` and is closed exactly-once inside `setExited`'s current-session guard,
  so stop/rm/wait release it and `docker start` re-publishes — `docker run -p 8080:80` behaves
  like local Docker. `WithoutPortForwards()` opts out. See
  [port-forwarding.md](./port-forwarding.md).
- exec/attach ride a hijack tunnel (proxy `http.Hijacker` <-> WS <-> hand-rolled Docker-socket
  hijack); the bidirectional bridge is OUTPUT-AUTHORITATIVE: wait on the Docker->client copy,
  half-close (`CloseWrite`) on stdin EOF — closing on either EOF loses non-interactive exec
  output. Terminal resize is an out-of-band REST call
  (`POST /.cornus/v1/deploy/exec/{id}/resize`), not in-stream. Kubernetes exec/attach are real
  (client-go remotecommand); stats/cp are documented not-supported stubs.
- Graceful `down` waits (bounded) for the server's terminal Done — `deploywire.Serve` uses an
  explicit reader-goroutine + `select` rather than relying on conn ctx behavior.

### devcontainer specifics

- Both flavors: single-container (`image`/`build`) and compose-based
  (`dockerComposeFile`+`service`+`runServices` with overlay + filter). Auto-detected when no
  compose file is present; a Compose file wins in a mixed repo.
- `initializeCommand` runs on the HOST; `onCreate`/`updateContent`/`postCreate`/`postStart`/
  `postAttach` run via server-side exec (independent of who holds the 9P mount session, so hooks
  work in foreground, mounted, and detached paths). `postStart`/`postAttach` re-run on
  `start`/`restart`; once-per-create hooks do not.
- JSONC stripping is length- and newline-preserving (removed bytes overwritten with spaces) so
  `json.SyntaxError.Offset` maps 1:1 for line/column error reporting. Unsupported fields are
  collected and warned once, never silently dropped.

## Operational Guidance

- Adding a service-level compose field: extend `pkg/compose` (custom UnmarshalJSON over
  `sigs.k8s.io/yaml` for polymorphic forms), translate in `translateService`, and warn (deduped
  `slog.Warn`) rather than hard-error on unknown fields.
- Adding a docker CLI vertical to the proxy: follow the one pattern — Backend method ->
  dockerhost pass-through -> REST route -> `Client` method -> proxy handler case; then validate
  with the REAL docker CLI (`e2e/scenarios/dockerd.star`) — unit tests replay only the calls one
  imagines, and all three live compose bugs proved it.
- The compose CLI plugin ignores `docker -H`; point it at the proxy via `DOCKER_HOST`, isolated
  to the compose invocation (a shared env would repoint Cornus's own dockerhost backend into
  recursion).

## Files

- `cmd/cornus/internal/composecli/` — CLI, `daemon.go`/`supervisor.go` (up -d supervisor,
  fingerprinting), lifecycle hook execution (`execRunner` seam)
- `cmd/cornus/internal/daemonize/` — `Spawn` (setsid re-exec), `SelfArgs` argv filter
- `cmd/cornus/daemon.go`, `pkg/dockerproxy/` — proxy daemon, `translate.go`, `state.go`,
  `images.go` (real image config, `imageCfgs` cache), `containers.go` (`parseLabelFilters`),
  `{attach,proxy,state}.go` (`startedC` parking, eventHub, `containerRecord.fwd`)
- `pkg/devcontainer/` — loader, JSONC parser, `buildFromSpec`
- `pkg/compose/` — parsing, `interpolate.go`, `ResolveMounts`, `applyProxyPolicy`, `BuildPlan`
- `pkg/client/` — server HTTP client, build/cache threading, `DeployAttach`
- `pkg/deploywire/` — graceful-down wait structure

## Tests

- `pkg/compose/compose_test.go` (translation, interpolation, env_file, volumes, build plan),
  `pkg/client/client_test.go`, `pkg/dockerproxy` unit suite (fake attacher, raw Docker JSON;
  `TestExecOutputSurvivesStdinEOF`, `TestComposeScaleAndRecreateDiff`, `TestEventsEmitsStart`,
  `TestWaitNextExit`, `TestAttachBeforeStart`, `TestContainerPortForward`, map-form label
  filters, `images_test.go` real-config round-trip), `pkg/devcontainer` JSONC + translation
  tests, `cmd/cornus/internal/composecli/lifecycle_test.go` + supervisor tests,
  `cmd/cornus/internal/daemonize` `TestStripDaemonFlag`.
- E2E: `compose.star`, `compose-build.star`, `compose-mounts.star`, `dockerd.star` (real docker
  CLI v29 + compose plugin, incl. `--scale` reconverge), `exec.star` (PTY harness via
  `exec_tty`), `devcontainer.star` (cornus's own translation), `devcontainer-vscode.star`
  (official `@devcontainers/cli` up/exec/rm against the proxy).

## Pitfalls

- Never close a bidirectional stdio bridge on stdin EOF (output-authoritative + CloseWrite).
- Compose dereferences `NetworkSettings.Networks` unconditionally and treats image-inspect 404 as
  fatal — the proxy must synthesize both (v5 also nil-derefs on container-LIST entries).
- Never hold the `wait?condition=next-exit` response header until exit, and never reply
  `{"StatusCode":0}` before a session exists — either deadlocks or breaks foreground
  `docker run`; the devcontainer CLI treats its keepalive run exiting (even 0) as fatal.
- When faking a wire API, test with the encodings real clients send TODAY: the label-filter
  map-form bug was invisible because unit tests hand-crafted the legacy list form.
- Foreground `compose up` blocks when a service publishes ports — E2E scenarios and scripts need
  `-d`/`--no-forward-ports` (`compose.star` uses `detach = True`).
- `daemonize.SelfArgs()` strips only exact `-d`/`--daemon` tokens (no combined-short rewrite);
  unix socket paths cap at ~108 bytes, and with `-d` a too-long `--socket` fails only in the
  child's log while the parent has already reported success.
- Relative bind sources reach Docker verbatim on the stateless path and are rejected —
  absolutize client-side.
- `stop` semantics differ by surface: the proxy keeps an `exited` record (record-level
  stop-and-keep); cornus compose fails loud when a supervisor holds the service's mounts.
- Each compose service is a separate Cornus deployment; inter-service networking is whatever the
  backend provides unless user networks are declared.
- `compose_up` on the kube E2E target does not `kind load` compose-built images — pre-build with
  `build()` under `<project>-<service>`.
- BuildKit progressui prints RUN vertex names even on cache hits — E2E markers must live in
  files, not RUN command strings.

## Recent Client Lifecycle Convergence

Foreground Compose now uses the client-agent reconcile engine, holds all selected services, follows container logs by default, and waits for teardown reconciliation before returning. Its Compose model support and CLI surface remain a curated deploy-to-server compatibility layer, not a local Docker-daemon clone.

Detached Compose uses the same lifecycle model for relay-backed egress. The background-agent
decision is a pure tested predicate covering mounts, relay egress, SOCKS5, and ports, avoiding
surface-specific lifetime drift.

The Docker proxy shares deploy-attach session plumbing instead of maintaining a parallel lifecycle. Its in-pod caretaker use is an opt-in loopback API endpoint with a separate client-scoped token; static `/_ping` and `/version` verify exposure but do not prove a server round trip.
