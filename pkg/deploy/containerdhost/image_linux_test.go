//go:build linux

package containerdhost

import (
	"context"
	"testing"

	"cornus/pkg/api"
)

func TestNormalizeRef(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Docker-style short names expand to fully qualified docker.io refs.
		{"nginx", "docker.io/library/nginx:latest"},
		{"nginx:1.27-alpine", "docker.io/library/nginx:1.27-alpine"},
		{"user/repo:tag", "docker.io/user/repo:tag"},
		{"user/repo", "docker.io/user/repo:latest"},
		// Already-qualified refs pass through unchanged (the localhost
		// plain-HTTP resolver path depends on the host staying intact).
		{"127.0.0.1:5000/x:y", "127.0.0.1:5000/x:y"},
		{"localhost:5000/web:v1", "localhost:5000/web:v1"},
		{"ghcr.io/a/b:v2", "ghcr.io/a/b:v2"},
		{
			"ghcr.io/a/b@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"ghcr.io/a/b@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		// Qualified but untagged names still get :latest.
		{"ghcr.io/a/b", "ghcr.io/a/b:latest"},
		{"127.0.0.1:5000/x", "127.0.0.1:5000/x:latest"},
	}
	for _, tt := range tests {
		got, err := normalizeRef(tt.in)
		if err != nil {
			t.Errorf("normalizeRef(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("normalizeRef(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeRefRejectsInvalid(t *testing.T) {
	for _, in := range []string{"", "UPPER/Case:tag", "nginx:!bad"} {
		if _, err := normalizeRef(in); err == nil {
			t.Errorf("normalizeRef(%q): expected error, got nil", in)
		}
	}
}

// A docker-style short image name in the spec must reach the containerd client
// as a fully qualified reference — docker.NewResolver cannot parse short names.
func TestApplyPullsNormalizedShortName(t *testing.T) {
	f := newFakeClient()
	b, _ := newTestBackend(t, f)

	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "nginx:1.27-alpine"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.pulled) != 1 || f.pulled[0] != "docker.io/library/nginx:1.27-alpine" {
		t.Fatalf("pulled = %v, want [docker.io/library/nginx:1.27-alpine]", f.pulled)
	}
	// The container record carries the normalized name too (what pullImage
	// handed to CreateContainer), keeping store and records consistent.
	if c := f.containers["cornus-web-0"]; c == nil || c.image != "docker.io/library/nginx:1.27-alpine" {
		t.Fatalf("container image = %+v", f.containers["cornus-web-0"])
	}
}
