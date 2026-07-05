// Package codex reads the developer's LOCAL OpenAI Codex CLI credential store —
// ~/.codex/auth.json on Linux. Like Claude Code, Codex keeps its own store; it is
// NOT the classic `openai` CLI (which is env-var driven — use the `env` backend
// with OPENAI_API_KEY for that).
//
// Codex has two auth modes:
//
//   - apikey: a real OpenAI API key at the top-level "OPENAI_API_KEY" field, valid
//     against https://api.openai.com — the clean path.
//   - chatgpt (OAuth sign-in): a short-lived token at "tokens.access_token". This
//     targets Codex's ChatGPT backend rather than api.openai.com, so pairing it
//     with the plain openai-proxy (upstream api.openai.com) is best-effort; a
//     dedicated ChatGPT-backend upstream is a follow-up.
//
// The key is returned under "api_key", which the openai-proxy delivery sends as
// `Authorization: Bearer`. By default the backend prefers the API key and falls
// back to the OAuth access token; set "path" to force a specific dotted field.
//
// Config keys:
//
//   - "file": the store path (default ~/.codex/auth.json).
//   - "path": dotted JSON path to the token; overrides the apikey→oauth fallback.
package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"cornus/pkg/credential"
	"cornus/pkg/credential/internal/jsonstore"
)

func init() { credential.Register("codex", newSource) }

type source struct {
	file string
	path string // explicit override; "" = apikey-then-oauth fallback
}

func newSource(cfg map[string]string) (credential.Source, error) {
	file := cfg["file"]
	if file == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("codex: resolve home: %w", err)
		}
		file = filepath.Join(home, ".codex", "auth.json")
	}
	return &source{file: file, path: cfg["path"]}, nil
}

func (s *source) Fetch(context.Context, map[string]string) (credential.Credential, error) {
	doc, err := jsonstore.Load(s.file)
	if err != nil {
		return credential.Credential{}, fmt.Errorf("codex: %w (is Codex logged in? try `codex login`)", err)
	}
	var tok string
	if s.path != "" {
		tok = jsonstore.String(doc, s.path)
	} else {
		// apikey mode first (usable against api.openai.com), then the ChatGPT
		// OAuth access token.
		if tok = jsonstore.String(doc, "OPENAI_API_KEY"); tok == "" {
			tok = jsonstore.String(doc, "tokens.access_token")
		}
	}
	if tok == "" {
		return credential.Credential{}, fmt.Errorf("codex: no token in %s", s.file)
	}
	return credential.Credential{Values: map[string]string{"api_key": tok}}, nil
}
