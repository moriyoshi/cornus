package clientconn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cornus/pkg/clientconfig"
	"cornus/pkg/tokencache"
)

// exchangeServer stands in for the server's token-exchange endpoint, minting a
// distinct credential per call so a re-exchange is visible in the result rather
// than only in a call count.
func exchangeServer(t *testing.T, minted *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := minted.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":      "minted-" + string(rune('0'+n)),
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"token_type":        "Bearer",
			"expires_in":        3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// isolateTokenCache points the on-disk token cache at a temp dir, so these tests
// neither read nor write the developer's real cached credentials.
func isolateTokenCache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CORNUS_TOKEN_CACHE", "file")
}

// TestRefreshExchangedTokenDiscardsTheCachedEntry is the regression test for the
// half of "refresh on expiry or 401" that did not exist.
//
// Expiry was always handled — tokencache.Get drops a lapsed entry. A 401 was not:
// resolveExchangedToken returns a cache hit or exchanges once, so a token the
// server stopped accepting BEFORE its stated expiry (restart with a new signing
// key, revocation, changed scope map) was handed back from the cache on every
// invocation. Every command failed identically and RE-RUNNING DID NOT HELP, for
// up to the token's full lifetime — an hour, at the TTL the server issues.
//
// The assertion that matters is the third one: the refresh must return a
// DIFFERENT credential. A refresh that dutifully re-reads the cache would satisfy
// a call-count check and fix nothing, and client.WithCredentialRefresher declines
// to replay an unchanged credential — so the whole feature would have been an
// extra round trip with no behavior change.
func TestRefreshExchangedTokenDiscardsTheCachedEntry(t *testing.T) {
	isolateTokenCache(t)
	var minted atomic.Int32
	srv := exchangeServer(t, &minted)

	cn := &Conn{Endpoint: srv.URL}
	cc := &clientconfig.Context{TokenExchange: &clientconfig.TokenExchange{Enabled: true, Scope: "api"}}
	const identity, subject = "server-identity", "subject-token"

	first, err := resolveExchangedToken(cn, identity, cc, subject)
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if minted.Load() != 1 {
		t.Fatalf("first call minted %d credentials, want 1", minted.Load())
	}

	// Second resolve must be a cache HIT — otherwise the test below cannot tell a
	// working refresh from a cache that never worked.
	cached, err := resolveExchangedToken(cn, identity, cc, subject)
	if err != nil {
		t.Fatalf("cached exchange: %v", err)
	}
	if cached != first || minted.Load() != 1 {
		t.Fatalf("the second resolve re-exchanged (minted=%d, token %q vs %q); this test's premise — that "+
			"a rejected credential would otherwise be served from the cache — no longer holds",
			minted.Load(), cached, first)
	}

	refreshed, err := refreshExchangedToken(cn, identity, cc, subject)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if minted.Load() != 2 {
		t.Errorf("refresh minted %d credentials in total, want 2: it read the cache instead of discarding it",
			minted.Load())
	}
	if refreshed == first {
		t.Errorf("refresh returned the SAME credential the server just rejected (%q). The client declines to "+
			"replay an unchanged credential, so this would be a wasted round trip and the command would keep "+
			"failing until the cache entry lapsed.", refreshed)
	}

	// And the fresh credential must be what a subsequent resolve serves, or the
	// next invocation goes back to the rejected one.
	after, err := resolveExchangedToken(cn, identity, cc, subject)
	if err != nil {
		t.Fatalf("resolve after refresh: %v", err)
	}
	if after != refreshed {
		t.Errorf("after a refresh the cache serves %q, want the fresh %q — the replacement was never stored, "+
			"so every later command re-exchanges (or worse, re-reads the dead one)", after, refreshed)
	}
}

// TestRefreshExchangedTokenSurvivesAnEmptyCache pins that the discard is
// best-effort. Deleting a key that is not there is not an error condition, and a
// refresh that failed on it would break exactly when the cache backend is `none`
// — the configuration chosen by users who do not want a token at rest, and so the
// one least able to afford an extra failure mode.
func TestRefreshExchangedTokenSurvivesAnEmptyCache(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("CORNUS_TOKEN_CACHE", "none")
	var minted atomic.Int32
	srv := exchangeServer(t, &minted)

	cn := &Conn{Endpoint: srv.URL}
	cc := &clientconfig.Context{TokenExchange: &clientconfig.TokenExchange{Enabled: true, Scope: "api"}}

	got, err := refreshExchangedToken(cn, "server-identity", cc, "subject-token")
	if err != nil {
		t.Fatalf("refresh with caching disabled: %v", err)
	}
	if got == "" {
		t.Error("refresh returned an empty credential with caching disabled")
	}
}

// TestExchangeCacheKeyIsStableAcrossResolveAndRefresh guards the failure mode a
// key mismatch would produce, which is silent: refresh would delete a key nobody
// reads, exchange a fresh credential, store it under that unread key, and leave
// the rejected one sitting in the cache for the next invocation. The command in
// hand would recover and every later one would fail again — an intermittency far
// harder to diagnose than the original permanent failure.
func TestExchangeCacheKeyIsStableAcrossResolveAndRefresh(t *testing.T) {
	cc := &clientconfig.Context{TokenExchange: &clientconfig.TokenExchange{Enabled: true, Scope: "api"}}
	const identity, subject = "server-identity", "subject-token"
	want := tokencache.Key("exchange", identity, exchangeSubjectIdentity(cc, subject), cc.TokenExchange.Scope)

	isolateTokenCache(t)
	var minted atomic.Int32
	srv := exchangeServer(t, &minted)
	cn := &Conn{Endpoint: srv.URL}

	if _, err := resolveExchangedToken(cn, identity, cc, subject); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	store, err := tokencache.Open(nil)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	if _, ok := store.Get(want); !ok {
		t.Fatalf("resolve did not store under the key this test derives (%q); the derivation here has "+
			"drifted from exchangeToken's and the checks below would be meaningless", want)
	}
	if _, err := refreshExchangedToken(cn, identity, cc, subject); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	e, ok := store.Get(want)
	if !ok {
		t.Fatal("after a refresh there is no entry under the shared key, so the next invocation re-exchanges")
	}
	if e.Token == "" {
		t.Error("the refreshed entry carries an empty credential")
	}
}
