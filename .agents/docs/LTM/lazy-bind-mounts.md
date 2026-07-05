# Lazy bind mounts for builds (design + as-built)

## Summary

A large `--build-context` directory is served to the build **on demand** instead of being
eagerly synced into a snapshot: the context is packaged as a synthetic single-layer OCI image, a
remote snapshotter (registered under the name "stargz") mounts the caller's directory (host bind
locally, kernel-9p over the WebSocket remotely) without extraction, and BuildKit's RUN cache-key
contenthash is pre-seeded from producer-computed digests so the cache-key scan never reads the
mount. Measured result: a 20MB context whose build reads 11 bytes transfers **11 bytes** over the
wire (vs 20,000,022 bytes without the seed). No BuildKit fork; all seams are public. Opt-in per
build via `cornus build --lazy` (or `CORNUS_LAZY_BUILD`).

## Key Facts

- Works end to end, local AND remote (`--builder` over 9P/WebSocket), regression-clean against
  `e2e/scenarios/build-edge.star` (all 7 cases, local + remote, with lazy on).
- Opt-in: `cornus build --lazy`, bound to `env='CORNUS_LAZY_BUILD'` via kong (flag OR env fold
  into `c.Lazy`). `CORNUS_LAZY_9P` is an internal measurement sub-toggle (backs *local* lazy
  contexts with an in-process p9 server so byte counts can be measured).
- The "stargz"-named snapshotter wrapper is **always on** (no env gate); lazy is a per-build
  routing decision, so a remote build can go lazy without the server opting in. Verified a no-op
  for ordinary builds.
- Three mechanisms, all required:
  1. **Image-shaped source**: named context routed as `context:<name>=oci-layout://<ref>` backed
     by a session-attached `content.Store` (`SolveOpt.OCIStores`); layer blob never materialized;
     layer digest = deterministic metadata manifest of the tree.
  2. **Remote snapshotter named "stargz"**: recognizes the lazy layer via a
     `containerd.io/snapshot/`-prefixed descriptor annotation, registers a Walk-discoverable
     COMMITTED snapshot + returns `ErrAlreadyExists` so BuildKit skips extraction; `Mounts` returns
     the backing (host bind or kernel-9p).
  3. **Contenthash pre-seed**: before Solve, `GetByBlob` force-creates the layer's ref and the
     caller-computed per-file digests are written via exported
     `contenthash.GetCacheContext`/`CacheContext.HandleChange`/`SetCacheContext`, so
     `needsScan` is false and the RUN cache-key walk (which would read every file) is skipped.
- Label constants (`pkg/build/internal/lazyctx/labels.go`):
  `LazyLabel = "containerd.io/snapshot/cornus.dev.lazy.ref"`,
  `RemoteSnapshotterName = "stargz"`.
- Remote backing: kernel-9p (`mount -t 9p -o trans=unix,version=9p2000.L,cache=loose,ro`) of a
  local unix socket; each connection is proxied over a new `'L'`-tagged yamux stream to the
  caller, which serves a confined read-only 9P export (mirrors the SSH-forward tunnel).
- `.dockerignore` must be applied identically to the manifest, the digests, and the 9P export,
  or the seed mismatches the mount (this was a real bug; fixed caller-side in `buildwire`).

## Details

### Problem

`RUN --mount=type=bind,from=<ctx>` avoids the image-*layer* copy but BuildKit still materializes
the whole subtree into a snapshot before the RUN: local-source contexts are
`Prepare(parent="")` -> empty active snapshot -> DiffCopy writes all files in -> bind mount of
`snapshots/N/fs`. For remote builds the entire tree streams caller->server regardless of what the
build reads. Goal: fetch on demand, keep content-addressed cache keys.

### Why the bind context must be an image (spike results — approaches that don't work)

- **Executor mount hook** (`mounthook_linux.go`, retained inert + tested): wrapping
  `base.WorkerOpt.Executor` and rewriting `[]executor.Mount` is shallow (~80 lines, public seam:
  `Src` is a lazy `Mountable` realized in `oci.GenerateSpec`), but runs *downstream* of input
  resolution — the snapshot is already synced by then. Cannot avoid the copy.
- **Frontend `input:`/local swap**: named-context schemes are fixed
  (`local`/`docker-image`/`oci-layout`/`git`/`http`/`input`, dispatch in
  `frontend/dockerui/namedcontext.go`); `input:` injects an arbitrary `llb.State` but its leaves
  are still standard sources. Only image-shaped sources reach the extract path.
- **Runtime-confirmed** (tracing snapshotter, privileged host): the
  `containerd.io/snapshot/remote` short-circuit (`cache/refs.go:302-305,1088-1107`, BuildKit
  v0.18.2) exists only on the image extract path. Local-source snapshots carry no labels, are
  never committed, and the source *writes into* whatever the snapshotter returns — swapping mounts
  in the snapshotter cannot make a `local:` context lazy.

Therefore: source the context as an image + present its layer as a remote snapshot. Source-data
laziness in BuildKit is **snapshotter-level, not source-level**.

### Architecture

```
caller dir ──(content-free stat walk)──▶ synthetic single-layer OCI image
                                         layer digest = manifest(path,size,mtime,mode,linkname)
                                         layer descriptor annotation = lazyctx.LazyLabel
   │
   ├─ named context: context:<name>=oci-layout://<ref>  (session-attached OCI store,
   │  SolveOpt.OCIStores — NO local sync, layer blob never exists)
   │
   ├─ remote snapshotter (wraps overlayfs via runc.SnapshotterFactory, named "stargz"):
   │    Prepare(labels incl. snapshot.ref + LazyLabel) → register committed snapshot,
   │    return ErrAlreadyExists → BuildKit skips extraction
   │    View/Mounts → backing mount ("dir:<path>" host bind, or "9p:<socket>" kernel-9p)
   │
   └─ contenthash pre-seed (before Solve): GetByBlob(layerDesc) → ref;
      per-file digests → GetCacheContext/HandleChange/SetCacheContext
      → RUN cache-key scan skipped → only RUN-touched files cross the wire
```

### The two BuildKit seams (both required, both easy to miss)

**OCI store attach.** Implement a containerd `content.Store` (only `Info` + `ReaderAt` are
load-bearing read-only) and register it on the **client** session via
`client.SolveOpt.OCIStores[storeID]` (re-keyed `"oci:"+storeID`, `client/solve.go:144-206`). Emit
`llb.OCILayout(ref, llb.OCIStore(sessionID, storeID))`. The in-process gateway forwarder cannot
attach it (owns no session) — it must be the outer client SolveOpt in `engine.Solve`.

**The "stargz" gate.** A label reaches `Snapshotter.Prepare` only if BOTH:
1. It is a **layer-descriptor annotation** keyed under `containerd.io/snapshot/` (containerd's
   `FilterInheritedLabels` drops everything else; `cache/pull.go:148`). A plain `cornus.dev/...`
   key never arrives.
2. The snapshotter is **named `"stargz"`** — the sole gate (`cache/refs.go:1002`, also `:974`,
   `manager.go:624`) routing layers through `prepareRemoteSnapshotsStargzMode`, the only path that
   passes labels into `Prepare` and honors the `ErrAlreadyExists` skip-extract fast path. Any
   other name takes the eager `unlazyLayer` path (`refs.go:1311`) with no labels.

Consequence: the name routes ALL layers (incl. base images) through the remote path, so non-lazy
layers must fall back cleanly. Validated: base-image pull+extract survives, ordinary builds green.

### The remote-snapshot protocol as BuildKit + containerd actually drive it

BuildKit calls `Prepare(ctx, "tmp-<id> <chainID>", parent, WithLabels{"containerd.io/snapshot.ref":
<chainID>, <descriptor SnapshotLabels>})` (`cache/refs.go:1088-1107`). But `runc.NewWorkerOpt`
wraps the snapshotter in **containerd's metadata snapshotter**, whose remote-commit protocol
(`metadata/snapshot.go:385-414`) is: after `Prepare` returns `ErrAlreadyExists`, it `Walk`s the
inner snapshotter for a COMMITTED snapshot whose `snapshot.ref` label == target and whose parent
matches. Merely returning `ErrAlreadyExists` is NOT enough — without a Walk-discoverable committed
snapshot the metadata layer returns ErrNotFound and BuildKit falls to `unlazyLayer` (blob fetch,
which fails: our layer has no blob).

So the remote snapshotter, on a lazy `Prepare`, registers a synthetic COMMITTED snapshot named
after the target ref (Kind Committed, `snapshot.ref`=target, `containerd.io/snapshot/remote`
label, matching parent); `Walk` yields it; `Stat` returns it (unlazy short-circuits at
`refs.go:1165` with no blob); `View`/`Mounts` return the backing mount. Backing schemes:
`dir:<path>` (read-only host bind) and `9p:<socket>` (a `{Type:"9p", trans=unix,ro,cache=loose}`
mount — BuildKit's LocalMounter performs the mount(2)).

Foreign remote requests (stargz-mode Prepare for layers we don't serve) must be **refused**
(ErrNotImplemented), not delegated: BuildKit `break`s to the eager path on a non-AlreadyExists
Prepare with a fresh key and never cleans a delegated snapshot — delegation leaks one temp
snapshot per layer.

### The contenthash problem and the producer-side-hash seed

**Critical finding:** the RUN cache key for a readonly bind mount is computed by contenthash,
which **walks the whole mount reading file content** (`cache/contenthash/checksum.go`: scanPath ->
prepareDigest -> io.CopyBuffer). `getMountDeps` (`solver/llbsolver/ops/exec.go`) sets
`ContentBasedHash = true` for readonly/skip-output/root mounts — i.e. all typical bind contexts.
The metadata-manifest digest is the *image/layer* key, not the RUN key. Measured: 9p backing
without the seed served **20,000,022 bytes** for a build that reads 11 bytes. The only in-LLB
escape, the `content-cache=off` mount option, is not parsed by the dockerfile frontend in v0.18.2.

**Solution (fork-free):** pre-seed the contenthash cache from producer-computed digests.
- `contenthash.GetCacheContext(ctx, ref)` / `SetCacheContext(ctx, ref, cc)` are exported;
  `CacheContext.HandleChange(ChangeKindAdd, path, fi, nil)` takes the digest from `fi.(Hashed)`
  (`type Hashed interface { Digest() digest.Digest }`; `fi.Sys()` must be `*fstypes.Stat`) — it
  never opens the file. Persisted via keyContentHash; `Checksum`'s `needsScan` then returns false.
- The ref must exist before Solve: `CacheManager().GetByBlob(ctx, layerDesc, nil, ...)` is
  content-addressed + idempotent, so pre-creating the ref and holding it (Release after Solve)
  makes the solve's own `GetByBlob` return the same seeded record. The synthetic layer descriptor
  must carry `containerd.io/uncompressed = <layerDigest>` so GetByBlob's diffID resolves
  (diffID == digest for an uncompressed layer).
- Digest format parity: `contenthash.NewFromStat(stat)` header + content produces the identical
  digest BuildKit's own scan would; `lazyctx.ComputeDigests(ctx, dir, ignore) []FileDigest`
  implements this. `FileDigest{Path, Stat *fstypes.Stat, Digest}` is serializable (Stat is a
  protobuf), so the caller computes digests locally (full local read, no network) and transmits
  them.

Rejected alternatives: CacheUpdater on the oci-layout source (no such hook on image sources);
wrapping cache.Manager (built internally by NewWorker, not injectable); a fully custom `input:`
LLB source with a cacheUpdater (much more code, loses oci-layout's lazy plumbing);
`content-cache=off` LLB post-processing (frontend surgery).

### Remote wiring (9p over WebSocket)

- Caller (`buildwire.Serve` + `ServeOpts.LazyContexts`, routed by `cmd/cornus` when `c.Lazy`):
  computes each lazy context's manifest digest + `ComputeDigests` locally, sends them in the
  `BuildSpec` (`LazySpec{Name, LayerDigest, LayerSize, Digests}`), and runs `wire.Serve9PBacking`
  — accepts `'L'`-tagged yamux streams, each serving a confined read-only 9P export of the
  context's dir.
- Server (`buildwire.LazyBackings` + build attach): per LazySpec, `wire.Backing9PSocket` returns a
  unix socket whose connections are proxied over new `'L'` streams to the caller; kernel-9p mounts
  it (`trans=unix`). `lazyctx.FromRemote(name, layerDigest, size, backing, digests)` builds the
  `LazyContext` without a local dir; `engine.Solve` pre-seeds contenthash from the caller's
  digests (the seed runs for both local and remote whenever `SolveInput.LazyContexts` is set).
- Local builds: `builder.Request.Lazy` -> `solve_linux.go` routes named contexts through
  `lazyctx.Prepare(name, dir, backing, ignore)` (backing = the dir), registers `lc.Store` on
  `SolveOpt.OCIStores`, sets `FrontendAttrs["context:<name>"] = lc.ContextAttr`.

### Cache-key manifest (layer identity)

`lazyctx/manifest.go`: deterministic metadata manifest (path,size,mtime,mode,linkname) via a
content-free walk with an ignore predicate; `Digest()` = sha256 of a canonical serialization =
the layer digest. mtime/size trust — the same trust BuildKit's local incremental relies on.
Note the layer digest (metadata) and the RUN cache key (seeded per-file content digests) are
distinct keys; content changes are caught by the seed digests.

### Measured results (privileged container, context = 20MB unread big.bin + 11-byte marker, RUN only `cat /d/marker`)

| Configuration | Bytes served over 9P |
|---|---|
| 9p backing, no contenthash seed | 20,000,022 (whole tree, cache-key scan) |
| 9p backing + seed, local | 11 |
| Remote `--builder` build over WebSocket, lazy | 11 |

### Backing FS decision

Default kernel-9p (`cache=loose` — safe because a build context is read-only/immutable for the
build): native page cache + readahead, near-zero FS code, 9P2000.L already spoken. FUSE is the
fallback for hosts lacking `CONFIG_9P_FS`. Reversible detail behind the snapshotter's `Mounts()`.

### Remaining work (polish, not mechanism)

GC/lifecycle of backing sockets + lazy snapshots + held refs; error paths + further v9fs interop
hardening; cross-platform caller (contenthash/fsutil on non-linux); benign "overlay differ
(ok=false)" export warning to investigate.

## Files

- `pkg/build/internal/lazyctx/` — cross-platform core:
  - `manifest.go` — deterministic metadata manifest (layer digest).
  - `image.go` — synthetic single-layer OCI image (config+manifest blobs real, layer blob never
    materialized; descriptor carries LazyLabel + `containerd.io/uncompressed`).
  - `store.go` — read-only `content.Store` for `SolveOpt.OCIStores`.
  - `labels.go` — `LazyLabel`, `RemoteSnapshotterName` ("stargz") + rationale.
  - `context.go` — `LazyContext`, `Prepare` (local dir), `FromRemote` (caller-computed digest).
  - `digests.go` — `ComputeDigests` / `FileDigest` (producer-side per-file contenthash digests).
- `pkg/build/builder/remotesnapshotter_linux.go` (+ test) — remote snapshotter: always-on
  "stargz" wrapper, metadata remote-commit protocol, `dir:`/`9p:` backing schemes,
  refuse-foreign-remote-requests.
- `pkg/build/builder/lazyseed_linux.go` — `hashedFileInfo`, `seedContentHash`,
  `Engine.preseedLazyRefs` (GetByBlob + hold refs across Solve).
- `pkg/build/builder/solve_linux.go` — engine wiring: lazy routing in `Build`/`Solve`,
  `dockerignoreFor`, pre-seed invocation, `CORNUS_LAZY_9P` measurement path.
- `pkg/build/builder/snapshotter_linux.go` (+ test) — tracing snapshotter
  (`CORNUS_SNAPSHOTTER_TRACE`), the recon tool that produced the live traces.
- `pkg/build/builder/mounthook_linux.go` (+ test) — inert executor-hook building block (spike #1).
- `pkg/wire/ninep_backing.go` (+ test) — `Serve9PBacking` (caller) / `Backing9PSocket` (server),
  `'L'`-tagged yamux streams, byte counting.
- `pkg/build/buildwire/ninep_backing.go` — `LazyBackings` (server glue) + `ignoreFunc`
  (.dockerignore -> `lazyctx.Ignore`, matching confinedfs's rule).
- `pkg/build/buildwire/spec.go` — `LazySpec` + `BuildSpec.LazyContexts`.
- `pkg/build/buildwire/serve.go` — caller side (digest computation, spec, 9P backing).
- `cmd/cornus/build.go` — `--lazy` flag (kong, `env='CORNUS_LAZY_BUILD'`), local/remote routing.

## Test Coverage

- Unit (no root, in default `go test ./...`):
  - `pkg/build/builder/remotesnapshotter_linux_test.go` — `TestRemoteSnapshotterServesLazyLayer`,
    `...PassesThroughOrdinaryLayers`, `...WalkYieldsCommitted`,
    `...RefusesForeignRemoteRequest`, `...ReleaseAll`, `TestRemoteMountsBackingScheme`.
  - `pkg/build/internal/lazyctx/` — manifest determinism/sensitivity/ignore/symlink,
    `TestSyntheticImageStructure`/`WriteLayout`, content-store serves-blobs-not-layer/read-only,
    `TestPrepareLazyContext`, `TestComputeDigestsDeterministicAndContentSensitive`.
  - `pkg/build/builder/snapshotter_linux_test.go`, `mounthook_linux_test.go` — spike wrappers.
- Root-gated: `pkg/wire/ninep_backing_test.go` `TestNinePBackingKernelMount` — real
  `mount -t 9p trans=unix` through the yamux proxy (run in a privileged container).
- E2E regression net: `e2e/scenarios/build-edge.star` (7 cases: .dockerignore, named-context
  ignore, symlink, multi-stage, build-arg, custom-dockerfile ignore precedence, negative
  ignored-COPY) — must stay green with lazy on, local + remote. Run privileged via `sg docker`
  (see memory `privileged-build-tests-via-docker`).
- Measurement harness: 20MB-unread + 11-byte-read fixture; the 9P servers count bytes
  (`CORNUS-9P` / `CORNUS-9P-BACKING` log lines); expect 11 bytes.

## Pitfalls

- **The snapshotter name "stargz" is load-bearing.** It is the sole gate for the label-carrying
  skip-extract path; it also routes ALL layers through that path, so the fallback for ordinary
  layers must be correct. Refuse (do not delegate) foreign stargz-mode remote requests, or you
  leak one temp snapshot per layer.
- **`ErrAlreadyExists` alone is insufficient.** containerd's metadata snapshotter requires a
  Walk-discoverable COMMITTED snapshot with `snapshot.ref`=target and matching parent; otherwise
  ErrNotFound -> blob fetch -> failure (no blob exists).
- **The lazy label must live under `containerd.io/snapshot/`** as a layer-descriptor annotation;
  any other key is filtered before `Prepare`.
- **contenthash defeats naive network laziness.** Without the pre-seed, the RUN cache-key scan
  reads every file over the backing (measured: whole 20MB). The seed digests must be computed with
  `contenthash.NewFromStat` parity or cold-cache keys diverge from scan results.
- **The seeded ref must be held.** Pre-seed refs are created with `GetByBlob` before Solve and
  Released only after, else GC can drop the seed.
- **`containerd.io/uncompressed` must be set** on the synthetic layer descriptor or GetByBlob's
  diffID resolution fails.
- **.dockerignore consistency**: the manifest, the per-file digests, and the 9P export must apply
  the identical ignore predicate (including keeping `.dockerignore` itself, confinedfs's rule) —
  divergence makes the seed mismatch the mount. This bug shipped once (lazy path served contexts
  unfiltered) and was caught by build-edge case 2.
- **v9fs xattr interop**: hugelgupf/p9's default ListXattrs returns ENOSYS -> v9fs listxattr fails
  EINVAL -> BuildKit contenthash errors on the first file ("failed to create hash for : invalid
  argument"). The confined export overrides ListXattrs -> empty and GetXattr -> ENODATA.
- **Local named-context `.dockerignore` asymmetry** (pre-existing): the eager local build path
  passes named contexts through `fsutil.NewFS` unfiltered, while the remote path filters
  caller-side. The lazy path filters on both.
- **Data-dir lock**: two engines on one data dir deadlock on BuildKit's boltdb (no lock timeout);
  `builder.New` takes a non-blocking flock on `<data-dir>/engine.lock` and fails fast. E2E
  harnesses must isolate per-role data dirs.
- **mtime/size trust**: the layer digest trusts metadata (same trust as BuildKit's local
  incremental); content changes are still caught by the seeded per-file content digests.
- **Privileged loop required**: executing builds needs root/privileged; iterate via
  `sg docker -c 'docker run --privileged ...'`.
