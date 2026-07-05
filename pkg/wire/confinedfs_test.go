package wire

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/p9"
)

// rawClient connects a p9 client directly to an attacher over an in-memory pipe,
// so tests can issue the kind of Twalk/Topen/Tcreate a hostile build server
// would — bypassing any honest FS adapter, which sanitizes paths itself.
func rawClient(t *testing.T, attacher p9.Attacher) *p9.Client {
	t.Helper()
	c1, c2 := net.Pipe()
	srv := p9.NewServer(attacher)
	go func() { _ = srv.Handle(c1, c1) }()
	cl, err := p9.NewClient(c2)
	if err != nil {
		t.Fatalf("p9 client: %v", err)
	}
	t.Cleanup(func() {
		_ = cl.Close()
		_ = c1.Close()
		_ = c2.Close()
	})
	return cl
}

// TestGuardFollowVsParent checks the Open-time vs Walk-time confinement split
// directly: a final-component escaping symlink is reachable (parent ok) but not
// followable (follow denied). It can't be exercised via the p9 client because
// p9 refuses to Open a symlink fid, so it is tested white-box.
func TestGuardFollowVsParent(t *testing.T) {
	ctxDir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ctxDir, "evilfile")); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(ctxDir)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		t.Fatal(err)
	}
	g := &guard{root: abs}

	if err := g.confinedParent("evilfile"); err != nil {
		t.Errorf("confinedParent(evilfile) = %v, want nil (reachable as a symlink)", err)
	}
	if err := g.confinedFollow("evilfile"); err == nil {
		t.Error("confinedFollow(evilfile) = nil, want denial (following escapes root)")
	}
	// Walking through it is denied at parent resolution too.
	if err := g.confinedParent("evilfile/secret"); err == nil {
		t.Error("confinedParent(evilfile/secret) = nil, want denial")
	}
}
