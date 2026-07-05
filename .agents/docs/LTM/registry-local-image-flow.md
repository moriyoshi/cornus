# Builtin Registry Image Flow

## Summary

Cornus uses its builtin registry as the default local image store for remote builds and Docker-compatible clients. Bare output references are qualified only when a Cornus registry endpoint exists, and the registry can optionally mirror upstream blobs and manifests.

## Key Facts

- `pkg/imageref` (`IsBare`, `QualifyBare`, `SplitHostRepo`) uses Docker's raw-string registry rule because parsed references cannot distinguish a bare name from explicit `docker.io/...`.
- `cmd/cornus/internal/reghost.Resolve` selects an override, then `/.cornus/v1/info` `RegistryHost`, then the client host; remote `cornus build` and `cornus compose build` qualify bare tags with it.
- Local in-process builds remain unchanged because they have no Cornus server or builtin registry to target.
- `docker.io`, `index.docker.io`, and `registry-1.docker.io` map to the builtin local store in the Docker proxy; other qualified hosts are copy-out targets.

## Details

`cornus build` exposes `--registry` / `CORNUS_REGISTRY`, while Compose qualifies both its generated primary tag and user `build.tags`. `Server.localPushTarget` redirects a push to an advertised registry host onto the colocated loopback registry; as of 2026-07-15 `Server.localPushTargets` (`pkg/server/server.go`) applies that same redirect to a build's additional `Tags` too (a compose build-group's non-first members), resolving the advertised host once for the whole build rather than once per tag. `pkg/server/build_attach.go`'s `handleBuildAttach` is the only call site that threads `Tags` into `builder.SolveInput`, so it is the only place that needed to switch from `localPushTarget` to `localPushTargets`.

`pkg/dockerproxy/push.go` handles `POST /images/{name}/push`, which previously returned an empty success through the image-inspection handler. It reads an existing builtin-registry image, returns Docker jsonmessage progress plus the manifest digest for a local-store name, or copies it to an external registry with go-containerregistry and `X-Registry-Auth`. Docker CLI normalization of `docker push app` to `docker.io/library/app` is stripped back to the local repository name.

### Pull-through mirror

`pkg/registry/mirror.go` supplies `Mirror{Host, Cache}` and `WithMirror`. `CORNUS_REGISTRY_MIRROR` enables anonymous upstream GET/HEAD fallback for missing manifests and blobs; `CORNUS_REGISTRY_MIRROR_CACHE` defaults to `true`. Cached results use normal `PutManifest` / `PutBlob` storage and blob re-dispatch, preserving Range and digest behavior. Tags, catalog, and referrers remain local; a local hit wins even when the mirror is unavailable.

## Files

- `pkg/imageref/` and `cmd/cornus/internal/reghost/` - reference classification and registry-host resolution.
- `cmd/cornus/build.go`, `cmd/cornus/internal/composecli/commands.go`, and `pkg/dockerproxy/push.go` - build-tag and Docker-push behavior.
- `pkg/registry/mirror.go`, `pkg/server/server.go`, and `pkg/e2e/upstream_registry.go` - mirror wiring and hermetic upstream test registry.

## Test Coverage

- Unit tests cover reference classification, Docker Hub normalization, local acknowledgement, external copy-out, not-found errors, cached/transparent reads, local-hit precedence, and mirror misses.
- `registry-mirror.star` runs on the local target; `docker-push.star` exercises a real Docker CLI on the Docker target. `upstream_registry(seed=[...])` is registered in both `predeclared()` and `predeclaredNames()`.
- `TestLocalPushTargets` (`pkg/server/server_info_test.go`) covers `localPushTargets` redirecting a build's `Target` and every `Tags` entry together. `e2e/scenarios/compose-build-group.star` (+ `compose-build-group.yaml` fixture) drives two compose services sharing one build context through a real server with `CORNUS_ADVERTISE_REGISTRY` set, asserting both members' images land in the co-located registry — the build-group path `registry-advertise.star` alone does not exercise.

## Pitfalls

- Do not use `name.ParseReference` alone to classify an explicitly qualified reference.
- Docker push moves only content that is already in the builtin registry.
