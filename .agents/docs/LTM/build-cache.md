# Build Cache (registry, inline, local)

## Summary

Cornus supports BuildKit build-cache import/export via `--cache-to` / `--cache-from` (buildx `type=...,key=val` syntax, repeatable) with `inline`, `registry`, and `local` backends, on both local builds and remote builds (`--builder`). The `registry` backend uses a `docker.RegistryHosts` resolver whose defaults make localhost registries plain-HTTP; `type=local` paths are remapped to opaque keys under the engine root so remote callers never name real server paths.

## Key Facts

- CLI: `cornus build --cache-to <spec> --cache-from <spec>`; specs use buildx syntax, e.g. `type=registry,ref=<host>/app:buildcache,registry.insecure=true`.
- Registry backend registered in the controller:
  - `ResolveCacheExporterFuncs["registry"] = registryremotecache.ResolveCacheExporterFunc(sm, hosts)`
  - `ResolveCacheImporterFuncs["registry"] = registryremotecache.ResolveCacheImporterFunc(sm, w.ContentStore(), hosts)`
  - The importer also lets `--cache-from` consume INLINE cache embedded in an image (export existed before; import did not).
- Resolver: `resolver.NewRegistryConfig(nil)` — defaults make localhost registries plain-HTTP (Cornus's own registry works out of the box); a per-entry `registry.insecure=true` cache attr covers other insecure registries; auth flows through the build session.
- `type=local` `dest=`/`src=` values are treated as opaque **keys**, mapped to `<Root>/localcache/<key>`; the key is auto-derived from the target image's repo path (`distribution/reference` `ParseNormalizedNamed` + `Path`) when omitted.
- Remote path wire: `buildwire.BuildSpec.CacheExports/CacheImports` (`CacheOption{Type,Attrs}`); the client (`runRemote`) fills them, the server's `build_attach` maps them into `SolveInput.CacheExports/Imports`.
- `builder.SolveInput` carries Cornus's `[]CacheOption` (not the BuildKit `client` type); conversion to `client.CacheOptionsEntry` happens only at the `SolveOpt` boundary in the linux-only solve, so `builder.go` does not import the buildkit client and the server maps with a plain struct copy.

## Details

### Registry backend

Plumbing: `builder.CacheOption{Type,Attrs}` on `Request`; `SolveInput.CacheExports/CacheImports` mapped into `SolveOpt.CacheExports/CacheImports` in the solve. Initially remote (`--builder`) builds errored with a clear message; the follow-up added the `buildwire.BuildSpec` cache fields and server mapping so both paths work.

### type=local keyed by pseudo-paths

BuildKit resolves a `type=local` cache's `dest=`/`src=` to a real directory on whichever process runs `client.Solve` (confirmed in `buildkit@v0.18.2/client/solve.go:parseCacheOptions`: export → `contentlocal.NewStore(ex.Attrs["dest"])`, import → `im.Attrs["src"]`). For a remote build that process is the **server**, so a caller would have to name a real writable server path it cannot see. `Engine.Solve` instead remaps the value to `<Root>/localcache/<key>`.

Single seam: `Engine.Build` funnels into `Engine.Solve`, and the remote server calls `Solve` too, so the remap lives in `solve_linux.go` just before `SolveOpt` assembly and covers both local and remote builds (one consistent key namespace). No CLI/wire changes — `parseCacheOpts`/`buildwire.CacheOption` already forward arbitrary attrs.

Helper: `internal/builder/localcache.go` (`resolveLocalCacheOpts` / `deriveCacheKey` / `safeCacheDir`; no build tag, so unit-testable unprivileged). `Engine` gained a `root` field. `safeCacheDir` confines keys under the cache root via the `"/"+filepath.Clean` idiom plus a `filepath.Rel` prefix check, so `../..` and absolute keys cannot escape. `registry`/`inline` and other attrs pass through untouched; Attrs maps are cloned, never mutated.

## Files

- `internal/builder/localcache.go` — local-cache key resolution (`resolveLocalCacheOpts`, `deriveCacheKey`, `safeCacheDir`)
- `internal/builder/solve_linux.go` — local-cache remap + `CacheOption` → `client.CacheOptionsEntry` conversion at the `SolveOpt` boundary
- `internal/builder/engine_linux_test.go` — privileged cache tests (`TestRegistryCache`, `TestLocalCache`, `TestBuildAndPush`)
- `internal/builder/localcache_test.go` — unprivileged unit tests
- `buildwire` — `BuildSpec.CacheExports/CacheImports`, `CacheOption{Type,Attrs}`

## Test Coverage

- `internal/builder/localcache_test.go` (unprivileged): explicit key, derived key, traversal confinement, pass-through, no-key-no-target error.
- `TestRegistryCache` (privileged): `--cache-to type=registry` pushes the cache manifest (verified via `remote.Get`); a fresh engine `--cache-from type=registry` imports and builds cleanly.
- Remote path (privileged): `--cache-to type=registry,ref=<host>/app:buildcache,registry.insecure=true` exported (manifest HTTP 200); a fresh remote `--cache-from` build reproduced an identical image digest, proving actual cache reuse.
- `TestLocalCache` (privileged, `--privileged` debian container via `sg docker`, runc 1.1.5): exports `--cache-to type=local,dest=demo` to `<Root>/localcache/demo` (asserts `index.json` materialized), seeds a fresh-bbolt engine's managed dir from only that export, imports via `--cache-from type=local,src=demo` (BuildKit logs `importing cache manifest from local:<id>`), and reproduces the identical image digest.
- Cross-platform: `go build` for GOOS=darwin/windows stays green (the buildkit `client` import is confined to linux-only solve code).

## Pitfalls

- `type=local` `dest=`/`src=` are NOT real paths in Cornus — they are keys under `<Root>/localcache/`; do not pass host paths expecting them to be used verbatim.
- BuildKit resolves local-cache paths on the process running `client.Solve` (the server for remote builds) — the root cause for the pseudo-path design.
- Insecure non-localhost cache registries need the per-entry `registry.insecure=true` attr; localhost is plain-HTTP by default.
- The registry logs a benign "unknown type: cacheconfig.v0" when storing the cache-config blob; it round-trips fine.
- Keep `builder.go` free of buildkit `client` imports; convert `CacheOption` to `client.CacheOptionsEntry` only inside the linux-only solve.
