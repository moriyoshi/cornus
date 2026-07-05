# cornus storage

Report a cornus server's storage consumption without changing anything.

## Synopsis

```sh
cornus storage usage [flags]
```

## Description

`cornus storage` groups server storage administration. Today it exposes a single,
non-destructive report; reclamation stays server-side (the `POST /.cornus/v1/gc`
endpoint and the periodic GC scheduler).

`cornus storage usage` fetches `GET /.cornus/v1/storage` and prints the current
footprint: the registry content store (blob count and total bytes) and, when the
per-file block cache is enabled, its footprint. It is the read-only counterpart to
garbage collection — it never deletes or evicts anything.

The report is computed by listing and stat-ing every registry blob, so it is
cheap against the filesystem backend but more expensive against an object store
such as S3 (one `HEAD` per blob). Treat it as an occasional operator query, not a
metric to poll on a tight loop.

The command resolves the server through the selected connection profile (see
[cornus config](/cli/config)), so `--context`, tokens, and TLS all apply; pass
`--server` to override the endpoint for one run.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--server` | `CORNUS_SERVER` | — | Remote cornus server URL (`http(s)://` or `ws(s)://`). Falls back to the selected connection profile. |
| `--format` | — | `text` | Output format: `text` (human-readable) or `json` (the raw report). |

The JSON form has these fields (`fileCache*` are omitted when the block cache is
disabled):

| Field | Description |
| --- | --- |
| `casBlobs` | Number of blobs in the registry content store. Zero in a pure re-export configuration (no content store). |
| `casBytes` | Total bytes of those blobs. |
| `fileCacheBytes` | On-disk size of the per-file block cache. |
| `fileCacheFiles` | Number of block-cache files. |

## Examples

Print a human-readable report:

```sh
cornus storage usage
```

```
Registry CAS: 128 blobs, 3.4 GiB
Block cache:  12 files, 512.0 MiB
```

Fetch the raw report for scripting:

```sh
cornus storage usage --format json
```

```json
{
  "casBlobs": 128,
  "casBytes": 3650722201,
  "fileCacheBytes": 536870912,
  "fileCacheFiles": 12
}
```

Query a specific server:

```sh
cornus storage usage --server https://cornus.example.com
```

## See also

- [cornus config](/cli/config) — connection profiles the command resolves against.
- Garbage collection is server-side: see the `POST /.cornus/v1/gc` endpoint and the
  `CORNUS_GC_INTERVAL` periodic scheduler in the server reference.
