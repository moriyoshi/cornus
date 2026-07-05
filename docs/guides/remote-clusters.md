# Working with remote clusters

Cornus is just as useful pointed at a remote server or an in-cluster deployment
as it is locally. The build engine, the registry, and the deploy engine all run
on the server, while your source, secrets, and bind mounts stay on your machine
and are streamed on demand. This page covers the pieces that make working
against a remote Cornus feel local: the endpoint flags, connection profiles,
automatic port-forwarding into a cluster, and minting credentials from your
Kubernetes access.

The two other halves of the remote story live next door: streaming a build
context to a remote builder is in [Building images](/guides/building-images),
and bind-mounting client-local directories into a remote workload is in
[Deploying workloads](/guides/deploying-workloads).

To build a cluster profile interactively (auto-detecting the in-cluster Service,
choosing auth, and generating a helm values snippet), run the
[`cornus setup`](/cli/setup) wizard.

## How it works

### Pointing the CLI at a server

Every client command that talks to a Cornus server takes an endpoint and reads a
bearer token from the environment:

| Setting | Env var | Used by |
| --- | --- | --- |
| `--server` | `CORNUS_SERVER` | `deploy`, `exec`, `port-forward`, `socks5`, `tunnel`, `compose`, `hub`, ... |
| `--builder` | `CORNUS_BUILDER` | `build` (the remote build attach endpoint) |
| `CORNUS_TOKEN` | `CORNUS_TOKEN` | bearer auth on `/.cornus/v1/*`, the archive `PUT`, and the WebSocket attaches |

An explicit endpoint on the command wins; otherwise it is resolved from the
selected connection profile (see below). The endpoint accepts `http(s)://` or
`ws(s)://` forms.

Re-typing the endpoint and token on every invocation gets old fast; connection
profiles remove them from the command line entirely.

### Connection profiles

`cornus config` manages a kubeconfig-style file (default the platform user
config dir, override with `--config-file` / `CORNUS_CONFIG`) that stores a named
connection once so every command uses it with nothing on the command line.

Profiles are honored by `deploy`, `exec`, `port-forward`, `socks5`, `tunnel`,
`compose`, `daemon docker`, `build`, and `hub`. Endpoint precedence is an
explicit flag, then the selected context's server; token precedence is
`CORNUS_TOKEN`, then the profile token. Select a profile per command with
`--context <name>` (env `CORNUS_CONTEXT`). A whole context can be loaded from a
JSON/YAML file (`--from-file` as a base layer, `--from-file-override` to let the
file win) and exported for round-tripping (`config view --export`). The full
field set is documented in [connection config](/reference/connection-config);
the command itself is [`cornus config`](/cli/config).

**See also:** [connection config](/reference/connection-config), [cornus config](/cli/config)

## Point a one-off command at a remote server

Target a server for a single command without creating a profile.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_SERVER=https://cornus.example.com CORNUS_TOKEN="$TOKEN" cornus exec -it web -- sh
```

- `--server` takes precedence over `CORNUS_SERVER`, which takes precedence over the selected profile. The endpoint accepts `http(s)://` or `ws(s)://`.
- The bearer token is read from `CORNUS_TOKEN` (or the profile); it is never a command flag.

**See also:** [cornus deploy](/cli/deploy)

## Create a connection profile for a remote server

Store a server URL, token, and TLS material once so commands need nothing on the command line.

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod
cornus deploy -f app.yaml
```

- `set-context` replaces the named context by default; pass `--merge` to edit in place and keep unset fields.
- Layering order is `--from-file` (base), then flags, then `--from-file-override` (top).

**See also:** [cornus config](/cli/config), [connection config](/reference/connection-config)

## Auto port-forward into an in-cluster server via a profile

For an in-cluster Cornus with no ingress, a profile can name the **Service**
instead of a URL, and the CLI opens the port-forward itself — the embedded
`kubectl port-forward` equivalent — for the lifetime of each command.

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster
cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

- Leave `--server` unset: an empty `server` with a `port-forward` block dials the in-cluster Service. No background `kubectl port-forward svc/cornus 5000:5000 &` is needed — the forward is set up and torn down around each command.
- `--pf-kube-context` selects a kubeconfig context; `--pf-service` skips Service auto-detection.

Reaching a deployed **workload's** ports is a separate, automatic concern: any
`ports:` a session publishes are tunneled to `127.0.0.1:<host>`, and
[`cornus port-forward`](/cli/port-forward) reaches even unpublished container
ports on demand. For a cluster profile both go straight to the workload pod over
SPDY with your kubeconfig, falling back to a tunnel through the Cornus server.

**See also:** [cornus config](/cli/config), [Networking and conduits](/guides/networking)

## Mint short-lived credentials from your own kube access

When Cornus runs in the cluster and trusts the cluster's own OIDC issuer, a
profile can **mint the bearer token from your Kubernetes access** instead of
storing a static one. The CLI requests a short-lived, audience-scoped
ServiceAccount token via the Kubernetes TokenRequest API and sends it as the
credential; Cornus verifies it against the cluster JWKS, so there is no
separately provisioned Cornus token.

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # mints a cluster token AND port-forwards -- no static token
```

- `--kube-auth-audience` must match the server's `CORNUS_JWT_AUDIENCE`.
- `--kube-auth-namespace` / `--kube-auth-kube-context` default to the `--pf-*` values; `--kube-auth-expiration-seconds` defaults to `3600`.

Server side, point the in-cluster Cornus at the cluster's JWKS and require the
same audience — this is the standard JWKS verify path, no server code change.

**See also:** [connection config](/reference/connection-config), [Security and authentication](/guides/security)

## Switch, view, and delete profiles

Manage the set of connection profiles, kubeconfig-style.

```sh
cornus config get-contexts          # list profiles (current marked *)
cornus config use-context staging   # make staging the default
cornus config current-context       # print the current context name
cornus config view                  # print the file (tokens redacted)
cornus config delete-context old    # remove a profile
```

- `view --show-tokens` prints bearer tokens; `view --export --context prod` emits one bare Context object that round-trips into `set-context --from-file`.
- `delete-context` clears the current-context pointer if it named the deleted context.

**See also:** [cornus config](/cli/config)

## Set a default namespace for a profile

Record the namespace of the cornus install so cluster detection and kube-auth default to it.

```sh
cornus config set-context staging -n cornus-system
```

- `-n`/`--namespace` auto-detects the Service and port unless `--pf-service` or `--no-detect` is set; add `--no-detect` to store the namespace without contacting the cluster.

**See also:** [cornus config](/cli/config), [connection config](/reference/connection-config)

## Route client-to-workload traffic through the server

For a cluster profile, logs and port-forward normally go straight to the pod
with your kubeconfig. To force the server-routed path instead, set `via-server`.

```sh
cornus config set-context cluster --merge --via-server
cornus port-forward --via-server web 8080:80    # per-command override
```

- Precedence is the per-command `--via-server` / `--no-via-server` flag (`--no-via-server` forces direct), then `CORNUS_VIA_SERVER` (`1`/`0`), then the profile field.
- It changes transport only; a `kube-auth` profile still mints its cluster token.

**See also:** [cornus port-forward](/cli/port-forward)

## Tail logs and exec against a remote deployment

Stream a workload's logs and run commands inside it over the resolved server or profile.

```sh
cornus compose logs --follow --tail 100 web
cornus exec -it web -- sh
```

- For a cluster profile, logs and exec go straight to the pod with your kubeconfig, falling back to the server proxy; `--via-server` forces the server-routed path.
- Everything after `--` in `exec` is passed to the command verbatim; `-t` downgrades to a plain stream when stdin is not a terminal.

**See also:** [cornus exec](/cli/exec), [cornus compose](/cli/compose)
