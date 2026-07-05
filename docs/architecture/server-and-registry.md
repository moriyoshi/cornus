# The server, registry, and content store

One HTTP process serves everything. A single mux routes the registry under
`/v2/*`, the build and deploy APIs under `/.cornus/v1/*`, and liveness/readiness
under `/healthz` and `/readyz`. Because the build engine and deploy backend are
built lazily on first use, an operator can run a registry-only or deploy-only
server without the other subsystems' prerequisites being present.

## Operational guardrails

The server is where the operational guardrails live:

- **Readiness is real.** `/readyz` flips to serving atomically and back to 503
  on shutdown; `/healthz` stays pure liveness.
- **Concurrency and serialization.** Builds run under a
  `CORNUS_BUILD_CONCURRENCY` semaphore (default: the CPU count). Apply and
  delete for a given deployment name are serialized by a per-name mutex, so two
  callers cannot race on the same workload.
- **Request-size caps.** The build-context tar is capped at 2 GiB
  (`CORNUS_MAX_BUILD_CONTEXT_BYTES`) and blob PUTs at 10 GiB; over-limit returns
  413 and aborts the upload session.
- **In-band build failures.** A build streams its output after the HTTP 200 has
  been sent, so a failure that happens mid-stream arrives as a `BUILD FAILED:`
  trailer in the body. Clients must scan the stream — the status code alone is
  not the source of truth.
- **Deploy-side stream errors are surfaced, not swallowed.** Logs, stats, and
  archive downloads write their 200 lazily, on the backend's first output byte,
  so a failure before any output returns a real 4xx/5xx with an error body. An
  error after output has started is stamped into the `X-Cornus-Stream-Error`
  HTTP trailer, which the Cornus client checks after EOF while still delivering
  the partial bytes.
- **Fail-closed config.** Malformed policy environment (`CORNUS_API_POLICY`,
  `CORNUS_HUB_POLICY`, `CORNUS_HUB_REGISTER_POLICY`) is a hard startup error,
  never fail-open.

Shutdown closes the lazily-built engine and deploy backend, releasing the
build engine's data-dir lock.

## The registry

The registry is an own-implementation OCI Distribution v1.1 handler set written
directly against a persistent content-addressable store — manifests and tags
survive restarts, which is what rules out the common in-memory registry
libraries. The supported surface is a practical subset of the spec: ping, blob
HEAD/GET (with `Range` support), monolithic/chunked/cross-repo-mount blob
upload, blob and manifest delete (manifests by digest only), manifest PUT/GET,
paginated tags and `_catalog` listing, and the Referrers API.

The split inside is deliberate: the content store owns sha256 addressing, digest
verification, upload staging, and manifest/tag/repo indexing, while the registry
layer is a thin set of OCI-protocol HTTP handlers on top.

## Pluggable persistence

Persistence is a plugin point with a deliberately tiny abstraction. A backend
implements only a minimal `ObjectStore` (`Get`/`Put`/`Stat`/`Delete`/`List`),
and *every* registry semantic lives once above that interface. The
content-addressed key layout — what actually lands in your directory or bucket —
is:

```
blobs/sha256/<aa>/<hex>          blob content
repos/<repo>/manifests/<hex>     value = media type
repos/<repo>/tags/<tag>          value = digest
```

Two backends ship. The **filesystem** backend is the native, zero-dependency
default. The **bucket** backend wraps a gocloud bucket, giving `mem://`,
`s3://`, and — in a `-tags cloudblob` build, because those drivers pull in the
Google/Azure SDKs — `gs://` and `azblob://`. S3-compatible servers such as MinIO
get an explicit client with a custom endpoint and path-style addressing. Select
the backend with `cornus serve --storage <ref>` / `CORNUS_STORAGE`; empty
defaults to the on-disk data-dir layout. See
[storage backends](/reference/storage-backends) for configuration.

**Resumable uploads are capability-based.** A backend that implements native
uploading handles the OCI PATCH/PUT upload flow itself; others fall back to
local staging. The filesystem backend appends to a session file and commits by
renaming into the blob path. The S3 backend is the interesting case: each OCI
PATCH is a separate HTTP request, so all upload state must live server-side.
Parts stream into an S3 multipart upload, and a small JSON sidecar object
carries the part ETags, the pending tail, and the running sha256 state —
keeping local staging under 5 MiB regardless of blob size.

## Miss fallbacks: pull-through mirror and local-store re-export

A `/v2/*` manifest or blob miss can fall through to a read-only source instead of
returning 404. The handler tries the local store first, then the configured
source; the sources are mutually exclusive.

- **Pull-through mirror** (`CORNUS_REGISTRY_MIRROR=<host>`). The miss is fetched
  from an upstream OCI registry and served; with `CORNUS_REGISTRY_MIRROR_CACHE`
  (default on) it is also persisted into the store, so later pulls resolve
  locally.
- **Local-store re-export** (`CORNUS_REGISTRY_SOURCE=host-native`, the **default**
  on a host backend). When you develop against a local Docker or containerd host
  you already have the image locally, so a second copy in a separate cornus
  registry is redundant. This makes `/v2/*` a *view* over that local store, per
  backend. Under **`containerd`** it backs `/v2/*` with the host containerd's native
  content store **read-write**: a push imports straight into the store (blobs by
  digest + an image record) and a pull reads it back, so a `cornus build` that
  pushes to `/v2/*` is immediately deployable — no build-worker configuration.
  Under **`dockerhost`** it is a **read-only** view of the local Docker daemon
  (misses served via `docker save`); a same-host deploy skips the registry pull for
  an image the daemon already has, and because classic Docker has no writable
  content store a `/v2/*` push is rejected `405` — a `cornus build` routes through
  the server, which `docker load`s the result into the daemon.

With no `--storage`, host-native keeps **no separate CAS**: `_catalog`/tag listings
reflect only the local store, and lifecycle is the runtime's job. The docker-daemon
view's `docker save` recomputes digests (pull by tag); the containerd view preserves
them. To keep the classic push-able registry, set `CORNUS_REGISTRY_SOURCE=off` or
pass an explicit `--storage` (a union CAS+source view); a configured mirror or a
non-host backend keeps it too. These modes are for local development, not a shared
high-fanout registry. See
[Reusing a local image store](/reference/server-env-vars#reusing-a-local-image-store).

## Garbage collection and crash-safety

Storage GC is on-demand **mark-and-sweep**. Roots are every repo's tags plus
manifest markers; the mark phase parses manifests and indexes (config, layers,
nested `manifests[]`, `subject`); the sweep removes unreachable blobs.
`POST /.cornus/v1/gc` triggers it (gated on the `gc` policy action) and also
prunes the build engine's local cache on a 7-day TTL.

Setting `CORNUS_GC_INTERVAL` (a Go duration) additionally runs the same GC
periodically in the background: unset disables it entirely, while a malformed or
non-positive value is a hard startup error — a typo'd schedule must not silently
disable reclamation. With more than one replica, `CORNUS_GC_LEASE` gates each
tick behind a compare-and-swap on a Kubernetes `coordination.k8s.io` Lease, so
replicas never sweep concurrently — a refused acquire just skips the tick (a
missed sweep beats a concurrent one). Stale upload staging is swept at startup
on a 24-hour TTL. See the [registry guide](/guides/registry) for operating GC.

No repair pass is needed after a crash, because manifest writes happen in
dependency order — **blob, then manifest marker, then tag** — so a crash leaves
at worst a GC-reclaimable orphan, never a tag pointing at missing data.

## Related pages

- [Registry & storage guide](/guides/registry) — serving, advertising, and GC
  in practice.
- [Storage backends](/reference/storage-backends) — every backend's
  configuration.
- [Server env vars](/reference/server-env-vars) — the full environment surface.
- [cornus serve](/cli/serve) — the serve command.
