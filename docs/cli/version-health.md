# cornus version / cornus health

Print the cornus version, or probe a running server's health endpoint.

## cornus version

Print the cornus version.

### Synopsis

```sh
cornus version
```

### Description

`cornus version` prints the binary's version string. It is overridable at build
time with `-ldflags "-X main.version=..."` and defaults to `dev`.

### Examples

```sh
cornus version
```

## cornus health

Probe a cornus server's `/healthz` endpoint and exit non-zero if it is not
healthy.

### Synopsis

```sh
cornus health [flags]
```

### Description

`cornus health` issues an HTTP `GET` to `http://<addr>/healthz` with a 5-second
timeout and exits non-zero unless the server returns `200 OK`. It is meant as a
container healthcheck, so no extra tools (such as `curl`) are needed in the
image.

### Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--addr` | — | `127.0.0.1:5000` | Server address to probe. |

### Examples

Probe the default local address:

```sh
cornus health
```

Probe a specific address:

```sh
cornus health --addr 127.0.0.1:8080
```

Use as a container healthcheck (Dockerfile):

```dockerfile
HEALTHCHECK CMD ["cornus", "health", "--addr", "127.0.0.1:5000"]
```
