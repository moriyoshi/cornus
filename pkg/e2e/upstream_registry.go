package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"go.starlark.net/starlark"

	"cornus/pkg/registry"
	"cornus/pkg/storage"
)

// bUpstreamRegistry starts an in-process OCI registry (no mirror of its own) over
// a mem store, seeds it with a distinct random image for each ref in `seed`, and
// returns its "127.0.0.1:PORT" host. It is the offline stand-in for an upstream
// registry (e.g. Docker Hub) that a served cornus can pull-through-proxy to via
// serve(env={"CORNUS_REGISTRY_MIRROR": <host>}). The host is loopback, so the
// registry mirror reaches it over plain HTTP. Closed on scenario teardown.
func (h *Harness) bUpstreamRegistry(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var seedVal starlark.Value
	if err := starlark.UnpackArgs("upstream_registry", args, kwargs, "seed?", &seedVal); err != nil {
		return nil, err
	}
	seeds, err := strOrList(seedVal)
	if err != nil {
		return nil, fmt.Errorf("upstream_registry: seed: %w", err)
	}

	dir, err := os.MkdirTemp("", "e2e-upstream")
	if err != nil {
		return nil, err
	}
	st, err := storage.Open(context.Background(), "mem://", dir)
	if err != nil {
		return nil, fmt.Errorf("upstream_registry: storage: %w", err)
	}
	mux := http.NewServeMux()
	registry.New(st).Register(mux)
	srv := httptest.NewServer(mux)
	h.upstreamCleanups = append(h.upstreamCleanups, func() { srv.Close(); _ = st.Close() })

	host := strings.TrimPrefix(srv.URL, "http://")
	for _, ref := range seeds {
		r, err := name.ParseReference(host+"/"+ref, name.Insecure)
		if err != nil {
			return nil, fmt.Errorf("upstream_registry: ref %q: %w", ref, err)
		}
		img, err := random.Image(1024, 2)
		if err != nil {
			return nil, err
		}
		if err := remote.Write(r, img); err != nil {
			return nil, fmt.Errorf("upstream_registry: seed %q: %w", ref, err)
		}
	}
	return starlark.String(host), nil
}

// stopUpstreams closes every in-process upstream registry started this scenario.
func (h *Harness) stopUpstreams() {
	for _, c := range h.upstreamCleanups {
		c()
	}
	h.upstreamCleanups = nil
}
