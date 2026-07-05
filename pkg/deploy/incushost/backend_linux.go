//go:build linux

package incushost

import (
	"fmt"
	"io"

	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
)

// incusConn is the narrow seam the backend uses to talk to the daemon. The
// methods return already-waited results (the real adapter runs Operation.Wait
// for async calls) so the backend logic — and the unit-test fake — deal in
// plain values, never Incus's Operation objects. Streaming exec is the sole
// exception: it needs the live Operation for its control channel, so Exec
// returns it directly.
type incusConn interface {
	// Instances lists application-container instances in the project.
	Instances() ([]incusapi.Instance, error)
	// Instance returns one instance, or (nil, nil) if it does not exist.
	Instance(name string) (*incusapi.Instance, error)
	// InstanceState returns an instance's live state, or (nil, nil) if it does
	// not exist.
	InstanceState(name string) (*incusapi.InstanceState, error)
	// CreateInstance creates req and waits for completion.
	CreateInstance(req incusapi.InstancesPost) error
	// SetInstanceState applies a lifecycle action (start/stop/restart/...) and
	// waits. A missing instance yields deploy.ErrNotFound (wrapped).
	SetInstanceState(name, action string, force bool, timeout int) error
	// DeleteInstance deletes name and waits. A missing instance is a no-op
	// success (delete-if-exists).
	DeleteInstance(name string) error
	// Exec starts a command and returns the live Operation for stream control.
	Exec(name string, req incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error)
	// GetFile / CreateFile bridge the cp paths.
	GetFile(name, path string) (io.ReadCloser, *incus.InstanceFileResponse, error)
	CreateFile(name, path string, args incus.InstanceFileArgs) error
	// ConsoleLog returns the instance's accumulated console log (an OCI app
	// container's PID-1 stdout/stderr), a raw unframed byte stream.
	ConsoleLog(name string) (io.ReadCloser, error)
	// Close releases the client connection.
	Close()
}

// realConn adapts an incus.InstanceServer (already scoped to the target project)
// to the incusConn seam, running Operation.Wait for the async lifecycle calls.
type realConn struct {
	srv incus.InstanceServer
}

func (c *realConn) Instances() ([]incusapi.Instance, error) {
	return c.srv.GetInstances(incusapi.InstanceTypeContainer)
}

func (c *realConn) Instance(name string) (*incusapi.Instance, error) {
	inst, _, err := c.srv.GetInstance(name)
	if err != nil {
		if isIncusNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return inst, nil
}

func (c *realConn) InstanceState(name string) (*incusapi.InstanceState, error) {
	st, _, err := c.srv.GetInstanceState(name)
	if err != nil {
		if isIncusNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return st, nil
}

func (c *realConn) CreateInstance(req incusapi.InstancesPost) error {
	op, err := c.srv.CreateInstance(req)
	if err != nil {
		return err
	}
	return op.Wait()
}

func (c *realConn) SetInstanceState(name, action string, force bool, timeout int) error {
	op, err := c.srv.UpdateInstanceState(name, incusapi.InstanceStatePut{
		Action:  action,
		Force:   force,
		Timeout: timeout,
	}, "")
	if err != nil {
		if isIncusNotFound(err) {
			return fmt.Errorf("incus: instance %q: %w", name, deploy.ErrNotFound)
		}
		return err
	}
	if err := op.Wait(); err != nil {
		if isIncusNotFound(err) {
			return fmt.Errorf("incus: instance %q: %w", name, deploy.ErrNotFound)
		}
		return err
	}
	return nil
}

func (c *realConn) DeleteInstance(name string) error {
	op, err := c.srv.DeleteInstance(name)
	if err != nil {
		if isIncusNotFound(err) {
			return nil
		}
		return err
	}
	if err := op.Wait(); err != nil && !isIncusNotFound(err) {
		return err
	}
	return nil
}

func (c *realConn) Exec(name string, req incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	return c.srv.ExecInstance(name, req, args)
}

func (c *realConn) GetFile(name, path string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	return c.srv.GetInstanceFile(name, path)
}

func (c *realConn) CreateFile(name, path string, args incus.InstanceFileArgs) error {
	return c.srv.CreateInstanceFile(name, path, args)
}

func (c *realConn) ConsoleLog(name string) (io.ReadCloser, error) {
	return c.srv.GetInstanceConsoleLog(name, nil)
}

func (c *realConn) Close() { c.srv.Disconnect() }

// Backend deploys OCI images as Incus application containers.
type Backend struct {
	conn    incusConn
	policy  hostpolicy.Policy
	dataDir string
	project string

	// execs tracks in-flight exec sessions (ExecCreate/Start/Inspect/Resize land
	// on the same server process, so an in-memory registry suffices).
	execs *execRegistry

	// remote / agentImage / companions carry the caretaker-companion wiring for
	// client-local mounts and port-forward (Phase 2); the fields are set from the
	// options so the surface is stable even before those paths land.
	remote     bool
	agentImage string
	companions *remotecompanion.Registry
}

var (
	_ deploy.Backend       = (*Backend)(nil)
	_ deploy.RemoteCapable = (*Backend)(nil)
)

// New connects to the Incus daemon per cfg (empty fields resolve from the
// environment) and returns a backend scoped to the configured project. By
// default it enforces a default-deny host-privilege policy; pass WithPolicy to
// relax it.
func New(cfg Config, opts ...Option) (deploy.Backend, error) {
	cfg = cfg.resolve()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	srv, err := incus.ConnectIncusUnix(cfg.Socket, nil)
	if err != nil {
		return nil, fmt.Errorf("incus: connecting to daemon at %s: %w (is incusd running and the socket accessible? set CORNUS_INCUS_SOCKET)", cfg.Socket, err)
	}
	srv = srv.UseProject(cfg.Project)
	return &Backend{
		conn:       &realConn{srv: srv},
		policy:     o.policy,
		dataDir:    cfg.DataDir,
		project:    cfg.Project,
		execs:      newExecRegistry(),
		remote:     o.remote,
		agentImage: o.agentImage,
		companions: o.companions,
	}, nil
}

// Name identifies the backend.
func (b *Backend) Name() string { return "incus" }

// Close releases the daemon connection.
func (b *Backend) Close() error {
	b.conn.Close()
	return nil
}

// Remote reports whether this backend was configured for the always-on remote
// companion path (CORNUS_INCUS_REMOTE). See deploy.RemoteCapable.
func (b *Backend) Remote() bool { return b.remote }

// isIncusNotFound reports whether err is Incus's "not found" API error, so the
// backend can map it to deploy.ErrNotFound / delete-if-exists.
func isIncusNotFound(err error) bool {
	return incusapi.StatusErrorCheck(err, 404)
}
