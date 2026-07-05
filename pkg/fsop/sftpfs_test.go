package fsop

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// TestSFTPFSContract runs the SAME assertions as TestLocalFSContract against the
// SFTP implementation.
//
// Running the same function rather than a parallel set of tests is the point. The
// abstraction was chosen over a second op-serving implementation specifically to
// stop the two routes drifting, and a contract asserted twice in two spellings is
// two contracts — the drift just moves into the tests, where it is harder to see.
//
// It needs no daemon: github.com/pkg/sftp ships a server, so a real client speaks
// the real protocol to a real server over an in-memory pipe, against a temp dir.
// That exercises the protocol's own semantics — which is where SFTP differs from
// os, and therefore where this implementation can be wrong.
func TestSFTPFSContract(t *testing.T) {
	root := t.TempDir()
	client := newPipeSFTP(t)

	mk := func(rel string, dir bool, content, symlinkTo string) {
		t.Helper()
		// Fixtures are created with os, NOT through the client: a fixture built by
		// the same code under test would hide a bug in both directions at once.
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
	RunFSContract(t, "SFTPFS", SFTPFS{Client: client}, root, mk)
}

// newPipeSFTP wires a real sftp server to a real sftp client over two pipes.
func newPipeSFTP(t *testing.T) *sftp.Client {
	t.Helper()
	srvIn, cliOut := io.Pipe()
	cliIn, srvOut := io.Pipe()

	srv, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{srvIn, srvOut})
	if err != nil {
		t.Fatalf("sftp server: %v", err)
	}
	go func() { _ = srv.Serve() }()

	cli, err := sftp.NewClientPipe(cliIn, cliOut)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	// Close the pipe WRITERS first. Closing the client alone deadlocks: its
	// receive goroutine is blocked reading a pipe that nothing has ended, and
	// Client.Close waits for that goroutine. Ending both directions gives each
	// side EOF, after which the closes return.
	t.Cleanup(func() {
		_ = cliOut.Close()
		_ = srvOut.Close()
		_ = cli.Close()
		_ = srv.Close()
	})
	return cli
}

// TestSFTPFSRefusesToClimbAboveItsRoot pins the confinement this implementation
// is responsible for.
//
// It is a WEAKER guarantee than LocalFS's on purpose, and the difference is worth
// stating: LocalFS resolves symlinks against its root because the surrounding
// filesystem is the HOST. Here the channel addresses exactly one instance, so the
// instance is the boundary and the daemon holds it. What must still hold is that a
// request path cannot climb above the declared root by spelling.
func TestSFTPFSRefusesToClimbAboveItsRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "..", "escaped.txt")
	if err := os.WriteFile(outside, []byte("host-secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	fsys := SFTPFS{Client: newPipeSFTP(t)}
	for _, rel := range []string{
		"/../escaped.txt",
		"/../../escaped.txt",
		"/a/../../escaped.txt",
	} {
		if _, err := fsys.Stat(root, rel); err == nil {
			t.Fatalf("%q resolved to a path above the root; a request path must not be able to "+
				"name anything outside it by spelling", rel)
		}
	}
}
