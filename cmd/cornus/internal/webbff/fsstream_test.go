package webbff

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
)

// bigContent is deliberately larger than the editor's bound and not a round number, so a
// copy that quietly truncated at the cap, or at a block boundary, is visible.
const bigContent = maxEditableFileSize + 4096 + 7

func writeBig(t *testing.T, path string) [32]byte {
	t.Helper()
	b := make([]byte, bigContent)
	for i := range b {
		b[i] = byte(i * 31)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// TestCopyIsNoLongerCapped is the headline: a file larger than maxEditableFileSize copies,
// byte for byte. That bound belongs to the editor, not to a transfer.
func TestCopyIsNoLongerCapped(t *testing.T) {
	s, projectDir, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	want := writeBig(t, filepath.Join(projectDir, "big.bin"))

	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/big.bin",
		`{"to":"proj-web/data/big.bin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("copy of a %d-byte file: %d %s", bigContent, rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(sharedDir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != bigContent {
		t.Fatalf("copied %d bytes, want %d", len(got), bigContent)
	}
	if sha256.Sum256(got) != want {
		t.Error("content differs: the copy is not byte-for-byte")
	}
}

// TestMoveIsNoLongerCapped: the cross-filesystem move falls back to copy-then-delete, so
// it goes through the same streaming path.
func TestMoveIsNoLongerCapped(t *testing.T) {
	s, projectDir, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	want := writeBig(t, filepath.Join(projectDir, "big.bin"))

	if rec := doReq(t, s, "POST", "/.cornus/web/fs/move?source=virtual&path=project/big.bin",
		`{"to":"proj-web/data/big.bin"}`); rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := os.ReadFile(filepath.Join(sharedDir, "big.bin"))
	if sha256.Sum256(got) != want {
		t.Errorf("moved content differs (%d bytes)", len(got))
	}
	if _, err := os.Stat(filepath.Join(projectDir, "big.bin")); !os.IsNotExist(err) {
		t.Error("source survived the move")
	}
}

// TestEditorKeepsItsBound: lifting the cap for transfers must not lift it for the editor,
// whose PUT body is held in memory and rendered into a text box.
func TestEditorKeepsItsBound(t *testing.T) {
	s, _, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	body := strings.Repeat("x", maxEditableFileSize+1)
	if rec := doReq(t, s, "PUT", "/.cornus/web/fs/content?source=virtual&path=project/huge.txt", body); rec.Code == http.StatusOK {
		t.Fatalf("the editor write path must stay bounded, got %d", rec.Code)
	}
}

// TestCopyRefusesNonRegularSource: a FIFO would block the reader forever, and a device is
// not a thing to copy. The streaming reader has to refuse before it opens.
func TestCopyRefusesNonRegularSource(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	fifo := filepath.Join(projectDir, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/pipe",
		`{"to":"proj-web/data/pipe"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("copying a fifo: got %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestCopyExactlyDetectsSizeChange pins the framing contract directly. A tar entry is
// framed BEFORE its bytes, so a source that grows yields a valid archive silently
// truncated, and one that shrinks yields an unterminated entry. Neither is detectable
// downstream.
func TestCopyExactlyDetectsSizeChange(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		var buf bytes.Buffer
		if err := copyExactly(&buf, strings.NewReader("hello"), 5); err != nil {
			t.Fatalf("exact size should succeed: %v", err)
		}
		if buf.String() != "hello" {
			t.Errorf("got %q", buf.String())
		}
	})
	t.Run("grew", func(t *testing.T) {
		var buf bytes.Buffer
		err := copyExactly(&buf, strings.NewReader("hello world"), 5)
		if err == nil || !strings.Contains(err.Error(), "grew") {
			t.Fatalf("a growing source must be an error, got %v", err)
		}
	})
	t.Run("shrank, and the frame is still well-formed", func(t *testing.T) {
		var buf bytes.Buffer
		err := copyExactly(&buf, strings.NewReader("hi"), 5)
		if err == nil || !strings.Contains(err.Error(), "shrank") {
			t.Fatalf("a shrinking source must be an error, got %v", err)
		}
		// Padded to the promised length: an entry that under-delivers corrupts the
		// archive, and the error belongs to the caller rather than the tar format.
		if buf.Len() != 5 || !bytes.Equal(buf.Bytes(), []byte{'h', 'i', 0, 0, 0}) {
			t.Errorf("frame not padded to size: %q", buf.Bytes())
		}
	})
}

// TestAbortedLocalWriteLeavesTheOriginal is the temp-plus-rename guarantee. Without it an
// aborted transfer destroys the previous content as well as failing — a window that grew
// from 10 MB to unbounded the moment the cap came off.
func TestAbortedLocalWriteLeavesTheOriginal(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	dst := filepath.Join(projectDir, "keep.txt")
	if err := os.WriteFile(dst, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := s.createStream(t.Context(), fsQuery{source: "virtual", path: "project/keep.txt"}, 100, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "partial"); err != nil {
		t.Fatal(err)
	}
	w.(failer).Fail()
	_ = w.Close()

	if b, _ := os.ReadFile(dst); string(b) != "original" {
		t.Fatalf("an abandoned write disturbed the destination: %q", b)
	}
	// And it left no litter behind.
	ents, _ := os.ReadDir(projectDir)
	for _, e := range ents {
		if strings.Contains(e.Name(), "cornus-tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// nonReadingFS models a CopyTo that returns WITHOUT draining its reader — a server that
// rejects the request early (404, 501, an auth failure). The writer is blocked on a pipe
// nobody is reading, so a transfer that does not close it wedges a goroutine and the
// request forever. This is the case the review predicted would deadlock, and the existing
// fake could not express it because it always drained.
type nonReadingFS struct {
	fakeContainerFS
	err error
}

func (f *nonReadingFS) CopyTo(_ context.Context, _, _ string, _ io.Reader, _ api.CopyToOptions) error {
	return f.err // never reads
}

func TestContainerWriteDoesNotDeadlockWhenNobodyReads(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.WriteFile(filepath.Join(projectDir, "src.bin"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfs = &nonReadingFS{
		fakeContainerFS: fakeContainerFS{
			statErr: errors.New("not found"),
			execFn:  func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
		},
		err: errors.New("upstream refused"),
	}

	done := make(chan int, 1)
	go func() {
		rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/src.bin",
			`{"to":"proj-web/app/dst.bin"}`)
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code == http.StatusOK {
			t.Fatalf("a CopyTo that refused must not report success, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deadlocked: the writer is blocked on a pipe nobody reads")
	}
}

// TestContainerReadSurfacesUpstreamError: the read side's failure reaches the caller only
// through Close (CopyFrom checks its stream-error trailer after draining), so a copy that
// defers Close and discards the result would call a truncated transfer a success.
func TestContainerReadSurfacesUpstreamError(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "f.bin", Mode: 0o644, Size: 8, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("12345678"))
	_ = tw.Close()

	s.cfs = &fakeContainerFS{
		stat:        api.PathStat{Name: "f.bin", Size: 8, Mode: 0o644},
		copyFromErr: errors.New("stream truncated upstream"),
		copyFrom:    tarBuf.Bytes(),
		execFn:      func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}
	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=container&workload=proj-web&path=/app/f.bin",
		`{"to":"/app/copy.bin"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("an upstream read failure must not report success: %s", rec.Body.String())
	}
}

// TestContainerReadClosesOnce pins that a SUCCESSFUL container-source copy returns.
// streamFile both defers rc.Close() and returns rc.Close() (the read side's error only
// surfaces there), so the reader is closed twice on every clean transfer. A Close that
// takes from its producer's one-shot channel a second time blocks forever, and the whole
// request goroutine wedges after the bytes have already landed.
func TestContainerReadClosesOnce(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "f.bin", Mode: 0o644, Size: 8, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("12345678"))
	_ = tw.Close()

	s.cfs = &fakeContainerFS{
		stat:     api.PathStat{Name: "f.bin", Size: 8, Mode: 0o644},
		copyFrom: tarBuf.Bytes(),
		execFn:   func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}

	done := make(chan int, 1)
	go func() {
		rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=proj-web/app/f.bin",
			`{"to":"project/out.bin"}`)
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("container->local copy: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wedged: the reader was closed twice and the second close never returned")
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "out.bin")); err != nil || string(b) != "12345678" {
		t.Fatalf("destination = %q, %v", b, err)
	}
}

// TestCopyTreeSkipsNonRegularEntries: a FIFO sitting in a copied tree is stepped over and
// NAMED, not a reason to abandon the other files. It listed as an ordinary "file", so it
// went down the branch that aborts the whole walk on any error — the copy failed, and the
// user was told nothing about which entry did it.
//
// The test would also hang rather than fail if the reader ever opened the FIFO, which is
// the other half of what is being pinned.
func TestCopyTreeSkipsNonRegularEntries(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	s.cfs = fatalContainerFS{t}
	if err := os.MkdirAll(filepath.Join(projectDir, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(filepath.Join(projectDir, "tree", "pipe")); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "tree", "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	type done struct {
		code    int
		skipped []string
	}
	ch := make(chan done, 1)
	go func() {
		rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/tree",
			`{"to":"project/copy"}`)
		var got struct {
			Skipped []string `json:"skipped"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		ch <- done{rec.Code, got.Skipped}
	}()
	select {
	case d := <-ch:
		if d.code != http.StatusOK {
			t.Fatalf("a fifo beside a regular file must not fail the tree: %d", d.code)
		}
		if len(d.skipped) != 1 || !strings.HasSuffix(d.skipped[0], "pipe") {
			t.Errorf("skipped = %v, want the fifo named", d.skipped)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wedged: the reader opened the fifo instead of refusing it")
	}
	if b, err := os.ReadFile(filepath.Join(projectDir, "copy", "keep.txt")); err != nil || string(b) != "keep" {
		t.Errorf("the regular file beside the fifo: %q %v", b, err)
	}
}

// refuseFileCopyToFS accepts directory archives and refuses file ones, so a tree copy gets
// its mkdirs and fails on the transfer itself.
type refuseFileCopyToFS struct{ fakeContainerFS }

func (f *refuseFileCopyToFS) CopyTo(ctx context.Context, name, path string, r io.Reader, o api.CopyToOptions) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return err
	}
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag == tar.TypeReg {
			return errors.New("upstream refused the write")
		}
	}
	return nil
}

// TestCopyTreeFailsWhenALinkedFileFailsToTransfer is the other half of H7/H8: `skipped`
// must mean "deliberately stepped over", never "it failed".
//
// The old walk recorded a skip for ANY error on a symlink entry, so a link to a perfectly
// good file whose transfer died halfway was reported as tidily handled — and FsMove, which
// keeps the source whenever anything was skipped, would say so while leaving a truncated
// destination nobody was told about.
func TestCopyTreeFailsWhenALinkedFileFailsToTransfer(t *testing.T) {
	s, projectDir, _ := explorerServer(t, runningUpstream(t))
	if err := os.MkdirAll(filepath.Join(projectDir, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "target.txt"), []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../target.txt", filepath.Join(projectDir, "tree", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s.cfs = &refuseFileCopyToFS{fakeContainerFS{
		statErr: errors.New("no such file or directory"),
		execFn:  func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}}

	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/tree",
		`{"to":"proj-web/app/tree"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("a failed transfer must not be reported as a skip: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "skipped") {
		t.Errorf("the failure was dressed up as a skip: %s", rec.Body.String())
	}
}

// TestUploadIsNoLongerCapped closes the asymmetry the streaming work left behind: a copy
// of a big file succeeded while the SAME file dragged in from the desktop 413'd, because
// the upload handler still rode through io.ReadAll under the editor's bound.
func TestUploadIsNoLongerCapped(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	body := strings.Repeat("u", maxEditableFileSize+4096+7)

	rec := doReq(t, s, "POST", "/.cornus/web/fs/upload?source=virtual&path=project&name=big.bin", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload of a %d-byte file: %d %s", len(body), rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(projectDir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(body) || sha256.Sum256(got) != sha256.Sum256([]byte(body)) {
		t.Errorf("uploaded %d bytes, want %d", len(got), len(body))
	}
}

// TestUploadIntoContainerIsFramed: the container destination is a tar entry sized before
// its bytes, so the declared length has to be the one that reaches the archive.
func TestUploadIntoContainerIsFramed(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	fake := &fakeContainerFS{
		statErr: errors.New("no such file or directory"),
		execFn:  func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}
	s.cfs = fake
	body := strings.Repeat("z", 300000)

	rec := doReq(t, s, "POST", "/.cornus/web/fs/upload?source=container&workload=proj-web&path=/app&name=big.bin", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	tr := tar.NewReader(bytes.NewReader(fake.copyToBuf.Bytes()))
	h, err := tr.Next()
	if err != nil {
		t.Fatalf("no archive entry: %v", err)
	}
	if h.Name != "big.bin" || h.Size != int64(len(body)) {
		t.Fatalf("entry = %q size %d, want big.bin size %d", h.Name, h.Size, len(body))
	}
	n, err := io.Copy(io.Discard, tr)
	if err != nil || n != int64(len(body)) {
		t.Fatalf("entry body = %d bytes (%v), want %d", n, err, len(body))
	}
}

// TestUploadWithoutContentLengthIsRefused: a body of unknown length cannot be framed, and
// the alternative — spool it somewhere to find out — writes the file twice. Say so.
func TestUploadWithoutContentLengthIsRefused(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	mux := http.NewServeMux()
	s.routes(mux)
	req := httptest.NewRequest("POST", "/.cornus/web/fs/upload?source=virtual&path=project&name=chunked.bin",
		io.NopCloser(strings.NewReader("some bytes")))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusLengthRequired {
		t.Fatalf("got %d, want 411 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, "chunked.bin")); !os.IsNotExist(err) {
		t.Error("a refused upload still created the destination")
	}
}

// TestContainerListingHoldsThousandsOfEntries: the listing used to share the terminal's
// 256 KiB capture bound, which ran out at a few thousand files — and copyTree turns a
// truncated listing into a 413, so a container directory of a few thousand entries could
// not be copied at all, long after the per-file size cap came off.
func TestContainerListingHoldsThousandsOfEntries(t *testing.T) {
	const entries = 9000
	var out strings.Builder
	for i := 0; i < entries; i++ {
		out.WriteString(nulRec("f", "4096", "1700000000", "644", "", fmt.Sprintf("file-%05d.bin", i)))
	}
	if out.Len() <= maxToolCapture {
		t.Fatalf("fixture is only %d bytes: it does not exceed the old bound", out.Len())
	}
	s, _, _ := explorerServer(t, runningUpstream(t))
	s.cfs = &fakeContainerFS{execFn: func(_ string, _ []string) (ExecResult, error) {
		return ExecResult{Stdout: out.String(), ExitKnown: true}, nil
	}}

	var got fsListing
	doJSON(t, s, "GET", "/.cornus/web/fs?source=container&workload=proj-web&path=/app", &got)
	if got.Truncated {
		t.Errorf("a %d-entry directory still reports truncated", entries)
	}
	if len(got.Entries) != entries {
		t.Errorf("listed %d entries, want %d", len(got.Entries), entries)
	}
}

// workdirBlindFS models the kubernetes backend: PodExecOptions has no working-directory
// field, so the backend logs "backend cannot honor exec option ... option=WorkingDir" and
// runs the command anyway. Every OTHER backend honours it, which is exactly why this went
// unnoticed — the fake honoured it too, so the unit suite agreed with three backends out
// of four and the explorer answered kubernetes with the wrong directory's contents under
// the requested path's name.
type workdirBlindFS struct {
	fakeContainerFS
	tree map[string]string // absolute dir -> listScript output
}

func (f *workdirBlindFS) Exec(_ context.Context, _, workdir string, cmd []string, _ int) (ExecResult, error) {
	_ = workdir // dropped on the floor, exactly as kubernetes drops it
	// Whatever the caller did NOT put in the argv, the shell runs in the image's default
	// working directory — "/" here. Modelling that rather than erroring is what makes
	// this test fail with the PRODUCTION symptom when the fix is reverted: a listing of
	// some other directory, served under the path that was asked for.
	dir := "/"
	if last := cmd[len(cmd)-1]; strings.HasPrefix(last, "/") && !strings.Contains(last, "\n") {
		dir = last
	}
	out, ok := f.tree[dir]
	if !ok {
		return ExecResult{ExitCode: listExitMissing, ExitKnown: true}, nil
	}
	return ExecResult{Stdout: out, ExitKnown: true}, nil
}

// TestContainerListingSurvivesABackendThatDropsWorkingDir is the kube regression, found by
// e2e/scenarios/web-fs.star the first time it ran against a real cluster: a request for
// /etc came back as the contents of /, labelled "/etc".
func TestContainerListingSurvivesABackendThatDropsWorkingDir(t *testing.T) {
	s, _, _ := explorerServer(t, runningUpstream(t))
	s.cfs = &workdirBlindFS{tree: map[string]string{
		"/":    nulRec("d", "4096", "0", "755", "", "etc") + nulRec("d", "4096", "0", "755", "", "usr"),
		"/etc": nulRec("f", "12", "0", "644", "", "hostname"),
	}}

	var got fsListing
	if rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=virtual&path=proj-web/etc", &got); rec.Code != http.StatusOK {
		t.Fatalf("listing /etc: %d", rec.Code)
	}
	names := []string{}
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	if len(names) != 1 || names[0] != "hostname" {
		t.Fatalf("listing /etc returned %v — that is some other directory, served under the path that was asked for", names)
	}

	// The paired negative: a path that does not exist must 404 rather than fall back to
	// listing something. The script's own exit code carries that now, because cd's
	// message cannot tell "missing" from "not a directory" and busybox and GNU word it
	// differently anyway.
	if rec := doJSON(t, s, "GET", "/.cornus/web/fs?source=virtual&path=proj-web/nope", &got); rec.Code != http.StatusNotFound {
		t.Errorf("a missing container directory should be 404, got %d", rec.Code)
	}
}
