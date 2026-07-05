package clientconn

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverProjectContextWalkUp finds a file in an ancestor directory when the
// walk starts from a nested subdirectory.
func TestDiscoverProjectContextWalkUp(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "proj")
	sub := filepath.Join(proj, "a", "b")
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(proj, "cornus-context.yaml"), "server: http://demo\n")

	got, err := (&Resolver{workDir: sub}).discoverProjectContext()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(proj, "cornus-context.yaml"); got != want {
		t.Fatalf("discoverProjectContext() = %q, want %q", got, want)
	}
}

// TestDiscoverProjectContextNearestWins prefers the nearest ancestor's file.
func TestDiscoverProjectContextNearestWins(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "proj")
	sub := filepath.Join(proj, "sub")
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(proj, "cornus-context.yaml"), "server: http://parent\n")
	mustWrite(t, filepath.Join(sub, "cornus-context.yaml"), "server: http://child\n")

	got, _ := (&Resolver{workDir: sub}).discoverProjectContext()
	if want := filepath.Join(sub, "cornus-context.yaml"); got != want {
		t.Fatalf("nearest ancestor should win: got %q, want %q", got, want)
	}
}

// TestDiscoverProjectContextPriority uses the ProjectContextNames order when more
// than one candidate is present in the same directory (.json before .yaml).
func TestDiscoverProjectContextPriority(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cornus-context.yaml"), "server: http://y\n")
	mustWrite(t, filepath.Join(dir, "cornus-context.json"), `{"server":"http://j"}`)

	got, _ := (&Resolver{workDir: dir}).discoverProjectContext()
	if want := filepath.Join(dir, "cornus-context.json"); got != want {
		t.Fatalf("priority: got %q, want %q", got, want)
	}
}

// TestDiscoverProjectContextNotFound returns "" (no error) when nothing is found.
func TestDiscoverProjectContextNotFound(t *testing.T) {
	got, err := (&Resolver{workDir: t.TempDir()}).discoverProjectContext()
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("no file: got %q, want empty", got)
	}
}

// TestDiscoverProjectContextStopsAtGitRoot bounds the walk at a repository root: a
// file above the repo is not found, one at the repo root is.
func TestDiscoverProjectContextStopsAtGitRoot(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	sub := filepath.Join(repo, "sub")
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(tmp, "cornus-context.yaml"), "server: http://above\n")

	if got, _ := (&Resolver{workDir: sub}).discoverProjectContext(); got != "" {
		t.Fatalf("walk should stop at the repo root, got %q", got)
	}
	// A file AT the repo root is still found.
	mustWrite(t, filepath.Join(repo, "cornus-context.yaml"), "server: http://root\n")
	if got, _ := (&Resolver{workDir: sub}).discoverProjectContext(); got != filepath.Join(repo, "cornus-context.yaml") {
		t.Fatalf("repo-root file should be found, got %q", got)
	}
}

// TestDiscoverProjectContextStopsAtHome bounds the walk at the home directory.
func TestDiscoverProjectContextStopsAtHome(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	proj := filepath.Join(home, "proj")
	mustMkdir(t, proj)
	t.Setenv("HOME", home)
	mustWrite(t, filepath.Join(tmp, "cornus-context.yaml"), "server: http://above-home\n")

	if got, _ := (&Resolver{workDir: proj}).discoverProjectContext(); got != "" {
		t.Fatalf("walk should stop at the home dir, got %q", got)
	}
}

// TestProjectOverrideFlags covers the explicit-path, missing-explicit, disable, and
// conflicting-flags branches of projectOverride.
func TestProjectOverrideFlags(t *testing.T) {
	dir := t.TempDir()

	t.Run("explicit path honored (and trusted for its fields)", func(t *testing.T) {
		p := filepath.Join(dir, "explicit.toml")
		mustWrite(t, p, "server = \"http://explicit\"\n")
		ov, path, err := (&Resolver{ProjectContextFile: p}).projectOverride()
		if err != nil {
			t.Fatal(err)
		}
		if ov == nil || ov.Server != "http://explicit" {
			t.Fatalf("explicit override not loaded/honored: %+v", ov)
		}
		if path != p {
			t.Fatalf("path = %q, want %q", path, p)
		}
	})

	t.Run("missing explicit errors", func(t *testing.T) {
		r := &Resolver{ProjectContextFile: filepath.Join(dir, "does-not-exist.yaml")}
		if _, _, err := r.projectOverride(); err == nil {
			t.Fatal("missing explicit --context-file should error")
		}
	})

	t.Run("no-context-file disables discovery", func(t *testing.T) {
		d := t.TempDir()
		mustWrite(t, filepath.Join(d, "cornus-context.yaml"), "via-server: true\n")
		ov, _, err := (&Resolver{workDir: d, NoProjectContext: true}).projectOverride()
		if err != nil {
			t.Fatal(err)
		}
		if ov != nil {
			t.Fatalf("--no-context-file should skip discovery, got %+v", ov)
		}
	})

	t.Run("explicit + no-context-file conflict", func(t *testing.T) {
		r := &Resolver{ProjectContextFile: "x.yaml", NoProjectContext: true}
		if _, _, err := r.projectOverride(); err == nil {
			t.Fatal("--context-file with --no-context-file should conflict")
		}
	})
}

// TestProjectOverrideStripsSensitiveUntrusted: an auto-discovered file may only
// contribute non-sensitive fields; its server/token/tls/... are dropped.
func TestProjectOverrideStripsSensitiveUntrusted(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cornus-context.yaml"), "server: http://evil:9\nvia-server: true\n")

	ov, _, err := (&Resolver{workDir: dir}).projectOverride()
	if err != nil {
		t.Fatal(err)
	}
	if ov == nil {
		t.Fatal("safe fields should still apply; got nil")
	}
	if ov.Server != "" {
		t.Errorf("Server = %q, want stripped from an untrusted auto-discovered file", ov.Server)
	}
	if ov.ViaServer == nil || *ov.ViaServer != true {
		t.Errorf("ViaServer = %v, want the safe field kept", ov.ViaServer)
	}
}

// TestProjectOverrideHonorsSensitiveWhenTrusted: --trust-context-file honors the
// sensitive fields of an auto-discovered file.
func TestProjectOverrideHonorsSensitiveWhenTrusted(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cornus-context.yaml"), "server: http://trusted:9\n")

	ov, _, err := (&Resolver{workDir: dir, TrustProjectContext: true}).projectOverride()
	if err != nil {
		t.Fatal(err)
	}
	if ov == nil || ov.Server != "http://trusted:9" {
		t.Fatalf("trusted override should honor server: %+v", ov)
	}
}

// TestResolveWithProjectOnly: with no global context selected, a trusted project
// override alone defines the connection.
func TestResolveWithProjectOnly(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cornus-context.yaml"),
		"server: http://demo.invalid:9999\nregistry-host: reg.invalid\n")
	r := &Resolver{ConfigFile: filepath.Join(dir, "absent-config.yaml"), workDir: dir, TrustProjectContext: true}

	cn, err := r.ResolveWith("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Cleanup()
	if cn.Endpoint != "http://demo.invalid:9999" {
		t.Errorf("Endpoint = %q, want the project override server", cn.Endpoint)
	}
	if cn.RegistryHost != "reg.invalid" {
		t.Errorf("RegistryHost = %q, want reg.invalid", cn.RegistryHost)
	}
}

// TestResolveWithOverrideMergesOntoBase: the override overlays fields it sets while
// the selected context supplies the rest.
func TestResolveWithOverrideMergesOntoBase(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	mustWrite(t, cfg, "current-context: base\ncontexts:\n  base:\n    server: http://base:1\n    token: base-tok\n")
	proj := filepath.Join(dir, "proj")
	mustMkdir(t, proj)
	mustWrite(t, filepath.Join(proj, "cornus-context.yaml"), "registry-host: proj-reg\n")

	cn, err := (&Resolver{ConfigFile: cfg, workDir: proj, TrustProjectContext: true}).ResolveWith("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Cleanup()
	if cn.Endpoint != "http://base:1" {
		t.Errorf("Endpoint = %q, want base server (override set no server)", cn.Endpoint)
	}
	if cn.Token != "base-tok" {
		t.Errorf("Token = %q, want base-tok", cn.Token)
	}
	if cn.RegistryHost != "proj-reg" {
		t.Errorf("RegistryHost = %q, want proj-reg from override", cn.RegistryHost)
	}
}

// TestResolveWithExplicitBeatsOverride: an explicit endpoint and CORNUS_TOKEN
// (tokenEnv) still win over a trusted project override.
func TestResolveWithExplicitBeatsOverride(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "cornus-context.yaml"), "server: http://override:3\ntoken: ov-tok\n")
	r := &Resolver{ConfigFile: filepath.Join(dir, "absent.yaml"), workDir: dir, TrustProjectContext: true}

	cn, err := r.ResolveWith("http://explicit:2", "env-tok")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Cleanup()
	if cn.Endpoint != "http://explicit:2" {
		t.Errorf("Endpoint = %q, want explicit endpoint", cn.Endpoint)
	}
	if cn.Token != "env-tok" {
		t.Errorf("Token = %q, want env-tok", cn.Token)
	}
}

// TestResolveWithTokenColocationDropsInheritedToken: a trusted override that
// redirects the endpoint but supplies no credential must not inherit the selected
// context's token.
func TestResolveWithTokenColocationDropsInheritedToken(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	mustWrite(t, cfg, "current-context: base\ncontexts:\n  base:\n    server: http://base:1\n    token: secret-tok\n")
	proj := filepath.Join(dir, "proj")
	mustMkdir(t, proj)
	mustWrite(t, filepath.Join(proj, "cornus-context.yaml"), "server: http://redirect:2\n") // no token

	cn, err := (&Resolver{ConfigFile: cfg, workDir: proj, TrustProjectContext: true}).ResolveWith("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Cleanup()
	if cn.Endpoint != "http://redirect:2" {
		t.Errorf("Endpoint = %q, want the override endpoint", cn.Endpoint)
	}
	if cn.Token != "" {
		t.Errorf("Token = %q, want empty: the base token must not follow a credential-less redirect", cn.Token)
	}
}

// TestResolveWithTokenColocationOwnCredKept: an override that redirects and brings
// its own credential keeps it (no drop).
func TestResolveWithTokenColocationOwnCredKept(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	mustWrite(t, cfg, "current-context: base\ncontexts:\n  base:\n    server: http://base:1\n    token: secret-tok\n")
	proj := filepath.Join(dir, "proj")
	mustMkdir(t, proj)
	mustWrite(t, filepath.Join(proj, "cornus-context.yaml"), "server: http://redirect:2\ntoken: own-tok\n")

	cn, err := (&Resolver{ConfigFile: cfg, workDir: proj, TrustProjectContext: true}).ResolveWith("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Cleanup()
	if cn.Token != "own-tok" {
		t.Errorf("Token = %q, want own-tok (override supplied its own credential)", cn.Token)
	}
}

// TestResolveWithExplicitServerExemptFromColocation: an explicit --server is the
// user's own endpoint choice, so the token co-location does not fire.
func TestResolveWithExplicitServerExemptFromColocation(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	mustWrite(t, cfg, "current-context: base\ncontexts:\n  base:\n    token: secret-tok\n")
	proj := filepath.Join(dir, "proj")
	mustMkdir(t, proj)
	mustWrite(t, filepath.Join(proj, "cornus-context.yaml"), "server: http://redirect:2\n")

	cn, err := (&Resolver{ConfigFile: cfg, workDir: proj, TrustProjectContext: true}).ResolveWith("http://explicit:2", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Cleanup()
	if cn.Endpoint != "http://explicit:2" {
		t.Errorf("Endpoint = %q, want explicit", cn.Endpoint)
	}
	if cn.Token != "secret-tok" {
		t.Errorf("Token = %q, want secret-tok kept (explicit endpoint is the user's choice)", cn.Token)
	}
}
