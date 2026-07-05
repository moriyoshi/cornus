//go:build linux

package registry

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	digest "github.com/opencontainers/go-digest"

	"github.com/google/go-containerregistry/pkg/v1/random"
)

// fakeImages is an in-memory images.Store (namespace-agnostic) for tests.
type fakeImages struct{ m map[string]images.Image }

func newFakeImages() *fakeImages { return &fakeImages{m: map[string]images.Image{}} }

func (f *fakeImages) Get(_ context.Context, name string) (images.Image, error) {
	img, ok := f.m[name]
	if !ok {
		return images.Image{}, errdefs.ErrNotFound
	}
	return img, nil
}
func (f *fakeImages) List(_ context.Context, _ ...string) ([]images.Image, error) {
	out := make([]images.Image, 0, len(f.m))
	for _, img := range f.m {
		out = append(out, img)
	}
	return out, nil
}
func (f *fakeImages) Create(_ context.Context, img images.Image) (images.Image, error) {
	if _, ok := f.m[img.Name]; ok {
		return images.Image{}, errdefs.ErrAlreadyExists
	}
	f.m[img.Name] = img
	return img, nil
}
func (f *fakeImages) Update(_ context.Context, img images.Image, _ ...string) (images.Image, error) {
	if _, ok := f.m[img.Name]; !ok {
		return images.Image{}, errdefs.ErrNotFound
	}
	f.m[img.Name] = img
	return img, nil
}
func (f *fakeImages) Delete(_ context.Context, name string, _ ...images.DeleteOpt) error {
	if _, ok := f.m[name]; !ok {
		return errdefs.ErrNotFound
	}
	delete(f.m, name)
	return nil
}

// newTestContainerdStore wires a containerdStore to a real local content store
// (temp dir, no daemon) and an in-memory image store.
func newTestContainerdStore(t *testing.T) (*containerdStore, content.Store, *fakeImages) {
	t.Helper()
	cs, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("local content store: %v", err)
	}
	is := newFakeImages()
	s := newContainerdStore("", "cornus", t.TempDir())
	s.stores = func(ctx context.Context) (context.Context, content.Store, images.Store, error) {
		return ctx, cs, is, nil
	}
	return s, cs, is
}

func TestContainerdStoreBlobRoundTrip(t *testing.T) {
	s, _, _ := newTestContainerdStore(t)
	ctx := context.Background()
	data := []byte("hello blob bytes")
	dgst := digest.FromBytes(data).String()

	gotDigest, size, err := s.PutBlob(ctx, bytes.NewReader(data), dgst)
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if gotDigest != dgst || size != int64(len(data)) {
		t.Fatalf("PutBlob = %s/%d, want %s/%d", gotDigest, size, dgst, len(data))
	}
	if sz, err := s.StatBlob(ctx, dgst); err != nil || sz != int64(len(data)) {
		t.Fatalf("StatBlob = %d, %v", sz, err)
	}
	rc, err := s.GetBlob(ctx, dgst)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Fatalf("GetBlob = %q, want %q", got, data)
	}
	// An absent blob maps to storage.ErrNotFound (→ 404).
	if _, err := s.StatBlob(ctx, digest.FromBytes([]byte("nope")).String()); err == nil {
		t.Fatalf("StatBlob(absent) = nil, want not-found")
	}
}

func TestContainerdStoreUploadCommit(t *testing.T) {
	s, _, _ := newTestContainerdStore(t)
	ctx := context.Background()
	data := []byte("chunk-one|chunk-two|chunk-three")
	dgst := digest.FromBytes(data).String()

	up, err := s.NewUpload(ctx)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	// Resume across "requests" via GetUpload(id), writing in chunks.
	for _, chunk := range [][]byte{data[:9], data[9:19], data[19:]} {
		u, err := s.GetUpload(ctx, up.ID())
		if err != nil {
			t.Fatalf("GetUpload: %v", err)
		}
		if _, err := u.Write(ctx, bytes.NewReader(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		_ = u.Close()
	}
	u, _ := s.GetUpload(ctx, up.ID())
	gotDigest, size, err := s.CommitUpload(ctx, u, dgst)
	if err != nil {
		t.Fatalf("CommitUpload: %v", err)
	}
	if gotDigest != dgst || size != int64(len(data)) {
		t.Fatalf("CommitUpload = %s/%d, want %s/%d", gotDigest, size, dgst, len(data))
	}
	rc, _ := s.GetBlob(ctx, dgst)
	defer rc.Close()
	if got, _ := io.ReadAll(rc); !bytes.Equal(got, data) {
		t.Fatalf("committed blob = %q, want %q", got, data)
	}
}

func TestContainerdStoreManifestRoundTrip(t *testing.T) {
	s, _, is := newTestContainerdStore(t)
	ctx := context.Background()

	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	// Push config + layers, then the manifest — the order an OCI push uses.
	cfg, _ := img.RawConfigFile()
	cfgDig, _ := img.ConfigName()
	if _, _, err := s.PutBlob(ctx, bytes.NewReader(cfg), cfgDig.String()); err != nil {
		t.Fatalf("PutBlob config: %v", err)
	}
	layers, _ := img.Layers()
	for _, l := range layers {
		ld, _ := l.Digest()
		rc, _ := l.Compressed()
		lb, _ := io.ReadAll(rc)
		rc.Close()
		if _, _, err := s.PutBlob(ctx, bytes.NewReader(lb), ld.String()); err != nil {
			t.Fatalf("PutBlob layer: %v", err)
		}
	}
	raw, _ := img.RawManifest()
	mt, _ := img.MediaType()
	wantDig := digest.FromBytes(raw).String()

	gotDig, err := s.PutManifest(ctx, "app", "v1", string(mt), raw)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if gotDig != wantDig {
		t.Fatalf("PutManifest digest = %s, want %s", gotDig, wantDig)
	}
	// The image is recorded so a deploy resolves it by name.
	if _, ok := is.m["app:v1"]; !ok {
		t.Fatalf("PutManifest did not record image app:v1 (%v)", is.m)
	}
	// (GC-ref labels are set on the manifest content in production, where the
	// containerd client's metadata store persists them; the bare local content
	// store used here does not track labels, so label computation is verified
	// separately in TestManifestGCLabels.)

	// Read it back by tag and by digest.
	for _, ref := range []string{"v1", wantDig} {
		body, dg, gotMT, err := s.GetManifest(ctx, "app", ref)
		if err != nil {
			t.Fatalf("GetManifest(%s): %v", ref, err)
		}
		if !bytes.Equal(body, raw) || dg != wantDig || gotMT == "" {
			t.Fatalf("GetManifest(%s) mismatch: dg=%s mt=%s", ref, dg, gotMT)
		}
	}

	// Catalog and tags reflect the pushed image.
	if tags, _ := s.Tags(ctx, "app"); len(tags) != 1 || tags[0] != "v1" {
		t.Fatalf("Tags = %v, want [v1]", tags)
	}
	if repos, _ := s.Repos(ctx); len(repos) != 1 || repos[0] != "app" {
		t.Fatalf("Repos = %v, want [app]", repos)
	}
}

func TestManifestGCLabels(t *testing.T) {
	man := []byte(`{"config":{"digest":"sha256:aaa"},"layers":[{"digest":"sha256:bbb"},{"digest":"sha256:ccc"}]}`)
	l := manifestGCLabels(man)
	if l["containerd.io/gc.ref.content.config"] != "sha256:aaa" ||
		l["containerd.io/gc.ref.content.l.0"] != "sha256:bbb" ||
		l["containerd.io/gc.ref.content.l.1"] != "sha256:ccc" {
		t.Fatalf("manifest labels = %v", l)
	}
	idx := []byte(`{"manifests":[{"digest":"sha256:m0"},{"digest":"sha256:m1"}]}`)
	li := manifestGCLabels(idx)
	if li["containerd.io/gc.ref.content.m.0"] != "sha256:m0" || li["containerd.io/gc.ref.content.m.1"] != "sha256:m1" {
		t.Fatalf("index labels = %v", li)
	}
}

func TestMatchImageName(t *testing.T) {
	cases := []struct {
		name, repo, ref string
		want            bool
	}{
		{"127.0.0.1:5000/app:v1", "app", "v1", true},
		{"docker.io/library/app:v1", "app", "v1", true},
		{"app:v1", "app", "v1", true},
		{"docker.io/team/app:v1", "team/app", "v1", true},
		{"ghcr.io/x/app:v1", "app", "v1", false},
		{"127.0.0.1:5000/app:v2", "app", "v1", false},
	}
	for _, c := range cases {
		if got := matchImageName(c.name, c.repo, c.ref); got != c.want {
			t.Errorf("matchImageName(%q,%q,%q) = %v, want %v", c.name, c.repo, c.ref, got, c.want)
		}
	}
}
