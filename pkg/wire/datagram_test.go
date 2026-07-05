package wire

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestDatagramRoundTrip(t *testing.T) {
	payloads := [][]byte{
		{},
		[]byte("hello"),
		[]byte("HELLO-UDP-HUB"),
		bytes.Repeat([]byte{0xab}, MaxDatagram),
	}
	var buf bytes.Buffer
	for _, p := range payloads {
		if err := WriteDatagram(&buf, p); err != nil {
			t.Fatalf("WriteDatagram(len=%d): %v", len(p), err)
		}
	}
	for i, want := range payloads {
		got, err := ReadDatagram(&buf)
		if err != nil {
			t.Fatalf("ReadDatagram #%d: %v", i, err)
		}
		if got == nil {
			t.Fatalf("ReadDatagram #%d: got nil slice", i)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadDatagram #%d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
	if _, err := ReadDatagram(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadDatagram past end: want EOF, got %v", err)
	}
}

func TestWriteDatagramTooLarge(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDatagram(&buf, make([]byte, MaxDatagram+1)); err == nil {
		t.Fatal("WriteDatagram: want error for oversized payload, got nil")
	}
	if buf.Len() != 0 {
		t.Fatalf("WriteDatagram rejected but wrote %d bytes", buf.Len())
	}
}

func TestReadDatagramShortPayload(t *testing.T) {
	// Length header says 10 bytes but only 3 follow.
	frame := []byte{0x00, 0x0a, 'a', 'b', 'c'}
	_, err := ReadDatagram(bytes.NewReader(frame))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadDatagram short payload: want ErrUnexpectedEOF, got %v", err)
	}
}

func TestReadDatagramShortHeader(t *testing.T) {
	_, err := ReadDatagram(bytes.NewReader([]byte{0x00}))
	if err == nil {
		t.Fatal("ReadDatagram short header: want error, got nil")
	}
}

// TestBridgeDatagramStreamEcho couples a framed in-process stream with a real
// connected UDP socket toward an echo server: a frame written on the stream
// must come back as the same frame, and closing the stream ends the bridge.
func TestBridgeDatagramStreamEcho(t *testing.T) {
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

	up, err := net.Dial("udp", echo.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	near, far := net.Pipe()
	done := make(chan struct{})
	go func() { BridgeDatagramStream(far, up); close(done) }()

	_ = near.SetDeadline(time.Now().Add(5 * time.Second))
	if err := WriteDatagram(near, []byte("bridge-ping")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDatagram(near)
	if err != nil || string(got) != "bridge-ping" {
		t.Fatalf("bridged echo = %q, %v", got, err)
	}

	near.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BridgeDatagramStream did not return after the stream closed")
	}
}

// TestBridgeDatagramStreamEmpty verifies that a zero-length datagram survives a
// full round trip through the bridge. The conn->stream direction must frame the
// empty datagram (conn.Read returns (0, nil) for it) rather than dropping it.
func TestBridgeDatagramStreamEmpty(t *testing.T) {
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

	up, err := net.Dial("udp", echo.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	near, far := net.Pipe()
	done := make(chan struct{})
	go func() { BridgeDatagramStream(far, up); close(done) }()

	_ = near.SetDeadline(time.Now().Add(5 * time.Second))
	if err := WriteDatagram(near, []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDatagram(near)
	if err != nil {
		t.Fatalf("bridged empty echo: %v", err)
	}
	if got == nil {
		t.Fatal("bridged empty echo: got nil slice, want empty non-nil")
	}
	if len(got) != 0 {
		t.Fatalf("bridged empty echo: got %d bytes, want 0", len(got))
	}

	near.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BridgeDatagramStream did not return after the stream closed")
	}
}
