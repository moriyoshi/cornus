package env_test

import (
	"context"
	"testing"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/env"
)

func TestEnvSource(t *testing.T) {
	t.Setenv("MY_API_KEY", "sk-secret")
	src, err := credential.Open("env", map[string]string{"var": "MY_API_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["value"] != "sk-secret" {
		t.Fatalf("values = %v", got.Values)
	}
}

func TestEnvSourceKey(t *testing.T) {
	t.Setenv("TOK", "sk-ant-oat-x")
	src, _ := credential.Open("env", map[string]string{"var": "TOK", "key": "oauth_token"})
	got, _ := src.Fetch(context.Background(), nil)
	if got.Values["oauth_token"] != "sk-ant-oat-x" {
		t.Fatalf("values = %v", got.Values)
	}
}

func TestEnvSourceUnset(t *testing.T) {
	src, _ := credential.Open("env", map[string]string{"var": "DEFINITELY_UNSET_VAR_XYZ"})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error for unset var")
	}
}

func TestEnvSourceMissingVar(t *testing.T) {
	if _, err := credential.Open("env", map[string]string{}); err == nil {
		t.Fatal("expected error for missing var config")
	}
}
