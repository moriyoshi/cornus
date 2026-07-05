package lineage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCollectFillsHostUserDir(t *testing.T) {
	dir := t.TempDir()
	o := Collect(dir)
	if o == nil {
		t.Fatal("Collect returned nil")
	}
	if o.Host == "" {
		t.Error("Host is empty")
	}
	if o.User == "" {
		t.Error("User is empty")
	}
	// dir is resolved to an absolute path; on some platforms TempDir already is.
	if abs, _ := filepath.Abs(dir); o.Directory != abs {
		t.Errorf("Directory = %q, want %q", o.Directory, abs)
	}
	if o.Subject != "" {
		t.Errorf("Subject = %q, want empty (server-stamped)", o.Subject)
	}
}

func TestCollectNoGitOutsideRepo(t *testing.T) {
	dir := t.TempDir() // a bare temp dir is not a git work tree
	if g := Collect(dir).Git; g != nil {
		t.Errorf("Git = %+v, want nil outside a repo", g)
	}
}

func TestCollectGitInsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Deterministic identity so commit works without global config.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("remote", "add", "origin", "https://example.com/repo.git")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-m", "init")

	g := Collect(dir).Git
	if g == nil {
		t.Fatal("Git is nil inside a repo")
	}
	if g.Remote != "https://example.com/repo.git" {
		t.Errorf("Remote = %q", g.Remote)
	}
	if g.Branch != "main" {
		t.Errorf("Branch = %q, want main", g.Branch)
	}
	if len(g.Commit) != 40 {
		t.Errorf("Commit = %q, want a 40-char SHA", g.Commit)
	}
	if g.Dirty {
		t.Error("Dirty = true on a clean tree")
	}

	// An untracked file dirties the tree.
	if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if g := Collect(dir).Git; g == nil || !g.Dirty {
		t.Errorf("Dirty = false after an untracked write")
	}
}
