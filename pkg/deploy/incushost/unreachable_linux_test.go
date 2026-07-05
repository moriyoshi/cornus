//go:build linux

package incushost

import (
	"context"
	"net"
	"strings"
	"testing"

	incusapi "github.com/lxc/incus/v6/shared/api"
)

// closedTCPPort returns a loopback port with nothing listening on it, so a dial
// to it fails immediately with ECONNREFUSED rather than burning a connect
// timeout. The listener is opened only to claim a port the kernel is not using,
// then closed.
func closedTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return port
}

// TestForwardPortExplainsAContainerizedServer pins the diagnosis a containerized
// incus server owes an operator.
//
// An incus instance lives on incusd's own bridge in the HOST network namespace.
// A cornus in a container of its own has no route to it and — unlike on docker,
// where cornus can attach its own container to the workload's network — no way to
// acquire one, because a docker container is not an incus instance. So the dial
// can only fail, and the kernel's word for that failure ("connection refused",
// "connection timed out") is identical to the word for a workload that is merely
// not listening yet. Without the hint an operator has no way to tell "your
// topology cannot work" from "wait a second and retry".
func TestForwardPortExplainsAContainerizedServer(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	b.isolatedNetwork = true
	id := applyOne(t, b, f, "web")
	f.states[id] = networkedState("127.0.0.1")

	err := b.ForwardPort(context.Background(), "web", closedTCPPort(t), "tcp", nil)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	for _, want := range []string{"own network namespace", "no route to the incus bridge", "CORNUS_INCUS_REMOTE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should carry %q, got %v", want, err)
		}
	}
}

// TestForwardPortAddsNothingOnAHostServer pins that the hint stays out of the way
// of the overwhelmingly common topology. A server on the host CAN route to the
// incus bridge, so a failed dial there means the workload is not listening — and
// telling that operator to set CORNUS_INCUS_REMOTE would send them to rebuild
// their deployment around a problem they do not have.
func TestForwardPortAddsNothingOnAHostServer(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f) // isolatedNetwork stays false: cornus is on the host
	id := applyOne(t, b, f, "web")
	f.states[id] = networkedState("127.0.0.1")

	err := b.ForwardPort(context.Background(), "web", closedTCPPort(t), "tcp", nil)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if strings.Contains(err.Error(), "CORNUS_INCUS_REMOTE") {
		t.Errorf("a host server must not be told to enable remote mode: %v", err)
	}
}

// TestUnreachableHintIsSuppressedInRemoteMode pins the other half of the gate.
// In remote mode ForwardPort never dials the instance at all — it relays through
// the companion — so a failure there has nothing to do with the netns, and
// naming remote mode as the remedy for it would advise enabling what is already
// enabled.
func TestUnreachableHintIsSuppressedInRemoteMode(t *testing.T) {
	b := testBackend(newFakeConn())
	b.isolatedNetwork, b.remote = true, true
	if h := b.unreachableHint(); h != "" {
		t.Errorf("hint = %q, want empty in remote mode", h)
	}
}

// TestPickIPv4IsDeterministicAcrossMultipleNICs is the regression guard for a
// defect that could not be observed from a single call.
//
// pickIPv4 used to range over the state's per-interface map, and Go deliberately
// randomizes map iteration order. With one addressed NIC that is invisible; with
// two or more — a profile that adds a second bridged NIC, or a macvlan/routed NIC
// beside eth0 — it returned a different address on different calls. Where the
// addresses differ in reachability (a macvlan NIC cannot be reached from its own
// host, the same kernel rule dockerhost demotes macvlan networks for), the
// symptom is a port-forward that works roughly half the time, which is far worse
// to diagnose than one that never works.
//
// Four addressed NICs and many iterations: a surviving map range would have to
// pick eth0 first 400 times running.
func TestPickIPv4IsDeterministicAcrossMultipleNICs(t *testing.T) {
	global := func(ip string) incusapi.InstanceStateNetwork {
		return incusapi.InstanceStateNetwork{Addresses: []incusapi.InstanceStateNetworkAddress{
			{Family: "inet", Scope: "global", Address: ip},
		}}
	}
	network := map[string]incusapi.InstanceStateNetwork{
		"lo": {Addresses: []incusapi.InstanceStateNetworkAddress{
			{Family: "inet", Scope: "local", Address: "127.0.0.1"},
		}},
		"eth0":    global("10.1.2.3"),
		"eth1":    global("10.9.9.9"),
		"macvlan": global("192.168.1.50"),
		"wlan0":   global("192.168.2.50"),
	}
	for i := 0; i < 400; i++ {
		// eth0 is both the deterministic answer (interface-name order) and the
		// primary NIC by incus convention.
		if got := pickIPv4(network); got != "10.1.2.3" {
			t.Fatalf("iteration %d: pickIPv4 = %q, want 10.1.2.3 every time", i, got)
		}
	}
}
