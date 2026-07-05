package server

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/config"
	"cornus/pkg/hostcheck"
	"cornus/pkg/hostenv"
	"cornus/pkg/storage"
)

// The default deployment — cornus on the host — must sail through, and get the
// identity mapper so every translating call site is a no-op.
func TestDetectHostOnHostIsUsable(t *testing.T) {
	got, err := detectHost(context.Background(), config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("detectHost: %v", err)
	}
	if got.env.Translating {
		t.Error("Translating = true with no container and no path map")
	}
	p := "/var/lib/cornus/mounts/x"
	if h, ok := got.mapper.ToHost(p); !ok || h != p {
		t.Errorf("ToHost(%q) = (%q, %v), want the identity", p, h, ok)
	}
}

// containerd resolves a path under DataDir for every deploy, so a configuration
// where it cannot see DataDir must stop the server rather than let every
// workload come up with empty volumes.
func TestDetectHostRejectsUnusableContainerdEnvironment(t *testing.T) {
	t.Setenv("CORNUS_DEPLOY_BACKEND", "containerd")
	t.Setenv(hostenv.HostPathMapEnv, "/somewhere/else=/srv/other")
	_, err := detectHost(context.Background(), config.Config{DataDir: "/var/lib/cornus"})
	if err == nil {
		t.Fatal("detectHost accepted an environment where every deploy would silently misbehave")
	}
	// The message has to carry the remedy: this failure is otherwise invisible.
	for _, want := range []string{"containerd", "/var/lib/cornus", hostenv.HostPathMapEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The same environment on dockerhost costs only client-local mounts, so it must
// still start.
func TestDetectHostAllowsDegradedDockerhost(t *testing.T) {
	t.Setenv("CORNUS_DEPLOY_BACKEND", "dockerhost")
	t.Setenv(hostenv.HostPathMapEnv, "/somewhere/else=/srv/other")
	got, err := detectHost(context.Background(), config.Config{DataDir: "/var/lib/cornus"})
	if err != nil {
		t.Fatalf("detectHost: %v", err)
	}
	if len(got.result.Problems()) == 0 {
		t.Error("expected a warning about the unreachable data dir")
	}
}

func TestDetectHostRejectsMalformedPathMap(t *testing.T) {
	t.Setenv(hostenv.HostPathMapEnv, "nonsense")
	if _, err := detectHost(context.Background(), config.Config{DataDir: t.TempDir()}); err == nil {
		t.Fatal("detectHost accepted a malformed path map")
	}
}

// The guarantee that matters: a server in an unusable environment never comes
// into existence, so nothing can deploy through it.
func TestNewRefusesUnusableHostEnvironment(t *testing.T) {
	t.Setenv("CORNUS_DEPLOY_BACKEND", "containerd")
	t.Setenv(hostenv.HostPathMapEnv, "/somewhere/else=/srv/other")
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(config.Config{DataDir: "/var/lib/cornus"}, st); err == nil {
		t.Fatal("New succeeded in an environment where every deploy would silently misbehave")
	}
}

func TestRuntimeForBackend(t *testing.T) {
	for in, want := range map[string]string{
		"":           hostenv.RuntimeDocker,
		"dockerhost": hostenv.RuntimeDocker,
		"containerd": hostenv.RuntimeContainerd,
		// No runtime whose mount namespace could differ from ours: bare execs
		// its OCI runtime as our own child, kubernetes hands it nothing.
		"bare":       hostenv.RuntimeUnknown,
		"kubernetes": hostenv.RuntimeUnknown,
		// incus is named even though it has no path problem either: the runtime is
		// what selects self-inspection, and incus's exists to recognize a cornus
		// running AS an instance — the one containerized shape on that backend that
		// can route to its workloads. Naming a runtime here is no longer a claim
		// that paths diverge.
		"incus": hostenv.RuntimeIncus,
	} {
		if got := runtimeForBackend(in); got != want {
			t.Errorf("runtimeForBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstFailureCarriesTheRemedy(t *testing.T) {
	res := hostcheck.Result{Checks: []hostcheck.Check{
		{Name: "a", Status: hostcheck.StatusWarn, Detail: "warn detail"},
		{Name: "b", Status: hostcheck.StatusFail, Detail: "fail detail", Hint: "do this"},
	}}
	if got := firstFailure(res); got != "fail detail — do this" {
		t.Errorf("firstFailure = %q", got)
	}
}
