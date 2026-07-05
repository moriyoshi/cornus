package dockerhost

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// TestWarnUnsupportedNeverRepeatsAWarning pins that this backend's prelude emits
// each warning at most once for a spec requesting everything it cannot honor.
//
// This is the test that was missing when the kubernetes-only warnings moved into
// deploy.WarnKubernetesOnlyFields: a backend that kept its own branch for one of
// those fields while also calling the shared helper produced TWO warnings that
// contradicted each other, and every existing assertion used strings.Contains,
// which cannot see a second line — so the whole suite stayed green.
//
// Counting is what catches duplication, and duplication is what a shared helper
// invites. dockerhost calls WarnKubernetesOnlyFields for Proxy, DNS, Hub, Docker,
// AgentForward and UpdateConfig; anyone adding a dockerhost-local branch for one
// of those six fails here.
func TestWarnUnsupportedNeverRepeatsAWarning(t *testing.T) {
	buf := captureLogs(t)
	b := newTestBackend(t, &fakeDocker{})

	// Everything this backend is known to warn about, requested at once.
	spec := api.DeploySpec{
		Name:         "web",
		Image:        "localhost:5000/app:v1",
		Proxy:        &api.ProxySpec{},
		DNS:          &api.DNSSpec{},
		Hub:          &api.HubSpec{},
		Docker:       &api.DockerSpec{},
		AgentForward: true,
		UpdateConfig: &api.UpdateConfig{},
		Credentials:  &api.CredentialSpec{},
		Resources:    &api.Resources{ReservedCPU: 0.5},
		Ingress:      &api.IngressSpec{Enabled: true},
		Knative:      &api.KnativeSpec{Enabled: true},
	}
	b.warnUnsupported(context.Background(), slog.Default(), spec)

	seen := map[string]int{}
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		// Key on the message text, which is what a reader actually sees.
		start := strings.Index(line, "msg=")
		if start < 0 {
			continue
		}
		seen[line[start:]]++
	}
	if len(seen) == 0 {
		t.Fatal("a spec requesting every unsupported field produced no warnings at all")
	}
	for msg, n := range seen {
		if n > 1 {
			t.Errorf("warning emitted %d times:\n  %s", n, msg[:min(len(msg), 160)])
		}
	}
}
