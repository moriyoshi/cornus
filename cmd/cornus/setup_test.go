package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/setupwiz"
	"cornus/pkg/clientconfig"
)

// TestSetupJSONRefused proves setup refuses json mode and points at the
// scriptable path.
func TestSetupJSONRefused(t *testing.T) {
	cli := &CLI{}
	cli.drv = cliout.New(cliout.Options{Output: "json"})
	err := (&SetupCmd{}).Run(cli)
	if err == nil || !strings.Contains(err.Error(), "config set-context") {
		t.Fatalf("json setup err = %v, want a message naming config set-context", err)
	}
}

// TestSetupNonTTYScripted drives the whole wizard through scripted stdin in plain
// mode (the non-TTY fallback) and asserts the context is saved to the honored
// --config-file path.
func TestSetupNonTTYScripted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	var buf strings.Builder
	// Local scenario: scenario=1, server-set-up=N(default), runtime=1(default),
	// server(default), name(default), make-current(default Y).
	cli.drv = cliout.New(cliout.Options{
		Stdout: &buf, Stderr: &buf,
		Stdin:  strings.NewReader("1\n\n\n\n\n\n"),
		Output: "plain",
	})
	if err := (&SetupCmd{}).Run(cli); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, err := clientconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := f.Contexts["local"]
	if c == nil || c.Server != "http://127.0.0.1:5000" {
		t.Fatalf("local context not saved to --config-file: %+v", c)
	}
	if f.CurrentContext != "local" {
		t.Errorf("current-context = %q, want local", f.CurrentContext)
	}
}

// TestSetupScenarioPresetSkipsPicker drives the wizard with --scenario through
// scripted stdin: the opening picker answer is absent from the script, so the
// flow only completes if the preset really replaced it.
func TestSetupScenarioPresetSkipsPicker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	var buf strings.Builder
	// No scenario line: server-set-up=N, runtime(default), server(default),
	// name(default), make-current(default Y).
	cli.drv = cliout.New(cliout.Options{
		Stdout: &buf, Stderr: &buf,
		Stdin:  strings.NewReader("\n\n\n\n\n"),
		Output: "plain",
	})
	if err := (&SetupCmd{Scenario: "local"}).Run(cli); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, err := clientconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c := f.Contexts["local"]; c == nil || c.Server != "http://127.0.0.1:5000" {
		t.Fatalf("preset local context: %+v", c)
	}
	if strings.Contains(buf.String(), "Which deployment scenario") {
		t.Error("the scenario picker was shown despite --scenario")
	}
}

// An unknown --scenario must fail before any prompting, naming the valid choices.
func TestSetupUnknownScenarioRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	var buf strings.Builder
	cli.drv = cliout.New(cliout.Options{Stdout: &buf, Stderr: &buf, Stdin: strings.NewReader(""), Output: "plain"})
	err := (&SetupCmd{Scenario: "kubernetes"}).Run(cli)
	if err == nil {
		t.Fatal("unknown scenario accepted")
	}
	for _, name := range setupwiz.ScenarioNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not list the valid name %q", err, name)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("a rejected scenario must not start the wizard; output was %q", buf.String())
	}
}

// TestSetContextCommandRoundTrip feeds the wizard's generated set-context flags
// through a real ConfigSetContextCmd (parsed by kong) and asserts the stored
// context equals BuildContext(a) apart from the redacted token — proving the
// equivalent command cannot drift from what the wizard saves, and that
// --registry-host is wired.
func TestSetContextCommandRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	a := setupwiz.Answers{
		Server:       "https://prod:8443",
		RegistryHost: "reg.example.com:5000",
		Token:        "realtoken",
		CACert:       "/ca.pem",
		ServerName:   "prod.example.com",
	}
	want := setupwiz.BuildContext(a)

	args := []string{"--config-file", path, "config", "set-context"}
	args = append(args, setupwiz.SetContextArgs("prod", want)...)
	args = append(args, "--no-detect")

	// Stub the first-context default prompt so the parse+run is non-interactive.
	restore := confirmSetDefaultContext
	confirmSetDefaultContext = func(*cliout.Driver, string) bool { return false }
	defer func() { confirmSetDefaultContext = restore }()

	var cli CLI
	parser, err := kong.New(&cli, kong.Name("cornus"))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	kctx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	if err := kctx.Run(&cli); err != nil {
		t.Fatalf("run set-context: %v", err)
	}

	f, _ := clientconfig.Load(path)
	got := f.Contexts["prod"]
	if got == nil {
		t.Fatal("prod context not stored")
	}
	// The equivalent command redacts the token, so compare everything else.
	if got.Token != "REDACTED" {
		t.Errorf("stored token = %q, want REDACTED (the wizard redacts)", got.Token)
	}
	got.Token, want.Token = "", ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}
