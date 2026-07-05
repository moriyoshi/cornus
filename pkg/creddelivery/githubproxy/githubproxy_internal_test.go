package githubproxy

import (
	"net/http"
	"testing"

	"cornus/pkg/credential"
)

func req(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInjectSetsBearerAndStripsClientAuth(t *testing.T) {
	r := req(t)
	r.Header.Set("Authorization", "Bearer APP-SENT-BOGUS")
	inject(credential.Credential{Values: map[string]string{"token": "gho_real"}}, r)
	if got := r.Header.Get("Authorization"); got != "Bearer gho_real" {
		t.Fatalf("Authorization = %q, want the injected token", got)
	}
}

// TestInjectKeyPrecedence pins that one delivery serves every source spelling:
// github-cli emits "token", an exec source may emit "api_key", static/env emit
// "value".
func TestInjectKeyPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"token wins", map[string]string{"token": "a", "api_key": "b", "value": "c"}, "a"},
		{"api_key over value", map[string]string{"api_key": "b", "value": "c"}, "b"},
		{"value alone", map[string]string{"value": "c"}, "c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t)
			inject(credential.Credential{Values: tc.values}, r)
			if got := r.Header.Get("Authorization"); got != "Bearer "+tc.want {
				t.Fatalf("Authorization = %q, want %q", got, "Bearer "+tc.want)
			}
		})
	}
}

// TestInjectEmptyTokenStillSendsHeader is the anti-silent-anonymous guard. If the
// header were omitted for an empty credential, GitHub would serve the request
// anonymously (public data, 60 req/hr) and a broken credential would look like
// working-but-wrong data instead of a 401.
func TestInjectEmptyTokenStillSendsHeader(t *testing.T) {
	r := req(t)
	inject(credential.Credential{Values: map[string]string{}}, r)
	if _, ok := r.Header["Authorization"]; !ok {
		t.Fatal("Authorization must be set even for an empty token, or the request goes out anonymous")
	}
	if got := r.Header.Get("Authorization"); got != "Bearer " {
		t.Fatalf("Authorization = %q, want a bare bearer that 401s cleanly", got)
	}
}

// TestInjectDefaultsUserAgent covers ReverseProxy blanking a missing User-Agent:
// GitHub 403s a request without one, with a message that reads like auth failure.
func TestInjectDefaultsUserAgent(t *testing.T) {
	r := req(t)
	r.Header.Set("User-Agent", "") // exactly what httputil.ReverseProxy does
	inject(credential.Credential{Values: map[string]string{"token": "t"}}, r)
	if got := r.Header.Get("User-Agent"); got != defaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, defaultUserAgent)
	}

	r2 := req(t)
	r2.Header.Set("User-Agent", "octokit/1.2")
	inject(credential.Credential{Values: map[string]string{"token": "t"}}, r2)
	if got := r2.Header.Get("User-Agent"); got != "octokit/1.2" {
		t.Fatalf("User-Agent = %q, want the app's own value preserved", got)
	}
}
