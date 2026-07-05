package webbff

// Streaming file transfer: the half of a copy that must not hold a file in memory.
//
// Every transfer used to ride through a []byte bounded by maxEditableFileSize, so a
// 10 MB file was the largest thing the explorer could move and a tree containing one
// failed partway, leaving behind what it had already written. That bound belongs to the
// EDITOR — a compose file is tiny and a text box has to hold it — not to a copy.
//
// Two things make streaming a copy different from streaming a download:
//
//   - A tar entry is framed with its size BEFORE its bytes. The destination has to be
//     told how many to expect, and a source that changes size underneath produces a
//     valid tar that is silently wrong. copyExactly makes that an error instead.
//   - A local destination is published by RENAME, not by writing in place. The container
//     extractor removes an existing file before extracting into it, so an aborted
//     transfer destroys the old content as well as failing — the bigger the file, the
//     wider that window, and uncapping made it very wide.

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"

	"cornus/pkg/api"
)

// openStream opens a file for reading without buffering it.
//
// The size it returns is the one IT obtained, never a listing's: a listing reports a
// symlink's own Lstat size, which is the length of the link TEXT, and framing a tar entry
// with that would truncate the file to a couple of dozen bytes.
func (s *Server) openStream(ctx context.Context, q fsQuery) (rc io.ReadCloser, size int64, mode os.FileMode, err error) {
	cq, _, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return nil, 0, 0, err
	}
	if atRoot {
		return nil, 0, 0, statusErr(http.StatusBadRequest, "is a directory")
	}
	switch cq.source {
	case "local":
		full, _, _, err := s.resolveLocal(cq.root, cq.path)
		if err != nil {
			return nil, 0, 0, err
		}
		// Stat, not Lstat: a symlink is copied as the file it points at, and that
		// file's size is the one the destination must be framed with.
		fi, err := os.Stat(full)
		if err != nil {
			// A dangling symlink and an entry that vanished under the walk both land
			// here, and neither is a reason to abandon the other 500 files.
			return nil, 0, 0, skipIfGone(mapOSErr(err))
		}
		if fi.IsDir() {
			return nil, 0, 0, skipEntry(statusErr(http.StatusBadRequest, "is a directory"))
		}
		if !fi.Mode().IsRegular() {
			// A device node, FIFO or socket. Opening a FIFO blocks forever, and
			// "copying" a device is not a thing the explorer should offer.
			return nil, 0, 0, skipEntry(statusErr(http.StatusBadRequest, "not a regular file"))
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, 0, 0, skipIfGone(mapOSErr(err))
		}
		return f, fi.Size(), fi.Mode().Perm(), nil
	case "container":
		if cq.workload == "" {
			return nil, 0, 0, statusErr(http.StatusBadRequest, "workload is required for source=container")
		}
		if err := s.ensureRunning(ctx, cq.workload); err != nil {
			return nil, 0, 0, err
		}
		p := cleanContainerPath(cq.path)
		st, err := s.cfs.StatPath(ctx, cq.workload, p)
		if err != nil {
			return nil, 0, 0, skipIfGone(mapContainerErr(err))
		}
		m := os.FileMode(st.Mode)
		if m&os.ModeDir != 0 {
			return nil, 0, 0, skipEntry(statusErr(http.StatusBadRequest, "is a directory"))
		}
		rc, err := s.containerReader(ctx, cq.workload, p)
		if err != nil {
			return nil, 0, 0, err
		}
		return rc, st.Size, m.Perm(), nil
	default:
		return nil, 0, 0, statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// containerReader streams one file out of a container: CopyFrom writes a tar into a pipe
// and the reader is parked on that tar's first entry.
//
// Close() joins the producing goroutine and returns ITS error. That matters: CopyFrom
// only checks the X-Cornus-Stream-Error trailer after draining the body to EOF, so a
// caller that abandons the stream — or that only defers Close and discards the result —
// loses the one signal that says the transfer was truncated upstream.
func (s *Server) containerReader(ctx context.Context, workload, p string) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := s.cfs.CopyFrom(ctx, workload, p, pw)
		// CloseWithError on EVERY exit, including a panic: CopyTo/CopyFrom block on the
		// pipe, so failing to close it wedges this goroutine and its reader forever.
		pw.CloseWithError(err)
		done <- err
	}()
	tr := tar.NewReader(pr)
	h, err := tr.Next()
	if err != nil {
		pr.CloseWithError(err)
		<-done
		return nil, statusErr(http.StatusBadGateway, "empty archive from container")
	}
	switch h.Typeflag {
	case tar.TypeDir:
		pr.CloseWithError(nil)
		<-done
		return nil, skipEntry(statusErr(http.StatusBadRequest, "is a directory"))
	case tar.TypeSymlink, tar.TypeLink:
		// Packed as a header with NO BODY, so reading on would yield an empty file with
		// no error — see the H7 note in the journal.
		pr.CloseWithError(nil)
		<-done
		return nil, skipEntry(statusErr(http.StatusBadRequest, "is a symlink"))
	}
	return &containerStream{Reader: tr, pr: pr, done: done}, nil
}

type containerStream struct {
	io.Reader
	pr     *io.PipeReader
	done   chan error
	closed bool
	err    error
}

// Close is IDEMPOTENT, and that is load-bearing rather than tidiness: done carries
// exactly one value, so a second receive would block forever. Callers close this twice
// by design — a deferred Close for the error paths, and an explicit one whose result is
// checked, because the producer's error arrives only here.
func (c *containerStream) Close() error {
	if c.closed {
		return c.err
	}
	c.closed = true
	// Close the pipe first so a producer still writing is released, then take its error.
	c.pr.CloseWithError(nil)
	if err := <-c.done; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		c.err = err
	}
	return c.err
}

// createStream opens a destination for exactly size bytes. The returned writer is
// published only on a successful Close; anything else leaves the destination as it was.
func (s *Server) createStream(ctx context.Context, q fsQuery, size int64, mode os.FileMode) (io.WriteCloser, error) {
	cq, st, atRoot, err := s.resolve(ctx, q)
	if err != nil {
		return nil, err
	}
	if atRoot {
		return nil, statusErr(http.StatusBadRequest, "cannot write to the virtual root")
	}
	if err := siteWritable(st); err != nil {
		return nil, err
	}
	if mode == 0 {
		mode = 0o644
	}
	switch cq.source {
	case "local":
		return s.localCreate(cq, mode)
	case "container":
		return s.containerCreate(ctx, cq, size, mode)
	default:
		return nil, statusErr(http.StatusBadRequest, "source must be local or container")
	}
}

// localCreate writes to a sibling temp and renames on Close, so a failed transfer never
// publishes a half-written file and never destroys the one that was there.
func (s *Server) localCreate(q fsQuery, mode os.FileMode) (io.WriteCloser, error) {
	full, _, _, err := s.resolveLocalWrite(q.root, q.path)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(full); err == nil && !fi.Mode().IsRegular() && !fi.IsDir() {
		return nil, statusErr(http.StatusBadRequest, "refusing to write over a %s", fi.Mode().Type())
	} else if err == nil && fi.IsDir() {
		return nil, statusErr(http.StatusConflict, "%s is a directory", pathpkg.Base(q.path))
	}
	dir, base := filepath.Dir(full), filepath.Base(full)
	// O_EXCL so a temp left by a crashed transfer is never silently reused, and so this
	// can never open an existing device node.
	f, err := os.OpenFile(filepath.Join(dir, "."+base+".cornus-tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, statusErr(http.StatusConflict, "a transfer into %s is already in progress", base)
		}
		return nil, mapOSErr(err)
	}
	return &localStream{f: f, tmp: f.Name(), final: full}, nil
}

type localStream struct {
	f            *os.File
	tmp, final   string
	failed, done bool
}

func (l *localStream) Write(p []byte) (int, error) {
	n, err := l.f.Write(p)
	if err != nil {
		l.failed = true
	}
	return n, err
}

// Fail marks the transfer as abandoned so Close discards the temp instead of publishing
// it. A caller that detects a bad source (it changed size) calls this before Close.
func (l *localStream) Fail() { l.failed = true }

func (l *localStream) Close() error {
	if l.done {
		return nil
	}
	l.done = true
	cerr := l.f.Close()
	if l.failed || cerr != nil {
		os.Remove(l.tmp)
		return cerr
	}
	if err := os.Rename(l.tmp, l.final); err != nil {
		os.Remove(l.tmp)
		return mapOSErr(err)
	}
	return nil
}

// containerCreate streams a one-entry tar into the workload. The entry is framed with
// size up front, which is why the caller must deliver exactly that many bytes.
//
// Unlike the local path this publishes in place: the extractor removes the destination
// before extracting, so an aborted transfer here still loses the previous content. Doing
// better needs a temp name plus a rename inside the container, which means an exec — and
// that would make a plain write depend on a shell the image may not have. Recorded in
// TODO.md rather than traded silently.
func (s *Server) containerCreate(ctx context.Context, q fsQuery, size int64, mode os.FileMode) (io.WriteCloser, error) {
	if q.workload == "" {
		return nil, statusErr(http.StatusBadRequest, "workload is required for source=container")
	}
	if err := s.ensureRunning(ctx, q.workload); err != nil {
		return nil, err
	}
	p := cleanContainerPath(q.path)
	if st, err := s.cfs.StatPath(ctx, q.workload, p); err == nil {
		if os.FileMode(st.Mode)&os.ModeDir != 0 {
			return nil, statusErr(http.StatusConflict, "%s is a directory", pathpkg.Base(p))
		}
	}
	parent, base := pathpkg.Dir(p), pathpkg.Base(p)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := s.cfs.CopyTo(ctx, q.workload, parent, pr, api.CopyToOptions{NoOverwriteDirNonDir: true})
		pr.CloseWithError(err)
		done <- err
	}()
	tw := tar.NewWriter(pw)
	if err := tw.WriteHeader(&tar.Header{
		Name: base, Mode: int64(mode.Perm()), Size: size, Typeflag: tar.TypeReg,
	}); err != nil {
		pw.CloseWithError(err)
		<-done
		return nil, err
	}
	return &containerStreamWriter{tw: tw, pw: pw, done: done}, nil
}

type containerStreamWriter struct {
	tw     *tar.Writer
	pw     *io.PipeWriter
	done   chan error
	failed bool
	closed bool
}

func (c *containerStreamWriter) Write(p []byte) (int, error) {
	n, err := c.tw.Write(p)
	if err != nil {
		c.failed = true
	}
	return n, err
}

func (c *containerStreamWriter) Fail() { c.failed = true }

func (c *containerStreamWriter) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.failed {
		// Tear the pipe down so the extractor sees a truncated stream and errors,
		// rather than committing a file we know is wrong.
		c.pw.CloseWithError(errors.New("transfer abandoned"))
		<-c.done
		return statusErr(http.StatusBadGateway, "transfer abandoned")
	}
	terr := c.tw.Close()
	c.pw.CloseWithError(terr)
	cerr := <-c.done
	if terr != nil {
		return terr
	}
	if cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
		return mapContainerErr(cerr)
	}
	return nil
}

// failer is implemented by both destinations: it says "do not publish what I wrote".
type failer interface{ Fail() }

// zeroReader pads a short source so a tar entry stays well-formed. tarcopy.packFile does
// the same thing for the same reason: a frame that promised N bytes and delivered fewer
// is a corrupt archive, and the error belongs to the caller, not to the tar format.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// copyExactly moves exactly size bytes and treats any other amount as an error.
//
// A tar entry is framed before its bytes, so a file that grows under the copy yields a
// VALID archive silently truncated at the promised size, and one that shrinks yields an
// unterminated entry. Neither is detectable downstream, which is why both are caught
// here.
func copyExactly(dst io.Writer, src io.Reader, size int64) error {
	n, err := io.Copy(dst, io.LimitReader(src, size))
	if err != nil {
		return err
	}
	if n < size {
		// Pad first so the frame stays well-formed, then report.
		if _, perr := io.CopyN(dst, zeroReader{}, size-n); perr != nil {
			return perr
		}
		return statusErr(http.StatusConflict,
			"file shrank while being copied: %d of %d bytes", n, size)
	}
	var probe [1]byte
	if m, _ := src.Read(probe[:]); m > 0 {
		return statusErr(http.StatusConflict, "file grew while being copied (was %d bytes)", size)
	}
	return nil
}

// streamFile copies one file from src to dst with no size limit and no buffering.
func (s *Server) streamFile(ctx context.Context, src, dst fsQuery) error {
	rc, size, mode, err := s.openStream(ctx, src)
	if err != nil {
		return err
	}
	defer rc.Close()

	w, err := s.createStream(ctx, dst, size, mode)
	if err != nil {
		return err
	}
	if err := copyExactly(w, rc, size); err != nil {
		if f, ok := w.(failer); ok {
			f.Fail()
		}
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// The READ side's error surfaces only on Close (the stream-error trailer), so a
	// deferred Close that discards it would call a truncated transfer a success.
	return rc.Close()
}
