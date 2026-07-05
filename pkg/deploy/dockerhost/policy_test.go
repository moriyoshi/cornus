package dockerhost

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// Policy mechanics (privileged gate, bind allowlist, env parsing) are covered
// in pkg/deploy/hostpolicy. This file keeps the backend-integration guarantee.

// TestApplyEnforcesPolicy proves the check runs inside Apply, before any Docker
// call, so a denied spec never reaches the daemon.
func TestApplyEnforcesPolicy(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	b.policy = Policy{} // default-deny, overriding the permissive test default

	_, err := b.Apply(context.Background(), api.DeploySpec{
		Name:   "web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/", Target: "/host"}},
	})
	if err == nil {
		t.Fatal("Apply should reject a root bind under default-deny")
	}
	if !strings.Contains(err.Error(), "not permitted by policy") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.pulled) != 0 || len(f.created) != 0 {
		t.Fatalf("denied spec must not touch the daemon: pulled=%v created=%v", f.pulled, f.created)
	}
}
