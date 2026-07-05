# cornus daemon

Long-running helper daemons: the client-side Docker Engine API proxy, the
client-side background agent status/stop controls, the host-environment
preflight, and the pod-facing sidecars.

## Synopsis

```sh
cornus daemon <subcommand> [flags]
```

## Description

`cornus daemon` groups helper processes. The end-user-facing subcommands are the
Docker Engine API proxy (`docker`), the background-agent controls (`status`,
`stop`), and the host-environment check (`preflight`). The remaining subcommands
are pod sidecars baked into generated pod specs, not run by hand. The cornus
server itself is [`cornus serve`](/cli/serve).

## cornus daemon docker

Run a local daemon that speaks a subset of the Docker Engine REST API on a unix
socket and translates container operations into cornus deploys against a remote
cornus server. Point `DOCKER_HOST` at its socket and stock `docker` runs
workloads on the remote cornus, with the caller's local bind-mount directories
streamed over 9P.

```sh
cornus daemon docker [flags]
```

The frontend is hosted by the single client-side background agent (spawned if
needed). A foreground run holds until `Ctrl-C`, then deregisters the frontend;
`-d`/`--daemon` registers it and returns, leaving the agent hosting it.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--host` | `CORNUS_HOST` | `http://localhost:5000` | Remote cornus server URL. Falls back to the selected connection profile, then the default. |
| `--socket` | `CORNUS_DOCKER_SOCK` | `$XDG_RUNTIME_DIR/cornus-docker.sock` | Unix socket to listen on. |
| `-d`, `--daemon` | — | `false` | Run in the background as a daemon (default: run in the foreground). |
| `--no-forward-ports` | — | `false` | Do not publish container ports (`docker -p`) on local listeners. |

Use this to drive stock `docker` / `docker compose` at a remote cornus server;
for the built-in Compose client see [`cornus compose`](/cli/compose), and for
the broader remote picture see [Working with remote clusters](/guides/remote-clusters).

::: warning Named-volume deletion
The proxy provisions named volumes through the selected deploy backend, but
`docker volume rm` only drops the name from this proxy process's memory; it does
not delete backend storage. `docker volume prune` and the volume phase of
`docker system prune` report nothing reclaimed and likewise leave backend
storage intact. Use Cornus's backend-aware volume lifecycle when the data must
be removed.
:::

## cornus daemon status

Show the running cornus client agent inventory (servers, projects, docker
frontends, and any conduit banners). Reports that no agent is running when there
is none.

```sh
cornus daemon status
```

## cornus daemon stop

Stop the running cornus client agent.

```sh
cornus daemon stop
```

## cornus daemon preflight

Check whether this process could actually drive the container runtime of the
configured deploy backend.

```sh
cornus daemon preflight
```

It runs the same detection and the same checks [`cornus serve`](/cli/serve) runs
at startup, so it answers for the real server rather than an approximation, and
it exits **non-zero** on a configuration the server would refuse to start on —
so an image smoke test or a CI job can gate on it.

The point is to run it *before* committing to a deployment: inside the container
image you are about to serve from, with the same mounts and the same environment,
while the binds are still cheap to change.

```
cornus runs in a container (a1b2c3d4e5f6) on a docker host; translating its paths for the runtime
  [ok  ] data-dir-host-visible: data dir /var/lib/cornus is /srv/cornus on the host
  [warn] client-local-mounts: client-local mounts unavailable: ...
           remedy: run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded
```

Each line is a check, its verdict (`ok`, `warn`, `fail`), what was found, and —
for anything needing action — the remedy. A `warn` means a capability is
unavailable but reports its own absence when something asks for it; a `fail`
means deploys would silently misbehave. `--output json` emits the same result as
a single object.

On a server running directly on the host, which needs none of this, the output is
one line saying so. See
[Running the server in a container](/guides/server-in-a-container).

## Pod sidecar and internal subcommands

These subcommands are not for end users. They exist because their spellings are
baked into generated pod specs or spawned by clients:

- `caretaker` — pod sidecar that runs the configured roles (9P mounts, hub,
  ...) until teardown.
- `caretaker-check` — sidecar readiness probe; exits 0 if every caretaker role
  is live.
- `net-redirect` — init container that iptables-redirects app egress into the
  caretaker proxy.

The hidden `mounts` and `agent` subcommands are internal to the client-side
background agent (spawned by clients such as `cornus compose up -d`, not run by
hand).

## Examples

Serve the Docker API proxy in the foreground and export `DOCKER_HOST`:

```sh
cornus daemon docker --host https://cornus.example.com:5000
export DOCKER_HOST=unix:///run/user/1000/cornus-docker.sock
docker run -d -v ./conf:/etc/app:ro nginx
```

Run the proxy detached on a custom socket:

```sh
cornus daemon docker -d --socket /run/cornus-docker.sock
```

Inspect and stop the background agent:

```sh
cornus daemon status
cornus daemon stop
```
