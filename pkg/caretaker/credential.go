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
	f := &credFetcher{open: open, role: role, ttl: credential.ParseTTL(role.TTL)}
	g, gctx := errgroup.WithContext(ctx)
	for _, d := range role.Deliveries {
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
	f.expiry = credential.Expiry(now, cred.Expiration, f.ttl)
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

// credExpiry and parseTTL moved to pkg/credential (Expiry / ParseTTL) so the
// SERVER-side file refresh shares this arithmetic rather than reimplementing it.
// Two answers to "when is this credential stale" would only ever surface as one
// path holding a secret the other had already replaced.

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
		if err := creddelivery.WriteFile(d.Path, d.Format, cred); err != nil {
			return err
		}
		return chownCredFile(d)
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
	for _, d := range role.Deliveries {
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

// chownCredFile gives the written file the ids the APP container reads it as.
//
// This caretaker and the app are different containers with different users. The
// caretaker runs as its image's user — root — and the app runs as spec.User via
// securityContext.RunAsUser; they share only the emptyDir the file lands in.
// creddelivery.WriteFile writes mode 0600, so without this a non-root app cannot
// read its own credential, and the only symptom is the application's own
// "permission denied" with nothing pointing back here.
//
// The host backends have done this since they gained file delivery
// (deploy.CredentialFileOwner); the kubernetes path never did, which is why a
// `user:` deployment with a file credential did not work there.
//
// A zero UID means the server had nothing to say — the spec named no user, or
// named one only the image can resolve — and the file is left as written, which
// is right for a root app.
func chownCredFile(d CredentialDelivery) error {
	if d.UID == 0 && d.GID == 0 {
		return nil
	}
	if err := os.Chown(d.Path, d.UID, d.GID); err != nil {
		return fmt.Errorf("credential file %s: set ownership to %d:%d: %w", d.Path, d.UID, d.GID, err)
	}
	return nil
}
