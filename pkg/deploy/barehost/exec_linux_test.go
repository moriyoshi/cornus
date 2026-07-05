//go:build linux

package barehost

import (
	"errors"
	"testing"
	"time"

	runc "github.com/containerd/go-runc"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

func TestExecExitCode(t *testing.T) {
	if got := execExitCode(nil); got != 0 {
		t.Errorf("nil err -> %d, want 0", got)
	}
	if got := execExitCode(&runc.ExitError{Status: 7}); got != 7 {
		t.Errorf("ExitError{7} -> %d, want 7", got)
	}
	// Wrapped ExitError is unwrapped via errors.As.
	if got := execExitCode(errors.New("wrap: " + (&runc.ExitError{Status: 3}).Error())); got != 1 {
		t.Errorf("opaque error -> %d, want 1", got)
	}
}

func TestExecProcessSpecInheritsBaseAndOverrides(t *testing.T) {
	base := &specs.Spec{Process: &specs.Process{
		Env:  []string{"PATH=/usr/bin", "FOO=bar"},
		Cwd:  "/app",
		User: specs.User{UID: 5, GID: 6},
	}}
	p, err := execProcessSpec(base, api.ExecConfig{
		Cmd: []string{"sh", "-c", "echo hi"},
		Env: []string{"EXTRA=1"},
		Tty: true,
	})
	if err != nil {
		t.Fatalf("execProcessSpec: %v", err)
	}
	if len(p.Args) != 3 || p.Args[0] != "sh" {
		t.Errorf("Args = %v, want the exec cmd", p.Args)
	}
	if !p.Terminal {
		t.Error("Terminal should be true for a TTY exec")
	}
	if p.Cwd != "/app" {
		t.Errorf("Cwd = %q, want inherited /app", p.Cwd)
	}
	// Inherited env is preserved and the extra appended.
	if !hasOpt(p.Env, "FOO=bar") || !hasOpt(p.Env, "EXTRA=1") {
		t.Errorf("Env = %v, want base + extra", p.Env)
	}
}

func TestExecProcessSpecUserOverride(t *testing.T) {
	p, err := execProcessSpec(&specs.Spec{Process: &specs.Process{}}, api.ExecConfig{
		Cmd:  []string{"id"},
		User: "1000:1000",
	})
	if err != nil {
		t.Fatalf("execProcessSpec: %v", err)
	}
	if p.User.UID != 1000 || p.User.GID != 1000 {
		t.Errorf("User = %+v, want 1000:1000", p.User)
	}
	if p.Cwd != "/" {
		t.Errorf("empty cwd should default to /, got %q", p.Cwd)
	}
	// A non-numeric user is rejected (needs the image passwd db).
	if _, err := execProcessSpec(&specs.Spec{Process: &specs.Process{}}, api.ExecConfig{Cmd: []string{"id"}, User: "root"}); err == nil {
		t.Error("non-numeric exec user should be rejected")
	}
}

func TestExecCreateUnknownDeployment(t *testing.T) {
	b, _ := newTestBackend(t)
	if _, err := b.ExecCreate(t.Context(), "ghost", api.ExecConfig{Cmd: []string{"sh"}}); err == nil {
		t.Error("ExecCreate on unknown deployment: want error")
	}
}

func TestExecRegistryReap(t *testing.T) {
	r := newExecRegistry()
	id, sess := r.add("cornus-web-0", api.ExecConfig{})
	if _, err := r.get(id); err != nil {
		t.Fatalf("get fresh session: %v", err)
	}
	// Mark finished in the past and force a reap via a new add.
	sess.mu.Lock()
	sess.finishedAt = time.Unix(1, 0)
	sess.mu.Unlock()
	old := execRetention
	execRetention = 1 // nanosecond: everything finished is stale
	defer func() { execRetention = old }()
	r.add("cornus-web-1", api.ExecConfig{})
	if _, err := r.get(id); err == nil {
		t.Error("finished session should have been reaped")
	}
}

func TestForwardPortRejectsBadProtoAndNoInstance(t *testing.T) {
	b, _ := newTestBackend(t)
	if err := b.ForwardPort(t.Context(), "web", 80, "sctp", nil); err == nil {
		t.Error("bad proto should be rejected")
	}
	if err := b.ForwardPort(t.Context(), "ghost", 80, "tcp", nil); !errors.Is(err, deploy.ErrNotFound) {
		// unknown deployment wraps ErrNotFound
		t.Errorf("unknown deployment ForwardPort = %v, want ErrNotFound", err)
	}
}

func TestSupportsUDPPortForward(t *testing.T) {
	b, _ := newTestBackend(t)
	if !b.SupportsUDPPortForward() {
		t.Error("bare backend should support UDP port-forward")
	}
}
