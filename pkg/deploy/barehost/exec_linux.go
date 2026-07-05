//go:build linux

package barehost

// Exec, attach, and port-forward for the bare backend, driving the OCI runtime
// directly. The exec-session registry mirrors containerdhost/exec_linux.go (exec
// create/start/inspect/resize must land on the same server); the difference is
// the runtime plumbing: `runc exec` with a pipe IO (non-TTY, stdcopy-framed) or a
// console socket (TTY, raw pty bridged to the caller), rather than containerd's
// task.Exec + cio. Attach is output-only (the log file); ForwardPort dials the
// instance's recorded CNI IP directly and splices.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/console"
	runc "github.com/containerd/go-runc"
	"github.com/docker/docker/pkg/stdcopy"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/wire"
)

// execRetention bounds how long a finished exec session is kept for a late
// ExecInspect before reaping (a var so tests can shorten it).
var execRetention = 10 * time.Minute

// execRegistry tracks exec sessions in memory (exec create/start/inspect/resize
// must land on the same server).
type execRegistry struct {
	mu       sync.Mutex
	sessions map[string]*execSession
}

type execSession struct {
	container string
	cfg       api.ExecConfig

	mu      sync.Mutex
	state   api.ExecState
	console console.Console // set for a running TTY exec, for out-of-band resize
	// pending window size buffered when a resize arrives before the pty exists.
	pendingW, pendingH uint16
	finishedAt         time.Time
}

func newExecRegistry() *execRegistry {
	return &execRegistry{sessions: map[string]*execSession{}}
}

func (r *execRegistry) add(container string, cfg api.ExecConfig) (string, *execSession) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := "exec-" + hex.EncodeToString(buf)
	sess := &execSession{container: container, cfg: cfg}
	r.mu.Lock()
	r.reapLocked(time.Now())
	r.sessions[id] = sess
	r.mu.Unlock()
	return id, sess
}

func (r *execRegistry) reapLocked(now time.Time) {
	for id, sess := range r.sessions {
		sess.mu.Lock()
		fin := sess.finishedAt
		sess.mu.Unlock()
		if !fin.IsZero() && now.Sub(fin) >= execRetention {
			delete(r.sessions, id)
		}
	}
}

func (r *execRegistry) get(id string) (*execSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sess, ok := r.sessions[id]
	if !ok {
		return nil, fmt.Errorf("bare: unknown exec %q (exec sessions are per-server)", id)
	}
	return sess, nil
}

func (s *execSession) setRunning() {
	s.mu.Lock()
	s.state = api.ExecState{Running: true}
	s.mu.Unlock()
}

func (s *execSession) setFinished(code int) {
	s.mu.Lock()
	s.state = api.ExecState{Running: false, ExitCode: code}
	s.finishedAt = time.Now()
	s.mu.Unlock()
}

// firstRunningInstance resolves a deployment name to its replica-0 record and
// verifies it is running (exec/attach/forward target the first instance only).
func (b *Backend) firstRunningInstance(ctx context.Context, name string) (*instanceRecord, error) {
	recs, err := b.recordsForApp(name)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	rec := recs[0]
	st, err := b.rt.State(ctx, rec.ID)
	if err != nil || st.Status != runcStateRunning {
		return nil, fmt.Errorf("bare: instance %s is not running", rec.ID)
	}
	return rec, nil
}

// ExecCreate registers an exec against the deployment's first instance.
func (b *Backend) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	rec, err := b.firstRunningInstance(ctx, name)
	if err != nil {
		return "", err
	}
	id, _ := b.execs.add(rec.ID, cfg)
	return id, nil
}

// readBundleConfig parses a container's OCI runtime spec from its bundle, the
// baseline an exec's process spec inherits (env/cwd/user).
func readBundleConfig(bundleDir string) (*specs.Spec, error) {
	data, err := os.ReadFile(bundleDir + "/config.json")
	if err != nil {
		return nil, fmt.Errorf("bare: read bundle config: %w", err)
	}
	var s specs.Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("bare: parse bundle config: %w", err)
	}
	return &s, nil
}

// execProcessSpec derives the exec's OCI process spec from the container's:
// same env/cwd/user baseline, overridden per cfg. (Mirrors containerdhost.)
func execProcessSpec(base *specs.Spec, cfg api.ExecConfig) (*specs.Process, error) {
	p := &specs.Process{}
	if base.Process != nil {
		*p = *base.Process
	}
	p.Args = cfg.Cmd
	p.Terminal = cfg.Tty
	if len(cfg.Env) > 0 {
		p.Env = append(append([]string{}, p.Env...), cfg.Env...)
	}
	if cfg.WorkingDir != "" {
		p.Cwd = cfg.WorkingDir
	}
	if p.Cwd == "" {
		p.Cwd = "/"
	}
	if cfg.User != "" {
		parts := strings.SplitN(cfg.User, ":", 2)
		uid, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("bare: exec user %q: only numeric uid[:gid] is supported", cfg.User)
		}
		p.User = specs.User{UID: uint32(uid)}
		if len(parts) == 2 {
			gid, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, fmt.Errorf("bare: exec user %q: only numeric uid[:gid] is supported", cfg.User)
			}
			p.User.GID = uint32(gid)
		}
	}
	return p, nil
}

// ExecStart runs a created exec and bridges conn to its stdio, returning when the
// process exits. Its exit code is recorded on the session (ExecInspect).
func (b *Backend) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig, conn io.ReadWriteCloser) error {
	sess, err := b.execs.get(execID)
	if err != nil {
		return err
	}
	rec, err := b.readRecord(sess.container)
	if err != nil {
		return err
	}
	base, err := readBundleConfig(rec.BundleDir)
	if err != nil {
		return err
	}
	pspec, err := execProcessSpec(base, sess.cfg)
	if err != nil {
		return err
	}
	if sess.cfg.Tty {
		return b.execTTY(ctx, sess, *pspec, conn)
	}
	return b.execPipe(ctx, sess, *pspec, conn)
}

// execPipe runs a non-TTY exec: stdin is an os.Pipe (so runc uses the fd directly
// and go-runc's Wait does not block on the caller keeping stdin open), stdout and
// stderr are stdcopy-framed onto conn. EOF on conn half-closes the process stdin.
func (b *Backend) execPipe(ctx context.Context, sess *execSession, pspec specs.Process, conn io.ReadWriteCloser) error {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return err
	}
	eio := &execPipeIO{
		stdin:  stdinR,
		stdout: stdcopy.NewStdWriter(conn, stdcopy.Stdout),
		stderr: stdcopy.NewStdWriter(conn, stdcopy.Stderr),
	}
	go func() {
		_, _ = io.Copy(stdinW, conn) // conn -> process stdin
		_ = stdinW.Close()           // EOF half-closes the process stdin
	}()
	sess.setRunning()
	err = b.rt.Exec(ctx, sess.container, pspec, runtimeExecOpts{IO: eio})
	_ = stdinR.Close()
	sess.setFinished(execExitCode(err))
	_ = conn.Close() // unblock the stdin pump if the caller never sent EOF
	return nil
}

// execPipeIO wires a non-TTY exec's stdio into the runc command. Stdin is the
// pipe read end (an *os.File, used directly); stdout/stderr are the stdcopy
// writers (io.Writers os/exec drains before Wait returns).
type execPipeIO struct {
	stdin  io.ReadCloser
	stdout io.Writer
	stderr io.Writer
}

func (e *execPipeIO) Stdin() io.WriteCloser { return nil }
func (e *execPipeIO) Stdout() io.ReadCloser { return nil }
func (e *execPipeIO) Stderr() io.ReadCloser { return nil }
func (e *execPipeIO) Set(cmd *exec.Cmd) {
	cmd.Stdin = e.stdin
	cmd.Stdout = e.stdout
	cmd.Stderr = e.stderr
}
func (e *execPipeIO) Close() error { return nil }

// execTTY runs a TTY exec: with a console socket + Detach, runc allocates a pty,
// sends its master over the socket, and returns (leaving the exec process running
// on the pty). The master is bridged raw to conn (no stdcopy framing — a TTY
// multiplexes both streams) and resized on demand; the process's exit is observed
// as the pty closing. Because runc detaches, its own exit code is not the exec
// process's, so a TTY exec reports 0 on clean pty close (exec.star asserts TTY
// *output*, not the code — the exit-code contract is covered by non-TTY exec).
func (b *Backend) execTTY(ctx context.Context, sess *execSession, pspec specs.Process, conn io.ReadWriteCloser) error {
	sock, err := runc.NewTempConsoleSocket()
	if err != nil {
		return fmt.Errorf("bare: exec console socket: %w", err)
	}
	defer sock.Close()

	masterCh := make(chan console.Console, 1)
	go func() {
		m, err := sock.ReceiveMaster()
		if err != nil {
			close(masterCh)
			return
		}
		masterCh <- m
	}()
	execErr := make(chan error, 1)
	go func() {
		execErr <- b.rt.Exec(ctx, sess.container, pspec, runtimeExecOpts{ConsoleSocket: sock, Detach: true})
	}()

	// runc sends the master then detaches, so the master arrives promptly. Wait
	// for it, but bail if runc errors before sending one (bad command, no pty).
	var master console.Console
	select {
	case m, ok := <-masterCh:
		if ok {
			master = m
		}
	case <-time.After(30 * time.Second):
	}
	if master == nil {
		var e error
		select {
		case e = <-execErr:
		default:
			e = fmt.Errorf("bare: tty exec: no pty master received")
		}
		sess.setFinished(execExitCode(e))
		_ = conn.Close()
		return nil
	}

	sess.mu.Lock()
	sess.console = master
	pw, ph := sess.pendingW, sess.pendingH
	sess.pendingW, sess.pendingH = 0, 0
	sess.state = api.ExecState{Running: true}
	sess.mu.Unlock()
	if pw > 0 && ph > 0 {
		_ = master.Resize(console.WinSize{Height: ph, Width: pw})
	}

	go func() { _, _ = io.Copy(master, conn) }() // conn -> pty input
	_, _ = io.Copy(conn, master)                 // pty output -> conn (ends when the pty closes = process exit)

	sess.mu.Lock()
	sess.console = nil
	sess.mu.Unlock()
	_ = master.Close()
	sess.setFinished(0)
	_ = conn.Close()
	return nil
}

// ExecInspect reports an exec's state.
func (b *Backend) ExecInspect(ctx context.Context, execID string) (api.ExecState, error) {
	sess, err := b.execs.get(execID)
	if err != nil {
		return api.ExecState{}, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.state, nil
}

// ExecResize resizes a running TTY exec's pty (out-of-band). A resize before the
// pty exists is buffered and applied when the exec starts; after exit it is a no-op.
func (b *Backend) ExecResize(ctx context.Context, execID string, height, width uint) error {
	sess, err := b.execs.get(execID)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	c := sess.console
	if c == nil {
		sess.pendingW, sess.pendingH = uint16(width), uint16(height)
	}
	sess.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Resize(console.WinSize{Height: uint16(height), Width: uint16(width)})
}

// Attach streams the deployment's first instance output to conn (docker attach).
// The instance's stdio is captured to its log file, so attach is output-only:
// it replays/follows that file in stdcopy framing (attaching stdin is not
// supported — M1's file-backed stdio has no stdin path).
func (b *Backend) Attach(ctx context.Context, name string, cfg api.AttachConfig, conn io.ReadWriteCloser) error {
	if cfg.Stdin {
		return fmt.Errorf("bare: attach stdin is not supported (container stdio is captured to the log file)")
	}
	recs, err := b.recordsForApp(name)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	opts := api.LogOptions{Follow: cfg.Stream, Stdout: cfg.Stdout, Stderr: cfg.Stderr}
	if !cfg.Logs {
		opts.Tail = "0"
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, conn) // client-side close cancels the stream
		cancel()
	}()
	err = streamRawLog(sctx, recs[0].LogPath, opts, conn)
	_ = conn.Close()
	return err
}

// ForwardPort bridges conn to a port inside the deployment's first instance by
// dialing its CNI-assigned IP directly (the bridge makes it routable from the
// host), so it reaches ports the instance never published. proto is "tcp" (or
// empty) or "udp".
func (b *Backend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if proto != "" && proto != "tcp" && proto != "udp" {
		return fmt.Errorf("bare: unsupported port-forward protocol %q (only tcp and udp)", proto)
	}
	recs, err := b.recordsForApp(name)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("bare: no instances for deployment %q: %w", name, deploy.ErrNotFound)
	}
	ip := recs[0].IP
	if ip == "" {
		return fmt.Errorf("bare: instance %s has no recorded IP", recs[0].ID)
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var d net.Dialer
	if proto == "udp" {
		upstream, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return fmt.Errorf("bare: dial instance udp %s: %w", addr, err)
		}
		wire.BridgeDatagramStream(conn, upstream)
		return nil
	}
	upstream, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("bare: dial instance %s:%d: %w", ip, port, err)
	}
	return deploy.Bridge(conn, upstream)
}

// SupportsUDPPortForward reports that this backend can bridge udp ForwardPort
// tunnels (the server probes this before acking a UDP tunnel).
func (b *Backend) SupportsUDPPortForward() bool { return true }
