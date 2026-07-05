package server

import (
	"context"
	"net/http"
	"time"

	"cornus/pkg/blockcache"
	"cornus/pkg/build/builder"
)

// localCacheTTL is the age past which an unused managed type=local build-cache
// entry is eligible for pruning by POST /.cornus/v1/gc. It is deliberately generous so a
// routinely-reused cache is never reclaimed out from under an active project.
const localCacheTTL = 7 * 24 * time.Hour

// fileCacheTTL is the age past which an unused per-file block-cache backing file
// is eligible for pruning by POST /.cornus/v1/gc. Matches localCacheTTL: a routinely
// reused cache is never reclaimed out from under active work.
const fileCacheTTL = 7 * 24 * time.Hour

// gcResponse is the JSON summary returned by POST /.cornus/v1/gc.
type gcResponse struct {
	// BlobsFreed is the number of unreachable registry CAS blobs deleted by the
	// storage mark-and-sweep GC.
	BlobsFreed int `json:"blobsFreed"`
	// LocalCacheFreed is the number of stale managed type=local build-cache
	// entries removed under <CacheDir>/localcache.
	LocalCacheFreed int `json:"localCacheFreed"`
	// LocalCacheError, when non-empty, reports that the best-effort localcache
	// prune failed; the CAS GC still ran and its result is authoritative.
	LocalCacheError string `json:"localCacheError,omitempty"`
	// FileCacheFreed is the number of per-file block-cache backing files removed
	// from the cache directory (TTL + size-cap eviction).
	FileCacheFreed int `json:"fileCacheFreed,omitempty"`
	// FileCacheError, when non-empty, reports that the best-effort block-cache
	// prune failed; like the localcache prune it never fails the whole call.
	FileCacheError string `json:"fileCacheError,omitempty"`
	// FileCacheBytes is the on-disk size of the block cache after pruning — a
	// pull-free way to observe how much disk the cache currently holds.
	FileCacheBytes int64 `json:"fileCacheBytes,omitempty"`
}

// handleGC serves POST /.cornus/v1/gc: an on-demand, DESTRUCTIVE reclamation admin op.
// It runs the registry CAS mark-and-sweep (storage Backend.GC) and prunes stale
// managed type=local build caches, returning a JSON summary.
//
// AuthZ: gated on the API policy with the "gc" action (mirroring how deploy/build
// are gated). When no CORNUS_API_POLICY is configured it is allow-all like the
// others — operators SHOULD configure a policy before exposing this endpoint,
// since GC is not safe to run against a registry with in-flight pushes.
//
// The build engine may be unavailable (non-linux, or simply never initialised);
// the localcache prune runs directly against <CacheDir>/localcache as a plain
// directory (no engine construction), so it is best-effort and never fails the
// whole call — the CAS GC is the authoritative result. Only POST is accepted.
func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.apiPolicy.Allow(Identity(r), "gc") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to gc"})
		return
	}

	// Serialise with the periodic scheduler (and any concurrent manual POST)
	// through the same gcRunning gate the scheduler uses: storage.GC is not
	// concurrency-safe, and overlapping sweeps double the listing/delete I/O and
	// inflate the reported BlobsFreed. If a run is already in flight, refuse with
	// 409 rather than starting a second one.
	if !s.gcRunning.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "gc already in progress"})
		return
	}
	defer s.gcRunning.Store(false)

	resp, err := s.runGC(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "registry gc failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// runGC is the storage-reclamation core shared by the POST /.cornus/v1/gc handler and
// the periodic GC scheduler (gcschedule.go). It runs the registry CAS
// mark-and-sweep and the best-effort localcache prune. A CAS GC failure is the
// returned error (authoritative); a localcache prune failure never fails the
// call and is reported inside the summary instead.
func (s *Server) runGC(ctx context.Context) (gcResponse, error) {
	// Registry CAS mark-and-sweep. This is the authoritative result; a failure
	// here is a real error. In a pure re-export configuration there is no CAS
	// (s.store is nil) — image lifecycle is the local runtime's job — so skip the
	// sweep and report zero blobs freed while the cache prunes below still run.
	resp := gcResponse{}
	if s.store != nil {
		freed, err := s.store.GC(ctx)
		if err != nil {
			return gcResponse{}, err
		}
		resp.BlobsFreed = freed
	}

	// Best-effort localcache prune. The localcache is a plain directory tree under
	// the engine's data Root (CacheDir), so we reclaim it without constructing the
	// (privileged) BuildKit engine, and a failure never fails the whole call.
	pruned, perr := builder.PruneLocalCache(s.cfg.CacheDir(), localCacheTTL)
	resp.LocalCacheFreed = pruned
	if perr != nil {
		resp.LocalCacheError = perr.Error()
	}

	// Best-effort per-file block-cache prune. Like the localcache it is a plain
	// directory tree; TTL evicts idle files and the size cap (when configured)
	// bounds total on-disk footprint. Only prune when the cache is enabled.
	if s.cfg.FileCacheEnabled {
		fcFreed, fcErr := blockcache.Prune(s.cfg.FileCacheDir, fileCacheTTL, s.cfg.FileCacheMaxBytes)
		resp.FileCacheFreed = fcFreed
		if fcErr != nil {
			resp.FileCacheError = fcErr.Error()
		}
		if b, _, uerr := blockcache.DiskUsage(s.cfg.FileCacheDir); uerr == nil {
			resp.FileCacheBytes = b
		}
	}
	return resp, nil
}
