package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cornus/pkg/storage"
)

// newTestRegistryStore starts a memory-backed registry and returns both the
// server and the backing store so tests can drive storage directly.
func newTestRegistryStore(t *testing.T, opts ...Option) (*httptest.Server, *storage.Backend) {
	t.Helper()
	st, err := storage.Open(context.Background(), "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mux := http.NewServeMux()
	New(st, opts...).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

func digestOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// putMonolithicBlob pushes a blob via a monolithic POST and returns the response.
func putMonolithicBlob(t *testing.T, base, repo, content string) *http.Response {
	t.Helper()
	u := fmt.Sprintf("%s/v2/%s/blobs/uploads/?digest=%s", base, repo, digestOf(content))
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(content))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestBlobDelete(t *testing.T) {
	srv, _ := newTestRegistryStore(t)
	content := "blob to delete"
	resp := putMonolithicBlob(t, srv.URL, "app", content)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("push status = %d, want 201", resp.StatusCode)
	}

	blobURL := fmt.Sprintf("%s/v2/app/blobs/%s", srv.URL, digestOf(content))
	req, _ := http.NewRequest(http.MethodDelete, blobURL, nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusAccepted {
		t.Fatalf("delete status = %d, want 202", del.StatusCode)
	}

	// Now gone.
	get, err := http.Get(blobURL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", get.StatusCode)
	}

	// Deleting again is 404.
	req2, _ := http.NewRequest(http.MethodDelete, blobURL, nil)
	del2, _ := http.DefaultClient.Do(req2)
	del2.Body.Close()
	if del2.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", del2.StatusCode)
	}
}

func TestBlobSizeLimitMonolithic(t *testing.T) {
	srv, _ := newTestRegistryStore(t, WithMaxBlobSize(16))
	content := strings.Repeat("A", 64) // exceeds the 16-byte cap
	resp := putMonolithicBlob(t, srv.URL, "app", content)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized monolithic PUT = %d, want 413", resp.StatusCode)
	}
}

func TestBlobSizeLimitChunked(t *testing.T) {
	srv, st := newTestRegistryStore(t, WithMaxBlobSize(16))

	// Start an upload session.
	resp, err := http.Post(srv.URL+"/v2/app/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("start upload: %v", err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("no upload Location")
	}

	// PATCH more than the cap.
	body := strings.Repeat("B", 64)
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+loc, strings.NewReader(body))
	patch, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	patch.Body.Close()
	if patch.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PATCH = %d, want 413", patch.StatusCode)
	}
	// The session must have been aborted, so no blob leaked to disk.
	if _, err := st.StatBlob(context.Background(), digestOf(body)); err != storage.ErrNotFound {
		t.Fatalf("blob leaked despite oversize: %v", err)
	}
}

func TestBlobSizeLimitAllowsLegitimate(t *testing.T) {
	srv, _ := newTestRegistryStore(t, WithMaxBlobSize(1<<20))
	content := "a legitimately small layer"
	resp := putMonolithicBlob(t, srv.URL, "app", content)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("legit blob = %d, want 201", resp.StatusCode)
	}
}

func TestBlobRangeRequest(t *testing.T) {
	srv, _ := newTestRegistryStore(t)
	content := "0123456789abcdef"
	resp := putMonolithicBlob(t, srv.URL, "app", content)
	resp.Body.Close()

	blobURL := fmt.Sprintf("%s/v2/app/blobs/%s", srv.URL, digestOf(content))

	// Full GET advertises byte ranges.
	full, _ := http.Get(blobURL)
	if full.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", full.Header.Get("Accept-Ranges"))
	}
	full.Body.Close()

	// bytes=5-9 => "56789".
	req, _ := http.NewRequest(http.MethodGet, blobURL, nil)
	req.Header.Set("Range", "bytes=5-9")
	part, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("range get: %v", err)
	}
	data, _ := io.ReadAll(part.Body)
	part.Body.Close()
	if part.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", part.StatusCode)
	}
	if string(data) != "56789" {
		t.Fatalf("range body = %q, want 56789", data)
	}
	if cr := part.Header.Get("Content-Range"); cr != "bytes 5-9/16" {
		t.Fatalf("Content-Range = %q, want bytes 5-9/16", cr)
	}

	// Suffix range bytes=-4 => "cdef".
	req2, _ := http.NewRequest(http.MethodGet, blobURL, nil)
	req2.Header.Set("Range", "bytes=-4")
	suf, _ := http.DefaultClient.Do(req2)
	sufData, _ := io.ReadAll(suf.Body)
	suf.Body.Close()
	if suf.StatusCode != http.StatusPartialContent || string(sufData) != "cdef" {
		t.Fatalf("suffix range = %d %q, want 206 cdef", suf.StatusCode, sufData)
	}

	// Unsatisfiable range => 416.
	req3, _ := http.NewRequest(http.MethodGet, blobURL, nil)
	req3.Header.Set("Range", "bytes=100-200")
	un, _ := http.DefaultClient.Do(req3)
	un.Body.Close()
	if un.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range = %d, want 416", un.StatusCode)
	}
}

func TestReferrersAPI(t *testing.T) {
	srv, st := newTestRegistryStore(t)
	ctx := context.Background()

	cfg, _, _ := st.PutBlob(ctx, strings.NewReader("cfg"), "")
	subjManifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": cfg},
	})
	subject, err := st.PutManifest(ctx, "app", "v1", "application/vnd.oci.image.manifest.v1+json", subjManifest)
	if err != nil {
		t.Fatalf("put subject: %v", err)
	}

	sigCfg, _, _ := st.PutBlob(ctx, strings.NewReader("sigcfg"), "")
	sigManifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"artifactType":  "application/vnd.example.sig",
		"config":        map[string]any{"digest": sigCfg},
		"subject":       map[string]any{"digest": subject},
	})
	sigDigest, err := st.PutManifest(ctx, "app", digestSuffix(t, sigManifest), "application/vnd.oci.image.manifest.v1+json", sigManifest)
	if err != nil {
		t.Fatalf("put sig: %v", err)
	}

	// GET referrers.
	resp, err := http.Get(fmt.Sprintf("%s/v2/app/referrers/%s", srv.URL, subject))
	if err != nil {
		t.Fatalf("referrers get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("referrers status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.oci.image.index.v1+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var index struct {
		MediaType string `json:"mediaType"`
		Manifests []struct {
			Digest       string `json:"digest"`
			ArtifactType string `json:"artifactType"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest != sigDigest {
		t.Fatalf("referrers = %+v, want one entry %s", index.Manifests, sigDigest)
	}

	// Filter by matching artifactType includes the entry and sets the header.
	f, _ := http.Get(fmt.Sprintf("%s/v2/app/referrers/%s?artifactType=application/vnd.example.sig", srv.URL, subject))
	if f.Header.Get("OCI-Filters-Applied") != "artifactType" {
		t.Fatalf("OCI-Filters-Applied = %q", f.Header.Get("OCI-Filters-Applied"))
	}
	var fi struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	json.NewDecoder(f.Body).Decode(&fi)
	f.Body.Close()
	if len(fi.Manifests) != 1 {
		t.Fatalf("filtered (match) manifests = %d, want 1", len(fi.Manifests))
	}

	// Filter by a non-matching artifactType yields an empty set.
	nf, _ := http.Get(fmt.Sprintf("%s/v2/app/referrers/%s?artifactType=application/vnd.other", srv.URL, subject))
	var nfi struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	json.NewDecoder(nf.Body).Decode(&nfi)
	nf.Body.Close()
	if len(nfi.Manifests) != 0 {
		t.Fatalf("filtered (no match) manifests = %d, want 0", len(nfi.Manifests))
	}
}

// digestSuffix returns the sha256 digest of data, used to push a manifest by
// digest (so it does not create a tag).
func digestSuffix(t *testing.T, data []byte) string {
	t.Helper()
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func TestTagsPagination(t *testing.T) {
	srv, st := newTestRegistryStore(t)
	ctx := context.Background()
	manifest := []byte(`{"schemaVersion":2}`)
	for _, tag := range []string{"a", "b", "c", "d"} {
		if _, err := st.PutManifest(ctx, "app", tag, "application/vnd.oci.image.manifest.v1+json", manifest); err != nil {
			t.Fatalf("put %s: %v", tag, err)
		}
	}

	// First page: n=2 => a, b with a Link to the next page.
	resp, _ := http.Get(srv.URL + "/v2/app/tags/list?n=2")
	var page struct {
		Tags []string `json:"tags"`
	}
	json.NewDecoder(resp.Body).Decode(&page)
	link := resp.Header.Get("Link")
	resp.Body.Close()
	if !equalStrings(page.Tags, []string{"a", "b"}) {
		t.Fatalf("page 1 = %v, want [a b]", page.Tags)
	}
	if !strings.Contains(link, `rel="next"`) || !strings.Contains(link, "last=b") {
		t.Fatalf("Link = %q, want next with last=b", link)
	}

	// Second page: last=b => c, d and no Link (final page).
	resp2, _ := http.Get(srv.URL + "/v2/app/tags/list?n=2&last=b")
	var page2 struct {
		Tags []string `json:"tags"`
	}
	json.NewDecoder(resp2.Body).Decode(&page2)
	link2 := resp2.Header.Get("Link")
	resp2.Body.Close()
	if !equalStrings(page2.Tags, []string{"c", "d"}) {
		t.Fatalf("page 2 = %v, want [c d]", page2.Tags)
	}
	if link2 != "" {
		t.Fatalf("final page Link = %q, want empty", link2)
	}
}

func TestCatalogPagination(t *testing.T) {
	srv, st := newTestRegistryStore(t)
	ctx := context.Background()
	manifest := []byte(`{"schemaVersion":2}`)
	for _, repo := range []string{"r1", "r2", "r3"} {
		if _, err := st.PutManifest(ctx, repo, "v1", "application/vnd.oci.image.manifest.v1+json", manifest); err != nil {
			t.Fatalf("put %s: %v", repo, err)
		}
	}
	resp, _ := http.Get(srv.URL + "/v2/_catalog?n=2")
	var cat struct {
		Repositories []string `json:"repositories"`
	}
	json.NewDecoder(resp.Body).Decode(&cat)
	link := resp.Header.Get("Link")
	resp.Body.Close()
	if !equalStrings(cat.Repositories, []string{"r1", "r2"}) {
		t.Fatalf("catalog page = %v, want [r1 r2]", cat.Repositories)
	}
	if !strings.Contains(link, "_catalog") || !strings.Contains(link, "last=r2") {
		t.Fatalf("Link = %q, want catalog next with last=r2", link)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
