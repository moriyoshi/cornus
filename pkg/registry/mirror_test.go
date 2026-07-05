package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"cornus/pkg/storage"
)

// newMirrorRegistry starts a registry with the given mirror (nil for none).
func newMirrorRegistry(t *testing.T, m *Mirror) *httptest.Server {
	t.Helper()
	st, err := storage.Open(context.Background(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mux := http.NewServeMux()
	var opts []Option
	if m != nil {
		opts = append(opts, WithMirror(m))
	}
	New(st, opts...).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func regHost(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

// seedImage pushes a random image to srv at repoTag and returns it.
func seedImage(t *testing.T, srv *httptest.Server, repoTag string) v1.Image {
	t.Helper()
	img, err := random.Image(512, 2)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	ref, err := name.ParseReference(regHost(srv)+"/"+repoTag, name.Insecure)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	return img
}

// pullImage pulls host/repoTag over plain HTTP.
func pullImage(t *testing.T, srv *httptest.Server, repoTag string) (v1.Image, error) {
	t.Helper()
	ref, err := name.ParseReference(regHost(srv)+"/"+repoTag, name.Insecure)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return remote.Image(ref, remote.WithContext(context.Background()))
}

func catalogRepos(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v2/_catalog")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return body.Repositories
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestMirrorCachePull pulls an image absent locally through a caching mirror and
// confirms it is served correctly and persisted into the local store.
func TestMirrorCachePull(t *testing.T) {
	upstream := newMirrorRegistry(t, nil)
	want := seedImage(t, upstream, "app:v1")
	wantDigest, _ := want.Digest()

	down := newMirrorRegistry(t, &Mirror{Host: regHost(upstream), Cache: true})
	got, err := pullImage(t, down, "app:v1")
	if err != nil {
		t.Fatalf("mirror pull: %v", err)
	}
	if gd, _ := got.Digest(); gd != wantDigest {
		t.Fatalf("pulled digest = %s, want %s", gd, wantDigest)
	}
	// Cached: the repo now exists locally.
	if repos := catalogRepos(t, down); !contains(repos, "app") {
		t.Fatalf("catalog = %v, want it to contain app after caching pull", repos)
	}
}

// TestMirrorTransparentNoCache pulls through a non-caching mirror: the pull
// succeeds but nothing is persisted locally.
func TestMirrorTransparentNoCache(t *testing.T) {
	upstream := newMirrorRegistry(t, nil)
	want := seedImage(t, upstream, "app:v1")
	wantDigest, _ := want.Digest()

	down := newMirrorRegistry(t, &Mirror{Host: regHost(upstream), Cache: false})
	got, err := pullImage(t, down, "app:v1")
	if err != nil {
		t.Fatalf("transparent pull: %v", err)
	}
	if gd, _ := got.Digest(); gd != wantDigest {
		t.Fatalf("pulled digest = %s, want %s", gd, wantDigest)
	}
	if repos := catalogRepos(t, down); contains(repos, "app") {
		t.Fatalf("catalog = %v, want empty (transparent mirror must not persist)", repos)
	}
}

// TestMirrorLocalHitPrecedence confirms a locally-present image is served from
// the store without consulting the mirror (which points at a dead host).
func TestMirrorLocalHitPrecedence(t *testing.T) {
	down := newMirrorRegistry(t, &Mirror{Host: "127.0.0.1:1", Cache: true})
	want := seedImage(t, down, "app:v1") // pushed straight into the local store
	wantDigest, _ := want.Digest()

	got, err := pullImage(t, down, "app:v1")
	if err != nil {
		t.Fatalf("local pull with dead mirror: %v", err)
	}
	if gd, _ := got.Digest(); gd != wantDigest {
		t.Fatalf("pulled digest = %s, want %s", gd, wantDigest)
	}
}

// TestMirrorMissReturns404 confirms an image absent both locally and upstream
// still yields the standard 404.
func TestMirrorMissReturns404(t *testing.T) {
	upstream := newMirrorRegistry(t, nil) // empty
	down := newMirrorRegistry(t, &Mirror{Host: regHost(upstream), Cache: true})

	resp, err := http.Get(down.URL + "/v2/ghost/manifests/v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for absent image", resp.StatusCode)
	}
}
