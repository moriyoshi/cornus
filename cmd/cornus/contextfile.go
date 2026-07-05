package main

import (
	"cornus/pkg/clientconfig"
)

// loadContextFiles loads each path as a bare clientconfig.Context document
// (JSON/YAML/TOML, strict), preserving order. It returns on the first error so
// callers can validate every file before mutating any stored config. It backs
// `config set-context --from-file` / --from-file-override.
//
// The parsing (clientconfig.LoadContextFile) and field-merge (clientconfig.Merge)
// helpers live in pkg/clientconfig so the connection resolver — which discovers a
// per-project override file and cannot import package main — shares the exact same
// bare-context shape and merge semantics.
func loadContextFiles(paths []string) ([]*clientconfig.Context, error) {
	out := make([]*clientconfig.Context, 0, len(paths))
	for _, p := range paths {
		ctx, err := clientconfig.LoadContextFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ctx)
	}
	return out, nil
}
