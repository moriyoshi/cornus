package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/storage"
)

// TestStorageUsageEndpoint drives GET /.cornus/v1/storage against a real storage
// backend seeded with a couple of blobs and asserts the non-destructive usage
// report reflects the CAS footprint.
func TestStorageUsageEndpoint(t *testing.T) {
	clearAuthEnv(t)

	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dir}, st)
	if err != nil {
		t.Fatal(err)
	}
	s.newBackend = func() (deploy.Backend, error) { return &fakeBackend{}, nil }

	// Seed two blobs directly into the CAS.
	var wantBytes int64
	for _, data := range [][]byte{bytes.Repeat([]byte("x"), 42), bytes.Repeat([]byte("y"), 58)} {
		_, size, err := st.PutBlob(context.Background(), bytes.NewReader(data), "")
		if err != nil {
			t.Fatalf("PutBlob: %v", err)
		}
		wantBytes += size
	}

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.cornus/v1/storage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /.cornus/v1/storage: code = %d, want 200", resp.StatusCode)
	}
	var out api.StorageUsage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CASBlobs != 2 {
		t.Errorf("CASBlobs = %d, want 2", out.CASBlobs)
	}
	if out.CASBytes != wantBytes {
		t.Errorf("CASBytes = %d, want %d", out.CASBytes, wantBytes)
	}
}

// TestStorageUsageMethodNotAllowed proves only GET is accepted.
func TestStorageUsageMethodNotAllowed(t *testing.T) {
	clearAuthEnv(t)

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/.cornus/v1/storage", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /.cornus/v1/storage: code = %d, want 405", resp.StatusCode)
	}
}
