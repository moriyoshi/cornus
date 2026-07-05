package exec_test

import (
	"context"
	"testing"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/exec"
)

func TestExecRawMode(t *testing.T) {
	// A bare-token printer (e.g. `ant auth print-credentials --access-token`) works
	// with raw mode + a key, no jq wrapper.
	src, err := credential.Open("exec", map[string]string{
		"command": "printf 'sk-ant-oat-abc\\n'",
		"raw":     "true",
		"key":     "oauth_token",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["oauth_token"] != "sk-ant-oat-abc" {
		t.Fatalf("raw value = %q (values %v)", got.Values["oauth_token"], got.Values)
	}
}

func TestExecRawDefaultKey(t *testing.T) {
	src, _ := credential.Open("exec", map[string]string{"command": "printf 'plainkey'", "raw": "1"})
	got, _ := src.Fetch(context.Background(), nil)
	if got.Values["value"] != "plainkey" {
		t.Fatalf("values = %v", got.Values)
	}
}

func TestExecRawEmpty(t *testing.T) {
	src, _ := credential.Open("exec", map[string]string{"command": "true", "raw": "true"})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty raw output")
	}
}
