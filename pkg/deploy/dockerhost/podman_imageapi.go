package dockerhost

// The registry's image-re-export client for the podman backend.
//
// pkg/registry's own client reads DOCKER_HOST and speaks Docker's
// /images/{ref}/get and /images/load. Neither is right here: podman's endpoint
// is an explicit operator choice with no default, and libpod's image routes live
// under the versioned /vN.0.0/libpod/ prefix. Pointing the Docker client at a
// podman socket would 404 at best; leaving it reading DOCKER_HOST would make a
// podman server re-export the DOCKER daemon's images — a confidently wrong
// answer, with the deploy backend and the registry serving two different
// runtimes.
//
// This type satisfies registry.DockerImageAPI structurally, so this package
// imports nothing from pkg/registry and no cycle appears.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"cornus/pkg/runtimeendpoint"
)

// PodmanImageAPI re-exports podman's image store for the /v2/* registry.
//
// The engine behind it is built LAZILY, on first use. That is not an
// optimization — it is required for parity with the Docker path and with the
// deploy backend, both of which reach the daemon only when asked to do
// something. newPodmanEngine pings /libpod/_ping to settle the API version, so
// building it eagerly would mean the server could not START while podman was
// merely stopped, even though nothing had asked it for an image yet. Measured:
// it turned `cornus serve` into "cannot reach the libpod API" at boot.
type PodmanImageAPI struct {
	endpoint runtimeendpoint.Endpoint

	mu  sync.Mutex
	eng *podmanEngine
}

// NewPodmanImageAPI resolves the podman endpoint the same way the backend does
// and returns a client for the registry's re-export source.
//
// Endpoint RESOLUTION happens here, because it is a pure environment read and a
// misconfiguration should be reported at startup. Reaching the daemon does not.
//
// It refuses the self-spawned service for the same reason PodmanSelfInspector
// does: that service belongs to a Backend which started it, and the registry has
// no such handle. Starting a second one to answer image reads would leave an
// orphan nobody owns.
func NewPodmanImageAPI() (*PodmanImageAPI, error) {
	access, err := resolvePodmanAccess(podmanEnvOrOS)
	if err != nil {
		return nil, err
	}
	if access.Spawn {
		return nil, fmt.Errorf(
			"host-native registry re-export needs %s: %s=1 starts a service per deploy backend, "+
				"which the registry cannot share", envPodmanSocket, envPodmanService)
	}
	return &PodmanImageAPI{endpoint: access.Endpoint}, nil
}

// engine builds the libpod engine on first use and memoizes it.
//
// A failure is NOT cached: a daemon that was down when the first image was
// requested may be up for the next one, and remembering the failure would make a
// transient outage permanent for the process's lifetime.
func (p *PodmanImageAPI) engine(ctx context.Context) (*podmanEngine, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.eng != nil {
		return p.eng, nil
	}
	eng, err := newPodmanEngine(ctx, p.endpoint)
	if err != nil {
		return nil, err
	}
	p.eng = eng
	return eng, nil
}

// ImageSave streams the image as a **docker-archive** tar.
//
// format=docker-archive is mandatory and easy to miss. libpod's export defaults
// to oci-archive over REST — even though `podman save` on the command line
// defaults to docker-archive — and go-containerregistry's tarball reader, which
// consumes this stream, reads docker-archive. Measured on Podman 5.8.2: without
// the parameter the tar contains blobs/sha256/... (OCI layout) instead of the
// <hash>.json + <layer>/layer.tar + VERSION docker-archive expects.
func (p *PodmanImageAPI) ImageSave(ctx context.Context, ref string) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("format", "docker-archive")
	eng, err := p.engine(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := eng.do(ctx, http.MethodGet, "/images/"+ref+"/get?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := expect(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("podman image save %s: %w", ref, err)
	}
	return resp.Body, nil
}

// ImageLoad loads a docker-archive tar into podman's image store.
//
// No format parameter here: libpod's load auto-detects docker-archive and
// oci-archive, so the direction that reads is the only one that must be told.
func (p *PodmanImageAPI) ImageLoad(ctx context.Context, r io.Reader) error {
	eng, err := p.engine(ctx)
	if err != nil {
		return err
	}
	resp, err := eng.doRaw(ctx, http.MethodPost, "/images/load", "application/x-tar", r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return fmt.Errorf("podman image load: %w", err)
	}
	// libpod answers {"Names":["<ref>",...]}. A body that names nothing means the
	// archive carried no loadable image — which the status code does not say.
	var out struct {
		Names []string `json:"Names"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("podman image load: decode response: %w (body %q)", err, strings.TrimSpace(string(body)))
	}
	if len(out.Names) == 0 {
		return fmt.Errorf("podman image load: the archive loaded no image (body %q)", strings.TrimSpace(string(body)))
	}
	return nil
}
