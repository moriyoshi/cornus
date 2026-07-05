// Package anthropicproxy is a credential delivery provider that proxies a
// workload's calls to the Anthropic API, injecting the developer's local
// credential. The app sets ANTHROPIC_BASE_URL to the sidecar and holds no key;
// the proxy forwards to https://api.anthropic.com with the right auth:
//
//   - an API key ("sk-ant-api...") is sent as x-api-key;
//   - an OAuth / `ant auth login` token ("sk-ant-oat...") is sent as
//     Authorization: Bearer plus the required anthropic-beta: oauth-2025-04-20
//     header.
//
// The credential value is taken from the Values keys, in order: "oauth_token"
// (forces OAuth), "api_key" (forces API-key), else "value"/"token" with the kind
// auto-detected by the "sk-ant-oat" prefix. Any client-sent auth is stripped.
package anthropicproxy

import (
	"net/http"
	"strings"

	"cornus/pkg/creddelivery"
	"cornus/pkg/creddelivery/internal/authproxy"
	"cornus/pkg/credential"
)

// oauthBeta is the beta header OAuth bearer tokens require on the Anthropic API.
const oauthBeta = "oauth-2025-04-20"

// defaultUpstream is the real Anthropic API; cfg["upstream"] overrides it for an
// Anthropic-compatible gateway (e.g. an on-prem proxy) or a test mock.
const defaultUpstream = "https://api.anthropic.com"

func init() {
	creddelivery.Register("anthropic-proxy", func(cfg map[string]string) (creddelivery.Endpoint, error) {
		up := cfg["upstream"]
		if up == "" {
			up = defaultUpstream
		}
		return &authproxy.Endpoint{
			Upstream:   up,
			BaseURLEnv: "ANTHROPIC_BASE_URL",
			Inject:     inject,
		}, nil
	})
}

func inject(cred credential.Credential, out *http.Request) {
	out.Header.Del("Authorization")
	out.Header.Del("X-Api-Key")

	tok, oauth := token(cred)
	if oauth {
		out.Header.Set("Authorization", "Bearer "+tok)
		addBeta(out, oauthBeta)
	} else {
		out.Header.Set("X-Api-Key", tok)
	}
}

// token resolves the credential value and whether it is an OAuth bearer token.
func token(cred credential.Credential) (string, bool) {
	if v := authproxy.Pick(cred, "oauth_token"); v != "" {
		return v, true
	}
	if v := authproxy.Pick(cred, "api_key"); v != "" {
		return v, false
	}
	v := authproxy.Pick(cred, "value", "token")
	return v, strings.HasPrefix(v, "sk-ant-oat")
}

// addBeta merges beta into the comma-separated anthropic-beta header if absent.
func addBeta(out *http.Request, beta string) {
	cur := out.Header.Get("anthropic-beta")
	for _, b := range strings.Split(cur, ",") {
		if strings.TrimSpace(b) == beta {
			return
		}
	}
	if cur == "" {
		out.Header.Set("anthropic-beta", beta)
	} else {
		out.Header.Set("anthropic-beta", cur+","+beta)
	}
}
