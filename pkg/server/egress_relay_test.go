package server

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// startEchoServer runs a TCP echo server the "client" (this test process) can
// reach, standing in for a destination only the client-side network can dial.
func startEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// egressAttachSpec builds a deploy-attach spec whose deployment routes egress by
// the given rules. Egress mode "proxy" makes the server hold and register the
// deploy-attach session natively (needsEgressRelay), so the caretaker's egress
// stream can find it.
func egressAttachSpec(rules []api.EgressRule) deploywire.DeployAttachSpec {
	return deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Egress: &api.EgressSpec{Mode: "proxy", Rules: rules},
		},
	}
}

func startEgressSession(t *testing.T, rules []api.EgressRule) (mux *yamux.Session, session string, cancel func()) {
	t.Helper()
	fb := &fakeAttachingBackend{
		creds:  make(chan []deploy.AttachCredential, 1),
		egress: make(chan *deploy.AttachEgress, 1),
	}
	srv := newTestServer(t, fb)

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancelCtx := context.WithTimeout(context.Background(), 15*time.Second)
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", egressAttachSpec(rules), nil, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var eg *deploy.AttachEgress
	select {
	case eg = <-fb.egress:
	case <-ctx.Done():
		t.Fatal("backend never received an AttachEgress")
	}
	if eg == nil || eg.Session == "" {
		t.Fatalf("AttachEgress = %+v, want a session id", eg)
	}
	m, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	return m, eg.Session, func() {
		m.Close()
		cancelCtx()
		srv.Close()
	}
}

func TestEgressRelayClientRouteRoundTrip(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	// Route loopback destinations to the client; the server relays the caretaker's
	// stream to the client's egress backing, which dials the echo server.
	mux, session, cancel := startEgressSession(t, []api.EgressRule{{Pattern: "127.0.0.0/8", Route: "client"}})
	defer cancel()

	stream, err := wire.OpenEgress(mux, session, "client", echoAddr)
	if err != nil {
		t.Fatalf("open egress: %v", err)
	}
	defer stream.Close()

	if _, err := io.WriteString(stream, "ping"); err != nil {
		t.Fatalf("write: %v", err)
	}
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read echo through the relay: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("relayed echo = %q, want ping", buf)
	}
}

func TestEgressRelayDenyAndClusterDrop(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	for _, route := range []string{"deny", "cluster"} {
		mux, session, cancel := startEgressSession(t, []api.EgressRule{{Pattern: "127.0.0.0/8", Route: route}})

		stream, err := wire.OpenEgress(mux, session, "client", echoAddr)
		if err != nil {
			t.Fatalf("open egress: %v", err)
		}
		// The server re-evaluates the policy and refuses to relay a non-relay route:
		// the stream is dropped, so a read returns EOF/error rather than an echo.
		stream.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, err := stream.Read(make([]byte, 4)); err == nil && n > 0 {
			t.Fatalf("route %q was relayed; server must drop non-client/gateway routes", route)
		}
		stream.Close()
		cancel()
	}
}

// dialGatewayEgress opens a sessionless gateway-routed egress stream against the
// caretaker endpoint and returns it.
func dialGatewayEgress(t *testing.T, srvURL, dest string) (*yamux.Session, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	mux, err := wire.Dial(ctx, "ws"+strings.TrimPrefix(srvURL, "http")+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	t.Cleanup(func() { mux.Close() })
	// No session (gateway terminus), route "gateway".
	stream, err := wire.OpenEgress(mux, "", "gateway", dest)
	if err != nil {
		t.Fatalf("open gateway egress: %v", err)
	}
	return mux, stream
}

func TestEgressGatewayTerminusRoundTrip(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	t.Setenv("CORNUS_EGRESS_GATEWAY", "1")
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	_, stream := dialGatewayEgress(t, srv.URL, echoAddr)
	defer stream.Close()
	// The server (gateway node) dials the destination directly and splices.
	if _, err := io.WriteString(stream, "ping"); err != nil {
		t.Fatal(err)
	}
	stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read echo via gateway: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("gateway echo = %q, want ping", buf)
	}
}

func TestEgressGatewayDisabledDrops(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	// CORNUS_EGRESS_GATEWAY unset => gateway egress disabled.
	t.Setenv("CORNUS_EGRESS_GATEWAY", "")
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	_, stream := dialGatewayEgress(t, srv.URL, echoAddr)
	defer stream.Close()
	stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := stream.Read(make([]byte, 4)); err == nil && n > 0 {
		t.Fatal("gateway egress must be dropped when disabled")
	}
}

func TestEgressGatewayOperatorPolicyDenies(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	t.Setenv("CORNUS_EGRESS_GATEWAY", "1")
	// Operator ceiling denies everything.
	t.Setenv("CORNUS_EGRESS_POLICY", `{"rules":[{"pattern":"*","route":"deny"}]}`)
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	_, stream := dialGatewayEgress(t, srv.URL, echoAddr)
	defer stream.Close()
	stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := stream.Read(make([]byte, 4)); err == nil && n > 0 {
		t.Fatal("gateway egress must be dropped when the operator policy denies it")
	}
}
