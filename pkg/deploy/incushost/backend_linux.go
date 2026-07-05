//go:build linux

package incushost

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/gorilla/websocket"
	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"
	"github.com/pkg/sftp"

	"cornus/pkg/deploy"
	"cornus/pkg/deploy/healthengine"
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
	// SFTP opens the daemon's file channel into an instance, which is what
	// serves structured filesystem operations (fsop_linux.go). The caller closes
	// it. Distinct from GetFile/CreateFile because the channel can rename and
	// readdir, which that API cannot express at all.
	SFTP(name string) (*sftp.Client, error)
	// ConsoleLog returns the instance's accumulated console log (an OCI app
	// container's PID-1 stdout/stderr), a raw unframed byte stream.
	ConsoleLog(name string) (io.ReadCloser, error)
	// ConsoleAttach attaches to the instance's LIVE console device and returns
	// the streaming function Incus hands back (calling it mirrors the console
	// onto the given stream until it ends) plus a stop func that detaches. It is
	// what makes Logs --follow possible: ConsoleLog is a one-shot snapshot of the
	// ring buffer, this is the tail of it as it grows.
	//
	// stop MUST be idempotent and safe to call concurrently: Logs calls it both
	// from its deferred teardown and from the goroutine watching the request
	// context, and whichever fires first must not make the second a double-close.
	ConsoleAttach(name string) (stream func(io.ReadWriteCloser) error, stop func(), err error)
	// CreateVolume creates a custom filesystem storage volume in pool. An
	// already-existing volume is not an error (create-if-absent).
	CreateVolume(pool, name string, config map[string]string) error
	// DeleteVolume deletes a custom storage volume; a missing volume is a no-op
	// success (delete-if-exists).
	DeleteVolume(pool, name string) error
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

func (c *realConn) SFTP(name string) (*sftp.Client, error) {
	return c.srv.GetInstanceFileSFTP(name)
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

// ConsoleAttach opens a dynamic console session. Force is set because these are
// cornus-managed instances and a `cornus logs --follow` must not fail merely
// because an earlier follow's session has not been reaped yet — Incus allows a
// single console client per instance, so without it a re-run races the teardown
// of its predecessor. The control channel is accepted and ignored: cornus never
// resizes a log-follow console.
func (c *realConn) ConsoleAttach(name string) (func(io.ReadWriteCloser) error, func(), error) {
	disconnect := make(chan bool)
	args := &incus.InstanceConsoleArgs{
		Control:           func(*websocket.Conn) {},
		ConsoleDisconnect: disconnect,
	}
	_, stream, err := c.srv.ConsoleInstanceDynamic(name, incusapi.InstanceConsolePost{Type: "console", Force: true}, args)
	if err != nil {
		return nil, nil, err
	}
	return stream, sync.OnceFunc(func() { close(disconnect) }), nil
}

func (c *realConn) CreateVolume(pool, name string, config map[string]string) error {
	err := c.srv.CreateStoragePoolVolume(pool, incusapi.StorageVolumesPost{
		Name:             name,
		Type:             "custom",
		ContentType:      "filesystem",
		StorageVolumePut: incusapi.StorageVolumePut{Config: config},
	})
	// Create-if-absent: a companion re-apply reuses the volume that is already
	// there rather than failing the deploy on a name clash it caused itself.
	if err != nil && incusapi.StatusErrorCheck(err, 409) {
		return nil
	}
	return err
}

func (c *realConn) DeleteVolume(pool, name string) error {
	if err := c.srv.DeleteStoragePoolVolume(pool, "custom", name); err != nil && !isIncusNotFound(err) {
		return err
	}
	return nil
}

func (c *realConn) Close() { c.srv.Disconnect() }

// Backend deploys OCI images as Incus application containers.
type Backend struct {
	conn    incusConn
	policy  hostpolicy.Policy
	dataDir string
	project string
	pool    string

	// execs tracks in-flight exec sessions (ExecCreate/Start/Inspect/Resize land
	// on the same server process, so an in-memory registry suffices).
	execs *execRegistry
	// health runs this backend's container health probes; incus has no
	// instance-level probe of its own (health_linux.go).
	health *healthengine.Engine
	// rearmMu / rearmed guard the once-per-backend startup pass that restores
	// probing for instances that outlived the server (health_linux.go).
	rearmMu sync.Mutex
	rearmed bool
	// chown owns credential directories into an instance's id range. nil means
	// the real syscall; tests replace it to observe the ids without root
	// (credential_file_linux.go).
	chown func(path string, uid, gid int) error

	// remote / agentImage / companions drive the caretaker-companion path: in
	// remote mode every replica gets a sibling companion instance running
	// `cornus caretaker` with the PortForward and AgentRelay roles, so
	// ForwardPort reaches the workload through the companion's connection and an
	// exec can carry a forwarded ssh-agent. See companion_linux.go.
	remote     bool
	agentImage string
	companions *remotecompanion.Registry
	creds      deploy.RegistryCredentials

	// isolatedNetwork records that this server runs in a container with its own
	// netns, so it has no route to an instance's bridge address. Diagnostic only:
	// it is read solely to explain a ForwardPort dial that already failed (see
	// WithIsolatedNetwork).
	isolatedNetwork bool

	// selfInstance names the incus instance this cornus IS, when it runs as one on
	// this daemon (see WithSelfInstance). Also diagnostic only, and it CANCELS
	// isolatedNetwork's explanation: an instance is a peer of the workloads on
	// incusd's bridge, so a failed dial there needs a different explanation than
	// "you have no route".
	selfInstance string
}

var (
	_ deploy.Backend             = (*Backend)(nil)
	_ deploy.RemoteCapable       = (*Backend)(nil)
	_ deploy.AgentForwardCapable = (*Backend)(nil)
	_ deploy.VolumeRemover       = (*Backend)(nil)
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
	b := &Backend{
		conn:       &realConn{srv: srv},
		policy:     o.policy,
		dataDir:    cfg.DataDir,
		project:    cfg.Project,
		pool:       cfg.Pool,
		execs:      newExecRegistry(),
		remote:     o.remote,
		agentImage: o.agentImage,
		companions: o.companions,
		creds:      o.creds,

		isolatedNetwork: o.isolatedNetwork,
		selfInstance:    o.selfInstance,
	}
	// Built after b: the engine probes THROUGH the backend.
	b.health = healthengine.New(b.probeExec)
	return b, nil
}

// Name identifies the backend.
func (b *Backend) Name() string { return "incus" }

// Close releases the daemon connection.
func (b *Backend) Close() error {
	b.conn.Close()
	return nil
}

// Remote reports whether this backend was configured for the caretaker-companion
// path (CORNUS_INCUS_REMOTE). See deploy.RemoteCapable. In remote mode every
// replica gets an always-on companion instance carrying the PortForward and
// AgentRelay roles, exactly like dockerhost/containerdhost — see
// companion_linux.go for what the incus shape of that companion is and why it
// is a sibling instance rather than a netns-sharing sidecar.
func (b *Backend) Remote() bool { return b.remote }

// AgentForwardEnabled implements deploy.AgentForwardCapable: ssh-agent
// forwarding is available exactly when this backend runs companions, i.e. in
// remote mode. The companion carries the caretaker AgentRelayRole and listens
// on remotecompanion.AgentSocketPath inside the shared agent volume the app
// instance also mounts, which is the SSH_AUTH_SOCK the server injects.
//
// It stays an explicit answer rather than letting the server infer it from
// Remote(), because the two are only accidentally equal here: a future
// non-remote companion (an egress or mount caretaker) would carry no agent
// relay, and the inference would then hand out an SSH_AUTH_SOCK pointing at a
// socket nothing listens on.
func (b *Backend) AgentForwardEnabled(context.Context, string) (bool, error) {
	return b.remote, nil
}

// isIncusNotFound reports whether err is Incus's "not found" API error, so the
// backend can map it to deploy.ErrNotFound / delete-if-exists.
func isIncusNotFound(err error) bool {
	return incusapi.StatusErrorCheck(err, 404)
}
