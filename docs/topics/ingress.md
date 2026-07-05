# Public ingress

Ingress is the inbound counterpart to [client-side egress](/topics/egress): it
requests a public **HTTP(S) Ingress** that fronts a workload's published port, so
the service is reachable at a real hostname instead of only through a
[port-forward](/guides/networking) or a [tunnel](/topics/tunnels). It is a
**Kubernetes-backend feature** — the `dockerhost` and `containerd` backends warn
and ignore it — and it fronts the workload's `ClusterIP` Service, so the spec must
publish at least one port.

Ingress is opt-in, via the deploy spec `ingress:` block or Compose's portable
`x-cornus-ingress:` extension. It never turns on implicitly.

To reach an ingress host **from your own machine** — including on the host backends,
and without real DNS — see [Reaching an ingress from your machine](#reaching-an-ingress-from-your-machine-through-the-socks5-conduit),
which routes it through the SOCKS5 conduit.

## Enabling it

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

## Host resolution

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

## Routing

| Field | Default | Meaning |
| --- | --- | --- |
| `path` | `/` | HTTP path prefix to route. |
| `pathType` | `Prefix` | Kubernetes path match type: `Prefix`, `Exact`, or `ImplementationSpecific`. |
| `port` | first published | Container port to route to; a non-zero value must match one of the spec's published ports. |
| `className` | server default | `IngressClassName`; empty falls back to `CORNUS_INGRESS_CLASS`, then the cluster's default IngressClass. |
| `annotations` | — | merged verbatim onto the Ingress object, for controller-specific knobs (rewrite target, body size, ...). |

## TLS

A `tls:` block requests HTTPS for the host(s); omit it for plain HTTP.

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # cert-manager provisions the cert
    # secretName: app-tls               # or bring your own existing secret
```

- `secretName` names an existing TLS secret; empty defaults to `<name>-tls`, which
  cert-manager provisions when a `clusterIssuer` (or the server default) is set.
- `clusterIssuer` sets the `cert-manager.io/cluster-issuer` annotation; empty falls
  back to the server default `CORNUS_INGRESS_TLS_ISSUER`.

## Server-side defaults and domain policy

An operator sets fallbacks so a workload can enable ingress with everything
defaulted (Helm `ingress.*` values, rendered as env). Leave them empty to require
each workload to specify its own host, so nothing is auto-exposed.

| Env var | Helm value | Meaning |
| --- | --- | --- |
| `CORNUS_INGRESS_DOMAIN` | `ingress.domain` | Base wildcard domain for host auto-derivation (e.g. `preview.example.com`). |
| `CORNUS_INGRESS_CLASS` | `ingress.className` | Default `IngressClassName`. |
| `CORNUS_INGRESS_TLS_ISSUER` | `ingress.tlsIssuer` | Default cert-manager cluster-issuer for TLS ingresses. |
| `CORNUS_INGRESS_ENFORCE_DOMAIN` | `ingress.enforceDomain` | When true (and a domain is set), reject a workload whose resolved host falls outside `domain`, so a shared controller cannot be made to serve an arbitrary hostname on a client's say-so. |

## In Compose

Use `x-cornus-ingress` at the project level or per service. A project-level block
provides **defaults** but does not enable ingress — ingress stays opt-in per
service. The `x-` prefix keeps the file valid for standard Compose tooling.

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.example.com            # scalar sugar; unioned with hosts:
      tls: { clusterIssuer: letsencrypt-prod }
```

See the [deploy spec](/reference/deploy-spec) for the full `IngressSpec` /
`IngressTLS` fields, the [Helm chart values](/reference/helm-values) for the
server defaults, and the [Ingress](/guides/ingress) guide for task-oriented
recipes.

## Reaching an ingress from your machine, through the SOCKS5 conduit

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
  TLS with a generated certificate. Works on **every** backend (including
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
