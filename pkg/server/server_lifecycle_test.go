package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/storage"
)

// newTestServerObj builds a *Server (not wrapped in httptest) for exercising
// lifecycle behaviour directly.
func newTestServerObj(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dir}, st)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNewRejectsMalformedPolicy proves a malformed hub policy env is a hard
// startup error (fail closed), not a silent allow-all.
func TestNewRejectsMalformedPolicy(t *testing.T) {
	for _, envName := range []string{"CORNUS_HUB_POLICY", "CORNUS_HUB_REGISTER_POLICY"} {
		t.Run(envName, func(t *testing.T) {
			t.Setenv(envName, "{not valid json")
			dir := t.TempDir()
			st, err := storage.Open(context.Background(), dir, dir+"/uploads")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := New(config.Config{DataDir: dir}, st); err == nil {
				t.Fatalf("New with malformed %s should fail, got nil error", envName)
			}
		})
	}
}

// TestNewAcceptsValidPolicy confirms valid JSON (and absence) still succeed.
func TestNewAcceptsValidPolicy(t *testing.T) {
	t.Setenv("CORNUS_HUB_POLICY", `{"web":["db"]}`)
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(config.Config{DataDir: dir}, st); err != nil {
		t.Fatalf("New with valid policy: %v", err)
	}
}

// TestReadyzGate confirms /readyz reports 503 until the readiness flag is set,
// and 200 once it is. /healthz stays 200 regardless (pure liveness).
func TestReadyzGate(t *testing.T) {
	s := newTestServerObj(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Not serving via Run, so readiness is still false.
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz before ready = %d, want 503", resp.StatusCode)
	}

	// Liveness is independent of readiness.
	resp, _ = http.Get(ts.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}

	s.ready.Store(true)
	resp, _ = http.Get(ts.URL + "/readyz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz after ready = %d, want 200", resp.StatusCode)
	}
}

// closeRecordingBackend embeds fakeBackend and records that Close was called.
type closeRecordingBackend struct {
	*fakeBackend
	closed atomic.Bool
}

func (b *closeRecordingBackend) Close() error { b.closed.Store(true); return nil }

// TestCloseResourcesClosesBackend verifies closeResources releases a constructed
// backend and clears the handle (so a leaked BuildKit/backend lock is freed).
func TestCloseResourcesClosesBackend(t *testing.T) {
	s := newTestServerObj(t)
	b := &closeRecordingBackend{fakeBackend: &fakeBackend{}}
	s.backend = b
	s.closeResources()
	if !b.closed.Load() {
		t.Fatal("closeResources did not Close the backend")
	}
	if s.backend != nil {
		t.Fatal("closeResources did not clear the backend handle")
	}
	// Idempotent: a second call is a no-op.
	s.closeResources()
}

// TestBuildConcurrency covers the env override and default.
func TestBuildConcurrency(t *testing.T) {
	t.Setenv("CORNUS_BUILD_CONCURRENCY", "3")
	if got := buildConcurrency(); got != 3 {
		t.Fatalf("buildConcurrency with env=3 = %d", got)
	}
	t.Setenv("CORNUS_BUILD_CONCURRENCY", "bogus")
	if got := buildConcurrency(); got < 1 {
		t.Fatalf("buildConcurrency fallback = %d, want >= 1", got)
	}
}

// TestMaxBuildContextBytes covers the env override and default.
func TestMaxBuildContextBytes(t *testing.T) {
	if got := maxBuildContextBytes(); got != defaultMaxBuildContextBytes {
		t.Fatalf("default = %d, want %d", got, defaultMaxBuildContextBytes)
	}
	t.Setenv("CORNUS_MAX_BUILD_CONTEXT_BYTES", "1024")
	if got := maxBuildContextBytes(); got != 1024 {
		t.Fatalf("override = %d, want 1024", got)
	}
}

// serializeBackend records overlapping Apply calls for the same name so a test
// can assert the server serialises them.
type serializeBackend struct {
	*fakeBackend
	mu       sync.Mutex
	inFlight map[string]int
	maxSeen  map[string]int
}

func (b *serializeBackend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	b.mu.Lock()
	b.inFlight[spec.Name]++
	if b.inFlight[spec.Name] > b.maxSeen[spec.Name] {
		b.maxSeen[spec.Name] = b.inFlight[spec.Name]
	}
	b.mu.Unlock()

	time.Sleep(20 * time.Millisecond) // widen the race window

	b.mu.Lock()
	b.inFlight[spec.Name]--
	b.mu.Unlock()
	return api.DeployStatus{Name: spec.Name}, nil
}

// TestDeployApplySerializesSameName fires concurrent applies of one name and
// asserts they never overlap in the backend.
func TestDeployApplySerializesSameName(t *testing.T) {
	sb := &serializeBackend{fakeBackend: &fakeBackend{}, inFlight: map[string]int{}, maxSeen: map[string]int{}}
	srv := newTestServer(t, sb)
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _ := json.Marshal(api.DeploySpec{Name: "web", Image: "img"})
			resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.maxSeen["web"] > 1 {
		t.Fatalf("same-name applies overlapped: max in-flight = %d, want 1", sb.maxSeen["web"])
	}
}

// TestRunServesTLSAndClosesOnShutdown runs the real Run loop over HTTPS, checks
// readiness flips true, then cancels and confirms the backend was Closed.
func TestRunServesTLSAndClosesOnShutdown(t *testing.T) {
	certFile, keyFile := writeSelfSignedCert(t)

	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	s, err := New(config.Config{DataDir: dir, HTTPAddr: addr}, st)
	if err != nil {
		t.Fatal(err)
	}
	s.TLSCertFile = certFile
	s.TLSKeyFile = keyFile
	b := &closeRecordingBackend{fakeBackend: &fakeBackend{}}
	s.backend = b // pre-set so closeResources on shutdown has something to close

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx) }()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	// Wait for readiness / the socket to accept TLS.
	var ready bool
	for i := 0; i < 100; i++ {
		resp, err := client.Get("https://" + addr + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && s.ready.Load() {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("server never became ready over TLS")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !b.closed.Load() {
		t.Fatal("backend was not Closed on shutdown")
	}
	if s.ready.Load() {
		t.Fatal("readiness should be false after shutdown")
	}
}

// freeAddr returns a currently-free 127.0.0.1 TCP address as host:port.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// writeSelfSignedCert generates an ephemeral self-signed cert/key pair and
// writes them to temp files, returning their paths.
func writeSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
