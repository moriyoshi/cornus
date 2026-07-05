package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/api"
	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"

	// The CLIENT side of the relay runs the source backend; register static.
	_ "cornus/pkg/credential/static"
	// The anthropic-proxy delivery provider under test (registers on import).
	_ "cornus/pkg/creddelivery/anthropicproxy"
)

// fakeAttachingBackend captures the AttachCredentials (and any AttachEgress) it is
// applied with.
type fakeAttachingBackend struct {
	fakeBackend
	creds  chan []deploy.AttachCredential
	egress chan *deploy.AttachEgress
}

func (f *fakeAttachingBackend) ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeployStatus, error) {
	return f.ApplyWithAttachments(ctx, spec, mounts, nil, nil)
}

func (f *fakeAttachingBackend) ApplyWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount, creds []deploy.AttachCredential, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	st, _ := f.Apply(ctx, spec)
	select {
	case f.creds <- creds:
	default:
	}
	if egress != nil {
		select {
		case f.egress <- egress:
		default:
		}
	}
	return st, nil
}

func credAttachSpec() deploywire.DeployAttachSpec {
	return deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "web",
			Image: "img",
			Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
				Name:    "db",
				Backend: "static",
				Config:  map[string]string{"username": "u", "password": "p"},
				Deliver: []api.CredentialDelivery{{Kind: "endpoint", Provider: "generic"}},
			}}},
		},
		CredentialSources: []deploywire.CredentialBacking{{
			Name: "db", Backend: "static", Config: map[string]string{"username": "u", "password": "p"},
		}},
	}
}

// TestCredentialRelayRoundTrip drives the full client -> server relay -> caretaker
// credential path in-process: a caller attaches with a credential source, the test
// plays the pod's caretaker (open a TagCredential stream, session + name), and
// fetches the credential the client minted.
func TestCredentialRelayRoundTrip(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", credAttachSpec(), nil, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var creds []deploy.AttachCredential
	select {
	case creds = <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
	if len(creds) != 1 || creds[0].Name != "db" {
		t.Fatalf("attach credentials = %+v", creds)
	}
	session := creds[0].Session

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()

	cred := fetchOverMux(t, mux, session, "db")
	if cred.Values["username"] != "u" || cred.Values["password"] != "p" {
		t.Fatalf("relayed credential = %v", cred.Values)
	}
}

// TestCredentialEnvDeliveryFetch proves the server resolves an env-kind delivery
// at deploy time: it fetches the value from the client over the held session and
// hands it to the backend as a resolved EnvVar (so the k8s backend can materialize
// a Secret). No caretaker relay is involved for the env value.
func TestCredentialEnvDeliveryFetch(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "agent",
			Image: "img",
			Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
				Name:    "key",
				Backend: "static",
				Config:  map[string]string{"value": "sk-resolved"},
				Deliver: []api.CredentialDelivery{{Kind: "env", EnvVar: "OPENAI_API_KEY"}},
			}}},
		},
		CredentialSources: []deploywire.CredentialBacking{{
			Name: "key", Backend: "static", Config: map[string]string{"value": "sk-resolved"},
		}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as, nil, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var creds []deploy.AttachCredential
	select {
	case creds = <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
	if len(creds) != 1 {
		t.Fatalf("creds = %+v", creds)
	}
	// The env delivery was resolved into EnvVars (not left in Deliver).
	if len(creds[0].EnvVars) != 1 || creds[0].EnvVars[0].Var != "OPENAI_API_KEY" || creds[0].EnvVars[0].Value != "sk-resolved" {
		t.Fatalf("resolved EnvVars = %+v", creds[0].EnvVars)
	}
	for _, d := range creds[0].Deliver {
		if d.Kind == "env" {
			t.Fatal("env delivery should be split out of Deliver")
		}
	}
}

// TestCredentialRelayUnauthorized confirms the relay drops a stream requesting a
// credential the session never declared (AllowsCredential gate).
func TestCredentialRelayUnauthorized(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", credAttachSpec(), nil, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()
	select {
	case <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer mux.Close()

	stream, err := wire.OpenTagged(mux, wire.TagCredential)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()
	// A name the session never declared. Include the (right) session id — the name
	// is what must be rejected.
	if _, err := io.WriteString(stream, "anything\nsecret\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := deploywire.FetchCredential(stream, nil); err == nil {
		t.Fatal("expected the credential relay to drop an undeclared name")
	}
}

// fetchOverMux plays a caretaker credential fetch: one TagCredential stream,
// session + name framing, then the request/response exchange.
func fetchOverMux(t *testing.T, mux *yamux.Session, session, name string) credential.Credential {
	t.Helper()
	cred, err := fetchCredOverMux(mux, session, name)
	if err != nil {
		t.Fatalf("fetch credential: %v", err)
	}
	return cred
}

// fetchCredOverMux is fetchOverMux without a *testing.T, so it can be used as a
// creddelivery.Getter from a server goroutine (t.Fatalf must not be called off
// the test goroutine).
func fetchCredOverMux(mux *yamux.Session, session, name string) (credential.Credential, error) {
	stream, err := wire.OpenTagged(mux, wire.TagCredential)
	if err != nil {
		return credential.Credential{}, err
	}
	if _, err := io.WriteString(stream, session+"\n"+name+"\n"); err != nil {
		return credential.Credential{}, err
	}
	return deploywire.FetchCredential(stream, nil)
}

// TestCredentialProxyRelayToMockUpstream is the hermetic end-to-end for the
// auth-injecting proxy delivery: a client `static` source mints an OAuth-looking
// Claude token, the anthropic-proxy Endpoint (opened with cfg["upstream"] pointed
// at a MOCK upstream) fetches it through the real server relay per request, injects
// the auth, and forwards to the mock. It proves the whole client-source -> relay ->
// proxy -> upstream path with the credential injected and the app's own auth
// overridden — no real Anthropic API involved.
func TestCredentialProxyRelayToMockUpstream(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "agent",
			Image: "img",
			Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
				Name:    "claude",
				Backend: "static",
				Config:  map[string]string{"oauth_token": "sk-ant-oat-relayed"},
				Deliver: []api.CredentialDelivery{{Kind: "endpoint", Provider: "anthropic-proxy"}},
			}}},
		},
		CredentialSources: []deploywire.CredentialBacking{{
			Name: "claude", Backend: "static", Config: map[string]string{"oauth_token": "sk-ant-oat-relayed"},
		}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as, nil, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var creds []deploy.AttachCredential
	select {
	case creds = <-fb.creds:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
	session := creds[0].Session

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()

	// Mock upstream stands in for api.anthropic.com; record what the proxy sends.
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()

	// The Getter relays back to the client source on every request (as the
	// caretaker credential role does over the pod-scoped session).
	get := func(context.Context) (credential.Credential, error) {
		return fetchCredOverMux(mux, session, "claude")
	}
	ep, err := creddelivery.Open("anthropic-proxy", map[string]string{"upstream": up.URL})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = ep.Serve(ctx, ln, get) }()

	proxyBase := "http://" + ln.Addr().String()
	for i := 0; i < 100; i++ {
		if c, e := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); e == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	req, _ := http.NewRequest("POST", proxyBase+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer APP-SENT-BOGUS") // must be overridden
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("call through proxy: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got.Get("Authorization") != "Bearer sk-ant-oat-relayed" {
		t.Fatalf("upstream Authorization = %q, want the relayed OAuth token", got.Get("Authorization"))
	}
	if got.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatalf("upstream anthropic-beta = %q", got.Get("Anthropic-Beta"))
	}
}
