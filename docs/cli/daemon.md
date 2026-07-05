# cornus daemon

Long-running helper daemons: the client-side Docker Engine API proxy, the
client-side background agent status/stop controls, and the pod-facing sidecars.

## Synopsis

```sh
cornus daemon <subcommand> [flags]
```

## Description

`cornus daemon` groups helper processes. The end-user-facing subcommands are the
Docker Engine API proxy (`docker`) and the background-agent controls (`status`,
`stop`). The remaining subcommands are pod sidecars baked into generated pod
specs, not run by hand. The cornus server itself is
[`cornus serve`](/cli/serve).

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
