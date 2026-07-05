package dockerproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// handleImageCreate is a no-op pull: the real image pull happens on the cornus
// server's deploy backend. It writes a minimal JSON progress stream so the
// Docker CLI's pull reader completes cleanly.
func (p *Proxy) handleImageCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"pull deferred to cornus server"}` + "\n"))
}

// handleImageList returns an empty image list (docker images / compose probes).
func (p *Proxy) handleImageList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// handleImageInspect answers GET /images/{name}/json. The proxy has no local
// image store, so it resolves the reference's real config from its registry
// (trying TLS then plaintext, for the cornus registry). Clients like the
// devcontainer CLI derive the container's entrypoint/cmd/env/user — and the
// devcontainer.metadata label — from Config, so a real one matters. When the
// ref cannot be resolved (offline, private, bogus) it falls back to a synthetic
// image so the client still believes the image is present: cornus resolves and
// pulls the real image on the server at deploy time, and a 404 here breaks
// `docker compose` (it inspects the image after pulling and treats "no such
// image" as fatal). Other /images/{name}/* sub-resources return a benign empty
// result.
func (p *Proxy) handleImageInspect(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/images/")
	// POST /images/{name}/push — the docker CLI push route falls into this
	// catch-all (ServeMux cannot express a multi-segment {name} before /push).
	if name, ok := strings.CutSuffix(rest, "/push"); ok && r.Method == http.MethodPost {
		p.handleImagePush(w, r, name)
		return
	}
	ref := strings.TrimSuffix(rest, "/json")
	if ref == rest { // not a /json inspect (e.g. /history) — don't error
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	if body := p.imageInspectBody(r.Context(), ref); body != nil {
		writeJSON(w, http.StatusOK, body)
		return
	}
	sum := sha256.Sum256([]byte(ref))
	writeJSON(w, http.StatusOK, map[string]any{
		"Id":           "sha256:" + hex.EncodeToString(sum[:]),
		"RepoTags":     []string{ref},
		"RepoDigests":  []string{},
		"Created":      "1970-01-01T00:00:00Z",
		"Architecture": "amd64",
		"Os":           "linux",
		"Size":         0,
		"Config":       map[string]any{},
		"RootFS":       map[string]any{"Type": "layers", "Layers": []string{}},
	})
}

// imageInspectBody fetches the reference's config from its registry and shapes
// it as a Docker image-inspect body, or nil if the ref cannot be resolved.
// Successful lookups are cached per ref (image content behind a ref changing
// mid-session is not a case the proxy needs to chase).
func (p *Proxy) imageInspectBody(ctx context.Context, ref string) map[string]any {
	if v, ok := p.imageCfgs.Load(ref); ok {
		return v.(map[string]any)
	}
	body := fetchImageInspect(ctx, ref)
	if body == nil {
		return nil
	}
	p.imageCfgs.Store(ref, body)
	return body
}

func fetchImageInspect(ctx context.Context, ref string) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := name.ParseReference(ref)
	if err != nil {
		return nil
	}
	img, err := remote.Image(r, remote.WithContext(ctx))
	if err != nil {
		// Retry as a plaintext registry (the cornus registry serves HTTP).
		ir, ierr := name.ParseReference(ref, name.Insecure)
		if ierr != nil {
			return nil
		}
		if img, err = remote.Image(ir, remote.WithContext(ctx)); err != nil {
			return nil
		}
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil
	}
	id, err := img.ConfigName()
	if err != nil {
		return nil
	}
	layers := make([]string, 0, len(cf.RootFS.DiffIDs))
	for _, d := range cf.RootFS.DiffIDs {
		layers = append(layers, d.String())
	}
	repoDigests := []string{}
	if dig, err := img.Digest(); err == nil {
		repoDigests = append(repoDigests, r.Context().Name()+"@"+dig.String())
	}
	return map[string]any{
		"Id":           id.String(),
		"RepoTags":     []string{ref},
		"RepoDigests":  repoDigests,
		"Created":      cf.Created.UTC().Format(time.RFC3339),
		"Architecture": cf.Architecture,
		"Os":           cf.OS,
		"Size":         0,
		"Config": map[string]any{
			"Entrypoint":   cf.Config.Entrypoint,
			"Cmd":          cf.Config.Cmd,
			"Env":          cf.Config.Env,
			"User":         cf.Config.User,
			"WorkingDir":   cf.Config.WorkingDir,
			"Labels":       cf.Config.Labels,
			"ExposedPorts": cf.Config.ExposedPorts,
		},
		"RootFS": map[string]any{"Type": "layers", "Layers": layers},
	}
}
