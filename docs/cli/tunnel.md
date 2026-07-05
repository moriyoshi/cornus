# cornus tunnel

Expose a deployment's port to the public internet through a server-hosted
tunnel, so the running application can be reached from anywhere.

## Synopsis

```sh
cornus tunnel [flags] <name> <port>
```

## Description

`cornus tunnel` asks a cornus server to host a public tunnel to a deployment's
port, useful for sharing in-progress work or receiving webhooks. The server
hosts the tunnel in-process and bridges it to the workload, so — like
[`cornus port-forward`](/cli/port-forward) — the tunnel reaches ports the
workload never published, on any backend, but with a public URL instead of a
local listener.

The tunnel backend is chosen on the **server** with `CORNUS_TUNNEL_BACKEND`
(default `ngrok`); the other backends are `ssh` (SSH reverse-tunneling),
`cloudflare` (Cloudflare Tunnel), and `tailscale` (Tailscale Funnel). See
[Public tunnels — Backends](/topics/tunnels) for what each needs, and the
[Tunnels guide](/guides/tunnels) for step-by-step setup per backend.

Any per-tunnel credential is injected into the server's already-authenticated
endpoint (the server cannot know it beforehand). What the credential *is*
depends on the backend: an ngrok authtoken for `ngrok`, an SSH private key
(PEM) or password for `ssh`, and nothing for `cloudflare` / `tailscale` (they
are anonymous / joined out-of-band). Supply it with the `CORNUS_TUNNEL_AUTHTOKEN`
environment variable (read into `--authtoken` automatically; `NGROK_AUTHTOKEN`
also still works, as a legacy alias) or `--authtoken-file` — both keep the
secret out of argv and shell history, unlike `--authtoken` itself. Best of
all, omit it entirely when the **server** has a default credential (also set
via `CORNUS_TUNNEL_AUTHTOKEN`, but in the server's own environment — the same
variable name means different things depending on which process reads it).

For the `ssh` backend specifically, `--forward-agent` is a further
alternative: it forwards the caller's local `ssh-agent` to the server's SSH
handshake instead of sending any key material at all, and is the only way to
authenticate with a passphrase-protected key. Like `ssh -A`, only use it
against a cornus server you trust — see the
[Tunnels guide](/guides/tunnels#expose-a-workload-over-ssh-reverse-forwarding)
for how it works and its trust model.

The command prints the public URL and keeps the tunnel up until `Ctrl-C` (or
`SIGTERM`), which tears it down.

The port must be in `1..65535`. The connection is resolved from `--server`,
otherwise from the selected connection profile (see
[`cornus config`](/cli/config)). See
[Public tunnels](/topics/tunnels) for the broader picture.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | Remote cornus server URL (`http(s)://` or `ws(s)://`). Falls back to the selected connection profile. |
| `--authtoken` | `CORNUS_TUNNEL_AUTHTOKEN` (also `NGROK_AUTHTOKEN` as a legacy alias) | — | Tunnel-backend credential (for example an ngrok authtoken). Injected into the server; omit only if the server has a default credential. Puts the secret in argv/history — prefer the env var (which populates this flag automatically with no argv exposure) or `--authtoken-file`. |
| `--authtoken-file` | — | — | Read the credential from this file instead of `--authtoken`, keeping it out of argv and shell history. Mutually exclusive with `--authtoken`. |
| `--forward-agent` | — | `false` | Forward the local ssh-agent (`SSH_AUTH_SOCK`) to the server so the `ssh` backend can authenticate with agent-held keys. Only supported by the `ssh` backend; only use against a trusted server. |
| `--proto` | — | `http` | Exposed protocol: `http` or `tcp`. |

Positional arguments:

- `<name>` — deployment name to expose (required).
- `<port>` — container port to expose through the tunnel (required).

## Examples

Expose container port 80 of the `web` deployment over HTTP, with the
authtoken read from the environment (no `--authtoken` needed on the command
line):

```sh
export CORNUS_TUNNEL_AUTHTOKEN=2ab3...
cornus tunnel web 80
```

Expose a raw TCP port, with the credential read from a file instead of argv:

```sh
cornus tunnel --proto tcp --authtoken-file ~/.config/cornus/ngrok-token db 5432
```

Rely on the server's default credential (no credential at all on the client):

```sh
cornus tunnel web 8080
```

Authenticate to an `ssh` backend relay using a forwarded ssh-agent instead of
a raw key or password (only against a trusted server):

```sh
cornus tunnel --forward-agent web 80
```
