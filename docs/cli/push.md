# cornus push

Copy an image into a registry (for example cornus's own).

## Synopsis

```sh
cornus push <source> <dest> [flags]
```

## Description

`cornus push` copies an image to the destination registry reference. The source
may be another registry reference or a local image tarball (OCI or
docker-archive). If `source` is an existing file on disk, it is loaded as a
tarball and pushed; otherwise it is parsed as a registry reference and copied.

When `CORNUS_TOKEN` is set, cornus authenticates against the destination
registry with that bearer token. The token is scoped to the destination
registry host only, so a cross-registry copy never sends the cornus token to an
unrelated source registry.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `source` (positional) | — | required | Source: a registry reference or a local image tarball path. |
| `dest` (positional) | — | required | Destination registry reference, e.g. `localhost:5000/app:v1`. |
| `--insecure` | — | `true` | Allow HTTP (non-TLS) registries. |

`CORNUS_TOKEN`, when set, supplies the bearer token used to authenticate against
the destination registry.

## Examples

Push a local OCI tarball into the cornus registry:

```sh
cornus push ./app.tar localhost:5000/app:v1
```

Copy an image from one registry to another:

```sh
cornus push docker.io/library/nginx:latest localhost:5000/nginx:latest
```

Authenticate against a token-protected registry:

```sh
CORNUS_TOKEN=$(cornus token ...) cornus push ./app.tar registry.example.com/app:v1
```

## See also

- [`cornus build`](/cli/build)
- [`cornus token`](/cli/token)
- [Security and authentication](/guides/security)
