package buildwire

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net"
	"testing"

	"github.com/hugelgupf/p9/fsimpl/composefs"
	"github.com/hugelgupf/p9/fsimpl/localfs"
	"github.com/hugelgupf/p9/p9"
	"github.com/tonistiigi/fsutil"
)

// TestP9FSWithFsutilWriteTar exercises the exact code path BuildKit uses to read
// a local mount (fsutil walk + per-file Open) against the p9-backed FS, over an
// in-memory pipe (no WebSocket/yamux), so a failure reproduces without a build.
func TestP9FSWithFsutilWriteTar(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "Dockerfile", "FROM scratch\n")
	mustWrite(t, dir, "sub/x.txt", "deep")

	c1, c2 := net.Pipe()
	attacher, err := composefs.New(composefs.WithMount("dockerfile", localfs.Attacher(dir)))
	if err != nil {
		t.Fatal(err)
	}
	srv := p9.NewServer(attacher)
	go func() { _ = srv.Handle(c1, c1) }()
	client, err := p9.NewClient(c2)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	fsys := &p9FS{client: client, root: []string{"dockerfile"}}

	var buf bytes.Buffer
	if err := fsutil.WriteTar(context.Background(), fsys, &buf); err != nil {
		t.Fatalf("fsutil.WriteTar: %v", err)
	}

	got := map[string]string{}
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			got[hdr.Name] = string(b)
		}
	}
	if got["Dockerfile"] != "FROM scratch\n" {
		t.Fatalf("Dockerfile content via fsutil = %q (all: %v)", got["Dockerfile"], got)
	}
	if got["sub/x.txt"] != "deep" {
		t.Fatalf("nested file via fsutil = %q (all: %v)", got["sub/x.txt"], got)
	}
}
