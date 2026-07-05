package credential_test

import (
	"context"
	"testing"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/exec"
	_ "cornus/pkg/credential/static"
)

func TestStaticValues(t *testing.T) {
	src, err := credential.Open("static", map[string]string{"AccessKeyId": "AKIA", "SecretAccessKey": "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["AccessKeyId"] != "AKIA" || got.Values["SecretAccessKey"] != "s3cr3t" {
		t.Fatalf("static values = %v", got.Values)
	}
}

func TestStaticValuesJSON(t *testing.T) {
	src, err := credential.Open("static", map[string]string{"values": `{"token":"abc"}`})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := src.Fetch(context.Background(), nil)
	if got.Values["token"] != "abc" {
		t.Fatalf("values = %v", got.Values)
	}
	// Fetch must return an independent copy (no shared mutation).
	got.Values["token"] = "mutated"
	again, _ := src.Fetch(context.Background(), nil)
	if again.Values["token"] != "abc" {
		t.Fatalf("Fetch returned a shared map: %v", again.Values)
	}
}

func TestStaticEmpty(t *testing.T) {
	if _, err := credential.Open("static", map[string]string{}); err == nil {
		t.Fatal("expected error for empty static config")
	}
}

func TestExecFlatJSON(t *testing.T) {
	src, err := credential.Open("exec", map[string]string{"command": `printf '{"k":"v"}'`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["k"] != "v" {
		t.Fatalf("exec values = %v", got.Values)
	}
}

func TestExecNeutralObject(t *testing.T) {
	src, _ := credential.Open("exec", map[string]string{"command": `printf '{"values":{"a":"1"},"expiration":"2030-01-01T00:00:00Z"}'`})
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["a"] != "1" || got.Expiration.IsZero() {
		t.Fatalf("exec neutral = %+v", got)
	}
}

func TestExecFailure(t *testing.T) {
	src, _ := credential.Open("exec", map[string]string{"command": "exit 3"})
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected error from failing command")
	}
}

func TestUnknownBackend(t *testing.T) {
	if _, err := credential.Open("nope", nil); err == nil {
		t.Fatal("expected unknown-backend error")
	}
}
