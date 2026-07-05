//go:build linux

package builder

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/worker/runc"
)

// SPIKE (lazy bind mounts): the tracing snapshotter is reconnaissance for a
// future *remote* snapshotter that would present a `RUN --mount=type=bind,from=X`
// context as a lazy, kernel-9p-backed snapshot (labeled
// containerd.io/snapshot/remote) that BuildKit mounts WITHOUT extracting — so a
// large bind context is read on demand instead of eagerly copied into a snapshot.
//
// Before implementing that, we need to see exactly how BuildKit drives the
// containerd snapshotter for such a build: which keys/parents/labels flow through
// Prepare/View/Mounts/Commit/Stat, and where extraction (Commit of a chain)
// happens vs. where a remote snapshot could be substituted. tracingSnapshotter
// wraps the real snapshotter, changes no behavior, and records those calls.
//
// Wired via runc.SnapshotterFactory only when CORNUS_SNAPSHOTTER_TRACE is set,
// so default builds are untouched.

const snapshotterTraceEnv = "CORNUS_SNAPSHOTTER_TRACE"

// snapEvent is one recorded snapshotter call.
type snapEvent struct {
	Op     string   // Prepare/View/Mounts/Commit/Stat/Remove/...
	Key    string   // key or name
	Parent string   // parent snapshot (Prepare/View)
	Labels []string // label keys carried in opts, or on a Stat result
	Mounts []string // "type:source" for each returned mount
	Err    string
}

// tracingSnapshotter delegates every snapshots.Snapshotter call to inner while
// recording the ones relevant to lazy bind mounts. It is behavior-preserving.
type tracingSnapshotter struct {
	inner snapshots.Snapshotter
	log   io.Writer // per-call trace line; may be nil

	mu     sync.Mutex
	events []snapEvent
}

var _ snapshots.Snapshotter = (*tracingSnapshotter)(nil)

func newTracingSnapshotter(inner snapshots.Snapshotter, log io.Writer) *tracingSnapshotter {
	return &tracingSnapshotter{inner: inner, log: log}
}

// traceSnapshotterFactory wraps a factory so its snapshotter is traced when
// CORNUS_SNAPSHOTTER_TRACE is set; otherwise it is returned unchanged.
func traceSnapshotterFactory(f runc.SnapshotterFactory) runc.SnapshotterFactory {
	if os.Getenv(snapshotterTraceEnv) == "" {
		return f
	}
	inner := f.New
	f.New = func(root string) (snapshots.Snapshotter, error) {
		sn, err := inner(root)
		if err != nil {
			return nil, err
		}
		return newTracingSnapshotter(sn, os.Stderr), nil
	}
	return f
}

func (s *tracingSnapshotter) record(e snapEvent) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	if s.log != nil {
		fmt.Fprintf(s.log, "CORNUS-SNAP %-7s key=%q parent=%q labels=%v mounts=%v err=%s\n",
			e.Op, e.Key, e.Parent, e.Labels, e.Mounts, e.Err)
	}
}

// Events returns a copy of the recorded events (for tests / post-run analysis).
func (s *tracingSnapshotter) Events() []snapEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]snapEvent, len(s.events))
	copy(out, s.events)
	return out
}

func optLabelKeys(opts []snapshots.Opt) []string {
	var info snapshots.Info
	for _, o := range opts {
		_ = o(&info)
	}
	return sortedKeysOf(info.Labels)
}

func sortedKeysOf(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mountDescs(ms []mount.Mount) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Type+":"+m.Source)
	}
	return out
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *tracingSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	ms, err := s.inner.Prepare(ctx, key, parent, opts...)
	s.record(snapEvent{Op: "Prepare", Key: key, Parent: parent, Labels: optLabelKeys(opts), Mounts: mountDescs(ms), Err: errStr(err)})
	return ms, err
}

func (s *tracingSnapshotter) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) ([]mount.Mount, error) {
	ms, err := s.inner.View(ctx, key, parent, opts...)
	s.record(snapEvent{Op: "View", Key: key, Parent: parent, Labels: optLabelKeys(opts), Mounts: mountDescs(ms), Err: errStr(err)})
	return ms, err
}

func (s *tracingSnapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	ms, err := s.inner.Mounts(ctx, key)
	s.record(snapEvent{Op: "Mounts", Key: key, Mounts: mountDescs(ms), Err: errStr(err)})
	return ms, err
}

func (s *tracingSnapshotter) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	err := s.inner.Commit(ctx, name, key, opts...)
	s.record(snapEvent{Op: "Commit", Key: key, Parent: name, Labels: optLabelKeys(opts), Err: errStr(err)})
	return err
}

func (s *tracingSnapshotter) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	info, err := s.inner.Stat(ctx, key)
	s.record(snapEvent{Op: "Stat", Key: key, Parent: info.Parent, Labels: sortedKeysOf(info.Labels), Err: errStr(err)})
	return info, err
}

func (s *tracingSnapshotter) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	out, err := s.inner.Update(ctx, info, fieldpaths...)
	s.record(snapEvent{Op: "Update", Key: info.Name, Labels: sortedKeysOf(info.Labels), Err: errStr(err)})
	return out, err
}

func (s *tracingSnapshotter) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	return s.inner.Usage(ctx, key)
}

func (s *tracingSnapshotter) Remove(ctx context.Context, key string) error {
	err := s.inner.Remove(ctx, key)
	s.record(snapEvent{Op: "Remove", Key: key, Err: errStr(err)})
	return err
}

func (s *tracingSnapshotter) Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error {
	return s.inner.Walk(ctx, fn, filters...)
}

func (s *tracingSnapshotter) Close() error { return s.inner.Close() }
