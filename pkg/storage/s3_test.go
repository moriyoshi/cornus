package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// TestS3Backend exercises the S3 ObjectStore against an S3-compatible endpoint
// (the winterbaume mock or MinIO). It is opt-in: set CORNUS_TEST_S3_ENDPOINT
// to the endpoint URL to run it. Optional env: CORNUS_TEST_S3_BUCKET (default
// "cornus"), AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (default "test").
//
// Run winterbaume first:
//
//	cd ../winterbaume && ./.agents/bin/cargo.sh run -p winterbaume-server -- --host 127.0.0.1 --port 5555
//	CORNUS_TEST_S3_ENDPOINT=http://127.0.0.1:5555 go test ./pkg/storage/ -run S3 -v
func TestS3Backend(t *testing.T) {
	endpoint := os.Getenv("CORNUS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CORNUS_TEST_S3_ENDPOINT to run the S3 integration test (e.g. winterbaume)")
	}
	bucket := os.Getenv("CORNUS_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "cornus"
	}
	ak := envOr("AWS_ACCESS_KEY_ID", "test")
	sk := envOr("AWS_SECRET_ACCESS_KEY", "test")
	ctx := context.Background()

	ensureBucket(t, ctx, endpoint, bucket, ak, sk)

	ref := fmt.Sprintf("s3://%s?region=us-east-1&endpoint=%s&path_style=true&access_key=%s&secret_key=%s",
		bucket, url.QueryEscape(endpoint), url.QueryEscape(ak), url.QueryEscape(sk))
	b, err := Open(ctx, ref, t.TempDir())
	if err != nil {
		t.Fatalf("open S3 backend: %v", err)
	}
	defer b.Close()

	// Blob round-trip: put / stat / get.
	content := "cornus over s3"
	digest, size, err := b.PutBlob(ctx, strings.NewReader(content), "")
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if sz, err := b.StatBlob(ctx, digest); err != nil || sz != size {
		t.Fatalf("StatBlob = %d, %v", sz, err)
	}
	rc, err := b.GetBlob(ctx, digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != content {
		t.Fatalf("GetBlob = %q", data)
	}

	// Manifest + tag round-trip, including List-backed Tags / Repos.
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	mt := "application/vnd.oci.image.manifest.v1+json"
	mdigest, err := b.PutManifest(ctx, "team/app", "v1", mt, manifest)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	got, d, gotMT, err := b.GetManifest(ctx, "team/app", "v1")
	if err != nil || string(got) != string(manifest) || d != mdigest || gotMT != mt {
		t.Fatalf("GetManifest = %q, %s, %q, %v", got, d, gotMT, err)
	}
	tags, err := b.Tags(ctx, "team/app")
	if err != nil || len(tags) != 1 || tags[0] != "v1" {
		t.Fatalf("Tags = %v, %v", tags, err)
	}
	repos, err := b.Repos(ctx)
	if err != nil || len(repos) == 0 {
		t.Fatalf("Repos = %v, %v", repos, err)
	}

	// Delete path.
	if err := b.DeleteManifest(ctx, "team/app", mdigest); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
}

// TestS3MultipartUpload drives the resumable upload path (NewUpload / Write /
// CommitUpload) against a real S3-compatible endpoint, proving that native S3
// multipart streaming works and that session state survives across the separate
// requests of the OCI upload protocol (simulated by GetUpload between writes).
// Opt-in via CORNUS_TEST_S3_ENDPOINT, like TestS3Backend.
func TestS3MultipartUpload(t *testing.T) {
	endpoint := os.Getenv("CORNUS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CORNUS_TEST_S3_ENDPOINT to run the S3 integration test (e.g. winterbaume)")
	}
	bucket := os.Getenv("CORNUS_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "cornus"
	}
	ak := envOr("AWS_ACCESS_KEY_ID", "test")
	sk := envOr("AWS_SECRET_ACCESS_KEY", "test")
	ctx := context.Background()

	ensureBucket(t, ctx, endpoint, bucket, ak, sk)

	ref := fmt.Sprintf("s3://%s?region=us-east-1&endpoint=%s&path_style=true&access_key=%s&secret_key=%s",
		bucket, url.QueryEscape(endpoint), url.QueryEscape(ak), url.QueryEscape(sk))
	b, err := Open(ctx, ref, t.TempDir())
	if err != nil {
		t.Fatalf("open S3 backend: %v", err)
	}
	defer b.Close()

	// Three chunks whose total exceeds the 5 MiB minimum part size, with the
	// first two individually >= 5 MiB so they force distinct multipart parts and
	// the last one is a small tail flushed at commit.
	const part = 5*1024*1024 + 137
	c1 := bytesRepeat(0x41, part)
	c2 := bytesRepeat(0x42, part)
	c3 := bytesRepeat(0x43, 4096)
	full := append(append(append([]byte{}, c1...), c2...), c3...)

	h := sha256.New()
	h.Write(full)
	wantDigest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	wantSize := int64(len(full))

	u, err := b.NewUpload(ctx)
	if err != nil {
		t.Fatalf("NewUpload: %v", err)
	}
	id := u.ID()

	writeChunk := func(u Upload, data []byte) Upload {
		if _, err := u.Write(ctx, bytes.NewReader(data)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		// Reopen via GetUpload to prove cross-request state survival.
		ru, err := b.GetUpload(ctx, id)
		if err != nil {
			t.Fatalf("GetUpload: %v", err)
		}
		return ru
	}
	u = writeChunk(u, c1)
	u = writeChunk(u, c2)
	u = writeChunk(u, c3)

	digest, size, err := b.CommitUpload(ctx, u, wantDigest)
	if err != nil {
		t.Fatalf("CommitUpload: %v", err)
	}
	if digest != wantDigest || size != wantSize {
		t.Fatalf("CommitUpload = %s, %d; want %s, %d", digest, size, wantDigest, wantSize)
	}

	rc, err := b.GetBlob(ctx, digest)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, full) {
		t.Fatalf("GetBlob length = %d, want %d (content mismatch)", len(got), len(full))
	}

	// Negative: committing with a wrong expected digest must fail and leave no
	// blob behind.
	u2, err := b.NewUpload(ctx)
	if err != nil {
		t.Fatalf("NewUpload (negative): %v", err)
	}
	if _, err := u2.Write(ctx, bytes.NewReader(c1)); err != nil {
		t.Fatalf("Write (negative): %v", err)
	}
	wrong := "sha256:" + strings.Repeat("0", 64)
	if _, _, err := b.CommitUpload(ctx, u2, wrong); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("CommitUpload with wrong digest = %v; want ErrDigestMismatch", err)
	}
	// The mismatched content hashes to c1's real digest; that blob must not exist.
	hc1 := sha256.New()
	hc1.Write(c1)
	if _, err := b.StatBlob(ctx, "sha256:"+hex.EncodeToString(hc1.Sum(nil))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StatBlob after mismatch = %v; want ErrNotFound", err)
	}
}

// bytesRepeat returns a slice of n bytes all set to v.
func bytesRepeat(v byte, n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = v
	}
	return buf
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ensureBucket creates the test bucket, tolerating "already exists".
func ensureBucket(t *testing.T, ctx context.Context, endpoint, bucket, ak, sk string) {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(ak, sk, "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
				return
			}
		}
		t.Fatalf("create bucket %q: %v", bucket, err)
	}
}
