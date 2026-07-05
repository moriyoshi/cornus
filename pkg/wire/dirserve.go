package wire

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/hugelgupf/p9/p9"
)

// DirServer exports a directory read-only over 9P2000.L (confined + jailed, via
// confinedAttacher) on a unix socket, so a kernel-9p client can mount it with
// `trans=unix` and pull files on demand. It counts the bytes served via ReadAt —
// the measurement of how much of a context a build actually transfers. This is
// the local/in-process backing for lazy bind mounts; the remote build path serves
// the same 9P tree over the WebSocket transport instead.
type DirServer struct {
	sock  string
	dir   string // temp dir holding the socket
	ln    net.Listener
	reads atomic.Int64

	mu     sync.Mutex
	closed bool
}

// ServeContextDir starts a DirServer exporting root over a fresh unix socket.
func ServeContextDir(root string) (*DirServer, error) {
	tmp, err := os.MkdirTemp("", "cornus-9p-")
	if err != nil {
		return nil, err
	}
	s := &DirServer{sock: filepath.Join(tmp, "ctx.sock"), dir: tmp}
	attacher, err := confinedAttacherCounted(root, nil, &s.reads)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	s.ln = ln
	go s.accept(attacher)
	return s, nil
}

func (s *DirServer) accept(attacher p9.Attacher) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			_ = p9.NewServer(attacher).Handle(conn, conn)
			conn.Close()
		}()
	}
}

// Socket is the unix socket path a 9p client mounts (trans=unix).
func (s *DirServer) Socket() string { return s.sock }

// ReadBytes returns the total bytes served over 9P so far.
func (s *DirServer) ReadBytes() int64 { return s.reads.Load() }

// Close stops the server and removes the socket.
func (s *DirServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.ln.Close()
	return os.RemoveAll(s.dir)
}
