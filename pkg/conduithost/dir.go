package conduithost

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// probeTimeout bounds the whole liveness probe of an advertised control socket:
// connect, send a ping, read the reply. A host answers a ping from a freshly
// spawned goroutine that touches no shared state, so this is generous by a wide
// margin — and it must stay generous, because misjudging a slow host as
// unresponsive is the expensive error, not the cheap one.
const probeTimeout = 3 * time.Second

// maxUnixPathLen is the shortest sun_path any supported kernel gives us (Linux is
// 108, macOS and the BSDs are 104). Exceeding it fails inside bind(2) with a
// message that names neither the path nor the limit, so it is checked up front.
const maxUnixPathLen = 104

// Registry is the rendezvous directory: one subdirectory per PORT, holding one
// control socket and one advertisement per live conduit bound on that port.
//
// Keyed by port at the directory level because coverage is decided per port
// (Covers requires equal ports), so resolving a request is one readdir of that
// port's directory rather than a scan of every conduit on the machine.
type Registry struct{ dir string }

// NewRegistry roots a registry at dir, which callers derive from the same base as
// the agent's own state (see cmd/cornus/internal/clientagent/paths.go: it resolves
// CORNUS_AGENT_DIR, then XDG_RUNTIME_DIR/cornus, then TMPDIR/cornus).
func NewRegistry(dir string) *Registry { return &Registry{dir: dir} }

// Dir is the registry root, for diagnostics.
func (r *Registry) Dir() string { return r.dir }

func (r *Registry) portDir(port int) string {
	return filepath.Join(r.dir, strconv.Itoa(port))
}

// lockPath serializes create-or-join for one port. It is per PORT, not per
// address, because the decision it protects — join this incumbent, refuse for
// that one, or bind — ranges over every conduit on the port.
func (r *Registry) lockPath(port int) string {
	return filepath.Join(r.portDir(port), ".lock")
}

// fileKey renders an address's IP as a filename component. Colons become dashes
// so the name is legal on Windows (which forbids ':' outright) while staying
// readable: "0.0.0.0" stays itself and "::1" becomes "--1". The mapping is
// injective because a dash never occurs in the textual form of an IP address, so
// no two distinct addresses collide onto one socket.
//
// The port is the DIRECTORY, so it needs no place in the name.
func fileKey(a Addr) string { return strings.ReplaceAll(a.IP.String(), ":", "-") }

// SocketPath is the control socket a conduit bound at a listens on.
func (r *Registry) SocketPath(a Addr) string {
	return filepath.Join(r.portDir(a.Port), fileKey(a)+".sock")
}

// statePath is the advertisement beside the socket.
func (r *Registry) statePath(a Addr) string {
	return filepath.Join(r.portDir(a.Port), fileKey(a)+".json")
}

// Entry is one live conduit's advertisement, written by its host and read by every
// would-be joiner.
type Entry struct {
	// Bind is the normalized address, so a reader need not re-derive it from the
	// filename.
	Bind   string `json:"bind"`
	Pid    int    `json:"pid"`
	Socket string `json:"socket"`
	// Settings is opaque to this package; see HelloResponse.Settings.
	Settings json.RawMessage `json:"settings,omitempty"`

	// responsive records whether this entry ANSWERED its liveness probe, as opposed
	// to merely accepting the connection. It is not serialized: it describes this
	// moment's observation, not the advertisement.
	responsive bool
}

// Responsive reports whether the conduit answered its liveness probe. An entry
// that is live-but-unresponsive still holds its address — so it blocks a bind —
// but must not be joined, because the join would block.
func (e Entry) Responsive() bool { return e.responsive }

// Addr parses the entry's bind address.
func (e Entry) Addr() (Addr, error) { return ParseAddr(e.Bind) }

// writeEntry publishes the advertisement for a conduit this process now hosts.
func (r *Registry) writeEntry(e Entry) error {
	a, err := e.Addr()
	if err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(r.statePath(a), b, 0o600)
}

// removeEntry deletes a conduit's socket and advertisement. Missing files are
// fine: reaping races with an orderly shutdown, and both want the same end state.
func (r *Registry) removeEntry(a Addr) {
	_ = os.Remove(r.statePath(a))
	_ = os.Remove(r.SocketPath(a))
}

// Live lists the conduits on port whose control socket still answers, reaping the
// advertisements of those that do not.
//
// Staleness is decided by DIALING, never by checking the recorded pid. A pid is
// the wrong instrument twice over: it can be recycled onto an unrelated process
// (the hazard agentproc.pidIsAgent exists to guard), and a live pid says nothing
// about whether that process still holds this socket. A socket that answers is
// the only evidence that matters.
//
// A dial that is REFUSED or finds no file is definitive: the host is gone, so the
// entry is reaped. A dial that times out is ambiguous — a wedged but living host
// looks the same as a dead one — so the entry is kept. That asymmetry is
// deliberate: keeping a stale entry costs a clear "port is taken" refusal, while
// reaping a live one lets a second process bind an address that is already served
// and split traffic between two conduits.
func (r *Registry) Live(port int) ([]Entry, error) {
	dir := r.portDir(port)
	names, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var live []Entry
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, n.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue // vanished under us: another reaper won
		}
		var e Entry
		if err := json.Unmarshal(b, &e); err != nil {
			// An unparseable advertisement can never be joined and would otherwise
			// block the port forever, so it is reaped like a corpse.
			_ = os.Remove(path)
			continue
		}
		a, err := e.Addr()
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		switch probeSocket(e.Socket) {
		case socketLive:
			e.responsive = true
			live = append(live, e)
		case socketDead:
			r.removeEntry(a)
		case socketUnresponsive, socketUnknown:
			// Kept, but NOT marked responsive: the address is still held, so it must
			// block a bind, while a join against it would hang. Callers distinguish the
			// two with Responsive.
			live = append(live, e)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Bind < live[j].Bind })
	return live, nil
}

type socketState int

const (
	// socketLive: connected AND answered. The only state that licenses a join.
	socketLive socketState = iota
	// socketDead: nothing is listening. The advertisement may be reaped.
	socketDead
	// socketUnresponsive: the socket accepted a connection but never answered. The
	// host process still holds its sockets — so the address is NOT free — but it is
	// not serving, so joining it would block. This is the state a dial-only probe
	// cannot see, and the one that silently wedged every other participant.
	socketUnresponsive
	// socketUnknown: the probe itself failed for a reason that is not evidence
	// either way.
	socketUnknown
)

// probeSocket classifies a control socket by connecting AND requiring an answer.
//
// Connecting alone proves almost nothing: the kernel completes a unix-socket
// connection into the listen backlog whether or not the owning process is still
// servicing it. A host that is wedged, stopped (SIGSTOP), or deadlocked therefore
// looks identical to a healthy one — and the next participant discovers otherwise
// only by blocking in the hello handshake, with the port lock held, until its own
// timeout expires. So the probe sends a ping and waits for the reply.
//
// The three failure directions are deliberately NOT collapsed. Reaping an
// unresponsive host would be actively harmful: it still holds the listening
// socket, so the address is not free, and a participant that reaped it would go on
// to fail at bind with an error about the kernel instead of one about the wedged
// process it should go and look at.
func probeSocket(path string) socketState {
	if path == "" {
		return socketDead
	}
	c, err := net.DialTimeout("unix", path, probeTimeout)
	if err != nil {
		// ENOENT and ECONNREFUSED both mean nothing is listening: the file is either
		// gone or a socket inode whose owner exited without unlinking it.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscallConnRefused) {
			return socketDead
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return socketUnknown
		}
		// Anything else (permission, address-family surprises) is not evidence of
		// death, so it must not license stealing the address.
		return socketUnknown
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(probeTimeout))
	if err := json.NewEncoder(c).Encode(Frame{ID: "probe", Op: OpPing}); err != nil {
		return socketUnresponsive
	}
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		return socketUnresponsive
	}
	if !reply.OK {
		// It answered, which is all this probe asks. A negative reply means a host
		// that is alive and disagreeing, which is a matter for the join.
		return socketLive
	}
	return socketLive
}

// ensurePortDir creates the port's directory, 0700 like the agent's own state
// directory: every participant runs as this user and nothing here is meant to be
// reachable by another.
func (r *Registry) ensurePortDir(port int) error {
	return os.MkdirAll(r.portDir(port), 0o700)
}

// checkSocketPath rejects a control socket path the kernel could not bind, with
// an error that names the path and the limit — bind(2) reports neither.
func checkSocketPath(path string) error {
	if len(path) > maxUnixPathLen {
		return fmt.Errorf("conduit control socket path is %d bytes, over the %d-byte unix socket limit: %s (set CORNUS_AGENT_DIR to a shorter directory)", len(path), maxUnixPathLen, path)
	}
	return nil
}

// LiveAll lists every live conduit in the registry, across all ports, reaping the
// advertisements of any that no longer answer.
//
// It exists for the caller that does not care WHICH address it gets — a published
// web UI, say, whose browser has one proxy setting and simply wants to be wherever
// the workloads are. Before conduits were addressable that question could only be
// asked of one agent's memory, so a conduit in any other process was invisible;
// asking the registry makes the answer cross-process, which is the whole point.
//
// Entries are ordered by port and then bind address, so a caller choosing among
// several gets the same answer every time rather than one that depends on readdir
// order.
func (r *Registry) LiveAll() ([]Entry, error) {
	ports, err := os.ReadDir(r.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, p := range ports {
		if !p.IsDir() {
			continue
		}
		port, err := strconv.Atoi(p.Name())
		if err != nil {
			continue // not a port directory
		}
		live, err := r.Live(port)
		if err != nil {
			continue // an unreadable port directory must not hide the others
		}
		out = append(out, live...)
	}
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i].Addr()
		aj, _ := out[j].Addr()
		if ai.Port != aj.Port {
			return ai.Port < aj.Port
		}
		return out[i].Bind < out[j].Bind
	})
	return out, nil
}
