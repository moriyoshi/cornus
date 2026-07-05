package hostcheck

import (
	"errors"
	"strings"
	"testing"

	"cornus/pkg/hostenv"
)

// fakeMapper answers from a fixed table; an absent path is not host-visible.
type fakeMapper struct {
	toHost      map[string]string
	propagation string
}

func (m fakeMapper) ToHost(p string) (string, bool) {
	h, ok := m.toHost[p]
	return h, ok
}

func (m fakeMapper) Propagation(string) string {
	if m.propagation == "" {
		return hostenv.PropagationShared
	}
	return m.propagation
}

// mapped is a mapper that knows the standard data dir.
func mapped() fakeMapper {
	return fakeMapper{toHost: map[string]string{"/var/lib/cornus": "/srv/cornus"}}
}

// unmapped is a mapper that knows nothing, i.e. the runtime sees none of our paths.
func unmapped() fakeMapper { return fakeMapper{toHost: map[string]string{}} }

func translating(runtime string) hostenv.Env {
	return hostenv.Env{
		InContainer: true, Translating: true, Runtime: runtime,
		SelfID:           "1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8",
		HostNetwork:      true,
		HostNetworkKnown: true,
	}
}

func input(backend string, env hostenv.Env, m hostenv.Mapper) Input {
	return Input{
		Backend: backend, DataDir: "/var/lib/cornus", MountsDir: "/var/lib/cornus/mounts",
		Env: env, Mapper: m,
		canMountLocal: func() error { return nil },
	}
}

func find(t *testing.T, r Result, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, r.Checks)
	return Check{}
}

func absent(t *testing.T, r Result, name string) {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			t.Errorf("unexpected %q check: %+v", name, c)
		}
	}
}

// The silent-corruption case: containerd hands the runtime a path under DataDir
// for every deploy, so an invisible DataDir must stop the server starting.
func TestContainerdUnmappedDataDirIsFatal(t *testing.T) {
	r := input("containerd", translating(hostenv.RuntimeContainerd), unmapped()).Run()
	c := find(t, r, CheckDataDir)
	if c.Status != StatusFail {
		t.Errorf("status = %q, want %q", c.Status, StatusFail)
	}
	if !r.Failed() {
		t.Error("Failed() = false; the server would start into a broken configuration")
	}
	if !strings.Contains(c.Hint, hostenv.HostPathMapEnv) || !strings.Contains(c.Hint, "-v ") {
		t.Errorf("hint names neither remedy: %q", c.Hint)
	}
}

// On dockerhost only client-local mounts depend on DataDir — volumes are
// daemon-managed — and that path reports its own failure at deploy time. Plain
// deploys work, so refusing to start would be wrong.
func TestDockerhostUnmappedDataDirIsNotFatal(t *testing.T) {
	r := input("", translating(hostenv.RuntimeDocker), unmapped()).Run()
	if c := find(t, r, CheckDataDir); c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
	if r.Failed() {
		t.Error("Failed() = true; dockerhost can still serve plain deploys")
	}
}

func TestMappedDataDirPasses(t *testing.T) {
	r := input("containerd", translating(hostenv.RuntimeContainerd), mapped()).Run()
	c := find(t, r, CheckDataDir)
	if c.Status != StatusOK {
		t.Errorf("status = %q, want %q", c.Status, StatusOK)
	}
	if !strings.Contains(c.Detail, "/srv/cornus") {
		t.Errorf("detail should name the host path: %q", c.Detail)
	}
	if r.Failed() {
		t.Error("Failed() = true on a fully mapped configuration")
	}
}

// Without shared propagation the 9P mount succeeds and stays invisible, and the
// runtime binds the empty directory underneath. That is a lost capability, not
// a corrupted deploy, so it warns.
func TestPropagationWarnsWhenNotShared(t *testing.T) {
	m := mapped()
	m.propagation = hostenv.PropagationPrivate
	r := input("", translating(hostenv.RuntimeDocker), m).Run()
	c := find(t, r, CheckPropagation)
	if c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
	if !strings.Contains(c.Hint, "rshared") {
		t.Errorf("hint should name rshared: %q", c.Hint)
	}
	if r.Failed() {
		t.Error("Failed() = true for a merely unavailable capability")
	}
}

func TestPropagationUnknownWarns(t *testing.T) {
	m := mapped()
	m.propagation = hostenv.PropagationUnknown
	r := input("", translating(hostenv.RuntimeDocker), m).Run()
	if c := find(t, r, CheckPropagation); c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
}

// containerd realizes client-local mounts through a companion, never through
// the server's own mounts dir, so its propagation is irrelevant there.
func TestPropagationSkippedForContainerd(t *testing.T) {
	m := mapped()
	m.propagation = hostenv.PropagationPrivate
	r := input("containerd", translating(hostenv.RuntimeContainerd), m).Run()
	absent(t, r, CheckPropagation)
	absent(t, r, CheckClientMounts)
}

func TestClientMountsWarnWhenUnavailable(t *testing.T) {
	in := input("", hostenv.Env{}, mapped())
	in.canMountLocal = func() error { return errors.New("9p kernel module not loaded") }
	r := in.Run()
	c := find(t, r, CheckClientMounts)
	if c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
	if !strings.Contains(c.Detail, "9p kernel module") {
		t.Errorf("detail should carry the underlying reason: %q", c.Detail)
	}
}

// TestHostNetworkSeverity pins the severity table for the netns question.
//
// The deferral this used to assert ("never fatal however it comes out") is
// resolved: containerd self-inspection can now PROVE an isolated netns, and a
// proven one is fatal, because a published port that silently exists nowhere the
// host can reach is the same class of quiet wrongness as a bind mount resolving to
// an empty directory.
//
// The three non-fatal rows are the point of the table, not filler. Each is a
// configuration that must keep starting: host networking works; an operator who
// DECLARED the isolated netns has already acknowledged it; and "cannot tell" is
// where every backend without a daemon to ask (bare) and the all-in-one E2E runner
// land, so failing there would reject setups that demonstrably work.
func TestHostNetworkSeverity(t *testing.T) {
	for name, tc := range map[string]struct {
		known, host, declared bool
		want                  Status
		wantFatal             bool
	}{
		"host netns":            {known: true, host: true, want: StatusOK},
		"own netns, discovered": {known: true, host: false, want: StatusFail, wantFatal: true},
		"own netns, declared":   {known: true, host: false, declared: true, want: StatusWarn},
		"cannot tell":           {known: false, host: false, want: StatusWarn},
	} {
		env := translating(hostenv.RuntimeContainerd)
		env.HostNetworkKnown, env.HostNetwork, env.HostNetworkDeclared = tc.known, tc.host, tc.declared
		r := input("containerd", env, mapped()).Run()
		if c := find(t, r, CheckHostNetwork); c.Status != tc.want {
			t.Errorf("%s: status = %q, want %q", name, c.Status, tc.want)
		}
		if got := r.Failed(); got != tc.wantFatal {
			t.Errorf("%s: Failed() = %v, want %v", name, got, tc.wantFatal)
		}
	}
}

// dockerhost's networking is the daemon's, not cornus's netns, so this check does
// not apply to it — the equivalent hazard there is handled inside the backend
// (dockerhost self-attaches to the workload's network).
func TestHostNetworkSkippedForDockerhost(t *testing.T) {
	env := translating(hostenv.RuntimeDocker)
	env.HostNetwork = false
	absent(t, input("", env, mapped()).Run(), CheckHostNetwork)
}

// TestHostNetworkCheckedWithoutPathTranslation pins that the netns question is
// asked independently of the path question.
//
// The check used to hang off the Translating branch, which made it unreachable in
// exactly the setup the guide recommends: bind the data dir at the SAME path on
// both sides and no CORNUS_HOST_PATH_MAP is needed, so nothing translates, so a
// containerized containerd server was told nothing at all about its network
// namespace. The two have no bearing on each other — CNI builds the bridge in
// cornus's netns whatever its paths mean.
func TestHostNetworkCheckedWithoutPathTranslation(t *testing.T) {
	env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeContainerd}
	for _, backend := range []string{"containerd", "bare"} {
		r := input(backend, env, mapped()).Run()
		c := find(t, r, CheckHostNetwork)
		if c.Status != StatusWarn {
			t.Errorf("%s: status = %q, want %q", backend, c.Status, StatusWarn)
		}
	}
}

// TestHostNetworkCheckedForBare pins that the check covers the OTHER backend
// built on the same CNI manager.
//
// bare and containerd share pkg/deploy/internal/hostrun's CNIManager, so both
// build the bridge, the IPAM and the portmap DNAT in cornus's own netns. Only
// containerd was ever checked. bare was the better-hidden of the two: it is
// excused from every other check here by sharesMountNamespace (its OCI runtime is
// cornus's own child, so its paths cannot diverge), which meant a containerized
// bare server produced NO host-environment output whatsoever while silently
// DNAT'ing its published ports inside its own container.
//
// The detail has to name that consequence, because it is the part an operator can
// neither see nor guess: the deploy succeeds, the port is reported as published,
// and connecting to it on the host is refused.
// The two states bare can actually be in are the ones asserted here. bare has no
// daemon to self-inspect, so it can never reach the DISCOVERED-isolated branch:
// its netns answer is either unknown, or whatever the operator declared with
// CORNUS_HOST_NETWORK. Neither is fatal, which is why the new fatal branch above
// is containerd-only in practice — and pinning that here is what would catch a
// future change that started failing bare servers on a question bare cannot even
// answer.
func TestHostNetworkCheckedForBare(t *testing.T) {
	t.Run("declared isolated", func(t *testing.T) {
		env := hostenv.Env{InContainer: true, HostNetworkKnown: true, HostNetwork: false, HostNetworkDeclared: true}
		r := input("bare", env, mapped()).Run()
		c := find(t, r, CheckHostNetwork)
		if c.Status != StatusWarn {
			t.Fatalf("status = %q, want %q", c.Status, StatusWarn)
		}
		if !strings.Contains(c.Detail, "published ports") {
			t.Errorf("detail must name the published-port consequence, got %q", c.Detail)
		}
		if r.Failed() {
			t.Error("Failed() = true: a declared topology is acknowledged, not refused")
		}
	})
	t.Run("cannot tell", func(t *testing.T) {
		env := hostenv.Env{InContainer: true}
		r := input("bare", env, mapped()).Run()
		if c := find(t, r, CheckHostNetwork); c.Status != StatusWarn {
			t.Errorf("status = %q, want %q", c.Status, StatusWarn)
		}
		if r.Failed() {
			t.Error("Failed() = true: bare has no daemon to ask, so it must not be failed for not knowing")
		}
	})
}

// A server on the host has no netns question to answer, so the check must not
// fire there and hand every ordinary bare/containerd operator a warning about a
// container they are not in.
func TestHostNetworkSkippedOnTheHost(t *testing.T) {
	for _, backend := range []string{"containerd", "bare"} {
		absent(t, input(backend, hostenv.Env{}, mapped()).Run(), CheckHostNetwork)
	}
}

// incus is the third non-docker host backend and the check must NOT reach it: its
// instances are networked by incusd on its own bridge, so cornus's netns has no
// bearing on where the workload network gets built. (The consequence for a
// containerized incus server — no route to that bridge — is reported by the
// backend at the point of use, as incushost.WithIsolatedNetwork.)
func TestHostNetworkSkippedForIncus(t *testing.T) {
	env := hostenv.Env{InContainer: true, HostNetworkKnown: true, HostNetwork: false}
	absent(t, input("incus", env, mapped()).Run(), CheckHostNetwork)
}

// kubernetes realizes everything through the pod caretaker and never resolves
// the server's own paths, so none of these checks apply to it.
func TestKubernetesSkipsHostChecks(t *testing.T) {
	for _, name := range []string{"kubernetes", "k8s"} {
		r := input(name, translating(hostenv.RuntimeDocker), unmapped()).Run()
		if len(r.Checks) != 1 || r.Checks[0].Name != CheckColocation {
			t.Errorf("%s: checks = %+v, want only the colocation note", name, r.Checks)
		}
		if r.Failed() {
			t.Errorf("%s: Failed() = true", name)
		}
	}
}

// bare execs the OCI runtime as cornus's own child, so the workload inherits
// cornus's mount namespace: a containerized bare server runs its workloads
// inside that same container and needs no host-visible data dir. Failing it for
// an unmapped DataDir would refuse to start a configuration that works.
func TestBareSkipsPathChecks(t *testing.T) {
	m := unmapped()
	m.propagation = hostenv.PropagationPrivate
	r := input("bare", translating(hostenv.RuntimeDocker), m).Run()
	absent(t, r, CheckDataDir)
	absent(t, r, CheckPropagation)
	if r.Failed() {
		t.Error("Failed() = true; bare's runtime shares this process's mount namespace")
	}
	// It still performs the kernel 9P mount itself, so that capability is checked.
	if c := find(t, r, CheckClientMounts); c.Status != StatusOK {
		t.Errorf("client-mount check = %q, want it to still run", c.Status)
	}
}

// A cornus sharing its runtime's mount namespace needs no translation, so the
// path checks must not run — this is the co-located containerized shape (the
// E2E runner) that must keep working untouched. It still warns that the
// assumption was never verified, since the alternative reading of the same
// evidence is a host runtime whose paths silently differ.
func TestCoLocatedSkipsPathChecks(t *testing.T) {
	env := hostenv.Env{InContainer: true, Translating: false, Runtime: hostenv.RuntimeDocker}
	r := input("", env, unmapped()).Run()
	absent(t, r, CheckDataDir)
	absent(t, r, CheckPropagation)
	if c := find(t, r, CheckSelfInspection); c.Status != StatusWarn {
		t.Errorf("self-inspection check = %q, want a warning", c.Status)
	}
	if r.Failed() {
		t.Error("Failed() = true for a co-located containerized cornus")
	}
	if !strings.Contains(find(t, r, CheckColocation).Detail, "co-located") {
		t.Errorf("summary should say co-located: %q", r.Summary())
	}
}

func TestSummary(t *testing.T) {
	for name, tc := range map[string]struct {
		env  hostenv.Env
		want string
	}{
		"host":                       {hostenv.Env{}, "runs on the host; runtime paths need no translation"},
		"co-located":                 {hostenv.Env{InContainer: true}, "co-located with its runtime"},
		"translating in a container": {translating(hostenv.RuntimeDocker), "translating its paths"},
		// An operator-declared map makes a HOST process translate too; saying
		// "needs no translation" there would contradict what is happening.
		"translating on the host": {
			hostenv.Env{Translating: true, Runtime: hostenv.RuntimeDocker},
			"cornus runs on the host; translating its paths for the runtime",
		},
		// An unknown runtime must not render as "on a unknown host".
		"unknown runtime": {
			hostenv.Env{InContainer: true, Translating: true, Runtime: hostenv.RuntimeUnknown},
			"cornus runs in a container; translating",
		},
	} {
		got := Result{Env: tc.env}.Summary()
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: Summary() = %q, want it to contain %q", name, got, tc.want)
		}
	}
	// The short id is what an operator matches against `docker ps`.
	if got := (Result{Env: translating(hostenv.RuntimeDocker)}).Summary(); !strings.Contains(got, "1f0a2b3c4d5e)") {
		t.Errorf("Summary() should carry the short container id: %q", got)
	}
}

func TestProblemsOrdersFailsFirst(t *testing.T) {
	r := Result{Checks: []Check{
		{Name: "a", Status: StatusOK},
		{Name: "b", Status: StatusWarn},
		{Name: "c", Status: StatusFail},
	}}
	got := r.Problems()
	if len(got) != 2 || got[0].Name != "c" || got[1].Name != "b" {
		t.Errorf("Problems() = %+v, want the fail first then the warn", got)
	}
}

func TestNormalizeBackend(t *testing.T) {
	for in, want := range map[string]string{
		"":           backendDockerhost,
		"dockerhost": backendDockerhost,
		"k8s":        backendKubernetes,
		"kubernetes": backendKubernetes,
		"containerd": backendContainerd,
		"bare":       backendBare,
		"incus":      backendIncus,
		"nonsense":   backendDockerhost,
	} {
		if got := normalizeBackend(in); got != want {
			t.Errorf("normalizeBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

// The gap hand-testing found: the containerd backend cannot be asked which
// container it runs in, so an unmapped data dir falls back to the identity —
// exactly where an operator most needs to be told, and previously the one case
// that produced no output at all.
func TestContainerizedWithNoMappingWarns(t *testing.T) {
	env := hostenv.Env{InContainer: true, Translating: false, Runtime: hostenv.RuntimeContainerd}
	r := input("containerd", env, unmapped()).Run()
	c := find(t, r, CheckSelfInspection)
	if c.Status != StatusWarn {
		t.Errorf("status = %q, want %q", c.Status, StatusWarn)
	}
	if !strings.Contains(c.Hint, hostenv.HostPathMapEnv) {
		t.Errorf("hint must name the knob that settles it: %q", c.Hint)
	}
	// Still not fatal: the same evidence is produced by a legitimate
	// runtime-in-this-container setup.
	if r.Failed() {
		t.Error("Failed() = true; the co-located reading of this evidence is valid")
	}
}

// A non-containerized server has nothing to be unsure about.
func TestHostServerDoesNotWarnAboutUnverifiedPaths(t *testing.T) {
	absent(t, input("containerd", hostenv.Env{}, unmapped()).Run(), CheckSelfInspection)
}

// bare's runtime is cornus's own child, so there is nothing to verify even in a
// container.
func TestBareDoesNotWarnAboutUnverifiedPaths(t *testing.T) {
	env := hostenv.Env{InContainer: true, Runtime: hostenv.RuntimeDocker}
	absent(t, input("bare", env, unmapped()).Run(), CheckSelfInspection)
}
