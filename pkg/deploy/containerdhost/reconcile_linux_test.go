//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

func TestNeedsNetnsRepair(t *testing.T) {
	base := map[string]string{
		labelNetNS:          "/run/cornus/netns/x",
		restart.PolicyLabel: "unless-stopped",
		restart.StatusLabel: string(ctd.Running),
	}
	with := func(overrides map[string]string) map[string]string {
		l := map[string]string{}
		for k, v := range base {
			l[k] = v
		}
		for k, v := range overrides {
			if v == "" {
				delete(l, k)
			} else {
				l[k] = v
			}
		}
		return l
	}
	for _, tc := range []struct {
		name    string
		labels  map[string]string
		running bool
		alive   bool
		want    bool
	}{
		{"stale pin, desired running", base, false, false, true},
		{"pin still alive", base, false, true, false},
		{"task already running", base, true, false, false},
		{"no recorded netns", with(map[string]string{labelNetNS: ""}), false, false, false},
		{"restart policy no", with(map[string]string{restart.PolicyLabel: "", restart.StatusLabel: ""}), false, false, false},
		{"explicitly stopped", with(map[string]string{restart.ExplicitlyStoppedLabel: "true"}), false, false, false},
		{"stop label cleared by Start", with(map[string]string{restart.ExplicitlyStoppedLabel: "false"}), false, false, true},
		{"desired state stopped", with(map[string]string{restart.StatusLabel: string(ctd.Stopped)}), false, false, false},
		{"on-failure policy repairs too", with(map[string]string{restart.PolicyLabel: "on-failure"}), false, false, true},
	} {
		aliveFn := func(string) bool { return tc.alive }
		got, reason := needsNetnsRepair(tc.labels, tc.running, aliveFn)
		if got != tc.want {
			t.Errorf("%s: needsNetnsRepair = %v (%s), want %v", tc.name, got, reason, tc.want)
		}
	}
}

// rebootFakes simulates a host reboot for the fake store: all tasks are gone.
// The netns pin paths recorded in the labels never existed on the test host,
// so they read as stale, exactly like after a real reboot.
func rebootFakes(f *fakeClient) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		c.task = nil
	}
}

func TestReconcileRepairsStaleNetns(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)
	ctx := context.Background()

	// a: default policy (unless-stopped) -> repaired.
	// b: unless-stopped but explicitly stopped -> skipped.
	// c: restart "no" -> skipped.
	for _, spec := range []api.DeploySpec{
		{Name: "a", Image: "img"},
		{Name: "b", Image: "img"},
		{Name: "c", Image: "img", Restart: "no"},
	} {
		if _, err := b.Apply(ctx, spec); err != nil {
			t.Fatalf("Apply %s: %v", spec.Name, err)
		}
	}
	if err := b.Stop(ctx, "b"); err != nil {
		t.Fatalf("Stop b: %v", err)
	}
	rebootFakes(f)

	repaired, skipped, err := b.reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if repaired != 1 || skipped != 2 {
		t.Fatalf("reconcile = %d repaired, %d skipped; want 1, 2", repaired, skipped)
	}
	if got := fn.setupCounts["cornus-a-0"]; got != 2 {
		t.Fatalf("a setup calls = %d, want 2 (create + repair)", got)
	}
	for _, id := range []string{"cornus-b-0", "cornus-c-0"} {
		if got := fn.setupCounts[id]; got != 1 {
			t.Fatalf("%s setup calls = %d, want 1 (create only)", id, got)
		}
	}
	// The repair re-recorded the fresh hostrun.Attachment (a new IP from the fake) and
	// the hosts files followed.
	labels := f.containers["cornus-a-0"].labels
	if labels[labelIP] == "" || labels[labelIP] == "10.4.0.9" {
		t.Fatalf("repair did not refresh the IP label: %q", labels[labelIP])
	}
	var ips map[string]string
	if err := json.Unmarshal([]byte(labels[labelNetIPs]), &ips); err != nil || ips[hostrun.DefaultNetwork] != labels[labelIP] {
		t.Fatalf("net-IPs label not refreshed: %q (%v)", labels[labelNetIPs], err)
	}
	data, err := os.ReadFile(b.hosts.Path("cornus-a-0"))
	if err != nil {
		t.Fatalf("read hosts: %v", err)
	}
	if want := labels[labelIP] + "\ta\n"; !strings.Contains(string(data), want) {
		t.Fatalf("hosts not resynced after repair; want %q in:\n%s", want, data)
	}
}

func TestEnsureReconciledRunsOnce(t *testing.T) {
	f := newFakeClient()
	b, fn := newTestBackend(t, f)
	ctx := context.Background()
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "a", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rebootFakes(f)
	b.reconciled = false // Apply consumed the pass on an empty store; rearm.

	// Any API entry point triggers the pass lazily; Status is read-only apart
	// from the reconcile itself.
	if _, err := b.Status(ctx, "a"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := fn.setupCounts["cornus-a-0"]; got != 2 {
		t.Fatalf("setup calls after first reconcile = %d, want 2", got)
	}
	// The pin path is still "stale" on this host (it never exists), but the
	// once-guard keeps the pass from re-repairing on every call.
	if _, err := b.Status(ctx, "a"); err != nil {
		t.Fatalf("Status again: %v", err)
	}
	if got := fn.setupCounts["cornus-a-0"]; got != 2 {
		t.Fatalf("reconcile ran twice: %d setup calls", got)
	}
}
