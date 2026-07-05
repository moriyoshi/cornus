package dockerproxy

import (
	"context"
	"io"
	"net"

	"cornus/pkg/api"
	"cornus/pkg/attachsession"
	"cornus/pkg/deploywire"
)

// deployAttacher is what the proxy needs from a cornus server client: open a
// long-lived deploy-attach session (blocking, serving caller-local mounts over
// 9P for the workload's lifetime) and query deployment status. *client.Client
// satisfies it; tests inject a fake.
type deployAttacher interface {
	DeployAttach(ctx context.Context, spec api.DeploySpec, events func(deploywire.Event)) error
	Status(ctx context.Context, name string) (api.DeployStatus, error)
	// Logs streams the named deployment's stdcopy-multiplexed log frames to w
	// until ctx is done or the stream ends.
	Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error
	// Stats streams the named deployment's Docker-format stats JSON to w until
	// ctx is done or the stream ends.
	Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error
	// StatPath returns metadata for path inside the named deployment.
	StatPath(ctx context.Context, name, path string) (api.PathStat, error)
	// CopyFrom writes a tar of path (from the named deployment) to w and returns
	// the path's stat.
	CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error)
	// CopyTo extracts the tar read from r into path inside the named deployment.
	CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error
	// ExecCreate creates an exec in the named deployment and returns the backend
	// exec id (docker exec create).
	ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error)
	// ExecStart opens the exec-start tunnel for execID and returns a raw net.Conn
	// carrying the exec's bidirectional stdio stream (the proxy bridges it to the
	// hijacked docker CLI connection).
	ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error)
	// ExecInspect reports an exec's state (docker exec inspect).
	ExecInspect(ctx context.Context, execID string) (api.ExecState, error)
	// ExecResize resizes the exec's TTY to height rows by width columns (docker
	// exec resize, i.e. the SIGWINCH from a `docker exec -it` window change).
	ExecResize(ctx context.Context, execID string, height, width uint) error
	// Attach opens the attach tunnel for the named deployment and returns a raw
	// net.Conn carrying its bidirectional stdio stream (docker attach).
	Attach(ctx context.Context, name string, cfg api.AttachConfig) (net.Conn, error)
	// PortForward opens a raw tunnel to a container port of the named
	// deployment's first instance (one tunnel per connection) — the proxy
	// publishes each container's PortBindings on local listeners with it.
	PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error)
}

// session is one container's deploy-attach hold. The mechanics live in the shared
// attachsession package (the same primitive the client reconcile engine's
// mountController uses); the proxy keeps the imperative container state machine
// around it (state.go / containers.go). Opened with attachsession.Open, awaited
// with WaitReady, torn down with Stop, and observed via Done().
type session = attachsession.Session
