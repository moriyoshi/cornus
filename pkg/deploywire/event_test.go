package deploywire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEventExitCodeWire pins the on-the-wire contract of Event.ExitCode,
// including what a VERSION-SKEWED peer sees. Both sides normally ship in one
// binary, but a client and server of different vintages must degrade to
// "unknown" and never to a fabricated success.
func TestEventExitCodeWire(t *testing.T) {
	// A code of 0 is a real answer and must survive the round trip — omitempty on
	// a *int omits only a nil pointer, not a pointer to zero.
	zero := 0
	b, err := json.Marshal(Event{Done: true, ExitCode: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"exitCode":0`) {
		t.Fatalf("encoded Event = %s, want an explicit exitCode 0", b)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ExitCode == nil || *back.ExitCode != 0 {
		t.Fatalf("decoded ExitCode = %v, want 0", back.ExitCode)
	}

	// A non-zero code round-trips as itself.
	three := 3
	b, _ = json.Marshal(Event{Done: true, ExitCode: &three})
	back = Event{}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ExitCode == nil || *back.ExitCode != 3 {
		t.Fatalf("decoded ExitCode = %v, want 3", back.ExitCode)
	}

	// An unknown code is omitted entirely, so an OLDER caller (which has no such
	// field) sees exactly the frame it always saw.
	b, _ = json.Marshal(Event{Done: true})
	if strings.Contains(string(b), "exitCode") {
		t.Fatalf("encoded Event = %s, want no exitCode member when unknown", b)
	}

	// An OLDER server's frame — no exitCode member at all — decodes to nil, i.e.
	// unknown. That is the same answer it could give before the field existed;
	// it must not read as 0.
	back = Event{}
	if err := json.Unmarshal([]byte(`{"done":true}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.ExitCode != nil {
		t.Fatalf("ExitCode from a legacy frame = %v, want nil (unknown)", *back.ExitCode)
	}
}
