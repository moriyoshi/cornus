package deploy

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// dialerBackend is a Backend that implements only ForwardPort; every other
// method panics, so a test that accidentally exercises one fails loudly.
type dialerBackend struct {
	Backend
	forward func(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error
}

func (b *dialerBackend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	return b.forward(ctx, name, port, proto, conn)
}

func TestPortForwardDialerBridgesBytes(t *testing.T) {
	var gotName, gotProto string
	var gotPort int
	be := &dialerBackend{forward: func(_ context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
		gotName, gotPort, gotProto = name, port, proto
		defer conn.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return err
		}
		_, err := conn.Write(append([]byte("echo:"), buf...))
		return err
	}}

	conn, err := PortForwardDialer{Backend: be}.PortForward(context.Background(), "web", 8080, "tcp")
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := make([]byte, len("echo:hello"))
	if _, err := io.ReadFull(conn, out); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "echo:hello" {
		t.Errorf("got %q, want %q", out, "echo:hello")
	}
	if gotName != "web" || gotPort != 8080 || gotProto != "tcp" {
		t.Errorf("ForwardPort got (%q,%d,%q), want (web,8080,tcp)", gotName, gotPort, gotProto)
	}
}

func TestPortForwardDialerSurfacesSetupError(t *testing.T) {
	want := errors.New("no ready instance")
	be := &dialerBackend{forward: func(context.Context, string, int, string, io.ReadWriteCloser) error {
		return want
	}}

	conn, err := PortForwardDialer{Backend: be}.PortForward(context.Background(), "web", 8080, "tcp")
	if err != nil {
		t.Fatalf("PortForward: %v", err)
	}
	defer conn.Close()

	// A bare EOF here would leave a proxy rendering an empty 502; the dialer must
	// report why the bridge never came up.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, want) {
		t.Fatalf("Read error = %v, want %v", err, want)
	}
}

func TestPortForwardDialerNilBackend(t *testing.T) {
	if _, err := (PortForwardDialer{}).PortForward(context.Background(), "web", 80, "tcp"); err == nil {
		t.Fatal("a nil backend must not yield a usable connection")
	}
}

// Compile-time proof that the adapter keeps the method set portfwd.Dialer
// requires, without pkg/deploy's tests importing pkg/portfwd.
var _ interface {
	PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error)
} = PortForwardDialer{}
