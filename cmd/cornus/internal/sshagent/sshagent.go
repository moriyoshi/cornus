// Package sshagent dials the caller's local ssh-agent, shared by every CLI
// command that forwards it to the server (`cornus tunnel --forward-agent`,
// `cornus exec --forward-agent`, `cornus compose exec --forward-agent`).
package sshagent

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/hashicorp/yamux"

	"cornus/pkg/wire"
)

// Dial connects to the local ssh-agent at SSH_AUTH_SOCK.
func Dial() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK is not set (no local ssh-agent found)")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("dialing SSH_AUTH_SOCK: %w", err)
	}
	return conn, nil
}

// ServeChannel accepts streams the server opens on sess — one per local
// connection a process inside the exec'd instance makes to the forwarded
// agent socket (see pkg/caretaker's AgentRelayRole and
// pkg/client.Client.ExecAgentChannel) — and relays each to a fresh dial of
// the real local agent. It returns once sess closes or ctx is done.
func ServeChannel(ctx context.Context, sess *yamux.Session) {
	go func() {
		<-ctx.Done()
		sess.Close()
	}()
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go func() {
			defer stream.Close()
			agentConn, err := Dial()
			if err != nil {
				return
			}
			defer agentConn.Close()
			wire.Pipe(agentConn, stream)
		}()
	}
}
