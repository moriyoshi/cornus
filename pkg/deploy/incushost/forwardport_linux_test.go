//go:build linux

package incushost

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/deploy"
	"cornus/pkg/wire"
)

// networkedState returns an instance state whose eth0 carries the given global
// IPv4 — what ForwardPort dials, since an Incus instance is reachable from the
// daemon host on its own bridge address.
func networkedState(ip string) *incusapi.InstanceState {
	return &incusapi.InstanceState{
		Status:     "Running",
		StatusCode: incusapi.Running,
		Network: map[string]incusapi.InstanceStateNetwork{
			"lo": {Addresses: []incusapi.InstanceStateNetworkAddress{
				{Family: "inet", Scope: "local", Address: "127.0.0.1"},
			}},
			"eth0": {Addresses: []incusapi.InstanceStateNetworkAddress{
				{Family: "inet6", Scope: "global", Address: "fd42::1"},
				{Family: "inet", Scope: "global", Address: ip},
			}},
		},
	}
}

// TestForwardPortRejectsUnsupportedProtocols pins the refusal for a protocol the
// bridge cannot carry, and that it is refused BEFORE any daemon work — an
// unsupported protocol is a client error, not a deployment lookup failure.
func TestForwardPortRejectsUnsupportedProtocols(t *testing.T) {
	b := testBackend(newFakeConn())
	for _, proto := range []string{"sctp", "TCP", "http"} {
		err := b.ForwardPort(context.Background(), "web", 80, proto, nil)
		if err == nil {
			t.Fatalf("protocol %q: expected a refusal", proto)
		}
		if errors.Is(err, deploy.ErrNotFound) {
			t.Fatalf("protocol %q: refused as not-found (%v); the protocol check must come first", proto, err)
		}
		if !strings.Contains(err.Error(), "only tcp and udp") {
			t.Fatalf("protocol %q: refusal should name what is supported, got %v", proto, err)
		}
	}
}

// TestForwardPortOnAnAbsentDeploymentIsNotFound pins that forwarding to a
// deployment with no instances is deploy.ErrNotFound, so the API answers 404
// rather than a dial error.
func TestForwardPortOnAnAbsentDeploymentIsNotFound(t *testing.T) {
	b := testBackend(newFakeConn())
	if err := b.ForwardPort(context.Background(), "ghost", 80, "tcp", nil); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestInstanceIPv4RefusesAnInstanceWithNoRoutableAddress pins the failure a
// just-created (or stopped) instance produces: without a global IPv4 there is
// nothing to dial, and the error has to say so instead of dialing ":port".
func TestInstanceIPv4RefusesAnInstanceWithNoRoutableAddress(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = &incusapi.InstanceState{
		Status: "Running",
		Network: map[string]incusapi.InstanceStateNetwork{
			"lo": {Addresses: []incusapi.InstanceStateNetworkAddress{
				{Family: "inet", Scope: "local", Address: "127.0.0.1"},
			}},
		},
	}
	_, err := b.instanceIPv4(id)
	if err == nil || !strings.Contains(err.Error(), "no global IPv4 address") {
		t.Fatalf("got %v", err)
	}
}

// TestInstanceIPv4ReportsNotFoundForAVanishedInstance pins the delete race: the
// instance was listed, then removed before its state was read.
func TestInstanceIPv4ReportsNotFoundForAVanishedInstance(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = nil // present in the listing, gone by the time state is read
	if _, err := b.instanceIPv4(id); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestInstanceIPv4PrefersTheInstanceBridgeAddress pins the happy path of address
// selection: the loopback interface is skipped and the global IPv4 (not the
// IPv6) is what gets dialed.
func TestInstanceIPv4PrefersTheInstanceBridgeAddress(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = networkedState("10.1.2.3")
	ip, err := b.instanceIPv4(id)
	if err != nil {
		t.Fatalf("instanceIPv4: %v", err)
	}
	if ip != "10.1.2.3" {
		t.Fatalf("ip = %q, want 10.1.2.3", ip)
	}
}

// echoTCP starts a loopback TCP echo server standing in for a process listening
// inside the instance, and returns its port.
func echoTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

// TestForwardPortBridgesTCPToTheInstanceAddress pins the data plane: bytes
// written by the caller reach the port at the instance's own IP and the reply
// comes back, and the bridge ends when the caller hangs up.
func TestForwardPortBridgesTCPToTheInstanceAddress(t *testing.T) {
	port := echoTCP(t)
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = networkedState("127.0.0.1")

	caller, appSide := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", port, "", appSide) }()

	_ = caller.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := caller.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(caller, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echoed %q", buf)
	}
	caller.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ForwardPort did not return after the caller hung up")
	}
}

// TestForwardPortBridgesUDPAsFramedDatagrams pins the UDP path: the stream side
// speaks cornus's length-framed datagram protocol (a stream cannot preserve
// datagram boundaries on its own), and each frame becomes exactly one packet to
// the instance — and back.
func TestForwardPortBridgesUDPAsFramedDatagrams(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	_, portStr, _ := net.SplitHostPort(pc.LocalAddr().String())
	port, _ := strconv.Atoi(portStr)

	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = networkedState("127.0.0.1")

	caller, appSide := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", port, "udp", appSide) }()

	_ = caller.SetDeadline(time.Now().Add(10 * time.Second))
	if err := wire.WriteDatagram(caller, []byte("hello")); err != nil {
		t.Fatalf("write datagram: %v", err)
	}
	got, err := wire.ReadDatagram(caller)
	if err != nil {
		t.Fatalf("read datagram: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("echoed datagram = %q", got)
	}
	caller.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardPort (udp): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ForwardPort did not return after the caller hung up")
	}
}
