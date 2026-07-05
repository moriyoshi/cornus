package wire

// The filesystem-operation stream.
//
// A cornus server can already move bytes into and out of a running workload through the
// archive primitives, but only where the backend has a way in: docker's archive endpoint,
// containerd's /proc/<pid>/root, incus's REST file API. Kubernetes has none of those —
// its archive trio is unsupported outright — and even where they exist they can express
// only "pack this path" and "unpack this tar". There is no readdir, no delete, no rename,
// and no way to copy from one place to another without dragging every byte out and back.
//
// TagFSOp is the structured alternative: one stream per operation, carrying a
// length-prefixed request, a length-prefixed reply, and — for the two ops that move bulk
// data — a chunk-framed tar body. It is served by whatever process can actually SEE the
// bytes, which for a pod means the caretaker sidecar and the volumes mounted into it.
//
// Everything here is deliberately free of any pkg/api dependency, which is the package
// invariant (see the package doc): the JSON bodies are opaque byte slices at this layer,
// and their shape is api.FSOpRequest / api.FSOpResponse one level up.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/hashicorp/yamux"
)

// TagFSOp identifies a filesystem-operation stream. Like TagPortForward — and unlike
// every other caretaker tag — the SERVER opens it TOWARD the caretaker: the server is
// the one being asked to do filesystem work, and the caretaker is the only party that
// can see the mounted bytes.
const TagFSOp = tagFSOp

// FSOpMaxFrame bounds one JSON frame. A directory listing is the frame that grows, and
// at roughly 150 bytes an entry this holds well over fifty thousand of them; beyond that
// the operator truncates and says so rather than allocating without limit on behalf of a
// peer.
const FSOpMaxFrame = 8 << 20

// fsopChunk is the body chunk size. Large enough that the 4-byte header is noise, small
// enough that a stalled reader does not sit behind a huge write.
const fsopChunk = 64 << 10

// ErrFSOpTruncated reports a body that ended without its terminator — the failure a bare
// stream cannot distinguish from a clean end, and the reason the body is framed at all.
// A tar reader handed a truncated archive reports a valid short archive, so truncation
// would otherwise be silently indistinguishable from an empty directory.
var ErrFSOpTruncated = errors.New("fsop: body ended before its terminator")

// OpenFSOp opens a server-initiated filesystem-operation stream on a caretaker's own
// session (found through the server's per-instance companion registry) and writes the
// request frame. The caller then writes a body for a put, and reads the reply with
// ReadFSOpFrame.
func OpenFSOp(sess *yamux.Session, req []byte) (net.Conn, error) {
	stream, err := openTagged(sess, tagFSOp)
	if err != nil {
		return nil, err
	}
	if err := WriteFSOpFrame(stream, req); err != nil {
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// WriteFSOpFrame writes one length-prefixed JSON frame.
func WriteFSOpFrame(w io.Writer, b []byte) error {
	if len(b) > FSOpMaxFrame {
		return fmt.Errorf("fsop: frame of %d bytes exceeds the %d limit", len(b), FSOpMaxFrame)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// ReadFSOpFrame reads one length-prefixed JSON frame.
func ReadFSOpFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > FSOpMaxFrame {
		return nil, fmt.Errorf("fsop: frame of %d bytes exceeds the %d limit", n, FSOpMaxFrame)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteFSOpBody copies src onto w as chunk frames and writes the terminator. The
// terminator is the point: a tar reader given a truncated archive reports a short but
// well-formed one, so a body that simply stopped would be indistinguishable from a body
// that finished. A caller that fails midway must NOT call this — it should abandon the
// stream, so the reader sees ErrFSOpTruncated.
func WriteFSOpBody(w io.Writer, src io.Reader) error {
	buf := make([]byte, fsopChunk)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if werr := WriteFSOpFrame(w, buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return WriteFSOpFrame(w, nil)
		}
		if err != nil {
			return err
		}
	}
}

// ReadFSOpBody copies a chunk-framed body from r into dst, stopping at the terminator.
// An EOF before the terminator is ErrFSOpTruncated.
func ReadFSOpBody(r io.Reader, dst io.Writer) error {
	for {
		b, err := ReadFSOpFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return ErrFSOpTruncated
			}
			return err
		}
		if len(b) == 0 {
			return nil
		}
		if _, err := dst.Write(b); err != nil {
			return err
		}
	}
}

// FSOpBodyReader adapts a chunk-framed body to an io.Reader, for a consumer (a tar
// reader) that wants to pull rather than be pushed. Reading past the terminator yields
// io.EOF; a body that ends early yields ErrFSOpTruncated, so the consumer fails instead
// of accepting a short archive.
func FSOpBodyReader(r io.Reader) io.Reader { return &fsopBody{r: r} }

type fsopBody struct {
	r    io.Reader
	buf  []byte
	done bool
	err  error
}

func (b *fsopBody) Read(p []byte) (int, error) {
	for len(b.buf) == 0 {
		if b.err != nil {
			return 0, b.err
		}
		if b.done {
			return 0, io.EOF
		}
		chunk, err := ReadFSOpFrame(b.r)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				b.err = ErrFSOpTruncated
			} else {
				b.err = err
			}
			return 0, b.err
		}
		if len(chunk) == 0 {
			b.done = true
			return 0, io.EOF
		}
		b.buf = chunk
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}
