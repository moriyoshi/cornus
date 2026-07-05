package sshclient

import (
	"os"
	"path/filepath"
	"testing"
)

func aliases(hosts []ConfigHost) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Alias)
	}
	return out
}

func TestConfigHostsListsConnectableAliases(t *testing.T) {
	withConfig(t, `
Host *
  ServerAliveInterval 30

Host devbox
  HostName 10.0.0.5
  User ops
  Port 2222

Host prod prod-eu
  HostName prod.example.com

Host *.internal
  User admin

Host devbox
  # a duplicate declaration must not produce a duplicate entry
  ForwardAgent yes
`)
	hosts := ConfigHosts()
	got := aliases(hosts)
	want := []string{"devbox", "prod", "prod-eu"}
	if len(got) != len(want) {
		t.Fatalf("aliases = %v, want %v (wildcards skipped, duplicates collapsed)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("aliases = %v, want %v (file order)", got, want)
		}
	}
	if hosts[0].HostName != "10.0.0.5" || hosts[0].User != "ops" || hosts[0].Port != "2222" {
		t.Errorf("devbox = %+v, want the effective HostName/User/Port", hosts[0])
	}
	if got, want := hosts[0].Summary(), "ops@10.0.0.5:2222"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
	// prod's port is the default, so it must not be spelled out.
	if got, want := hosts[1].Summary(), "prod.example.com"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

func TestConfigHostsSkipsNegatedPatterns(t *testing.T) {
	withConfig(t, "Host !nope ok\n  HostName h\n")
	if got, want := aliases(ConfigHosts()), 1; len(got) != want {
		t.Fatalf("aliases = %v, want only the non-negated one", got)
	}
}

func TestConfigHostsMissingFileYieldsNil(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config")
	prev := configFinder
	configFinder = func() string { return missing }
	t.Cleanup(func() { configFinder = prev })
	if got := ConfigHosts(); got != nil {
		t.Fatalf("ConfigHosts() = %v, want nil for a missing config (callers fall back to free text)", got)
	}
}

func TestConfigHostsUnparsableFileYieldsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	// Match Exec is rejected outright by the parser (it would execute code).
	if err := os.WriteFile(path, []byte("Match Exec \"true\"\n  User x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := configFinder
	configFinder = func() string { return path }
	t.Cleanup(func() { configFinder = prev })
	if got := ConfigHosts(); got != nil {
		t.Fatalf("ConfigHosts() = %v, want nil for an unparsable config", got)
	}
}

// A summary that would only repeat the alias adds nothing to the picker.
func TestConfigHostSummaryEmptyWhenUninformative(t *testing.T) {
	if got := (ConfigHost{Alias: "box", HostName: "box", Port: "22"}).Summary(); got != "" {
		t.Errorf("Summary = %q, want empty", got)
	}
}
