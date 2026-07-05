# Connection config reference

The **connection config** is the CLI-side, kubeconfig-style file that describes how to reach a remote cornus server: a set of named **contexts**, each holding an endpoint, credentials, TLS material, and an optional in-cluster port-forward target. It lives on a developer's machine and is **never read by the server** (that is a separate, server-side data-directory config).

You normally manage this file with [`cornus config`](/cli/config) rather than editing it by hand, but the format is documented here. The canonical source of truth is [`pkg/clientconfig/clientconfig.go`](https://github.com/moriyoshi/cornus/blob/main/pkg/clientconfig/clientconfig.go).

## File location

The default path is the platform user config directory, under `cornus/config.yaml`:

- Linux/BSD: `~/.config/cornus/config.yaml`
- macOS: `~/Library/Application Support/cornus/config.yaml`
- Windows: `%AppData%\cornus\config.yaml`

An explicitly set `$XDG_CONFIG_HOME` is honored on **every** OS (an opt-in for users who standardize on XDG): the file is then `$XDG_CONFIG_HOME/cornus/config.yaml`. The global `--config-file` flag and the `CORNUS_CONFIG` environment variable override the path entirely.

The file holds bearer tokens and key paths, so it is written mode `0600` under a `0700` directory. A missing file is not an error — the CLI treats it the same as an empty config.

## Sample config

```yaml
current-context: staging
contexts:
  local:
    server: http://127.0.0.1:5000

  staging:
    server: https://cornus.staging.example.com
    token: eyJhbGciOi...
    tls:
      ca-cert: /etc/cornus/staging-ca.pem
    conduit:
      mode: socks5
      socks5:
        listen: 127.0.0.1:1080
        service-host-suffix: .cornus.internal

  prod-cluster:
    # No static server URL: dial the in-cluster Service via port-forward.
    port-forward:
      kube-context: prod
      namespace: cornus
      service: cornus
      remote-port: 5000
    kube-auth:
      audience: cornus
      expiration-seconds: 3600
    registry-host: registry.prod.example.com:5000
```

## `File`

The top-level document.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `current-context` | string | — | The context used when no `--context` flag is given. Empty means "no context selected"; the CLI then relies on per-command flags and environment variables. |
| `contexts` | map[string][Context](#context) | — | The named connection profiles, keyed by name. |

## `Context`

One named remote endpoint with the credentials and transport settings to reach it.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `server` | string | — | The cornus server base URL (e.g. `https://cornus.example.com` or `http://127.0.0.1:5000`). When `port-forward` is set and `server` is empty, the CLI forwards to the in-cluster Service and dials the local end instead. |
| `registry-host` | string | derived from the server | Overrides the `host[:port]` that built images are tagged with and that deploy pull refs carry. Empty (the usual case) derives it: the CLI asks the server (`GET /.cornus/v1/info`), falling back to the `server` endpoint's host. Set this only for topologies the server cannot introspect. |
| `token` | string | `CORNUS_TOKEN` env | The bearer token / JWT sent as `Authorization: Bearer`. Empty falls back to the `CORNUS_TOKEN` environment variable. |
| `tls` | [TLS](#tls) | system defaults | Optional custom-CA / mTLS / insecure settings for HTTPS endpoints. |
| `port-forward` | [PortForward](#portforward) | — | When set, an in-cluster Service the CLI port-forwards to before dialing. |
| `kube-auth` | [KubeAuth](#kubeauth) | — | When set, derives the bearer token from the cluster (a short-lived ServiceAccount token via the Kubernetes TokenRequest API) instead of a static `token`. Takes precedence over `token` but yields to an explicit `CORNUS_TOKEN` override. |
| `via-server` | bool (nullable) | unset (direct) | Forces workload streaming operations (compose logs, port-forward) to route through the cornus server proxy instead of the CLI reaching workload pods directly with the developer's kubeconfig. Only matters for a cluster profile. Lowest-precedence layer, below the `CORNUS_VIA_SERVER` env var and the `--via-server` flag. Transport-only: it does not disable `kube-auth` token minting. |
| `conduit` | [Conduit](#conduit) | port-forward | How a client session exposes a deployment's ports to the caller. Lowest-precedence layer, below the `CORNUS_CONDUIT` env var and the `--conduit` flag. See [Remote workflows](/topics/remote-workflows). |

## `Conduit`

A context's session conduit preference: the mode plus, for SOCKS5, its proxy settings.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | string | `port-forward` | `port-forward` (per-port automatic forwarding, Compose-like) or `socks5` (a single client-side SOCKS5 split-tunnel proxy). |
| `socks5` | [Socks5](#socks5) | — | Tunes the SOCKS5 proxy; consulted only when `mode` is `socks5`. |

## `Socks5`

Configures the SOCKS5 split-tunnel proxy.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `listen` | string | `127.0.0.1:1080` | Local address the proxy binds. |
| `service-host-suffix` | string | `.cornus.internal` | Builds the everyday default resolution rule: a CONNECT host bearing this suffix is stripped to a service name and tunneled in, everything else egresses directly. Ignored when `resolve` is set. |
| `resolve` | [][ResolveRule](#resolverule) | — | An advanced, ordered list of resolution rules that replaces the suffix default entirely; the first matching rule wins. |
| `bare-service-names` | bool (nullable) | enabled | Whether a bare, single-label host that names a live service (e.g. `web`, in addition to `web.cornus.internal`) is routed inward. Set `false` to disable it when a service name would shadow a real single-label host reached directly. |

## `ResolveRule`

One SOCKS5 resolution rule.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `pattern` | string | — | A regexp tested against the `host:port` CONNECT subject. |
| `replace` | string | — | A template yielding `service:port` (sed-style `\1` backreferences accepted). |

## `TLS`

Client-side TLS material for an HTTPS endpoint. `Config()` returns the system defaults when none of these are set. `client-cert` and `client-key` must be set together.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `ca-cert` | string | system trust store | Path to a PEM CA bundle that verifies the server certificate, for a server whose CA is not in the system trust store. |
| `insecure-skip-verify` | bool | `false` | Disables server certificate verification. Testing only. |
| `client-cert` | string | — | Path to a PEM client certificate for mTLS. |
| `client-key` | string | — | Path to the matching PEM client key for mTLS. |

See [Auth and TLS](/topics/auth-and-tls) for the server side of mTLS and bearer authentication.

## `PortForward`

An in-cluster Service to forward to before dialing (consumed by the CLI's service-forwarder).

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `kube-context` | string | current kube context | The kubeconfig context to use. |
| `namespace` | string | — | Namespace of the Service. |
| `service` | string | — | Service name to forward to. |
| `remote-port` | int | — | The Service port; the CLI resolves it to a ready backing pod and its target port. |

## `KubeAuth`

A cluster-issued ServiceAccount token to mint as the cornus bearer credential.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `kube-context` | string | the `port-forward` block's value | The kubeconfig context to mint against. |
| `namespace` | string | the `port-forward` block's value | Namespace of the ServiceAccount. |
| `service-account` | string | — | ServiceAccount to mint the token for. |
| `audience` | string | — | Token audience. Must match the server's `CORNUS_JWT_AUDIENCE`. |
| `expiration-seconds` | int64 | cluster default | Requested token lifetime. |

## Project context override

A project can carry a bare `Context` document named `cornus-context.json`, `cornus-context.yaml`, `cornus-context.yml`, or `cornus-context.toml`. Cornus searches upward from the working directory, uses the nearest file, and stops at the repository root or your home directory. Its fields overlay the selected stored context; explicit command flags and environment variables still win. It can also provide a connection when no stored context is selected.

```yaml
server: https://cornus.staging.example.com
via-server: true
conduit:
  mode: socks5
```

Use `--context-file PATH` or `CORNUS_CONTEXT_FILE=PATH` for an explicit file. A missing explicit file is an error. `--no-context-file` disables discovery and cannot be combined with `--context-file`.

### Trust boundary

An auto-discovered file is working-tree input, not a trusted credential store. By default it contributes only `via-server`; endpoint, token, TLS, registry, port-forward, kube-auth, SSH-tunnel, and conduit settings are ignored. On Unix, Cornus also ignores a file owned by another user or one in a world-writable non-sticky directory.

Use `--trust-context-file` / `CORNUS_TRUST_CONTEXT_FILE=1` only for a trusted working tree. An explicitly named `--context-file` is also trusted. An override that changes the endpoint must supply its own `token` or `kube-auth`; otherwise the selected context credential is dropped. Cornus warns whenever it skips or strips a project override.

## See also

- [`cornus config`](/cli/config) — create, select, and edit contexts.
- [Remote workflows](/topics/remote-workflows) — conduits, port-forwarding, and driving a remote server.
- [Auth and TLS](/topics/auth-and-tls) — bearer tokens, mTLS, and cluster-minted identities.
