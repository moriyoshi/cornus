package caretaker

// The filesystem-operation role.
//
// The caretaker shares only the app's NETWORK namespace (dockerhost sets
// NetworkMode: "container:<app>"; kubernetes never sets ShareProcessNamespace), so it has
// a mount namespace and a PID namespace of its own. It cannot see the app's image layers
// and it cannot reach /proc/<app-pid>/root. What it CAN see is whatever the backend
// mounted into it — the workload's volumes — and that is precisely the case the archive
// primitives serve worst: on kubernetes they do not exist at all, and everywhere else a
// volume-to-volume copy drags every byte out to the caller and straight back.
//
// So this role is deliberately NOT a general container-filesystem service. Each root maps
// one path as the APP names it onto the directory where the caretaker finds the same
// bytes, and a path under no root is refused with FSErrUnsupported — a positive answer
// the caller falls back on, never a guess.
//
// Every operation goes through pkg/deploy/containerdhost/tarcopy, the same confined
// pack/unpack the containerd backend uses. Reusing it is the point: a copy served here
// and a copy served by containerd then agree entry for entry, including the
// docker-cp-compatible naming and the NoOverwriteDirNonDir refusal. An in-container
// `cp -a` would agree with neither.

import (
	"encoding/json"
	"io"
	"net"
	"sort"
	"strings"

	pathpkg "path"

	"cornus/pkg/api"
	fsoppkg "cornus/pkg/fsop"
	"cornus/pkg/wire"
)

// FSOpRole lets the cornus server run structured filesystem operations against the paths
// this caretaker can see. Like PortForwardRole — and unlike every other role — the SERVER
// opens the stream toward the caretaker, so the role needs no listener of its own: it
// registers a tag on the connection's shared dispatcher.
type FSOpRole struct {
	Server string `json:"server"`
	// Roots is the entire authority of this role. An empty Roots serves nothing, which
	// is a working configuration: the caretaker answers every path with
	// FSErrUnsupported and the caller relays instead.
	Roots []FSOpRoot `json:"roots,omitempty"`
}

// FSOpRoot is one place the caretaker can reach the app's bytes: Target is the path as
// the APP CONTAINER names it, Path is where the same bytes are mounted in the
// CARETAKER's own mount namespace. They differ because a sidecar cannot generally mount a
// volume at the app's own path (that path may already be occupied, and on kubernetes the
// container's mountPath is per-container anyway).
type FSOpRoot struct {
	Target   string `json:"target"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// maxFSOpEntries bounds one directory listing. It is a truncation, reported as one — a
// caller that copies a truncated listing builds an incomplete tree and calls it success,
// so FSOpResponse.Truncated exists to be checked rather than displayed.
const maxFSOpEntries = 50000

// registerFSOp wires the TagFSOp handler onto d. Registration happens before the
// dispatcher starts, so a stream can never arrive for a role that is not yet wired.
func registerFSOp(d *tagDispatch, role FSOpRole) {
	roots := normalizeFSOpRoots(role.Roots)
	d.handle(wire.TagFSOp, func(stream net.Conn) { serveFSOpStream(stream, roots) })
}

// normalizeFSOpRoots cleans and orders the roots longest-target-first, so a nested root
// wins over the one it sits inside. Targets that are not absolute are dropped: an empty
// or relative target would prefix-match every path, which is how one malformed entry
// silently captures the whole namespace.
func normalizeFSOpRoots(in []FSOpRoot) []FSOpRoot {
	out := make([]FSOpRoot, 0, len(in))
	for _, r := range in {
		if !strings.HasPrefix(r.Target, "/") || r.Path == "" {
			continue
		}
		r.Target = pathpkg.Clean(r.Target)
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Target) > len(out[j].Target) })
	return out
}

// serveFSOpStream answers one operation. Exactly one request and one reply ride each
// stream: a failed op then cannot desync the framing for the next one, and the stream
// itself carries the body for the two ops that move bulk data.
func serveFSOpStream(stream net.Conn, roots []FSOpRoot) {
	defer stream.Close()
	raw, err := wire.ReadFSOpFrame(stream)
	if err != nil {
		return
	}
	var req api.FSOpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeFSOpReply(stream, api.FSOpResponse{Error: "fsop: malformed request: " + err.Error()})
		return
	}
	resp, body := runFSOp(req, roots, stream)
	if err := writeFSOpReply(stream, resp); err != nil {
		return
	}
	if body == nil {
		return
	}
	defer body.Close()
	// A failure while packing must NOT terminate the body: the reader distinguishes a
	// finished stream from an abandoned one by the terminator alone (wire.ErrFSOpTruncated).
	if err := wire.WriteFSOpBody(stream, body); err != nil {
		return
	}
}

func writeFSOpReply(stream net.Conn, resp api.FSOpResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return wire.WriteFSOpFrame(stream, b)
}

// runFSOp performs one operation against the caretaker's roots.
//
// The operations themselves live in pkg/fsop, shared with the host backends,
// which reach a workload's bytes through /proc/<pid>/root instead of through
// mounted volumes. Only the wire framing is this file's: the body of a put
// arrives on the stream, and a get's body is written back onto it.
func runFSOp(req api.FSOpRequest, roots []FSOpRoot, stream net.Conn) (api.FSOpResponse, io.ReadCloser) {
	return fsoppkg.Run(req, fsopRoots(roots), wire.FSOpBodyReader(stream))
}

// fsopRoots converts the WIRE form of a root (json-tagged, carried in the
// caretaker config) into the executor's. They are kept separate deliberately:
// the wire shape is a compatibility surface and must not move because the
// executor's does.
func fsopRoots(roots []FSOpRoot) []fsoppkg.Root {
	out := make([]fsoppkg.Root, 0, len(roots))
	for _, r := range roots {
		out = append(out, fsoppkg.Root{Target: r.Target, Path: r.Path, ReadOnly: r.ReadOnly})
	}
	return out
}
