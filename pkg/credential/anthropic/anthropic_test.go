package anthropic_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/anthropic"
)

// writeStub writes an executable shell stub and returns its path.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ant-stub.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAnthropicSource(t *testing.T) {
	// Stub `ant`: verify the documented subcommand and print a token.
	stub := writeStub(t, `[ "$1" = auth ] && [ "$2" = print-credentials ] && [ "$3" = --access-token ] || { echo "unexpected args: $*" >&2; exit 2; }; printf 'sk-ant-oat-live\n'`)
	src, err := credential.Open("anthropic", map[string]string{"command": stub})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["oauth_token"] != "sk-ant-oat-live" {
		t.Fatalf("oauth_token = %q (values %v)", got.Values["oauth_token"], got.Values)
	}
}

func TestAnthropicSourceProfile(t *testing.T) {
	// With a profile, --profile <p> is appended.
	stub := writeStub(t, `for a in "$@"; do echo "$a"; done | grep -qx work || { echo "missing profile" >&2; exit 3; }; printf 'tok'`)
	src, _ := credential.Open("anthropic", map[string]string{"command": stub, "profile": "work"})
	if _, err := src.Fetch(context.Background(), nil); err != nil {
		t.Fatalf("profile not passed: %v", err)
	}
}

func TestAnthropicSourceEmpty(t *testing.T) {
	stub := writeStub(t, `exit 0`) // no output
	src, _ := credential.Open("anthropic", map[string]string{"command": stub})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error when no token is printed")
	}
}

func TestAnthropicSourceFailure(t *testing.T) {
	stub := writeStub(t, `echo "not logged in" >&2; exit 1`)
	src, _ := credential.Open("anthropic", map[string]string{"command": stub})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error when ant fails")
	}
}
