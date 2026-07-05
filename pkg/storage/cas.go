package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultCatalogTTL bounds how long a cached repository catalog is served before
// Repos re-walks the repos/ tree. Repo-creating / removing writes (PutManifest,
// DeleteManifest) invalidate the cache explicitly, so the TTL is only a safety
// net for mutations this process did not perform (e.g. external tooling writing
// straight to the object store). A few seconds keeps _catalog cheap under bursty
// polling without letting a stale list linger.
const defaultCatalogTTL = 5 * time.Second

// Backend is the registry-facing storage surface. It implements the sha256 CAS,
// resumable uploads, and manifest / tag / repo indexing once, on top of any
// ObjectStore. It is what the registry handlers talk to.
type Backend struct {
	obj        ObjectStore
	stagingDir string
	// native is non-nil when obj advertises the NativeUploader capability.
	native NativeUploader

	// catalog caches the sorted repository list produced by walking repos/, so
	// repeated _catalog requests do not re-descend the whole tree (expensive on
	// S3). It is guarded by catalogMu and refreshed on a TTL or on explicit
	// invalidation by repo-mutating writes.
	catalogMu    sync.Mutex
	catalogList  []string  // last computed sorted repo list (valid iff catalogValid)
	catalogAt    time.Time // when catalogList was computed
	catalogValid bool
	catalogGen   uint64 // bumped on every invalidation; guards store-after-walk races
	catalogTTL   time.Duration
	now          func() time.Time // clock seam; time.Now in production, overridable in tests
}

// NewBackend wraps an ObjectStore. stagingDir is a local directory used to stage
// resumable uploads for backends that do not implement NativeUploader.
func NewBackend(obj ObjectStore, stagingDir string) (*Backend, error) {
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: create staging dir: %w", err)
	}
	b := &Backend{obj: obj, stagingDir: stagingDir, catalogTTL: defaultCatalogTTL, now: time.Now}
	if nu, ok := obj.(NativeUploader); ok {
		b.native = nu
	}
	// Best-effort reap of upload sessions / staging temps left behind by a prior
	// process that crashed or was killed mid-upload.
	_, _ = b.SweepStaleUploads(DefaultUploadTTL)
	return b, nil
}

// Close releases the underlying ObjectStore.
func (b *Backend) Close() error { return b.obj.Close() }

// --- key layout -------------------------------------------------------------

func blobKey(digest string) (string, error) {
	algo, hexv, err := ParseDigest(digest)
	if err != nil {
		return "", err
	}
	return path.Join("blobs", algo, hexv[:2], hexv), nil
}

func manifestKey(repo, hexv string) string {
	return path.Join("repos", sanitizeRepo(repo), "manifests", hexv)
}

func tagKey(repo, tag string) string {
	return path.Join("repos", sanitizeRepo(repo), "tags", sanitize(tag))
}

// --- blobs ------------------------------------------------------------------

// StatBlob returns the size of a blob, or ErrNotFound.
func (b *Backend) StatBlob(ctx context.Context, digest string) (int64, error) {
	key, err := blobKey(digest)
	if err != nil {
		return 0, err
	}
	return b.obj.Stat(ctx, key)
}

// GetBlob opens a blob for reading, or returns ErrNotFound.
func (b *Backend) GetBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	key, err := blobKey(digest)
	if err != nil {
		return nil, err
	}
	return b.obj.Get(ctx, key)
}

// PutBlob streams r into the CAS, computing its sha256 digest. If expect is
// non-empty it must match the computed digest. The content is buffered to a
// local temp file first so the digest can be verified before the object is
// committed and so its size is known to the backend.
func (b *Backend) PutBlob(ctx context.Context, r io.Reader, expect string) (digest string, size int64, err error) {
	tmp, err := os.CreateTemp(b.stagingDir, "blob-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return "", 0, err
	}
	digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	if expect != "" && expect != digest {
		return "", 0, fmt.Errorf("%w: expected %s got %s", ErrDigestMismatch, expect, digest)
	}

	key, err := blobKey(digest)
	if err != nil {
		return "", 0, err
	}
	// Skip the upload if the blob already exists (content-addressed: identical).
	if _, statErr := b.obj.Stat(ctx, key); statErr == nil {
		return digest, size, nil
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	if err = b.obj.Put(ctx, key, tmp, size); err != nil {
		return "", 0, err
	}
	return digest, size, nil
}

// DeleteBlob removes a blob from the CAS by digest. Returns ErrNotFound if the
// blob does not exist. Callers wanting to reclaim only unreferenced blobs should
// use GC instead; this is the unconditional low-level delete used by the
// registry's blob DELETE handler.
func (b *Backend) DeleteBlob(ctx context.Context, digest string) error {
	key, err := blobKey(digest)
	if err != nil {
		return err
	}
	if _, err := b.obj.Stat(ctx, key); err != nil {
		return err // ErrNotFound bubbles up
	}
	return b.obj.Delete(ctx, key)
}

// --- uploads ----------------------------------------------------------------

// NewUpload starts a resumable upload session.
func (b *Backend) NewUpload(ctx context.Context) (Upload, error) {
	if b.native != nil {
		return b.native.NewUpload(ctx)
	}
	return newStagedUpload(b.stagingDir, newUploadID())
}

// GetUpload reopens an existing upload session.
func (b *Backend) GetUpload(ctx context.Context, id string) (Upload, error) {
	if b.native != nil {
		return b.native.GetUpload(ctx, id)
	}
	return openStagedUpload(b.stagingDir, id)
}

// AbortUpload discards an in-progress upload session.
func (b *Backend) AbortUpload(ctx context.Context, id string) error {
	if b.native != nil {
		return b.native.AbortUpload(ctx, id)
	}
	return abortStagedUpload(b.stagingDir, id)
}

// committableUpload is an optional capability: an upload that can commit itself
// directly into its backing store (e.g. the filesystem upload renaming its
// session file into the blob path), avoiding a copy through PutBlob.
type committableUpload interface {
	Commit(ctx context.Context, expect string) (digest string, size int64, err error)
}

// CommitUpload finalizes an upload: it hashes the accumulated bytes, verifies
// them against expect (if set), commits the blob into the CAS, and discards the
// session. A native upload that implements committableUpload commits itself;
// otherwise the staged bytes are streamed through PutBlob.
func (b *Backend) CommitUpload(ctx context.Context, u Upload, expect string) (digest string, size int64, err error) {
	if cu, ok := u.(committableUpload); ok {
		return cu.Commit(ctx, expect)
	}
	rc, err := u.Reader(ctx)
	if err != nil {
		return "", 0, err
	}
	digest, size, err = b.PutBlob(ctx, rc, expect)
	rc.Close()
	if err != nil {
		return "", 0, err
	}
	_ = u.Close()
	_ = b.AbortUpload(ctx, u.ID())
	return digest, size, nil
}

// --- manifests & tags -------------------------------------------------------

// PutManifest stores manifest content as a blob and records that it belongs to
// repo, remembering its media type. If ref is a tag (not a digest) the tag is
// pointed at the manifest. Returns the manifest digest.
//
// Write-ordering invariant. Object stores give us no multi-key transaction, so
// the three Puts below are ordered so that any crash leaves a self-consistent
// (or harmlessly repairable) state, never a dangling tag:
//
//  1. manifest CONTENT blob   (blobs/sha256/**)
//  2. per-repo membership MARKER  (repos/<repo>/manifests/<hex>)
//  3. TAG that points at the digest  (repos/<repo>/tags/<tag>)  -- written LAST
//
// A tag is therefore never written before the data it references. A crash
// between steps can only leave a blob without a marker/tag, or a blob+marker
// without a tag -- both invisible to readers (a marker/blob with no tag is not
// reachable by tag) and both reclaimable: GC (gc.go) roots reachability at tags
// AND markers, so an orphan marker keeps its blob until DeleteManifest removes
// it, and a bare blob is swept. The dangerous inverse -- a tag whose marker or
// blob is missing -- cannot arise from this ordering; if one is nonetheless seen
// (manual deletion, external mutation) GetManifest treats it as not-found rather
// than serving or 500ing a broken read.
func (b *Backend) PutManifest(ctx context.Context, repo, ref, mediaType string, content []byte) (digest string, err error) {
	// 1. content blob.
	digest, _, err = b.PutBlob(ctx, strings.NewReader(string(content)), "")
	if err != nil {
		return "", err
	}
	_, hexv, _ := ParseDigest(digest)
	// 2. membership marker (must exist before the tag can point at this digest).
	if err := b.obj.Put(ctx, manifestKey(repo, hexv), strings.NewReader(mediaType), int64(len(mediaType))); err != nil {
		return "", err
	}
	// The marker may have created a previously-unseen repository, so drop the
	// cached catalog to force a fresh walk on the next Repos call.
	b.invalidateCatalog()
	// 3. tag, written last so it never precedes the blob+marker it references.
	if !isDigest(ref) {
		if err := b.obj.Put(ctx, tagKey(repo, ref), strings.NewReader(digest), int64(len(digest))); err != nil {
			return "", err
		}
	}
	return digest, nil
}

// GetManifest resolves ref (a tag or digest) within repo and returns the
// manifest content, its digest, and its stored media type.
//
// Read-side tolerance. Because PutManifest writes the tag last (see its
// invariant), a tag is only ever published after its marker and blob exist.
// Even so, this read is defensively self-consistent: if a resolved tag points at
// a missing/corrupt digest, or at a digest whose membership marker or content
// blob is absent, we return ErrNotFound (the OCI MANIFEST_UNKNOWN path) rather
// than a broken 200 or a 500. We deliberately do NOT actively repair (delete) a
// dangling tag here: a delete could race a concurrent PutManifest that is mid-
// write, so we prefer read-side tolerance. Any genuinely orphaned marker/blob is
// reclaimed by GC, not here.
func (b *Backend) GetManifest(ctx context.Context, repo, ref string) (content []byte, digest, mediaType string, err error) {
	fromTag := !isDigest(ref)
	if !fromTag {
		digest = ref
	} else {
		rc, rerr := b.obj.Get(ctx, tagKey(repo, ref))
		if rerr != nil {
			return nil, "", "", rerr // ErrNotFound bubbles up
		}
		raw, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr != nil {
			return nil, "", "", rerr
		}
		digest = strings.TrimSpace(string(raw))
	}

	_, hexv, perr := ParseDigest(digest)
	if perr != nil {
		if fromTag {
			// A tag whose stored value is not a valid digest is a corrupt /
			// dangling tag: report it as a clean not-found, not a 500.
			return nil, "", "", ErrNotFound
		}
		return nil, "", "", perr
	}
	mtRC, merr := b.obj.Get(ctx, manifestKey(repo, hexv))
	if merr != nil {
		return nil, "", "", merr // ErrNotFound: manifest does not belong to this repo
	}
	mt, merr := io.ReadAll(mtRC)
	mtRC.Close()
	if merr != nil {
		return nil, "", "", merr
	}
	mediaType = string(mt)

	rc, err := b.GetBlob(ctx, digest)
	if err != nil {
		return nil, "", "", err
	}
	defer rc.Close()
	content, err = io.ReadAll(rc)
	if err != nil {
		return nil, "", "", err
	}
	return content, digest, mediaType, nil
}

// DeleteManifest removes a manifest membership marker (by digest) from a repo.
func (b *Backend) DeleteManifest(ctx context.Context, repo, digest string) error {
	_, hexv, err := ParseDigest(digest)
	if err != nil {
		return err
	}
	key := manifestKey(repo, hexv)
	if _, err := b.obj.Stat(ctx, key); err != nil {
		return err // ErrNotFound
	}
	if err := b.obj.Delete(ctx, key); err != nil {
		return err
	}
	// Removing the last marker of a repo drops it from the catalog, so invalidate.
	b.invalidateCatalog()
	return nil
}

// Tags lists the tags defined in a repository, sorted.
func (b *Backend) Tags(ctx context.Context, repo string) ([]string, error) {
	prefix := path.Join("repos", sanitizeRepo(repo), "tags") + "/"
	keys, err := b.obj.List(ctx, prefix, "/")
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.HasSuffix(k, "/") {
			continue // a nested "directory"; tags are flat
		}
		tags = append(tags, path.Base(k))
	}
	sort.Strings(tags)
	return tags, nil
}

// Repos lists all repositories that have at least one stored manifest, sorted.
//
// The result is cached: walking the whole repos/ tree is expensive on remote
// object stores (S3), and _catalog is polled far more often than the repo set
// changes. A cached list is reused until its TTL (catalogTTL) elapses or a
// repo-mutating write (PutManifest / DeleteManifest) invalidates it, so a newly
// pushed repo is visible immediately and a stale list can never outlive the TTL.
// The tree is walked without holding catalogMu so concurrent readers are not
// serialized behind a slow S3 traversal; the fresh list is then stored under the
// lock (a redundant concurrent walk is harmless — the last writer wins with an
// equally-fresh result).
func (b *Backend) Repos(ctx context.Context) ([]string, error) {
	// Fast path: a still-fresh cached list satisfies the request without I/O.
	b.catalogMu.Lock()
	if b.catalogValid && b.now().Sub(b.catalogAt) < b.catalogTTL {
		out := append([]string(nil), b.catalogList...)
		b.catalogMu.Unlock()
		return out, nil
	}
	genBefore := b.catalogGen
	b.catalogMu.Unlock()

	// Slow path: recompute without holding the lock so a slow traversal does not
	// block unrelated requests.
	var repos []string
	if err := b.walkRepos(ctx, "repos/", &repos); err != nil {
		return nil, err
	}
	sort.Strings(repos)

	// Publish the fresh list, but only if no invalidation raced our walk. If a
	// write invalidated the cache after we snapshotted genBefore, our traversal
	// may have missed that write's marker, so we must not mark the (possibly
	// stale) list valid -- the next Repos call re-walks. The caller still gets our
	// freshly-walked list either way.
	b.catalogMu.Lock()
	if b.catalogGen == genBefore {
		b.catalogList = repos
		b.catalogAt = b.now()
		b.catalogValid = true
	}
	out := append([]string(nil), repos...)
	b.catalogMu.Unlock()
	return out, nil
}

// invalidateCatalog drops the cached repository list so the next Repos call
// re-walks the tree. Called by writes that can add or remove a repository. The
// generation bump also cancels any in-flight walk from publishing a snapshot
// taken before this invalidation (see Repos).
func (b *Backend) invalidateCatalog() {
	b.catalogMu.Lock()
	b.catalogValid = false
	b.catalogList = nil
	b.catalogGen++
	b.catalogMu.Unlock()
}

// walkRepos descends the repos/ tree looking for "manifests" marker directories.
// A directory named "manifests" that holds at least one marker file identifies
// its parent path (from repos/ down) as a repository. Repo names may themselves
// contain a "manifests" path segment (e.g. "team/manifests/api"), so every
// subdirectory -- including a "manifests" directory -- is still descended into so
// nested repositories are discovered. Recording a repo only when the marker
// directory actually contains a marker file avoids treating an intermediate
// "manifests" path segment as a false repository.
func (b *Backend) walkRepos(ctx context.Context, prefix string, out *[]string) error {
	entries, err := b.obj.List(ctx, prefix, "/")
	if err != nil {
		return err
	}
	// If this directory is itself a "manifests" marker directory holding at least
	// one marker file, its parent path is a repository.
	if path.Base(strings.TrimSuffix(prefix, "/")) == "manifests" {
		for _, e := range entries {
			if !strings.HasSuffix(e, "/") {
				name := strings.TrimSuffix(strings.TrimPrefix(prefix, "repos/"), "/")
				*out = append(*out, strings.TrimSuffix(name, "/manifests"))
				break
			}
		}
	}
	// Descend into every subdirectory so nested repositories -- including those
	// whose path contains a "manifests" segment -- are discovered.
	for _, e := range entries {
		if !strings.HasSuffix(e, "/") {
			continue
		}
		if err := b.walkRepos(ctx, e, out); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

func newUploadID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// sanitize makes an arbitrary reference safe to use as a single key element.
func sanitize(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(s)
}

// sanitizeRepo keeps repository path separators (foo/bar) but strips traversal.
func sanitizeRepo(repo string) string {
	parts := strings.Split(repo, "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			parts[i] = "_"
		}
	}
	return strings.Join(parts, "/")
}
