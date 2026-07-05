package webbff

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"cornus/pkg/agentdetect"
)

// The dev mock replays a corpus of agent screens (web/mock/agent-screens.json) and
// reports each one's state from the corpus rather than classifying it. That is only
// true if the SAME BYTES, sent through a real session, reach the same verdict here.
//
// pkg/agentdetect/screens_test.go already checks the corpus against the manifests as
// plain strings. This is the other half, and it is the half that makes "end to end"
// mean anything: the bytes go through termSession.readLoop, the OSC scanner that
// decides what a terminal would paint, the headless VT100, and renderLocked's
// bottom-12-rows window before any rule sees them. Every one of those can change what
// a rule matches, and none of them is exercised by classifying a string.
const mockScreensPath = "../../../../web/mock/agent-screens.json"

type mockScreen struct {
	Agent  string   `json:"agent"`
	State  string   `json:"state"`
	Rule   string   `json:"rule"`
	Title  string   `json:"title"`
	Screen []string `json:"screen"`
}

func loadMockScreens(t *testing.T) []mockScreen {
	t.Helper()
	raw, err := os.ReadFile(mockScreensPath)
	if err != nil {
		t.Fatalf("read %s: %v", mockScreensPath, err)
	}
	var doc struct {
		Screens []mockScreen `json:"screens"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", mockScreensPath, err)
	}
	if len(doc.Screens) == 0 {
		t.Fatalf("%s has no screens", mockScreensPath)
	}
	return doc.Screens
}

// emitScreen is the byte stream the mock sends for one entry, and it must stay
// identical to startAgentDemo's in faketerm.ts — that equality is what lets this
// test speak for the mock at all.
//
// The clear-and-home leads, because a TUI repaints its whole screen rather than
// scrolling: without it a step's lines stay in the render window and keep matching
// their own rules under the next step's. It cannot fail HERE — every subtest gets a
// fresh session, so there is nothing to stack — which is why the mock's own test
// plays a whole sequence through one session, where dropping it does fail.
func emitScreen(e mockScreen) string {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	if e.Title != "" {
		b.WriteString("\x1b]0;" + e.Title + "\x07")
	}
	for _, line := range e.Screen {
		b.WriteString(line + "\r\n")
	}
	return b.String()
}

// detSnapshot reads what the detector would classify: the rendered screen and the
// title it sniffed off the stream.
func detSnapshot(d *detector) (screen, title string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.renderLocked(), d.oscTitle
}

func TestMockAgentScreensThroughARealSession(t *testing.T) {
	set, err := agentdetect.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range loadMockScreens(t) {
		t.Run(e.Agent+"/"+e.State, func(t *testing.T) {
			fe := &fakeExec{}
			mgr := fastDetectManager(t, fe)
			// Launched straight into the agent, which is how the mock's demo presents
			// it: the foreground program IS the agent, so the manifest is resolved
			// from the argv with no /proc probe in the picture.
			sess, err := mgr.Create(context.Background(), "web", []string{e.Agent}, "")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			t.Cleanup(func() { mgr.Kill(sess.id) })
			shell := shellOf(t, fe)

			writeAll(t, shell, emitScreen(e))

			// Wait for the bytes to land, observed as the screen the detector holds
			// rather than as a sleep.
			last := e.Screen[len(e.Screen)-1]
			eventually(t, "the screen reaches the detector", func() bool {
				screen, _ := detSnapshot(sess.det)
				return strings.Contains(screen, last)
			})

			// The session must also be able to SAY which agent it is: the corpus keys
			// on the manifest id, and the mock reports that id in the session list.
			if got := mgr.Get(sess.id).info().Agent; got != e.Agent {
				t.Fatalf("session reports agent %q, want %q", got, e.Agent)
			}

			screen, title := detSnapshot(sess.det)
			res := set.Lookup(e.Agent).Classify(agentdetect.Input{Screen: screen, OSCTitle: title})
			if string(res.State) != e.State || res.RuleID != e.Rule {
				t.Fatalf("rendered screen classified %q via %q, want %q via %q.\n"+
					"Sniffed title: %q\nRendered screen:\n%s",
					res.State, res.RuleID, e.State, e.Rule, title, screen)
			}

			// And the state the session actually reports — the one the workspace badge
			// and the Overview list read — settles to the same thing.
			//
			// This assertion is deliberately NOT the whole test, because for a
			// "working" entry it cannot fail: any output at all marks a session
			// working, and a screen that matches NOTHING leaves that verdict standing
			// (onSettle keeps the committed state when no rule fires). Only the
			// classification above can tell those two apart. For "blocked" and "idle"
			// it is the real proof, since both require a rule to have fired.
			eventually(t, "the session settles to "+e.State, func() bool {
				return stateOf(t, mgr, sess) == sessionState(e.State)
			})
		})
	}
}
