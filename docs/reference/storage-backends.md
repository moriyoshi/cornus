# Registry storage backends

The cornus registry is a small OCI registry (`/v2/*`) built on a sha256 **content-addressable store** (CAS): every blob — image layers, config, and manifests — is keyed by the digest of its own bytes, so identical content is stored once and integrity is verifiable by re-hashing. Manifests and tags are thin references into that store.

Where the CAS *lives* is pluggable, selected with the [`cornus serve`](/cli/serve) `--storage` flag (env `CORNUS_STORAGE`). The default is the filesystem layout under the server data directory.

Uploads are **resumable on every backend** (the S3 backend streams them as native multipart uploads). Space is reclaimed on demand — `POST /.cornus/v1/gc` runs a mark-and-sweep over the CAS and prunes stale build caches — and, optionally, periodically via `CORNUS_GC_INTERVAL` (see [Server environment variables](/reference/server-env-vars)).

## Backends

| `--storage` value | Backend | Persistence | Notes |
| --- | --- | --- | --- |
| a path or `file://path` | Filesystem (default) | Durable, on local disk | The default when `--storage` is unset: the filesystem layout under the data dir. |
| `mem://` | In-memory | Ephemeral | Lost on restart. Handy for tests and throwaway servers. |
| `s3://bucket?…` | AWS S3 / S3-compatible | Durable, object storage | Native multipart uploads. Query params tune region, endpoint, and path style. |
| `gs://bucket` | Google Cloud Storage | Durable, object storage | Requires a `-tags cloudblob` build (see below). |
| `azblob://container` | Azure Blob Storage | Durable, object storage | Requires a `-tags cloudblob` build (see below). |

```sh
cornus serve --storage /var/lib/cornus                       # filesystem (default)
cornus serve --storage mem://                                # in-memory (ephemeral)
cornus serve --storage 's3://my-bucket?region=us-east-1'     # AWS S3
```

## Filesystem

The default. Pass a bare path (or `file://path`) and the registry writes its CAS layout under it. When `--storage` is omitted entirely, the store lives under the server data directory.

## In-memory

`mem://` keeps the entire CAS in process memory. It is ephemeral — everything is lost when the server stops — so it suits tests and short-lived servers, not durable registries.

> On a host backend, `/v2/*` [defaults to re-exporting the local Docker/containerd store](/reference/server-env-vars#reusing-a-local-image-store) (`CORNUS_REGISTRY_SOURCE=host-native`) and keeps **no content store at all** — not even an in-memory one. Pass `--storage` to layer a CAS under the re-export (a union view), or set `CORNUS_REGISTRY_SOURCE=off` for the classic persistent registry.

## S3 and S3-compatible

`s3://bucket` stores the CAS in an S3 bucket, streaming uploads as native S3 multipart uploads. Query parameters configure the connection:

| Param | Meaning |
| --- | --- |
| `region` | AWS region of the bucket. |
| `endpoint` | Override the S3 endpoint (for S3-compatible services such as MinIO). |
| `path_style` | `true` to use path-style addressing (needed by many S3-compatible services). |
| `access_key` / `secret_key` | Explicit credentials (otherwise the standard AWS credential chain is used). |

```sh
# S3-compatible (MinIO, and similar): override endpoint + path-style
cornus serve --storage 's3://my-bucket?region=us-east-1&endpoint=http://localhost:9000&path_style=true&access_key=KEY&secret_key=SECRET'
```

When several replicas share one `s3://` CAS, each replica's interval GC runs uncoordinated with the others, so enable `CORNUS_GC_INTERVAL` on at most one replica (or rely on on-demand `POST /.cornus/v1/gc`) until coordinated GC exists.

## Google Cloud Storage and Azure Blob (`-tags cloudblob`)

`gs://` (GCS) and `azblob://` (Azure Blob) work via the gocloud blob abstraction, but their drivers pull in the Google/Azure SDKs. To keep the default binary lean, they are **behind a build tag**: build with `-tags cloudblob` to enable them. The default build returns a clear "not supported in this build" error for those schemes.

```sh
# Enable the Google Cloud Storage / Azure Blob backends:
CGO_ENABLED=0 go build -tags "netgo osusergo cloudblob" -o cornus ./cmd/cornus
cornus serve --storage 'gs://my-bucket'
```

Credentials for `s3://` / `gs://` / `azblob://` come from the standard cloud credential chains.

## Data directory and persistence

Independent of which CAS backend you choose, the server keeps working state — the filesystem CAS (when `--storage` is a path or unset), in-progress uploads, and the build cache — under its **data directory**, set with `--data-dir` (env `CORNUS_DATA`).

```sh
cornus serve --data-dir /var/lib/cornus       # or CORNUS_DATA=/var/lib/cornus
```

- To survive restarts in a container, back the data dir with a durable volume: a named volume (Docker) or a PVC (the shipped StatefulSet uses `volumeClaimTemplates`, mounting `/var/lib/cornus`).
- When `--storage` points at an object store (`s3://`, `gs://`, `azblob://`), the CAS lives in that bucket instead of the data dir — so the durable blob storage no longer depends on the local volume, though the data dir still holds the build cache and uploads.

## See also

- [`cornus serve`](/cli/serve) — the `--storage` flag and the rest of the server surface.
- [Server environment variables](/reference/server-env-vars) — `CORNUS_STORAGE`, `CORNUS_GC_INTERVAL`, and related knobs.
