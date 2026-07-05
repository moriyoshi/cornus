package buildwire

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/patternmatcher"

	"cornus/pkg/wire"
)

// --- caller side ------------------------------------------------------------

// serveCallerStreams is the caller's single accept loop for the streams the
// build server opens on demand. It owns sess.AcceptStream and dispatches each
// stream by its 1-byte tag — SSH agent tunnels to the caller's local agent,
// lazy-context 9P backings to the caller's confined read-only export — closing
// any stream with an unrecognized tag. Two goroutines both accepting on one
// session would race (yamux hands each stream to exactly one AcceptStream
// caller), misrouting and closing half of each kind; a single dispatcher avoids
// that. It runs until the session closes.
//
// opts.SSHSockets maps an ssh id to its agent socket path; opts.LazyContexts
// maps a context name to its local directory; ignores holds each context's
// compiled .dockerignore; reads accumulates bytes served over backings.
func serveCallerStreams(sess *yamux.Session, opts ServeOpts, ignores map[string]*patternmatcher.PatternMatcher, reads *atomic.Int64) {
	for {
		tag, stream, err := wire.AcceptTagged(sess)
		if err != nil {
			return
		}
		switch tag {
		case tagSSH:
			go proxySSHToAgent(stream, opts.SSHSockets)
		case tagLazy9P:
			go serveOne9P(stream, opts.LazyContexts, ignores, reads)
		default:
			stream.Close()
		}
	}
}

// serveOne9P serves a single lazy-context 9P backing stream the build server
// opened: it reads the requested context name, then serves that context's local
// directory as a read-only, confined 9P export (honoring its .dockerignore).
// Unknown names are dropped. Bytes written back to the server are counted into
// reads for the caller's "served N bytes" progress line.
func serveOne9P(stream net.Conn, dirs map[string]string, ignores map[string]*patternmatcher.PatternMatcher, reads *atomic.Int64) {
	defer stream.Close()
	name, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	dir, ok := dirs[name]
	if !ok {
		return
	}
	attacher, err := wire.ConfinedAttacher(dir, ignores[name])
	if err != nil {
		return
	}
	var conn net.Conn = stream
	if reads != nil {
		conn = &wire.MeteredConn{Conn: stream, OnWrite: func(n int) { reads.Add(int64(n)) }}
	}
	_ = p9.NewServer(attacher).Handle(conn, conn)
}

func proxySSHToAgent(stream net.Conn, sockets map[string]string) {
	defer stream.Close()
	id, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	sock := sockets[id]
	if sock == "" {
		sock = os.Getenv("SSH_AUTH_SOCK")
	}
	if sock == "" {
		return
	}
	agent, err := net.Dial("unix", sock)
	if err != nil {
		return
	}
	defer agent.Close()
	wire.Pipe(stream, agent)
}

// --- server side ------------------------------------------------------------

// SSH builds an sshprovider whose agent sockets tunnel back to the caller's
// agents over the session. It returns the attachable, a cleanup func, and an
// error. When the build declared no SSH ids it returns (nil, no-op, nil).
func (s *ServerSession) SSH() (session.Attachable, func(), error) {
	if len(s.Spec.SSHIDs) == 0 {
		return nil, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "cornus-ssh-")
	if err != nil {
		return nil, nil, err
	}
	var listeners []net.Listener
	cleanup := func() {
		for _, l := range listeners {
			l.Close()
		}
		os.RemoveAll(dir)
	}

	confs := make([]sshprovider.AgentConfig, 0, len(s.Spec.SSHIDs))
	for _, id := range s.Spec.SSHIDs {
		sock := filepath.Join(dir, sanitizeID(id)+".sock")
		l, err := net.Listen("unix", sock)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		listeners = append(listeners, l)
		go s.sshAccept(l, id)
		confs = append(confs, sshprovider.AgentConfig{ID: id, Paths: []string{sock}})
	}

	provider, err := sshprovider.NewSSHAgentProvider(confs)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("buildwire: ssh provider: %w", err)
	}
	return provider, cleanup, nil
}

func (s *ServerSession) sshAccept(l net.Listener, id string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.sshTunnel(conn, id)
	}
}

// sshTunnel proxies one agent connection from BuildKit (via the temp socket) to
// the caller over a new SSH-tagged stream.
func (s *ServerSession) sshTunnel(conn net.Conn, id string) {
	defer conn.Close()
	stream, err := s.sess.OpenStream()
	if err != nil {
		return
	}
	defer stream.Close()
	if _, err := stream.Write([]byte{tagSSH}); err != nil {
		return
	}
	if _, err := io.WriteString(stream, id+"\n"); err != nil {
		return
	}
	wire.Pipe(conn, stream)
}

// --- helpers ----------------------------------------------------------------

func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
