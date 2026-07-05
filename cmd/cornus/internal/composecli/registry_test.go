package composecli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cornus/pkg/client"
)

// infoServer returns an httptest server whose /.cornus/v1/info replies with the given JSON
// body and status. A status of 404 simulates a server predating the endpoint.
func infoServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.cornus/v1/info" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegistryHostForPrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit override wins without contacting server", func(t *testing.T) {
		// A server that would fail the test if hit for /.cornus/v1/info.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("server contacted despite override: %s", r.URL.Path)
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		r := &runtime{client: client.New(srv.URL), registryOverride: "override.example:5000"}
		if got := r.registryHostFor(ctx); got != "override.example:5000" {
			t.Fatalf("got %q, want override", got)
		}
	})

	t.Run("server-advertised host used when no override", func(t *testing.T) {
		srv := infoServer(t, http.StatusOK, `{"registry_host":"reg.example:30500","registry_scheme":"http"}`)
		r := &runtime{client: client.New(srv.URL)}
		if got := r.registryHostFor(ctx); got != "reg.example:30500" {
			t.Fatalf("got %q, want advertised host", got)
		}
	})

	t.Run("falls back to endpoint host when server has no advertised host", func(t *testing.T) {
		srv := infoServer(t, http.StatusOK, `{}`)
		c := client.New(srv.URL)
		r := &runtime{client: c}
		if got := r.registryHostFor(ctx); got != c.Host() {
			t.Fatalf("got %q, want endpoint host %q", got, c.Host())
		}
	})

	t.Run("falls back to endpoint host when endpoint predates /.cornus/v1/info", func(t *testing.T) {
		srv := infoServer(t, http.StatusNotFound, `not found`)
		c := client.New(srv.URL)
		r := &runtime{client: c}
		if got := r.registryHostFor(ctx); got != c.Host() {
			t.Fatalf("got %q, want endpoint host %q", got, c.Host())
		}
	})

	t.Run("result is memoized", func(t *testing.T) {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"registry_host":"reg.example:30500"}`))
		}))
		t.Cleanup(srv.Close)
		r := &runtime{client: client.New(srv.URL)}
		_ = r.registryHostFor(ctx)
		_ = r.registryHostFor(ctx)
		if hits != 1 {
			t.Fatalf("server hit %d times, want 1 (memoized)", hits)
		}
	})
}
