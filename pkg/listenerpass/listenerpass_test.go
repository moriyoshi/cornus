package listenerpass

import (
	"net"
	"strings"
	"testing"
)

// The transport requirement is not decoration: on unix the descriptor travels as
// ancillary data, which only a unix-domain socket carries. The error has to name
// the type it actually got, because the usual way to reach it is a wrapper that is
// invisible at the call site.
func TestSendAndReceiveRejectNonUnixConnections(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	err = Send(c1, ln, Peer{Pid: 1})
	if err == nil {
		t.Fatal("Send over a non-unix connection succeeded")
	}
	if !strings.Contains(err.Error(), "net.UnixConn") {
		t.Errorf("error %q does not say what it needs", err)
	}

	if _, err := Receive(c2); err == nil {
		t.Fatal("Receive over a non-unix connection succeeded")
	}
}

func TestNilArgumentsAreRejected(t *testing.T) {
	if err := Send(nil, nil, Peer{}); err == nil {
		t.Error("Send(nil, nil) succeeded")
	}
	if _, err := Receive(nil); err == nil {
		t.Error("Receive(nil) succeeded")
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 372, maxPayload} {
		got, err := decodeHeader(encodeHeader(n))
		if err != nil {
			t.Errorf("decodeHeader(encodeHeader(%d)) = %v", n, err)
			continue
		}
		if got != n {
			t.Errorf("round-tripped %d as %d", n, got)
		}
	}
}

// A corrupt or hostile length must not become an allocation.
func TestHeaderRejectsAnOversizedPayload(t *testing.T) {
	if _, err := decodeHeader(encodeHeader(maxPayload + 1)); err == nil {
		t.Error("decodeHeader accepted a payload over the limit")
	}
	if _, err := decodeHeader([]byte{1, 2}); err == nil {
		t.Error("decodeHeader accepted a short header")
	}
}
