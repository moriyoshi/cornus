package kubernetes

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// TestPackArgs pins the tar invocation, which is a compatibility contract rather
// than an implementation detail: `tar cf - -C parent base` is the one spelling
// GNU, busybox and toybox all implement, and it is what kubectl cp has used for a
// decade. A "simpler" `tar cf - /abs/path` would pack the whole ancestry into the
// archive and every consumer of the stream would be wrong about what it holds.
func TestPackArgs(t *testing.T) {
	for path, want := range map[string]string{
		"/etc/hosts":     "tar cf - -C /etc hosts",
		"/etc/":          "tar cf - -C / etc",
		"etc/hosts":      "tar cf - -C /etc hosts",
		"/usr/share/doc": "tar cf - -C /usr/share doc",
		"/single":        "tar cf - -C / single",
		"/a//b/../c":     "tar cf - -C /a c",
	} {
		if got := strings.Join(packArgs(path), " "); got != want {
			t.Errorf("packArgs(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestErrNoTarIsRoutedAsUnsupported is a coupling test, and the coupling is the
// point. An image with no tar must reach the client as 501, because that is what
// pkg/server's streamErrStatus maps "unsupported" onto and what the web BFF keys
// its fallback to the caretaker on. If this error stops saying "unsupported", a
// distroless workload with volumes silently stops falling back and starts failing
// outright — and nothing else in the tree would notice.
func TestErrNoTarIsRoutedAsUnsupported(t *testing.T) {
	msg := errNoTar("web-abc123").Error()
	if !strings.Contains(msg, "unsupported") {
		t.Errorf("errNoTar must contain \"unsupported\" so streamErrStatus answers 501; got %q", msg)
	}
	// And it has to say which pod and what the way out is, since this is the text a
	// user reads when a copy will not run.
	for _, want := range []string{"web-abc123", "tar", "volumes"} {
		if !strings.Contains(msg, want) {
			t.Errorf("errNoTar message %q does not mention %q", msg, want)
		}
	}
}

// TestStatFromHeader covers the projection of a tar header onto the docker-shaped
// stat. The NAME assertions are the ones with a bug behind them: tar writes the
// archive-relative name and appends a slash for a directory, so passing the header
// name straight through would report "doc/" where every other backend reports
// "doc".
func TestStatFromHeader(t *testing.T) {
	mt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("file", func(t *testing.T) {
		st := statFromHeader("/etc/hosts", &tar.Header{
			Name: "hosts", Size: 42, Mode: 0o644, ModTime: mt, Typeflag: tar.TypeReg,
		})
		if st.Name != "hosts" || st.Size != 42 || st.Mtime != "2026-08-04T12:00:00Z" {
			t.Errorf("stat = %+v", st)
		}
		if st.Mode&0o777 != 0o644 {
			t.Errorf("mode = %o, want 0644 in the low bits", st.Mode)
		}
		if st.LinkTarget != "" {
			t.Errorf("LinkTarget = %q, want empty for a regular file", st.LinkTarget)
		}
	})

	t.Run("directory keeps no trailing slash", func(t *testing.T) {
		st := statFromHeader("/usr/share/doc", &tar.Header{
			Name: "doc/", Mode: 0o755, ModTime: mt, Typeflag: tar.TypeDir,
		})
		if st.Name != "doc" {
			t.Errorf("Name = %q, want %q — the header's trailing slash must not leak", st.Name, "doc")
		}
	})

	t.Run("symlink carries its target", func(t *testing.T) {
		st := statFromHeader("/bin/sh", &tar.Header{
			Name: "sh", Linkname: "/bin/busybox", ModTime: mt, Typeflag: tar.TypeSymlink,
		})
		if st.LinkTarget != "/bin/busybox" {
			t.Errorf("LinkTarget = %q, want the readlink value", st.LinkTarget)
		}
	})
}

// TestHeaderTapWriterPassesBytesThrough is the assertion that matters most in this
// file: the archive contract is that the tar bytes reach the caller UNCHANGED, and
// the stat is read by looking at the stream rather than by re-encoding it. A tap
// that dropped or duplicated the first block would still produce a correct-looking
// stat while corrupting every download.
//
// Driven in small, awkward writes because that is what a network stream delivers —
// a tap that assumed the first Write carried a whole 512-byte block would pass a
// single-write test and fail in production.
func TestHeaderTapWriterPassesBytesThrough(t *testing.T) {
	var src bytes.Buffer
	tw := tar.NewWriter(&src)
	body := strings.Repeat("payload!", 300)
	if err := tw.WriteHeader(&tar.Header{
		Name: "hosts", Size: int64(len(body)), Mode: 0o644, ModTime: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	want := src.Bytes()

	var sink bytes.Buffer
	tap := &headerTapWriter{w: &sink}
	for off := 0; off < len(want); off += 7 {
		end := min(off+7, len(want))
		if _, err := tap.Write(want[off:end]); err != nil {
			t.Fatal(err)
		}
	}

	if !bytes.Equal(sink.Bytes(), want) {
		t.Fatalf("the tap altered the stream: got %d bytes, want %d (equal=%v)",
			sink.Len(), len(want), bytes.Equal(sink.Bytes(), want))
	}
	hdr, err := tap.header()
	if err != nil {
		t.Fatalf("header(): %v", err)
	}
	if hdr.Name != "hosts" || hdr.Size != int64(len(body)) {
		t.Errorf("header = %+v, want hosts/%d", hdr, len(body))
	}
}

// TestHeaderTapWriterShortStream covers an exec that produced nothing (tar died
// before writing a header): header() must say so rather than hand back a zero
// value that reads as a real, empty file.
func TestHeaderTapWriterShortStream(t *testing.T) {
	tap := &headerTapWriter{w: io.Discard}
	if _, err := tap.Write([]byte("too short")); err != nil {
		t.Fatal(err)
	}
	if _, err := tap.header(); err == nil {
		t.Error("header() on a truncated stream returned no error")
	}
}

// TestLimitedWriterCapsAndNeverFails: tar's stderr is an untrusted image writing
// into the server's memory, so it is bounded — and bounding it must never fail the
// exec, since a diagnostic sink is not a reason to lose a working transfer.
func TestLimitedWriterCapsAndNeverFails(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, n: 10}
	n, err := lw.Write([]byte(strings.Repeat("x", 100)))
	if err != nil || n != 100 {
		t.Fatalf("Write = %d, %v — must report the full length and no error", n, err)
	}
	if _, err := lw.Write([]byte("more")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if buf.Len() != 10 {
		t.Errorf("kept %d bytes, want the 10-byte cap", buf.Len())
	}
}
