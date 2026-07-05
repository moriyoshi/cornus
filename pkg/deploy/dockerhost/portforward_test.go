package dockerhost

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"cornus/pkg/deploy"
	"cornus/pkg/wire"
)

// TestForwardPortEchoes stands up a local TCP echo server as the stand-in
// "container", points the fake Docker engine's inspect at 127.0.0.1, and asserts
// ForwardPort dials the container IP:port and splices bytes both ways.
func TestForwardPortEchoes(t *testing.T) {
	// Echo server plays the container's listening port.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	_, portStr, _ := net.SplitHostPort(echo.Addr().String())
	echoPort := mustAtoi(t, portStr)

	f := &fakeDocker{
		containers: map[string]map[string]any{
			"id-web": {
				"Id": "id-web", "Image": "img", "State": "running",
				"Labels": map[string]string{deploy.LabelApp: "web"},
				"ip":     "127.0.0.1",
			},
		},
	}
	b := newTestBackend(t, f)

	// client end is what the server would hand to ForwardPort; test end is the
	// caller side we write/read on.
	testEnd, clientEnd := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", echoPort, "tcp", clientEnd) }()

	if _, err := io.WriteString(testEnd, "ping-fwd"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("ping-fwd"))
	if err := readFullDeadline(testEnd, got, 2*time.Second); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "ping-fwd" {
		t.Fatalf("forward echo = %q, want ping-fwd", got)
	}

	// Closing our end tears down the bridge and ForwardPort returns.
	testEnd.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardPort did not return after the tunnel closed")
	}
}

// TestForwardPortUDPEchoes stands up a local UDP echo server as the stand-in
// "container" and asserts ForwardPort with proto=udp dials a connected UDP
// socket to the container IP:port and bridges length-prefixed datagram frames
// both ways.
func TestForwardPortUDPEchoes(t *testing.T) {
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, src, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteToUDP(buf[:n], src)
		}
	}()
	echoPort := echo.LocalAddr().(*net.UDPAddr).Port

	f := &fakeDocker{
		containers: map[string]map[string]any{
			"id-web": {
				"Id": "id-web", "Image": "img", "State": "running",
				"Labels": map[string]string{deploy.LabelApp: "web"},
				"ip":     "127.0.0.1",
			},
		},
	}
	b := newTestBackend(t, f)

	testEnd, clientEnd := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", echoPort, "udp", clientEnd) }()

	_ = testEnd.SetDeadline(time.Now().Add(5 * time.Second))
	if err := wire.WriteDatagram(testEnd, []byte("ping-udp-fwd")); err != nil {
		t.Fatal(err)
	}
	got, err := wire.ReadDatagram(testEnd)
	if err != nil {
		t.Fatalf("read echoed datagram: %v", err)
	}
	if string(got) != "ping-udp-fwd" {
		t.Fatalf("udp forward echo = %q, want ping-udp-fwd", got)
	}

	testEnd.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardPort did not return after the tunnel closed")
	}
}

// TestForwardPortRejectsUnknownProto asserts the tcp/udp-only guard.
func TestForwardPortRejectsUnknownProto(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	_, clientEnd := net.Pipe()
	defer clientEnd.Close()
	if err := b.ForwardPort(context.Background(), "web", 80, "sctp", clientEnd); err == nil {
		t.Fatal("ForwardPort with proto=sctp should error")
	}
}

// TestSupportsUDPPortForward pins the capability the server's port-forward
// handler probes before acking a udp tunnel.
func TestSupportsUDPPortForward(t *testing.T) {
	b := newTestBackend(t, &fakeDocker{})
	if !b.SupportsUDPPortForward() {
		t.Fatal("dockerhost must advertise UDP port-forward support")
	}
}

// TestPickNetworkIPDeterministic asserts pickNetworkIP returns a stable,
// routable address for a container on multiple networks regardless of Go's
// randomized map iteration order: the default bridge network is preferred, and
// absent a bridge the lexicographically first network name wins.
func TestPickNetworkIPDeterministic(t *testing.T) {
	// A container on an internal-only overlay plus the default bridge must
	// always yield the bridge IP the server host can route to.
	multi := map[string]string{
		"internal-overlay": "10.0.7.5",
		"bridge":           "172.17.0.3",
	}
	for i := 0; i < 100; i++ {
		if got := pickNetworkIP(multi); got != "172.17.0.3" {
			t.Fatalf("pickNetworkIP(bridge present) = %q, want 172.17.0.3", got)
		}
	}

	// No bridge: choice must still be stable (lexicographically first name).
	noBridge := map[string]string{
		"zeta-net":  "10.0.9.9",
		"alpha-net": "10.0.1.1",
	}
	for i := 0; i < 100; i++ {
		if got := pickNetworkIP(noBridge); got != "10.0.1.1" {
			t.Fatalf("pickNetworkIP(no bridge) = %q, want 10.0.1.1 (alpha-net)", got)
		}
	}

	// A bridge entry with an empty IP must not be chosen; skip to the next.
	emptyBridge := map[string]string{
		"bridge":    "",
		"alpha-net": "10.0.1.1",
	}
	if got := pickNetworkIP(emptyBridge); got != "10.0.1.1" {
		t.Fatalf("pickNetworkIP(empty bridge) = %q, want 10.0.1.1", got)
	}

	if got := pickNetworkIP(map[string]string{}); got != "" {
		t.Fatalf("pickNetworkIP(empty) = %q, want empty string", got)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a port: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func readFullDeadline(c net.Conn, b []byte, d time.Duration) error {
	_ = c.SetReadDeadline(time.Now().Add(d))
	defer c.SetReadDeadline(time.Time{})
	_, err := io.ReadFull(c, b)
	return err
}
