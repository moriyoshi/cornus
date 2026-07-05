package registry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/validate"

	"cornus/pkg/storage"
)

// fakeDaemon serves canned `docker save` tars keyed by the daemon ref, standing
// in for a live Docker daemon.
type fakeDaemon struct {
	tars  map[string][]byte
	saves int // count of ImageSave calls, for cache assertions
}

func (f *fakeDaemon) ImageSave(_ context.Context, ref string) (io.ReadCloser, error) {
	f.saves++
	b, ok := f.tars[ref]
	if !ok {
		return nil, fmt.Errorf("no such image %q", ref)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeDaemon) ImageLoad(_ context.Context, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// saveTar builds a docker-save-format tar of a random image tagged repoTag.
func saveTar(t *testing.T, repoTag string) []byte {
	t.Helper()
	img, err := random.Image(1024, 3)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	ref, err := name.NewTag(repoTag)
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	var buf bytes.Buffer
	if err := tarball.Write(ref, img, &buf); err != nil {
		t.Fatalf("tarball.Write: %v", err)
	}
	return buf.Bytes()
}

// newDaemonRegistry starts a pure re-export registry: NO content store (nil), so
// every read is served by the daemon source and every write is rejected — the
// real docker-daemon-mode configuration.
func newDaemonRegistry(t *testing.T, fake DockerImageAPI) *httptest.Server {
	t.Helper()
	reg := New(nil)
	reg.source = newDaemonSource(fake, t.TempDir(), 0)
	mux := http.NewServeMux()
	reg.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDaemonSourcePull re-exports a daemon image and confirms a standard OCI pull
// serves a self-consistent image (manifest + config + every layer verify).
func TestDaemonSourcePull(t *testing.T) {
	fake := &fakeDaemon{tars: map[string][]byte{"app:v1": saveTar(t, "app:v1")}}
	srv := newDaemonRegistry(t, fake)

	got, err := pullImage(t, srv, "app:v1")
	if err != nil {
		t.Fatalf("daemon-source pull: %v", err)
	}
	// validate fetches manifest, config, and every layer and verifies each digest
	// against the served bytes — the strongest internal-consistency check.
	if err := validate.Image(got); err != nil {
		t.Fatalf("pulled image failed validation: %v", err)
	}
}

// TestDaemonSourceCaches confirms a manifest pull warms the cache so the
// subsequent blob fetches reuse one `docker save` rather than re-saving per blob.
func TestDaemonSourceCaches(t *testing.T) {
	fake := &fakeDaemon{tars: map[string][]byte{"app:v1": saveTar(t, "app:v1")}}
	srv := newDaemonRegistry(t, fake)

	if _, err := pullImage(t, srv, "app:v1"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if fake.saves != 1 {
		t.Fatalf("ImageSave called %d times for one pull, want 1 (config+layers must reuse the cached image)", fake.saves)
	}
}

// TestDaemonSourceManifestHead confirms HEAD on a daemon-backed manifest returns
// 200 with the content-digest header (docker/containerd probe with HEAD first).
func TestDaemonSourceManifestHead(t *testing.T) {
	fake := &fakeDaemon{tars: map[string][]byte{"app:v1": saveTar(t, "app:v1")}}
	srv := newDaemonRegistry(t, fake)

	req, _ := http.NewRequest(http.MethodHead, srv.URL+"/v2/app/manifests/v1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get(contentDigestHeader) == "" {
		t.Fatalf("HEAD missing %s header", contentDigestHeader)
	}
}

// TestDaemonSourceMiss confirms an image the daemon does not have yields the
// standard 404, and a cold blob request (no prior manifest) also 404s.
func TestDaemonSourceMiss(t *testing.T) {
	fake := &fakeDaemon{tars: map[string][]byte{"app:v1": saveTar(t, "app:v1")}}
	srv := newDaemonRegistry(t, fake)

	resp, err := http.Get(srv.URL + "/v2/ghost/manifests/v1")
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("absent manifest status = %d, want 404", resp.StatusCode)
	}

	coldBlob := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	resp2, err := http.Get(srv.URL + "/v2/app/blobs/" + coldBlob)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("cold blob status = %d, want 404", resp2.StatusCode)
	}
}

// TestDaemonSourceEmptyListings confirms catalog and tags stay empty — the daemon
// has no catalog concept and the backing store holds nothing.
func TestDaemonSourceEmptyListings(t *testing.T) {
	fake := &fakeDaemon{tars: map[string][]byte{"app:v1": saveTar(t, "app:v1")}}
	srv := newDaemonRegistry(t, fake)
	if _, err := pullImage(t, srv, "app:v1"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if repos := catalogRepos(t, srv); len(repos) != 0 {
		t.Fatalf("catalog = %v, want empty (daemon source persists nothing)", repos)
	}
}

// TestDaemonSourceRejectsWrites confirms a pure re-export registry (no CAS) is
// read-only: write verbs return 405 rather than silently accepting content that
// would never reach the daemon.
func TestDaemonSourceRejectsWrites(t *testing.T) {
	fake := &fakeDaemon{tars: map[string][]byte{}}
	srv := newDaemonRegistry(t, fake)

	cases := []struct {
		method, path string
	}{
		{http.MethodPut, "/v2/app/manifests/v1"},
		{http.MethodDelete, "/v2/app/manifests/sha256:" + zeroDigest},
		{http.MethodPost, "/v2/app/blobs/uploads/"},
		{http.MethodDelete, "/v2/app/blobs/sha256:" + zeroDigest},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405 (read-only re-export)", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestDaemonSourceUnionRejectsWrites confirms host-native re-export is read-only
// even in "union mode" — a CAS co-resident with the daemon source. The E2E
// harness always starts the server with --storage, so the registry has a real
// content store (store != nil) alongside the daemon source. Writing to that CAS
// is meaningless in re-export mode (images are docker-loaded into the daemon,
// never pushed through /v2/*), so every write verb must still 405. Regression for
// the registry-host-native E2E, where PUT /v2/<repo>/manifests/v2 returned 201
// because readOnly() keyed only on store == nil.
func TestDaemonSourceUnionRejectsWrites(t *testing.T) {
	st, err := storage.Open(context.Background(), "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := &fakeDaemon{tars: map[string][]byte{}}
	reg := New(st, WithDaemonSource(fake, t.TempDir())) // store != nil AND a daemon source
	mux := http.NewServeMux()
	reg.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cases := []struct {
		method, path string
	}{
		{http.MethodPut, "/v2/app/manifests/v2"},
		{http.MethodDelete, "/v2/app/manifests/sha256:" + zeroDigest},
		{http.MethodPost, "/v2/app/blobs/uploads/"},
		{http.MethodDelete, "/v2/app/blobs/sha256:" + zeroDigest},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405 (read-only host-native re-export, union mode)", c.method, c.path, resp.StatusCode)
		}
	}
}

const zeroDigest = "0000000000000000000000000000000000000000000000000000000000000000"

// TestDaemonRefMapping checks tag vs digest reference translation.
func TestDaemonRefMapping(t *testing.T) {
	if got := daemonRef("app", "v1"); got != "app:v1" {
		t.Errorf("daemonRef tag = %q, want app:v1", got)
	}
	dig := "sha256:aa00000000000000000000000000000000000000000000000000000000000000"
	if got, want := daemonRef("team/app", dig), "team/app@"+dig; got != want {
		t.Errorf("daemonRef digest = %q, want %q", got, want)
	}
}
