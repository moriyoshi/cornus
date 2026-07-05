package kubernetes

// The kubernetes realization of the archive trio (StatPath / CopyFrom / CopyTo):
// tar, run inside the app container over the pods/exec subresource.
//
// This is the mechanism `kubectl cp` uses, and it is here because it is the only
// way to reach a path in the container IMAGE. The caretaker route (fsop.go) can
// only serve what was mounted into the caretaker — the workload's volumes — and
// the caretaker has its own mount namespace, so the app's image layers are out of
// its reach by construction. Without this, an image-layer path could be listed
// (that is an exec) and then neither read, written, nor copied.
//
// # Why this was refused before, and what changed
//
// fsplan.go argues against shelling into someone else's image to move bytes, and
// the argument is a good one: exit codes that cannot be trusted, GNU and busybox
// disagreeing, distroless images with no tools at all. Two of those three do not
// apply here, and the third is handled rather than ignored:
//
//   - Exit codes ARE trustworthy on this backend. The pods/exec protocol carries
//     the process's termination status out of band, which client-go surfaces as
//     utilexec.CodeExitError. That is a real exit code, not an inspect call racing
//     a dying container.
//   - GNU and busybox agree about the ONE invocation used here. `tar cf - -C dir
//     name` and `tar xf - -C dir` are not extensions; they are what every tar
//     implements, and they are what kubectl cp has relied on for a decade. No
//     flag beyond that is used, so there is nothing for the two to disagree about.
//   - An image with no tar is real and is REPORTED, not papered over: the failure
//     is turned into an "unsupported" error, which the server maps to 501 and the
//     client treats as "this route is not available here" rather than "your copy
//     failed". A workload with volumes then still transfers through the caretaker,
//     exactly as it did before this file existed.
//
// # This does not replace the caretaker route
//
// For a volume-backed path the caretaker is still better and is still preferred
// (see fsplan.go): it needs no tar in the app image, it reports real errnos, and a
// copy between two paths of one volume never leaves the pod. This is the route for
// everything the caretaker cannot see.

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	pathpkg "path"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/utils/exec"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// tarMissingExit is the shell's "command not found". A container whose image has
// no tar answers every archive request with it, which is the one failure that
// must become "unsupported" rather than an error (see errNoTar).
const tarMissingExit = 127

// errNoTar reports an image with no tar binary.
//
// The wording is load-bearing twice over: pkg/server's streamErrStatus maps
// "unsupported" onto 501, and the web BFF's clientContainerFS keys its fallback
// to the caretaker route on exactly that 501. Reword this and a distroless
// workload with volumes stops falling back and starts failing.
func errNoTar(pod string) error {
	return fmt.Errorf("kubernetes: pod %q has no tar in its image, so archive operations on "+
		"container-image paths are unsupported for it; paths inside the workload's volumes are "+
		"still served through the caretaker", pod)
}

// ErrNoTarForTest exposes errNoTar to tests in other packages. It exists so
// pkg/server can assert that this error really does map to the 501 its clients
// fall back on — a chain that spans three packages and is otherwise never
// compiled together.
func ErrNoTarForTest(pod string) error { return errNoTar(pod) }

// execTar runs one tar invocation in the deployment's app container.
//
// stdin, stdout and stderr are wired straight through — no stdcopy framing, since
// the pods/exec protocol demultiplexes for us — so a tar stream crosses without
// being re-encoded. Returns errNoTar when the image has no tar, and otherwise an
// error carrying tar's own stderr, which is the only diagnostic the caller has.
func (b *Backend) execTar(ctx context.Context, pod string, args []string, stdin io.Reader, stdout io.Writer) error {
	if b.restConfig == nil {
		return fmt.Errorf("kubernetes: archive requires a real cluster connection (rest.Config); not available on this backend")
	}
	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(b.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: execContainer,
			Command:   args,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(b.restConfig, "POST", req.URL())
	if err != nil {
		return err
	}
	// Bounded: tar's diagnostics are short, and this is an untrusted image's
	// stderr being read into the server's memory.
	var errBuf bytes.Buffer
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: &limitedWriter{w: &errBuf, n: 8 << 10},
		Tty:    false,
	})
	if streamErr == nil {
		return nil
	}
	var codeErr utilexec.CodeExitError
	if errors.As(streamErr, &codeErr) {
		msg := strings.TrimSpace(errBuf.String())
		if codeErr.ExitStatus() == tarMissingExit || strings.Contains(strings.ToLower(msg), "executable file not found") {
			return errNoTar(pod)
		}
		// tar says "No such file or directory" for a missing path; surfacing it as
		// ErrNotFound is what turns a mistyped path into a 404 instead of a 500.
		if strings.Contains(strings.ToLower(msg), "no such file") {
			return fmt.Errorf("kubernetes: %s: %w", msg, deploy.ErrNotFound)
		}
		if msg == "" {
			msg = fmt.Sprintf("tar exited %d", codeErr.ExitStatus())
		}
		return fmt.Errorf("kubernetes: archive: %s", msg)
	}
	return streamErr
}

// limitedWriter keeps at most n bytes and silently drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		take := p
		if len(take) > l.n {
			take = take[:l.n]
		}
		n, err := l.w.Write(take)
		l.n -= n
		if err != nil {
			return len(p), nil // the sink is ours; never fail the exec over it
		}
	}
	return len(p), nil
}

// packArgs is the pack invocation for a container-absolute path: tar the base
// name from within its parent, so the archive is rooted at the path itself
// rather than carrying its whole ancestry. Same shape docker's archive GET
// produces, which is what the callers already parse.
func packArgs(path string) []string {
	clean := pathpkg.Clean("/" + strings.TrimPrefix(path, "/"))
	return []string{"tar", "cf", "-", "-C", pathpkg.Dir(clean), pathpkg.Base(clean)}
}

// StatPath returns metadata for a path inside the deployment's first pod.
//
// It reads the path's own tar header rather than shelling out to `stat`, which
// looks indirect and is the more robust of the two: it needs no second tool in
// the image, and it sidesteps `stat -c` format strings, where GNU and busybox and
// toybox genuinely do differ. The header carries exactly what api.PathStat holds.
//
// Only the first header is read; the stream is then abandoned (the deferred
// cancel tears the exec down), so a HEAD of a huge file or a deep tree costs one
// block rather than the whole archive.
func (b *Backend) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := b.execTar(ctx, pod, packArgs(path), nil, pw)
		_ = pw.CloseWithError(err)
		done <- err
	}()

	hdr, err := tar.NewReader(pr).Next()
	if err != nil {
		_ = pr.CloseWithError(err)
		// The exec's error is the useful one (no tar, no such file); the reader's
		// is usually just "the stream ended".
		if execErr := <-done; execErr != nil {
			return api.PathStat{}, execErr
		}
		return api.PathStat{}, fmt.Errorf("kubernetes: archive: reading tar header for %s: %w", path, err)
	}
	_ = pr.CloseWithError(context.Canceled)
	return statFromHeader(path, hdr), nil
}

// statFromHeader projects a tar header onto the docker-shaped stat. The NAME
// comes from the requested path, not the header: tar writes the archive-relative
// name, which for a directory carries a trailing slash the other backends do not
// put there.
func statFromHeader(path string, hdr *tar.Header) api.PathStat {
	return api.PathStat{
		Name:       pathpkg.Base(pathpkg.Clean("/" + strings.TrimPrefix(path, "/"))),
		Size:       hdr.Size,
		Mode:       uint32(hdr.FileInfo().Mode()),
		Mtime:      hdr.ModTime.UTC().Format(time.RFC3339Nano),
		LinkTarget: hdr.Linkname,
	}
}

// CopyFrom writes a tar of path to w and returns the path's stat.
//
// The first 512-byte block is buffered so the header can be read for the stat and
// then written on verbatim: the contract is that the tar bytes pass through
// UNCHANGED, so this must not re-encode the stream, only look at it.
func (b *Backend) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return api.PathStat{}, err
	}
	hw := &headerTapWriter{w: w}
	if err := b.execTar(ctx, pod, packArgs(path), nil, hw); err != nil {
		return api.PathStat{}, err
	}
	hdr, err := hw.header()
	if err != nil {
		return api.PathStat{}, fmt.Errorf("kubernetes: archive: reading tar header for %s: %w", path, err)
	}
	return statFromHeader(path, hdr), nil
}

// headerTapWriter passes every byte through while keeping a copy of the first tar
// header block, so the stat can be read from a stream that is not re-encoded.
type headerTapWriter struct {
	w   io.Writer
	buf bytes.Buffer
}

func (h *headerTapWriter) Write(p []byte) (int, error) {
	if h.buf.Len() < tarHeaderSize {
		take := p
		if want := tarHeaderSize - h.buf.Len(); len(take) > want {
			take = take[:want]
		}
		h.buf.Write(take)
	}
	return h.w.Write(p)
}

func (h *headerTapWriter) header() (*tar.Header, error) {
	if h.buf.Len() < tarHeaderSize {
		return nil, io.ErrUnexpectedEOF
	}
	// The reader wants a whole block plus the terminator it will not reach; giving
	// it the one header block is enough for Next() to parse it.
	return tar.NewReader(bytes.NewReader(h.buf.Bytes())).Next()
}

// tarHeaderSize is one tar block, which is one header.
const tarHeaderSize = 512

// CopyTo extracts the tar read from r into path inside the deployment's first pod.
//
// api.CopyToOptions is deliberately NOT emulated. NoOverwriteDirNonDir and
// CopyUIDGID are docker archive semantics with no tar equivalent that busybox and
// GNU both honour, and approximating them would mean the same gesture behaving
// differently depending on which tar the image happened to ship. tar's own
// behaviour is what happens, and it is documented rather than dressed up:
// extraction overwrites, and ownership follows tar's default for the uid running
// in the container.
func (b *Backend) CopyTo(ctx context.Context, name, path string, r io.Reader, _ api.CopyToOptions) error {
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return err
	}
	dst := pathpkg.Clean("/" + strings.TrimPrefix(path, "/"))
	return b.execTar(ctx, pod, []string{"tar", "xf", "-", "-C", dst}, r, io.Discard)
}
