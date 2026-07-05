package server

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"cornus/pkg/authtoken"
	"cornus/pkg/config"
)

func TestInstallationSecretFileLifecycle(t *testing.T) {
	t.Setenv(installationSecretEnv, "")
	dir := t.TempDir()
	first, provenance, err := loadInstallationSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	if provenance != installationSecretFromFile || len(first) != installationSecretSize {
		t.Fatalf("generated secret provenance/size = %v/%d", provenance, len(first))
	}
	path := filepath.Join(dir, installationSecretFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("installation key mode = %04o, want 0600", info.Mode().Perm())
	}
	second, provenance, err := loadInstallationSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	if provenance != installationSecretFromFile || !bytes.Equal(first, second) {
		t.Fatal("installation secret was not stable across reload")
	}
}

func TestInstallationSecretRejectsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not available")
	}
	t.Setenv(installationSecretEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, installationSecretFile)
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, installationSecretSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadInstallationSecret(dir); err == nil {
		t.Fatal("group/world-readable installation key was accepted")
	}
}

func TestInstallationSecretEnvironmentWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, installationSecretFile)
	if err := os.WriteFile(path, []byte("deliberately-insecure-and-ignored-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(installationSecretEnv, "shared-installation-secret-from-env")
	secret, provenance, err := loadInstallationSecret(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret) != "shared-installation-secret-from-env" || provenance != installationSecretFromEnv {
		t.Fatalf("secret/provenance = %q/%v", secret, provenance)
	}
}

func TestInternalIssuerMarkerCannotBeForgedBySubject(t *testing.T) {
	clearAuthEnv(t)
	operatorSecret := "operator-secret-0123456789abcdef"
	internalSecret := "internal-secret-0123456789abcdef"
	t.Setenv("CORNUS_JWT_HS256_SECRET", operatorSecret)
	t.Setenv(installationSecretEnv, internalSecret)
	a, err := newAuthenticator(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	var gotInternal bool
	var gotSubject string
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInternal = Internal(r)
		gotSubject = Identity(r)
		w.WriteHeader(http.StatusOK)
	}))
	mint := func(secret, issuer, audience string) string {
		tok, err := authtoken.Issue(authtoken.IssueOptions{
			Subject: "cornus:internal", Scope: authtoken.ScopeRegistryPull,
			Issuer: issuer, Audience: audience, TTL: time.Hour,
			HS256Secret: []byte(secret),
		})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	internal := mint(internalSecret, internalIssuer, internalAudience)
	if rec := doReq(t, h, http.MethodGet, "/v2/repo/manifests/latest", internal); rec.Code != http.StatusOK {
		t.Fatalf("internal credential: code = %d", rec.Code)
	}
	if !gotInternal || gotSubject != "cornus:internal" {
		t.Fatalf("internal request marker/subject = %v/%q", gotInternal, gotSubject)
	}

	gotInternal, gotSubject = false, ""
	forgedSubject := mint(operatorSecret, "", "")
	if rec := doReq(t, h, http.MethodGet, "/v2/repo/manifests/latest", forgedSubject); rec.Code != http.StatusOK {
		t.Fatalf("operator credential: code = %d", rec.Code)
	}
	if gotInternal {
		t.Fatal("operator token forged the internal marker through its subject")
	}
	if gotSubject != "cornus:internal" {
		t.Fatalf("operator subject = %q", gotSubject)
	}
}

func TestInternalRegistryAuthIsScopedToOwnHosts(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CORNUS_AUTH_TOKEN", "operator-token")
	t.Setenv(installationSecretEnv, "internal-secret-0123456789abcdef")
	t.Setenv("CORNUS_ADVERTISE_REGISTRY", "advertised.example:30500")
	a, err := newAuthenticator(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, auth: a}
	creds, err := s.internalRegistryAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"127.0.0.1:5000", "localhost:5000", "advertised.example:30500"} {
		cred, ok := creds[host]
		if !ok || cred.Username != internalUsername || cred.Password == "" {
			t.Fatalf("credential for %s = %+v, present=%v", host, cred, ok)
		}
	}
	if _, ok := creds["docker.io"]; ok {
		t.Fatal("internal credential leaked to a third-party registry host")
	}

	// The minted build credential is registry push scoped: it reaches registry
	// reads and writes, but no Cornus API route.
	token := creds["127.0.0.1:5000"].Password
	h := a.wrap(okHandler())
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := doReq(t, h, method, "/v2/app/manifests/latest", token); rec.Code != http.StatusOK {
			t.Fatalf("internal build token on %s registry: code = %d", method, rec.Code)
		}
	}
	if rec := doReq(t, h, http.MethodPost, "/.cornus/v1/deploy", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("internal build token on API: code = %d, want 401", rec.Code)
	}
}

func TestInternalPullCredentialsAreOwnHostReadOnly(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CORNUS_AUTH_TOKEN", "operator-token")
	t.Setenv(installationSecretEnv, "internal-secret-0123456789abcdef")
	a, err := newAuthenticator(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{HTTPAddr: ":5000"}, auth: a}
	if _, ok, err := s.internalPullCredentials(context.Background(), "docker.io/library/alpine:latest"); err != nil || ok {
		t.Fatalf("third-party pull credential: ok=%v err=%v", ok, err)
	}
	cred, ok, err := s.internalPullCredentials(context.Background(), "127.0.0.1:5000/app:v1")
	if err != nil || !ok || cred.Username != internalUsername || cred.Password == "" {
		t.Fatalf("own pull credential = %+v, ok=%v err=%v", cred, ok, err)
	}
	h := a.wrap(okHandler())
	if rec := doReq(t, h, http.MethodGet, "/v2/app/manifests/v1", cred.Password); rec.Code != http.StatusOK {
		t.Fatalf("pull token on registry GET: code = %d", rec.Code)
	}
	if rec := doReq(t, h, http.MethodPut, "/v2/app/manifests/v1", cred.Password); rec.Code != http.StatusUnauthorized {
		t.Fatalf("pull token on registry PUT: code = %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, http.MethodGet, "/.cornus/v1/deploy", cred.Password); rec.Code != http.StatusUnauthorized {
		t.Fatalf("pull token on API: code = %d, want 401", rec.Code)
	}
}
