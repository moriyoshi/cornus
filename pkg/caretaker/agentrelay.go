package caretaker

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/wire"
)

// AgentRelayRole relays ssh-agent-forwarding connections from inside this app
// instance to whichever `cornus exec --forward-agent` session currently holds
// the real local agent for it. SocketPath is a fixed, well-known path inside
// the remote companion's shared scratch volume — the same path `cornus exec`
// injects as SSH_AUTH_SOCK for a --forward-agent session, so the two agree
// with no runtime handshake.
//
// This is caretaker-initiated (like EgressRole): for each local connection
// accepted on SocketPath, the caretaker opens a TagAgentRelay stream to the
// server and pipes. If no exec session currently has the agent forwarded for
// this instance, the server closes the stream immediately (see
// relayAgentMuxed) — the same failure mode real ssh-agent forwarding has when
// nothing is forwarding.
type AgentRelayRole struct {
	Server     string `json:"server"`
	SocketPath string `json:"socketPath"`
}

// runAgentRelay listens on role.SocketPath and relays each accepted
// connection over the pod-scoped session.
func runAgentRelay(ctx context.Context, sess *yamux.Session, role AgentRelayRole) error {
	if dir := filepath.Dir(role.SocketPath); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("agent-relay: mkdir %s: %w", dir, err)
		}
	}
	_ = os.Remove(role.SocketPath) // stale socket from a prior companion generation
	l, err := net.Listen("unix", role.SocketPath)
	if err != nil {
		return fmt.Errorf("agent-relay: listen %s: %w", role.SocketPath, err)
	}
	defer l.Close()
	defer os.Remove(role.SocketPath)
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("agent-relay: accept: %w", err)
		}
		go serveAgentRelayConn(sess, conn)
	}
}

// serveAgentRelayConn opens a caretaker agent-relay stream for one locally
// accepted connection and splices; a fast server-side close (no exec session
// currently forwarding) just ends this one connection.
func serveAgentRelayConn(sess *yamux.Session, conn net.Conn) {
	defer conn.Close()
	stream, err := wire.OpenAgentRelay(sess)
	if err != nil {
		return
	}
	defer stream.Close()
	wire.Pipe(conn, stream)
}

// agentRelayReady reports whether the agent-relay socket exists and accepts
// connections — the readiness the sidecar's startup probe checks.
func agentRelayReady(role AgentRelayRole) error {
	c, err := net.DialTimeout("unix", role.SocketPath, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("agent-relay socket not accepting connections at %s: %w", role.SocketPath, err)
	}
	c.Close()
	return nil
}
