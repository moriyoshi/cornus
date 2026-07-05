package dockerhost

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// yamuxPipe returns a connected (server, client) yamux session pair over a
// real TCP loopback connection — standing in for the WebSocket transport a
// real caretaker/server pair would use.
func yamuxPipe(t *testing.T) (serverSess, clientSess *yamux.Session) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	serverConnCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			serverConnCh <- c
		}
	}()
	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	serverConn := <-serverConnCh
	serverSess, err = yamux.Server(serverConn, nil)
	if err != nil {
		t.Fatalf("yamux.Server: %v", err)
	}
	clientSess, err = yamux.Client(clientConn, nil)
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	t.Cleanup(func() { serverSess.Close(); clientSess.Close() })
	return serverSess, clientSess
}

// TestForwardPortViaCompanion proves that, in remote mode, ForwardPort routes
// through the registered companion connection (opening a server-initiated
// TagPortForward stream) instead of dialing the instance directly — mirroring
// what pkg/caretaker's PortForwardRole does on the real caretaker side.
func TestForwardPortViaCompanion(t *testing.T) {
	b := &Backend{remote: true, companions: remotecompanion.NewRegistry()}

	// serverSess is what handleCaretakerUnified would have accepted from the
	// caretaker; clientSess is the caretaker's own end. Register the SERVER
	// side under the instance ForwardPort will look up (replica 0).
	serverSess, clientSess := yamuxPipe(t)
	b.companions.Put(remotecompanion.InstanceKey("web", 0), serverSess)

	// A fake "app port" inside the shared netns the companion would dial.
	appLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen app port: %v", err)
	}
	defer appLn.Close()
	_, portStr, _ := net.SplitHostPort(appLn.Addr().String())
	port, _ := strconv.Atoi(portStr)
	go func() {
		conn, err := appLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn) // echo
	}()

	// Mimic pkg/caretaker's PortForwardRole accept loop on the caretaker side.
	go func() {
		tag, stream, err := wire.AcceptTagged(clientSess)
		if err != nil || tag != wire.TagPortForward {
			return
		}
		defer stream.Close()
		p, err := wire.ReadLine(stream)
		if err != nil {
			return
		}
		proto, err := wire.ReadLine(stream)
		if err != nil || proto != "tcp" {
			return
		}
		upstream, err := net.Dial("tcp", "127.0.0.1:"+p)
		if err != nil {
			return
		}
		defer upstream.Close()
		wire.Pipe(stream, upstream)
	}()

	// The external port-forward/tunnel caller's side of the tunnel.
	callerConn, appSideConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- b.ForwardPort(context.Background(), "web", port, "tcp", appSideConn)
	}()

	if _, err := callerConn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	callerConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(callerConn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echoed %q, want %q", buf, "ping")
	}
	callerConn.Close()
	<-done
}

// TestForwardPortViaCompanionNotConnected proves a clear error (not a hang or
// panic) when the instance's companion has not registered yet.
func TestForwardPortViaCompanionNotConnected(t *testing.T) {
	b := &Backend{remote: true, companions: remotecompanion.NewRegistry()}
	_, appSideConn := net.Pipe()
	defer appSideConn.Close()
	if err := b.ForwardPort(context.Background(), "web", 80, "tcp", appSideConn); err == nil {
		t.Error("ForwardPort with no registered companion should error, not hang or silently succeed")
	}
}
