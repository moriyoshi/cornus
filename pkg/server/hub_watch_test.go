package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"cornus/pkg/caretaker"
	"cornus/pkg/hub"
)

// TestHubCatalogPushDynamicRebind is the reactive-discovery artifact: a spoke
// with a DYNAMIC reach set connects FIRST, then a provider registers "echo" —
// the hub pushes the catalog change over the existing control connection and the
// spoke binds a listener at hub.SyntheticIP("echo") that relays. When the
// provider disconnects, the push removes the service and the listener goes away.
func TestHubCatalogPushDynamicRebind(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
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

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	// A free port on echo's synthetic loopback IP for the dynamic listener.
	synthIP := hub.SyntheticIP("echo")
	probe, err := net.Listen("tcp", synthIP+":0")
	if err != nil {
		t.Fatalf("probe listen on %s: %v", synthIP, err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	addr := net.JoinHostPort(synthIP, strconv.Itoa(port))

	// The watching spoke connects BEFORE any provider exists: no static reach, a
	// dynamic reach set only.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	go func() {
		_ = caretaker.Run(watchCtx, caretaker.Config{Hub: &caretaker.HubRole{
			Server:       srv.URL,
			ReachDynamic: &caretaker.HubDynamicReach{Ports: []int{port}},
		}})
	}()

	// Nothing may be bound yet (the catalog is empty).
	time.Sleep(100 * time.Millisecond)
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("dynamic listener bound before the service was registered")
	}

	// NOW the provider registers "echo" (dial-direct) — after the spoke connected.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"
	provider, err := hub.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("provider dial: %v", err)
	}
	ctl, err := hub.Register(provider, hub.Registration{Services: []hub.Service{
		{Name: "echo", Addr: echo.Addr().String()},
	}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// The push must create the listener and the relay must work end to end.
	var got string
	for i := 0; i < 200; i++ {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_, _ = conn.Write([]byte("ping\n"))
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err == nil && line == "ping\n" {
			got = line
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != "ping\n" {
		t.Fatalf("dynamic reach echo = %q, want %q (listener never appeared or never relayed)", got, "ping\n")
	}

	// Unregister (disconnect the provider): the push must remove the listener.
	ctl.Close()
	provider.Close()
	gone := false
	for i := 0; i < 200; i++ {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			gone = true
			break
		}
		c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		t.Fatal("dynamic listener still accepting after the provider unregistered, want it unbound")
	}
}

// TestCatalogNotifier drives the notifier directly: a subscriber gets the current
// catalog as its initial snapshot, a kicked change delivers the new set, and
// cancel closes the channel and stops the poll loop (last subscriber out).
func TestCatalogNotifier(t *testing.T) {
	reg := hub.NewRegistry()
	reg.Register("c1", "web", "10.0.0.1:80", "")
	n := newCatalogNotifier(reg, 50*time.Millisecond)

	ch, cancel := n.subscribe()
	recv := func() []string {
		select {
		case names, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
			return names
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a catalog update")
			return nil
		}
	}

	if got := recv(); strings.Join(got, ",") != "web" {
		t.Fatalf("initial snapshot = %v, want [web]", got)
	}

	reg.Register("c2", "db", "10.0.0.2:5432", "")
	n.changed()
	if got := recv(); strings.Join(got, ",") != "db,web" {
		t.Fatalf("after change = %v, want [db web]", got)
	}

	// Removal is a change too.
	reg.RemoveConn("c2")
	n.changed()
	if got := recv(); strings.Join(got, ",") != "web" {
		t.Fatalf("after removal = %v, want [web]", got)
	}

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected the channel to be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
	cancel() // idempotent
}
