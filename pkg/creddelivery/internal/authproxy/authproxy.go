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
	"strings"

	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
)

// Injector strips any client-sent auth and sets the provider's real auth header(s)
// on the outbound request from the fetched credential.
type Injector func(cred credential.Credential, out *http.Request)

// PlaceholderValue is handed to the app in the provider's API-key variable when
// the endpoint declares a KeyEnv. It is deliberately not a credential: Inject
// strips whatever auth the client sends before forwarding, so this value never
// reaches the upstream.
const PlaceholderValue = "sk-cornus-credential-proxy-placeholder"

// Endpoint reverse-proxies to Upstream, injecting auth via Inject. BaseURLEnv is
// the environment variable (e.g. ANTHROPIC_BASE_URL) pointed at the proxy so the
// app's SDK routes through it. KeyEnv, when set, is the provider's API-key
// variable (e.g. ANTHROPIC_API_KEY), filled with PlaceholderValue: SDKs and CLIs
// refuse to start — Claude Code tells the user to run `claude login`, the OpenAI
// SDK raises "api_key must be set" — when their key variable is missing, even
// though the proxy is the thing actually holding the credential.
type Endpoint struct {
	Upstream   string
	BaseURLEnv string
	KeyEnv     string
	Inject     Injector

	// ExtraEnv, when non-nil, contributes provider-specific variables merged into
	// Env's result after BaseURLEnv/KeyEnv. It exists for providers whose SDKs
	// need more than one URL (github-proxy also advertises GITHUB_GRAPHQL_URL).
	ExtraEnv func(addr string) map[string]string

	// RewriteUpstreamURLs rewrites absolute Location and Link RESPONSE headers
	// that point back at Upstream into the proxy's own base URL. Without it a
	// client that follows a redirect, or walks a paginated API's rel="next"
	// links, leaves the proxy and reaches the vendor unauthenticated — page one
	// carries the credential and page two does not. Off by default: the LLM APIs
	// paginate in the body and do not redirect, so they need no rewriting.
	RewriteUpstreamURLs bool
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
	if e.RewriteUpstreamURLs {
		// The rule is fully determined by (Upstream, listener addr), so the hook
		// carries no provider-specific knowledge.
		from := upstreamPrefix(target)
		to := "http://" + ln.Addr().String()
		proxy.ModifyResponse = func(resp *http.Response) error {
			rewriteHeaderURLs(resp.Header, from, to)
			return nil
		}
	}
	srv := &http.Server{Handler: proxy}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Env advertises the loopback proxy to the app via the provider's base-URL var,
// plus a placeholder API key so key-requiring clients start up, plus whatever
// ExtraEnv contributes.
func (e *Endpoint) Env(_ /* name */, addr string) map[string]string {
	env := map[string]string{e.BaseURLEnv: "http://" + addr}
	if e.KeyEnv != "" {
		env[e.KeyEnv] = PlaceholderValue
	}
	if e.ExtraEnv != nil {
		for k, v := range e.ExtraEnv(addr) {
			env[k] = v
		}
	}
	return env
}

// upstreamPrefix is the absolute-URL prefix the upstream mints in its own
// Location/Link headers: scheme://host plus any base path (GitHub Enterprise
// serves its REST API under /api/v3, and its links carry that prefix).
func upstreamPrefix(target *url.URL) string {
	return target.Scheme + "://" + target.Host + strings.TrimSuffix(target.Path, "/")
}

// rewriteHeaderURLs replaces the from prefix with to in the response headers
// that carry absolute upstream URLs a client will follow. Header-only by
// design: response BODIES also carry absolute URLs, but rewriting those would
// mean decompressing, re-encoding, buffering unbounded responses and
// invalidating Content-Length/ETag.
func rewriteHeaderURLs(h http.Header, from, to string) {
	for _, name := range []string{"Location", "Link"} {
		vs := h.Values(name)
		if len(vs) == 0 {
			continue
		}
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = strings.ReplaceAll(v, from, to)
		}
		h[name] = out
	}
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
