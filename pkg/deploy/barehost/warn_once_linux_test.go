//go:build linux

package barehost

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
// It exists because of what happened when the kubernetes-only warnings moved into
// deploy.WarnKubernetesOnlyFields: a backend that kept its own agentForward (or
// updateConfig) block while ALSO calling the shared helper produced two warnings
// that contradicted each other — "this backend offers no ssh-agent forwarding"
// directly above "available exactly when the server runs in remote mode". Every
// other assertion in the suite uses strings.Contains, which cannot see a second
// line, so the whole suite stayed green.
//
// Counting is what catches duplication, and duplication is what a shared helper
// invites. Anything added to warnUnsupported that the helper already covers will
// fail here rather than shipping two mutually contradicting lines to an operator.
func TestWarnUnsupportedNeverRepeatsAWarning(t *testing.T) {
	buf := captureLogs(t)

	// Everything this backend is known to warn about, requested at once.
	spec := api.DeploySpec{
		Name:            "web",
		Image:           "localhost:5000/app:v1",
		Proxy:           &api.ProxySpec{},
		DNS:             &api.DNSSpec{},
		Hub:             &api.HubSpec{},
		Docker:          &api.DockerSpec{},
		AgentForward:    true,
		UpdateConfig:    &api.UpdateConfig{},
		Healthcheck:     &api.Healthcheck{Test: []string{"CMD", "true"}},
		Ingress:         &api.IngressSpec{Enabled: true},
		Knative:         &api.KnativeSpec{Enabled: true},
		StopSignal:      "SIGINT",
		StopGracePeriod: "30s",
		Init:            boolPtr(true),
		StdinOpen:       true,
		ExtraHosts:      []string{"db:10.0.0.5"},
		DNSOptions:      []string{"ndots:2"},
		Resources:       &api.Resources{ReservedCPU: 0.5, ReservedMemory: 1 << 20},
		Networks:        []api.NetworkAttachment{{Name: "back", Driver: "macvlan", IP: "10.9.0.4"}},
	}
	warnUnsupported(context.Background(), slog.Default(), spec)

	lines := warnLines(buf.String())
	if len(lines) == 0 {
		t.Fatal("a spec requesting every unsupported field produced no warnings at all")
	}

	// Two counts, because the duplicate that actually shipped was not a repeat of
	// the same string. Counting verbatim messages catches a helper called twice;
	// counting the FIELD each message is about catches the real historical shape —
	// two differently-worded lines about one field, which no strings.Contains
	// assertion and no verbatim count can see.
	verbatim, byField := map[string]int{}, map[string][]string{}
	for _, line := range lines {
		verbatim[line]++
		byField[warnTopic(line)] = append(byField[warnTopic(line)], line)
	}
	for msg, n := range verbatim {
		if n > 1 {
			t.Errorf("warning emitted %d times:\n  %s", n, trunc(msg))
		}
	}
	for field, msgs := range byField {
		if len(msgs) > 1 {
			t.Errorf("%d warnings about the same field %q; an operator reading them cannot tell which one is true:\n  %s\n  %s",
				len(msgs), field, trunc(msgs[0]), trunc(msgs[1]))
		}
	}
}

// warnLines extracts the msg-and-attributes tail of every WARN record in a
// slog text-handler stream.
func warnLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		if start := strings.Index(line, "msg="); start >= 0 {
			lines = append(lines, line[start:])
		}
	}
	return lines
}

// warnTopic reduces a warning to the spec field it is about: the token after
// "ignores ", or the whole message for the warnings phrased differently (the
// cluster-Ingress one). Two warnings that reduce to the same topic are two
// answers to one question.
func warnTopic(msg string) string {
	i := strings.Index(msg, "ignores ")
	if i < 0 {
		return msg
	}
	rest := msg[i+len("ignores "):]
	if j := strings.IndexAny(rest, " :;,("); j >= 0 {
		rest = rest[:j]
	}
	return strings.Trim(rest, `"`)
}

func trunc(s string) string { return s[:min(len(s), 160)] }
