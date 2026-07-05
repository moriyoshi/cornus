package clientconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSigned writes a throwaway self-signed cert + key PEM pair into dir and
// returns their paths, for exercising the TLS loading branches.
func writeSelfSigned(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cornus-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestTLSConfig(t *testing.T) {
	// Nil / empty settings mean "use system defaults" — no config built.
	if cfg, err := (*TLS)(nil).Config(); err != nil || cfg != nil {
		t.Errorf("nil TLS.Config() = %v, %v; want nil, nil", cfg, err)
	}
	if cfg, err := (&TLS{}).Config(); err != nil || cfg != nil {
		t.Errorf("empty TLS.Config() = %v, %v; want nil, nil", cfg, err)
	}

	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir)

	// CA + mTLS client pair loads into a usable config.
	cfg, err := (&TLS{CACert: cert, ClientCert: cert, ClientKey: key}).Config()
	if err != nil {
		t.Fatalf("full TLS.Config(): %v", err)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs not set from ca-cert")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("Certificates = %d, want 1", len(cfg.Certificates))
	}

	// insecure-skip-verify alone is enough to build a config.
	if cfg, err := (&TLS{InsecureSkipVerify: true}).Config(); err != nil || cfg == nil || !cfg.InsecureSkipVerify {
		t.Errorf("insecure TLS.Config() = %v, %v", cfg, err)
	}

	// server-name alone is enough to build a config and sets SNI (needed for the
	// SSH-tunnel case where the dial host is 127.0.0.1 but the cert names the server).
	if cfg, err := (&TLS{ServerName: "cornus.example.com"}).Config(); err != nil || cfg == nil || cfg.ServerName != "cornus.example.com" {
		t.Errorf("server-name TLS.Config() = %v, %v", cfg, err)
	}

	// A client cert without its key (or vice versa) is an error.
	if _, err := (&TLS{ClientCert: cert}).Config(); err == nil {
		t.Error("client-cert without client-key = nil error, want error")
	}

	// A bad CA file is an error.
	if _, err := (&TLS{CACert: filepath.Join(dir, "missing.pem")}).Config(); err == nil {
		t.Error("missing ca-cert = nil error, want error")
	}
}

func TestDefaultPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/root")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join("/xdg/root", "cornus", "config.yaml")
	if got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathFallsBackToUserConfigDir(t *testing.T) {
	// With XDG unset, the path comes from os.UserConfigDir(); assert it lands
	// under a cornus/config.yaml suffix regardless of the platform-native root.
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if filepath.Base(got) != "config.yaml" || filepath.Base(filepath.Dir(got)) != "cornus" {
		t.Errorf("DefaultPath() = %q, want a .../cornus/config.yaml path", got)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if f == nil || f.Contexts == nil || len(f.Contexts) != 0 || f.CurrentContext != "" {
		t.Errorf("Load missing = %+v, want empty non-nil File", f)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	in := &File{
		CurrentContext: "prod",
		Contexts: map[string]*Context{
			"prod": {
				Server: "https://cornus.example.com",
				Token:  "tok-123",
				TLS:    &TLS{CACert: "/ca.pem", ClientCert: "/c.pem", ClientKey: "/k.pem"},
			},
			"dev": {
				PortForward: &PortForward{Namespace: "cornus", Service: "cornus", RemotePort: 5000},
			},
		},
	}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Secrets on disk must not be world-readable.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perms = %o, want 600", perm)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.CurrentContext != "prod" {
		t.Errorf("CurrentContext = %q, want prod", out.CurrentContext)
	}
	if c := out.Contexts["prod"]; c == nil || c.Server != "https://cornus.example.com" || c.Token != "tok-123" || c.TLS == nil || c.TLS.CACert != "/ca.pem" {
		t.Errorf("prod context round-trip mismatch: %+v", c)
	}
	if c := out.Contexts["dev"]; c == nil || c.PortForward == nil || c.PortForward.RemotePort != 5000 {
		t.Errorf("dev context round-trip mismatch: %+v", c)
	}
}

// TestSaveTightensPreExistingPerms verifies Save enforces 0600 even when the
// target file already exists with looser permissions. os.WriteFile only applies
// the mode when it creates the file, so a config left 0644 by an external tool
// would otherwise keep its world-readable mode and leak the stored token.
func TestSaveTightensPreExistingPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Pre-create the file world-readable, as an editor or older code path might.
	if err := os.WriteFile(path, []byte("current-context: stale\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	// Chmod explicitly so the seed mode is 0644 regardless of the process umask.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod seed: %v", err)
	}

	in := &File{Contexts: map[string]*Context{"a": {Server: "http://a", Token: "secret"}}}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perms after Save = %o, want 600", perm)
	}
}

func TestConduitRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := &File{Contexts: map[string]*Context{
		"a": {Server: "http://a", Conduit: &Conduit{Mode: "socks5", Socks5: &Socks5{
			Listen:            "127.0.0.1:9050",
			ServiceHostSuffix: ".svc.internal",
			Resolve:           []ResolveRule{{Pattern: `^(.*):5000$`, Replace: `\1:10000`}},
		}}},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e := out.Contexts["a"].Conduit
	if e == nil || e.Mode != "socks5" || e.Socks5 == nil {
		t.Fatalf("conduit round-trip mismatch: %+v", e)
	}
	if e.Socks5.Listen != "127.0.0.1:9050" || e.Socks5.ServiceHostSuffix != ".svc.internal" {
		t.Errorf("socks5 fields mismatch: %+v", e.Socks5)
	}
	if len(e.Socks5.Resolve) != 1 || e.Socks5.Resolve[0].Pattern != `^(.*):5000$` || e.Socks5.Resolve[0].Replace != `\1:10000` {
		t.Errorf("resolve rules mismatch: %+v", e.Socks5.Resolve)
	}
}

func TestResolve(t *testing.T) {
	f := &File{
		CurrentContext: "prod",
		Contexts: map[string]*Context{
			"prod": {Server: "https://prod"},
			"dev":  {Server: "http://dev"},
		},
	}

	// Explicit name wins.
	name, ctx, err := f.Resolve("dev")
	if err != nil || name != "dev" || ctx.Server != "http://dev" {
		t.Errorf("Resolve(dev) = %q, %+v, %v", name, ctx, err)
	}

	// Empty name falls back to CurrentContext.
	name, ctx, err = f.Resolve("")
	if err != nil || name != "prod" || ctx.Server != "https://prod" {
		t.Errorf("Resolve(\"\") = %q, %+v, %v", name, ctx, err)
	}

	// Unknown name is an error.
	if _, _, err := f.Resolve("nope"); err == nil {
		t.Error("Resolve(nope) = nil error, want not-found error")
	}

	// No name and no current context is the legitimate "unset" state.
	empty := &File{Contexts: map[string]*Context{}}
	name, ctx, err = empty.Resolve("")
	if err != nil || name != "" || ctx != nil {
		t.Errorf("Resolve on empty = %q, %+v, %v; want \"\", nil, nil", name, ctx, err)
	}
}
