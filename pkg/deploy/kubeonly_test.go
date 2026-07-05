package deploy

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

func warnFor(t *testing.T, spec api.DeploySpec, remoteEnv string) string {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	WarnKubernetesOnlyFields(context.Background(), log, spec, remoteEnv)
	return buf.String()
}

// TestWarnKubernetesOnlyFieldsCoversEveryField pins that each kubernetes-only
// field produces exactly one warning naming it.
//
// The regression: dockerhost (the DEFAULT backend), containerdhost, and barehost
// referenced spec.Proxy / DNS / Hub / Docker / AgentForward NOWHERE — no mapping
// and no warning — while nothing in pkg/server gated them by backend. A user who
// wrote an x-cornus-hub block and deployed to the default backend got a
// successful deploy and no feature. Only incushost warned.
func TestWarnKubernetesOnlyFieldsCoversEveryField(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
		want string
	}{
		{"Proxy", api.DeploySpec{Proxy: &api.ProxySpec{}}, "ignores proxy"},
		{"DNS", api.DeploySpec{DNS: &api.DNSSpec{}}, "ignores dns records"},
		{"Hub", api.DeploySpec{Hub: &api.HubSpec{}}, "ignores hub"},
		{"Docker", api.DeploySpec{Docker: &api.DockerSpec{}}, "ignores docker"},
		{"AgentForward", api.DeploySpec{AgentForward: true}, "ignores agentForward"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := warnFor(t, tc.spec, "CORNUS_X_REMOTE")
			if n := strings.Count(out, tc.want); n != 1 {
				t.Errorf("warning %q emitted %d times, want exactly 1:\n%s", tc.want, n, out)
			}
		})
	}
}

// TestWarnKubernetesOnlyFieldsIsSilentForASpecThatAsksForNone guards the other
// direction: a spec requesting none of these must produce nothing at all. A warn
// helper that fires unconditionally trains people to ignore it.
func TestWarnKubernetesOnlyFieldsIsSilentForASpecThatAsksForNone(t *testing.T) {
	if out := warnFor(t, api.DeploySpec{Name: "web", Image: "img"}, "CORNUS_X_REMOTE"); out != "" {
		t.Errorf("warned about something the spec did not request:\n%s", out)
	}
}

// TestAgentForwardWarnsInBothRemoteModes pins that agentForward warns whether or
// not the backend has a remote mode, with the message matched to which.
//
// This is the case I got wrong when writing the helper: the first version fired
// only when remoteModeEnv was non-empty, so barehost — which passes "" because it
// deliberately keeps its companions single-purpose — would have gone on dropping
// agentForward in exactly the silence this helper exists to end.
func TestAgentForwardWarnsInBothRemoteModes(t *testing.T) {
	spec := api.DeploySpec{AgentForward: true}

	withRemote := warnFor(t, spec, "CORNUS_DOCKER_REMOTE")
	if !strings.Contains(withRemote, "remote mode") || !strings.Contains(withRemote, "CORNUS_DOCKER_REMOTE") {
		t.Errorf("a backend WITH a remote mode must name it:\n%s", withRemote)
	}

	withoutRemote := warnFor(t, spec, "")
	if !strings.Contains(withoutRemote, "ignores agentForward") {
		t.Errorf("a backend with NO remote mode must still warn:\n%s", withoutRemote)
	}
	if strings.Contains(withoutRemote, "remote mode") {
		t.Errorf("a backend with no remote mode must not promise one:\n%s", withoutRemote)
	}
}

// TestEachFieldWarnsExactlyOnce is what would have caught the duplicate I
// introduced while migrating incushost onto this helper: incus kept its own
// agentForward warning, so the two fired together AND contradicted each other
// ("offers no ssh-agent forwarding" next to "available in remote mode"). Every
// existing test used strings.Contains, which cannot see a second line.
func TestEachFieldWarnsExactlyOnce(t *testing.T) {
	spec := api.DeploySpec{
		Proxy:        &api.ProxySpec{},
		DNS:          &api.DNSSpec{},
		Hub:          &api.HubSpec{},
		Docker:       &api.DockerSpec{},
		AgentForward: true,
	}
	out := warnFor(t, spec, "CORNUS_X_REMOTE")
	if n := strings.Count(out, "level=WARN"); n != 5 {
		t.Errorf("got %d warnings for 5 requested fields, want exactly 5:\n%s", n, out)
	}
}
