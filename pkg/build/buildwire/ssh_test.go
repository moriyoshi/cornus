package buildwire

import (
	"io"
	"net"
	"path/filepath"
	"testing"

	"github.com/hashicorp/yamux"
)

// testYamuxConfig mirrors wire's internal yamux config (log output discarded) for
// the buildwire transport tests that stand up a raw yamux pair.
func testYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}

// TestSSHTunnel verifies the agent-forwarding tunnel: a connection to the
// server's temp socket is proxied over a yamux SSH stream to the caller, which
// forwards it to a local agent socket. No BuildKit involved.
func TestSSHTunnel(t *testing.T) {
	// Fake agent: reads 4 bytes, replies "PONG".
	agentSock := filepath.Join(t.TempDir(), "agent.sock")
	al, err := net.Listen("unix", agentSock)
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()
	go func() {
		for {
			c, err := al.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("PONG"))
			}()
		}
	}()

	// yamux pair over a TCP loopback.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	srvCh := make(chan *yamux.Session, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			srvCh <- nil
			return
		}
		s, _ := yamux.Server(conn, testYamuxConfig())
		srvCh <- s
	}()
	cConn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	callerSess, err := yamux.Client(cConn, testYamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	serverSess := <-srvCh
	if serverSess == nil {
		t.Fatal("server session not established")
	}

	// Caller forwards SSH streams to the fake agent.
	go serveCallerStreams(callerSess, ServeOpts{SSHSockets: map[string]string{"default": agentSock}}, nil, nil)

	// Server side: a temp socket whose connections tunnel to the caller.
	tmpSock := filepath.Join(t.TempDir(), "ssh.sock")
	l, err := net.Listen("unix", tmpSock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s := &ServerSession{sess: serverSess}
	go s.sshAccept(l, "default")

	// Simulate BuildKit dialing the temp socket.
	conn, err := net.Dial("unix", tmpSock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("PING")); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 4)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(resp) != "PONG" {
		t.Fatalf("got %q, want PONG (tunnel didn't reach the agent)", resp)
	}
}
