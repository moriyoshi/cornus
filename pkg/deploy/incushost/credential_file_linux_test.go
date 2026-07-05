//go:build linux

package incushost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// The create/start split this file's feature depends on removed an assertion
// that instances are created with Start:true. This is the replacement contract,
// and it is the one that was always worth holding: the workload ENDS UP RUNNING.
// Asserting the flag only ever stood in for that.
func TestApplyStartsEveryInstance(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	st, err := b.Apply(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1", Replicas: 3,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(st.Instances) != 3 {
		t.Fatalf("instances = %d, want 3", len(st.Instances))
	}
	for _, in := range st.Instances {
		if !in.Running {
			t.Fatalf("instance %s is not running after Apply: the create/start split left it "+
				"stopped, so a deploy silently produces a workload that never starts", in.ID)
		}
	}
}

// credSpecAndDir builds a deployment plus a server-written credential directory,
// owned by root as the server itself would leave it.
func credSpecAndDir(t *testing.T, user string) (api.DeploySpec, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("s3cr3t"), 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	return api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", User: user}, dir
}

// chownRecorder captures the ownership changes ownCredentialDirs decides on,
// so the ids and the paths can be asserted on any host. The first version of
// these two tests called the real syscall and skipped unless root — so on an
// ordinary machine the only tests covering the feature never ran, and the package
// still reported ok.
type chownRecorder struct{ calls map[string][2]int }

func newChownRecorder() *chownRecorder { return &chownRecorder{calls: map[string][2]int{}} }

func (c *chownRecorder) fn(path string, uid, gid int) error {
	c.calls[path] = [2]int{uid, gid}
	return nil
}

// stoppedInstance seeds a created-but-not-started instance carrying raw as its
// volatile.idmap.next — the state Apply calls ownCredentialDirs in.
func stoppedInstance(f *fakeConn, name, raw string) {
	f.insts[name] = &incusapi.Instance{
		Name:        name,
		InstancePut: incusapi.InstancePut{Config: map[string]string{idmapNextConfigKey: raw}},
	}
}

// TestOwnCredentialDirsChownsIntoTheInstanceRange is the feature: a file the
// server wrote as ITSELF is handed to a workload that runs behind a user
// namespace, and must end up owned by the host ids that workload's own uid maps
// to. Owning it as the server (uid 0) leaves it unreadable inside, which is
// silent — the deploy succeeds and the application fails later.
//
// The DIRECTORY is asserted alongside the file because the measured failure on
// the other remapping runtime was `statfs` on the directory, before any file was
// opened; a chown reaching the files but not their parent reproduces it.
func TestOwnCredentialDirsChownsIntoTheInstanceRange(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	rec := newChownRecorder()
	b.chown = rec.fn
	spec, dir := credSpecAndDir(t, "1000")
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	stoppedInstance(f, instanceName("web", 0), idmapRangesFixture)

	if err := b.ownCredentialDirs(context.Background(), spec, []string{dir}, 1); err != nil {
		t.Fatalf("ownCredentialDirs: %v", err)
	}
	// uid 1000 inside, range base 1000000 -> host 1001000. Owning it as the range
	// BASE (1000000, i.e. container root) is exactly as unreadable to a uid-1000
	// workload as leaving it owned by the server.
	for _, path := range []string{dir, filepath.Join(dir, "token"), sub, filepath.Join(sub, "inner")} {
		got, ok := rec.calls[path]
		if !ok {
			t.Fatalf("%s was never owned: a credential tree the workload cannot traverse is "+
				"as unreadable as one it cannot open", path)
		}
		if got != [2]int{1001000, 1001000} {
			t.Fatalf("%s owned as %v, want [1001000 1001000]", path, got)
		}
	}
}

// TestOwnCredentialDirsIsANoOpWithoutDirs: a plain Apply must behave exactly as
// before. Without this, the "one ordering carries every deploy" claim rests on
// nothing.
func TestOwnCredentialDirsIsANoOpWithoutDirs(t *testing.T) {
	b := testBackend(newFakeConn())
	spec, _ := credSpecAndDir(t, "1000")
	if err := b.ownCredentialDirs(context.Background(), spec, nil, 1); err != nil {
		t.Fatalf("no directories must be a no-op, got: %v", err)
	}
}

// TestOwnCredentialDirsRefusesDisagreeingReplicas pins the case
// security.idmap.isolated=true creates: one host directory is bind-mounted into
// every replica, so it carries ONE ownership. If the replicas were given
// different ranges, delivering replica 0's would leave the others with a file
// they cannot read — deploy succeeds, one replica works, the rest fail later on
// their own permission error.
func TestOwnCredentialDirsRefusesDisagreeingReplicas(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	// Injected so the refusal is what fails this test. Without it the real syscall
	// runs, and on an unprivileged host a missing check surfaces as `lchown:
	// operation not permitted` — a failure, but for the wrong reason, and one that
	// would vanish when the suite runs as root.
	b.chown = newChownRecorder().fn
	spec, dir := credSpecAndDir(t, "1000")
	stoppedInstance(f, instanceName("web", 0), idmapRangesFixture)
	// Replica 1 on a DIFFERENT base, as isolated id maps allocate.
	stoppedInstance(f, instanceName("web", 1), `[{"Isuid":true,"Isgid":true,"Hostid":2000000,"Nsid":0,"Maprange":65536}]`)
	err := b.ownCredentialDirs(context.Background(), spec, []string{dir}, 2)
	if err == nil {
		t.Fatal("replicas with different id ranges were accepted: one host directory cannot be " +
			"readable by both, so every replica but the first gets an unreadable credential")
	}
	if !strings.Contains(err.Error(), "isolated") {
		t.Fatalf("refusal does not name the cause an operator can act on: %v", err)
	}
}

// TestOwnCredentialDirsRefusesANamedUser: a name lives in the image's
// /etc/passwd, which this backend never reads. Guessing root is precisely the
// silently-unreadable case, so it must refuse rather than deliver.
func TestOwnCredentialDirsRefusesANamedUser(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	b.chown = newChownRecorder().fn
	spec, dir := credSpecAndDir(t, "app")
	stoppedInstance(f, instanceName("web", 0), idmapRangesFixture)
	if err := b.ownCredentialDirs(context.Background(), spec, []string{dir}, 1); err == nil {
		t.Fatal("a named user was accepted; the uid it resolves to is not knowable here, so the " +
			"files would be owned by a guess")
	}
}

// TestBackendDeclaresFileDelivery pins the capability itself.
//
// It was FALSE and nothing in the suite said so, which is why flipping it to true
// broke no test — the refusal that shaped this backend's credential behaviour for
// months was pinned by nothing at all. Both halves are asserted: the declaration
// a caller reads, and the interface that makes it true.
func TestBackendDeclaresFileDelivery(t *testing.T) {
	b := testBackend(newFakeConn())
	if !b.BindsCredentialDir(context.Background()) {
		t.Fatal("BindsCredentialDir is false: file deliveries go back to a caretaker, and this " +
			"backend's companion is a sibling instance that cannot carry mounts — so they are refused")
	}
	var _ deploy.LateIDCredentialBinder = b
}
