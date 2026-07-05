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
	sess, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"})
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
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	writeAll(t, shell, "Thinking… (esc to interrupt)\r\n")
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
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"})
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
	sess, err := mgr.Create(context.Background(), "web", []string{"claude"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)
	// Drain stdin so sess.input (a blocking net.Pipe write) never wedges.
	go func() { _, _ = io.Copy(io.Discard, shell) }()

	writeAll(t, shell, "Do you want to proceed? [y/n]\r\n")
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
	sess, err := mgr.Create(context.Background(), "web", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { mgr.Kill(sess.id) })
	shell := shellOf(t, fe)

	// Bogus CSI/OSC/8-bit noise, then a real prompt.
	writeAll(t, shell, "\x1b[999;999Z\x1b]raw\x07\xc3\x28\x1b[?garbage")
	writeAll(t, shell, "\r\nOverwrite existing file?\r\n")
	eventually(t, "detects prompt after garbage", func() bool {
		return stateOf(t, mgr, sess) == stateBlocked
	})
}

// TestDetectUserOverrideRule: a user rule file under the config dir extends the
// built-in rules. Linux-only because it drives os.UserConfigDir via XDG.
func TestDetectUserOverrideRule(t *testing.T) {
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
	sess, err := mgr.Create(context.Background(), "web", nil)
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
	blocked := []string{
		"Do you want to proceed?",
		"Continue? [y/n]",
		"❯ 1. Yes",
		"(Use arrow keys)",
		"Press enter to continue",
		"Enter passphrase:",
	}
	for _, s := range blocked {
		if !rs.matches(stateBlocked, "", s) {
			t.Errorf("expected %q to match a blocked rule", s)
		}
	}
	working := []string{"esc to interrupt", "Generating...", "⠹ building"}
	for _, s := range working {
		if !rs.matches(stateWorking, "", s) {
			t.Errorf("expected %q to match a working rule", s)
		}
	}
	// Ordinary shell output is neither.
	if rs.matches(stateBlocked, "", "$ ls -la") || rs.matches(stateWorking, "", "$ ls -la") {
		t.Error("plain shell output should match no rule")
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
