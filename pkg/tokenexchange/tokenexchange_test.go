package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// echoServer records the form it received and answers with resp.
func echoServer(t *testing.T, status int, resp any, got *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got != nil {
			*got = r.PostForm
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestExchangeSendsRFC8693AndReturnsTheIssuedScope(t *testing.T) {
	var form url.Values
	srv := echoServer(t, http.StatusOK, map[string]any{
		"access_token":      "minted",
		"issued_token_type": tokenTypeAccessToken,
		"token_type":        "Bearer",
		"expires_in":        3600,
		// The server issued LESS than the subject claimed. The caller must report
		// what was issued, not what it asked for — that is what it caches against.
		"scope": "registry:pull",
	}, &form)
	defer srv.Close()

	res, err := Exchange(context.Background(), Options{
		Server:       srv.URL,
		SubjectToken: "subject-jwt",
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != "minted" || res.Scope != "registry:pull" || res.ExpiresIn != time.Hour {
		t.Fatalf("result = %+v", res)
	}
	if got := form.Get("grant_type"); got != grantType {
		t.Fatalf("grant_type = %q", got)
	}
	if got := form.Get("subject_token"); got != "subject-jwt" {
		t.Fatalf("subject_token = %q", got)
	}
	if got := form.Get("subject_token_type"); got != tokenTypeJWT {
		t.Fatalf("subject_token_type = %q", got)
	}
	// Absent, not empty: an empty scope parameter is a request for no scope, which
	// is not the same as declining to narrow.
	if _, ok := form["scope"]; ok {
		t.Fatalf("scope was sent when none was requested: %q", form.Get("scope"))
	}
}

func TestExchangeSendsRequestedScope(t *testing.T) {
	var form url.Values
	srv := echoServer(t, http.StatusOK, map[string]any{
		"access_token": "minted", "token_type": "Bearer", "expires_in": 60, "scope": "registry:pull",
	}, &form)
	defer srv.Close()

	if _, err := Exchange(context.Background(), Options{
		Server: srv.URL, SubjectToken: "s", Scope: " registry:pull ", HTTPClient: srv.Client(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := form.Get("scope"); got != "registry:pull" {
		t.Fatalf("scope = %q, want it trimmed to registry:pull", got)
	}
}

// TestExchangeSurfacesOAuthErrors: a caller has to tell "this identity is
// entitled to nothing" from "you built the request wrong", and matching on prose
// would break the first time a message is reworded.
func TestExchangeSurfacesOAuthErrors(t *testing.T) {
	srv := echoServer(t, http.StatusBadRequest, map[string]any{
		"error":             "invalid_grant",
		"error_description": "no scope-map rule matched",
	}, nil)
	defer srv.Close()

	_, err := Exchange(context.Background(), Options{
		Server: srv.URL, SubjectToken: "s", HTTPClient: srv.Client(),
	})
	var oe *Error
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v, want a *tokenexchange.Error", err)
	}
	if oe.Code != "invalid_grant" || oe.Status != http.StatusBadRequest {
		t.Fatalf("error = %+v", oe)
	}
	if oe.Description == "" {
		t.Fatal("the description was dropped; it is what tells an operator which rule to add")
	}
}

// TestExchangeUnsupportedIsDistinct: a server with no exchange route (auth off,
// or an older cornus) must be distinguishable, because the caller's correct
// response is to fall back to the direct path rather than fail the command.
func TestExchangeUnsupportedIsDistinct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := Exchange(context.Background(), Options{
		Server: srv.URL, SubjectToken: "s", HTTPClient: srv.Client(),
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestExchangeRejectsUnusableResponses: every one of these would otherwise
// surface later as a failed request whose error says nothing about the exchange.
func TestExchangeRejectsUnusableResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp map[string]any
	}{
		{"no token", map[string]any{"token_type": "Bearer", "expires_in": 60}},
		{"not a bearer", map[string]any{"access_token": "t", "token_type": "mac", "expires_in": 60}},
		{"unknown issued type", map[string]any{
			"access_token": "t", "token_type": "Bearer", "expires_in": 60,
			"issued_token_type": "urn:example:saml2",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := echoServer(t, http.StatusOK, tc.resp, nil)
			defer srv.Close()
			if _, err := Exchange(context.Background(), Options{
				Server: srv.URL, SubjectToken: "s", HTTPClient: srv.Client(),
			}); err == nil {
				t.Fatal("an unusable response was accepted")
			}
		})
	}
}

func TestExchangeValidatesItsOwnInputs(t *testing.T) {
	if _, err := Exchange(context.Background(), Options{SubjectToken: "s"}); err == nil {
		t.Fatal("no server was accepted")
	}
	if _, err := Exchange(context.Background(), Options{Server: "http://x"}); err == nil {
		t.Fatal("no subject token was accepted")
	}
}
