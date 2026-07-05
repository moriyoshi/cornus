// Package openaiproxy is a credential delivery provider that proxies a workload's
// calls to the OpenAI API, injecting the developer's local API key. The app sets
// OPENAI_BASE_URL to the sidecar and holds no key; the proxy forwards to
// https://api.openai.com with Authorization: Bearer <key>. The key is taken from
// the Values keys "api_key", "value", or "token"; any client-sent auth is
// stripped.
package openaiproxy

import (
	"net/http"

	"cornus/pkg/creddelivery"
	"cornus/pkg/creddelivery/internal/authproxy"
	"cornus/pkg/credential"
)

// defaultUpstream is the real OpenAI API; cfg["upstream"] overrides it for an
// OpenAI-compatible gateway (Azure OpenAI, a self-hosted proxy) or a test mock.
const defaultUpstream = "https://api.openai.com"

func init() {
	creddelivery.Register("openai-proxy", func(cfg map[string]string) (creddelivery.Endpoint, error) {
		up := cfg["upstream"]
		if up == "" {
			up = defaultUpstream
		}
		return &authproxy.Endpoint{
			Upstream:   up,
			BaseURLEnv: "OPENAI_BASE_URL",
			Inject:     inject,
		}, nil
	})
}

func inject(cred credential.Credential, out *http.Request) {
	out.Header.Del("Authorization")
	if key := authproxy.Pick(cred, "api_key", "value", "token"); key != "" {
		out.Header.Set("Authorization", "Bearer "+key)
	}
}
