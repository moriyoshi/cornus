// Package registry implements the OCI Distribution Specification v1.1 /v2/*
// HTTP surface backed by cornus's persistent content-addressable store.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"cornus/pkg/storage"
)

const apiVersionHeader = "Docker-Distribution-API-Version"
const contentDigestHeader = "Docker-Content-Digest"

// defaultMaxBlobSize caps a single blob upload (monolithic PUT or the cumulative
// size of a chunked upload) so an unauthenticated client cannot exhaust the disk
// with one request. It is deliberately generous (well above any realistic image
// layer) and overridable via WithMaxBlobSize.
const defaultMaxBlobSize = 10 << 30 // 10 GiB

// maxManifestSize caps a manifest/index PUT body. Manifests are small JSON
// documents (references to blobs, not blob data), so 32 MiB is far above any
// realistic manifest while still bounding memory per request.
const maxManifestSize = 32 << 20 // 32 MiB

// Store is the content backend the registry handlers drive. *storage.Backend
// (the CAS) satisfies it, and so does the containerd-backed store used by
// host-native re-export on the containerd backend — so /v2/* reads and writes
// can be served either by the persistent CAS or straight from a local runtime's
// image store.
type Store interface {
	StatBlob(ctx context.Context, digest string) (int64, error)
	GetBlob(ctx context.Context, digest string) (io.ReadCloser, error)
	PutBlob(ctx context.Context, r io.Reader, expect string) (digest string, size int64, err error)
	DeleteBlob(ctx context.Context, digest string) error
	NewUpload(ctx context.Context) (storage.Upload, error)
	GetUpload(ctx context.Context, id string) (storage.Upload, error)
	AbortUpload(ctx context.Context, id string) error
	CommitUpload(ctx context.Context, u storage.Upload, expect string) (digest string, size int64, err error)
	PutManifest(ctx context.Context, repo, ref, mediaType string, content []byte) (digest string, err error)
	GetManifest(ctx context.Context, repo, ref string) (content []byte, digest, mediaType string, err error)
	DeleteManifest(ctx context.Context, repo, digest string) error
	Tags(ctx context.Context, repo string) ([]string, error)
	Repos(ctx context.Context) ([]string, error)
	Referrers(ctx context.Context, repo, subject string) ([]storage.Descriptor, error)
}

// Registry is an http.Handler serving the /v2/ API over a Store.
type Registry struct {
	store       Store
	maxBlobSize int64
	// source, when set, makes a manifest/blob miss fall through to a read-only
	// upstream: a pull-through Mirror (an upstream OCI registry) or the daemon
	// re-export source (the local Docker daemon). nil keeps the registry
	// local-only, returning 404 on a miss.
	source imageSource
	// sourceReadOnly forces read-only even when a content store is present. The
	// daemon re-export source (host-native) is an inherently read-only view over
	// the runtime's own image store; images are mutated there (docker load), never
	// pushed through /v2/*. A CAS may still be co-resident (the harness always
	// passes --storage), but writing to it is meaningless in re-export mode, so all
	// write verbs must 405 regardless. A pull-through Mirror leaves this false: it
	// caches fetched content into the CAS, so that store stays writable.
	sourceReadOnly bool
}

// Option configures a Registry.
type Option func(*Registry)

// WithMaxBlobSize overrides the per-blob upload ceiling (bytes). A value <= 0
// resets it to the default.
func WithMaxBlobSize(n int64) Option {
	return func(r *Registry) {
		if n <= 0 {
			n = defaultMaxBlobSize
		}
		r.maxBlobSize = n
	}
}

// New returns a Registry backed by st. st may be nil: in a pure re-export
// configuration (a source is set with no persistent content store) the registry
// keeps no CAS, serving reads straight from the source and rejecting writes. A
// nil store with no source serves nothing (every request 404s / is rejected).
func New(st Store, opts ...Option) *Registry {
	r := &Registry{store: st, maxBlobSize: defaultMaxBlobSize}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// readOnly reports whether write verbs — blob upload, manifest PUT,
// blob/manifest DELETE — must be rejected. Two cases: a pure re-export
// configuration with no content store to write to (store == nil), or a re-export
// source that is read-only by nature even alongside a co-resident CAS
// (sourceReadOnly, set by WithDaemonSource). In both, the authoritative store is
// the local runtime's (the daemon or containerd), mutated there, not through /v2/*.
func (r *Registry) readOnly() bool { return r.store == nil || r.sourceReadOnly }

// rejectReadOnly writes the 405 used for every write verb in pure re-export mode.
func rejectReadOnly(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED",
		"registry is read-only in re-export mode; change images in the local Docker/containerd store instead")
}

// Register mounts the registry on mux under /v2/.
func (r *Registry) Register(mux *http.ServeMux) {
	mux.Handle("/v2/", r)
	mux.Handle("/v2", http.RedirectHandler("/v2/", http.StatusMovedPermanently))
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set(apiVersionHeader, "registry/2.0")

	if req.URL.Path == "/v2/" || req.URL.Path == "/v2" {
		// API version check / ping.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
		return
	}
	if req.URL.Path == "/v2/_catalog" {
		r.handleCatalog(w, req)
		return
	}

	name, kind, ref, ok := parsePath(req.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "unrecognized path")
		return
	}

	switch kind {
	case "blob-upload":
		r.handleBlobUpload(w, req, name, ref)
	case "blob":
		r.handleBlob(w, req, name, ref)
	case "manifest":
		r.handleManifest(w, req, name, ref)
	case "referrers":
		r.handleReferrers(w, req, name, ref)
	case "tags":
		r.handleTags(w, req, name)
	default:
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "unrecognized path")
	}
}

// parsePath decomposes /v2/<name>/{blobs|manifests|tags}/... into the
// repository name, the resource kind, and a trailing reference.
func parsePath(p string) (name, kind, ref string, ok bool) {
	rest := strings.TrimPrefix(p, "/v2/")
	if rest == p { // no /v2/ prefix
		return "", "", "", false
	}

	if i := strings.Index(rest, "/blobs/uploads"); i >= 0 {
		name = rest[:i]
		ref = strings.TrimPrefix(rest[i+len("/blobs/uploads"):], "/")
		return name, "blob-upload", ref, name != ""
	}
	if strings.HasSuffix(rest, "/tags/list") {
		name = strings.TrimSuffix(rest, "/tags/list")
		return name, "tags", "", name != ""
	}
	if i := strings.LastIndex(rest, "/referrers/"); i >= 0 {
		name = rest[:i]
		ref = rest[i+len("/referrers/"):]
		return name, "referrers", ref, name != "" && ref != ""
	}
	if i := strings.LastIndex(rest, "/blobs/"); i >= 0 {
		name = rest[:i]
		ref = rest[i+len("/blobs/"):]
		return name, "blob", ref, name != "" && ref != ""
	}
	if i := strings.LastIndex(rest, "/manifests/"); i >= 0 {
		name = rest[:i]
		ref = rest[i+len("/manifests/"):]
		return name, "manifest", ref, name != "" && ref != ""
	}
	return "", "", "", false
}

// --- blobs ------------------------------------------------------------------

func (r *Registry) handleBlob(w http.ResponseWriter, req *http.Request, name, digest string) {
	switch req.Method {
	case http.MethodHead, http.MethodGet:
		if r.readOnly() {
			// No content store (pure re-export): serve straight from the source.
			if r.serveBlobFromSource(w, req, name, digest) {
				return
			}
			writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
			return
		}
		size, err := r.store.StatBlob(req.Context(), digest)
		if err != nil {
			if r.serveBlobFromSource(w, req, name, digest) {
				return
			}
			writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
			return
		}
		w.Header().Set(contentDigestHeader, digest)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")

		start, length, hasRange, satisfiable := parseRange(req.Header.Get("Range"), size)
		if hasRange && !satisfiable {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			writeError(w, http.StatusRequestedRangeNotSatisfiable, "RANGE_INVALID", "requested range not satisfiable")
			return
		}

		if req.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusOK)
			return
		}

		rc, err := r.store.GetBlob(req.Context(), digest)
		if err != nil {
			writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
			return
		}
		defer rc.Close()

		if hasRange {
			if err := seekForward(rc, start); err != nil {
				// Cannot honor the range; fall back to a full-body 200.
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
				w.WriteHeader(http.StatusOK)
				_, _ = io.Copy(w, rc)
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.CopyN(w, rc, length)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)

	case http.MethodDelete:
		if r.readOnly() {
			rejectReadOnly(w)
			return
		}
		if err := r.store.DeleteBlob(req.Context(), digest); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown")
				return
			}
			writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		w.WriteHeader(http.StatusAccepted)

	default:
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
	}
}

// seekForward advances rc to offset, using io.Seeker when available and falling
// back to discarding bytes otherwise (gocloud blob readers are not seekable).
func seekForward(rc io.ReadCloser, offset int64) error {
	if offset == 0 {
		return nil
	}
	if s, ok := rc.(io.Seeker); ok {
		_, err := s.Seek(offset, io.SeekStart)
		return err
	}
	_, err := io.CopyN(io.Discard, rc, offset)
	return err
}

// parseRange parses a single-range HTTP Range header ("bytes=start-end",
// "bytes=start-", or "bytes=-suffix") against a known content size. hasRange is
// false when there is no (or an unsupported) Range header, in which case the
// caller serves the full body. When hasRange is true, satisfiable reports
// whether the range lies within the content.
func parseRange(header string, size int64) (start, length int64, hasRange, satisfiable bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false, false
	}
	spec := strings.TrimPrefix(header, prefix)
	if strings.ContainsRune(spec, ',') {
		return 0, 0, false, false // multiple ranges unsupported; serve full body
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false, false
	}
	startStr, endStr := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])

	if startStr == "" {
		// Suffix range: last N bytes.
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		if n > size {
			n = size
		}
		if size == 0 {
			return 0, 0, true, false
		}
		return size - n, n, true, true
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}
	if start >= size {
		return 0, 0, true, false // unsatisfiable
	}
	end := size - 1
	if endStr != "" {
		e, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || e < start {
			return 0, 0, false, false
		}
		if e < end {
			end = e
		}
	}
	return start, end - start + 1, true, true
}

func (r *Registry) handleBlobUpload(w http.ResponseWriter, req *http.Request, name, uploadID string) {
	if r.readOnly() {
		rejectReadOnly(w)
		return
	}
	ctx := req.Context()
	switch req.Method {
	case http.MethodPost:
		// Cross-repo mount: ?mount=<digest>&from=<repo>
		if mount := req.URL.Query().Get("mount"); mount != "" {
			if _, err := r.store.StatBlob(ctx, mount); err == nil {
				w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, mount))
				w.Header().Set(contentDigestHeader, mount)
				w.WriteHeader(http.StatusCreated)
				return
			}
			// Fall through to a normal upload session if not present.
		}
		// Monolithic upload: ?digest=<digest> with the full body.
		if digest := req.URL.Query().Get("digest"); digest != "" {
			got, _, err := r.store.PutBlob(ctx, r.capBody(req.Body), digest)
			if err != nil {
				r.blobPutError(w, err)
				return
			}
			w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, got))
			w.Header().Set(contentDigestHeader, got)
			w.WriteHeader(http.StatusCreated)
			return
		}
		// Start a resumable upload session.
		u, err := r.store.NewUpload(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		id := u.ID()
		_ = u.Close()
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, id))
		w.Header().Set("Docker-Upload-UUID", id)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPatch:
		u, err := r.store.GetUpload(ctx, uploadID)
		if err != nil {
			writeError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload unknown")
			return
		}
		total, err := u.Write(ctx, r.capBody(req.Body))
		_ = u.Close()
		if err != nil {
			if errors.Is(err, errBlobTooLarge) {
				_ = r.store.AbortUpload(ctx, uploadID)
				r.blobTooLarge(w)
				return
			}
			writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		if total > r.maxBlobSize {
			_ = r.store.AbortUpload(ctx, uploadID)
			r.blobTooLarge(w)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uploadID))
		w.Header().Set("Docker-Upload-UUID", uploadID)
		w.Header().Set("Range", fmt.Sprintf("0-%d", total-1))
		w.WriteHeader(http.StatusAccepted)

	case http.MethodPut:
		// Final chunk may be in the body; digest is required.
		digest := req.URL.Query().Get("digest")
		if digest == "" {
			writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "missing digest")
			return
		}
		u, err := r.store.GetUpload(ctx, uploadID)
		if err != nil {
			writeError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "upload unknown")
			return
		}
		if req.ContentLength != 0 {
			total, werr := u.Write(ctx, r.capBody(req.Body))
			if werr != nil {
				_ = u.Close()
				if errors.Is(werr, errBlobTooLarge) {
					_ = r.store.AbortUpload(ctx, uploadID)
					r.blobTooLarge(w)
					return
				}
				if writeStoreUnwritable(w, werr) {
					return
				}
				writeError(w, http.StatusInternalServerError, "UNKNOWN", werr.Error())
				return
			}
			if total > r.maxBlobSize {
				_ = u.Close()
				_ = r.store.AbortUpload(ctx, uploadID)
				r.blobTooLarge(w)
				return
			}
		}
		got, _, err := r.store.CommitUpload(ctx, u, digest)
		if err != nil {
			r.blobPutError(w, err)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/%s", name, got))
		w.Header().Set(contentDigestHeader, got)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		_ = r.store.AbortUpload(ctx, uploadID)
		w.WriteHeader(http.StatusNoContent)

	default:
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
	}
}

func (r *Registry) blobPutError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrDigestMismatch) {
		writeError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
		return
	}
	if errors.Is(err, errBlobTooLarge) {
		r.blobTooLarge(w)
		return
	}
	if writeStoreUnwritable(w, err) {
		return
	}
	writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
}

// writeStoreUnwritable answers a write the registry cannot persist because its
// storage is not writable by this process, and reports whether it did.
//
// Without this the condition surfaced as a bare `500 UNKNOWN` carrying only a
// raw syscall string, which says nothing about the cause and which push clients
// then retry with backoff — forever, since the condition is permanent. The most
// common cause by far is a data directory left owned by another user, typically
// root, after the server was run privileged once: the blob store's shard
// directories become undeletable and uncreatable by the ordinary user the server
// now runs as. Naming the effective uid makes that self-diagnosing.
//
// 503 rather than 500 because this is a server-state problem, not a failed
// operation, and it keeps the case distinguishable in logs and metrics from
// genuine internal errors.
func writeStoreUnwritable(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, fs.ErrPermission) {
		return false
	}
	slog.Error("registry storage is not writable; a push cannot be persisted",
		"uid", os.Geteuid(), "err", err)
	writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", fmt.Sprintf(
		"registry storage is not writable by this server (uid %d): %v — "+
			"the data directory is likely owned by another user; a data dir populated by a "+
			"privileged run cannot be written by an unprivileged one",
		os.Geteuid(), err))
	return true
}

func (r *Registry) blobTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge, "BLOB_UPLOAD_INVALID",
		fmt.Sprintf("blob exceeds maximum size of %d bytes", r.maxBlobSize))
}

// errBlobTooLarge signals that an upload body exceeded the configured ceiling.
var errBlobTooLarge = errors.New("blob exceeds maximum size")

// capBody wraps a request body so reads fail with errBlobTooLarge once the
// per-blob ceiling is crossed, bounding how much a single request can stream to
// disk regardless of the (client-supplied and untrusted) Content-Length.
func (r *Registry) capBody(body io.Reader) io.Reader {
	return &cappedReader{r: body, remaining: r.maxBlobSize}
}

type cappedReader struct {
	r         io.Reader
	remaining int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	if c.remaining < 0 {
		return n, errBlobTooLarge
	}
	return n, err
}

// --- manifests --------------------------------------------------------------

func (r *Registry) handleManifest(w http.ResponseWriter, req *http.Request, name, ref string) {
	switch req.Method {
	case http.MethodHead, http.MethodGet:
		if r.readOnly() {
			// No content store (pure re-export): serve straight from the source.
			if r.serveManifestFromSource(w, req, name, ref) {
				return
			}
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		content, digest, mediaType, err := r.store.GetManifest(req.Context(), name, ref)
		if err != nil {
			if r.serveManifestFromSource(w, req, name, ref) {
				return
			}
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		if mediaType == "" {
			mediaType = "application/vnd.oci.image.manifest.v1+json"
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set(contentDigestHeader, digest)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)

	case http.MethodPut:
		if r.readOnly() {
			rejectReadOnly(w)
			return
		}
		// Read one byte past the ceiling so a body that exactly fills the limit is
		// accepted while anything larger is detected. io.LimitReader alone would
		// return EOF at the cap, silently truncating and storing a corrupt manifest.
		body, err := io.ReadAll(io.LimitReader(req.Body, maxManifestSize+1))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		if int64(len(body)) > maxManifestSize {
			writeError(w, http.StatusRequestEntityTooLarge, "MANIFEST_INVALID",
				fmt.Sprintf("manifest exceeds maximum size of %d bytes", maxManifestSize))
			return
		}
		mediaType := req.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = detectMediaType(body)
		}
		digest, err := r.store.PutManifest(req.Context(), name, ref, mediaType, body)
		if err != nil {
			if writeStoreUnwritable(w, err) {
				return
			}
			writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		// When the client pushes by digest, OCI requires the server to reject the
		// request if the body's computed digest does not match the reference.
		// PutManifest stores under the computed digest, so compare it to ref here.
		if _, _, derr := storage.ParseDigest(ref); derr == nil && ref != digest {
			writeError(w, http.StatusBadRequest, "DIGEST_INVALID",
				fmt.Sprintf("provided digest %s does not match computed digest %s", ref, digest))
			return
		}
		w.Header().Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, digest))
		w.Header().Set(contentDigestHeader, digest)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if r.readOnly() {
			rejectReadOnly(w)
			return
		}
		if err := r.store.DeleteManifest(req.Context(), name, ref); err != nil {
			writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}
		w.WriteHeader(http.StatusAccepted)

	default:
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
	}
}

// --- referrers --------------------------------------------------------------

func (r *Registry) handleReferrers(w http.ResponseWriter, req *http.Request, name, digest string) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		return
	}
	var refs []storage.Descriptor
	if !r.readOnly() {
		// No content store (pure re-export): the local runtime has no referrers
		// graph, so return the spec-compliant empty index below.
		var err error
		refs, err = r.store.Referrers(req.Context(), name, digest)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository unknown")
				return
			}
			// A malformed subject digest is a client error.
			writeError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
			return
		}
	}

	if at := req.URL.Query().Get("artifactType"); at != "" {
		filtered := refs[:0:0]
		for _, d := range refs {
			if d.ArtifactType == at {
				filtered = append(filtered, d)
			}
		}
		refs = filtered
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	if refs == nil {
		refs = []storage.Descriptor{}
	}

	const indexType = "application/vnd.oci.image.index.v1+json"
	w.Header().Set("Content-Type", indexType)
	writeJSONRaw(w, http.StatusOK, map[string]any{
		"schemaVersion": 2,
		"mediaType":     indexType,
		"manifests":     refs,
	})
}

// --- tags & catalog ---------------------------------------------------------

func (r *Registry) handleTags(w http.ResponseWriter, req *http.Request, name string) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		return
	}
	if r.readOnly() {
		// No content store (pure re-export): the local runtime exposes no tag
		// catalog, so report an empty list rather than error.
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "tags": []string{}})
		return
	}
	tags, err := r.store.Tags(req.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	page, next := paginate(tags, req.URL.Query())
	if next != "" {
		w.Header().Set("Link", linkNext(fmt.Sprintf("/v2/%s/tags/list", name), req.URL.Query().Get("n"), next))
	}
	if page == nil {
		page = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "tags": page})
}

func (r *Registry) handleCatalog(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
		return
	}
	if r.readOnly() {
		// No content store (pure re-export): the local runtime exposes no catalog.
		writeJSON(w, http.StatusOK, map[string]any{"repositories": []string{}})
		return
	}
	repos, err := r.store.Repos(req.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
		return
	}
	page, next := paginate(repos, req.URL.Query())
	if next != "" {
		w.Header().Set("Link", linkNext("/v2/_catalog", req.URL.Query().Get("n"), next))
	}
	if page == nil {
		page = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": page})
}

// paginate applies the OCI ?n= / ?last= pagination parameters to a sorted list.
// Entries less than or equal to last are dropped; at most n remain (n omitted or
// non-positive means no limit). next is the last item of a truncated page (the
// value to pass as ?last= for the following page), or "" when the page is final.
func paginate(items []string, q map[string][]string) (page []string, next string) {
	last := firstValue(q, "last")
	if last != "" {
		i := sort.SearchStrings(items, last)
		for i < len(items) && items[i] <= last {
			i++
		}
		items = items[i:]
	}
	n, ok := parseN(firstValue(q, "n"))
	if !ok || n <= 0 || n >= len(items) {
		return items, ""
	}
	page = items[:n]
	return page, page[len(page)-1]
}

func firstValue(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func parseN(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func linkNext(base, n, last string) string {
	q := url.Values{}
	if n != "" {
		q.Set("n", n)
	}
	q.Set("last", last)
	return fmt.Sprintf("<%s?%s>; rel=\"next\"", base, q.Encode())
}

// writeJSONRaw writes v as JSON at the given status without overriding a
// Content-Type header the caller already set.
func writeJSONRaw(w http.ResponseWriter, code int, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// --- helpers ----------------------------------------------------------------

func detectMediaType(body []byte) string {
	var probe struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(body, &probe); err == nil && probe.MediaType != "" {
		return probe.MediaType
	}
	return "application/vnd.oci.image.manifest.v1+json"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": code, "message": msg}},
	})
}
