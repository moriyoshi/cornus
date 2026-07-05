package webbff

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fastDetectManager makes a manager whose detectors settle almost immediately, so
// state transitions are observable within the shared `eventually` 2s deadline.
func fastDetectManager(t *testing.T, fe *fakeExec) *termManager {
	t.Helper()
	mgr := newTermManager(fe)
	mgr.detSettle = 15 * time.Millisecond
	return mgr
}

func stateOf(t *testing.T, mgr *termManager, sess *termSession) sessionState {
	t.Helper()
	got := mgr.Get(sess.id)
	if got == nil {
		return ""
	}
	return sessionState(got.info().State)
}

// TestDetectIdleAfterOutput: a burst of ordinary output reads as working, then
// settles to idle once the screen goes quiet with no prompt visible.
func TestDetectIdleAfterOutput(t *testing.T) {
	fe := &fakeExec{}
	mgr := fastDetectManager(t, fe)
	sess, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	writeAll(t, shell, "ok done\r\n")
	eventually(t, "settles to idle", func() bool { return stateOf(t, mgr, sess) == stateIdle })
}

// TestDetectWorkingIndicator: a persistent "still working" indicator on an
// otherwise quiet screen classifies as working and stays there.
func TestDetectWorkingIndicator(t *testing.T) {
	fe := &fakeExec{}
	mgr := fastDetectManager(t, fe)
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	writeAll(t, shell, "\u2733 Working\u2026 (esc to interrupt)\r\n")
	eventually(t, "is working", func() bool { return stateOf(t, mgr, sess) == stateWorking })

	// With no further output, it must NOT drift to idle.
	time.Sleep(80 * time.Millisecond)
	if got := stateOf(t, mgr, sess); got != stateWorking {
		t.Fatalf("state = %q after quiet, want working (indicator still on screen)", got)
	}
}

// TestDetectBlockedPrompt: a visible approval prompt on a quiet screen is blocked.
func TestDetectBlockedPrompt(t *testing.T) {
	fe := &fakeExec{}
	mgr := fastDetectManager(t, fe)
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	writeAll(t, shell, "Do you want to proceed?\r\n  1. Yes\r\n  2. No\r\n")
	eventually(t, "is blocked", func() bool { return stateOf(t, mgr, sess) == stateBlocked })
}

// TestDetectInputAcksBlocked: answering the prompt (browser stdin) clears blocked
// and, with no new output, the still-visible prompt does not re-block.
func TestDetectInputAcksBlocked(t *testing.T) {
	fe := &fakeExec{}
	mgr := fastDetectManager(t, fe)
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)
	// Drain stdin so sess.input (a blocking net.Pipe write) never wedges.
	go func() { _, _ = io.Copy(io.Discard, shell) }()

	writeAll(t, shell, "Do you want to proceed?\r\n\u276f 1. Yes\r\n  2. No\r\n")
	eventually(t, "is blocked", func() bool { return stateOf(t, mgr, sess) == stateBlocked })

	sess.input([]byte("y\r"))
	eventually(t, "clears blocked", func() bool { return stateOf(t, mgr, sess) == stateIdle })

	// The prompt text is still on screen, but it was acknowledged: stays cleared.
	time.Sleep(80 * time.Millisecond)
	if got := stateOf(t, mgr, sess); got == stateBlocked {
		t.Fatalf("state re-blocked on an already-answered prompt")
	}
}

// TestDetectToleratesGarbage: malformed escape sequences must not break detection
// — a clean prompt after the garbage still classifies as blocked.
func TestDetectToleratesGarbage(t *testing.T) {
	fe := &fakeExec{}
	mgr := fastDetectManager(t, fe)
	// Launched straight into an agent. The subject here is TOLERANCE of garbage,
	// not rule scoping — and the built-in rules are scoped to the foreground
	// program, so a plain shell would settle to idle for the right reason and make
	// this test pass or fail on something it is not about.
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"}, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	// Bogus CSI/OSC/8-bit noise, then a real prompt.
	writeAll(t, shell, "\x1b[999;999Z\x1b]raw\x07\xc3\x28\x1b[?garbage")
	writeAll(t, shell, "\r\nDo you want to proceed?\r\n\u276f 1. Yes\r\n  2. No\r\n")
	eventually(t, "detects prompt after garbage", func() bool {
		return stateOf(t, mgr, sess) == stateBlocked
	})
}

// TestDetectUserOverrideRule: a user rule file under the config dir extends the
// built-in rules. Linux-only because it drives os.UserConfigDir via XDG.
func TestDetectUserOverrideRule(t *testing.T) {
	// SUPERSEDED. Classification now runs on the per-agent herdr manifests
	// (pkg/agentdetect); this cornus-native rule schema no longer reaches the
	// detector, so the user-extension path under ~/.config/cornus/agent-detection/
	// currently affects nothing. Kept as a marker rather than deleted: the path is
	// user-facing and needs migrating to the manifest schema or retiring on
	// purpose, not by silent attrition. See TODO.
	t.Skip("user rule files are superseded by the herdr manifests; see TODO")
	if runtime.GOOS != "linux" {
		t.Skip("override path exercises XDG_CONFIG_HOME (linux)")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	dir := filepath.Join(cfg, "cornus", "agent-detection")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rule := "[[rule]]\nstate = \"blocked\"\npattern = '(?i)awaiting captain input'\n"
	if err := os.WriteFile(filepath.Join(dir, "custom.toml"), []byte(rule), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}

	fe := &fakeExec{}
	mgr := fastDetectManager(t, fe) // loadRules() runs here, after the env is set
	sess, err := mgr.Create(context.Background(), "web", nil, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	writeAll(t, shell, "AWAITING CAPTAIN INPUT\r\n")
	eventually(t, "user rule blocks", func() bool { return stateOf(t, mgr, sess) == stateBlocked })
}

// TestDefaultRulesMatch checks the embedded rules load and recognise a spread of
// representative prompts/indicators — a guard on rules.toml itself.
func TestDefaultRulesMatch(t *testing.T) {
	rs := loadRules()
	if len(rs.rules) == 0 {
		t.Fatal("no rules loaded from embedded rules.toml")
	}
	// Every built-in rule is SCOPED to the programs it was written for, so each
	// sample is checked against a foreground program that rule applies to. The
	// agent argument is the LIVE foreground program, not the launch argv.
	blocked := []struct{ agent, screen string }{
		{"claude", "Do you want to proceed?"},
		{"claude", "Continue? [y/n]"},
		{"claude", "❯ 1. Yes"},
		{"claude", "(Use arrow keys)"},
		{"apt", "Press enter to continue"},
		{"ssh", "Enter passphrase:"},
		{"sudo", "Password:"},
	}
	for _, tc := range blocked {
		if !rs.matches(stateBlocked, tc.agent, tc.screen) {
			t.Errorf("expected %q under agent %q to match a blocked rule", tc.screen, tc.agent)
		}
	}
	working := []string{"esc to interrupt", "Generating...", "⠹ building"}
	for _, s := range working {
		if !rs.matches(stateWorking, "claude", s) {
			t.Errorf("expected %q to match a working rule", s)
		}
	}
	// Ordinary shell output is neither, whoever is in front.
	if rs.matches(stateBlocked, "claude", "$ ls -la") || rs.matches(stateWorking, "claude", "$ ls -la") {
		t.Error("plain shell output should match no rule")
	}
}

// The scoping IS the feature: the same words are a prompt when the program showing
// them is prompting, and are just text when the program showing them is `cat`,
// `less`, `git log` or `grep`. Before the built-in rules carried a scope, all four
// of those classified a session as needing attention.
func TestDefaultRulesDoNotFireOnProgramsMerelyDisplayingText(t *testing.T) {
	rs := loadRules()
	// Screens a user is simply LOOKING at, with the foreground program that put
	// them there. Every one of these was "blocked" before scoping.
	cases := []struct{ agent, screen string }{
		{"cat", "Run the installer and answer: Do you want to enable telemetry?"},
		{"less", "- fixed the [y/n] prompt in the uninstaller"},
		{"git", "abc1234 delete the stale mirror?"},
		{"grep", "src/tui.go:  press enter to continue"},
		{"bat", "❯ 1. Yes"},
		// An UNKNOWN foreground program (the probe could not name it) matches no
		// scoped rule. Conservative by design: a missed prompt in a session we
		// cannot identify beats a false "needs you" on every session.
		{"", "Do you want to proceed?"},
		{"", "Continue? [y/n]"},
	}
	for _, tc := range cases {
		if rs.matches(stateBlocked, tc.agent, tc.screen) {
			t.Errorf("agent %q merely displaying %q was classified as blocked", tc.agent, tc.screen)
		}
	}

	// `git` IS scoped for credential prompts, so it must still block on one — the
	// scope is per rule, not a blanket allow/deny per program.
	if !rs.matches(stateBlocked, "git", "Password for 'https://example.com':") {
		t.Error("git prompting for a password should still block")
	}
}

func TestAgentName(t *testing.T) {
	cases := map[string]struct {
		cmd  []string
		want string
	}{
		"claude":     {[]string{"claude", "--flag"}, "claude"},
		"abs path":   {[]string{"/usr/local/bin/codex"}, "codex"},
		"plain sh":   {[]string{"/bin/sh"}, ""},
		"bash":       {[]string{"bash", "-l"}, ""},
		"empty":      {nil, ""},
		"just slash": {[]string{"/"}, ""},
	}
	for name, tc := range cases {
		if got := agentName(tc.cmd); got != tc.want {
			t.Errorf("%s: agentName(%v) = %q, want %q", name, tc.cmd, got, tc.want)
		}
	}
}
