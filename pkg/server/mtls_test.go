package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/hub"
	"cornus/pkg/storage"
)

// testCA returns a self-signed CA cert, its key, and a pool trusting it.
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return caCert, key, pool
}

// clientCert issues a leaf client certificate with the given CommonName (the
// spoke identity), signed by the test CA.
func clientCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// newTestServerTLS starts the cornus handler over TLS requiring a client cert
// verified against caPool.
func newTestServerTLS(t *testing.T, backend deploy.Backend, caPool *x509.CertPool) *httptest.Server {
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
	s.newBackend = func() (deploy.Backend, error) { return backend, nil }
	srv := httptest.NewUnstartedServer(s.Handler())
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: caPool}
	srv.StartTLS()
	return srv
}

// TestHubMTLSIdentityIsAuthoritative confirms that under mTLS the verified client-
// cert CommonName is the identity used for policy, overriding whatever the spoke
// declares on the control stream. Policy allows only "web" → "echo": a spoke whose
// cert is "web" reaches echo even while DECLARING "denied"; a spoke whose cert is
// "intruder" is refused even while declaring "web".
func TestHubMTLSIdentityIsAuthoritative(t *testing.T) {
	t.Setenv("CORNUS_HUB_POLICY", `{"web":["echo"]}`)

	caCert, caKey, caPool := testCA(t)

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()

	srv := newTestServerTLS(t, &fakeBackend{}, caPool)
	defer srv.Close()
	wssURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/.cornus/v1/caretaker/attach"

	serverPool := x509.NewCertPool()
	serverPool.AddCert(srv.Certificate())
	clientTLS := func(cn string) *tls.Config {
		return &tls.Config{Certificates: []tls.Certificate{clientCert(t, caCert, caKey, cn)}, RootCAs: serverPool}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Destination (cert "dest") registers echo dial-direct.
	dst, err := hub.DialTLS(ctx, wssURL, clientTLS("dest"))
	if err != nil {
		t.Fatalf("dest dial: %v", err)
	}
	defer dst.Close()
	dreg, err := hub.Register(dst, hub.Registration{Services: []hub.Service{{Name: "echo", Addr: echo.Addr().String()}}})
	if err != nil {
		t.Fatalf("dest register: %v", err)
	}
	defer dreg.Close()

	reach := func(certCN, declared string) string {
		sess, err := hub.DialTLS(ctx, wssURL, clientTLS(certCN))
		if err != nil {
			t.Fatalf("%s dial: %v", certCN, err)
		}
		defer sess.Close()
		// Declare a (possibly lying) identity on the control stream.
		creg, err := hub.Register(sess, hub.Registration{Identity: declared})
		if err != nil {
			t.Fatalf("%s register: %v", certCN, err)
		}
		defer creg.Close()

		var got string
		for i := 0; i < 100; i++ {
			stream, err := hub.OpenTo(sess, "echo")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, _ = stream.Write([]byte("ping\n"))
			_ = stream.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
			buf := make([]byte, 5)
			n, _ := io.ReadFull(stream, buf)
			stream.Close()
			if n == 5 {
				got = string(buf)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		return got
	}

	// Cert "web" wins over the declared "denied" → allowed.
	if got := reach("web", "denied"); got != "ping\n" {
		t.Errorf("cert=web declared=denied: got %q, want echo (cert identity should win)", got)
	}
	// Cert "intruder" wins over the declared "web" → denied.
	if got := reach("intruder", "web"); got != "" {
		t.Errorf("cert=intruder declared=web: got %q, want no echo (cert identity should win)", got)
	}
}
