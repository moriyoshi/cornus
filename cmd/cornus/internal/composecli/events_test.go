package composecli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
)

// TestServiceEventHuman locks the plain-mode wording (byte-identical to the
// pre-driver output) for each event shape.
func TestServiceEventHuman(t *testing.T) {
	up := svcUp("web", api.DeployStatus{Instances: []api.InstanceStatus{
		{Running: true}, {Running: true},
	}})
	cases := []struct {
		ev   serviceEvent
		want string
	}{
		{up, "web  up (2/2 running)\n"},
		{svcEvent("web", "up", "mounted; streaming over 9P"), "web  up (mounted; streaming over 9P)\n"},
		{svcEvent("web", "forwarding", "127.0.0.1:8080 -> web:80"), "web  forwarding 127.0.0.1:8080 -> web:80\n"},
		{svcEvent("web", "up-to-date", "held by background agent"), "web  is up-to-date (held by background agent)\n"},
		{svcEvent("web", "recreated", "configuration changed (mounted; held by background agent)"), "web  recreated: configuration changed (mounted; held by background agent)\n"},
		{svcEvent("web", "removed", ""), "web  removed\n"},
		{svcEvent("web", "started", ""), "web  started\n"},
		{serviceEvent{Service: "web", Event: "transition", Instance: "web-0", State: "running"}, "web  web-0: running\n"},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		d := cliout.New(cliout.Options{Stdout: io.Discard, Stderr: &buf, Output: "plain"})
		d.Event(tc.ev)
		if buf.String() != tc.want {
			t.Errorf("Human(%+v) = %q, want %q", tc.ev, buf.String(), tc.want)
		}
	}
}

// TestServiceEventJSON confirms json mode emits clean structured fields instead
// of a padded message blob.
func TestServiceEventJSON(t *testing.T) {
	var buf bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: io.Discard, Stderr: &buf, Output: "json"})
	d.Event(svcUp("web", api.DeployStatus{Instances: []api.InstanceStatus{{Running: true}, {}}}))

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, buf.String())
	}
	if got["service"] != "web" || got["event"] != "up" || got["running"] != float64(1) || got["total"] != float64(2) {
		t.Errorf("unexpected json: %v", got)
	}
	if strings.Contains(buf.String(), "  ") {
		t.Errorf("json output leaked layout padding: %q", buf.String())
	}
}
