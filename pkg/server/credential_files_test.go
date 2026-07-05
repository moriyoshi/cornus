package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
)

// newCredFiles builds a materializer over a temp dir with one group, the shape
// prepareCredentialFiles produces for a single credential directory.
func newCredFiles(t *testing.T, containerDir string, files ...deploy.CredentialFile) *credentialFiles {
	t.Helper()
	root := t.TempDir()
	host := filepath.Join(root, "0")
	return &credentialFiles{
		dir:    root,
		mounts: []api.Mount{{Source: host, Target: containerDir, ReadOnly: true}},
		groups: []credFileGroup{{containerDir: containerDir, hostDir: host, files: files}},
	}
}

// readThroughLinks reads the path a workload would open — the per-file symlink,
// which resolves through ..data into the live version directory. Reading the
// version dir directly would test the wrong thing: the workload never names it.
func readThroughLinks(t *testing.T, cf *credentialFiles, base string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cf.groups[0].hostDir, base))
	if err != nil {
		t.Fatalf("read %s through its symlink: %v", base, err)
	}
	return string(b)
}

// TestCredentialFilesLayoutResolvesThroughDataLink pins the shape a workload
// sees: a plain filename it can open, resolving through ..data. If the per-file
// entry were a regular file instead of a symlink, refresh would have to overwrite
// it in place and could be observed half-written.
func TestCredentialFilesLayoutResolvesThroughDataLink(t *testing.T) {
	cf := newCredFiles(t, "/creds", deploy.CredentialFile{
		Path: "/creds/db.json", Content: []byte(`{"v":1}`), Mode: credFileMode,
	})
	if err := cf.write(context.Background()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readThroughLinks(t, cf, "db.json"); got != `{"v":1}` {
		t.Errorf("content = %q, want the rendered credential", got)
	}

	link, err := os.Readlink(filepath.Join(cf.groups[0].hostDir, "db.json"))
	if err != nil {
		t.Fatalf("db.json must be a symlink so a refresh can swap it atomically: %v", err)
	}
	if link != filepath.Join(dataLink, "db.json") {
		t.Errorf("db.json -> %q, want it to point through %s", link, dataLink)
	}
	if _, err := os.Readlink(filepath.Join(cf.groups[0].hostDir, dataLink)); err != nil {
		t.Errorf("%s must be a symlink: %v", dataLink, err)
	}
}

// TestCredentialFilesRefreshSwapsAtomically is the reason for the whole layout.
// A bind pins an inode, so a refresh that replaced the file by rename would leave
// the workload reading the dead inode forever. Swapping ..data instead means the
// SAME path resolves to new content, with the per-file symlink untouched.
func TestCredentialFilesRefreshSwapsAtomically(t *testing.T) {
	cf := newCredFiles(t, "/creds", deploy.CredentialFile{
		Path: "/creds/db.json", Content: []byte("v1"), Mode: credFileMode,
	})
	ctx := context.Background()
	if err := cf.write(ctx); err != nil {
		t.Fatalf("write: %v", err)
	}
	firstLink, _ := os.Readlink(filepath.Join(cf.groups[0].hostDir, "db.json"))
	firstData, _ := os.Readlink(filepath.Join(cf.groups[0].hostDir, dataLink))

	cf.groups[0].files[0].Content = []byte("v2")
	if err := cf.write(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := readThroughLinks(t, cf, "db.json"); got != "v2" {
		t.Errorf("after refresh content = %q, want v2 — the workload's path must resolve to the new value", got)
	}
	if link, _ := os.Readlink(filepath.Join(cf.groups[0].hostDir, "db.json")); link != firstLink {
		t.Errorf("the per-file symlink moved (%q -> %q); only %s may change", firstLink, link, dataLink)
	}
	nowData, _ := os.Readlink(filepath.Join(cf.groups[0].hostDir, dataLink))
	if nowData == firstData {
		t.Errorf("%s still points at %q; the refresh did not swap versions", dataLink, nowData)
	}
	// The superseded version is reclaimed, or every refresh leaks a directory for
	// the life of the session.
	if _, err := os.Stat(filepath.Join(cf.groups[0].hostDir, firstData)); !os.IsNotExist(err) {
		t.Errorf("superseded version dir %s survived the refresh", firstData)
	}
}

// TestCredentialFilesHideVersionDirsFromTheWorkload pins that a workload listing
// its credential directory sees the credentials and nothing else. The dot-dot
// prefix is what does it, and an application that iterates the directory (an
// aws-credentials profile scan, say) would otherwise trip over the machinery.
func TestCredentialFilesHideVersionDirsFromTheWorkload(t *testing.T) {
	cf := newCredFiles(t, "/creds",
		deploy.CredentialFile{Path: "/creds/a.json", Content: []byte("a"), Mode: credFileMode},
		deploy.CredentialFile{Path: "/creds/b.json", Content: []byte("b"), Mode: credFileMode},
	)
	if err := cf.write(context.Background()); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(cf.groups[0].hostDir)
	if err != nil {
		t.Fatal(err)
	}
	var visible []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "..") {
			visible = append(visible, e.Name())
		}
	}
	if len(visible) != 2 || visible[0] != "a.json" || visible[1] != "b.json" {
		t.Errorf("workload-visible entries = %v, want exactly the two credential files", visible)
	}
}

// TestCredentialFilesMode pins 0600. It matches creddelivery.WriteFile, which the
// caretaker uses for the same delivery on kubernetes — the two must not disagree
// about how exposed a credential file is because a different process wrote it.
func TestCredentialFilesMode(t *testing.T) {
	cf := newCredFiles(t, "/creds", deploy.CredentialFile{
		Path: "/creds/db.json", Content: []byte("x"), Mode: credFileMode,
	})
	if err := cf.write(context.Background()); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := os.Stat(filepath.Join(cf.groups[0].hostDir, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	// The directories must stay traversable, or a 0600 file inside them is
	// unreachable no matter who owns it.
	dst, err := os.Stat(cf.groups[0].hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if dst.Mode().Perm()&0o111 == 0 {
		t.Errorf("credential dir mode = %v, want the execute bit so the workload can traverse it", dst.Mode().Perm())
	}
}

// TestRenderCredentialFilesRejectsUnusablePaths keeps a malformed spec from
// producing a file somewhere unintended. A relative path would resolve against
// the server's working directory, which is not the container's.
func TestRenderCredentialFilesRejectsUnusablePaths(t *testing.T) {
	cred := credential.Credential{Values: map[string]string{"token": "t"}}
	for _, tc := range []struct{ name, path, want string }{
		{"empty", "", "no path"},
		{"relative", "creds/db.json", "must be absolute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderCredentialFiles("db", cred, []api.CredentialDelivery{{Kind: "file", Path: tc.path}}, 0, 0)
			if err == nil {
				t.Fatalf("path %q should be refused", tc.path)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestSpecCaretakerKindsFileFollowsTheCapability is the routing decision in one
// assertion: the same spec needs a caretaker on a backend that cannot bind a
// server path, and needs none on one that can. Getting this backwards either
// demands CORNUS_ADVERTISE_URL for nothing, or silently drops the credential.
func TestSpecCaretakerKindsFileFollowsTheCapability(t *testing.T) {
	spec := api.DeploySpec{Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
		Name: "db", Deliveries: []api.CredentialDelivery{{Kind: "file", Path: "/creds/db.json"}},
	}}}}
	if got := deploy.SpecCaretakerKinds(spec, deploy.ServerDelivers{}); len(got) != 1 || got[0] != "file" {
		t.Errorf("without the capability, kinds = %v, want [file] — it must go to a caretaker", got)
	}
	if got := deploy.SpecCaretakerKinds(spec, deploy.ServerDelivers{Files: true}); got != nil {
		t.Errorf("with the capability, kinds = %v, want none — the server materializes it", got)
	}
}

// remapBackend reports an id map like a runtime that remaps (rootless podman,
// incus): container root -> the daemon's user, everything above -> a subuid
// range. Shaped after the real podman /info reply.
type remapBackend struct {
	*fakeBackend
	m deploy.IDMap
}

func (r *remapBackend) IDMap(context.Context, string) (deploy.IDMap, error) { return r.m, nil }

var podmanLikeMap = deploy.IDMap{
	{ContainerID: 0, HostID: 1001, Count: 1, UIDs: true, GIDs: true},
	{ContainerID: 1, HostID: 100000, Count: 65536, UIDs: true, GIDs: true},
}

// TestCredentialFileOwnerIsTranslatedForARemappingRuntime is the point of the
// whole facility.
//
// spec.User names a CONTAINER-side id. On a runtime that remaps, chowning to
// that number produces a file owned by an id the workload's namespace does not
// map — reported as 65534, the overflow uid — which no mode bit can rescue,
// because a userns root holds CAP_DAC_OVERRIDE only over ids inside its map.
//
// Note the expected value: a workload running as 1000 needs 100999, NOT the
// range base. Owning it as container root is exactly as unreadable to that
// workload as leaving it unmapped.
func TestCredentialFileOwnerIsTranslatedForARemappingRuntime(t *testing.T) {
	spec := api.DeploySpec{Name: "web", User: "1000:1000"}
	uid, gid, _, err := credentialFileHostOwner(context.Background(), spec, &remapBackend{fakeBackend: &fakeBackend{}, m: podmanLikeMap})
	if err != nil {
		t.Fatalf("credentialFileHostOwner: %v", err)
	}
	if uid != 100999 || gid != 100999 {
		t.Fatalf("host owner = %d:%d, want 100999:100999 — the container-side 1000 translated "+
			"through the runtime's map", uid, gid)
	}
}

// TestCredentialFileOwnerIsUntouchedWithoutAMap: the backends that do not remap
// (rootful dockerhost, containerd, bare) must be completely unaffected, or this
// change would break the three that already worked.
func TestCredentialFileOwnerIsUntouchedWithoutAMap(t *testing.T) {
	spec := api.DeploySpec{Name: "web", User: "1000:1000"}
	uid, gid, _, err := credentialFileHostOwner(context.Background(), spec, &fakeBackend{})
	if err != nil {
		t.Fatalf("credentialFileHostOwner: %v", err)
	}
	if uid != 1000 || gid != 1000 {
		t.Fatalf("host owner = %d:%d on a backend with no id map, want the container ids unchanged", uid, gid)
	}
}

// TestCredentialFileOwnerRefusesAnUnmappableUser is the half that stops this
// re-creating the bug. Falling back to the container-side number for an id the
// runtime does not map writes an unreadable file and reports success.
func TestCredentialFileOwnerRefusesAnUnmappableUser(t *testing.T) {
	spec := api.DeploySpec{Name: "web", User: "70000:70000"} // past the subuid range
	if _, _, _, err := credentialFileHostOwner(context.Background(), spec, &remapBackend{fakeBackend: &fakeBackend{}, m: podmanLikeMap}); err == nil {
		t.Fatal("an unmappable user resolved anyway; the file would be written owned by an id " +
			"the workload cannot see as its own, and the deploy would report success")
	}
}
