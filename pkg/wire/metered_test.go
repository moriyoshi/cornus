package wire

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
)

func TestMeteredConnCounts(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	var wrote, read atomic.Int64
	w := &MeteredConn{Conn: c1, OnWrite: func(n int) { wrote.Add(int64(n)) }}
	r := &MeteredConn{Conn: c2, OnRead: func(n int) { read.Add(int64(n)) }}

	const payload = "hello, 9p mount metering"
	go func() {
		_, _ = io.WriteString(w, payload)
		c1.Close()
	}()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	if wrote.Load() != int64(len(payload)) {
		t.Errorf("OnWrite summed %d, want %d", wrote.Load(), len(payload))
	}
	if read.Load() != int64(len(payload)) {
		t.Errorf("OnRead summed %d, want %d", read.Load(), len(payload))
	}
}

// TestMeteredConnNilCallbacks ensures nil callbacks are a safe passthrough.
func TestMeteredConnNilCallbacks(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := &MeteredConn{Conn: c1} // both callbacks nil
	go func() {
		_, _ = io.WriteString(m, "x")
		c1.Close()
	}()
	got, err := io.ReadAll(c2)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "x" {
		t.Fatalf("payload = %q", got)
	}
}
