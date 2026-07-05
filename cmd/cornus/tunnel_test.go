package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAuthToken(t *testing.T) {
	t.Setenv("NGROK_AUTHTOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, err := resolveAuthToken("", path)
	if err != nil {
		t.Fatalf("resolveAuthToken: %v", err)
	}
	if tok != "s3cr3t" {
		t.Errorf("token = %q, want %q (trailing newline trimmed)", tok, "s3cr3t")
	}

	tok, err = resolveAuthToken("direct", "")
	if err != nil {
		t.Fatalf("resolveAuthToken: %v", err)
	}
	if tok != "direct" {
		t.Errorf("token = %q, want %q", tok, "direct")
	}

	tok, err = resolveAuthToken("", "")
	if err != nil {
		t.Fatalf("resolveAuthToken: %v", err)
	}
	if tok != "" {
		t.Errorf("token = %q, want empty", tok)
	}
}

func TestResolveAuthTokenErrors(t *testing.T) {
	t.Setenv("NGROK_AUTHTOKEN", "")
	if _, err := resolveAuthToken("direct", "/some/path"); err == nil {
		t.Error("--authtoken and --authtoken-file together: expected an error")
	}
	if _, err := resolveAuthToken("", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("nonexistent --authtoken-file: expected an error")
	}
}

// TestResolveAuthTokenLegacyAlias verifies the NGROK_AUTHTOKEN env var still
// works as a fallback when neither --authtoken nor --authtoken-file is set
// (kong's own binding on --authtoken now reads CORNUS_TUNNEL_AUTHTOKEN
// instead), and that an explicit flag value always wins over it.
func TestResolveAuthTokenLegacyAlias(t *testing.T) {
	t.Setenv("NGROK_AUTHTOKEN", "legacy-tok")

	tok, err := resolveAuthToken("", "")
	if err != nil {
		t.Fatalf("resolveAuthToken: %v", err)
	}
	if tok != "legacy-tok" {
		t.Errorf("token = %q, want the NGROK_AUTHTOKEN fallback %q", tok, "legacy-tok")
	}

	tok, err = resolveAuthToken("explicit", "")
	if err != nil {
		t.Fatalf("resolveAuthToken: %v", err)
	}
	if tok != "explicit" {
		t.Errorf("token = %q, want --authtoken to win over the env fallback", tok)
	}
}
