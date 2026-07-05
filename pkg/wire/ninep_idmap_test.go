package wire

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hugelgupf/p9/p9"
)

// mapOwnerStream rewrites two fields at fixed byte offsets inside an Rgetattr.
// Offsets derived by hand are exactly the kind of thing that is wrong silently,
// and a test that encodes the message with the same offsets it asserts on would
// only be checking my arithmetic against itself.
//
// So this drives a REAL p9 server and a REAL p9 client through the proxy: the
// server encodes the reply with the library's own encoder, the client decodes it
// with the library's own decoder, and the rewrite has to land in the right place
// for the client to see it at all. Wrong offsets corrupt the message and the
// client fails or reports something else.
func idmapHarness(t *testing.T, dir string, uid, gid uint32) *p9.Client {
	t.Helper()
	attacher, err := confinedAttacherCounted(dir, nil, nil)
	if err != nil {
		t.Fatalf("attacher: %v", err)
	}

	// Caller export server <-> the proxy's remote end.
	callerConn, proxyRemote := net.Pipe()
	callerSrv := p9.NewServer(attacher)
	go func() { _ = callerSrv.Handle(callerConn, callerConn) }()

	// The proxy's "kernel" side <-> the test client.
	kernelConn, testConn := net.Pipe()
	go pipeMappingOwner(kernelConn, proxyRemote, uid, gid)

	cl, err := p9.NewClient(testConn, p9.WithMessageSize(1<<20))
	if err != nil {
		t.Fatalf("test client: %v", err)
	}
	t.Cleanup(func() {
		_ = cl.Close()
		_ = testConn.Close()
		_ = kernelConn.Close()
		_ = proxyRemote.Close()
		_ = callerConn.Close()
	})
	return cl
}

func getattrThrough(t *testing.T, cl *p9.Client, name string) p9.Attr {
	t.Helper()
	root, err := cl.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer root.Close()
	_, f, err := root.Walk([]string{name})
	if err != nil {
		t.Fatalf("walk %s: %v", name, err)
	}
	defer f.Close()
	_, _, attr, err := f.GetAttr(p9.AttrMask{Mode: true, Size: true, UID: true, GID: true})
	if err != nil {
		t.Fatalf("getattr %s: %v", name, err)
	}
	return attr
}

func TestMapOwnerStreamRewritesOwnership(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Values no real file on the test host would have, so a pass cannot come from
	// the proxy leaving the caller's own ids in place.
	const wantUID, wantGID = 424242, 434343
	cl := idmapHarness(t, dir, wantUID, wantGID)

	attr := getattrThrough(t, cl, "f.txt")
	if attr.UID != p9.UID(wantUID) || attr.GID != p9.GID(wantGID) {
		t.Fatalf("GetAttr through the mapping splice = uid %d gid %d, want %d/%d",
			attr.UID, attr.GID, wantUID, wantGID)
	}
	// The rest of the message has to survive the rewrite. Size is read from a
	// field AFTER uid/gid, so a wrong offset or a mangled frame shows up here.
	if attr.Size != 5 {
		t.Errorf("Size = %d, want 5: the rewrite disturbed the rest of the attributes", attr.Size)
	}
	if !attr.Mode.IsRegular() {
		t.Errorf("Mode = %v, want a regular file: the rewrite disturbed a field BEFORE uid", attr.Mode)
	}
}

// TestMapOwnerStreamPassesBulkDataThrough: only Rgetattr is rewritten, and every
// other message — including a multi-hundred-KiB Rread payload that is spliced
// rather than buffered whole — must cross unchanged. A framing mistake in the
// splice path corrupts the data rather than the attributes, so this is the half
// that the ownership assertions cannot catch.
func TestMapOwnerStreamPassesBulkDataThrough(t *testing.T) {
	dir := t.TempDir()
	payload := make([]byte, 300*1024)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	cl := idmapHarness(t, dir, 1001, 1001)
	got := readFile(t, cl, "big.bin")
	if len(got) != len(payload) {
		t.Fatalf("read %d bytes through the mapping splice, want %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("payload differs at byte %d (got %d want %d): the splice mis-framed a message",
				i, got[i], payload[i])
		}
	}
}

// TestMapOwnerStreamWalkAndReaddir exercises the message types that are NOT
// rewritten but share the stream, so a mistake in message-boundary handling shows
// up as a wrong directory listing rather than as wrong ownership.
func TestMapOwnerStreamWalkAndReaddir(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cl := idmapHarness(t, dir, 1001, 1001)

	root, err := cl.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer root.Close()
	_, dirFile, err := root.Walk(nil)
	if err != nil {
		t.Fatalf("walk root: %v", err)
	}
	defer dirFile.Close()
	if _, _, err := dirFile.Open(p9.ReadOnly); err != nil {
		t.Fatalf("open root: %v", err)
	}
	ents, err := dirFile.Readdir(0, 4096)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range ents {
		seen[e.Name] = true
	}
	for _, n := range []string{"a", "b", "c"} {
		if !seen[n] {
			t.Fatalf("readdir through the mapping splice lost %q (got %v)", n, seen)
		}
	}
}
