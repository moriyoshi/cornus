// Package claudecode reads the developer's LOCAL Claude Code credential store —
// ~/.claude/.credentials.json on Linux — and extracts the OAuth access token
// Claude Code keeps refreshed while it runs. This is a DIFFERENT store from the
// `ant auth login` profile (the "anthropic" backend); Claude Code does not share
// credentials with the ant CLI.
//
// The token is returned under "oauth_token", which the anthropic-proxy delivery
// sends as `Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20`. The
// token's expiry (expiresAt, ms epoch) is carried on the credential so the
// broker re-reads the file near expiry and picks up whatever fresher token
// Claude Code has written.
//
// Config keys:
//
//   - "file": the store path (default ~/.claude/.credentials.json).
//   - "path": dotted JSON path to the token (default
//     "claudeAiOauth.accessToken").
//   - "expiry_path": dotted JSON path to the ms-epoch expiry (default
//     "claudeAiOauth.expiresAt"); missing/zero means no expiry.
//
// macOS stores the credential in the Keychain rather than a file; that is not yet
// supported here (use an `exec` source with a `security find-generic-password`
// command, or export the token to an env var and use the `env` backend).
package claudecode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cornus/pkg/credential"
	"cornus/pkg/credential/internal/jsonstore"
)

func init() { credential.Register("claude-code", newSource) }

type source struct {
	file       string
	tokenPath  string
	expiryPath string
}

func newSource(cfg map[string]string) (credential.Source, error) {
	file := cfg["file"]
	if file == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("claude-code: resolve home: %w", err)
		}
		file = filepath.Join(home, ".claude", ".credentials.json")
	}
	tokenPath := cfg["path"]
	if tokenPath == "" {
		tokenPath = "claudeAiOauth.accessToken"
	}
	expiryPath := cfg["expiry_path"]
	if expiryPath == "" {
		expiryPath = "claudeAiOauth.expiresAt"
	}
	return &source{file: file, tokenPath: tokenPath, expiryPath: expiryPath}, nil
}

func (s *source) Fetch(context.Context, map[string]string) (credential.Credential, error) {
	doc, err := jsonstore.Load(s.file)
	if err != nil {
		return credential.Credential{}, fmt.Errorf("claude-code: %w (is Claude Code logged in?)", err)
	}
	tok := jsonstore.String(doc, s.tokenPath)
	if tok == "" {
		return credential.Credential{}, fmt.Errorf("claude-code: no token at %q in %s", s.tokenPath, s.file)
	}
	cred := credential.Credential{Values: map[string]string{"oauth_token": tok}}
	if ms, ok := jsonstore.Walk(doc, s.expiryPath).(float64); ok && ms > 0 {
		cred.Expiration = time.UnixMilli(int64(ms))
	}
	return cred, nil
}
