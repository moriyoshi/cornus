//go:build linux

package containerdhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/docker/docker/pkg/stdcopy"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// execRetention bounds how long a finished exec session is kept around for a
// late ExecInspect before the registry reaps it. Without reaping the sessions
// map would grow without bound over a long-lived daemon (one entry per exec).
// It is a var so tests can shorten it.
var execRetention = 10 * time.Minute

// execRegistry tracks exec sessions in memory (the kubernetes backend's
// pattern; exec create/start/inspect/resize must land on the same server).
type execRegistry struct {
	mu       sync.Mutex
	sessions map[string]*execSession
}

type execSession struct {
	container string
	cfg       api.ExecConfig

	mu      sync.Mutex
	state   api.ExecState
	process ctd.Process // set once started, for out-of-band resize
	// pendingW/pendingH buffer a TTY size that arrived before the process
	// started (the client sends the initial window size as soon as the exec
	// stream opens, racing process.Start); ExecStart applies it.
	pendingW, pendingH uint32
	// finishedAt is stamped when ExecStart returns; a non-zero value makes the
	// session eligible for reaping once it is older than execRetention.
	finishedAt time.Time
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

// reapLocked drops finished sessions older than execRetention so the map does
// not grow without bound. The caller holds r.mu; each session's finishedAt is
// read under its own lock (r.mu is never taken while sess.mu is held, so this
// ordering cannot deadlock).
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
		return nil, fmt.Errorf("containerd: unknown exec %q (exec sessions are per-server)", id)
	}
	return sess, nil
}

// ExecCreate registers an exec against the deployment's first instance and
// returns its id. The process starts on ExecStart.
func (b *Backend) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	c, err := b.firstInstance(ctx, name)
	if err != nil {
		return "", err
	}
	if _, err := runningTask(b.ns(ctx), c); err != nil {
		return "", err
	}
	id, _ := b.execs.add(c.ID(), cfg)
	return id, nil
}

// execProcessSpec derives the exec's OCI process spec from the container's:
// same env/cwd/user baseline, overridden per cfg.
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
		// Numeric uid[:gid] only; resolving names needs the image's passwd
		// database, which a running exec cannot consult cheaply.
		parts := strings.SplitN(cfg.User, ":", 2)
		uid, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("containerd: exec user %q: only numeric uid[:gid] is supported", cfg.User)
		}
		p.User = specs.User{UID: uint32(uid)}
		if len(parts) == 2 {
			gid, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, fmt.Errorf("containerd: exec user %q: only numeric uid[:gid] is supported", cfg.User)
			}
			p.User.GID = uint32(gid)
		}
	}
	return p, nil
}

// ExecStart runs a created exec and bridges conn to its stdio. Non-TTY output
// is stdcopy-multiplexed; stdin EOF half-closes the process stdin (CloseIO)
// while output keeps flowing, and the call returns when the process exits and
// its output is drained.
func (b *Backend) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig, conn io.ReadWriteCloser) error {
	sess, err := b.execs.get(execID)
	if err != nil {
		return err
	}
	nctx := b.ns(ctx)
	c, err := b.client.LoadContainer(nctx, sess.container)
	if err != nil {
		return err
	}
	task, err := runningTask(nctx, c)
	if err != nil {
		return err
	}
	baseSpec, err := c.Spec(nctx)
	if err != nil {
		return err
	}
	pspec, err := execProcessSpec(baseSpec, sess.cfg)
	if err != nil {
		return err
	}

	stdinR, stdinW := io.Pipe()
	// When the process exits, containerd closes the stdin FIFO and cio stops
	// draining stdinR, so a pump goroutine mid-write on stdinW would block
	// forever (conn.Close only unblocks a conn Read, not a pipe Write). Closing
	// the read half on return makes any pending stdinW.Write return, reclaiming
	// the goroutine and the pipe.
	defer stdinR.CloseWithError(io.ErrClosedPipe)
	var stdout, stderr io.Writer
	if sess.cfg.Tty {
		stdout = conn
	} else {
		stdout = stdcopy.NewStdWriter(conn, stdcopy.Stdout)
		stderr = stdcopy.NewStdWriter(conn, stdcopy.Stderr)
	}
	ioOpts := []cio.Opt{cio.WithStreams(stdinR, stdout, stderr)}
	if sess.cfg.Tty {
		ioOpts = append(ioOpts, cio.WithTerminal)
	}

	process, err := task.Exec(nctx, execID, pspec, cio.NewCreator(ioOpts...))
	if err != nil {
		return fmt.Errorf("containerd: exec in %s: %w", sess.container, err)
	}
	waitCh, err := process.Wait(nctx)
	if err != nil {
		_, _ = process.Delete(nctx)
		return err
	}
	if err := process.Start(nctx); err != nil {
		_, _ = process.Delete(nctx)
		return err
	}
	sess.mu.Lock()
	sess.process = process
	sess.state = api.ExecState{Running: true, Pid: int(process.Pid())}
	pendingW, pendingH := sess.pendingW, sess.pendingH
	sess.pendingW, sess.pendingH = 0, 0
	sess.mu.Unlock()
	if sess.cfg.Tty && pendingW > 0 && pendingH > 0 {
		// Apply the buffered pre-start window size (see ExecResize).
		if err := process.Resize(nctx, pendingW, pendingH); err != nil {
			logging.FromContext(ctx, slog.Group("containerd", "exec", execID)).
				WarnContext(ctx, "initial exec TTY resize failed", "error", err)
		}
	}

	// stdin pump: conn -> process. EOF half-closes stdin, never the tunnel
	// (deploy.Bridge semantics, realized on containerd's CloseIO).
	go func() {
		_, _ = io.Copy(stdinW, conn)
		_ = stdinW.Close()
		_ = process.CloseIO(nctx, ctd.WithStdinCloser)
	}()

	exit := <-waitCh
	// Drain the fifo pumps so trailing output reaches conn before close.
	if pio := process.IO(); pio != nil {
		pio.Wait()
	}
	// Clear sess.process before deleting it: a concurrent ExecResize reads
	// sess.process under sess.mu, and calling Resize on an already-deleted
	// process surfaces a spurious "not found" error to the client. Nil-ing it
	// first makes such a racing resize a no-op.
	sess.mu.Lock()
	sess.process = nil
	sess.mu.Unlock()
	_, _ = process.Delete(nctx)
	code := int(exit.ExitCode())
	sess.mu.Lock()
	sess.state = api.ExecState{Running: false, ExitCode: code}
	sess.finishedAt = time.Now()
	sess.mu.Unlock()
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

// ExecResize resizes the exec's TTY (out-of-band, separate from the ExecStart
// stream). A resize before the process starts is buffered and applied by
// ExecStart (the client's initial window size races process.Start); a resize
// after exit is a no-op.
func (b *Backend) ExecResize(ctx context.Context, execID string, height, width uint) error {
	sess, err := b.execs.get(execID)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	process := sess.process
	if process == nil {
		sess.pendingW, sess.pendingH = uint32(width), uint32(height)
	}
	sess.mu.Unlock()
	if process == nil {
		return nil
	}
	return process.Resize(b.ns(ctx), uint32(width), uint32(height))
}

// Attach streams the deployment's first instance output to conn (docker
// attach). The log shim owns the task's stdio fifos, so attach is output-only:
// it replays or follows the instance's log file in stdcopy framing. Attaching
// stdin is not supported on this backend.
func (b *Backend) Attach(ctx context.Context, name string, cfg api.AttachConfig, conn io.ReadWriteCloser) error {
	if cfg.Stdin {
		return fmt.Errorf("containerd: attach stdin is not supported (task stdio is owned by the log shim)")
	}
	c, err := b.firstInstance(ctx, name)
	if err != nil {
		return err
	}
	opts := api.LogOptions{
		Follow: cfg.Stream,
		Stdout: cfg.Stdout,
		Stderr: cfg.Stderr,
	}
	if !cfg.Logs {
		// No replay: only output produced from now on.
		opts.Tail = "0"
	}
	// Cancel the stream when the client side goes away.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	err = streamLogFile(sctx, b.logPath(c.ID()), opts, conn)
	_ = conn.Close()
	return err
}

// ForwardPort bridges conn to a port inside the deployment's first instance by
// dialing its CNI-assigned IP directly (dockerhost parity), so it reaches ports
// the instance never published. The CNI bridge makes the instance IP routable
// from the host for both protocols, so no netns entry is needed. proto is "tcp"
// (or empty) or "udp": tcp splices the raw byte stream; udp opens a connected
// UDP socket to the instance and bridges conn's length-prefixed datagram frames
// (wire.WriteDatagram) to it, one tunnel per client flow.
//
// In remote mode (WithRemote) it reroutes through that instance's always-on
// remote-companion caretaker instead, exactly like dockerhost's ForwardPort:
// the companion joins the instance's pinned netns, so the server opens a
// server-initiated TagPortForward stream on the companion's connection
// (looked up in the per-instance registry) and relays through THAT.
func (b *Backend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if proto != "" && proto != "tcp" && proto != "udp" {
		return fmt.Errorf("containerd: unsupported port-forward protocol %q (only tcp and udp)", proto)
	}
	if b.remote {
		return b.forwardPortViaCompanion(ctx, name, port, proto, conn)
	}
	c, err := b.firstInstance(ctx, name)
	if err != nil {
		return err
	}
	labels, err := c.Labels(b.ns(ctx))
	if err != nil {
		return err
	}
	ip := labels[labelIP]
	if ip == "" {
		return fmt.Errorf("containerd: instance %s has no recorded IP", c.ID())
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var d net.Dialer
	if proto == "udp" {
		upstream, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return fmt.Errorf("containerd: dial instance udp %s: %w", addr, err)
		}
		wire.BridgeDatagramStream(conn, upstream)
		return nil
	}
	upstream, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("containerd: dial instance %s:%d: %w", ip, port, err)
	}
	return deploy.Bridge(conn, upstream)
}

// forwardPortViaCompanion reroutes ForwardPort through the deployment's first
// instance's remote-companion caretaker connection (looked up in the
// per-instance registry — always replica 0, matching firstInstance's existing
// "first instance only" scope). The companion joins that instance's pinned
// netns, so its PortForwardRole accept loop can dial 127.0.0.1:port even
// though the server itself cannot reach the instance directly.
func (b *Backend) forwardPortViaCompanion(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if b.companions == nil {
		return fmt.Errorf("containerd: remote mode has no companion registry configured")
	}
	instance := remotecompanion.InstanceKey(name, 0)
	sess := b.companions.Get(instance)
	if sess == nil {
		return fmt.Errorf("containerd: remote companion for %q is not connected yet", instance)
	}
	stream, err := wire.OpenPortForward(sess, port, proto)
	if err != nil {
		return fmt.Errorf("containerd: open port-forward relay to companion: %w", err)
	}
	if proto == "udp" {
		wire.BridgeDatagramStream(conn, stream)
		return nil
	}
	// wire.Pipe, not deploy.Bridge: a yamux stream has no CloseWrite, so
	// Bridge's half-close-on-client-EOF branch would silently no-op and leak
	// this stream until the companion's own upstream connection happens to
	// end for unrelated reasons. A port-forward tunnel has no stdin/stdout
	// asymmetry to preserve anyway — tear down as soon as either side ends.
	wire.Pipe(conn, stream)
	return nil
}

// SupportsUDPPortForward reports that this backend can bridge proto "udp"
// ForwardPort tunnels (framed datagrams to a connected UDP socket). The server's
// port-forward handler probes for this optional capability before acking a UDP
// tunnel.
func (b *Backend) SupportsUDPPortForward() bool { return true }
