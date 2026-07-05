package composecli

import (
	"reflect"
	"testing"

	"cornus/cmd/cornus/internal/execdrive"
	"cornus/pkg/client"
)

// The live *client.Client must satisfy the execdrive.Client seam the interactive
// drive depends on, so a signature change there is caught at compile time here.
var _ execdrive.Client = (*client.Client)(nil)

// TestResolveExecTTY covers the pseudo-TTY decision for compose exec: on by
// default, off with -T, and downgraded (reported) when stdin is not a terminal.
func TestResolveExecTTY(t *testing.T) {
	cases := []struct {
		name           string
		noTTY          bool
		stdinIsTerm    bool
		wantTTY        bool
		wantDowngraded bool
	}{
		{"default terminal", false, true, true, false},
		{"default pipe downgrades", false, false, false, true},
		{"-T on terminal", true, true, false, false},
		{"-T on pipe: no downgrade warning", true, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tty, downgraded := resolveExecTTY(tc.noTTY, tc.stdinIsTerm)
			if tty != tc.wantTTY || downgraded != tc.wantDowngraded {
				t.Errorf("resolveExecTTY(%v, %v) = (%v, %v), want (%v, %v)",
					tc.noTTY, tc.stdinIsTerm, tty, downgraded, tc.wantTTY, tc.wantDowngraded)
			}
		})
	}
}

// TestParseExecEnv covers KEY=VALUE, bare-KEY environment lookup, and the
// empty-name rejection for --env.
func TestParseExecEnv(t *testing.T) {
	t.Setenv("CORNUS_EXEC_TEST_ENV", "from-env")

	got, err := parseExecEnv([]string{"A=1", "B=", "CORNUS_EXEC_TEST_ENV", "C=x=y"})
	if err != nil {
		t.Fatalf("parseExecEnv: %v", err)
	}
	want := []string{"A=1", "B=", "CORNUS_EXEC_TEST_ENV=from-env", "C=x=y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseExecEnv = %v, want %v", got, want)
	}

	// A nil/empty input yields nil (no env override).
	if got, err := parseExecEnv(nil); err != nil || got != nil {
		t.Errorf("parseExecEnv(nil) = (%v, %v), want (nil, nil)", got, err)
	}

	// An empty name ("=VALUE") is rejected.
	if _, err := parseExecEnv([]string{"=oops"}); err == nil {
		t.Error("parseExecEnv(=oops) should reject an empty name")
	}
}

// TestExecConfig checks the exec-create config carries the command, resolved env,
// working dir, user, TTY, and privileged flag, and always keeps stdin/stdout/
// stderr attached (interactive by default, like docker compose exec).
func TestExecConfig(t *testing.T) {
	c := &ExecCmd{
		Workdir:    "/srv",
		User:       "app",
		Privileged: true,
		Cmd:        []string{"sh", "-c", "echo hi"},
	}
	cfg := c.execConfig([]string{"A=1"}, true)
	if !reflect.DeepEqual(cfg.Cmd, c.Cmd) {
		t.Errorf("Cmd = %v, want %v", cfg.Cmd, c.Cmd)
	}
	if !reflect.DeepEqual(cfg.Env, []string{"A=1"}) {
		t.Errorf("Env = %v, want [A=1]", cfg.Env)
	}
	if cfg.WorkingDir != "/srv" || cfg.User != "app" || !cfg.Privileged {
		t.Errorf("WorkingDir/User/Privileged = %q/%q/%v", cfg.WorkingDir, cfg.User, cfg.Privileged)
	}
	if !cfg.Tty {
		t.Error("Tty should be true when requested")
	}
	if !cfg.AttachStdin || !cfg.AttachStdout || !cfg.AttachStderr {
		t.Errorf("all std streams should attach: in=%v out=%v err=%v", cfg.AttachStdin, cfg.AttachStdout, cfg.AttachStderr)
	}
	// Without a TTY the config still attaches every stream (piped `-T` input).
	if cfg := c.execConfig(nil, false); cfg.Tty || !cfg.AttachStdin {
		t.Errorf("no-TTY config: Tty=%v AttachStdin=%v (want false/true)", cfg.Tty, cfg.AttachStdin)
	}
}
