package agentdetect

import (
	"regexp"
	"strings"
	"testing"
)

// The defect this whole package exists to fix. `/proc/<pid>/comm` names the
// INTERPRETER, not the agent — measured on a real host, a Node process reports
// "node-MainThread" — so identification keyed on comm alone matches nothing for
// any agent behind a runtime, which is most of them.
func TestAgentNameSeesThroughRuntimes(t *testing.T) {
	set, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		comm string
		argv []string
		want string
	}{
		// The measured case: Node renames its main thread, and the agent is a
		// script path in argv.
		// THE measured case: comm is "node-MainThread" (Node names its main thread),
		// and the agent is only visible in argv. Without normalising that suffix the
		// name is not even recognisable as a runtime, so the unwrap never fires.
		{"node-MainThread wrapping codex", "node-MainThread",
			[]string{"node", "/usr/bin/codex"}, "codex"},
		{"node-MainThread wrapping nothing known", "node-MainThread",
			[]string{"/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, ""},
		{"node wrapping a claude shim", "node",
			[]string{"node", "/home/u/.local/bin/claude"}, "claude"},
		{"node wrapping codex", "node",
			[]string{"node", "/path/to/bin/codex"}, "codex"},
		{"bun wrapping codex", "bun", []string{"bun", "/opt/codex"}, "codex"},
		{"python -m", "python3", []string{"python3", "-m", "codex"}, "codex"},
		{"env wrapping", "env", []string{"env", "FOO=1", "codex"}, "codex"},

		// Inline code is not a program name. Naming the agent after a fragment of
		// JavaScript is the failure this guards.
		{"node -e is not a program", "node",
			[]string{"node", "-e", "require('claude')"}, ""},
		{"python -c is not a program", "python3",
			[]string{"python3", "-c", "import codex"}, ""},

		// A directly-executed agent needs no unwrapping.
		{"direct binary", "claude", []string{"claude"}, "claude"},
		{"direct binary with args", "codex", []string{"codex", "--resume"}, "codex"},
		{"alias resolves", "claude-code", []string{"claude-code"}, "claude"},
		{"case and extension normalised", "Claude.CMD", []string{"Claude.CMD"}, "claude"},

		// A shell at a prompt is not an agent, and neither is an unrelated program.
		{"plain shell", "bash", []string{"bash"}, ""},
		{"shell running something unknown", "bash", []string{"bash", "-c", "make"}, ""},
		{"unrelated program", "vim", []string{"vim", "README.md"}, ""},
		{"cat", "cat", []string{"cat", "INSTALL.md"}, ""},
		{"empty", "", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := set.Identify(tc.comm, tc.argv); got != tc.want {
				t.Fatalf("Identify(%q, %q) = %q, want %q", tc.comm, tc.argv, got, tc.want)
			}
		})
	}
}

// Alias resolution comes from the manifests themselves, not a list in our code —
// so a refreshed bundle that renames an agent keeps working.
func TestBundledSetIndexesIDsAndAliases(t *testing.T) {
	set, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	if len(set.IDs()) < 15 {
		t.Fatalf("only %d manifests loaded: %v", len(set.IDs()), set.IDs())
	}
	m := set.Lookup("claude")
	if m == nil {
		t.Fatal("no manifest for claude")
	}
	if len(m.Rules) == 0 {
		t.Fatal("claude manifest has no rules")
	}
	// Its declared alias must resolve to the same manifest.
	for _, alias := range m.Aliases {
		if got := set.Lookup(alias); got != m {
			t.Errorf("alias %q did not resolve to the claude manifest", alias)
		}
	}
	if set.Knows("definitely-not-an-agent") {
		t.Error("Knows() accepted an unknown name")
	}
}

// Region extraction is where a rule's precision comes from, and it is the thing
// cornus's own rules never had. Each case is a region the bundled manifests
// actually use.
func TestRegions(t *testing.T) {
	screen := strings.Join([]string{
		"first line",
		"",
		"scrolled output: delete everything? [y/n]",
		"────────────────",
		"› tell me something",
		"────────────────",
		"  waiting for confirmation",
		"",
	}, "\n")
	in := Input{Screen: screen, OSCTitle: "claude — building", OSCProgress: "4;0"}

	cases := []struct{ region, want string }{
		{"osc_title", "claude — building"},
		{"osc_progress", "4;0"},
		{"whole_recent", screen},
		{"bottom_non_empty_lines(1)", "  waiting for confirmation\n"},
		{"top_non_empty_lines(1)", "first line\n"},
		// Between the second-from-last rule and the next rule below it.
		{"prompt_box_body", "› tell me something"},
		{"after_last_horizontal_rule", "  waiting for confirmation\n"},
		{"after_last_prompt_marker", "────────────────\n  waiting for confirmation\n"},
		// An unrecognised region yields nothing, so a rule written for it cannot
		// silently widen to the whole screen.
		{"no_such_region", ""},
	}
	for _, tc := range cases {
		t.Run(tc.region, func(t *testing.T) {
			if got := Region(in, tc.region); got != tc.want {
				t.Fatalf("Region(%q) = %q, want %q", tc.region, got, tc.want)
			}
		})
	}
}

// The gate semantics, taken from upstream rather than guessed: `contains` is
// case-insensitive AND, `regex` matches the region whole, `line_regex` must match
// SOME line, `any` constrains only when present, and `not` is a veto.
func TestGateSemantics(t *testing.T) {
	parse := func(t *testing.T, body string) *Manifest {
		t.Helper()
		m, err := ParseManifest([]byte("id = \"t\"\n" + body))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	classify := func(m *Manifest, screen string) Result {
		return m.Classify(Input{Screen: screen})
	}

	t.Run("contains is case-insensitive AND", func(t *testing.T) {
		m := parse(t, `
[[rules]]
id = "r"
state = "blocked"
contains = ["Approve", "CHANGES"]
`)
		if !classify(m, "approve these changes").Matched {
			t.Error("contains should be case-insensitive")
		}
		if classify(m, "approve this").Matched {
			t.Error("all contains needles must be present")
		}
	})

	t.Run("line_regex anchors per line, regex does not", func(t *testing.T) {
		m := parse(t, `
[[rules]]
id = "r"
state = "blocked"
line_regex = ["^> yes$"]
`)
		if !classify(m, "noise\n> yes\nmore").Matched {
			t.Error("line_regex must match some line")
		}
		if classify(m, "a > yes b").Matched {
			t.Error("line_regex must not match mid-line")
		}
	})

	t.Run("any constrains only when present", func(t *testing.T) {
		m := parse(t, `
[[rules]]
id = "r"
state = "blocked"
any = [ { contains = ["alpha"] }, { contains = ["beta"] } ]
`)
		if !classify(m, "has beta").Matched {
			t.Error("any should match when one branch does")
		}
		if classify(m, "has neither").Matched {
			t.Error("any should fail when no branch matches")
		}
	})

	t.Run("not is a veto", func(t *testing.T) {
		m := parse(t, `
[[rules]]
id = "r"
state = "blocked"
contains = ["proceed?"]
not = [ { contains = ["cancelled"] } ]
`)
		if !classify(m, "proceed?").Matched {
			t.Error("rule should match without the vetoed text")
		}
		if classify(m, "proceed? cancelled").Matched {
			t.Error("not should veto the match")
		}
	})

	t.Run("highest priority wins, ties go to the first rule", func(t *testing.T) {
		m := parse(t, `
[[rules]]
id = "low"
state = "idle"
priority = 10
contains = ["x"]

[[rules]]
id = "high"
state = "blocked"
priority = 20
contains = ["x"]

[[rules]]
id = "tie"
state = "working"
priority = 20
contains = ["x"]
`)
		got := classify(m, "x")
		if got.RuleID != "high" || got.State != StateBlocked {
			t.Fatalf("winner = %q (%s), want the higher-priority rule 'high' (blocked)", got.RuleID, got.State)
		}
	})
}

// Every bundled manifest must compile, and every rule must name a region this
// implementation actually understands. A region we do not implement silently
// yields "" and the rule can never fire — a whole agent's detection could be
// dead and nothing would say so.
func TestEveryBundledRuleUsesAnImplementedRegion(t *testing.T) {
	set, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	probe := Input{Screen: "x", OSCTitle: "x", OSCProgress: "x"}
	for _, id := range set.IDs() {
		m := set.Lookup(id)
		for _, r := range m.Rules {
			// A region is "implemented" if it is one of the named forms or parses
			// as a counted form. Detected by asking Region for a screen that is
			// non-empty everywhere, so only an unknown spec yields "".
			if Region(probe, r.Region) == "" && r.Region != "prompt_box_body" &&
				!strings.HasPrefix(r.Region, "after_last_") {
				t.Errorf("%s: rule %q uses unimplemented region %q — it can never fire",
					id, r.ID, r.Region)
			}
			switch r.State {
			case StateWorking, StateIdle, StateBlocked, StateUnknown, StateDone:
			default:
				t.Errorf("%s: rule %q has unknown state %q", id, r.ID, r.State)
			}
		}
	}
}

// The manifests are authored against Rust's regex crate; Go's RE2 spells two
// things differently. This is the shim, and it is load-bearing: 9 of the 60
// patterns in the bundle use \uHHHH, and one uses \p{Alphabetic}.
func TestTranslateRegex(t *testing.T) {
	cases := []struct{ in, want string }{
		{`[\u2800-\u28FF]`, `[\x{2800}-\x{28FF}]`},
		{`^⚠[\u{fe0e}\u{fe0f}]?`, `^⚠[\x{fe0e}\x{fe0f}]?`},
		{`\p{Alphabetic}+\w*ing\b`, `\p{L}+\w*ing\b`},
		{`\P{Alphabetic}`, `\P{L}`},
		// Untouched: already-RE2 syntax must survive verbatim.
		{`^\s*(◔|◑|●)\s+x`, `^\s*(◔|◑|●)\s+x`},
		{`\x{2800}`, `\x{2800}`},
	}
	for _, tc := range cases {
		if got := translateRegex(tc.in); got != tc.want {
			t.Errorf("translateRegex(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// And the translation must actually produce something RE2 accepts, matching
	// the braille spinner the rule was written for.
	re := regexp.MustCompile(translateRegex(`^\s*[\u2800-\u28FF]+\s+\p{Alphabetic}+\w*ing\b`))
	if !re.MatchString("  ⠹ building the thing") {
		t.Error("translated spinner pattern did not match a spinner line")
	}
}

// A refresh of the vendored bundle that introduces regex syntax RE2 cannot take
// would make Bundled() fail and kill detection outright. This is the guard: every
// pattern in every bundled manifest must compile after translation.
func TestEveryBundledPatternCompiles(t *testing.T) {
	if _, err := Bundled(); err != nil {
		t.Fatalf("bundled manifests do not compile — a refresh may have introduced "+
			"regex syntax Go's RE2 rejects; extend translateRegex: %v", err)
	}
}
