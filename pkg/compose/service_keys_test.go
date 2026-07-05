package compose

import (
	"strings"
	"testing"
)

// TestServiceKeysTranslate asserts the batch of common runtime keys (user,
// working_dir, hostname, labels, stop_signal, stop_grace_period, init, tty,
// stdin_open, read_only) parse and populate the matching api.DeploySpec fields.
func TestServiceKeysTranslate(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    user: "1000:2000"
    working_dir: /srv
    hostname: web-host
    labels:
      com.example.tier: frontend
      role: web
    stop_signal: SIGINT
    stop_grace_period: 1m30s
    init: true
    tty: true
    stdin_open: true
    read_only: true
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	spec := plans["web"].Spec

	if spec.User != "1000:2000" {
		t.Errorf("User = %q, want 1000:2000", spec.User)
	}
	if spec.WorkingDir != "/srv" {
		t.Errorf("WorkingDir = %q, want /srv", spec.WorkingDir)
	}
	if spec.Hostname != "web-host" {
		t.Errorf("Hostname = %q, want web-host", spec.Hostname)
	}
	if spec.Labels["com.example.tier"] != "frontend" || spec.Labels["role"] != "web" {
		t.Errorf("Labels = %v, want tier=frontend role=web", spec.Labels)
	}
	if spec.StopSignal != "SIGINT" {
		t.Errorf("StopSignal = %q, want SIGINT", spec.StopSignal)
	}
	if spec.StopGracePeriod != "1m30s" {
		t.Errorf("StopGracePeriod = %q, want 1m30s", spec.StopGracePeriod)
	}
	if spec.Init == nil || !*spec.Init {
		t.Errorf("Init = %v, want true", spec.Init)
	}
	if !spec.TTY {
		t.Error("TTY = false, want true")
	}
	if !spec.StdinOpen {
		t.Error("StdinOpen = false, want true")
	}
	if !spec.ReadOnly {
		t.Error("ReadOnly = false, want true")
	}
}

// TestServiceKeysLabelsListForm asserts the KEY=VALUE list form of labels
// decodes just like the map form.
func TestServiceKeysLabelsListForm(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    labels:
      - "role=web"
      - "tier=frontend"
`)
	proj, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc := proj.Services()["web"]
	if svc.Labels["role"] != "web" || svc.Labels["tier"] != "frontend" {
		t.Errorf("Labels = %v, want role=web tier=frontend", svc.Labels)
	}
}

// TestServiceKeysNotWarned asserts none of the ten batch keys produce an
// unsupported-field warning once plumbed through.
func TestServiceKeysNotWarned(t *testing.T) {
	file := writeCompose(t, `
services:
  web:
    image: nginx
    user: "1000"
    working_dir: /srv
    hostname: web-host
    labels:
      a: b
    stop_signal: SIGINT
    stop_grace_period: 10s
    init: true
    tty: true
    stdin_open: true
    read_only: true
`)
	lines := captureWarnings(t, func() {
		if _, err := Load(file); err != nil {
			t.Fatalf("Load: %v", err)
		}
	})
	for _, key := range []string{"user", "working_dir", "hostname", "labels", "stop_signal", "stop_grace_period", "init", "tty", "stdin_open", "read_only"} {
		for _, ln := range lines {
			if strings.Contains(ln, `field "`+key+`"`) {
				t.Errorf("key %q unexpectedly warned: %s", key, ln)
			}
		}
	}
}

// TestServiceKeysMerge asserts the batch keys deep-merge across files (override
// wins on scalars/pointer, labels merge key-by-key).
func TestServiceKeysMerge(t *testing.T) {
	dir := t.TempDir()
	base := writeFiles(t, map[string]string{"compose.yaml": `
services:
  web:
    image: nginx
    user: "1000"
    working_dir: /base
    labels:
      a: base
      keep: kept
`})
	_ = dir
	override := writeFiles(t, map[string]string{"compose.yaml": `
services:
  web:
    image: nginx
    working_dir: /override
    hostname: over-host
    labels:
      a: override
    read_only: true
`})
	proj, err := Load(base, override)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc := proj.Services()["web"]
	if svc.User != "1000" {
		t.Errorf("User = %q, want 1000 (kept from base)", svc.User)
	}
	if svc.WorkingDir != "/override" {
		t.Errorf("WorkingDir = %q, want /override", svc.WorkingDir)
	}
	if svc.Hostname != "over-host" {
		t.Errorf("Hostname = %q, want over-host", svc.Hostname)
	}
	if svc.Labels["a"] != "override" || svc.Labels["keep"] != "kept" {
		t.Errorf("Labels = %v, want a=override keep=kept", svc.Labels)
	}
	if !svc.ReadOnly {
		t.Error("ReadOnly = false, want true (from override)")
	}
}

// TestServiceKeysRestart covers the generous `restart:` decoding: the string
// policies parse as-is, the bare boolean `no` (YAML-coerced to false) becomes
// "no", and `true` plus unknown strings are rejected at parse time.
func TestServiceKeysRestart(t *testing.T) {
	ok := []struct{ in, want string }{
		{`"no"`, "no"},
		{"no", "no"}, // YAML coerces bare `no` to false -> "no"
		{"always", "always"},
		{"unless-stopped", "unless-stopped"},
		{"on-failure", "on-failure"},
		{"on-failure:5", "on-failure:5"},
	}
	for _, tc := range ok {
		file := writeCompose(t, "services:\n  web:\n    image: nginx\n    restart: "+tc.in+"\n")
		proj, err := Load(file)
		if err != nil {
			t.Fatalf("restart: %s: Load: %v", tc.in, err)
		}
		if got := string(proj.Services()["web"].Restart); got != tc.want {
			t.Errorf("restart: %s => %q, want %q", tc.in, got, tc.want)
		}
	}

	bad := []string{"yes", "true", "on", "sometimes", "on-failure:-1", "on-failure:abc"}
	for _, in := range bad {
		file := writeCompose(t, "services:\n  web:\n    image: nginx\n    restart: "+in+"\n")
		if _, err := Load(file); err == nil {
			t.Errorf("restart: %s: expected an error, got nil", in)
		} else if !strings.Contains(err.Error(), "restart") {
			t.Errorf("restart: %s: error %q does not mention restart", in, err)
		}
	}
}

// TestServiceKeysRestartSplit asserts the `on-failure:N` short form splits into
// the bare policy word plus RestartMaxAttempts in the translated spec (matching
// the api.DeploySpec contract), and that deploy.restart_policy stays
// authoritative over both.
func TestServiceKeysRestartSplit(t *testing.T) {
	specFor := func(t *testing.T, yaml string) (string, int) {
		t.Helper()
		proj, err := Load(writeCompose(t, yaml))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		plans, err := proj.Plan("proj")
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		spec := plans["web"].Spec
		return spec.Restart, spec.RestartMaxAttempts
	}

	// Short form: policy word extracted, count plumbed into RestartMaxAttempts.
	if pol, n := specFor(t, "services:\n  web:\n    image: nginx\n    restart: on-failure:5\n"); pol != "on-failure" || n != 5 {
		t.Errorf("on-failure:5 => (%q, %d), want (\"on-failure\", 5)", pol, n)
	}
	// Bare on-failure: no count, backend default (0 = unlimited).
	if pol, n := specFor(t, "services:\n  web:\n    image: nginx\n    restart: on-failure\n"); pol != "on-failure" || n != 0 {
		t.Errorf("on-failure => (%q, %d), want (\"on-failure\", 0)", pol, n)
	}
	// deploy.restart_policy is authoritative over the service-level short form.
	yaml := "services:\n  web:\n    image: nginx\n    restart: on-failure:5\n" +
		"    deploy:\n      restart_policy:\n        condition: on-failure\n        max_attempts: 3\n"
	if pol, n := specFor(t, yaml); pol != "on-failure" || n != 3 {
		t.Errorf("deploy override => (%q, %d), want (\"on-failure\", 3)", pol, n)
	}
}
