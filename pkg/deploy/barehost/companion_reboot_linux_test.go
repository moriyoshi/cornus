//go:build linux

package barehost

import (
	"log/slog"
	"os"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// Companion caretakers were excluded from host-reboot recovery outright
// (needsRebootRecovery returns false for any record with a Role), on the correct
// observation that recoverInstance would mint them a PRIVATE netns when they are
// supposed to JOIN their app instance's. The consequence was that after a reboot
// every companion fell through to launchSupervised with an unmounted rootfs and a
// bundle naming a dead pin, failed, and never came back.
//
// These tests cover the two halves of the replacement: WHICH companions may be
// resurrected at all (a per-role question, because a reboot severs the client
// connections some of them relay through), and WHETHER a given one currently
// needs rebuilding (a per-bundle question, because a companion's record carries
// no netns of its own to probe).

// bundleWithNetns writes a minimal OCI bundle naming netnsPath, and returns its
// directory.
func bundleWithNetns(t *testing.T, netnsPath string) string {
	t.Helper()
	dir := t.TempDir()
	spec := &specs.Spec{
		Version: "1.0.2",
		Linux: &specs.Linux{
			Namespaces: []specs.LinuxNamespace{
				{Type: specs.PIDNamespace},
				{Type: specs.NetworkNamespace, Path: netnsPath},
			},
		},
	}
	if err := writeBundleConfig(dir, spec); err != nil {
		t.Fatalf("writeBundleConfig: %v", err)
	}
	return dir
}

// TestCompanionRecoverableByRole pins the role policy. The distinction is not
// cosmetic: the mount and egress caretakers relay through the CLIENT that
// requested the deployment, and a host reboot destroyed that connection, so
// bringing them back produces a caretaker that cannot serve. Telemetry exports on
// its own and is safe to restart.
func TestCompanionRecoverableByRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
		why  string
	}{
		{roleTelemetryCaretaker, true, "self-contained: exports outward without relaying through the server"},
		{roleMountCaretaker, false, "its 9P source is the client's filesystem, gone after a reboot"},
		{roleEgressCaretaker, false, "its egress path terminates at the client, gone after a reboot"},
		{"some-role-added-later", false, "an unknown role must opt IN, never inherit resurrection"},
		{"", false, "not a companion at all"},
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			if got := companionRecoverable(c.role); got != c.want {
				t.Errorf("companionRecoverable(%q) = %v, want %v (%s)", c.role, got, c.want, c.why)
			}
		})
	}
}

// TestAppInstanceFor pins the companion -> app lookup, including the two ways it
// must NOT match: another companion of the same deployment, and the same
// deployment's other replicas.
func TestAppInstanceFor(t *testing.T) {
	app0 := &instanceRecord{ID: "cornus-web-0", App: "web", Replica: 0}
	app1 := &instanceRecord{ID: "cornus-web-1", App: "web", Replica: 1}
	other := &instanceRecord{ID: "cornus-api-0", App: "api", Replica: 0}
	comp0 := &instanceRecord{ID: "cornus-web-otel-0", App: "web", Replica: 0, Role: roleTelemetryCaretaker}
	mount0 := &instanceRecord{ID: "cornus-web-mount-0", App: "web", Replica: 0, Role: roleMountCaretaker}
	recs := []*instanceRecord{comp0, mount0, other, app1, app0}

	if got := appInstanceFor(recs, comp0); got != app0 {
		t.Errorf("appInstanceFor(replica 0 companion) = %v, want the replica-0 APP record", got)
	}
	comp1 := &instanceRecord{ID: "cornus-web-otel-1", App: "web", Replica: 1, Role: roleTelemetryCaretaker}
	if got := appInstanceFor(recs, comp1); got != app1 {
		t.Errorf("appInstanceFor(replica 1 companion) = %v, want the replica-1 app record", got)
	}
	// A companion whose app instance is gone is orphaned, not silently attached
	// to some other replica.
	orphan := &instanceRecord{ID: "cornus-gone-otel-0", App: "gone", Replica: 0, Role: roleTelemetryCaretaker}
	if got := appInstanceFor(recs, orphan); got != nil {
		t.Errorf("appInstanceFor(orphan) = %v, want nil", got)
	}
	// A companion at a replica index the app no longer has (scaled down) is also
	// orphaned rather than matched to replica 0.
	scaledAway := &instanceRecord{ID: "cornus-web-otel-7", App: "web", Replica: 7, Role: roleTelemetryCaretaker}
	if got := appInstanceFor(recs, scaledAway); got != nil {
		t.Errorf("appInstanceFor(departed replica) = %v, want nil", got)
	}
}

// TestCompanionNeedsRebootRecovery pins the per-bundle decision. The companion
// record's own NetNS is EMPTY by design, so the signal has to be its bundle's
// netns path compared against the app's current pin — a test that probed the
// companion record would pass while observing nothing.
func TestCompanionNeedsRebootRecovery(t *testing.T) {
	alive := func(string) bool { return true }
	dead := func(string) bool { return false }

	const oldPin = "/run/cornus/netns/old-pin"
	const freshPin = "/run/cornus/netns/fresh-pin"

	t.Run("bundle names the app's stale pin -> recover", func(t *testing.T) {
		comp := &instanceRecord{ID: "c", Role: roleTelemetryCaretaker, BundleDir: bundleWithNetns(t, oldPin)}
		app := &instanceRecord{ID: "a", NetNS: freshPin}
		got, err := companionNeedsRebootRecovery(comp, app, alive)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Error("a companion whose bundle names the app's OLD netns must be repointed")
		}
	})

	t.Run("bundle already names the app's live pin -> no rebuild", func(t *testing.T) {
		// This is the plain-crash case, and it is what makes the second pass safe
		// to run on every reconcile rather than only after a reboot.
		comp := &instanceRecord{ID: "c", Role: roleTelemetryCaretaker, BundleDir: bundleWithNetns(t, freshPin)}
		app := &instanceRecord{ID: "a", NetNS: freshPin}
		got, err := companionNeedsRebootRecovery(comp, app, alive)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("an already-correct bundle must not be rewritten")
		}
	})

	t.Run("app netns not yet live -> defer", func(t *testing.T) {
		// The app pass has not (or could not) rebuild the app's pin yet. Repointing
		// at a dead namespace would just bake in a second wrong answer.
		comp := &instanceRecord{ID: "c", Role: roleTelemetryCaretaker, BundleDir: bundleWithNetns(t, oldPin)}
		app := &instanceRecord{ID: "a", NetNS: freshPin}
		got, err := companionNeedsRebootRecovery(comp, app, dead)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("must not repoint at an app netns that is not alive")
		}
	})

	t.Run("orphaned companion -> no recovery", func(t *testing.T) {
		comp := &instanceRecord{ID: "c", Role: roleTelemetryCaretaker, BundleDir: bundleWithNetns(t, oldPin)}
		got, err := companionNeedsRebootRecovery(comp, nil, alive)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("a companion with no app instance must not be recovered")
		}
	})

	t.Run("app with no netns -> no recovery", func(t *testing.T) {
		comp := &instanceRecord{ID: "c", Role: roleTelemetryCaretaker, BundleDir: bundleWithNetns(t, oldPin)}
		app := &instanceRecord{ID: "a"}
		got, err := companionNeedsRebootRecovery(comp, app, alive)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("no app netns means nothing to rejoin")
		}
	})

	t.Run("an app instance is never handled here", func(t *testing.T) {
		// App instances go through needsRebootRecovery; this predicate must not
		// claim them, or a reboot would rebuild the same record twice by two
		// different routes.
		appRec := &instanceRecord{ID: "a", BundleDir: bundleWithNetns(t, oldPin)}
		other := &instanceRecord{ID: "b", NetNS: freshPin}
		got, err := companionNeedsRebootRecovery(appRec, other, alive)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("companionNeedsRebootRecovery must ignore app-instance records")
		}
	})

	t.Run("unreadable bundle is an error, not a false negative", func(t *testing.T) {
		// Silently reporting "no rebuild needed" for a bundle we could not read
		// would strand the companion with no diagnostic.
		comp := &instanceRecord{ID: "c", Role: roleTelemetryCaretaker, BundleDir: t.TempDir()}
		app := &instanceRecord{ID: "a", NetNS: freshPin}
		if _, err := companionNeedsRebootRecovery(comp, app, alive); err == nil {
			t.Error("a missing config.json must surface as an error")
		}
	})
}

// TestBundleNetnsPathRoundTrip pins the read half against the write half: the
// path rewriteNetnsPath writes is the path bundleNetnsPath reads back. Asserting
// each side separately would let both drift together — the failure mode the
// containerd hostname tests were rewritten for.
func TestBundleNetnsPathRoundTrip(t *testing.T) {
	dir := bundleWithNetns(t, "/run/cornus/netns/first")
	got, err := bundleNetnsPath(dir)
	if err != nil {
		t.Fatalf("bundleNetnsPath: %v", err)
	}
	if got != "/run/cornus/netns/first" {
		t.Fatalf("bundleNetnsPath = %q, want the written path", got)
	}
	if err := rewriteNetnsPath(dir, "/run/cornus/netns/second"); err != nil {
		t.Fatalf("rewriteNetnsPath: %v", err)
	}
	got, err = bundleNetnsPath(dir)
	if err != nil {
		t.Fatalf("bundleNetnsPath after rewrite: %v", err)
	}
	if got != "/run/cornus/netns/second" {
		t.Fatalf("bundleNetnsPath after rewrite = %q, want the rewritten path", got)
	}
}

// TestBundleNetnsPathNoNetworkNamespace mirrors rewriteNetnsPath's own refusal:
// a spec with no network namespace is a loud error in BOTH directions, never an
// empty string that would compare unequal to every real pin and so trigger an
// endless rebuild.
func TestBundleNetnsPathNoNetworkNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := writeBundleConfig(dir, &specs.Spec{Linux: &specs.Linux{}}); err != nil {
		t.Fatalf("writeBundleConfig: %v", err)
	}
	if _, err := bundleNetnsPath(dir); err == nil {
		t.Error("bundleNetnsPath should error when the spec has no network namespace")
	}
}

// TestReconcileCompanionsStandsDownClientTetheredRoles covers the loop itself,
// not just its predicates: the stand-down has a SIDE EFFECT (clearing
// DesiredRunning) that a pure-predicate test cannot observe. Without it the
// supervisor would retry a mount caretaker forever whose 9P peer no longer
// exists.
func TestReconcileCompanionsStandsDownClientTetheredRoles(t *testing.T) {
	const freshPin = "/run/cornus/netns/fresh"
	const stalePin = "/run/cornus/netns/stale"

	for _, tc := range []struct {
		role            string
		wantStoodDown   bool
		wantBundleNetns string
	}{
		// Client-tethered: left stopped, and its bundle is NOT repointed (there is
		// nothing to run).
		{roleMountCaretaker, true, stalePin},
		{roleEgressCaretaker, true, stalePin},
		// Self-contained: kept desired-running and repointed at the app's new pin.
		{roleTelemetryCaretaker, false, freshPin},
	} {
		t.Run(tc.role, func(t *testing.T) {
			b, rt := newTestBackend(t)
			seedInstance(t, b, rt, "web", 0, true)
			// The app instance came back on a freshly-allocated pin.
			if _, err := b.updateRecord(instanceName("web", 0), func(r *instanceRecord) error {
				r.NetNS = freshPin
				return nil
			}); err != nil {
				t.Fatalf("update app record: %v", err)
			}

			compID := "cornus-web-" + tc.role + "-0"
			seedCompanion(t, b, rt, "web", 0, tc.role, compID)
			// The companion is dead (the reboot killed it) and its bundle still
			// names the pin from before the reboot.
			delete(rt.cs, compID)
			bundle := b.bundleDir(compID)
			if err := os.MkdirAll(bundle, 0o700); err != nil {
				t.Fatalf("mkdir bundle: %v", err)
			}
			if err := writeBundleConfig(bundle, &specs.Spec{
				Version: "1.0.2",
				Linux: &specs.Linux{Namespaces: []specs.LinuxNamespace{
					{Type: specs.NetworkNamespace, Path: stalePin},
				}},
			}); err != nil {
				t.Fatalf("writeBundleConfig: %v", err)
			}

			recs, err := b.listRecords()
			if err != nil {
				t.Fatalf("listRecords: %v", err)
			}
			b.reconcileCompanions(t.Context(), recs, slog.Default(), func(string) bool { return true })

			got, err := b.readRecord(compID)
			if err != nil {
				t.Fatalf("readRecord: %v", err)
			}
			if stoodDown := !got.DesiredRunning; stoodDown != tc.wantStoodDown {
				t.Errorf("DesiredRunning=%v (stood down=%v), want stood down=%v",
					got.DesiredRunning, stoodDown, tc.wantStoodDown)
			}
			netns, err := bundleNetnsPath(bundle)
			if err != nil {
				t.Fatalf("bundleNetnsPath: %v", err)
			}
			if netns != tc.wantBundleNetns {
				t.Errorf("bundle netns = %q, want %q", netns, tc.wantBundleNetns)
			}
		})
	}
}
