package clientconn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"cornus/pkg/clientconfig"
	"cornus/pkg/tokencache"
	"cornus/pkg/tokenexchange"
)

// exchangeTimeout bounds the one extra round trip. Generous, because for a
// cluster profile it rides a port-forward that has just been established.
const exchangeTimeout = 30 * time.Second

// resolveExchangedToken trades the credential a profile produced for one the
// SERVER minted, caching the result so the trade happens once per token lifetime
// rather than once per command.
//
// Without the cache this would be a latency regression: the CLI is a short-lived
// process, so every invocation would mint a ServiceAccount token AND exchange it
// — two round trips where sending the subject token directly needs none. With it,
// the common case is a cache read.
//
// What the exchange buys, beyond the audit trail on the server side, is that the
// credential on the wire can be NARROWER than what the caller is entitled to: a
// profile used for pulling images can hold one that cannot deploy, even though
// the same identity could have obtained one that could.
func resolveExchangedToken(cn *Conn, identity string, cc *clientconfig.Context, subject string) (string, error) {
	return exchangeToken(cn, identity, cc, subject, false)
}

// refreshExchangedToken re-runs the exchange with the cache entry DISCARDED, for
// a credential the server has rejected with 401 before its stated expiry.
//
// The discard is the whole point. The cache honours the expiry the server
// reported, so a token invalidated early — the server restarted with a new
// signing key, the token was revoked, the scope map changed — stays "valid" to
// the cache and is handed back on every subsequent invocation. Without dropping
// it, a refresh would return the same dead string and the retry would earn a
// second identical 401 (client.WithCredentialRefresher declines to replay in that
// case, so the only symptom would have been an extra round trip and a failure
// that re-running never fixes, for up to the token's full lifetime).
func refreshExchangedToken(cn *Conn, identity string, cc *clientconfig.Context, subject string) (string, error) {
	return exchangeToken(cn, identity, cc, subject, true)
}

func exchangeToken(cn *Conn, identity string, cc *clientconfig.Context, subject string, discardCached bool) (string, error) {
	if cn.Endpoint == "" {
		return "", errors.New("token-exchange requires a server endpoint")
	}
	if identity == "" {
		return "", errors.New("token-exchange has no stable server identity to cache against")
	}
	scope := cc.TokenExchange.Scope
	key := tokencache.Key("exchange", identity, exchangeSubjectIdentity(cc, subject), scope)

	store, err := tokencache.Open(func(format string, args ...any) {
		slog.Debug(fmt.Sprintf(format, args...))
	})
	if err != nil {
		// A misconfigured CORNUS_TOKEN_CACHE is worth reporting: the user asked for
		// a backend by name and did not get it.
		return "", err
	}
	if discardCached {
		// Best-effort: a store that cannot delete still gets a fresh exchange below,
		// and the new entry overwrites the old one on the way out.
		if err := store.Delete(key); err != nil {
			slog.Debug("could not drop the rejected exchanged credential from the cache", "error", err)
		}
	} else if e, ok := store.Get(key); ok {
		return e.Token, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), exchangeTimeout)
	defer cancel()
	res, err := tokenexchange.Exchange(ctx, tokenexchange.Options{
		Server:       cn.Endpoint,
		SubjectToken: subject,
		Scope:        scope,
		HTTPClient:   exchangeHTTPClient(cn),
	})
	switch {
	case errors.Is(err, tokenexchange.ErrUnsupported):
		// The server has no exchange endpoint — it predates one, or it has no
		// JWT/JWKS verifier so the route is deliberately unregistered. Sending the
		// subject token directly is exactly what happened before exchange existed,
		// and is what the scope map on the far side will evaluate anyway. Degrading
		// beats failing a command over an optimization.
		slog.Debug("server does not offer token exchange; sending the credential directly", "server", cn.Endpoint)
		return subject, nil
	case err != nil:
		return "", err
	}

	// Cache against the expiry the SERVER reported. A client-side guess would
	// drift from the token's real lifetime in whichever direction is worse.
	if res.ExpiresIn > 0 {
		if err := store.Set(key, tokencache.Entry{
			Token:   res.Token,
			Expires: time.Now().Add(res.ExpiresIn),
		}); err != nil {
			// Not fatal: the token in hand is good, it just will not be reused.
			slog.Debug("could not cache the exchanged credential", "error", err)
		}
	}
	return res.Token, nil
}

// exchangeSubjectIdentity names WHAT produced the subject token, so two profiles
// on one machine that exchange different identities against the same server do
// not share a cache entry.
//
// It is deliberately the CONFIGURATION rather than the token, for the same reason
// sessionCacheIdentity is: a kube-auth subject token is freshly minted on every
// invocation, so keying on its bytes would miss every single time. A static token
// has no such indirection, so there the bytes ARE the identity — and keying on
// them means rotating the token invalidates the cache by itself.
func exchangeSubjectIdentity(cc *clientconfig.Context, subject string) string {
	if cc.KubeAuth != nil {
		ka := cc.KubeAuth
		return fmt.Sprintf("kube-auth|%s|%s|%s|%s", ka.KubeContext, ka.Namespace, ka.ServiceAccount, ka.Audience)
	}
	return "token|" + subject
}

// exchangeHTTPClient dials the server the way the rest of this connection does —
// through the profile's TLS material and any tunnel dialer — so the exchange
// works for an ssh-tunnel profile rather than only a directly reachable one.
func exchangeHTTPClient(cn *Conn) *http.Client {
	tr := &http.Transport{}
	if cn.TLS != nil {
		tr.TLSClientConfig = cn.TLS.Clone()
	}
	if cn.DialContext != nil {
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return cn.DialContext(ctx, network, addr)
		}
	}
	return &http.Client{Transport: tr, Timeout: exchangeTimeout}
}
