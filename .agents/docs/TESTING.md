# Testing

Cornus has two layers of automated testing: the in-process Go test suite
(`go test ./...`) and a Starlark-powered end-to-end (E2E) harness that drives a
real Cornus server against pluggable targets.

- [Unit and integration tests](#unit-and-integration-tests-go-test)
  - [Cloud-storage backends (emulator runs)](#cloud-storage-backends-emulator-runs)
- [E2E test harness](#e2e-test-harness)
  - [How it fits together](#how-it-fits-together)
  - [The runner (`cornus-e2e`)](#the-runner-cornus-e2e)
  - [Targets](#targets)
  - [The scenario language](#the-scenario-language)
  - [Builtin reference](#builtin-reference)
  - [Return-value shapes](#return-value-shapes)
  - [Scenarios](#scenarios)
  - [Benchmark suite (opt-in)](#benchmark-suite-opt-in)
  - [Dependencies and preflight](#dependencies-and-preflight)
  - [Containerized runner](#containerized-runner-all-in-one-image)
  - [Extending the harness](#extending-the-harness)

## Unit and integration tests (`go test`)

```sh
go test ./...
```

The registry, deploy backends (dockerhost + kubernetes via a fake clientset), and
server APIs are covered without external daemons. The full build-engine integration
test (`pkg/build/builder`) and the S3 storage test are opt-in (root / a rootless userns
stack / a live S3 endpoint) and skip otherwise. See the top-level `CLAUDE.md` for the
build/vet/test gate every Go change must pass.

### Cloud-storage backends (emulator runs)

The `gs://` / `azblob://` registry backends exist only in a `-tags cloudblob`
build, and their tests (`TestGCSBackend` / `TestAzblobBackend` in
`pkg/storage/cloudblob_test.go`) are opt-in: they skip unless
`CORNUS_TEST_GCS` / `CORNUS_TEST_AZBLOB` name a bucket/container. Credentials
and emulator endpoints come from each SDK's standard environment, so both
tests run against local emulators. Validated reproductions:

**GCS via fake-gcs-server:**

```sh
docker run -d --name gcs-emul -p 127.0.0.1:44430:4443 fsouza/fake-gcs-server \
  -scheme http -port 4443 -public-host 127.0.0.1:44430
# create the bucket via the emulator's API:
curl -s -X POST "http://127.0.0.1:44430/storage/v1/b?project=test" \
  -H 'Content-Type: application/json' -d '{"name":"test-bucket"}'
STORAGE_EMULATOR_HOST=127.0.0.1:44430 CORNUS_TEST_GCS='gs://test-bucket' \
  go test -tags cloudblob ./pkg/storage/ -run GCS -v
```

**Azure Blob via Azurite** (`--skipApiVersionCheck` is required; the account
key below is Azurite's PUBLIC well-known `devstoreaccount1` dev credential,
fine to commit):

```sh
docker run -d --name az-emul -p 127.0.0.1:44431:10000 \
  mcr.microsoft.com/azure-storage/azurite \
  azurite-blob --blobHost 0.0.0.0 --blobPort 10000 --skipApiVersionCheck
# create the container `test-container` with the devstoreaccount1 connection string
AZURE_STORAGE_ACCOUNT=devstoreaccount1 \
AZURE_STORAGE_KEY='Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==' \
AZURE_STORAGE_DOMAIN='127.0.0.1:44431' AZURE_STORAGE_PROTOCOL=http \
AZURE_STORAGE_IS_LOCAL_EMULATOR=true \
CORNUS_TEST_AZBLOB='azblob://test-container' \
  go test -tags cloudblob ./pkg/storage/ -run Azblob -v
```

**E2E coverage over the same emulators:** `e2e/scenarios/registry-gcs.star` and
`e2e/scenarios/registry-azblob.star` drive the WHOLE registry HTTP surface
(serve + OCI round-trip + catalog/tags) over `serve(storage=...)`, gated on the
same `CORNUS_TEST_GCS` / `CORNUS_TEST_AZBLOB` env so they self-skip in default
runs (they are in the Makefile `SCENARIOS` list for `e2e-check`). The harness's
`serve()` spawns cornus with the runner's environment, so the emulator env
above reaches the server process — but the SERVED binary must be a
`-tags cloudblob` build, which `make e2e-cloudblob` handles (it builds
`bin/cornus-cloudblob` with the tag and runs exactly these two scenarios on
`--target local`). With the emulators from the commands above running (bucket
`test-bucket` / container `test-container` created), run:

```sh
STORAGE_EMULATOR_HOST=127.0.0.1:44430 CORNUS_TEST_GCS='gs://test-bucket' \
AZURE_STORAGE_ACCOUNT=devstoreaccount1 \
AZURE_STORAGE_KEY='Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==' \
AZURE_STORAGE_DOMAIN='127.0.0.1:44431' AZURE_STORAGE_PROTOCOL=http \
AZURE_STORAGE_IS_LOCAL_EMULATOR=true \
CORNUS_TEST_AZBLOB='azblob://test-container' \
  make e2e-cloudblob
```

Both scenarios were validated live against fake-gcs-server and Azurite with
exactly this flow (2026-07-07).

### These backends now execute in CI (the `integration-backends` job)

The standard gate only COMPILE-checks the opt-in storage/credential backends
(`go build -tags cloudblob`), so historically S3 / GCS / Azure and the `aws-sts`
credential source were never executed in CI. The `integration-backends` job in
`.github/workflows/ci.yml` closes that: it stands up three local emulators —
winterbaume (one binary serving both S3 and STS, pinned + sha256-verified),
fake-gcs-server, and Azurite — pre-creates the GCS bucket and Azure container,
and runs all four in ONE tagged invocation
(`go test -tags "credaws cloudblob" -run 'S3|STS|GCS|Azblob' ./pkg/storage/ ./pkg/credential/awssts/`).
The `credaws` and `cloudblob` tags compose, so a single binary covers every
backend; no cloud account or egress to AWS/GCP/Azure is needed. The same tests
still self-skip in the standard gate (no endpoints set) and in a bare
`go test ./...`, so nothing outside that job depends on an emulator. The E2E-tier
equivalents (`registry-s3/gcs/azblob.star`, `credentials-sts.star`) remain
manual/opt-in (`make e2e-cloudblob` / `make e2e-credaws`) — CI executes the
adapter behavior at the unit tier, not the full registry/broker HTTP path.

## E2E test harness

The harness lives in `pkg/e2e/` (the engine) and `cmd/cornus-e2e/` (the CLI). It
drives a **real** `cornus` binary — the server plus its compose client and Docker
API proxy — end to end against a chosen deployment target, and asserts on observed
behavior. Scenarios are `.star` (Starlark) files under `e2e/scenarios/`.

### How it fits together

```
cmd/cornus-e2e/main.go        CLI: parse flags, build a Target, preflight, run scenarios
  └─ pkg/e2e/target.go        Target: local | docker | containerd | bare | kube — the deploy backend + env
  └─ pkg/e2e/harness.go       Harness: one per scenario; registers Starlark builtins,
  │                             runs the .star file, tears everything down after
  └─ pkg/e2e/preflight.go     Preflight: probe required tools/privileges before setup
  └─ pkg/e2e/value.go         Starlark <-> Go value conversion helpers
```

Execution model (`cmd/cornus-e2e/main.go`):

1. Parse flags, build the `Target`, then **preflight** the tools/privileges the
   target + scenarios need (fail-fast; `--skip-preflight` bypasses).
2. `target.Setup()` once (e.g. create the kind cluster).
3. For **each** scenario, construct a fresh `Harness` (`e2e.New`) and call
   `RunFile`. Each scenario gets its own server, data dirs, and ssh agent.
4. On scenario finish (pass or fail), the harness tears down in order: stop
   long-lived `deploy_attach` processes (dropping the caller connection unwinds
   the workload and its mounts), then the `cornus daemon docker` proxy, then the
   server, then the ssh-agent, then remove the per-harness temp data root. Any
   `port_forward` processes are killed too.
5. `target.Teardown()` once at the end (unless `--keep`).

Key internal invariant: **the served `cornus` and a local `cornus build` must use
different data dirs.** BuildKit's boltdb takes an exclusive, no-timeout file lock on
`cache.db`, so sharing one deadlocks. The harness hands out isolated per-role dirs
(`server`, `build`, `attach`, ...) under a per-harness temp root; `build(fresh_cache=True)`
allocates a brand-new numbered build dir so a local cache starts empty (used to prove
a `--cache-from` import is what produces a hit).

### The runner (`cornus-e2e`)

`make build` compiles both `cornus` and `cornus-e2e` into `$(BIN)`, and every
`e2e-*` Make target depends on it (passing `--cornus $(BIN)/cornus` via
`E2E_FLAGS`). Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--target` | `docker` | Deployment target: `docker`, `containerd`, `bare` (daemonless OCI runtime), `kube` (kind), or `local` (build-only, no external env). |
| `--cornus` | `cornus` (env `CORNUS_BIN`) | Path to the `cornus` binary (also provides the compose client and Docker API proxy). |
| `--storage` | `mem://` | Default registry storage backend for the test server (the `serve()` default). |
| `--cluster` | `cornus-e2e` | kind cluster name (kube target). |
| `--namespace` | `cornus-e2e` | Kubernetes namespace (kube target). |
| `--containerd-address` | `/run/containerd/containerd.sock` (env `CORNUS_CONTAINERD_ADDRESS`) | containerd socket (containerd target). |
| `--containerd-namespace` | `cornus-e2e` | containerd namespace (containerd target). |
| `--bare-runtime` | `runc` (env `CORNUS_BARE_RUNTIME`) | OCI runtime binary for the bare target (runc/crun/youki). |
| `--bare-snapshotter` | auto (env `CORNUS_BARE_SNAPSHOTTER`) | snapshotter for the bare target (overlayfs/native). |
| `--keep` | off | Do not tear down the kind cluster after running. |
| `--check` | off | Parse **and resolve** scenarios only; do not execute (see below). |
| `--preflight` | off | Probe the environment, print a report, and exit. |
| `--skip-preflight` | off | Skip the automatic preflight gate before executing. |
| `--scenario-timeout` | `10m` (env `CORNUS_E2E_SCENARIO_TIMEOUT`) | Per-scenario wall-clock timeout: a scenario that exceeds it fails fast (its hung child processes are killed) instead of blocking the whole run until the CI job timeout. Applies to every target, including the containerized runner. `0` disables. |
| `<scenario>...` | — | One or more scenario `.star` files. |

Scenarios reference repo-root-relative paths (e.g. `context = "e2e/scenarios/app"`),
so run the harness from the repo root.

### Targets

A `Target` (`pkg/e2e/target.go`) supplies the environment `cornus serve` needs to
select the matching deploy backend, and prepares built images so the target can run
them. Five implementations:

| Target | Backend | `serve` env it injects | `PrepareImage` |
|--------|---------|------------------------|----------------|
| `local` | server default (never deploys) | none | no-op — build-only scenarios |
| `docker` | `dockerhost` | `CORNUS_DEPLOY_BACKEND=dockerhost`, `CORNUS_ALLOW_BIND_SOURCES=/` (so stateless host-bind scenarios work), `DOCKER_HOST` passthrough | no-op — the backend pulls from the same-host registry |
| `containerd` | `containerd` | `CORNUS_DEPLOY_BACKEND=containerd`, `CORNUS_CONTAINERD_ADDRESS`, `CORNUS_CONTAINERD_NAMESPACE`, `CORNUS_BUILD_WORKER=containerd`, `CORNUS_ALLOW_BIND_SOURCES=/`, `CORNUS_CNI_BIN_DIR`/`CNI_PATH` passthrough | no-op — the backend pulls from the same-host registry (localhost refs are plain-HTTP automatically) |
| `bare` | `bare` (daemonless OCI runtime) | `CORNUS_DEPLOY_BACKEND=bare`, `CORNUS_BARE_RUNTIME`, `CORNUS_ALLOW_BIND_SOURCES=/`, `CORNUS_BARE_SNAPSHOTTER`/`CORNUS_CNI_BIN_DIR`/`CNI_PATH`/`CORNUS_BARE_INSECURE_REGISTRIES` passthrough | no-op — the backend pulls from the same-host registry into its own content store (no image-store sharing; the build engine's default worker pushes and the backend pulls it back) |
| `kube` | `kubernetes` (kind) | `CORNUS_DEPLOY_BACKEND=kubernetes`, `KUBECONFIG`, `CORNUS_K8S_NAMESPACE`, `CORNUS_K8S_IMAGE_PULL_POLICY=IfNotPresent`, `CORNUS_K8S_SIDECAR_IMAGE=cornus:e2e` | `kind load image-archive` — pulls the pushed ref to a tarball and loads it into the cluster nodes |

Bare specifics: there is no daemon to reach. `Setup` verifies the configured OCI
runtime binary (`--bare-runtime`, default `runc`) is on PATH, that the process is
root (the overlay snapshotter mount, per-instance netns, and CNI all need it —
rootless is out of scope for now), and that the same CNI reference plugins the
containerd backend needs (`bridge`, `portmap`, `host-local`, `loopback`) exist under
`CORNUS_CNI_BIN_DIR`/`CNI_PATH`/`/opt/cni/bin`. `Teardown` best-effort deletes any
leftover `cornus-*` containers under the backend's tmpfs runtime state root
(`/run/cornus/bare-runc`) via the runtime CLI itself. Unlike the containerd target it
sets no build-worker override — the bare backend keeps its own content store and
pulls each image from the co-located registry.

Containerd specifics: `Setup` verifies the daemon answers `Version` over the Go
client on the configured socket (usually root-only) and that the CNI reference
plugins the backend requires (`bridge`, `portmap`, `host-local`, `loopback`) exist
under `CORNUS_CNI_BIN_DIR`, `CNI_PATH`, or `/opt/cni/bin` — the same resolution
the backend uses. `Teardown` best-effort deletes any leftover containers labeled
`cornus.managed=true` in the target namespace (default `cornus-e2e`, so test state
never mixes with a production `cornus` namespace). `CORNUS_BUILD_WORKER=containerd`
also points the server's build engine at the same daemon.

Kube specifics: `Setup` creates the kind cluster (reusing an existing one of the
same name), writes a kubeconfig, ensures the namespace, and discovers the **kind
docker-network gateway** — the address an in-cluster pod uses to reach a host-run
server. `serve()` detects this (via the `advertiser` interface), binds all
interfaces, and sets `CORNUS_ADVERTISE_URL` to the gateway URL so the mount/hub
caretaker sidecars can dial back. The kube sidecars run the `cornus` binary, so they
use the `cornus:e2e` image regardless of the workload's app image — that image must
be loaded into the cluster (the containerized runner does this).

### The scenario language

A scenario is a Starlark file executed with these dialect options
(`scenarioFileOptions`): **top-level `for`/`if`** and **reassignment of top-level
names** are enabled, so a scenario can naturally write `st = wait(...)` more than
once and use control flow at the top level. `--check` parses AND resolves against the
predeclared builtin names, catching structural errors and undefined-name typos up
front (it shares the exact options `RunFile` uses, so `--check` predicts what a run
accepts).

The only injected global constant is **`TARGET`** — the string `"local"`,
`"docker"`, `"containerd"`, `"bare"`, or `"kube"`. Scenarios branch on it to skip
target-specific assertions.
Everything else is a builtin function. A canonical scenario:

```python
# e2e/scenarios/deploy.star
serve()
image = build(name = "demo", context = "e2e/scenarios/app", args = {"GREETING": "from-e2e"})
deploy(name = "demo", image = image, replicas = 2)
st = wait(name = "demo", running = 2, timeout = "180s")
assert_eq(st["running"], 2, "expected 2 running instances")
remove(name = "demo")
```

Ordering rules enforced by the builtins: **`serve()` must be called first** — `build`,
`deploy_attach`, `dockerd_up`, `port_forward`, etc. error out if the server is not up.
Durations are Go duration strings (`"180s"`, `"1m"`). Keyword args are the norm;
`?` below marks an optional argument.

### Builtin reference

Signatures reflect `starlark.UnpackArgs` in `pkg/e2e/harness.go`. Dict args take
`{str: str}`; list args take `[str]` (a bare string is also accepted where noted).

**Server and process lifecycle**

| Builtin | Signature | Returns / effect |
|---------|-----------|------------------|
| `serve` | `serve(storage?, env?, name?, addr?)` | Starts `cornus serve` (storage defaults to the `--storage` value). Returns the `"127.0.0.1:PORT"` address; on kube binds all interfaces and advertises the gateway URL. `env={k: v}` injects extra server-process env (appended last, so it wins over the target's serve env) — e.g. `serve(env={"CORNUS_API_POLICY": ...})` to boot an auth-enabled server for a negative-path scenario. **`name=` makes it an ADDITIONAL replica** for multi-replica scenarios: it gets its own data dir (`server-<name>`), returns a handle (`.addr`, `.name`, `.data_dir`) instead of a bare string, and deliberately does NOT take over `h.server`/`h.client`/`h.registryHost` — the unnamed call keeps meaning "the server" for every other builtin, so existing scenarios are unaffected. Replicas are stopped on teardown, not by `stop_server()`. **`addr=`** (only valid with `name=`) pins the listen address, for when a replica's own URL must be in its environment before it starts — the hub case, where `CORNUS_HUB_FORWARD_URL` is what peers dial back on and so cannot be discovered afterwards; pair it with `"127.0.0.1:" + free_port()`. |
| `cornus_bg` | `cornus_bg(*args, env?, log?)` | Backgrounds a long-lived `cornus` process and returns a handle (`.log`). Distinct from `cornus()` (waits for exit) and `cornus_stream()` (waits for a line, then interrupts): a hub spoke must stay up for the whole scenario, holding the connection a cross-replica forward routes to. Killed on teardown. |
| `redis` | `redis(image?)` | Runs a real Redis container and returns a handle (`.url` for `CORNUS_HUB_REDIS`, `.addr`, `.container`), waiting for it to answer `PING` rather than merely to have been created. Real, not miniredis: a multi-replica scenario is about two independent PROCESSES agreeing through a store neither owns. Removed on teardown. Needs docker. |
| `tcp_echo` | `tcp_echo()` | Listens on loopback and echoes every byte back; returns a handle (`.addr`). The delivery target a hub spoke registers, in-process so no helper binary or socat is needed. Closed on teardown. |
| `dial_echo` | `dial_echo(addr, line, retry?)` | Writes `line` to `addr` and returns the echoed reply (retrying, default 20x1s). Proves a cross-replica forward actually round-trips to the hosting spoke, rather than only that a listener accepted the connection. |
| `stop_server` | `stop_server()` | Kills the server but keeps data dirs / ssh-agent. Pair with a later `serve()` on the same `--storage` to prove persistence across a restart. |
| `dockerd_up` | `dockerd_up()` | Starts `cornus daemon docker` against the server; returns its `"unix://<sock>"` `DOCKER_HOST`. |
| `port_forward` | `port_forward(name, port, server?)` | Backgrounds `cornus port-forward` to a deployment's container port; returns the local `"127.0.0.1:PORT"`. Exercises CLI -> server -> backend port-forward end to end. |
| `tunnel` | `tunnel(name, port, server?, proto?)` | Backgrounds `cornus tunnel` to a deployment's container port; the server hosts a public tunnel and bridges it to the workload. Returns the public `"https://..."` URL. Backend-agnostic — the server picks the backend via `CORNUS_TUNNEL_BACKEND` (pass it through `serve(env=...)`). The default ngrok path needs `NGROK_AUTHTOKEN` in the harness env (inherited by the CLI, never on argv), so `deploy-tunnel.star` gates on `getenv("NGROK_AUTHTOKEN")`; the `tailscale` path is anonymous and `deploy-tunnel-tailscale.star` gates on `getenv("CORNUS_TUNNEL_TAILSCALE_E2E")` (needs a Funnel-enabled tailnet node). Killed on teardown with the other backgrounded CLIs. |
| `web` | `web(host?, compose_file?, project?, frontend?, mcp?, publish?, conduit?, publish_name?)` | Backgrounds `cornus web` (the local web UI + its `/.cornus/web/*` backend-for-frontend) against the running server; returns its `"http://127.0.0.1:PORT"` base URL. `compose_file`/`project` add `-f`/`-p` so the project/graph/mounts endpoints have a project to describe; `frontend="127.0.0.1:PORT"` (e.g. a `frontend_stub()`) runs the **detached-frontend** mode — non-BFF requests reverse-proxy to that dev server instead of the embedded SPA; `mcp=False` passes `--no-mcp`. `http_get()` the returned base + `/.cornus/web/...` to assert the BFF reflects real deployed workloads / compose projects / mounts. `publish=True` instead runs `cornus web --publish-in-conduit`: the UI binds NO port and is hosted inside the background agent, published in the **shared** SOCKS5 conduit, so the return value is the published NAME rather than a URL — reach it only through the proxy (`http_get(url="http://<name>/…", socks5=<proxy addr>)`). Pin the proxy address with `conduit="socks5://127.0.0.1:PORT"` (so the scenario knows where to point `socks5=`) and override the default name (the conduit suffix apex, `cornus.internal`) with `publish_name=`. The name is read back off the client's own "cornus web UI (in conduit)" line, so a scenario asserts the name the product actually published. Killed on teardown. Used by `web.star`. |
| `web_stop` | `web_stop(handle, timeout?)` | SIGINTs a `web(publish=True)` client (Ctrl-C) and waits for it to exit (default `timeout="30s"`), returning `{"output", "code"}`. The agent parks on the client's held connection and reaps the registration when it closes, so the process exiting **is** the withdrawal of the published name — which is what makes the clean exit part of the contract rather than incidental. |
| `frontend_stub` | `frontend_stub()` | Starts an in-process HTTP server on the harness loopback that answers any path with a sentinel HTML body (`FRONTEND-STUB`); returns its `"127.0.0.1:PORT"`. Point `web(frontend=...)` at it to prove `cornus web`'s detached-frontend reverse-proxy path end to end WITHOUT shipping node/Vite into CI — the same in-harness serving-endpoint pattern as `trace_sink`/`egress_proxy`. Closed on teardown. |
| `egress_proxy` | `egress_proxy()` | Starts an in-process SOCKS5 recording proxy on the harness loopback and returns its `"127.0.0.1:PORT"` address, for use as the CLIENT's own proxy (`ALL_PROXY=socks5h://<addr>` via `compose_up_bg(env=...)`). It resolves names itself (socks5h / remote DNS) and tunnels best-effort; every destination it is asked to reach is recorded (before dialing, so an unreachable sentinel still registers). Closed on teardown. Used to prove client-side egress leaves through the client's OWN proxy (`compose-egress-client-proxy.star`). |
| `egress_proxy_hits` | `egress_proxy_hits(addr)` | Returns the list of `"host:port"` destinations the `egress_proxy` at `addr` has been asked to reach — i.e. the destinations the client dialed through its own proxy. |
| `trace_sink` | `trace_sink()` | Starts an in-process HTTP endpoint on the harness loopback that records the W3C `traceparent` header of every request and answers `204` for any path/method; returns its `"127.0.0.1:PORT"` address. Point the real CLI at it as a `--server` to prove distributed-tracing context injection end to end without a deploy backend (the `204` keeps a driving `cornus deploy --delete` backend-independent). Closed on teardown. Used by `observability-trace.star`. |
| `trace_sink_headers` | `trace_sink_headers(addr, name?)` | Returns the list of the named header's values (default `traceparent`; e.g. `name="baggage"`) the `trace_sink` at `addr` recorded — one entry per request (`""` when a request carried none) — so a scenario can assert telemetry-on injects a valid sampled context / W3C baggage and telemetry-off injects nothing. |
| `otlp_collector` | `otlp_collector()` | Starts an in-process OTLP/HTTP trace receiver on the harness loopback and returns its `"127.0.0.1:PORT"` address for a scenario to pass as `OTEL_EXPORTER_OTLP_ENDPOINT` (`http://<addr>`). Unlike `trace_sink` (headers only) it decodes the exported spans, so a served cornus AND a client cornus can both export to it and a scenario can prove real client<->server span parentage. Answers only `/v1/traces`. Closed on teardown. Used by `observability-trace-otlp.star`. |
| `otlp_spans` | `otlp_spans(addr, min?, timeout?, service?)` | Returns all spans the `otlp_collector` at `addr` received, each `{trace_id, span_id, parent_span_id, name, service}`. Polls until `min` spans arrive or `timeout` (default `"15s"`); the optional `service` scopes the `min` count to that `service.name`, so a scenario can wait specifically for the async server span rather than racing the eagerly-flushed client spans. |
| `otlp_logs` | `otlp_logs(addr, min?, timeout?, service?)` | The logs analogue of `otlp_spans`: every log record the `otlp_collector` at `addr` has received, each as `{body, service, severity}`. Polls until `min` records arrive (optionally scoped to one `service.name`) or `timeout` (default `"15s"`). It is the read side of cornus's **re-export** — a scenario asserts that what a workload printed reached not only the local store but the configured upstream backend. Used by `observability-export.star`. |
| `sleep` | `sleep(duration)` | Sleep (context-cancellable). |
| `log` | `log(msg)` | Print a `• msg` progress line. |
| `getenv` | `getenv(name, default?)` | Read an env var (lets a scenario self-skip when an external prerequisite is absent). |

**MCP stdio**

| Builtin | Signature | Returns / effect |
|---------|-----------|------------------|
| `mcp_stdio` | `mcp_stdio(host?, compose_file?, project?, timeout?, env?)` | Launches `cornus web --mcp-stdio`, connects with the official MCP SDK's command transport, and completes initialization. Defaults `host` to the running `serve()` server. Returns `{"handle", "protocol_version", "server_name", "server_version"}`. `timeout` defaults to `"30s"` and bounds initialization plus each later request. Any session left open is closed before server teardown. |
| `mcp_list_tools` | `mcp_list_tools(handle)` | Calls `tools/list` on a live stdio session and returns the sorted tool-name list. |
| `mcp_call` | `mcp_call(handle, tool, arguments?)` | Calls one MCP tool. `arguments` accepts recursively nested JSON-shaped Starlark dicts/lists. Returns `{"is_error", "text", "value"}`: `text` is the concatenated text content and `value` is its decoded JSON when applicable. A tool-domain error sets `is_error=True`; a transport/protocol failure aborts the scenario. |
| `mcp_list_resources` | `mcp_list_resources(handle)` | Calls `resources/list` and returns the sorted resource-URI list. |
| `mcp_read_resource` | `mcp_read_resource(handle, uri)` | Calls `resources/read` and returns the same `{"is_error", "text", "value"}` envelope as `mcp_call`. |
| `mcp_close` | `mcp_close(handle)` | Gracefully closes the MCP client, which closes the child's stdin and waits for `cornus web --mcp-stdio` to exit. A forced or non-zero exit fails the scenario. |

**Build**

| Builtin | Signature | Notes |
|---------|-----------|-------|
| `build` | `build(name, context, dockerfile?, args?, secret?, build_context?, ssh?, builder?, no_cache?, expect_fail?, cache_to?, cache_from?, lazy?, lazy_9p?, no_push?, capture?, fresh_cache?)` | Builds `<registryHost>/<name>:latest`. `builder=True` runs remotely over `ws://.../api/build/attach`; else a local in-process engine (own data dir). `secret={id: path}`, `build_context={name: path}`, `args={k: v}`. `cache_to`/`cache_from` accept a string or list of buildx cache specs. `lazy_9p` kernel-9p-backs lazy contexts to measure bytes pulled (implies `lazy`, needs `Cap9P`). `expect_fail=True` asserts the build must fail. `capture=True` tees the build log. Returns the tag string, or `{"tag", "log"}` when `capture` — including on an `expect_fail` failure (so a scenario can `assert_contains` the `BUILD FAILED:` reason). Non-`no_push` builds are then `PrepareImage`d for the target. |
| `build_upload` | `build_upload(target, context, dockerfile?, no_push?, no_cache?)` | Raw `POST /.cornus/v1/build` tar upload (thinner than `build`; no secrets/ssh/named-contexts/cache/lazy). Returns `{"status", "log"}`. |
| `ssh_agent` | `ssh_agent()` | Starts an ssh-agent with a fresh ed25519 key for a later `build(ssh="default")`. Returns the key fingerprint. |

**Deploy and workload lifecycle**

| Builtin | Signature | Notes |
|---------|-----------|-------|
| `deploy` | `deploy(name, image, ports?, env?, replicas?, restart?, command?, mounts?, privileged?, volumes?, dns?, hub_identity?, hub_export?, hub_import?, docker?, ingress?, agent_forward?, expect_fail?)` | Applies a `DeploySpec` via the client. `ports=["8080:80", "53:53/udp"]`; `mounts=["src:dst[:ro]"]` (host bind); `volumes=["[name@]target[:size[:storageclass]]"]` (anonymous vs named); `dns={name: ip}`; hub: `hub_export=["name=port[/udp][:deliver]"]`, `hub_import=["name=port[/udp][,port...]"]`; `docker="tcp"\|"unix"\|"both"` requests the caretaker Docker-API role (a Docker Engine API endpoint on pod-loopback + injected `DOCKER_HOST`); `ingress=<host>` or `ingress={host, hosts, domain, path, path_type, port, class_name, tls_secret, tls_issuer, enabled}` requests a Kubernetes Ingress fronting the Service (kube only — `hosts` is comma-separated and `@` maps to the apex; the empty dict `{}` enables it with an auto-derived host from the server default domain, which `domain` overrides); `agent_forward=True` requests the kubernetes-only `AgentRelayRole` opt-in (`api.DeploySpec.AgentForward`), needed before `cornus exec --forward-agent` will work against the deployment (kube only — dockerhost/containerdhost gate this on `CORNUS_DOCKER_REMOTE`/`CORNUS_CONTAINERD_REMOTE` instead, an env var rather than a spec field). Returns a status dict; `expect_fail=True` asserts rejection and returns the rejection message string (so a scenario can `assert_contains` on it). |
| `deploy_attach` | `deploy_attach(name, image, local_mount?, mounts?, command?, ports?, env?, replicas?, restart?, timeout?, privileged?)` | Backgrounds a long-lived `cornus deploy --server ws://... --local-mount ...` whose local dirs are served over 9P; blocks until **that session itself** announces the workload up — the `deployed <name>` line the client prints off the server's `Ready` event, after the server applied this spec and awaited every desired instance (default `timeout="180s"`). It deliberately does NOT wait on `Status(name)`: any running container carrying the deployment's label satisfies that, including one a previous failed run left on the same daemon, so the wait used to return before this run's deploy had done anything. Stopped by `attach_stop` or teardown. `local_mount=["./host:/ctr[:ro]"]` becomes `--local-mount` flags, which the CLI APPENDS after the spec file's own mounts; `mounts=["src:dst[:ro]"]` instead writes RAW `api.Mount` entries into the spec FILE, exactly as a hand-written `cornus deploy -f spec.json` would. Only `mounts` can control the ORDER of the mount list, so it is the only way to express a NON-client-local source (a named/bare-name volume) sitting BETWEEN two client-local binds — the sparse `m0, m2` naming `deploy-mounts-sparse-index.star` guards. Client-local sources are served over 9P either way. |
| `attach_stop` | `attach_stop(name)` | SIGINT the `deploy_attach` process so the server tears the workload down and unwinds mounts. |
| `status` | `status(name)` | Current status dict. |
| `wait` | `wait(name, running?, timeout?)` | Poll until `running` (default 1) instances are up (default `timeout="60s"`); returns the status dict or errors on timeout. |
| `start` / `stop` / `restart` | `start(name)` etc. | Lifecycle action via the client. |
| `remove` | `remove(name)` | Delete the deployment. |
| `pod_exec` | `pod_exec(app, cmd)` | **kube only.** `sh -c cmd` in the app container of a deployment's pod; returns stdout. Used to read a mounted file back and confirm a live 9P sidecar mount. |

**Compose / devcontainer / Docker proxy**

| Builtin | Signature | Notes |
|---------|-----------|-------|
| `compose_up` / `compose_ps` / `compose_down` / `compose_build` / `compose_stop` / `compose_start` / `compose_restart` | `compose_<sub>(file, project?, detach?)` | Runs `cornus compose -f <file> [-p <project>] <sub>` with `CORNUS_HOST` pointed at the server. `up` with `detach=True` adds `-d` (backgrounds a mounts daemon for bind-mount services). On the kube target, `up` first pre-builds any `build:` service images (`cornus compose build`) and `kind load`s them into the cluster nodes (`prepareComposeBuildImages`) — an in-cluster pod cannot pull from the host-bound registry; the subsequent `up`'s rebuild is a warm-cache hit on the same tag. Returns combined output. |
| `compose_up_bg` / `compose_up_wait` / `compose_up_stop` | `compose_up_bg(file, project?, conduit?, env?)`; `compose_up_wait(handle, timeout?)`; `compose_up_stop(handle, timeout?)` | `compose_up_bg` backgrounds a FOREGROUND `compose up` (no `-d`) that holds the deploy-attach session (and serves its client-local backings) the way an interactive terminal does, returning a handle. `env={k: v}` sets extra env on that HELD client process — the seam for pointing the client's OWN egress dialer at a proxy (`ALL_PROXY`/`HTTP(S)_PROXY`), used by `compose-egress-client-proxy.star`. `compose_up_wait` waits for it to self-exit after an external `down` removed its workloads; `compose_up_stop` SIGINTs it (Ctrl-C) and asserts the foreground exit tore its workloads down. |
| `devcontainer_up` / `devcontainer_ps` / `devcontainer_down` | `devcontainer_<sub>(dir, project?, detach?)` | Runs `cornus compose --devcontainer <dir> <sub>` (a repo with only a `.devcontainer/devcontainer.json`). Same env/knobs as `compose_*`. |
| `docker_compose` | `docker_compose(*args)` | Runs `docker compose <args...>` against the `dockerd_up()` proxy via `DOCKER_HOST` env (the compose plugin ignores `-H`). Requires `dockerd_up()` first. |
| `devcontainer_cli` | `devcontainer_cli(*args)` | Runs the OFFICIAL `devcontainer` CLI (@devcontainers/cli — the engine VS Code's Dev Containers extension shells out to) against the `dockerd_up()` proxy via `DOCKER_HOST` env. Distinct from `devcontainer_*`, which drive cornus's own translation. Requires `dockerd_up()` first. |
| `cornus` | `cornus(*argv, env?, expect_fail?)` | Runs the `cornus` binary directly with the target's serve env. `env={...}` appends (wins over serve env — e.g. point `CORNUS_CONFIG` at a throwaway client config). `expect_fail=True` asserts a non-zero exit and returns the combined output instead of aborting. |

**HTTP / FTP / registry**

| Builtin | Signature | Notes |
|---------|-----------|-------|
| `registry_roundtrip` | `registry_roundtrip(ref)` | Pushes a random image to `<registryHost>/<ref>` and pulls it back; returns the digest, erroring on mismatch. |
| `http_get` | `http_get(url, retry?, socks5?, allow_error?, insecure?, ca_file?, retry_5xx?, retry_until?, host?)` | GET with a connection-error retry loop (default `retry="15s"`) — smooths the sub-second window where a freshly published port accepts TCP before the workload serves. Returns `{"status", "body"}`. Any HTTP response (even 500) is returned verbatim, not retried away. `socks5="127.0.0.1:PORT"` routes the GET through a client-side SOCKS5 proxy (a conduit in socks5 mode), so the host is resolved by the proxy — used to prove a workload is reachable by name / short alias through the split-tunnel (see `socks5-aliasing.star`). `host="app.example.com"` sends that Host header and TLS SNI while still dialling the address in the URL, which is how a scenario reaches a declared ingress hostname without DNS. `allow_error=True` returns `{"error": str}` instead of aborting when the request never succeeds. `insecure=True` skips TLS verification; `ca_file="bundle.pem"` instead verifies against a specific PEM certificate/CA bundle. The two TLS options are mutually exclusive. **`retry_5xx=True`** additionally retries a 5xx *response* (a reverse proxy answering 502/503 while its upstream comes up is transient in the same way a dial refusal is, but arrives as an HTTP response the plain loop cannot absorb). **`retry_until=<status>`** is the wider form for a transient answer that is not a 5xx at all: a real ingress controller that has not yet ingested a freshly created Ingress routes the host to its **default backend and answers 404**, so `retry_5xx` returns it verbatim and the assertion fails on controller sync latency. Pass the status being waited for and every other status is transient until the deadline. Both stay bounded by `retry` and return the **last** response as-is once it passes, so a route that never appears still fails the assertion with its real status. Use `retry_until` only for a **positive** assertion — retrying a 404 you are asserting makes "not yet" and "never" the same observation. |
| `http` | `http(method, url, body?, headers?)` | Single arbitrary request, **no retry** — for registry wire-protocol edges (HEAD, chunked PUT/PATCH, cross-repo mount, DELETE). Returns `{"status", "body", "headers"}` (headers canonicalized, multi-values joined with `", "`). |
| `ftp_roundtrip` | `ftp_roundtrip(addr, user, password, content, path?, active?, advertise_host?)` | STOR then RETR against a deployed FTP server (minimal in-harness FTP client, no third-party dep). Passive by default (ignores the masqueraded PASV address); `active=True` makes the server dial back to a client listener (`advertise_host` overrides the PORT address). Returns `{"ok", "downloaded", "n", "error"}`. |

**Shell / exec / files / external tools**

| Builtin | Signature | Notes |
|---------|-----------|-------|
| `sh` | `sh(cmd)` | `sh -c cmd`; returns `{"code", "output"}` (output trimmed). |
| `exec_tty` | `exec_tty(argv, input?, rows?, cols?, timeout?, env?)` | Runs `argv` under a **real PTY** for interactive `-it` sessions (native `cornus exec -i -t`, or `docker exec -it` via the proxy), with window-size propagation. `argv[0]` `"cornus"` -> the harness binary, `"docker"` -> PATH. Writes `input`, reads all output to EOF/`timeout` (default `"30s"`), returns `{"output", "code"}`. |
| `write_file` | `write_file(path, content)` | Write a file (creating parent dirs) — e.g. mutate a build-context file between builds to prove cache invalidation. Point it at a temp dir so the committed tree is untouched. |
| `read_file` | `read_file(path, default?)` | Read a file back as a string (verbatim, NOT trimmed — unlike `sh`). If the file is missing and `default` was given, returns `default` instead of erroring, so a scenario can poll for a file a workload writes asynchronously. |
| `temp_dir` | `temp_dir()` | Create a fresh temp dir and return its path (the harness replacement for `sh(cmd="mktemp -d")`). Created 0755 so non-root container processes can traverse it when bind-mounted (e.g. nginx serving a mounted docroot). |
| `remove_all` | `remove_all(path=...)` | Recursively remove a path, the harness replacement for `sh(cmd="rm -rf ...")`. SANDBOXED: it deletes only at or under a directory this scenario created with `temp_dir()`, resolving symlinks first, so a path outside (or a symlink escaping to one) is REFUSED rather than removed. Removing a path that does not exist is a no-op. Use `sh()` for the rare case that must reach outside a scenario temp dir (`deploy-reboot-survival.star` wiping `/run/cornus/*` to simulate a reboot); that is deliberately out of the sandbox. |
| `kubectl` / `docker` / `kind` | `kubectl(*args)` etc. | Run the external tool; returns combined output. On the kube target, `kubectl`/`kind` get `KUBECONFIG` set to the harness cluster. |

**Assertions**

| Builtin | Signature |
|---------|-----------|
| `assert_eq` | `assert_eq(got, want, msg?)` |
| `assert_true` | `assert_true(cond, msg?)` |
| `assert_contains` | `assert_contains(s, sub, msg?)` |
| `fail` | `fail(msg)` — abort the scenario |

### Return-value shapes

Builtins hand Starlark dicts back to scenarios (`pkg/e2e/value.go` does the Go
-> Starlark conversion). The recurring shapes:

- **Status dict** (`deploy`, `deploy_attach`, `status`, `wait`):
  `{"name": str, "image": str, "backend": str, "running": int, "total": int}`
  where `running` counts instances reporting `Running` and `total` is the instance
  count.
- **`http_get`**: `{"status": int, "body": str}`.
- **`http`**: `{"status": int, "body": str, "headers": {str: str}}`.
- **`sh`**: `{"code": int, "output": str}`.
- **`exec_tty`**: `{"output": str, "code": int}`.
- **`mcp_stdio`**: `{"handle": str, "protocol_version": str, "server_name": str, "server_version": str}`.
- **`mcp_call`** / **`mcp_read_resource`**: `{"is_error": bool, "text": str, "value": any}`.
- **`build_upload`** / **`build(capture=True)`**: `{"status": int, "log": str}` /
  `{"tag": str, "log": str}`.
- **`ftp_roundtrip`**: `{"ok": bool, "downloaded": str, "n": int, "error": str}`.

### Scenarios

Scenarios are `.star` files in `e2e/scenarios/`; the default suite is the
`SCENARIOS` list in the `Makefile`. The containerd target runs the smaller
backend-agnostic `SCENARIOS_CONTAINERD` list (`deploy`, `lifecycle`, `exec`,
`compose`) — the rest of the suite is docker-/kube-specific or self-skips; grow
that list as the containerd backend's coverage grows. The `bare` target runs the
identical backend-agnostic subset as `SCENARIOS_BARE` (it reuses the containerd
backend's CNI networking / hosts-file DNS and the same lifecycle contract). A
representative sample:

| Scenario | What it covers |
|----------|----------------|
| `registry.star` | push/pull round-trip through the registry (real OCI client) |
| `deploy.star` | build → push → deploy → wait for replicas → teardown |
| `ftp.star` | FTP server: passive-mode STOR+RETR round-trip over published control + data ports (bidirectional) |
| `ftp-active.star` | FTP server: active-mode STOR+RETR round-trip, the server dialing back to the host client (connect-back does not traverse a published port; uses the docker-bridge gateway) |
| `ftp-usernet.star` | Two FTP workloads (server + client) over a user-network: bidirectional put/get/compare over real pod-to-pod connectivity (kube + Multus only; FTP cannot ride the name-based hub overlay because PASV/PORT embed addresses) |
| `compose.star` | `cornus compose` up / ps / down |
| `mcp-stdio-protocol.star` | real `cornus web --mcp-stdio` process: initialize identity/version negotiation, tools/resources discovery, in-band tool errors without session loss, stdin-close clean shutdown, and the `--publish-in-conduit` conflict |
| `mcp-stdio-tools.star` | MCP stdio against a live two-service Compose project: workload/detail joins, dependency graph, bounded logs, nested-argument one-shot exec with stdout/stderr/nonzero exit code, and allow-listed file reads |
| `provider.star` | Compose provider services (`provider:`): a stub plugin (absolute-path `type`) records each lifecycle call, asserting `up`/`stop`/`down` invoke it with sorted `--key=value` option flags + the service name, and `ps` renders `provider:<type>`. Backend-agnostic (the plugin runs client-side); on kube it also deploys a dependent and reads `printenv` back via `pod_exec` to prove `setenv` output is injected as `<SERVICE>_<VAR>` |
| `lifecycle.star` | deploy → stop / start / restart, asserting running counts |
| `build-mounts.star` | build with bind / secret / cache / ssh mounts, locally and over the remote 9P/WebSocket path |
| `build-edge.star` | build-backend edge cases (local + remote): `.dockerignore` filtering, named-context ignore, symlink transmission, multi-stage, build-arg, custom-Dockerfile ignore precedence, and a negative COPY-of-ignored-file case |
| `caretaker-docker-endpoint.star` | caretaker `docker` role (kube only): deploy a busybox app with `docker="tcp"`, then from inside the pod assert `DOCKER_HOST` is injected and the loopback Docker Engine API endpoint answers `/_ping` and `/version` (identifying itself as the cornus proxy) — proving the endpoint is exposed to the container over the shared pod netns |
| `socks5-ingress.star` | ingress-via-conduit, EMULATE mode (docker + kube): a foreground `compose up --conduit socks5` with `CORNUS_INGRESS_CONDUIT=emulate` deploys an nginx with `x-cornus-ingress` hosts (`app.example.com`, `www.app.example.com`) that bear no `.cornus.internal` suffix, so only the emulated ingress (a client-side reverse proxy registered as a published local name) can route them; `http_get(socks5=proxy)` reaches nginx at each host, and an unregistered `.invalid` host correctly falls through to a failing direct dial |
| `socks5-ingress-tls.star` | ingress-via-conduit, EMULATE **TLS termination** (docker + kube): configures a BYO certificate with no explicit pattern, so its DNS SAN selects the host; `http_get("https://…", socks5=proxy, ca_file=certificate)` proves the configured certificate terminates the verified handshake and proxies to the workload |
| `deploy-ingress.star` | automatic ingress creation (kube only): covers auto-derived and explicit hosts, class/path/TLS issuer defaults, multiple hosts with an `@` apex, and owner-ref cleanup; its native-BYO section drives a real detached CLI deploy through a profile, verifies SAN-derived selection creates a `kubernetes.io/tls` Secret containing the supplied certificate/key, checks the Ingress references it, and proves deletion garbage-collects it. A final section then **fetches through a real controller** — a `traefik/whoami` deployed with `ingress={}` is reached over the installed ingress-nginx (node InternalIP + the controller Service's http nodePort) at its AUTO-DERIVED host, so the derived host / server-default class / backend port are proved to ROUTE and not merely to be spelled. That fetch is gated on the controller being present in the cluster (a `--ignore-not-found` namespace + Service probe), not on the target or on `E2E_INGRESS_NGINX`, because the scenario runs on BOTH kube CI legs: plain kube has no controller and keeps the object assertions only |
| `compose-deps.star` | `compose up SERVICE` **dependency expansion** (backend-agnostic; skips only on `local`): naming one service deploys its TRANSITIVE `depends_on` closure (fixture graph `web -> api -> db`, `web -> cache`, plus an edge-free `solo`, so `db` is only reachable two hops out), `--no-deps` suppresses it and deploys the named service alone, and a dependency-free service grows nothing. Every assertion is a `status()` count of a real workload — the expansion is unit-tested, but what it decides is the SELECTION that crosses the deploy boundary; the announcement line is checked only as a secondary assertion on legs whose deploy result already agrees |
| `ingress-settings-controller.star` | ingress SETTINGS propagation judged by a REAL controller (kube only, needs `E2E_INGRESS_NGINX=1`): where `deploy-ingress.star` reads the generated fields back with `kubectl`, this one dials the installed ingress-nginx directly (node InternalIP + the controller Service's nodePort — no tunnel in the path) and asserts each setting by BEHAVIOUR: an `annotations` rewrite-target actually rewrites the path the backend reports, `path_type: Exact` serves `/exact` but not `/exact/sub`, an explicit `port` wins over a first published port nothing listens on, a `class_name` the controller owns is served while an unmatched one is created-but-ignored, and `tls.secret_name` makes the controller terminate with the BYO certificate (pinned as the client's only trust root, so falling back to nginx's default certificate fails the handshake instead of passing) |

**Negative / unhappy-path scenarios** (the failure surface, one per subsystem):

| Scenario | What it covers |
|----------|----------------|
| `registry-errors.star` | registry failure paths (agnostic): 404 `NAME_UNKNOWN` / 405 `UNSUPPORTED` / 400 `DIGEST_INVALID` (missing digest) / 404 `BLOB_UPLOAD_UNKNOWN` / 404 `MANIFEST_UNKNOWN`, the cross-repo digest-leak guard, and a regression-lock on the manifest-validation gap (an unvalidated manifest body is stored with 201) |
| `registry-auth.star` | registry auth end to end (agnostic): boots an auth-enabled server via `serve(env=...)`, mints real JWTs with `cornus token issue`, and asserts a push is 401 (no cred) / 403 (identity lacks push) / 202 (authorized) |
| `cli-errors.star` | CLI input validation + fail-closed startup (agnostic): port-forward bad mapping, `deploy -f` nameless/malformed spec, `config use-context` unknown context, and `cornus serve` exiting non-zero before binding on a malformed `CORNUS_GC_INTERVAL` / `CORNUS_API_POLICY` |
| `observability-trace.star` | distributed tracing, client -> server (agnostic, no backend): the real `cornus` binary injects a valid sampled W3C `traceparent` AND a `baggage` header carrying `cornus.command` into its server requests when telemetry is enabled (`CORNUS_OTEL`, with `OTEL_*_EXPORTER=none` to stay network-free) and injects nothing when off — asserted through a `trace_sink()` recording endpoint |
| `observability-trace-otlp.star` | distributed tracing, client -> server span PARENTAGE (skips on local — needs a running server + deploy backend): both a served cornus and a client cornus export traces to an `otlp_collector()`, and the scenario asserts a client span (service `cornus-cli`) and a real server span (service `cornus`) share one trace id and the server span is a child (non-empty parent) |
| `observability-store.star` | the built-in observability store end to end: an UNINSTRUMENTED workload's stdout/stderr is recorded with zero configuration, full-text search filters it, and — the headline — the record survives `remove()` of the workload that produced it. Also covers the OTLP receiver rejecting an unparseable export and the Grafana/Loki envelope. Self-skips when the build lacks `-tags imbh` (probed via `/.cornus/v1/obs/status`). Carries the ONE-SHOT known-failing reproducer as a final `E2E_OBS_ONESHOT`-gated section — see below |
| `observability-export.star` | **re-export**: a workload's recorded output reaches BOTH the local store and a real upstream backend (an `otlp_collector()` read back with `otlp_logs`), plus the **gateway** shape — no store at all, forward only — where the receive route still answers and still rejects garbage. The gateway leg needs no `imbh` build |
| `observability-compose-logs.star` | `compose logs --from=runtime\|store`, `--match`, the two refusals (`--follow`+store, `--match`+runtime), and that `--from=store` still answers after `compose down` |
| `observability-observe-cli.star` | the `cornus observe` query surfaces against a real store: logs, `--output json`, SQL over the `logs` table, `status`, empty-but-answering traces, an unresolvable PromQL metric producing a diagnostic rather than an empty series, and the Prometheus/Loki/Tempo datasource envelopes |
| `observability-mcp.star` | the `observe_*` MCP tools and the `cornus://observe/errors` resource over the real `--mcp-stdio` transport, against real recorded output |
| `observability-telemetry-mux.star` | endpoint-optional telemetry (`x-cornus-telemetry` with no endpoint deploys) and that it rides the caretaker connection by default — proven by the workload reaching Running, since `caretaker-check` gates the app on the relay's loopback listener. Needs `CORNUS_TEST_OTEL` + an `otelcol`-tagged sidecar image |
| `observability-metrics.star` | the resource-usage feed against a REAL backend: the recorder samples, the PromQL names the docs promise resolve, EVERY replica is sampled with distinct values, the server's own process metrics reach the store, and the readings reach the re-export upstream. Runs on the host backends and — since 2026-07-28 — on **kube**, where it needs metrics-server (see the two gates below) |
| `deploy-errors.star` | deploy/runtime failures (docker-only): image-pull failure, host-port conflict, privileged rejected by the default-deny host policy, and crash-on-start showing `running==0` (host backends have no synchronous health wait) |
| `build-fail.star` | build failures (build engine): the client `cornus build` path fails local+remote on RUN-false / unresolvable FROM / parse error / missing COPY source; the POST `/.cornus/v1/build` in-band `BUILD FAILED:` trailer (HTTP still 200) via `build_upload`; and pre-stream 400s (missing `?t=`, non-tar body). The `BUILD FAILED:` literal is emitted only by the POST `/.cornus/v1/build` path, so assert it via `build_upload`. |

`observability-metrics.star` on the **kube** target has two gates, and they are
deliberately different in kind:

- **metrics-server** is probed off the CLUSTER (`kubectl get apiservice
  v1beta1.metrics.k8s.io --ignore-not-found`), not off `E2E_METRICS_SERVER` —
  the same shape as `deploy-ingress.star`'s ingress-controller gate, and for the
  same reason: the scenario also runs on the plain kube leg and via
  `make e2e-kube`, where nothing installed it. Absent -> the whole kube leg
  self-skips with a message naming the flag, because
  `(*kubernetes.Backend).SampleMetrics` reads `metrics.k8s.io` and 404s without
  it, so there is nothing to exercise. Present but the APIService not
  `Available` -> **hard failure**, never a fallback to skipping.
- **the observability store** is probed off the SERVER
  (`/.cornus/v1/obs/status`). Absent it is normally a self-skip — but when
  metrics-server IS installed the scenario `fail()`s instead, because a leg that
  went to the trouble of installing metrics-server must not go green having run
  no assertions. That is what forces `E2E_METRICS_SERVER=1` and
  `E2E_BUILD_TAGS=... imbh sable_extern_lib` to be set together.

The kube leg also asserts a DIFFERENT cpu family: metrics-server reports an
instantaneous rate and no pid count, so the recorder emits `container.cpu.usage`
(gauge, PromQL `container_cpu_usage`) instead of `container.cpu.time` (sum, split
by `cpu_mode`) and no `cornus.container.pids`. The scenario parameterizes the
family names on `TARGET` rather than asserting a host spelling the product
deliberately does not produce on kube. Reproduce with:

```sh
make e2e-container E2E_TARGETS=kube E2E_METRICS_SERVER=1 \
  E2E_BUILD_TAGS="netgo osusergo imbh sable_extern_lib" \
  IMAGE=cornus-e2e:imbh \
  E2E_SCENARIOS=e2e/scenarios/observability-metrics.star
```

### The observability family really runs on the kube leg (2026-07-28)

Until this date every containerized leg built `CGO_ENABLED=0`, so the store was a
stub, `/.cornus/v1/obs/status` 404'd, and **8 of the 10 `observability-*.star`
scenarios self-skipped in every leg — docker included — for as long as they had
existed**. They reported green while running nothing. The kube leg now builds
`netgo osusergo otelcol imbh sable_extern_lib` and sets `E2E_METRICS_SERVER=1`
(inseparable: see the two gates above), so all ten execute for real there. The
other legs are unchanged and deliberately so — enabling them is a separate
decision with its own runtime cost.

All ten were RUN on kube before the CI flip rather than after it. What that pass
found, since none of the eight had ever executed anywhere:

- `store`, `export` and `observe-cli` failed on ONE shared defect, and not one in
  the observability layer: their probe workloads used `restart="no"`, which is
  one-shot, which the kubernetes backend realizes as a **Job** — and
  `(*kubernetes.Backend).List` enumerates Deployments only, so the recorder
  (which drives its tails off `List`) never sees the workload and records
  nothing, silently. The `restart="no"` was incidental to all three subjects, so
  they now deploy long-lived workloads; the correct one-shot behaviour is stated
  unweakened in the `E2E_OBS_ONESHOT`-gated section at the end of
  `observability-store.star`, handled exactly like `auth-build-deploy.star` (the
  assertion stays in the tree and stays parse+resolve-checked, but cannot turn
  the leg red). Full detail and the verbatim failures are in TODO.md, "One-shot
  workloads are invisible to the kubernetes backend's `List`". Reproduce with:

  ```sh
  make e2e-image E2E_BUILD_TAGS="netgo osusergo imbh sable_extern_lib" IMAGE=cornus-e2e:imbh
  # raw docker run: the Makefile's e2e-container does not (yet) pass
  # E2E_OBS_ONESHOT through to the container.
  docker run --rm --privileged \
    -e E2E_TARGETS=kube -e E2E_OBS_ONESHOT=1 \
    -e E2E_SCENARIOS=e2e/scenarios/observability-store.star \
    cornus-e2e:imbh
  ```

- `telemetry-mux` PASSED but vacuously. `deploy()`'s `telemetry` kwarg was
  unpacked as a plain Go `string`, so `telemetry=""` — the endpoint-less block
  (`x-cornus-telemetry: {}`), the scenario's headline case — was
  indistinguishable from omitting the kwarg and deployed an ordinary workload.
  `bDeploy` now unpacks it as a `starlark.Value`, so a present-but-empty
  `telemetry` sets `api.TelemetrySpec{Enabled: true}` with no endpoint and the
  server's defaulting path is actually exercised.

- `export`'s GATEWAY leg (forward-only, store nothing) then failed a SECOND time,
  for an unrelated reason: it expressed "no store" by simply not setting
  `CORNUS_OBS`. With the store compiled in, `resolveObsEnabled`
  (`cmd/cornus/serve.go`) follows the BUILD when neither `--obs` nor `--no-obs`
  is given — so the store defaulted ON, and the query route that the leg asserts
  must 404 answered `200` with the PREVIOUS server's records (same per-scenario
  data dir, after `stop_server()`). The leg now sets `CORNUS_OBS: "0"`
  explicitly. Worth remembering generally: on an `imbh` build, omitting
  `CORNUS_OBS` means ON, not OFF.

`dockerd-exit-code.star` is the exit-code half of the same proxy. The code is
threaded `api.DeployStatus.TerminalExitCode` -> `deploywire.Event.ExitCode` ->
`pkg/dockerproxy` wait/inspect and unit-tested at every hop against a fake
attacher — but a fake cannot reach the thing that only exists live: the proxy's
`awaitExit` racing a POLL of real backend statuses against the deploy-attach
session's `Done()`. The scenario drives both arms with a real dockerhost workload
and asserts what `docker wait` actually prints — a real `0` for a container that
exits on its own (the poll arm), and `125` **with an Error** for one torn down
while running (the `Done()` arm), never a fabricated `0` — plus that re-asking
returns the remembered answer rather than re-deriving one. It also reads the raw
`POST /containers/{id}/wait` body over the proxy socket with `curl` (skipped, with
a log line, when `curl` is absent), because the `Error` field is where the
"unknown, not 0" contract is fully visible; the docker CLI collapses it to the
bare code. Docker-only: it needs a backend that actually fills
`api.InstanceStatus.ExitCode` (dockerhost does; barehost/incushost never do).

A NON-ZERO self-exit lives in its own scenario, `dockerd-exit-code-nonzero.star`,
which is a normal `SCENARIOS` member and ungated. It was written as a
KNOWN-FAILING reproducer and is worth keeping in mind as a pattern: the defect was
that `pkg/dockerproxy/translate.go` mapped Docker's default (and explicit) `no`
restart policy to nothing, so `deploy.RestartPolicy` defaulted it to
`unless-stopped` and dockerhost restarted an exited container forever — it never
settled, `TerminalExitCode` never resolved (a restarting container inspects as
`State.Running=true`, so `InstanceStatus.ExitCode` stays nil) and `docker wait`
blocked until the client gave up. The scenario asserted CORRECT behaviour the
whole time and was never weakened to match the defect; while it failed it was kept
out of `SCENARIOS` and self-skipping, and it was promoted only when the translator
was fixed (2026-07-28). A test pinned to a defect turns the defect into a
specification, which is why the assertion came first and the fix second.

Run it like any other docker-target scenario:

```sh
make e2e-container E2E_TARGETS=docker \
  E2E_SCENARIOS=e2e/scenarios/dockerd-exit-code-nonzero.star
```

Others cover the dockerd proxy (`dockerd.star` — including a live
`docker compose up -d --scale web=2` section that asserts both numbered
instances run and converge away on `down`), exec (`exec.star`), user networks
and isolation fabrics (`deploy-network`/`netpolicy`/`proxy`/`cilium`/`multus`), the
hub overlay (`deploy-hub`, `deploy-hub-udp`), volumes, dev containers, build caches,
lazy contexts, connection profiles, and in-cluster port-forward / kube-auth. Note
`build-lazy-9p.star` is intentionally **not** in `SCENARIOS`: it needs the 9p kernel
module and runs only on the `local` target — `make e2e-one TARGET=local
SCENARIO=e2e/scenarios/build-lazy-9p.star`.

`deploy-mounts-sidecar-docker.star` / `deploy-mounts-sidecar-containerd.star`
cover the caretaker-sidecar mount-relay path added to dockerhost/containerd
(`CORNUS_DOCKER_REMOTE` / `CORNUS_CONTAINERD_REMOTE`, see
`pkg/deploy/dockerhost/mounts.go` / `pkg/deploy/containerdhost/mounts_linux.go`)
— the same shared-propagation mechanism `deploy-mounts.star` already covers for
kube, now available on the two host backends too (an explicit opt-in; the
existing single-host kernel-9p fast path stays the default). Both self-skip
without `CORNUS_AGENT_IMAGE` (a prebuilt cornus-embedding image) and need
privileged Docker/containerd (the mount caretaker companion runs with
kernel-mount privilege), so — like `build-lazy-9p.star` — they are kept out of
the default `SCENARIOS`/`SCENARIOS_CONTAINERD` lists (only syntax/resolve-
checked via `EXTRA_CHECK_SCENARIOS`). The containerized runner's docker-target
run builds and exports `CORNUS_AGENT_IMAGE` automatically
(`prepare_docker_agent_image` in `e2e/container/entrypoint.sh`, the same small
`appimage.Dockerfile` the kube caretaker sidecar image uses), so it picks up
`deploy-mounts-sidecar-docker.star` from the default `e2e/scenarios/*.star`
glob with no extra setup. The containerd target has no equivalent wiring yet
— containerd has its own separate image store, so an image built via
`docker build` is not visible to it — run
`deploy-mounts-sidecar-containerd.star` manually once an image is available in
containerd's own store (e.g. via `ctr images import`):
`make e2e-one TARGET=containerd SCENARIO=e2e/scenarios/deploy-mounts-sidecar-containerd.star`.

`deploy-mounts-sparse-index.star` is the **sparse client-local mount-index**
("m2 gap") regression guard. The client names each client-local bind `m<i>` by
its index in the FULL `spec.Mounts` list and skips non-local entries
(`resolveLocalMounts`), so a named/bare-name volume interleaved between two
client-local binds makes the served names sparse (`m0`, `m2`) while
`LocalMount.Index` still points at the original slot; every consumer must key
off that Index, never off its own dense loop position
(`MountManager.Prepare` in `pkg/deploywire/backing.go`,
`spec.Mounts[lm.Index].Target` in `pkg/server/deploy_attach.go`). Only a RAW
spec can produce that shape — Compose and the Docker API proxy both route named
volumes into `spec.Volumes` — so the scenario uses `deploy_attach(mounts=[...])`
(the spec-file mount list) rather than `local_mount`, and asserts the middle
named volume stays an empty Docker volume while each client-local mount serves,
and writes back to, its OWN client dir. docker target only: mounts-only
deploy-attach on dockerhost takes the kernel-9P host-mount path (root + the 9p
module), kubernetes rejects a `spec.Mounts` entry with no 9P backing outright,
and containerd/incus reject client-local mounts on this path. Like
`async-write-docker.star` it therefore stays out of the default `SCENARIOS`
(`EXTRA_CHECK_SCENARIOS` only) and runs in CI through the containerized runner's
docker-target `e2e/scenarios/*.star` glob. Verified live on the docker target in
the containerized runner.

`compose-down-volumes.star` is backend-parametric rather than docker-only: the
`down --volumes` CLI path is one code path (`removeProjectVolumes` -> `DELETE
/.cornus/v1/volume/{name}` -> `deploy.VolumeRemover`) but each backend realizes
and reaps the volume differently, so the scenario probes the concrete artifact
per target — a Docker named volume (`docker volume ls`), a shared un-owned PVC
(`kubectl get pvc -l cornus.volume=...`), or the host directory under
`<CORNUS_DATA>/<backend>/volumes/named/` that containerd and bare share
(`pkg/deploy/internal/hostrun`). The containerd/bare branch pins the server's
data dir with `serve(env={"CORNUS_DATA": temp_dir()})` so it knows where to
look. It also fails loudly if the CLI soft-skips on a backend without
`VolumeRemover` (HTTP 501), which would otherwise make the removal assertion
vacuous.

`compose-env-file.star` covers compose **interpolation sources** end to end —
sibling `.env` discovery, `--env-file` (repeatable, later wins, and REPLACING the
default `.env` rather than merging with it), the process environment winning over
both per key, and a missing `--env-file` failing loudly — judged by what the
DEPLOYED container has in its environment (`cornus exec printf '[%s]' "$VAR"`),
not by rendering the model. Backend-agnostic (public image, no build); skipped
only on the `local` target.

`deploy-remote-portforward-docker.star` exercises the follow-on always-on
**remote companion**: `CORNUS_DOCKER_REMOTE` now creates the companion for
*every* instance (sharing its network namespace), not only when the deploy
also uses `--mount`. It proves `cornus port-forward` reroutes through the
companion instead of the server dialing the Docker bridge directly — the same
`Backend.ForwardPort` bridge `cornus tunnel` also rides, so this covers both
without needing a real external tunnel provider. It self-skips without
`CORNUS_AGENT_IMAGE` and needs privileged Docker (same prerequisites as the
mounts-sidecar pair above), and is likewise kept out of `SCENARIOS` and only
syntax/resolve-checked via `EXTRA_CHECK_SCENARIOS` — but it was also run for
real against a live Docker daemon during development (not just
syntax-checked).

`deploy-remote-exec-agent.star` proves `cornus exec --forward-agent` relays a
real local ssh-agent (started by the harness's `ssh_agent()` builtin) into the
exec'd process via the caretaker's `AgentRelayRole`, asserting `ssh-add -l`
inside the workload reports the SAME key fingerprint the harness's local agent
holds — from ONE scenario body covering **all three** backends: on
docker/containerd it self-skips without `CORNUS_AGENT_IMAGE` exactly like the
pair above (an always-on, backend-wide `CORNUS_DOCKER_REMOTE`/
`CORNUS_CONTAINERD_REMOTE` mode); on kube it needs no such gating — the opt-in
is per-deployment (`DeploySpec.AgentForward`, `deploy()`'s `agent_forward=True`
kwarg), not an env-gated backend mode, and the kube target already loads a
`cornus:e2e` sidecar image — so it runs for real unconditionally there and is
kept IN the default `SCENARIOS` list (like `deploy-dns.star`), while still
self-skipping harmlessly on a bare `make e2e-docker`/`make e2e-containerd` host
run with no `CORNUS_AGENT_IMAGE` set. It also proves the negative on kube: a
deployment applied *without* `agent_forward` rejects `--forward-agent` with a
clear error. This scenario caught a real bug during its original (docker-only)
development: the companion's agent-relay socket was only reachable inside the
app container when the deploy also declared a `--mount` (the scratch volume
that carries it was, at first, only provisioned by `ApplyWithMounts`); the fix
gives the agent-relay socket its own dedicated per-replica scratch volume/dir
(`remotecompanion.AgentScratchDir`), independent of any `--mount` volumes, so
it exists for every remote-mode instance regardless of mounts.

`devcontainer-vscode.star` is also outside `SCENARIOS` (its `devcontainer-cli`
capability would fail-fast every run without a global npm install): it drives the
REAL VS Code devcontainer toolchain — `@devcontainers/cli`, the engine the VS Code
Dev Containers extension shells out to — with `DOCKER_HOST` pointed at the
`cornus daemon docker` proxy, asserting postCreateCommand effects, bidirectional
workspace bind-mount visibility, and `devcontainer exec` exit codes. Docker target
only, and it self-skips unless root + 9p (the dockerhost side kernel-9p-mounts the
caller-local workspace bind). The containerized runner bakes the CLI in and globs
all scenarios, so CI executes it; locally `npm install -g @devcontainers/cli` and
`make e2e-one TARGET=docker SCENARIO=e2e/scenarios/devcontainer-vscode.star`
(as root). This is distinct from `devcontainer.star`, which covers cornus's own
`cornus compose --devcontainer` translation.

`devcontainer.star` (kube-only, in `SCENARIOS`) also carries the **`runArgs`
end-to-end proof**. `pkg/devcontainer/runargs.go` maps ~35 docker-run flags onto
the synthesized compose service, and `TestRunArgsMapped` covers that mapping — but
a compose-plan test can only show what cornus WRITES DOWN, never that the backend
honored it. So the fixture
(`e2e/scenarios/devcontainer/.devcontainer/devcontainer.json`) carries
`--cap-add SYS_PTRACE`, `--cap-drop MKNOD`, `--shm-size 256m` and
`--hostname devbox`, and the scenario reads each back from INSIDE the running
container with `pod_exec`: `CapEff` in `/proc/self/status` (bit 19 set, bit 27
clear — one added and one dropped capability, so a container that was simply
given everything fails too), a `stat -f` of `/dev/shm` (exactly 256 MiB, not the
64 MiB pod default), and `/proc/sys/kernel/hostname`. Every one of those four
flags was checked to survive the KUBERNETES translation specifically
(`capabilities.add/drop`, an `emptyDir{medium: Memory, sizeLimit}` at `/dev/shm`,
`podSpec.hostname`). Flags that are parsed but **docker-only** are deliberately
kept out of the fixture rather than asserted on one backend: `--ulimit` and
`--device` are dropped by the kubernetes backend with a warning
(`warnUnsupportedResourceKeys`), `--init` has no kubernetes equivalent,
`--security-opt seccomp=…`/`apparmor=…` map to nothing (only
`no-new-privileges` and `label=k:v` do), and `--pids-limit` is not recognized by
`runArgs` at all.

`web.star` is outside the default `SCENARIOS` list but IS integrated with the
real stack: it stands up `cornus web` against a real deployed 2-service compose
project (`web-compose.yaml`, public images + a named volume, so it is
backend-agnostic and needs no build engine) and asserts its `/.cornus/web/*` BFF
reflects the live workloads, the `depends_on` dependency graph edge, and the
mount — then exercises the detached-frontend reverse-proxy mode
(`web(frontend=frontend_stub())`), asserting `/` proxies to the stand-in dev
server while the BFF stays served at the same origin. A final leg covers the third
serving mode, `cornus web --publish-in-conduit` (`web(publish=True, conduit=…)`):
the UI binds no port at all, is hosted inside the background client agent, and is
published in the shared SOCKS5 conduit, so the only way in is by name through the
proxy (`http_get(url="http://cornus.internal/.cornus/web/config", socks5=…)`) —
and the same BFF still joins the live workloads from there. It publishes **two**
UIs on one shared proxy on purpose: the headline assertion is that a name is
WITHDRAWN when its client exits (`web_stop`), and with a single tenant "the name
stopped resolving" would be indistinguishable from "the proxy went away with its
last tenant" — the survivor, still answering afterwards, makes the difference
observable. The **containerized runner
builds cornus with the web UI embedded** (a node stage in
`e2e/container/Dockerfile`, mirroring the release Dockerfile), and the runner's
default `e2e/scenarios/*.star` glob includes `web.star`, so it runs on the
`docker` and `kube` targets in CI (`.github/workflows/e2e.yml`) against the real
SPA — the `GET /` assertion checks the actual app HTML (root node + hashed asset
ref) when the UI is embedded, and tolerates the 503 "run make web" notice for
node-less local builds. Run it directly with `make e2e-web`; it is
parse+resolve-checked in `make e2e-check` via `EXTRA_CHECK_SCENARIOS`. The
frontend itself also has jsdom render tests (`web/ npm test`, against a mocked
BFF) — separate from this cross-process E2E.

`web-fs.star` is the companion covering the BFF's **file explorer** (`/.cornus/web/fs*`),
which `web.star` does not touch. It exists because the webbff unit suite drives the
explorer against a fake `containerFS`: that fake can prove where a request was SENT (one
whose every method calls `t.Fatal` is how "no byte reached the daemon" is asserted) but
never that the answer is right. Only a real backend shows that a file streamed out of a
container comes back byte for byte, that a refusal the BFF issues is the one the kernel
would have issued, and that the directory a container path was redirected onto is the
directory the container is actually reading.

Its **two arms are complementary, and each asserts the other's negative**:

- **docker / containerd / bare / incus — the relay arm.** The archive primitives work
  here, so bytes move through the BFF: a file streamed OUT of the container, an 11 MB
  file relayed in and back out with matching checksums (11 MB because the old cap was
  10 MB, so that file is precisely the one that could not move at all), an upload into a
  container path, a move whose source really goes, and the H6 refusal — a file write over
  a same-named DIRECTORY must be refused and the tree must survive, which is worth
  checking per backend because `NoOverwriteDirNonDir` is best-effort (barehost's gVisor
  tar-exec path and incus ignore it, so the BFF's own `StatPath` pre-check is what holds).
  Preflight must report `route: "relay"` — the honest answer where nothing is bind-backed.
- **kube — the redirect arm.** The kubernetes backend answers "cp/archive not supported
  … use kubectl cp" for `StatPath`/`CopyFrom`/`CopyTo`, so the relay CANNOT work there,
  and the client-local bind redirect is not an optimization but the only reason the
  explorer is usable at all. The arm writes through the BFF and reads it back with
  `pod_exec` (and the converse), so a redirect onto the wrong host directory — invisible
  to any fatal-fake assertion — fails on the first `cat`. It also asserts `route: "here"`
  (which depends on the `api.Origin` host/directory gate, only meaningful in production
  shape), that a `:ro` bind is browsable but 403 to write **and that the kernel refuses
  the container too** (agreement is the contract; each side alone is individually
  defensible), and that a bind-backed path stays browsable with the workload stopped.

  The kube arm additionally covers the **caretaker filesystem operator**, which only this
  backend truly needs: two named volumes ride the same service, and the arm asserts a
  volume-backed path is readable and writable at all (impossible without the operator —
  the archive trio is unsupported), that a volume-to-volume copy runs *inside the pod*,
  and that a move within one volume is a rename whose source really goes. Preflight must
  report `route: "server"` with `native: true`; that assertion is the only thing that can
  catch the planner silently degrading, because a wrong route still copies, just the slow
  way. It also probes the **server's own** `POST /.cornus/v1/deploy/{name}/fsop` before the
  BFF's: the BFF collapses every way this can fail into one status, so without it a
  failure says nothing about which link went — no operator on the backend, no caretaker
  registered for the pod, or no root covering the path.

Why the arms are split by target rather than merged: client-local bind mounts need root
to kernel-9p-mount on the non-kube targets, which is why every bind scenario in this
suite (`deploy-mounts.star`, `compose-mounts.star`) is kube-only.

**The kube arm needs a current `cornus:e2e` in the cluster, and nothing checks that for
you.** `KubeTarget.Setup` creates the kind cluster but does NOT build or load the sidecar
image — only the containerized runner's `prepare_kube` does. So a direct `make e2e-kube`
against a pre-existing cluster silently tests whatever binary was loaded into it last,
and a caretaker-side change appears to fail in the *server*. Reload it explicitly when
running against a kept cluster:

```sh
ctx=$(mktemp -d)
CGO_ENABLED=0 go build -tags "netgo osusergo" -o "$ctx/cornus" ./cmd/cornus
cp e2e/container/appimage.Dockerfile "$ctx/Dockerfile"
docker build -t cornus:e2e "$ctx" && kind load docker-image cornus:e2e --name cornus-e2e
```

The scenario is validated by neutralization, not just by passing: restoring the
close-twice defect in `containerStream.Close` makes it fail with
`scenario timed out … Post ".../fs/copy?source=virtual&path=webfs-app/work/seed.txt":
context deadline exceeded` — the request that wedges, named. Note the failure mode is a
`--scenario-timeout` expiry (default 10m) rather than an assertion, because a hung
transfer has no answer to assert on; set `CORNUS_E2E_SCENARIO_TIMEOUT` lower when
deliberately reproducing it. Run it with `make e2e-web-fs`
(`TARGET=kube` for the other arm); parse-checked in `make e2e-check` via
`EXTRA_CHECK_SCENARIOS`.

`auth-build-deploy.star` is the **authenticated dataplane specification**. It
boots an auth-enabled server the same way `registry-auth.star` does
(`serve(env={"CORNUS_JWT_HS256_SECRET": ..., "CORNUS_API_POLICY": ...})` plus
`cornus token issue`) and asserts the two INTERNAL registry paths nothing else
covers: a build must push into the closed registry, and a deploy must pull from
it. Both now PASS — the server-side build's exporter carries an internal
`registry:push` credential minted from the installation secret, and the deploy
backend pulls with a short-lived internal credential.

It is in `SCENARIOS` and runs on the ordinary docker leg. Reproduce with:

```sh
make e2e-container E2E_TARGETS=docker E2E_SCENARIOS=e2e/scenarios/auth-build-deploy.star
```

> **Corrected 2026-08-01.** This section previously described the scenario as the
> suite's one known-failing reproducer, kept out of `SCENARIOS` and self-skipping
> unless `E2E_AUTH_INTERNAL=1`. Every part of that was stale: the defects were
> fixed by the S4 dataplane work, the scenario IS in `SCENARIOS` (and is not in
> `EXTRA_CHECK_SCENARIOS`), and **nothing anywhere read `E2E_AUTH_INTERNAL`** —
> the Makefile forwarded it into the container and no scenario consulted it, so
> the documented reproduce command was inert. Found by running it: the flag
> changed nothing and the scenario passed either way. The dead knob has been
> removed from the Makefile.

```sh
make e2e-check            # syntax-check (parse + resolve) all scenarios (no Docker/kind needed)
make e2e-docker           # run all scenarios against the local Docker host
make e2e-containerd       # run the backend-agnostic subset (SCENARIOS_CONTAINERD) against
                          # the host's containerd daemon (root + CNI plugins required)
make e2e-bare             # run the backend-agnostic subset (SCENARIOS_BARE) against the
                          # daemonless bare backend driving runc directly (root + an OCI
                          # runtime + CNI plugins required; BARE_RUNTIME=crun to override)
make e2e-kube             # run all against a kind cluster (auto create/destroy; KEEP=1 to keep)
make e2e-one TARGET=kube SCENARIO=e2e/scenarios/deploy.star
make e2e-web              # opt-in web-UI scenario (web.star) against the docker target
                          # (override with TARGET=kube/containerd); not in the default suite
make e2e-web-fs           # opt-in web file-explorer scenario (web-fs.star): the relay arm on
                          # docker/containerd/bare/incus, the bind-redirect arm on TARGET=kube
# build-only scenarios need no Docker/kind; the "local" target just needs the
# build engine (root or a rootless stack) and ssh-keygen/ssh-agent/ssh-add:
cornus-e2e --target local e2e/scenarios/build-mounts.star
```

### Benchmark suite (opt-in)

A separate, opt-in **benchmark suite** lives under `e2e/benchmarks/*.star` (NOT
`e2e/scenarios/`, so the default suite glob never runs it). Benchmarks drive the
same real stack as scenarios — `serve()`, `deploy_attach`, `exec_tty`, `build` —
but instead of asserting behavior they TIME it and report throughput/latency.

Three timing builtins (registered like any other in `predeclared()`):

| Builtin | Signature | Returns / effect |
|---------|-----------|------------------|
| `now` | `now()` | Monotonic seconds since process start, as a float. Diff two readings to time a block of work: `t0 = now(); ...; dt = now() - t0`. |
| `benchmark` | `benchmark(name, fn, iters?, warmup?)` | Times the Starlark callable `fn` over `iters` runs (default 1) after `warmup` untimed runs (default 0); prints a `▸ BENCH` summary and returns `{name, iters, warmup, avg_s, min_s, max_s, total_s}`. Use for repeatable Starlark-driven work; for one-shot throughput prefer `now()`+`bench_record` so a fixed `exec_tty`/process cost is not folded into the number. |
| `bench_record` | `bench_record(name, value, unit?, extra?)` | Records a pre-measured metric (`unit` default `"s"`); `extra={k: num/str}` carries extra fields (e.g. `{"MB": 64, "MBps": 92.4}`). Prints a `▸ BENCH` line and returns the record dict. |

Output is a human-readable `▸ BENCH <name>: ...` line per record. If
`CORNUS_E2E_BENCH_JSON` names a file, every record is ALSO appended to it as one
JSON object per line (JSONL) — each stamped with `kind`, `target`, and a
timestamp — so a run's numbers survive across the per-scenario `Harness`
instances and can be processed by a machine.

Benchmarks are opt-in on two axes: they live outside the suite glob AND each
self-skips unless `CORNUS_E2E_BENCH` is set. Run them with `make e2e-bench`
(which sets it); override the target with `TARGET=` and collect JSONL with
`BENCH_JSON=<path>`:

```sh
make e2e-bench                                  # docker target (default)
make e2e-bench BENCH_JSON=/tmp/bench.jsonl      # also write JSONL
make e2e-bench TARGET=kube                      # a different target
```

They are parse+resolve-checked by `make e2e-check` (via `SCENARIOS_BENCH`) and by
`go test ./pkg/e2e/` (`TestBenchmarkScenariosParse`); the timing builtins
themselves are unit-tested (`TestBenchBuiltins`). Current benchmarks:

| Benchmark | What it measures |
|-----------|------------------|
| `bench-mount-write.star` | Async writable mount I/O (docker host-mount path, `cache=mmap` block proxy): sequential write throughput (MB/s) and a container-local baseline (the 9P + block-proxy overhead ratio), sequential read-back, and small-op (WAL-like) `fsync` latency (ops/s, ms/op). |

### Dependencies and preflight

Each target and scenario needs a specific slice of the environment. The runner
**preflights** these before provisioning anything: it probes the tools and
privileges the selected target + scenarios require and fails fast with a legible
report instead of crashing mid-scenario. Run it standalone with `--preflight`, or
rely on the automatic gate before every run (`--skip-preflight` to bypass):

```sh
cornus-e2e --preflight --target kube e2e/scenarios/*.star
```

The required capabilities are the union of what the **target** inherently needs and
what each **scenario** needs. Scenario needs are inferred by a conservative **token
scan** of the `.star` source (deliberately not execution, so preflight can run before
any target exists): `build(` or `build_upload(` implies the build engine;
`ssh_agent(` implies the ssh tools; `lazy_9p` implies 9p; `devcontainer_cli(`
implies the official devcontainer CLI; a `compose_up(`/`compose_build(`
whose referenced compose file has a `build:` section also implies the build engine.

| Capability | What it is | Required by |
|------------|-----------|-------------|
| `docker` | docker CLI + a reachable daemon | docker target; kube target (inspects the kind network) |
| `kind` | the `kind` binary | kube target (create/delete cluster) |
| `kubectl` | the `kubectl` binary | kube target; `pod_exec`/`kubectl` builtins |
| `build-engine` | root, or a rootless user-namespace stack (runc + overlayfs) | any scenario calling `build()`/`build_upload()`, or driving a compose `build:` |
| `ssh-tools` | ssh-keygen / ssh-agent / ssh-add | scenarios calling `ssh_agent()` — `build-mounts` |
| `9p` | 9p filesystem support in the kernel | kube target (mount sidecars); `lazy_9p` build scenarios |
| `devcontainer-cli` | the official `devcontainer` binary (`npm install -g @devcontainers/cli`) | scenarios calling `devcontainer_cli()` — `devcontainer-vscode` |
| `containerd` | a reachable containerd daemon (probed via the Go client on `CORNUS_CONTAINERD_ADDRESS` or the stock socket; access usually needs root) | containerd target |
| `oci-runtime` | a runc-compatible OCI runtime binary (runc/crun/youki, `CORNUS_BARE_RUNTIME`) on PATH plus root (overlayfs/netns/CNI) | bare target |

The registry-only scenario (`registry.star`) needs none beyond its target;
`compose.star` and `lifecycle.star` use public images and need no build engine.

### Containerized runner (all-in-one image)

`e2e/container/Dockerfile` builds a single self-contained image that bundles
everything the full suite needs — an in-container dockerd (Docker-in-Docker), the
in-process build engine's `runc`/overlayfs/user-namespace deps, `kind` + `kubectl`,
the ssh tooling, and the Cornus binaries + scenarios. It runs the whole suite
(including the privileged build path and the kind/Kubernetes path) from a generic
CI or self-hosted host with only a privileged docker run:

```sh
make e2e-image                              # build cornus-e2e:latest
make e2e-container                          # run the docker-target suite (privileged)
make e2e-container E2E_TARGETS="docker kube"  # also spin up an in-container kind cluster
make e2e-container E2E_TARGETS="containerd"  # run the containerd subset against an
                                              # in-container standalone containerd
make e2e-container E2E_TARGETS="bare"        # run the bare subset against the daemonless
                                              # runc backend (reuses the staged runc + CNI plugins)
make e2e-bare-container                       # shortcut for the line above — run the bare target
                                              # on any Docker host (no host root/runc/CNI needed;
                                              # E2E_SCENARIOS=... overrides the scenario list)
make e2e-incus-container                      # the incus counterpart: runs the incus subset against
                                              # the incusd the image itself ships (Incus 7.2 from
                                              # Zabbly), started + initialized by the entrypoint
```

Two knobs matter when a run is meant to PROVE a backend works rather than to
sweep the whole suite:

| Env | Effect |
| --- | --- |
| `E2E_STRICT=1` | Turns every "this target cannot run here" self-skip inside `entrypoint.sh` into a hard failure: an incusd older than 6.3 (no OCI application containers) and a bare companion agent image that could not be built (which would silently self-skip `deploy-egress-bare.star` + `deploy-mounts-sidecar-bare.star`). Default `0`, which is what the full-suite runs want. |
| `E2E_PREFLIGHT_ONLY=1` | Does the full per-target setup (starts and initializes the daemon) and then runs only `cornus-e2e --preflight`, exiting non-zero when a required capability is missing. The cheap "can this host run this target at all?" gate. |
| `E2E_METRICS_SERVER=1` | Installs metrics-server into the kind cluster (kube target), from the manifest VENDORED + checksummed under `e2e/container/metrics-server/`. This is not an alternative path through a feature — it is the ONLY metric source the kubernetes deploy backend has, so without it `observability-metrics.star` exercises nothing on kube. See below. |
| `E2E_BUILD_TAGS="netgo osusergo imbh sable_extern_lib"` | Builds the runner's `cornus` with the built-in observability store. **The default image has no store at all**: `imbh` is cgo-only, and the image's build stage is `CGO_ENABLED=0` unless the tag is present, so every `observability-*` scenario that probes `/.cornus/v1/obs/status` self-skips in a default containerized run. Passing the tag switches that stage to a cgo build and resolves `CGO_LDFLAGS` via `imbhgo-fetch` (the same archive `make imbh-lib` uses). The result is glibc-dynamic rather than fully static, which is fine for both places it lands (the runner image and the `cornus:e2e` sidecar image are the same `debian:bookworm-slim`), and is why it is opt-in. The CI kube legs pass it (see "In CI" below); every other leg still runs a store-less binary, so the observability scenarios continue to self-skip there. |
| `E2E_OBS_ONESHOT=1` | Un-skips the known-failing one-shot section of `observability-store.star`: a `restart="no"` workload's output is never recorded on kube, because the backend realizes it as a Job and `(*kubernetes.Backend).List` lists Deployments only. Off by default so the kube leg's green means something; see the 2026-07-28 section above. |

Both exist for the dedicated `bare` / `incus` CI legs, where a missing daemon
must be a red run rather than a green one that exercised nothing. Reproduce a
leg exactly with, e.g., `make e2e-incus-container E2E_STRICT=1`.

The bare target reuses the existing all-in-one image rather than a dedicated one: the
image already stages `runc` (for the build engine) and the CNI reference plugins (for
the containerd target), which is everything the daemonless backend needs. So running
it "on a Docker host" is just a privileged `docker run` of `cornus-e2e:latest` with
`E2E_TARGETS=bare` — no host-level root, runc, or CNI install required.

The container **must** be privileged: the build engine mounts overlayfs and creates
namespaces, and kind + the 9p mount sidecars need it too. Select targets with
`E2E_TARGETS` (default `docker`); the entrypoint starts dockerd, runs preflight, and
for the kube target pre-creates the kind cluster and loads the `cornus:e2e` app
image the mount/sidecar scenarios reference. For the containerd target it starts the
dind base image's standalone `containerd` binary on the stock socket, points
`CORNUS_CNI_BIN_DIR` at the CNI reference plugins staged into `/opt/cornus/cni` at
image build time, and runs the backend-agnostic scenario subset (unless
`E2E_SCENARIOS` overrides). For the bare target it needs no daemon — it points
`CORNUS_CNI_BIN_DIR` at the same staged plugins, selects the native snapshotter when
the data dir sits on the outer container's overlayfs (the same overlay-on-overlay
guard the containerd branch uses), and drives the staged `runc` directly. See
`e2e/container/entrypoint.sh`.

**In CI:** `.github/workflows/e2e.yml` runs this container runner on GitHub Actions
on pushes to `main` and via manual `workflow_dispatch`. The matrix has six legs,
all `fail-fast: false` so one flake cannot mask another leg's real regression:
`docker`, `kube`, `containerd`, `bare`, `incus`, and `kube-ingress` (a second kube
run with a real ingress controller, narrowed to the ingress scenarios). Every leg
is one privileged `docker run` of the same image — nothing is installed on the
runner host — so the `incus` leg needs no host Incus: the image ships its own
incusd. The `bare` and `incus` legs additionally set `E2E_STRICT=1` and run a
preceding `E2E_PREFLIGHT_ONLY=1` gate step, because a leg that exists for exactly
one backend must fail, not green-skip, when that backend's daemon/runtime is
missing. It builds the runner image with a `type=gha` layer cache and
executes it privileged — the same path as `make e2e-container`.

The image is built in TWO variants, keyed on the `matrix.target`: the two kube
legs get `netgo osusergo otelcol imbh sable_extern_lib` (the embedded Collector
plus the observability store; ~2.06 GB) and every other leg gets
`netgo osusergo` (~1.90 GB). Keying on the target rather than the leg is
deliberate — `kube-ingress` does not need the store, but a third variant would
cost more build time (its own cache entry) than carrying the store costs it. The
kube LEG additionally sets `E2E_METRICS_SERVER=1`, which must never be set
without the `imbh` tag: `observability-metrics.star` hard-fails on that
combination by design.

PRs still get only
the lightweight `e2e-check` syntax gate in `ci.yml`; full execution is scoped to
merges and manual dispatch because it is heavy and must run privileged.

### Extending the harness

- **New scenario:** add a `.star` file under `e2e/scenarios/`, add it to the
  `SCENARIOS` list in the `Makefile` (unless it is target-specific like
  `build-lazy-9p.star`), and `make e2e-check` it. Reference paths relative to the
  repo root.
- **New builtin:** register it in `Harness.predeclared()` (`pkg/e2e/harness.go`) AND
  add its name to `predeclaredNames()`. The `TestPredeclaredNamesInSync` test
  enforces that the two stay in sync — `--check` resolves against `predeclaredNames`,
  so a builtin missing from that set would make every scenario using it fail to
  resolve. Prefer returning a dict (via `anyDict`) for structured results so
  scenarios can assert on fields.
- **New capability/preflight probe:** add a `Capability` const + `capInfo`, wire it
  into `targetNeeds`/`scenarioNeeds` in `pkg/e2e/preflight.go`.
