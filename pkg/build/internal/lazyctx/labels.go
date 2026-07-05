package lazyctx

// LazyLabel is the layer-descriptor annotation (and thus snapshot label) that
// marks a layer as a cornus lazy bind context. Its value references the 9p
// export subtree the remote snapshotter mounts on demand (a host dir in the
// current skeleton).
//
// The key MUST be under the "containerd.io/snapshot/" prefix: BuildKit only
// forwards a layer-descriptor annotation to Snapshotter.Prepare if it survives
// containerd's snapshots.FilterInheritedLabels, which keeps only
// "containerd.io/snapshot.ref" and keys prefixed "containerd.io/snapshot/"
// (containerd snapshots/snapshotter.go). A plain "cornus.dev/..." key is
// silently dropped and never reaches Prepare. See
// buildkit cache/pull.go:148 and refs.go:1083.
const LazyLabel = "containerd.io/snapshot/cornus.dev.lazy.ref"

// RemoteSnapshotterName is the name the cornus remote snapshotter MUST be
// registered under (runc.SnapshotterFactory.Name) for BuildKit to route pulled
// layers through the label-carrying, skip-extraction remote-snapshot path. That
// path is gated solely on Snapshotter.Name() == "stargz"
// (buildkit cache/refs.go:1002, 974; manager.go:624); under any other name
// BuildKit takes the eager unlazyLayer path and Prepare receives no labels at
// all. This is the single most load-bearing (and easily-missed) requirement of
// the whole design. See .agents/docs/LTM/lazy-bind-mounts.md.
const RemoteSnapshotterName = "stargz"
