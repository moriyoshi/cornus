package caretaker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/miekg/dns"

	"cornus/pkg/hub"
	"cornus/pkg/wire"
)

// fakeHubEcho makes serverSess behave like the hub's data-relay far end: every
// 'D' stream has its service name line read and its bytes echoed back.
func fakeHubEcho(t *testing.T, serverSess *yamux.Session) {
	t.Helper()
	go func() {
		for {
			tag, stream, err := wire.AcceptTagged(serverSess)
			if err != nil {
				return
			}
			if tag != wire.TagData {
				stream.Close()
				continue
			}
			go func(c net.Conn) {
				defer c.Close()
				if _, err := wire.ReadLine(c); err != nil { // service name
					return
				}
				_, _ = io.Copy(c, c)
			}(stream)
		}
	}()
}

// dialEcho dials addr and round-trips one line, returning it ("" on any failure).
func dialEcho(addr string) string {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("ping\n"))
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	line, _ := bufio.NewReader(conn).ReadString('\n')
	return line
}

// TestDynamicReachBindsAndUnbinds drives runDynamicReach deterministically: a
// fake hub pushes catalog snapshots on an in-memory control pipe. A service
// appearing in the catalog gets a listener at its synthetic IP that relays; the
// spoke's own registered service is never imported; a service vanishing from the
// catalog has its listener closed while an in-flight connection keeps relaying
// (drain, not kill).
func TestDynamicReachBindsAndUnbinds(t *testing.T) {
	clientSess, serverSess := yamuxPair(t)
	fakeHubEcho(t, serverSess)

	ctlNear, ctlFar := net.Pipe()
	t.Cleanup(func() { ctlNear.Close(); ctlFar.Close() })

	// A free port on the discovered service's synthetic IP.
	synthIP := hub.SyntheticIP("dynecho")
	probe, err := net.Listen("tcp", synthIP+":0")
	if err != nil {
		t.Fatalf("probe listen on %s: %v", synthIP, err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	addr := net.JoinHostPort(synthIP, strconv.Itoa(port))
	selfAddr := net.JoinHostPort(hub.SyntheticIP("self"), strconv.Itoa(port))

	role := &HubRole{
		Register:     []HubService{{Name: "self", Addr: "127.0.0.1:1"}},
		ReachDynamic: &HubDynamicReach{Ports: []int{port}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runDynamicReach(ctx, ctlNear, clientSess, role) }()

	enc := json.NewEncoder(ctlFar)
	if err := enc.Encode(hub.CatalogUpdate{Services: []string{"dynecho", "self"}}); err != nil {
		t.Fatalf("push catalog: %v", err)
	}

	// The listener for "dynecho" appears and relays through the (fake) hub.
	var got string
	for i := 0; i < 200; i++ {
		if got = dialEcho(addr); got == "ping\n" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got != "ping\n" {
		t.Fatalf("dynamic listener echo = %q, want %q", got, "ping\n")
	}
	// "self" is registered by this spoke, so it must never be dynamically bound.
	if c, err := net.DialTimeout("tcp", selfAddr, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("own registered service was dynamically imported, want it skipped")
	}

	// Hold a connection open across the unbind to prove drain semantics.
	held, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("held dial: %v", err)
	}
	defer held.Close()

	// The service vanishes: the listener must close...
	if err := enc.Encode(hub.CatalogUpdate{Services: []string{}}); err != nil {
		t.Fatalf("push empty catalog: %v", err)
	}
	gone := false
	for i := 0; i < 200; i++ {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			gone = true
			break
		}
		c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	if !gone {
		t.Fatal("dynamic listener still accepting after the service vanished")
	}

	// ...but the in-flight connection keeps relaying (drain, not kill).
	if _, err := held.Write([]byte("still\n")); err != nil {
		t.Fatalf("held write after unbind: %v", err)
	}
	_ = held.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(held).ReadString('\n')
	if err != nil || line != "still\n" {
		t.Fatalf("held relay after unbind = %q (err %v), want %q", line, err, "still\n")
	}

	// Teardown: cancelling the context ends the watcher cleanly.
	cancel()
	ctlNear.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDynamicReach returned %v after ctx cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runDynamicReach did not return after ctx cancel")
	}
}

// TestDynamicReachPublishesDNS closes the dynamic-import DNS gap end to end
// inside one process, exactly as a pod running both roles: a catalog bind must
// also publish the service name into the process-wide dynamic overlay so the
// caretaker's dns role (a plain newDNSServer, no extra wiring) resolves it —
// bare and search-expanded — to the SAME synthetic IP the listener bound; an
// unbind withdraws the record; the spoke's own registered service is never
// published; and the watcher's teardown leaves no residue in the overlay.
func TestDynamicReachPublishesDNS(t *testing.T) {
	clientSess, serverSess := yamuxPair(t)
	fakeHubEcho(t, serverSess)

	ctlNear, ctlFar := net.Pipe()
	t.Cleanup(func() { ctlNear.Close(); ctlFar.Close() })

	// A free port on the discovered service's synthetic IP.
	synthIP := hub.SyntheticIP("dnsecho")
	probe, err := net.Listen("tcp", synthIP+":0")
	if err != nil {
		t.Fatalf("probe listen on %s: %v", synthIP, err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	// The pod's dns role: standalone construction, no reference to the hub role —
	// the overlay is the only join. One static record proves deploy-time records
	// stay authoritative alongside dynamic ones.
	srv := newDNSServer(DNSRole{
		Records: map[string]string{"static": "10.222.0.5"},
		Domain:  "cmns.svc.cluster.local",
	})
	resolveA := func(name string) (string, bool) {
		req := new(dns.Msg)
		req.SetQuestion(dns.CanonicalName(name), dns.TypeA)
		w := &fakeResponseWriter{}
		srv.handle(w, req)
		if w.msg == nil || !w.msg.Authoritative || len(w.msg.Answer) != 1 {
			return "", false
		}
		a, ok := w.msg.Answer[0].(*dns.A)
		if !ok {
			return "", false
		}
		return a.A.String(), true
	}

	role := &HubRole{
		Register:     []HubService{{Name: "self", Addr: "127.0.0.1:1"}},
		ReachDynamic: &HubDynamicReach{Ports: []int{port}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runDynamicReach(ctx, ctlNear, clientSess, role) }()

	enc := json.NewEncoder(ctlFar)
	if err := enc.Encode(hub.CatalogUpdate{Services: []string{"dnsecho", "self"}}); err != nil {
		t.Fatalf("push catalog: %v", err)
	}

	// The name appears in DNS (eventually — the record lands with the bind) and
	// points at the listener's synthetic IP, bare and search-expanded.
	deadline := time.Now().Add(2 * time.Second)
	var got string
	var owned bool
	for time.Now().Before(deadline) {
		if got, owned = resolveA("dnsecho"); owned {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !owned || got != synthIP {
		t.Fatalf("dnsecho resolved to %q (owned=%v), want the synthetic %s", got, owned, synthIP)
	}
	if got, owned = resolveA("dnsecho.cmns.svc.cluster.local"); !owned || got != synthIP {
		t.Fatalf("dnsecho.<domain> resolved to %q (owned=%v), want %s", got, owned, synthIP)
	}
	// The spoke's own registered service is skipped: bound nowhere, published nowhere.
	if _, owned := resolveA("self"); owned {
		t.Fatal("own registered service was published to DNS, want it skipped")
	}
	// The static record is untouched by dynamic churn.
	if got, owned := resolveA("static"); !owned || got != "10.222.0.5" {
		t.Fatalf("static resolved to %q (owned=%v), want 10.222.0.5", got, owned)
	}

	// The service vanishes: the record must be withdrawn with the unbind.
	if err := enc.Encode(hub.CatalogUpdate{Services: []string{}}); err != nil {
		t.Fatalf("push empty catalog: %v", err)
	}
	withdrawn := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, owned := resolveA("dnsecho"); !owned {
			withdrawn = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !withdrawn {
		t.Fatal("dnsecho still resolving after the service vanished")
	}

	// Teardown leaves the process-wide overlay clean for the rest of the process.
	cancel()
	ctlNear.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runDynamicReach did not return after ctx cancel")
	}
	dnsDynamic.mu.RLock()
	residue := len(dnsDynamic.m)
	dnsDynamic.mu.RUnlock()
	if residue != 0 {
		t.Fatalf("dynamic overlay holds %d records after teardown, want none", residue)
	}
}
