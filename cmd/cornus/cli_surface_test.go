package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// productionParser builds a parser with the SAME options main() uses, plus the
// two seams needed to observe it: captured writers and a captured exit.
//
// It calls parserOptions() rather than restating the list. That matters more than
// it looks: the two behaviors asserted below (what a mistyped flag prints, and
// what --version prints) are properties OF THOSE OPTIONS, so a test that built
// its own option list would prove only that its own list behaves as configured —
// while `main` could carry different options entirely. Both of these were closed
// as done with no test at all; a lookalike parser would have been the second-best
// way to keep them uncovered.
func productionParser(t *testing.T, cli *CLI) (*kong.Kong, *bytes.Buffer, *[]int) {
	t.Helper()
	var out bytes.Buffer
	exits := []int{}
	opts := append(parserOptions(),
		kong.Writers(&out, &out),
		kong.Exit(func(code int) { exits = append(exits, code) }),
	)
	parser, err := kong.New(cli, opts...)
	if err != nil {
		t.Fatalf("kong.New with production options: %v", err)
	}
	return parser, &out, &exits
}

// TestVersionFlagPrintsTheStampedVersion covers `cornus --version`, the spelling
// every other CLI answers to. The existing coverage was for the `version`
// SUBCOMMAND, which is a different code path and was never the missing one.
//
// Both halves are asserted because the flag is useless without either: the value
// has to be the build-stamped version (kong prints vars["version"], so a missing
// kong.Vars entry prints an empty line and still "works"), and it has to exit 0
// (kong.VersionFlag.BeforeReset calls app.Exit(0); without the exit, parsing
// continues and kong then reports a missing command).
func TestVersionFlagPrintsTheStampedVersion(t *testing.T) {
	var cli CLI
	parser, out, exits := productionParser(t, &cli)
	// The Parse error is deliberately not checked, and that is not laziness.
	// kong.VersionFlag prints and then calls app.Exit(0); in production that IS
	// os.Exit, so the process is gone. Here Exit is a recorder, so control returns
	// and kong goes on to complain about the missing command — an artifact of the
	// only seam that makes the flag observable at all. What the exit stub buys is
	// better than the error: the exit CODE is asserted below, which is the thing a
	// user actually depends on.
	_, _ = parser.Parse([]string{"--version"})

	got := strings.TrimSpace(out.String())
	if got == "" {
		t.Error("--version printed nothing. kong.VersionFlag prints the `version` kong.Var, so an absent " +
			"kong.Vars{\"version\": ...} in parserOptions makes the flag print a blank line and look wired.")
	}
	// Equality, not containment: the flag's whole job is to print the version and
	// nothing else, so a version buried in usage output is a failure the user sees
	// as noise. (Parse writes nothing itself — the missing-command error above is
	// only returned, and printing it is FatalIfErrorf's job, which is not called.)
	if got != version {
		t.Errorf("--version printed %q, want exactly the build-stamped version %q", got, version)
	}
	if len(*exits) != 1 || (*exits)[0] != 0 {
		t.Errorf("--version requested exits %v, want exactly [0]: without the exit, kong keeps parsing and "+
			"then fails on the missing command, so the user sees the version followed by an error", *exits)
	}
}

// TestUnknownFlagPrintsShortUsage covers kong.ShortUsageOnError. The full help
// for this CLI is ~170 lines; printing it on a mistyped flag scrolls the actual
// error off an 80x24 terminal, which is the entire reason the option is set.
//
// The assertion is on LENGTH plus content, and length is the only thing that can
// distinguish the two configurations — both print the error, both print usage,
// and they differ only in how much. The threshold is deliberately loose (a short
// usage block is a handful of lines; the full command tree is over a hundred), so
// this fails on the configuration change and not on help text being reworded.
func TestUnknownFlagPrintsShortUsage(t *testing.T) {
	var cli CLI
	parser, out, _ := productionParser(t, &cli)
	_, err := parser.Parse([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("parsing an unknown flag succeeded; this test can no longer see what it guards")
	}
	// FatalIfErrorf is what consults usageOnError — Parse itself does not print.
	parser.FatalIfErrorf(err)

	printed := out.String()
	if !strings.Contains(printed, "no-such-flag") {
		t.Errorf("output does not name the offending flag:\n%s", printed)
	}
	lines := strings.Count(strings.TrimSpace(printed), "\n") + 1
	const maxShort = 30
	if lines > maxShort {
		t.Errorf("a mistyped flag printed %d lines (limit %d). That is the full command tree, which scrolls "+
			"the error message off an 80x24 terminal — kong.ShortUsageOnError() has been dropped from "+
			"parserOptions or replaced by kong.UsageOnError().", lines, maxShort)
	}
}

// TestDeprecatedMountCommandsStayAbsent guards a REMOVAL. `mount-agent` and
// `mountcheck` were deprecated aliases, deleted while the version was still
// `dev` with no published release, so there were no users to break.
//
// A removal has no natural regression test — nothing fails if it comes back —
// and reintroducing an alias is exactly the kind of thing a later "restore
// compatibility" change does. The positive control matters as much as the
// negative one: the walk must find the commands that DO exist, or an empty or
// mis-shaped model would let this pass by seeing nothing at all.
func TestDeprecatedMountCommandsStayAbsent(t *testing.T) {
	var cli CLI
	parser, _, _ := productionParser(t, &cli)
	names := map[string]bool{}
	var walk func(n *kong.Node)
	walk = func(n *kong.Node) {
		if n == nil {
			return
		}
		if n.Type == kong.CommandNode {
			names[n.Name] = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(parser.Model.Node)

	// Positive control first.
	for _, live := range []string{"deploy", "daemon", "caretaker"} {
		if !names[live] {
			t.Fatalf("the command walk did not find %q, so it is not seeing the command tree and the "+
				"absence checks below prove nothing (found %d commands)", live, len(names))
		}
	}
	for _, gone := range []string{"mount-agent", "mountcheck"} {
		if names[gone] {
			t.Errorf("the deprecated %q command is back in the CLI surface. It was removed deliberately "+
				"(no published release carried it); if it is being restored on purpose, record the reason "+
				"in .agents/docs/TODO.md and delete this check.", gone)
		}
	}
}
