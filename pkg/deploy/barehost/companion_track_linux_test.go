//go:build linux

package barehost

import (
	"strings"
	"testing"
)

// seedCompanion writes a companion record + fake runtime state for app/replica
// under the given role, mirroring what startCompanion persists.
func seedCompanion(t *testing.T, b *Backend, rt *fakeRuntime, app string, replica int, role, compID string) {
	t.Helper()
	rec := &instanceRecord{
		ID: compID, App: app, Replica: replica, Role: role,
		BundleDir: b.bundleDir(compID), LogPath: b.logPath(compID),
		Restart: "unless-stopped", DesiredRunning: true,
	}
	if err := b.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord companion: %v", err)
	}
	rt.cs[compID] = &runtimeState{ID: compID, Status: runcStateRunning, Bundle: rec.BundleDir}
}

// TestCompanionExcludedFromStatusAndList verifies companions are tracked as
// records (so Delete/supervision see them) but never reported as app instances.
func TestCompanionExcludedFromStatusAndList(t *testing.T) {
	b, rt := newTestBackend(t)
	seedInstance(t, b, rt, "web", 0, true)
	seedCompanion(t, b, rt, "web", 0, roleEgressCaretaker, "cornus-web-egress-0")

	st, err := b.Status(t.Context(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 1 {
		t.Fatalf("Status reported %d instances, want 1 (companion must be excluded): %+v", len(st.Instances), st.Instances)
	}
	if st.Instances[0].ID != instanceName("web", 0) {
		t.Errorf("Status instance = %q, want the app instance", st.Instances[0].ID)
	}

	all, err := b.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || len(all[0].Instances) != 1 {
		t.Fatalf("List = %+v, want one app with one instance (companion excluded)", all)
	}
}

// TestDeleteReapsCompanionsFirst verifies Delete removes both the app instance
// and its companion, and kills the companion BEFORE the app instance (the
// companion joins the app's netns, so it must go first).
func TestDeleteReapsCompanionsFirst(t *testing.T) {
	b, rt := newTestBackend(t)
	seedInstance(t, b, rt, "web", 0, true)
	seedCompanion(t, b, rt, "web", 0, roleMountCaretaker, "cornus-web-mount-0")

	if err := b.Delete(t.Context(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Both records gone.
	if recs, _ := b.recordsForApp("web"); len(recs) != 0 {
		t.Fatalf("records remain after Delete: %+v", recs)
	}
	// Both containers deleted from the runtime.
	if _, ok := rt.cs["cornus-web-mount-0"]; ok {
		t.Error("companion container not deleted")
	}
	if _, ok := rt.cs[instanceName("web", 0)]; ok {
		t.Error("app container not deleted")
	}
	// The companion is killed before the app instance.
	joined := strings.Join(rt.calls, ",")
	ci := strings.Index(joined, "kill:cornus-web-mount-0")
	ai := strings.Index(joined, "kill:"+instanceName("web", 0))
	if ci < 0 || ai < 0 {
		t.Fatalf("expected kills for both companion and app; calls=%v", rt.calls)
	}
	if ci > ai {
		t.Errorf("companion killed after app (want before); calls=%v", rt.calls)
	}
}
