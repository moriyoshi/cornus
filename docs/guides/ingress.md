# Ingress

Task-oriented recipes for giving a workload a public HTTP(S) hostname on the
Kubernetes backend. For the model behind them see [Ingress](/topics/ingress) and
the [deploy spec](/reference/deploy-spec). Ingress fronts a published port, so the
workload must publish at least one; the `dockerhost` / `containerd` backends warn
and ignore the Ingress object.

To reach a workload at its ingress host from your own machine (any backend, no DNS),
skip to [Reach the ingress from your machine](#reach-the-ingress-from-your-machine-through-the-conduit).

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

**See also:** [Ingress](/topics/ingress), [deploy spec](/reference/deploy-spec)

## Set explicit hostnames

Route one or more hostnames to the same Service; each becomes its own Ingress rule.

```yaml
ingress:
  hosts:
    - app.example.com
    - www.example.com
```

- Use the special token `@` for the apex (the base domain itself, no `<name>.` prefix): `hosts: ["@"]`.

**See also:** [Ingress](/topics/ingress)

## Serve HTTPS with cert-manager

Request a certificate from a cert-manager cluster-issuer; cornus adds the issuer annotation and cert-manager provisions the secret.

```yaml
ingress:
  hosts: ["app.example.com"]
  tls:
    clusterIssuer: letsencrypt-prod     # empty falls back to CORNUS_INGRESS_TLS_ISSUER
```

- `secretName` defaults to `<name>-tls`. To bring your own certificate, set `tls: { secretName: my-existing-tls }` and omit `clusterIssuer`.

**See also:** [Ingress](/topics/ingress), [securing a server](/guides/security)

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

**See also:** [Ingress](/topics/ingress), [deploy spec](/reference/deploy-spec)

## Expose a Compose service

Add `x-cornus-ingress` to a service (project-level provides defaults but does not enable ingress).

```yaml
services:
  web:
    image: registry.example/web:v1
    ports: ["8080:80"]                 # required: the ingress fronts a published port
    x-cornus-ingress:
      host: web.example.com
      port: 80                          # container port to route to (omit to use the first published)
      tls: { cluster_issuer: letsencrypt-prod }
```

Watch three things: the service **must** publish a port (`ports:`) or the deploy is
rejected; `port` is the **container** port your app listens on, not the public
`80`/`443`; and the keys are **snake_case** (`path_type`, `class_name`,
`cluster_issuer`), not the deploy spec's camelCase. See the
[Ingress topic](/topics/ingress#in-compose) for the details.

**See also:** [Ingress](/topics/ingress), [Compose, devcontainers, and the docker CLI](/guides/compose-devcontainers-docker)

## Reach the ingress from your machine (through the conduit)

Reach a workload at its ingress host from a dev machine with no DNS: run the session
in SOCKS5 mode and opt in with `--ingress-conduit`. `native` tunnels to the real
cluster ingress controller (Kubernetes plus direct cluster access); `emulate` runs a
client-side reverse proxy with a generated cert and works on **any** backend.

```sh
# native: reach the real controller (it does routing + TLS with the cluster cert)
cornus compose up --conduit socks5 --ingress-conduit native

# emulate: a client-side reverse proxy (works on dockerhost / containerd too)
cornus deploy -f app.yaml --server https://cornus.example.com \
  --conduit socks5 --ingress-conduit emulate
```

Point your browser's SOCKS5 proxy at the conduit with **remote DNS** (socks5h), then
open the workload's ingress host, e.g. `https://web.example.com/`.

- `cornus setup` probes the cluster and can record the mode for you; pin it manually
  with `cornus config set-context <name> --conduit-mode socks5 --ingress-conduit native`.
- `native` serves the cluster's own certificate; `emulate` terminates TLS locally —
  signed by [mkcert](https://github.com/FiloSottile/mkcert)'s CA when it is installed
  (`mkcert -install`), so the browser trusts it automatically, otherwise a self-signed
  CA you trust once.

**See also:** [Public ingress](/topics/ingress), [Networking recipes](/guides/networking), [cornus setup](/cli/setup)
