package caretaker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
)

// fsopHarness stands the real role up on one side of a real yamux pair and hands back the
// server-side session, so every test below drives the SAME path a backend does — request
// framing, dispatch, tarcopy, reply framing. A fake would let the two halves drift, and
// the framing is exactly where they would drift.
func fsopHarness(t *testing.T, roots []FSOpRoot) *yamux.Session {
	t.Helper()
	serverSide, caretakerSide := yamuxPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		<-ctx.Done()
		caretakerSide.Close()
	}()
	disp := newTagDispatch()
	registerFSOp(disp, FSOpRole{Roots: roots})
	go disp.run(ctx, caretakerSide)
	return serverSide
}

// fsopFixture builds a two-root layout: a writable "/data" and a read-only "/ro", each
// mounted somewhere else in the caretaker's own namespace — which is the whole point, a
// sidecar cannot mount a volume at the app's own path.
func fsopFixture(t *testing.T) (roots []FSOpRoot, dataDir, roDir string) {
	t.Helper()
	base := t.TempDir()
	dataDir = filepath.Join(base, "vol-data")
	roDir = filepath.Join(base, "vol-ro")
	if err := os.MkdirAll(filepath.Join(dataDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write(filepath.Join(dataDir, "top.txt"), "top")
	write(filepath.Join(dataDir, "sub", "nested.txt"), "nested")
	write(filepath.Join(roDir, "seed.sql"), "select 1;")
	return []FSOpRoot{
		{Target: "/data", Path: dataDir},
		{Target: "/ro", Path: roDir, ReadOnly: true},
	}, dataDir, roDir
}

func fsop(t *testing.T, sess *yamux.Session, req api.FSOpRequest, body io.Reader, out io.Writer) api.FSOpResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := deploywire.FSOp(ctx, sess, req, body, out)
	if err != nil {
		t.Fatalf("%s %s: transport: %v", req.Op, req.Path, err)
	}
	return resp
}

func TestFSOpStatAndList(t *testing.T) {
	roots, _, _ := fsopFixture(t)
	sess := fsopHarness(t, roots)

	st := fsop(t, sess, api.FSOpRequest{Op: api.FSOpStat, Path: "/data/top.txt"}, nil, nil)
	if st.Error != "" {
		t.Fatalf("stat: %s", st.Error)
	}
	if st.Stat == nil || st.Stat.Name != "top.txt" || st.Stat.Size != 3 {
		t.Fatalf("stat = %+v", st.Stat)
	}

	ls := fsop(t, sess, api.FSOpRequest{Op: api.FSOpList, Path: "/data"}, nil, nil)
	if ls.Error != "" {
		t.Fatalf("list: %s", ls.Error)
	}
	names := map[string]bool{}
	for _, e := range ls.Entries {
		names[e.Name] = true
	}
	if !names["top.txt"] || !names["sub"] || len(ls.Entries) != 2 {
		t.Fatalf("listing = %+v", ls.Entries)
	}

	// The root itself is addressable by the target path the APP uses, not by the
	// caretaker's own directory — the mapping is the role's entire job.
	if got := fsop(t, sess, api.FSOpRequest{Op: api.FSOpList, Path: "/ro"}, nil, nil); len(got.Entries) != 1 {
		t.Fatalf("read-only root listing = %+v", got.Entries)
	}
}

// TestFSOpClassifiesRefusals: the codes exist because the caller's next move depends on
// which refusal it was. A path under no root must be "unsupported" — that is what makes
// the caller relay instead of reporting a failure to the user.
func TestFSOpClassifiesRefusals(t *testing.T) {
	roots, _, _ := fsopFixture(t)
	sess := fsopHarness(t, roots)

	for _, tc := range []struct {
		name string
		req  api.FSOpRequest
		code string
	}{
		{"a path under no root", api.FSOpRequest{Op: api.FSOpStat, Path: "/etc/hosts"}, api.FSErrUnsupported},
		{"the image layers", api.FSOpRequest{Op: api.FSOpList, Path: "/"}, api.FSErrUnsupported},
		{"a missing file", api.FSOpRequest{Op: api.FSOpStat, Path: "/data/nope"}, api.FSErrNotFound},
		{"listing a file", api.FSOpRequest{Op: api.FSOpList, Path: "/data/top.txt"}, api.FSErrNotDir},
		{"writing a read-only root", api.FSOpRequest{Op: api.FSOpMkdir, Path: "/ro/new"}, api.FSErrReadOnly},
		{"deleting from a read-only root", api.FSOpRequest{Op: api.FSOpRemove, Path: "/ro/seed.sql"}, api.FSErrReadOnly},
		{"copying INTO a read-only root", api.FSOpRequest{Op: api.FSOpCopy, Path: "/data/top.txt", To: "/ro/top.txt"}, api.FSErrReadOnly},
		{"a destination under no root", api.FSOpRequest{Op: api.FSOpCopy, Path: "/data/top.txt", To: "/etc/top.txt"}, api.FSErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := fsop(t, sess, tc.req, nil, nil)
			if resp.Error == "" {
				t.Fatalf("succeeded; want %s", tc.code)
			}
			if resp.Code != tc.code {
				t.Fatalf("code = %q (%s), want %q", resp.Code, resp.Error, tc.code)
			}
		})
	}
	// And the read-only root really is intact after all of that.
	if _, err := os.Stat(filepath.Join(roots[1].Path, "seed.sql")); err != nil {
		t.Fatalf("the read-only root lost a file: %v", err)
	}
}

func TestFSOpGetAndPutRoundTripATree(t *testing.T) {
	roots, dataDir, _ := fsopFixture(t)
	sess := fsopHarness(t, roots)

	var tarball bytes.Buffer
	got := fsop(t, sess, api.FSOpRequest{Op: api.FSOpGet, Path: "/data/sub"}, nil, &tarball)
	if got.Error != "" {
		t.Fatalf("get: %s", got.Error)
	}
	if !got.Body {
		t.Fatal("get did not announce a body")
	}
	if names := tarNames(t, tarball.Bytes()); len(names) == 0 || names[0] != "sub" {
		t.Fatalf("archive names = %v, want a top-level \"sub\"", names)
	}

	// Put it back under a fresh destination and check the bytes actually landed.
	if err := os.MkdirAll(filepath.Join(dataDir, "dest"), 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	put := fsop(t, sess, api.FSOpRequest{Op: api.FSOpPut, Path: "/data/dest"}, bytes.NewReader(tarball.Bytes()), nil)
	if put.Error != "" {
		t.Fatalf("put: %s", put.Error)
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "dest", "sub", "nested.txt"))
	if err != nil || string(b) != "nested" {
		t.Fatalf("extracted file = %q, %v", b, err)
	}
}

// TestFSOpCopyMovesNoBytesAcrossTheWire is the operation the whole role exists for. The
// assertion that matters is not that the copy worked — a relay would also work — but that
// it worked with an empty request body and an empty reply body: nothing was transferred.
func TestFSOpCopyMovesNoBytesAcrossTheWire(t *testing.T) {
	roots, dataDir, _ := fsopFixture(t)
	sess := fsopHarness(t, roots)

	resp := fsop(t, sess, api.FSOpRequest{Op: api.FSOpCopy, Path: "/data/sub", To: "/data/copied"}, nil, nil)
	if resp.Error != "" {
		t.Fatalf("copy: %s", resp.Error)
	}
	if resp.Body {
		t.Fatal("a native copy announced a body; bytes crossed the wire")
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "copied", "nested.txt"))
	if err != nil || string(b) != "nested" {
		t.Fatalf("copied file = %q, %v", b, err)
	}
	// The source survives a copy.
	if _, err := os.Stat(filepath.Join(dataDir, "sub", "nested.txt")); err != nil {
		t.Fatalf("copy consumed the source: %v", err)
	}

	// A copy that renames as it goes has to rewrite the archive's top-level entry;
	// getting that wrong lands the tree under the OLD name, which reads as success
	// until somebody looks.
	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpCopy, Path: "/data/top.txt", To: "/data/renamed.txt"}, nil, nil); r.Error != "" {
		t.Fatalf("renaming copy: %s", r.Error)
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "renamed.txt")); err != nil || string(b) != "top" {
		t.Fatalf("renaming copy landed wrong: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "top.txt")); err != nil {
		t.Fatalf("renaming copy consumed the source: %v", err)
	}
}

// TestFSOpCopyAcrossRootsWorks: two volumes on one workload is the case that motivated
// the role, and the two roots are different directories in the caretaker.
func TestFSOpCopyAcrossRootsWorks(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := fsopHarness(t, []FSOpRoot{{Target: "/a", Path: a}, {Target: "/b", Path: b}})

	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpCopy, Path: "/a/f.txt", To: "/b/f.txt"}, nil, nil); r.Error != "" {
		t.Fatalf("cross-root copy: %s", r.Error)
	}
	if got, err := os.ReadFile(filepath.Join(b, "f.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("cross-root copy = %q, %v", got, err)
	}
}

func TestFSOpRenameMkdirAndRemove(t *testing.T) {
	roots, dataDir, _ := fsopFixture(t)
	sess := fsopHarness(t, roots)

	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpMkdir, Path: "/data/deep/er"}, nil, nil); r.Error != "" {
		t.Fatalf("mkdir: %s", r.Error)
	}
	if fi, err := os.Stat(filepath.Join(dataDir, "deep", "er")); err != nil || !fi.IsDir() {
		t.Fatalf("mkdir did not create parents: %v", err)
	}
	// A second mkdir is a success, not an EEXIST: that is what makes it retryable.
	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpMkdir, Path: "/data/deep/er"}, nil, nil); r.Error != "" {
		t.Fatalf("mkdir is not idempotent: %s", r.Error)
	}

	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpRename, Path: "/data/top.txt", To: "/data/deep/moved.txt"}, nil, nil); r.Error != "" {
		t.Fatalf("rename: %s", r.Error)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "top.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rename left the source behind: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dataDir, "deep", "moved.txt")); err != nil || string(b) != "top" {
		t.Fatalf("renamed file = %q, %v", b, err)
	}

	// Non-recursive remove refuses a non-empty directory; recursive takes it.
	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpRemove, Path: "/data/deep"}, nil, nil); r.Error == "" {
		t.Fatal("non-recursive remove took a non-empty directory")
	}
	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpRemove, Path: "/data/deep", Recursive: true}, nil, nil); r.Error != "" {
		t.Fatalf("recursive remove: %s", r.Error)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "deep")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recursive remove left the tree: %v", err)
	}
	// Delete-if-exists, matching Backend.Delete and VolumeRemover: a retry is a success.
	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpRemove, Path: "/data/deep", Recursive: true}, nil, nil); r.Error != "" {
		t.Fatalf("removing what is already gone: %s", r.Error)
	}
	// Removing a whole mount root is refused whatever the recursion flag says.
	if r := fsop(t, sess, api.FSOpRequest{Op: api.FSOpRemove, Path: "/data", Recursive: true}, nil, nil); r.Error == "" {
		t.Fatal("a mount root was removed")
	}
}

// TestFSOpRootsMatchOnPathBoundaries: "/data" must not capture "/database". A string
// prefix test passes every other assertion in this file and fails only here.
func TestFSOpRootsMatchOnPathBoundaries(t *testing.T) {
	base := t.TempDir()
	data, database := filepath.Join(base, "data"), filepath.Join(base, "database")
	for _, d := range []string{data, database} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(database, "marker"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Only /data is a root. A string-prefix test would hand /database/marker to it and
	// then report "not found" for a file that exists — the answer must be that this
	// operator does not serve that path at all, so the caller relays.
	sess := fsopHarness(t, []FSOpRoot{{Target: "/data", Path: data}})
	if got := fsop(t, sess, api.FSOpRequest{Op: api.FSOpStat, Path: "/database/marker"}, nil, nil); got.Code != api.FSErrUnsupported {
		t.Fatalf("/database/marker: code = %q (%s), want unsupported — /data captured it", got.Code, got.Error)
	}

	// With both declared, each resolves to its own root.
	both := fsopHarness(t, []FSOpRoot{{Target: "/data", Path: data}, {Target: "/database", Path: database}})
	if st := fsop(t, both, api.FSOpRequest{Op: api.FSOpStat, Path: "/database/marker"}, nil, nil); st.Error != "" {
		t.Fatalf("/database/marker resolved to the wrong root: %s", st.Error)
	}
	// The nested root wins over the one it sits inside, whichever order they arrive in.
	nested := []FSOpRoot{{Target: "/data", Path: data}, {Target: "/data/inner", Path: database}}
	if got, ok := resolveFSOp(normalizeFSOpRoots(nested), "/data/inner/marker"); !ok || got.root.Path != database {
		t.Fatalf("nested root lost to its parent: %+v (ok=%v)", got, ok)
	}
}

// TestFSOpDropsMalformedRoots: an empty or relative Target would prefix-match every path,
// which is how one bad config entry silently captures the whole namespace.
func TestFSOpDropsMalformedRoots(t *testing.T) {
	got := normalizeFSOpRoots([]FSOpRoot{
		{Target: "", Path: "/tmp/x"},
		{Target: "relative", Path: "/tmp/y"},
		{Target: "/ok", Path: ""},
		{Target: "/ok/", Path: "/tmp/z"},
	})
	if len(got) != 1 || got[0].Target != "/ok" {
		t.Fatalf("normalized roots = %+v, want just a cleaned /ok", got)
	}
	if _, ok := resolveFSOp(got, "/anything"); ok {
		t.Fatal("a path under no root resolved anyway")
	}
}

// TestFSOpUnknownOpIsUnsupportedNotAHang: a caretaker that simply closed the stream on an
// op it does not know would be indistinguishable from a crash, and the caller would wait
// out yamux's stream-open timeout for every one.
func TestFSOpUnknownOpIsUnsupportedNotAHang(t *testing.T) {
	roots, _, _ := fsopFixture(t)
	sess := fsopHarness(t, roots)
	resp := fsop(t, sess, api.FSOpRequest{Op: api.FSOp("chown"), Path: "/data/top.txt"}, nil, nil)
	if resp.Code != api.FSErrUnsupported {
		t.Fatalf("code = %q (%s), want unsupported", resp.Code, resp.Error)
	}
	if !strings.Contains(resp.Error, "unsupported") {
		t.Fatalf("error %q does not contain \"unsupported\"; pkg/server maps that substring onto 501", resp.Error)
	}
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		out = append(out, strings.TrimSuffix(hdr.Name, "/"))
	}
}
