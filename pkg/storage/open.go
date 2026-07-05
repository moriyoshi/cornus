package storage

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gocloud.dev/blob"
	"gocloud.dev/blob/memblob"
	s3blob "gocloud.dev/blob/s3blob"
)

// Open builds a registry storage Backend from a reference. Supported forms:
//
//	<path> or file://<path>   local filesystem (default; native uploads)
//	mem://                    in-memory, ephemeral (tests)
//	s3://<bucket>?region=&endpoint=&path_style=&access_key=&secret_key=
//	                          S3 / S3-compatible (e.g. MinIO, winterbaume)
//	gs://<bucket>             Google Cloud Storage (via gocloud; untested)
//	azblob://<container>      Azure Blob (via gocloud; untested)
//
// stagingDir holds resumable upload staging and temporary blob writes.
func Open(ctx context.Context, ref, stagingDir string) (*Backend, error) {
	if ref == "" {
		return nil, fmt.Errorf("storage: empty reference")
	}
	u, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("storage: parse reference %q: %w", ref, err)
	}

	switch u.Scheme {
	case "", "file":
		root := filesystemRoot(ref, u)
		obj, err := NewFilesystem(root, stagingDir)
		if err != nil {
			return nil, err
		}
		return NewBackend(obj, stagingDir)

	case "mem":
		return NewBackend(newBlobObjectStore(memblob.OpenBucket(nil)), stagingDir)

	case "s3":
		bucket, client, err := openS3Bucket(ctx, u)
		if err != nil {
			return nil, err
		}
		return NewBackend(newS3ObjectStore(bucket, client, u.Host, stagingDir), stagingDir)

	default:
		// gs:// and azblob:// need the Google/Azure gocloud drivers, which are only
		// registered in a `-tags cloudblob` build (they pull in the cloud SDKs). Give
		// a clear error in the default build rather than gocloud's raw "no driver".
		if !cloudBlobBuilt && (u.Scheme == "gs" || u.Scheme == "azblob") {
			return nil, fmt.Errorf("storage: %s:// is not supported in this build; rebuild with -tags cloudblob to enable Google Cloud Storage / Azure Blob", u.Scheme)
		}
		// Pass through any gocloud-registered scheme.
		bucket, err := blob.OpenBucket(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("storage: open %q: %w", ref, err)
		}
		return NewBackend(newBlobObjectStore(bucket), stagingDir)
	}
}

// filesystemRoot resolves the local directory for a file reference. A bare path
// (no scheme) is used verbatim; file://<path> uses the URL host+path.
func filesystemRoot(ref string, u *url.URL) string {
	if u.Scheme == "" {
		return ref
	}
	if u.Host != "" {
		return filepath.Join(u.Host, u.Path)
	}
	return u.Path
}

// openS3Bucket opens an S3 (or S3-compatible) bucket. The bucket name is the URL
// host; query params configure region, a custom endpoint, path-style addressing,
// and static credentials (for mocks like winterbaume / MinIO). The raw *s3.Client
// is returned alongside the gocloud bucket so the S3 ObjectStore can reuse it for
// native multipart uploads.
func openS3Bucket(ctx context.Context, u *url.URL) (*blob.Bucket, *s3.Client, error) {
	q := u.Query()
	region := q.Get("region")
	if region == "" {
		region = "us-east-1"
	}

	loadOpts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if ak, sk := q.Get("access_key"), q.Get("secret_key"); ak != "" {
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(ak, sk, "")))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: load aws config: %w", err)
	}

	endpoint := q.Get("endpoint")
	pathStyle, _ := strconv.ParseBool(q.Get("path_style"))
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		if pathStyle {
			o.UsePathStyle = true
		}
	})

	bucket, err := s3blob.OpenBucket(ctx, client, u.Host, nil)
	if err != nil {
		return nil, nil, err
	}
	return bucket, client, nil
}
