package agentdetect

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The dev mock replays a corpus of agent screens (web/mock/agent-screens.json) so
// every agent cornus can classify has something to look at with no backend. The
// mock reports each screen's state FROM THE CORPUS rather than classifying it,
// which is only honest while the corpus agrees with the manifests — so this test
// is what makes the mock's badges true.
//
// It reads the same file the mock does. A test reaching across the repo into the
// frontend's mock directory is deliberate: the file has one meaning, and splitting
// it into "the mock's copy" and "the test's copy" is exactly how a demo drifts into
// showing states the real backend would never produce.
const screensPath = "../../web/mock/agent-screens.json"

type screenEntry struct {
	Agent  string   `json:"agent"`
	State  string   `json:"state"`
	Rule   string   `json:"rule"`
	Title  string   `json:"title"`
	Screen []string `json:"screen"`
}

func loadScreens(t *testing.T) []screenEntry {
	t.Helper()
	raw, err := os.ReadFile(screensPath)
	if err != nil {
		t.Fatalf("read %s: %v (the dev mock replays this corpus; it is not optional)", screensPath, err)
	}
	var doc struct {
		Screens []screenEntry `json:"screens"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", screensPath, err)
	}
	if len(doc.Screens) == 0 {
		t.Fatalf("%s has no screens", screensPath)
	}
	return doc.Screens
}

// Each entry must classify to the state it declares, THROUGH THE RULE it names.
//
// Asserting the rule and not only the state is the point. Several rules can fire on
// one screen, and a screen that lands on "blocked" through an unrelated rule is a
// screen the mock is showing for the wrong reason — it would keep passing after the
// rule it was written for stopped working.
func TestMockAgentScreensClassifyAsDeclared(t *testing.T) {
	set, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range loadScreens(t) {
		t.Run(e.Agent+"/"+e.State, func(t *testing.T) {
			m := set.Lookup(e.Agent)
			if m == nil {
				t.Fatalf("no bundled manifest for agent %q", e.Agent)
			}
			res := m.Classify(Input{Screen: strings.Join(e.Screen, "\n"), OSCTitle: e.Title})
			if !res.Matched {
				t.Fatalf("no rule fired; want %q via %q.\nTitle: %q\nScreen:\n%s",
					e.State, e.Rule, e.Title, strings.Join(e.Screen, "\n"))
			}
			if string(res.State) != e.State || res.RuleID != e.Rule {
				t.Fatalf("classified %q via rule %q (region %q); want %q via %q.\nTitle: %q\nScreen:\n%s",
					res.State, res.RuleID, res.Region, e.State, e.Rule, e.Title,
					strings.Join(e.Screen, "\n"))
			}
		})
	}
}

// The corpus has to cover what the bundle can classify, or "test all the known
// agents" quietly means "the ones somebody remembered". The requirement is derived
// from the manifests rather than listed here: every state a manifest has a live
// rule for needs a screen, so refreshing the bundle with a new agent — or a new
// state for an existing one — fails here until the mock can show it.
//
// Rules marked skip_state_update are excluded: they exist to say "this screen is
// evidence of nothing", so there is no state for the mock to demonstrate.
func TestMockAgentScreensCoverEveryBundledManifest(t *testing.T) {
	set, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range loadScreens(t) {
		have[e.Agent+"/"+e.State] = true
	}
	for _, id := range set.IDs() {
		want := map[State]bool{}
		for _, r := range set.Lookup(id).Rules {
			if r.SkipStateUpdate {
				continue
			}
			switch r.State {
			case StateWorking, StateIdle, StateBlocked:
				want[r.State] = true
			}
		}
		for _, st := range []State{StateWorking, StateBlocked, StateIdle} {
			if want[st] && !have[id+"/"+string(st)] {
				t.Errorf("%s: the manifest can classify %q but %s has no such screen — "+
					"the dev mock cannot demonstrate it", id, st, screensPath)
			}
		}
	}
}

// A screen the detector cannot see is a screen the mock shows and the backend never
// classifies. Both limits below are the real renderer's (renderLocked in
// cmd/cornus/internal/webbff/agentdetect.go): only the last detBottomLines rows are
// matched, and a row wider than the screen wraps into a different line than the one
// a ^-anchored rule was written against.
//
// Checked here rather than left to the end-to-end test because the failure is
// specific: that test would report "classified idle, want blocked" and leave the
// reason to be found.
func TestMockAgentScreensFitTheDetectorsScreen(t *testing.T) {
	const maxLines, maxCols = 12, 80
	for _, e := range loadScreens(t) {
		if len(e.Screen) > maxLines {
			t.Errorf("%s/%s: %d lines; only the last %d are matched",
				e.Agent, e.State, len(e.Screen), maxLines)
		}
		for i, line := range e.Screen {
			if line != strings.TrimRight(line, " \t") {
				t.Errorf("%s/%s line %d has trailing whitespace, which the renderer strips: %q",
					e.Agent, e.State, i+1, line)
			}
			if n := len([]rune(line)); n > maxCols {
				t.Errorf("%s/%s line %d is %d columns; it wraps at %d and stops being the line "+
					"the rule matches: %q", e.Agent, e.State, i+1, n, maxCols, line)
			}
		}
	}
}
