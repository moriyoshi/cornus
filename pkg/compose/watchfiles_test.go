package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOnFileReadCollectsAllSources verifies that LoadOptions.OnFileRead reports
// every file that fed the load: the compose file(s), the sibling .env, a
// per-service env_file, an included file, and an `extends` target file — so
// `compose up --watch` can build a complete reload trigger set.
func TestOnFileReadCollectsAllSources(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	compose := write("compose.yaml", `
name: demo
include:
  - included.yaml
services:
  web:
    image: ${IMG}
    env_file:
      - web.env
  app:
    extends:
      file: base.yaml
      service: base
`)
	dotEnv := write(".env", "IMG=nginx:1.27\n")
	webEnv := write("web.env", "FOO=bar\n")
	included := write("included.yaml", "services:\n  cache:\n    image: redis:7\n")
	base := write("base.yaml", "services:\n  base:\n    image: busybox\n")

	var got []string
	_, err := LoadDocumentWithOptions(LoadOptions{
		OnFileRead: func(p string) {
			abs, _ := filepath.Abs(p)
			got = append(got, filepath.Clean(abs))
		},
	}, compose)
	if err != nil {
		t.Fatalf("LoadDocumentWithOptions: %v", err)
	}

	seen := map[string]bool{}
	for _, p := range got {
		seen[p] = true
	}
	for _, want := range []string{compose, dotEnv, webEnv, included, base} {
		abs, _ := filepath.Abs(want)
		if !seen[filepath.Clean(abs)] {
			t.Errorf("OnFileRead did not report %s; got %v", want, got)
		}
	}
}

// TestOnFileReadRecordsMissingOptionalFiles verifies that an absent sibling .env
// is still reported, so creating it later triggers a reload.
func TestOnFileReadRecordsMissingOptionalFiles(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No .env written.

	var got []string
	if _, err := LoadDocumentWithOptions(LoadOptions{
		OnFileRead: func(p string) { got = append(got, filepath.Clean(p)) },
	}, compose); err != nil {
		t.Fatalf("LoadDocumentWithOptions: %v", err)
	}

	wantEnv := filepath.Clean(filepath.Join(dir, ".env"))
	found := false
	for _, p := range got {
		if p == wantEnv {
			found = true
		}
	}
	if !found {
		t.Errorf("expected absent sibling .env %s to be reported; got %v", wantEnv, got)
	}
}
