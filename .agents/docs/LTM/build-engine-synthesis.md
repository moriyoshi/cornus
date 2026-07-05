# Build Engine Synthesis (transport, caches, lazy contexts)

## Summary

Everything about Cornus's in-process BuildKit build subsystem (`pkg/build`): the engine seam that
keeps BuildKit types out of cross-platform code, the 9P-on-WebSocket remote-build transport with
its confined caller-side export, build-cache import/export across three backends, the lazy
bind-mount mechanism that serves large contexts on demand, and the choice of BuildKit worker
(runc default, containerd via `CORNUS_BUILD_WORKER=containerd`). The source documents share the
same seams (`SolveInput`, `buildwire.BuildSpec`, the confined 9P attacher, `.dockerignore`
handling), so an agent touching any one of them needs the invariants from all of them.

## Included Documents

| Document | Focus |
|----------|-------|
| [remote-build-9p-transport.md](./remote-build-9p-transport.md) | WebSocket/yamux/9P transport, confinement, symlinks, SSH tunneling |
| [build-cache.md](./build-cache.md) | `--cache-to`/`--cache-from` backends; `type=local` pseudo-path keys |
| [lazy-bind-mounts.md](./lazy-bind-mounts.md) | Synthetic OCI image + "stargz" snapshotter + contenthash pre-seed |
| [containerd-backend.md](./containerd-backend.md) | Build-worker facet only: `CORNUS_BUILD_WORKER=containerd` (deploy backend covered elsewhere) |

Note on paths: the two older docs predate the `internal/` -> `pkg/` restructure. Current paths:
`internal/builder` -> `pkg/build/builder`, `internal/buildwire` -> `pkg/build/buildwire`, the
BuildKit-free transport core -> `pkg/wire`, `internal/client` -> `pkg/client`.

## Stable Knowledge

### Engine seams

- `engine.Solve(SolveInput)` is the single solve funnel: local `Engine.Build` and the server's
  `/.cornus/v1/build/attach` handler both call it, so per-build behavior (cache remap, lazy routing,
  contenthash pre-seed, `FrontendAttrs`) lives once in `solve_linux.go`.
- `SolveInput`/`Request` carry Cornus-owned types (`CacheOption{Type,Attrs}`, `TargetStage`,
  `NamedContexts`, `SSH`, `LazyContexts`); conversion to BuildKit `client` types happens only
  inside the `//go:build linux` solve code, keeping `builder.go` and all callers cross-platform.
- The engine is a process singleton per data dir: `builder.New` takes a non-blocking flock on
  `<data-dir>/engine.lock` and fails fast ("data dir ... in use") instead of deadlocking on
  BuildKit's boltdb.
- Worker selection: `CORNUS_BUILD_WORKER=containerd` picks BuildKit's `worker/containerd` branch
  in `newWorkerOpt` (`pkg/build/builder/engine_containerd_linux.go`), targeting a bare containerd
  host. Worker state lives under `<Root>/containerd-<snapshotter>/` and coexists with the runc
  worker's dir (GC policy and `engine.lock` unchanged; bolt files stay under Root). The worker's
  ImageStore means tagged builds land in the HOST containerd image store in addition to the
  registry push. Lazy builds are rejected with a clear error (construction-time AND per-build) —
  the stargz-named snapshotter wrapper is runc-factory plumbing. A pre-dial socket probe makes a
  dead containerd socket fail in ~0ms instead of the 5s dial timeout.

### Remote transport (buildwire)

- One WebSocket (`GET /.cornus/v1/build/attach`) carries a yamux session: a control stream
  (`BuildSpec` out, progress back), a 9P stream, server-initiated `'S'` SSH-agent tunnel streams,
  and `'L'` lazy-backing streams. The caller runs the `p9.Server` (role inversion); caches stay
  server-side.
- The caller-side 9P export tree: `context/`, `dockerfile/`, `ctx/<name>/` (named contexts),
  `secrets/<id>` (in-memory staticfs). All directory exports go through `confinedAttacher`:
  no `..` traversal, no symlink escape (parent-chain `EvalSymlinks` on Walk, full resolution
  denial on Open), read-only (`EROFS`), `.dockerignore` filtered before bytes cross the wire.
- Docker-parity symlink semantics: escaping symlinks are transmitted *as symlinks* (Linkname read
  over 9P into `fstypes.Stat`), never followed on the caller.
- p9 <-> fsutil adapter requirements (all load-bearing): wrap entries so `Info().Sys()` returns
  `*fstypes.Stat`; map p9 `ENOENT` -> `fs.ErrNotExist` (BuildKit probes optional files); treat
  `SkipDir` from the root walk fn as stop, not error.

### Build cache

- CLI `--cache-to`/`--cache-from` with buildx syntax; backends: `inline`, `registry`
  (`registryremotecache` exporter/importer + `resolver.NewRegistryConfig` hosts — localhost is
  plain-HTTP, others need `registry.insecure=true`), and `local`.
- `type=local` `dest=`/`src=` values are opaque KEYS remapped to `<Root>/localcache/<key>`
  (`resolveLocalCacheOpts`/`safeCacheDir`, traversal-confined), never real caller paths — BuildKit
  resolves local-cache paths on the process running `client.Solve`, which is the server for
  remote builds.
- Remote wire: `buildwire.BuildSpec.CacheExports/CacheImports`; compose/devcontainer `cache_from`
  refs fold into `type=registry` imports client-side (`pkg/client`), no server mapping needed.

### Lazy bind mounts

- Three mechanisms, all required: (1) named context as a synthetic single-layer OCI image via
  `oci-layout://` + session-attached `content.Store` (`SolveOpt.OCIStores`, layer blob never
  materialized); (2) a remote snapshotter that MUST be named `"stargz"` (the sole BuildKit gate
  for the label-carrying skip-extract path) registering a Walk-discoverable COMMITTED snapshot and
  returning `ErrAlreadyExists`; (3) a contenthash pre-seed from producer-computed per-file digests
  (`lazyctx.ComputeDigests`, `contenthash.NewFromStat` parity) so the RUN cache-key scan never
  reads the mount. Measured: 20MB context, 11-byte read -> 11 bytes over the wire.
- Opt-in per build: `cornus build --lazy` / `CORNUS_LAZY_BUILD`; the snapshotter wrapper is
  always on and a verified no-op for ordinary builds.
- Backing mounts: `dir:<path>` host bind locally, `9p:<socket>` kernel-9p
  (`trans=unix,cache=loose,ro`) remotely, proxied over `'L'` streams.

### .dockerignore semantics (cross-cutting invariant)

- Each named context honors its OWN `<dir>/.dockerignore`, never the main context's patterns.
- The `.dockerignore` file itself is always served (re-application is idempotent).
- On the lazy path the manifest, the per-file digests, and the 9P export must apply the identical
  ignore predicate — divergence makes the seed mismatch the mount (shipped once, caught by
  build-edge case 2).
- Known asymmetry: the eager LOCAL build path passes named contexts through `fsutil.NewFS`
  unfiltered; remote and lazy paths filter. `build-edge.star`'s named-ignore case is remote-only.

## Operational Guidance

- Route any new per-build knob through `SolveInput` and map it in `solve_linux.go`; add the wire
  field to `buildwire.BuildSpec` and the server mapping in `pkg/server/build_attach.go`. Do not
  import the BuildKit `client` package outside linux-only solve code.
- Executing builds needs root or a rootless userns stack; iterate in a privileged container via
  `sg docker` (see memory `privileged-build-tests-via-docker`). `go build ./...` always compiles
  the engine; only execution is gated.
- The regression net for any transport/lazy change is `e2e/scenarios/build-edge.star` (7 cases,
  local + remote), plus `build-mounts.star`, `build-cache.star`, `build-lazy.star`.
- Hostile-input confinement tests must drive a raw `p9.Client` — the honest server-side adapter
  sanitizes paths and cannot exercise escapes.

## Files

- `pkg/build/builder/` — engine (`//go:build linux`), `solve_linux.go` (frontendAttrs, cache
  remap, lazy routing, pre-seed), `localcache.go`, `remotesnapshotter_linux.go`,
  `lazyseed_linux.go`, `lock_linux.go`, `sweep_linux.go`, `engine_containerd_linux.go`
  (containerd worker branch)
- `pkg/build/buildwire/` — `Serve`/`Attach`, `BuildSpec`, `p9fs.go`, `confinedfs.go`, `ssh.go`,
  `ninep_backing.go` (LazyBackings)
- `pkg/build/internal/lazyctx/` — manifest, synthetic image, content store, digests
- `pkg/wire/` — BuildKit-free transport core (`Serve9PBacking`, `Backing9PSocket`, tags)
- `cmd/cornus/build.go` — `--builder`, `--build-context`, `--secret`, `--ssh`, `--lazy`

## Tests

- Unprivileged, in the default gate: `localcache_test.go`, `buildwire_test.go`,
  `p9fs_fsutil_test.go`, `confinedfs_test.go` (raw p9.Client), `TestSSHTunnel`, all
  `lazyctx` tests, `remotesnapshotter_linux_test.go`, containerd-worker unit tests (env
  resolution, lazy rejection, dead-socket fast-fail).
- Privileged: `engine_linux_test.go` (`TestRegistryCache`, `TestLocalCache`,
  `TestBuildAndPush`, root-gated); `TestBuildAndPushContainerdWorker` (root+daemon-gated;
  asserts registry pull-back AND `ImageService().Get` sees the tag);
  `pkg/wire/ninep_backing_test.go` kernel-mount test.
- E2E: `build-edge.star`, `build-mounts.star`, `build-cache.star`, `build-invalidate.star`,
  `build-lazy.star`, `build-lazy-9p.star` (NOT in default SCENARIOS), `build-upload.star`.
  The containerd E2E target (`--target containerd`, `make e2e-containerd`) sets
  `CORNUS_BUILD_WORKER=containerd` in ServeEnv, exercising the containerd worker live.

## Pitfalls

The full lists live in the source docs; the ones that bite across topic boundaries:

- The snapshotter name "stargz", the `containerd.io/snapshot/`-prefixed label, the
  Walk-discoverable committed snapshot, and `containerd.io/uncompressed` on the descriptor are
  ALL load-bearing; refuse (never delegate) foreign stargz-mode remote requests.
- Without the contenthash pre-seed, the RUN cache-key scan reads the entire mount over the
  network; the seeded ref must be held (`GetByBlob` before Solve, Release after).
- `type=local` cache `dest=`/`src=` are keys under `<Root>/localcache/`, not host paths.
- `--lazy` and `CORNUS_BUILD_WORKER=containerd` are mutually exclusive — the containerd worker
  rejects lazy builds; do not try to thread the "stargz" wrapper into it.
- v9fs xattr interop: the confined export must override ListXattrs -> empty and
  GetXattr -> ENODATA, or contenthash fails with "invalid argument" on the first file.
- `CORNUS-9P served N bytes` / `CORNUS-9P-BACKING` progress lines are scenario-parsed contracts
  printed to `progressW` — never convert them to slog.
- Local build + harness-launched server sharing a data dir: the engine.lock fail-fast catches it,
  but tests should isolate per-role data dirs and use `no_cache=True` when exercising wire mounts.
- `sshprovider.toAgentSource` stats the agent socket at construction — start the agent first.
