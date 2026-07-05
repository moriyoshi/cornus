# Public tunnels

A **public tunnel** (`cornus tunnel`) exposes one workload port to the public
internet through a hosted relay, with no cluster-native Ingress resource and no
published port required on the network path. Use it to share work in
progress, receive webhooks, or test on a phone. For a persistent hostname
backed by a real Ingress resource instead of a hosted relay, see
[Ingress](/topics/ingress).

`cornus tunnel <name> <port>` hands back a **public https URL** for a workload
port and stays up until `Ctrl-C`. The Cornus **server** hosts the tunnel and
bridges each inbound connection to the workload through the same byte-bridge
port-forward uses, so it reaches a port the workload never published, on any
backend (Docker host, containerd, or Kubernetes).

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
active.

| Backend | Injected credential | Notes |
| --- | --- | --- |
| `ngrok` (default) | an ngrok authtoken (`NGROK_AUTHTOKEN`) | in-process ngrok agent, no subprocess |
| `ssh` | an SSH private key (PEM), a password, or a forwarded ssh-agent (`--forward-agent`) | SSH remote-forward to a self-hostable tunnel server (sish, serveo, pinggy, localhost.run, plain `sshd` with GatewayPorts); reuses the in-binary SSH stack |
| `cloudflare` | none (anonymous) | Cloudflare quick tunnel via the `cloudflared` binary (`CORNUS_TUNNEL_CLOUDFLARED_BIN`) |
| `tailscale` | none | Tailscale Funnel via the `tailscale` binary; the node joins the tailnet out-of-band, so one Funnel per node |

For the `ssh` backend, configure the endpoint with `CORNUS_TUNNEL_SSH_ADDR` /
`CORNUS_TUNNEL_SSH_USER` and host-key verification with
`CORNUS_TUNNEL_SSH_KNOWN_HOSTS` or `CORNUS_TUNNEL_SSH_HOSTKEY` (fail-closed;
`CORNUS_TUNNEL_SSH_INSECURE=1` for dev only).

For step-by-step setup instructions for each backend, see the
[Tunnels guide](/guides/tunnels). The full environment-variable reference is in
[Server env vars](/reference/server-env-vars).
