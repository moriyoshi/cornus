// Package herdr embeds the vendored herdr agent-detection manifests.
//
// The manifests themselves are third-party data, bundled unmodified under the
// Apache License 2.0 — see LICENSE and README.md in this directory for the
// upstream commit and the attribution terms. This file is Cornus's own: it does
// nothing but make the bytes reachable from Go.
//
// It deliberately does NOT interpret them. Cornus's detector reads a different,
// much simpler schema (cmd/cornus/internal/webbff/rules.toml), and teaching it
// this one — regions, priorities, any/all/not gates — is separate work. Embedding
// the data first keeps that work from also being a vendoring exercise, and gives
// the bundle a test that fails if a refresh ever lands truncated or unparseable.
package herdr

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed manifests/*.toml
var manifestFS embed.FS

// Agent is one bundled manifest: the agent id taken from its filename, and the
// raw TOML exactly as upstream ships it.
type Agent struct {
	// ID is the file's basename without the extension ("claude",
	// "github-copilot"). It is NOT authoritative — the manifest's own `id` and
	// `aliases` fields are, and a consumer that matches process names must read
	// those. This is here so a caller can find a file without parsing all of them.
	ID string
	// TOML is the manifest's bytes, unmodified.
	TOML []byte
}

// Manifests returns every bundled manifest, ordered by ID so callers and tests
// see a stable sequence.
func Manifests() ([]Agent, error) {
	entries, err := fs.ReadDir(manifestFS, "manifests")
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		b, err := manifestFS.ReadFile(path.Join("manifests", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Agent{ID: strings.TrimSuffix(e.Name(), ".toml"), TOML: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
