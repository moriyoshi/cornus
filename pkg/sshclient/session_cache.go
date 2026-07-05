package sshclient

import (
	"time"

	"cornus/pkg/tokencache"
)

// SSH-key session caching, on the shared client credential store.
//
// This file used to own its own JSON-file cache under the runtime directory.
// pkg/tokencache is that mechanism, generalized: same runtime-dir location, same
// 0600-in-0700 discipline, same write-then-rename, plus an OS keyring backend
// where one is reachable. Two caches for two kinds of short-lived client
// credential would have drifted — different locations, different expiry margins,
// and only one of them ever gaining the keyring.
//
// The exported API here is unchanged, so callers (clientconn, and the read-only
// background-agent path) are unaffected. What DID change is where an entry
// physically lives: on a desktop with a Secret Service it is now in the keyring
// rather than a file. Existing cached sessions are therefore orphaned by the
// upgrade — harmless, since they are hour-lived and the miss costs one re-mint.

const sessionExpiryMargin = 2 * time.Minute

// sessionKey addresses one cached session.
//
// identity must be a STABLE name for the server the session was minted against —
// derived from the connection profile, never from a resolved endpoint. A
// port-forward profile binds a different random localhost port on every run, so
// keying on the dialed address would produce a fresh key each time and the cache
// would never hit. Callers own that derivation; see clientconn.sessionCacheIdentity.
//
// The "ssh-session" prefix namespaces these against other credentials in the same
// store (an exchanged OAuth token, say), so two kinds of credential for the same
// server can never collide on one key.
func sessionKey(identity, fingerprint, scope string) string {
	return tokencache.Key("ssh-session", identity, fingerprint, scope)
}

// store opens the shared credential store. A store that cannot be opened is not
// an error worth failing a command over: caching is an optimization, so this
// degrades to no caching and the caller mints afresh.
func store() tokencache.Store {
	s, err := tokencache.Open(nil)
	if err != nil {
		return tokencache.None()
	}
	return s
}

// ReadSessionEntry returns the cached session verbatim, with its expiry and
// without applying the safety margin. A long-lived caller that must decide when to
// renew needs the expiry itself; ReadSession is the margin-applying form built on
// this. A missing entry reports ok=false and no error.
func ReadSessionEntry(identity, fingerprint, scope string) (token string, expiresAt time.Time, ok bool, err error) {
	e, found := store().GetRaw(sessionKey(identity, fingerprint, scope))
	if !found {
		return "", time.Time{}, false, nil
	}
	return e.Token, e.Expires, true, nil
}

// ReadSession returns a cached SSH-key session only when it remains valid beyond
// the two-minute safety margin. A missing or nearly expired entry is a cache miss.
//
// The margin is applied HERE rather than taken from the store's default, because
// it is this credential's renewal policy: an SSH-key session is re-mintable at any
// time by a foreground command, so it is worth abandoning earlier than a token
// whose re-minting needs a round trip to a third party.
func ReadSession(identity, fingerprint, scope string, now time.Time) (string, bool, error) {
	token, expiresAt, ok, err := ReadSessionEntry(identity, fingerprint, scope)
	if err != nil || !ok {
		return "", false, err
	}
	if !now.Add(sessionExpiryMargin).Before(expiresAt) {
		return "", false, nil
	}
	return token, true, nil
}

// WriteSession stores one short-lived SSH-key session in the shared credential
// store.
func WriteSession(identity, fingerprint, scope, token string, expiresAt time.Time) error {
	return store().Set(sessionKey(identity, fingerprint, scope), tokencache.Entry{
		Token:   token,
		Expires: expiresAt,
	})
}

// DeleteSession drops a cached session, so a credential that the server has
// started refusing is not re-presented on every later invocation.
func DeleteSession(identity, fingerprint, scope string) error {
	return store().Delete(sessionKey(identity, fingerprint, scope))
}
