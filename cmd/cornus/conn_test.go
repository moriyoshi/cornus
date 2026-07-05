package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/clientconfig"
)

// writeConfig saves f at a temp path and returns a *CLI pointed at it.
func writeConfig(t *testing.T, f *clientconfig.File) *CLI {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := clientconfig.Save(path, f); err != nil {
		t.Fatal(err)
	}
	return &CLI{Config: path}
}

func TestResolveConnEndpointPrecedence(t *testing.T) {
	t.Setenv("CORNUS_TOKEN", "")

	// No config file at all: nothing resolves, and requireConn errors.
	cli := &CLI{Config: filepath.Join(t.TempDir(), "none.yaml")}
	cn, err := cli.resolveConn("")
	if err != nil || cn.Endpoint != "" {
		t.Fatalf("resolveConn(no config) = %q, %v; want empty endpoint", cn.Endpoint, err)
	}
	if _, err := cli.requireConn(""); err == nil {
		t.Error("requireConn with no server = nil error, want error")
	}

	cli = writeConfig(t, &clientconfig.File{
		CurrentContext: "prod",
		Contexts: map[string]*clientconfig.Context{
			"prod": {Server: "https://prod.example.com"},
			"pfonly": {PortForward: &clientconfig.PortForward{
				Namespace: "cornus", Service: "cornus", RemotePort: 5000}},
		},
	})

	// Current context supplies the endpoint.
	cn, err = cli.resolveConn("")
	if err != nil || cn.Endpoint != "https://prod.example.com" {
		t.Fatalf("resolveConn(current) = %q, %v", cn.Endpoint, err)
	}

	// Explicit --server wins over the context server.
	cn, err = cli.resolveConn("http://explicit:5000")
	if err != nil || cn.Endpoint != "http://explicit:5000" {
		t.Fatalf("resolveConn(explicit) = %q, %v", cn.Endpoint, err)
	}

	// An unknown --context is an error.
	cli.Context = "missing"
	if _, err := cli.resolveConn(""); err == nil {
		t.Error("resolveConn(unknown context) = nil error, want error")
	}

	// A port-forward-only profile triggers svcforward; with no usable kubeconfig it
	// fails fast rather than resolving an endpoint. (KUBECONFIG points at a missing
	// file so the test never touches a real cluster.)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "nonexistent-kubeconfig"))
	cli.Context = "pfonly"
	if _, err := cli.resolveConn(""); err == nil {
		t.Error("resolveConn(port-forward profile, no kubeconfig) = nil error, want error")
	}
}

func TestResolveConnAppliesProfileCredentials(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]api.DeployStatus{})
	}))
	defer srv.Close()

	cli := writeConfig(t, &clientconfig.File{
		CurrentContext: "prod",
		Contexts:       map[string]*clientconfig.Context{"prod": {Server: srv.URL, Token: "profile-tok"}},
	})

	// The profile token rides the request when CORNUS_TOKEN is unset.
	t.Setenv("CORNUS_TOKEN", "")
	cn, err := cli.resolveConn("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cn.Client().List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer profile-tok" {
		t.Fatalf("Authorization = %q, want Bearer profile-tok", gotAuth)
	}

	// An explicit CORNUS_TOKEN env overrides the profile token.
	t.Setenv("CORNUS_TOKEN", "env-tok")
	cn, err = cli.resolveConn("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cn.Client().List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer env-tok" {
		t.Fatalf("Authorization = %q, want Bearer env-tok", gotAuth)
	}
}
