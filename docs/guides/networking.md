# Networking recipes

Task-oriented recipes for reaching workloads: per-port forwards, a SOCKS5
split-tunnel, and the workload-to-workload hub. For exposing a workload
publicly through a hosted tunnel, see the [Tunnels guide](/guides/tunnels).
For the model behind these recipes see [remote workflows](/topics/remote-workflows)
and [the workload hub](/topics/hub).

## Forward local ports to a workload

Bind a local listener per mapping and forward each connection to the first instance of a deployment, reaching ports that were never published.

```sh
cornus port-forward web 8080:80 5432:5432
```

- Each mapping is `LOCAL:REMOTE` (or a bare `PORT`), optionally with a `/tcp` or `/udp` suffix, e.g. `cornus port-forward dns 5353:53/udp`.
- `--address 0.0.0.0` binds all interfaces; UDP works on the dockerhost/containerd/bare backends but Kubernetes port-forward is TCP-only.

**See also:** [cornus port-forward](/cli/port-forward), [remote workflows](/topics/remote-workflows)

## Run a SOCKS5 split-tunnel proxy to reach services by name

Bind a local SOCKS5 proxy that tunnels service-suffixed hosts into the cluster and dials everything else directly.

```sh
cornus socks5
curl --socks5-hostname 127.0.0.1:1080 http://web.cornus.internal/
```

- Any host ending in `--service-host-suffix` (default `.cornus.internal`) is tunneled to the matching service; the suffix is stripped to derive the service name.
- `--resolve 'PATTERN=REPLACE'` is the advanced form (ordered, first match wins, sed-style `\1` backreferences) and replaces the suffix default.

**See also:** [cornus socks5](/cli/socks5), [remote workflows](/topics/remote-workflows)

## Choose a conduit for a deploy or compose session

Pick how a `--server` session exposes workload ports to you: per-port listeners or one SOCKS5 proxy.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --conduit socks5
cornus compose up --conduit port-forward
```

- Precedence is `--conduit`, then `CORNUS_CONDUIT`, then the profile mode; `--no-forward-ports` disables the conduit entirely.
- A bare word sets only the mode; a `socks5://host:port[?suffix=SUFFIX]` URL also sets the bind address and service-host suffix.

**See also:** [remote workflows](/topics/remote-workflows), [cornus deploy](/cli/deploy)

## Reach a whole Compose stack and its web UI through one browser proxy

Run the Compose stack in SOCKS5 mode and publish the `cornus web` UI into the same
shared conduit, so a single browser proxy setting reaches every service *and* the
UI by name.

```sh
# 1. Make socks5 the conduit for this connection (once per profile).
cornus config set-context --conduit-mode socks5

# 2. Bring the stack up detached. In socks5 mode the background agent hosts one
#    shared proxy and registers each service's short name in it.
cornus compose up -d

# 3. Publish the web UI into that same shared conduit (binds no local port).
cornus web --publish-in-conduit
```

Point your browser's SOCKS5 proxy at the agent's proxy — the `cornus socks5` /
profile listen address, `127.0.0.1:1080` by default — with **remote DNS**
(SOCKS5h). One setting then reaches all of:

- `http://web.cornus.internal/` — the Compose service named `web` (its short name,
  registered by the socks5-mode `compose up`).
- `http://db.cornus.internal:5432/` — any other service, likewise by short name.
- `http://cornus.internal/` — the `cornus web` UI.

How it fits together:

- All three share **one** background agent, **one** connection, and **one** SOCKS5
  proxy. `compose up -d`, `cornus daemon docker`, and `cornus web --publish-in-conduit`
  all join the same shared conduit keyed on the connection and its socks5 settings.
- The Compose *short* names (`web`, not the deployment name `demo-web`) resolve only
  because the workload sessions run in **socks5** mode — step 1 is what registers
  them. If your stack runs in the default port-forward mode, the UI still publishes
  and services still resolve by their full deployment name (`demo-web.cornus.internal`),
  but the short names do not.
- The web UI binds no port of its own; it is reachable exactly where the proxy is,
  so it inherits the proxy's loopback boundary rather than adding a new surface.
- Keep the conduit settings consistent across the commands (the same `--conduit`
  URL, or all relying on the profile). Divergent `listen`/`suffix` values make the
  second command's proxy collide with the first on its bind address.

**See also:** [cornus web](/cli/web), [cornus compose](/cli/compose), [cornus socks5](/cli/socks5)

## Reach a workload's ingress host through the conduit

Reach a workload at its declared `x-cornus-ingress` hostname (e.g.
`web.example.com`) from your machine, with no real DNS — opt in with
`--ingress-conduit` on a socks5 session.

```sh
# native: tunnel to the real cluster ingress controller (kubernetes + kube access)
cornus compose up --conduit socks5 --ingress-conduit native

# emulate: a client-side reverse proxy with a generated cert (any backend)
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate
curl --socks5-hostname 127.0.0.1:1080 \
  --cacert ~/.local/share/cornus/ingress-ca.pem https://web.example.com/
```

- **native** hands the browser's SNI/`Host` straight to the real controller, which
  routes and terminates TLS with the cluster cert; **emulate** proxies by
  `Host`/path to the workload and terminates TLS locally — signed by
  [mkcert](https://github.com/FiloSottile/mkcert)'s CA when it is installed
  (`mkcert -install`, then browsers trust it automatically), else a self-signed CA
  (`~/.local/share/cornus/ingress-ca.pem`) you trust once.
- Precedence is `--ingress-conduit` > `CORNUS_INGRESS_CONDUIT` > the profile
  (`cornus config set-context --ingress-conduit`); `off` disables it. `cornus setup`
  probes the cluster and picks a default. Use **remote DNS** (socks5h) in your
  browser.

**See also:** [Public ingress](/topics/ingress), [cornus config](/cli/config)

## Join the workload-to-workload hub as a spoke

Connect this host to the overlay to offer a local service and/or reach an overlay service by name.

```sh
cornus hub --identity laptop \
  --register api=127.0.0.1:8080 \
  --reach db=127.0.0.1:5432
```

- `--register name=host:port` offers a local service (relayed to this spoke, so a NAT'd host stays reachable); `--reach name=listen_ip:port` binds a local listener into the overlay. At least one is required.
- The server is resolved from `--server` or the selected profile; a profile carrying client-TLS material is refused for `hub` for now.

**See also:** [cornus hub](/cli/hub), [the workload hub](/topics/hub)

## Export and import services across workloads via the hub

For workloads deployed on Kubernetes, declare hub membership in the deploy spec instead of the CLI.

```yaml
name: api
image: localhost:5000/api:v1
hub:
  identity: api                 # policy identity (defaults to the deployment name)
  export:
    - { name: api, port: 8080 }
    - { name: udpecho, port: 9000, protocol: udp, deliver: true }
  import:
    - { name: db, ports: [5432] }
```

- `export` lists services this workload hosts; `import` lists services it reaches (a synthetic loopback IP and DNS record are wired per import, so a plain `dial(peer)` funnels into the hub).
- Set `deliver: true` on an export the hub cannot reach directly; `importDynamic` opts into dynamic catalog discovery. `hub:` is kubernetes-only.

**See also:** [deploy spec](/reference/deploy-spec), [the workload hub](/topics/hub)
