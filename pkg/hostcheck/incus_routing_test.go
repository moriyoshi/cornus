package hostcheck

import (
	"strings"
	"testing"

	"cornus/pkg/hostenv"
)

// TestIncusRoutingWarnsForAServerBesideIncusd is the check's reason to exist.
//
// Before it, a containerized incus operator's ENTIRE preflight output was a warning
// about paths — on a backend where cornus hands incusd no path to translate, so the
// remedy it named (CORNUS_HOST_PATH_MAP) changed nothing. The consequence that does
// bite went unmentioned until a port-forward dial failed.
func TestIncusRoutingWarnsForAServerBesideIncusd(t *testing.T) {
	env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeIncus}
	r := input("incus", env, mapped()).Run()

	c := find(t, r, CheckRouting)
	if c.Status != StatusWarn {
		t.Fatalf("status = %q, want %q", c.Status, StatusWarn)
	}
	if r.Failed() {
		t.Error("Failed() = true: deploys and host-published ports are unaffected, so this must not stop the server")
	}
	// All three remedies, because which one an operator can take depends on their
	// deployment and naming only one would strand the others.
	for _, want := range []string{"--network host", "incus instance", "CORNUS_INCUS_REMOTE"} {
		if !strings.Contains(c.Hint, want) {
			t.Errorf("hint must name %q, got %q", want, c.Hint)
		}
	}
	// The detail must be CONDITIONAL, not an assertion of failure. "Containerized
	// beside incusd" does not imply "no route": if incusd runs in the same container
	// its bridge is in the netns cornus already occupies, and nothing available here
	// can tell the two apart. Measured: the unconditional phrasing claimed "cannot
	// reach a workload" on all 9 server starts of the incus E2E leg, in the very run
	// where deploy-portforward.star reached one.
	if !strings.Contains(c.Detail, "if its container has a network namespace of its own") {
		t.Errorf("detail must state the CONDITION rather than assert an unreachable workload, got %q", c.Detail)
	}
}

// TestIncusRoutingIsCleanWhenTheServerCanReachWorkloads: the two configurations that
// genuinely have no routing problem must come back OK, or the warning is noise that
// operators learn to ignore in exactly the setups they got right.
func TestIncusRoutingIsCleanWhenTheServerCanReachWorkloads(t *testing.T) {
	t.Run("cornus is itself an instance", func(t *testing.T) {
		env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeIncus, SelfID: "cornus-srv"}
		c := find(t, input("incus", env, mapped()).Run(), CheckRouting)
		if c.Status != StatusOK {
			t.Errorf("status = %q (%s), want %q", c.Status, c.Detail, StatusOK)
		}
		if !strings.Contains(c.Detail, "cornus-srv") {
			t.Errorf("detail should name the instance, got %q", c.Detail)
		}
	})
	t.Run("remote mode", func(t *testing.T) {
		env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeIncus}
		in := input("incus", env, mapped())
		in.RemoteMode = true
		if c := find(t, in.Run(), CheckRouting); c.Status != StatusOK {
			t.Errorf("status = %q (%s), want %q", c.Status, c.Detail, StatusOK)
		}
	})
}

// A SelfID from some OTHER runtime must not be mistaken for an instance name. On a
// docker host SelfID is a container id; treating it as one here would silence the
// warning for the very topology that needs it.
func TestIncusRoutingIgnoresASelfIDFromAnotherRuntime(t *testing.T) {
	env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeDocker, SelfID: "deadbeefcafe"}
	if c := find(t, input("incus", env, mapped()).Run(), CheckRouting); c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
}

// The check must not fire where it cannot apply: a server on the host routes to
// incusbr0 fine, and no other backend has an incus bridge at all.
func TestIncusRoutingNotCheckedElsewhere(t *testing.T) {
	absent(t, input("incus", hostenv.Env{}, mapped()).Run(), CheckRouting)
	for _, backend := range []string{"", "containerd", "bare", "kubernetes"} {
		env := hostenv.Env{InContainer: true}
		absent(t, input(backend, env, mapped()).Run(), CheckRouting)
	}
}

// TestSummaryArticle: the runtime name is interpolated into the startup line, and
// "on a incus host" was what it read before. Cosmetic, but it is the first line an
// operator sees.
func TestSummaryArticle(t *testing.T) {
	r := Result{Env: hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeIncus}}
	if got := r.Summary(); !strings.Contains(got, "on an incus host") {
		t.Errorf("Summary() = %q, want it to read \"on an incus host\"", got)
	}
	r = Result{Env: hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeDocker}}
	if got := r.Summary(); !strings.Contains(got, "on a docker host") {
		t.Errorf("Summary() = %q, want it to read \"on a docker host\"", got)
	}
}

// TestIncusGetsNoPathWarningItCannotActOn: the unverified-paths warning is about
// paths cornus hands the runtime, and this backend hands it none — incusd owns every
// path it ever sees. Before this gate that warning was the only preflight output a
// containerized incus server produced, and its remedy (CORNUS_HOST_PATH_MAP) changed
// nothing, while the consequence that does bite went unmentioned.
func TestIncusGetsNoPathWarningItCannotActOn(t *testing.T) {
	env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeIncus}
	r := input("incus", env, mapped()).Run()
	absent(t, r, CheckSelfInspection)
	// ...and the check that DOES apply is present, so this is a swap, not a silencing.
	find(t, r, CheckRouting)
}

// The same warning must keep firing where it is actionable, or the gate above has
// quietly removed a real guard.
func TestPathWarningStillFiresWhereItIsActionable(t *testing.T) {
	for _, backend := range []string{"", "containerd"} {
		env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeContainerd}
		if c := find(t, input(backend, env, unmapped()).Run(), CheckSelfInspection); c.Status != StatusWarn {
			t.Errorf("%s: status = %q, want %q", backend, c.Status, StatusWarn)
		}
	}
}
