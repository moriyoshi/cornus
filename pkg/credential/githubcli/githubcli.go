// Package githubcli is a credential source that reads the developer's LOCAL
// GitHub CLI login by running `gh auth token`. The workload then rides the
// caller's own `gh auth login` without a token ever entering the image, the
// deploy spec, or the pod spec.
//
// Config keys:
//
//   - "command": the gh binary. Defaults to $CORNUS_GH_BIN when that is set,
//     else "gh" resolved via PATH.
//   - "hostname" (alias "host"): pass --hostname, for GitHub Enterprise Server.
//   - "user": pass --user, to pick one of several logged-in accounts.
//   - "timeout": optional Go duration bounding the run (default 30s).
//   - "key": the credential value name (default "token").
//
// Why shell out rather than read ~/.config/gh/hosts.yml: gh stores the token in
// the OS keyring whenever one is reachable and falls back to the file only
// otherwise, so a file parser works on some machines and silently reports "no
// token" on others. `gh auth token` is the only stable interface. It is also
// non-interactive, prints the secret on stdout alone, and exits non-zero with a
// diagnostic on stderr when there is no login — the same contract the
// "anthropic" backend relies on.
//
// The token is returned under "token", which the github-proxy delivery sends as
// `Authorization: Bearer`, and which the `file` (format raw) and `env` delivery
// kinds already fall back to. No Expiration is set: gh reports none, so the
// caretaker's TTL governs refresh. Because gh refreshes its own OAuth token in
// place, re-running it each TTL is what keeps a long session alive — but each
// run may touch the OS keyring, so prefer an explicit `ttl:` of an hour or so
// over the 5-minute default.
//
// $CORNUS_GH_BIN overrides the "gh" default on the CLIENT, for a machine where
// the binary is absent from PATH or installed under another name. It is a
// per-machine adaptation of a spec that says nothing, so an explicit
// config["command"] — a deliberate statement by whoever wrote the spec, and
// possibly a wrapper that must not be bypassed — still wins over it. Precedence:
// config["command"] > $CORNUS_GH_BIN > "gh".
//
// Environment inherited from the caller matters: gh honors GH_TOKEN and
// GITHUB_TOKEN (either overrides the credential store outright), and
// GH_ENTERPRISE_TOKEN for non-github.com hosts. That makes the same spec work in
// CI, and it means a stale GITHUB_TOKEN in a shell rc file silently shadows a
// fresh `gh auth login`. GH_CONFIG_DIR applies if the cornus client runs with a
// different $HOME.
package githubcli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"cornus/pkg/credential"
)

func init() { credential.Register("github-cli", newSource) }

// binEnv names the client-side override for the gh executable, in the same
// CORNUS_*_BIN family as CORNUS_TUNNEL_CLOUDFLARED_BIN / CORNUS_TUNNEL_TAILSCALE_BIN.
// It is deliberately NOT spelled CORNUS_CREDENTIAL_GH_BIN: the generic delivery
// injects CORNUS_CREDENTIAL_<NAME>_URL into the APP container, so for a credential
// named "gh" that spelling would sit one suffix away from a variable on the other
// side of the wire with an unrelated meaning.
const binEnv = "CORNUS_GH_BIN"

type source struct {
	bin     string
	args    []string
	timeout time.Duration
	key     string
}

func newSource(cfg map[string]string) (credential.Source, error) {
	bin := cfg["command"]
	if bin == "" {
		bin = os.Getenv(binEnv)
	}
	if bin == "" {
		bin = "gh"
	}
	args := []string{"auth", "token"}
	// "hostname" is gh's own flag spelling; "host" is accepted as an alias.
	host := cfg["hostname"]
	if host == "" {
		host = cfg["host"]
	}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	if user := cfg["user"]; user != "" {
		args = append(args, "--user", user)
	}
	timeout := 30 * time.Second
	if t := cfg["timeout"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return nil, fmt.Errorf("github-cli: parse timeout: %w", err)
		}
		timeout = d
	}
	key := cfg["key"]
	if key == "" {
		key = "token"
	}
	return &source{bin: bin, args: args, timeout: timeout, key: key}, nil
}

func (s *source) Fetch(ctx context.Context, _ map[string]string) (credential.Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, s.bin, s.args...)
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return credential.Credential{}, fmt.Errorf("github-cli: %s %s: %w: %s",
			s.bin, strings.Join(s.args, " "), err, strings.TrimSpace(stderr.String()))
	}
	tok := strings.TrimSpace(stdout.String())
	if tok == "" {
		return credential.Credential{}, fmt.Errorf("github-cli: no token from %s (is the GitHub CLI logged in? try `gh auth login`)", s.bin)
	}
	return credential.Credential{Values: map[string]string{s.key: tok}}, nil
}
