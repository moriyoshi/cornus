package claudecode_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/claudecode"
)

func writeStore(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeCodeStore(t *testing.T) {
	// The real store shape: claudeAiOauth.{accessToken,expiresAt}.
	exp := time.Now().Add(2 * time.Hour).UnixMilli()
	store := writeStore(t, `{"claudeAiOauth":{"accessToken":"sk-ant-oat-cc","refreshToken":"r","expiresAt":`+strconv.FormatInt(exp, 10)+`,"scopes":["a"]}}`)

	src, err := credential.Open("claude-code", map[string]string{"file": store})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["oauth_token"] != "sk-ant-oat-cc" {
		t.Fatalf("oauth_token = %q", got.Values["oauth_token"])
	}
	if got.Expiration.UnixMilli() != exp {
		t.Fatalf("expiration = %v, want ms %d", got.Expiration, exp)
	}
}

func TestClaudeCodeMissingToken(t *testing.T) {
	store := writeStore(t, `{"claudeAiOauth":{"refreshToken":"r"}}`)
	src, _ := credential.Open("claude-code", map[string]string{"file": store})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error when accessToken is absent")
	}
}

func TestClaudeCodeMissingFile(t *testing.T) {
	src, _ := credential.Open("claude-code", map[string]string{"file": "/no/such/creds.json"})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error for missing store file")
	}
}

func TestClaudeCodeCustomPath(t *testing.T) {
	store := writeStore(t, `{"token":"raw-tok"}`)
	src, _ := credential.Open("claude-code", map[string]string{"file": store, "path": "token"})
	got, err := src.Fetch(context.Background(), nil)
	if err != nil || got.Values["oauth_token"] != "raw-tok" {
		t.Fatalf("custom path: %v / %v", got.Values, err)
	}
}
