package main

import "testing"

func TestLocalBuildRegistryAuthIsTargetScoped(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "client-token")
	creds, err := localBuildRegistryAuth(&CLI{}, "registry.example:5443/team/app:v1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("credentials = %+v, want one target host", creds)
	}
	cred, ok := creds["registry.example:5443"]
	if !ok || cred.Username != "token" || cred.Password != "client-token" {
		t.Fatalf("target credential = %+v, present=%v", cred, ok)
	}
	if _, ok := creds["docker.io"]; ok {
		t.Fatal("target credential leaked to a base-image registry")
	}
}

func TestLocalBuildRegistryAuthOffOrAbsent(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "client-token")
	if creds, err := localBuildRegistryAuth(&CLI{}, "not a valid reference !!!", false); err != nil || creds != nil {
		t.Fatalf("no-push credentials/error = %+v/%v", creds, err)
	}
	t.Setenv("CORNUS_TOKEN", "")
	if creds, err := localBuildRegistryAuth(&CLI{}, "registry.example/app:v1", true); err != nil || creds != nil {
		t.Fatalf("tokenless credentials/error = %+v/%v", creds, err)
	}
	if _, err := localBuildRegistryAuth(&CLI{}, "not a valid reference !!!", true); err == nil {
		t.Fatal("invalid pushed target was accepted")
	}
}
