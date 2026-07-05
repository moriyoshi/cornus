package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"cornus/pkg/storage"
)

// DockerImageAPI is the minimal Docker Engine REST surface the daemon re-export
// source needs. It is an interface so the source can be driven by a fake in
// tests without a live daemon; NewDockerImageAPI returns the real client.
type DockerImageAPI interface {
	// ImageSave streams `docker save ref` (GET /images/{ref}/get), a docker-archive
	// tar of the image. The caller must close the returned reader.
	ImageSave(ctx context.Context, ref string) (io.ReadCloser, error)
	// ImageLoad loads a docker-archive tar (POST /images/load, `docker load`) from
	// r into the daemon's image store, tagging it as the archive dictates. It reads
	// r to completion and reports any error the daemon streams back. Used by the
	// build path in docker-daemon re-export mode.
	ImageLoad(ctx context.Context, r io.Reader) error
}

// dockerRESTClient is a tiny hand-rolled Docker Engine API client, mirroring the
// transport construction in pkg/deploy/dockerhost so the registry package does
// not pull in the heavy moby client (the same reason that backend hand-rolls its
// own). It speaks only the endpoints the re-export source needs.
type dockerRESTClient struct {
	http *http.Client
	// host is the scheme+authority placed in request URLs. For unix sockets it
	// is a fixed placeholder the dialer ignores.
	host string
}

// NewDockerImageAPI builds a Docker Engine API client from DOCKER_HOST (default
// unix:///var/run/docker.sock). An unsupported DOCKER_HOST scheme is an error so
// the caller can fail closed.
func NewDockerImageAPI() (DockerImageAPI, error) {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	switch {
	case strings.HasPrefix(host, "unix://"):
		sock := strings.TrimPrefix(host, "unix://")
		return &dockerRESTClient{
			http: &http.Client{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "unix", sock)
					},
				},
				Timeout: 0, // streaming saves can be long
			},
			host: "http://docker",
		}, nil
	case strings.HasPrefix(host, "tcp://"):
		addr := strings.TrimPrefix(host, "tcp://")
		return &dockerRESTClient{
			http: &http.Client{Timeout: 0},
			host: "http://" + addr,
		}, nil
	default:
		return nil, fmt.Errorf("registry daemon source: unsupported DOCKER_HOST %q", host)
	}
}

func (c *dockerRESTClient) ImageSave(ctx context.Context, ref string) (io.ReadCloser, error) {
	u := c.host + "/images/" + url.PathEscape(ref) + "/get"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("docker image save %s: %s: %s", ref, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp.Body, nil
}

func (c *dockerRESTClient) ImageLoad(ctx context.Context, r io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/images/load?quiet=1", r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf("docker image load: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	// The load progress is a JSON stream; the daemon reports a failure as an
	// {"error":...} object with HTTP 200, so scan for it rather than blindly
	// draining (mirrors imagePull in the dockerhost backend).
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("docker image load: %s", msg.Error)
		}
	}
}

// defaultDaemonTTL bounds how long a reconstructed image (and its digest index)
// is reused before the daemon is re-consulted. Short, because the daemon's store
// can change under us (a rebuild reassigns a tag); a client's manifest-then-blob
// pull always completes well within it.
const defaultDaemonTTL = 60 * time.Second

// daemonSource implements imageSource by re-exporting the local Docker daemon's
// image store. It never persists into the local content store (it is not a
// cachingSource), so the daemon remains the single authoritative store.
type daemonSource struct {
	api        DockerImageAPI
	stagingDir string // where the transient `docker save` tars are written
	ttl        time.Duration
	now        func() time.Time

	mu     sync.Mutex
	images map[string]*loadedImage // key: daemon ref (see daemonRef)
	index  map[string]string       // blob digest ("sha256:...") -> daemon ref key
}

// loadedImage is a reconstructed daemon image plus the temp tar backing it. The
// tar must stay on disk for the image's whole cache lifetime because tarball's
// layer opener re-opens it by path on each read.
type loadedImage struct {
	img     v1.Image
	tarPath string
	expiry  time.Time
}

// newDaemonSource builds a daemon re-export source. stagingDir must exist and be
// writable (the caller passes the server's uploads/staging dir).
func newDaemonSource(api DockerImageAPI, stagingDir string, ttl time.Duration) *daemonSource {
	if ttl <= 0 {
		ttl = defaultDaemonTTL
	}
	return &daemonSource{
		api:        api,
		stagingDir: stagingDir,
		ttl:        ttl,
		now:        time.Now,
		images:     map[string]*loadedImage{},
		index:      map[string]string{},
	}
}

// WithDaemonSource makes a manifest/blob miss fall through to the local Docker
// daemon's image store (re-export), served via `docker save`. stagingDir is a
// writable directory for the transient save tars.
func WithDaemonSource(api DockerImageAPI, stagingDir string) Option {
	return func(r *Registry) {
		if api == nil {
			return
		}
		r.source = newDaemonSource(api, stagingDir, defaultDaemonTTL)
	}
}

// daemonRef maps a /v2/ repository and reference (a tag or a "sha256:..." digest)
// to the name `docker save` expects: "repo:tag" or "repo@sha256:...".
func daemonRef(repo, ref string) string {
	if _, _, err := storage.ParseDigest(ref); err == nil {
		return repo + "@" + ref
	}
	return repo + ":" + ref
}

func (s *daemonSource) manifest(ctx context.Context, repo, ref string) (body []byte, mediaType, digest string, err error) {
	li, err := s.load(ctx, daemonRef(repo, ref))
	if err != nil {
		return nil, "", "", err
	}
	raw, err := li.img.RawManifest()
	if err != nil {
		return nil, "", "", err
	}
	mt, err := li.img.MediaType()
	if err != nil {
		return nil, "", "", err
	}
	dig, err := li.img.Digest()
	if err != nil {
		return nil, "", "", err
	}
	return raw, string(mt), dig.String(), nil
}

func (s *daemonSource) blob(ctx context.Context, repo, digest string) (rc io.ReadCloser, size int64, err error) {
	s.mu.Lock()
	key, ok := s.index[digest]
	var li *loadedImage
	if ok {
		li = s.images[key]
		if li != nil && !s.now().Before(li.expiry) {
			li, ok = nil, false // expired — force a miss so the client re-pulls the manifest
		}
	}
	s.mu.Unlock()
	if !ok || li == nil {
		// A cold blob request (no prior manifest fetch warmed the index) or an
		// expired entry: report a miss so the handler returns the standard 404.
		// Standard OCI clients always fetch the manifest before its blobs.
		return nil, 0, fmt.Errorf("registry daemon source: blob %s not in index for %s", digest, repo)
	}

	cfgHash, err := li.img.ConfigName()
	if err != nil {
		return nil, 0, err
	}
	if cfgHash.String() == digest {
		raw, err := li.img.RawConfigFile()
		if err != nil {
			return nil, 0, err
		}
		return io.NopCloser(bytes.NewReader(raw)), int64(len(raw)), nil
	}
	h, err := v1.NewHash(digest)
	if err != nil {
		return nil, 0, err
	}
	layer, err := li.img.LayerByDigest(h)
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

// load returns the reconstructed image for a daemon ref, fetching it via `docker
// save` on a cache miss. The fetch happens outside the lock so concurrent
// requests for different images do not serialize; a race that fetches the same
// ref twice keeps one result and discards the other's temp tar.
func (s *daemonSource) load(ctx context.Context, ref string) (*loadedImage, error) {
	s.mu.Lock()
	if li := s.images[ref]; li != nil && s.now().Before(li.expiry) {
		s.mu.Unlock()
		return li, nil
	}
	s.mu.Unlock()

	tarPath, img, err := s.fetch(ctx, ref)
	if err != nil {
		return nil, err
	}
	li := &loadedImage{img: img, tarPath: tarPath, expiry: s.now().Add(s.ttl)}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Lost a race: another goroutine populated this ref while we fetched. Keep the
	// existing entry and drop ours (remove its now-orphan tar).
	if existing := s.images[ref]; existing != nil && s.now().Before(existing.expiry) {
		_ = os.Remove(tarPath)
		return existing, nil
	}
	s.evictExpiredLocked()
	if old := s.images[ref]; old != nil {
		_ = os.Remove(old.tarPath)
	}
	s.images[ref] = li
	s.indexImageLocked(ref, img)
	return li, nil
}

// fetch streams `docker save ref` to a temp file and reconstructs a v1.Image.
func (s *daemonSource) fetch(ctx context.Context, ref string) (tarPath string, img v1.Image, err error) {
	rc, err := s.api.ImageSave(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()

	f, err := os.CreateTemp(s.stagingDir, "daemon-save-*.tar")
	if err != nil {
		return "", nil, err
	}
	tmp := f.Name()
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", nil, err
	}
	img, err = tarball.ImageFromPath(tmp, nil)
	if err != nil {
		_ = os.Remove(tmp)
		return "", nil, fmt.Errorf("registry daemon source: reconstruct %s: %w", ref, err)
	}
	return tmp, img, nil
}

// indexImageLocked records the image's config and layer digests -> ref so a
// subsequent blob GET (which carries only repo+digest) can find the image.
func (s *daemonSource) indexImageLocked(ref string, img v1.Image) {
	if cfg, err := img.ConfigName(); err == nil {
		s.index[cfg.String()] = ref
	}
	if m, err := img.Manifest(); err == nil {
		for _, l := range m.Layers {
			s.index[l.Digest.String()] = ref
		}
	}
}

// evictExpiredLocked drops expired images, removing their temp tars and index
// entries. On Linux an in-flight read holds the file open across the unlink, so
// removing a just-expired tar cannot corrupt a stream that already started.
func (s *daemonSource) evictExpiredLocked() {
	now := s.now()
	for ref, li := range s.images {
		if now.Before(li.expiry) {
			continue
		}
		_ = os.Remove(li.tarPath)
		delete(s.images, ref)
		for dig, r := range s.index {
			if r == ref {
				delete(s.index, dig)
			}
		}
	}
}
