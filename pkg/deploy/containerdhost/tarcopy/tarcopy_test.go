package tarcopy

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func tarEntries(t *testing.T, b []byte) map[string]*tar.Header {
	t.Helper()
	out := map[string]*tar.Header{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		h := *hdr
		out[hdr.Name] = &h
	}
}

func TestStat(t *testing.T) {
	root := t.TempDir()
	mtime := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	mkFile(t, filepath.Join(root, "etc", "foo"), "hello", 0o640)
	if err := os.Chtimes(filepath.Join(root, "etc", "foo"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("foo", filepath.Join(root, "etc", "link")); err != nil {
		t.Fatal(err)
	}

	st, err := Stat(root, "/etc/foo")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if st.Name != "foo" || st.Size != 5 || os.FileMode(st.Mode).Perm() != 0o640 || st.LinkTarget != "" {
		t.Errorf("file stat: %+v", st)
	}
	if got, err := time.Parse(time.RFC3339Nano, st.Mtime); err != nil || !got.Equal(mtime) {
		t.Errorf("mtime: %q, err %v, want %v", st.Mtime, err, mtime)
	}

	st, err = Stat(root, "/etc")
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if st.Name != "etc" || os.FileMode(st.Mode)&os.ModeDir == 0 {
		t.Errorf("dir stat: %+v", st)
	}

	st, err = Stat(root, "/etc/link")
	if err != nil {
		t.Fatalf("Stat symlink: %v", err)
	}
	if os.FileMode(st.Mode)&os.ModeSymlink == 0 || st.LinkTarget != "foo" {
		t.Errorf("symlink stat: %+v", st)
	}

	if _, err := Stat(root, "/no/such/path"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing path: got %v, want ErrNotExist", err)
	}
}

func TestPackDirRoundTripsThroughUnpack(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "app", "bin", "run.sh"), "#!/bin/sh\n", 0o755)
	mkFile(t, filepath.Join(root, "app", "conf.txt"), "conf", 0o600)
	if err := os.Symlink("conf.txt", filepath.Join(root, "app", "conf.link")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	st, err := Pack(root, "/app", &buf)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if st.Name != "app" {
		t.Errorf("Pack stat name: %q", st.Name)
	}
	ents := tarEntries(t, buf.Bytes())
	for _, name := range []string{"app/", "app/bin/", "app/bin/run.sh", "app/conf.txt", "app/conf.link"} {
		if ents[name] == nil {
			t.Fatalf("missing tar entry %q; have %v", name, keys(ents))
		}
	}
	if ents["app/conf.link"].Typeflag != tar.TypeSymlink || ents["app/conf.link"].Linkname != "conf.txt" {
		t.Errorf("symlink entry: %+v", ents["app/conf.link"])
	}
	if ents["app/bin/run.sh"].FileInfo().Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode: %v", ents["app/bin/run.sh"].FileInfo().Mode())
	}

	// Unpack into a fresh root and verify contents, modes and symlink.
	root2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(root2, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Unpack(root2, "/dst", bytes.NewReader(buf.Bytes()), UnpackOptions{}); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root2, "dst", "app", "bin", "run.sh"))
	if err != nil || string(b) != "#!/bin/sh\n" {
		t.Fatalf("run.sh content: %q, %v", b, err)
	}
	fi, err := os.Stat(filepath.Join(root2, "dst", "app", "bin", "run.sh"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh unpacked mode: %v, %v", fi.Mode(), err)
	}
	fi, err = os.Stat(filepath.Join(root2, "dst", "app", "conf.txt"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("conf.txt unpacked mode: %v, %v", fi.Mode(), err)
	}
	link, err := os.Readlink(filepath.Join(root2, "dst", "app", "conf.link"))
	if err != nil || link != "conf.txt" {
		t.Errorf("unpacked symlink: %q, %v", link, err)
	}
}

func keys(m map[string]*tar.Header) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestPackSingleFile(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "etc", "hostname"), "box\n", 0o644)
	var buf bytes.Buffer
	st, err := Pack(root, "/etc/hostname", &buf)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if st.Name != "hostname" || st.Size != 4 {
		t.Errorf("stat: %+v", st)
	}
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("first entry: %v", err)
	}
	if hdr.Name != "hostname" || hdr.Typeflag != tar.TypeReg {
		t.Errorf("entry: %+v", hdr)
	}
	b, _ := io.ReadAll(tr)
	if string(b) != "box\n" {
		t.Errorf("content: %q", b)
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("expected single entry, next err %v", err)
	}
}

// TestPackFileShrinkZeroPads exercises a file that shrinks between the Lstat
// that sized its tar header and the read of its body (the confinement root is
// a live container). packFile must zero-pad to the declared size so the entry
// stays well-formed and tw.Close does not fail with "missed writing N bytes".
func TestPackFileShrinkZeroPads(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p) // sizes the header at 10 bytes
	if err != nil {
		t.Fatal(err)
	}
	// Truncate the file after the stat, before packing.
	if err := os.WriteFile(p, []byte("012"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := packFile(tw, fi, p, "f"); err != nil {
		t.Fatalf("packFile: %v", err)
	}
	// Would fail here with "archive/tar: missed writing N bytes" if the body
	// were left short of the header size.
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tr := tar.NewReader(&buf)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if hdr.Size != 10 {
		t.Fatalf("header size = %d, want 10", hdr.Size)
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 10 {
		t.Fatalf("body len = %d, want 10", len(body))
	}
	if string(body[:3]) != "012" {
		t.Fatalf("body prefix = %q, want %q", body[:3], "012")
	}
	for i, c := range body[3:] {
		if c != 0 {
			t.Fatalf("body[%d] = %d, want zero padding", i+3, c)
		}
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected single entry, next err %v", err)
	}
}

func TestPackSymlinkTopLevel(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "target"), "x", 0o644)
	if err := os.Symlink("target", filepath.Join(root, "ln")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	st, err := Pack(root, "/ln", &buf)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if st.LinkTarget != "target" {
		t.Errorf("stat LinkTarget: %q", st.LinkTarget)
	}
	ents := tarEntries(t, buf.Bytes())
	if len(ents) != 1 || ents["ln"] == nil || ents["ln"].Typeflag != tar.TypeSymlink || ents["ln"].Linkname != "target" {
		t.Errorf("entries: %v", ents)
	}
}

func makeTar(t *testing.T, entries []tar.Header, contents map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range entries {
		hdr := entries[i]
		if c, ok := contents[hdr.Name]; ok {
			hdr.Size = int64(len(c))
		}
		if hdr.ModTime.IsZero() {
			hdr.ModTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if c, ok := contents[hdr.Name]; ok {
			if _, err := io.WriteString(tw, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnpackRejectsEscapingNames(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/abs"} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "dst"), 0o755); err != nil {
			t.Fatal(err)
		}
		archive := makeTar(t, []tar.Header{
			{Name: name, Typeflag: tar.TypeReg, Mode: 0o644},
		}, map[string]string{name: "boom"})
		err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{})
		if err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("name %q: got err %v, want outside error", name, err)
		}
	}
}

func TestUnpackNoOverwriteDirNonDir(t *testing.T) {
	// Directory replaced by file.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dst", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := makeTar(t, []tar.Header{
		{Name: "x", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"x": "file"})
	err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{NoOverwriteDirNonDir: true})
	if err == nil || !strings.Contains(err.Error(), "cannot overwrite") {
		t.Errorf("dir->file: got %v, want cannot overwrite", err)
	}
	// Without the option it succeeds.
	if err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Errorf("dir->file without option: %v", err)
	}

	// File replaced by directory.
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkFile(t, filepath.Join(root, "dst", "y"), "f", 0o644)
	archive = makeTar(t, []tar.Header{
		{Name: "y", Typeflag: tar.TypeDir, Mode: 0o755},
	}, nil)
	err = Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{NoOverwriteDirNonDir: true})
	if err == nil || !strings.Contains(err.Error(), "cannot overwrite") {
		t.Errorf("file->dir: got %v, want cannot overwrite", err)
	}
	if err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Errorf("file->dir without option: %v", err)
	}
}

func TestUnpackOverwritesExistingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkFile(t, filepath.Join(root, "dst", "f"), "old", 0o600)
	archive := makeTar(t, []tar.Header{
		{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"f": "new"})
	if err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "dst", "f"))
	if err != nil || string(b) != "new" {
		t.Errorf("content: %q, %v", b, err)
	}
}

func TestUnpackIntoNonDirDestErrors(t *testing.T) {
	root := t.TempDir()
	mkFile(t, filepath.Join(root, "dst"), "iamafile", 0o644)
	archive := makeTar(t, []tar.Header{
		{Name: "f", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"f": "x"})
	err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("got %v, want not a directory", err)
	}
}

func TestSymlinkInsideRootConfinesResolution(t *testing.T) {
	// A symlink inside the root pointing to "/" must resolve to the root
	// itself, never to the host filesystem root.
	root := t.TempDir()
	if err := os.Symlink("/", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	mkFile(t, filepath.Join(root, "inroot.txt"), "confined", 0o644)

	// Stat through the escape link resolves within root.
	st, err := Stat(root, "/escape/inroot.txt")
	if err != nil {
		t.Fatalf("Stat via escape link: %v", err)
	}
	if st.Name != "inroot.txt" || st.Size != int64(len("confined")) {
		t.Errorf("stat: %+v", st)
	}
	// A path that exists on the host but not inside root must not be found.
	if _, err := Stat(root, "/escape/etc/passwd"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat must not escape root: got %v", err)
	}

	// Pack through the escape link reads inside root only.
	var buf bytes.Buffer
	if _, err := Pack(root, "/escape/inroot.txt", &buf); err != nil {
		t.Fatalf("Pack via escape link: %v", err)
	}
	if _, err := Pack(root, "/escape/etc/passwd", &buf); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Pack must not escape root: got %v", err)
	}

	// Unpack with dest under the escape link writes inside root only.
	archive := makeTar(t, []tar.Header{
		{Name: "written.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"written.txt": "ok"})
	if err := Unpack(root, "/escape", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Fatalf("Unpack via escape link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "written.txt")); err != nil {
		t.Fatalf("file must land inside root: %v", err)
	}
	if _, err := os.Stat("/written.txt"); err == nil {
		t.Fatalf("file leaked to host root")
	}
}

func TestUnpackSymlinkEntryPointingOutsideIsNotFollowed(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink entry whose target is outside is created verbatim, then a
	// file created through it must stay confined to root.
	archive := makeTar(t, []tar.Header{
		{Name: "ln", Typeflag: tar.TypeSymlink, Linkname: "/", Mode: 0o777},
		{Name: "ln/pwned.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}, map[string]string{"ln/pwned.txt": "x"})
	if err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	link, err := os.Readlink(filepath.Join(root, "dst", "ln"))
	if err != nil || link != "/" {
		t.Fatalf("symlink created verbatim: %q, %v", link, err)
	}
	// The file written "through" the link must be inside root (at root/,
	// since the link points at the container root), never on the host.
	if _, err := os.Stat(filepath.Join(root, "pwned.txt")); err != nil {
		t.Fatalf("file must be confined to root: %v", err)
	}
	if _, err := os.Stat("/pwned.txt"); err == nil {
		t.Fatalf("file leaked to host root")
	}
}

func TestUnpackHardlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := makeTar(t, []tar.Header{
		{Name: "orig", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "hard", Typeflag: tar.TypeLink, Linkname: "orig"},
	}, map[string]string{"orig": "same"})
	if err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "dst", "hard"))
	if err != nil || string(b) != "same" {
		t.Errorf("hardlink content: %q, %v", b, err)
	}
}

func TestUnpackRestoresMtime(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dst"), 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	archive := makeTar(t, []tar.Header{
		{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mtime},
		{Name: "d/f", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: mtime},
	}, map[string]string{"d/f": "x"})
	if err := Unpack(root, "/dst", bytes.NewReader(archive), UnpackOptions{}); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	fi, err := os.Stat(filepath.Join(root, "dst", "d", "f"))
	if err != nil || !fi.ModTime().Equal(mtime) {
		t.Errorf("file mtime: %v, %v, want %v", fi.ModTime(), err, mtime)
	}
	fi, err = os.Stat(filepath.Join(root, "dst", "d"))
	if err != nil || !fi.ModTime().Equal(mtime) {
		t.Errorf("dir mtime: %v, %v, want %v", fi.ModTime(), err, mtime)
	}
}
