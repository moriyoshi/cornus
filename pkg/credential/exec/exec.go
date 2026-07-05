// Package exec is a credential source that runs a local command on the client
// and parses its stdout as the credential. It lets a developer reuse any
// existing credential-minting tool (aws CLI, gcloud, vault, a shell one-liner)
// without a dedicated cornus backend.
//
// Config keys:
//
//   - "command" (required): a command line run via `sh -c`.
//   - "timeout": optional Go duration bounding the run (default 30s).
//   - "raw": when truthy, the command's stdout is NOT parsed as JSON — the
//     trimmed output is used verbatim as a single value under "key" (default
//     "value"). This lets refresh-aware token printers be used directly, e.g.
//     `ant auth print-credentials --access-token` under key "oauth_token", with
//     no jq wrapper.
//   - "key": the value name for raw mode (default "value").
//
// Without "raw", the command's stdout must be JSON — either the neutral
// credential object (`{"values":{...},"expiration":"..."}`) or a flat object of
// string values (which becomes the value set).
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"cornus/pkg/credential"
)

func init() { credential.Register("exec", newSource) }

type source struct {
	command string
	timeout time.Duration
	raw     bool
	key     string
}

func newSource(cfg map[string]string) (credential.Source, error) {
	cmd := cfg["command"]
	if cmd == "" {
		return nil, fmt.Errorf("exec: missing command")
	}
	timeout := 30 * time.Second
	if t := cfg["timeout"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return nil, fmt.Errorf("exec: parse timeout: %w", err)
		}
		timeout = d
	}
	key := cfg["key"]
	if key == "" {
		key = "value"
	}
	return &source{command: cmd, timeout: timeout, raw: isTruthy(cfg["raw"]), key: key}, nil
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (s *source) Fetch(ctx context.Context, _ map[string]string) (credential.Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "sh", "-c", s.command)
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return credential.Credential{}, fmt.Errorf("exec: run %q: %w: %s", s.command, err, stderr.String())
	}
	if s.raw {
		v := strings.TrimSpace(stdout.String())
		if v == "" {
			return credential.Credential{}, fmt.Errorf("exec: command produced no output")
		}
		return credential.Credential{Values: map[string]string{s.key: v}}, nil
	}
	return parse(stdout.Bytes())
}

// parse accepts either the neutral credential object or a flat string map.
func parse(out []byte) (credential.Credential, error) {
	var cred credential.Credential
	if err := json.Unmarshal(out, &cred); err == nil && len(cred.Values) > 0 {
		return cred, nil
	}
	var flat map[string]string
	if err := json.Unmarshal(out, &flat); err != nil {
		return credential.Credential{}, fmt.Errorf("exec: stdout is not credential JSON: %w", err)
	}
	if len(flat) == 0 {
		return credential.Credential{}, fmt.Errorf("exec: command produced no values")
	}
	return credential.Credential{Values: flat}, nil
}
