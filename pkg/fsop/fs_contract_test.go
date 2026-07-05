package fsop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// The contract every FS implementation must satisfy.
//
// This exists because the abstraction's whole promise — one meaning for an
// operation however its bytes are reached — is otherwise unenforced. Statement
// coverage of pkg/fsop across its consumers is ~82%, but three behaviours were
// each deleted in turn and NOT ONE test failed:
//
//   - List describing symlinks instead of following them,
//   - List refusing a non-directory with FSErrNotDir,
//   - Remove treating a missing path as success.
//
// The lines executed; nothing asserted what they did. That is coverage without
// assertions, and it is exactly how a second implementation drifts from the first
// while every suite stays green.
//
// RunFSContract is exported to the package's tests so the incus SFTP
// implementation runs the SAME assertions rather than a sympathetic rewrite of
// them. A contract asserted twice in two spellings is two contracts.
func RunFSContract(t *testing.T, name string, fsys FS, root string, mk func(rel string, dir bool, content, symlinkTo string)) {
	t.Helper()

	t.Run(name+"/list describes symlinks rather than following them", func(t *testing.T) {
		mk("/ld", true, "", "")
		mk("/ld/real.txt", false, "hello", "")
		mk("/ld/link", false, "", "real.txt")
		ents, err := fsys.List(root, "/ld")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		var link *api.PathStat
		for i := range ents {
			if ents[i].Name == "link" {
				link = &ents[i]
			}
		}
		if link == nil {
			t.Fatal("the symlink is missing from the listing")
		}
		if link.LinkTarget != "real.txt" {
			t.Fatalf("LinkTarget = %q, want %q: a listing that follows links reports the TARGET's "+
				"kind and size for something that is a link, and the caller then walks into it",
				link.LinkTarget, "real.txt")
		}
		if os.FileMode(link.Mode)&os.ModeSymlink == 0 {
			t.Fatalf("mode %v does not say symlink", os.FileMode(link.Mode))
		}
	})

	t.Run(name+"/list refuses a non-directory", func(t *testing.T) {
		mk("/nf.txt", false, "x", "")
		_, err := fsys.List(root, "/nf.txt")
		if err == nil {
			t.Fatal("listing a FILE succeeded; the caller cannot tell an empty directory from a file")
		}
		var se *StatusError
		if !asStatusError(err, &se) || se.Code != api.FSErrNotDir {
			t.Fatalf("error = %v, want a StatusError coded %q — the caller's next move depends on "+
				"WHICH refusal it was, and a bare string collapses them", err, api.FSErrNotDir)
		}
	})

	t.Run(name+"/list of an empty directory is empty, not an error", func(t *testing.T) {
		mk("/empty", true, "", "")
		ents, err := fsys.List(root, "/empty")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(ents) != 0 {
			t.Fatalf("entries = %d, want 0", len(ents))
		}
	})

	t.Run(name+"/remove of a missing path is success", func(t *testing.T) {
		// Delete-if-exists, matching Backend.Delete and VolumeRemover: a RETRIED
		// delete must not report a failure for work already done.
		if err := fsys.Remove(root, "/never-existed", false); err != nil {
			t.Fatalf("non-recursive remove of a missing path failed: %v", err)
		}
		if err := fsys.Remove(root, "/never-existed-either", true); err != nil {
			t.Fatalf("recursive remove of a missing path failed: %v", err)
		}
	})

	t.Run(name+"/remove recursive clears a tree, non-recursive does not", func(t *testing.T) {
		mk("/rt", true, "", "")
		mk("/rt/inner", true, "", "")
		mk("/rt/inner/f.txt", false, "x", "")
		if err := fsys.Remove(root, "/rt", false); err == nil {
			t.Fatal("non-recursive remove of a NON-EMPTY directory succeeded")
		}
		if err := fsys.Remove(root, "/rt", true); err != nil {
			t.Fatalf("recursive remove: %v", err)
		}
		if _, err := fsys.Stat(root, "/rt"); err == nil {
			t.Fatal("the tree is still there after a recursive remove")
		}
	})

	t.Run(name+"/mkdirall creates missing parents", func(t *testing.T) {
		if err := fsys.MkdirAll(root, "/a/b/c"); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if _, err := fsys.Stat(root, "/a/b/c"); err != nil {
			t.Fatalf("stat after MkdirAll: %v", err)
		}
	})

	t.Run(name+"/rename moves the bytes", func(t *testing.T) {
		mk("/rn.txt", false, "payload", "")
		if err := fsys.Rename(root, "/rn.txt", root, "/rn2.txt"); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if _, err := fsys.Stat(root, "/rn.txt"); err == nil {
			t.Fatal("the source still exists after a rename")
		}
		var buf bytes.Buffer
		if err := fsys.Pack(root, "/rn2.txt", &buf); err != nil {
			t.Fatalf("Pack after rename: %v", err)
		}
		if !bytes.Contains(buf.Bytes(), []byte("payload")) {
			t.Fatal("the renamed file's bytes did not survive")
		}
	})

	t.Run(name+"/stat of a missing path classifies as not-found", func(t *testing.T) {
		_, err := fsys.Stat(root, "/definitely-missing")
		if err == nil {
			t.Fatal("stat of a missing path succeeded")
		}
		if got := classify(err).Code; got != api.FSErrNotFound {
			t.Fatalf("classified as %q, want %q — the caller distinguishes 'tell the user' from "+
				"'relay this instead' by this code alone", got, api.FSErrNotFound)
		}
	})

	t.Run(name+"/pack then unpack round-trips", func(t *testing.T) {
		mk("/rt2", true, "", "")
		mk("/rt2/f.txt", false, "roundtrip", "")
		var buf bytes.Buffer
		if err := fsys.Pack(root, "/rt2/f.txt", &buf); err != nil {
			t.Fatalf("Pack: %v", err)
		}
		if err := fsys.MkdirAll(root, "/dest"); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := fsys.Unpack(root, "/dest", &buf, UnpackOptions{}); err != nil {
			t.Fatalf("Unpack: %v", err)
		}
		var out bytes.Buffer
		if err := fsys.Pack(root, "/dest/f.txt", &out); err != nil {
			t.Fatalf("Pack of the unpacked copy: %v", err)
		}
		if !bytes.Contains(out.Bytes(), []byte("roundtrip")) {
			t.Fatal("the round-tripped bytes did not survive")
		}
	})
}

func asStatusError(err error, target **StatusError) bool {
	for err != nil {
		if se, ok := err.(*StatusError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestLocalFSContract runs the contract against the implementation every host
// backend and the caretaker already use.
func TestLocalFSContract(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, dir bool, content, symlinkTo string) {
		t.Helper()
		p := filepath.Join(root, strings.TrimPrefix(rel, "/"))
		switch {
		case dir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", p, err)
			}
		case symlinkTo != "":
			if err := os.Symlink(symlinkTo, p); err != nil {
				t.Fatalf("symlink %s: %v", p, err)
			}
		default:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("mkdir parent: %v", err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}
	}
	RunFSContract(t, "LocalFS", LocalFS{}, root, mk)
}
