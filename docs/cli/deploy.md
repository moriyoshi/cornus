# cornus deploy

Apply (or delete) a deployment spec, locally or against a remote cornus server.

## Synopsis

```sh
cornus deploy -f <spec> [flags]
```

## Description

`cornus deploy` reads a deployment spec (YAML or JSON) and applies it. Without
`--server` it deploys to the local backend on this host; with `--server` it
deploys against a remote cornus server. See [Deploy spec](/reference/deploy-spec)
for the file format.

### Knative Serving descriptors

`-f` also accepts a **Knative Serving Service manifest** (`serving.knative.dev/v1`,
Kind `Service` — a "ksvc"), a first-class descriptor alongside the native spec,
docker-compose, and devcontainers. `cornus deploy` detects one by its
`apiVersion`/`kind` and translates it into a deployment (image, env, ports,
command/args, resources, exec probes, plus the autoscaling knobs `minScale`,
`maxScale`, `target`, `class`, `metric`, `containerConcurrency`, `timeoutSeconds`).

On a Kubernetes cluster that has **Knative Serving installed**, the deploy
round-trips into a native `serving.knative.dev/v1` Service, so Knative's
autoscaler owns replicas and scale-to-zero and its Route provides the URL
(reported in the deploy status). On any other target — a plain cluster, or the
`dockerhost`/`containerd`/`bare` backends — the workload runs as an ordinary
container and a warning notes that autoscaling is not realized; set
`CORNUS_KNATIVE_STRICT=true` to fail instead of degrading. `cornus restart` cuts a
new revision; `stop`/`start` do not apply to a scale-to-zero service.

```bash
cornus deploy -f service.yaml --server wss://cornus.example.com
```

Serving only (no Eventing) and a single always-latest revision (no traffic
splitting) are supported today; a ksvc combined with mounts, user networks,
volumes, or the proxy/DNS/hub roles is rejected rather than partly applied.

The local backend is chosen by `CORNUS_DEPLOY_BACKEND`: `dockerhost` (the
default) or `containerd`. Any other value — including `kubernetes`, which only
the server honors — falls back to `dockerhost` with a warning. See
[Deploy backends](/reference/deploy-backends).

Against a `--server`, the default is a foreground deploy-attach session:
client-local bind mounts (including `--local-mount`) are streamed over 9P,
published ports are auto-forwarded to local listeners unless `--no-forward-ports`
is set, and `Ctrl-C` (or `SIGTERM`) requests a graceful teardown. With
`--detach`, the spec is POSTed once and the command exits, leaving the workload
running; tear it down later with `cornus deploy -f <spec> --delete --server <url>`.
Detached deploys reject client-local mounts and client-sourced credentials, and
published ports bind on the server host rather than being auto-forwarded. See
[Working with remote clusters](/guides/remote-clusters).

The `--conduit` flag selects how a `--server` session reaches the workload:
per-port local listeners (`port-forward`, the default) or a single SOCKS5
split-tunnel proxy reaching services by name (`socks5`). It takes precedence
over the `CORNUS_CONDUIT` environment variable and the profile mode;
`--no-forward-ports` disables the conduit entirely.

With `--conduit socks5`, `--ingress-conduit` additionally reaches the
deployment's declared ingress host (`ingress:` / `x-cornus-ingress`) through the
proxy — `native` tunnels to the real cluster ingress controller, `emulate` runs a
client-side reverse proxy with a generated cert. See
[Ingress](/guides/ingress).

The `--egress-*` flags route container egress through the client-side network.
See [Egress](/guides/egress).

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `-f`, `--file` | — | required | Deployment spec file (YAML or JSON). |
| `--delete` | — | `false` | Delete the named deployment instead of applying it (works locally and against a `--server`). |
| `-d`, `--detach` | — | `false` | Stateless remote deploy: POST the spec to the `--server`, print the status, and exit; the workload persists with no client session. Client-local bind mounts are rejected and published ports are not auto-forwarded. A no-op for local deploys. |
| `--server` | — | — | Remote cornus server URL (`http(s)://` or `ws(s)://`). When set, deploy runs against the remote server. |
| `--local-mount` | — | — | Client-local bind mount `SRC:DST[:ro][,cache][,async]` served over 9P to a `--server`. `cache` is immutable and read-only; `async` is writable, cache-coherent, and single-writer only. Repeatable. |
| `--no-forward-ports` | — | `false` | Do not auto-forward published ports to local listeners during a `--server` session (also disables the conduit). |
| `--conduit` | `CORNUS_CONDUIT` | profile mode | Session conduit mode: `port-forward` (default) or `socks5`. A bare word sets only the mode; a `socks5://host:port[?suffix=SUFFIX]` URL also overrides the bind address and service-host suffix (`socks5h://` is a synonym). Takes precedence over `CORNUS_CONDUIT` and the profile mode. |
| `--ingress-conduit` | `CORNUS_INGRESS_CONDUIT` | profile | Reach the deployment ingress through the SOCKS5 conduit: `native` (tunnel to the real cluster ingress controller) or `emulate` (a client-side reverse proxy with a generated cert), or `off`. Requires `--conduit socks5`. Takes precedence over `CORNUS_INGRESS_CONDUIT` and the profile. See [Ingress](/guides/ingress). |
| `--via-server`, `--no-via-server` | `CORNUS_VIA_SERVER` | profile | Route auto-forwarded ports through the cornus server proxy instead of connecting to pods directly with your kubeconfig (cluster profiles only). `--no-via-server` forces the direct path. Overrides `CORNUS_VIA_SERVER` and the profile. |
| `--egress` | — | — | Route container egress through the client-side network: `env` (propagate proxy vars), `proxy` (caretaker forward proxy), or `transparent` (nftables + relay). |
| `--egress-route` | — | — | Egress routing rule `PATTERN=ROUTE` (route: `client`, `gateway`, `cluster`, or `deny`), first match wins. Repeatable. |
| `--egress-default` | — | `cluster` | Egress route for unmatched destinations: `cluster` (default), `client`, `gateway`, or `deny`. |
| `--egress-pac` | — | — | Path to a PAC-style JS file (`FindProxyForURL`) that decides egress routing; supersedes `--egress-route`. |
| `--telemetry-endpoint` | — | — | Enable the embedded Collector and export workload telemetry to this OTLP endpoint. |
| `--telemetry-protocol` | — | `grpc` | Exporter protocol: `grpc` or `http/protobuf`. |
| `--telemetry-header` | — | — | Static OTLP export header `KEY=VALUE`. Repeatable. |
| `--telemetry-insecure` | — | `false` | Disable transport security to the OTLP endpoint. |
| `--telemetry-signal` | — | all | Restrict pipelines to `traces`, `metrics`, or `logs`. Repeatable. |
| `--telemetry-service-name` | — | deployment name | Override injected `OTEL_SERVICE_NAME`. |
| `--telemetry-debug` | — | `false` | Also log collected telemetry to Collector stdout. |

The `CORNUS_DEPLOY_BACKEND` environment variable selects the local backend
(`dockerhost` default, or `containerd`).

## Examples

Apply a spec to the local Docker host:

```sh
cornus deploy -f app.yaml
```

Deploy against a remote server and stay in the foreground:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
```

Detached deploy, then tear down later:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com --detach
cornus deploy -f app.yaml --server https://cornus.example.com --delete
```

Stream a local directory into the workload and reach services over SOCKS5:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --local-mount ./data:/data:ro \
  --conduit socks5
```

Route egress through the client with a routing rule:

```sh
cornus deploy -f app.yaml --server https://cornus.example.com \
  --egress proxy \
  --egress-route 'api.internal=client' \
  --egress-default deny
```

Delete a local deployment:

```sh
cornus deploy -f app.yaml --delete
```

## See also

- [Deploy spec](/reference/deploy-spec)
- [Deploy backends](/reference/deploy-backends)
- [Working with remote clusters](/guides/remote-clusters)
- [Egress](/guides/egress)
- [Credentials](/guides/credentials)
- [`cornus exec`](/cli/exec)
- [`cornus port-forward`](/cli/port-forward)
