package hostcheck

import (
	"strings"
	"testing"

	"cornus/pkg/hostenv"
)

// TestDataDirCheckOnlyRunsForBackendsThatHandOverThePath pins which backends the
// data-dir check applies to, and why the answer is not "every backend that
// translates".
//
// The check's whole content is a consequence — "client-local mounts will be
// rejected", or for containerd "every deploy would get empty volumes and no
// managed /etc/hosts" — plus a hint to bind-mount the directory. For incus BOTH
// halves are false. incushost hands incusd a user's bind source (a host path by
// definition; translating it would be the bug), a storage-pool volume NAME, or
// tmpfs. It never builds a path from its data dir, and it is not a MountingBackend,
// so the mounts dir never reaches it either.
//
// An operator who followed that hint bind-mounted the data dir, watched the
// warning disappear, and changed nothing — client-local mounts stay rejected on
// incus because the backend cannot realize them at all. Advice that cannot help is
// worse than silence.
func TestDataDirCheckOnlyRunsForBackendsThatHandOverThePath(t *testing.T) {
	// A mapper that resolves nothing is what makes the check fire at all; with a
	// working one every case below would pass vacuously.
	m := unmapped()

	for _, tc := range []struct {
		backend string
		want    bool // is the data-dir check expected to appear?
		because string
	}{
		{"dockerhost", true, "the server kernel-9P-mounts under the data dir and the daemon binds the mountpoint"},
		{"containerd", true, "volume backings, the managed /etc/hosts, the log file and the log-shim binary all come from it"},
		{"incus", false, "incusd is handed user bind sources and volume NAMES; nothing cornus built under its data dir"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			r := input(tc.backend, translating(hostenv.RuntimeDocker), m).Run()
			_, found := lookup(r, CheckDataDir)
			if found != tc.want {
				t.Fatalf("data-dir check present = %v, want %v: %s", found, tc.want, tc.because)
			}
		})
	}
}

// TestIncusIsNotToldToBindMountForMountsItCannotDo is the user-facing half of the
// test above: whatever checks incus does draw, none of them may promise that
// client-local mounts become available.
//
// Stated separately because the gate and the message are different failure modes.
// Re-adding incus to the gate would fail the test above; re-wording some OTHER
// check into the same false promise would not, and this catches that.
func TestIncusIsNotToldToBindMountForMountsItCannotDo(t *testing.T) {
	m := unmapped()
	r := input("incus", translating(hostenv.RuntimeDocker), m).Run()

	for _, c := range r.Checks {
		if strings.Contains(c.Detail, "client-local mounts will be rejected") ||
			strings.Contains(c.Hint, "bind-mount it from the host") {
			t.Errorf("check %q tells an incus operator that a bind mount would make client-local mounts "+
				"available:\n  detail: %s\n  hint:   %s\n"+
				"incus is not a MountingBackend; client-local mounts are refused on it whatever the data dir "+
				"does, so following this hint costs the operator work and changes nothing.",
				c.Name, c.Detail, c.Hint)
		}
	}
}

// TestDataDirCheckStillFailsContainerd is the positive control. The gate above
// narrows which backends are checked, and a too-wide narrowing would silently
// disarm the one case that must REFUSE TO START: a containerized cornus driving a
// host containerd whose data dir it cannot see gets empty volumes on every deploy.
func TestDataDirCheckStillFailsContainerd(t *testing.T) {
	m := unmapped()
	r := input("containerd", translating(hostenv.RuntimeContainerd), m).Run()

	c := find(t, r, CheckDataDir)
	if c.Status != StatusFail {
		t.Errorf("status = %q, want %q: this is the configuration where deploys silently come up against "+
			"empty directories, which is what `cornus serve` must refuse to start on", c.Status, StatusFail)
	}
	if !r.Failed() {
		t.Error("Result.Failed() = false, so serving would continue")
	}
}

// lookup reports whether a named check is present, without failing the test the
// way find does.
func lookup(r Result, name string) (Check, bool) {
	for _, c := range r.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}
