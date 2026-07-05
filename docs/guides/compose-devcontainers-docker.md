# Compose, devcontainers, and the docker CLI

Recipes for the Docker-compatible surfaces: the built-in
[cornus compose](/cli/compose) client, devcontainer support, and driving the
stock `docker` CLI through [cornus daemon docker](/cli/daemon). All of them
resolve their server from `--host` / a connection profile / `http://localhost:5000`.

## Bring a Compose project up and down (cornus compose up / down)

Build if needed, deploy, and stream logs in the foreground; then tear the project down.

```sh
cornus compose up
# Ctrl-C to stop, or from another terminal:
cornus compose down
```

- A foreground `up` holds client-local mounts and auto-forwarded ports and stays up until Ctrl-C, then removes what it brought up. `down` stops services in reverse dependency order; add `--volumes` to also remove project-scoped named volumes.
- Compose file discovery looks for `compose.yaml` / `compose.yml` / `docker-compose.yaml` / `docker-compose.yml` in the working directory.

**See also:** [cornus compose](/cli/compose), [deploying workloads](/guides/deploying-workloads)

## Inspect a project (cornus compose ps / logs)

List services and their status, and stream their logs.

```sh
cornus compose ps
cornus compose logs --follow --tail 100 web
```

- `ps` takes `--format table|json`, `-q`, or `--services`. `logs` streams every selected service concurrently; there is no short `-f` for `--follow` because the group already owns `-f` for `--file`.
- For a cluster profile, logs are read directly from the pods with your kubeconfig, falling back to the server proxy.

**See also:** [cornus compose](/cli/compose)

## Build images during up (cornus compose up --build, with --ssh)

Build service images before starting, forwarding your ssh-agent to build steps that need it.

```sh
cornus compose up --build --ssh default
```

- `--build` builds all images before starting (build services are always built). `--ssh` takes `default` or `id[=socket]` and merges over each service's `build.ssh`.
- To build without starting, use `cornus compose build [--no-cache] [--build-arg KEY=VALUE]`.

**See also:** [cornus compose](/cli/compose), [building images](/guides/building-images)

## Use multiple compose files, an env file, and profiles (-f, --env-file, --profile)

Merge several Compose files, point at a specific env file, and activate profiled services.

```sh
cornus compose \
  -f compose.yaml -f compose.prod.yaml \
  --env-file .env.prod \
  --profile debug up
```

- These are group flags that apply to every subcommand. `-f` is repeatable and layered; `--env-file` replaces the default `.env` discovery (later files win, the process environment still overrides them); `--profile` is repeatable and also honors `COMPOSE_PROFILES`.
- Multi-file merge cannot currently clear an inherited scalar. In particular, a later file cannot turn inherited `privileged`, `tty`, `stdin_open`, or `read_only` values from `true` to `false`, and the Compose `!reset` / `!override` YAML tags are not supported. Flatten such overrides into one file before passing them to Cornus.

**See also:** [cornus compose](/cli/compose)

## Run detached with the background agent (cornus compose up -d)

Return immediately, handing client-local mounts, forwarded ports, SOCKS5, and relay-backed egress to the background agent.

```sh
cornus compose up -d
# later:
cornus compose down
```

- `-d`/`--detach` hands mounts, forwarded ports, any SOCKS5 proxy, and `proxy`/`transparent` egress sessions to the client-side background agent and returns. Stop it later with `down`. Inspect or stop the agent with `cornus daemon status` / `cornus daemon stop`.
- File-backed Compose `configs:` and `secrets:` are single-file client-local mounts. They work through the parent-directory/subpath realization on dockerhost; Kubernetes rejects them because its shared 9P sidecar mount cannot project one file onto an arbitrary rootfs target. Directory bind mounts remain supported on Kubernetes. The containerd backend does not currently support client-local deploy mounts.

**See also:** [cornus compose](/cli/compose), [cornus daemon](/cli/daemon)

## Rebuild / restart / stop / start services

Rebuild an image or cycle running services without a full down/up.

```sh
cornus compose build web          # rebuild one service's image
cornus compose restart web        # restart in forward dependency order
cornus compose stop web           # stop in reverse dependency order
cornus compose start web          # start in forward dependency order
```

- Each of `restart` / `stop` / `start` takes an optional service list (default: all). A service whose client-local mounts are held by a background `up -d` helper is refused; use `down` to stop it.

**See also:** [cornus compose](/cli/compose)

## Run a Dev Container (cornus compose --devcontainer, or auto-detected .devcontainer)

Bring up a devcontainer definition and run its lifecycle hooks.

```sh
# Explicit path or search directory:
cornus compose --devcontainer .devcontainer up
# Or auto-detected when no Compose file is present:
cornus compose up
```

- A devcontainer is used with `--devcontainer`, when an `-f` argument points at a `devcontainer.json`, or (auto-detect) when no Compose file is present but a `.devcontainer/devcontainer.json` (or `.devcontainer.json`) is discoverable. A Compose file always wins in a mixed repo.
- Lifecycle hooks run: `initializeCommand` on the host before any container, then per-service `onCreate` / `updateContent` / `postCreate` / `postStart` / `postAttach` as containers come up.

Cornus implements a deliberate subset of the Dev Container schema:

| Area | Fields acted on |
| --- | --- |
| Container source | `image`; `build.dockerfile`, `build.context`, `build.args`, `build.target`, `build.cacheFrom` |
| Workspace and runtime | `workspaceFolder`, `workspaceMount`, `mounts` (`type=bind`, `type=volume`, `type=tmpfs`), `forwardPorts`, `appPort`, `containerEnv`, `remoteEnv`, `overrideCommand`, `containerUser`, `remoteUser` |
| Container options | `runArgs` (single-container definitions), `build.options` |
| Compose-based definitions | `dockerComposeFile`, `service`, `runServices` |
| Lifecycle | `initializeCommand`, `onCreateCommand`, `updateContentCommand`, `postCreateCommand`, `postStartCommand`, `postAttachCommand` |

`features` and `hostRequirements` are recognized but ignored with a warning, as
are editor-specific `customizations`.

For a single-container definition, `runArgs` is translated as a `docker run` argv:
around fifty flags map onto the equivalent service keys, including `--cap-add` /
`--cap-drop`, `--device`, `--hostname`, `--user`, `--shm-size`, `--ulimit`,
`--sysctl`, `--security-opt`, `--tmpfs`, `--read-only`, `--init`, `--privileged`,
the `--dns*` family, and the port/volume/env forms. An argument outside that set
is ignored with a warning naming it. Note that a flag mapping here does not mean
every backend realizes it — `--ulimit` and `--device`, for instance, are dropped
by the kubernetes translation. `build.options` is likewise applied as a
`docker build` argv.

For a Compose-based definition, all `runArgs` are ignored with a warning: the
compose file is the place to express those keys.

`containerUser` sets the user the container's own processes run as, and
`remoteUser` (falling back to `containerUser`) selects the lifecycle-command user.
Other schema fields are outside Cornus's supported boundary.

**See also:** [cornus compose](/cli/compose)

## Drive the stock docker CLI against a Cornus server (cornus daemon docker + DOCKER_HOST)

Run a local proxy that speaks the Docker Engine API and translates container ops into cornus deploys, then point stock `docker` at it.

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

- A foreground run holds until Ctrl-C; `-d`/`--daemon` registers the frontend on the background agent and returns. The socket defaults to `$XDG_RUNTIME_DIR/cornus-docker.sock` (override with `--socket` / `CORNUS_DOCKER_SOCK`).
- The caller's local bind-mount directories are streamed to the server over 9P.
- Named volumes are provisioned by the selected deploy backend, but `docker volume rm` does not delete that backend storage; it only drops the name from this proxy process's memory. `docker volume prune` and the volume phase of `docker system prune` report nothing reclaimed and likewise leave backend storage intact. Use Cornus's backend-aware volume lifecycle instead.

**See also:** [cornus daemon](/cli/daemon), [working with remote clusters](/guides/remote-clusters)

## Render the merged config / print versions (cornus compose config / version)

Inspect cornus's parsed and merged view of the project, or print the Compose CLI version.

```sh
cornus compose config              # full merged model as YAML
cornus compose config --services   # just service names, in dependency order
cornus compose version --short
```

- `config` also takes `--volumes`, `--images`, `--format yaml|json`, and `-q` (validate only, print nothing). `version` takes `--short` or `--format pretty|json`.

**See also:** [cornus compose](/cli/compose), [cornus version-health](/cli/version-health)
