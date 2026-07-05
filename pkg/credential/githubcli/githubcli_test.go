package githubcli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/credential"
	_ "cornus/pkg/credential/githubcli"
)

// writeStub writes an executable shell stub standing in for `gh` and returns its
// path. Every test drives the backend through credential.Open, so registration
// under the "github-cli" name is part of what is under test.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh-stub.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGitHubCLISource(t *testing.T) {
	// The stub asserts the exact argv the backend documents, then prints a token.
	stub := writeStub(t, `[ "$1" = auth ] && [ "$2" = token ] && [ $# -eq 2 ] || { echo "unexpected args: $*" >&2; exit 2; }; printf 'gho_livetoken\n'`)
	src, err := credential.Open("github-cli", map[string]string{"command": stub})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["token"] != "gho_livetoken" {
		t.Fatalf("token = %q (values %v)", got.Values["token"], got.Values)
	}
	if !got.Expiration.IsZero() {
		t.Fatalf("expiration = %v, want zero (gh reports none, so TTL governs)", got.Expiration)
	}
}

func TestGitHubCLISourceHostnameAndUser(t *testing.T) {
	// GitHub Enterprise Server plus multi-account selection: both flags appended,
	// in the documented order.
	stub := writeStub(t, `printf '%s\n' "$*"`)
	for _, tc := range []struct {
		name string
		cfg  map[string]string
		want string
	}{
		{"hostname", map[string]string{"hostname": "ghe.corp"}, "auth token --hostname ghe.corp"},
		{"host alias", map[string]string{"host": "ghe.corp"}, "auth token --hostname ghe.corp"},
		{"hostname wins over alias", map[string]string{"hostname": "a.corp", "host": "b.corp"}, "auth token --hostname a.corp"},
		{"user", map[string]string{"user": "octocat"}, "auth token --user octocat"},
		{"both", map[string]string{"hostname": "ghe.corp", "user": "octocat"}, "auth token --hostname ghe.corp --user octocat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]string{"command": stub}
			for k, v := range tc.cfg {
				cfg[k] = v
			}
			src, err := credential.Open("github-cli", cfg)
			if err != nil {
				t.Fatal(err)
			}
			got, err := src.Fetch(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Values["token"] != tc.want {
				t.Fatalf("argv = %q, want %q", got.Values["token"], tc.want)
			}
		})
	}
}

// TestGitHubCLIBinEnvOverride pins the whole precedence chain
// (config["command"] > $CORNUS_GH_BIN > "gh"). Each stub prints an identifying
// token, so the assertion names WHICH binary ran rather than merely that one did.
func TestGitHubCLIBinEnvOverride(t *testing.T) {
	fromEnv := writeStub(t, `printf 'from-env\n'`)
	fromCfg := writeStub(t, `printf 'from-config\n'`)

	t.Run("env supplies the binary when the spec is silent", func(t *testing.T) {
		t.Setenv("CORNUS_GH_BIN", fromEnv)
		src, err := credential.Open("github-cli", nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := src.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Values["token"] != "from-env" {
			t.Fatalf("token = %q, want the env-named binary to have run", got.Values["token"])
		}
	})

	t.Run("explicit config beats the env", func(t *testing.T) {
		t.Setenv("CORNUS_GH_BIN", fromEnv)
		src, err := credential.Open("github-cli", map[string]string{"command": fromCfg})
		if err != nil {
			t.Fatal(err)
		}
		got, err := src.Fetch(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Values["token"] != "from-config" {
			t.Fatalf("token = %q, want config[\"command\"] to win over $CORNUS_GH_BIN", got.Values["token"])
		}
	})

	// The "set but empty" and "unset" cases must resolve to "gh" rather than
	// exec'ing the empty string. They are pinned in githubcli_internal_test.go,
	// which can read the resolved path directly — asserting through Fetch would
	// pass vacuously on any machine that happens to have a working `gh`.
}

func TestGitHubCLISourceKeyOverride(t *testing.T) {
	stub := writeStub(t, `printf 'ghp_pat\n'`)
	src, err := credential.Open("github-cli", map[string]string{"command": stub, "key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["value"] != "ghp_pat" || got.Values["token"] != "" {
		t.Fatalf("values = %v, want the token under \"value\" only", got.Values)
	}
}

// TestGitHubCLISourceEmpty covers a stub that succeeds but prints nothing: an
// empty credential must be an error, not a silently empty token that would later
// authenticate as nobody.
func TestGitHubCLISourceEmpty(t *testing.T) {
	stub := writeStub(t, `exit 0`)
	src, err := credential.Open("github-cli", map[string]string{"command": stub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Fetch(context.Background(), nil); err == nil {
		t.Fatal("expected an error for empty output")
	} else if !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("error %q should name the remediation", err)
	}
}

// TestGitHubCLISourceFailure mirrors a logged-out gh: exit 1 with the reason on
// stderr. The reason must survive into the error, since it is the only thing the
// caller sees over the relay (errors cross the wire as strings).
func TestGitHubCLISourceFailure(t *testing.T) {
	stub := writeStub(t, `echo "no oauth token found for github.com" >&2; exit 1`)
	src, err := credential.Open("github-cli", map[string]string{"command": stub})
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Fetch(context.Background(), nil)
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "no oauth token found") {
		t.Fatalf("error %q dropped the stub's stderr", err)
	}
}

func TestGitHubCLISourceBadTimeout(t *testing.T) {
	if _, err := credential.Open("github-cli", map[string]string{"timeout": "soon"}); err == nil {
		t.Fatal("expected a config error for an unparseable timeout")
	}
}
