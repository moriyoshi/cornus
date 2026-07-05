//go:build linux

package hostrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// traversals are the names that used to escape the volume store. filepath.Join
// CLEANS its result, so each of these resolved to a directory OUTSIDE the store —
// "../../../../../../etc" landed on "/etc" exactly.
var traversals = []string{
	"..",
	"../etc",
	"../../../../../../etc",
	"a/../../../../../../root",
	".ssh/../../../../../../etc/shadow",
	"/etc",
	"/",
	"sub/dir",
	"./cache",
	"",
}

// TestNamedVolumeDirRejectsTraversal is the regression test for a path-traversal
// that gave anyone who could reach the deploy API an arbitrary host bind mount on
// containerd and bare: spec.Volumes is never seen by pkg/deploy/hostpolicy, so a
// volume named "../../../../../../etc" bound the host's /etc read-write into a
// container.
func TestNamedVolumeDirRejectsTraversal(t *testing.T) {
	s := NewVolumeStore(t.TempDir(), "containerd", "containerd")
	for _, name := range traversals {
		t.Run(name, func(t *testing.T) {
			dir, err := s.NamedVolumeDir(name)
			if err == nil {
				t.Fatalf("NamedVolumeDir(%q) = %q, want an error — this escapes the store", name, dir)
			}
			if dir != "" {
				t.Fatalf("NamedVolumeDir(%q) returned a path %q alongside its error", name, dir)
			}
		})
	}
}

// TestAnonVolumesDirRejectsTraversal covers the same hole via the DEPLOYMENT name,
// which is joined onto the data dir the same way.
func TestAnonVolumesDirRejectsTraversal(t *testing.T) {
	s := NewVolumeStore(t.TempDir(), "bare", "bare")
	for _, name := range traversals {
		t.Run(name, func(t *testing.T) {
			if dir, err := s.AnonVolumesDir(name); err == nil {
				t.Fatalf("AnonVolumesDir(%q) = %q, want an error", name, dir)
			}
		})
	}
}

// TestVolumeDirsAcceptOrdinaryNames proves the fix did not over-tighten: the
// project-scoped names cornus actually generates must still resolve, and must
// resolve UNDER the store.
func TestVolumeDirsAcceptOrdinaryNames(t *testing.T) {
	root := t.TempDir()
	s := NewVolumeStore(root, "containerd", "containerd")
	for _, name := range []string{"cache", "proj_cache", "shop-web", "db.data", "v1", "UPPER", "a"} {
		named, err := s.NamedVolumeDir(name)
		if err != nil {
			t.Fatalf("NamedVolumeDir(%q): %v", name, err)
		}
		if !strings.HasPrefix(named, filepath.Join(root, "containerd", "volumes", "named")+string(filepath.Separator)) {
			t.Fatalf("NamedVolumeDir(%q) = %q, outside the store", name, named)
		}
		anon, err := s.AnonVolumesDir(name)
		if err != nil {
			t.Fatalf("AnonVolumesDir(%q): %v", name, err)
		}
		if !strings.HasPrefix(anon, filepath.Join(root, "containerd", "volumes", "anon")+string(filepath.Separator)) {
			t.Fatalf("AnonVolumesDir(%q) = %q, outside the store", name, anon)
		}
	}
}

// TestRemoveVolumeRejectsTraversal is the second primitive: RemoveVolume is
// os.RemoveAll of the resolved directory and is reachable from
// DELETE /.cornus/v1/volume/{name}, so an unvalidated name deleted an arbitrary
// host directory. The victim directory here must survive.
func TestRemoveVolumeRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.MkdirAll(filepath.Join(victim, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The store lives deep enough that "../.." reaches the victim.
	s := NewVolumeStore(filepath.Join(root, "data"), "containerd", "containerd")

	for _, name := range []string{"../../victim", "../../../victim", victim} {
		if err := s.RemoveVolume(name); err == nil {
			t.Errorf("RemoveVolume(%q) = nil, want an error", name)
		}
	}
	if _, err := os.Stat(filepath.Join(victim, "keep")); err != nil {
		t.Fatalf("the victim directory was deleted by a traversing volume name: %v", err)
	}
}

// TestInstanceMountsRejectsTraversingVolume proves the deploy path itself refuses,
// not merely the helper — this is the call an attacker would actually reach, and
// it must fail BEFORE any MkdirAll or bind mount is produced.
func TestInstanceMountsRejectsTraversingVolume(t *testing.T) {
	s := NewVolumeStore(t.TempDir(), "containerd", "containerd")
	spec := api.DeploySpec{
		Name:    "app",
		Volumes: []api.VolumeSpec{{Name: "../../../../../../etc", Target: "/x"}},
	}
	mounts, vols, err := s.InstanceMounts(spec, 0)
	if err == nil {
		t.Fatalf("InstanceMounts accepted a traversing volume name: mounts=%v vols=%v", mounts, vols)
	}
	if mounts != nil || vols != nil {
		t.Fatalf("InstanceMounts returned mounts/vols alongside its error: %v / %v", mounts, vols)
	}
	if !strings.Contains(err.Error(), "traversal") && !strings.Contains(err.Error(), "path separator") {
		t.Fatalf("error = %q, want it to name the reason", err)
	}

	// An anonymous volume under a traversing DEPLOYMENT name is refused too.
	_, _, err = s.InstanceMounts(api.DeploySpec{
		Name:    "../../../../../../etc",
		Volumes: []api.VolumeSpec{{Target: "/x"}},
	}, 0)
	if err == nil {
		t.Fatal("InstanceMounts accepted a traversing deployment name")
	}
}

// TestUnderDir covers the containment assertion independently of the name rules,
// since it is the half that holds if a spelling slips past ValidateVolumeName.
func TestUnderDir(t *testing.T) {
	tests := []struct {
		dir, path string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b/c/d", true},
		{"/a/b", "/a", false},
		{"/a/b", "/a/c", false},
		{"/a/b", "/", false},
		{"/a/b", "/a/bc", false}, // sibling with a shared prefix
	}
	for _, tt := range tests {
		if got := underDir(tt.dir, tt.path); got != tt.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", tt.dir, tt.path, got, tt.want)
		}
	}
}
