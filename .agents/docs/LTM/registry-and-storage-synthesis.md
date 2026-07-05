# Registry and Storage Synthesis

## Summary

Cornus exposes one OCI Distribution API over several storage realizations. The
classic path stores content in Cornus's own CAS backed by filesystem or object
storage. Host-native mode instead projects a Docker or containerd image store
through `/v2/*`. Image-reference qualification, registry advertisement,
build-push redirection, Docker-compatible push, and pull-through mirroring sit
above those storage choices and must preserve the repository path while adapting
the host to the caller's network vantage.

## Included Documents

| Document | Focus |
|----------|-------|
| [registry-and-storage.md](./registry-and-storage.md) | Cornus CAS, object stores, resumable uploads, registry protocol, and advertised registry routing |
| [registry-local-image-flow.md](./registry-local-image-flow.md) | Bare-reference qualification, local and external Docker push, and pull-through mirroring |
| [host-native-registry.md](./host-native-registry.md) | Docker read-only and containerd read-write runtime-native stores |

## Stable Knowledge

- The classic registry owns OCI semantics once in `pkg/storage`: content-addressed
  blobs, manifests, tags, repositories, digest verification, and resumable
  uploads are independent of the selected `ObjectStore`.
- Filesystem is the default persistent store. Memory and S3 use the same backend
  contract; GCS and Azure drivers are compiled only with `-tags cloudblob`.
  S3 alone supplies native multipart upload, with resumable state persisted in a
  bounded sidecar.
- A bare image name is a raw-reference classification problem. `pkg/imageref`
  must decide whether a host was explicit before reference parsing erases that
  distinction. Remote builds qualify bare output tags through
  `reghost.Resolve`; local in-process builds have no Cornus registry to target.
- Registry host selection is vantage-dependent. An override wins, followed by
  `/.cornus/v1/info`, then the client endpoint. The repository path remains
  stable while server-side push redirection can replace an advertised host with
  the co-located loopback registry.
- The Docker proxy treats Docker Hub aliases as the local Cornus image store.
  Push to an external host is a copy-out operation from content already present
  locally.
- `CORNUS_REGISTRY_MIRROR` supplies anonymous GET/HEAD fallback for missing
  manifests and blobs. Local content always wins; catalog, tags, and referrers
  remain local. Cache mode persists fallback results through the normal store.
- `CORNUS_REGISTRY_SOURCE=host-native` resolves by deploy backend. Docker exposes
  a read-only `docker save` view and accepts server-routed build archives through
  daemon loading. Containerd implements the full registry store contract over
  its content and image stores.
- A registry with a nil store is intentionally read-only. Write verbs return
  `405`; adding an auxiliary CAS must not silently make the Docker daemon view
  writable.

## Operational Guidance

- Choose the registry realization before changing build export behavior:
  in-process BuildKit pushes require a writable registry, while the Docker
  host-native path needs server-routed archive loading.
- Keep source selection, advertised registry resolution, and local push
  redirection separate. One selects bytes, one tells clients and runtimes where
  to connect, and one adapts the build-side network vantage.
- Add storage backends beneath the shared CAS contract. Add runtime projections
  through the registry source/store seams rather than teaching protocol handlers
  about daemon APIs.
- Validate S3 changes with winterbaume v0.2.5 or later. Validate cloudblob
  changes with the tagged served binary, not merely a tagged E2E runner.

## Files

- `pkg/storage/` - CAS semantics and filesystem, blob, S3, GCS, and Azure stores.
- `pkg/registry/` - OCI handlers, mirror fallback, Docker source, and containerd
  source/store.
- `pkg/imageref/` and `cmd/cornus/internal/reghost/` - reference classification
  and registry endpoint resolution.
- `pkg/server/` - server info, source wiring, and co-located push redirection.
- `pkg/dockerproxy/push.go` - Docker-compatible local acknowledgement and
  external copy-out.

## Tests

- Storage and registry unit suites exercise CAS conformance, resumable uploads,
  manifest rules, nil-store hardening, mirrors, and runtime-native operations.
- `registry-edges.star`, `registry-errors.star`, `registry-advertise.star`,
  `registry-mirror.star`, and `docker-push.star` cover the default paths.
- S3, GCS, Azure, Docker host-native, and containerd host-native scenarios are
  target- or environment-gated because they require their corresponding service.

## Pitfalls

- Docker re-export emits uncompressed layers, so external layer digests are not
  preserved. Pull Docker host-native images by tag.
- Do not classify explicit references solely with
  `name.ParseReference`.
- Containerd's local content-store fake does not prove label persistence or
  garbage-collection reachability; keep a live E2E vertical.
- A single registry hostname rarely works unchanged for the client, build
  process, and node runtime. Preserve the repo and adapt the host per vantage.
