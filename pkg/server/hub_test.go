package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"cornus/pkg/caretaker"
	"cornus/pkg/hub"
)

// TestHubRelaysToRegisteredService drives real proxied traffic through the hub:
// spoke B registers an echo service; spoke A opens a data stream to it by name;
// the hub dials B's registered address and splices, so A's bytes echo back. Proves
// the registry + one-way relay end to end, in-process.
func TestHubRelaysToRegisteredService(t *testing.T) {
	// Backend echo — service "echo"'s real endpoint.
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
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Spoke B: register "echo" -> the backend echo's address.
	sessB, err := hub.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("spoke B dial: %v", err)
	}
	defer sessB.Close()
	ctl, err := hub.Register(sessB, hub.Registration{Services: []hub.Service{
		{Name: "echo", Addr: echo.Addr().String()},
	}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer ctl.Close()

	// Spoke A: reach "echo" through the hub. Registration is applied asynchronously
	// on the server (control-stream decode), so retry until it resolves.
	sessA, err := hub.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("spoke A dial: %v", err)
	}
	defer sessA.Close()

	var got string
	for i := 0; i < 100; i++ {
		stream, err := hub.OpenTo(sessA, "echo")
		if err != nil {
			t.Fatalf("open data stream: %v", err)
		}
		if _, err := stream.Write([]byte("ping\n")); err != nil {
			stream.Close()
			t.Fatalf("write: %v", err)
		}
		_ = stream.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := bufio.NewReader(stream).ReadString('\n')
		stream.Close()
		if err == nil && line == "ping\n" {
			got = line
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != "ping\n" {
		t.Fatalf("echo through hub = %q, want %q", got, "ping\n")
	}
}

// TestHubUnknownServiceCloses confirms a data stream for an unregistered service
// is closed (the spoke sees EOF), not left hanging.
func TestHubUnknownServiceCloses(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := hub.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()

	stream, err := hub.OpenTo(sess, "nope")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("expected EOF/closed stream for an unknown service")
	}
}

// TestHubRoleEndToEnd drives the caretaker hub ROLE against the real server: one
// caretaker registers an echo service; another runs a reach loopback listener that
// forwards to the hub. A client dialing the loopback reaches the echo through the
// hub — proving the full spoke→hub→spoke(-registered-addr) path via caretaker.Run.
func TestHubRoleEndToEnd(t *testing.T) {
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

	// Grab a free loopback port for the reach listener.
	probe, err := net.Listen("tcp", "127.0.0.9:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	reachPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	regCtx, regCancel := context.WithCancel(context.Background())
	defer regCancel()
	go func() {
		_ = caretaker.Run(regCtx, caretaker.Config{Hub: &caretaker.HubRole{
			Server:   srv.URL,
			Register: []caretaker.HubService{{Name: "echo", Addr: echo.Addr().String()}},
		}})
	}()

	reachCtx, reachCancel := context.WithCancel(context.Background())
	defer reachCancel()
	go func() {
		_ = caretaker.Run(reachCtx, caretaker.Config{Hub: &caretaker.HubRole{
			Server: srv.URL,
			Reach:  []caretaker.HubPeer{{Name: "echo", Listen: "127.0.0.9", Ports: []int{reachPort}}},
		}})
	}()

	// Dial the reach loopback; retry while both caretakers connect/register.
	addr := net.JoinHostPort("127.0.0.9", strconv.Itoa(reachPort))
	var got string
	for i := 0; i < 100; i++ {
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
		t.Fatalf("echo through hub role = %q, want %q", got, "ping\n")
	}
}

// TestHubDeliveryEndToEnd exercises Phase 2 ingress delivery: the destination
// spoke registers its service for DELIVERY (a local Target, no hub-dialable Addr),
// so the hub must reach it by opening an ingress stream to that spoke, which dials
// its local echo and splices. A source spoke reaching the service by name gets its
// bytes echoed back — proving the hub never dials the target itself.
func TestHubDeliveryEndToEnd(t *testing.T) {
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

	probe, err := net.Listen("tcp", "127.0.0.9:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	reachPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	// Destination spoke: register "echo" for DELIVERY (Target only, no Addr).
	dstCtx, dstCancel := context.WithCancel(context.Background())
	defer dstCancel()
	go func() {
		_ = caretaker.Run(dstCtx, caretaker.Config{Hub: &caretaker.HubRole{
			Server:   srv.URL,
			Register: []caretaker.HubService{{Name: "echo", Target: echo.Addr().String()}},
		}})
	}()

	// Source spoke: reach "echo" via a loopback listener.
	srcCtx, srcCancel := context.WithCancel(context.Background())
	defer srcCancel()
	go func() {
		_ = caretaker.Run(srcCtx, caretaker.Config{Hub: &caretaker.HubRole{
			Server: srv.URL,
			Reach:  []caretaker.HubPeer{{Name: "echo", Listen: "127.0.0.9", Ports: []int{reachPort}}},
		}})
	}()

	addr := net.JoinHostPort("127.0.0.9", strconv.Itoa(reachPort))
	var got string
	for i := 0; i < 100; i++ {
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
		t.Fatalf("echo through hub delivery = %q, want %q", got, "ping\n")
	}
}

// TestHubPolicyEnforced confirms the hub allow-matrix: with a policy permitting
// only caller "allowed" → "echo", a caller declaring identity "allowed" reaches the
// service, while one declaring "denied" is refused (its data stream is closed).
func TestHubPolicyEnforced(t *testing.T) {
	t.Setenv("CORNUS_HUB_POLICY", `{"allowed":["echo"]}`)

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

	srv := newTestServer(t, &fakeBackend{}) // reads CORNUS_HUB_POLICY at New
	defer srv.Close()

	freePort := func() int {
		l, err := net.Listen("tcp", "127.0.0.9:0")
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	allowPort, denyPort := freePort(), freePort()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Destination registers echo (dial-direct); its identity is irrelevant to the
	// reach policy.
	go func() {
		_ = caretaker.Run(ctx, caretaker.Config{Hub: &caretaker.HubRole{
			Server:   srv.URL,
			Register: []caretaker.HubService{{Name: "echo", Addr: echo.Addr().String()}},
		}})
	}()
	// Allowed caller and denied caller — distinguished only by declared Identity.
	go func() {
		_ = caretaker.Run(ctx, caretaker.Config{Hub: &caretaker.HubRole{
			Server: srv.URL, Identity: "allowed",
			Reach: []caretaker.HubPeer{{Name: "echo", Listen: "127.0.0.9", Ports: []int{allowPort}}},
		}})
	}()
	go func() {
		_ = caretaker.Run(ctx, caretaker.Config{Hub: &caretaker.HubRole{
			Server: srv.URL, Identity: "denied",
			Reach: []caretaker.HubPeer{{Name: "echo", Listen: "127.0.0.9", Ports: []int{denyPort}}},
		}})
	}()

	try := func(port int) string {
		addr := net.JoinHostPort("127.0.0.9", strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return ""
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("ping\n"))
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		return line
	}

	// Allowed caller: retry until the caretakers connect/register, expect echo.
	var allowed string
	for i := 0; i < 100; i++ {
		if allowed = try(allowPort); allowed == "ping\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if allowed != "ping\n" {
		t.Fatalf("allowed caller: echo = %q, want %q", allowed, "ping\n")
	}
	// Denied caller: the hub closes the data stream, so no echo comes back even
	// after the path is otherwise live.
	if got := try(denyPort); got != "" {
		t.Fatalf("denied caller: got %q, want no echo (policy should refuse)", got)
	}
}

// TestHubRegisterPolicy confirms registration authorization: with a register
// policy permitting only identity "trusted" to host "echo", a "trusted" spoke's
// registration takes effect (a caller reaches it) while a "rogue" spoke's is
// dropped (nothing to reach).
func TestHubRegisterPolicy(t *testing.T) {
	t.Setenv("CORNUS_HUB_REGISTER_POLICY", `{"trusted":["echo"]}`)

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
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Register "echo" from a given identity, then a fresh caller tries to reach it.
	reachableAs := func(identity string) bool {
		reg, err := hub.Dial(ctx, wsURL)
		if err != nil {
			t.Fatalf("register dial: %v", err)
		}
		defer reg.Close()
		ctl, err := hub.Register(reg, hub.Registration{
			Identity: identity,
			Services: []hub.Service{{Name: "echo", Addr: echo.Addr().String()}},
		})
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		defer ctl.Close()

		caller, err := hub.Dial(ctx, wsURL)
		if err != nil {
			t.Fatalf("caller dial: %v", err)
		}
		defer caller.Close()
		for i := 0; i < 60; i++ {
			stream, err := hub.OpenTo(caller, "echo")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, _ = stream.Write([]byte("ping\n"))
			_ = stream.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			buf := make([]byte, 5)
			n, _ := io.ReadFull(stream, buf)
			stream.Close()
			if n == 5 {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	if !reachableAs("trusted") {
		t.Error("trusted identity registering echo should be reachable")
	}
	if reachableAs("rogue") {
		t.Error("rogue identity registering echo should be dropped (not reachable)")
	}
}

// TestHubCatalog confirms the live directory endpoint lists currently-registered
// services and drops them when the provider disconnects.
func TestHubCatalog(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"
	catURL := srv.URL + "/.cornus/v1/hub/catalog"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	catalog := func() []string {
		resp, err := http.Get(catURL)
		if err != nil {
			t.Fatalf("get catalog: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Services []string `json:"services"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Services
	}

	sess, err := hub.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctl, err := hub.Register(sess, hub.Registration{Services: []hub.Service{
		{Name: "web", Addr: "10.0.0.1:80"},
		{Name: "db", Addr: "10.0.0.2:5432"},
	}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Registration is async; poll until both appear.
	var got []string
	for i := 0; i < 100; i++ {
		got = catalog()
		if len(got) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Join(got, ",") != "db,web" {
		t.Fatalf("catalog = %v, want [db web] (sorted)", got)
	}

	// Disconnecting the provider removes its services from the catalog.
	ctl.Close()
	sess.Close()
	for i := 0; i < 100; i++ {
		if len(catalog()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("catalog still %v after provider disconnect, want empty", catalog())
}
