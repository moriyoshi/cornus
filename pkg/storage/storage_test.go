package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

// backendFactories returns one constructor per backend under test. Each runs the
// full CAS / upload / manifest / tag suite, proving identical behaviour.
func backendFactories(t *testing.T) map[string]func() *Backend {
	return map[string]func() *Backend{
		"filesystem": func() *Backend {
			dir := t.TempDir()
			b, err := Open(context.Background(), dir, dir+"/uploads")
			if err != nil {
				t.Fatalf("open filesystem: %v", err)
			}
			return b
		},
		"memory": func() *Backend {
			b, err := Open(context.Background(), "mem://", t.TempDir())
			if err != nil {
				t.Fatalf("open mem: %v", err)
			}
			return b
		},
	}
}

func sha256Of(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

func TestBackends(t *testing.T) {
	for name, factory := range backendFactories(t) {
		factory := factory
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			t.Run("blob put/get/stat", func(t *testing.T) {
				b := factory()
				defer b.Close()
				content := "hello cornus"
				want := sha256Of(content)

				got, size, err := b.PutBlob(ctx, strings.NewReader(content), "")
				if err != nil || got != want || size != int64(len(content)) {
					t.Fatalf("PutBlob = %s, %d, %v", got, size, err)
				}
				if sz, err := b.StatBlob(ctx, want); err != nil || sz != int64(len(content)) {
					t.Fatalf("StatBlob = %d, %v", sz, err)
				}
				rc, err := b.GetBlob(ctx, want)
				if err != nil {
					t.Fatalf("GetBlob: %v", err)
				}
				data, _ := io.ReadAll(rc)
				rc.Close()
				if string(data) != content {
					t.Fatalf("GetBlob = %q", data)
				}
				if _, err := b.StatBlob(ctx, sha256Of("absent")); err != ErrNotFound {
					t.Fatalf("StatBlob(absent) = %v, want ErrNotFound", err)
				}
			})

			t.Run("blob digest mismatch", func(t *testing.T) {
				b := factory()
				defer b.Close()
				if _, _, err := b.PutBlob(ctx, strings.NewReader("data"), sha256Of("other")); err == nil {
					t.Fatal("expected digest mismatch")
				}
			})

			t.Run("chunked upload commit", func(t *testing.T) {
				b := factory()
				defer b.Close()
				u, err := b.NewUpload(ctx)
				if err != nil {
					t.Fatalf("NewUpload: %v", err)
				}
				id := u.ID()
				if _, err := u.Write(ctx, strings.NewReader("foo")); err != nil {
					t.Fatalf("Write: %v", err)
				}
				u.Close()

				// Resume in a fresh session handle, as the registry does per request.
				u2, err := b.GetUpload(ctx, id)
				if err != nil {
					t.Fatalf("GetUpload: %v", err)
				}
				total, err := u2.Write(ctx, strings.NewReader("bar"))
				if err != nil || total != 6 {
					t.Fatalf("Write resume = %d, %v", total, err)
				}
				digest, size, err := b.CommitUpload(ctx, u2, sha256Of("foobar"))
				if err != nil || digest != sha256Of("foobar") || size != 6 {
					t.Fatalf("CommitUpload = %s, %d, %v", digest, size, err)
				}
				if _, err := b.StatBlob(ctx, digest); err != nil {
					t.Fatalf("committed blob missing: %v", err)
				}
			})

			t.Run("upload abort", func(t *testing.T) {
				b := factory()
				defer b.Close()
				u, _ := b.NewUpload(ctx)
				id := u.ID()
				u.Write(ctx, strings.NewReader("x"))
				u.Close()
				if err := b.AbortUpload(ctx, id); err != nil {
					t.Fatalf("AbortUpload: %v", err)
				}
				if _, err := b.GetUpload(ctx, id); err != ErrNotFound {
					t.Fatalf("GetUpload after abort = %v, want ErrNotFound", err)
				}
			})

			t.Run("manifests and tags", func(t *testing.T) {
				b := factory()
				defer b.Close()
				manifest := []byte(`{"schemaVersion":2}`)
				mt := "application/vnd.oci.image.manifest.v1+json"
				digest, err := b.PutManifest(ctx, "library/app", "v1", mt, manifest)
				if err != nil {
					t.Fatalf("PutManifest: %v", err)
				}

				content, d, gotMT, err := b.GetManifest(ctx, "library/app", "v1")
				if err != nil || string(content) != string(manifest) || d != digest || gotMT != mt {
					t.Fatalf("GetManifest by tag = %q, %s, %q, %v", content, d, gotMT, err)
				}
				if _, _, _, err := b.GetManifest(ctx, "library/app", digest); err != nil {
					t.Fatalf("GetManifest by digest: %v", err)
				}
				// Cross-repo isolation: same digest must not resolve elsewhere.
				if _, _, _, err := b.GetManifest(ctx, "other/repo", digest); err != ErrNotFound {
					t.Fatalf("cross-repo manifest = %v, want ErrNotFound", err)
				}

				tags, err := b.Tags(ctx, "library/app")
				if err != nil || len(tags) != 1 || tags[0] != "v1" {
					t.Fatalf("Tags = %v, %v", tags, err)
				}
				repos, err := b.Repos(ctx)
				if err != nil || len(repos) != 1 || repos[0] != "library/app" {
					t.Fatalf("Repos = %v, %v", repos, err)
				}

				if err := b.DeleteManifest(ctx, "library/app", digest); err != nil {
					t.Fatalf("DeleteManifest: %v", err)
				}
				if _, _, _, err := b.GetManifest(ctx, "library/app", digest); err != ErrNotFound {
					t.Fatalf("GetManifest after delete = %v, want ErrNotFound", err)
				}
			})

			t.Run("blob dedup", func(t *testing.T) {
				b := factory()
				defer b.Close()
				d1, _, _ := b.PutBlob(ctx, strings.NewReader("same"), "")
				d2, _, _ := b.PutBlob(ctx, strings.NewReader("same"), "")
				if d1 != d2 {
					t.Fatalf("dedup digests differ: %s %s", d1, d2)
				}
			})
		})
	}
}

func TestParseDigest(t *testing.T) {
	if _, _, err := ParseDigest("sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	for _, bad := range []string{"nope", "md5:abc", "sha256:short", "sha256:" + strings.Repeat("z", 64)} {
		if _, _, err := ParseDigest(bad); err == nil {
			t.Errorf("ParseDigest(%q) accepted, want error", bad)
		}
	}
}
