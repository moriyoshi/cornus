# Remote workflows

Cornus is just as useful pointed at a remote server or an in-cluster deployment
as it is locally. The build engine, the registry, and the deploy engine all run
on the server, while your source, secrets, and bind mounts stay on your machine
and are streamed on demand. This page ties together the pieces that make working
against a remote Cornus feel local: the endpoint flags, remote builds, remote
deploys with client-local mounts, connection profiles, session conduits, and
minting credentials from your Kubernetes access.

## Pointing the CLI at a server

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

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_TOKEN=<token> cornus exec -i -t web sh --server https://cornus.example.com
```

Re-typing the endpoint and token on every invocation gets old fast; connection
profiles remove them from the command line entirely.

## Remote builds

`cornus build --builder` runs the build on a Cornus server while streaming the
caller's context, named bind directories, secrets, and SSH agent over
**9P-on-WebSocket**. The build stays BuildKit-native and caches stay on the
server; the host needs no Docker and no build privileges.

```sh
cornus build --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 \
  --build-context data=./data \
  --secret id=token,src=./token.txt \
  --ssh default ./context
```

Inside the Dockerfile the streamed inputs appear as ordinary buildx mounts:

```dockerfile
RUN --mount=type=bind,from=data ...
RUN --mount=type=secret,id=token ...
RUN --mount=type=ssh ...
```

The caller's ssh-agent is forwarded for `type=ssh` mounts, so a private
dependency fetch works without the key ever leaving your machine.

### Lazy contexts

By default the named context is synced eagerly. With `--lazy` (or
`CORNUS_LAZY_BUILD`) the context is served on demand instead, so only the bytes
the build actually reads cross the wire — a 20 MB context whose build reads 11
bytes transfers 11 bytes. Lazy contexts are not supported with
`CORNUS_BUILD_WORKER=containerd`.

```sh
cornus build --lazy --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 --build-context data=./big-data ./context
```

A profile with a `server` routes the build remotely on its own (an explicit
`--builder` still wins). Build caches keyed with `type=local` use a name rather
than a filesystem path, so the same `--cache-to` / `--cache-from` works
identically for local and remote builds. See [`cornus build`](/cli/build) for
the full flag set.

## Remote deploys with client-local bind mounts

`cornus deploy --server` runs the deployment on the remote server while
bind-mounting directories that live on *this* machine, streamed over 9P with
`--local-mount` (or Compose `volumes:`). The deployment lives as long as the
command stays connected, which is what makes it an inner-loop tool: edit a file
locally and the workload sees it.

```sh
cornus deploy --server http://cornus.example:5000 \
  --local-mount ./config:/etc/app:ro --local-mount ./data:/data -f deploy.yaml
```

Published ports from the spec's `ports:` are auto-forwarded to
`127.0.0.1:<host>` on your machine for the session's lifetime — even when the
backend is a Kubernetes cluster — so the workload answers locally. Opt out with
`--no-forward-ports`. Client-local mounts are served from the server's own
`<DataDir>/mounts` area and are always permitted, so they work without relaxing
the host-privilege policy. See [`cornus deploy`](/cli/deploy).

Blindly tunneling 9P means every read crosses the wire, which hurts for large or
write-heavy mounts. Two suffixes opt into a server-side file cache: `,cache`
(implies `:ro`) is a read-through cache for **immutable** inputs like datasets or
model weights, and `,async` is a writable, cache-coherent mount for a
**single-writer** workload such as a development database. Both need the server's
file cache enabled; `,async` mounts can be tuned for database-shaped random I/O
with `CORNUS_BLOCK_COHERENCE` / `CORNUS_BLOCK_READAHEAD` set on both ends. See
[client-local bind mounts](/architecture/deploy-engine#client-local-bind-mounts)
for how the caching works and [server environment variables](/reference/server-env-vars#remote-9p-file-cache-and-writable-mounts)
for the knobs.

```sh
cornus deploy --server http://cornus.example:5000 \
  --local-mount ./models:/models:ro,cache \
  --local-mount ./db:/var/lib/app:async -f deploy.yaml
```

## Connection profiles

`cornus config` manages a kubeconfig-style file (default the platform user
config dir, override with `--config-file` / `CORNUS_CONFIG`) that stores a named
connection once so every command uses it with nothing on the command line.

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod          # make it the default context

cornus config get-contexts              # list profiles (current is marked *)
cornus config view                      # print the file (bearer tokens redacted)

# Commands now need no --server / CORNUS_TOKEN:
cornus deploy -f app.yaml
cornus compose up
```

Profiles are honored by `deploy`, `exec`, `port-forward`, `socks5`, `tunnel`,
`compose`, `daemon docker`, `build`, and `hub`. Endpoint precedence is an
explicit flag, then the selected context's server; token precedence is
`CORNUS_TOKEN`, then the profile token. Select a profile per command with
`--context <name>` (env `CORNUS_CONTEXT`). A whole context can be loaded from a
JSON/YAML file (`--from-file` as a base layer, `--from-file-override` to let the
file win) and exported for round-tripping (`config view --export`). The full
field set is documented in [connection config](/reference/connection-config);
the command itself is [`cornus config`](/cli/config).

### Automatic port-forward into a cluster

For an in-cluster Cornus with no ingress, a profile can name the **Service**
instead of a URL, and the CLI opens the port-forward itself — the embedded
`kubectl port-forward` equivalent — for the lifetime of each command:

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster

cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

No background `kubectl port-forward svc/cornus 5000:5000 &` and no `--server`:
the forward is set up and torn down around each command. `--pf-kube-context`
selects a kubeconfig context. Reaching a deployed **workload's** ports is a
separate, automatic concern: any `ports:` a session publishes are tunneled to
`127.0.0.1:<host>`, and [`cornus port-forward`](/cli/port-forward) reaches even
unpublished container ports on demand. For a cluster profile both go straight to
the workload pod over SPDY with your kubeconfig, falling back to a tunnel through
the Cornus server.

## Session conduits: port-forward vs SOCKS5

The way a session exposes workloads to the caller is its **conduit mode**. The
default is per-port forwarding (one local listener per published port,
Compose-compatible). The opt-in alternative is a single client-side **SOCKS5
split-tunnel proxy**: hostnames under a service-host suffix (default
`.cornus.internal`) are tunneled to the matching workload by name, and every
other destination is dialed directly from your machine. One proxy reaches every
service by name, with no per-port listeners.

```sh
# Make SOCKS5 the conduit for a profile, so compose up / deploy --server use it:
cornus config set-context demo --conduit-mode socks5
# Pin the shared proxy's bind address and suffix in one value:
cornus config set-context demo --conduit-mode 'socks5://.shared:1085?suffix=.demo.internal'

# Per-run override (flag > CORNUS_CONDUIT > profile > default port-forward):
cornus compose up --conduit socks5                    # join the shared proxy
cornus compose up --conduit 'socks5://'               # own proxy, ephemeral port
cornus deploy --server http://cornus.example:5000 --conduit socks5 -f deploy.yaml
```

A bare word (or `socks5://.shared`) joins the profile's shared proxy; a
`socks5://` URL with an authority spins up a private, session-local proxy that
coexists with it. In SOCKS5 mode the shared per-server proxy also covers
`cornus daemon docker` containers, so one proxy reaches Docker containers and
Compose services by name. SOCKS5 CONNECT is TCP-only. The standalone ad-hoc
proxy is [`cornus socks5`](/cli/socks5).

### `--via-server`

For a cluster profile, logs and port-forward normally go straight to the pod
with your kubeconfig. To force the server-routed path instead, set
`via-server`, honored in precedence order: a per-command `--via-server` flag
(`--no-via-server` forces direct), then `CORNUS_VIA_SERVER` (`1`/`0`), then the
profile field. It changes transport only; a `kube-auth` profile still mints its
cluster token.

## Minting short-lived credentials from your Kubernetes access

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

Server side, point the in-cluster Cornus at the cluster's JWKS and require the
same audience — this is the standard JWKS verify path, no server code change.
See [auth and TLS](/topics/auth-and-tls) for the verifier configuration and
[connection config](/reference/connection-config) for the `kube-auth` fields.
