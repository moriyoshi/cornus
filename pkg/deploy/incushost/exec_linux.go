//go:build linux

package incushost

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gorilla/websocket"
	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// execRegistry tracks in-flight exec sessions keyed by an opaque id. ExecCreate
// registers a session; ExecStart runs it and records its terminal state;
// ExecInspect/ExecResize look it up. It is server-local (a single process owns
// the exec), so a plain map under a mutex suffices.
type execRegistry struct {
	mu    sync.Mutex
	seq   int
	execs map[string]*execSession
}

func newExecRegistry() *execRegistry {
	return &execRegistry{execs: map[string]*execSession{}}
}

// execSession is one exec's lifecycle. control is set once ExecStart's websocket
// control handler fires, so ExecResize can push a window-resize to a live TTY.
// width/height hold the last requested terminal size: a resize can arrive before
// the exec starts (or before its control channel connects), so the size is
// remembered and applied both as the initial PTY size at start and via a
// window-resize once the control channel is live.
type execSession struct {
	instance string
	cfg      api.ExecConfig

	mu       sync.Mutex
	started  bool
	done     bool
	exitCode int
	control  *websocket.Conn
	width    int
	height   int
}

func (r *execRegistry) create(instance string, cfg api.ExecConfig) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("incusexec-%d", r.seq)
	r.execs[id] = &execSession{instance: instance, cfg: cfg}
	return id
}

func (r *execRegistry) get(id string) (*execSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.execs[id]
	return s, ok
}

// ExecCreate registers an exec against the deployment's first instance and
// returns its opaque id. It does not start the exec; ExecStart runs it.
func (b *Backend) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	id, err := b.firstInstance(name)
	if err != nil {
		return "", err
	}
	warnUnsupportedExec(ctx, name, cfg)
	return b.execs.create(id, cfg), nil
}

// ExecStart runs a previously-created exec, bridging conn to its stdio via Incus
// ExecInstance. For a non-TTY exec the process output is written back
// stdcopy-multiplexed (Docker's 8-byte stream header), satisfying the framing
// contract; a TTY exec is a single raw stream. It returns when the process exits.
func (b *Backend) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig, conn io.ReadWriteCloser) error {
	sess, ok := b.execs.get(execID)
	if !ok {
		return fmt.Errorf("incus: exec %q not found", execID)
	}
	post := buildExecPost(sess.cfg)
	// Seed the initial PTY size from any resize that already arrived (the client
	// usually sends the terminal size right after create, before/around start).
	sess.mu.Lock()
	if sess.width > 0 && sess.height > 0 {
		post.Width = sess.width
		post.Height = sess.height
	}
	sess.mu.Unlock()

	args := &incus.InstanceExecArgs{
		Stdin:    conn,
		DataDone: make(chan bool),
		Control: func(c *websocket.Conn) {
			// Capture the control channel and flush any size requested before it
			// was live, so a resize that raced ahead of the connection still lands.
			sess.mu.Lock()
			sess.control = c
			w, h := sess.width, sess.height
			sess.mu.Unlock()
			if w > 0 && h > 0 {
				_ = c.WriteJSON(resizeMsg(w, h))
			}
		},
	}
	if post.Interactive {
		args.Stdout = conn
		args.Stderr = conn
	} else {
		args.Stdout = stdcopy.NewStdWriter(conn, stdcopy.Stdout)
		args.Stderr = stdcopy.NewStdWriter(conn, stdcopy.Stderr)
	}

	sess.mu.Lock()
	sess.started = true
	sess.mu.Unlock()

	op, err := b.conn.Exec(sess.instance, post, args)
	if err != nil {
		return fmt.Errorf("incus: exec on %s: %w", sess.instance, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("incus: exec wait: %w", err)
	}
	<-args.DataDone

	sess.mu.Lock()
	sess.done = true
	sess.exitCode = execReturnCode(op)
	sess.mu.Unlock()
	return nil
}

// ExecInspect reports an exec's state. Incus does not surface the exec'd
// process's pid to the client, so Pid stays 0 (documented; matches kubernetes).
func (b *Backend) ExecInspect(ctx context.Context, execID string) (api.ExecState, error) {
	sess, ok := b.execs.get(execID)
	if !ok {
		return api.ExecState{}, fmt.Errorf("incus: exec %q not found", execID)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return api.ExecState{
		Running:  sess.started && !sess.done,
		ExitCode: sess.exitCode,
		Pid:      0,
	}, nil
}

// ExecResize records the requested terminal size and, if the exec's control
// channel is already live, pushes a window-resize to it. The size is remembered
// even when the channel is not yet up (or the exec has not started) so ExecStart
// can seed the initial PTY size and the control handler can flush it on connect.
func (b *Backend) ExecResize(ctx context.Context, execID string, height, width uint) error {
	sess, ok := b.execs.get(execID)
	if !ok {
		return fmt.Errorf("incus: exec %q not found", execID)
	}
	sess.mu.Lock()
	sess.width = int(width)
	sess.height = int(height)
	c := sess.control
	sess.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.WriteJSON(resizeMsg(int(width), int(height)))
}

// resizeMsg builds the Incus exec control message that resizes an interactive
// PTY to width columns by height rows.
func resizeMsg(width, height int) incusapi.InstanceExecControl {
	return incusapi.InstanceExecControl{
		Command: "window-resize",
		Args: map[string]string{
			"width":  strconv.Itoa(width),
			"height": strconv.Itoa(height),
		},
	}
}

// Attach is not supported on the incus backend: Incus exposes an instance
// CONSOLE (a single PTY stream to PID 1), which does not match docker attach's
// stream semantics (per-stream stdcopy framing, optional log replay) for an OCI
// application container. Use exec instead. This is a deliberate design decision,
// not a deferred stub.
func (b *Backend) Attach(ctx context.Context, name string, cfg api.AttachConfig, conn io.ReadWriteCloser) error {
	if _, err := b.firstInstance(name); err != nil {
		return err
	}
	return fmt.Errorf("incus: attach is not supported (Incus exposes a console, not docker-attach streams); use exec instead")
}

// buildExecPost maps an ExecConfig to the Incus exec request. It is pure so the
// mapping is unit-tested without a daemon.
func buildExecPost(cfg api.ExecConfig) incusapi.InstanceExecPost {
	post := incusapi.InstanceExecPost{
		Command:     cfg.Cmd,
		WaitForWS:   true,
		Interactive: cfg.Tty,
		Environment: map[string]string{},
		Cwd:         cfg.WorkingDir,
	}
	for _, kv := range cfg.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			post.Environment[k] = v
		}
	}
	if cfg.User != "" {
		if uid, err := strconv.ParseUint(cfg.User, 10, 32); err == nil {
			post.User = uint32(uid)
		}
	}
	return post
}

// execReturnCode extracts the process exit status from a finished exec
// operation's metadata ("return"), defaulting to 0.
func execReturnCode(op incus.Operation) int {
	md := op.Get().Metadata
	if md == nil {
		return 0
	}
	if v, ok := md["return"]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return 0
}

// warnUnsupportedExec surfaces exec-config fields Incus's exec API cannot honor,
// per the cross-backend "warn per-field, do not silently drop" contract.
func warnUnsupportedExec(ctx context.Context, name string, cfg api.ExecConfig) {
	log := logging.FromContext(ctx, slog.Group("incus", "deployment", name))
	if cfg.Privileged {
		log.WarnContext(ctx, "backend ignores exec Privileged (Incus exec has no privileged flag)")
	}
	if cfg.User != "" {
		if _, err := strconv.ParseUint(cfg.User, 10, 32); err != nil {
			log.WarnContext(ctx, "backend exec accepts only a numeric uid; user name ignored", "user", cfg.User)
		}
	}
	if cfg.ForwardAgent {
		log.WarnContext(ctx, "backend does not support exec ssh-agent forwarding")
	}
}
