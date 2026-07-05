package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"
)

// certReloader serves the server certificate through tls.Config.GetCertificate,
// re-reading the cert/key files when their modification time changes. This is what
// lets an external rotator (cert-manager, Vault, SPIFFE, ...) renew the cert in
// place without a cornus restart: cert-manager rewrites the mounted Secret, the
// file mtime advances, and the next handshake picks up the new pair. On a reload
// error (e.g. a half-written file mid-rotation) the last good certificate is kept.
type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cached  *tls.Certificate
	modTime time.Time
}

// load reads and caches the key pair once, so a bad path fails fast at startup.
func (r *certReloader) load() error {
	_, err := r.GetCertificate(nil)
	return err
}

// GetCertificate satisfies tls.Config.GetCertificate. It stats the cert file (cheap
// per handshake) and reloads only when the mtime advanced or nothing is cached yet.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fi, err := os.Stat(r.certFile)
	if err != nil {
		if r.cached != nil {
			return r.cached, nil
		}
		return nil, err
	}
	if r.cached != nil && !fi.ModTime().After(r.modTime) {
		return r.cached, nil
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cached != nil {
			return r.cached, nil // keep serving the last good pair through a transient error
		}
		return nil, err
	}
	r.cached = &cert
	r.modTime = fi.ModTime()
	return r.cached, nil
}

// caReloader serves the client-CA pool for mTLS the same way, so a rotated CA
// bundle is picked up without a restart.
type caReloader struct {
	caFile string

	mu      sync.Mutex
	cached  *x509.CertPool
	modTime time.Time
}

// load reads and caches the CA pool once (fail fast on a bad path at startup).
func (r *caReloader) load() error {
	_, err := r.pool()
	return err
}

// pool returns the current client-CA pool, reloading when the file mtime advanced.
func (r *caReloader) pool() (*x509.CertPool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fi, err := os.Stat(r.caFile)
	if err != nil {
		if r.cached != nil {
			return r.cached, nil
		}
		return nil, err
	}
	if r.cached != nil && !fi.ModTime().After(r.modTime) {
		return r.cached, nil
	}
	pem, err := os.ReadFile(r.caFile)
	if err != nil {
		if r.cached != nil {
			return r.cached, nil
		}
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		if r.cached != nil {
			return r.cached, nil
		}
		return nil, fmt.Errorf("parsing TLS client CA %q: no certificates found", r.caFile)
	}
	r.cached = pool
	r.modTime = fi.ModTime()
	return r.cached, nil
}

// tlsConfig builds the server TLS config from reloading cert (and, when a client CA
// is set, a reloading mTLS pool). It loads both eagerly so a misconfiguration is a
// hard startup error, then serves them through the reload callbacks.
func tlsConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cr := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := cr.load(); err != nil {
		return nil, fmt.Errorf("loading TLS cert %q: %w", certFile, err)
	}
	cfg := &tls.Config{GetCertificate: cr.GetCertificate}

	if clientCAFile != "" {
		ca := &caReloader{caFile: clientCAFile}
		if err := ca.load(); err != nil {
			return nil, fmt.Errorf("loading TLS client CA %q: %w", clientCAFile, err)
		}
		cfg.ClientAuth = tls.VerifyClientCertIfGiven
		// GetConfigForClient supplies the freshly-loaded pool per handshake so a
		// rotated CA takes effect without a restart.
		cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
			pool, err := ca.pool()
			if err != nil {
				return nil, err
			}
			c := cfg.Clone()
			c.GetConfigForClient = nil // avoid recursion
			c.ClientCAs = pool
			return c, nil
		}
	}
	return cfg, nil
}
