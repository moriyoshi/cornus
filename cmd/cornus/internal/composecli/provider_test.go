package composecli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"cornus/pkg/compose"
)

// TestParseProviderStream covers the provider plugin stdout JSON protocol: info
// lines are forwarded, setenv is prefixed with the service name, rawsetenv passes
// through, an error message becomes the returned error, and non-JSON lines are
// forwarded verbatim rather than failing the parse.
func TestParseProviderStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"info","message":"creating database"}`,
		`{"type":"debug","message":"internal detail"}`,
		`plain text progress line`,
		`{"type":"setenv","message":"URL=mysql://host:3306"}`,
		`{"type":"setenv","message":"TOKEN=abc=def"}`,
		`{"type":"rawsetenv","message":"GLOBAL_KEY=raw"}`,
		``,
	}, "\n")

	var infos []string
	env, err := parseProviderStream(strings.NewReader(stream), "database", func(m string) {
		infos = append(infos, m)
	})
	if err != nil {
		t.Fatalf("parseProviderStream error: %v", err)
	}
	if env["DATABASE_URL"] != "mysql://host:3306" {
		t.Errorf("DATABASE_URL = %q, want mysql://host:3306", env["DATABASE_URL"])
	}
	// The value itself may contain '=': only the first '=' splits key from value.
	if env["DATABASE_TOKEN"] != "abc=def" {
		t.Errorf("DATABASE_TOKEN = %q, want abc=def", env["DATABASE_TOKEN"])
	}
	if env["GLOBAL_KEY"] != "raw" {
		t.Errorf("GLOBAL_KEY (rawsetenv) = %q, want raw", env["GLOBAL_KEY"])
	}
	if _, ok := env["DATABASE_GLOBAL_KEY"]; ok {
		t.Error("rawsetenv should not be prefixed")
	}
	// info, debug, and the plain-text line all reach the callback.
	if len(infos) != 3 {
		t.Errorf("info callbacks = %d (%v), want 3", len(infos), infos)
	}
}

// TestParseProviderStreamError asserts an `error` message is surfaced.
func TestParseProviderStreamError(t *testing.T) {
	stream := `{"type":"info","message":"trying"}` + "\n" +
		`{"type":"error","message":"quota exceeded"}` + "\n" +
		`{"type":"setenv","message":"URL=late"}`
	env, err := parseProviderStream(strings.NewReader(stream), "db", nil)
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("error = %v, want containing 'quota exceeded'", err)
	}
	// setenv after the error is still collected; the caller discards env on error.
	if env["DB_URL"] != "late" {
		t.Errorf("DB_URL = %q, want late", env["DB_URL"])
	}
}

// TestProviderEnvPrefix covers the service-name normalization for the env prefix.
func TestProviderEnvPrefix(t *testing.T) {
	cases := map[string]string{
		"database": "DATABASE_",
		"my-db":    "MY_DB_",
		"a.b":      "A_B_",
	}
	for in, want := range cases {
		if got := providerEnvPrefix(in); got != want {
			t.Errorf("providerEnvPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInjectProviderEnv asserts a dependent service's spec gains its provider
// dependency's env, while its own environment wins on a key clash and the shared
// plan map is not mutated.
func TestInjectProviderEnv(t *testing.T) {
	svcs := map[string]compose.ServiceDocument{
		"app": {
			Image:       "alpine",
			DependsOn:   compose.DependsOn{{Service: "database", Required: true}},
			Environment: compose.Environment{"OWN": "x", "DATABASE_URL": "override"},
		},
		"database": {Provider: &compose.Provider{Type: "awesomecloud"}},
	}
	proj := compose.NewProject(&compose.ProjectDocument{Services: svcs}).View(nil)
	plans, err := proj.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rt := &runtime{project: proj, plans: plans}
	rt.initProviderState([]string{"app", "database"})
	rt.providers.env["database"] = map[string]string{"DATABASE_URL": "mysql://h", "DATABASE_TOKEN": "t"}

	spec := serviceSpec(rt, nil, "app")
	if spec.Env["DATABASE_TOKEN"] != "t" {
		t.Errorf("DATABASE_TOKEN = %q, want t", spec.Env["DATABASE_TOKEN"])
	}
	if spec.Env["DATABASE_URL"] != "override" {
		t.Errorf("DATABASE_URL = %q, want override (service env wins)", spec.Env["DATABASE_URL"])
	}
	if spec.Env["OWN"] != "x" {
		t.Errorf("OWN = %q, want x", spec.Env["OWN"])
	}
	// The plan's underlying env map must be untouched (no DATABASE_TOKEN there).
	if _, ok := plans["app"].Spec.Env["DATABASE_TOKEN"]; ok {
		t.Error("injection mutated the shared plan env map")
	}
}

// --- runner.run integration via a helper-process fake plugin ------------------

// fakeProviderExec returns an exec seam that re-invokes this test binary as the
// provider plugin, driven by TestProviderHelperProcess.
func fakeProviderExec(t *testing.T) providerExecFunc {
	t.Helper()
	return func(ctx context.Context, bin string, args []string) *exec.Cmd {
		cs := append([]string{"-test.run=TestProviderHelperProcess", "--", bin}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_PROVIDER_HELPER=1")
		return cmd
	}
}

// TestProviderHelperProcess is not a real test: invoked as a subprocess by
// fakeProviderExec, it plays the provider plugin. It emits an info line and a
// setenv on `up`, and fails on a `boom` service to exercise the error path.
func TestProviderHelperProcess(t *testing.T) {
	if os.Getenv("GO_PROVIDER_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	// args: <bin> compose --project-name=<p> <up|down> [flags...] <service>
	service := args[len(args)-1]
	var cmd string
	for _, a := range args {
		if a == "up" || a == "down" {
			cmd = a
		}
	}
	if service == "boom" {
		os.Stdout.WriteString(`{"type":"error","message":"provisioning failed"}` + "\n")
		os.Exit(1)
	}
	if cmd == "up" {
		os.Stdout.WriteString(`{"type":"info","message":"creating"}` + "\n")
		os.Stdout.WriteString(`{"type":"setenv","message":"URL=mysql://h:3306"}` + "\n")
	}
	os.Exit(0)
}

func TestProviderRunnerUp(t *testing.T) {
	if _, err := exec.LookPath("awesomecloud"); err != nil {
		// resolveProviderBinary needs the type on PATH; skip if a same-named binary
		// happens to be absent (the fake exec ignores the resolved path but the
		// lookup still runs). Create a stub on PATH via the test's temp dir.
		dir := t.TempDir()
		stub := dir + "/awesomecloud"
		if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	r := providerRunner{exec: fakeProviderExec(t)}
	plan := compose.ProviderPlan{Type: "awesomecloud", Flags: []string{"--type=mysql"}}
	env, err := r.run(context.Background(), plan, "proj", "database", providerUp, nil)
	if err != nil {
		t.Fatalf("run up: %v", err)
	}
	if env["DATABASE_URL"] != "mysql://h:3306" {
		t.Errorf("DATABASE_URL = %q, want mysql://h:3306", env["DATABASE_URL"])
	}
}

func TestProviderRunnerError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/awesomecloud", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := providerRunner{exec: fakeProviderExec(t)}
	plan := compose.ProviderPlan{Type: "awesomecloud"}
	_, err := r.run(context.Background(), plan, "proj", "boom", providerUp, nil)
	if err == nil || !strings.Contains(err.Error(), "provisioning failed") {
		t.Fatalf("run error = %v, want containing 'provisioning failed'", err)
	}
}
