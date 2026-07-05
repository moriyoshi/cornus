# Compose extensions and compatibility

Cornus reads ordinary Compose files. This guide covers the two places it is not
ordinary: the `x-cornus-*` extension fields it understands and the compose-spec
`provider:` services it delegates, and the handful of `docker compose` flags
where it deliberately behaves differently. The flag-by-flag reference is
[`cornus compose`](/cli/compose); recipes for running projects are in
[Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker).

## How it works

A Compose file is discovered, merged and interpolated the way `docker compose`
does it, and each service becomes a cornus deployment. Everything the Compose
specification defines keeps its meaning. Cornus adds two things on top:

- **`x-cornus-*` extension fields**, for capabilities the specification has no
  place for — brokering a credential minted on your machine, routing a
  container's outbound traffic through your network, exporting its telemetry.
- **Its own answer where a `docker compose` flag does not map onto a deploy
  API.** No flag is ever accepted silently when it does nothing: one that cannot
  be honored says so on stderr before the command does its work, and names what
  to do instead. See [Docker Compose compatibility](#docker-compose-compatibility).

### The extension fields

| Field | What it declares | Documented in |
| --- | --- | --- |
| `x-cornus-shells:` | Interactive shells the service's image has, most preferred first | [below](#interactive-shell-candidates) |
| `x-cornus-credentials:` | A credential minted on your machine and brokered into the service | [Credentials](/guides/credentials#from-a-compose-file) |
| `x-cornus-egress:` | Outbound traffic routed through the caller's network | [Egress](/guides/egress) |
| `x-cornus-ingress:` | A public HTTP(S) hostname for the service | [Ingress](/guides/ingress) |
| `x-cornus-telemetry:` | OpenTelemetry export for the service's signals | [Observability](/guides/observability) |
| `x-cornus-agent-forward:` | Permission for `exec --forward-agent` into this service | [`cornus exec`](/cli/exec) |

Most of these also take a **project-level** block, which supplies the default for
every service that declares none; a service block then overrides it **wholesale**
rather than merging field by field, because the block is a single preference and
not a set of independent keys. That is the rule for `x-cornus-shells:`,
`x-cornus-credentials:`, `x-cornus-egress:` and `x-cornus-telemetry:`.

`x-cornus-ingress:` is the exception, and deliberately so: a project-level block
does **not** turn ingress on anywhere. Ingress stays opt-in per service, and the
project block field-merges its domain, class and TLS issuer into the services
that already opted in, with a service's own value winning. Otherwise one
project-wide default would publish every service in the stack.
`x-cornus-agent-forward:` has no project-level form at all.

## Interactive shell candidates

`x-cornus-shells:` lists the interactive shells a service's image has, most
preferred first. It is read by the
[`cornus web`](/guides/web-ui#terminal-shell-discovery) terminal, which probes
these before its own candidate list, so a service whose image ships an unusual
shell opens in it without anyone configuring a browser.

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

It changes nothing about the deployment. No deploy backend reads it, it is not
part of the spec the backend compares against a running container, and editing it
therefore never recreates anything.

## Provider services

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

## Auto-reload on edit

With [`up --watch`](/cli/compose#cornus-compose-up), `up` keeps watching every
file the project loaded from — the compose file(s), the sibling `.env` or
`--env-file` entries, each service's `env_file:`, and any `include:` / `extends`
targets. When you edit and save any of them, the configuration is reloaded in
full and the running project is re-reconciled toward the new desired state: a
service whose spec changed is recreated, a service you added is started, and a
service you removed is torn down. Unchanged services are left running.

```sh
cornus compose up --watch        # foreground
cornus compose up -d --watch     # held by the background agent
```

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

## Docker Compose compatibility

The flags where cornus and `docker compose` are not simply identical fall into
three groups.

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
| `ps` prints different columns | `SERVICE`/`NAME`/`IMAGE`/`STATUS` instead of docker's `NAME IMAGE COMMAND SERVICE CREATED STATUS PORTS`. Three of docker's columns describe a local container that a cornus deployment has no equivalent for: a `DeployStatus` carries no command, no creation time and no port bindings, because the deployment is a spec applied to a backend that may not have the concept at all (on kubernetes the ports belong to a Service and the creation time to a ReplicaSet). `SERVICE` comes first — the Compose identity you actually look things up by — with `NAME` as the backend resource it maps to. Script against `--format json`, `--quiet` or `--services`, which promise stability where the column set does not. |
| `--no-color` is global | cornus declares it once on the root command and every subcommand inherits it, so `compose logs --no-color` behaves as docker's per-command flag does. |

**See also:** [`cornus compose`](/cli/compose),
[Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker),
[Credentials](/guides/credentials), [Egress](/guides/egress),
[deploy spec reference](/reference/deploy-spec)
