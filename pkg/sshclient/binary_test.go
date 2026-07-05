package sshclient

import (
	"context"
	"slices"
	"testing"
)

func TestSSHForwardArgs(t *testing.T) {
	// Interactive first connect: no BatchMode, unix-socket local forward.
	got := sshForwardArgs("devbox", "127.0.0.1", "5000", "/run/cornus/t.sock", true)
	want := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", "/run/cornus/t.sock:127.0.0.1:5000", "devbox"}
	if !slices.Equal(got, want) {
		t.Errorf("interactive args = %v, want %v", got, want)
	}

	// Reconnect respawn: BatchMode so it fails instead of prompting.
	got = sshForwardArgs("devbox", "127.0.0.1", "5000", "/run/cornus/t.sock", false)
	if !slices.Contains(got, "BatchMode=yes") {
		t.Errorf("respawn args missing BatchMode: %v", got)
	}
}

func TestRuntimeDirPrecedence(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("CORNUS_AGENT_DIR", "/agent")
	if got := runtimeDir(); got != "/run/user/1000/cornus" {
		t.Errorf("runtimeDir with XDG = %q", got)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := runtimeDir(); got != "/agent" {
		t.Errorf("runtimeDir with CORNUS_AGENT_DIR = %q", got)
	}
}

func TestDialViaBinaryNoSSH(t *testing.T) {
	// With an empty PATH, the ssh binary cannot be found and DialViaBinary errors
	// clearly rather than hanging.
	t.Setenv("PATH", "")
	_, err := DialViaBinary(context.Background(), "devbox", "127.0.0.1:5000", false)
	if err == nil {
		t.Fatal("DialViaBinary with no ssh binary = nil error, want error")
	}
}
