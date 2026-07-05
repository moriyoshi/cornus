# Tunnels

A **public tunnel** (`cornus tunnel`) exposes one workload port to the public
internet through a hosted relay, with no cluster-native Ingress resource and no
published port required on the network path. Use it to share work in progress,
receive webhooks, or test on a phone. For a persistent hostname backed by a real
Ingress resource instead of a hosted relay, see [Ingress](/guides/ingress).

## How it works

`cornus tunnel <name> <port>` hands back a **public https URL** for a workload
port and stays up until `Ctrl-C`. The Cornus **server** hosts the tunnel and
bridges each inbound connection to the workload through the same byte-bridge
port-forward uses, so it reaches a port the workload never published, on any
backend (Docker host, containerd, or Kubernetes).

```sh
cornus tunnel [--authtoken TOKEN | --authtoken-file FILE] [--proto http|tcp] <name> <port>
```

```sh
cornus tunnel --server http://cornus.example:5000 \
  --authtoken "$NGROK_AUTHTOKEN" web 80
```

The tunnel credential is injected by the client on the already-authenticated
request; the server never knows it beforehand. An operator can instead set
`CORNUS_TUNNEL_AUTHTOKEN` on the server as a default credential, letting callers
omit `--authtoken`. Choose HTTP or raw TCP with `--proto http` / `--proto tcp`.
See [`cornus tunnel`](/cli/tunnel) for the full flag set.

## Backends

The tunnel backend is chosen on the server with `CORNUS_TUNNEL_BACKEND` (default
`ngrok`). The concrete backends are pluggable and only the selected one is
active. All four share the same client command; only the server-side
`CORNUS_TUNNEL_BACKEND` and its per-backend environment variables change.

| Backend | Injected credential | Notes |
| --- | --- | --- |
| `ngrok` (default) | an ngrok authtoken (`NGROK_AUTHTOKEN`) | in-process ngrok agent, no subprocess |
| `ssh` | an SSH private key (PEM), a password, or a forwarded ssh-agent (`--forward-agent`) | SSH remote-forward to a self-hostable tunnel server (sish, serveo, pinggy, localhost.run, plain `sshd` with GatewayPorts); reuses the in-binary SSH stack |
| `cloudflare` | none (anonymous) | Cloudflare quick tunnel via the `cloudflared` binary (`CORNUS_TUNNEL_CLOUDFLARED_BIN`) |
| `tailscale` | none | Tailscale Funnel via the `tailscale` binary; the node joins the tailnet out-of-band, so one Funnel per node |

For the `ssh` backend, configure the endpoint with `CORNUS_TUNNEL_SSH_ADDR` /
`CORNUS_TUNNEL_SSH_USER` and host-key verification with
`CORNUS_TUNNEL_SSH_KNOWN_HOSTS` or `CORNUS_TUNNEL_SSH_HOSTKEY` (fail-closed;
`CORNUS_TUNNEL_SSH_INSECURE=1` for dev only). The full environment-variable
reference is in [Server env vars](/reference/server-env-vars).

## Passing the credential safely

`--authtoken TOKEN` puts the secret directly in argv, which any other user on
the machine can read via `ps` and which shells often write to history — avoid it
for anything but a quick local test. Prefer, in order: no credential at all (the
server has a default — see below), the backend's env var (kong reads it into
`--authtoken` automatically, so the value never appears as a command-line
argument), or `--authtoken-file FILE` (reads the secret from a file, keeping it
out of both argv and history). The recipes below use the env-var / file forms
throughout.

## Expose a workload with ngrok (default)

The default backend — no extra binary to install and no server-side network
setup beyond the authtoken.

1. Sign in at [ngrok.com](https://ngrok.com) and copy your authtoken from the
   dashboard's "Your Authtoken" page.
2. Supply the token either per client call or as a server-side default:
   ```sh
   # client-side, per call: exported once, read automatically — cornus never
   # needs --authtoken on the command line when this is set.
   export CORNUS_TUNNEL_AUTHTOKEN=2ab3...
   cornus tunnel web 80
   ```
   Or set the *same variable name* as a server-side default (systemd unit,
   container env, Helm `values.yaml`, wherever the server process gets its
   environment) so clients need no credential at all — it's the same name in
   two different processes' environments, not one shared value:
   ```
   CORNUS_TUNNEL_AUTHTOKEN=2ab3...
   ```
   `NGROK_AUTHTOKEN` still works too, as a legacy alias, on the client side.
3. cornus prints the public `https://<random>.ngrok-free.app` URL and blocks
   until `Ctrl-C`, which tears the tunnel down.

- `CORNUS_TUNNEL_BACKEND` already defaults to `ngrok`, so no server-side
  backend selection is needed.
- The ngrok agent runs in-process on the server; there is nothing to install.
- A free ngrok account gets a new random subdomain each run; a paid plan can
  pin a stable one.

**See also:** [cornus tunnel](/cli/tunnel)

## Expose a workload over SSH reverse-forwarding

Reuses cornus's in-binary SSH stack against any endpoint that accepts an SSH
remote-forward (`ssh -R`) — a self-hosted relay (sish, a plain `sshd` with
`GatewayPorts yes`) or a public one (serveo.net, pinggy.io, localhost.run).

1. Pick or stand up an SSH tunnel endpoint that accepts `ssh -R`.
2. Point the server at it by setting these in the server's environment
   (systemd unit, container env, Helm `values.yaml`):
   ```
   CORNUS_TUNNEL_BACKEND=ssh
   CORNUS_TUNNEL_SSH_ADDR=tunnel.example.com:22
   CORNUS_TUNNEL_SSH_USER=cornus
   ```
   `CORNUS_TUNNEL_SSH_USER` defaults to `cornus` if unset;
   `CORNUS_TUNNEL_SSH_BIND` defaults to `0.0.0.0:0` (let the remote end pick a
   port).
3. Configure host-key verification. This backend fails closed — one of these
   is required:
   ```sh
   CORNUS_TUNNEL_SSH_KNOWN_HOSTS=/etc/cornus/known_hosts
   # or pin a single key:
   CORNUS_TUNNEL_SSH_HOSTKEY="ssh-ed25519 AAAA... tunnel.example.com"
   # dev only, skips verification entirely:
   CORNUS_TUNNEL_SSH_INSECURE=1
   ```
4. Tell cornus how to derive the public URL. If the relay prints its own URL
   in the SSH session banner (sish, serveo, pinggy do this), pick it up
   automatically:
   ```sh
   CORNUS_TUNNEL_SSH_URL_FROM_SESSION=1
   ```
   Otherwise template it from the bound remote port:
   ```sh
   CORNUS_TUNNEL_SSH_URL_TEMPLATE='https://{port}.tunnel.example.com'
   ```
5. Supply the SSH credential, one of two ways:

   - **A shared server-side identity** — an unencrypted private key PEM or a
     password, whichever the relay accepts. Since the relay's SSH handshake
     happens on the **server**, not the client, this is usually one shared
     service identity for the whole cornus server rather than a per-caller
     one, so set it once as the server-side default and let clients omit a
     credential entirely:
     ```
     CORNUS_TUNNEL_AUTHTOKEN=<PEM contents, or a password>
     ```
     For a genuinely per-caller credential, read it from a file client-side
     instead of putting it in argv:
     ```sh
     cornus tunnel --authtoken-file ~/.ssh/id_ed25519 web 80
     ```
   - **A forwarded ssh-agent** — the key material never leaves the client at
     all; the server's SSH handshake asks the caller's local `ssh-agent` to
     sign the challenge instead:
     ```sh
     cornus tunnel --forward-agent web 80
     ```
     This is the only way to authenticate with a passphrase-protected key,
     since the agent (not cornus) holds it decrypted. Like `ssh -A`, only use
     `--forward-agent` against a cornus server you trust: while the tunnel is
     starting, the server can ask the forwarded agent to sign arbitrary
     challenges, not only ones from the relay. cornus only consults the agent
     during the SSH handshake itself, not for the tunnel's whole lifetime.

- Passphrase-protected private keys handed directly as `--authtoken` are not
  supported — use `--forward-agent` for those, or fall back to an unencrypted
  key or a password.
- With no known-hosts file, no pinned host key, and no insecure opt-in, the
  connection is refused rather than trusting an unverified host.

**See also:** [cornus tunnel](/cli/tunnel), [Server env vars](/reference/server-env-vars)

## Expose a workload with Cloudflare Tunnel

An anonymous Cloudflare "quick tunnel" — no Cloudflare account, API token, or
DNS zone required. This backend shells out to the `cloudflared` binary, which
the published cornus image does not bundle — build a custom image on top of
it if the server runs as a container:

```dockerfile
FROM ghcr.io/moriyoshi/cornus:latest
RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && curl -fsSL -o /usr/local/bin/cloudflared \
         https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
    && chmod +x /usr/local/bin/cloudflared \
    && apt-get purge -y curl && rm -rf /var/lib/apt/lists/*
```

Deploy that image in place of the stock one (update the Helm `image.repository`
/ `image.tag` values or the k8s manifest). Running the server directly on a
host instead of as a container just needs `cloudflared` installed on that host.

1. Install `cloudflared` on the server host — via the custom image above, or
   directly on the host if the server isn't containerized.
2. If it isn't on `PATH`, point cornus at it in the server's environment:
   ```
   CORNUS_TUNNEL_CLOUDFLARED_BIN=/usr/local/bin/cloudflared
   ```
3. Select the backend on the server:
   ```
   CORNUS_TUNNEL_BACKEND=cloudflare
   ```
4. Run the tunnel — no `--authtoken` needed, the backend is anonymous:
   ```sh
   cornus tunnel web 80
   ```
5. cornus prints a `https://<random-words>.trycloudflare.com` URL.

- Quick tunnels are ephemeral: the hostname changes every run. Named tunnels
  (a stable hostname on your own domain, via a Cloudflare account token) are
  not supported yet.

**See also:** [cornus tunnel](/cli/tunnel)

## Expose a workload with Tailscale Funnel

Publishes through a node already joined to your tailnet — no cornus-managed
credential at all; the node's tailnet membership is the authorization. This
backend shells out to the `tailscale` binary, which the published cornus
image does not bundle. `sudo tailscale up` is an interactive command meant for
a long-lived host — it isn't something you can run by hand against an
ephemeral pod, so the two deployment shapes need different setups.

### On Kubernetes, via the Helm chart

The chart can run `tailscaled` as a sidecar that joins the tailnet
unattended and shares the `tailscale` CLI binary with the cornus container —
no custom image, no manual `tailscale up`.

1. Create a tailnet auth key in the Tailscale admin console — **reusable**
   and, ideally, **ephemeral**-tagged, since the sidecar's state isn't
   persisted across pod restarts and an ephemeral node deregisters itself
   when it disconnects instead of accumulating in the tailnet:
   ```sh
   kubectl create secret generic cornus-tailscale-authkey \
     --from-literal=authkey=tskey-auth-...
   ```
2. In the Tailscale admin console, enable HTTPS certificates for the tailnet
   (**DNS → Enable HTTPS**), and grant the node the Funnel attribute via the
   tailnet ACL policy (a `nodeAttrs` entry with the `funnel` attribute — see
   Tailscale's Funnel documentation for the exact ACL snippet).
3. Enable the sidecar in `values.yaml` (or `--set`):
   ```yaml
   tailscale:
     enabled: true
     authKeySecret: cornus-tailscale-authkey
   ```
   This sets `CORNUS_TUNNEL_BACKEND`, `CORNUS_TUNNEL_TAILSCALE_BIN`, and
   `TS_SOCKET` on the cornus container for you — see the chart's
   `values.yaml` "tailscale" block for the full set of knobs (hostname,
   image, extra `tailscale up` args).
4. Run the tunnel — no `--authtoken` needed:
   ```sh
   cornus tunnel web 80
   ```
5. cornus prints the node's public `https://<node>.ts.net/` URL.

### Anywhere else: a plain host, or a container outside the Helm chart

This backend shells out to the `tailscale` binary, which the published
`ghcr.io/moriyoshi/cornus:latest` image does not bundle. If the server runs as
a container outside the Helm chart (a bare `docker run`, a hand-written k8s
manifest, `docker compose`), build a custom image layering it on:

```dockerfile
FROM ghcr.io/moriyoshi/cornus:latest
RUN apt-get update && apt-get install -y --no-install-recommends curl gnupg \
    && curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg \
         -o /usr/share/keyrings/tailscale-archive-keyring.gpg \
    && curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list \
         -o /etc/apt/sources.list.d/tailscale.list \
    && apt-get update && apt-get install -y --no-install-recommends tailscale \
    && apt-get purge -y curl gnupg && rm -rf /var/lib/apt/lists/*
```

Run `tailscaled` alongside it (a sidecar container sharing the pod/host
network namespace, or a second process in the same container) and deploy the
custom image in place of the stock one. Running the server directly on a
plain host needs Tailscale installed on that host instead — no custom image.

1. Install Tailscale — via the custom image above, or directly on the host if
   the server isn't containerized — and join it to your tailnet:
   ```sh
   sudo tailscale up
   ```
2. Follow the same Tailscale admin console step above (enable HTTPS
   certificates, grant the Funnel attribute).
3. If `tailscale` isn't on `PATH`, point cornus at it in the server's
   environment:
   ```
   CORNUS_TUNNEL_TAILSCALE_BIN=/usr/bin/tailscale
   ```
4. Select the backend on the server:
   ```
   CORNUS_TUNNEL_BACKEND=tailscale
   ```
5. Run the tunnel — no `--authtoken` needed:
   ```sh
   cornus tunnel web 80
   ```
6. cornus prints the node's public `https://<node>.ts.net/` URL.

- A node serves only one Funnel on port 443 at a time, so concurrent tunnels
  on the same server host conflict — a Tailscale Funnel limitation, not a
  cornus one.
- The URL is reachable by anyone on the internet by default; restrict access
  with Tailscale ACLs if that isn't what you want.

**See also:** [cornus tunnel](/cli/tunnel), [Helm chart values](/reference/helm-values)
