// Package generic is the DEFAULT, cloud-neutral credential delivery endpoint. It
// serves the cornus-native JSON contract — a GET returns
// {"values":{...},"expiration":"..."} — which any language reads with a plain
// HTTP GET, no SDK and no cloud vocabulary. It is the surface a workload gets
// unless it explicitly opts into a cloud adapter.
package generic

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"cornus/pkg/creddelivery"
)

func init() {
	creddelivery.Register("generic", func(map[string]string) (creddelivery.Endpoint, error) { return endpoint{}, nil })
}

type endpoint struct{}

func (endpoint) Serve(ctx context.Context, ln net.Listener, get creddelivery.Getter) error {
	mux := http.NewServeMux()
	// One endpoint serves one credential; answer on any path so a caller can use
	// either "/" or "/credentials/<name>" interchangeably.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cred, err := get(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cred)
	})
	srv := &http.Server{Handler: mux}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (endpoint) Env(name, addr string) map[string]string {
	url := "http://" + addr + "/credentials/" + name
	return map[string]string{
		// Name-qualified is always unambiguous with multiple sources.
		"CORNUS_CREDENTIAL_" + envName(name) + "_URL": url,
		// Convenience for the common single-source case (last write wins).
		"CORNUS_CREDENTIALS_URL": url,
	}
}

func (endpoint) WellKnownAddr() string { return "" }

// envName upper-cases name and replaces any non-alphanumeric run with a single
// underscore, so a logical name is a valid environment-variable segment.
func envName(name string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
