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

  remote-docker:
    # No static server URL: carry HTTP to the remote loopback listener over SSH.
    ssh-tunnel:
      addr: devbox
      user: ops
      remote-addr: 127.0.0.1:5000

  staging:
    server: https://cornus.staging.example.com
    # Every image in this environment is Debian-based; try bash first.
    shells:
      - /bin/bash
      - /bin/sh
    key-auth:
      identity-file: /home/alice/.ssh/id_ed25519
      key-fingerprint: SHA256:example
      name: alice-laptop
    tls:
      ca-cert: /etc/cornus/staging-ca.pem
    conduit:
      mode: socks5
      socks5:
        listen: 127.0.0.1:1080
        service-host-suffix: .cornus.internal
      ingress:
        mode: emulate
        certificates:
          - certificate: /etc/cornus/web.pem
            key: /etc/cornus/web-key.pem

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
| `key-auth` | [KeyAuth](#keyauth) | — | When set, proves possession of an enrolled SSH key and mints a short-lived session. It takes precedence over `kube-auth` and `token`, but yields to `CORNUS_TOKEN`. `key-auth` and `kube-auth` are mutually exclusive. |
| `via-server` | bool (nullable) | unset (direct) | Forces workload streaming operations (compose logs, port-forward) to route through the cornus server proxy instead of the CLI reaching workload pods directly with the developer's kubeconfig. Only matters for a cluster profile. Lowest-precedence layer, below the `CORNUS_VIA_SERVER` env var and the `--via-server` flag. Transport-only: it does not disable `kube-auth` token minting. |
| `conduit` | [Conduit](#conduit) | port-forward | How a client session exposes a deployment's ports to the caller. Lowest-precedence layer, below the `CORNUS_CONDUIT` env var and the `--conduit` flag. See [Networking and conduits](/guides/networking). |
| `ssh-tunnel` | [SSHTunnel](#sshtunnel) | — | When `server` is empty, reaches a cornus server through SSH. This is the host-backend analogue of `port-forward`; the two automatic transports are mutually exclusive. An explicit `server` makes this block inert. |
| `tunnel` | [Tunnel](#tunnel) | — | Defaults for public tunnels ([`cornus tunnel`](/cli/tunnel), [`cornus ingress-tunnel`](/cli/ingress-tunnel)), so they need not be repeated on every invocation. |
| `shells` | list of strings | — | Candidate interactive shells for workloads reached through this profile, most preferred first. Read by the [`cornus web`](/cli/web#terminal-shell-discovery) terminal, which probes a workload's own `x-cornus-shells:` first, then these, then the browser's own list. Each entry is a command **string**, not a pre-split argument list (`/bin/busybox sh` is one entry). Security-sensitive: it names a binary that gets executed inside your workload, so a project override supplies it only when trusted. |

## `KeyAuth`

Selects the SSH signer used for short-lived Cornus client sessions. The profile
contains only a path and public fingerprint, never private-key material or a
minted session token.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `identity-file` | string | — | Local SSH private-key path. Encrypted keys use the normal interactive/`SSH_ASKPASS` prompt. |
| `key-fingerprint` | string | — | SHA256 public-key fingerprint. With no identity file it selects a key from `SSH_AUTH_SOCK`; with a file it pins the expected public key and lets the background agent address the session cache without unlocking the key. |
| `name` | string | fingerprint | Human-readable enrollment name and resulting caller identity. |
| `scope` | string | `api` | Requested session scope. |
| `ttl` | string | `1h` | Requested Go-duration lifetime, at most `24h`. |

## `Conduit`

A context's session conduit preference: the mode plus, for SOCKS5, its proxy settings.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | string | `port-forward` | `port-forward` (per-port automatic forwarding, Compose-like) or `socks5` (a single client-side SOCKS5 split-tunnel proxy). |
| `socks5` | [Socks5](#socks5) | — | Tunes the SOCKS5 proxy; consulted only when `mode` is `socks5`. |
| `ingress` | [Ingress](#ingress) | — | Configures native or emulated ingress handling and optional user-provided server certificates. |

## `Socks5`

Configures the SOCKS5 split-tunnel proxy.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `listen` | string | `127.0.0.1:1080` | Local address the proxy binds. |
| `service-host-suffix` | string | `.cornus.internal` | Builds the everyday default resolution rule: a CONNECT host bearing this suffix is stripped to a service name and tunneled in, everything else egresses directly. Ignored when `resolve` is set. |
| `resolve` | [][ResolveRule](#resolverule) | — | An advanced, ordered list of resolution rules that replaces the suffix default entirely; the first matching rule wins. |
| `bare-service-names` | bool (nullable) | enabled | Whether a bare, single-label host that names a live service (e.g. `web`, in addition to `web.cornus.internal`) is routed inward. Set `false` to disable it when a service name would shadow a real single-label host reached directly. |

## `SSHTunnel`

Describes the SSH connection used to reach a cornus server on a remote container
host. The transport is backend-agnostic — it carries raw bytes to a cornus
server, so the same block reaches a `dockerhost`, `containerd`, `bare`, or
`incus` server unchanged. Once it is configured, ordinary commands use it
transparently; no per-command tunnel flag is required. `addr` may be an
`ssh_config` host alias, so the normal user, port, identity, proxy, and host-key
settings continue to apply unless `no-ssh-config` disables them.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `addr` | string | — | SSH destination: an `ssh_config` `Host` alias or a literal `host[:port]`. |
| `user` | string | `ssh_config`, then current user | SSH login user. |
| `remote-addr` | string | `127.0.0.1:5000` | Cornus listen address as seen from the remote host. |
| `identity-file` | string | SSH agent / `ssh_config` | Explicit PEM private-key path for public-key authentication. |
| `no-agent` | bool | `false` | Disables authentication through the local `SSH_AUTH_SOCK`. |
| `known-hosts` | string | `ssh_config`, then `~/.ssh/known_hosts` | Explicit `known_hosts` file used for host-key verification. |
| `host-key` | string | — | Pins one expected host key as an `authorized_keys`-format line. |
| `insecure-host-key` | bool | `false` | Disables host-key verification. Development use only. |
| `remote-tls` | bool | `false` | Uses HTTPS through the SSH tunnel because the remote cornus process terminates TLS. Usually paired with `tls.server-name`. |
| `no-ssh-config` | bool | `false` | Skips both user and system SSH config files; only the explicit fields in this block are used. |
| `use-ssh-binary` | bool | auto | Forces the persistent `ssh -N -L` fallback transport. Cornus selects it automatically when the resolved host has a `ProxyCommand`; it honors the full OpenSSH configuration, including `Match`. |

## `Ingress`

Configures ingress reached through a SOCKS5 conduit. Its certificate rules are also
used before a native Kubernetes deploy, including a detached deploy, to create and
wire managed TLS Secrets; that materialization does not require a conduit to remain
running.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | string | off | `native` uses the cluster ingress controller; `emulate` terminates ingress locally; empty/off disables ingress handling. |
| `controller` | [IngressController](#ingresscontroller) | discovered | Native ingress controller Service override. |
| `ca-file` | string | generated | Emulate-mode CA certificate used to sign fallback leaf certificates. Must be paired with `ca-key-file`. |
| `ca-key-file` | string | generated | Private key paired with `ca-file`. |
| `certificates` | [][IngressCertificate](#ingresscertificate) | — | Ordered user-provided server-certificate rules shared by emulated and native ingress. |

## `Tunnel`

Per-profile defaults for public tunnels. It stores **no credential** — only the
path to one — so a profile that is shared or checked in can never leak an
authtoken.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `authtoken-file` | string | — | Path to a file holding the tunnel-backend credential, used as the default `--authtoken-file`. Empty means passing one per invocation, or relying on the server's own default (`CORNUS_TUNNEL_AUTHTOKEN` in the server's environment). |
| `ingress-host-mode` | string | `auto` | Default `--host-mode` for [`cornus ingress-tunnel`](/cli/ingress-tunnel): `auto`, `passthrough`, `alias`, or `rewrite`. See [host handling](/cli/ingress-tunnel#host-handling). |

An explicit flag always wins over these defaults. [`cornus setup`](/cli/setup)
offers to fill them in after probing what the server can actually host.

## `IngressCertificate`

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `pattern` | string | certificate DNS SANs | Exact DNS name or one-label wildcard such as `*.example.com`. An explicit pattern must be covered by the certificate SANs. Exact rules win over wildcards; among wildcards, the longest suffix wins. |
| `certificate` | string | — | Path to a PEM certificate chain. Required with `key`. |
| `key` | string | — | Path to the matching PEM private key. Required with `certificate`. |

For emulated ingress, SNI selects a rule and an unmatched name uses the configured
or generated fallback CA. For native Kubernetes ingress, every explicit concrete
ingress host must match a rule. Cornus groups hosts that select the same certificate,
creates stable `kubernetes.io/tls` Secrets owned by the workload Deployment, updates
them on certificate rotation, wires them into the Ingress, and removes obsolete
managed Secrets. Auto-derived hosts and the `@` token must be expanded to concrete
hostnames when managed certificates are used.

Because native materialization sends private-key bytes with the deploy request,
Cornus permits it only over HTTPS, an SSH tunnel/custom dialer, or plaintext HTTP on
loopback (including a local Kubernetes port-forward). It rejects remote plaintext
HTTP before serializing the request.

## `IngressController`

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `kube-context` | string | profile cluster context | Kubeconfig context used for the native controller port-forward. |
| `namespace` | string | — | Namespace containing the ingress controller Service. |
| `service` | string | discovered | Ingress controller Service name. |
| `http-port` | int | discovered | Controller HTTP Service port. |
| `https-port` | int | discovered | Controller HTTPS Service port. |

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
| `server-name` | string | URL hostname | Overrides the SNI and certificate hostname, for example when `remote-tls` reaches a certificate-bearing server through `127.0.0.1`. |
| `insecure-skip-verify` | bool | `false` | Disables server certificate verification. Testing only. |
| `client-cert` | string | — | Path to a PEM client certificate for mTLS. |
| `client-key` | string | — | Path to the matching PEM client key for mTLS. |

See [Security and authentication](/guides/security) for the server side of mTLS and bearer authentication.

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

## `TokenExchange`

Trade whatever credential the fields above produced for a short-lived Cornus
credential via [OAuth 2.0 Token Exchange](/guides/security#exchange-a-third-party-token-for-a-cornus-credential),
cached between commands.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | Perform the exchange. |
| `scope` | string | — | Narrow the issued credential (e.g. `registry:pull`). Empty takes whatever the server's scope map grants. |

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus \
  --token-exchange --token-exchange-scope registry:pull
```

- It is independent of which field produced the subject token, so a cluster
  ServiceAccount token, an OIDC token, and a static `token` all exchange the same
  way.
- `scope` may only **narrow**. A scope the server's policy does not grant is
  refused rather than quietly reduced, so a profile that pins one fails loudly if
  policy changes underneath it instead of silently gaining access.
- A `key-auth` profile is left alone: that credential is already Cornus-minted and
  names its scope, so there is nothing to exchange.
- A server with no exchange endpoint — an older Cornus, or one with no JWT/JWKS
  verifier — is not an error. The credential is sent directly, exactly as before.

The issued credential is cached so the exchange happens once per token lifetime
rather than once per command; see [`CORNUS_TOKEN_CACHE`](/reference/server-env-vars).

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

An auto-discovered file is working-tree input, not a trusted credential store. By default it contributes only `via-server`; endpoint, token, TLS, registry, port-forward, kube-auth, SSH-tunnel, conduit, and shell settings are ignored. On Unix, Cornus also ignores a file owned by another user or one in a world-writable non-sticky directory.

`shells` is in that stripped set even though it carries no credential: it names a binary the web terminal executes inside your workload, so a file anyone who can open a pull request may write must not choose it.

Use `--trust-context-file` / `CORNUS_TRUST_CONTEXT_FILE=1` only for a trusted working tree. An explicitly named `--context-file` is also trusted. An override that changes the endpoint must supply its own `token` or `kube-auth`; otherwise the selected context credential is dropped. Cornus warns whenever it skips or strips a project override.

## See also

- [`cornus config`](/cli/config) — create, select, and edit contexts.
- [Networking and conduits](/guides/networking) — conduit modes and port-forwarding.
- [Working with remote clusters](/guides/remote-clusters) — driving a remote server from a profile.
- [Security and authentication](/guides/security) — bearer tokens, mTLS, and cluster-minted identities.
