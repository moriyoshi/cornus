package setupwiz

import (
	"strings"
	"testing"
)

// The contract that matters most: with color off, every style is the identity
// and the only visible change is that backticks come off. A piped `cornus setup`
// must not start emitting escape sequences into someone's log.
func TestGuideStylesPlainOutputCarriesNoEscapes(t *testing.T) {
	g := newGuideStyles(false)
	lines := append(g.heading("No server yet — %s", "Local server"), g.steps([]string{
		"Start the server: `sudo CORNUS_DEPLOY_BACKEND=bare cornus serve` (add `--data-dir`).",
		"Plain prose with no spans at all.",
	})...)
	lines = append(lines, g.docLine(setupGuideURL+"#local-bare"))
	for _, l := range lines {
		if strings.Contains(l, "\x1b") {
			t.Errorf("no-color rendering emitted an escape sequence: %q", l)
		}
		if strings.Contains(l, "`") {
			t.Errorf("backticks must come off even with color disabled: %q", l)
		}
	}
	joined := strings.Join(lines, "\n")
	// The words themselves survive intact — this is what every other test in the
	// package greps for, and what a user pipes into a file.
	for _, want := range []string{
		"No server yet — Local server",
		"sudo CORNUS_DEPLOY_BACKEND=bare cornus serve",
		"--data-dir",
		"docs: " + setupGuideURL + "#local-bare",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plain rendering lost %q:\n%s", want, joined)
		}
	}
	// A rule is decoration; with nothing to decorate it is noise. The blank line
	// after the heading is not decoration and must survive: without it the first
	// step begins on the row below the sentence and reads as part of it.
	if strings.Contains(joined, "─") {
		t.Errorf("no-color heading should not draw a rule:\n%s", joined)
	}
	if head := g.heading("Title"); len(head) != 1 {
		t.Errorf("no-color heading should be the bare title, got %q", head)
	}
}

// With color on the text must still be readable as text: the styling wraps the
// words rather than replacing them.
func TestGuideStylesColorWrapsRatherThanReplaces(t *testing.T) {
	g := newGuideStyles(true)
	line := g.steps([]string{"Check the host: `cornus daemon preflight` first."})[0]
	if !strings.Contains(line, "\x1b") {
		t.Fatalf("color rendering emitted no escape sequences: %q", line)
	}
	if strings.Contains(line, "`") {
		t.Errorf("backticks must be replaced by the highlight, not kept: %q", line)
	}
	for _, want := range []string{"Check the host:", "cornus daemon preflight", "first."} {
		if !strings.Contains(line, want) {
			t.Errorf("styled line lost %q: %q", want, line)
		}
	}
	if head := g.heading("Title"); len(head) != 2 || !strings.Contains(head[1], "─") {
		t.Errorf("color heading should be title + rule, got %q", head)
	}
}

// Ordinals are right-aligned so the text starts at one column whether there are
// nine steps or ten; a ragged left edge is exactly what this styling exists to
// remove.
func TestGuideStepsAlignOrdinals(t *testing.T) {
	g := newGuideStyles(false)
	items := make([]string, 10)
	for i := range items {
		items[i] = "step"
	}
	lines := g.steps(items)
	first, last := lines[0], lines[9]
	if strings.Index(first, "step") != strings.Index(last, "step") {
		t.Errorf("text columns differ between step 1 and step 10:\n%q\n%q", first, last)
	}
	if !strings.HasPrefix(first, "   1  ") {
		t.Errorf("step 1 should be right-aligned into a 2-wide gutter, got %q", first)
	}
}

// The synopsis is the headline command; a reader copies it, so the "$ " prompt
// marker must not be part of what they copy, and the blank line after it must
// separate it from the steps.
func TestGuideSynopsisRendersAsAPromptedCommand(t *testing.T) {
	g := newGuideStyles(false)
	lines := g.synopsis("sudo cornus serve --data-dir /var/lib/cornus")
	if len(lines) != 2 || lines[1] != "" {
		t.Fatalf("synopsis should be the command plus a blank separator, got %q", lines)
	}
	if !strings.HasPrefix(lines[0], "  $ sudo cornus serve") {
		t.Errorf("synopsis line = %q, want a prompt marker then the command", lines[0])
	}
	// Nothing to run means nothing to show — not an empty prompt.
	if got := g.synopsis(""); got != nil {
		t.Errorf("empty synopsis should render nothing, got %q", got)
	}
}

// Every arrangement that stands a server up must headline the command that does
// it, and that headline must agree with the step that spells it out — they are
// two renderings of one fact, and a reader who copies the headline and then
// reads the step must not find them different.
func TestGuideSynopsisAgreesWithTheSteps(t *testing.T) {
	check := func(t *testing.T, a *Answers, wantIn string) {
		t.Helper()
		g := guideFor(a)
		if g.Synopsis == "" {
			t.Fatalf("scenario %d backend %q has no synopsis", a.Scenario, a.LocalBackend)
		}
		if strings.Contains(g.Synopsis, "`") {
			t.Errorf("synopsis is rendered as code already; it must not carry backticks: %q", g.Synopsis)
		}
		if !strings.Contains(strings.Join(g.Setup, "\n"), wantIn) {
			t.Errorf("scenario %d backend %q: no step contains %q, so the headline stands alone:\n%v",
				a.Scenario, a.LocalBackend, wantIn, g.Setup)
		}
	}
	for _, b := range localBackends {
		t.Run("local-"+backendAnchor(b.Key), func(t *testing.T) {
			a := &Answers{Scenario: ScenarioLocal, LocalBackend: b.Key}
			// The step wraps the command without --data-dir, so compare the head.
			check(t, a, backendSudo(b.Key)+backendEnvPrefix(b.Key)+"cornus serve")
		})
	}
	for _, sc := range []Scenario{ScenarioSSHDocker, ScenarioSSHContainerd, ScenarioSSHBare, ScenarioSSHIncus} {
		t.Run("ssh-"+backendAnchor(scenarioBackend(&Answers{Scenario: sc})), func(t *testing.T) {
			b := scenarioBackend(&Answers{Scenario: sc})
			check(t, &Answers{Scenario: sc}, backendSudo(b)+backendEnvPrefix(b)+"cornus serve --addr 127.0.0.1:5000")
		})
	}
	check(t, &Answers{Scenario: ScenarioKubeURL}, "helm install cornus oci://ghcr.io/moriyoshi/charts/cornus")
	check(t, &Answers{Scenario: ScenarioDockerContainer}, containerRunCommand(&Answers{Scenario: ScenarioDockerContainer}))
}

// The URL scenario stands nothing up, so a headline command would be a lie.
func TestGuideSynopsisAbsentWhenNothingIsStartedUp(t *testing.T) {
	if g := guideFor(&Answers{Scenario: ScenarioURL}); g.Synopsis != "" {
		t.Errorf("the someone-else-runs-it scenario should have no synopsis, got %q", g.Synopsis)
	}
}

// The local Kubernetes headline must carry the registry advertisement, because
// without it every deploy fails at image pull — burying it three steps down is
// how people lose an afternoon.
func TestLocalKubernetesSynopsisCarriesTheRegistryAdvertisement(t *testing.T) {
	got := guideFor(&Answers{Scenario: ScenarioLocal, LocalBackend: backendKubernetes}).Synopsis
	for _, want := range []string{"CORNUS_ADVERTISE_REGISTRY=", "CORNUS_DEPLOY_BACKEND=kubernetes", "cornus serve"} {
		if !strings.Contains(got, want) {
			t.Errorf("local kubernetes synopsis missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "sudo") {
		t.Errorf("local kubernetes drives no local runtime and needs no sudo: %q", got)
	}
}

// The URL is underlined and the label is not: the underline marks what is
// followable, so extending it over "docs: " would blur where the thing you can
// click begins.
func TestGuideDocLineUnderlinesOnlyTheURL(t *testing.T) {
	const url = "https://cornus.dev/guides/server-setup#local-bare"
	got := newGuideStyles(true).docLine(url)
	// One escape pair around the whole URL, not one per character.
	if !strings.Contains(got, "\x1b[4m"+url+"\x1b[24m") {
		t.Errorf("URL is not underlined as a single run: %q", got)
	}
	if n := strings.Count(got, "\x1b[4m"); n != 1 {
		t.Errorf("underline emitted %d times, want once: %q", n, got)
	}
	if strings.Contains(got, "\x1b[4mdocs: ") {
		t.Errorf("the label must not be underlined: %q", got)
	}
	// And no escapes at all without color.
	if plain := newGuideStyles(false).docLine(url); plain != "docs: "+url {
		t.Errorf("no-color docLine = %q, want the bare label and URL", plain)
	}
}

// The documentation pointer sits directly under the heading — it names the same
// thing the heading does, the full version of this runbook, rather than being
// one more step to carry out. A blank line then opens the body, so the first
// step never begins on the row below a sentence.
func TestServerGuidePutsTheDocumentationPointerUnderTheHeading(t *testing.T) {
	ui := &scriptUI{confirms: []bool{false}, selects: []int{2}} // not set up; runtime: Bare
	w, _ := newTestWizard(t, ui, "")
	a := &Answers{Scenario: ScenarioLocal}
	if err := w.serverSetupStep(a).ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	var docAt, headingAt = -1, -1
	for i, n := range ui.notes {
		if docAt < 0 && strings.HasPrefix(n, "docs: ") {
			docAt = i
		}
		if headingAt < 0 && strings.Contains(n, "No server yet") {
			headingAt = i
		}
	}
	if docAt < 0 || headingAt < 0 {
		t.Fatalf("guide missing the pointer or the heading: %q", ui.notes)
	}
	if docAt != headingAt+1 {
		t.Errorf("pointer at %d must sit directly under the heading at %d:\n%q", docAt, headingAt, ui.notes)
	}
	// And a blank line separates the header block from the body.
	if docAt+1 >= len(ui.notes) || ui.notes[docAt+1] != "" {
		t.Errorf("no blank line after the pointer:\n%q", ui.notes)
	}
	// And it appears once — it used to trail the steps as well.
	n := 0
	for _, line := range ui.notes {
		if strings.HasPrefix(line, "docs: ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("pointer appears %d times, want once: %q", n, ui.notes)
	}
}

// An unpaired backtick is a typo in the guide text, not a licence to swallow the
// rest of the line.
func TestGuideInlineLeavesUnpairedBacktickAlone(t *testing.T) {
	g := newGuideStyles(false)
	const s = "a `b` then a stray ` tail"
	got := g.inline(s)
	if !strings.HasSuffix(got, "stray ` tail") {
		t.Errorf("unpaired backtick mangled the remainder: %q", got)
	}
	if !strings.HasPrefix(got, "a b then") {
		t.Errorf("the paired span before it should still render: %q", got)
	}
}

// Every guide string is rendered through inline(), so an unbalanced backtick
// anywhere in the table would silently ship a stray one to the terminal.
func TestGuideTextHasBalancedBackticks(t *testing.T) {
	for s := ScenarioLocal; s <= ScenarioIncusContainer; s++ {
		for _, b := range localBackends {
			g := guideFor(&Answers{Scenario: s, LocalBackend: b.Key})
			for _, line := range append(append([]string{}, g.Setup...), g.Next...) {
				if n := strings.Count(line, "`"); n%2 != 0 {
					t.Errorf("scenario %d backend %q: odd backtick count in %q", s, b.Key, line)
				}
			}
		}
	}
}
