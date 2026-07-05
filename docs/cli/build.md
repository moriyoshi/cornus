# cornus build

Build an image from a context with the BuildKit-based engine and push it to a
registry.

## Synopsis

```sh
cornus build -t <ref> [flags] [context]
```

## Description

`cornus build` builds the image named by `-t/--tag` from a build context
directory (the positional argument, default `.`). By default it uses the
in-process build engine on this host and pushes the result to the target
registry.

With `--builder` (or a selected connection profile that names a server), the
build runs on a remote cornus server instead: this machine streams the context,
the `--build-context` directories, and secrets to it over 9P/WebSocket. See
[Remote workflows](/topics/remote-workflows).

On a remote build, a `-t/--tag` **without a registry part** (e.g. `app:v1` or
`team/app:v1`) is qualified to the server's builtin registry — a bare tag names
the *default* registry, and Cornus's default is its own registry, not Docker Hub.
`--registry` / `CORNUS_REGISTRY` overrides that host; when unset it defaults to
the server-advertised registry host, then the builder endpoint host. A tag that
already carries a registry (e.g. `registry.example.com/app:v1`) is left untouched,
and a purely local in-process build leaves bare tags to Docker's own
normalization.

`--build-arg`, `--secret`, `--ssh`, and `--build-context` are all repeatable.
`--cache-to` / `--cache-from` accept buildx-style cache specs; for `type=local`,
the `dest=` / `src=` value is an engine-managed key (auto-derived from `--tag`
if omitted), not a filesystem path.

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `-t`, `--tag` | — | required | Target image reference, e.g. `localhost:5000/app:v1`. |
| `context` (positional) | — | `.` | Build context directory. |
| `-f`, `--file` | — | `Dockerfile` | Path to the Dockerfile, relative to the context. |
| `--build-arg` | — | — | Build args `KEY=VALUE`, repeatable. |
| `--secret` | — | — | Secret mounts `id=NAME,src=PATH` (`RUN --mount=type=secret`), repeatable. If `src` is omitted it defaults to the id. |
| `--ssh` | — | — | SSH agent forwarding: `default` or `ID[=SOCKET]` (`RUN --mount=type=ssh`), repeatable. A missing socket falls back to `$SSH_AUTH_SOCK`. |
| `--build-context` | — | — | Named build context `NAME=PATH` (`RUN --mount=type=bind,from=NAME`), repeatable. |
| `--builder` | `CORNUS_BUILDER` | — | Remote cornus build endpoint (`ws://` or `http(s)://` base URL). When set, the build runs there and this machine streams the context, build-context dirs, and secrets over 9P/WebSocket. |
| `--registry` | `CORNUS_REGISTRY` | derived | Registry host for a `--tag` without a registry part, on remote builds. Defaults to the server-advertised host, else the builder endpoint host. |
| `--rootless` | `CORNUS_ROOTLESS` | `false` | Run the build in rootless mode (user namespaces). |
| `--lazy` | `CORNUS_LAZY_BUILD` | `false` | Serve `--build-context` dirs on demand over 9P (lazy build) instead of syncing them eagerly. Also enabled server-wide by `CORNUS_LAZY_BUILD`. |
| `--cache-to` | — | — | Cache export backend (buildx syntax), e.g. `type=registry,ref=HOST/app:cache[,registry.insecure=true]`. Repeatable. |
| `--cache-from` | — | — | Cache import backend (buildx syntax), e.g. `type=registry,ref=HOST/app:cache[,registry.insecure=true]`. Repeatable. |
| `--no-cache` | — | `false` | Do not use the build cache. |
| `--no-push` | — | `false` | Build only; do not push the result. |
| `--insecure` | — | `true` | Allow pushing to an HTTP (non-TLS) registry. |

## Examples

Build and push a local image:

```sh
cornus build -t localhost:5000/app:v1 .
```

Build with an alternate Dockerfile and a build arg:

```sh
cornus build -t localhost:5000/app:v1 -f docker/Dockerfile --build-arg VERSION=1.2.3 .
```

Pass a secret and forward the SSH agent:

```sh
cornus build -t localhost:5000/app:v1 \
  --secret id=npmrc,src=$HOME/.npmrc \
  --ssh default .
```

Run the build on a remote cornus builder:

```sh
cornus build -t registry.example.com/app:v1 --builder wss://build.example.com .
```

Export and import a registry cache:

```sh
cornus build -t localhost:5000/app:v1 \
  --cache-to type=registry,ref=localhost:5000/app:cache \
  --cache-from type=registry,ref=localhost:5000/app:cache .
```

## See also

- [Remote workflows](/topics/remote-workflows)
- [`cornus push`](/cli/push)
- [Quick start](/introduction/quick-start)
