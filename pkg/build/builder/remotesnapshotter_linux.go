//go:build linux

package builder

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/errdefs"
	"github.com/moby/buildkit/worker/runc"

	"cornus/pkg/build/internal/lazyctx"
)

// lazyBuildEnv, when set, wraps the base snapshotter with the remote snapshotter
// and — critically — registers it under the name "stargz", the sole name
// BuildKit routes through the label-carrying, skip-extraction remote path. Off by
// default. With no layer yet carrying lazyctx.LazyLabel, every layer takes the
// passthrough path, so enabling it exercises exactly one question: does the
// "stargz" name break ordinary builds?
const lazyBuildEnv = "CORNUS_LAZY_BUILD"

// lazyBuildEnabled reports whether lazy named-context builds are on. Both the
// snapshotter wiring and the solve-side context wiring key off this, so they stay
// consistent.
func lazyBuildEnabled() bool { return os.Getenv(lazyBuildEnv) != "" }

// lazy9pEnv, when set (with CORNUS_LAZY_BUILD), backs lazy contexts with a
// kernel-9p mount of an in-process p9 server instead of a host-dir bind — so we
// can measure how much of a context a build actually pulls over 9P. Requires the
// 9p kernel module (trans=unix / 9pnet_fd).
const lazy9pEnv = "CORNUS_LAZY_9P"

func lazy9pEnabled() bool { return os.Getenv(lazy9pEnv) != "" }

// lazySnapshotterFactory wraps f with the remote snapshotter, renamed to "stargz"
// (the name BuildKit gates lazy remote-snapshot behavior on). It is applied
// unconditionally: the wrap is a no-op for ordinary builds (verified —
// TestBuildAndPush passes with it active), so lazy is a per-build routing decision
// (the --lazy flag / CORNUS_LAZY_BUILD chooses to serve contexts lazily) rather
// than a server-startup toggle. This lets a remote build go lazy without the
// server itself opting in.
// reg, when non-nil, records each remoteSnapshotter the factory builds so the
// engine can release their bookkeeping on Close (see snapshotterRegistry).
func lazySnapshotterFactory(f runc.SnapshotterFactory, reg *snapshotterRegistry) runc.SnapshotterFactory {
	inner := f.New
	return runc.SnapshotterFactory{
		Name: lazyctx.RemoteSnapshotterName, // "stargz" — the gate (refs.go:1002)
		New: func(root string) (snapshots.Snapshotter, error) {
			sn, err := inner(root)
			if err != nil {
				return nil, err
			}
			rs := newRemoteSnapshotter(sn)
			if reg != nil {
				reg.add(rs)
			}
			return rs, nil
		},
	}
}

// remoteSnapshotter is the SKELETON of cornus's lazy-bind-mount snapshotter. It
// wraps a base containerd snapshotter and, for layers cornus marks as lazy,
// presents them as *remote* snapshots that BuildKit mounts WITHOUT extracting —
// so a large `RUN --mount=type=bind,from=<image-context>` reads its files on
// demand from the backing instead of being eagerly copied into a snapshot.
//
// Protocol (mirrors the stargz/remote-snapshotter contract BuildKit drives at
// cache/refs.go:1088-1107): BuildKit Prepares each pulled layer with
// WithLabels{"containerd.io/snapshot.ref": <chainID>, <source labels>}. A
// snapshotter that can serve the layer remotely creates the committed snapshot
// under <chainID>, labels it "containerd.io/snapshot/remote", and returns
// errdefs.ErrAlreadyExists — signalling "ready, do not download/extract". Later,
// Stat(<chainID>) shows the remote label (BuildKit's isLazy, refs.go:304) and
// View/Mounts return the backing mount.
//
// SKELETON SCOPE: cornus marks a lazy layer with lazyctx.LazyLabel, whose value
// is a host directory that backs the snapshot with a read-only bind. The real
// implementation replaces that host dir with a per-snapshot kernel-9p mount
// (mount -t 9p,cache=loose) of the caller's export subtree, set up at Prepare and
// torn down at Remove. Everything else — the BuildKit interaction proven here —
// is unchanged. Not wired into the engine yet: no layer carries the label until
// the image-source plumbing (synthetic OCI layer over 9p) exists.
//
// CRITICAL: for BuildKit to deliver the label to Prepare at all and take the
// skip-extraction path, this snapshotter must be registered under the name
// lazyctx.RemoteSnapshotterName ("stargz") — that name is BuildKit's sole gate
// for the remote-snapshot path (refs.go:1002). See
// .agents/docs/LTM/lazy-bind-mounts.md.
const (
	// remoteSnapshotLabel is what BuildKit's isLazy() inspects (refs.go:304).
	remoteSnapshotLabel = "containerd.io/snapshot/remote"
	// targetRefLabel is the committed snapshot id BuildKit asks us to create.
	targetRefLabel = "containerd.io/snapshot.ref"
)

// lazySnap is a committed lazy layer: a synthetic committed snapshot the
// containerd metadata snapshotter can discover (via Walk) and whose View/Mounts
// return the backing instead of an unpacked layer.
type lazySnap struct {
	parent string // backend parent key (bparent), "" for a base layer
	dir    string // backing (host dir today; a 9p mountpoint later)
}

type remoteSnapshotter struct {
	inner snapshots.Snapshotter

	// Leak note: committed/views are the snapshotter's only unbounded state.
	// Entries are ADDED in Prepare (committed) / View (views), DROPPED in Remove
	// when BuildKit releases the snapshot, and — because the engine is a
	// process-lifetime singleton (server.getEngine) — a build that aborts/crashes
	// before BuildKit issues its Remove would otherwise leak its entry for the
	// whole server lifetime. releaseAll() (wired into Engine.Close) SWEEPS both
	// maps at engine teardown so the bound is: live-build entries + at most one
	// engine's worth of orphaned entries, all freed on shutdown.
	mu        sync.Mutex
	committed map[string]lazySnap // name (== snapshot.ref target) -> backing
	views     map[string]string   // active view key -> backing dir
}

var _ snapshots.Snapshotter = (*remoteSnapshotter)(nil)

func newRemoteSnapshotter(inner snapshots.Snapshotter) *remoteSnapshotter {
	return &remoteSnapshotter{
		inner:     inner,
		committed: map[string]lazySnap{},
		views:     map[string]string{},
	}
}

// remoteMounts is the mount handed to BuildKit for a lazy layer. The backing
// string selects the transport:
//
//	9p:<socket>  → a read-only kernel-9p mount of a p9 server (trans=unix), for
//	               remote builds: files are pulled on demand from the caller.
//	dir:<path> or <path> → a read-only bind of a host directory (local builds).
//
// BuildKit's LocalMounter runs the actual mount(2), so the snapshotter stays
// stateless; containerd unmounts on release.
func remoteMounts(backing string) []mount.Mount {
	if sock, ok := strings.CutPrefix(backing, "9p:"); ok {
		return []mount.Mount{{
			Type:   "9p",
			Source: sock,
			// cache=loose is safe: a build context is read-only for the build.
			Options: []string{"ro", "trans=unix", "version=9p2000.L", "msize=1048576", "cache=loose"},
		}}
	}
	return []mount.Mount{{
		Type:    "bind",
		Source:  strings.TrimPrefix(backing, "dir:"),
		Options: []string{"rbind", "ro"},
	}}
}

func applyOpts(opts []snapshots.Opt) snapshots.Info {
	var info snapshots.Info
	for _, o := range opts {
		_ = o(&info)
	}
	return info
}

func (s *remoteSnapshotter) committedInfo(name string, ls lazySnap) snapshots.Info {
	return snapshots.Info{
		Kind:   snapshots.KindCommitted,
		Name:   name,
		Parent: ls.parent,
		Labels: map[string]string{targetRefLabel: name, remoteSnapshotLabel: "1"},
	}
}

// Prepare handles BuildKit's stargz-mode remote-snapshot requests (identified by
// the containerd.io/snapshot.ref label) and delegates everything else.
//
//   - Our lazy layer (carries lazyctx.LazyLabel): register a COMMITTED snapshot
//     named after the target ref — which the containerd metadata snapshotter then
//     discovers via Walk and records — and return ErrAlreadyExists so BuildKit
//     skips extraction (metadata/snapshot.go:385-414).
//   - A remote request that is NOT ours: REFUSE with an error — do NOT delegate.
//     A delegated Prepare would make the base create a temp snapshot that then
//     leaks (BuildKit breaks to the eager path with a fresh key, refs.go:1102-1135).
//   - No remote request (ordinary prepare): delegate to the base unchanged.
func (s *remoteSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	info := applyOpts(opts)
	target := info.Labels[targetRefLabel]
	if target == "" {
		return s.inner.Prepare(ctx, key, parent, opts...)
	}
	dir := info.Labels[lazyctx.LazyLabel]
	if dir == "" {
		return nil, fmt.Errorf("builder: %s is not a cornus lazy layer: %w", target, errdefs.ErrNotImplemented)
	}
	s.mu.Lock()
	s.committed[target] = lazySnap{parent: parent, dir: dir}
	s.mu.Unlock()
	return nil, fmt.Errorf("cornus lazy layer %s ready: %w", target, errdefs.ErrAlreadyExists)
}

// Walk yields our committed lazy snapshots first (so the metadata snapshotter's
// post-Prepare Walk can match one by snapshot.ref + parent), then delegates.
func (s *remoteSnapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error {
	s.mu.Lock()
	infos := make([]snapshots.Info, 0, len(s.committed))
	for name, ls := range s.committed {
		infos = append(infos, s.committedInfo(name, ls))
	}
	s.mu.Unlock()
	for _, i := range infos {
		if err := fn(ctx, i); err != nil {
			return err
		}
	}
	return s.inner.Walk(ctx, fn, filters...)
}

// Stat returns our synthetic info for a lazy committed snapshot or view (so
// unlazy's Stat(snapshotID) succeeds and no blob is fetched), else delegates.
func (s *remoteSnapshotter) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	s.mu.Lock()
	if ls, ok := s.committed[key]; ok {
		s.mu.Unlock()
		return s.committedInfo(key, ls), nil
	}
	if _, ok := s.views[key]; ok {
		s.mu.Unlock()
		return snapshots.Info{Kind: snapshots.KindView, Name: key, Labels: map[string]string{remoteSnapshotLabel: "1"}}, nil
	}
	s.mu.Unlock()
	return s.inner.Stat(ctx, key)
}

// View mounts a lazy committed layer read-only: the view is the backing bind.
func (s *remoteSnapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	s.mu.Lock()
	if ls, ok := s.committed[parent]; ok {
		s.views[key] = ls.dir
		s.mu.Unlock()
		return remoteMounts(ls.dir), nil
	}
	s.mu.Unlock()
	return s.inner.View(ctx, key, parent, opts...)
}

// Mounts returns the backing mount for a lazy view (or committed layer), else delegates.
func (s *remoteSnapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	s.mu.Lock()
	if dir, ok := s.views[key]; ok {
		s.mu.Unlock()
		return remoteMounts(dir), nil
	}
	if ls, ok := s.committed[key]; ok {
		s.mu.Unlock()
		return remoteMounts(ls.dir), nil
	}
	s.mu.Unlock()
	return s.inner.Mounts(ctx, key)
}

func (s *remoteSnapshotter) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	s.mu.Lock()
	_, c := s.committed[info.Name]
	_, v := s.views[info.Name]
	s.mu.Unlock()
	if c || v {
		return info, nil
	}
	return s.inner.Update(ctx, info, fieldpaths...)
}

func (s *remoteSnapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	s.mu.Lock()
	_, c := s.committed[key]
	_, v := s.views[key]
	s.mu.Unlock()
	if c || v {
		return snapshots.Usage{}, nil // lazy: no local usage
	}
	return s.inner.Usage(ctx, key)
}

// Remove drops a lazy snapshot (the real impl also unmounts its 9p backing), else delegates.
func (s *remoteSnapshotter) Remove(ctx context.Context, key string) error {
	s.mu.Lock()
	_, c := s.committed[key]
	_, v := s.views[key]
	delete(s.committed, key)
	delete(s.views, key)
	s.mu.Unlock()
	if c || v {
		return nil
	}
	return s.inner.Remove(ctx, key)
}

func (s *remoteSnapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	return s.inner.Commit(ctx, name, key, opts...)
}

func (s *remoteSnapshotter) Close() error { return s.inner.Close() }

// releaseAll clears the committed/views bookkeeping at engine teardown, freeing
// any entries an aborted/crashed build never Removed (see the Leak note on the
// struct). It is TEARDOWN-ONLY: never call it mid-build — an in-flight solve may
// still reference a committed entry — and it intentionally does NOT close the
// inner snapshotter (BuildKit's worker teardown owns that via Close). The lock is
// held while clearing so a concurrent snapshotter call sees a consistent map.
func (s *remoteSnapshotter) releaseAll() {
	s.mu.Lock()
	s.committed = map[string]lazySnap{}
	s.views = map[string]string{}
	s.mu.Unlock()
}

// snapshotterRegistry collects the remoteSnapshotter instances the factory builds
// (BuildKit may construct the worker's snapshotter lazily/once) so Engine.Close
// can release them all. Guarded because the factory New closure can run off the
// goroutine that constructed the engine.
type snapshotterRegistry struct {
	mu      sync.Mutex
	remotes []*remoteSnapshotter
}

func (r *snapshotterRegistry) add(s *remoteSnapshotter) {
	r.mu.Lock()
	r.remotes = append(r.remotes, s)
	r.mu.Unlock()
}

// releaseAll clears every registered snapshotter's bookkeeping (engine teardown).
func (r *snapshotterRegistry) releaseAll() {
	r.mu.Lock()
	remotes := r.remotes
	r.mu.Unlock()
	for _, s := range remotes {
		s.releaseAll()
	}
}
