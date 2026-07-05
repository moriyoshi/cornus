//go:build linux

package barehost

// The exec session lifecycle and the stdio plumbing around it. The runtime is
// faked, but everything the backend itself owns is real: the session state
// machine, the exit code it records, the stdcopy framing on the wire, the resize
// buffering, and attach's refusals.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/console"
	runc "github.com/containerd/go-runc"
	"github.com/docker/docker/pkg/stdcopy"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// memConn is the caller side of an exec/attach hijacked connection: a fixed
// input script and a captured output stream.
type memConn struct {
	mu     sync.Mutex
	in     *bytes.Reader
	out    bytes.Buffer
	closed bool
}

func newMemConn(input string) *memConn { return &memConn{in: bytes.NewReader([]byte(input))} }

func (c *memConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.EOF
	}
	return c.in.Read(p)
}

func (c *memConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}

func (c *memConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *memConn) written() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.out.Bytes()...)
}

// demux splits a stdcopy-framed stream into its stdout and stderr halves.
func demux(t *testing.T, framed []byte) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errOut, bytes.NewReader(framed)); err != nil {
		t.Fatalf("demux stdcopy stream: %v", err)
	}
	return out.String(), errOut.String()
}

// seedRunnableInstance seeds a record whose fake container is running with a
// pid, and writes a real bundle config.json — the two things exec, copy, and
// stats all resolve before they touch the runtime.
func seedRunnableInstance(t *testing.T, b *Backend, rt *fakeRuntime, app string, replica int) *instanceRecord {
	t.Helper()
	rec := seedInstance(t, b, rt, app, replica, true)
	rt.cs[rec.ID].Pid = 4242
	if err := os.MkdirAll(rec.BundleDir, 0o711); err != nil {
		t.Fatal(err)
	}
	base := &specs.Spec{Process: &specs.Process{
		Env: []string{"PATH=/usr/bin", "APP=1"},
		Cwd: "/srv",
	}}
	if err := writeBundleConfig(rec.BundleDir, base); err != nil {
		t.Fatalf("writeBundleConfig: %v", err)
	}
	return rec
}

// --- session state machine ---

func TestExecSessionStateTransitions(t *testing.T) {
	sess := &execSession{}
	if sess.state.Running {
		t.Error("a fresh session must not report running")
	}
	sess.setRunning()
	if !sess.state.Running || sess.state.ExitCode != 0 {
		t.Errorf("after setRunning: %+v, want running with no exit code", sess.state)
	}
	if !sess.finishedAt.IsZero() {
		t.Error("a running session must not carry a finish time (it would be reaped)")
	}
	sess.setFinished(3)
	if sess.state.Running || sess.state.ExitCode != 3 {
		t.Errorf("after setFinished(3): %+v, want stopped with exit code 3", sess.state)
	}
	if sess.finishedAt.IsZero() {
		t.Error("a finished session must be stamped so the registry can reap it")
	}
}

func TestExecInspectReportsTheSessionState(t *testing.T) {
	b, _ := newTestBackend(t)
	id, sess := b.execs.add("cornus-web-0", api.ExecConfig{})

	st, err := b.ExecInspect(t.Context(), id)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if st.Running || st.ExitCode != 0 {
		t.Errorf("fresh session state = %+v, want not-running", st)
	}
	sess.setFinished(42)
	if st, err = b.ExecInspect(t.Context(), id); err != nil || st.ExitCode != 42 {
		t.Errorf("ExecInspect after finish = %+v, %v; want exit code 42", st, err)
	}
}

// TestExecOperationsOnAnUnknownSessionExplainWhy pins the per-server nature of
// the registry: an exec id from another server process is not silently ignored.
func TestExecOperationsOnAnUnknownSessionExplainWhy(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := t.Context()
	if _, err := b.ExecInspect(ctx, "exec-nope"); err == nil {
		t.Error("ExecInspect on an unknown session: want error")
	}
	if err := b.ExecResize(ctx, "exec-nope", 24, 80); err == nil {
		t.Error("ExecResize on an unknown session: want error")
	}
	if err := b.ExecStart(ctx, "exec-nope", api.ExecStartConfig{}, newMemConn("")); err == nil {
		t.Error("ExecStart on an unknown session: want error")
	}
}

// --- resize ---

// fakeConsole stands in for a pty master: it records the sizes it is asked for
// so a resize can be observed without allocating a real terminal.
type fakeConsole struct {
	mu    sync.Mutex
	sizes []console.WinSize
}

func (c *fakeConsole) Read([]byte) (int, error) { return 0, io.EOF }
func (c *fakeConsole) Write(p []byte) (int, error) {
	return len(p), nil
}
func (c *fakeConsole) Close() error { return nil }
func (c *fakeConsole) Resize(ws console.WinSize) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sizes = append(c.sizes, ws)
	return nil
}
func (c *fakeConsole) ResizeFrom(console.Console) error { return nil }
func (c *fakeConsole) SetRaw() error                    { return nil }
func (c *fakeConsole) DisableEcho() error               { return nil }
func (c *fakeConsole) Reset() error                     { return nil }
func (c *fakeConsole) Size() (console.WinSize, error)   { return console.WinSize{}, nil }
func (c *fakeConsole) Fd() uintptr                      { return 0 }
func (c *fakeConsole) Name() string                     { return "fake-pty" }
func (c *fakeConsole) recorded() []console.WinSize {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]console.WinSize(nil), c.sizes...)
}

var _ console.Console = (*fakeConsole)(nil)

// TestExecResizeBuffersUntilThePtyExists covers the ordering hazard the buffer
// exists for: a client can send its window size before the exec has started, and
// that size must not be lost.
func TestExecResizeBuffersUntilThePtyExists(t *testing.T) {
	b, _ := newTestBackend(t)
	id, sess := b.execs.add("cornus-web-0", api.ExecConfig{Tty: true})

	if err := b.ExecResize(t.Context(), id, 40, 120); err != nil {
		t.Fatalf("ExecResize before the pty exists = %v, want nil", err)
	}
	sess.mu.Lock()
	pw, ph := sess.pendingW, sess.pendingH
	sess.mu.Unlock()
	if pw != 120 || ph != 40 {
		t.Errorf("buffered size = %dx%d, want 120x40", pw, ph)
	}
}

// TestExecResizeForwardsToALivePty covers the other half: once the pty exists
// the resize goes straight through instead of being buffered.
func TestExecResizeForwardsToALivePty(t *testing.T) {
	b, _ := newTestBackend(t)
	id, sess := b.execs.add("cornus-web-0", api.ExecConfig{Tty: true})
	fc := &fakeConsole{}
	sess.mu.Lock()
	sess.console = fc
	sess.mu.Unlock()

	if err := b.ExecResize(t.Context(), id, 24, 80); err != nil {
		t.Fatalf("ExecResize: %v", err)
	}
	got := fc.recorded()
	if len(got) != 1 || got[0].Height != 24 || got[0].Width != 80 {
		t.Fatalf("pty resizes = %+v, want one 80x24", got)
	}
	sess.mu.Lock()
	pending := sess.pendingW
	sess.mu.Unlock()
	if pending != 0 {
		t.Errorf("a forwarded resize must not also be buffered, pendingW = %d", pending)
	}
}

// --- bundle config ---

func TestReadBundleConfig(t *testing.T) {
	dir := t.TempDir()
	if err := writeBundleConfig(dir, &specs.Spec{Version: "1.0.2", Process: &specs.Process{Cwd: "/srv"}}); err != nil {
		t.Fatal(err)
	}
	got, err := readBundleConfig(dir)
	if err != nil {
		t.Fatalf("readBundleConfig: %v", err)
	}
	if got.Process == nil || got.Process.Cwd != "/srv" {
		t.Errorf("parsed spec = %+v, want the container's process baseline", got.Process)
	}

	if _, err := readBundleConfig(filepath.Join(dir, "absent")); err == nil {
		t.Error("readBundleConfig without a bundle: want error")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBundleConfig(dir); err == nil {
		t.Error("readBundleConfig on a torn config.json: want error")
	}
}

// --- non-TTY exec, end to end over the fake runtime ---

// TestExecStartFramesOutputAndRecordsTheExitCode drives the whole non-TTY path:
// the session's process spec is derived from the bundle, the process's two output
// streams arrive stdcopy-framed and separable on the caller's connection, and its
// exit code lands on the session for a later inspect.
func TestExecStartFramesOutputAndRecordsTheExitCode(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedRunnableInstance(t, b, rt, "web", 0)

	var gotArgs, gotEnv []string
	rt.execFn = func(_ context.Context, _ string, process specs.Process, opts runtimeExecOpts) error {
		gotArgs, gotEnv = process.Args, process.Env
		runAsExecProcess(opts, func(_ io.Reader, stdout, stderr io.Writer) {
			_, _ = io.WriteString(stdout, "to stdout")
			_, _ = io.WriteString(stderr, "to stderr")
		})
		return &runc.ExitError{Status: 5}
	}

	execID, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"sh", "-c", "echo hi"}, Env: []string{"X=1"}})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	conn := newMemConn("")
	if err := b.ExecStart(ctx, execID, api.ExecStartConfig{}, conn); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}

	if len(gotArgs) != 3 || gotArgs[0] != "sh" {
		t.Errorf("exec args = %v, want the requested command", gotArgs)
	}
	if !hasOpt(gotEnv, "APP=1") || !hasOpt(gotEnv, "X=1") {
		t.Errorf("exec env = %v, want the container baseline plus the request's own", gotEnv)
	}
	out, errOut := demux(t, conn.written())
	if out != "to stdout" || errOut != "to stderr" {
		t.Errorf("demuxed streams = (%q, %q), want them framed separately", out, errOut)
	}
	st, err := b.ExecInspect(ctx, execID)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if st.Running || st.ExitCode != 5 {
		t.Errorf("session state = %+v, want a finished exec with code 5", st)
	}
}

// TestExecStartForwardsCallerInputToTheProcessStdin covers the other direction:
// bytes the client sends reach the process, and EOF on the connection half-closes
// its stdin rather than leaving it hanging.
func TestExecStartForwardsCallerInputToTheProcessStdin(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedRunnableInstance(t, b, rt, "web", 0)

	rt.execFn = func(_ context.Context, _ string, _ specs.Process, opts runtimeExecOpts) error {
		runAsExecProcess(opts, func(stdin io.Reader, stdout, _ io.Writer) {
			// Reading to EOF proves the pump closed the write end when the caller's
			// connection ended; without that this would block forever.
			data, _ := io.ReadAll(stdin)
			_, _ = stdout.Write(data)
		})
		return nil
	}

	execID, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"cat"}})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	conn := newMemConn("piped-in")
	done := make(chan error, 1)
	go func() { done <- b.ExecStart(ctx, execID, api.ExecStartConfig{}, conn) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecStart: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ExecStart blocked: the process stdin was never closed")
	}
	if out, _ := demux(t, conn.written()); out != "piped-in" {
		t.Errorf("echoed stdout = %q, want the caller's input", out)
	}
}

func TestExecStartWithoutABundleConfigFails(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, true)
	rt.cs[instanceName("web", 0)].Pid = 4242 // running, but no bundle was ever written

	execID, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"sh"}})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if err := b.ExecStart(ctx, execID, api.ExecStartConfig{}, newMemConn("")); err == nil {
		t.Error("ExecStart without a bundle config: want error")
	}
}

// TestExecStartRejectsANonNumericUser surfaces the unsupported-user refusal at
// start time rather than letting the runtime fail opaquely.
func TestExecStartRejectsANonNumericUser(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedRunnableInstance(t, b, rt, "web", 0)

	execID, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"id"}, User: "root"})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if err := b.ExecStart(ctx, execID, api.ExecStartConfig{}, newMemConn("")); err == nil {
		t.Error("ExecStart with a named user: want the numeric-uid refusal")
	}
}

// --- attach ---

// TestAttachRefusesStdin pins the documented limitation rather than silently
// dropping the client's keystrokes: the container's stdio is a log file, so there
// is no stdin to attach to.
func TestAttachRefusesStdin(t *testing.T) {
	b, rt := newTestBackend(t)
	seedInstance(t, b, rt, "web", 0, true)
	err := b.Attach(t.Context(), "web", api.AttachConfig{Stdin: true}, newMemConn(""))
	if err == nil {
		t.Fatal("attach with stdin: want a refusal")
	}
	if !strings.Contains(err.Error(), "stdin") {
		t.Errorf("error = %v, want it to name stdin", err)
	}
}

func TestAttachUnknownDeploymentWrapsErrNotFound(t *testing.T) {
	b, _ := newTestBackend(t)
	err := b.Attach(t.Context(), "ghost", api.AttachConfig{Stdout: true}, newMemConn(""))
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("Attach(unknown) = %v, want a wrap of ErrNotFound", err)
	}
}

// TestAttachReplaysTheInstanceLogFramed covers the output-only attach: prior
// output is replayed in the stdcopy framing every deploy client demuxes.
func TestAttachReplaysTheInstanceLogFramed(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, true)
	fio, err := newFileIO(rec.LogPath)
	if err != nil {
		t.Fatalf("newFileIO: %v", err)
	}
	if _, err := fio.log.WriteString("already-logged\n"); err != nil {
		t.Fatal(err)
	}
	fio.Close()

	conn := newMemConn("")
	if err := b.Attach(t.Context(), "web", api.AttachConfig{Stdout: true, Logs: true}, conn); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if out, _ := demux(t, conn.written()); out != "already-logged\n" {
		t.Errorf("attached output = %q, want the replayed log", out)
	}
}

// --- the stdio adapters the runtime drives ---

// TestExecPipeIOWiresTheProcessStreams pins the adapter contract go-runc relies
// on: the streams are handed over by mutating the command (so runc uses the fds
// directly), and the accessor methods stay nil so go-runc does not also try to
// pump them itself.
func TestExecPipeIOWiresTheProcessStreams(t *testing.T) {
	stdin, stdout, stderr := io.NopCloser(bytes.NewReader(nil)), &bytes.Buffer{}, &bytes.Buffer{}
	eio := &execPipeIO{stdin: stdin, stdout: stdout, stderr: stderr}
	var cmd exec.Cmd
	eio.Set(&cmd)
	if cmd.Stdin != stdin || cmd.Stdout != stdout || cmd.Stderr != stderr {
		t.Errorf("Set wired %v/%v/%v, want the session's own streams", cmd.Stdin, cmd.Stdout, cmd.Stderr)
	}
	if eio.Stdin() != nil || eio.Stdout() != nil || eio.Stderr() != nil {
		t.Error("the pipe accessors must stay nil; go-runc must not pump these streams itself")
	}
	if err := eio.Close(); err != nil {
		t.Errorf("Close = %v, want nil (the session owns the lifetimes)", err)
	}
}

// TestCopyIOLeavesUnsetStreamsAtTheRuntimeDefault matters because the copy paths
// deliberately wire only the streams they need: a copy-out sets stdout only, and
// forcing a nil stdin onto the command would give the process a closed stdin
// instead of the runtime's default.
func TestCopyIOLeavesUnsetStreamsAtTheRuntimeDefault(t *testing.T) {
	var out bytes.Buffer
	cio := &copyIO{stdout: &out}
	var cmd exec.Cmd
	cmd.Stdin = os.Stdin // pretend the runtime had a default in place
	cio.Set(&cmd)
	if cmd.Stdout != &out {
		t.Errorf("stdout = %v, want the copy's writer", cmd.Stdout)
	}
	if cmd.Stdin != os.Stdin {
		t.Errorf("stdin = %v, want the runtime default left untouched", cmd.Stdin)
	}
	if cmd.Stderr != nil {
		t.Errorf("stderr = %v, want it left unset", cmd.Stderr)
	}
	if cio.Stdin() != nil || cio.Stdout() != nil || cio.Stderr() != nil {
		t.Error("the copy accessors must stay nil")
	}
	if err := cio.Close(); err != nil {
		t.Errorf("Close = %v, want nil", err)
	}
}

// TestFileIOGivesTheContainerTheLogFileDirectly pins the restart-safety choice:
// the container inherits the log fd (not a pipe the server holds), and its stdin
// is /dev/null.
func TestFileIOGivesTheContainerTheLogFileDirectly(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "cornus-web-0.log")
	fio, err := newFileIO(logPath)
	if err != nil {
		t.Fatalf("newFileIO: %v", err)
	}
	defer fio.Close()
	var cmd exec.Cmd
	fio.Set(&cmd)
	if cmd.Stdout != fio.log || cmd.Stderr != fio.log {
		t.Error("both output streams must be the log file itself")
	}
	if cmd.Stdin != fio.null {
		t.Error("stdin must be /dev/null")
	}
	if fio.Stdin() != nil || fio.Stdout() != nil || fio.Stderr() != nil {
		t.Error("the file-backed accessors must stay nil; go-runc must not pump the log through the server")
	}
	if _, err := os.Stat(filepath.Dir(logPath)); err != nil {
		t.Errorf("the logs directory should have been created: %v", err)
	}
}
