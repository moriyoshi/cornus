//go:build unix

package clientconn

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProvenanceWorldWritableDirSkipped: a context file in a world-writable,
// non-sticky directory (anyone could have planted it) is skipped unless the operator
// opts into trusting project context files.
func TestProvenanceWorldWritableDirSkipped(t *testing.T) {
	ww := filepath.Join(t.TempDir(), "ww")
	if err := os.Mkdir(ww, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ww, 0o777); err != nil { // defeat umask; non-sticky, world-writable
		t.Fatal(err)
	}
	p := filepath.Join(ww, "cornus-context.yaml")
	mustWrite(t, p, "server: http://demo\n")

	// Even an explicitly named file is skipped when its provenance is untrusted.
	ov, _, err := (&Resolver{ProjectContextFile: p}).projectOverride()
	if err != nil {
		t.Fatal(err)
	}
	if ov != nil {
		t.Fatalf("world-writable-dir file should be skipped, got %+v", ov)
	}

	// --trust-context-file bypasses provenance.
	ov, _, err = (&Resolver{ProjectContextFile: p, TrustProjectContext: true}).projectOverride()
	if err != nil {
		t.Fatal(err)
	}
	if ov == nil || ov.Server != "http://demo" {
		t.Fatalf("trust flag should honor the file, got %+v", ov)
	}
}
