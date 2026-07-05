package fsop

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"cornus/pkg/api"
)

// tree builds a small directory and returns its root.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// wholeRoot is the shape a host BACKEND uses: one root at "/", so a request path
// is used as-is. The caretaker's multi-root case is covered in pkg/caretaker.
func wholeRoot(path string) []Root { return []Root{{Target: "/", Path: path}} }

// TestServeWritesTheBodyAndOnlyThen pins the contract a deploy.FSOperator owes
// its caller, and the reason Serve exists rather than each backend copying the
// body itself.
//
// The response must not be returned before the archive is complete. A caller
// that saw success and then a short read would write a truncated tree and call
// it a successful copy.
func TestServeWritesTheBodyAndOnlyThen(t *testing.T) {
	root := tree(t)
	var buf bytes.Buffer
	resp, err := Serve(api.FSOpRequest{Op: api.FSOpGet, Path: "/sub/f.txt"}, wholeRoot(root), nil, &buf)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("get failed: %s", resp.Error)
	}
	if !resp.Body {
		t.Fatal("a get must report Body so the caller knows to read one")
	}
	names := tarNames(t, buf.Bytes())
	if len(names) != 1 || names[0] != "f.txt" {
		t.Fatalf("archive entries = %v, want just f.txt (docker-cp names the top level after the source's basename)", names)
	}
}

// TestServeDrainsWhenNobodyWantsTheBody: a nil out must not abandon the reader.
//
// The packer writes into an UNBUFFERED io.Pipe and blocks on its first write.
// Closing the body unblocks it; forgetting to leaves one goroutine blocked
// forever per discarded get, holding an open directory tree, with no error
// anywhere.
//
// The assertion has to be the GOROUTINE. Two earlier versions of this test did
// not observe the leak: one waited only for Serve to return (dropping the read
// makes it return SOONER), and the invariant turned out not to be the read at
// all but the Close — which is now what this catches, and what the code says.
func TestServeDrainsWhenNobodyWantsTheBody(t *testing.T) {
	root := tree(t)
	settle(t)
	before := runtime.NumGoroutine()

	if _, err := Serve(api.FSOpRequest{Op: api.FSOpGet, Path: "/sub"}, wholeRoot(root), nil, nil); err != nil {
		t.Fatalf("Serve with a nil out: %v", err)
	}

	// Poll: the packer may take a moment to notice the pipe closed and exit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines went %d -> %d and stayed there: the packer is blocked writing to a pipe "+
		"nobody read, which leaks once per discarded get", before, runtime.NumGoroutine())
}

// settle waits for goroutines from earlier tests to finish, so the baseline this
// test compares against is not someone else's cleanup still in flight.
func settle(t *testing.T) {
	t.Helper()
	start := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == start {
			return
		}
		start = n
	}
}

// TestRenameIsInPlace is the operation this whole interface exists for on a host
// backend: no bytes move. Asserting the source is GONE is what distinguishes a
// rename from a copy that happened to work.
func TestRenameIsInPlace(t *testing.T) {
	root := tree(t)
	resp, err := Serve(api.FSOpRequest{Op: api.FSOpRename, Path: "/sub/f.txt", To: "/sub/moved.txt"},
		wholeRoot(root), nil, nil)
	if err != nil || resp.Error != "" {
		t.Fatalf("rename: err=%v resp=%s", err, resp.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "f.txt")); !os.IsNotExist(err) {
		t.Fatal("the source survived the rename, so this was a copy")
	}
	b, err := os.ReadFile(filepath.Join(root, "sub", "moved.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("destination content = %q, err = %v", b, err)
	}
}

// TestConfinementRefusesAnEscapingSymlink is the security property the whole
// package rests on when a BACKEND uses it: the root is a container's rootfs
// reached through /proc/<pid>/root, and a symlink inside the container that
// points at "/" must resolve to the container's root, never the host's.
func TestConfinementRefusesAnEscapingSymlink(t *testing.T) {
	root := tree(t)
	// A classic escape: an absolute symlink to the host root.
	if err := os.Symlink("/", filepath.Join(root, "escape")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// Something that exists on the host but must NOT be reachable through it.
	resp, _ := Serve(api.FSOpRequest{Op: api.FSOpStat, Path: "/escape/etc/hostname"},
		wholeRoot(root), nil, nil)
	if resp.Error == "" {
		t.Fatal("a symlink to / resolved outside the root; a container symlink would read the HOST filesystem")
	}
}

// TestUnknownOpIsUnsupported keeps the vocabulary the caller branches on: an
// unknown op must be answerable, because "unsupported" tells it to relay while
// an error tells it to fail the user's operation.
func TestUnknownOpIsUnsupported(t *testing.T) {
	root := tree(t)
	resp, err := Serve(api.FSOpRequest{Op: api.FSOp("chown"), Path: "/sub"}, wholeRoot(root), nil, nil)
	if err != nil {
		t.Fatalf("an unknown op must not be a transport error: %v", err)
	}
	if resp.Code != api.FSErrUnsupported {
		t.Fatalf("code = %q, want unsupported", resp.Code)
	}
}

// TestPathUnderNoRootIsUnsupported: a backend passes one root at "/", so nothing
// can miss — but the caretaker passes several, and a path outside them all must
// be a positive "not mine" rather than a not-found.
func TestPathUnderNoRootIsUnsupported(t *testing.T) {
	resp, _ := Serve(api.FSOpRequest{Op: api.FSOpStat, Path: "/elsewhere"},
		[]Root{{Target: "/data", Path: t.TempDir()}}, nil, nil)
	if resp.Code != api.FSErrUnsupported {
		t.Fatalf("code = %q, want unsupported — a not-found would tell the caller the file is missing "+
			"when the truth is that this operator does not serve that path", resp.Code)
	}
}

// TestReadOnlyRootRefusesWrites pins that the flag is honoured for every writing
// op, not just the one someone remembered.
func TestReadOnlyRootRefusesWrites(t *testing.T) {
	root := tree(t)
	ro := []Root{{Target: "/", Path: root, ReadOnly: true}}
	for _, req := range []api.FSOpRequest{
		{Op: api.FSOpMkdir, Path: "/new"},
		{Op: api.FSOpRemove, Path: "/sub/f.txt"},
		{Op: api.FSOpPut, Path: "/sub"},
		{Op: api.FSOpRename, Path: "/sub/f.txt", To: "/sub/x"},
	} {
		resp, _ := Serve(req, ro, emptyTar(), nil)
		if resp.Code != api.FSErrReadOnly {
			t.Errorf("%s on a read-only root: code = %q (%s), want read-only", req.Op, resp.Code, resp.Error)
		}
	}
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("reading archive: %v", err)
		}
		names = append(names, h.Name)
	}
}

func emptyTar() io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.Close()
	return &buf
}
