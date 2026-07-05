//go:build linux

package containerdhost

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy/containerdhost/logfmt"
)

// writeTestLog writes records to <dir>/containerd/logs/<id>.log and returns
// the file path.
func writeTestLog(t *testing.T, dir, id string, recs []logfmt.Record) string {
	t.Helper()
	path := filepath.Join(dir, "containerd", "logs", id+".log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := logfmt.NewWriter(f)
	for _, r := range recs {
		if err := w.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// demux runs streamLogFile (non-follow) and demultiplexes the stdcopy frames.
func demux(t *testing.T, path string, opts api.LogOptions) (stdout, stderr string) {
	t.Helper()
	var framed bytes.Buffer
	if err := streamLogFile(context.Background(), path, opts, &framed); err != nil {
		t.Fatalf("streamLogFile: %v", err)
	}
	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, &framed); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	return outBuf.String(), errBuf.String()
}

var testBase = time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

func testRecords() []logfmt.Record {
	return []logfmt.Record{
		{Time: testBase, Stream: "stdout", Log: "out one\n"},
		{Time: testBase.Add(1 * time.Second), Stream: "stderr", Log: "err one\n"},
		{Time: testBase.Add(2 * time.Second), Stream: "stdout", Log: "out two\n"},
		{Time: testBase.Add(3 * time.Second), Stream: "stderr", Log: "err two\n"},
	}
}

func TestStreamLogFileDemux(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "c1", testRecords())

	stdout, stderr := demux(t, path, api.LogOptions{})
	if stdout != "out one\nout two\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "err one\nerr two\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestStreamLogFileStreamSelection(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "c1", testRecords())

	stdout, stderr := demux(t, path, api.LogOptions{Stdout: true})
	if stdout != "out one\nout two\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	stdout, stderr = demux(t, path, api.LogOptions{Stderr: true})
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr != "err one\nerr two\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestStreamLogFileTail(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "c1", testRecords())

	stdout, stderr := demux(t, path, api.LogOptions{Tail: "2"})
	if stdout != "out two\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "err two\n" {
		t.Errorf("stderr = %q", stderr)
	}

	stdout, stderr = demux(t, path, api.LogOptions{Tail: "all"})
	if stdout != "out one\nout two\n" || stderr != "err one\nerr two\n" {
		t.Errorf("tail=all got stdout=%q stderr=%q", stdout, stderr)
	}

	var sink bytes.Buffer
	if err := streamLogFile(context.Background(), path, api.LogOptions{Tail: "bogus"}, &sink); err == nil {
		t.Error("tail=bogus: want error, got nil")
	}
}

func TestStreamLogFileSince(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "c1", testRecords())

	// RFC3339: cut off the first two records.
	since := testBase.Add(2 * time.Second).Format(time.RFC3339)
	stdout, stderr := demux(t, path, api.LogOptions{Since: since})
	if stdout != "out two\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "err two\n" {
		t.Errorf("stderr = %q", stderr)
	}

	// Unix seconds form.
	stdout, _ = demux(t, path, api.LogOptions{Since: "1"}) // 1970: everything matches
	if stdout != "out one\nout two\n" {
		t.Errorf("unix since stdout = %q", stdout)
	}

	var sink bytes.Buffer
	if err := streamLogFile(context.Background(), path, api.LogOptions{Since: "not-a-time"}, &sink); err == nil {
		t.Error("since=not-a-time: want error, got nil")
	}
}

func TestStreamLogFileTimestamps(t *testing.T) {
	recs := []logfmt.Record{
		{Time: testBase, Stream: "stdout", Log: "hello\n"},
		// A split long line: two partial chunks then the terminator. Only the
		// first chunk of the run gets a timestamp prefix.
		{Time: testBase.Add(time.Second), Stream: "stdout", Log: "aaa", Partial: true},
		{Time: testBase.Add(time.Second), Stream: "stdout", Log: "bbb", Partial: true},
		{Time: testBase.Add(time.Second), Stream: "stdout", Log: "ccc\n"},
	}
	path := writeTestLog(t, t.TempDir(), "c1", recs)

	stdout, _ := demux(t, path, api.LogOptions{Timestamps: true})
	want := testBase.Format(time.RFC3339Nano) + " hello\n" +
		testBase.Add(time.Second).Format(time.RFC3339Nano) + " aaabbbccc\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestStreamLogFileMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "containerd", "logs", "nope.log")
	var framed bytes.Buffer
	if err := streamLogFile(context.Background(), path, api.LogOptions{}, &framed); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if framed.Len() != 0 {
		t.Errorf("missing file produced %d bytes of output", framed.Len())
	}
}

// syncBuffer is a mutex-guarded bytes.Buffer usable across goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStreamLogFileFollow(t *testing.T) {
	dir := t.TempDir()
	path := writeTestLog(t, dir, "c1", []logfmt.Record{
		{Time: testBase, Stream: "stdout", Log: "first\n"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	streamDone := make(chan error, 1)
	go func() {
		err := streamLogFile(ctx, path, api.LogOptions{Follow: true}, pw)
		pw.Close()
		streamDone <- err
	}()
	var out, errOut syncBuffer
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = stdcopy.StdCopy(&out, &errOut, pr)
	}()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for out.String() != want {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: stdout = %q, want %q", out.String(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("first\n")

	// Append a record after the initial drain; the follower must pick it up.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	w := logfmt.NewWriter(f)
	if err := w.WriteRecord(logfmt.Record{Time: testBase.Add(time.Second), Stream: "stdout", Log: "second\n"}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	waitFor("first\nsecond\n")

	cancel()
	select {
	case err := <-streamDone:
		if err != nil {
			t.Errorf("streamLogFile returned %v after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streamLogFile did not return after ctx cancel")
	}
	<-copyDone
}

func TestStreamLogFileFollowMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "containerd", "logs", "late.log")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	streamDone := make(chan error, 1)
	go func() {
		err := streamLogFile(ctx, path, api.LogOptions{Follow: true}, pw)
		pw.Close()
		streamDone <- err
	}()
	var out, errOut syncBuffer
	go func() { _, _ = stdcopy.StdCopy(&out, &errOut, pr) }()

	// The file appears only after the follow started.
	time.Sleep(50 * time.Millisecond)
	writeTestLog(t, dir, "late", []logfmt.Record{
		{Time: testBase, Stream: "stdout", Log: "late line\n"},
	})

	deadline := time.Now().Add(5 * time.Second)
	for out.String() != "late line\n" {
		if time.Now().After(deadline) {
			t.Fatalf("timeout: stdout = %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-streamDone; err != nil {
		t.Errorf("streamLogFile returned %v after cancel", err)
	}
}

func TestLogMaxBytes(t *testing.T) {
	t.Setenv(logMaxBytesEnv, "")
	if got := logMaxBytes(); got != defaultLogMaxBytes {
		t.Errorf("default = %d, want %d", got, defaultLogMaxBytes)
	}
	t.Setenv(logMaxBytesEnv, "1024")
	if got := logMaxBytes(); got != 1024 {
		t.Errorf("override = %d, want 1024", got)
	}
	for _, bad := range []string{"bogus", "-5", "0"} {
		t.Setenv(logMaxBytesEnv, bad)
		if got := logMaxBytes(); got != defaultLogMaxBytes {
			t.Errorf("%q = %d, want default %d", bad, got, defaultLogMaxBytes)
		}
	}
}

func TestRotateLogIfNeeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c1.log")

	// Missing live file: no-op.
	if err := rotateLogIfNeeded(path, 10); err != nil {
		t.Fatalf("missing file: %v", err)
	}

	// At or below the cap: not rotated.
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogIfNeeded(path, 10); err != nil {
		t.Fatalf("below cap: %v", err)
	}
	if _, err := os.Stat(rotatedLogPath(path)); !os.IsNotExist(err) {
		t.Fatal("below cap must not rotate")
	}

	// Above the cap: live moves to .1, no live file remains.
	if err := os.WriteFile(path, []byte("first generation over cap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogIfNeeded(path, 10); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("live file must be gone after rotation")
	}
	old, err := os.ReadFile(rotatedLogPath(path))
	if err != nil || string(old) != "first generation over cap" {
		t.Fatalf("rotated content = %q, %v", old, err)
	}

	// A second rotation replaces the old generation (exactly one is kept).
	if err := os.WriteFile(path, []byte("second generation over cap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogIfNeeded(path, 10); err != nil {
		t.Fatalf("second rotate: %v", err)
	}
	old, err = os.ReadFile(rotatedLogPath(path))
	if err != nil || string(old) != "second generation over cap" {
		t.Fatalf("rotated content after second rotation = %q, %v", old, err)
	}
}

func TestStreamLogFileReadsRotatedGeneration(t *testing.T) {
	dir := t.TempDir()
	path := writeTestLog(t, dir, "c1", []logfmt.Record{
		{Time: testBase.Add(time.Second), Stream: "stdout", Log: "new\n"},
	})
	// A rotated generation with older records.
	f, err := os.Create(rotatedLogPath(path))
	if err != nil {
		t.Fatal(err)
	}
	w := logfmt.NewWriter(f)
	if err := w.WriteRecord(logfmt.Record{Time: testBase, Stream: "stdout", Log: "old\n"}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	stdout, _ := demux(t, path, api.LogOptions{})
	if stdout != "old\nnew\n" {
		t.Errorf("stdout = %q, want rotated generation first", stdout)
	}
	// Tail counts across both generations, newest last.
	stdout, _ = demux(t, path, api.LogOptions{Tail: "1"})
	if stdout != "new\n" {
		t.Errorf("tail=1 stdout = %q", stdout)
	}
	stdout, _ = demux(t, path, api.LogOptions{Tail: "2"})
	if stdout != "old\nnew\n" {
		t.Errorf("tail=2 stdout = %q", stdout)
	}
}

func TestReadLogRecordsResetsOnShrunkenFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTestLog(t, dir, "c1", []logfmt.Record{
		{Time: testBase, Stream: "stdout", Log: "fresh\n"},
	})
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A follower's offset can exceed the file size after a rotation swapped in
	// a smaller fresh live file; the reader must restart from the top.
	recs, off, err := readLogRecords(path, st.Size()+1000)
	if err != nil {
		t.Fatalf("readLogRecords: %v", err)
	}
	if len(recs) != 1 || recs[0].Log != "fresh\n" {
		t.Fatalf("records = %+v", recs)
	}
	if off != st.Size() {
		t.Fatalf("offset = %d, want %d", off, st.Size())
	}
}

// TestStartRotatesOversizedLog drives rotation through the backend lifecycle:
// Stop then Start with an over-cap log file must rotate it before the fresh
// task (and shim) starts.
func TestStartRotatesOversizedLog(t *testing.T) {
	t.Setenv(logMaxBytesEnv, "10")
	f := newFakeClient()
	b, _ := newTestBackend(t, f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	path := b.logPath("cornus-web-0")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("way more than ten bytes of logs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background(), "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(rotatedLogPath(path)); err != nil {
		t.Fatalf("Start must rotate the oversized log: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("live log must have been moved aside")
	}
}

func TestLogPath(t *testing.T) {
	b := &Backend{dataDir: "/var/lib/cornus"}
	if got, want := b.logPath("abc"), "/var/lib/cornus/containerd/logs/abc.log"; got != want {
		t.Errorf("logPath = %q, want %q", got, want)
	}
}

func TestLogURI(t *testing.T) {
	b := &Backend{dataDir: t.TempDir()}
	uri, err := b.logURI("abc")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse %q: %v", uri, err)
	}
	if u.Scheme != "binary" {
		t.Errorf("scheme = %q, want binary", u.Scheme)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != exe {
		t.Errorf("path = %q, want the test executable %q", u.Path, exe)
	}
	q := u.Query()
	if len(q) != 1 {
		t.Errorf("query has %d keys, want exactly 1 (argv order is nondeterministic otherwise): %v", len(q), q)
	}
	if got, want := q.Get(logShimArg), b.logPath("abc"); got != want {
		t.Errorf("query[%s] = %q, want %q", logShimArg, got, want)
	}
	if !strings.HasPrefix(uri, "binary://") {
		t.Errorf("uri = %q, want binary:// prefix", uri)
	}
}

// TestStreamLogFileSinceDuration exercises the duration form of the shared
// since grammar (deploy.ParseSince): "<d> ago" relative to now. A huge
// lookback (10 years) deterministically includes every test record.
func TestStreamLogFileSinceDuration(t *testing.T) {
	path := writeTestLog(t, t.TempDir(), "c1", testRecords())
	stdout, stderr := demux(t, path, api.LogOptions{Since: "87600h"})
	if stdout != "out one\nout two\n" || stderr != "err one\nerr two\n" {
		t.Errorf("duration since: stdout=%q stderr=%q", stdout, stderr)
	}
}
