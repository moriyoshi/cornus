package webbff

// The file-explorer surface: /.cornus/web/fs*. It browses two concrete sources behind
// one unified JSON shape — the developer's LOCAL filesystem (confined to a set of
// roots: the compose project dir plus each bind-mount source) and a running
// WORKLOAD's CONTAINER filesystem (via the deploy backend's docker-cp + exec
// primitives). It is a strict superset of the legacy flat editor
// (/.cornus/web/files*), which stays.
//
// On top of those, a VIRTUAL namespace (source=virtual) unifies everything under one
// slash path: the root lists the mounts (local roots + workloads as directories) and
// /<mount>/<subpath> resolves onto the concrete source. Every operation — including
// FsCopy, which can move a file between any two mounts — is expressed as a virtual
// path, so the SPA addresses the whole tree without a source selector. See the
// "virtual namespace" section for the resolver.
//
// Local containment mirrors the build server's confined 9P export (pkg/wire/
// confinedfs.go): every resolved path must, after symlink resolution, still live
// under its root, so "../" and escaping symlinks are refused at the door.
//
// The container side has no ReadDir, so a directory LISTING is produced by exec'ing
// a portable shell glob loop that emits NUL-framed records (busybox + GNU safe,
// newline-in-filename safe, injection-free because the directory rides as the exec
// WorkingDir rather than being spliced into the script). Reads/writes/mkdir go
// through single-entry tars (CopyFrom/CopyTo); rename/delete are direct mv/rm execs.
// A shell-less image falls back to reading only the tar headers of a recursive
// CopyFrom.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	gofs "io/fs"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/logging"
)

// ---- wire shapes ----

// fsEntry is one directory child. Mtime is RFC3339 on both sources; Mode is octal
// permission bits ("0644"); LinkTarget is set only for symlinks. Running is set only
// for the workload mounts of the virtual root listing (nil for files and local
// roots) so the UI can tell running workloads from stopped ones.
type fsEntry struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "dir" | "file" | "symlink"
	Size       int64  `json:"size"`
	Mtime      string `json:"mtime,omitempty"`
	Mode       string `json:"mode,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Running    *bool  `json:"running,omitempty"`
	// ReadOnly marks a virtual-root mount whose bind is declared `:ro`. The kernel
	// holds the container to it, so the explorer says so up front rather than letting
	// a write get as far as a 403.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// fsListing is a one-level directory listing. Path is the normalized path echoed
// back (relative to root for local, container-absolute for container). Truncated is
// set when a container listing exceeded the capture cap.
type fsListing struct {
	Source    string    `json:"source"`
	Root      string    `json:"root,omitempty"`
	Path      string    `json:"path"`
	Entries   []fsEntry `json:"entries"`
	Truncated bool      `json:"truncated,omitempty"`
	// ReadOnly marks a listing whose directory cannot be written — it sits under a bind
	// declared `:ro`. It travels with the LISTING because that is what a pane has: an
	// entry only carries the flag at the virtual root, so a pane showing a folder deep
	// inside a read-only mount would otherwise have no way to know.
	ReadOnly bool `json:"readOnly,omitempty"`
	// Refused travels with the VIRTUAL ROOT listing only: bind-mount sources the
	// explorer declined to expose (see browsableSource). It rides the listing rather
	// than /fs/roots because this is where a user looks for a mount, and a root that
	// simply is not there reads as a defect in whoever wrote the compose file.
	Refused []fsRefusedRoot `json:"refused,omitempty"`
}

// fsRoot is a browsable local root: the project dir or a bind-mount source.
type fsRoot struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// fsRefusedRoot is a bind-mount source the explorer declined to expose (see
// browsableSource). It travels to the UI so a missing root is explained rather than
// silently absent.
type fsRefusedRoot struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// fsWorkloadRef is a workload the container source can target.
type fsWorkloadRef struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
}

// fsRoots is what the source switcher is populated from.
type fsRoots struct {
	Roots     []fsRoot        `json:"roots"`
	Workloads []fsWorkloadRef `json:"workloads"`
	Refused   []fsRefusedRoot `json:"refused,omitempty"`
}

// localRoot is a resolved, confined browsing root. Real is the symlink-resolved
// absolute anchor every path under it is checked against.
//
// ReadOnly carries the `:ro` of the bind mount the root came from. It used to be
// dropped, which made a read-only declaration mean nothing here: the container was
// held to it by the kernel while the explorer served the same bytes writably.
type localRoot struct {
	ID       string
	Label    string
	Real     string
	ReadOnly bool
}

// ---- the container-filesystem seam ----

// containerFS is the slice of the deploy backend the explorer needs, behind an
// interface so tests can drive the container source without a live daemon (the real
// exec path is a hijacked WebSocket that httptest cannot serve).
type containerFS interface {
	StatPath(ctx context.Context, name, path string) (api.PathStat, error)
	CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error)
	CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error
	// Exec captures at most limit bytes of each stream. The bound is the CALLER's
	// because the two callers want different things: a terminal command is tool output
	// and 256 KiB of it is plenty, while a directory listing is data whose size is set by
	// how many files someone happens to have.
	Exec(ctx context.Context, name, workdir string, cmd []string, limit int) (ExecResult, error)
	// FSOp runs one structured filesystem operation where the bytes actually are — a
	// server-side volume served by the deployment's caretaker, or by a backend that owns
	// its volumes outright. It is the only route that can copy WITHIN a workload without
	// pulling every byte through this process.
	//
	// It is optional in practice: a backend with no operator answers
	// client.ErrFSOpUnsupported, and every caller here is required to fall back to the
	// relay rather than surface that.
	FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error)
}

// The three archive primitives fall back to the structured operator when the backend has
// no archive at all. That is not an optimization, it is the only reason a kubernetes
// volume can be read or written: its StatPath/CopyFrom/CopyTo are unsupported outright,
// so before this every explorer read on that backend was a 501.
//
// The substitution is byte-compatible rather than merely similar — the caretaker serves
// get and put through the SAME pkg/deploy/containerdhost/tarcopy the containerd backend
// packs and unpacks with — so a tar produced one way extracts identically the other.
//
// The decision is made BEFORE the call, not as a retry after it, and the live kube run is
// what forced that. A retry has to be gated on nothing having moved — a spent stream
// cannot be replayed, and a second attempt would append to a half-written destination —
// but a PUT streams its tar from a pipe while the request is in flight, so Go's transport
// had already pumped bytes out of that pipe by the time the server's 501 came back. The
// guard fired correctly and the fallback never happened. Learning the answer from a
// bodyless StatPath and remembering it is what makes the write path work at all.
//
// archiveKnown is a property of the SERVER's backend, not of a deployment: one client
// talks to one server running one backend. It is learned lazily rather than probed,
// because StatPath already precedes every transfer the explorer makes.

// clientContainerFS is the production containerFS: a thin adapter over the cornus server
// client the BFF already holds, plus what it has learned about that server's backend.
type clientContainerFS struct {
	c *client.Client

	mu sync.Mutex
	// archiveKnown is nil until the first StatPath answers; then it is whether this
	// server's backend implements the archive primitives at all.
	archiveKnown *bool
}

// archiveWorks reports the learned answer, defaulting to "yes" while unknown so a server
// whose backend does have an archive is never denied it on a guess.
func (f *clientContainerFS) archiveWorks() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.archiveKnown == nil || *f.archiveKnown
}

func (f *clientContainerFS) learnArchive(ok bool) {
	f.mu.Lock()
	f.archiveKnown = &ok
	f.mu.Unlock()
}

func (f *clientContainerFS) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	if f.archiveWorks() {
		st, err := f.c.StatPath(ctx, name, path)
		if !archiveGone(err) {
			if err == nil {
				f.learnArchive(true)
			}
			return st, err
		}
		// HEAD carries no body and reads nothing, so this one IS safely retryable —
		// and it is where the answer for every later transfer is learned.
		f.learnArchive(false)
		resp, ferr := f.c.FSOp(ctx, name, api.FSOpRequest{Op: api.FSOpStat, Path: path}, nil, nil)
		if ferr != nil || resp.Stat == nil {
			return st, err // the ORIGINAL error: the operator could not help either
		}
		return *resp.Stat, nil
	}
	resp, err := f.c.FSOp(ctx, name, api.FSOpRequest{Op: api.FSOpStat, Path: path}, nil, nil)
	if err != nil {
		return api.PathStat{}, err
	}
	if resp.Stat == nil {
		return api.PathStat{}, statusErr(http.StatusBadGateway, "the operator returned no stat for %s", path)
	}
	return *resp.Stat, nil
}

func (f *clientContainerFS) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	if f.archiveWorks() {
		cw := &countingWriter{w: w}
		st, err := f.c.CopyFrom(ctx, name, path, cw)
		// Belt and braces: the learned answer normally makes this unreachable, but a
		// call that has already written cannot be retried whatever the status says.
		if !archiveGone(err) || cw.n > 0 {
			if err == nil {
				f.learnArchive(true)
			}
			return st, err
		}
		f.learnArchive(false)
	}
	resp, ferr := f.c.FSOp(ctx, name, api.FSOpRequest{Op: api.FSOpGet, Path: path}, nil, w)
	if ferr != nil {
		return api.PathStat{}, ferr
	}
	if resp.Stat != nil {
		return *resp.Stat, nil
	}
	return api.PathStat{}, nil
}

func (f *clientContainerFS) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	if f.archiveWorks() {
		cr := &countingReader{r: r}
		err := f.c.CopyTo(ctx, name, path, cr, opts)
		if !archiveGone(err) || cr.n > 0 {
			if err == nil {
				f.learnArchive(true)
			}
			return err
		}
		f.learnArchive(false)
	}
	_, ferr := f.c.FSOp(ctx, name, api.FSOpRequest{
		Op:                   api.FSOpPut,
		Path:                 path,
		NoOverwriteDirNonDir: opts.NoOverwriteDirNonDir,
		CopyUIDGID:           opts.CopyUIDGID,
	}, r, nil)
	return ferr
}

// archiveGone reports an archive call that failed BECAUSE the backend has no archive. A
// 501 is the server's answer for an unsupported backend primitive (streamErrStatus maps
// "not supported"/"unsupported" onto it).
func archiveGone(err error) bool {
	if err == nil {
		return false
	}
	var ae *client.APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotImplemented
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
func (f *clientContainerFS) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	return f.c.FSOp(ctx, name, req, body, out)
}
func (f *clientContainerFS) Exec(ctx context.Context, name, workdir string, cmd []string, limit int) (ExecResult, error) {
	return execCapture(ctx, f.c, name, workdir, cmd, limit)
}

// execCapture runs cmd once inside a workload (optionally under workdir) and captures
// stdout, stderr, and the exit code, each bounded to limit. It is the shared core of
// Server.ExecRun and the container source's directory listing.
func execCapture(ctx context.Context, cl *client.Client, name, workdir string, cmd []string, limit int) (ExecResult, error) {
	if len(cmd) == 0 {
		return ExecResult{}, statusErr(http.StatusBadRequest, "cmd is required")
	}
	execID, err := cl.ExecCreate(ctx, name, api.ExecConfig{
		Cmd: cmd, WorkingDir: workdir, Tty: false, AttachStdin: false, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return ExecResult{}, err
	}
	stream, err := cl.ExecStart(ctx, execID, api.ExecStartConfig{Tty: false})
	if err != nil {
		return ExecResult{}, err
	}
	defer stream.Close()

	var stdout, stderr capBuffer
	stdout.cap, stderr.cap = limit, limit
	if _, err := stdcopy.StdCopy(&stdout, &stderr, stream); err != nil {
		return ExecResult{}, err
	}
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	res.ExitCode, res.ExitKnown = inspectExit(ctx, cl, execID)
	return res, nil
}

// inspectExitAttempts / inspectExitDelay bound the settle poll below: half a second
// in total, which is far longer than the gap between the stdio stream closing and
// the daemon reaping the process, and short enough that a genuinely wedged exec does
// not hold a request open.
const (
	inspectExitAttempts = 20
	inspectExitDelay    = 25 * time.Millisecond
)

// inspectExit reads an exec's final status, returning known=false rather than a
// misleading zero when it cannot.
//
// Two failure modes are folded in here. ExecInspect can fail, which the previous
// code swallowed — leaving ExitCode at 0 and reporting a failed command as a
// successful one. And docker routinely still reports Running immediately after the
// stdio stream closes, so the first answer is not authoritative; this polls until
// the status settles instead of trusting it.
func inspectExit(ctx context.Context, cl *client.Client, execID string) (code int, known bool) {
	for i := 0; i < inspectExitAttempts; i++ {
		st, err := cl.ExecInspect(ctx, execID)
		if err != nil {
			return 0, false
		}
		if !st.Running {
			return st.ExitCode, true
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-time.After(inspectExitDelay):
		}
	}
	return 0, false
}

// ---- local roots ----

// buildLocalRoots records the browsable local roots: the compose project directory
// plus every distinct bind-mount source directory that resolves outside it (sources
// inside the project are already reachable through the project root). It runs from
// loadProject after baseDir/plans are known.
func (s *Server) buildLocalRoots() {
	s.localRoots = nil
	s.localRootByID = map[string]localRoot{}

	real := func(dir string) (string, bool) {
		r, err := filepath.EvalSymlinks(dir)
		if err != nil {
			r = dir
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			return "", false
		}
		return abs, true
	}
	add := func(id, label, dir string, readOnly bool) {
		abs, ok := real(dir)
		if !ok {
			return
		}
		// The safeguard is applied on the RESOLVED path, and before the root exists at
		// all: a /proc or / root that is merely hidden from the switcher would still be
		// reachable by anyone who typed its id.
		if reason, ok := browsableSource(abs); !ok {
			// Recorded rather than dropped: a root that simply vanishes from the
			// switcher reads as a bug. Roots() hands these to the UI so it can say
			// which source was refused and why.
			s.refusedRoots = append(s.refusedRoots, fsRefusedRoot{Path: abs, Reason: reason})
			logging.FromContext(context.Background()).Warn(
				"web: bind-mount source is not browsable in the file explorer",
				"path", abs, "reason", reason)
			return
		}
		for i, existing := range s.localRoots {
			if existing.Real != abs {
				continue
			}
			// One host directory can be mounted twice with different modes. The root
			// is writable if ANY mount of it is, because the container really can
			// write those bytes through that mount — refusing would be a fiction in
			// the safe direction, but a fiction all the same.
			if existing.ReadOnly && !readOnly {
				s.localRoots[i].ReadOnly = false
				s.localRootByID[existing.ID] = s.localRoots[i]
			}
			return
		}
		lr := localRoot{ID: id, Label: label, Real: abs, ReadOnly: readOnly}
		s.localRoots = append(s.localRoots, lr)
		s.localRootByID[id] = lr
	}

	var projectReal string
	if s.baseDir != "" {
		label := s.projectName
		if label == "" {
			label = filepath.Base(s.baseDir)
		}
		if abs, ok := real(s.baseDir); ok {
			projectReal = abs
		}
		// The project directory is the developer's own working tree, not a mount, so
		// it carries no `:ro` to honour.
		add("project", label, s.baseDir, false)
	}

	i := 0
	for _, svc := range s.order {
		plan := s.plans[svc]
		for _, m := range plan.Spec.Mounts {
			if m.Source == "" {
				continue
			}
			fi, err := os.Stat(m.Source)
			if err != nil || !fi.IsDir() {
				continue
			}
			if abs, ok := real(m.Source); ok && projectReal != "" && underRoot(projectReal, abs) {
				continue // reachable through the project root already
			}
			add(fmt.Sprintf("mount%d", i), filepath.Base(m.Source), m.Source, m.ReadOnly)
			i++
		}
	}
}

// Roots is the source switcher's discovery: the local roots plus the workloads the
// container source can target (running ones are flagged).
func (s *Server) Roots(ctx context.Context) fsRoots {
	out := fsRoots{Roots: []fsRoot{}, Workloads: []fsWorkloadRef{}, Refused: s.refusedRoots}
	for _, r := range s.localRoots {
		out.Roots = append(out.Roots, fsRoot{ID: r.ID, Label: r.Label, Path: r.Real, ReadOnly: r.ReadOnly})
	}
	if list, err := s.client.List(ctx); err == nil {
		for _, st := range list {
			_, running := runningSummary(st)
			out.Workloads = append(out.Workloads, fsWorkloadRef{Name: st.Name, Running: running})
		}
		sort.Slice(out.Workloads, func(i, j int) bool { return out.Workloads[i].Name < out.Workloads[j].Name })
	}
	return out
}

// ---- virtual namespace ----

// The virtual source unifies every browsable location behind one slash-separated
// path space so a single path can name anything (and copies can span two of them).
// The first segment is a MOUNT — a local root ID ("project", "mount0", …) or a
// workload name — and the remainder is that mount's own sub-path. The bare root
// (empty path) lists the mounts themselves as directories.

// resolveVirtual maps a virtual path to the concrete fsQuery its mount targets.
// atRoot is true for the bare virtual root (no mount selected), which only listing
// serves. A mount that is not a known local root is treated as a workload, so an
// unknown mount surfaces as a normal container not-found downstream.
func (s *Server) resolveVirtual(vpath string) (concrete fsQuery, atRoot bool, err error) {
	rel := strings.TrimPrefix(pathpkg.Clean("/"+filepath.ToSlash(vpath)), "/")
	if rel == "" {
		return fsQuery{}, true, nil
	}
	mount, sub := rel, ""
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		mount, sub = rel[:i], rel[i+1:]
	}
	if _, ok := s.localRootByID[mount]; ok {
		return fsQuery{source: "local", root: mount, path: sub}, false, nil
	}
	return fsQuery{source: "container", workload: mount, path: "/" + sub}, false, nil
}

// virtualize resolves q when it targets the virtual source, returning the concrete
// query to delegate to. atRoot flags the bare virtual root, which no per-file
// operation can act on; concrete queries pass through untouched.
func (s *Server) virtualize(q fsQuery) (concrete fsQuery, atRoot bool, err error) {
	if q.source != "virtual" {
		return q, false, nil
	}
	return s.resolveVirtual(q.path)
}

// virtualRootListing is the top of the virtual namespace: each local root and each
// workload as a directory. Local roots come first (in discovery order), then the
// workloads sorted by name with their running state attached.
func (s *Server) virtualRootListing(ctx context.Context) fsListing {
	entries := make([]fsEntry, 0)
	for _, r := range s.localRoots {
		entries = append(entries, fsEntry{Name: r.ID, Kind: "dir", Mode: "0755", ReadOnly: r.ReadOnly})
	}
	if list, err := s.client.List(ctx); err == nil {
		wls := make([]fsEntry, 0, len(list))
		for _, st := range list {
			_, running := runningSummary(st)
			r := running
			wls = append(wls, fsEntry{Name: st.Name, Kind: "dir", Mode: "0755", Running: &r})
		}
		sort.Slice(wls, func(i, j int) bool { return wls[i].Name < wls[j].Name })
		entries = append(entries, wls...)
	}
	return fsListing{Source: "virtual", Path: "", Entries: entries, Refused: s.refusedRoots}
}

// ---- containment ----

// underRoot reports whether full, after symlink resolution, stays within root. For a
// not-yet-existing leaf it recurses to the parent, so a new file under an in-root
// directory is allowed while an escaping symlink is refused. Ported from
// pkg/wire/confinedfs.go guard.within.
func underRoot(root, full string) bool {
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, gofs.ErrNotExist) {
			parent := filepath.Dir(full)
			if parent == full {
				return false
			}
			return underRoot(root, parent)
		}
		return false
	}
	return resolved == root || strings.HasPrefix(resolved, root+string(os.PathSeparator))
}

// resolveLocal maps (rootID, rel) to a cleaned absolute path proven to stay within
// the root, plus the root and the normalized slash-relative path to echo back. An
// empty rootID defaults to the first (project) root.
func (s *Server) resolveLocal(rootID, rel string) (full string, root localRoot, cleanRel string, err error) {
	if len(s.localRoots) == 0 {
		return "", localRoot{}, "", statusErr(http.StatusNotFound, "no local root (no compose project loaded)")
	}
	if rootID == "" {
		root = s.localRoots[0]
	} else {
		r, ok := s.localRootByID[rootID]
		if !ok {
			return "", localRoot{}, "", statusErr(http.StatusBadRequest, "unknown root %q", rootID)
		}
		root = r
	}
	clean := pathpkg.Clean("/" + filepath.ToSlash(rel)) // strips ".." and any leading slash
	cleanRel = strings.TrimPrefix(clean, "/")
	full = filepath.Join(root.Real, filepath.FromSlash(clean))
	if !underRoot(root.Real, full) {
		return "", localRoot{}, "", statusErr(http.StatusForbidden, "path escapes root")
	}
	return full, root, cleanRel, nil
}

// resolveLocalWrite is resolveLocal for a MUTATION. It exists so the read-only bit of a
// `:ro` bind cannot be forgotten at one call site: every local write, mkdir, rename and
// delete goes through here, and adding a new mutation without it is a visible omission
// rather than a silent one.
func (s *Server) resolveLocalWrite(rootID, rel string) (full string, root localRoot, cleanRel string, err error) {
	full, root, cleanRel, err = s.resolveLocal(rootID, rel)
	if err != nil {
		return "", localRoot{}, "", err
	}
	if root.ReadOnly {
		return "", localRoot{}, "", statusErr(http.StatusForbidden,
			"%s is a read-only bind mount", root.Label)
	}
	return full, root, cleanRel, nil
}

// resolve maps a query onto the place its bytes actually LIVE, returning both the
// concrete query the source-specific code below understands and the site that decided
// it.
//
// This is virtualize plus the client-local bind redirect: a container path covered by a
// bind mount comes back as a LOCAL query anchored at the bind source, so it is served
// from the developer's own disk with no daemon round trip — instead of a tar to the
// server, a docker-cp into the container, and the container's write travelling back out
// over 9P to that same disk.
//
// The site is returned alongside because the concrete query cannot carry everything.
// A `:ro` bind whose source sits inside the project directory resolves into the
// (writable) "project" root, so the read-only bit lives on the site, not the root —
// which is why every mutation checks siteWritable rather than the root alone.
func (s *Server) resolve(ctx context.Context, q fsQuery) (concrete fsQuery, st fsSite, atRoot bool, err error) {
	q, atRoot, err = s.virtualize(q)
	if err != nil || atRoot {
		return q, fsSite{}, atRoot, err
	}
	if q.source != "container" || q.workload == "" {
		return q, fsSite{}, false, nil
	}
	st, err = s.containerSite(ctx, q.workload, cleanContainerPath(q.path))
	if err != nil {
		return q, fsSite{}, false, err
	}
	if st.kind != siteClient {
		return q, st, false, nil
	}
	return fsQuery{source: "local", root: st.root.ID, path: st.path}, st, false, nil
}

// siteWritable refuses a mutation the resolved site does not permit. It carries the
// read-only bit a redirect would otherwise drop, and names where the same bytes ARE
// writable, since the bind source is usually reachable as a local root too.
func siteWritable(st fsSite) error {
	if st.kind == siteClient && st.readOnly {
		return statusErr(http.StatusForbidden,
			"%s is a read-only bind mount; write it through its local root instead", st.why)
	}
	return nil
}

// skippable marks an error as "step over this ONE entry", as opposed to "this transfer
// failed". A directory walk consults it; a direct request does not, so the wrapped
// status is still what the caller sees.
//
// The distinction is the whole point of the response's `skipped` list, and it used to be
// made in the wrong place: copyTree recorded a skip for ANY error on a symlink entry, so
// a link to a 2 GB file that failed halfway through was reported as "deliberately stepped
// over" — and FsMove, which keeps the source whenever anything was skipped, would tell
// the user a partial transfer had been tidily handled. Only the source itself can say
// which of the two happened, so the classification lives where the file is opened.
type skippable struct{ err error }

func (s *skippable) Error() string { return s.err.Error() }
func (s *skippable) Unwrap() error { return s.err }

// skipEntry wraps err as skippable. It is applied to exactly three things: an entry that
// is not a transferable file at all (a directory reached through a symlink, a device,
// FIFO or socket, a link whose archive entry carries no body), and an entry that has
// VANISHED between the listing and the copy — a minutes-long walk of a live directory
// will meet that, and losing 500 files to it would be absurd.
func skipEntry(err error) error {
	if err == nil {
		return nil
	}
	return &skippable{err: err}
}

func isSkippable(err error) bool {
	var s *skippable
	return errors.As(err, &s)
}

// skipIfGone wraps a not-found as skippable and passes everything else through.
func skipIfGone(err error) error {
	var se *statusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return skipEntry(err)
	}
	return err
}

// mapOSErr turns a filesystem error into an HTTP-coded statusError.
func mapOSErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gofs.ErrNotExist):
		return statusErr(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, gofs.ErrPermission):
		return statusErr(http.StatusForbidden, "%s", err.Error())
	case errors.Is(err, gofs.ErrExist):
		return statusErr(http.StatusConflict, "%s", err.Error())
	default:
		return err
	}
}

// ---- listing ----

// FsList returns a one-level directory listing for the selected source.
func (s *Server) FsList(ctx context.Context, q fsQuery) (fsListing, error) {
	if q.source == "virtual" {
		cq, atRoot, err := s.resolveVirtual(q.path)
		if err != nil {
			return fsListing{}, err
		}
		if atRoot {
			return s.virtualRootListing(ctx), nil
		}
		out, err := s.FsList(ctx, cq)
		if err != nil {
			return fsListing{}, err
		}
		// Echo the virtual path back so the client's breadcrumbs stay in the
		// virtual namespace rather than the resolved mount's.
		out.Source = "virtual"
		out.Root = ""
		out.Path = strings.TrimPrefix(pathpkg.Clean("/"+filepath.ToSlash(q.path)), "/")
		return out, nil
	}
	// A container directory covered by a client-local bind is listed straight off the
	// developer's disk. Note the listing is still labelled "container": the caller
	// asked about a container path and the mount is an implementation detail, not a
	// change of address.
	if q.source == "container" && q.workload != "" {
		cq, st, _, err := s.resolve(ctx, q)
		if err != nil {
			return fsListing{}, err
		}
		if st.kind == siteClient {
			out, err := s.localList(cq.root, cq.path)
			if err != nil {
				return fsListing{}, err
			}
			out.Source, out.Root, out.Path = "container", "", st.containerPath
			out.ReadOnly = out.ReadOnly || st.readOnly
			return out, nil
		}
	}
	switch q.source {
	case "local":
		return s.localList(q.root, q.path)
	case "container":
		if q.workload == "" {
			return fsListing{}, statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		return s.containerList(ctx, q.workload, q.path)
	default:
		return fsListing{}, statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

func (s *Server) localList(rootID, rel string) (fsListing, error) {
	full, root, cleanRel, err := s.resolveLocal(rootID, rel)
	if err != nil {
		return fsListing{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return fsListing{}, mapOSErr(err)
	}
	if !info.IsDir() {
		return fsListing{}, statusErr(http.StatusBadRequest, "not a directory")
	}
	des, err := os.ReadDir(full)
	if err != nil {
		return fsListing{}, mapOSErr(err)
	}
	entries := make([]fsEntry, 0, len(des))
	for _, de := range des {
		fi, err := os.Lstat(filepath.Join(full, de.Name()))
		if err != nil {
			continue // vanished between ReadDir and Lstat
		}
		entries = append(entries, localEntry(full, fi))
	}
	sortEntries(entries)
	return fsListing{
		Source: "local", Root: root.ID, Path: cleanRel,
		Entries: entries, ReadOnly: root.ReadOnly,
	}, nil
}

// localEntry builds an fsEntry from an Lstat'd file in dir (Lstat so symlinks report
// as symlinks rather than their targets).
func localEntry(dir string, fi os.FileInfo) fsEntry {
	e := fsEntry{
		Name:  fi.Name(),
		Size:  fi.Size(),
		Mtime: fi.ModTime().UTC().Format(time.RFC3339),
		Mode:  fmt.Sprintf("%04o", fi.Mode().Perm()),
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		e.Kind = "symlink"
		if lt, err := os.Readlink(filepath.Join(dir, fi.Name())); err == nil {
			e.LinkTarget = lt
		}
	case fi.IsDir():
		e.Kind = "dir"
	default:
		e.Kind = "file"
	}
	return e
}

// listScript enumerates one directory level and emits, per entry, six NUL-terminated
// fields: type(d|f|l), size, mtime(epoch), mode(octal), symlink-target, name. NUL framing
// survives spaces and newlines in filenames; the glob set matches dotfiles and
// ".."-prefixed names but never "." or "..".
//
// The directory is "$1", NOT the exec's working directory, and that is a correctness
// requirement rather than a style choice: kubernetes CANNOT express a working directory
// for an exec at all. PodExecOptions has no such field, so the backend logs
// "backend cannot honor exec option ... option=WorkingDir" and runs the command anyway
// (kubernetes.go, ExecCreate) — which meant this glob ran in the image's default
// workdir and the explorer answered every container-path listing with the contents of
// some other directory, labelled with the path the user had asked for. Found by
// e2e/scenarios/web-fs.star the first time it ran on kube: a request for /etc came back
// as /.
//
// The exit codes are the classification. Matching on shell stderr was already awkward
// (busybox and GNU word it differently) and cd's message does not distinguish "missing"
// from "not a directory" at all.
const listScript = `[ -e "$1" ] || [ -L "$1" ] || exit 4
[ -d "$1" ] || exit 3
cd "$1" || exit 5
for e in * .[!.]* ..?*; do
  [ -e "$e" ] || [ -L "$e" ] || continue
  if [ -L "$e" ]; then t=l; lt=$(readlink "$e" 2>/dev/null);
  elif [ -d "$e" ]; then t=d; lt=;
  else t=f; lt=; fi
  sz=$(stat -c %s "$e" 2>/dev/null || echo 0)
  mt=$(stat -c %Y "$e" 2>/dev/null || echo 0)
  md=$(stat -c %a "$e" 2>/dev/null || echo 0)
  printf '%s\0%s\0%s\0%s\0%s\0%s\0' "$t" "$sz" "$mt" "$md" "$lt" "$e"
done`

// listScriptCmd builds the argv that runs listScript against dir. `sh -c SCRIPT NAME ARG`
// puts NAME in $0 and ARG in $1, so the path travels as an ARGUMENT — never interpolated
// into the script text, where a directory named `"; rm -rf /` would be shell source.
func listScriptCmd(dir string) []string {
	return []string{"/bin/sh", "-c", listScript, "sh", dir}
}

// listScript's own exit codes, distinct from anything the glob loop produces.
const (
	listExitNotDir  = 3
	listExitMissing = 4
)

// maxListCapture bounds ONE directory listing's exec output. listScript spends roughly
// 40-100 bytes per entry, so this holds a directory of some 100k files — where
// maxToolCapture, the bound this shared with the terminal until it was separated, ran out
// at a few thousand. That mattered beyond the listing: copyTree turns a truncated listing
// into a 413, so a container directory of a few thousand entries could not be COPIED at
// all, long after the per-file size cap was lifted.
//
// It is a wider bound, not the absence of one — the output is still assembled in memory.
// Paging the glob loop would remove the bound entirely, at the cost of one exec round trip
// per page and a listing that can tear when the directory changes between pages. That
// trade is not obviously worth it, so what is left is honest instead: a listing past this
// still reports Truncated, and a copy still refuses rather than silently producing a
// partial tree.
const maxListCapture = 8 << 20

func (s *Server) containerList(ctx context.Context, workload, p string) (fsListing, error) {
	if err := s.ensureRunning(ctx, workload); err != nil {
		return fsListing{}, err
	}
	dir := cleanContainerPath(p)
	// No working directory is requested: the path is an argument (see listScript).
	res, err := s.cfs.Exec(ctx, workload, "", listScriptCmd(dir), maxListCapture)
	if err != nil {
		return s.containerListTar(ctx, workload, dir) // shell would not start
	}
	if !res.ExitKnown {
		// The glob loop may or may not have finished. Parsing what arrived would
		// report a short — or empty — directory as if it were complete, so fall back
		// to the tar listing, which either succeeds wholly or fails.
		return s.containerListTar(ctx, workload, dir)
	}
	if res.ExitCode != 0 {
		low := strings.ToLower(res.Stderr)
		switch {
		case res.ExitCode == listExitNotDir:
			return fsListing{}, statusErr(http.StatusBadRequest, "not a directory")
		case res.ExitCode == listExitMissing:
			return fsListing{}, statusErr(http.StatusNotFound, "%s", dir)
		case strings.Contains(low, "not a directory"):
			return fsListing{}, statusErr(http.StatusBadRequest, "not a directory")
		case strings.Contains(low, "no such file") && !strings.Contains(low, "sh"):
			return fsListing{}, statusErr(http.StatusNotFound, "%s", dir)
		default:
			// missing/ broken shell, or an unclear failure: try the tar fallback.
			return s.containerListTar(ctx, workload, dir)
		}
	}
	entries := parseListing(res.Stdout)
	return fsListing{
		Source: "container", Path: dir, Entries: entries,
		Truncated: len(res.Stdout) >= maxListCapture,
	}, nil
}

// parseListing decodes listScript's NUL-framed output into sorted entries.
func parseListing(out string) []fsEntry {
	parts := strings.Split(out, "\x00")
	var entries []fsEntry
	for i := 0; i+6 <= len(parts); i += 6 {
		t, szs, mts, mds, lt, name := parts[i], parts[i+1], parts[i+2], parts[i+3], parts[i+4], parts[i+5]
		if name == "" {
			continue
		}
		e := fsEntry{Name: name, LinkTarget: lt}
		switch t {
		case "d":
			e.Kind = "dir"
		case "l":
			e.Kind = "symlink"
		default:
			e.Kind = "file"
		}
		e.Size, _ = strconv.ParseInt(szs, 10, 64)
		if sec, err := strconv.ParseInt(mts, 10, 64); err == nil && sec > 0 {
			e.Mtime = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		if v, err := strconv.ParseUint(mds, 8, 32); err == nil && v > 0 {
			e.Mode = fmt.Sprintf("%04o", v)
		}
		entries = append(entries, e)
	}
	sortEntries(entries)
	return entries
}

// containerListTar is the shell-less fallback: stream a recursive CopyFrom of dir and
// synthesize a listing from the top-level tar headers only. Bodies are skipped (not
// buffered), but the whole subtree is still transferred — a documented cost paid only
// by images without a shell.
func (s *Server) containerListTar(ctx context.Context, workload, dir string) (fsListing, error) {
	pr, pw := io.Pipe()
	go func() {
		_, err := s.cfs.CopyFrom(ctx, workload, dir, pw)
		pw.CloseWithError(err)
	}()
	tr := tar.NewReader(pr)
	base := pathpkg.Base(dir)
	var entries []fsEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = pr.CloseWithError(err)
			if isNotFound(err) {
				return fsListing{}, statusErr(http.StatusNotFound, "%s", dir)
			}
			return fsListing{}, statusErr(http.StatusNotImplemented,
				"cannot list container filesystem: image has neither a shell nor tar (%v)", err)
		}
		// docker cp prefixes entries with the source dir's base name.
		rel := strings.TrimPrefix(pathpkg.Clean(h.Name), base+"/")
		if rel == "" || rel == "." || rel == base || strings.Contains(rel, "/") {
			continue // the dir itself, or a nested descendant
		}
		fi := h.FileInfo()
		e := fsEntry{
			Name: rel, Size: h.Size, Mode: fmt.Sprintf("%04o", fi.Mode().Perm()),
			Mtime: h.ModTime.UTC().Format(time.RFC3339), LinkTarget: h.Linkname,
		}
		switch h.Typeflag {
		case tar.TypeDir:
			e.Kind = "dir"
		case tar.TypeSymlink, tar.TypeLink:
			e.Kind = "symlink"
		default:
			e.Kind = "file"
		}
		entries = append(entries, e)
	}
	sortEntries(entries)
	return fsListing{Source: "container", Path: dir, Entries: entries}, nil
}

// sortEntries orders a listing dirs-first, then case-insensitively by name.
func sortEntries(entries []fsEntry) {
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].Kind == "dir", entries[j].Kind == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// ---- stat ----

// FsStat returns metadata for a single entry.
func (s *Server) FsStat(ctx context.Context, q fsQuery) (fsEntry, error) {
	q, _, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return fsEntry{}, err
	}
	if atRoot {
		return fsEntry{Name: "", Kind: "dir", Mode: "0755"}, nil
	}
	switch q.source {
	case "local":
		full, _, _, err := s.resolveLocal(q.root, q.path)
		if err != nil {
			return fsEntry{}, err
		}
		fi, err := os.Lstat(full)
		if err != nil {
			return fsEntry{}, mapOSErr(err)
		}
		return localEntry(filepath.Dir(full), fi), nil
	case "container":
		if q.workload == "" {
			return fsEntry{}, statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		if err := s.ensureRunning(ctx, q.workload); err != nil {
			return fsEntry{}, err
		}
		p := cleanContainerPath(q.path)
		st, err := s.cfs.StatPath(ctx, q.workload, p)
		if err != nil {
			return fsEntry{}, mapContainerErr(err)
		}
		return pathStatEntry(pathpkg.Base(p), st), nil
	default:
		return fsEntry{}, statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// pathStatEntry converts an api.PathStat into an fsEntry.
func pathStatEntry(name string, st api.PathStat) fsEntry {
	mode := os.FileMode(st.Mode)
	e := fsEntry{
		Name: name, Size: st.Size, Mtime: st.Mtime,
		Mode: fmt.Sprintf("%04o", mode.Perm()), LinkTarget: st.LinkTarget,
	}
	switch {
	case mode&os.ModeSymlink != 0:
		e.Kind = "symlink"
	case mode&os.ModeDir != 0:
		e.Kind = "dir"
	default:
		e.Kind = "file"
	}
	return e
}

// ---- read / download ----

// FsOpen returns a file's contents for reading or download. When bounded (the editor
// read path) a file larger than maxEditableFileSize is a 413; downloads of local
// files are unbounded, but container reads always buffer and so stay capped.
func (s *Server) FsOpen(ctx context.Context, q fsQuery, bounded bool) (name string, data io.ReadCloser, err error) {
	q, _, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return "", nil, err
	}
	if atRoot {
		return "", nil, statusErr(http.StatusBadRequest, "is a directory")
	}
	switch q.source {
	case "local":
		full, _, _, err := s.resolveLocal(q.root, q.path)
		if err != nil {
			return "", nil, err
		}
		fi, err := os.Stat(full)
		if err != nil {
			return "", nil, mapOSErr(err)
		}
		if fi.IsDir() {
			return "", nil, statusErr(http.StatusBadRequest, "is a directory")
		}
		if bounded && fi.Size() > maxEditableFileSize {
			return "", nil, statusErr(http.StatusRequestEntityTooLarge, "file too large")
		}
		f, err := os.Open(full)
		if err != nil {
			return "", nil, mapOSErr(err)
		}
		return fi.Name(), f, nil
	case "container":
		if q.workload == "" {
			return "", nil, statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		if err := s.ensureRunning(ctx, q.workload); err != nil {
			return "", nil, err
		}
		p := cleanContainerPath(q.path)
		st, err := s.cfs.StatPath(ctx, q.workload, p)
		if err != nil {
			return "", nil, mapContainerErr(err)
		}
		if os.FileMode(st.Mode)&os.ModeDir != 0 {
			return "", nil, statusErr(http.StatusBadRequest, "is a directory")
		}
		if bounded && st.Size > maxEditableFileSize {
			return "", nil, statusErr(http.StatusRequestEntityTooLarge, "file too large")
		}
		var buf bytes.Buffer
		if _, err := s.cfs.CopyFrom(ctx, q.workload, p, &buf); err != nil {
			return "", nil, mapContainerErr(err)
		}
		body, err := singleTarEntry(&buf)
		if err != nil {
			return "", nil, err
		}
		return pathpkg.Base(p), io.NopCloser(bytes.NewReader(body)), nil
	default:
		return "", nil, statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// singleTarEntry returns the body of the first regular entry in a tar (docker cp of a
// single file produces exactly one).
func singleTarEntry(r io.Reader) ([]byte, error) {
	tr := tar.NewReader(r)
	h, err := tr.Next()
	if err != nil {
		return nil, statusErr(http.StatusBadGateway, "empty archive from container")
	}
	if h.Typeflag == tar.TypeDir {
		return nil, statusErr(http.StatusBadRequest, "is a directory")
	}
	// A symlink is packed as a HEADER WITH NO BODY (tarcopy.Pack Lstats and switches on
	// the mode), so reading on would yield zero bytes with no error — turning every
	// container symlink into an empty regular file, silently. It has to be named.
	if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
		return nil, statusErr(http.StatusBadRequest, "is a symlink")
	}
	return io.ReadAll(tr)
}

// ---- write / upload / mkdir / rename / delete ----

// FsWrite creates or overwrites a file with data.
func (s *Server) FsWrite(ctx context.Context, q fsQuery, data []byte) error {
	if len(data) > maxEditableFileSize {
		return statusErr(http.StatusRequestEntityTooLarge, "file too large")
	}
	q, st, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return err
	}
	if err := siteWritable(st); err != nil {
		return err
	}
	if atRoot {
		return statusErr(http.StatusBadRequest, "cannot write to the virtual root")
	}
	switch q.source {
	case "local":
		full, _, _, err := s.resolveLocalWrite(q.root, q.path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info, err := os.Stat(full); err == nil {
			mode = info.Mode().Perm()
		}
		return mapOSErr(os.WriteFile(full, data, mode))
	case "container":
		return s.containerPut(ctx, q.workload, cleanContainerPath(q.path), data, false)
	default:
		return statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// FsMkdir creates a directory (and any missing parents).
func (s *Server) FsMkdir(ctx context.Context, q fsQuery) error {
	q, st, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return err
	}
	if err := siteWritable(st); err != nil {
		return err
	}
	if atRoot {
		return statusErr(http.StatusBadRequest, "cannot create a directory at the virtual root")
	}
	switch q.source {
	case "local":
		full, _, _, err := s.resolveLocalWrite(q.root, q.path)
		if err != nil {
			return err
		}
		return mapOSErr(os.MkdirAll(full, 0o755))
	case "container":
		return s.containerPut(ctx, q.workload, cleanContainerPath(q.path), nil, true)
	default:
		return statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// containerPut writes a single file or directory into a workload by extracting a
// one-entry tar into the target's parent directory.
func (s *Server) containerPut(ctx context.Context, workload, p string, data []byte, dir bool) error {
	if workload == "" {
		return statusErr(http.StatusBadRequest, "workload is required for source=container")
	}
	if err := s.ensureRunning(ctx, workload); err != nil {
		return err
	}
	// The option above is best-effort: barehost's gVisor tar-exec path and incus ignore
	// it entirely. Refusing here as well makes the guarantee independent of the backend.
	if st, err := s.cfs.StatPath(ctx, workload, p); err == nil {
		if isDir := os.FileMode(st.Mode)&os.ModeDir != 0; isDir != dir {
			return statusErr(http.StatusConflict,
				"%s already exists and is not the same kind of thing", p)
		}
	}
	parent, base := pathpkg.Dir(p), pathpkg.Base(p)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var hdr *tar.Header
	if dir {
		hdr = &tar.Header{Name: base + "/", Mode: 0o755, Typeflag: tar.TypeDir}
	} else {
		mode := int64(0o644)
		if st, err := s.cfs.StatPath(ctx, workload, p); err == nil {
			if perm := os.FileMode(st.Mode).Perm(); perm != 0 {
				mode = int64(perm)
			}
		}
		hdr = &tar.Header{Name: base, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !dir {
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	// Without NoOverwriteDirNonDir the extractor does os.RemoveAll when an existing
	// DIRECTORY meets a non-directory entry (tarcopy.go:316-320): writing a one-byte
	// file over a same-named non-empty directory would wipe the tree, with no
	// confirmation and no error.
	if err := s.cfs.CopyTo(ctx, workload, parent, &buf, api.CopyToOptions{NoOverwriteDirNonDir: true}); err != nil {
		return mapContainerErr(err)
	}
	return nil
}

// FsRename moves a path to dst within the same root/workload.
func (s *Server) FsRename(ctx context.Context, q fsQuery, dst string) error {
	if dst == "" {
		return statusErr(http.StatusBadRequest, "missing destination")
	}
	if q.source == "virtual" {
		cq, atRoot, err := s.resolveVirtual(q.path)
		if err != nil {
			return err
		}
		if atRoot {
			return statusErr(http.StatusBadRequest, "cannot rename the virtual root")
		}
		dq, dstRoot, err := s.resolveVirtual(dst)
		if err != nil {
			return err
		}
		if dstRoot || dq.source != cq.source || dq.root != cq.root || dq.workload != cq.workload {
			return statusErr(http.StatusBadRequest, "cannot rename across roots (copy instead)")
		}
		q, dst = cq, dq.path
	}
	switch q.source {
	case "local":
		fromFull, _, _, err := s.resolveLocalWrite(q.root, q.path)
		if err != nil {
			return err
		}
		toFull, _, _, err := s.resolveLocalWrite(q.root, dst)
		if err != nil {
			return err
		}
		return mapOSErr(os.Rename(fromFull, toFull))
	case "container":
		if q.workload == "" {
			return statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		if err := s.ensureRunning(ctx, q.workload); err != nil {
			return err
		}
		from, to := cleanContainerPath(q.path), cleanContainerPath(dst)
		// Prefer the operator: it has a real errno, and it works on an image with no
		// shell. The `mv` exec below is the frozen legacy fallback for a path no
		// operator serves, and it is why containerExecOK's exit-code handling still
		// matters (see H13 in the journal).
		if site, err := s.containerSite(ctx, q.workload, from); err == nil {
			if done, err := s.fsopUnary(ctx, site, api.FSOpRequest{Op: api.FSOpRename, Path: from, To: to}); done {
				return err
			}
		}
		return s.containerExecOK(ctx, q.workload, []string{"mv", "--", from, to})
	default:
		return statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// FsDelete removes a path (recursively when recursive is set).
func (s *Server) FsDelete(ctx context.Context, q fsQuery, recursive bool) error {
	q, st, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return err
	}
	if err := siteWritable(st); err != nil {
		return err
	}
	if atRoot {
		return statusErr(http.StatusBadRequest, "cannot delete the virtual root")
	}
	switch q.source {
	case "local":
		full, _, cleanRel, err := s.resolveLocalWrite(q.root, q.path)
		if err != nil {
			return err
		}
		if cleanRel == "" {
			return statusErr(http.StatusBadRequest, "cannot delete the root")
		}
		if recursive {
			return mapOSErr(os.RemoveAll(full))
		}
		return mapOSErr(os.Remove(full))
	case "container":
		if q.workload == "" {
			return statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		if err := s.ensureRunning(ctx, q.workload); err != nil {
			return err
		}
		p := cleanContainerPath(q.path)
		if p == "/" {
			return statusErr(http.StatusBadRequest, "cannot delete the root")
		}
		if done, err := s.fsopUnary(ctx, st, api.FSOpRequest{Op: api.FSOpRemove, Path: p, Recursive: recursive}); done {
			return err
		}
		flag := "-f"
		if recursive {
			flag = "-rf"
		}
		return s.containerExecOK(ctx, q.workload, []string{"rm", flag, "--", p})
	default:
		return statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// containerExecOK runs a direct-exec mutation (no shell) and maps a nonzero exit onto
// an HTTP-coded error.
func (s *Server) containerExecOK(ctx context.Context, workload string, cmd []string) error {
	res, err := s.cfs.Exec(ctx, workload, "", cmd, maxToolCapture)
	if err != nil {
		return mapContainerErr(err)
	}
	// An unreadable exit status is not success. This is a mutation — the caller may
	// delete a source behind it — so "we don't know" must fail loudly rather than
	// inherit the zero value's meaning.
	if !res.ExitKnown {
		return statusErr(http.StatusBadGateway,
			"%s: exit status unavailable, refusing to report success", cmd[0])
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("%s exited %d", cmd[0], res.ExitCode)
		}
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "no such file"):
			return statusErr(http.StatusNotFound, "%s", msg)
		case strings.Contains(low, "permission denied"):
			return statusErr(http.StatusForbidden, "%s", msg)
		default:
			return statusErr(http.StatusBadGateway, "%s", msg)
		}
	}
	return nil
}

// maxCopyDepth bounds a recursive copy. A tree deeper than this inside one request is
// either a mistake or a cycle reached through a mount, and the walk must not run away.
const maxCopyDepth = 32

// FsCopy copies a file or a DIRECTORY from src to dst anywhere in the virtual namespace
// — across local roots and workloads in any combination. Each file rides through memory
// (read then write), so this reuses every source's existing read/write path and stays
// bounded by maxEditableFileSize per file, like the rest of the surface. A directory is
// walked one level at a time, re-creating it at the destination.
//
// When dst is an existing directory the item lands inside it under its source basename,
// matching `cp file dir/` and `cp -r dir other/`.
//
// It returns the paths it deliberately skipped: symlinks that do not resolve to a
// readable file (a link to a directory, a dangling link). Those are named rather than
// aborting a whole tree, since one odd link should not cost the other 500 files —
// everything else fails loudly.
func (s *Server) FsCopy(ctx context.Context, src, dst fsQuery) ([]string, error) {
	src, srcRoot, err := s.virtualize(src)
	if err != nil {
		return nil, err
	}
	dst, dstRoot, err := s.virtualize(dst)
	if err != nil {
		return nil, err
	}
	if srcRoot || dstRoot {
		return nil, statusErr(http.StatusBadRequest, "cannot copy the virtual root")
	}
	return s.copyResolved(ctx, src, dst, true)
}

// copyResolved is FsCopy with the "land inside an existing destination directory" step
// made optional. FsMove has already applied it by the time it delegates here, and
// applying it twice would nest a directory inside its own copy.
func (s *Server) copyResolved(ctx context.Context, src, dst fsQuery, adjust bool) ([]string, error) {
	st, err := s.FsStat(ctx, src)
	if err != nil {
		return nil, err
	}
	if st.Kind != "dir" {
		// A single file keeps the original behaviour, including landing inside dst when
		// dst is a directory.
		if adjust {
			if d, err := s.FsStat(ctx, dst); err == nil && d.Kind == "dir" {
				dst.path = joinChild(dst.path, pathpkg.Base(strings.TrimSuffix(srcBaseOf(src), "/")))
			}
		}
		// Dropping a file into the folder it already lives in resolves the destination
		// back onto the source. It used to answer "ok" having rewritten the file with
		// its own contents — a success report for work that did not happen.
		if s.sameResolvedPath(ctx, src, dst) {
			return nil, errSamePath(opCopy)
		}
		// A single file is worth the same in-place route as a tree: a 4 GB database
		// dump between two volumes is the case that hurts most.
		if done, err := s.copyOnServer(ctx, src, dst); done {
			return nil, err
		}
		return nil, s.streamFile(ctx, src, dst)
	}

	// A mount root has no basename to land under, and copying an entire mount is not
	// what any UI gesture means.
	if src.path == "" || pathpkg.Base(src.path) == "." {
		return nil, statusErr(http.StatusBadRequest, "cannot copy a mount root")
	}
	if adjust {
		if d, err := s.FsStat(ctx, dst); err == nil && d.Kind == "dir" {
			dst.path = joinChild(dst.path, pathpkg.Base(src.path))
		}
	}
	// Copying a directory into itself (or into its own subtree) would recurse into the
	// copy as it is being written. Only reachable when both sides resolved to the same
	// mount; different sources cannot overlap.
	if s.sameResolvedPath(ctx, src, dst) {
		return nil, errSamePath(opCopy)
	}
	if s.dstUnderSrc(ctx, src, dst) {
		return nil, statusErr(http.StatusBadRequest, "cannot copy a folder into itself")
	}
	if done, err := s.copyOnServer(ctx, src, dst); done {
		return nil, err
	}
	var skipped []string
	if err := s.copyTree(ctx, src, dst, 0, &skipped); err != nil {
		return skipped, err
	}
	return skipped, nil
}

// copyOnServer runs the whole copy inside the deployment when both ends live in
// server-side volumes it can already see, so not one byte travels to this process and
// back. done is false when the route does not apply or the operator declined it, and the
// caller relays instead.
//
// Declining is SILENT by design. An operator that cannot serve a path is not a failure —
// the copy still happens, just the long way round — and surfacing it would put an error
// in front of a user whose operation succeeded.
func (s *Server) copyOnServer(ctx context.Context, src, dst fsQuery) (done bool, err error) {
	srcSite, err := s.site(ctx, src)
	if err != nil {
		return false, nil
	}
	dstSite, err := s.site(ctx, dst)
	if err != nil {
		return false, nil
	}
	p := planTransfer(opCopy, srcSite, dstSite, s.serverFSOps(ctx))
	if p.where != execServer {
		return false, nil
	}
	resp, err := s.cfs.FSOp(ctx, srcSite.workload, api.FSOpRequest{
		Op:   api.FSOpCopy,
		Path: srcSite.containerPath,
		To:   dstSite.containerPath,
		// Without this an incoming file that meets an existing directory deletes the
		// whole tree. The relay route sets it too; the two must agree or the same
		// gesture is destructive on one route and refused on the other.
		NoOverwriteDirNonDir: true,
	}, nil, nil)
	if err != nil {
		if errors.Is(err, client.ErrFSOpUnsupported) || resp.Code == api.FSErrUnsupported {
			return false, nil
		}
		return true, fsopStatusErr(resp, err)
	}
	return true, nil
}

// fsopUnary runs a single-path operation through the structured operator when the site is
// one an operator can serve. done is false when it is not — the caller then takes its
// existing route, which for a container path is the frozen legacy exec.
//
// The site check comes first on purpose: an operator only ever serves what was mounted
// into it, so asking about an image-layer path is a round trip whose answer is known.
func (s *Server) fsopUnary(ctx context.Context, st fsSite, req api.FSOpRequest) (done bool, err error) {
	if st.kind != siteServer || st.workload == "" || !s.fsopAvailable(ctx, st.workload) {
		return false, nil
	}
	resp, ferr := s.cfs.FSOp(ctx, st.workload, req, nil, nil)
	if ferr == nil {
		return true, nil
	}
	switch {
	case errors.Is(ferr, client.ErrFSOpUnsupported), resp.Code == api.FSErrUnsupported:
		return false, nil
	case resp.Code == api.FSErrCrossDevice:
		// Two volumes are two filesystems, and rename(2) will not cross them. The
		// legacy `mv` would have (as a non-atomic copy+unlink), so falling through
		// keeps a gesture that used to work working rather than trading it for a
		// tidier error.
		return false, nil
	}
	return true, fsopStatusErr(resp, ferr)
}

// fsopStatusErr turns an operator's refusal into the HTTP status the explorer already
// speaks, so a server-side copy and a relayed one fail the same way to the UI.
func fsopStatusErr(resp api.FSOpResponse, err error) error {
	code := http.StatusInternalServerError
	switch resp.Code {
	case api.FSErrNotFound:
		code = http.StatusNotFound
	case api.FSErrReadOnly:
		code = http.StatusForbidden
	case api.FSErrExists, api.FSErrNotEmpty, api.FSErrCrossDevice:
		code = http.StatusConflict
	case api.FSErrNotDir, api.FSErrIsDir:
		code = http.StatusBadRequest
	}
	msg := resp.Error
	if msg == "" {
		msg = err.Error()
	}
	return statusErr(code, "%s", msg)
}

// dstUnderSrc reports whether the destination is the source itself or ANY descendant of
// it — the walk that would recurse into what it is writing.
//
// It compares RESOLVED paths. The old test compared fsQuery fields, which called two
// names for one directory two different filesystems: with `./data:/data`, copying
// `mount0/tree` into `web/data/tree/sub` is a folder into its own subtree, and a
// spelling comparison sees an ordinary cross-mount copy. It also has to hold for a source
// that is any ancestor, not just the immediate parent, since the runaway is the same at
// any depth.
func (s *Server) dstUnderSrc(ctx context.Context, src, dst fsQuery) bool {
	ss, err := s.site(ctx, src)
	if err != nil {
		return false
	}
	ds, err := s.site(ctx, dst)
	if err != nil {
		return false
	}
	return withinPath(sitePath(ds), sitePath(ss))
}

// copyTree re-creates the directory at src under dst and copies its children. It is one
// FsList per directory, so it works across sources without any source-specific code.
func (s *Server) copyTree(ctx context.Context, src, dst fsQuery, depth int, skipped *[]string) error {
	if depth > maxCopyDepth {
		return statusErr(http.StatusBadRequest, "directory tree too deep to copy: %s", src.path)
	}
	// List BEFORE creating the destination: a source directory that vanished under the
	// walk should leave no empty directory behind at the far end.
	listing, err := s.FsList(ctx, src)
	if err != nil {
		return skipIfGone(err)
	}
	if err := s.FsMkdir(ctx, dst); err != nil {
		return err
	}
	// A truncated listing means entries were dropped before we ever saw them; copying
	// what is left would silently produce an incomplete tree.
	if listing.Truncated {
		return statusErr(http.StatusRequestEntityTooLarge, "directory too large to copy: %s", src.path)
	}
	for _, e := range listing.Entries {
		child, into := src, dst
		child.path = joinChild(src.path, e.Name)
		into.path = joinChild(dst.path, e.Name)
		// One classification for every kind of entry, and it is the SOURCE that makes it
		// (see skippable): an entry that is not a transferable file, or that vanished
		// under the walk, is stepped over and named; everything else fails the copy.
		// Keying it on e.Kind instead — a symlink is always a skip, a file is never one —
		// meant a symlink whose transfer genuinely failed was reported as handled.
		var err error
		if e.Kind == "dir" {
			err = s.copyTree(ctx, child, into, depth+1, skipped)
		} else {
			err = s.copyFileTo(ctx, child, into, e.Size)
		}
		if err != nil {
			if !isSkippable(err) {
				return err
			}
			*skipped = append(*skipped, child.path)
		}
	}
	return nil
}

// copyFileTo copies one file to an exact destination path (no "inside a directory"
// adjustment — the caller has already resolved where it goes). The size comes from the
// listing, so an oversize file is named before it is read.
func (s *Server) copyFileTo(ctx context.Context, src, dst fsQuery, size int64) error {
	// size comes from the listing and is deliberately IGNORED for framing: a listing
	// reports a symlink's own Lstat size, which is the length of the link text.
	// openStream obtains the real one. The argument stays so the signature still says
	// what the walk knows.
	_ = size
	return s.streamFile(ctx, src, dst)
}

// ---- helpers ----

// ensureRunning gives the container source a clean 404/409 instead of a raw upstream
// error when the workload is missing or down.
func (s *Server) ensureRunning(ctx context.Context, name string) error {
	st, err := s.client.Status(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return statusErr(http.StatusNotFound, "workload %q not found", name)
		}
		return err
	}
	if _, running := runningSummary(st); !running {
		return statusErr(http.StatusConflict, "workload %q is not running", name)
	}
	return nil
}

// cleanContainerPath normalizes a container path to a clean absolute path.
func cleanContainerPath(p string) string {
	if p == "" {
		return "/"
	}
	return pathpkg.Clean("/" + strings.TrimPrefix(p, "/"))
}

func isNotFound(err error) bool {
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "not found") || strings.Contains(low, "no such")
}

// mapContainerErr codes an upstream container-fs error (404 for not-found, else 502).
func mapContainerErr(err error) error {
	var se *statusError
	if errors.As(err, &se) {
		return err
	}
	if isNotFound(err) {
		return statusErr(http.StatusNotFound, "%s", err.Error())
	}
	return statusErr(http.StatusBadGateway, "%s", err.Error())
}

// sameResolvedPath reports whether two queries name the SAME bytes.
//
// It compares resolved paths, never the spellings. One file is reachable under two
// different names — a local root and the bind mount that exports it to a container — so
// comparing queries would call `project/data/x` and `web/data/x` different files and
// happily "copy" one onto the other.
func (s *Server) sameResolvedPath(ctx context.Context, a, b fsQuery) bool {
	sa, err := s.site(ctx, a)
	if err != nil {
		return false
	}
	sb, err := s.site(ctx, b)
	if err != nil {
		return false
	}
	return sitePath(sa) == sitePath(sb)
}

// errSamePath is the refusal both the operation and the preflight give, so the report and
// the outcome cannot disagree.
func errSamePath(op fsOp) error {
	return statusErr(http.StatusBadRequest,
		"the source and the destination are the same file; nothing to %s", op)
}

// srcBaseOf names the item a copy is moving, in the spelling its own source uses.
func srcBaseOf(q fsQuery) string {
	p := q.path
	if q.source == "container" {
		p = cleanContainerPath(p)
	}
	return pathpkg.Base(pathpkg.Clean("/" + strings.TrimPrefix(filepath.ToSlash(p), "/")))
}

// ---- move ----

// FsMove moves a file or directory anywhere in the virtual namespace.
//
// It exists because the browser used to compose one: FsRename refuses to cross mounts,
// so a cross-mount move was a copy request followed by a delete request, two round trips
// that could half-succeed with nothing to say so.
//
// When both sides land on the same filesystem — which now includes two different local
// roots, and a container path a client-local bind resolved onto the developer's disk —
// this is one os.Rename: instant, atomic, and indifferent to size. Otherwise it is a
// copy followed by a delete of the source, and the delete happens ONLY after a copy that
// reported nothing skipped. A move that silently dropped the symlinks a copy stepped
// over would be data loss, so a partial copy keeps the source and says what it skipped.
func (s *Server) FsMove(ctx context.Context, src, dst fsQuery) ([]string, error) {
	srcStat, err := s.FsStat(ctx, src)
	if err != nil {
		return nil, err
	}
	// `mv x dir/` lands x inside dir. Applied once, here, and before the sites are
	// resolved: appending a basename can cross a shadow boundary (a bind at /data, a
	// volume at /data/cache), so resolving first would answer for the wrong path.
	if d, err := s.FsStat(ctx, dst); err == nil && d.Kind == "dir" {
		dst.path = joinChild(dst.path, moveBaseName(src))
	}

	srcSite, err := s.site(ctx, src)
	if err != nil {
		return nil, err
	}
	dstSite, err := s.site(ctx, dst)
	if err != nil {
		return nil, err
	}
	// A move mutates BOTH ends: it removes the source as surely as it writes the
	// destination, so a read-only source is as much a refusal as a read-only target.
	if err := siteWritable(srcSite); err != nil {
		return nil, err
	}
	if err := siteWritable(dstSite); err != nil {
		return nil, err
	}

	if sitePath(srcSite) == sitePath(dstSite) {
		return nil, errSamePath(opMove)
	}
	if srcStat.Kind == "dir" && withinPath(sitePath(dstSite), sitePath(srcSite)) {
		return nil, statusErr(http.StatusBadRequest, "cannot move a folder into itself")
	}
	switch p := planTransfer(opMove, srcSite, dstSite, s.serverFSOps(ctx)); p.where {
	case execHere:
		if p.native {
			done, err := s.renameOnClient(srcSite, dstSite)
			if done {
				return nil, err
			}
			// Not renamable (a different device, typically) — fall through and copy.
		}
	case execServer:
		if done, err := s.renameOnServer(ctx, srcSite, dstSite); done {
			return nil, err
		}
		// The operator declined, or the two volumes are different filesystems —
		// fall through to copy-then-delete, exactly as the client route does.
	}

	skipped, err := s.copyResolved(ctx, src, dst, false)
	if err != nil {
		return skipped, err
	}
	if len(skipped) > 0 {
		return skipped, nil // source deliberately kept; the copy was not complete
	}
	return nil, s.FsDelete(ctx, src, srcStat.Kind == "dir")
}

// moveBaseName is the name a moved item keeps. A container path is named by its own
// basename, a local path by the last element of its root-relative path.
func moveBaseName(q fsQuery) string {
	p := q.path
	if q.source == "container" {
		p = cleanContainerPath(p)
	}
	return pathpkg.Base(pathpkg.Clean("/" + strings.TrimPrefix(filepath.ToSlash(p), "/")))
}

// renameOnServer renames inside the deployment, which is what makes a move between two
// paths of one volume free. done is false when the operator declined or the two paths sit
// on different filesystems — the same two reasons renameOnClient gives up — and the
// caller falls back to copy-then-delete.
func (s *Server) renameOnServer(ctx context.Context, src, dst fsSite) (done bool, err error) {
	resp, err := s.cfs.FSOp(ctx, src.workload, api.FSOpRequest{
		Op: api.FSOpRename, Path: src.containerPath, To: dst.containerPath,
	}, nil, nil)
	if err == nil {
		return true, nil
	}
	switch {
	case errors.Is(err, client.ErrFSOpUnsupported),
		resp.Code == api.FSErrUnsupported,
		// Two volumes are two filesystems; rename(2) cannot cross them, and neither
		// can the operator. Copy-then-delete is the answer, not an error.
		resp.Code == api.FSErrCrossDevice:
		return false, nil
	}
	return true, fsopStatusErr(resp, err)
}

// renameOnClient renames within the developer's filesystem. done is false when the
// rename cannot serve — a cross-device move, or a mount point — and the caller should
// fall back to copy-then-delete rather than report a failure.
func (s *Server) renameOnClient(src, dst fsSite) (done bool, err error) {
	from, _, _, err := s.resolveLocal(src.root.ID, src.path)
	if err != nil {
		return true, err
	}
	to, _, _, err := s.resolveLocal(dst.root.ID, dst.path)
	if err != nil {
		return true, err
	}
	if err := os.Rename(from, to); err != nil {
		// EXDEV is the ordinary case (two local roots on different filesystems).
		// EBUSY is a mount point, which every bind target is. Neither is a failure —
		// they mean "not with this syscall".
		if errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.EBUSY) {
			return false, nil
		}
		return true, mapOSErr(err)
	}
	return true, nil
}
