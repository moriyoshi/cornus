# cornus compose

A Docker Compose-compatible client that redirects Compose commands to a running
cornus server over its `/.cornus/v1/*` endpoints.

## Synopsis

```sh
cornus compose [group flags] <subcommand> [flags]
```

## Description

`cornus compose` mirrors `docker compose`: it loads a Compose project (or a
devcontainer definition), then builds, deploys, and manages its services
against a cornus server. Alias `cornus compose` as `docker-compose` for drop-in
use, or drive stock `docker` / `docker compose` through
[`cornus daemon docker`](/cli/daemon) instead.

The project source is a Compose file or a devcontainer. Compose file discovery
looks for `compose.yaml`, `compose.yml`, `docker-compose.yaml`, or
`docker-compose.yml` in the working directory. A devcontainer is used when
`--devcontainer` is given, when an `-f` argument points at a `devcontainer.json`,
or (auto-detect) when no Compose file is present but a
`.devcontainer/devcontainer.json` (or `.devcontainer.json`) is discoverable. A
Compose file always wins in a mixed repo.

The server connection is resolved from `--host`, otherwise the selected
connection profile, otherwise `http://localhost:5000`. Built images are tagged
for, and deploy pull refs baked against, the registry resolved from
`--registry` / `CORNUS_REGISTRY` / the profile, then the server-advertised host
(`GET /.cornus/v1/info`), then the endpoint host. See the
[deploy spec reference](/reference/deploy-spec) for the resulting deployment
shape.

## Group flags

These flags sit on the `compose` group and apply to every subcommand.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `-f`, `--file` | — | discovery | Compose file(s). Repeatable. Defaults to `compose.yaml` / `docker-compose.yml` in the working directory. |
| `--env-file` | — | `.env` | Env file(s) for variable interpolation, replacing the default `.env` discovery. Repeatable; later files win; the process environment still overrides them. |
| `--profile` | `COMPOSE_PROFILES` | — | Activate services with the given profile (compose `profiles:`). Repeatable; also honors `COMPOSE_PROFILES`. |
| `--devcontainer` | — | — | Path to a `devcontainer.json` file or a directory to search for `.devcontainer/devcontainer.json`. Overrides Compose-file discovery. |
| `-p`, `--project-name` | `COMPOSE_PROJECT_NAME` | dir name | Project name (default: the Compose file directory name). |
| `-H`, `--host` | `CORNUS_HOST` | `http://localhost:5000` | cornus server endpoint. Falls back to the selected connection profile, then the default. |
| `--registry` | `CORNUS_REGISTRY` | derived | Registry `host[:port]` to tag built images with and to bake into deploy pull refs. Overrides the profile and the server-advertised value; empty derives from the server, then the endpoint host. |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | Route logs and auto-forwarded ports through the cornus server proxy instead of connecting to pods directly with your kubeconfig (cluster profiles only). `--no-via-server` forces the direct path. |

### Devcontainer support

When the project comes from a devcontainer definition
(`.devcontainer/devcontainer.json`), `cornus compose` runs its lifecycle
commands: the `initializeCommand` runs on the host before any container is
created, and the per-service `postCreate` / `postStart` / `postAttach` hooks
run as the containers come up. Plain Compose services have no lifecycle hooks.

## cornus compose up

Create and start services (build if needed, then deploy).

```sh
cornus compose up [flags] [services...]
```

Services are brought up in dependency order, honoring `depends_on` conditions.
A foreground `up` mirrors `docker compose up`: it holds any client-local bind
mounts (streamed over 9P), holds auto-forwarded published ports, attaches to
the services' logs, and stays up until `Ctrl-C` — then removes what it brought
up. `-d`/`--detach` hands mounts, forwarded ports, any SOCKS5 proxy, and
relay-backed egress sessions to the
background helper and returns immediately (stopped later by `down`).

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--build` | — | `false` | Build images before starting (build services are always built). |
| `--ssh` | — | — | SSH agent forwarding for builds: `default` or `id[=socket]` (`RUN --mount=type=ssh`), repeatable. Merges over each service `build.ssh`. |
| `-d`, `--detach` | — | `false` | Detached mode: deploy, hand client-local mounts, forwarded ports, SOCKS5, and relay-backed egress to the background agent, and return immediately. |
| `--no-forward-ports` | — | `false` | Do not auto-forward published service ports to local listeners. |
| `--no-attach` | — | `false` | Do not stream service logs in the foreground (still holds mounts/forwards until `Ctrl-C`). |
| `--no-log-prefix` | — | `false` | Do not prefix streamed log lines with the service name. |
| `--conduit` | `CORNUS_CONDUIT` | profile | Session conduit mode: `port-forward` (per-port local listeners, the default) or `socks5` (one split-tunnel proxy reaching services by name). A bare word sets only the mode; a `socks5://host:port[?suffix=SUFFIX]` URL also overrides the bind address and suffix. `--no-forward-ports` disables the conduit entirely. |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | profile | Reach a service ingress (`x-cornus-ingress`) through the SOCKS5 conduit: `native` (tunnel to the real cluster ingress controller) or `emulate` (a client-side reverse proxy with a generated cert), or `off`. Requires `--conduit socks5`. Takes precedence over `CORNUS_INGRESS_CONDUIT` and the profile. See [Public ingress](/topics/ingress). |
| `--egress` | — | — | Route container egress through the client-side network: `env` (propagate proxy vars), `proxy` (caretaker forward proxy), or `transparent` (nftables + relay). |
| `--egress-route` | — | — | Egress routing rule `PATTERN=ROUTE` (route: `client`\|`gateway`\|`cluster`\|`deny`), first match wins. Repeatable. |
| `--egress-default` | — | `cluster` | Egress route for unmatched destinations: `cluster`, `client`, `gateway`, or `deny`. |
| `--egress-pac` | — | — | Path to a PAC-style JS file (`FindProxyForURL`) that decides egress routing; supersedes `--egress-route`. |
| `--telemetry-endpoint` | — | — | Enable the embedded Collector and export every selected service telemetry to this OTLP endpoint. |
| `--telemetry-protocol` | — | `grpc` | Exporter protocol: `grpc` or `http/protobuf`. |
| `--telemetry-header` | — | — | Static OTLP export header `KEY=VALUE`. Repeatable. |
| `--telemetry-insecure` | — | `false` | Disable transport security to the OTLP endpoint. |
| `--telemetry-signal` | — | all | Restrict pipelines to `traces`, `metrics`, or `logs`. Repeatable. |
| `--telemetry-service-name` | — | deployment name | Override injected `OTEL_SERVICE_NAME`. |
| `--telemetry-debug` | — | `false` | Also log collected telemetry to Collector stdout. |

See [Client-side egress](/topics/egress) for the egress
routing model.

## cornus compose down

Stop and remove services in reverse dependency order.

```sh
cornus compose down [flags] [services...]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--wait` / `--no-wait` | — | `true` | Wait for workloads to terminate before returning. `--no-wait` returns as soon as the delete is accepted. |
| `-v`, `--volumes` | — | `false` | Also remove named volumes declared in the Compose file (project-scoped, non-external). External volumes are never removed. |

## cornus compose ps

List services and their status.

```sh
cornus compose ps [flags] [services...]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `-q`, `--quiet` | — | `false` | Only print resource identifiers of created services, one per line. |
| `--services` | — | `false` | Only print service names, one per line, in dependency order. |
| `--format` | — | `table` | Output format: `table` or `json`. |

## cornus compose logs

View output from services. Every selected service streams concurrently.

```sh
cornus compose logs [flags] [services...]
```

For a cluster profile, logs are read directly from the workload pods with your
kubeconfig credentials, falling back to the server proxy only if that path
cannot start.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--follow` | — | `false` | Follow log output. |
| `-n`, `--tail` | — | `all` | Number of lines to show from the end of the logs, per service (`all` for everything). |
| `-t`, `--timestamps` | — | `false` | Show timestamps. |
| `--since` | — | — | Show logs since a timestamp (RFC3339) or relative duration (e.g. `42m`). |
| `--until` | — | — | Show logs before a timestamp (RFC3339) or relative duration. Not supported on the kubernetes backend (ignored with a warning). |
| `--no-log-prefix` | — | `false` | Do not prefix each log line with its service name. |

Note: there is no short `-f` for `--follow`, because the `compose` group already
owns `-f` for `--file`.

## cornus compose build

Build (and push) images for services that define a build section, via the
cornus build engine.

```sh
cornus compose build [flags] [services...]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--ssh` | — | — | SSH agent forwarding: `default` or `id[=socket]` (`RUN --mount=type=ssh`), repeatable. Merges over each service `build.ssh`. |
| `--no-cache` | — | `false` | Do not use the build cache. |
| `--build-arg` | — | — | Set a build-time variable `KEY=VALUE` (repeatable). A bare `KEY` takes its value from the environment. Overrides the compose `build.args`. |

## cornus compose exec

Run a command inside a service's running container, mirroring
`docker compose exec`. Execs into the service's first instance; higher replica
indices are not addressable.

```sh
cornus compose exec [flags] <service> -- <cmd> [args...]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `-d`, `--detach` | — | `false` | Detached mode. Not yet supported by the cornus exec backends. |
| `-e`, `--env` | — | — | Set an environment variable `KEY=VALUE` (repeatable). A bare `KEY` takes its value from the local environment. |
| `-w`, `--workdir` | — | — | Working directory for the command inside the container. |
| `-u`, `--user` | — | — | Run the command as this user (name or `uid[:gid]`). |
| `-T`, `--no-TTY` | — | `false` | Disable pseudo-TTY allocation (a TTY is allocated by default when stdin is a terminal). |
| `--privileged` | — | `false` | Give extended privileges to the command. |
| `--index` | — | `1` | Index of the container instance when the service has multiple replicas (only the first instance is addressable). |
| `--forward-agent` | — | `false` | Forward the local ssh-agent into the exec session (remote-mode dockerhost/containerdhost, or kubernetes with the service's `x-cornus-agent-forward: true` set; see [`cornus exec`](/cli/exec)). |

::: warning `-e`/`--env` visibility on Kubernetes
The Kubernetes `pods/exec` API has no per-exec environment parameter, so on a
cluster profile cornus emulates it by wrapping the command as
`env KEY=VALUE... <cmd>...`. Anything passed with `-e` is then visible to
`ps`/`/proc/<pid>/cmdline` inside the pod for the life of that process. It is
also visible from outside the pod to anyone who has exec access to it, not
just processes already running inside. The dockerhost and containerd
backends set exec environment natively and do not have this exposure. Do not
pass secrets through `-e` on a cluster profile; use a mounted file or an
image/deploy-time env var instead.
:::

## cornus compose restart / stop / start

Restart, stop, or start services. Each takes an optional positional list of
services (default: all). `stop` acts in reverse dependency order; `start` and
`restart` act in forward order. A service with client-local mounts held by the
background `up -d` helper is refused — use `down` to stop it.

```sh
cornus compose restart [services...]
cornus compose stop [services...]
cornus compose start [services...]
```

## cornus compose config

Parse, resolve, and render the Compose model (cornus's parsed/merged view).

```sh
cornus compose config [flags]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--services` | — | `false` | Print service names, one per line, in dependency order. |
| `--volumes` | — | `false` | Print top-level volume names, one per line, sorted. |
| `--images` | — | `false` | Print each service image, one per line, in dependency order. |
| `--format` | — | `yaml` | Output format for the full dump: `yaml` or `json`. |
| `-q`, `--quiet` | — | `false` | Validate the model only; print nothing. |

## cornus compose version

Show the Compose CLI version.

```sh
cornus compose version [flags]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--short` | — | `false` | Print just the bare version string. |
| `--format` | — | `pretty` | Output format: `pretty` or `json`. |

## Examples

Bring a project up in the foreground and stream its logs:

```sh
cornus compose up
```

Build and start in detached mode against a remote server:

```sh
cornus compose --host https://cornus.example.com:5000 up --build -d
```

Bring up only selected services, reaching them through a SOCKS5 conduit:

```sh
cornus compose up --conduit socks5 web api
```

In socks5 mode the background agent hosts one shared proxy and registers each
service's short name in it, so a browser (or any SOCKS5 client) reaches
`web.cornus.internal`, `api.cornus.internal`, and so on through a single proxy.
`cornus web --publish-in-conduit` publishes the web UI into that same shared
conduit, so one browser proxy setting reaches the whole stack and the UI together —
see [Reach a whole Compose stack and its web UI through one browser
proxy](/guides/networking) and [cornus web](/cli/web).

Follow the last 100 lines of one service's logs:

```sh
cornus compose logs --follow --tail 100 web
```

Tear the project down and remove its named volumes:

```sh
cornus compose down --volumes
```

Open a shell in a service's container:

```sh
cornus compose exec web -- sh
```
