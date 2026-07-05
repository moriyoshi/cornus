package server

import (
	"context"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// A backend whose id map only exists once its container does
// (deploy.LateIDCredentialBinder) needs two things the ordinary path cannot give
// it: the server must not ask for host ids BEFORE Apply, and Apply must hand over
// the directories to be owned.
//
// Both are pinned here because neither is visible in a passing deploy. The first
// failure mode is an error at deploy time; the second is silent — the files stay
// owned by the server, the deploy succeeds, and the workload fails later on its
// own permission error, which is the exact shape this whole facility exists to
// prevent.

// lateBackend records which Apply the server chose.
type lateBackend struct {
	fileCredBackend
	ownedDirs [][]string
	plain     int
	idMapErr  error
}

func (l *lateBackend) Name() string { return "incus" }

// IDMap fails the way incus does before Apply: there is no instance to read the
// map from. This is what made the ordinary pre-Apply resolution ERROR rather
// than merely answer badly.
func (l *lateBackend) IDMap(context.Context, string) (deploy.IDMap, error) {
	return nil, l.idMapErr
}

func (l *lateBackend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	l.plain++
	return api.DeployStatus{Name: spec.Name}, nil
}

func (l *lateBackend) ApplyOwningCredentialDirs(ctx context.Context, spec api.DeploySpec, dirs []string) (api.DeployStatus, error) {
	l.ownedDirs = append(l.ownedDirs, dirs)
	return api.DeployStatus{Name: spec.Name}, nil
}

// TestApplyRoutesCredentialDirsToALateBinder: with directories to own, a late
// binder must be given them. Falling through to plain Apply is the silent
// failure — everything succeeds and the credential is unreadable inside.
func TestApplyRoutesCredentialDirsToALateBinder(t *testing.T) {
	b := &lateBackend{}
	dirs := []string{"/var/lib/cornus/mounts/creds-abc/0"}
	if _, err := applyOwningCredentials(context.Background(), b, api.DeploySpec{Name: "web"}, dirs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(b.ownedDirs) != 1 {
		t.Fatalf("late binder was not asked to own the credential directories (plain Apply calls=%d): "+
			"the files stay owned by the server and the workload cannot read them", b.plain)
	}
	if got := b.ownedDirs[0]; len(got) != 1 || got[0] != dirs[0] {
		t.Fatalf("dirs handed over = %v, want %v", got, dirs)
	}
}

// TestPlainApplyWhenThereIsNothingToOwn: the late path exists for credentials
// alone. A deploy with none must take the ordinary route, or every deploy on this
// backend goes down a path built for a case it is not in.
func TestPlainApplyWhenThereIsNothingToOwn(t *testing.T) {
	b := &lateBackend{}
	if _, err := applyOwningCredentials(context.Background(), b, api.DeploySpec{Name: "web"}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if b.plain != 1 || len(b.ownedDirs) != 0 {
		t.Fatalf("plain=%d owned=%d, want 1 and 0", b.plain, len(b.ownedDirs))
	}
}

// TestNonLateBackendIsUnaffected: a backend that resolves its map up front keeps
// the ordinary Apply even with credential directories present, because its files
// were already written with the right ids.
func TestNonLateBackendIsUnaffected(t *testing.T) {
	b := &countingCredBackend{}
	if _, err := applyOwningCredentials(context.Background(), b, api.DeploySpec{Name: "web"}, []string{"/x"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if b.applied != 1 {
		t.Fatalf("plain Apply calls = %d, want 1", b.applied)
	}
}

type countingCredBackend struct {
	fileCredBackend
	applied int
}

func (c *countingCredBackend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	c.applied++
	return api.DeployStatus{Name: spec.Name}, nil
}

// TestInitialOwnerDoesNotAskALateBinder is the regression for the error that
// blocked this feature: resolving host ids before Apply asks the backend for a
// map that lives on a container which does not exist, and incus answers with an
// error rather than a fallback. The first write therefore uses CONTAINER-side
// ids, and the backend corrects them after create.
func TestInitialOwnerDoesNotAskALateBinder(t *testing.T) {
	b := &lateBackend{idMapErr: errNoInstanceYet}
	uid, gid, translated, err := initialCredentialFileOwner(context.Background(),
		api.DeploySpec{Name: "web", User: "1000"}, b)
	if err != nil {
		t.Fatalf("initialCredentialFileOwner errored for a late binder: %v — this is the failure "+
			"that made file delivery impossible on this backend", err)
	}
	if uid != 1000 || gid != 1000 {
		t.Fatalf("initial owner = %d:%d, want the CONTAINER-side 1000:1000 (the backend translates "+
			"once the instance exists)", uid, gid)
	}
	if !translated {
		t.Fatal("translated must be true for a late binder: it drives making the server's tree " +
			"traversable, and the question that answers is whether a REMAPPING runtime will walk it")
	}
}

// TestInitialOwnerStillAsksAnOrdinaryBackend: the exemption is for late binders
// only. A backend whose map is knowable must still be asked, or its files are
// written with untranslated ids and silently unreadable.
func TestInitialOwnerStillAsksAnOrdinaryBackend(t *testing.T) {
	b := &mappingCredBackend{}
	uid, _, translated, err := initialCredentialFileOwner(context.Background(),
		api.DeploySpec{Name: "web", User: "1000"}, b)
	if err != nil {
		t.Fatalf("initialCredentialFileOwner: %v", err)
	}
	if uid != 1001000 || !translated {
		t.Fatalf("owner = %d (translated=%v), want 1001000 translated: an ordinary backend's map is "+
			"knowable now and must be used", uid, translated)
	}
}

type mappingCredBackend struct{ fileCredBackend }

func (*mappingCredBackend) IDMap(context.Context, string) (deploy.IDMap, error) {
	return deploy.IDMap{{ContainerID: 0, HostID: 1000000, Count: 65536, UIDs: true, GIDs: true}}, nil
}

var errNoInstanceYet = errNoInstance{}

type errNoInstance struct{}

func (errNoInstance) Error() string { return "incus: instance cornus-web-0 not found" }
