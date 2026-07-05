package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// jwksAlgs is the set of asymmetric signature algorithms a JWKS verifier accepts.
// HS256 is deliberately absent: a JWKS carries public keys, so allowing HMAC would
// reopen the public-key-as-HMAC confusion the single-key verifier already guards.
var jwksAlgs = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
}

// jwksSource yields the current key set, with rotation support. get returns the
// cached set (refreshed by the source's own policy); refresh forces a re-read/-fetch
// (rate-limited for a URL source) so a freshly-rotated key can be picked up on an
// otherwise-unknown kid.
type jwksSource interface {
	get() (*jose.JSONWebKeySet, error)
	refresh() (*jose.JSONWebKeySet, error)
}

// jwksResolver selects a verification key by the token's kid, refreshing once if the
// kid is unknown (rotation).
type jwksResolver struct{ src jwksSource }

// find returns the JWK for kid, trying a refresh if it is not in the cached set.
func (r *jwksResolver) find(kid string) (*jose.JSONWebKey, error) {
	ks, err := r.src.get()
	if err != nil {
		return nil, err
	}
	if jwk := pickKey(ks, kid); jwk != nil {
		return jwk, nil
	}
	// Unknown kid: the signer may have rotated. Force one (rate-limited) refresh.
	ks, err = r.src.refresh()
	if err != nil {
		return nil, err
	}
	if jwk := pickKey(ks, kid); jwk != nil {
		return jwk, nil
	}
	return nil, fmt.Errorf("jwks: no key for kid %q", kid)
}

// pickKey selects the JWK matching kid. When the token carries no kid it is only
// unambiguous if the set holds exactly one key.
func pickKey(ks *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
	if kid != "" {
		if k := ks.Key(kid); len(k) > 0 {
			return &k[0]
		}
		return nil
	}
	if len(ks.Keys) == 1 {
		return &ks.Keys[0]
	}
	return nil
}

// parseJWKS decodes a JWKS document and rejects an empty set.
func parseJWKS(data []byte) (*jose.JSONWebKeySet, error) {
	var ks jose.JSONWebKeySet
	if err := json.Unmarshal(data, &ks); err != nil {
		return nil, fmt.Errorf("jwks: parse: %w", err)
	}
	if len(ks.Keys) == 0 {
		return nil, errors.New("jwks: no keys in set")
	}
	return &ks, nil
}

// jwksFileSource reads a JWKS from a local file, reloading on mtime change — the
// rotation path for a file-mounted key set (e.g. a Kubernetes Secret/ConfigMap an
// operator updates).
type jwksFileSource struct {
	path string

	mu     sync.Mutex
	cached *jose.JSONWebKeySet
	mtime  time.Time
}

func (s *jwksFileSource) get() (*jose.JSONWebKeySet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(s.path)
	if err != nil {
		if s.cached != nil {
			return s.cached, nil
		}
		return nil, err
	}
	if s.cached != nil && !fi.ModTime().After(s.mtime) {
		return s.cached, nil
	}
	return s.reloadLocked(fi.ModTime())
}

func (s *jwksFileSource) refresh() (*jose.JSONWebKeySet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mt := s.mtime
	if fi, err := os.Stat(s.path); err == nil {
		mt = fi.ModTime()
	}
	return s.reloadLocked(mt)
}

func (s *jwksFileSource) reloadLocked(mtime time.Time) (*jose.JSONWebKeySet, error) {
	data, err := os.ReadFile(s.path)
	if err == nil {
		var ks *jose.JSONWebKeySet
		ks, err = parseJWKS(data)
		if err == nil {
			s.cached, s.mtime = ks, mtime
			return ks, nil
		}
	}
	if s.cached != nil {
		return s.cached, nil // keep the last good set through a transient read/parse error
	}
	return nil, err
}

// jwksURLSource fetches a JWKS from a URL, caching it for ttl. refresh re-fetches at
// most once per minRefetch, so an unknown-kid probe cannot be turned into a fetch
// flood against the issuer.
type jwksURLSource struct {
	url        string
	client     *http.Client
	ttl        time.Duration
	minRefetch time.Duration

	mu          sync.Mutex
	cached      *jose.JSONWebKeySet
	fetchedAt   time.Time
	lastAttempt time.Time // last fetch attempt (success OR failure); gates negative caching
	lastErr     error     // error from the last failed attempt (only meaningful when cached == nil)
}

func (s *jwksURLSource) get() (*jose.JSONWebKeySet, error) {
	return s.fetchIfStale(s.ttl)
}

func (s *jwksURLSource) refresh() (*jose.JSONWebKeySet, error) {
	return s.fetchIfStale(s.minRefetch)
}

// fetchIfStale returns the cached set when it is younger than maxAge; otherwise it
// re-fetches. A FAILED attempt is recorded (lastAttempt) and gated by minRefetch
// just like a successful one, so an unreachable/slow issuer is polled at most once
// per minRefetch instead of on every request — without that negative caching a
// stale-cache-plus-down-issuer turns every verify into a fresh 10s HTTP GET. The
// HTTP request itself runs WITHOUT s.mu held, so a slow issuer never serializes
// concurrent verifies behind the lock (it only briefly holds the lock to read the
// cache decision and to swap in the result).
func (s *jwksURLSource) fetchIfStale(maxAge time.Duration) (*jose.JSONWebKeySet, error) {
	s.mu.Lock()
	if s.cached != nil && time.Since(s.fetchedAt) < maxAge {
		ks := s.cached
		s.mu.Unlock()
		return ks, nil
	}
	// A recent attempt (success or failure) suppresses another fetch for
	// minRefetch: this is the negative cache that stops a down issuer from being
	// polled once per request while the lock is held.
	if !s.lastAttempt.IsZero() && time.Since(s.lastAttempt) < s.minRefetch {
		ks, err := s.cached, s.lastErr
		s.mu.Unlock()
		if ks != nil {
			return ks, nil // serve the last good set through the backoff window
		}
		return nil, err
	}
	s.mu.Unlock()

	ks, err := s.fetch()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAttempt = time.Now()
	if err != nil {
		s.lastErr = err
		if s.cached != nil {
			return s.cached, nil // serve the last good set through a transient fetch error
		}
		return nil, err
	}
	s.cached, s.fetchedAt, s.lastErr = ks, s.lastAttempt, nil
	return ks, nil
}

// fetch performs the HTTP GET and parses the JWKS. It holds no lock, so a slow or
// hung issuer (bounded by the 10s context) never blocks other verifies.
func (s *jwksURLSource) fetch() (*jose.JSONWebKeySet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: GET %s: status %d", s.url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseJWKS(data)
}

// newJWKSResolver builds a resolver from the environment. A file source is validated
// eagerly (fail fast on a bad path); a URL source is lazy (fetched on first verify),
// so a briefly-unreachable issuer at boot does not stop the server.
func newJWKSResolver() (*jwksResolver, error) {
	file := os.Getenv("CORNUS_JWT_JWKS_FILE")
	url := os.Getenv("CORNUS_JWT_JWKS_URL")
	switch {
	case file != "" && url != "":
		return nil, errors.New("set only one of CORNUS_JWT_JWKS_FILE / CORNUS_JWT_JWKS_URL")
	case file != "":
		src := &jwksFileSource{path: file}
		if _, err := src.get(); err != nil {
			return nil, fmt.Errorf("CORNUS_JWT_JWKS_FILE: %w", err)
		}
		return &jwksResolver{src: src}, nil
	case url != "":
		src := &jwksURLSource{
			url:        url,
			client:     &http.Client{Timeout: 15 * time.Second},
			ttl:        5 * time.Minute,
			minRefetch: time.Minute,
		}
		return &jwksResolver{src: src}, nil
	}
	return nil, nil
}
