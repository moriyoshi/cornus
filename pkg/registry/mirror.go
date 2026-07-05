package registry

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"cornus/pkg/storage"
)

// imageSource is a read-only upstream a registry miss can fall through to. Both
// the pull-through Mirror (an upstream OCI registry) and the daemon re-export
// source (the local Docker daemon's image store) implement it, so the miss
// fallback in handleManifest/handleBlob is shared.
type imageSource interface {
	// manifest returns repo's manifest at ref (a tag or digest): the raw bytes,
	// its media type, and its digest ("sha256:...").
	manifest(ctx context.Context, repo, ref string) (body []byte, mediaType, digest string, err error)
	// blob returns repo's blob (by digest) as a reader the caller must close,
	// plus its size.
	blob(ctx context.Context, repo, digest string) (rc io.ReadCloser, size int64, err error)
}

// Mirror configures pull-through proxying to an upstream registry. When set on a
// Registry (WithMirror), a GET/HEAD manifest or blob that is not present in the
// local store is fetched from Host and served; with Cache, the fetched content
// is also persisted into the local store so later pulls resolve locally. Only
// pulls (manifests + blobs) fall through — tags, catalog, and referrers stay
// local. Upstream access is anonymous (public images only).
type Mirror struct {
	// Host is the upstream registry host, e.g. "docker.io". go-containerregistry
	// applies Docker Hub's "library/" normalization for single-segment repos.
	Host string
	// Cache persists fetched manifests/blobs into the local store (pull-through
	// cache). When false, content is proxied transparently without persisting.
	Cache bool
}

// WithMirror enables pull-through proxying to m (nil leaves the registry
// local-only, returning 404 on a miss).
func WithMirror(m *Mirror) Option {
	return func(r *Registry) {
		if m != nil {
			r.source = m
		}
	}
}

// cacheFetched reports whether fetched content is persisted into the local store.
// It satisfies the optional caching-source interface the serve* helpers probe.
func (m *Mirror) cacheFetched() bool { return m.Cache }

// cachingSource is an optional interface an imageSource implements when a fetched
// manifest/blob should be persisted into the local store (a pull-through cache).
// Sources that omit it are proxied transparently, never cached.
type cachingSource interface{ cacheFetched() bool }

// nameOpts returns the reference-parse options for the upstream: a loopback host
// (test stand-in) is treated as plain HTTP.
func (m *Mirror) nameOpts() []name.Option {
	if isLoopbackHost(m.Host) {
		return []name.Option{name.Insecure}
	}
	return nil
}

func (m *Mirror) remoteOpts(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithContext(ctx)}
}

// manifest fetches repo's manifest at ref (a tag or digest) from the upstream.
func (m *Mirror) manifest(ctx context.Context, repo, ref string) (body []byte, mediaType, digest string, err error) {
	sep := ":"
	if _, _, derr := storage.ParseDigest(ref); derr == nil {
		sep = "@"
	}
	r, err := name.ParseReference(m.Host+"/"+repo+sep+ref, m.nameOpts()...)
	if err != nil {
		return nil, "", "", err
	}
	desc, err := remote.Get(r, m.remoteOpts(ctx)...)
	if err != nil {
		return nil, "", "", err
	}
	return desc.Manifest, string(desc.MediaType), desc.Digest.String(), nil
}

// blob fetches repo's blob (by digest) from the upstream, returning a reader the
// caller must close and the blob's size.
func (m *Mirror) blob(ctx context.Context, repo, digest string) (rc io.ReadCloser, size int64, err error) {
	dref, err := name.NewDigest(m.Host+"/"+repo+"@"+digest, m.nameOpts()...)
	if err != nil {
		return nil, 0, err
	}
	layer, err := remote.Layer(dref, m.remoteOpts(ctx)...)
	if err != nil {
		return nil, 0, err
	}
	size, err = layer.Size()
	if err != nil {
		return nil, 0, err
	}
	rc, err = layer.Compressed()
	if err != nil {
		return nil, 0, err
	}
	return rc, size, nil
}

// serveManifestFromSource fetches name's manifest at ref from the configured
// source and serves it (caching it locally when the source is a cachingSource
// that opts in). It returns false when no source is configured or the fetch
// fails, so the caller falls back to the standard 404.
func (r *Registry) serveManifestFromSource(w http.ResponseWriter, req *http.Request, name, ref string) bool {
	if r.source == nil {
		return false
	}
	body, mediaType, digest, err := r.source.manifest(req.Context(), name, ref)
	if err != nil {
		return false
	}
	if mediaType == "" {
		mediaType = "application/vnd.oci.image.manifest.v1+json"
	}
	if cs, ok := r.source.(cachingSource); ok && cs.cacheFetched() && r.store != nil {
		// Best-effort: still serve what we fetched even if the local write fails.
		_, _ = r.store.PutManifest(req.Context(), name, ref, mediaType, body)
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set(contentDigestHeader, digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return true
}

// serveBlobFromSource fetches name's blob (by digest) from the configured source
// and serves it. When the source is a cachingSource that opts in, it persists
// the blob and re-dispatches to the local blob path (so Range requests and the
// digest header behave identically); otherwise it streams the body straight
// through. Returns false when no source is configured or the fetch fails.
func (r *Registry) serveBlobFromSource(w http.ResponseWriter, req *http.Request, name, digest string) bool {
	if r.source == nil {
		return false
	}
	rc, size, err := r.source.blob(req.Context(), name, digest)
	if err != nil {
		return false
	}
	defer rc.Close()
	if cs, ok := r.source.(cachingSource); ok && cs.cacheFetched() && r.store != nil {
		if _, _, perr := r.store.PutBlob(req.Context(), rc, digest); perr != nil {
			return false
		}
		r.handleBlob(w, req, name, digest) // now a local hit
		return true
	}
	w.Header().Set(contentDigestHeader, digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = io.Copy(w, rc)
	}
	return true
}

// isLoopbackHost reports whether host (possibly "host:port") is the loopback
// interface, which is reached over plain HTTP.
func isLoopbackHost(host string) bool {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
