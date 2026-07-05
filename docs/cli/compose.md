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
[`cornus daemon docker`](/cli/daemon) instead. Where the two CLIs are not
identical — a flag cornus cannot honor, or one that means something else here —
see [Docker Compose compatibility](#docker-compose-compatibility).

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

### Interactive shell candidates

`x-cornus-shells:` lists the interactive shells a service's image has, most
preferred first. It is read by the [`cornus web`](/cli/web#terminal-shell-discovery)
terminal, which probes these before its own candidate list, so a service whose
image ships an unusual shell opens in it without anyone configuring a browser.

```yaml
services:
  api:
    image: myorg/api
    x-cornus-shells:
      - /bin/bash
      - /bin/busybox sh
```

Each entry is a command **string**, not a pre-split argument list, and is split
the same way `command:` and `entrypoint:` are — so `/bin/busybox sh` is one entry.
A bare string is accepted for a single candidate (`x-cornus-shells: /bin/bash`).

A project-level `x-cornus-shells:` block supplies the default for every service
that declares none; a service list overrides it wholesale rather than adding to
it, since the list is a preference order.

It changes nothing about the deployment. No deploy backend reads it, it is not
part of the spec the backend compares against a running container, and editing it
therefore never recreates anything.

### Provider services

A service may delegate its lifecycle to an external provider plugin instead of
being built or pulled and run as a container (compose-spec `provider:`). Such a
service names a plugin `type` and passes it provider-specific `options`:

```yaml
services:
  database:
    provider:
      type: awesomecloud
      options:
        type: mysql
        version: "8"
  app:
    image: my/app
    depends_on:
      - database
```

- **Discovery.** For `type: awesomecloud`, cornus runs the Docker CLI plugin
  `docker-awesomecloud` if it is on `PATH`, else a binary named `awesomecloud`.
  The plugin runs on the machine invoking `cornus compose`, not on the server.
- **Lifecycle.** On `up`, cornus invokes the plugin as
  `<plugin> compose --project-name=<project> up [--key=value ...] <service>`,
  with each `options` entry passed as a `--key=value` flag (a list value becomes
  repeated flags). On `down`, it invokes the same with `down`. The plugin is
  expected to be idempotent.
- **Environment injection.** The plugin reports environment variables on stdout
  (a `setenv KEY=VALUE` protocol). Each is exposed to services that `depends_on`
  the provider, prefixed with the upper-cased provider service name — so the
  `database` provider above contributes `DATABASE_URL`, `DATABASE_TOKEN`, etc.
  to `app`. A `rawsetenv` variable is exposed to dependents without the prefix.
  A dependent's own `environment:` wins on a name clash.
- **Lifecycle commands.** `cornus compose stop` invokes the plugin's `stop`,
  `start` re-runs `up` (idempotent), and `restart` is stop-then-up. `down` tears
  the resource down via the plugin's `down`.
- **Constraints.** `provider` is mutually exclusive with `image`, `build`, and
  `deploy`. A provider service is shown in `cornus compose ps` as
  `provider:<type>` rather than a deployed workload. A `--watch` reload re-runs
  the plugin's `up` (idempotent) so an edited provider config takes effect.

## cornus compose up

Create and start services (build if needed, then deploy).

```sh
cornus compose up [flags] [services...]
```

Services are brought up in dependency order, honoring `depends_on` conditions.
An explicit service list also brings up what those services depend on — the
transitive closure of their `depends_on`, as `docker compose up web` does — and
says so whenever that adds anything:

```
also starting dependencies of [web]: [cache db] (--no-deps to skip)
```

Only services in the project's active selection are pulled in, so a dependency
excluded by `--profile` / `COMPOSE_PROFILES` stays excluded rather than being
resurrected. `--no-deps` brings up exactly the services you named and nothing
else.

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
| `--watch` | — | `false` | Watch the loaded compose files and env files for edits and automatically reload the configuration and re-reconcile the running services. Works in the foreground and, with `-d`, in the background agent. See [Auto-reload](#auto-reload) below. |
| `--no-forward-ports` | — | `false` | Do not auto-forward published service ports to local listeners. |
| `--no-attach` | — | `false` | Do not stream service logs in the foreground (still holds mounts/forwards until `Ctrl-C`). A project-wide switch, unlike `docker compose`, where `--no-attach` names a service to leave unattached — see [Docker Compose compatibility](#docker-compose-compatibility). |
| `--no-deps` | — | `false` | Do not also bring up the `depends_on` dependencies of the named services. Only meaningful with an explicit service list; without one, every service is selected already. |
| `--force-recreate` | — | `false` | Recreate the workloads even when nothing about them changed. dockerhost and kubernetes leave an unchanged workload alone, so this is what forces the replacement; containerd, bare and incus recreate on every `up` regardless. |
| `-t`, `--timeout` | — | — | Accepted for `docker compose` compatibility and **not honored**: warns and continues. Set `stop_grace_period:` on the service instead — see [Docker Compose compatibility](#docker-compose-compatibility). |
| `--no-log-prefix` | — | `false` | Do not prefix streamed log lines with the service name. |
| `--remove-orphans` | — | `false` | Remove workloads for services no longer defined in the Compose file (left behind after a service was removed or renamed). Without it, `up` only warns about them. |
| `--conduit` | `CORNUS_CONDUIT` | profile | Session conduit mode: `port-forward` (per-port local listeners, the default) or `socks5` (one split-tunnel proxy reaching services by name). A bare word sets only the mode; a `socks5://host:port[?suffix=SUFFIX]` URL also overrides the bind address and suffix. `--no-forward-ports` disables the conduit entirely. |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | profile | Reach a service ingress (`x-cornus-ingress`) through the SOCKS5 conduit: `native` (tunnel to the real cluster ingress controller) or `emulate` (a client-side reverse proxy with a generated cert), or `off`. Requires `--conduit socks5`. Takes precedence over `CORNUS_INGRESS_CONDUIT` and the profile. See [Ingress](/guides/ingress). |
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

### Auto-reload

With `--watch`, `up` keeps watching every file the project loaded from — the
compose file(s), the sibling `.env` or `--env-file` entries, each service's
`env_file:`, and any `include:` / `extends` targets. When you edit and save any
of them, the configuration is reloaded in full and the running project is
re-reconciled toward the new desired state: a service whose spec changed is
recreated, a service you added is started, and a service you removed is torn
down. Unchanged services are left running.

- **Foreground** (`up --watch`): the interactive session reloads in place, then
  keeps holding the new set (and re-attaches logs). A removed service — mounted
  or fire-and-forget — is deleted server-side, matching a foreground exit's
  cleanup.
- **Detached** (`up -d --watch`): the background agent watches the files and, on
  a change, re-runs the same `up -d --watch` to re-plan and reconcile. Removed
  *agent-held* services (client-local mounts, forwarded ports, relay egress) are
  torn down; a removed pure fire-and-forget service is left running (as a plain
  re-`up -d` also leaves it — use `down` or `up --remove-orphans` to clear it).
  Changing the server or conduit settings in the file needs a `down` + `up`.

A full `down` stops the watcher; a partial `down SERVICE` leaves it running.

See [Egress](/guides/egress) for the egress
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
| `--remove-orphans` | — | `false` | Also remove workloads for services no longer defined in the Compose file (left behind after a service was removed or renamed). |
| `--rmi` | — | — | Accepted for `docker compose` compatibility and **not honored** (`local`\|`all`): warns, then tears the workloads down anyway. Any other value is rejected outright. See [Docker Compose compatibility](#docker-compose-compatibility). |
| `-t`, `--timeout` | — | — | Accepted for `docker compose` compatibility and **not honored**: warns and continues. Set `stop_grace_period:` on the service instead. |

Orphan detection is by workload lineage: every `compose up` stamps each workload
with its owning project, so `up`/`down` can tell a project's leftover workloads
(a service you deleted or renamed) from workloads of other projects. `up` warns
about them; `--remove-orphans` (on either `up` or `down`) removes them. A
workload with no recorded project — a raw `cornus deploy`, or one from another
project — is never touched.

## cornus compose ps

List services and their status.

```sh
cornus compose ps [flags] [services...]
```

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `-q`, `--quiet` | — | `false` | Only print resource identifiers of created services, one per line. |
| `--services` | — | `false` | Only print service names, one per line, in dependency order. |
| `--format` | — | `table` | Output format: `table` (SERVICE / NAME / IMAGE / STATUS) or `json`. |

The default columns are `SERVICE`, `NAME`, `IMAGE`, `STATUS` — deliberately not
`docker compose ps`'s `NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS`. Three
of docker's columns describe a local container that a cornus deployment has no
equivalent for: a `DeployStatus` carries no command, no creation time and no
port bindings, because the deployment is a spec applied to a backend that may
not have the concept at all (on kubernetes the ports belong to a Service and
the creation time to a ReplicaSet). What cornus prints instead is `SERVICE`
first — the Compose identity you actually look things up by — with `NAME` as
the backend resource it maps to.

For scripting, use the outputs that promise stability rather than the column
set: `--format json` (every field, machine-readable), `--quiet` (resource ids)
and `--services` (service names).

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
| `--index` | — | — | Stream only this replica of each selected service, 1-based like `docker compose logs --index`. Reads the live runtime; mutually exclusive with `--all-replicas`, `--from=store`, `--match` and `--severity`. An index past the service's replica count is refused with the valid range. |
| `--all-replicas` | — | `false` | Stream every instance of a scaled service, not just the first. Each line is tagged with its replica ordinal. |
| `--from` | — | `auto` | Where to read from: `auto`, `runtime`, or `store`. See below. |
| `--match` | — | — | Only show lines containing this text. Implies `--from=store`. |
| `--severity` | — | — | Only show records at or above `debug`, `info`, `warn`, `error`, or `fatal`. Implies `--from=store`. |

Note: there is no short `-f` for `--follow`, because the `compose` group already
owns `-f` for `--file` — spell `--follow` in full. A `cornus compose logs -f web`
pasted from a docker habit reads `web` as a Compose file name, so that failure
explains what `-f` means here instead of only reporting a missing file.

There is no per-command `--no-color` either: cornus's
[global `--no-color`](/cli/) is available on every subcommand, so
`cornus compose logs --no-color` works exactly as the docker per-command flag
does.

### Reading recorded logs

When the server runs with [`--obs`](/guides/observability#the-built-in-store) it
records every workload's output, so logs outlive the container that produced
them. `--from` selects the source:

| Value | Reads from |
| --- | --- |
| `auto` (default) | The live runtime, falling back to the store only when the runtime produced nothing and failed — so it never returns fewer lines than `runtime` would. |
| `runtime` | Only the live container output, exactly as before. |
| `store` | Only the recorded history, which survives `compose down`. |

```sh
# Still answerable after the containers are gone
cornus compose down
cornus compose logs web --from=store --since 1h

# Searching and level filtering need the store: a live byte stream has no records
cornus compose logs web --match "connection refused"
cornus compose logs web --severity error
```

`--follow` tails the live runtime and cannot be combined with `--from=store`;
`--match` / `--severity` cannot be combined with `--from=runtime`. Both
combinations are refused with an explanation rather than silently resolved.

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
| `--pull` | — | `false` | Always attempt to pull a newer version of each base image. OR'd with each service's `build.pull`, so it can turn pulling on for a build that did not ask for it, never off for one that did. |
| `--push` | — | `false` | Already the default: every `cornus compose build` pushes to the cornus registry. The flag only prints where the images went — see [Docker Compose compatibility](#docker-compose-compatibility). |
| `-q`, `--quiet` | — | `false` | Do not print build progress. Failures are still reported in full. |

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

`restart` and `stop` also take `-t`/`--timeout` for `docker compose`
compatibility. It is **not honored** — it warns and continues; see
[Docker Compose compatibility](#docker-compose-compatibility).

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

## Docker Compose compatibility

No flag is ever accepted silently when it does nothing: one that cannot be
honored says so on stderr before the command does its work, and names what to
do instead. The flags where cornus and `docker compose` are not simply
identical fall into three groups.

### Implemented

| Flag | Behavior |
| --- | --- |
| `up --no-deps` | Brings up only the services you named, skipping the `depends_on` expansion that `up` now does by default. |
| `up --force-recreate` | Replaces the workloads even when their spec is unchanged. It works by stamping one label with a token fixed for the life of the `cornus` process. On dockerhost that label is part of the fingerprint the backend compares against the running container, so it forces the recreate an unchanged spec would otherwise skip; on kubernetes the label lands in the pod template's annotations, so the Deployment rolls a fresh ReplicaSet — the same mechanism as `kubectl rollout restart`. The containerd, bare and incus backends recreate on every `up` anyway. Because the token is per process, a `up --watch --force-recreate` reload does not re-roll every service on every file save. |
| `logs --index` | Streams one replica of a scaled service, 1-based as in docker. |
| `build --pull` | Re-resolves each base image, OR'd with the service's `build.pull`. |
| `build -q`/`--quiet` | Suppresses build progress rendering only. A failed build still reports its error. |

### Accepted for compatibility, but not honored

These are taken so a command line copied from `docker compose` still runs, and
each warns once, naming why and what replaces it.

| Flag | Why, and what to use instead |
| --- | --- |
| `-t`, `--timeout` on `up`, `down`, `stop`, `restart` | The cornus deploy API carries no per-call shutdown timeout — the server owns lifecycle timing. The grace period is a property of the service: set `stop_grace_period:` in the Compose file, which the backend applies as the container stop timeout / the pod's `terminationGracePeriodSeconds`. |
| `down --rmi=local\|all` | Nothing in the stack can delete an image: the deploy backends expose only workloads and volumes, and built images live in the cornus registry on the server. The teardown you asked for still happens; use [`cornus storage`](/cli/storage) to see what the server holds, and reclaim image space on the backend host itself. A value other than `local` or `all` is rejected before the project is even loaded. |
| `build --push` | Already unconditionally on: every compose build pushes, because the deploy pulls the image back from the registry. It is noted rather than swallowed because the meanings differ — docker pushes to the registry named in the image tag, cornus always pushes to **its** registry, and the note prints which host that was. |

### Deliberate divergences

| Difference | Detail |
| --- | --- |
| `logs` has no `-f` short | The `compose` group owns `-f`/`--file` for every subcommand, and it cannot be shadowed per command. Spell `logs --follow`. `logs -f web` explains itself rather than failing with a bare "no such file". |
| `up --no-attach` is a boolean | In docker it takes a service name and leaves that one service unattached while the whole project comes up. Here it is a project-wide switch, and the positional arguments select which services to bring up — so `up --no-attach web` brings up only `web`. Combining the two warns about exactly that. |
| `ps` prints different columns | `SERVICE`/`NAME`/`IMAGE`/`STATUS` instead of docker's set; `COMMAND`, `CREATED` and `PORTS` have no equivalent in a deployment's status and no meaning on kubernetes. Script against `--format json`, `--quiet` or `--services`. |
| `--no-color` is global | cornus declares it once on the root command and every subcommand inherits it, so `compose logs --no-color` behaves as docker's per-command flag does. |

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
