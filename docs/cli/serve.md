# cornus serve

Run the cornus server: the OCI registry, the build engine, and the deploy
engine, all in one process.

## Synopsis

```sh
cornus serve [flags]
```

## Description

`cornus serve` starts the unified HTTP server that hosts `/v2/*` (the OCI
registry) and `/.cornus/v1/*` (build, deploy, exec, and tunnel endpoints). It listens
until interrupted (`Ctrl-C` or `SIGTERM`).

Registry blobs and manifests are persisted through the storage backend selected
by `--storage`; when unset, storage lives under the data dir. See
[Storage backends](/reference/storage-backends) for the supported URL forms.

When `--tls-cert` and `--tls-key` are both set, the server speaks HTTPS. Adding
`--tls-client-ca` turns on mutual TLS: a verified client certificate's
CommonName becomes the caller identity, while presenting a client certificate
stays optional. See [Security and authentication](/guides/security).

For the full set of environment variables the server honors, see
[Server environment variables](/reference/server-env-vars).

## Flags

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--addr` | `CORNUS_ADDR` | `:5000` | HTTP listen address for `/v2/*` and `/.cornus/v1/*`. |
| `--rootless` | `CORNUS_ROOTLESS` | `false` | Run the build engine in rootless mode (user namespaces). |
| `--storage` | `CORNUS_STORAGE` | data dir | Registry persistence backend: a path, `file://`, `mem://`, or `s3://bucket?region=&endpoint=&path_style=`. See [Storage backends](/reference/storage-backends). |
| `--otel` | `CORNUS_OTEL` | `false` | Enable OpenTelemetry (traces/metrics/logs) via the standard `OTEL_*` env. Also enabled implicitly when any `OTEL_*` exporter/endpoint env var is set. |
| `--tls-cert` | `CORNUS_TLS_CERT` | — | PEM certificate file; serve HTTPS when set together with `--tls-key`. |
| `--tls-key` | `CORNUS_TLS_KEY` | — | PEM private-key file; serve HTTPS when set together with `--tls-cert`. |
| `--tls-client-ca` | `CORNUS_TLS_CLIENT_CA` | — | PEM CA bundle to verify client certificates (mTLS). A verified cert CommonName becomes the caller identity; presenting a cert stays optional. |
| `--file-cache` | `CORNUS_FILE_CACHE` | `false` | Enable the server per-file cache for immutable client-local mount reads. Requires `--file-cache-dir`. |
| `--file-cache-dir` | `CORNUS_FILE_CACHE_DIR` | — | Required directory for file-cache data; use a dedicated volume. |
| `--file-cache-chunk-size` | `CORNUS_FILE_CACHE_CHUNK_SIZE` | `1048576` | File-cache block size in bytes. |
| `--file-cache-max-bytes` | `CORNUS_FILE_CACHE_MAX_BYTES` | unlimited | Soft file-cache size cap enforced by garbage collection. |

## Examples

Serve on the default address, storing data under the data dir:

```sh
cornus serve
```

Listen on a specific address and keep the registry in memory:

```sh
cornus serve --addr :8080 --storage mem://
```

Persist the registry to S3-compatible storage:

```sh
cornus serve --storage 's3://my-bucket?region=us-east-1&path_style=true'
```

Serve HTTPS with mutual TLS:

```sh
cornus serve \
  --tls-cert server.crt \
  --tls-key server.key \
  --tls-client-ca clients-ca.pem
```

## See also

- [Storage backends](/reference/storage-backends)
- [Server environment variables](/reference/server-env-vars)
- [Security and authentication](/guides/security)
- [Architecture](/architecture/)
