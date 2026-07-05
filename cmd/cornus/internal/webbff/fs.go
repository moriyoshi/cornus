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
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/client"
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
}

// fsRoot is a browsable local root: the project dir or a bind-mount source.
type fsRoot struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
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
}

// localRoot is a resolved, confined browsing root. Real is the symlink-resolved
// absolute anchor every path under it is checked against.
type localRoot struct {
	ID    string
	Label string
	Real  string
}

// ---- the container-filesystem seam ----

// containerFS is the slice of the deploy backend the explorer needs, behind an
// interface so tests can drive the container source without a live daemon (the real
// exec path is a hijacked WebSocket that httptest cannot serve).
type containerFS interface {
	StatPath(ctx context.Context, name, path string) (api.PathStat, error)
	CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error)
	CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error
	Exec(ctx context.Context, name, workdir string, cmd []string) (ExecResult, error)
}

// clientContainerFS is the production containerFS: a thin adapter over the cornus
// server client the BFF already holds.
type clientContainerFS struct{ c *client.Client }

func (f clientContainerFS) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	return f.c.StatPath(ctx, name, path)
}
func (f clientContainerFS) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	return f.c.CopyFrom(ctx, name, path, w)
}
func (f clientContainerFS) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	return f.c.CopyTo(ctx, name, path, r, opts)
}
func (f clientContainerFS) Exec(ctx context.Context, name, workdir string, cmd []string) (ExecResult, error) {
	return execCapture(ctx, f.c, name, workdir, cmd)
}

// execCapture runs cmd once inside a workload (optionally under workdir) and captures
// stdout, stderr, and the exit code, each bounded to maxToolCapture. It is the shared
// core of Server.ExecRun and the container source's directory listing.
func execCapture(ctx context.Context, cl *client.Client, name, workdir string, cmd []string) (ExecResult, error) {
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
	stdout.cap, stderr.cap = maxToolCapture, maxToolCapture
	if _, err := stdcopy.StdCopy(&stdout, &stderr, stream); err != nil {
		return ExecResult{}, err
	}
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if st, err := cl.ExecInspect(ctx, execID); err == nil {
		res.ExitCode = st.ExitCode
	}
	return res, nil
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
	add := func(id, label, dir string) {
		abs, ok := real(dir)
		if !ok {
			return
		}
		for _, existing := range s.localRoots {
			if existing.Real == abs {
				return
			}
		}
		lr := localRoot{ID: id, Label: label, Real: abs}
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
		add("project", label, s.baseDir)
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
			add(fmt.Sprintf("mount%d", i), filepath.Base(m.Source), m.Source)
			i++
		}
	}
}

// Roots is the source switcher's discovery: the local roots plus the workloads the
// container source can target (running ones are flagged).
func (s *Server) Roots(ctx context.Context) fsRoots {
	out := fsRoots{Roots: []fsRoot{}, Workloads: []fsWorkloadRef{}}
	for _, r := range s.localRoots {
		out.Roots = append(out.Roots, fsRoot{ID: r.ID, Label: r.Label, Path: r.Real})
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
		entries = append(entries, fsEntry{Name: r.ID, Kind: "dir", Mode: "0755"})
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
	return fsListing{Source: "virtual", Path: "", Entries: entries}
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
	return fsListing{Source: "local", Root: root.ID, Path: cleanRel, Entries: entries}, nil
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

// listScript enumerates one directory level (the exec WorkingDir) and emits, per
// entry, six NUL-terminated fields: type(d|f|l), size, mtime(epoch), mode(octal),
// symlink-target, name. NUL framing survives spaces and newlines in filenames; the
// glob set matches dotfiles and ".."-prefixed names but never "." or "..".
const listScript = `for e in * .[!.]* ..?*; do
  [ -e "$e" ] || [ -L "$e" ] || continue
  if [ -L "$e" ]; then t=l; lt=$(readlink "$e" 2>/dev/null);
  elif [ -d "$e" ]; then t=d; lt=;
  else t=f; lt=; fi
  sz=$(stat -c %s "$e" 2>/dev/null || echo 0)
  mt=$(stat -c %Y "$e" 2>/dev/null || echo 0)
  md=$(stat -c %a "$e" 2>/dev/null || echo 0)
  printf '%s\0%s\0%s\0%s\0%s\0%s\0' "$t" "$sz" "$mt" "$md" "$lt" "$e"
done`

func (s *Server) containerList(ctx context.Context, workload, p string) (fsListing, error) {
	if err := s.ensureRunning(ctx, workload); err != nil {
		return fsListing{}, err
	}
	dir := cleanContainerPath(p)
	res, err := s.cfs.Exec(ctx, workload, dir, []string{"/bin/sh", "-c", listScript})
	if err != nil {
		return s.containerListTar(ctx, workload, dir) // shell would not start
	}
	if res.ExitCode != 0 {
		low := strings.ToLower(res.Stderr)
		switch {
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
		Truncated: len(res.Stdout) >= maxToolCapture,
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
	q, atRoot, err := s.virtualize(q)
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
	q, atRoot, err := s.virtualize(q)
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
		if st.Size > maxEditableFileSize {
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
	return io.ReadAll(tr)
}

// ---- write / upload / mkdir / rename / delete ----

// FsWrite creates or overwrites a file with data.
func (s *Server) FsWrite(ctx context.Context, q fsQuery, data []byte) error {
	if len(data) > maxEditableFileSize {
		return statusErr(http.StatusRequestEntityTooLarge, "file too large")
	}
	q, atRoot, err := s.virtualize(q)
	if err != nil {
		return err
	}
	if atRoot {
		return statusErr(http.StatusBadRequest, "cannot write to the virtual root")
	}
	switch q.source {
	case "local":
		full, _, _, err := s.resolveLocal(q.root, q.path)
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
	q, atRoot, err := s.virtualize(q)
	if err != nil {
		return err
	}
	if atRoot {
		return statusErr(http.StatusBadRequest, "cannot create a directory at the virtual root")
	}
	switch q.source {
	case "local":
		full, _, _, err := s.resolveLocal(q.root, q.path)
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
	if err := s.cfs.CopyTo(ctx, workload, parent, &buf, api.CopyToOptions{}); err != nil {
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
		fromFull, _, _, err := s.resolveLocal(q.root, q.path)
		if err != nil {
			return err
		}
		toFull, _, _, err := s.resolveLocal(q.root, dst)
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
		return s.containerExecOK(ctx, q.workload, []string{"mv", "--", from, to})
	default:
		return statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// FsDelete removes a path (recursively when recursive is set).
func (s *Server) FsDelete(ctx context.Context, q fsQuery, recursive bool) error {
	q, atRoot, err := s.virtualize(q)
	if err != nil {
		return err
	}
	if atRoot {
		return statusErr(http.StatusBadRequest, "cannot delete the virtual root")
	}
	switch q.source {
	case "local":
		full, _, cleanRel, err := s.resolveLocal(q.root, q.path)
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
	res, err := s.cfs.Exec(ctx, workload, "", cmd)
	if err != nil {
		return mapContainerErr(err)
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

// FsCopy copies a single file from src to dst anywhere in the virtual namespace —
// across local roots and workloads in any combination. It rides the file through
// memory (read then write), so it reuses every source's existing read/write path and
// is bounded by maxEditableFileSize like the rest of the surface. Directories are not
// copied. When dst is an existing directory the file lands inside it under its source
// basename, matching `cp file dir/`.
func (s *Server) FsCopy(ctx context.Context, src, dst fsQuery) error {
	src, srcRoot, err := s.virtualize(src)
	if err != nil {
		return err
	}
	dst, dstRoot, err := s.virtualize(dst)
	if err != nil {
		return err
	}
	if srcRoot || dstRoot {
		return statusErr(http.StatusBadRequest, "cannot copy the virtual root")
	}
	name, body, err := s.FsOpen(ctx, src, true) // bounded read (413 on oversize)
	if err != nil {
		return err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxEditableFileSize+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxEditableFileSize {
		return statusErr(http.StatusRequestEntityTooLarge, "file too large")
	}
	if st, err := s.FsStat(ctx, dst); err == nil && st.Kind == "dir" {
		dst.path = joinChild(dst.path, name)
	}
	return s.FsWrite(ctx, dst, data)
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
