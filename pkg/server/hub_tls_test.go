package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cornus/pkg/caretaker"
	"cornus/pkg/config"
	"cornus/pkg/storage"
)

// newServerForTest constructs a bare *Server (no httptest wrapper) so tests can
// assert on New's error and unexported fields.
func newServerForTest(t *testing.T) (*Server, error) {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	return New(config.Config{DataDir: dir}, st)
}

// reachThroughLoopback dials the reach listener until the echo round-trips,
// returning the echoed line ("" on timeout).
func reachThroughLoopback(t *testing.T, addr string) string {
	t.Helper()
	for i := 0; i < 200; i++ {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_, _ = conn.Write([]byte("ping\n"))
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err == nil && line == "ping\n" {
			return line
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

// certPEM PEM-encodes one DER certificate.
func certPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// runCaretakerTLSEcho proves the caretaker's TLS hook end to end against a real
// TLS server: one caretaker registers an echo service and another reaches it,
// both dialing wss:// with the TLS material mkTLS derives from the running
// server's certificate. Without the hook the handshake would fail — the httptest
// certificate chains to no system root.
func runCaretakerTLSEcho(t *testing.T, mkTLS func(t *testing.T, srv *httptest.Server, cfg *caretaker.Config)) {
	t.Helper()
	echo := startEcho(t) // the shared helper from hub_multireplica_test.go

	s, err := newServerForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(s.Handler())
	defer srv.Close()

	probe, err := net.Listen("tcp", "127.0.0.9:0")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	reachPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	registerCfg := caretaker.Config{Hub: &caretaker.HubRole{
		Server:   srv.URL,
		Register: []caretaker.HubService{{Name: "echo", Addr: echo.Addr().String()}},
	}}
	reachCfg := caretaker.Config{Hub: &caretaker.HubRole{
		Server: srv.URL,
		Reach:  []caretaker.HubPeer{{Name: "echo", Listen: "127.0.0.9", Ports: []int{reachPort}}},
	}}
	mkTLS(t, srv, &registerCfg)
	mkTLS(t, srv, &reachCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = caretaker.Run(ctx, registerCfg) }()
	go func() { _ = caretaker.Run(ctx, reachCfg) }()

	addr := net.JoinHostPort("127.0.0.9", strconv.Itoa(reachPort))
	if got := reachThroughLoopback(t, addr); got != "ping\n" {
		t.Fatalf("echo through TLS hub = %q, want %q", got, "ping\n")
	}
}

// TestCaretakerTLSClientConfigDial covers the in-process hook (the `cornus hub`
// CLI path): Config.TLSClientConfig carries the trust roots to the wss:// dial.
func TestCaretakerTLSClientConfigDial(t *testing.T) {
	runCaretakerTLSEcho(t, func(t *testing.T, srv *httptest.Server, cfg *caretaker.Config) {
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		cfg.TLSClientConfig = &tls.Config{RootCAs: pool}
	})
}

// TestCaretakerTLSFilesDial covers the serializable (sidecar-path) hook: the CA
// rides the config JSON as a file path (Config.TLS.CAFile) loaded at Run.
func TestCaretakerTLSFilesDial(t *testing.T) {
	runCaretakerTLSEcho(t, func(t *testing.T, srv *httptest.Server, cfg *caretaker.Config) {
		caFile := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(caFile, certPEM(srv.Certificate()), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.TLS = &caretaker.TLSFiles{CAFile: caFile}
	})
}

// TestHubForwardCAEnv covers CORNUS_HUB_FORWARD_CA's fail-closed parsing: a
// missing or malformed file is a hard startup error, and a valid PEM bundle
// lands as the inter-replica forward dial's TLS config.
func TestHubForwardCAEnv(t *testing.T) {
	t.Run("missing file is a startup error", func(t *testing.T) {
		t.Setenv("CORNUS_HUB_FORWARD_CA", filepath.Join(t.TempDir(), "absent.pem"))
		if _, err := newServerForTest(t); err == nil {
			t.Fatal("New succeeded with a missing CORNUS_HUB_FORWARD_CA file, want an error")
		}
	})

	t.Run("malformed file is a startup error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "garbage.pem")
		if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CORNUS_HUB_FORWARD_CA", path)
		if _, err := newServerForTest(t); err == nil {
			t.Fatal("New succeeded with a malformed CORNUS_HUB_FORWARD_CA file, want an error")
		}
	})

	t.Run("valid bundle configures the forward dial", func(t *testing.T) {
		caCert, _, _ := testCA(t)
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, certPEM(caCert), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CORNUS_HUB_FORWARD_CA", path)
		s, err := newServerForTest(t)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if s.forwardTLS == nil || s.forwardTLS.RootCAs == nil {
			t.Fatal("forwardTLS not configured from CORNUS_HUB_FORWARD_CA")
		}
	})

	t.Run("unset leaves the historical dial", func(t *testing.T) {
		t.Setenv("CORNUS_HUB_FORWARD_CA", "")
		s, err := newServerForTest(t)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if s.forwardTLS != nil {
			t.Fatal("forwardTLS set without CORNUS_HUB_FORWARD_CA, want nil")
		}
	})
}
