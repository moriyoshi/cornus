package caretaker

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"cornus/pkg/wire"
)

// TestPortForwardRoleRelaysToLocalPort proves runPortForwardAccept accepts a
// server-initiated TagPortForward stream (mirroring wire.OpenPortForward, the
// primitive ForwardPort uses in remote mode) and dials/splices to the
// requested local port.
func TestPortForwardRoleRelaysToLocalPort(t *testing.T) {
	serverSideSess, caretakerSideSess := yamuxPair(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn) // echo
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// runPortForwardAccept has no owned stream/listener to close on its own on
	// shutdown — it relies on its caller closing sess once ctx is done
	// (exactly what runCaretakerConn does in production); mirror that here.
	go func() {
		<-ctx.Done()
		caretakerSideSess.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- runPortForwardAccept(ctx, caretakerSideSess) }()

	stream, err := wire.OpenPortForward(serverSideSess, port, "tcp")
	if err != nil {
		t.Fatalf("OpenPortForward: %v", err)
	}
	defer stream.Close()

	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echoed %q, want %q", buf, "ping")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runPortForwardAccept returned %v on ctx cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPortForwardAccept did not return after ctx cancel (sess not closed?)")
	}
}

// TestPortForwardRoleUDP proves the udp branch bridges framed datagrams.
func TestPortForwardRoleUDP(t *testing.T) {
	serverSideSess, caretakerSideSess := yamuxPair(t)

	udpLn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpLn.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := udpLn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			udpLn.WriteToUDP(buf[:n], addr)
		}
	}()
	port := udpLn.LocalAddr().(*net.UDPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runPortForwardAccept(ctx, caretakerSideSess)

	stream, err := wire.OpenPortForward(serverSideSess, port, "udp")
	if err != nil {
		t.Fatalf("OpenPortForward: %v", err)
	}
	defer stream.Close()

	if err := wire.WriteDatagram(stream, []byte("pingu")); err != nil {
		t.Fatalf("write datagram: %v", err)
	}
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := wire.ReadDatagram(stream)
	if err != nil {
		t.Fatalf("read datagram: %v", err)
	}
	if string(got) != "pingu" {
		t.Fatalf("echoed %q, want %q", got, "pingu")
	}
}
