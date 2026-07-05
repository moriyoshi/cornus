package dockerhost

// The engine seam.
//
// Everything in this package outside engine.go is ORCHESTRATION — Apply's
// create-or-recreate, the spec-hash idempotency fingerprint (reuse.go),
// self-preservation (self.go), host-route diagnosis (hostisolation.go,
// selfnet.go), the companion/mount-relay/egress machinery (mounts.go,
// egress.go, attachments.go), telemetry. None of it cares which daemon is
// behind the socket; it reaches the runtime exclusively through this interface.
//
// engine.go's *engineClient is the Docker Engine REST implementation. A second
// implementation speaking Podman's native libpod API can satisfy the same
// interface without duplicating any of the orchestration above, which is the
// whole point of naming the seam: the package hosts engines, and `dockerhost`
// is its historical name rather than a claim about what it drives.
//
// Two deliberate shapes, both learned from what a second implementation needs:
//
//   - There is no raw `hijack(method, path, body)` here. It used to be how
//     ExecStart and Attach reached the daemon, which put Docker REST PATHS in
//     the interface — a second engine could not implement it without pretending
//     to be Docker. The two callers are semantic methods instead (execStart,
//     containerAttach); *engineClient still hijacks internally.
//   - nonLocal() is a method, not the exported overTCP field it replaced, for
//     the same reason: "can this process reach the workload's own IP" is a
//     question every engine answers, but only the Docker client answers it by
//     looking at a DOCKER_HOST scheme.
//
// The types crossing this boundary (createBody, containerSummary,
// containerInspectResult) are Docker-SHAPED but engine-neutral in role: they are
// the package's internal currency, and a non-Docker engine translates them at
// its own edge rather than the orchestration learning a second vocabulary.

import (
	"context"
	"io"
	"net"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/hostenv"
)

// Engine is the runtime-facing surface the deploy orchestration depends on.
type Engine interface {
	// --- images ---

	// imagePull pulls ref, waiting for the transfer to finish, and reports a
	// failure the daemon streamed back rather than only an HTTP status.
	imagePull(ctx context.Context, ref string, credential *deploy.RegistryCredential) error
	// imageExists reports whether the runtime already has ref.
	imageExists(ctx context.Context, ref string) (bool, error)
	// imageInspect resolves ref to its CONTENT id (not the ref), which is what
	// makes the reuse fingerprint safe against a mutable tag. A missing image is
	// present=false with no error.
	imageInspect(ctx context.Context, ref string) (id string, present bool, err error)

	// --- container lifecycle ---

	containerCreate(ctx context.Context, name string, body createBody) (string, error)
	containerStart(ctx context.Context, id string) error
	containerStop(ctx context.Context, id string) error
	containerRestart(ctx context.Context, id string) error
	containerRemove(ctx context.Context, id string) error

	// --- container introspection ---

	// containerList returns cornus-managed containers carrying label.
	containerList(ctx context.Context, label string) ([]containerSummary, error)
	containerInspect(ctx context.Context, id string) (containerInspectResult, error)
	// containerAddresses returns the container's per-network addresses and the
	// network its primary address came from.
	containerAddresses(ctx context.Context, id string) (map[string]string, string, error)
	containerNetworks(ctx context.Context, id string) (map[string]string, error)

	// --- streams ---

	// containerLogs returns the log stream. The bytes MUST be stdcopy-framed for
	// a non-TTY container: deploy.Backend's contract requires it, and callers
	// demux unconditionally.
	containerLogs(ctx context.Context, id string, opts api.LogOptions) (io.ReadCloser, error)
	// containerStats returns the metrics stream, in DOCKER's stats JSON shape.
	// An engine whose runtime reports a different shape must translate here —
	// the bytes reach clients that parse them as docker stats.
	containerStats(ctx context.Context, id string, stream bool) (io.ReadCloser, error)

	// --- exec ---

	execCreate(ctx context.Context, id string, cfg api.ExecConfig) (string, error)
	// execStart runs a created exec and returns its raw bidirectional stdio
	// stream, already past any protocol upgrade. Non-TTY output MUST be
	// stdcopy-framed.
	execStart(ctx context.Context, execID string, tty bool) (net.Conn, error)
	execInspect(ctx context.Context, execID string) (api.ExecState, error)
	execResize(ctx context.Context, execID string, h, w uint) error
	// containerAttach returns the container's raw bidirectional stdio stream,
	// already past any protocol upgrade. Non-TTY output MUST be stdcopy-framed.
	containerAttach(ctx context.Context, id string, cfg api.AttachConfig) (net.Conn, error)

	// --- archive (docker cp semantics) ---

	containerArchiveStat(ctx context.Context, id, path string) (api.PathStat, error)
	containerArchiveGet(ctx context.Context, id, path string) (io.ReadCloser, api.PathStat, error)
	containerArchivePut(ctx context.Context, id, path string, r io.Reader, opts api.CopyToOptions) error

	// --- networks ---

	networkEnsure(ctx context.Context, net api.NetworkAttachment) error
	networkConnect(ctx context.Context, net api.NetworkAttachment, containerID string) error
	networkJoin(ctx context.Context, netName, containerID string) error
	networkLeave(ctx context.Context, netName, containerID string) error
	networkInspect(ctx context.Context, name string) (labels map[string]string, members []string, err error)
	networkDriver(ctx context.Context, name string) (string, error)
	networkRemove(ctx context.Context, name string) error

	// --- volumes ---

	volumeEnsure(ctx context.Context, v api.VolumeSpec) error
	volumeInspect(ctx context.Context, name string) (mountpoint string, err error)
	volumeRemove(ctx context.Context, name string) error

	// --- host relationship ---

	// selfInspect fetches the subset of a container's inspect that hostenv needs
	// to confirm this process's own identity on this runtime.
	selfInspect(ctx context.Context, id string) (hostenv.SelfInspect, error)
	// nonLocal reports that the endpoint may name a runtime on another machine,
	// so the workload's container IP need not be routable from here. It does not
	// PROVE remoteness (a tcp:// endpoint can be loopback) and is used only to
	// add a clause to an error that has already happened.
	nonLocal() bool
	// usernsRemapped reports whether the daemon maps container ids into a
	// separate host range (Docker's userns-remap). It matters because a file
	// this server writes must be owned by ids the WORKLOAD can see, and a
	// remapped daemon does not report the map — so the answer decides between
	// serving a credential file and refusing to.
	usernsRemapped(ctx context.Context) (bool, error)
}

// *engineClient is the Docker Engine REST implementation of Engine.
var _ Engine = (*engineClient)(nil)
