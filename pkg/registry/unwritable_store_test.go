package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/pkg/storage"
)

// TestBlobPushOnUnwritableStore proves a push that cannot be persisted because
// the blob store is not writable reports that specifically, instead of a bare
// 500 carrying only a raw syscall string.
//
// This is the failure a data directory populated by a PRIVILEGED run produces
// once the server is restarted as an ordinary user: the blob shard directories
// are owned by root, so creating a new shard fails with EACCES on every push.
// Reproduced here without root by making the blob root read-only for its own
// owner, which yields the same EACCES from the same MkdirAll.
func TestBlobPushOnUnwritableStore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the store cannot be made unwritable")
	}
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mux := http.NewServeMux()
	New(st).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Start the upload while the store is still writable, so the failure lands on
	// the commit — exactly where a real push fails.
	resp, err := http.Post(srv.URL+"/v2/app/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if loc == "" {
		t.Fatalf("no upload Location (status %d)", resp.StatusCode)
	}

	blobs := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blobs, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore so t.TempDir cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(blobs, 0o755) })

	body := "unwritable-store-payload"
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+loc+"?digest="+digestOf(body), strings.NewReader(body))
	put, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer put.Body.Close()
	raw, _ := io.ReadAll(put.Body)

	if put.StatusCode == http.StatusCreated {
		t.Skip("store still writable (unusual filesystem or capability); nothing to assert")
	}
	if put.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unwritable store: status=%d body=%s, want 503", put.StatusCode, raw)
	}

	var got struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("response is not an OCI error document: %v (%s)", err, raw)
	}
	if len(got.Errors) != 1 {
		t.Fatalf("want exactly one error, got %d (%s)", len(got.Errors), raw)
	}
	if got.Errors[0].Code != "UNAVAILABLE" {
		t.Errorf("code = %q, want UNAVAILABLE", got.Errors[0].Code)
	}
	// The message must be actionable: it has to say the storage is unwritable and
	// name the uid, since "which user is this running as" is the whole diagnosis.
	msg := got.Errors[0].Message
	if !strings.Contains(msg, "not writable") {
		t.Errorf("message does not say the storage is unwritable: %q", msg)
	}
	if !strings.Contains(msg, "uid") {
		t.Errorf("message does not name the uid: %q", msg)
	}
}

// TestBlobPushOnWritableStoreUnaffected pins that the new branch is inert on a
// healthy store: a normal push still succeeds with 201.
func TestBlobPushOnWritableStoreUnaffected(t *testing.T) {
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mux := http.NewServeMux()
	New(st).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v2/app/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()

	body := "healthy-store-payload"
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+loc+"?digest="+digestOf(body), strings.NewReader(body))
	put, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer put.Body.Close()
	if put.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(put.Body)
		t.Fatalf("healthy push: status=%d body=%s, want 201", put.StatusCode, raw)
	}
}
