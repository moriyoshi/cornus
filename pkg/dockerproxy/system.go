package dockerproxy

import "net/http"

// handlePing answers GET/HEAD /_ping with the version-negotiation headers the
// Docker CLI reads before its first real call.
func (p *Proxy) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Api-Version", apiVersion)
	w.Header().Set("Ostype", "linux")
	w.Header().Set("Docker-Experimental", "false")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// handleVersion answers GET /version.
func (p *Proxy) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"Version":       "cornus-docker-proxy",
		"ApiVersion":    apiVersion,
		"MinAPIVersion": "1.24",
		"Os":            "linux",
		"Arch":          "amd64",
		"Components": []map[string]any{
			{"Name": "cornus-docker-proxy", "Version": "cornus"},
		},
	})
}

// handleInfo answers GET /info with the minimum a client/compose reads.
func (p *Proxy) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ID":            "cornus-docker-proxy",
		"Name":          "cornus-docker-proxy",
		"OSType":        "linux",
		"ServerVersion": "cornus",
		"Driver":        "cornus",
		"NCPU":          1,
		"MemTotal":      0,
	})
}
