package hostcheck

import (
	"strings"
	"testing"

	"cornus/pkg/hostenv"
)

// netnsDir is the path containerdhost.NetnsDir carries. Spelled literally here so
// this package's tests do not import pkg/deploy (and cannot import the internal
// package that owns it); TestNetnsDirConstantsAgree in the containerdhost package
// is what keeps the two spellings honest.
const testNetnsDir = "/run/cornus/netns"

// withNetns is input() plus the netns path the server supplies in production.
func withNetns(backend string, env hostenv.Env, m fakeMapper) Input {
	in := input(backend, env, m)
	in.NetnsDir = testNetnsDir
	return in
}

// TestNetnsInvisibleToContainerdIsFatal is the silent-to-loud conversion this
// check exists for.
//
// Cornus pins each instance's netns under /run — a container-private tmpfs — and
// hands that path to containerd, whose shim reopens it in the HOST's mount
// namespace. Measured on a `ctr`-run cornus container: a file created at that path
// inside the container is not visible from containerd's mount namespace at all.
// So without the bind EVERY deploy fails, and it fails late: after the image pull
// and after the previous healthy deployment has already been torn down. Refusing
// to start is the cheaper failure by a wide margin.
func TestNetnsInvisibleToContainerdIsFatal(t *testing.T) {
	env := translating(hostenv.RuntimeContainerd)
	// mapped(), not unmapped(): the data dir must be VISIBLE here so the netns
	// directory is the only thing wrong. With unmapped() the data-dir check fails
	// too, and the Failed() assertion below would pass no matter what this check
	// decided — proving nothing about the behaviour it names.
	r := withNetns("containerd", env, mapped()).Run()

	c := find(t, r, CheckNetns)
	if c.Status != StatusFail {
		t.Fatalf("status = %q, want %q", c.Status, StatusFail)
	}
	if !r.Failed() {
		t.Error("Failed() = false: a server that cannot start any workload must not start")
	}
	if !strings.Contains(c.Detail, testNetnsDir) {
		t.Errorf("detail must name the directory at fault, got %q", c.Detail)
	}
	// The remedy is the whole value of a preflight message; without it an operator
	// has a verdict and nowhere to go.
	if !strings.Contains(c.Hint, "rshared") || !strings.Contains(c.Hint, hostenv.HostPathMapEnv) {
		t.Errorf("hint must name both remedies (bind it shared, or declare the mapping), got %q", c.Hint)
	}
}

// TestNetnsVisibleAndSharedPasses: the documented topology must be clean, or the
// check is just noise operators learn to ignore.
func TestNetnsVisibleAndSharedPasses(t *testing.T) {
	m := mapped()
	m.toHost[testNetnsDir] = "/run/cornus/netns"
	env := translating(hostenv.RuntimeContainerd)
	r := withNetns("containerd", env, m).Run()

	if c := find(t, r, CheckNetns); c.Status != StatusOK {
		t.Errorf("status = %q (%s), want %q", c.Status, c.Detail, StatusOK)
	}
	if r.Failed() {
		t.Error("Failed() = true for a correctly bound netns directory")
	}
}

// TestNetnsPrivatePropagationWarns: bound but private behaves like not bound at
// all — pins created inside never appear outside — but the mount table cannot
// always tell us, so a wrong fatal would refuse a server that works.
func TestNetnsPrivatePropagationWarns(t *testing.T) {
	m := mapped()
	m.toHost[testNetnsDir] = "/run/cornus/netns"
	m.propagation = hostenv.PropagationPrivate
	env := translating(hostenv.RuntimeContainerd)
	r := withNetns("containerd", env, m).Run()

	c := find(t, r, CheckNetns)
	if c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
	if r.Failed() {
		t.Error("Failed() = true: propagation cannot always be determined, so it must not be fatal")
	}
}

// TestNetnsNotCheckedWhereItCannotApply keeps this from becoming a warning every
// operator sees and none can act on.
func TestNetnsNotCheckedWhereItCannotApply(t *testing.T) {
	t.Run("on the host", func(t *testing.T) {
		// Same filesystem on both sides: there is nothing to bind.
		absent(t, withNetns("containerd", hostenv.Env{}, unmapped()).Run(), CheckNetns)
	})
	t.Run("bare shares cornus's mount namespace", func(t *testing.T) {
		// bare's OCI runtime is cornus's own child, so it reopens the pin at the
		// very path cornus created it at. Asking it to bind /run/cornus would be
		// advice with no failure behind it.
		env := hostenv.Env{InContainer: true}
		absent(t, withNetns("bare", env, unmapped()).Run(), CheckNetns)
	})
	t.Run("other backends never pin a netns", func(t *testing.T) {
		env := translating(hostenv.RuntimeDocker)
		absent(t, withNetns("", env, unmapped()).Run(), CheckNetns)
		absent(t, withNetns("incus", env, unmapped()).Run(), CheckNetns)
		absent(t, withNetns("kubernetes", env, unmapped()).Run(), CheckNetns)
	})
	t.Run("co-located containerd, paths not translating", func(t *testing.T) {
		// The DinD shape: cornus and containerd in ONE container, so the pin is
		// visible by construction and no bind is needed. hostenv cannot tell this
		// apart from "the host's containerd, nothing declared", so the check must
		// stay quiet rather than advise `rshared` on a directory that already
		// resolves. Measured: it fired on all 11 server starts of the containerd
		// E2E leg before this gate existed.
		env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeContainerd}
		r := withNetns("containerd", env, unmapped()).Run()
		absent(t, r, CheckNetns)
		// ...and the check that DOES apply to "we cannot tell" is still there, so
		// this is a swap rather than a silencing.
		find(t, r, CheckSelfInspection)
	})
	t.Run("no netns dir supplied", func(t *testing.T) {
		// pkg/server always supplies it; a caller that does not must not get a
		// verdict about the empty path.
		env := translating(hostenv.RuntimeContainerd)
		absent(t, input("containerd", env, unmapped()).Run(), CheckNetns)
	})
}
