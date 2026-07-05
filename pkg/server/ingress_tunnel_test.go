package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/tunnel"
)

// ingressBackend is a deploy.Backend that lists project-tagged deployments and
// answers ForwardPort with an HTTP echo, so a request driven through a tunnel
// can be traced all the way to the workload it routed to.
type ingressBackend struct {
	deploy.Backend
	names   []string
	project string
}

func (b *ingressBackend) List(context.Context) ([]api.DeployStatus, error) {
	out := make([]api.DeployStatus, 0, len(b.names))
	for _, n := range b.names {
		out = append(out, api.DeployStatus{Name: n, Origin: &api.Origin{Project: b.project}})
	}
	return out, nil
}

func (b *ingressBackend) ForwardPort(_ context.Context, name string, port int, _ string, conn io.ReadWriteCloser) error {
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("workload=%s port=%d host=%s path=%s", name, port, req.Host, req.URL.Path)
	_, err = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	return err
}

// fakeGateway stands in for a cluster ingress controller.
type fakeGateway struct {
	hosts   map[string][]string
	dialErr error
	// httpsSeen records whether DialIngress was asked for the HTTPS port.
	httpsSeen chan bool
	// served records what the "controller" received, proving passthrough.
	served chan string
}

func (g *fakeGateway) IngressHosts(_ context.Context, name string) ([]string, error) {
	return g.hosts[name], nil
}

func (g *fakeGateway) DialIngress(_ context.Context, https bool) (net.Conn, error) {
	if g.httpsSeen != nil {
		select {
		case g.httpsSeen <- https:
		default:
		}
	}
	if g.dialErr != nil {
		return nil, g.dialErr
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		br := bufio.NewReader(server)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if g.served != nil {
			select {
			case g.served <- req.Host:
			default:
			}
		}
		fmt.Fprint(server, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	}()
	return client, nil
}

// gatewayBackend composes the two so only backends WITH a gateway satisfy
// deploy.IngressGateway.
type gatewayBackend struct {
	*ingressBackend
	*fakeGateway
}

// newIngressTunnelServer builds a Server with the ingress manager wired, the
// given backend, and a fake tunnel provider.
func newIngressTunnelServer(t *testing.T, backend deploy.Backend, prov tunnel.Provider) *Server {
	t.Helper()
	s := &Server{
		ingress:   newIngressManager(t.TempDir()),
		tunnels:   &tunnelManager{name: "fake", backend: prov, sessions: map[string]*tunnelSession{}},
		apiPolicy: nil,
	}
	s.newBackend = func() (deploy.Backend, error) { return backend, nil }
	t.Cleanup(s.tunnels.closeAll)
	return s
}

// roundTripThroughTunnel drives one HTTP request into the tunnel the way a
// visitor would: hand the provider a connection and read the response off it.
func roundTripThroughTunnel(t *testing.T, conns chan net.Conn, host, path string) string {
	t.Helper()
	visitor, edge := net.Pipe()
	conns <- edge

	done := make(chan string, 1)
	go func() {
		fmt.Fprintf(visitor, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
		b, _ := io.ReadAll(visitor)
		done <- string(b)
	}()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("no response came back through the tunnel")
		return ""
	}
}

// TestIngressTunnelServesProjectThroughMux is the end-to-end shape on a host
// backend: two services of one project, each on its own path, reachable through
// a single public URL.
func TestIngressTunnelServesProjectThroughMux(t *testing.T) {
	be := &ingressBackend{names: []string{"web", "api"}, project: "shop"}
	conns := make(chan net.Conn)
	prov := &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"}
	s := newIngressTunnelServer(t, be, prov)

	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))
	apiSpec := ingressSpec("api", 9090, "shop.example.com")
	apiSpec.Ingress.Path = "/api"
	s.ingress.apply(ctx, apiSpec)

	st, err := s.startIngressTunnel(ctx, be, ingressTunnelScope{project: "shop"}, api.IngressTunnelRequest{}, "tok", nil)
	if err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	if st.URL != "https://abc123.ngrok.app" || st.Target != "mux" {
		t.Fatalf("status = %+v, want the fake URL fronting the mux", st)
	}
	// The provider handed back a hostname that is not the ingress host, so auto
	// mode must have settled on aliasing rather than passthrough.
	if st.HostMode != api.HostModeAlias {
		t.Errorf("hostMode = %q, want %q", st.HostMode, api.HostModeAlias)
	}

	// A visitor arrives with the TUNNEL hostname, not the ingress hostname.
	if got := roundTripThroughTunnel(t, conns, "abc123.ngrok.app", "/api/v1"); !strings.Contains(got, "workload=api port=9090") {
		t.Errorf("response = %q, want it routed to the api workload", got)
	}
	if got := roundTripThroughTunnel(t, conns, "abc123.ngrok.app", "/"); !strings.Contains(got, "workload=web port=8080") {
		t.Errorf("response = %q, want it routed to the web workload", got)
	}
}

// TestIngressTunnelAliasPreservesClientHost pins the reason alias mode is the
// default over rewriting: the app must see the hostname the browser is on, or
// its redirects and cookies point somewhere the visitor cannot reach.
func TestIngressTunnelAliasPreservesClientHost(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"})

	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))
	if _, err := s.startIngressTunnel(ctx, be, ingressTunnelScope{deployment: "web"}, api.IngressTunnelRequest{}, "tok", nil); err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}

	got := roundTripThroughTunnel(t, conns, "abc123.ngrok.app", "/")
	if !strings.Contains(got, "host=abc123.ngrok.app") {
		t.Errorf("response = %q, want the workload to see the tunnel hostname", got)
	}
}

// TestIngressTunnelRewriteReplacesHost proves the opt-in mode does what it says,
// for apps that key on their configured hostname.
func TestIngressTunnelRewriteReplacesHost(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"})

	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))
	st, err := s.startIngressTunnel(ctx, be, ingressTunnelScope{deployment: "web"},
		api.IngressTunnelRequest{HostMode: api.HostModeRewrite}, "tok", nil)
	if err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	if st.HostMode != api.HostModeRewrite {
		t.Fatalf("hostMode = %q, want rewrite", st.HostMode)
	}

	got := roundTripThroughTunnel(t, conns, "abc123.ngrok.app", "/")
	if !strings.Contains(got, "host=shop.example.com") {
		t.Errorf("response = %q, want the Host rewritten to the ingress hostname", got)
	}
}

// TestIngressTunnelPrefersRealController proves a backend with a reachable
// ingress controller gets a raw splice to it — the cluster's own routing and
// certificates — rather than the server's emulated mux.
func TestIngressTunnelPrefersRealController(t *testing.T) {
	gw := &fakeGateway{hosts: map[string][]string{"web": {"shop.example.com"}}, served: make(chan string, 1)}
	be := gatewayBackend{
		ingressBackend: &ingressBackend{names: []string{"web"}, project: "shop"},
		fakeGateway:    gw,
	}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"})

	st, err := s.startIngressTunnel(context.Background(), be, ingressTunnelScope{deployment: "web"}, api.IngressTunnelRequest{}, "tok", nil)
	if err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	if st.Target != "controller" {
		t.Fatalf("target = %q, want controller", st.Target)
	}
	// A controller front is a raw splice, so the request must reach it untouched.
	if st.HostMode != api.HostModePassthrough {
		t.Errorf("hostMode = %q, want passthrough in front of a real controller", st.HostMode)
	}
	roundTripThroughTunnel(t, conns, "abc123.ngrok.app", "/")
	select {
	case host := <-gw.served:
		if host != "abc123.ngrok.app" {
			t.Errorf("controller saw Host %q, want the request passed through untouched", host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the controller never received the tunneled request")
	}
}

// TestIngressTunnelFallsBackToMuxWhenControllerUnreachable proves a cluster with
// no reachable controller still gets a working tunnel, rather than a published
// URL that answers nothing.
func TestIngressTunnelFallsBackToMuxWhenControllerUnreachable(t *testing.T) {
	gw := &fakeGateway{hosts: map[string][]string{"web": {"shop.example.com"}}, dialErr: net.ErrClosed}
	be := gatewayBackend{
		ingressBackend: &ingressBackend{names: []string{"web"}, project: "shop"},
		fakeGateway:    gw,
	}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"})

	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))
	st, err := s.startIngressTunnel(ctx, be, ingressTunnelScope{deployment: "web"}, api.IngressTunnelRequest{}, "tok", nil)
	if err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	if st.Target != "mux" {
		t.Fatalf("target = %q, want the mux fallback", st.Target)
	}
	if got := roundTripThroughTunnel(t, conns, "abc123.ngrok.app", "/"); !strings.Contains(got, "workload=web") {
		t.Errorf("response = %q, want it served by the mux fallback", got)
	}
}

// TestIngressTunnelWithoutAnIngressIsRefused proves the failure names the fix:
// there is nothing to front until something declares an ingress.
func TestIngressTunnelWithoutAnIngressIsRefused(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: make(chan net.Conn), url: "https://x"})

	_, err := s.startIngressTunnel(context.Background(), be, ingressTunnelScope{deployment: "web"}, api.IngressTunnelRequest{}, "tok", nil)
	if err == nil {
		t.Fatal("a deployment with no ingress should not get a tunnel")
	}
	if !strings.Contains(err.Error(), "ingress") {
		t.Errorf("error %q should explain that an ingress must be declared first", err)
	}
}

// TestIngressTunnelRewriteRefusedInFrontOfController proves the combination that
// cannot work is refused up front, rather than silently ignored: a controller
// front is a raw byte stream with no HTTP layer to rewrite in.
func TestIngressTunnelRewriteRefusedInFrontOfController(t *testing.T) {
	gw := &fakeGateway{hosts: map[string][]string{"web": {"shop.example.com"}}}
	be := gatewayBackend{
		ingressBackend: &ingressBackend{names: []string{"web"}, project: "shop"},
		fakeGateway:    gw,
	}
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: make(chan net.Conn), url: "https://x"})

	_, err := s.startIngressTunnel(context.Background(), be, ingressTunnelScope{deployment: "web"},
		api.IngressTunnelRequest{HostMode: api.HostModeRewrite}, "tok", nil)
	if err == nil {
		t.Fatal("rewrite in front of a real controller should be refused")
	}
	if !strings.Contains(err.Error(), "raw byte stream") {
		t.Errorf("error %q should explain why rewriting is impossible there", err)
	}
}

// TestIngressTunnelScopeKeysCannotCollide proves an ingress tunnel and a
// deployment tunnel of the same name are distinct sessions.
func TestIngressTunnelScopeKeysCannotCollide(t *testing.T) {
	deployKey := "web"
	scoped := ingressTunnelScope{deployment: "web"}.key()
	project := ingressTunnelScope{project: "web"}.key()
	if scoped == deployKey || project == deployKey || scoped == project {
		t.Fatalf("keys collide: deployment tunnel %q, ingress deployment %q, ingress project %q", deployKey, scoped, project)
	}
}

// TestIngressTunnelEndpointRoundTrip exercises the HTTP surface: POST, GET, DELETE.
func TestIngressTunnelEndpointRoundTrip(t *testing.T) {
	clearAuthEnv(t)
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"})
	s.ingress.apply(context.Background(), ingressSpec("web", 8080, "shop.example.com"))
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/.cornus/v1/ingress-tunnel", s.handleIngressTunnel)

	post := func(body string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, "/.cornus/v1/ingress-tunnel", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec.Result()
	}

	resp := post(`{"deployment":"web","authToken":"tok"}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST status = %d (%s)", resp.StatusCode, b)
	}
	var st api.IngressTunnelStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if !st.Active || st.URL == "" || st.Scope != "deployment/web" {
		t.Fatalf("status = %+v, want an active tunnel scoped to deployment/web", st)
	}

	// An unknown host mode is a request error, not a 502.
	if got := post(`{"deployment":"web","hostMode":"nonsense"}`).StatusCode; got != http.StatusBadRequest {
		t.Errorf("unknown hostMode status = %d, want 400", got)
	}
	// A scope naming both is refused rather than silently preferring one.
	if got := post(`{"deployment":"web","project":"shop"}`).StatusCode; got != http.StatusBadRequest {
		t.Errorf("ambiguous scope status = %d, want 400", got)
	}
	// And a request naming neither.
	if got := post(`{}`).StatusCode; got != http.StatusBadRequest {
		t.Errorf("scopeless status = %d, want 400", got)
	}
}

// TestIngressTunnelPassthroughWhenProviderGrantsHostname proves the payoff of
// requesting the ingress hostname as the tunnel's public hostname: when a
// provider can grant it (an ngrok reserved domain, a sish bind host), auto mode
// settles on passthrough and nothing about the request is adjusted at all.
func TestIngressTunnelPassthroughWhenProviderGrantsHostname(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	conns := make(chan net.Conn)
	prov := &fakeTunnelProvider{conns: conns, url: "https://shop.example.com"}
	s := newIngressTunnelServer(t, be, prov)

	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))
	st, err := s.startIngressTunnel(ctx, be, ingressTunnelScope{deployment: "web"}, api.IngressTunnelRequest{}, "tok", nil)
	if err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	if st.HostMode != api.HostModePassthrough {
		t.Fatalf("hostMode = %q, want passthrough when the tunnel hostname is the ingress hostname", st.HostMode)
	}
	// And the server asked for it in the first place.
	if got := prov.opts().Domain; got != "shop.example.com" {
		t.Errorf("requested Domain = %q, want the ingress hostname", got)
	}
	if got := roundTripThroughTunnel(t, conns, "shop.example.com", "/"); !strings.Contains(got, "workload=web") {
		t.Errorf("response = %q, want it routed to web", got)
	}
}

// TestFillIngressTunnelURL pins the precedence for the URL a deployment reports:
// a backend-reported address wins, then a deployment-scoped tunnel, then the
// project's.
func TestFillIngressTunnelURL(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://proj.ngrok.app"})
	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))

	// Nothing published yet.
	st := api.DeployStatus{Name: "web", Origin: &api.Origin{Project: "shop"}}
	s.fillIngressTunnelURL(&st)
	if st.URL != "" {
		t.Fatalf("URL = %q, want empty before anything is published", st.URL)
	}

	// A project tunnel covers its member deployments.
	if _, err := s.startIngressTunnel(ctx, be, ingressTunnelScope{project: "shop"}, api.IngressTunnelRequest{}, "tok", nil); err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	st = api.DeployStatus{Name: "web", Origin: &api.Origin{Project: "shop"}}
	s.fillIngressTunnelURL(&st)
	if st.URL != "https://proj.ngrok.app" {
		t.Errorf("URL = %q, want the project tunnel's", st.URL)
	}

	// A URL the backend already reported is a property of the workload and must
	// not be overwritten by a session someone happened to open.
	st = api.DeployStatus{Name: "web", Origin: &api.Origin{Project: "shop"}, URL: "https://knative.example"}
	s.fillIngressTunnelURL(&st)
	if st.URL != "https://knative.example" {
		t.Errorf("URL = %q, want the backend-reported address kept", st.URL)
	}

	// A deployment with no project affiliation is unaffected by the project tunnel.
	st = api.DeployStatus{Name: "other"}
	s.fillIngressTunnelURL(&st)
	if st.URL != "" {
		t.Errorf("URL = %q, want empty for an unrelated deployment", st.URL)
	}
}

// TestIngressTunnelRawProtoReachesControllerHTTPS proves --proto tcp is actually
// honored in front of a real controller: the client's bytes (TLS included) must
// reach the controller's HTTPS port, which is the only configuration that
// delivers end-to-end TLS. It used to be accepted and silently ignored, dialling
// the controller's plain HTTP port.
func TestIngressTunnelRawProtoReachesControllerHTTPS(t *testing.T) {
	gw := &fakeGateway{hosts: map[string][]string{"web": {"shop.example.com"}}, httpsSeen: make(chan bool, 4)}
	be := gatewayBackend{
		ingressBackend: &ingressBackend{names: []string{"web"}, project: "shop"},
		fakeGateway:    gw,
	}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "tcp://relay.example:7000"})

	if _, err := s.startIngressTunnel(context.Background(), be, ingressTunnelScope{deployment: "web"},
		api.IngressTunnelRequest{Proto: "tcp"}, "tok", nil); err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}
	// Front selection probes the controller once with https=false to decide
	// reachability; discard that so what is asserted below is the dial that
	// actually carries a visitor's bytes.
	select {
	case <-gw.httpsSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("front selection never probed the controller")
	}

	roundTripThroughTunnel(t, conns, "shop.example.com", "/")
	select {
	case https := <-gw.httpsSeen:
		if !https {
			t.Error("a raw tunnel dialled the controller's plain HTTP port; the client's TLS could never reach it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the controller was never dialled")
	}
}

// TestIngressTunnelRawProtoRefusedInFrontOfMux proves the combination that cannot
// work is refused with an explanation, instead of handing back a URL that fails
// the moment anyone speaks https to it.
func TestIngressTunnelRawProtoRefusedInFrontOfMux(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: make(chan net.Conn), url: "tcp://x:1"})
	s.ingress.apply(context.Background(), ingressSpec("web", 8080, "shop.example.com"))

	_, err := s.startIngressTunnel(context.Background(), be, ingressTunnelScope{deployment: "web"},
		api.IngressTunnelRequest{Proto: "tcp"}, "tok", nil)
	if err == nil {
		t.Fatal("--proto tcp in front of the mux should be refused")
	}
	if !strings.Contains(err.Error(), "plain HTTP") {
		t.Errorf("error %q should explain that the mux cannot terminate TLS", err)
	}
}

// TestIngressTunnelWithdrawsHostAliasOnStop proves a stopped tunnel stops
// routing its hostname. The alias used to be registered at start and never
// removed, so it accumulated one entry per tunnel ever opened and kept resolving
// a hostname nobody was serving — reachable through CORNUS_INGRESS_LISTEN where
// an operator configured it.
func TestIngressTunnelWithdrawsHostAliasOnStop(t *testing.T) {
	be := &ingressBackend{names: []string{"web"}, project: "shop"}
	conns := make(chan net.Conn)
	s := newIngressTunnelServer(t, be, &fakeTunnelProvider{conns: conns, url: "https://abc123.ngrok.app"})

	ctx := context.Background()
	s.ingress.apply(ctx, ingressSpec("web", 8080, "shop.example.com"))
	scope := ingressTunnelScope{deployment: "web"}
	if _, err := s.startIngressTunnel(ctx, be, scope, api.IngressTunnelRequest{}, "tok", nil); err != nil {
		t.Fatalf("startIngressTunnel: %v", err)
	}

	if _, known, _ := s.ingress.routes().Lookup("abc123.ngrok.app", "/"); !known {
		t.Fatal("the tunnel hostname should route while the tunnel is up")
	}

	s.tunnels.stop(scope.key())

	if _, known, _ := s.ingress.routes().Lookup("abc123.ngrok.app", "/"); known {
		t.Error("the tunnel hostname must stop routing once the tunnel is stopped")
	}
	// The ingress host itself is a property of the deployment, not of the tunnel,
	// so it must survive.
	if _, known, _ := s.ingress.routes().Lookup("shop.example.com", "/"); !known {
		t.Error("stopping a tunnel must not withdraw the deployment's own ingress host")
	}
}

// TestTunnelCleanupRunsAfterTeardownRace pins the ordering guarantee addCleanup
// makes: registering against an already-torn-down session runs the work
// immediately rather than stranding it, which is what keeps a stop racing a start
// from leaving an alias behind.
func TestTunnelCleanupRunsAfterTeardownRace(t *testing.T) {
	ts := &tunnelSession{}
	ts.runCleanups() // the session is torn down before anything registers

	ran := false
	ts.addCleanup(func() { ran = true })
	if !ran {
		t.Error("a cleanup registered after teardown must run immediately, not be dropped")
	}
}
