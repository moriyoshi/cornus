# E2E Harness and Coverage

## Summary

Cornus's end-to-end test system is a Starlark-driven harness (`cmd/cornus-e2e`, `pkg/e2e`) that runs `.star` scenarios against pluggable targets (host Docker, a kind Kubernetes cluster, or a local privileged environment), plus an all-in-one Docker-in-Docker runner image that executes the full suite — including the privileged build engine and the kind path — from a single `docker run --privileged`. The suite has been validated green across both targets repeatedly (32-scenario kube run; 36-scenario dual-target run after the `pkg/` restructure), and the baseline runs themselves surfaced and fixed several real Cornus bugs (locked-data-dir hang, missing privileged-deploy support, async-delete flakes).

## Key Facts

- Harness code: `pkg/e2e` (formerly `internal/e2e`); runner binary: `cmd/cornus-e2e`; scenarios: `e2e/scenarios/*.star` (fixtures in sibling dirs).
- Run locally: `make e2e-docker` (host Docker), `make e2e-kube` (kind cluster, auto create/destroy; `KEEP=1` to keep), `make e2e-containerd` (bare containerd host; `--target containerd`), `make e2e-one TARGET=<docker|kube|local|containerd> SCENARIO=e2e/scenarios/foo.star`, `make e2e-check` (syntax+resolve check, no Docker needed).
- `ContainerdTarget` (`--target containerd`, gated on the `CapContainerd` preflight capability) drives the containerd deploy backend; its ServeEnv also sets `CORNUS_BUILD_WORKER=containerd` so server-side builds exercise the containerd build worker. containerd is also in the CI e2e matrix.
- Cloud-blob registry scenarios: `make e2e-cloudblob` runs `registry-gcs.star`/`registry-azblob.star` against fake-gcs-server/Azurite and handles the build-tag gotcha — the SERVED cornus binary needs `-tags cloudblob`, not the runner.
- Containerized full suite: `make e2e-image` builds `cornus-e2e:latest` (override with `IMAGE=`), then `make e2e-container E2E_TARGETS="docker kube"` runs it with `--privileged`. Entrypoint env: `E2E_TARGETS` (space-separated `docker`/`kube`/`local`, default `docker`), `E2E_SCENARIOS` (explicit paths/glob; default all scenarios), `E2E_STORAGE` (registry backend for `serve()`, default `mem://`), `E2E_CLUSTER`, `E2E_MULTUS=1` (install Multus in kind and run the real-NAD path of `deploy-multus`), plus the matrix gates `E2E_MULTUS_IPVLAN=1`/`E2E_MULTUS_MACVLAN=1`/`E2E_MULTUS_DETACHED=1` (passed through by `make e2e-container`).
- The runner image (`e2e/container/Dockerfile`) is `docker:27-dind` + `runc`, `shadow-uidmap`, `openssh-*`, `util-linux`, arch-detected `kind`+`kubectl`, `nodejs`/`npm` + a pinned `@devcontainers/cli` (ARG `DEVCONTAINERS_CLI_VERSION`, e.g. 0.80.0, with a version smoke), static Cornus binaries, and the scenarios. `entrypoint.sh` starts an in-container dockerd, runs preflight, then the harness per target; for kube it pre-creates the kind cluster, builds and `kind load`s the `cornus:e2e` app/sidecar image (`appimage.Dockerfile`), and hands the cluster to the harness with `--keep`.
- On the dev box the user is in the `docker` group (id 988) but long-lived shells predate the membership — wrap docker commands in `sg docker -c '...'`.
- `--preflight` reports capability probes and exits; every run auto-gates on preflight (`--skip-preflight` bypasses). Capabilities: docker, kind, kubectl, ssh-tools, build-engine (root or rootless userns), 9p, containerd (`CapContainerd`), devcontainer-cli (`CapDevcontainerCLI`, probed as a `devcontainer` binary, flagged by a `devcontainer_cli(` token scan — the cornus-native `devcontainer_up(` does NOT flag it).
- Any `build()` scenario needs the build engine (root or a rootless user-namespace stack); the build integration path cannot run unprivileged, hence the dind runner or a privileged container.
- `build-lazy-9p.star` and `devcontainer-vscode.star` are deliberately NOT in the Makefile `SCENARIOS` list — their preflight capabilities (Cap9P, CapDevcontainerCLI) are all-or-nothing fail-fasts that would abort `make e2e-docker` on hosts without the 9p kernel module / a global npm `@devcontainers/cli` install. The Makefile `EXTRA_CHECK_SCENARIOS` list appends both to `make e2e-check` only, so they stay resolve-checked. Run them explicitly (`make e2e-one TARGET=local SCENARIO=e2e/scenarios/build-lazy-9p.star`) or via the container's full glob.
- 9p on hosts without the module and without sudo: a privileged helper container can load it into the shared kernel — `docker run --rm --privileged -v /lib/modules:/lib/modules:ro alpine:3.20 sh -c 'apk add -q kmod && modprobe 9p 9pnet'` — after which `make e2e-container` runs the full docker leg.
- Hub multi-replica validation is separate from the Starlark suite (which runs a single server): `make e2e-multireplica` (Redis store, shell script) and `make e2e-multireplica-kube` (Kubernetes-native store, inside the e2e image).
- Canonical human+agent harness reference is `.agents/docs/TESTING.md` (extracted from README; a full builtin/signature reference, target/serve-env tables, preflight model, containerized-runner docs). This LTM doc is the durable findings + scenario/coverage history; TESTING.md is the how-to-use surface. The containerized runner is also wired into GitHub Actions (`.github/workflows/e2e.yml`) — see `ci-github-actions.md`.
- Adding a builtin: register it in BOTH `predeclared()` and `predeclaredNames()` in `pkg/e2e/harness.go` (`TestPredeclaredNamesInSync` enforces equality). A builtin in `predeclared()` but missing from `predeclaredNames()` makes EVERY scenario using it fail to resolve under `--check` (not merely skip a check), since `--check` resolves against `predeclaredNames`.

## Details

### Harness design

Each scenario is a `.star` file executed by `go.starlark.net` with Go-backed builtins. Core set: `serve`, `build`, `deploy`, `status`, `wait`, `start`/`stop`/`restart`, `remove`, `compose_up`/`compose_ps`/`compose_down`, `registry_roundtrip`, `http_get`, `sh`, `kubectl`/`docker`/`kind`, `assert_*`, `log`, `sleep`. Later additions:

- `build()` kwargs: `expect_fail=True` (asserts a build MUST fail — enables negative tests; with `capture=True` it now RETURNS `{"tag","log"}` from the failed build instead of short-circuiting to `None`, so a scenario can inspect the failed build's log), `cache_to`/`cache_from` (string or list -> `--cache-to`/`--cache-from`), `lazy`, `no_push`, `capture` (returns `{tag, log}` with combined build output for progress-marker asserts), `fresh_cache` (isolated per-build engine data dir so a `--cache-from` hit can only come from the registry).
- `deploy()` kwargs: `command` (-> `spec.Command`), `entrypoint` (-> `spec.Entrypoint`), `mounts` (list of `src:dst[:ro]` -> host bind mounts), `privileged=True` (-> `spec.Privileged`), `expect_fail=True` (asserts the deploy MUST be rejected and RETURNS the rejection message string — see below). `deploy_attach` also takes `entrypoint`.
- `http_get(retry=...)`: retries transient connection-level errors up to a window (default 15s); once any HTTP response arrives it is returned verbatim, so a real 500 is never retried away.
- Polling helpers: `wait_file`, `wait_logs`, `wait_gone` (poll for a deployment to disappear — required on Kubernetes, where deletion is asynchronous).
- `stop_server()`: stops the server process without tearing down data dirs, so a scenario can restart against the same `--storage` dir (persistence tests).
- `cornus(*args)`: runs the Cornus binary with the target's serve env — exercises the real CLI surface (push/deploy/health/version) instead of the client library.
- `compose_build`/`compose_stop`/`compose_start`/`compose_restart`.
- `dockerd_up()`: launches `cornus daemon docker` against the running server, returns its `unix://` DOCKER_HOST; killed before the server on teardown (it holds deploy-attach sessions). It is a subcommand of the Cornus binary itself (`cornus daemon docker`).
- `build_upload(target, context, dockerfile?, no_push?, no_cache?)`: walks + tars a context dir and POSTs it to `POST /.cornus/v1/build?t=...`, returning `{status, log}` (the full streamed body).
- `ftp_roundtrip(...)`: a hand-rolled FTP client (net/bufio only). Passive mode dials the CONTROL host with the PASV-advertised port, deliberately ignoring the advertised h1-h4 so masquerade/private-IP mismatches never break the test. `active=True` opens a local listener and sends `PORT`; `advertise_host` overrides the connect-back address (validated IPv4 dotted-quad). Returns `{"ok","downloaded","n","error"}`; errors surface as `ok=false`, never a panic. Unit-tested against an in-process fake FTP server whose PASV reply advertises a bogus host to prove the client ignores it.
- `pod_exec` (used by kube scenarios such as `ftp-usernet.star`): loops up to 30s, each iteration RE-RESOLVING the newest Running pod (`--field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp`, `{.items[-1:]...}`) and retrying on transient churn errors (`isTransientExecErr`: "not found", "unable to upgrade connection", "container not found", "ContainerCreating", "error dialing backend", "container is not created or running"). Genuine non-zero command exits still surface immediately. This tolerates pod rescheduling / a not-yet-ready `app` container after `wait` reports Running (see Notable bugs).
- `devcontainer_cli(*args)`: runs the `devcontainer` binary with `DOCKER_HOST` env selection like `docker_compose`; errors without `dockerd_up()` first.
- `port_forward(name, port, server?)` (backgrounds `cornus port-forward`, returns the local address) and `free_port()` (allocates a free local port for forward specs).
- `temp_dir()`: fresh temp dir (prefix `cornus-e2e-scenario-`), path returned; deliberately chmod'd **0755** (mktemp/MkdirTemp default 0700) because scenarios bind-mount these dirs into containers whose processes run as non-root uids — nginx 403s on a 0700 docroot. NOT auto-removed (same lifecycle as the mktemp idiom; host tmp cleanup owns them).
- `read_file(path, default?)`: VERBATIM file read (not trimmed, unlike `sh()`); `default` is returned only on `os.ErrNotExist` — deliberately narrower than `cat f 2>/dev/null || true`, so a polling loop fails loudly on a real read error (permission, IsADirectory) instead of spinning to a misleading timeout.

File-plumbing idioms (from the sweep that replaced `sh()` mktemp/printf/cat composites with `temp_dir()`/`write_file()`/`read_file()`):

- `read_file` is verbatim while `sh()` trims: a file WITH a trailing newline fails `assert_eq` against the bare string where `sh(cmd="cat ...")["output"]` would have passed. Write markers with `printf %s`/`write_file` (no trailing newline), use `assert_contains`, or `.strip()` at the call site.
- The `; echo EXIT=$?` idiom is redundant: `sh()` has always captured a non-zero exit in `{"code"}` (ExitError path) instead of failing the scenario — assert on `r["code"]` directly. Reserve output-marker tricks for builtins that DO abort on non-zero (`docker()`, `cornus()` without `expect_fail`).
- `grep -qw 9p /proc/filesystems` ≡ `"9p" in read_file("/proc/filesystems").split()` — the file's lines are `[nodev\t]<fsname>`, so whitespace-split tokens ARE grep -w's word boundaries.
- What still legitimately uses `sh()`: real external commands — `id -u`, `cp -r` (fixture copy), `truncate -s 16M` (sparse file), docker/devcontainer CLI invocations needing `DOCKER_HOST=...` env prefixes or rc capture.

The harness stays lean by `exec`ing the `cornus`/`cornus compose` binaries (BuildKit is not linked into the e2e binary) and talking to the server via the client package. A `Target` abstraction provides environments:

- `DockerTarget` — dockerhost backend against the host daemon; `PrepareImage` is a no-op.
- `KubeTarget` — creates/destroys a kind cluster, writes its kubeconfig, sets the kube serve-env; `PrepareImage` `crane`-pulls the built image and `kind load`s it so pods run it without an in-cluster registry. The kube target sets `CORNUS_K8S_IMAGE_PULL_POLICY=IfNotPresent` to use `kind load`ed images. `compose_up` on kube auto-builds and kind-loads `build:` service images (`prepareComposeBuildImages`/`composeBuildImageRefs`, riding the same `PrepareImage` path as `build()`).
- `ContainerdTarget` (`--target containerd`) — bare containerd host (no dockerd); ServeEnv sets `CORNUS_DEPLOY_BACKEND=containerd` AND `CORNUS_BUILD_WORKER=containerd`; gated on `CapContainerd`.
- Local (`--target local`) — for build-engine scenarios on a privileged host without a deploy backend.

Per-role data-dir isolation: `serve()` and local `build()` get distinct `CORNUS_DATA` dirs under a per-harness temp root (`Harness.dataDir(role)`), removed on teardown. Without this, a local build after a remote build deadlocks on the server's BuildKit boltdb lock (see Pitfalls).

**`--check` mode** resolves, not just parses: `e2e.Check` uses `starlark.SourceProgramOptions` (originally `syntax.Parse`, which missed resolve errors such as top-level `for`). Check and RunFile share `syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}` (per-call, no global mutation), so scenarios may use top-level for/if and global reassignment as authors naturally write them. `predeclaredNames()` is kept in sync with `predeclared()` by `TestPredeclaredNamesInSync`; `TestScenariosParse` globs all scenarios so every new scenario is check-covered by `go test ./pkg/e2e/`.

### Preflight

`pkg/e2e/preflight.go` defines a `Capability` enum (docker / kind / kubectl / ssh-tools / build-engine / 9p / containerd / devcontainer-cli) with one probe each: `docker version`, `LookPath`, euid==0 or the unprivileged-userns knobs (`/proc/sys/kernel/unprivileged_userns_clone`, `/proc/sys/user/max_user_namespaces`), 9p via `/proc/filesystems` + `/sys/module/9p`, and `CapDevcontainerCLI` via `probeBinary("devcontainer")`. `targetNeeds()` plus `scenarioNeeds()` (a token scan of the `.star` source for `build(`, `build_upload(`, `ssh_agent(`, `devcontainer_cli(`, done before any target is provisioned) aggregate the required set with `RequiredBy` attribution, so a missing tool/privilege fails legibly up front ("kind not on PATH, required by kube target") instead of crashing mid-scenario. Hermetic unit tests in `preflight_test.go` (no docker exec; asserts the cornus-native `devcontainer_up(` does NOT flag CapDevcontainerCLI). Known limit: the token scan would not flag a compose file with a `build:` section driven only through `compose_up(` (today `compose_build(` happens to contain the `build(` substring).

Self-skip vs preflight: cornus-e2e preflight is all-or-nothing fail-fast over the union of target+scenario needs, so a ROOT or environment requirement there would abort entire suite runs on developer hosts. Requirements like devcontainer-vscode.star's root+9p therefore live IN the scenario as a self-skip (`id -u`, `"9p" in read_file("/proc/filesystems").split()`) — the same pattern as registry-s3's `getenv` self-skip — keeping full-glob container runs green. (The underlying constraint there: proxy `start` rides deploy-attach, whose caller-local mounts the dockerhost backend realizes via `deploywire.CanMountLocal()` = euid 0 + kernel 9p.)

### Containerized runner and self-hosting

The all-in-one image runs the FULL suite — privileged local + remote (9P/WebSocket) builds with bind/secret/cache/ssh mounts, compose, deploy, lifecycle, registry, and kind-in-dind including the 9p sidecar mount scenarios — from one privileged container. Verified via the project's own flow (`make e2e-image` + `make e2e-container`), not just hand-rolled `docker run`.

Self-hosting (dogfood): Cornus can deploy its own E2E runner. This required adding `api.DeploySpec.Privileged` (opt-in, off by default -> dockerhost `HostConfig.Privileged`, k8s app-container `securityContext.privileged`) because the runner's dind dockerd fails unprivileged (`mount: /sys/kernel/security: permission denied`). Verified: `cornus serve` as a filesystem-backed registry, `docker push` of the runner image into it, `cornus deploy -f` with `privileged: true` — Cornus pulled from its own registry, created the container with `Privileged=true`, and the full suite passed inside the Cornus-managed workload.

### Scenario inventory

All scenarios are registered in the Makefile `SCENARIOS` list except `build-lazy-9p.star` and `devcontainer-vscode.star` (both in `EXTRA_CHECK_SCENARIOS`; see Key Facts). "docker-only" scenarios self-skip on other targets.

| Scenario | Covers |
|---|---|
| `registry.star` | Push/pull digest round-trip via `registry_roundtrip`; `/v2/` ping, `_catalog`, `tags/list`. Needs no capabilities. |
| `registry-persistence.star` | Push -> `stop_server` -> re-serve the same `file://` dir -> manifest + tag survive (mem:// could not catch this). |
| `registry-edges.star` | Wire-protocol edges incl. blob DELETE contract: DELETE -> 202, HEAD -> 404, second DELETE -> 404 (gated by the `push` API policy). |
| `registry-errors.star` (agnostic) | Wire-protocol error surface: 404 NAME_UNKNOWN / 405 UNSUPPORTED / 400 DIGEST_INVALID (missing digest) / 404 BLOB_UPLOAD_UNKNOWN / 404 MANIFEST_UNKNOWN / cross-repo digest-leak guard -> 404, plus a regression-lock on the manifest-*validation gap* (`PutManifest` stores an unvalidated body -> 201). Runtime-verified on `local`. |
| `registry-auth.star` (agnostic) | Boots `serve(env={CORNUS_JWT_HS256_SECRET, CORNUS_API_POLICY:{"ci-bot":["push"]}})`, mints real JWTs with `cornus token issue`, asserts the full push story: 401 (no cred) / 403 (valid cred, identity lacks push) / 202 (authorized). Verified on `local`. |
| `cli-errors.star` (agnostic) | CLI misuse + fail-closed startup: port-forward bad mapping, `deploy -f` nameless/malformed spec, `config use-context` unknown context, and `cornus serve` exiting non-zero BEFORE binding on malformed `CORNUS_GC_INTERVAL` / `CORNUS_API_POLICY`. Verified on `local`. |
| `deploy-errors.star` (docker-only) | image-pull failure, host-port conflict ("port is already allocated"), `privileged=True` rejected by the default-deny host policy, and crash-on-start observed as `running==0` (host backends have no synchronous health wait — poll `Status`). Verified on docker (daemon 29.2.1). |
| `build-fail.star` (build engine) | `cornus build` fails local+remote on RUN-false / unresolvable FROM / parse error / missing COPY source (testdata under `e2e/scenarios/build-fail/`); the POST `/.cornus/v1/build` in-band `BUILD FAILED:` trailer via `build_upload` (HTTP still 200); and pre-stream 400s (missing `?t=`, non-tar body). Verified on `local`. |
| `registry-s3.star` | S3-backed registry storage. |
| `registry-gcs.star` / `registry-azblob.star` | GCS / Azure Blob registry storage against fake-gcs-server / Azurite via `make e2e-cloudblob`; the SERVED cornus binary needs `-tags cloudblob` (not the runner). Validated live against both emulators. |
| `cli.star` (docker-only) | Real CLI binary: `version`, `health`, `push` (local tarball -> registry), `deploy -f` + `--delete` on the local dockerhost. |
| `deploy.star` | Core arc: build -> push -> deploy -> verify -> teardown; replicas + idempotency. |
| `deploy-config.star` (docker-only) | DeploySpec surface live: env, published ports (`http_get`), `command` override (lands in `.Config.Cmd` AND executes), ro + rw host bind mounts (rw write polled via `wait_file`; `:ro` write rejected), restart policy — cross-checked with `docker inspect`. |
| `deploy-shape.star` | Deployed-object shape assertions. |
| `deploy-mounts.star` | Client-local 9P mounts (kube mount sidecars). |
| `deploy-volumes.star` / `deploy-named-volume.star` | Anonymous / named volumes. |
| `deploy-network.star` / `deploy-dns.star` | User networks, service DNS. |
| `deploy-netpolicy.star` / `deploy-netpolicy-enforce.star` | Network policy declaration / enforcement. |
| `deploy-proxy.star` / `deploy-proxy-coop.star` / `deploy-proxy-mounts.star` | Proxy variants: enforcing/uid, cooperative/hostAliases, enforcing+mounts — validated to coexist back-to-back in a shared namespace. |
| `deploy-hub.star` / `deploy-hub-udp.star` | Hub overlay (TCP/UDP). `deploy-hub` is known flaky under dind on kube (importer pod Failed after Running; passes on re-run). |
| `deploy-multus.star` | Multus NAD attach (`net1`, bridge, host-local IPAM), name resolution, NAD reaped on compose down. Real-NAD path gated on `E2E_MULTUS=1`; self-skips otherwise. |
| `deploy-multus-ipvlan.star` / `deploy-multus-macvlan.star` / `deploy-multus-detached.star` | The remaining user-network matrix rows, env-gated on `E2E_MULTUS_IPVLAN=1` / `E2E_MULTUS_MACVLAN=1` / `E2E_MULTUS_DETACHED=1` (the `e2e-container` target passes the Multus envs through). ipvlan/macvlan: pinned static IPs live on `net1`, caretaker DNS answers secondary IPs (macvlan asserts strictly pod-to-pod — slave-to-parent is impossible by kernel semantics). Detached (row D): the user network IS the primary interface (no `net1`, no caretaker), driven via `cornus deploy --detach` + `networks[].default: true`. All validated live in kind-in-dind. |
| `deploy-cilium.star` | Cilium network policy; self-skips without the CNP CRD (plain kind). |
| `lifecycle.star` | Deploy -> stop=0 / start=1 / restart=1 against real containers. Public image, no build engine. |
| `lifecycle-restart.star` | Restart-policy semantics (containerd's restart monitor): boot-count via a bind-mounted log, PID 1 = sh with a TERM trap; resurrection after `kill 1`, explicit stop sticks past a monitor interval. Validated live on containerd-in-dind. |
| `exec.star` | Exec into workloads (includes an asserted-failure `context canceled` case). |
| `compose.star` | `compose up`/`ps`/`down` with public images; `http_get` of the compose-published port; post-down `wait_gone`. |
| `compose-build.star` (docker-only) | Compose `build:` -> up -> ps -> stop/start/restart -> down on a from-source service. |
| `compose-mounts.star` | Compose with 9p mount sidecars (kube). |
| `devcontainer.star` | Devcontainer flow (cornus's own `cornus compose --devcontainer` translation). |
| `devcontainer-vscode.star` (NOT in SCENARIOS; in `EXTRA_CHECK_SCENARIOS`) | The OFFICIAL `@devcontainers/cli` (VS Code's engine) against the dockerd proxy via `devcontainer_cli(...)`: `devcontainer up` on an image-based fixture, label-filter lookup, postCreateCommand, bidirectional workspace bind visibility over 9P, containerEnv, `devcontainer exec` exit-code propagation. Self-skips unless docker target + root + 9p (self-skip, not preflight — see the rationale above). See `dockerd-proxy.md`. |
| `dockerd.star` (docker-only) | `cornus daemon docker` Docker-API proxy: `docker -H <proxy> run/ps/inspect/stop/rm`, cross-checked against the Cornus server at each step; compose `--scale web=2` then reconverge to `--scale web=1` sections, validated live against docker 29.2.1 / compose v5.0.2. |
| `build-edge.star` | 7 edge cases, each run local AND remote (9P/WebSocket): `.dockerignore` filtering; named-context `.dockerignore` (remote); symlink transmission (regresses the p9fs `Linkname` fix); multi-stage `COPY --from`; build args; `<dockerfile>.dockerignore` precedence over `.dockerignore`; negative `COPY` of an ignored file MUST fail (`expect_fail`; its `excluded.txt not found` log line is the asserted failure). |
| `build-mounts.star` | BIND/SECRET/CACHE/SSH RUN mounts, local + remote. |
| `build-cache.star` | `--cache-to` exports a cache manifest to the registry (asserted via `http_get`); a second `fresh_cache` `--cache-from` build serves the RUN from cache (unique RUN echo marker present on miss, absent on hit). |
| `build-invalidate.star` | Cache invalidation: touch a file -> miss. |
| `build-lazy.star` | 16 MiB named context, build touches only `small.txt`; parses `CORNUS-9P served N bytes` and asserts N < 1 MiB. |
| `build-lazy-9p.star` (NOT in SCENARIOS) | Lazy build over the 9p kernel module; parses the `CORNUS-9P served N bytes` progress marker. |
| `build-upload.star` | Raw `POST /.cornus/v1/build` tar-upload endpoint (query params `t`, `dockerfile`, `push`, `no-cache`, `insecure`, `build-arg`; streamed `text/plain` ending `BUILD OK <ref> <digest>`): asserts 200, `BUILD OK`, digest, the streamed context marker (no_cache so RUN always executes), and catalog/tag presence in Cornus's own registry. |
| `ftp.star` (docker-only) | Bidirectional passive-mode FTP through published ports `12100:21` + `30000:30000` (`FTP_PASV_ADDRESS=127.0.0.1`): STOR then RETR of a ~2KB varied-byte payload, byte-equality asserted — proves multi-port publishing and a genuine separate data connection, which an HTTP GET cannot. Server fixture is a Go program (`github.com/fclairamb/ftpserverlib`, afero MemMapFs) as a nested module (`e2e/scenarios/ftp/`, own go.mod), built by Cornus's own build engine; env-configurable `FTP_LISTEN`/`FTP_PASV_PORT`/`FTP_PASV_ADDRESS` (`auto` derives per connection)/`FTP_USER`/`FTP_PASSWORD`. |
| `ftp-active.star` (docker-only) | Active-mode FTP: harness opens a listener, sends `PORT`, server dials back; the docker-bridge gateway is computed as the advertise host. |
| `ftp-usernet.star` (kube, gated) | FTP server + busybox client as two workloads on a shared user-network; client does ftpput/ftpget/cmp via `pod_exec`, signals `FTP-USERNET-OK`. Deliberately a user-network, not the hub overlay: FTP embeds IP:port in PASV/PORT, which a name-based relay cannot rewrite — real L2/L3 connectivity is the correct fabric. Self-skips without kube + Multus. |
| `deploy-portforward.star` | `cornus port-forward` end to end: deploys `nginx:alpine` with NO published ports, forwards a fresh local port to container `:80` via the `port_forward` builtin, `http_get`s it (200 + "nginx") plus a concurrent second GET. Target-agnostic (skips `local`). Passed live on kind. See `port-forwarding.md`. |
| `deploy-autoforward.star` | Automatic client-side forwarding of published ports over a deploy session (`pkg/portfwd`), with 9P mounts + concurrent connections on kube. Passed live on docker AND kind. See `port-forwarding.md`. |
| `connection-profile.star` | Connection profiles through the real binary: a throwaway-`CORNUS_CONFIG` `cornus config set-context`/`use-context`, then `compose up`/`ps`/`down` driven PURELY through the profile (no `-H` on argv), cross-checked server-side. Docker + kube. See `remote-cluster-connection-ergonomics.md`. |
| `incluster-portforward.star` / `incluster-kubeauth.star` (kube-only) | Deploy cornus INTO kind as a Deployment+ClusterIP Service (`cornus:e2e`) and drive the CLI through a port-forward profile (`svcforward`); the kubeauth variant adds a TokenRequest-minted SA token authenticating through the tunnel with an unauthenticated-rejection negative control. Self-skip off kube. See `remote-cluster-connection-ergonomics.md`. |

### Negative / unhappy-path coverage

The suite was ~95% happy-path; the negative surface is now covered by five dedicated scenarios (`registry-errors`, `registry-auth`, `cli-errors`, `deploy-errors`, `build-fail`, all in the Makefile `SCENARIOS` list and green under `make e2e-check`), on top of the incidental negative asserts that already lived in `registry-edges`, `build-edge` (`edge-mustfail`), `deploy-shape` (bad-mount), `incluster-kubeauth` (no-token), the netpolicy/proxy `BLOCKED` checks, and `exec`/`cli` exit codes.

Three small, backward-compatible harness enablers in `pkg/e2e/harness.go` let negative tests assert the *reason*, not just that something failed. Crucially, NONE add a builtin, so `predeclaredNames()` / `TestPredeclaredNamesInSync` / `--check` are unaffected (2026-07-07):

1. `build(expect_fail=True, capture=True)` RETURNS `{"tag","log"}` from the expected failure (previously short-circuited to `None`).
2. `deploy(expect_fail=True)` RETURNS the rejection message string (was `None`); existing scenarios ignore the return value, so it is safe — lets `assert_contains` on the exact error.
3. `serve(env={...})` injects extra server-process env (appended last), enabling an auth-enabled server for `registry-auth`.

Deliberately skipped negatives: oversized-blob 413 (a 10 GiB cap is impractical to stream), path-traversal tar ("illegal path in tar"), and data-dir flock contention (the harness isolates per-role data dirs).

Durable code-level facts surfaced by mapping the failure surface (each latent behavior, not a bug, but easy to get wrong in tests):

- **`BUILD FAILED:` is POST-only.** The in-band failure trailer (`pkg/server/build.go:133`) is emitted ONLY by `POST /.cornus/v1/build` (the `build_upload` builtin). The `build` builtin's paths — the local in-process engine and the remote build-attach WebSocket — surface failures as the `cornus build` CLI's own non-zero exit + differently-formatted output, NOT a `BUILD FAILED:` line. Lesson: assert failure *text* via `build_upload`; assert only *that it failed* via `build(expect_fail=True)`.
- **Manifest PUT is unvalidated (latent gap).** `storage.PutManifest` (`pkg/storage/cas.go`) writes the raw body as blob + membership marker + tag with NO JSON parse, schema check, or referenced-blob-existence check; `handleManifest` PUT returns 201 for any body. An explicit `Content-Type` is stored verbatim; only an empty one triggers `detectMediaType`, which silently falls back to the OCI manifest media type on any parse failure. So a malformed manifest / a manifest referencing a missing blob currently *succeed*; `registry-errors.star` locks this permissive behavior so a future move to real validation is a deliberate, visible change.
- **Cross-repo isolation is marker-gated, not content-gated.** Blob content lives in one shared CAS; per-repo access is gated by a membership marker written per repo. GET-ing a manifest by a digest that exists under repo A but was never pushed to repo B fails the marker lookup -> `storage.ErrNotFound` -> 404 `MANIFEST_UNKNOWN`. The phrase "manifest does not belong to this repo" is only a source comment in `cas.go`, not a runtime message.
- **Host backends have no synchronous health wait (backend-asymmetric).** dockerhost/containerd `Apply` returns once the container *starts*; a crash-on-start workload's deploy "succeeds" and only `Status` later reveals `running==0`. containerd explicitly *ignores* `Healthcheck` (warns and drops it); only the kubernetes backend maps it to liveness/readiness probes. A test that wants to observe a crash on a host backend must poll `Status`, not rely on the deploy erroring.
- **The static auth token has an empty identity -> always denied by a policy.** `CORNUS_AUTH_TOKEN` authenticates but yields identity `""`, and a configured `CORNUS_API_POLICY` fails closed on the empty identity (`pkg/server/apipolicy.go`). To satisfy a policy you need a real identity — a JWT (`sub`) or mTLS CommonName. `cornus token issue --sub X --hs256-secret S` mints a matching JWT (the server is verify-only). `/healthz`, `/readyz`, `/metrics` are auth-exempt (`auth.go:209`), so `serve()`'s health probe still passes with auth enabled — which is what makes `serve(env=...)` auth scenarios viable. The `/v2/*` challenge is deliberately Basic, not Bearer (docker-login compatibility); anonymous pull is opt-in (`CORNUS_REGISTRY_ANONYMOUS_PULL`) and never covers push.
- **Startup config is validated before the listener binds.** `gcIntervalFromEnv` and `parsePolicyEnv` run inside `server.New` (before `srv.Run` calls `net.Listen`), so a malformed `CORNUS_GC_INTERVAL` / `CORNUS_API_POLICY` makes `cornus serve` exit non-zero *without* binding — cleanly testable with `cornus("serve", ..., expect_fail=True)`.
- **Privileged is default-deny; the docker E2E target does not opt in.** `DockerTarget.ServeEnv` (`pkg/e2e/target.go`) sets `CORNUS_ALLOW_BIND_SOURCES=/` but NOT `CORNUS_ALLOW_PRIVILEGED`, so a `deploy(privileged=True)` is rejected by `hostpolicy` — a path that was entirely untested until `deploy-errors.star`.

### `command` vs `entrypoint`: the DeploySpec mapping contract

`api.DeploySpec` (`pkg/api/deploy.go:19-27`) follows Docker semantics: `Command` supplies ARGUMENTS to the image ENTRYPOINT (Docker `CMD`, carried in k8s `container.Args`), and only `Entrypoint` overrides the ENTRYPOINT (k8s `container.Command`). The kube backend implements this correctly, exactly matching dockerhost — a kube-vs-docker discrepancy in 6 kube-only scenarios (2026-07-08) was the *scenarios* encoding the wrong mental model (setting `command` on ENTRYPOINT-bearing images expecting it to REPLACE the entrypoint, e.g. `cornus:e2e` ENTRYPOINT `["cornus"]` + `command=["sleep","3600"]` ran `cornus sleep 3600` -> crash -> `waiting for 1 running` timeout), not a backend bug. The fix EXPOSED the existing `Entrypoint` where the plumbing had been missing:

- Harness: `entrypoint?` added to the `deploy` and `deploy_attach` builtins -> `spec.Entrypoint`.
- Compose: an `entrypoint:` field added to `compose.Service` and mapped to `spec.Entrypoint` in `translateService` (was silently unsupported).
- Devcontainer: the `overrideCommand` keep-alive was injected as `Command` (args to the ENTRYPOINT, so it never ran on an ENTRYPOINT image); now emitted as `Entrypoint`, matching the real `@devcontainers/cli`.
- Scenarios: `cornus:e2e` mount workloads use `entrypoint=["sleep"], command=["3600"]`; `deploy-shape` now asserts BOTH mappings (`Entrypoint -> .command`, `Command -> .args`) — the smoking gun that first proved the mapping.

### Coverage-audit method and closed gaps

A four-agent survey (registry+storage, CLI+routes, build engine, kubernetes+dockerd proxy) inventoried each subsystem's feature surface WITHOUT reading `e2e/scenarios/`, then the cross-reference against the scenario set was done by hand — keeping each inventory unbiased by existing coverage. It found and drove the closure of: registry discovery + `file://` persistence, build remote-cache/lazy/`--no-push`/tar-upload, the entire cornus daemon docker proxy (previously zero E2E), the real CLI binaries (the harness had driven the client library directly), compose `build:` + lifecycle subcommands, and the DeploySpec env/ports/command/mounts/restart assertions. Still open at last audit: registry HEAD/cross-repo mount/chunked upload/`DIGEST_INVALID` (needs a generic HTTP-verb builtin; only `http_get` exists), kubectl object-shape assertions on kube, `docker compose` against the dockerd proxy (needs DOCKER_HOST env threading), and the proxy's known stubs (empty `docker logs`, no `exec`/`build`).

### Notable bugs the baselines caught

Running the suite for real (not just `--check`) repeatedly paid for itself:

- **`--check` parse-only blind spot**: top-level `for` in fixtures passed `syntax.Parse` but failed at execution. Fixed by making Check resolve (SourceProgramOptions), which immediately also surfaced a latent `deploy.star` global-reassign bug that had never been executed — resolved via shared permissive `FileOptions`.
- **Data-dir boltdb lock deadlock**: `cornus serve` and a local `cornus build` sharing `config.DefaultDataDir()` deadlock — BuildKit's boltcache opens `cache.db` with no flock Timeout, and the server's lazily-started engine holds the lock for its lifetime, so the next local build blocks forever with zero output. Two fixes: per-role harness data dirs, and a product fix — `builder.New` takes a non-blocking exclusive flock on `<data-dir>/engine.lock` (`lock_linux.go`) before constructing the BuildKit controller, failing fast with "data dir ... is in use by another cornus process" (regression test `TestLockDataDirRejectsSecondHolder`, no root needed).
- **dind published-port readiness race**: a freshly published port accepts TCP (docker-proxy up) before the workload serves — `000` at t+0/t+1s, `200` from t+2s under dind. `wait(running=N)` only means "backend reports started", NOT "app is serving". Fixed harness-side with the `http_get` retry window.
- **Kubernetes deletion is asynchronous**: a synchronous post-`compose_down` gone-assertion flakes on kube; poll with `wait_gone`.
- **`mktemp -d` 0700 -> nginx 403**: nginx's non-root worker cannot traverse a 0700 bind-mounted docroot (host or container alike); `chmod 755` the dir. Only bites containers whose serving uid differs from the dir creator (root-running alpine bypassed it).
- **Cornus could not deploy privileged containers**: no `Privileged` knob anywhere in DeploySpec/dockerhost/k8s — added opt-in `DeploySpec.Privileged`, unblocking self-hosting the runner.
- **ftpserverlib active mode binds source port 20** (privileged) by default — active transfers fail with "bind: permission denied" without root. Set `Settings.ActiveTransferPortNon20 = true`.
- **Progress markers are scenario-parsed contracts**: a slog logging sweep converted the `CORNUS-9P served N bytes` emitter to `slog.Debug` (invisible to the progress stream), silently breaking `build-lazy-9p.star`; restored as `fmt.Fprintf(progressW, ...)` in `pkg/build/builder/solve_linux.go`. Logging sweeps must leave `progressW` prints alone.
- **Stale scenario contracts rot**: `registry-edges.star` still asserted blob DELETE -> 405 after DELETE became an implemented feature (202); scenarios must track the real contract.
- **`pod_exec` racing pod recreation** (`deploy-hub.star` kube flake, 2026-07-08): `bPodExec` resolved the pod once via `jsonpath={.items[0].metadata.name}` (which can pick a Terminating pod) and treated any `kubectl exec` failure as a hard error, so a rescheduled importer pod or a briefly-not-ready `app` container after `wait` reported Running escaped the scenario's own retry loop (whose `|| echo PENDING` only guards curl *inside* the container, not kubectl) and aborted the whole scenario. Fix: `pod_exec` now loops up to 30s re-resolving the newest Running pod and retrying on `isTransientExecErr` classes (see the builtin description). Improves every `pod_exec` caller, not just deploy-hub. `deploy-hub` may still be flaky under dind for the separate importer-pod-Failed reason below.
- **Named-context `.dockerignore` asymmetry** (product, documented): filtering happens only on the remote path (caller-side confinedfs); the local build path passes named contexts through `fsutil.NewFS` unfiltered — so `--build-context data=./data` + `./data/.dockerignore` excludes on `--builder` builds but not local ones. The build-edge named-ignore case is therefore remote-only.

### Full-suite validation history (reference points)

- Docker + build-engine first live run: full unit suite plus the non-build docker scenarios green; the build engine's first end-to-end execution happened as root in a `--privileged` `golang:1.25-bookworm` container.
- Build-backend baseline: `build-edge.star` all 7 cases green, local AND remote — the regression net to run before/after any lazy-bind change.
- Post user-network work: full default kube suite, 32 scenarios green in one automated cluster, all three proxy variants coexisting; `E2E_MULTUS=1` real-NAD run green including NAD GC.
- Post `pkg/` restructure: `E2E_TARGETS="docker kube"` dind run, 33/36 outright + 3 pre-existing staleness issues fixed (registry-edges contract, 9P marker, deploy-hub dind flake).

### Failure diagnostics and local kube verification

`wait` timeouts on Kubernetes call `kubeWaitDiag`, which best-effort captures `kubectl describe pod`,
caretaker logs (current and `--previous`), and app logs. This changes an opaque readiness timeout
into container state, probe, scheduling, image-pull, or crash evidence while remaining a no-op on
non-kube targets. It directly distinguished a missing workload from the initial sidecar-crash
hypothesis in the relay-egress investigation.

A host kind installation is not required for kube verification. The all-in-one E2E image runs
Docker-in-Docker plus kind inside a privileged container, matching CI. To run a subset, build the
image and invoke its entrypoint directly because the `e2e-container` Make recipe does not forward
`E2E_SCENARIOS`:

```sh
make e2e-image
docker run --rm --privileged \
  -e E2E_TARGETS=kube \
  -e E2E_SCENARIOS="e2e/scenarios/deploy-egress-proxy.star e2e/scenarios/deploy-egress-transparent.star" \
  cornus-e2e:latest
```

This path proved the relay-egress lifecycle fix on real kube. Docker and containerd were already
green; the two previously failing proxy/transparent scenarios then passed in kind. The durable gate
and per-target recipes live in `.agents/docs/QUALITY_GATE.md`.

### Recent CI lessons

- Interactive TTY tests must answer cursor-position query `ESC[6n` with `ESC[<row>;<col>R`; replying
  to unrelated OSC queries corrupts the line editor.
- Foreground `compose up` is intentionally held. Scenarios that only need deployment must use `-d`;
  bounded harness call timeouts are a backstop, not the lifecycle fix.
- `reportReconcile` must stop on a seen-then-removed workload so foreground Compose exits after
  external deletion instead of polling forever.
- Caretaker readiness includes egress: `egressReady` dials the configured loopback listen port.
  Listener readiness is distinct from process survival and gates the app until relay setup succeeds;
  `ListenPort == 0` is ready because no listener is expected.
- Kube fixture queries must carry the scenario namespace. The credentials AI proxy failure was an
  empty-jsonpath/namespace bug, not a product credential failure; use empty-tolerant jsonpath and
  `-n cornus-e2e` for scenario-owned objects.
- When CI evidence is missing, add diagnostics before changing timeouts or guessing at backend state.

## Files

- `/home/moriyoshi/src/chimpose/cmd/cornus-e2e/` — runner binary (`--target`, `--check`, `--preflight`, `--skip-preflight`, `--keep`, `--cornus`, `--compose`, `--storage`).
- `/home/moriyoshi/src/chimpose/pkg/e2e/harness.go` — builtins, per-role data dirs, `predeclared()`/`predeclaredNames()`.
- `/home/moriyoshi/src/chimpose/pkg/e2e/value.go`, `target.go` — Starlark value plumbing; DockerTarget/KubeTarget.
- `/home/moriyoshi/src/chimpose/pkg/e2e/preflight.go` (+ `preflight_test.go`) — Capability probes, `targetNeeds`/`scenarioNeeds`.
- `/home/moriyoshi/src/chimpose/pkg/e2e/ftp_test.go` — in-process fake FTP server (PASV bogus-host, PORT dial-back) unit tests.
- `/home/moriyoshi/src/chimpose/e2e/scenarios/*.star` + fixture dirs (e.g. `build-edge/`, `build-upload/`, `ftp/` — a nested Go module with its own go.mod, compiled in CI via `cd e2e/scenarios/ftp && go build ./...`).
- `/home/moriyoshi/src/chimpose/e2e/container/{Dockerfile,entrypoint.sh,appimage.Dockerfile,multus-daemonset-thick.yml}` — all-in-one runner.
- `/home/moriyoshi/src/chimpose/e2e/multireplica-hub.sh`, `multireplica-hub-kube.sh`, `e2e/echoserver/` — hub multi-replica validation outside the Starlark suite.
- `/home/moriyoshi/src/chimpose/Makefile` — `SCENARIOS` list and all `e2e-*` targets.
- `/home/moriyoshi/src/chimpose/pkg/build/builder/lock_linux.go` — engine.lock fail-fast (found by the baseline).

## Test Coverage

How to run everything, by environment:

- **No Docker at all**: `make e2e-check` (resolving syntax check of every scenario); `go test ./pkg/e2e/` (hermetic — includes `TestScenariosParse` over all scenarios, preflight tests, FTP client tests against an in-process fake, `TestPredeclaredNamesInSync`). `pkg/e2e` `TestHarnessRegistryScenario` runs a real registry round-trip through the harness when `CORNUS_BIN` is set.
- **Host Docker, unprivileged user**: `make e2e-docker` runs everything except build-engine scenarios (preflight skips/fails them legibly). On the dev box, prefix with `sg docker -c '...'` if the shell predates docker-group membership.
- **Privileged (build engine)**: run inside a `--privileged` container as root, or use `make e2e-one TARGET=local SCENARIO=...` on a root/rootless-userns host for build-only scenarios.
- **Kubernetes**: `make e2e-kube` (needs kind + kubectl; `KEEP=1` keeps the cluster).
- **Everything at once (CI shape)**: `make e2e-container E2E_TARGETS="docker kube"` — dind + privileged build engine + kind-in-dind in one image. Add `E2E_MULTUS=1` for the real-NAD Multus path; `E2E_SCENARIOS` to narrow; `E2E_STORAGE` to switch the registry backend. This full-glob run is the ONLY standard path that exercises `build-lazy-9p.star`.
- **Gated/self-skipping**: `deploy-cilium` (needs the CNP CRD), `ftp-usernet` (kube + Multus), `deploy-multus` real-NAD path (`E2E_MULTUS=1`), docker-only scenarios on kube (self-skip), build scenarios on unprivileged hosts (preflight).
- **Hub multi-replica**: `make e2e-multireplica` (docker; skips cleanly without it) and `make e2e-multireplica-kube` (needs the e2e image).

## Pitfalls

- Do not add `build-lazy-9p.star` or `devcontainer-vscode.star` to the Makefile `SCENARIOS` list: their Cap9P/CapDevcontainerCLI preflights abort the entire suite on hosts missing the capability. Keep them in `EXTRA_CHECK_SCENARIOS` so `make e2e-check` still resolves them.
- Gate root/environment requirements with a scenario self-skip, not a preflight capability — preflight is all-or-nothing fail-fast over the union of target+scenario needs.
- `CORNUS-9P served N bytes` and `CORNUS-9P-BACKING` are scenario-parsed contracts printed to `progressW`, not diagnostics — never convert them to slog.
- `wait(running=N)` means the backend reports started, not that the app serves; use `http_get(retry=...)`/`wait_file`/`wait_logs`, and `wait_gone` after teardown on kube (async delete).
- Never let a scenario's server and a local build share a data dir; the harness isolates them, and the product now fails fast on `engine.lock`, but hand-rolled tests can still trip the pattern.
- `scenarioNeeds` is a token scan (`build(`, `build_upload(`, `ssh_agent(`); a compose-file-only `build:` section driven purely through `compose_up(` would go unflagged.
- `mktemp -d` dirs are 0700 — `chmod 755` anything served by a non-root worker (nginx 403 otherwise, on host and container alike). Prefer the `temp_dir()` builtin, whose 0755 mode is the contract; note its dirs are NOT auto-removed.
- `read_file` is verbatim (`sh()` trims): a trailing newline in the file breaks `assert_eq` against the bare string — write markers without one, use `assert_contains`, or `.strip()`.
- Don't append `; echo EXIT=$?` to `sh()` commands — `sh()` already returns the exit code in `{"code"}`.
- Assumption caveats: `build-lazy`'s < 1 MiB bound assumes the lazy snapshotter is effective in the environment; `build-cache`'s hit assertion assumes a reproducible cache key.
- `deploy-hub.star` is known flaky on kube under dind (importer pod Failed after Running; passes on re-run). Separately, the `pod_exec`-vs-pod-recreation race that also hit it is now fixed in the builtin (retry + newest-Running-pod re-resolution).
- `BUILD FAILED:` only appears on the `POST /.cornus/v1/build` path (`build_upload`); the `build` builtin's CLI/build-attach paths fail with a non-zero exit and differently-formatted output. Assert failure *text* via `build_upload`, only *that it failed* via `build(expect_fail=True)`.
- Host backends (dockerhost/containerd) have NO synchronous health wait — a crash-on-start deploy "succeeds"; poll `Status` for `running==0`. Only the kubernetes backend maps `Healthcheck` to probes (containerd drops it with a warning).
- `CORNUS_AUTH_TOKEN` authenticates with identity `""`, which a `CORNUS_API_POLICY` fails closed on; a policy scenario needs a real identity — a JWT `sub` (`cornus token issue --sub X --hs256-secret S`) or an mTLS CommonName. `/healthz`, `/readyz`, `/metrics` are auth-exempt, so `serve(env=...)` health probes still pass with auth on.
- `command` supplies ARGS to the image ENTRYPOINT (k8s `.args` / Docker `CMD`); use `entrypoint` (k8s `.command`) to REPLACE the entrypoint. Setting `command` on an ENTRYPOINT-bearing image doubles/mangles argv (e.g. `cornus sleep 3600`), not replaces it — the same contract on every backend.
- kind cannot pull from an in-container registry — images reach kind via `PrepareImage` (`crane` pull + `kind load`), and conversely a build pushed to an in-container registry is unreachable by the host daemon.
- Scenarios may use top-level for/if and global reassignment (`FileOptions{TopLevelControl, GlobalReassign}` are enabled) — but keep `--check` green; it resolves, so undefined names fail before any run.

## Recent Harness Coverage

The harness gained SOCKS5-aware `http_get`, conduit arguments on foreground and detached compose helpers, `allow_error` for negative routing assertions, an environment map for held `compose_up_bg`, and recording `egress_proxy()` / `egress_proxy_hits()`. New scenarios cover SOCKS5 aliasing and isolation, mounted port-less aliasing, foreground compose logs, Compose profiles/dependencies/volumes/merge/configs/secrets, egress defaults and caller-proxy routing, caretaker Docker exposure, and AI credential delivery.

Interactive `exec_tty` failures were an input race. Compose CI hangs came from foreground lifecycle and Kubernetes pod-not-found races; fixes restored docker/containerd runs and hardened Kubernetes reconciliation. Always record whether coverage is resolve-checked, docker-live, or kube-live: `make e2e-check` alone cannot validate live relay, mount, or Kubernetes behavior.

## Caretaker Entrypoint And Crash-Loop Coverage

`deploy-mounts.star` queries the generated kube pod with `kubectl -o jsonpath` and asserts the
`cornus-caretaker` init container's `command[0]` is `cornus`, guarding the app-image fallback case.

Kube-only `deploy-crashloop.star` deploys `cornus:e2e` with `entrypoint=["/bin/false"]` in detached
mode and polls `status()` for `running == 0` plus a CrashLoopBackOff diagnostic. `statusDict` now
exposes instance id, state, running, health, message, and optional exit_code; `toStar` supports
`[]any`, enabling the scenario. It is in Makefile `SCENARIOS`.

Both scenarios pass `cornus-e2e --check` and were live-validated in the containerized kind runner
with `E2E_TARGETS=kube` and `E2E_SCENARIOS="deploy-mounts.star deploy-crashloop.star"`: both passed.
The live crash-loop case produced the expected diagnostic, and a transient Unschedulable message
streamed during warm-up before the mount scenario recovered, demonstrating readiness diagnostics.
