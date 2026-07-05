package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"cornus/pkg/storage"
)

// newTestRegistryRef starts a registry over the storage backend named by ref.
func newTestRegistryRef(t *testing.T, ref string) *httptest.Server {
	t.Helper()
	st, err := storage.Open(context.Background(), ref, t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open(%q): %v", ref, err)
	}
	t.Cleanup(func() { st.Close() })
	mux := http.NewServeMux()
	New(st).Register(mux)
	return httptest.NewServer(mux)
}

// newTestRegistry starts a filesystem-backed registry (default backend).
func newTestRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestRegistryRef(t, t.TempDir())
}

// TestBackendsConformance runs the push/pull/tags round-trip against each
// in-process storage backend, proving identical registry behaviour.
func TestBackendsConformance(t *testing.T) {
	backends := map[string]string{
		"filesystem": t.TempDir(),
		"memory":     "mem://",
	}
	for name, ref := range backends {
		ref := ref
		t.Run(name, func(t *testing.T) {
			srv := newTestRegistryRef(t, ref)
			defer srv.Close()
			host := strings.TrimPrefix(srv.URL, "http://")
			pushPullTags(t, host)
		})
	}
}

// pushPullTags pushes two tags of a random image, pulls one back by digest, and
// verifies the tag listing.
func pushPullTags(t *testing.T, host string) {
	t.Helper()
	var lastDigest string
	for _, tag := range []string{"v1", "v2"} {
		img, err := random.Image(1024, 2)
		if err != nil {
			t.Fatalf("random.Image: %v", err)
		}
		ref, err := name.ParseReference(host+"/app:"+tag, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("push %s: %v", tag, err)
		}
		d, _ := img.Digest()
		lastDigest = d.String()
	}

	digRef, err := name.ParseReference(host+"/app@"+lastDigest, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Image(digRef); err != nil {
		t.Fatalf("pull by digest: %v", err)
	}

	repo, _ := name.NewRepository(host+"/app", name.Insecure)
	tags, err := remote.List(repo)
	if err != nil {
		t.Fatalf("remote.List: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range tags {
		got[tg] = true
	}
	if !got["v1"] || !got["v2"] {
		t.Fatalf("tags = %v, want v1 and v2", tags)
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	img, err := random.Image(1024, 3) // 3 layers
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	ref, err := name.ParseReference(host+"/library/demo:v1", name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}

	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("remote.Write (push): %v", err)
	}

	pulled, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("remote.Image (pull): %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("pulled digest %s != pushed %s", gotDigest, wantDigest)
	}

	// The layers must be retrievable and consistent.
	if _, err := pulled.Manifest(); err != nil {
		t.Fatalf("pulled manifest: %v", err)
	}
}

func TestPullByDigest(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	img, _ := random.Image(512, 1)
	tagRef, _ := name.ParseReference(host+"/app:latest", name.Insecure)
	if err := remote.Write(tagRef, img); err != nil {
		t.Fatalf("push: %v", err)
	}
	d, _ := img.Digest()
	digRef, err := name.ParseReference(host+"/app@"+d.String(), name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference digest: %v", err)
	}
	if _, err := remote.Image(digRef); err != nil {
		t.Fatalf("pull by digest: %v", err)
	}
}

func TestTagsList(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	for _, tag := range []string{"v1", "v2"} {
		img, _ := random.Image(256, 1)
		ref, _ := name.ParseReference(host+"/multi:"+tag, name.Insecure)
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("push %s: %v", tag, err)
		}
	}
	repo, _ := name.NewRepository(host+"/multi", name.Insecure)
	tags, err := remote.List(repo)
	if err != nil {
		t.Fatalf("remote.List: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range tags {
		got[tg] = true
	}
	if !got["v1"] || !got["v2"] {
		t.Fatalf("tags = %v, want v1 and v2", tags)
	}
}

// TestPutManifestByMismatchedDigest verifies that pushing a manifest by digest
// whose reference does not match the body's computed digest is rejected with
// 400 DIGEST_INVALID, per OCI, rather than silently reported as success.
func TestPutManifestByMismatchedDigest(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()

	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	// A syntactically valid but incorrect digest reference (all zeroes).
	wrongDigest := "sha256:" + strings.Repeat("0", 64)

	url := srv.URL + "/v2/app/manifests/" + wrongDigest
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT by mismatched digest status = %d, want 400", resp.StatusCode)
	}

	// And the content must not be reachable under the (wrong) digest the client used.
	getResp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET by mismatched digest status = %d, want 404", getResp.StatusCode)
	}
}

// TestPutManifestByCorrectDigest ensures a by-digest push whose reference matches
// the body's computed digest still succeeds after the mismatch check was added.
func TestPutManifestByCorrectDigest(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()

	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	sum := sha256.Sum256(body)
	correctDigest := "sha256:" + hex.EncodeToString(sum[:])

	url := srv.URL + "/v2/app/manifests/" + correctDigest
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT by correct digest status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get(contentDigestHeader); got != correctDigest {
		t.Fatalf("%s = %q, want %q", contentDigestHeader, got, correctDigest)
	}
}

// TestPutManifestTooLarge verifies that a manifest body larger than the
// per-manifest ceiling is rejected with 413 rather than silently truncated and
// stored (which would report a corrupt, digest-consistent fragment as success).
func TestPutManifestTooLarge(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()

	// One byte over the ceiling must be rejected.
	body := make([]byte, maxManifestSize+1)

	url := srv.URL + "/v2/app/manifests/big"
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT oversize manifest status = %d, want 413", resp.StatusCode)
	}

	// The truncated content must not have been stored under the tag.
	getResp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET manifest: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after rejected push status = %d, want 404", getResp.StatusCode)
	}
}

func TestPing(t *testing.T) {
	srv := newTestRegistry(t)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v2/ status = %d, want 200", resp.StatusCode)
	}
	if v := resp.Header.Get(apiVersionHeader); v != "registry/2.0" {
		t.Fatalf("%s = %q, want registry/2.0", apiVersionHeader, v)
	}
}

func TestParsePath(t *testing.T) {
	cases := []struct {
		path            string
		name, kind, ref string
		ok              bool
	}{
		{"/v2/library/app/blobs/sha256:abc", "library/app", "blob", "sha256:abc", true},
		{"/v2/app/manifests/v1", "app", "manifest", "v1", true},
		{"/v2/a/b/c/manifests/sha256:xy", "a/b/c", "manifest", "sha256:xy", true},
		{"/v2/app/blobs/uploads/", "app", "blob-upload", "", true},
		{"/v2/app/blobs/uploads/abc123", "app", "blob-upload", "abc123", true},
		{"/v2/app/tags/list", "app", "tags", "", true},
		{"/v2/", "", "", "", false},
	}
	for _, c := range cases {
		n, k, r, ok := parsePath(c.path)
		if ok != c.ok || (ok && (n != c.name || k != c.kind || r != c.ref)) {
			t.Errorf("parsePath(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.path, n, k, r, ok, c.name, c.kind, c.ref, c.ok)
		}
	}
}
