# cornus daemon docker: Local Docker API Proxy

## Summary

`cornus daemon docker` (`cmd/cornus/daemon.go`, `internal/dockerproxy`) is a persistent local daemon that speaks a subset of the Docker Engine REST API on a unix socket. Pointing `DOCKER_HOST` at it lets the stock `docker` CLI and `docker compose` drive Cornus's remote deploy-attach transport, so e.g. `docker run -d -v ./local:/ctr:ro IMAGE` runs on a remote Cornus server with the local directory streamed over 9P. Coverage spans run/ps/inspect/stop/rm, compose up/ps/down, logs/stats/cp, exec/attach (including interactive `-it` with terminal resize), on both dockerhost and kubernetes backends.

## Key Facts

- Wire structs are hand-rolled (not moby types), matching `dockerhost/engine.go`, to avoid moby version coupling. Tests drive handlers with raw Docker-shaped JSON plus a fake attacher.
- Docker's create/start split vs Cornus's atomic Apply: `create` only BUFFERS (translate → DeploySpec → record, no remote call); `start` opens a long-lived deploy-attach (`client.DeployAttach`) held for the container lifetime; `stop`/`rm` cancel it. This is why the proxy must be a persistent daemon.
- Seam: the `deployAttacher` interface (`DeployAttach` + `Status`, later also `ExecResize` etc.), which `*client.Client` already satisfies — handlers are unit-testable with a fake.
- Identity: one Docker container = one single-replica Cornus deployment. Deployment name derives from the docker `--name` (sanitized) or `cornus-<id12>`; the proxy keeps its own id↔record map and synthesizes `ps`/`inspect` from it. `deploymentName` (state.go) lowercases and maps non-alphanumeric → `-`, so compose's `<project>-<service>-1` maps verbatim (e.g. `dcomp-web-1`).
- Create-time labels are stored and echoed so `com.docker.compose.*` filters work. `parseLabelFilters` (`pkg/dockerproxy/containers.go`) accepts BOTH the legacy `{"label":["k=v"]}` list form and the modern `{"label":{"k=v":true}}` map form — docker CLIs at API >= 1.22 send the map form, and a list-only decoder silently matches EVERYTHING (the networks store handled both from the start).
- Create-time `Entrypoint` threads all the way through: `DeploySpec.Entrypoint []string` (`pkg/api/deploy.go`; empty keeps the image default, matching Docker). dockerhost maps it to Docker's `Entrypoint` create slot (Cmd stays Cmd, `pkg/deploy/dockerhost/engine.go` `createBody.Entrypoint`); kubernetes maps it to container `Command` and demotes `spec.Command` to `Args` (Docker semantics; `Command=spec.Command` stays when unset). `pkg/dockerproxy/translate.go` sets `spec.Entrypoint = req.Entrypoint`.
- `GET /images/{name}/json` resolves the ref's REAL config via go-containerregistry: secure parse first (ggcr's `name.ParseReference` already yields http for localhost/127.0.0.1 registries), then an explicit `name.Insecure` retry; per-ref success cache on the Proxy (`imageCfgs sync.Map`; failures are NOT cached so a late-starting registry is retried); 30s timeout; the synthetic empty-config fallback is preserved for unresolvable refs so offline compose is unchanged (`pkg/dockerproxy/images.go`). Clients like the devcontainer CLI derive entrypoint/cmd/env/user and `devcontainer.metadata` from this config.
- Published ports auto-forward on the client: `Proxy.start` publishes each container's PortBindings through `pkg/portfwd` after `waitReady`; the group lives in `containerRecord.fwd` and is closed exactly-once inside `setExited`'s current-session guard (outside the lock — `Close` drains), so stop/rm/wait all release it and `docker start` re-publishes. `docker run -p 8080:80` behaves like local Docker. `dockerproxy.New` is variadic; `WithoutPortForwards()` opts out. See `port-forwarding.md`.
- Version-prefix (`/vX.Y`) stripping routes both versioned and unversioned requests; `/_ping` sets the negotiation headers (docker CLI v29 negotiates API 1.43).
- An unnamed `POST /containers/create` deploys as `cornus-<12-hex short id>`, and inspect's `Name` reports exactly that — `docker inspect --format {{.Name}}` recovers the server-side deploy name portably (the devcontainer CLI creates unnamed containers and finds them purely by `devcontainer.local_folder` label filter).
- `deploywire` and `dockerproxy` have zero BuildKit dependencies; `/build` is intentionally unimplemented because it would relink BuildKit into the build-free proxy.
- Kubernetes backend: logs, exec, and attach are real implementations (exec/attach via client-go `remotecommand`); stats and cp are documented not-supported stubs (`kubectl top` / `kubectl cp`).
- REST verticals follow one pattern: Backend method → dockerhost Docker pass-through → REST route on the Cornus server → `Client` method → proxy `handleContainerItem` case.

## Details

### Compose support

Stock `docker compose up -d` / `ps` / `down` works against the proxy (verified live against a real Cornus server with a dockerhost backend). Required fakes:

- fake `/networks` (create/list/inspect/delete + `POST /networks/{id}/connect|disconnect` no-ops)
- fake `/volumes`
- hold-open `/events`
- network capture on create so inspect reports non-nil `NetworkSettings`

Three bugs that only the LIVE compose run caught (unit tests replay only the calls one imagines):

1. Image inspect 404 is fatal to compose. `docker run` tolerates a 404 on `GET /images/{name}/json` (it pulls), but compose inspects after pulling and treats "no such image" as fatal. Fix: return a synthetic 200 image inspect (Cornus resolves the real image at deploy time); 200 works for both run and compose.
2. Nil `NetworkSettings` panics compose. Compose dereferences `inspect.NetworkSettings.Networks` right after create. Fix: always populate `NetworkSettings` (Networks map per attached network, captured from the create body's `NetworkingConfig.EndpointsConfig`) plus `HostConfig`.
3. Teardown race: the proxy's stop/rm cancelled the deploy-attach session and returned before the server's async `backend.Delete` ran, so `compose down` returned with containers still up. Fixed in the transport: on graceful `down` the client waits (bounded) for the server's terminal `Done` before returning. Review-driven hardening: the original wait implicitly relied on the control-stream read not honoring the caller ctx (buildwire dials `NetConn` with `context.Background()`). Restructured `deploywire.Serve`: a reader goroutine feeds a buffered `res` channel and the main flow `select`s on `res` vs `ctx.Done()`, making the bounded graceful-wait explicit regardless of the conn's ctx behavior; `sess.Close()` remains guaranteed by the top-level defer, which also unblocks the reader.

Compose caveat: each service is a separate Cornus deployment. The faked network satisfies compose, but inter-service networking is whatever the backend provides (dockerhost default bridge / k8s Services), not a compose user-network.

Compose v5 scale reconverge (`docker compose up --scale web=N` then reconverge to a smaller N): network labels are stored at create time and echoed back, and the container-list JSON (`containerSummary`) carries `NetworkSettings.Networks` — compose v5 nil-derefs on list entries without it. Validated live against docker 29.2.1 / compose v5.0.2 (up 2 -> reconverge 1 -> down, `dockerd.star` scale sections).

### Foreground `docker run`: attach, wait, and events

Foreground `docker run` in the docker CLI issues attach -> wait -> start; getting any of the three wrong hangs or truncates the run:

- **attach before start**: the proxy must NOT call the backend `Attach` immediately (the deployment does not exist yet and the hijacked stream silently closes). `attachContainer` parks on the record's started-channel and opens the backend tunnel once the session is live — dockerd accepts attach on a created container the same way.
- **`wait?condition=next-exit`**: never reply `{"StatusCode":0}` instantly when no session exists (wait arrives BEFORE start). `next-exit`/`removed` block until a session goes live and then ends. Subtlety: the docker client's ContainerWait blocks on the response HEADER before the CLI issues start — hold the header and `docker run` deadlocks (wait waits for start, start waits for wait). dockerd's actual protocol, which the proxy replicates: flush `200` + Content-Type immediately, send the JSON body when the container exits.
- **`/events`**: a hold-open-only endpoint is not enough. A small eventHub publishes container start/die/stop/destroy events in BOTH the legacy (`status`/`id`/`from`) and modern (`Type`/`Action`/`Actor` with labels in `Attributes`) forms, filtered by the event/type/label/container filter keys; map-form and list-form `filters` both parse. `docker run` (create/start/stop/rm) publishes the events.

Plumbing: `containerRecord.sess`/state live behind a mutex with a `startedC` channel (closed on session-live, re-armed on stop) so attach and wait can park before start. Regressions: `TestEventsEmitsStart`, `TestWaitNextExit`, `TestAttachBeforeStart` (`pkg/dockerproxy/events_test.go`) mirror the exact subscribe-before-run / wait-before-start / attach-before-start sequences.

### Devcontainer CLI compatibility (@devcontainers/cli)

The official `@devcontainers/cli` (the engine VS Code's Dev Containers extension shells out to) works against the proxy with `DOCKER_HOST` pointed at it. What the CLI (0.80.0) actually does on `up` for an image config — durable protocol knowledge:

1. spawns `docker events --format {{json .}} --filter event=start` and blocks until a start event whose `Actor.Attributes` carry its `devcontainer.local_folder`/`.config_file` labels arrives;
2. spawns FOREGROUND `docker run --sig-proxy=false -a STDOUT -a STDERR ... --entrypoint /bin/sh <image> -c 'echo Container started\ntrap "exit 0" 15\nexec "$@"\nwhile sleep 1 & wait $!; do :; done'` (a keepalive — a trace log shows only the first line, which misleads);
3. treats the docker run process EXITING — even with status 0 — as fatal (it rejects the `started` promise).

So the CLI requires the foreground-run protocol above plus three general proxy fixes (not test shims): create-time `Entrypoint` threading (the CLI creates with `Entrypoint=["/bin/sh"] Cmd=["-c", <keepalive>]`, which previously degenerated to argv `["-c", ...]`), the real image config on `GET /images/{name}/json` (entrypoint/cmd/env/user + `devcontainer.metadata` derivation), and the modern map-form label-filter encoding (the CLI locates its container exclusively by label filter). `devcontainer up --log-level trace` prints each docker invocation — the debug loop of choice; its minified stack trace is useless.

### Published-port auto-forward

`Proxy.start` publishes the container's PortBindings client-side via `pkg/portfwd` after `waitReady`; the forward group is stored in `containerRecord.fwd` and closed exactly-once in `setExited`'s current-session guard (outside the lock, since `Close` drains). stop/rm/wait all release it; `docker start` re-publishes. `dockerproxy.New` takes variadic options; `WithoutPortForwards()` disables the behavior. Full engine semantics in `port-forwarding.md`.

### stats and cp (REST verticals)

- `docker stats`: `GET /containers/{id}/stats`. Server `handleDeployStats` streams `application/json` with per-write flush.
- `docker cp`: container archive `GET`/`PUT`/`HEAD /containers/{id}/archive?path=`. Server `handleDeployArchive` method-splits GET/PUT/HEAD and sets the stat header before the tar body. The `X-Docker-Container-Path-Stat` header round-trips dockerhost → server → client → proxy → CLI so `docker cp` sees the stat on both HEAD and GET exactly as against a real daemon.
- New `api` types: `StatsOptions{Stream}`, `PathStat{Name,Size,Mode,Mtime,LinkTarget}` (JSON tags match Docker's `container.PathStat` so it round-trips unchanged), `CopyToOptions{NoOverwriteDirNonDir, CopyUIDGID}`, plus `PathStatHeader` + `EncodePathStat`/`DecodePathStat` (base64 JSON).
- `Backend` gained `Stats`, `StatPath`, `CopyFrom`, `CopyTo`; dockerhost implements all via Docker endpoints, with a `firstInstanceID` helper resolving name → container (`Logs` refactored onto it too).
- Server routing: `handleDeployItem` switches the action segment — `logs`/`stats`/`archive` to dedicated handlers, else `handleDeployAction`.

### exec and attach (the hijacking vertical)

exec-create and exec-inspect are plain REST; exec-start and attach need a bidirectional raw stdio tunnel:

```
docker CLI conn --(http.Hijacker at proxy)--> WebSocket(net.Conn via wire) --> cornus server
   --(hand-rolled raw hijack of Docker's /exec/{id}/start or /containers/{id}/attach)--> dockerd
```

- Cornus talks to Docker over raw HTTP (no moby SDK), so the Docker-side hijack is hand-rolled in `dockerhost/engine.go` (`hijack`: dial a fresh socket conn, write the upgrade request, parse the status line + headers, return a `hijackedConn` reading from the buffered reader).
- The proxy `http.Hijacker`s the docker CLI conn and replicates the daemon handshake: `101 UPGRADED` + `vnd.docker.raw-stream` when the request carried `Upgrade` (`-it`), else `200` raw-stream.
- The WS preamble carries `ExecStartConfig`/`AttachConfig` as the first frame; the server decodes it then calls `backend.ExecStart`/`Attach`.
- `Backend` gained `ExecCreate/ExecStart/ExecInspect/Attach`; `api` types: `ExecConfig/ExecStartConfig/ExecState/AttachConfig`.
- Routes: `POST /.cornus/v1/deploy/{name}/exec`, `GET /.cornus/v1/deploy/exec/{id}/json`, WS `/.cornus/v1/deploy/exec/{id}/start`, WS `/.cornus/v1/deploy/{name}/attach`; on the proxy, an `/exec/` route plus container `exec`/`attach` cases.

Bridge shutdown policy (bug found only by the real-daemon E2E): the bidirectional bridge originally closed BOTH conns when EITHER copy returned. A non-interactive `docker exec` (no `-i`) sends no stdin, so the stdin direction EOFs instantly and tore the tunnel down before Docker's stdout arrived — `docker exec dproxy echo X` returned "". Echo-only fakes masked it (they emit output only in response to input). Fix: the bridge is output-authoritative — it waits on the Docker→client copy (process output) and closes both only when that finishes; a stdin EOF triggers a best-effort `CloseWrite` half-close of the Docker side (real `*net.TCPConn`/`*net.UnixConn`) instead of a full teardown.

### Interactive sessions and terminal resize

- Resize is an OUT-OF-BAND control-plane call, not in the hijacked stdio stream: `Backend.ExecResize(ctx, execID string, height, width uint) error` in `internal/deploy/deploy.go`; server route `POST /.cornus/v1/deploy/exec/{id}/resize?h=&w=` (`internal/server/deploy_exec.go`); `Client.ExecResize` (`internal/client/client.go`); dockerhost forwards to the daemon's `/exec/{id}/resize` (`engine.go execResize`, plain non-hijack POST).
- Proxy: `/exec/{id}/resize` forwards to `deployAttacher.ExecResize`; `/containers/{id}/resize` is accepted as a documented no-op (Cornus attach has no per-container primary-TTY resize primitive).
- Kubernetes exec (real, `internal/deploy/kubernetes/kubernetes.go`): client-go `k8s.io/client-go/tools/remotecommand`. `ExecCreate` resolves the first ready pod + "app" container and stores an `execSession` (id, cfg, buffered `sizeCh`, exitCode, done) in an in-process registry. `ExecStart` builds the `pods/exec` subresource request (`PodExecOptions{Command, Stdin, Stdout:true, Stderr:!Tty, TTY}`), `NewSPDYExecutor`, `StreamWithContext`, mapping the single wire conn onto stdin+stdout (TTY merges stderr at source; non-TTY combines stdout/stderr, same limitation as kube logs). `ExecResize` non-blocking-sends into `sizeCh`, drained by a `TerminalSizeQueue`. Exit code captured via `errors.As(err, utilexec.CodeExitError)` and surfaced through `ExecInspect`. `Attach` uses `pods/attach`. Pulled `moby/spdystream` + `mxk/go-flowrate` (indirect).
- Native CLI (`cmd/cornus/exec.go`): `cornus exec [-i] [-t] --server URL <name> <cmd...>`. With `-t` and a terminal stdin it `term.MakeRaw`s, sends the initial size plus a `SIGWINCH`-driven resize loop via `ExecResize`, bridges stdin → conn (CloseWrite on EOF) and conn → stdout, and propagates the remote exit code (`ExecInspect` → `os.Exit`). The `Cmd` positional uses kong `passthrough` so flags in the command (e.g. `sh -c ...`) reach the command rather than being parsed as Cornus flags.
- `cornus exec -t` allocates a PTY only when stdin is a real terminal (2026-07-07). The TTY collapses into one decision `tty := c.Tty && term.IsTerminal(stdin)`, used for `ExecCreate`, `ExecStart`, and the local raw-mode block alike — previously `Tty: c.Tty` was passed unconditionally, so a piped/CI `-t` made the server allocate a PTY the client could not drive in raw mode (CRLF-translated, garbled output). A non-terminal `-t` downgrades to a plain stream with a one-line stderr warning, matching docker/kubectl. This keeps E2E/CI usage from wedging on a PTY. No other unguarded interactive/TTY surface exists in the CLI (no ReadPassword/Scanln prompts; build progress is already TTY-aware via `progressui.AutoMode` in `pkg/build/builder/solve_linux.go`).

## Files

- `cmd/cornus/daemon.go` — proxy daemon entrypoint
- `internal/dockerproxy/` — Docker API handlers, id↔record state (`state.go` `deploymentName`), exec/resize routes
- `internal/deploy/deploy.go` — `Backend` interface (Stats, StatPath, CopyFrom, CopyTo, ExecCreate, ExecStart, ExecInspect, ExecResize, Attach)
- `internal/deploy/dockerhost/engine.go` — raw-HTTP Docker pass-throughs, hand-rolled `hijack`/`hijackedConn`, `execResize`, `firstInstanceID`
- `internal/deploy/kubernetes/kubernetes.go` — remotecommand-based exec/attach; stats/cp not-supported stubs
- `internal/server/deploy_exec.go` — exec/resize server routes; `handleDeployItem`/`handleDeployStats`/`handleDeployArchive` on the server
- `internal/client/client.go` — `Client.ExecResize` and the other client methods
- `cmd/cornus/exec.go` — native `cornus exec -it` CLI
- `deploywire` — `Serve` reader-goroutine/`select` structure for the bounded graceful teardown wait
- `pkg/dockerproxy/images.go` — real image config via go-containerregistry (`imageCfgs` cache); `pkg/dockerproxy/types.go` — `configJSON.Entrypoint`, `stateJSON.StartedAt`; `pkg/dockerproxy/containers.go` — `parseLabelFilters` (both encodings), lazy-header Logs/Stats
- `pkg/dockerproxy/{attach,proxy,state}.go` — `startedC` parking, eventHub, `containerRecord.fwd` port-forward group, `WithoutPortForwards()`
- `e2e/scenarios/dockerd.star` (incl. compose `--scale` sections), `e2e/scenarios/exec.star`, `dockerd-compose.yaml` fixture, `e2e/scenarios/devcontainer-vscode.star` + `e2e/scenarios/devcontainer-vscode/.devcontainer/` fixture — E2E scenarios
- `.agents-workspace/tmp/dockerd-e2e-run.sh` — privileged-container run harness for the live E2E

## Test Coverage

- Proxy unit tests (fake attacher, raw Docker-shaped JSON): `TestToDeploySpec`, `TestSplitPortProto`, `TestDeploymentName`, `TestContainerRunLifecycle`, `TestLabelFilter`, `TestPingAndVersion`, `TestContainerStats`, `TestContainerArchive` (PUT→GET→HEAD round-trip), `TestExecOutputSurvivesStdinEOF`, `TestExecResize`, `TestComposeSequence` (asserts `specFor("myproj-web-1")`).
- Server: `TestDeployStats`, `TestDeployArchive`, `TestExecStartOutputWithoutStdin`.
- dockerhost: `TestBridgeOutputSurvivesStdinEOF`.
- Kubernetes: `TestStatsNotSupported`, `TestArchiveNotSupported` (fake-clientset).
- The stdin-EOF regression tests emit output independent of stdin and send no stdin; they were verified to FAIL under the old close-on-either-EOF policy.
- E2E `dockerd.star`: real `docker` CLI (v29.2.1) against the proxy in a privileged container with the host socket mounted — run/ps/inspect/logs/stats/cp/exec/stop/rm, non-TTY exec (stdout streaming + exit-code propagation via exec-inspect), `docker exec -it` asserting a 30x120 terminal size forwarded through the proxy, and `docker compose -p dcomp up -d`/`ps`/`down` (compose plugin v2.29.7) against the `dockerd-compose.yaml` fixture. `stats --no-stream` asserts the format-invariant `CPU` header (name/id is remapped through the proxy); `cp` asserts a host→container→host payload round-trip.
- E2E `exec.star` (docker + kube/kind targets): native `cornus exec -i -t ... sh` running `stty size; echo ...; exit`, asserting the marker plus the exact `<rows> <cols>` (proving resize propagates), and a non-TTY exec asserting exit code 7. Driven via a `github.com/creack/pty`-based `exec_tty(argv, input, rows, cols, timeout, env)` harness builtin (treats Linux EIO-on-slave-close as EOF).
- Harness builtin `docker_compose(...)` runs `docker compose <args>` with `DOCKER_HOST=unix://<proxy-sock>` appended last in the child env (so it wins over inherited values); registered in `predeclared()`/`predeclaredNames()`; requires `dockerd_up()` first. `devcontainer_cli(*args)` does the same for the `devcontainer` binary.
- Devcontainer/foreground fixes: `pkg/dockerproxy/images_test.go` (real config round-tripped through cornus's own in-process registry + fallback case), `translate_test.go` (entrypoint), `proxy_test.go` (map-form + non-matching label filters, `fakeAttacher.PortForward` + `TestContainerPortForward`: start->dial->echo, stop->refused, restart->works), `dockerhost_test.go` `TestToCreateBodyEntrypoint`, `kubernetes_test.go` `TestApplyEntrypoint`, `pkg/dockerproxy/events_test.go` (`TestEventsEmitsStart`, `TestWaitNextExit`, `TestAttachBeforeStart`).
- E2E `devcontainer-vscode.star`: `devcontainer up` on an image-based fixture, label-filter container lookup, postCreateCommand side effects, bidirectional workspace bind-mount visibility (host<->container over deploy-attach 9P), containerEnv, `devcontainer exec` exit-code propagation, `rm -f` teardown — passes hands-off in the containerized runner. Distinct from `devcontainer.star` (cornus's own `cornus compose --devcontainer` translation). Fixture design: image-based, entrypoint-less `alpine:3.20`, `overrideCommand: true`, `userEnvProbe: "none"`, and a postCreateCommand that writes INTO the workspace (one hook proves both the lifecycle-exec path and container->host bind visibility); the scenario copies `.devcontainer/` to a temp workspace so postCreate writes never dirty the committed tree.

## Pitfalls

- The compose CLI plugin does NOT honor `docker -H`; it must be pointed at the proxy via `DOCKER_HOST`. Keep that env isolated to compose invocations — putting `DOCKER_HOST` in the E2E harness's shared `toolEnv` would repoint `serve()`, since Cornus's own dockerhost backend reads `DOCKER_HOST` and would recurse into its proxy.
- Compose treats a 404 image inspect as fatal and dereferences `inspect.NetworkSettings.Networks` unconditionally — the proxy must return a synthetic 200 image inspect and always-populated `NetworkSettings`/`HostConfig`. Compose v5 additionally nil-derefs `NetworkSettings.Networks` on container-LIST entries during scale reconverge.
- When faking a wire API, test with the encodings real clients send today, not the ones the docs show first: the label-filter map-form bug was invisible because `proxy_test.go` and `compose_test.go` both hand-crafted the legacy list form; every real docker CLI >= API 1.22 got unfiltered `ps` results.
- Never hold the `wait?condition=next-exit` response header until exit — flush `200` + Content-Type immediately and send the JSON body at exit, or foreground `docker run` deadlocks (its ContainerWait blocks on the header before the CLI issues start). And never reply `{"StatusCode":0}` before a session exists.
- The devcontainer CLI treats its foreground `docker run` process exiting — even 0 — as fatal; the keepalive command must actually keep running, which requires the attach-parks-on-startedC and blocking-wait semantics above.
- Never close a bidirectional stdio bridge on stdin EOF; be output-authoritative and half-close (`CloseWrite`) the Docker side instead, or non-interactive exec loses its output.
- Do not rely on a WebSocket `NetConn`'s ctx behavior for graceful-shutdown waits; make the wait explicit (reader goroutine + `select` on result vs `ctx.Done()`), as done in `deploywire.Serve`.
- `stop` tears the deployment down (no stop-and-keep): a client-served 9P mount cannot outlive the caller.
- `/build` is out of scope for the proxy — it would relink BuildKit into the deliberately build-free binary. Interactive `-it`/`attach` are not driven by the automated E2E shell-out harness (needs a PTY); they are unit-tested and share the exec tunnel that the live E2E exercises, and the PTY path is covered by the `exec_tty` builtin scenarios.
- Client-go logs a benign `failed to get reader: context canceled` when the remotecommand stream closes on shell exit — cosmetic.
- Kubernetes stats/cp are intentionally not supported (`kubectl top` / `kubectl cp`); TTY exec merges stderr at source and non-TTY combines stdout/stderr (same limitation as kube logs).

## Shared Attach And In-Pod Reuse

The Docker frontend shares the deploy-attach primitive rather than keeping a parallel mount/session lifecycle. Keep frontend API translation separate from session ownership: the client agent reconciler owns held resources, while dockerproxy consumes the shared attach seam.

The proxy can run as a caretaker Docker role inside a Kubernetes pod. It serves loopback TCP or Unix and calls the Cornus client API with a separately provisioned client token; the attach token has the wrong audience. Static `/_ping` and `/version` are valid readiness and E2E exposure probes, but `/containers/json` is proxy-local state and does not prove a request reached the server.

## Shared Interactive Exec Drive

Native `cornus exec` and `cornus compose exec` share `cmd/cornus/internal/execdrive` for terminal
raw mode, initial size, resize forwarding, stdin/stdout bridging, and Docker-compatible exit-code
mapping (`ExitCode` / `InspectFailCode`, with 125 fallback). The platform-specific SIGWINCH watcher
is supplied through `Options.ResizeNotify`, keeping this transport-oriented package independent of
platform build tags; Compose retains its small watcher copy intentionally.

Dockerhost currently forces `ExecStartConfig.Detach` false and Kubernetes exec is an attached SPDY
stream. Compose therefore recognizes but rejects `--detach` rather than pretending the process can
survive the client stream.
