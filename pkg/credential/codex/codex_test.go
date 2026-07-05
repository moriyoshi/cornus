package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/codex"
)

func writeStore(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexApiKeyMode(t *testing.T) {
	// apikey mode is preferred: a real key valid against api.openai.com.
	store := writeStore(t, `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-proj-live","tokens":null}`)
	src, err := credential.Open("codex", map[string]string{"file": store})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["api_key"] != "sk-proj-live" {
		t.Fatalf("api_key = %q", got.Values["api_key"])
	}
}

func TestCodexOAuthFallback(t *testing.T) {
	// No API key -> fall back to the ChatGPT OAuth access token.
	store := writeStore(t, `{"auth_mode":"chatgpt","tokens":{"access_token":"oauth-acc","refresh_token":"r","account_id":"a"},"last_refresh":"2026-07-10T00:00:00Z"}`)
	src, _ := credential.Open("codex", map[string]string{"file": store})
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["api_key"] != "oauth-acc" {
		t.Fatalf("api_key = %q", got.Values["api_key"])
	}
}

func TestCodexExplicitPath(t *testing.T) {
	store := writeStore(t, `{"tokens":{"id_token":"idtok"}}`)
	src, _ := credential.Open("codex", map[string]string{"file": store, "path": "tokens.id_token"})
	got, err := src.Fetch(context.Background(), nil)
	if err != nil || got.Values["api_key"] != "idtok" {
		t.Fatalf("explicit path: %v / %v", got.Values, err)
	}
}

func TestCodexEmpty(t *testing.T) {
	store := writeStore(t, `{"auth_mode":"chatgpt","tokens":null}`)
	src, _ := credential.Open("codex", map[string]string{"file": store})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error when no token present")
	}
}

func TestCodexMissingFile(t *testing.T) {
	src, _ := credential.Open("codex", map[string]string{"file": "/no/such/auth.json"})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error for missing store")
	}
}
