package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gocloud.dev/blob"
)

// s3MinPartSize is the S3 minimum size for every multipart part except the last
// (5 MiB). Chunks smaller than this are coalesced in the persisted tail buffer
// until they cross the threshold, then flushed as a part.
const s3MinPartSize = 5 * 1024 * 1024

// s3MaxSingleCopy is the largest source object S3 (and S3-compatible stores)
// will copy in a single CopyObject call (5 GiB). Larger objects must be copied
// with a multipart UploadPartCopy loop.
const s3MaxSingleCopy = 5 * 1024 * 1024 * 1024

// s3CopyPartSize is the byte-range size each UploadPartCopy part copies when an
// object exceeds s3MaxSingleCopy. It stays at or below the 5 GiB per-part copy
// limit.
const s3CopyPartSize = 4 * 1024 * 1024 * 1024

// s3ObjectStore is the S3 (and S3-compatible) ObjectStore. It delegates the
// plain get/put/stat/delete/list/close surface to an embedded blobObjectStore
// (the gocloud bucket) and additionally implements NativeUploader: resumable OCI
// uploads stream straight to S3 via the multipart API instead of being staged
// whole to a local file. Only a bounded per-session sidecar (the sub-5-MiB tail
// plus small metadata) is persisted locally, in the Backend staging dir.
type s3ObjectStore struct {
	*blobObjectStore
	client     *s3.Client
	bucket     string
	stagingDir string
}

// newS3ObjectStore builds an S3 ObjectStore that reuses client for multipart
// operations. bucket is the S3 bucket name; stagingDir is the Backend's staging
// directory, used to persist the small resumable-upload session sidecars.
func newS3ObjectStore(bucket *blob.Bucket, client *s3.Client, bucketName, stagingDir string) *s3ObjectStore {
	return &s3ObjectStore{
		blobObjectStore: &blobObjectStore{bucket: bucket},
		client:          client,
		bucket:          bucketName,
		stagingDir:      stagingDir,
	}
}

// uploadKey is the temporary S3 key an in-progress multipart upload assembles at
// before its content-addressed digest is known.
func uploadKey(id string) string { return "uploads/" + id }

func (s *s3ObjectStore) sidecarPath(id string) string {
	return filepath.Join(s.stagingDir, "s3upload-"+sanitize(id)+".json")
}

// --- session persistence ----------------------------------------------------

// s3Session is the durable state of an in-progress multipart upload. It is small
// and bounded (metadata + a sub-5-MiB tail + the marshaled sha256 state), so it
// can persist across the separate HTTP requests of the OCI upload protocol as a
// JSON sidecar without staging the whole blob locally.
type s3Session struct {
	UploadID  string   `json:"upload_id"`  // S3 multipart upload id
	Key       string   `json:"key"`        // temp S3 key ("uploads/<id>")
	Parts     []s3Part `json:"parts"`      // completed parts, in order
	HashState []byte   `json:"hash_state"` // marshaled crypto/sha256 state
	Tail      []byte   `json:"tail"`       // pending bytes not yet a part (< 5 MiB)
	Total     int64    `json:"total"`      // total bytes written so far
}

type s3Part struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

// --- NativeUploader ---------------------------------------------------------

func (s *s3ObjectStore) NewUpload(ctx context.Context) (Upload, error) {
	id := newUploadID()
	key := uploadKey(id)
	out, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 create multipart upload: %w", err)
	}
	u := &s3Upload{
		store: s,
		id:    id,
		sess:  &s3Session{UploadID: aws.ToString(out.UploadId), Key: key},
		hash:  sha256.New(),
	}
	if err := u.save(); err != nil {
		// Best-effort cleanup of the just-created multipart upload.
		_, _ = s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: out.UploadId,
		})
		return nil, err
	}
	return u, nil
}

func (s *s3ObjectStore) GetUpload(_ context.Context, id string) (Upload, error) {
	return s.loadUpload(id)
}

func (s *s3ObjectStore) AbortUpload(ctx context.Context, id string) error {
	u, err := s.loadUpload(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	u.abortMultipart(ctx)
	return u.deleteSidecar()
}

// loadUpload reconstructs an s3Upload (its session + sha256 state) from its
// on-disk sidecar. Returns ErrNotFound if the sidecar is missing.
func (s *s3ObjectStore) loadUpload(id string) (*s3Upload, error) {
	data, err := os.ReadFile(s.sidecarPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sess := &s3Session{}
	if err := json.Unmarshal(data, sess); err != nil {
		return nil, fmt.Errorf("storage: s3 load upload %s: %w", id, err)
	}
	h := sha256.New()
	if len(sess.HashState) > 0 {
		if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(sess.HashState); err != nil {
			return nil, fmt.Errorf("storage: s3 restore hash state: %w", err)
		}
	}
	return &s3Upload{store: s, id: id, sess: sess, hash: h}, nil
}

// --- s3Upload ---------------------------------------------------------------

// s3Upload is a native, resumable S3 multipart upload. It implements Upload and
// committableUpload; CommitUpload drives it via Commit, so Reader is never used
// on the happy path.
type s3Upload struct {
	store *s3ObjectStore
	id    string
	sess  *s3Session
	hash  hash.Hash
}

func (u *s3Upload) ID() string { return u.id }

// save persists the session (including the marshaled sha256 state) so a
// subsequent GetUpload in a later HTTP request resumes exactly where this one
// left off.
func (u *s3Upload) save() error {
	state, err := u.hash.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return fmt.Errorf("storage: s3 marshal hash state: %w", err)
	}
	u.sess.HashState = state
	data, err := json.Marshal(u.sess)
	if err != nil {
		return err
	}
	tmp := u.store.sidecarPath(u.id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, u.store.sidecarPath(u.id))
}

func (u *s3Upload) deleteSidecar() error {
	err := os.Remove(u.store.sidecarPath(u.id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (u *s3Upload) abortMultipart(ctx context.Context) {
	_, _ = u.store.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(u.store.bucket),
		Key:      aws.String(u.sess.Key),
		UploadId: aws.String(u.sess.UploadID),
	})
}

// uploadPart flushes data as the next multipart part and records it.
func (u *s3Upload) uploadPart(ctx context.Context, data []byte) error {
	pn := int32(len(u.sess.Parts) + 1)
	out, err := u.store.client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(u.store.bucket),
		Key:           aws.String(u.sess.Key),
		UploadId:      aws.String(u.sess.UploadID),
		PartNumber:    aws.Int32(pn),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("storage: s3 upload part %d: %w", pn, err)
	}
	u.sess.Parts = append(u.sess.Parts, s3Part{
		PartNumber: pn,
		ETag:       aws.ToString(out.ETag),
		Size:       int64(len(data)),
	})
	return nil
}

// Write streams r into the running sha256 hash and the pending tail buffer,
// flushing the tail as a multipart part whenever it reaches the 5-MiB minimum.
// Memory stays bounded (~5 MiB) regardless of chunk size, and state is persisted
// before returning so a later request can resume.
func (u *s3Upload) Write(ctx context.Context, r io.Reader) (int64, error) {
	buf := make([]byte, 1<<20) // 1 MiB read buffer
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			u.hash.Write(buf[:n])
			u.sess.Tail = append(u.sess.Tail, buf[:n]...)
			u.sess.Total += int64(n)
			if len(u.sess.Tail) >= s3MinPartSize {
				part := u.sess.Tail
				u.sess.Tail = nil
				if err := u.uploadPart(ctx, part); err != nil {
					return 0, err
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, rerr
		}
	}
	if err := u.save(); err != nil {
		return 0, err
	}
	return u.sess.Total, nil
}

// Reader is not supported for S3 native uploads: bytes live in an incomplete
// multipart upload that cannot be read back until it is completed. CommitUpload
// uses Commit (committableUpload) and never calls this.
func (u *s3Upload) Reader(_ context.Context) (io.ReadCloser, error) {
	return nil, errors.New("storage: s3 upload does not support Reader; commit directly")
}

func (u *s3Upload) Close() error { return nil }

// Commit finalizes the multipart upload: it flushes the remaining tail as the
// final part, verifies the digest, completes the multipart object at the temp
// key, and moves it (server-side) to its content-addressed CAS key.
func (u *s3Upload) Commit(ctx context.Context, expect string) (digest string, size int64, err error) {
	digest = "sha256:" + hex.EncodeToString(u.hash.Sum(nil))
	size = u.sess.Total

	if expect != "" && expect != digest {
		u.abortMultipart(ctx)
		_ = u.deleteSidecar()
		return "", 0, fmt.Errorf("%w: expected %s got %s", ErrDigestMismatch, expect, digest)
	}

	key, err := blobKey(digest)
	if err != nil {
		return "", 0, err
	}

	// Empty blob: S3 requires >= 1 part to complete a multipart upload, so abort
	// it and write the empty object through the normal put path instead.
	if u.sess.Total == 0 && len(u.sess.Parts) == 0 {
		u.abortMultipart(ctx)
		// Only statErr == nil proves the empty object already exists. ErrNotFound
		// means we must write it; any other (transient) error must NOT be swallowed
		// -- reporting success without writing would record a blob the bucket does
		// not hold, 404ing every later read.
		_, statErr := u.store.Stat(ctx, key)
		switch {
		case statErr == nil:
			// Empty blob already present; nothing to write.
		case errors.Is(statErr, ErrNotFound):
			if err = u.store.Put(ctx, key, bytes.NewReader(nil), 0); err != nil {
				return "", 0, err
			}
		default:
			return "", 0, statErr
		}
		_ = u.deleteSidecar()
		return digest, 0, nil
	}

	// Flush any remaining buffered bytes as the final part (may be < 5 MiB).
	if len(u.sess.Tail) > 0 {
		if err = u.uploadPart(ctx, u.sess.Tail); err != nil {
			return "", 0, err
		}
		u.sess.Tail = nil
	}

	// If the content-addressed blob already exists, it is byte-identical; drop
	// the temp multipart upload and skip the complete + copy.
	if _, statErr := u.store.Stat(ctx, key); statErr == nil {
		u.abortMultipart(ctx)
		_ = u.deleteSidecar()
		return digest, size, nil
	}

	parts := make([]types.CompletedPart, len(u.sess.Parts))
	for i, p := range u.sess.Parts {
		parts[i] = types.CompletedPart{ETag: aws.String(p.ETag), PartNumber: aws.Int32(p.PartNumber)}
	}
	if _, err = u.store.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(u.store.bucket),
		Key:             aws.String(u.sess.Key),
		UploadId:        aws.String(u.sess.UploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		return "", 0, fmt.Errorf("storage: s3 complete multipart upload: %w", err)
	}

	// Move the assembled object from its temp key to the CAS key (server-side).
	// Objects larger than the 5 GiB single-copy limit are copied via a multipart
	// UploadPartCopy loop inside copyObject.
	if err = u.copyObject(ctx, u.sess.Key, key, size); err != nil {
		// The multipart upload was completed, so the temp key now holds a finished
		// object; delete it (and the sidecar) so the failed attempt leaves nothing
		// orphaned and a retry starts clean.
		_, _ = u.store.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(u.store.bucket), Key: aws.String(u.sess.Key),
		})
		_ = u.deleteSidecar()
		return "", 0, fmt.Errorf("storage: s3 copy to blob key: %w", err)
	}
	_, _ = u.store.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(u.store.bucket), Key: aws.String(u.sess.Key),
	})
	_ = u.deleteSidecar()
	return digest, size, nil
}

// copyObject copies the object at srcKey to dstKey server-side. Objects at or
// below the 5 GiB single-copy limit use a single CopyObject; larger objects are
// copied with a multipart UploadPartCopy loop, since S3's single-operation
// CopyObject rejects sources over 5 GiB (image layers routinely exceed it).
// size is the source object's byte length.
func (u *s3Upload) copyObject(ctx context.Context, srcKey, dstKey string, size int64) error {
	source := u.store.bucket + "/" + srcKey
	if size <= s3MaxSingleCopy {
		_, err := u.store.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(u.store.bucket),
			Key:        aws.String(dstKey),
			CopySource: aws.String(source),
		})
		return err
	}

	create, err := u.store.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(u.store.bucket),
		Key:    aws.String(dstKey),
	})
	if err != nil {
		return err
	}
	abort := func() {
		_, _ = u.store.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(u.store.bucket), Key: aws.String(dstKey), UploadId: create.UploadId,
		})
	}

	var parts []types.CompletedPart
	var pn int32
	for off := int64(0); off < size; off += s3CopyPartSize {
		end := off + s3CopyPartSize - 1
		if end > size-1 {
			end = size - 1
		}
		pn++
		out, cerr := u.store.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket:          aws.String(u.store.bucket),
			Key:             aws.String(dstKey),
			UploadId:        create.UploadId,
			CopySource:      aws.String(source),
			CopySourceRange: aws.String(fmt.Sprintf("bytes=%d-%d", off, end)),
			PartNumber:      aws.Int32(pn),
		})
		if cerr != nil {
			abort()
			return cerr
		}
		var etag *string
		if out.CopyPartResult != nil {
			etag = out.CopyPartResult.ETag
		}
		parts = append(parts, types.CompletedPart{ETag: etag, PartNumber: aws.Int32(pn)})
	}
	if _, err := u.store.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(u.store.bucket),
		Key:             aws.String(dstKey),
		UploadId:        create.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		abort()
		return err
	}
	return nil
}
