# Host-Native Registry

## Summary

On dockerhost and containerdhost, Cornus can expose the runtime's native image
store through `/v2/*`, avoiding a redundant Cornus CAS. The default
`CORNUS_REGISTRY_SOURCE=host-native` resolves per backend: Docker is a read-only
`docker save` view with build-to-daemon loading, while containerd is a
digest-preserving read-write registry over its content and image stores.

## Key Facts

- `host-native` is the default only for dockerhost and containerdhost; `off`
  selects the classic CAS.
- Supplying `--storage` creates a union only where the source semantics permit
  it; Docker's daemon source remains write-rejecting.
- Pure re-export uses a nil `registry.Store`, so it performs no guaranteed-miss
  CAS lookup and rejects write verbs with `405`.
- Containerd implements the full `registry.Store` contract; pushes import blobs,
  manifests, tags, and GC references directly.
- Docker re-export recomputes layer digests because `docker save` emits
  uncompressed layers. Pull Docker-host-native images by tag, not by an external
  digest.

## Details

The feature evolved from a Docker-only `imageSource` fallback through a
containerd source, builder export into Docker, deploy skip-pull, optional storage,
one backend-resolving token, and finally a read-write containerd store.

The important build constraint is routing. In-process BuildKit pushes directly
to `/v2/*`; a pure read-only Docker view cannot accept that push. Server-routed
builds can instead receive a Docker archive and load it into the daemon. The
containerd worker no longer needs special push suppression because a registry
push imports into the same native store.

`go-containerregistry/pkg/v1/daemon` is incompatible with the pinned Docker
client API, so the Docker path uses a small REST client plus
`pkg/v1/tarball`. Containerd's `content/local` test store does not persist labels;
unit tests verify GC-label computation and live E2E must verify persistence.

## Files

- `pkg/registry/daemon_source.go` - Docker read path.
- `pkg/registry/containerd_store_linux.go` - containerd source/store.
- `pkg/registry/registry.go` - optional store and source gating.
- `cmd/cornus/serve.go` - source resolution and defaults.
- `e2e/scenarios/registry-host-native-containerd.star` - containerd vertical.

## Test Coverage

Unit tests cover source resolution, nil-store reads, `405` write hardening, Docker
union mode, and containerd blob/manifest/tag operations. Docker and containerd
scenarios are registered; the containerd scenario requires a real privileged
containerd target.

## Pitfalls

- A co-resident CAS must not accidentally make the Docker daemon view writable.
- Do not assume Docker preserves external layer digests.
- A fake `content/local` store cannot prove containerd metadata-label
  persistence.
