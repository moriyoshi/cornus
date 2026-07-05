package caretaker

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"cornus/pkg/hub"
	"cornus/pkg/wire"
)

// echoServer starts a TCP echo listener and returns its address and port.
func echoServer(t *testing.T) (addr string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
			}()
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return ln.Addr().String(), p
}

// TestOneDispatcherServesEveryRoleTag pins the fix for the caretaker's duplicate accept
// loops. yamux hands each stream to exactly ONE AcceptStream caller, and the caretaker
// had two — runPortForwardAccept and serveIngress — each closing tags it did not
// recognize. A pod carrying a hub role with delivery targets AND a PortForward role
// therefore raced for every inbound stream and dropped roughly half of each kind, with
// nothing logged: closing a foreign tag is the correct move for a loop that only owns one.
//
// The rounds matter. One stream of each kind would pass under the old design half the
// time; twenty of each makes "the wrong loop won" a certainty rather than a flake.
func TestOneDispatcherServesEveryRoleTag(t *testing.T) {
	const rounds = 20
	serverSideSess, caretakerSideSess := yamuxPair(t)
	addr, port := echoServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The dispatcher has no owned listener to interrupt; it relies on its caller closing
	// the session once ctx is done, exactly as runCaretakerConn does.
	go func() {
		<-ctx.Done()
		caretakerSideSess.Close()
	}()

	disp := newTagDispatch()
	registerPortForward(disp, "")
	registerIngress(disp, map[string]hubTarget{"svc": {addr: addr, proto: "tcp"}})
	done := make(chan error, 1)
	go func() { done <- disp.run(ctx, caretakerSideSess) }()

	roundTrip := func(t *testing.T, stream net.Conn, want string) {
		t.Helper()
		defer stream.Close()
		if _, err := io.WriteString(stream, want); err != nil {
			t.Fatalf("write: %v", err)
		}
		stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, len(want))
		if _, err := io.ReadFull(stream, buf); err != nil {
			t.Fatalf("read echo: %v", err)
		}
		if string(buf) != want {
			t.Fatalf("echoed %q, want %q", buf, want)
		}
	}

	for i := 0; i < rounds; i++ {
		t.Run(fmt.Sprintf("portforward-%d", i), func(t *testing.T) {
			stream, err := wire.OpenPortForward(serverSideSess, port, "tcp")
			if err != nil {
				t.Fatalf("OpenPortForward: %v", err)
			}
			roundTrip(t, stream, "pf")
		})
		t.Run(fmt.Sprintf("deliver-%d", i), func(t *testing.T) {
			stream, err := hub.OpenDeliver(serverSideSess, "svc")
			if err != nil {
				t.Fatalf("OpenDeliver: %v", err)
			}
			roundTrip(t, stream, "dl")
		})
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("dispatch returned %v on ctx cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return after ctx cancel")
	}
}

// TestDispatcherClosesUnknownTags: an unregistered tag is closed, and now that means what
// it says — no other loop could have taken it.
func TestDispatcherClosesUnknownTags(t *testing.T) {
	serverSideSess, caretakerSideSess := yamuxPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ctx.Done()
		caretakerSideSess.Close()
	}()
	disp := newTagDispatch()
	registerPortForward(disp, "")
	go disp.run(ctx, caretakerSideSess)

	stream, err := hub.OpenDeliver(serverSideSess, "svc")
	if err != nil {
		t.Fatalf("OpenDeliver: %v", err)
	}
	defer stream.Close()
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(stream, make([]byte, 1)); err == nil {
		t.Fatal("an unregistered tag was served instead of closed")
	}
}
