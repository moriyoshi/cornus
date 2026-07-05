//go:build linux

package containerdhost

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// udpServer starts a real UDP socket standing in for the workload port the
// companion dials, and RECORDS every datagram it receives.
//
// It deliberately does NOT echo. An echo server cannot detect a relay that
// transforms the payload symmetrically: double-framing applied on the way out is
// undone on the way back, so the caller sees exactly the bytes it sent while the
// WORKLOAD received something else entirely. That is not hypothetical — it is what
// the first version of this test failed to catch when the framing bug was
// deliberately reintroduced. What the workload receives is the only thing that
// matters here, so the server records it and replies with a fixed token.
func udpServer(t *testing.T, reply string) (port int, received func() [][]byte) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	var mu sync.Mutex
	var got [][]byte
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			got = append(got, append([]byte(nil), buf[:n]...))
			mu.Unlock()
			if _, err := pc.WriteTo([]byte(reply), addr); err != nil {
				return
			}
		}
	}()
	_, portStr, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ = strconv.Atoi(portStr)
	return port, func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := make([][]byte, len(got))
		copy(out, got)
		return out
	}
}

// TestForwardPortViaCompanionUDPDeliversTheCallerPayload covers the
// remote-companion UDP path, which the existing companion tests do not — they
// send TCP only.
//
// forwardPortViaCompanion's own comment (containerdhost/exec_linux.go) records why the distinction is not
// cosmetic: on this side of a companion relay BOTH ends are already framed — the
// caller's tunnel carries wire datagrams, and the caretaker's PortForwardRole
// reads wire datagrams off the stream before writing them to the real UDP socket.
// So the relay must be a byte-for-byte `wire.Pipe`. Using
// `wire.BridgeDatagramStream` (the natural-looking choice, and what the DIRECT
// non-companion path correctly does, because there the far side IS a packet
// socket) adds a second layer of framing, and the workload receives a
// length-prefixed blob instead of its datagram.
//
// # Why this asserts on the SERVER, not on an echo
//
// The first version of this test used a UDP echo server and asserted the caller
// got its bytes back. It PASSED with the framing bug deliberately reintroduced,
// because the extra layer is applied on the way out and stripped on the way back:
// the round trip is symmetric, so the caller cannot tell. The workload can — it is
// the only party that sees the payload without the return trip to undo the damage.
// So the server records what it receives and replies with a fixed token.
func TestForwardPortViaCompanionUDPDeliversTheCallerPayload(t *testing.T) {
	b := &Backend{remote: true, companions: remotecompanion.NewRegistry()}
	serverSess, clientSess := yamuxPipe(t)
	b.companions.Put(remotecompanion.InstanceKey("dns", 0), serverSess)

	const reply = "pong"
	appPort, received := udpServer(t, reply)

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
		if err != nil || proto != "udp" {
			return
		}
		upstream, err := net.Dial("udp", "127.0.0.1:"+p)
		if err != nil {
			return
		}
		defer upstream.Close()
		wire.BridgeDatagramStream(stream, upstream)
	}()

	callerConn, appSideConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- b.ForwardPort(context.Background(), "dns", appPort, "udp", appSideConn)
	}()
	defer func() { callerConn.Close(); <-done }()

	payload := "hello-datagram"
	_ = callerConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := wire.WriteDatagram(callerConn, []byte(payload)); err != nil {
		t.Fatalf("write datagram: %v", err)
	}

	// Read the reply first: it proves the datagram reached the server and came
	// back, so the assertion below is not racing an in-flight write.
	_ = callerConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := wire.ReadDatagram(callerConn)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if string(got) != reply {
		t.Errorf("reply = %q, want %q", got, reply)
	}

	rec := received()
	if len(rec) != 1 {
		t.Fatalf("the workload received %d datagrams, want 1", len(rec))
	}
	if string(rec[0]) != payload {
		t.Errorf("the WORKLOAD received %q, want %q.\n"+
			"The companion relay is transforming the payload — most likely converting framing "+
			"(BridgeDatagramStream) where both ends are already framed, which wraps the datagram in a "+
			"second length prefix. A caller-side echo assertion cannot see this: the extra layer is "+
			"stripped again on the way back.", rec[0], payload)
	}
}

// TestForwardPortViaCompanionUDPPreservesDatagramBoundaries is the assertion a
// single datagram cannot make. A relay that mishandles framing can still deliver
// ONE payload intact by luck of buffering; what it cannot do is keep two separate
// datagrams separate. Boundary preservation is the entire reason the framing
// exists, since a UDP consumer reads one datagram per Read.
func TestForwardPortViaCompanionUDPPreservesDatagramBoundaries(t *testing.T) {
	b := &Backend{remote: true, companions: remotecompanion.NewRegistry()}
	serverSess, clientSess := yamuxPipe(t)
	b.companions.Put(remotecompanion.InstanceKey("dns", 0), serverSess)
	appPort, received := udpServer(t, "ack")

	go func() {
		tag, stream, err := wire.AcceptTagged(clientSess)
		if err != nil || tag != wire.TagPortForward {
			return
		}
		defer stream.Close()
		p, _ := wire.ReadLine(stream)
		proto, err := wire.ReadLine(stream)
		if err != nil || proto != "udp" {
			return
		}
		upstream, err := net.Dial("udp", "127.0.0.1:"+p)
		if err != nil {
			return
		}
		defer upstream.Close()
		wire.BridgeDatagramStream(stream, upstream)
	}()

	callerConn, appSideConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- b.ForwardPort(context.Background(), "dns", appPort, "udp", appSideConn)
	}()
	defer func() { callerConn.Close(); <-done }()

	want := []string{"first", "second"}
	for _, w := range want {
		_ = callerConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := wire.WriteDatagram(callerConn, []byte(w)); err != nil {
			t.Fatalf("write %q: %v", w, err)
		}
		// One ack per datagram, so both have landed before the check below.
		_ = callerConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := wire.ReadDatagram(callerConn); err != nil {
			t.Fatalf("read ack for %q: %v", w, err)
		}
	}

	rec := received()
	if len(rec) != len(want) {
		t.Fatalf("the workload received %d datagrams, want %d: boundaries were coalesced or split, "+
			"which is what a byte-stream relay does to UDP when the framing is mishandled (got %q)",
			len(rec), len(want), rec)
	}
	for i, w := range want {
		if string(rec[i]) != w {
			t.Errorf("workload datagram %d = %q, want %q", i, rec[i], w)
		}
	}
}
