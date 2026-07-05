# Ingress

Ingress is the inbound counterpart to [client-side egress](/guides/egress): it
requests a public **HTTP(S)** front door for a workload's published port, so the
service is reachable at a real hostname instead of only through a
[port-forward](/guides/networking) or a [tunnel](/guides/tunnels). It fronts the
workload's published port, so the spec must publish at least one.

Ingress is opt-in, via the deploy spec `ingress:` block or Compose's portable
`x-cornus-ingress:` extension. It never turns on implicitly.

## What realizes it

The hosts and paths you declare are the same everywhere; what differs is what
serves them:

| Backend | Realized by |
| --- | --- |
| `kubernetes` | a real `networking.k8s.io/v1` **Ingress** object fronting the workload's `ClusterIP` Service, served by the cluster's ingress controller |
| `dockerhost`, `containerd`, `bare`, `incus` | the **cornus server itself**, which keeps a host/path routing table and proxies to the workload |

Only the Kubernetes path creates a cluster object, so the class name,
annotations, and cert-manager TLS settings below apply there. Everywhere else the
server routes the same declaration, which is what makes a compose project behave
the same way on a laptop and in a cluster.

::: warning The server-side front door is for development
Server-side ingress routing exists so a compose project behaves the same on a
laptop as in a cluster. It is **not** a production ingress: it terminates no TLS
of its own, applies no rate limiting or access control, and its error pages name
the internal workload and container port they failed to reach — useful while you
are debugging, unwanted on an address strangers can reach.

Use it for development and preview work. For anything durable or public, deploy
to a cluster with a real ingress controller, which is what cornus dials when one
is available.
:::

## Reaching it

Declaring an ingress does not by itself make it reachable from your machine —
that needs DNS pointing somewhere, which a preview environment rarely has. Three
ways to close the gap, in rough order of how often they are what you want:

- [`cornus ingress-tunnel`](/cli/ingress-tunnel) gives the whole project one
  **public URL**, with the routing done for you. See
  [Publish the ingress publicly](#publish-the-ingress-publicly).
- The [SOCKS5 conduit](#reach-the-ingress-from-your-machine-through-the-conduit)
  routes the declared hostnames from your own machine, with no public exposure.
- `CORNUS_INGRESS_LISTEN` binds the server's front door on an address you can
  point DNS or an `/etc/hosts` entry at.

## How it works

### Enabling it

Any of these enable ingress:

- `ingress: { enabled: true }` in a deploy spec,
- a bare `x-cornus-ingress: {}` (or `x-cornus-ingress: true`) in Compose, or
- any non-empty host (`hosts:` / Compose `host:`), which implies `enabled`.

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }     # the Service the Ingress fronts
ingress:
  enabled: true                        # host auto-derived from the server domain
  tls: {}                              # HTTPS via the server's default issuer
```

### Host resolution

- **Explicit `hosts:`** — each hostname becomes its own Ingress rule, all sharing
  one TLS entry and fronting the same Service. The special token `@` maps to the
  **apex** (the base domain itself, with no `<name>.` prefix), following the
  DNS-zone convention.
- **Auto-derived (when `hosts` is empty)** — the backend builds a single host as
  `<subdomain>.<domain>`:
  - `domain` is a client override of the base domain; empty falls back to the
    server default `CORNUS_INGRESS_DOMAIN`.
  - `subdomain` defaults to the deployment name (the Compose translator sets it to
    `<service>.<project>` so different projects get distinct hostnames); labels are
    sanitized to DNS-1123.
  - A deploy with **neither** an explicit host nor any base domain is rejected.

### Routing

The ingress fronts one of the workload's **published container ports** (its
`ClusterIP` Service), so the spec must publish at least one port. A deploy that
enables ingress with no `ports:` is rejected with `ingress requires the deployment
to publish at least one port`.

| Deploy-spec field | Compose key | Default | Meaning |
| --- | --- | --- | --- |
| `path` | `path` | `/` | HTTP path prefix to route. |
| `pathType` | `path_type` | `Prefix` | Kubernetes path match type: `Prefix`, `Exact`, or `ImplementationSpecific` (case-sensitive — lowercase `prefix` is rejected). |
| `port` | `port` | first published | The **container** port to route to — the port your app listens on, **not** the public HTTP/HTTPS port (those stay 80/443). Zero uses the first published port; a non-zero value must match one of the workload's published container ports, else `ingress: port N is not among the deployment's published container ports`. |
| `className` | `class_name` | server default | `IngressClassName`; empty falls back to `CORNUS_INGRESS_CLASS`, then the cluster's default IngressClass. |
| `annotations` | `annotations` | — | merged verbatim onto the Ingress object, for controller-specific knobs (rewrite target, body size, ...). |

The deploy spec uses the camelCase field names in the first column; the Compose
`x-cornus-ingress` extension uses the snake_case keys in the second column (see
[Expose a Compose service](#expose-a-compose-service)).

### Server-side defaults and domain policy

An operator sets fallbacks so a workload can enable ingress with everything
defaulted (Helm `ingress.*` values, rendered as env). Leave them empty to require
each workload to specify its own host, so nothing is auto-exposed.

| Env var | Helm value | Meaning |
| --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | `ingress.domain` | Base wildcard domain for host auto-derivation (e.g. `preview.example.com`). |
| `CORNUS_INGRESS_CLASS` | `ingress.className` | Default `IngressClassName`. |
| `CORNUS_INGRESS_TLS_ISSUER` | `ingress.tlsIssuer` | Default cert-manager cluster-issuer for TLS ingresses. |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | `ingress.enforceDomain` | When true (and a domain is set), reject a workload whose resolved host falls outside `domain`, so a shared controller cannot be made to serve an arbitrary hostname on a client's say-so. |

**See also:** [deploy spec](/reference/deploy-spec), [Helm chart values](/reference/helm-values)

## Expose a workload on an auto-derived host

Enable ingress and let the server derive the host as `<subdomain>.<domain>` from its
base domain (`CORNUS_INGRESS_DOMAIN`).

```yaml
name: web
image: localhost:5000/web:v1
ports:
  - { host: 8080, container: 80 }
ingress:
  enabled: true
```

- `subdomain` defaults to the deployment name, so this deploys to `web.<CORNUS_INGRESS_DOMAIN>` (the Compose translator uses `<service>.<project>` instead). If the server has no base domain and you set none, the deploy is rejected.

**See also:** [deploy spec](/reference/deploy-spec)

## Set explicit hostnames

Route one or more hostnames to the same Service; each becomes its own Ingress rule.

```yaml
ingress:
  hosts:
    - app.example.com
    - www.example.com
```

- Use the special token `@` for the apex (the base domain itself, no `<name>.` prefix): `hosts: ["@"]`.

## Serve HTTPS with cert-manager

Request a certificate from a cert-manager cluster-issuer; cornus adds the issuer annotation and cert-manager provisions the secret.

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # empty falls back to CORNUS_INGRESS_TLS_ISSUER
```

- A `tls:` block requests HTTPS for the host(s); omit it for plain HTTP.
- `secretName` names an existing TLS secret; empty defaults to `<name>-tls`, which cert-manager provisions when a `clusterIssuer` (or the server default) is set. To bring your own existing secret, set `tls: { secretName: my-existing-tls }` and omit `clusterIssuer`.
- `clusterIssuer` sets the `cert-manager.io/cluster-issuer` annotation; empty falls back to the server default `CORNUS_INGRESS_TLS_ISSUER`.

**See also:** [Security and authentication](/guides/security)

## Bring your own certificate

Put certificate rules in the selected connection profile. `pattern` is optional;
when omitted, Cornus creates selectors from every DNS SAN in the certificate.

```yaml
contexts:
  prod:
    server: https://cornus.example.com
    conduit:
      ingress:
        mode: native
        certificates:
          - certificate: /etc/cornus/example-com.pem
            key: /etc/cornus/example-com-key.pem
          - pattern: api.other.example
            certificate: /etc/cornus/api.pem
            key: /etc/cornus/api-key.pem
```

Patterns are exact names or one-label wildcards such as `*.example.com`. An
explicit pattern must be covered by the certificate SANs. Exact matches win over
wildcards; the longest wildcard suffix wins among wildcard matches.

In `emulate` mode, SNI selects the certificate served by the local ingress proxy;
an unmatched name uses the normal generated-CA fallback. In `native` mode, Cornus
matches every concrete ingress host before deployment, groups hosts by selected
certificate, creates stable `kubernetes.io/tls` Secrets owned by the workload
Deployment, and wires them into the Kubernetes Ingress. Reapplying rotates Secret
data in place and removes obsolete managed Secrets.

Native managed certificates require explicit concrete `ingress.hosts`: expand an
auto-derived host or `@` apex token in the spec. Every host must match a certificate
rule. This also works with detached Compose and deploy operations because the
certificate is durable Kubernetes state, not a client-side conduit listener.

The native path sends private-key material in the deploy request. Cornus therefore
rejects it over remote plaintext HTTP before request serialization; use HTTPS, an
SSH-tunnel profile, or a loopback endpoint such as a Kubernetes port-forward. The
key never appears in status or diagnostic output. See the
[connection config reference](/reference/connection-config#ingresscertificate) for
the complete fields.

## Route a specific path, port, or class

Override the defaults when the workload publishes several ports or the cluster has multiple ingress controllers.

```yaml
ingress:
  hosts: ["api.example.com"]
  path: /v1
  pathType: Prefix                       # or Exact / ImplementationSpecific
  port: 8443                             # must match a published container port
  className: nginx                       # empty uses CORNUS_INGRESS_CLASS, then the cluster default
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "50m"
```

**See also:** [deploy spec](/reference/deploy-spec)

## Expose a Compose service

Use `x-cornus-ingress` at the project level or per service. A project-level block
provides **defaults** but does not enable ingress — ingress stays opt-in per
service. The `x-` prefix keeps the file valid for standard Compose tooling.

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]                 # the ingress fronts a published port (here container :80)
    x-cornus-ingress:
      host: web.example.com            # scalar sugar; unioned with hosts:
      port: 80                          # container port to route to; omit to use the first published
      path_type: Prefix
      tls: { cluster_issuer: letsencrypt-prod }
```

Three things trip people up here; all three fail silently or at deploy time:

- **Publish the port.** The service needs a `ports:` entry — the ingress fronts a
  published container port. A service that only listens internally must still list
  it (`ports: ["80"]`, or long form `- target: 80` to avoid binding a host port,
  which also sidesteps a host-port clash with another service).
- **`port` is the container port, not the public one.** It is the port your app
  listens on inside the container (e.g. `3000`, `8000`), never `80`/`443` — TLS and
  the public HTTP(S) ports are handled for you by `tls: {}`.
- **Keys are snake_case.** Inside `x-cornus-ingress` write `path_type`,
  `class_name`, and under `tls:` `secret_name` / `cluster_issuer` — not the deploy
  spec's camelCase (`pathType`, `className`, `secretName`, `clusterIssuer`). A
  camelCase key is an unknown field and is silently ignored. Values are
  case-sensitive too: `path_type: Prefix`, not `prefix`. A bare `tls: {}` is enough
  to request HTTPS with the server default issuer.

**See also:** [deploy spec](/reference/deploy-spec), [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker)

## Reach the ingress from your machine (through the conduit)

The public Ingress above gives a workload a real hostname, but reaching it from a
dev machine still needs DNS pointing at the cluster's ingress controller. The
[SOCKS5 conduit](/guides/networking) closes that gap: with one browser proxy
setting, a workload's ingress host resolves through the proxy — no `/etc/hosts`
edits, no real DNS. It is **opt-in** and rides the socks5 conduit
(`--conduit socks5`), in one of two modes:

- **native** — a transparent tunnel to the cluster's *real* ingress controller
  Service. Your browser's TLS ClientHello (SNI) and `Host` header pass straight
  through, so the actual controller does the Host/path routing and terminates TLS
  with the cluster's own certificate. Kubernetes only, and your session must have
  direct cluster access (a port-forward / kube-auth profile). The controller
  Service is discovered by the server and advertised over `GET /.cornus/v1/info`
  (override with `CORNUS_INGRESS_CONTROLLER=<namespace>/<service>[:http/https]`).
- **emulate** — a small client-side HTTP(S) reverse proxy that routes by
  `Host`/path to the workload's container port through the conduit, terminating
  TLS with a matching user-provided certificate or a generated fallback. Works on **every** backend (including
  `dockerhost` / `containerd`, which have no controller). **TLS trust, out of the
  box:** if [mkcert](https://github.com/FiloSottile/mkcert) is installed and you
  have run `mkcert -install`, the emulated ingress signs its leaf certificates with
  mkcert's already-trusted local CA, so your browser and `curl` trust
  `https://<host>/` with **no manual step**. Otherwise it falls back to a persisted
  self-signed CA (`~/.local/share/cornus/ingress-ca.pem`) you trust once (or pass
  `--cacert`). An explicit `--ingress-emulate-ca` / `--ingress-emulate-ca-key`
  overrides both.

Enable it per run or pin it in a profile:

```sh
# per run
cornus compose up --conduit socks5 --ingress-conduit native
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate

# or pin it in the connection profile (see cornus config)
cornus config set-context prod --conduit-mode socks5 --ingress-conduit native
```

Point your browser's SOCKS5 proxy at the conduit (with **remote DNS** / socks5h)
and open the workload's ingress host, e.g. `https://web.example.com/`. Precedence
is `--ingress-conduit` > `CORNUS_INGRESS_CONDUIT` > the profile; `off` disables it.

`cornus setup` probes the server and picks a default for you: a discovered
controller proposes **native**, an ingress domain without a reachable controller
proposes **emulate**, otherwise **off**.

Two notes: native and emulate both apply to the same `x-cornus-ingress` spec —
native is preferred where a real controller exists, emulate is the portable
fallback. The controller's `annotations` / `className` / cert-manager fields are
Kubernetes-only and ignored by emulation.

**See also:** [Networking and conduits](/guides/networking), [cornus setup](/cli/setup)

## Publish the ingress publicly

[`cornus ingress-tunnel`](/cli/ingress-tunnel) puts a public URL in front of the
ingress — every hostname and path the scope declares, behind one address:

```sh
cornus ingress-tunnel --project myapp
```

```
Ingress tunnel for project/myapp ready at https://abc123.ngrok.app
  serving: web.myapp.example.com, api.myapp.example.com
  fronting: the cluster ingress controller
  host: passed through untouched
```

This is the difference from [`cornus tunnel`](/cli/tunnel), which exposes a single
workload port and knows nothing about your declared hosts and paths.

To publish on every `compose up` instead of running the command, declare it next
to the ingress it applies to:

```yaml
services:
  web:
    image: myapp:latest
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.myapp.example.com
      tunnel: true
```

The tunnel credential still comes from the client — a connection profile's
`authtoken-file`, or `CORNUS_TUNNEL_AUTHTOKEN` — never from the compose file,
which is checked in. See the
[tunnels guide](/guides/tunnels#tunnelling-the-ingress-instead-of-a-port) for what
each backend can do, and
[host handling](/cli/ingress-tunnel#host-handling) for the one thing worth
understanding before you share the URL: whether the app sees the tunnel's hostname
or the one you declared.
