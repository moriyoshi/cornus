# cornus exec

Run a command inside a deployment's first instance via a remote cornus server
(docker exec).

## Synopsis

```sh
cornus exec [flags] <name> -- <cmd> [args...]
```

## Description

`cornus exec` creates and starts an exec against the remote cornus server,
bridging local stdio to the command running in the deployment's first instance.
Everything after the deployment name is passed to the command verbatim, so flags
like `-c` reach the command rather than cornus.

The server is chosen by `--server` / `CORNUS_SERVER`, falling back to the
selected connection profile (see [`cornus config`](/cli/config)).

With `-i` local stdin is forwarded to the command. With `-t` cornus requests a
pseudo-TTY, but only when stdin is itself a terminal: a piped or CI invocation
degrades to a plain stream with a warning instead of a server PTY the client
cannot drive. In TTY mode the local terminal is driven in raw mode and window
resizes are forwarded.

cornus propagates the remote command's exit code as its own. If the command
finished but its exit status could not be retrieved (an inspect failure), cornus
exits `125`, matching docker's convention for "the command ran but the tooling
could not complete".

`--forward-agent` forwards the local ssh-agent (`SSH_AUTH_SOCK`) into the exec
session, so a command like `ssh` run inside it can use agent-held keys. It rides
a caretaker `AgentRelayRole`, available two ways depending on the backend:

- **dockerhost/containerdhost**: works against a remote-mode backend
  (`CORNUS_DOCKER_REMOTE` / `CORNUS_CONTAINERD_REMOTE` — see
  [deploy backends](/reference/deploy-backends)), since it rides the same
  always-on companion sidecar that mode already provisions per instance. Against
  a co-located (non-remote) backend it is rejected with a clear error.
- **kubernetes**: works only against a deployment applied with `agentForward`
  set in its [DeploySpec](/reference/deploy-spec) (a Compose service sets this
  with `x-cornus-agent-forward: true`) — an explicit per-deployment opt-in,
  since kubernetes has no backend-wide "remote mode" and running a caretaker
  sidecar for every deployment just for this would be wasteful. Against a
  deployment applied without it, it is rejected with a clear error.

Like `ssh -A`, only use it against a cornus server you trust: while the exec
session is open, the server can ask the forwarded agent to sign arbitrary
challenges, not only ones the exec'd command itself issued.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | selected profile | Remote cornus server URL (`http(s)://` or `ws(s)://`). Falls back to the selected connection profile. |
| `-i`, `--interactive` | — | `false` | Keep stdin open and forward it to the command. |
| `-t`, `--tty` | — | `false` | Allocate a pseudo-TTY (downgraded to a plain stream when stdin is not a terminal). |
| `--forward-agent` | — | `false` | Forward the local ssh-agent into the exec session (remote-mode dockerhost/containerdhost, or kubernetes with `agentForward` set on the deployment). |
| `name` (positional) | — | required | Deployment name to exec into. |
| `cmd...` (positional) | — | required | Command and arguments to run (passed verbatim). |

## Examples

Run a one-off command:

```sh
cornus exec myapp -- ls -la /app
```

Open an interactive shell:

```sh
cornus exec -it myapp -- sh
```

Target an explicit server:

```sh
cornus exec --server https://cornus.example.com myapp -- env
```

## See also

- [`cornus deploy`](/cli/deploy)
- [`cornus config`](/cli/config)
- [Working with remote clusters](/guides/remote-clusters)
