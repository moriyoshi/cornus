# cornus compose: Docker Compose-Compatible Client

## Summary

`cornus compose` (implemented in `cmd/cornus/internal/composecli`; a standalone `cornus-compose` binary until the 2026-07-05 single-binary consolidation) accepts the same commands as `docker compose` but redirects them to a running Cornus server over its `/.cornus/v1/*` endpoints — a client, not a local Docker driver (alias it as `docker-compose`). It parses Compose files via `internal/compose`, deploys in dependency order via `internal/client`, streams client-local bind mounts over deploy-attach 9P, and supervises detached mount sessions with one per-project daemon. Named volumes are realized as shared, persistent stores (Docker named-volume semantics) across all backends.

## Key Facts

- `cornus compose` shares `internal/client` and `internal/compose` with the main module; since the single-binary consolidation it ships inside `cornus` (the old standalone client built to ~7.4M).
- Commands: `up` (build if `build:` exists -> push as `<host>/<project>-<service>:latest` -> deploy, in dependency order), `down` (reverse), `ps`, `build`, `restart`/`stop`/`start`. Deployment names are project-qualified: `<project>-<service>`.
- Supported service fields: image, build (incl. `target`, string-or-list `cache_from`), command, environment, env_file, ports, volumes (bind + named), restart, depends_on, deploy.replicas, provider. Top-level `volumes:` is parsed; networks and `extends` are unhandled.
- Provider services (compose-spec `provider:`): a service delegates its lifecycle to an external plugin instead of an image/build. Parsed in `pkg/compose` (`Provider`/`ProviderOptions` in types.go, `ProviderPlan` on `ServicePlan`; `provider` in `supportedServiceFields`; `translateService` short-circuits and rejects provider+image/build/deploy). The runtime lives in `composecli/provider.go`: `resolveProviderBinary` finds `docker-<type>` or `<type>` on PATH; `providerRunner.run` invokes `<bin> compose --project-name=<p> up|down [--k=v...] <svc>` and `parseProviderStream` decodes the newline-delimited JSON stdout protocol (`info`/`debug`/`error`/`setenv`/`rawsetenv`). `setenv KEY=VALUE` is injected into dependents (those with `depends_on`) as `<SERVICE_UPPER>_KEY`; `rawsetenv` passes through unprefixed; the dependent's own env wins. Wired into `runForeground`/`upDetached` (run in the per-service errgroup, gated for dependents via a per-provider readiness channel checked in `waitForDependencies`; env folded in by `serviceSpec`->`injectProviderEnv`), `DownCmd.Run` (plugin `down`), `runAction`/`providerLifecycle` (`stop`->plugin `stop`, `start`->`up`, `restart`->stop+up), and `psRows` (`provider:<type>`). Reload re-provisions providers: the detached agent's `--watch` re-execs the CLI (so `upDetached` re-runs provider `up`), and foreground `reloadAndReconcile` calls `runProviderReload` before re-deploying dependents; both rely on providers being idempotent.
- `${VAR}` interpolation over process env overlaid on a sibling `.env` (process env wins); all Compose forms supported including `${VAR:-def}`, `${VAR:?err}`, `${VAR:+alt}`, `$$`, and nested `${A:-${B}}`.
- Bind mounts with relative sources are absolutized client-side by `compose.ServicePlan.ResolveMounts(baseDir)`; services with bind mounts deploy via `client.DeployAttach` (9P streaming), never stateless `client.Deploy`. Compose bind mounts must be `:ro` (DeployAttach rejects read-write).
- The kubernetes backend never creates a `hostPath` from a bind mount; stateless `Apply` rejects bind-mount specs outright, and client-local mounts are realized as privileged native-sidecar 9P mounts.
- `up -d` self-daemonizes into one per-project supervisor with a unix control socket at `$XDG_RUNTIME_DIR/cornus-compose/<project>.sock`; repeated `up -d` reuses the running supervisor, and re-`up` diffs each service by fingerprint (up-to-date vs recreate).
- `up -d` routes any service with a client-local mount, relay-backed egress, SOCKS5 conduit, or
  published-port forwarding to the persistent background agent. `needsBackgroundAgent` is the
  single lifecycle predicate for this decision.
- Foreground `compose up` (no `-d`) BLOCKS whenever any selected service publishes ports: it holds client-side port forwards (like mount services), so scripts must use `-d` or `--no-forward-ports`. Ctrl-C (a genuine foreground exit) now also DELETES the mount-free deployments it created, so terminating `up` stops everything it brought up, like `docker compose up` (`removeDeployments` in `commands.go`, tracked via the `mountFree` slice). Only the real foreground exit removes them: the early non-blocking returns (`up -d`, `--no-forward-ports` with no mounts) still leave them running for `down`.
- `cornus daemon docker` and `cornus daemon mounts` run in the FOREGROUND by default; `-d`/`--daemon` re-execs a detached (setsid) child via `cmd/cornus/internal/daemonize` and returns immediately.
- Named volumes: `api.VolumeSpec.Name` empty == anonymous (per-deployment, ephemeral, reaped with the deployment); non-empty == named (shared across deployments, survives `cornus delete` of any sharer). Compose name resolution: explicit `name:` wins, `external: true` keeps the literal name, else `<project>_<vol>`.
- Build progress uses BuildKit's `progressui.NewDisplay(w, progressui.AutoMode).UpdateFrom(ctx, ch)`; AutoMode degrades to plain append-only text on non-TTY writers, so E2E substring assertions on step stdout survive.
- `--scale N` and compose recreate diffing need no server logic: the compose plugin drives scaling client-side (N creates named `<project>-<svc>-1..N`), and the recreate decision is keyed on the `com.docker.compose.config-hash` label, which `internal/dockerproxy` round-trips verbatim through list and inspect.
- `cornus compose logs` (`logs.go`) mirrors `docker compose logs` (per-service filter, `--follow`, `--tail`/`-n`, `--timestamps`/`-t`, `--since`, `--no-log-prefix`); services stream concurrently with a shared-mutex line-buffering `prefixWriter`. `--follow` has NO `-f` short — the parent `compose` group owns `-f` for `--file` and kong rejects the duplicate.
- `cornus compose logs` prefers reading pod logs directly from the cluster with the developer's kubeconfig (`pkg/kubelogs`) and only falls back to the server proxy (`GET /.cornus/v1/deploy/{name}/logs`) as a last resort, since the server's ServiceAccount usually lacks RBAC to read workload pod logs. See [[kubernetes-backend]] and [[client-daemon-and-conduit]].
- `cornus compose up` reports cluster-side reconcile per container: it polls `Client.Status` after `Deploy` and prints one line per instance on each state change (`web  web-0: pending` -> `web  web-0: running`), because `POST /.cornus/v1/deploy` returns the instant objects are created (kube: before any pod is scheduled, so `ReadyReplicas == 0`).
- `cornus compose down` is synchronous by default (`--wait`/`--no-wait` negatable flag): it waits for workloads to fully terminate and reports per-instance teardown. A foreground `compose up` self-exits when every held deployment reports zero instances (`watchGone`), mirroring `docker compose up` exiting when its containers disappear. See [[port-forwarding]].
- `restart:` decodes YAML's bare `no` (which YAML 1.1 coerces to the boolean `false`) as the string `"no"` and validates every value against the compose-spec vocabulary (`no`/`always`/`unless-stopped`/`on-failure`/`on-failure:N`); the `on-failure:N` retry count is split out and plumbed to `spec.RestartMaxAttempts` rather than riding along inside `spec.Restart`. See "Compose `restart:` decoding and validation" below.
- `cornus compose exec` resolves a SERVICE to its first instance and drives it through the existing server exec endpoints (no new server API); it supports Docker-compatible `-e`/`-w`/`-u`/`-T`/`--privileged`/`--index`, but both `--index > 1` (no instance selector in the server exec API) and `-d`/`--detach` (no backend can safely return early from an exec) are rejected outright. `cmd/cornus/internal/execdrive` shares raw-mode/resize/exit-code plumbing between native `cornus exec` and `compose exec`.
- `cornus compose exec -e KEY=VALUE` against a Kubernetes target is visible via `ps`/`/proc/<pid>/cmdline` in the guest for the life of that process — a genuine `pods/exec` API limitation (no per-exec env parameter), not a cornus oversight; dockerhost and containerdhost are unaffected (native exec-create `Env` / OCI `Process.Env`). See the Pitfalls section.
- `Service.AgentForward bool` (compose key `x-cornus-agent-forward`) opts a service into `cornus exec --forward-agent`/SSH-agent forwarding; see [[remote-companion-and-agent-forwarding]] for the full feature.

## Details

### Compose parsing (`internal/compose`)

Parses a Compose file and translates services into `api.DeploySpec` + `BuildPlan`. Flexible YAML fields (command string-or-list, environment map-or-list, ports short/long, volumes, build string-or-object, depends_on list-or-map, env_file string/list/`{path, required}`) are handled via custom `UnmarshalJSON` over `sigs.k8s.io/yaml` (YAML -> JSON). Dependency order is a topological sort of `depends_on` with cycle detection.

`loadFile` decodes YAML to a generic `any`, interpolates string values (`interpolate.go`), re-encodes to JSON, then decodes into the typed `Project` — so the polymorphic field unmarshalers run unchanged. Interpolation matching is depth-aware so `${A:-${B}}` parses; required-but-unset (`:?` / `?`) returns an error from `Load`. `env_file` (`EnvFiles` type + `applyEnvFiles`) loads KEY=VALUE files (`#` comments, `export `, quoted values) relative to the compose file's dir and merges them into the service environment, with inline `environment:` taking precedence; optional missing files (`required: false`) are skipped.

`compose.BuildPlan{Target, CacheFrom}` is populated from `svc.Build.Target`/`.CacheFrom` (`cache_from` parses via `decodeStringOrList`).

### Client (`internal/client`)

HTTP client for the Cornus server: `Deploy`/`List`/`Status`/`Delete`/`Action`/`Build`. `Build` tars the context dir and streams it to `/.cornus/v1/build` (accepts `build-arg` query params). `client.BuildRequest{Target, CacheFrom}`: the target stage rides `buildwire.BuildSpec.TargetStage` (json `targetStage,omitempty`, mapped in `internal/server/build_attach.go` to `SolveInput.TargetStage` -> `FrontendAttrs["target"]`); each `cache_from` ref is folded into a `type=registry` cache import (`{ref: <ref>}`) at `internal/client.Build`, riding the existing `CacheImports` plumbing — no extra server mapping needed for cache.

### Deploy lifecycle

`deploy.Backend` has `Start`/`Stop`/`Restart` (dockerhost via the Docker Engine API; kubernetes was initially stubbed), exposed as `POST /.cornus/v1/deploy/{name}/{start|stop|restart}`.

### Bind mounts and the deploy-attach path

`Plan` keeps the raw (possibly relative) bind source as a pure translation; the CLI calls `ResolveMounts(baseDir)` in `runtime.load()` to absolutize against the project directory. Without this, a relative source like `./data` reached the Docker daemon verbatim and was rejected ("includes invalid characters for a local volume name ... use absolute path").

Scope: server-host bind paths only work when the server is local. Client-local mounts stream over 9P via `client.DeployAttach`; on k8s the server realizes them as a privileged native-sidecar mount (never hostPath), on docker as a host 9P mount. In the kubernetes backend, `deployment()` generates no `hostPath` volumes from `spec.Mounts`; stateless `Apply` rejects bind-mount specs with an error pointing at the deploy-attach path; `ApplyWithMounts` rejects any mount lacking a client-local 9P backing, and `deploymentWithMounts` builds the base with no mounts at all.

Because a client-served mount can't outlive its client, `up` (no `-d`) runs mount services in the foreground until Ctrl-C, tearing them down on exit; mount-free services deploy fire-and-forget but a foreground exit deletes the ones it created too (so terminating `up` stops everything it brought up).

### `up -d` per-project supervisor

`up -d` spawns a detached `setsid` helper (`spawnDaemon` in `cmd/cornus/internal/composecli/commands.go`, delegating to `daemonize.Spawn`; socket protocol in `daemon.go`, `signalAndWait` in `daemon_unix.go`/`daemon_other.go`) that runs as a per-project supervisor with a unix control socket (`supervisor.go`, the `cornus daemon mounts` subcommand, re-exec'd via `os.Executable()`):

- Holds one deploy-attach session per mount service; sessions keyed by service name, idempotent.
- Listens on `$XDG_RUNTIME_DIR/cornus-compose/<project>.sock`; protocol is JSON `up`/`down`/`ping` (`daemon.go`). State `{pid,project,host,log}` recorded under `$XDG_RUNTIME_DIR/cornus-compose/`.
- `up -d`: deploy mount-free services fire-and-forget, then — if a supervisor is reachable (ping) — send mount services over the socket; otherwise spawn the supervisor once. Second and later invocations reuse the existing process (the original one-helper-per-invocation design orphaned prior helpers and left duplicate sessions).
- Port-publishing services are handed to the same daemon: `daemonService` carries `ForwardPorts`/`ForwardOnly` (`ForwardOnly` = the CLI already deployed the service fire-and-forget and the daemon holds only the port-forward listeners), and `daemonResponse` carries `Forwards` so the CLI prints the daemon-side bound addresses. Supervisor `startService` branches on the shape; the idle-exit logic needed no change. See `port-forwarding.md` for the `pkg/portfwd` engine.
- `down`/`stop`: send `down` (names, or all) over the socket; the supervisor tears sessions down and exits when idle (replying before exit so the caller isn't cut off). SIGTERM fallback (bounded wait, SIGKILL) if the socket is unreachable; remaining resources are removed via the API.
- Starts are serialized (`startMu`) so concurrent `up -d` can't double-start a service.
- Deferred: `stop`/`start`/`restart` coordination with a running supervisor.

Relay-backed egress is session state just like a client-local mount. An earlier decision counted
mounts, SOCKS5, and ports but omitted `spec.Egress.NeedsRelay()`. A detached relay-only service then
entered `runForeground`, deployed on a held session, immediately returned because detach suppresses
the foreground hold, and ran teardown; the server consequently removed the workload. The pure
`needsBackgroundAgent(specs, mode)` helper now includes mounts and relay egress plus the conduit and
port rules, so `up -d` hands relay services to the agent that owns their deploy-attach session.

`TestNeedsBackgroundAgent` covers relay-only proxy/transparent modes, environment-only egress,
mounts, SOCKS5, and ports with each conduit mode. Both proxy and transparent egress scenarios pass
on a real kind cluster through the containerized E2E runner.

### Re-`up` service fingerprinting

The mounts daemon fingerprints each service as the sha256 of the canonical JSON of the resolved `daemonService` (including the forward shape). On re-`up`, unchanged services are kept (`up-to-date`); changed or fingerprint-less services are recreated via the normal teardown path (`recreated: configuration changed`). Daemon responses are stamped `Protocol: 2`; the CLI warns when an older daemon build cannot detect changes and keeps it running (killing it would drop held mounts).

### `cornus daemon *` -d/--daemon and the daemonize package

Both `daemon docker` and `daemon mounts` block in the foreground until SIGINT/SIGTERM by default; `-d`/`--daemon` daemonizes. Implementation in `cmd/cornus/internal/daemonize`:

- `Spawn(args, logPath)` (`spawn_unix.go`, with a `spawn_other.go` `!unix` stub) is the session-leader (setsid) re-exec, moved verbatim from composecli's former `spawnDetached`.
- `SelfArgs()` returns `os.Args[1:]` with `-d`/`--daemon`/`--daemon=...` stripped, so the detached child runs the same invocation in the foreground. Stripping the parsed flag from the RAW argv (rather than reconstructing args from struct fields) preserves all other flags — including globals like `--context`/`--config-file` on the root `CLI` struct — without enumerating them. Known accepted blind spot: a combined short group (e.g. `-dp proj`) is not rewritten; only the exact `-d` token is. A `CORNUS_DAEMONIZED=1` env-marker respawn was considered and rejected: the argv filter leaves no hidden state and `/proc/<pid>/cmdline` shows no misleading `-d`.
- `daemon docker -d` logs to `<socket minus .sock>.log` next to the socket and prints the pid + `export DOCKER_HOST=...`; it writes no state (nothing consumes it). `daemon mounts -d` logs to the compose stateDir AND writes `daemonState` (pid/socket/log), so a hand-daemonized mounts supervisor is discoverable by `compose up -d`/`down` exactly like one spawned by compose.
- `compose up -d`'s own `spawnDaemon` still spawns the foreground `daemon mounts` child directly via `daemonize.Spawn` (no double fork, pid known to the parent), and the E2E harness's foreground `daemon docker` child relies on the unchanged foreground default.

### Named volumes (shared + persistent)

`api.VolumeSpec` gained `Name`; each backend branches on `v.Name != ""`. New label `deploy.LabelVolume = "cornus.volume"`.

- compose: `Project.Volumes map[string]VolumeDef` parses top-level `volumes:` (merged across files); `translateService` emits `api.VolumeSpec{Name: volumeResourceName(project, source, defs)}`.
- dockerproxy (`internal/dockerproxy/translate.go`): `toDeploySpec` routes `HostConfig.Mounts` with `Type:"volume"` and bare-name `-v name:target` binds to named `VolumeSpec`s. Fixed a latent bug: the old `parseBind` accepted `cache:/data` as a host bind (Source "cache"); `parseNamedVolume` + `isHostPath` classify a non-path source as a named volume.
- kubernetes: a named volume backs one shared PVC named by `namedPVCName(logical)` (DNS-1123-sanitized + a sha256[:4] suffix so distinct logical names can't collide). The named PVC carries NO Deployment owner-ref (anonymous PVCs still do), so `cornus delete` — which relies on owner-ref GC cascade — leaves it intact. Labelled `cornus.managed=true` + `cornus.volume=<name>` (no app label). `claimName(spec,i)` picks shared vs per-deployment; the populate initContainer (seed-if-empty, idempotent) is reused unchanged.
- dockerhost: `toCreateBody` passes `v.Name` as `mountSpec.Source`, so Docker shares one persistent named volume (empty Source stays anonymous).

### Build progress output (`progressui`)

`internal/builder/solve_linux.go` replaced a hand-rolled `drainProgress` with BuildKit's `progressui.NewDisplay(w, progressui.AutoMode).UpdateFrom(ctx, ch)`. Verified against buildkit@v0.18.2: AutoMode draws the redrawing TTY view only when `console.ConsoleFromFile` succeeds; a captured pipe (ENOTTY) or non-`*os.File` writer degrades to plain append-only text (`textMux.printVtx` emits each RUN step's stdout verbatim as `#N T.SSS <line>`). A cache-HIT step produces no logs, preserving marker-absent assertions. `w==nil` -> `io.Discard` (channel still fully drained); `UpdateFrom` gets `context.Background()` so it returns only when BuildKit closes the channel — the drain goroutine never aborts the build, preserving errgroup semantics.

### Scale and recreate diffing (`internal/dockerproxy`)

Both work with no server-side logic. `--scale N` maps to N deterministic `deploymentName`s; a project-label `ps` lists all N. Compose's keep-vs-recreate decision is client-side, keyed on `com.docker.compose.config-hash`, which the proxy round-trips through `/containers/json` (list, `rec.req.Labels`) and `/containers/{id}/json` (inspect, `Config.Labels`).

### `cornus compose logs` (`logs.go`)

Added as `Logs LogsCmd` in `compose.go` (`cmd/cornus/internal/composecli/logs.go`). Server plumbing already existed (`client.Logs` + `GET /.cornus/v1/deploy/{name}/logs`, a stdcopy-multiplexed `application/vnd.docker.raw-stream` body). The command bridges each service's raw stream through an `io.Pipe` into `stdcopy.StdCopy`, demuxing to stdout/stderr. Services stream concurrently (as Compose does); a shared mutex + line-buffering `prefixWriter` tags each line with the width-padded service name without interleaving partial lines, and trailing newline-less output is flushed.

Fidelity caveat: `--follow` has NO `-f` short. The parent `compose` group already owns `-f` for `--file` as a global flag inherited by every subcommand; kong rejects the duplicate short, and `-f` after `logs` would silently bind to `--file` anyway.

### Direct-to-cluster pod logs (`pkg/kubelogs`)

`cornus compose logs` originally always proxied through the server, which on a kube backend fulfils the request with the cornus server's own ServiceAccount — usually lacking RBAC to read workload pod logs. `pkg/kubelogs.Open(ctx, Options) (io.ReadCloser, error)` reads pod logs with the developer's kubeconfig credentials (the same ones `svcforward`/`kubeauth` use): loads the kubeconfig via `kubeclient.Load`, selects the pod by the `cornus.app` label (first Running, else first found — mirrors the kube backend `firstPod`), builds `PodLogOptions` from `api.LogOptions` (reusing `deploy.ParseSince`), and streams. Single-pod only, matching the server's documented limitation. Every setup failure (kubeconfig, list/RBAC, no pod, stream open) surfaces before any bytes flow, so the caller can fall back safely.

- `clientconn.Conn` gains `KubeCluster{KubeContext, Namespace}`, populated in `Resolve` from the profile's `PortForward`/`KubeAuth` block (nil for non-cluster profiles); precedence mirrors `mintKubeToken`.
- The `composecli` runtime gains `kubeLogs kubeLogOpener` (interface seam, faked in tests). `streamServiceLogs` tries `kubeLogs.Open` first; on Open error (no bytes) it logs a debug line and falls through to the proxy. Once the copy starts it never falls back (would duplicate output). The kube stream is unframed -> written straight to the stdout writer (no stdcopy), matching the backend folding both streams into the pod log. `ctx.Err()` during setup is a clean stop.

### Cluster-side reconcile reporting (`reconcile.go`)

`POST /.cornus/v1/deploy` (`handleDeployCollection` -> `backend.Apply`) returns a single JSON `DeployStatus` the instant objects are created, with no wait anywhere on the deploy path: kube `applyDeployment` returns `b.Status()` immediately after Create/Update (`pkg/deploy/kubernetes/kubernetes.go`), and `statusOf` projects `ReadyReplicas` onto synthetic `<name>-<i>` slots — so a mount-free `up` printed `0/1 running` before any pod scheduled. Even the streaming attach path (`/.cornus/v1/deploy/attach`) emits one `Event{Ready:true}` right after `Apply` whose "ready" is not real readiness (`pkg/server/deploy_attach.go`). `GET /.cornus/v1/deploy/{name}` (`Client.Status`) already returns fresh per-instance `InstanceStatus{ID,State,Running}`.

Fix is client-side only (no server/backend/protocol/api-type changes): `reportReconcile` polls `Client.Status` (500ms) after `Deploy`, printing one line per instance whenever its state changes, until every instance is running, ctx is cancelled, or a 120s bound elapses (bound is non-fatal: it notes giving up and returns the last status so the up still holds ports/mounts). A narrow `statusPoller` interface with a compile-time assert that `*client.Client` satisfies it. Wired into both mount-free branches — `runForeground` and `upDetached` (`commands.go`) — replacing the discarded `Deploy` status with the freshly polled one; a post-wait `ctx.Err()` bail tears down cleanly on Ctrl-C. An empty instance set (backends report it before any container exists) is treated as "not yet running" so the poll keeps going. This poll is uniform across kube/docker/containerd; richer per-pod phases (ContainerCreating/CrashLoopBackOff/image-pull) would need `InstanceStatus`/`statusOf` enriched in the backend interface — only `running`/`pending` are portable today.

### Compose `restart:` decoding and validation

Compose is parsed via `sigs.k8s.io/yaml`, which round-trips YAML -> JSON before `encoding/json`. YAML 1.1 coerces the bare word `no` — the compose-spec *default* restart value — to the boolean `false`; against a plain `string` `Restart` field that failed to unmarshal, so `restart: no` (a common, valid config) was rejected. Separately, `restart:` had never been actively validated anywhere — backends only *warn* on unknown values — so a typo like `restart: alwyas` silently reached the runtime.

Fix (`pkg/compose/types.go`): a named `type Restart string` with a custom `UnmarshalJSON` — string forms decode as-is; bool `false` (YAML `no`) reads back as `"no"`; bool `true` (YAML `yes`/`true`/`on`) is rejected outright (names no valid policy). Both paths funnel through `validateRestart`, which accepts `""` (unset), `no`, `always`, `unless-stopped`, `on-failure`, and `on-failure:N` (N a non-negative int), rejecting everything else with a message naming the valid policies. `Restart string` became `Restart Restart` (json tag unchanged); `pkg/compose/project.go` needed a `string(svc.Restart)` conversion where the value flows into `api.DeploySpec.Restart` (named types are not implicitly assignable to `string`).

The `on-failure:N` short form's retry count was validated but originally never reached `spec.RestartMaxAttempts` — worse, the WHOLE string `"on-failure:5"` was passed through to `spec.Restart` verbatim, which broke two backends outright: dockerhost set `restartPolicy.Name = "on-failure:5"`, an invalid Docker restart-policy name; containerdhost's `restart.NewPolicy("on-failure:5")` **errored**, failing the entire deploy. Fixed with `(Restart).split() (policy string, maxAttempts int)` (`on-failure:5` -> `("on-failure", 5)`, everything else -> `(value, 0)`); `project.go` now does `spec.Restart, spec.RestartMaxAttempts = svc.Restart.split()`, replacing the old verbatim assignment. `deploy.restart_policy` (the raw `DeploySpec`-level override) stays authoritative and overrides both afterward, unchanged.

### Foreground `up` exit code on Ctrl-C during startup (kube-widened race)

CI's `compose-up-signal-teardown.star` (SIGINT during a foreground, non-`-d` `up`, expecting exit 0) failed only on the kube target with exit 1 (`context canceled`). Root cause: `runForeground` has a startup deploy loop, then a steady-state hold (`select{<-ctx.Done(); <-gone}`) that on either branch tears down and returns `nil` (exit 0) — but a SIGINT landing WHILE STILL in the startup loop hit a guard that did `if ctx.Err() != nil { teardown(); return ctx.Err() }`, returning bare `context.Canceled`, which kong's `FatalIfErrorf` maps to exit 1. This was a timing-sensitive race present on every backend, not kube-specific logic — kube's slower reconcile just widened the window enough to fail deterministically in CI (the client polls the same server the harness does, so on docker/containerd it usually reaches the blocking hold before the SIGINT lands; on kube it can still be finishing the startup loop). An interrupt at that point also skipped `removeDeployments`, so it could additionally leak the mount-free deployments the up had created.

Fix (`cmd/cornus/internal/composecli/commands.go`): a pure, unit-testable helper `shutdownExit(genuine, ctxErr error) (err error, remove bool)` — a cancelled context (user Ctrl-C) returns `(nil, true)`, a clean shutdown that removes the mount-free deployments created so far; a live context returns `(genuine, false)`, so a real failure still propagates and removes nothing. A `finish` closure inside `runForeground` calls `teardown()` then `shutdownExit(genuine, ctx.Err())`, running `removeDeployments` when `remove` is true. Every startup-loop error/cancel return path (dependency wait, build, missing-image, deploy, the post-`reportReconcile` `ctx.Err()` guard, service hooks, mounted `expose`) now routes through `finish` instead of the old bare `teardown(); return ctx.Err()`. Net effect: a Ctrl-C anywhere in startup now tears down, removes the mount-free deployments, and returns exit 0 deterministically on every backend; the steady-state hold-exit path (already correct) was left as-is.

### Synchronous `down` + idle foreground `up` self-exit

Three `docker compose`-divergent teardown behaviors were addressed:

1. Idle foreground `up` never noticed a `down`. A foreground `up` (holding deploy-attach 9P sessions and/or auto-forwarded published ports) blocked on `<-ctx.Done()` in `runForeground`, which only fires on Ctrl-C; `down` deletes deployments server-side but has no channel to that foreground process (it only talks to the `up -d` supervisor over its socket). Fix: `runForeground` now `select`s on `watchGone(ctx, rt.client, resources, poll)` alongside `ctx.Done()` — when every held deployment reports zero instances the up prints `services removed; exiting.` and tears down.
2. `down` was fire-and-forget (`DownCmd.Run` called `Delete` then printed `removed`; the k8s delete only marks the object for deletion). `down` now waits (synchronous by default, `--wait`/`--no-wait` via a kong `negatable` flag) for workloads to fully terminate.
3. `down` reported no teardown status. `reportTeardown` (the `down` counterpart of `reportReconcile`) polls `Status` until `len(Instances)==0`, printing per-instance transitions then `removed`.

Implementation (`reconcile.go`): the shared poll-and-print loop is factored into `pollTransitions(...) (api.DeployStatus, pollOutcome)`; `reportReconcile` (done = all running) and `reportTeardown` (done = zero instances) are thin wrappers with their own terminal/timeout messages. `watchGone` returns a channel closed only when all named deployments are gone; on `ctx.Done()` it returns WITHOUT closing so the reader distinguishes Ctrl-C from external removal. "Gone" is uniform across backends: `Status` returns an empty-`Instances` `DeployStatus` with no error once a deployment is fully removed.

### `cornus compose exec`

`compose exec` resolves a SERVICE name to its `plan.Resource` deployment and executes in its first instance through the existing server exec endpoints — no new server-side plumbing was needed. `ExecCmd` supports Docker-compatible `-e`/`--env`, `-w`/`--workdir`, `-u`/`--user`, `-T`/`--no-TTY`, `--privileged`, and `--index`; `--index > 1` is rejected because the server exec API has no instance selector. `-d`/`--detach` is recognized but explicitly unsupported until the backends gain detach semantics: dockerhost forces `Detach: false` on its exec-create call and Kubernetes exec is an attached SPDY stream over `pods/exec` (see [[kubernetes-backend]]; no detached form exists there), so returning from the client early would abandon the exec'd process with no way to reattach.

`cmd/cornus/internal/execdrive` centralizes raw-mode terminal setup, initial resize, stdin/stdout bridging, and Docker-compatible exit-code mapping (`ExitCode`/`InspectFailCode`, with a 125 fallback) shared by both native `cornus exec` and `compose exec`. `-e KEY=VALUE` sets a value; a bare `-e KEY` imports the value from the local environment. The default is interactive with a pseudo-TTY when stdin is a terminal, otherwise it warns and falls back to a plain stream. The small SIGWINCH-watcher duplication between package `main` and `composecli` is intentional: `execdrive.Options.ResizeNotify` injects it, keeping `execdrive` itself free of platform-specific files.

### `compose exec -e` on Kubernetes: argv/ps exposure (security note, docs-only fix)

A user-flagged security concern was investigated across all three deploy backends: does `-e KEY=VALUE` leak the value via `ps`/`/proc/<pid>/cmdline` in the guest? dockerhost (native Docker exec-create API `Env` field) and containerdhost (OCI runtime spec `Process.Env` set directly) are both **not** exposed via `ps`. Kubernetes **is** exposed: its `execCommand` (see [[kubernetes-backend]]) wraps the command as `env KEY=VALUE... cmd...` because the `pods/exec` subresource (client-go `remotecommand`) has no per-exec `Env` parameter at all — only `Command []string` — a genuine Kubernetes API constraint with no native alternative, not a cornus oversight. The exposure is visible on argv/`ps` for the life of that process to anyone with exec access to the pod (not just processes already running inside it — `kubectl exec` access from outside the pod too), and is scoped strictly to values a user explicitly types with `-e` on `cornus compose exec` against a Kubernetes target (plain `cornus exec`, non-compose, has no `-e` flag at all). Confirmed architecturally unrelated to the credential-broker feature (`pkg/creddelivery`): broker-managed secrets are injected as pod-spec `Containers[0].Env` at container-*create* time, or via a mounted `emptyDir`, never through `ExecConfig`/exec.

This was previously undocumented in all three doc trees. Fix applied: added a `## cornus compose exec` section (flags table + usage example) to `docs/cli/compose.md`, `docs/ja/cli/compose.md`, `docs/zh/cli/compose.md`, each with a `::: warning` callout describing the Kubernetes-only argv exposure and recommending a mounted file or an image/deploy-time env var instead of `-e` for secrets on cluster profiles. No code change was made (none is apparent — `pods/exec` has no env parameter to switch to); this is a documentation-only mitigation. A possible future option, not built: writing the `KEY=VALUE` assignments to a short-lived in-pod file and sourcing it — this only relocates the exposure to a file rather than eliminating it.

## Files

- `cmd/cornus/internal/composecli/` — CLI, `daemon.go`, `daemon_unix.go`/`daemon_other.go`, `supervisor.go`, `logs.go`, `reconcile.go`
- `pkg/kubelogs/` — `Open`, direct-to-cluster pod log streaming via developer kubeconfig
- `cmd/cornus/internal/clientconn/clientconn.go` — `Conn.KubeCluster{KubeContext, Namespace}`, populated in `Resolve`
- `cmd/cornus/internal/daemonize/` — `daemonize.go` (`SelfArgs`/`stripDaemonFlag`), `spawn_unix.go` (`Spawn`), `spawn_other.go`, `daemonize_test.go`
- `cmd/cornus/daemon.go` — `DockerProxyCmd.Daemon` (`-d`), `detach()`
- `internal/compose/` — parsing, translation, `interpolate.go`, `ResolveMounts`, `Project.Volumes`, `BuildPlan`
- `pkg/compose/types.go` — `type Restart string`, `UnmarshalJSON`, `validateRestart`, `(Restart).split()`; `Service.AgentForward` (`x-cornus-agent-forward`)
- `cmd/cornus/internal/composecli/commands.go` — `shutdownExit`, the `finish` closure in `runForeground`
- `cmd/cornus/internal/execdrive/` — raw-mode terminal setup, resize, stdin/stdout bridging, and exit-code mapping (`ExitCode`/`InspectFailCode`) shared by native `cornus exec` and `compose exec`
- `internal/client/` — server HTTP client, `Build` (target stage + cache_from mapping)
- `internal/dockerproxy/translate.go` — `toDeploySpec`, `parseNamedVolume`, `isHostPath`
- `internal/deploy/kubernetes/` — bind-mount rejection, sidecar mounts, `namedPVCName`, `claimName`
- `internal/builder/solve_linux.go` — progressui build output
- `internal/server/` — deploy lifecycle action routes, `/.cornus/v1/build` build-args
- `e2e/scenarios/compose-mounts.star` + `compose-mounts.yaml` + `compose-mounts-data/marker`
- `e2e/scenarios/deploy-named-volume.star`

## Test Coverage

- `internal/compose/compose_test.go` — translation, dependency order + cycle detection, interpolation (`.env` + process-env override + `$$`, required-var error), env_file merge/precedence/optional-missing, `TestResolveMounts`, `TestNamedVolume` (scoping/`name:`/`external`), `TestAnonymousVolume`, `TestBuildTargetAndCacheFrom`, `TestBuildCacheFromScalar`.
- `pkg/compose/service_keys_test.go` — `TestServiceKeysRestart` (accepts `"no"`/bare `no`/`always`/`unless-stopped`/`on-failure`/`on-failure:5`; rejects `yes`/`true`/`on`/`sometimes`/`on-failure:-1`/`on-failure:abc`), `TestServiceKeysRestartSplit` (`on-failure:5` -> `("on-failure", 5)`, bare `on-failure` -> `("on-failure", 0)`, `deploy.restart_policy` override wins).
- `pkg/compose/agentforward_test.go` — opted-in and not-opted-in `AgentForward` translation.
- `cmd/cornus/internal/composecli/commands_test.go` — `TestShutdownExit` (cancelled ctx -> `(nil, remove=true)` even with a wrapped cancel error present; live ctx + genuine error -> `(genuine, remove=false)`; live ctx, no error -> `(nil, false)`), pinning the startup-loop Ctrl-C exit-code contract without needing a live client or timing race. The fix was also run 3x back-to-back against the containerized kube E2E runner (`compose-up-signal-teardown.star`, all green, exit 0) — but since the underlying trigger is a race, a green harness run does not by itself prove the fixed startup-loop branch was exercised; `TestShutdownExit` is what deterministically pins the startup-loop-cancel path.
- `e2e/scenarios/compose-exec.star` — resize, environment (`-e`), non-zero exit, and unknown-service behavior for `compose exec` on docker; the `exec_tty` harness builtin always runs under a PTY and maps argv[0] `cornus` to the built binary, so scenarios omit `-T` for interactive-size coverage and pass it for the non-TTY path.
- `internal/client/client_test.go` — Deploy/List/Status/Delete/Action, Build tar contents + `build-arg` query, `TestClientBuildTargetAndCacheFrom` (target stage over the wire, cache_from -> registry import, empty entry dropped).
- `internal/server` — `TestDeployLifecycleActions` (start/stop/restart routes + unknown action 404).
- `internal/dockerproxy` — `TestComposeScaleAndRecreateDiff` (3-replica create+start+ps; config-hash intact through list and inspect), `TestToDeploySpecNamedVolume`.
- kubernetes — `TestApplyRejectsBindMounts`, `TestApplyWithMountsInjectsSidecar`, `TestNamedVolumeSharedAndPersistent` (two deployments share one un-owned PVC; survives deleting one).
- dockerhost — `TestToCreateBodyNamedVolume`.
- `cmd/cornus/internal/composecli` — `TestDaemonStateRoundTrip`, `TestSupervisorDispatch` (ping/idle-down/unknown-action), a forward-only supervisor session over an injected echo dialer; `logs_test.go` runs the real `client.Client` against an httptest server emitting stdcopy frames (prefixed multi-service demux stdout+stderr, `--no-log-prefix` passthrough, partial-line flush; `-race` clean); direct-log-path test (proxy never contacted) and fallback-on-Open-error test (proxy used); `reconcile_test.go` (scripted-fake transitions printed once per change, transient Status errors don't abort, timeout path, cancelled ctx returns promptly, empty->running convergence, end-to-end through a real `*client.Client` over httptest, plus `TestReportTeardown*` and `TestWatchGone*` reusing the `scriptedPoller` fake); `commands_test.go` `TestDownWaitFlag` (kong `--wait` default / `--no-wait` opt-out).
- `pkg/kubelogs` — fake-clientset (label selection, no-pods `ErrNotFound`, bad `since`).
- `cmd/cornus/internal/daemonize` — `TestStripDaemonFlag` (argv-filter table test).
- `internal/builder/solve_linux_test.go` — `frontendAttrs` target + named-context filtering, empty TargetStage sets no attr, cacheEntries registry import.
- E2E: `compose-mounts.star` (docker- and kube-target; second `up -d` reuses the supervisor); `deploy-named-volume.star` (kube-only, out of default SCENARIOS, needs a cluster); a throwaway fake Docker Engine API (~5 endpoints) lets the full stack run live without a real daemon (`DOCKER_HOST=tcp://...` when launching `cornus serve`). `compose-down.star` (in the Makefile `SCENARIOS` list) proves synchronous `down` cross-target (workloads gone the moment it returns, asserted with no polling; `--no-wait` returns immediately and still removes everything) and, kube-gated (`if TARGET == "kube"`), the idle foreground `up` self-exit; two harness builtins support it — `compose_up_bg(file, project?)` backgrounds a FOREGROUND `compose up` capturing its output, `compose_up_wait(handle, timeout?)` waits for self-exit and returns `{output, code}` (both registered in `predeclared()`/`predeclaredNames()`; leftovers SIGINT'd in `stopServer`).

## Pitfalls

- Relative bind sources must be absolutized client-side (`ResolveMounts`) — the stateless deploy path forwards them verbatim and Docker rejects them.
- cornus compose bind mounts via the stateless path are server-host paths; against a remote server the path must exist on the server. Client-local streaming requires deploy-attach.
- Never turn bind mounts into k8s `hostPath` — node-local, non-portable, silently broken on multi-node placement. The kube backend rejects them instead.
- Compose bind mounts must be `:ro`; DeployAttach rejects read-write.
- k8s foreground deletion is async — E2E "removed" assertions after `down` must poll, since the Deployment lingers briefly.
- In compose-mounts E2E on kube, the service image must be the Cornus image (it doubles as the mount agent).
- A per-invocation daemon design for `up -d` orphans previous helpers; the supervisor must be per-project with a control socket.
- Foreground `compose up` now blocks whenever a selected service publishes ports; scripts (and E2E scenarios) must pass `-d` or `--no-forward-ports` — `compose.star` tripped over exactly this and uses `detach = True`.
- Unix socket paths are capped at ~108 bytes; with `-d` a too-long `--socket` fails in the CHILD (`bind: invalid argument`) and is visible only in the log file, while the parent has already reported success.
- `daemonize.SelfArgs()` strips only exact `-d`/`--daemon` tokens; a combined short group like `-dp proj` is not rewritten.
- On re-`up` against an older (pre-`Protocol: 2`) daemon, the CLI cannot detect spec changes — it warns and keeps the daemon rather than killing it (which would drop held mounts).
- Docker bind parsing: a non-path source like `cache:/data` is a named volume, not a host bind (`isHostPath`).
- Shared ReadWriteOnce PVCs need sharing pods co-located on one node — fine on single-node kind, a real constraint elsewhere.
- BuildKit progressui markers used by E2E assertions must live in a FILE, not the RUN command string, or vertex-name printing can false-match.
- Named k8s PVCs must not carry a Deployment owner-ref or the owner-ref GC cascade deletes them on `cornus delete`.
- `cornus compose logs --follow` has no `-f` short — the parent `compose` group already binds `-f` to `--file`; kong rejects a duplicate and `-f` after `logs` would silently mean `--file`.
- On a kube backend the cornus server's ServiceAccount usually cannot read workload pod logs, so `compose logs` must read directly with the developer kubeconfig (`pkg/kubelogs`) and only proxy through the server as a last resort. Fall back only on `Open` error before any bytes flow — once the copy starts, falling back would duplicate output.
- `POST /.cornus/v1/deploy` reports success the instant objects are created, not when pods are ready (kube reports `0/1 running`); real reconcile status needs a client-side poll of `GET /.cornus/v1/deploy/{name}` (`reportReconcile`), not the deploy/attach `Event{Ready:true}` (which is not real readiness). An empty `Instances` set means "not yet running", not done.
- A foreground `compose up` blocks on `<-ctx.Done()` and never learns about a `down` (which only talks to the `up -d` supervisor socket) unless it also watches for its deployments going to zero instances (`watchGone`); `watchGone` must NOT close its channel on `ctx.Done()` so Ctrl-C stays distinguishable from external removal.
- `compose down` on k8s only marks objects for deletion; it must wait (synchronous by default) for workloads to terminate, or callers see `removed` while the workload lingers. E2E assertions on synchronous `down` need no polling, but `--no-wait` removals still do.
- `restart:` must decode via the named `Restart` type, not a plain `string` — YAML 1.1 coerces the bare word `no` (the compose-spec default) to the boolean `false`, so a plain-string field silently rejects a common, valid config.
- A SIGINT during `runForeground`'s startup loop (before the steady-state hold) must route through `shutdownExit`/`finish`, not a bare `teardown(); return ctx.Err()` — the latter returns `context.Canceled`, which kong maps to exit 1 instead of the expected exit 0, and skips removing mount-free deployments. The race is present on every backend but is more likely to be hit on kube's slower reconcile.
- `cornus compose exec -e` against a Kubernetes target exposes the value via `ps`/`/proc/<pid>/cmdline` for the life of that process — a `pods/exec` API limitation with no code-level fix available; use a mounted file or an image/deploy-time env var for secrets on cluster profiles instead.
- `compose exec -d`/`--detach` must stay a rejected, clearly-erroring combination rather than silently downgrading to attached: dockerhost forces `Detach: false` on exec-create and Kubernetes exec is an attached SPDY stream with no detached form, so returning from the client early would abandon the exec'd process with no way to reattach. Likewise `--index > 1` is rejected outright — the server exec API has no instance selector to honor it.

## Foreground Lifecycle And Compose Fidelity

Foreground `compose up` holds whenever it selected services, even without mounts, published ports, or SOCKS5. `-d`, no selected services, and `--no-forward-ports` with no client-local mounts remain non-blocking. Once workloads are ready, foreground up follows selected container logs using the prefixed `compose logs -f` streamer; `--no-attach` and `--no-log-prefix` opt out. The hold cancels and joins the streamer on Ctrl-C or external down.

Compose now supports field-level multi-file merge, `extends`, `include`, profiles, runtime configs/secrets as read-only mounts, port/expose ranges, IPv6 host IPs, SELinux `z`/`Z`, shell-compatible command splitting, healthcheck start interval, and long-form `depends_on` conditions. `InstanceStatus.Health` and `ExitCode` provide cross-backend gating data; completion dependencies use a completion-aware reconcile path.

The CLI adds `down --volumes`, `ps --quiet|--services|--format`, `config`, `version`, repeatable `--env-file`/`--profile`, build `--no-cache`/`--build-arg`, and logs `--until`. `down --volumes` uses optional `deploy.VolumeRemover`; external volumes are never removed and unsupported backends soft-skip. Intentional divergences include `logs --follow` without `-f`, boolean `--no-attach`, and a smaller default `ps` table.

File-based configs and secrets inherit the client-local 9P bind realization. Because they bind a SINGLE file, the client exports the file's PARENT directory over 9P (a 9P mount root must be a directory) and carries the basename as `LocalMount.Subpath`; `MountManager.Prepare` rewrites the source to `<mountpoint>/<subpath>` so the dockerhost runtime binds just that file. The kube mount sidecar propagates a directory 9P mount via a shared emptyDir and cannot place one file at an arbitrary rootfs target (e.g. the config default path `/<name>` at the fs root), so single-file binds are rejected on kube (`rejectFileMounts`); `compose-configs-secrets.star` is dockerhost-only. A non-root same-host dockerhost deployment cannot use them even if direct host binds are permitted; a future safe same-host detection may select direct binds while retaining 9P for remote servers.

Compose build forwards SSH-agent configuration through the build wire, preserving `RUN --mount=type=ssh` for local and remote builds. Foreground up shares the agent reconcile engine, removes mount-free deployments when it exits, and waits for teardown reconciliation before returning so it does not leave fire-and-forget workloads behind.

`TestStreamLogsFollowStopsOnCancel` must drain and synchronize its follow-stream writer before test completion; a pre-existing race in the blocking test server was a test fixture defect, not product log-stream behavior.
