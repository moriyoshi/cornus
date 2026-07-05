// Package fsop executes cornus's structured filesystem operations against a
// LOCAL directory tree, confined to it.
//
// It is the shared body of two callers that reach a workload's bytes by
// completely different routes and must nevertheless agree entry for entry:
//
//   - the caretaker, which sees the volumes a backend mounted into it and maps
//     each one from the path the APP names to the path the caretaker sees;
//   - a co-located host backend, which sees the whole container rootfs through
//     the init process's /proc/<pid>/root and therefore has exactly one root.
//
// Keeping one implementation is the point rather than a tidiness: a copy served
// by either route produces the same tree, including the docker-cp-compatible
// naming, the NoOverwriteDirNonDir refusal, and the FSErr* classification a
// caller decides its next move from. Two implementations of "copy a directory"
// is how routes start disagreeing.
//
// Every operation goes through pkg/deploy/containerdhost/tarcopy, whose path
// resolution (continuity fs.RootPath) is what confines a container symlink so it
// cannot escape to the host.
package fsop

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"strings"
	"syscall"

	"cornus/pkg/api"
)

// Root is one place this process can reach a workload's bytes: Target is the
// path as the WORKLOAD names it, Path is where the same bytes live in this
// process's mount namespace.
//
// A backend serving a whole container rootfs uses a single root with Target "/",
// which makes resolve an identity on the request path. The caretaker uses one
// per mounted volume, which is why the two differ at all.
type Root struct {
	Target   string
	Path     string
	ReadOnly bool
}

// maxEntries bounds one directory listing. It is a truncation, reported as one — a
// caller that copies a truncated listing builds an incomplete tree and calls it success,
// so FSOpResponse.Truncated exists to be checked rather than displayed.
const maxEntries = 50000

// target is a request path resolved onto a root.
type target struct {
	root Root
	// rel is the path WITHIN the root, always absolute so it can be handed straight to
	// tarcopy (whose paths are container-absolute against the confinement root).
	rel string
}

// Resolve reports which root serves p, and the path within it. Exported because
// "does this operator serve that path at all" is a question callers legitimately
// ask before deciding to relay instead — and because a nested root must win over
// the one it sits inside, which is easy to get wrong twice.
func Resolve(roots []Root, p string) (root Root, rel string, ok bool) {
	t, found := resolve(roots, p)
	return t.root, t.rel, found
}

// resolve maps an app-container path onto the root that holds it. The match is on
// path boundaries, not on string prefixes: "/data" must not capture "/database".
func resolve(roots []Root, p string) (target, bool) {
	cp := pathpkg.Clean("/" + strings.TrimPrefix(p, "/"))
	for _, r := range roots {
		switch {
		case cp == r.Target:
			return target{root: r, rel: "/"}, true
		case r.Target == "/":
			return target{root: r, rel: cp}, true
		case strings.HasPrefix(cp, r.Target+"/"):
			return target{root: r, rel: strings.TrimPrefix(cp, r.Target)}, true
		}
	}
	return target{}, false
}

// Run performs one operation against a LOCAL tree, returning the reply and — for
// a get — the tar body to stream after it. It is RunFS with LocalFS, which is
// what every caller that opens files directly wants.
func Run(req api.FSOpRequest, roots []Root, body io.Reader) (api.FSOpResponse, io.ReadCloser) {
	return RunFS(req, roots, LocalFS{}, body)
}

// RunFS is Run against an arbitrary FS. Every decision below — which root serves
// the path, the read-only refusal, the mount-root refusals, the truncation, the
// FSErr* classification — is made HERE and not in the FS, so an operation means
// the same thing however its bytes are reached.
func RunFS(req api.FSOpRequest, roots []Root, fsys FS, body io.Reader) (api.FSOpResponse, io.ReadCloser) {
	t, ok := resolve(roots, req.Path)
	if !ok {
		return api.FSOpUnsupported("no root covers " + req.Path), nil
	}
	switch req.Op {
	case api.FSOpStat:
		st, err := fsys.Stat(t.root.Path, t.rel)
		if err != nil {
			return classify(err), nil
		}
		return api.FSOpResponse{Stat: &st}, nil

	case api.FSOpList:
		return listDir(fsys, t), nil

	case api.FSOpGet:
		// Stat FIRST. Pack of a special file (a socket, a device node) produces an
		// empty but well-formed archive, so without this a missing or unreadable
		// path would arrive as a successful copy of nothing.
		st, err := fsys.Stat(t.root.Path, t.rel)
		if err != nil {
			return classify(err), nil
		}
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(fsys.Pack(t.root.Path, t.rel, pw)) }()
		return api.FSOpResponse{Body: true, Stat: &st}, pr

	case api.FSOpPut:
		if err := writable(t); err != nil {
			return classify(err), nil
		}
		err := fsys.Unpack(t.root.Path, t.rel, body, UnpackOptions{
			NoOverwriteDirNonDir: req.NoOverwriteDirNonDir,
			CopyUIDGID:           req.CopyUIDGID,
		})
		if err != nil {
			return classify(err), nil
		}
		return api.FSOpResponse{}, nil

	case api.FSOpMkdir:
		if err := writable(t); err != nil {
			return classify(err), nil
		}
		if err := fsys.MkdirAll(t.root.Path, t.rel); err != nil {
			return classify(err), nil
		}
		return api.FSOpResponse{}, nil

	case api.FSOpRemove:
		return remove(fsys, t, req.Recursive), nil

	case api.FSOpRename, api.FSOpCopy:
		return transfer(req, roots, fsys, t), nil
	}
	return api.FSOpUnsupported("unknown operation " + string(req.Op)), nil
}

// listDir reads one directory. The truncation is decided HERE rather than in the
// FS: a caller that copies a truncated listing builds an incomplete tree and
// calls it success, so every implementation must truncate at the same bound and
// report it the same way.
func listDir(fsys FS, t target) api.FSOpResponse {
	ents, err := fsys.List(t.root.Path, t.rel)
	if err != nil {
		return classify(err)
	}
	resp := api.FSOpResponse{Entries: ents}
	if len(ents) > maxEntries {
		resp.Entries, resp.Truncated = ents[:maxEntries], true
	}
	if resp.Entries == nil {
		// An empty directory yields an empty slice, which is a different thing
		// from "not a directory".
		resp.Entries = []api.PathStat{}
	}
	return resp
}

// remove deletes a path. Delete-if-exists, matching Backend.Delete and VolumeRemover:
// removing what is not there is a success, so a retried delete does not report a failure
// for work that is already done.
func remove(fsys FS, t target, recursive bool) api.FSOpResponse {
	if err := writable(t); err != nil {
		return classify(err)
	}
	if t.rel == "/" {
		return api.FSOpResponse{Error: "refusing to remove a mount root", Code: api.FSErrReadOnly}
	}
	if err := fsys.Remove(t.root.Path, t.rel, recursive); err != nil {
		return classify(err)
	}
	return api.FSOpResponse{}
}

// transfer serves rename and copy, both of which name a second path and so must
// resolve it against the roots too — a destination under no root is not something this
// operator can do at all, and saying "unsupported" lets the caller relay it instead.
func transfer(req api.FSOpRequest, roots []Root, fsys FS, src target) api.FSOpResponse {
	dst, ok := resolve(roots, req.To)
	if !ok {
		return api.FSOpUnsupported("no root covers " + req.To)
	}
	if err := writable(dst); err != nil {
		return classify(err)
	}
	if req.Op == api.FSOpRename {
		if err := writable(src); err != nil {
			return classify(err)
		}
		return renamePath(fsys, src, dst)
	}
	return copyPath(fsys, src, dst)
}

func renamePath(fsys FS, src, dst target) api.FSOpResponse {
	if err := fsys.Rename(src.root.Path, src.rel, dst.root.Path, dst.rel); err != nil {
		return classify(err)
	}
	return api.FSOpResponse{}
}

// copyPath copies src to dst entirely within the caretaker — the operation the whole role
// exists for, since it is the one where no byte crosses the wire.
//
// It runs Pack into Unpack through a pipe rather than walking the tree itself. That is
// not a shortcut: a copy served here then produces byte-for-byte what a get followed by a
// put would, including the docker-cp naming and the mode/mtime/symlink handling. Two
// implementations of "copy a directory" is exactly how routes start disagreeing.
func copyPath(fsys FS, src, dst target) api.FSOpResponse {
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
	if err := fsys.MkdirAll(dst.root.Path, destDir); err != nil {
		return classify(err)
	}

	pr, pw := io.Pipe()
	packErr := make(chan error, 1)
	go func() {
		err := fsys.Pack(src.root.Path, src.rel, pw)
		pw.CloseWithError(err)
		packErr <- err
	}()
	tarStream := io.Reader(pr)
	if srcBase != dstBase {
		tarStream = renameTarRoot(pr, srcBase, dstBase)
	}
	unpackErr := fsys.Unpack(dst.root.Path, destDir, tarStream, UnpackOptions{})
	// Drain so the packer is never left blocked on a pipe nobody reads.
	pr.CloseWithError(unpackErr)
	if err := <-packErr; err != nil {
		return classify(err)
	}
	if unpackErr != nil {
		return classify(unpackErr)
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

func writable(t target) error {
	if t.root.ReadOnly {
		return &StatusError{Code: api.FSErrReadOnly, Msg: t.root.Target + " is mounted read-only"}
	}
	return nil
}

// StatusError carries an FSErr* code for a refusal that is OURS rather than the
// kernel's. Exported because an FS implementation has refusals of its own —
// "that is not a directory" over a channel that reports no errno — and they must
// classify identically to the local one's.
type StatusError struct {
	Code string
	Msg  string
}

func (e *StatusError) Error() string { return e.Msg }

// classify classifies an error into the FSErr* vocabulary. The classification is the
// point: the caller's next move depends on WHICH failure it was — unsupported means
// "relay this instead", cross-device means "copy then delete", not-found means "tell the
// user" — and a bare error string collapses all three.
func classify(err error) api.FSOpResponse {
	if err == nil {
		return api.FSOpResponse{}
	}
	var se *StatusError
	if errors.As(err, &se) {
		return api.FSOpResponse{Error: se.Msg, Code: se.Code}
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

// Serve is Run plus the body handling a deploy.FSOperator owes its caller: on a
// get, the packed archive is written to out and the response is returned only
// once it is complete.
//
// A caller that saw success and then a short read would write a truncated tree
// and call it a successful copy, so the response waits for the archive.
//
// CLOSING the body is what prevents a leak, not reading it. The packer writes
// into an unbuffered pipe and blocks on its first write; Close unblocks it with
// ErrClosedPipe and it exits. That differs from deploywire.FSOp, which must
// DRAIN because its body shares one stream with the next request — here the pipe
// is private to this call, so a nil out costs nothing and reading the bytes to
// discard them would be pure waste.
func Serve(req api.FSOpRequest, roots []Root, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	return ServeFS(req, roots, LocalFS{}, body, out)
}

// ServeFS is Serve against an arbitrary FS.
func ServeFS(req api.FSOpRequest, roots []Root, fsys FS, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	resp, rc := RunFS(req, roots, fsys, body)
	if rc == nil {
		return resp, nil
	}
	defer rc.Close()
	if out == nil {
		return resp, nil
	}
	if _, err := io.Copy(out, rc); err != nil {
		return resp, fmt.Errorf("fsop: writing body: %w", err)
	}
	return resp, nil
}
