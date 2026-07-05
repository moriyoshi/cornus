//go:build linux

package builder

import (
	"context"
	"testing"

	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/errdefs"

	"cornus/pkg/build/internal/lazyctx"
)

// TestRemoteSnapshotterServesLazyLayer exercises the remote-snapshot protocol the
// skeleton must satisfy for BuildKit to skip extraction: a Prepare carrying the
// cornus lazy label + the target snapshot ref returns ErrAlreadyExists and does
// NOT touch the base snapshotter; Stat then reports the remote label BuildKit's
// isLazy() checks; and View/Mounts return the backing bind.
func TestRemoteSnapshotterServesLazyLayer(t *testing.T) {
	base := &fakeSnapshotter{
		prepareMounts: nil,
		statInfo:      snapshots.Info{Name: "base"},
	}
	s := newRemoteSnapshotter(base)
	ctx := context.Background()
	const (
		chainID = "sha256:deadbeef"
		backing = "/host/lazy-ctx"
	)

	// BuildKit prepares the pulled layer with snapshot.ref + our lazy label.
	_, err := s.Prepare(ctx, "tmp-key "+chainID, "",
		snapshots.WithLabels(map[string]string{
			targetRefLabel:    chainID,
			lazyctx.LazyLabel: backing,
		}))
	if !errdefs.IsAlreadyExists(err) {
		t.Fatalf("Prepare error = %v, want IsAlreadyExists (signals skip-extract)", err)
	}
	if base.prepareCalls != 0 {
		t.Fatalf("base snapshotter was hit for a lazy layer (%d prepares)", base.prepareCalls)
	}

	// isLazy() will Stat the committed ref and require the remote label.
	info, err := s.Stat(ctx, chainID)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Kind != snapshots.KindCommitted {
		t.Errorf("Stat kind = %v, want Committed", info.Kind)
	}
	if _, ok := info.Labels[remoteSnapshotLabel]; !ok {
		t.Errorf("Stat labels missing %q: %v", remoteSnapshotLabel, info.Labels)
	}

	// The bind context is consumed via a View of the committed layer, then Mounts.
	vm, err := s.View(ctx, chainID+"-view", chainID)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(vm) != 1 || vm[0].Type != "bind" || vm[0].Source != backing {
		t.Fatalf("View mounts = %+v, want ro bind to %s", vm, backing)
	}
	mm, err := s.Mounts(ctx, chainID+"-view")
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	if len(mm) != 1 || mm[0].Source != backing {
		t.Fatalf("Mounts = %+v, want bind to %s", mm, backing)
	}
	var ro bool
	for _, o := range mm[0].Options {
		if o == "ro" {
			ro = true
		}
	}
	if !ro {
		t.Errorf("backing bind not read-only: %v", mm[0].Options)
	}
}

// TestRemoteSnapshotterPassesThroughOrdinaryLayers confirms an ordinary prepare
// (no snapshot.ref label) is delegated to the base snapshotter unchanged.
func TestRemoteSnapshotterPassesThroughOrdinaryLayers(t *testing.T) {
	base := &fakeSnapshotter{statInfo: snapshots.Info{Name: "ordinary", Parent: "p"}}
	s := newRemoteSnapshotter(base)
	ctx := context.Background()

	if _, err := s.Prepare(ctx, "k", "parent"); err != nil {
		t.Fatalf("Prepare passthrough: %v", err)
	}
	if base.prepareCalls != 1 {
		t.Errorf("base prepares = %d, want 1 (delegated)", base.prepareCalls)
	}
	info, err := s.Stat(ctx, "k")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "ordinary" {
		t.Errorf("Stat did not delegate: %+v", info)
	}
}

// TestRemoteSnapshotterWalkYieldsCommitted proves the containerd metadata
// snapshotter's post-Prepare protocol works: after a lazy Prepare, Walk must
// yield a COMMITTED snapshot whose snapshot.ref label == target and whose parent
// matches, or the metadata layer returns ErrNotFound and BuildKit falls back to
// fetching the (absent) blob.
func TestRemoteSnapshotterWalkYieldsCommitted(t *testing.T) {
	base := &fakeSnapshotter{}
	s := newRemoteSnapshotter(base)
	ctx := context.Background()
	const target = "sha256:abc123"

	_, err := s.Prepare(ctx, "bkey", "bparent",
		snapshots.WithLabels(map[string]string{targetRefLabel: target, lazyctx.LazyLabel: "/host/d"}))
	if !errdefs.IsAlreadyExists(err) {
		t.Fatalf("Prepare: %v", err)
	}

	var found *snapshots.Info
	err = s.Walk(ctx, func(_ context.Context, i snapshots.Info) error {
		if i.Kind == snapshots.KindCommitted && i.Labels[targetRefLabel] == target && i.Parent == "bparent" {
			ii := i
			found = &ii
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if found == nil {
		t.Fatal("Walk yielded no committed snapshot matching snapshot.ref+parent (metadata layer would ErrNotFound)")
	}
	if found.Name != target {
		t.Errorf("committed name = %q, want %q", found.Name, target)
	}
	// Stat(target) must then succeed so unlazy short-circuits without a blob.
	if info, err := s.Stat(ctx, target); err != nil || info.Kind != snapshots.KindCommitted {
		t.Errorf("Stat(target) = %+v, %v; want a committed snapshot", info, err)
	}
}

// TestRemoteSnapshotterRefusesForeignRemoteRequest confirms a stargz-mode prepare
// (snapshot.ref set) for a layer that is NOT ours is refused with an error rather
// than delegated — otherwise the base would create a temp snapshot that leaks
// once BuildKit falls back to eager extraction.
func TestRemoteSnapshotterRefusesForeignRemoteRequest(t *testing.T) {
	base := &fakeSnapshotter{}
	s := newRemoteSnapshotter(base)

	_, err := s.Prepare(context.Background(), "tmp-key sha256:x", "",
		snapshots.WithLabels(map[string]string{targetRefLabel: "sha256:x"}))
	if err == nil {
		t.Fatal("foreign remote-snapshot request should be refused with an error")
	}
	if errdefs.IsAlreadyExists(err) {
		t.Error("must not report AlreadyExists (would falsely claim we served it)")
	}
	if base.prepareCalls != 0 {
		t.Errorf("base was hit (%d prepares) — would leak a temp snapshot", base.prepareCalls)
	}
}

// TestRemoteSnapshotterReleaseAll proves the engine-teardown sweep clears the
// committed/views bookkeeping an aborted build never Removed (the Leak note
// invariant): after a lazy Prepare + View leave entries behind, releaseAll drops
// them so the long-lived engine does not retain them for its whole lifetime.
func TestRemoteSnapshotterReleaseAll(t *testing.T) {
	base := &fakeSnapshotter{}
	s := newRemoteSnapshotter(base)
	ctx := context.Background()
	const target = "sha256:leak"

	if _, err := s.Prepare(ctx, "k "+target, "bparent",
		snapshots.WithLabels(map[string]string{targetRefLabel: target, lazyctx.LazyLabel: "/host/d"})); !errdefs.IsAlreadyExists(err) {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := s.View(ctx, "view-key", target); err != nil {
		t.Fatalf("View: %v", err)
	}

	s.mu.Lock()
	nc, nv := len(s.committed), len(s.views)
	s.mu.Unlock()
	if nc == 0 || nv == 0 {
		t.Fatalf("precondition: committed=%d views=%d, want both > 0", nc, nv)
	}

	// Simulate the abort: BuildKit never calls Remove; engine teardown sweeps.
	s.releaseAll()

	s.mu.Lock()
	nc, nv = len(s.committed), len(s.views)
	s.mu.Unlock()
	if nc != 0 || nv != 0 {
		t.Errorf("after releaseAll committed=%d views=%d, want 0/0", nc, nv)
	}

	// Registry.releaseAll must fan out to every registered snapshotter and be
	// safe on a fresh entry.
	reg := &snapshotterRegistry{}
	reg.add(s)
	s.mu.Lock()
	s.committed["x"] = lazySnap{dir: "/host/x"}
	s.mu.Unlock()
	reg.releaseAll()
	s.mu.Lock()
	nc = len(s.committed)
	s.mu.Unlock()
	if nc != 0 {
		t.Errorf("registry releaseAll did not clear committed: %d", nc)
	}
}

func TestRemoteMountsBackingScheme(t *testing.T) {
	// 9p backing → a kernel-9p mount (trans=unix) of the socket.
	nine := remoteMounts("9p:/run/cornus/ctx.sock")
	if len(nine) != 1 || nine[0].Type != "9p" || nine[0].Source != "/run/cornus/ctx.sock" {
		t.Fatalf("9p mount = %+v", nine)
	}
	var trans bool
	for _, o := range nine[0].Options {
		if o == "trans=unix" {
			trans = true
		}
	}
	if !trans {
		t.Errorf("9p options missing trans=unix: %v", nine[0].Options)
	}
	// dir backing → a read-only host bind.
	for _, b := range []string{"dir:/host/ctx", "/host/ctx"} {
		bind := remoteMounts(b)
		if len(bind) != 1 || bind[0].Type != "bind" || bind[0].Source != "/host/ctx" {
			t.Errorf("bind mount for %q = %+v", b, bind)
		}
	}
}
