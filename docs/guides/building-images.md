# Building images

Task-oriented recipes for the in-process BuildKit engine, run locally or on a
remote cornus server. For every flag and its behavior, see [cornus build](/cli/build).

## Build a Dockerfile and push to the bundled registry

Build the image named by `-t` from a context directory and push it to the target registry.

```sh
cornus build -t localhost:5000/app:latest .
```

- The positional context defaults to `.`; use `-f docker/Dockerfile` for a non-default Dockerfile path (relative to the context).
- `--insecure` (default `true`) allows pushing to a plain-HTTP registry such as `localhost:5000`.

**See also:** [cornus build](/cli/build), [registry](/guides/registry)

## Build without pushing (--no-push)

Build the image only, leaving nothing in the registry.

```sh
cornus build -t localhost:5000/app:latest --no-push .
```

- Useful to validate a Dockerfile or warm the cache without publishing a tag.

**See also:** [cornus build](/cli/build)

## Pass build args (--build-arg)

Set build-time variables consumed by `ARG` in the Dockerfile.

```sh
cornus build -t localhost:5000/app:latest \
  --build-arg VERSION=1.2.3 \
  --build-arg COMMIT=$(git rev-parse --short HEAD) .
```

- `--build-arg` is repeatable, one `KEY=VALUE` per flag.

**See also:** [cornus build](/cli/build)

## Use a build cache mount (RUN --mount=type=cache)

Persist a package or compiler cache directory across builds. This is a Dockerfile feature, no CLI flag needed.

```dockerfile
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add --no-cache curl
```

```sh
cornus build -t localhost:5000/app:latest .
```

- The cache lives in the build engine; it survives between builds on the same host or remote builder.

**See also:** [cornus build](/cli/build)

## Pass a secret to a build (--secret id=NAME,src=PATH)

Mount a secret file into a `RUN --mount=type=secret` step without baking it into the image.

```sh
cornus build -t localhost:5000/app:latest \
  --secret id=npmrc,src=$HOME/.npmrc .
```

```dockerfile
RUN --mount=type=secret,id=npmrc,target=/root/.npmrc npm ci
```

- `--secret` is repeatable. If `src` is omitted it defaults to the id.
- On a remote build ( `--builder` ) the secret streams to the server over 9P/WebSocket and never lands in a layer.

**See also:** [cornus build](/cli/build), [credentials](/guides/credentials)

## Forward an SSH agent to a build (--ssh)

Give a `RUN --mount=type=ssh` step access to your local ssh-agent, e.g. to clone a private repo.

```sh
cornus build -t localhost:5000/app:latest --ssh default .
```

```dockerfile
RUN --mount=type=ssh git clone git@github.com:me/private.git
```

- `--ssh` is repeatable and takes `default` or `ID[=SOCKET]`; a missing socket falls back to `$SSH_AUTH_SOCK`.

**See also:** [cornus build](/cli/build)

## Use named build contexts (--build-context NAME=PATH)

Expose an extra directory to the build so a step can bind-mount it with `from=NAME`.

```sh
cornus build -t localhost:5000/app:latest \
  --build-context data=./data .
```

```dockerfile
RUN --mount=type=bind,from=data,target=/data ./import.sh /data
```

- `--build-context` is repeatable. On a remote build the directory is streamed to the server (eagerly by default, or lazily with `--lazy`).

**See also:** [cornus build](/cli/build)

## Build on a remote server (--builder) and stream context lazily (--lazy)

`cornus build --builder` runs the build on a Cornus server while streaming the
caller's context, named bind directories, secrets, and SSH agent over
**9P-on-WebSocket**. The build stays BuildKit-native and caches stay on the
server; the host needs no Docker and no build privileges.

```sh
cornus build --builder ws://build-server:5000/.cornus/v1/build/attach \
  -t build-server:5000/app:v1 \
  --build-context data=./big-data \
  --lazy ./context
```

Inside the Dockerfile the streamed inputs appear as ordinary buildx mounts
(`RUN --mount=type=bind,from=data`, `--mount=type=secret,id=token`,
`--mount=type=ssh`). The caller's ssh-agent is forwarded for `type=ssh` mounts,
so a private dependency fetch works without the key ever leaving your machine.

- `--builder` accepts a `ws://` / `wss://` or `http(s)://` base URL (env `CORNUS_BUILDER`); a selected connection profile that names a server also routes the build remotely, and an explicit `--builder` still wins.
- By default the named context is synced eagerly. With `--lazy` (or `CORNUS_LAZY_BUILD`) it is served on demand instead, so only the bytes the build actually reads cross the wire — a 20 MB context whose build reads 11 bytes transfers 11 bytes. Lazy is not supported on the `containerd` build worker (`CORNUS_BUILD_WORKER=containerd`).
- Build caches keyed with `type=local` use a name rather than a filesystem path, so the same `--cache-to` / `--cache-from` works identically for local and remote builds.

**See also:** [cornus build](/cli/build), [remote clusters](/guides/remote-clusters)

## Import/export a remote build cache (--cache-to / --cache-from)

Persist and reuse the build cache across machines or CI runs using a registry-backed cache.

```sh
cornus build -t localhost:5000/app:latest \
  --cache-to type=registry,ref=localhost:5000/app:cache \
  --cache-from type=registry,ref=localhost:5000/app:cache .
```

- Both flags are repeatable and take buildx-style specs. For `type=local`, the `dest=` / `src=` value is an engine-managed key (auto-derived from `--tag` if omitted), not a filesystem path, so it works the same for local and remote builds.

**See also:** [cornus build](/cli/build)

## Force a clean build (--no-cache)

Ignore any cached layers and rebuild every step from scratch.

```sh
cornus build -t localhost:5000/app:latest --no-cache .
```

- Use to reproduce a build deterministically or after upstream base-image changes.

**See also:** [cornus build](/cli/build)

## Build rootless (--rootless)

Run the local build inside user namespaces instead of as root.

```sh
cornus build -t localhost:5000/app:latest --rootless .
```

- Also settable server-wide via `CORNUS_ROOTLESS`. Needs a working rootless user-namespace stack on the host.

**See also:** [cornus build](/cli/build), [Security and authentication](/guides/security)
