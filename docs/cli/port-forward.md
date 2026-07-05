# cornus port-forward

Forward one or more local ports to a deployment's container ports, reaching
ports that were never published to a host or exposed through a Service.

## Synopsis

```sh
cornus port-forward [flags] <name> <ports...>
```

## Description

`cornus port-forward` binds a local listener per mapping and forwards each
accepted connection over its own tunnel to the first instance of a deployment,
much like `kubectl port-forward`. It stays in the foreground until `Ctrl-C`
(or `SIGTERM`).

For a cluster connection profile it tunnels each connection straight to the
workload pod over the Kubernetes `pods/portforward` SPDY subresource using your
kubeconfig credentials (the server's own ServiceAccount usually cannot),
falling back to the server proxy only when the direct attempt cannot open a
tunnel. For a non-cluster profile it tunnels through the cornus server, which
bridges to the container. Either way it reaches ports that were never published
to a host or exposed via a Service.

Each port mapping is `LOCAL:REMOTE` (or a bare `PORT` for the same local and
container port), optionally with a `/tcp` or `/udp` suffix in Compose ports
notation (default `tcp`), for example `5353:53/udp`. Ports must be in
`1..65535`. A `/udp` mapping forwards datagrams instead of a byte stream and is
supported on the dockerhost, containerd, and bare backends; Kubernetes port-forward is
TCP-only, so such mappings are skipped with a warning.

The connection is resolved from `--server`, otherwise from the selected
connection profile (see [`cornus config`](/cli/config)). See
[Remote workflows](/topics/remote-workflows) for how port-forward fits the
larger set of ways to reach workloads.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | Remote cornus server URL (`http(s)://` or `ws(s)://`). Falls back to the selected connection profile. |
| `--address` | — | `127.0.0.1` | Local address to bind the listeners on. |
| `--via-server` / `--no-via-server` | `CORNUS_VIA_SERVER` | profile | Route the forward through the cornus server proxy instead of connecting to the pod directly with your kubeconfig (cluster profiles only). `--no-via-server` forces the direct path. Overrides `CORNUS_VIA_SERVER` and the profile. |

Positional arguments:

- `<name>` — deployment name to forward to (required).
- `<ports...>` — one or more `LOCAL:REMOTE[/tcp|/udp]` mappings, or a bare
  `PORT` (required).

## Examples

Forward local port 8080 to container port 80:

```sh
cornus port-forward web 8080:80
```

Forward several ports at once, binding on all interfaces:

```sh
cornus port-forward --address 0.0.0.0 web 8080:80 5432:5432
```

Forward a UDP port (dockerhost / containerd backends):

```sh
cornus port-forward dns 5353:53/udp
```

Force the server-proxy path instead of the direct-to-pod path:

```sh
cornus port-forward --via-server web 8080:80
```
