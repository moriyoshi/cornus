# Registry and storage

Cornus bundles a small OCI registry (`/v2/*`) backed by a content-addressable store. These recipes cover choosing a storage backend, moving images in and out, and reclaiming space. For the full backend catalog see [storage backends](/reference/storage-backends).

## Serve the registry with filesystem storage

Persist the registry CAS on local disk (the default when `--storage` is unset).

```sh
cornus serve --storage /var/lib/cornus     # or file:///var/lib/cornus
cornus serve                               # unset: store lives under the data dir
```

- A bare path or `file://path` writes the CAS layout under that directory. Durable across restarts.
- With `--storage` omitted, the store lives under the server data directory (`--data-dir` / `CORNUS_DATA`).

**See also:** [storage backends](/reference/storage-backends), [cornus serve](/cli/serve)

## Serve with ephemeral in-memory storage

Keep the entire registry in process memory for tests and throwaway servers.

```sh
cornus serve --storage mem://
```

- Everything is lost when the server stops. Not for durable registries.

**See also:** [storage backends](/reference/storage-backends), [cornus serve](/cli/serve)

## Serve with S3 or S3-compatible storage

Store the CAS in an S3 bucket, streamed as native S3 multipart uploads.

```sh
# AWS S3 (credentials from the standard AWS chain):
cornus serve --storage 's3://my-bucket?region=us-east-1'

# S3-compatible (MinIO and similar): override endpoint + path style:
cornus serve --storage 's3://my-bucket?region=us-east-1&endpoint=http://localhost:9000&path_style=true&access_key=KEY&secret_key=SECRET'
```

- Query params: `region`, `endpoint`, `path_style`, and explicit `access_key` / `secret_key` (otherwise the standard AWS credential chain is used).
- When several replicas share one `s3://` CAS, enable `CORNUS_GC_INTERVAL` on at most one replica (see garbage collection below).

**See also:** [storage backends](/reference/storage-backends), [server env vars](/reference/server-env-vars)

## Serve with GCS or Azure Blob storage

Use Google Cloud Storage (`gs://`) or Azure Blob (`azblob://`) as the durable backend.

```sh
# These schemes require a -tags cloudblob build:
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
cornus serve --storage 'gs://my-bucket'
cornus serve --storage 'azblob://my-container'
```

- The default binary returns a clear "not supported in this build" error for these schemes; build with `-tags cloudblob` to enable them.
- Credentials come from the standard Google / Azure credential chains.

**See also:** [storage backends](/reference/storage-backends), [cornus serve](/cli/serve)

## Push and pull images to the registry

Move images into the registry with `cornus push`, or with stock Docker tooling.

```sh
# Push a local OCI/docker-archive tarball, or copy a registry ref, with cornus:
cornus push ./app.tar localhost:5000/app:v1
cornus push docker.io/library/nginx:latest localhost:5000/nginx:latest

# Stock docker against the same registry:
docker push localhost:5000/app:v1
docker pull localhost:5000/app:v1
```

- If the `source` argument is a file on disk it is loaded as a tarball; otherwise it is treated as a registry reference.
- `cornus push --insecure` (default `true`) allows plain-HTTP registries. When auth is on, set `CORNUS_TOKEN` and `cornus push` sends it as a registry bearer credential.

**See also:** [cornus push](/cli/push), [building images](/guides/building-images)

## Allow anonymous pulls

Let unauthenticated clients pull while push and delete still require auth.

```sh
CORNUS_REGISTRY_ANONYMOUS_PULL=1 cornus serve   # 1/true/yes/on
```

- Only meaningful when auth is otherwise enabled. It opens `GET` / `HEAD` under `/v2/*` while every other verb still needs a credential.
- An explicit `pull` rule in `CORNUS_API_POLICY` overrides this (with a startup warning when both are set).

**See also:** [Security and authentication](/guides/security)

## Advertise the registry to cluster runtimes

Tell deploy targets which registry host to pull built images from.

```sh
# host[:port], or https://host for a TLS registry:
CORNUS_ADVERTISE_REGISTRY=cornus.example:5000 cornus serve
```

- The server publishes this at `GET /.cornus/v1/info` as the registry that deploy targets pull from; unset, it is derived from how the server is reached.
- `CORNUS_ADVERTISE_URL` is a separate knob: the in-cluster cornus URL a pod mount-agent / caretaker dials back to (required for client-local mounts on the kubernetes backend).

**See also:** [server env vars](/reference/server-env-vars), [remote clusters](/guides/remote-clusters)

## Use an external OCI registry instead of the bundled one

Push into and deploy from a registry you already run.

```sh
# Copy a build's output into any external registry:
CORNUS_TOKEN=$(cornus token issue --sub ci --hs256-secret "$SECRET") \
  cornus push ./app.tar registry.example.com/app:v1
```

- `cornus push` targets any OCI registry reference; the bearer token is scoped to the destination host only, so a cross-registry copy never leaks the token to an unrelated source registry.
- For remote builds, `CORNUS_REGISTRY` sets the registry host used for tags that omit a registry part.

**See also:** [cornus push](/cli/push), [building images](/guides/building-images)

## Reclaim space with garbage collection

Run a mark-and-sweep over the CAS to prune unreferenced blobs and stale build caches.

```sh
# On demand: POST the GC endpoint on a running server.
curl -X POST http://localhost:5000/.cornus/v1/gc

# Periodically: run the same GC on an interval (Go duration).
CORNUS_GC_INTERVAL=1h cornus serve
```

- `POST /.cornus/v1/gc` is a destructive endpoint; under auth it is gated by the `gc` action in `CORNUS_API_POLICY`.
- `CORNUS_GC_INTERVAL` unset disables the scheduler; a malformed or non-positive value is a startup error. When several replicas share one `s3://` store, enable it on at most one replica. `CORNUS_GC_LEASE` adds a Kubernetes Lease leader gate.

**See also:** [server env vars](/reference/server-env-vars), [storage backends](/reference/storage-backends)
