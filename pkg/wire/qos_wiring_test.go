package wire

import (
	"io"
	"net"
	"testing"

	"github.com/hashicorp/yamux"
)

// TestBackingStreamClass verifies the REAL backing openers assign the intended
// yamux QoS class — i.e. the fork's per-stream priority is wired into the actual
// mount/backing data path (OpenBlockBacking / OpenBacking / OpenTagged), not just
// the A/B harness. It guards against regressing to a raw sess.OpenStream() that
// bypasses openTagged and drops the class.
func TestBackingStreamClass(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	cfg := yamuxConfig()
	client, err := yamux.Client(c1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(c2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	// Drain accepted streams so opens that write a name line don't stall.
	go func() {
		for {
			s, err := server.AcceptStream()
			if err != nil {
				return
			}
			go func(s net.Conn) { _, _ = io.Copy(io.Discard, s) }(s)
		}
	}()

	cases := []struct {
		name string
		open func() (net.Conn, error)
		want uint8
	}{
		{"block backing ('b')", func() (net.Conn, error) { return OpenBlockBacking(client, "m") }, yamux.ClassBulk},
		{"9p backing ('L')", func() (net.Conn, error) { return OpenBacking(client, "m") }, yamux.ClassBulk},
		{"control ('C')", func() (net.Conn, error) { return OpenTagged(client, TagControl) }, yamux.ClassHigh},
		{"egress backing ('e')", func() (net.Conn, error) { return OpenEgressBacking(client, "d") }, yamux.ClassNormal},
	}
	for _, tc := range cases {
		conn, err := tc.open()
		if err != nil {
			t.Fatalf("%s: open: %v", tc.name, err)
		}
		s, ok := conn.(*yamux.Stream)
		if !ok {
			t.Fatalf("%s: returned %T, not *yamux.Stream", tc.name, conn)
		}
		if got := s.Priority(); got != tc.want {
			t.Errorf("%s: send class = %d, want %d", tc.name, got, tc.want)
		}
		conn.Close()
	}
}
