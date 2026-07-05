package caretaker

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/sync/errgroup"

	"cornus/pkg/creddelivery"
	"cornus/pkg/credential"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// defaultCredTTL bounds how long a fetched credential is reused when it carries
// no expiry and the role sets no TTL — short enough that a rotated upstream
// credential is picked up promptly, long enough to avoid re-minting per request.
const defaultCredTTL = 5 * time.Minute

// expirySkew is trimmed off a credential's own expiry so it is re-minted before
// it actually expires (clock skew + propagation).
const expirySkew = 30 * time.Second

// runCredential serves one credential role for the life of ctx: it shares a
// single cached fetcher (so an endpoint and a file for the same source do not
// double-mint) and runs each delivery. It returns when ctx is cancelled or a
// delivery fails fatally (which drops the whole pod-scoped connection, matching
// the mount role's fail-fast).
func runCredential(ctx context.Context, sess *yamux.Session, role CredentialRole) error {
	return runCredentialWith(ctx, func() (net.Conn, error) { return sess.OpenStream() }, role)
}

// runCredentialWith is runCredential with the stream opener injected, so tests
// can drive it over an in-process session without a real WebSocket.
func runCredentialWith(ctx context.Context, open func() (net.Conn, error), role CredentialRole) error {
	f := &credFetcher{open: open, role: role, ttl: parseTTL(role.TTL)}
	g, gctx := errgroup.WithContext(ctx)
	for _, d := range role.Deliver {
		d := d
		switch d.Kind {
		case "", "endpoint":
			g.Go(func() error { return serveCredEndpoint(gctx, d, f) })
		case "file":
			g.Go(func() error { return serveCredFile(gctx, d, f) })
		default:
			return fmt.Errorf("credential %s: unknown delivery kind %q", role.Name, d.Kind)
		}
	}
	return g.Wait()
}

// credFetcher mints (via the relay) and caches one credential. open yields a
// fresh stream on the pod-scoped session for each fetch.
type credFetcher struct {
	open func() (net.Conn, error)
	role CredentialRole
	ttl  time.Duration

	mu     sync.Mutex
	cached credential.Credential
	expiry time.Time
	have   bool
}

func (f *credFetcher) get(ctx context.Context) (credential.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	if f.have && now.Before(f.expiry) {
		return f.cached, nil
	}
	cred, err := f.fetch(ctx)
	if err != nil {
		return credential.Credential{}, err
	}
	f.cached = cred
	f.have = true
	f.expiry = credExpiry(now, cred.Expiration, f.ttl)
	return cred, nil
}

// fetch opens one credential stream on the pod-scoped session, writes the
// session/name lines the server relay routes on, and performs the exchange.
func (f *credFetcher) fetch(ctx context.Context) (credential.Credential, error) {
	stream, err := f.open()
	if err != nil {
		return credential.Credential{}, err
	}
	defer stream.Close()
	if _, err := stream.Write([]byte{wire.TagCredential}); err != nil {
		return credential.Credential{}, err
	}
	if _, err := io.WriteString(stream, f.role.Session+"\n"+f.role.Name+"\n"); err != nil {
		return credential.Credential{}, err
	}
	return deploywire.FetchCredential(stream, nil)
}

// credExpiry is the earliest of the credential's own (skew-trimmed) expiry and
// now+ttl, so a short-lived upstream credential drives refresh even under a long
// TTL.
func credExpiry(now, exp time.Time, ttl time.Duration) time.Time {
	deadline := now.Add(ttl)
	if !exp.IsZero() {
		if e := exp.Add(-expirySkew); e.Before(deadline) {
			deadline = e
		}
	}
	if deadline.Before(now) {
		return now // never cache a value already past its (skewed) expiry
	}
	return deadline
}

func parseTTL(s string) time.Duration {
	if s == "" {
		return defaultCredTTL
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return defaultCredTTL
}

// serveCredEndpoint binds the delivery's resolved address and serves the
// provider's HTTP shape, fetching a fresh (cached) credential per request.
func serveCredEndpoint(ctx context.Context, d CredentialDelivery, f *credFetcher) error {
	ep, err := creddelivery.Open(d.Provider, endpointConfig(d))
	if err != nil {
		return fmt.Errorf("credential %s: %w", f.role.Name, err)
	}
	if d.WellKnown {
		if host, _, e := net.SplitHostPort(d.Addr); e == nil {
			if e := ensureLocalAddr(host); e != nil {
				return fmt.Errorf("credential %s: bind well-known %s: %w", f.role.Name, host, e)
			}
		}
	}
	ln, err := net.Listen("tcp", d.Addr)
	if err != nil {
		return fmt.Errorf("credential %s: listen %s: %w", f.role.Name, d.Addr, err)
	}
	return ep.Serve(ctx, ln, f.get)
}

// endpointConfig builds the non-secret provider config from a resolved delivery
// (nil when there is nothing to pass), keeping the caretaker unaware of which
// knobs a given provider reads.
func endpointConfig(d CredentialDelivery) map[string]string {
	if d.Upstream == "" {
		return nil
	}
	return map[string]string{"upstream": d.Upstream}
}

// serveCredFile writes the credential to the shared-volume path and refreshes it
// on the TTL cadence until ctx is cancelled. The initial write gates readiness
// (the file must exist before the app container starts).
func serveCredFile(ctx context.Context, d CredentialDelivery, f *credFetcher) error {
	write := func() error {
		cred, err := f.get(ctx)
		if err != nil {
			return err
		}
		return creddelivery.WriteFile(d.Path, d.Format, cred)
	}
	if err := write(); err != nil {
		return fmt.Errorf("credential %s: write %s: %w", f.role.Name, d.Path, err)
	}
	ticker := time.NewTicker(f.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := write(); err != nil {
				return fmt.Errorf("credential %s: refresh %s: %w", f.role.Name, d.Path, err)
			}
		}
	}
}

// credentialReady checks cross-process-visible liveness for the readiness probe:
// each file exists, and each endpoint has a bound listener.
func credentialReady(role CredentialRole) error {
	for _, d := range role.Deliver {
		switch d.Kind {
		case "", "endpoint":
			c, err := net.DialTimeout("tcp", d.Addr, 500*time.Millisecond)
			if err != nil {
				return fmt.Errorf("credential %s: endpoint %s not live: %w", role.Name, d.Addr, err)
			}
			c.Close()
		case "file":
			if _, err := os.Stat(d.Path); err != nil {
				return fmt.Errorf("credential %s: file %s not written: %w", role.Name, d.Path, err)
			}
		}
	}
	return nil
}
