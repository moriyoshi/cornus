// Package authproxy is the shared implementation behind the LLM auth-injecting
// delivery providers (anthropic-proxy, openai-proxy). It is a creddelivery
// Endpoint: an HTTP reverse proxy on a loopback listener that, per request,
// fetches the relayed credential and injects the provider's auth header before
// forwarding to the real upstream over TLS. The app points its base URL at the
// proxy and never holds the raw secret; short-lived tokens stay fresh because the
// credential is fetched (TTL-cached) on every request.
package authproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
)

// Injector strips any client-sent auth and sets the provider's real auth header(s)
// on the outbound request from the fetched credential.
type Injector func(cred credential.Credential, out *http.Request)

// Endpoint reverse-proxies to Upstream, injecting auth via Inject. BaseURLEnv is
// the environment variable (e.g. ANTHROPIC_BASE_URL) pointed at the proxy so the
// app's SDK routes through it.
type Endpoint struct {
	Upstream   string
	BaseURLEnv string
	Inject     Injector
}

// Serve runs the reverse proxy on ln until ctx is cancelled.
func (e *Endpoint) Serve(ctx context.Context, ln net.Listener, get creddelivery.Getter) error {
	target, err := url.Parse(e.Upstream)
	if err != nil {
		return fmt.Errorf("authproxy: parse upstream %q: %w", e.Upstream, err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)         // scheme/host/path + Host header
			pr.Out.Host = target.Host // ensure SNI + Host match the real API
		},
		Transport: &injectTransport{get: get, inject: e.Inject, base: http.DefaultTransport},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "cornus credential proxy: "+err.Error(), http.StatusBadGateway)
		},
	}
	srv := &http.Server{Handler: proxy}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Env advertises the loopback proxy to the app via the provider's base-URL var.
func (e *Endpoint) Env(_ /* name */, addr string) map[string]string {
	return map[string]string{e.BaseURLEnv: "http://" + addr}
}

// WellKnownAddr is empty — the proxy binds loopback and is advertised via env.
func (e *Endpoint) WellKnownAddr() string { return "" }

type injectTransport struct {
	get    creddelivery.Getter
	inject Injector
	base   http.RoundTripper
}

func (t *injectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cred, err := t.get(req.Context())
	if err != nil {
		return nil, fmt.Errorf("fetch credential: %w", err)
	}
	t.inject(cred, req)
	return t.base.RoundTrip(req)
}

// Pick returns the first non-empty value among keys, or "".
func Pick(cred credential.Credential, keys ...string) string {
	for _, k := range keys {
		if v := cred.Values[k]; v != "" {
			return v
		}
	}
	return ""
}
