// Package githubproxy is a credential delivery provider that proxies a
// workload's calls to the GitHub REST API, injecting the caller's own GitHub
// token. The app sets GITHUB_API_URL to the sidecar and holds no token; the
// proxy forwards to https://api.github.com with `Authorization: Bearer`.
//
// Bearer is used unconditionally: GitHub accepts it for classic PATs (ghp_),
// fine-grained PATs (github_pat_), OAuth tokens (gho_) and installation tokens
// (ghs_), and REQUIRES it for App JWTs. So unlike anthropic-proxy there is no
// token-shape detection. The value is taken from the Values keys, in order:
// "token" (what the github-cli source emits), "api_key", then "value".
//
// cfg["upstream"] retargets the proxy at a GitHub Enterprise Server REST base
// (https://ghe.corp/api/v3) or a test mock. It is the ONLY knob, because it is
// the only per-delivery config key the caretaker and the kubernetes backend
// plumb through to a provider.
//
// # Scope invariant
//
// The source's hostname and this delivery's upstream live in different layers —
// source config is resolved on the client, delivery config on the deploy path —
// and nothing correlates them. Declaring a github-cli source with
// `hostname: ghe.corp` while leaving upstream at the default therefore sends a
// valid GitHub Enterprise credential to api.github.com. It cannot be validated
// here; always configure the pair together.
//
// # What this does NOT cover
//
//   - git. Only the REST API rides this proxy; git-over-HTTPS goes straight to
//     github.com:443, so `git clone` / `git push` are unaffected and unauthenticated.
//   - The gh CLI. gh takes a hostname rather than a base URL and always speaks
//     https, so it cannot be pointed at a plaintext loopback listener.
//   - GraphQL on GitHub Enterprise. On api.github.com GraphQL is upstream+/graphql,
//     but GHES serves REST under /api/v3 and GraphQL under the SIBLING /api/graphql,
//     which one reverse proxy with one target prefix cannot reach. GITHUB_GRAPHQL_URL
//     is therefore advertised only when the upstream has no base path — emitting a
//     wrong URL would be worse than emitting none, since a client that saw it would
//     use it and reach GHES with no credential.
//   - Absolute URLs in response BODIES (url, html_url, commits_url, _links). Only
//     Location and Link headers are rewritten; see authproxy.rewriteHeaderURLs.
//   - uploads.github.com (release assets), which is a separate host.
//   - A GitHub Enterprise instance behind a private CA: the proxy dials with
//     http.DefaultTransport, so the CA must reach the caretaker via SSL_CERT_FILE /
//     SSL_CERT_DIR or be baked into its image.
package githubproxy

import (
	"fmt"
	"net/http"
	"net/url"

	"cornus/pkg/creddelivery"
	"cornus/pkg/creddelivery/internal/authproxy"
	"cornus/pkg/credential"
)

// defaultUpstream is the real GitHub REST API; cfg["upstream"] overrides it for
// GitHub Enterprise Server, a gateway, or a test mock.
const defaultUpstream = "https://api.github.com"

// defaultUserAgent is sent when the app's client set none. httputil.ReverseProxy
// deliberately blanks a missing User-Agent, and GitHub answers a request without
// one with 403 "Request forbidden by administrative rules" — an error that reads
// like an auth failure and would be blamed on this proxy.
const defaultUserAgent = "cornus-github-proxy"

func init() {
	creddelivery.Register("github-proxy", func(cfg map[string]string) (creddelivery.Endpoint, error) {
		up := cfg["upstream"]
		if up == "" {
			up = defaultUpstream
		}
		u, err := url.Parse(up)
		if err != nil {
			return nil, fmt.Errorf("github-proxy: parse upstream %q: %w", up, err)
		}
		ep := &authproxy.Endpoint{
			Upstream:   up,
			BaseURLEnv: "GITHUB_API_URL",
			// KeyEnv is deliberately empty. A placeholder GITHUB_TOKEN would be
			// picked up by consumers that never read GITHUB_API_URL — the gh CLI
			// (both GH_TOKEN and GITHUB_TOKEN override its credential store),
			// git-over-HTTPS credential helpers, and hand-rolled curl calls —
			// turning "no credential configured" into a confusing 401 far from
			// the cause. A workload whose client demands the variable can set any
			// dummy itself via the spec's env block; the proxy strips whatever
			// the client sends.
			Inject: inject,
			// GitHub paginates with absolute rel="next" URLs in the Link header
			// and 301s renamed repos with an absolute Location; without rewriting,
			// page one is authenticated and page two is not.
			RewriteUpstreamURLs: true,
		}
		if u.Path == "" || u.Path == "/" {
			ep.ExtraEnv = graphqlEnv
		}
		return ep, nil
	})
}

// graphqlEnv advertises the GraphQL endpoint, which sits at /graphql under an
// upstream with no base path.
func graphqlEnv(addr string) map[string]string {
	return map[string]string{"GITHUB_GRAPHQL_URL": "http://" + addr + "/graphql"}
}

func inject(cred credential.Credential, out *http.Request) {
	out.Header.Del("Authorization")
	// Set the header even when the token is empty. Omitting it would leave the
	// request ANONYMOUS, which GitHub happily serves (public data, 60 req/hr) —
	// so a broken credential would surface as wrong data rather than a failure.
	// A bare "Bearer " gets a clean 401. Injector cannot return an error, so this
	// is the only way to make the failure visible.
	out.Header.Set("Authorization", "Bearer "+authproxy.Pick(cred, "token", "api_key", "value"))
	if out.Header.Get("User-Agent") == "" {
		out.Header.Set("User-Agent", defaultUserAgent)
	}
}
