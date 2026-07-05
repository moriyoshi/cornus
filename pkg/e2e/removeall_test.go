package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// EVERY dangerous path in this file goes through resolveRemovable, which is pure
// and deletes nothing. The one test that actually removes files drives the
// builtin with paths inside t.TempDir() only. Do not add a case that calls
// bRemoveAll with a path outside a t.TempDir(), and do not neutralize
// bRemoveAll: for a destructive operation, removing the guard IS the failure —
// which is exactly how the predecessor of this builtin destroyed a home
// directory. See removeall.go's safety model.

// newRoot makes a directory that qualifies as a sandbox root: under the test's
// own temp dir, with the base name prefix only temp_dir() produces.
func newRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), tempDirPrefix+"root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	// Resolve, so comparisons hold on hosts where TMPDIR is a symlink.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	return resolved
}

// TestResolveRemovableRefusesOutsideTheSandbox is the core safety assertion.
//
// Every case supplies a VALID root, so a refusal is attributable to the PATH
// rather than to the absence of roots. Without that, each row would pass on the
// errNoRoots branch and certify nothing about containment — a negative case
// proves nothing unless the guard it names is the only thing between it and a
// pass.
func TestResolveRemovableRefusesOutsideTheSandbox(t *testing.T) {
	root := newRoot(t)
	roots := []string{root}
	home, _ := os.UserHomeDir()

	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"filesystem root", "/"},
		{"root's parent via dotdot", "/.."},
		{"tmp dotdot resolving to /", "/tmp/.."},
		{"bare dot is not absolute", "."},
		{"relative path", "some/relative/path"},
		{"relative dotdot", "../.."},
		{"etc", "/etc"},
		{"a real file outside", "/etc/passwd"},
		{"the parent of the root", filepath.Dir(root)},
		{"escaping the root with dotdot", filepath.Join(root, "..", "..", "elsewhere")},
		{"an unregistered sibling temp dir", filepath.Join(filepath.Dir(root), tempDirPrefix+"other")},
		{"the user's home directory", home},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "the user's home directory" && c.path == "" {
				t.Skip("no home directory reported on this host")
			}
			got, err := resolveRemovable(roots, c.path)
			if err == nil {
				t.Fatalf("resolveRemovable(%q) returned %q with no error: it would have been DELETED", c.path, got)
			}
			if got != "" {
				t.Errorf("refusal returned a non-empty path %q", got)
			}
			// The refusal must be about containment, not about missing roots —
			// otherwise this row is not testing what it claims.
			if errors.Is(err, errNoRoots) {
				t.Errorf("refused for lack of roots, not for the path: %v", err)
			}
		})
	}
}

// TestResolveRemovableAcceptsInsideTheSandbox is the other half: an
// over-restrictive guard that refuses everything would pass the refusal table
// above while making the builtin useless.
func TestResolveRemovableAcceptsInsideTheSandbox(t *testing.T) {
	root := newRoot(t)
	roots := []string{root}

	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	file := filepath.Join(nested, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	for _, c := range []struct{ name, path string }{
		{"the root itself", root},
		{"a subdirectory", nested},
		{"a file", file},
		{"a path that does not exist (removal is a no-op)", filepath.Join(root, "missing", "deeper")},
		{"an uncleaned path that stays inside", filepath.Join(root, "a", "..", "a", "b")},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveRemovable(roots, c.path)
			if err != nil {
				t.Fatalf("resolveRemovable(%q) refused a path inside the sandbox: %v", c.path, err)
			}
			if !strings.HasPrefix(got, root) {
				t.Errorf("returned %q, which is not under the root %q", got, root)
			}
		})
	}
}

// TestResolveRemovableRefusesSymlinkEscape is the case a lexical prefix check
// gets wrong. The link LIVES inside the sandbox, so its path passes any
// string-prefix test, while its target is outside.
func TestResolveRemovableRefusesSymlinkEscape(t *testing.T) {
	root := newRoot(t)
	outside := t.TempDir() // deliberately NOT prefixed, so never a root
	victim := filepath.Join(outside, "precious")
	if err := os.WriteFile(victim, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Through the link, at the escaped directory's content.
	if got, err := resolveRemovable([]string{root}, filepath.Join(link, "precious")); err == nil {
		t.Fatalf("a symlink escape resolved to %q and would have been DELETED", got)
	}
	// The LINK ITSELF is refused too, even though os.RemoveAll would only unlink
	// it and leave the target alone. That is deliberate over-strictness: the
	// resolution is uniform, and a scenario that needs to drop a dangling link can
	// use sh(). Erring toward refusal is the correct bias for this builtin.
	if _, err := resolveRemovable([]string{root}, link); err == nil {
		t.Error("a symlink pointing outside the sandbox should be refused, even though removing it would be harmless")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the victim file was disturbed by a pure predicate: %v", err)
	}
}

// TestSandboxRootsRejectsRootsTheHarnessDidNotMint is the structural guarantee.
// Even if a future change registered "/" or the home directory as a root, no
// deletion outside a temp_dir() becomes possible, because a root is recognized
// only by the prefix os.MkdirTemp in bTempDir produces.
func TestSandboxRootsRejectsRootsTheHarnessDidNotMint(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, bad := range [][]string{
		nil,
		{},
		{""},
		{"/"},
		{"/tmp"},
		{"/etc"},
		{home},
		{"relative-not-absolute"},
		{filepath.Join(os.TempDir(), "not-our-prefix")},
	} {
		if _, err := sandboxRoots(bad); !errors.Is(err, errNoRoots) {
			t.Errorf("sandboxRoots(%v) accepted a root the harness never created (err=%v)", bad, err)
		}
	}

	// And a bad root mixed in with a good one must not widen the sandbox.
	root := newRoot(t)
	if _, err := resolveRemovable([]string{"/", root}, "/etc/passwd"); err == nil {
		t.Fatal("a bogus '/' root widened the sandbox: /etc/passwd would have been DELETED")
	}
	if _, err := resolveRemovable([]string{"/", root}, filepath.Join(root, "ok")); err != nil {
		t.Errorf("a bogus root should not disable a legitimate one: %v", err)
	}
}

// TestRemoveAllBuiltinDeletesOnlyInsideItsTempDirs is the ONE test that really
// deletes. Both the sandbox root and the victim live under t.TempDir(), so the
// worst possible outcome is the loss of this test's own scratch space.
func TestRemoveAllBuiltinDeletesOnlyInsideItsTempDirs(t *testing.T) {
	h := &Harness{}
	root := newRoot(t)
	h.tempDirs = []string{root}

	target := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(target, "deep"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "deep", "f"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := callRemoveAll(t, h, target); err != nil {
		t.Fatalf("remove_all inside the sandbox failed: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("remove_all did not remove %s (err=%v)", target, err)
	}
	// Removing an already-absent path is a no-op, not an error.
	if _, err := callRemoveAll(t, h, target); err != nil {
		t.Errorf("second remove_all should be a no-op: %v", err)
	}

	// A path outside the sandbox is refused. The path used here is a sibling
	// under this test's OWN t.TempDir(), never a real system path, so even a
	// total failure of the guard could only delete test scratch space.
	sibling := filepath.Join(filepath.Dir(root), "not-a-root")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	if _, err := callRemoveAll(t, h, sibling); err == nil {
		t.Fatal("remove_all deleted a path outside its temp dirs")
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("the refused path was removed anyway: %v", err)
	}
}

// TestRemoveAllIsRegistered guards the wiring: a builtin absent from
// predeclared() is invisible to scenarios, and TestPredeclaredNamesInSync only
// checks the two lists agree with each other, not that either contains this one.
func TestRemoveAllIsRegistered(t *testing.T) {
	if !predeclaredNames()["remove_all"] {
		t.Error("remove_all is missing from predeclaredNames()")
	}
	// predeclared() is not called here: it dereferences h.target, which a
	// zero-value Harness does not have. TestPredeclaredNamesInSync already proves
	// the two lists agree, so the name being present above means the builtin is
	// registered in both.
}

// callRemoveAll drives the builtin exactly as a scenario would (keyword arg),
// so the test covers argument handling and not just resolveRemovable.
func callRemoveAll(t *testing.T, h *Harness, path string) (starlark.Value, error) {
	t.Helper()
	return h.bRemoveAll(nil, nil, nil, []starlark.Tuple{
		{starlark.String("path"), starlark.String(path)},
	})
}
