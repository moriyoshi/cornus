# Remote container hosts over SSH

Reach a cornus server running directly on a remote **container host** through an
SSH tunnel — the host counterpart of [remote clusters](/guides/remote-clusters)
(which tunnels through the Kubernetes API instead). Once a context is
configured, ordinary commands (`deploy`, `compose`, `exec`, `build`, …) route
through the tunnel with no per-command flags.

The tunnel binds **no local port**: cornus dials the remote server over the SSH
connection directly, so nothing is left listening on your machine.

**The tunnel does not care which runtime the far end drives.** The transport
carries raw bytes to a cornus server; what that server deploys onto — Docker,
containerd, an OCI runtime with no daemon, or Incus — is chosen on the *host*
with `CORNUS_DEPLOY_BACKEND` and changes nothing about the context you configure
here. What it does change is what has to be installed there; see
[Host prerequisites](#host-prerequisites).

To build this context interactively (choosing the SSH destination and remote
address, verifying the connection, and generating a systemd unit for the host),
run the [`cornus setup`](/cli/setup) wizard. Its four SSH scenarios — `ssh-docker`,
`ssh-containerd`, `ssh-bare`, and `ssh-incus` — ask identical questions and
differ only in the setup guide they show and the backend the generated unit
selects.

## Set up a context

If the host is already in your `~/.ssh/config`, name the alias and cornus reads
the rest (HostName, User, Port, IdentityFile, known_hosts, ProxyJump):

```sh
cornus config set-context devbox --ssh-host devbox
cornus config use-context devbox
cornus compose -f compose.yaml up -d   # runs on devbox, through the tunnel
```

Without an ssh_config entry, give the address and credentials explicitly:

```sh
cornus config set-context devbox \
  --ssh-host ssh.example.com:22 \
  --ssh-user ops \
  --ssh-identity-file ~/.ssh/id_ed25519
```

`cornus config get-contexts` shows an SSH-tunnel profile as
`(ssh-tunnel ops@ssh.example.com:22 -> 127.0.0.1:5000)`.

- `--ssh-remote-addr` is where the cornus server listens **from the remote host's
  view** (default `127.0.0.1:5000`). The tunnel exits on the remote host, so its
  own loopback reaches a server bound to the default `:5000`.
- Explicit `--ssh-*` flags override the ssh_config-resolved values; `--ssh-no-config`
  ignores ssh_config entirely.

## Host prerequisites

Everything above configures your side. On the remote host, `cornus serve` needs
whatever its chosen backend needs — and none of these fail at startup, so a
server that is missing one looks healthy until a deploy fails for reasons
several layers away:

| `CORNUS_DEPLOY_BACKEND` | What the remote host needs |
| --- | --- |
| unset (`dockerhost`) | The Docker socket (`/var/run/docker.sock`, or `CORNUS_DOCKER_SOCK`). |
| `containerd` | Root, a containerd socket, and the CNI plugins (`bridge`, `portmap`, `host-local`, `loopback`) in `/opt/cni/bin`. |
| `bare` | Root, an OCI runtime on `PATH` (`runc` by default; `CORNUS_BARE_RUNTIME` selects `crun`, `youki`, or `runsc`), and the same CNI plugins. No daemon of any kind. |
| `incus` | incusd **6.3+** reachable at `CORNUS_INCUS_SOCKET` (default `/var/lib/incus/unix.socket`), plus `skopeo` and `umoci` installed **on the daemon host** — incusd shells out to them to flatten the OCI image, so they are needed where incusd runs, not where cornus runs. |

Check a host before committing to it by running the same startup checks
`cornus serve` runs, on the host itself:

```sh
ssh devbox 'CORNUS_DEPLOY_BACKEND=bare cornus daemon preflight'
```

It exits non-zero on an environment `cornus serve` would refuse. For the full
capability and privilege model of each backend, see
[deploy backends](/reference/deploy-backends).

## Authentication

Authentication follows OpenSSH:

- The local **ssh-agent** is used by default. If your agent holds a key the host
  rejects and you hit "too many authentication failures", pass `--ssh-no-agent`.
- `--ssh-identity-file` adds an explicit key. A passphrase-protected key is
  prompted for **once**, on the first foreground connect — honoring `SSH_ASKPASS`
  / `SSH_ASKPASS_REQUIRE`, then the terminal. A reconnect never prompts. For a
  tunnel that survives drops unattended, load the key into your ssh-agent (the
  agent holds it decrypted; nothing decrypted is kept in cornus).
- Host-key verification is **fail-closed**: cornus uses your `known_hosts`
  (`--ssh-known-hosts`, or ssh_config's `UserKnownHostsFile`, or
  `~/.ssh/known_hosts`), or a key pinned with `--ssh-host-key`. `--ssh-insecure-host-key`
  disables the check (dev only).

## TLS through the tunnel

An SSH tunnel carries raw bytes, so if the remote server terminates TLS you can
dial it over HTTPS end-to-end with `--ssh-tls`. Because the endpoint is dialed as
`127.0.0.1:<port>` through the tunnel, tell cornus the certificate's real hostname
so verification matches:

```sh
cornus config set-context devbox --ssh-host devbox \
  --ssh-tls --tls-server-name cornus.internal.example.com
```

Alternatively supply a CA that trusts the presented cert (`--tls-ca-cert`), or
`--insecure-skip-verify` for development.

## Bastions and ProxyCommand

`ProxyJump` (bastion chains) is honored natively — set it in ssh_config on the
host alias and cornus dials each hop in-process:

```
Host devbox
  HostName 10.0.0.5
  User ops
  ProxyJump bastion.example.com
```

For `ProxyCommand` or `Match` blocks — which the in-process path does not
implement — cornus falls back to the system `ssh` binary, running one persistent
`ssh -N -L <unix-socket>:<remote>` and dialing that unix socket (still no local
TCP port). This is automatic when the host has a `ProxyCommand`, and can be forced
with `--ssh-use-binary`. It requires the `ssh` binary and is Linux/macOS only.

## Reconnection

If the SSH connection drops (network blip, sshd restart, host reboot), cornus
re-establishes it on demand, so a subsequent command transparently succeeds. A
command that is **mid-stream** when the link drops (`logs -f`, an interactive
`exec`, a running build) surfaces the drop as an error — rerun it once the link
is back.

## Registry note

If the remote host's registry is reachable only through the same SSH tunnel, set
an explicit `--registry` / `CORNUS_REGISTRY` the deploy target can pull from — the
node pulls images itself, not through your CLI's tunnel. See
[building images](/guides/building-images).

**See also:** [remote clusters](/guides/remote-clusters),
[cornus config](/cli/config), [deploy backends](/reference/deploy-backends).
