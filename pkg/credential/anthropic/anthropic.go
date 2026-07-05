// Package anthropic is a turnkey credential source that reads the developer's
// LOCAL Anthropic credential from the harness-managed store — the `ant auth
// login` profile under ~/.config/anthropic/, which Claude Code and the Claude
// Agent SDK share. It shells out to `ant auth print-credentials --access-token`,
// which reads the store AND refreshes the short-lived OAuth token, so paired with
// the broker's TTL re-fetch the container always sees a live token.
//
// The result is returned under the "oauth_token" key, which the anthropic-proxy
// delivery sends as `Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20`.
//
// Config keys:
//
//   - "command": the ant binary (default "ant"). Overridable for tests / a
//     non-PATH install.
//   - "profile": optional named profile → `--profile <p>`.
//   - "timeout": optional Go duration bounding the run (default 30s).
//
// It is the ergonomic equivalent of an `exec` source running the same command in
// `raw` mode with key "oauth_token".
package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"cornus/pkg/credential"
)

func init() { credential.Register("anthropic", newSource) }

type source struct {
	bin     string
	args    []string
	timeout time.Duration
}

func newSource(cfg map[string]string) (credential.Source, error) {
	bin := cfg["command"]
	if bin == "" {
		bin = "ant"
	}
	args := []string{"auth", "print-credentials", "--access-token"}
	if p := cfg["profile"]; p != "" {
		args = append(args, "--profile", p)
	}
	timeout := 30 * time.Second
	if t := cfg["timeout"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return nil, fmt.Errorf("anthropic: parse timeout: %w", err)
		}
		timeout = d
	}
	return &source{bin: bin, args: args, timeout: timeout}, nil
}

func (s *source) Fetch(ctx context.Context, _ map[string]string) (credential.Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, s.bin, s.args...)
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return credential.Credential{}, fmt.Errorf("anthropic: %s %s: %w: %s", s.bin, strings.Join(s.args, " "), err, strings.TrimSpace(stderr.String()))
	}
	tok := strings.TrimSpace(stdout.String())
	if tok == "" {
		return credential.Credential{}, fmt.Errorf("anthropic: no access token from %s (is a profile logged in? try `ant auth login`)", s.bin)
	}
	return credential.Credential{Values: map[string]string{"oauth_token": tok}}, nil
}
