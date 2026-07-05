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
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/continuity/fs"

	"cornus/pkg/api"
	"cornus/pkg/deploy/containerdhost/tarcopy"
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

// fsopTarget is a request path resolved onto a root.
type fsopTarget struct {
	root FSOpRoot
	// rel is the path WITHIN the root, always absolute so it can be handed straight to
	// tarcopy (whose paths are container-absolute against the confinement root).
	rel string
}

// resolveFSOp maps an app-container path onto the root that holds it. The match is on
// path boundaries, not on string prefixes: "/data" must not capture "/database".
func resolveFSOp(roots []FSOpRoot, p string) (fsopTarget, bool) {
	cp := pathpkg.Clean("/" + strings.TrimPrefix(p, "/"))
	for _, r := range roots {
		switch {
		case cp == r.Target:
			return fsopTarget{root: r, rel: "/"}, true
		case r.Target == "/":
			return fsopTarget{root: r, rel: cp}, true
		case strings.HasPrefix(cp, r.Target+"/"):
			return fsopTarget{root: r, rel: strings.TrimPrefix(cp, r.Target)}, true
		}
	}
	return fsopTarget{}, false
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

// runFSOp performs one operation, returning the reply and — for a get — the tar body to
// stream after it.
func runFSOp(req api.FSOpRequest, roots []FSOpRoot, stream net.Conn) (api.FSOpResponse, io.ReadCloser) {
	t, ok := resolveFSOp(roots, req.Path)
	if !ok {
		return api.FSOpUnsupported("no caretaker root covers " + req.Path), nil
	}
	switch req.Op {
	case api.FSOpStat:
		st, err := tarcopy.Stat(t.root.Path, t.rel)
		if err != nil {
			return fsopErr(err), nil
		}
		return api.FSOpResponse{Stat: &st}, nil

	case api.FSOpList:
		return fsopList(t), nil

	case api.FSOpGet:
		// Stat FIRST. Pack of a special file (a socket, a device node) produces an
		// empty but well-formed archive, so without this a missing or unreadable
		// path would arrive as a successful copy of nothing.
		st, err := tarcopy.Stat(t.root.Path, t.rel)
		if err != nil {
			return fsopErr(err), nil
		}
		pr, pw := io.Pipe()
		go func() {
			_, err := tarcopy.Pack(t.root.Path, t.rel, pw)
			pw.CloseWithError(err)
		}()
		return api.FSOpResponse{Body: true, Stat: &st}, pr

	case api.FSOpPut:
		if err := fsopWritable(t); err != nil {
			return fsopErr(err), nil
		}
		err := tarcopy.Unpack(t.root.Path, t.rel, wire.FSOpBodyReader(stream), tarcopy.UnpackOptions{
			NoOverwriteDirNonDir: req.NoOverwriteDirNonDir,
			CopyUIDGID:           req.CopyUIDGID,
		})
		if err != nil {
			return fsopErr(err), nil
		}
		return api.FSOpResponse{}, nil

	case api.FSOpMkdir:
		if err := fsopWritable(t); err != nil {
			return fsopErr(err), nil
		}
		host, err := fs.RootPath(t.root.Path, t.rel)
		if err != nil {
			return fsopErr(err), nil
		}
		if err := os.MkdirAll(host, 0o755); err != nil {
			return fsopErr(err), nil
		}
		return api.FSOpResponse{}, nil

	case api.FSOpRemove:
		return fsopRemove(t, req.Recursive), nil

	case api.FSOpRename, api.FSOpCopy:
		return fsopTransfer(req, roots, t), nil
	}
	return api.FSOpUnsupported("unknown operation " + string(req.Op)), nil
}

// fsopList reads one directory. Symlinks are described, never followed — a listing that
// followed them would report a directory's size and kind for something that is a link,
// and the caller would then walk into it.
func fsopList(t fsopTarget) api.FSOpResponse {
	host, err := fs.RootPath(t.root.Path, t.rel)
	if err != nil {
		return fsopErr(err)
	}
	fi, err := os.Stat(host)
	if err != nil {
		return fsopErr(err)
	}
	if !fi.IsDir() {
		return api.FSOpResponse{Error: t.rel + ": not a directory", Code: api.FSErrNotDir}
	}
	des, err := os.ReadDir(host)
	if err != nil {
		return fsopErr(err)
	}
	resp := api.FSOpResponse{Entries: make([]api.PathStat, 0, len(des))}
	if len(des) > maxFSOpEntries {
		des, resp.Truncated = des[:maxFSOpEntries], true
	}
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			// The entry vanished between the readdir and the stat. A live directory
			// does this; dropping it is right, failing the whole listing is not.
			continue
		}
		e := api.PathStat{
			Name:  de.Name(),
			Size:  info.Size(),
			Mode:  uint32(info.Mode()),
			Mtime: info.ModTime().UTC().Format(time.RFC3339Nano),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			e.LinkTarget, _ = os.Readlink(filepath.Join(host, de.Name()))
		}
		resp.Entries = append(resp.Entries, e)
	}
	return resp
}

// fsopRemove deletes a path. Delete-if-exists, matching Backend.Delete and VolumeRemover:
// removing what is not there is a success, so a retried delete does not report a failure
// for work that is already done.
func fsopRemove(t fsopTarget, recursive bool) api.FSOpResponse {
	if err := fsopWritable(t); err != nil {
		return fsopErr(err)
	}
	if t.rel == "/" {
		return api.FSOpResponse{Error: "refusing to remove a mount root", Code: api.FSErrReadOnly}
	}
	host, err := fs.RootPath(t.root.Path, t.rel)
	if err != nil {
		return fsopErr(err)
	}
	if recursive {
		if err := os.RemoveAll(host); err != nil {
			return fsopErr(err)
		}
		return api.FSOpResponse{}
	}
	if err := os.Remove(host); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fsopErr(err)
	}
	return api.FSOpResponse{}
}

// fsopTransfer serves rename and copy, both of which name a second path and so must
// resolve it against the roots too — a destination under no root is not something this
// operator can do at all, and saying "unsupported" lets the caller relay it instead.
func fsopTransfer(req api.FSOpRequest, roots []FSOpRoot, src fsopTarget) api.FSOpResponse {
	dst, ok := resolveFSOp(roots, req.To)
	if !ok {
		return api.FSOpUnsupported("no caretaker root covers " + req.To)
	}
	if err := fsopWritable(dst); err != nil {
		return fsopErr(err)
	}
	if req.Op == api.FSOpRename {
		if err := fsopWritable(src); err != nil {
			return fsopErr(err)
		}
		return fsopRename(src, dst)
	}
	return fsopCopy(src, dst)
}

func fsopRename(src, dst fsopTarget) api.FSOpResponse {
	from, err := fs.RootPath(src.root.Path, src.rel)
	if err != nil {
		return fsopErr(err)
	}
	to, err := fs.RootPath(dst.root.Path, dst.rel)
	if err != nil {
		return fsopErr(err)
	}
	if err := os.Rename(from, to); err != nil {
		return fsopErr(err)
	}
	return api.FSOpResponse{}
}

// fsopCopy copies src to dst entirely within the caretaker — the operation the whole role
// exists for, since it is the one where no byte crosses the wire.
//
// It runs Pack into Unpack through a pipe rather than walking the tree itself. That is
// not a shortcut: a copy served here then produces byte-for-byte what a get followed by a
// put would, including the docker-cp naming and the mode/mtime/symlink handling. Two
// implementations of "copy a directory" is exactly how routes start disagreeing.
func fsopCopy(src, dst fsopTarget) api.FSOpResponse {
	if src.rel == "/" {
		return api.FSOpResponse{Error: "refusing to copy a mount root", Code: api.FSErrIsDir}
	}
	if dst.rel == "/" {
		return api.FSOpResponse{Error: "refusing to overwrite a mount root", Code: api.FSErrIsDir}
	}
	// Pack names its top-level entry after the SOURCE's basename and Unpack extracts
	// into a directory, so a copy that renames as it goes ("/a/x" -> "/b/y") has to
	// rewrite that one name. Doing it on the tar stream keeps both halves untouched.
	srcBase, dstBase := pathpkg.Base(src.rel), pathpkg.Base(dst.rel)
	destDir := pathpkg.Dir(dst.rel)
	destHost, err := fs.RootPath(dst.root.Path, destDir)
	if err != nil {
		return fsopErr(err)
	}
	if err := os.MkdirAll(destHost, 0o755); err != nil {
		return fsopErr(err)
	}

	pr, pw := io.Pipe()
	packErr := make(chan error, 1)
	go func() {
		_, err := tarcopy.Pack(src.root.Path, src.rel, pw)
		pw.CloseWithError(err)
		packErr <- err
	}()
	body := io.Reader(pr)
	if srcBase != dstBase {
		body = renameTarRoot(pr, srcBase, dstBase)
	}
	unpackErr := tarcopy.Unpack(dst.root.Path, destDir, body, tarcopy.UnpackOptions{})
	// Drain so the packer is never left blocked on a pipe nobody reads.
	pr.CloseWithError(unpackErr)
	if err := <-packErr; err != nil {
		return fsopErr(err)
	}
	if unpackErr != nil {
		return fsopErr(unpackErr)
	}
	return api.FSOpResponse{}
}

// renameTarRoot rewrites the leading path component of every entry from -> to, so a
// packed "x/..." extracts as "y/...". Entry bodies are copied through untouched; only the
// header name (and a hard link's target, which names another entry in the same archive)
// is rewritten.
func renameTarRoot(r io.Reader, from, to string) io.Reader {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(rewriteTarNames(r, pw, from, to)) }()
	return pr
}

func rewriteTarNames(r io.Reader, w io.Writer, from, to string) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return tw.Close()
		}
		if err != nil {
			return err
		}
		hdr.Name = swapTarPrefix(hdr.Name, from, to)
		if hdr.Typeflag == tar.TypeLink {
			hdr.Linkname = swapTarPrefix(hdr.Linkname, from, to)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}

// swapTarPrefix replaces the leading component from with to, leaving anything else
// alone. Matching on the component and not the raw string keeps "x" from rewriting
// "xyz/f".
func swapTarPrefix(name, from, to string) string {
	trimmed := strings.TrimSuffix(name, "/")
	switch {
	case trimmed == from:
		return to + strings.TrimPrefix(name, trimmed)
	case strings.HasPrefix(name, from+"/"):
		return to + strings.TrimPrefix(name, from)
	}
	return name
}

func fsopWritable(t fsopTarget) error {
	if t.root.ReadOnly {
		return &fsopStatusErr{code: api.FSErrReadOnly, msg: t.root.Target + " is mounted read-only"}
	}
	return nil
}

// fsopStatusErr carries an FSErr* code for a refusal that is ours rather than the
// kernel's.
type fsopStatusErr struct {
	code string
	msg  string
}

func (e *fsopStatusErr) Error() string { return e.msg }

// fsopErr classifies an error into the FSErr* vocabulary. The classification is the
// point: the caller's next move depends on WHICH failure it was — unsupported means
// "relay this instead", cross-device means "copy then delete", not-found means "tell the
// user" — and a bare error string collapses all three.
func fsopErr(err error) api.FSOpResponse {
	if err == nil {
		return api.FSOpResponse{}
	}
	var se *fsopStatusErr
	if errors.As(err, &se) {
		return api.FSOpResponse{Error: se.msg, Code: se.code}
	}
	resp := api.FSOpResponse{Error: err.Error()}
	switch {
	case errors.Is(err, os.ErrNotExist):
		resp.Code = api.FSErrNotFound
	case errors.Is(err, os.ErrExist):
		resp.Code = api.FSErrExists
	case errors.Is(err, syscall.EXDEV):
		resp.Code = api.FSErrCrossDevice
	case errors.Is(err, syscall.ENOTEMPTY):
		resp.Code = api.FSErrNotEmpty
	case errors.Is(err, syscall.ENOTDIR):
		resp.Code = api.FSErrNotDir
	case errors.Is(err, syscall.EISDIR):
		resp.Code = api.FSErrIsDir
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EROFS):
		resp.Code = api.FSErrReadOnly
	}
	return resp
}
