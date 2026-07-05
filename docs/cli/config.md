# cornus config

Manage the client-side connection profiles (contexts) used to reach a remote
cornus server, mirroring the shape of `kubectl config`.

## Synopsis

```sh
cornus config <subcommand> [flags]
```

## Description

`cornus config` reads and writes the cornus client config file, which stores
one or more named contexts (connection profiles) and a current-context
pointer. The file lives at the platform user config dir, or the path given by
the global `--config-file` flag / `CORNUS_CONFIG`.

For a guided path that picks the deployment scenario, asks only the relevant
questions, verifies the connection, and prints setup guidance, use the
interactive [`cornus setup`](/cli/setup) wizard — a front-end over `set-context`.

Each context describes how to reach a server: its base URL, bearer token or
ServiceAccount-minted auth, TLS material, an optional automatic port-forward to
an in-cluster Service, the direct-vs-proxy `via-server` toggle, and the session
conduit (port-forward or SOCKS5). The full schema is documented in
[Connection config](/reference/connection-config).

### Client config file format

The file is YAML with a `contexts:` map keyed by name and a `current-context:`
field, for example:

```yaml
current-context: prod
contexts:
  prod:
    server: https://cornus.example.com:5000
    token: eyJhbGci...
  staging:
    namespace: cornus-system
```

Bearer tokens are redacted by `view` unless `--show-tokens` (or `--export`) is
given. See [Connection config](/reference/connection-config) for every field.

## cornus config get-contexts

List the configured connection profiles as a table (a `*` marks the current
context).

```sh
cornus config get-contexts
```

## cornus config current-context

Print the current (default) context name. Errors if none is set.

```sh
cornus config current-context
```

## cornus config use-context

Set the current (default) context.

```sh
cornus config use-context <name>
```

## cornus config set-context

Create or update a context.

```sh
cornus config set-context [flags] <name>
```

By default `set-context` *replaces* any existing context of the same name: the
result is exactly what this invocation specifies. Layering order is
`--from-file` (base), then the individual flags, then `--from-file-override`
(top). Pass `--merge` to instead layer the given settings onto the existing
context, leaving unset fields in place — the edit-in-place mode.

When the config has no contexts yet and the terminal is interactive, the newly
created context is offered as the default (current) context. `--insecure-skip-verify`
only ever enables the setting.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | — | — | Cornus server base URL (`http(s)://host:port`). |
| `--token` | — | — | Bearer token / JWT sent as `Authorization: Bearer`. |
| `--tls-ca-cert` | — | — | PEM CA bundle that verifies the server certificate. |
| `--tls-client-cert` | — | — | PEM client certificate for mTLS (requires `--tls-client-key`). |
| `--tls-client-key` | — | — | PEM client key for mTLS (requires `--tls-client-cert`). |
| `--tls-server-name` | — | — | Override the certificate hostname (SNI) verified against, for when the dial address differs from the cert identity (e.g. an SSH-tunnel endpoint dialed as `127.0.0.1`). |
| `--insecure-skip-verify` | — | `false` | Disable server certificate verification (testing only). |
| `-n`, `--namespace` | — | — | Namespace of the cornus install; auto-detects the Service and port unless `--pf-service` or `--no-detect` is set. |
| `--no-detect` | — | `false` | Store `--namespace` without contacting the cluster to detect the Service. |
| `--pf-kube-context` | — | — | kubeconfig context for the automatic port-forward. |
| `--pf-namespace` | — | — | Namespace of the in-cluster Service to port-forward to (alias for `--namespace`). |
| `--pf-service` | — | — | Name of the in-cluster Service to port-forward to (skips auto-detection). |
| `--pf-remote-port` | — | — | Service port to port-forward to. |
| `--kube-auth-service-account` | — | — | Mint the bearer token from this cluster ServiceAccount via the TokenRequest API (instead of a static `--token`). |
| `--kube-auth-audience` | — | — | Audience for the minted ServiceAccount token; must match the server `CORNUS_JWT_AUDIENCE`. |
| `--kube-auth-namespace` | — | — | Namespace of the ServiceAccount (defaults to `--pf-namespace`). |
| `--kube-auth-kube-context` | — | — | kubeconfig context to mint the token through (defaults to `--pf-kube-context`). |
| `--kube-auth-expiration-seconds` | — | `3600` | Requested token lifetime in seconds (0 = default 3600). |
| `--ssh-host` | — | — | Reach the server through an SSH tunnel to this destination: an ssh_config `Host` alias or `host[:port]` (the docker/containerd-host analogue of `--pf-*`, mutually exclusive with them). |
| `--ssh-user` | — | — | SSH login user (defaults to ssh_config, then the current user). |
| `--ssh-remote-addr` | — | `127.0.0.1:5000` | Address the remote cornus server listens on, from the remote host's view. |
| `--ssh-identity-file` | — | — | PEM private key for SSH public-key auth (defaults to the ssh-agent and ssh_config `IdentityFile`). |
| `--ssh-no-agent` | — | `false` | Do not use the local ssh-agent (mainly for the "too many authentication failures" case). |
| `--ssh-known-hosts` | — | — | `known_hosts` file for SSH host-key verification (defaults to ssh_config, then `~/.ssh/known_hosts`). |
| `--ssh-host-key` | — | — | Pin a single SSH host key as an `authorized_keys`-format line. |
| `--ssh-insecure-host-key` | — | `false` | Skip SSH host-key verification (dev only). |
| `--ssh-no-config` | — | `false` | Do not consult `~/.ssh/config` or `/etc/ssh/ssh_config`; use only the `--ssh-*` flags. |
| `--ssh-use-binary` | — | `false` | Force the system `ssh` binary (unix-socket forward) for full ssh_config fidelity (`ProxyCommand`, `Match`). Auto-selected when the host has a `ProxyCommand`. |
| `--ssh-tls` | — | `false` | Dial the tunneled endpoint over `https://` because the remote server terminates TLS (usually paired with `--tls-server-name`). |
| `--via-server` / `--no-via-server` | — | — | Route workload logs/port-forward through the cornus server proxy instead of reaching pods directly with your kubeconfig (cluster profiles only). Overridden per-run by `CORNUS_VIA_SERVER` or a command `--via-server` flag. |
| `--conduit-mode` | — | — | How a client session exposes ports: `port-forward` (per-port local listeners, the default), `socks5` (one split-tunnel proxy reaching services by name), or a `socks5://host:port[?suffix=SUFFIX]` URL that also sets the proxy bind address and suffix. Overridden per-run by `CORNUS_CONDUIT` or a command `--conduit` flag. |
| `--socks5-service-host-suffix` | — | `.cornus.internal` | Host suffix whose SOCKS5 `CONNECT` targets are tunneled to the matching service; other hosts conduit directly. |
| `--socks5-resolve` | — | — | Advanced SOCKS5 resolution rule `PATTERN=REPLACE` (repeatable, ordered, first match wins); replaces the suffix default. |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | — | Reach a workload ingress (`x-cornus-ingress`) through the SOCKS5 conduit: `native` (tunnel to the real cluster ingress controller), `emulate` (a client-side reverse proxy with a generated cert), or `off`. Requires `--conduit-mode socks5`. See [Ingress](/guides/ingress). |
| `--ingress-controller` | — | — | Native-mode ingress controller Service to tunnel to, as `<namespace>/<service>[:httpPort/httpsPort]`. Empty learns it from the server (`GET /.cornus/v1/info`). |
| `--ingress-emulate-ca` / `--ingress-emulate-ca-key` | — | — | Emulate-mode PEM CA cert/key that signs the per-host leaf certs. Empty auto-detects [mkcert](https://github.com/FiloSottile/mkcert)'s locally-trusted CA (after `mkcert -install`), else generates a persisted self-signed CA (`~/.local/share/cornus/ingress-ca.pem`). |
| `--from-file` | — | — | Load a context definition (bare Context object, JSON/YAML) as a base layer that individual flags override; repeatable, later files win. |
| `--from-file-override` | — | — | Load a context definition that overrides the individual flags; repeatable, later files win. |
| `--merge` | — | `false` | Merge the given settings into the existing context instead of replacing it: unset fields keep their stored value (edit-in-place). |

## cornus config delete-context

Remove a context. Clears the current-context pointer if it named the deleted
context.

```sh
cornus config delete-context <name>
```

## cornus config view

Print the client config file, with bearer tokens redacted by default.

```sh
cornus config view [flags]
```

`--export` instead prints a single context as a bare Context object (no
`contexts:` wrapper) that round-trips into `set-context --from-file`; in that
mode the token is included by default (the point is a reusable export) unless
`--redact`. Without `--export`, the exported context is selected by the global
`--context` flag, otherwise the current context.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--show-tokens` | — | `false` | Print bearer tokens instead of redacting them (whole-file view). |
| `--export` | — | `false` | Print only one context as a bare Context object, ready to feed back into `set-context --from-file`. |
| `--redact` | — | `false` | With `--export`, replace the bearer token with `REDACTED` (export includes the real token by default). |
| `-o`, `--output-file` | — | stdout | Write to this file (created `0600`) instead of stdout. |

## Examples

Create a context that talks to a server directly and make it current:

```sh
cornus config set-context prod --server https://cornus.example.com:5000 --token "$TOKEN"
cornus config use-context prod
```

Create a cluster context that auto-detects the in-cluster Service and mints a
ServiceAccount token:

```sh
cornus config set-context staging \
  --namespace cornus-system \
  --kube-auth-service-account cornus-client \
  --kube-auth-audience cornus
```

Edit an existing context in place (keep unset fields):

```sh
cornus config set-context prod --merge --conduit-mode socks5
```

Export one context (with its token) for reuse elsewhere:

```sh
cornus config view --export --context prod -o prod-context.yaml
```
