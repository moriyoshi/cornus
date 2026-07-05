package caretaker

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/sync/errgroup"

	"cornus/pkg/wire"
)

// yamuxPair returns a connected yamux client/server session pair over an in-memory
// pipe (no WebSocket), for driving the hub stream primitives in-process.
func yamuxPair(t *testing.T) (client, server *yamux.Session) {
	t.Helper()
	c, s := net.Pipe()
	cs, err := yamux.Client(c, nil)
	if err != nil {
		t.Fatalf("yamux client: %v", err)
	}
	ss, err := yamux.Server(s, nil)
	if err != nil {
		t.Fatalf("yamux server: %v", err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	return cs, ss
}

// TestUDPReachFlow drives runUDPReach end to end: a real UDP socket is the near
// end; a fake hub far end accepts each 'D' stream, reads the service name, and
// echoes framed datagrams. Two distinct client sources prove per-source flow
// routing — each source's echo comes back only to it.
func TestUDPReachFlow(t *testing.T) {
	clientSess, serverSess := yamuxPair(t)

	// Fake hub far end: accept 'D' streams and echo framed datagrams.
	go func() {
		for {
			tag, stream, err := wire.AcceptTagged(serverSess)
			if err != nil {
				return
			}
			if tag != wire.TagData {
				stream.Close()
				continue
			}
			go func() {
				defer stream.Close()
				if _, err := wire.ReadLine(stream); err != nil { // service name
					return
				}
				for {
					d, err := wire.ReadDatagram(stream)
					if err != nil {
						return
					}
					if err := wire.WriteDatagram(stream, d); err != nil {
						return
					}
				}
			}()
		}
	}()

	// Near end: bind an ephemeral UDP socket and run the reach flow over it.
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	reachAddr := pc.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, gctx := errgroup.WithContext(ctx)
	runUDPReach(gctx, g, clientSess, "echo", pc)

	send := func(src *net.UDPConn, msg string) string {
		if _, err := src.WriteToUDP([]byte(msg), reachAddr); err != nil {
			t.Fatalf("send: %v", err)
		}
		src.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 1500)
		n, _, err := src.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("read echo for %q: %v", msg, err)
		}
		return string(buf[:n])
	}

	src1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("src1: %v", err)
	}
	defer src1.Close()
	src2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("src2: %v", err)
	}
	defer src2.Close()

	if got := send(src1, "HELLO-UDP-HUB"); got != "HELLO-UDP-HUB" {
		t.Fatalf("src1 echo: got %q", got)
	}
	if got := send(src2, "second-source"); got != "second-source" {
		t.Fatalf("src2 echo: got %q", got)
	}
	// A second datagram on src1 reuses its flow.
	if got := send(src1, "again"); got != "again" {
		t.Fatalf("src1 reuse echo: got %q", got)
	}
}

// TestUDPReachServerPushKeepsFlowAlive proves the idle GC does not reclaim a
// flow that is actively delivering hub->client datagrams (a server-push /
// asymmetric pattern) even though the client stopped sending after its initial
// subscription. The fake hub reads one subscription datagram, then streams
// replies continuously; with the fix, the delivered replies refresh the flow's
// liveness, so the client keeps receiving past several idle periods.
func TestUDPReachServerPushKeepsFlowAlive(t *testing.T) {
	// Shrink the idle window so the test runs fast; restore afterwards.
	saved := udpFlowIdle
	udpFlowIdle = 150 * time.Millisecond
	defer func() { udpFlowIdle = saved }()

	clientSess, serverSess := yamuxPair(t)

	// Fake hub: a realistic bidirectional bridge. It reads the service name, then
	// concurrently (a) drains client->hub datagrams and (b) streams replies every
	// 20ms. When the client side of the stream goes away — e.g. the idle GC
	// half-closes it — the drain read errors and the pusher stops, exactly as a
	// real provider bridge tears down when its peer disconnects.
	go func() {
		for {
			tag, stream, err := wire.AcceptTagged(serverSess)
			if err != nil {
				return
			}
			if tag != wire.TagData {
				stream.Close()
				continue
			}
			go func() {
				defer stream.Close()
				if _, err := wire.ReadLine(stream); err != nil { // service name
					return
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						if _, err := wire.ReadDatagram(stream); err != nil {
							return
						}
					}
				}()
				for {
					select {
					case <-done:
						return
					default:
					}
					if err := wire.WriteDatagram(stream, []byte("feed")); err != nil {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
	}()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	reachAddr := pc.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, gctx := errgroup.WithContext(ctx)
	runUDPReach(gctx, g, clientSess, "feed", pc)

	src, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("src: %v", err)
	}
	defer src.Close()

	// Subscribe once, then stop sending — only the hub pushes from here on.
	if _, err := src.WriteToUDP([]byte("subscribe"), reachAddr); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Keep reading pushes well past several idle windows. With the fix the flow
	// stays alive as long as pushes flow; without it, the GC closes the flow
	// after one idle period and the reads start timing out.
	buf := make([]byte, 1500)
	deadline := time.Now().Add(6 * udpFlowIdle) // well past several GC ticks
	got := 0
	for time.Now().Before(deadline) {
		src.SetReadDeadline(time.Now().Add(udpFlowIdle))
		n, _, rerr := src.ReadFromUDP(buf)
		if rerr != nil {
			t.Fatalf("push read failed after %d datagrams (flow reclaimed mid-stream?): %v", got, rerr)
		}
		if string(buf[:n]) != "feed" {
			t.Fatalf("push payload: got %q", buf[:n])
		}
		got++
	}
	if got < 5 {
		t.Fatalf("expected sustained server-push delivery, got only %d datagrams", got)
	}
}

// TestDeliverIngressUDP drives the delivery far-end bridge: a real UDP echo server
// is the local target, and deliverIngress bridges a framed stream to it. Proves a
// framed datagram round-trips through a connected UDP socket back onto the stream.
func TestDeliverIngressUDP(t *testing.T) {
	// Real UDP echo server = the delivered service's local target.
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, src, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			echo.WriteToUDP(buf[:n], src)
		}
	}()

	targets := map[string]hubTarget{"echo": {addr: echo.LocalAddr().String(), proto: "udp"}}

	client, server := net.Pipe()
	defer client.Close()
	go deliverIngress(server, targets)

	// The hub side of an ingress stream: name line, then framed datagrams.
	if _, err := client.Write([]byte("echo\n")); err != nil {
		t.Fatalf("write name: %v", err)
	}
	if err := wire.WriteDatagram(client, []byte("PING")); err != nil {
		t.Fatalf("write datagram: %v", err)
	}
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := wire.ReadDatagram(client)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "PING" {
		t.Fatalf("echo: got %q want PING", got)
	}
}
