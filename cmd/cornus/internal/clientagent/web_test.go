package clientagent

import (
	"net"
	"testing"

	"cornus/pkg/clientconduit"
)

// This file is what remains of web_test.go. Most of it tested the agent hosting web
// UIs, which it no longer does — `cornus web --publish-in-conduit` hosts its own BFF
// and publishes it through the rendezvous. What survives is the conduit-sharing
// behaviour those tests happened to exercise through `web-serve`, driven here through
// the docker frontend instead.

func socks5Conduit() ConduitCfg {
	return ConduitCfg{Mode: clientconduit.ModeSocks5, Socks5Listen: "127.0.0.1:0"}
}

func socks5ConduitWith(suffix string) ConduitCfg {
	cfg := socks5Conduit()
	cfg.Socks5Suffix = suffix
	return cfg
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func theConn(t *testing.T, a *Agent) *connState {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.conns) != 1 {
		t.Fatalf("conns = %d, want exactly 1", len(a.conns))
	}
	for _, cs := range a.conns {
		return cs
	}
	return nil
}

func conduitCounts(t *testing.T, a *Agent, cs *connState) (int, map[conduitKey]int) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	refs := map[conduitKey]int{}
	for k, es := range cs.conduit {
		refs[k] = es.refs
	}
	return len(cs.conduit), refs
}

// dockerAt starts a docker frontend on cfg, standing in for any frontend that
// brings a conduit up.
func dockerAt(t *testing.T, a *Agent, cfg ConduitCfg) {
	t.Helper()
	resp := a.doDockerServe(Request{
		Socket:  t.TempDir() + "/docker.sock",
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Conduit: ToWireConduit(cfg),
	})
	if !resp.OK {
		t.Fatalf("docker-serve = %+v", resp)
	}
}

// Two configurations naming one address are ONE conduit, not a collision. This is
// the rule the redesign turns on: a conduit is its address, so the second caller
// shares it and the incumbent's settings govern.
func TestSecondConduitAtAHeldAddressConsolidates(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	addr := freeAddr(t)

	held := socks5ConduitWith(".demo.internal")
	held.Socks5Listen = addr
	dockerAt(t, a, held)
	cs := theConn(t, a)

	// Same address, different suffix. Under the old settings-keyed model this was
	// refused with a bind conflict.
	other := socks5ConduitWith(".other.internal")
	other.Socks5Listen = addr
	resp := a.doDockerServe(Request{
		Socket:  t.TempDir() + "/second.sock",
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Conduit: ToWireConduit(other),
	})
	if !resp.OK {
		t.Fatalf("a second conduit at a held address must consolidate, not fail: %s", resp.Error)
	}
	if n, refs := conduitCounts(t, a, cs); n != 1 {
		t.Fatalf("conduits = %d (refs %v), want 1: one address is one conduit", n, refs)
	}
}

// And two different addresses stay two conduits — the only thing that should split
// them.
func TestConduitDifferentAddressesDoNotConflict(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))

	first := socks5ConduitWith(".demo.internal")
	first.Socks5Listen = freeAddr(t)
	dockerAt(t, a, first)

	second := socks5ConduitWith(".other.internal")
	second.Socks5Listen = freeAddr(t)
	resp := a.doDockerServe(Request{
		Socket:  t.TempDir() + "/second.sock",
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Conduit: ToWireConduit(second),
	})
	if !resp.OK {
		t.Fatalf("docker-serve on a free address = %+v", resp)
	}
	cs := theConn(t, a)
	if n, _ := conduitCounts(t, a, cs); n != 2 {
		t.Fatalf("conduits = %d, want 2: different addresses are different conduits", n)
	}
}

// An EPHEMERAL conduit is private across processes but still shared within this
// one: two frontends with identical settings must land on ONE bound port, because a
// browser has a single proxy setting and would otherwise reach only one of them.
func TestEphemeralConduitsStillShareWithinTheAgent(t *testing.T) {
	a := newTestAgent(t, fakeResolve(nil))
	cfg := socks5Conduit()
	dockerAt(t, a, cfg)
	resp := a.doDockerServe(Request{
		Socket:  t.TempDir() + "/second.sock",
		Conn:    ConnSpec{Server: "http://fake:5000"},
		Conduit: ToWireConduit(cfg),
	})
	if !resp.OK {
		t.Fatalf("second docker-serve = %+v", resp)
	}
	cs := theConn(t, a)
	n, refs := conduitCounts(t, a, cs)
	if n != 1 {
		t.Fatalf("conduits = %d (refs %v), want 1", n, refs)
	}
	for k, r := range refs {
		if r != 2 {
			t.Fatalf("conduit %v refs = %d, want 2 (both frontends)", k, r)
		}
	}
}
