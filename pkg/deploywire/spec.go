// Package deploywire carries a long-lived remote cornus deployment over a
// single WebSocket, reusing pkg/wire's yamux-over-WebSocket transport (no
// BuildKit dependency). The control stream carries the DeploySpec out and
// status/log events back; the caller's bind-mount source directories are served
// on demand over 9P (wire's 'L' backing) and kernel-9p-mounted on the server,
// which rewrites the spec's mount sources before handing it to the deploy backend.
//
// Because the mounts are served from the caller, the caller must stay connected
// for the container's lifetime: when the control stream drops, the server tears
// the deployment down (a container bound to a dead 9P mount only gets EIO).
package deploywire

import "cornus/pkg/api"

// DeployAttachSpec is sent once by the caller on the control stream to start a
// remote deployment whose bind mounts are served from the caller.
type DeployAttachSpec struct {
	// Spec is the deployment to apply. For each LocalMount, the server rewrites
	// Spec.Mounts[Index].Source to a server-side kernel-9p mountpoint before
	// applying, so the deploy backend stays unaware of 9P.
	Spec api.DeploySpec `json:"spec"`
	// LocalMounts identifies which Spec.Mounts entries the caller serves over
	// 9P (by index) and the backing name it exports each under.
	LocalMounts []LocalMount `json:"localMounts,omitempty"`
	// CredentialSources are client-side credential backings the caller serves for
	// the lifetime of the session: for each, the caller runs the named source
	// backend and answers the workload's credential-fetch requests relayed through
	// the server. The workload may only fetch a name listed here (AllowsCredential).
	CredentialSources []CredentialBacking `json:"credentialSources,omitempty"`
}

// CredentialBacking ties a credential Name to the client-side source backend
// (pkg/credential) that mints it. The secret is never carried here — only the
// backend name and its non-secret config; the value is produced on the caller at
// fetch time.
type CredentialBacking struct {
	Name    string            `json:"name"`
	Backend string            `json:"backend"`
	Config  map[string]string `json:"config,omitempty"`
}

// LocalMount ties one entry of Spec.Mounts (by index) to a 9P backing the caller
// serves under Name (matching the dirs key passed to wire.Serve9PBacking).
type LocalMount struct {
	Index    int    `json:"index"`
	Name     string `json:"name"`
	ReadOnly bool   `json:"readOnly"`
	// Subpath, when non-empty, means the mount source is a single FILE, not a
	// directory: the caller exports the file's PARENT directory over 9P (a 9P
	// mount root must be a directory) and Subpath is the file's basename within
	// it. The server rewrites Spec.Mounts[Index].Source to <mountpoint>/<Subpath>
	// so the deploy backend bind-mounts just that file. Empty for directory
	// mounts (the common case). Used by Compose file-based configs/secrets.
	Subpath string `json:"subpath,omitempty"`
	// Immutable requests server-side block caching of this mount's on-demand 9P
	// reads (see api.Mount.Immutable). Only honored with ReadOnly.
	Immutable bool `json:"immutable,omitempty"`
	// AsyncCached requests the writable, cache-coherent block protocol for this
	// mount (see api.Mount.AsyncCache): the container mounts cache=mmap (async
	// writeback) and the server terminates the mount in a writable block proxy that
	// keeps the read cache coherent. Only honored when NOT ReadOnly.
	AsyncCached bool `json:"asyncCached,omitempty"`
}

// Cacheable reports whether this mount may be served through the read-only
// server-side block cache: it must be both immutable and read-only.
func (lm LocalMount) Cacheable() bool { return lm.Immutable && lm.ReadOnly }

// WritableCacheable reports whether this mount uses the writable, cache-coherent
// block protocol: it must be async-cached and NOT read-only (mutually exclusive
// with Cacheable).
func (lm LocalMount) WritableCacheable() bool { return lm.AsyncCached && !lm.ReadOnly }

// Event is a server→caller control frame. Many are sent per session; Done is
// terminal.
type Event struct {
	Log    string            `json:"log,omitempty"`
	Status *api.DeployStatus `json:"status,omitempty"`
	Ready  bool              `json:"ready,omitempty"` // sent once after Apply succeeds
	Err    string            `json:"error,omitempty"`
	Done   bool              `json:"done,omitempty"` // teardown complete / terminal error
}

// command is a caller→server control frame sent after the spec.
type command struct {
	Action string `json:"action"` // "down" for graceful teardown
}
