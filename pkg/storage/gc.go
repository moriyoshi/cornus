package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// errNotManifest indicates that a blob was read in full but is not manifest JSON
// (i.e. it is a config or layer leaf). GC keeps such a blob but does not traverse
// into it. It is distinct from a genuine read failure, which must abort GC.
var errNotManifest = errors.New("storage: blob is not a manifest")

// gcManifestReadLimit bounds how many bytes GC reads when parsing a manifest to
// discover its referenced blobs. Image manifests and indexes are small; this
// guards against a marker that somehow points at a large blob.
const gcManifestReadLimit = 32 << 20

// DefaultUploadTTL is how long an in-progress upload session (or leaked staging
// temp file) may sit idle before SweepStaleUploads reaps it.
const DefaultUploadTTL = 24 * time.Hour

// stagingPrefixes are the filename prefixes SweepStaleUploads is willing to
// delete from the staging directory. Every artifact cornus writes there is
// transient (native fs upload sessions, staged uploads, S3 sidecars, and blob /
// put temp files); durable blobs never live in the staging dir. Restricting to
// known prefixes keeps the reaper from touching anything unexpected.
var stagingPrefixes = []string{"session-", "upload-", "s3upload-", "put-", "blob-"}

// gcDescriptor captures the digest fields GC cares about across OCI image
// manifests, Docker/OCI image indexes (manifest lists), and the referrers
// subject descriptor.
type gcDescriptor struct {
	Digest string `json:"digest"`
}

type gcManifest struct {
	Config    gcDescriptor   `json:"config"`
	Layers    []gcDescriptor `json:"layers"`
	Manifests []gcDescriptor `json:"manifests"`
	Subject   *gcDescriptor  `json:"subject"`
}

// GC performs a mark-and-sweep garbage collection over the blob CAS. It builds
// the set of reachable blobs by walking every repository's manifest markers and
// tags as roots, parsing each manifest to follow its config, layers, nested
// index manifests, and subject, then deletes every blob under blobs/sha256/**
// that is not reachable. It returns the number of blobs freed.
//
// GC is an explicit maintenance operation and is not concurrency-safe with
// in-flight pushes: a freshly uploaded blob that no manifest references yet is
// considered unreachable. Run it when the registry is quiescent.
func (b *Backend) GC(ctx context.Context) (freed int, err error) {
	reachable := map[string]struct{}{}

	// Collect roots: manifest marker digests and tag targets across all repos.
	repos, err := b.Repos(ctx)
	if err != nil {
		return 0, err
	}
	var queue []string
	enqueueRoot := func(d string) {
		if !isDigest(d) {
			return
		}
		queue = append(queue, d)
	}
	for _, repo := range repos {
		hexes, herr := b.manifestHexes(ctx, repo)
		if herr != nil {
			return 0, herr
		}
		for _, hexv := range hexes {
			enqueueRoot("sha256:" + hexv)
		}
		tags, terr := b.Tags(ctx, repo)
		if terr != nil {
			return 0, terr
		}
		for _, tag := range tags {
			d, derr := b.readTag(ctx, repo, tag)
			if derr != nil {
				continue // dangling / unreadable tag: nothing to root
			}
			enqueueRoot(d)
		}
	}

	// Mark: BFS over manifests. Roots and index children / subjects are parsed as
	// manifests; their config and layers are leaves (kept but not traversed).
	visited := map[string]struct{}{}
	for len(queue) > 0 {
		d := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if _, ok := visited[d]; ok {
			continue
		}
		visited[d] = struct{}{}
		reachable[d] = struct{}{}

		m, perr := b.parseManifestBlob(ctx, d)
		if perr != nil {
			// A missing blob (ErrNotFound) or a blob that is simply not manifest
			// JSON (errNotManifest) is a legitimate leaf: keep it, don't traverse.
			// Any other error is a genuine read failure on a blob that may exist
			// (I/O, permission, deadline); treating it as a leaf would under-mark
			// reachability and sweep still-referenced children, so abort GC.
			if errors.Is(perr, ErrNotFound) || errors.Is(perr, errNotManifest) {
				continue
			}
			return 0, fmt.Errorf("storage: gc read manifest %s: %w", d, perr)
		}
		if isDigest(m.Config.Digest) {
			reachable[m.Config.Digest] = struct{}{}
		}
		for _, l := range m.Layers {
			if isDigest(l.Digest) {
				reachable[l.Digest] = struct{}{}
			}
		}
		for _, sub := range m.Manifests {
			if isDigest(sub.Digest) {
				reachable[sub.Digest] = struct{}{}
				queue = append(queue, sub.Digest)
			}
		}
		if m.Subject != nil && isDigest(m.Subject.Digest) {
			reachable[m.Subject.Digest] = struct{}{}
			queue = append(queue, m.Subject.Digest)
		}
	}

	// Sweep: delete every blob not in the reachable set.
	keys, err := b.obj.List(ctx, "blobs/sha256/", "")
	if err != nil {
		return 0, err
	}
	for _, key := range keys {
		if strings.HasSuffix(key, "/") {
			continue
		}
		digest := "sha256:" + path.Base(key)
		if !isDigest(digest) {
			continue
		}
		if _, ok := reachable[digest]; ok {
			continue
		}
		if err := b.obj.Delete(ctx, key); err != nil {
			return freed, err
		}
		freed++
	}
	return freed, nil
}

// manifestHexes lists the hex digests of every manifest marker in a repo.
func (b *Backend) manifestHexes(ctx context.Context, repo string) ([]string, error) {
	prefix := path.Join("repos", sanitizeRepo(repo), "manifests") + "/"
	keys, err := b.obj.List(ctx, prefix, "/")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.HasSuffix(k, "/") {
			continue
		}
		out = append(out, path.Base(k))
	}
	return out, nil
}

// readTag returns the digest a tag points at within a repo.
func (b *Backend) readTag(ctx context.Context, repo, tag string) (string, error) {
	rc, err := b.obj.Get(ctx, tagKey(repo, tag))
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// parseManifestBlob fetches a blob by digest and parses it as a manifest or
// index. It distinguishes three outcomes for the caller:
//   - ErrNotFound: the blob is absent (a leaf; GC keeps it, no traversal).
//   - errNotManifest: the blob was read but is not manifest JSON, i.e. a config
//     or layer leaf (GC keeps it, no traversal).
//   - any other error: a genuine read failure (I/O, permission, deadline) on a
//     blob that may exist; the caller must NOT treat this as a leaf.
func (b *Backend) parseManifestBlob(ctx context.Context, digest string) (gcManifest, error) {
	var m gcManifest
	rc, err := b.GetBlob(ctx, digest)
	if err != nil {
		return m, err // ErrNotFound or a genuine read error
	}
	data, err := io.ReadAll(io.LimitReader(rc, gcManifestReadLimit))
	rc.Close()
	if err != nil {
		return m, err // genuine read failure on an existing blob
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, errNotManifest
	}
	return m, nil
}

// SweepStaleUploads deletes upload sessions and leaked staging temp files whose
// modification time is older than ttl (DefaultUploadTTL when ttl <= 0). It
// reclaims sessions abandoned mid-protocol (a client that starts a chunked
// upload and never commits) plus any orphaned put / blob temp files. Only files
// with a known staging prefix are considered. Returns the number removed.
func (b *Backend) SweepStaleUploads(ttl time.Duration) (removed int, err error) {
	if ttl <= 0 {
		ttl = DefaultUploadTTL
	}
	entries, err := os.ReadDir(b.stagingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-ttl)
	for _, e := range entries {
		name := e.Name()
		if !hasStagingPrefix(name) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(b.stagingDir, name)); rerr == nil {
			removed++
		}
	}
	return removed, nil
}

func hasStagingPrefix(name string) bool {
	for _, p := range stagingPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
