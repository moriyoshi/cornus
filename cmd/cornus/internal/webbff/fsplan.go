package webbff

// The FS operation planner: where do these bytes actually live, and where should the
// work run?
//
// A path in the virtual namespace does not say what backs it. `/<workload>/app/x` may be
// the developer's own disk — cornus bind mounts are CLIENT-LOCAL, served to the container
// over 9P from the caller's machine — or a server-side volume, or the container's own
// image layers. Copying between two of them naively relays every byte through this
// process, which for a bind-mounted path means the bytes travel to the daemon and back
// over 9P to the very disk they started on.
//
// site() answers the first question and planTransfer() the second. planTransfer is a
// PURE function: no I/O, no context, no server. That is deliberate — every routing
// decision is then a table-driven unit test with no daemon and no filesystem, and the
// `why` string makes a wrong decision say so out loud instead of merely being slow.

import (
	"context"
	"fmt"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"cornus/pkg/api"
)

// siteKind is where a path's bytes live.
type siteKind int

const (
	// siteClient is the developer's own filesystem: this process can touch it
	// directly, with no daemon round trip at all.
	siteClient siteKind = iota
	// siteContainer is inside the app container, reachable only through the archive
	// primitives (StatPath/CopyFrom/CopyTo).
	siteContainer
	// siteServer is a server-side volume.
	siteServer
)

func (k siteKind) String() string {
	switch k {
	case siteClient:
		return "client"
	case siteContainer:
		return "container"
	case siteServer:
		return "server"
	}
	return "unknown"
}

// fsSite is one resolved endpoint of an operation.
type fsSite struct {
	kind     siteKind
	workload string // empty at siteClient when the path came from a local root

	// path is the path AS THAT SITE NAMES IT: root-relative at siteClient,
	// container-absolute at siteContainer.
	path string
	// containerPath is always the container-absolute path when the request came in
	// through a workload, kept even at siteClient and siteServer so degrading to a
	// container route is a field selection rather than a re-derivation.
	containerPath string
	// root is the confined local root backing a siteClient path.
	root localRoot
	// volume is the named volume backing a siteServer path.
	volume string
	// readOnly is set when writes must be refused, whatever the filesystem would say.
	readOnly bool
	// why records what decided this site, for error messages and tests. A misrouted
	// operation is otherwise silent — it just does the wrong thing quickly.
	why string
}

// fsOp is the operation being planned.
type fsOp int

const (
	opCopy fsOp = iota
	opMove
)

func (o fsOp) String() string {
	if o == opMove {
		return "move"
	}
	return "copy"
}

// execAt is where the work runs. There is deliberately no "exec in the app container"
// route: see the note on planTransfer.
type execAt int

const (
	// execHere is this process, on the developer's filesystem.
	execHere execAt = iota
	// execServer is one structured request to a cornus process that can already see
	// both paths. Not yet available — see fsPlan.native and the fsop work.
	execServer
	// execRelay streams through this process using the archive primitives.
	execRelay
)

func (e execAt) String() string {
	switch e {
	case execHere:
		return "here"
	case execServer:
		return "server"
	case execRelay:
		return "relay"
	}
	return "unknown"
}

// fsPlan is the decided execution of a two-path operation.
type fsPlan struct {
	op       fsOp
	where    execAt
	src, dst fsSite
	// native means one in-place primitive does the whole job — no bytes move.
	native bool
	why    string
}

// serverFSOps reports whether a structured server-side filesystem operation is available
// for a workload.
//
// It is a real probe, not a guess about the backend. Whether a path can be served is the
// OPERATOR's answer — a kubernetes caretaker serves the volumes it was given and refuses
// everything else — and no amount of reasoning on this side can substitute for asking.
// The probe is one stat against a volume target this workload actually declares, which is
// the cheapest question whose answer is the one that matters.
//
// The result is memoized per workload. A caretaker that has not connected yet answers
// "unsupported", so a negative is not permanent; it is re-probed after
// fsopProbeTTL rather than condemning the workload to relaying for the life of the
// process.
func (s *Server) serverFSOps(ctx context.Context) func(string) bool {
	return func(workload string) bool { return s.fsopAvailable(ctx, workload) }
}

// fsopProbeTTL bounds how long a probe result is reused. Short enough that a pod whose
// caretaker was still starting recovers on the next gesture; long enough that a tree walk
// does not re-probe per entry.
const fsopProbeTTL = 30 * time.Second

type fsopProbe struct {
	ok bool
	at time.Time
}

func (s *Server) fsopAvailable(ctx context.Context, workload string) bool {
	if workload == "" {
		return false
	}
	s.fsopMu.Lock()
	if p, ok := s.fsopKnown[workload]; ok && time.Since(p.at) < fsopProbeTTL {
		s.fsopMu.Unlock()
		return p.ok
	}
	s.fsopMu.Unlock()

	ok := s.probeFSOp(ctx, workload)

	s.fsopMu.Lock()
	if s.fsopKnown == nil {
		s.fsopKnown = map[string]fsopProbe{}
	}
	s.fsopKnown[workload] = fsopProbe{ok: ok, at: time.Now()}
	s.fsopMu.Unlock()
	return ok
}

// probeFSOp asks the operator about a path this workload really has. Probing "/" would
// be worse than useless: no operator serves the image layers, so every workload would
// answer no.
func (s *Server) probeFSOp(ctx context.Context, workload string) bool {
	target := ""
	for _, m := range s.mountsFor(workload) {
		if m.kind == mountVolume {
			target = m.target
			break
		}
	}
	if target == "" {
		return false
	}
	_, err := s.cfs.FSOp(ctx, workload, api.FSOpRequest{Op: api.FSOpStat, Path: target}, nil, nil)
	return err == nil
}

// planTransfer decides where a two-path operation runs.
//
// There is no in-container exec route by design. Shelling into someone else's image to
// move bytes is unreliable in ways that compound: exit codes cannot be trusted (docker
// reports an exec Running for a moment after its stdio closes, and ExecInspect can fail
// outright), busybox and GNU disagree and neither has --one-file-system, distroless
// images have no cp or mv at all, and every such route answers
// folder-onto-existing-folder differently from the others. A same-workload native
// operation is expressed as one STRUCTURED request to a cornus process instead, which
// has real errnos. Until that exists the honest answer is to relay.
func planTransfer(op fsOp, src, dst fsSite, serverOps func(string) bool) fsPlan {
	p := fsPlan{op: op, src: src, dst: dst}

	// Both on the developer's disk. Note this holds across DIFFERENT local roots and
	// across a bind-resolved container path — they are all one filesystem, which is
	// exactly the case the old fsQuery-identity check could not see.
	if src.kind == siteClient && dst.kind == siteClient {
		p.where = execHere
		// A move can be a rename; a copy cannot save anything, because the generic
		// streaming path is already pure local file I/O once both sides are here.
		p.native = op == opMove
		p.why = "both paths are on the client filesystem"
		return p
	}

	// Same workload, both sides reachable by a server-side operation.
	if src.workload != "" && src.workload == dst.workload &&
		serverSideVisible(src) && serverSideVisible(dst) && serverOps(src.workload) {
		p.where, p.native = execServer, true
		p.why = "both paths are server-side in " + src.workload
		return p
	}

	p.where, p.native = execRelay, false
	p.why = fmt.Sprintf("%s -> %s crosses the client/server boundary", src.kind, dst.kind)
	return p
}

// serverSideVisible reports whether a site is one a server-side fsop could act on. A
// container path backed by nothing we know about is not: the app's image layers live in
// the container's own mount namespace, which no sidecar shares.
func serverSideVisible(s fsSite) bool { return s.kind == siteServer }

// ---- resolution ----

// site resolves a query to the place its bytes actually live.
func (s *Server) site(ctx context.Context, q fsQuery) (fsSite, error) {
	q, atRoot, err := s.virtualize(q)
	if err != nil {
		return fsSite{}, err
	}
	if atRoot {
		return fsSite{}, statusErr(http.StatusBadRequest, "the virtual root is not a file")
	}
	switch q.source {
	case "local":
		_, root, cleanRel, err := s.resolveLocal(q.root, q.path)
		if err != nil {
			return fsSite{}, err
		}
		return fsSite{
			kind: siteClient, path: cleanRel, root: root,
			readOnly: root.ReadOnly, why: "local root " + root.ID,
		}, nil
	case "container":
		if q.workload == "" {
			return fsSite{}, statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		return s.containerSite(ctx, q.workload, cleanContainerPath(q.path))
	default:
		return fsSite{}, statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// containerSite decides what backs a container path, redirecting onto the developer's
// own filesystem when the path falls under a client-local bind mount.
func (s *Server) containerSite(ctx context.Context, workload, p string) (fsSite, error) {
	base := fsSite{kind: siteContainer, workload: workload, path: p, containerPath: p}

	m, rel, ok := lookupMount(s.mountsFor(workload), p)
	if !ok {
		base.why = "no mount covers this path"
		return base, nil
	}
	switch m.kind {
	case mountVolume:
		base.kind, base.volume = siteServer, m.volume
		base.readOnly = m.readOnly
		base.why = "named volume at " + m.target
		return base, nil
	case mountAnonymous:
		// Per-replica and unnameable: treat as an ordinary container path rather than
		// pretending it is a shared location.
		base.why = "anonymous volume at " + m.target
		return base, nil
	case mountOpaque:
		base.why = "opaque mount at " + m.target
		return base, nil
	}

	// A bind. Everything below decides whether the redirect is SAFE, and every refusal
	// falls back to the container path — always correct, only slower.
	if reason, ok := browsableSource(m.source); !ok {
		base.why = m.source + ": " + reason
		return base, nil
	}
	if !s.originHere(ctx, workload) {
		// The mount table comes from the compose file on disk, but DeployStatus
		// carries no mounts, so the two are joined by name alone. A workload deployed
		// from another machine or another checkout would redirect to a directory it
		// never had.
		base.why = "deployment did not originate from this project directory"
		return base, nil
	}
	root, ok := s.rootForSource(m.source)
	if !ok {
		base.why = "bind source is not a confined root"
		return base, nil
	}
	// Re-run the containment check against the root that actually anchors it, so a
	// redirected path is held to exactly the same confinement as a directly browsed
	// one — including the refusal of symlinks that escape the root.
	_, _, cleanRel, err := s.resolveLocal(root.ID, pathpkg.Join(root.sub, rel))
	if err != nil {
		base.why = "bind source did not resolve: " + err.Error()
		return base, nil
	}
	return fsSite{
		kind: siteClient, workload: workload, path: cleanRel, containerPath: p,
		root: root.localRoot, readOnly: m.readOnly || root.ReadOnly,
		why: "client-local bind at " + m.target,
	}, nil
}

// boundRoot is a local root plus the sub-path within it that a bind source maps to. A
// bind whose source lives INSIDE the project directory gets no root of its own
// (buildLocalRoots deliberately skips it, being reachable through "project" already), so
// the anchor is the project root and sub is the path from it down to the bind source.
type boundRoot struct {
	localRoot
	sub string
}

// rootForSource finds the confined root that anchors a bind source.
func (s *Server) rootForSource(source string) (boundRoot, bool) {
	// An exact root wins.
	for _, r := range s.localRoots {
		if r.Real == source {
			return boundRoot{localRoot: r}, true
		}
	}
	// Otherwise the innermost root containing it.
	best := boundRoot{}
	found := false
	for _, r := range s.localRoots {
		if !strings.HasPrefix(source, r.Real+"/") {
			continue
		}
		if found && len(r.Real) <= len(best.Real) {
			continue
		}
		best, found = boundRoot{localRoot: r, sub: strings.TrimPrefix(source, r.Real+"/")}, true
	}
	return best, found
}

// originHere reports whether a workload was deployed from this machine and this project
// directory. Failure to confirm is a "no": see originMatchesHere.
func (s *Server) originHere(ctx context.Context, workload string) bool {
	st, err := s.client.Status(ctx, workload)
	if err != nil {
		return false
	}
	return originMatchesHere(st, s.baseDir)
}
