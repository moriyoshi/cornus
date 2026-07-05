// Package webui embeds the built web UI (the SolidJS single-page app served by
// `cornus web`). The Vite build emits into dist/ (see web/ at the repo root and
// the `make web` target); a plain `go build ./...` without a prior frontend
// build still compiles — dist/ then holds only a .gitkeep and the handler
// serves a "not built" notice instead of the app.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// notBuilt is served when the embedded dist/ holds no index.html — i.e. the
// binary was built without running the frontend build.
const notBuilt = "cornus web UI assets are not embedded in this binary.\n" +
	"Build them with `make web` (or `cd web && npm ci && npm run build`) and rebuild cornus.\n"

// Built reports whether the frontend build is embedded in this binary.
func Built() bool {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, "index.html")
	return err == nil
}

// Handler returns the SPA handler: exact asset paths are served from the
// embedded dist/, and any other GET falls back to index.html so client-side
// routes deep-link correctly.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: dist/ is embedded at compile time. Fail loudly if not.
		panic("webui: embedded dist missing: " + err.Error())
	}
	return handlerFor(sub)
}

// handlerFor builds the SPA handler over fsys (split from Handler so tests can
// drive it with a fstest.MapFS).
func handlerFor(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: unknown paths (client-side routes) get index.html.
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			http.Error(w, notBuilt, http.StatusServiceUnavailable)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
