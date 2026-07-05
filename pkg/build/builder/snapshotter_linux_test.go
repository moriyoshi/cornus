//go:build linux

package builder

import (
	"context"
	"testing"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
)

// fakeSnapshotter is a minimal snapshots.Snapshotter returning canned values, so
// the tracing wrapper can be tested without a real snapshotter or root.
type fakeSnapshotter struct {
	prepareMounts []mount.Mount
	statInfo      snapshots.Info
	committed     []string // name<-key pairs recorded as "name/key"
	prepareCalls  int
}

func (f *fakeSnapshotter) Prepare(_ context.Context, _, _ string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	f.prepareCalls++
	return f.prepareMounts, nil
}
func (f *fakeSnapshotter) View(_ context.Context, _, _ string, _ ...snapshots.Opt) ([]mount.Mount, error) {
	return f.prepareMounts, nil
}
func (f *fakeSnapshotter) Mounts(_ context.Context, _ string) ([]mount.Mount, error) {
	return f.prepareMounts, nil
}
func (f *fakeSnapshotter) Commit(_ context.Context, name, key string, _ ...snapshots.Opt) error {
	f.committed = append(f.committed, name+"/"+key)
	return nil
}
func (f *fakeSnapshotter) Stat(_ context.Context, _ string) (snapshots.Info, error) {
	return f.statInfo, nil
}
func (f *fakeSnapshotter) Update(_ context.Context, info snapshots.Info, _ ...string) (snapshots.Info, error) {
	return info, nil
}
func (f *fakeSnapshotter) Usage(_ context.Context, _ string) (snapshots.Usage, error) {
	return snapshots.Usage{}, nil
}
func (f *fakeSnapshotter) Remove(_ context.Context, _ string) error { return nil }
func (f *fakeSnapshotter) Walk(_ context.Context, _ snapshots.WalkFunc, _ ...string) error {
	return nil
}
func (f *fakeSnapshotter) Close() error { return nil }

// TestTracingSnapshotterRecordsAndDelegates proves the wrapper (1) satisfies the
// snapshots.Snapshotter interface, (2) returns the inner snapshotter's values
// unchanged, and (3) records the calls we need to study remote-snapshot behavior,
// including labels carried in opts and the mounts returned.
func TestTracingSnapshotterRecordsAndDelegates(t *testing.T) {
	fake := &fakeSnapshotter{
		prepareMounts: []mount.Mount{{Type: "bind", Source: "/host/ctx", Options: []string{"rbind", "ro"}}},
		statInfo:      snapshots.Info{Parent: "p", Labels: map[string]string{"containerd.io/snapshot/remote": "1"}},
	}
	tr := newTracingSnapshotter(fake, nil)

	ctx := context.Background()
	const remoteLabel = "containerd.io/snapshot/remote"

	ms, err := tr.Prepare(ctx, "k1", "parent1", snapshots.WithLabels(map[string]string{remoteLabel: "1"}))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(ms) != 1 || ms[0].Source != "/host/ctx" {
		t.Fatalf("delegation lost the inner mounts: %+v", ms)
	}
	if _, err := tr.View(ctx, "k2", "parent2"); err != nil {
		t.Fatalf("View: %v", err)
	}
	if _, err := tr.Stat(ctx, "k1"); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if err := tr.Commit(ctx, "committed1", "k1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(fake.committed) != 1 || fake.committed[0] != "committed1/k1" {
		t.Fatalf("Commit not delegated: %v", fake.committed)
	}

	ev := tr.Events()
	byOp := map[string]snapEvent{}
	for _, e := range ev {
		byOp[e.Op] = e
	}

	// Prepare recorded with its parent, the remote label, and the returned mount.
	p := byOp["Prepare"]
	if p.Key != "k1" || p.Parent != "parent1" {
		t.Errorf("Prepare event = %+v", p)
	}
	if !contains(p.Labels, remoteLabel) {
		t.Errorf("Prepare labels missing %q: %v", remoteLabel, p.Labels)
	}
	if !contains(p.Mounts, "bind:/host/ctx") {
		t.Errorf("Prepare mounts = %v", p.Mounts)
	}
	// Stat surfaces the remote label from the inner Info — this is exactly what
	// BuildKit's isLazy() inspects to decide a layer is a remote snapshot.
	if !contains(byOp["Stat"].Labels, remoteLabel) {
		t.Errorf("Stat labels = %v, want %q", byOp["Stat"].Labels, remoteLabel)
	}
	for _, op := range []string{"Prepare", "View", "Stat", "Commit"} {
		if _, ok := byOp[op]; !ok {
			t.Errorf("missing recorded %s event", op)
		}
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
