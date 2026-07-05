package webbff

// HTTP adapters for the file-explorer surface (/.cornus/web/fs*). Each is a thin
// wrapper over the value-returning core methods in fs.go, following the same
// writeJSON / writeErr / statusErr conventions as the rest of the BFF.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// fsQuery is the shared query surface: source selects the backend, workload targets a
// container, root selects a local root, path is relative-to-root (local) or
// container-absolute (container).
type fsQuery struct {
	source   string
	workload string
	root     string
	path     string
}

func parseFsQuery(r *http.Request) fsQuery {
	q := r.URL.Query()
	return fsQuery{
		source:   q.Get("source"),
		workload: q.Get("workload"),
		root:     q.Get("root"),
		path:     q.Get("path"),
	}
}

func (s *Server) handleFsRoots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Roots(r.Context()))
}

func (s *Server) handleFsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.FsList(r.Context(), parseFsQuery(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleFsStat(w http.ResponseWriter, r *http.Request) {
	out, err := s.FsStat(r.Context(), parseFsQuery(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleFsRead(w http.ResponseWriter, r *http.Request) {
	download := r.URL.Query().Get("download") != ""
	name, body, err := s.FsOpen(r.Context(), parseFsQuery(r), !download)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer body.Close()
	if download {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	} else if ct := imageContentType(name); ct != "" {
		// Inline image read: serve the real image type so the web image viewer's <img>
		// (and SVG in particular) renders it.
		w.Header().Set("Content-Type", ct)
	} else {
		// Editor read: text so CodeMirror gets a string, matching the legacy
		// /files/content endpoint.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = io.Copy(w, body)
}

// imageContentType returns the image MIME type for a filename's extension, or "" when
// it is not a recognized image (so callers fall back to text/plain).
func imageContentType(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	switch strings.ToLower(name[i:]) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

func (s *Server) handleFsWrite(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxEditableFileSize+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.FsWrite(r.Context(), parseFsQuery(r), data); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

// uploadFormMemory bounds what a multipart upload holds in RAM; anything past it spills
// to a temp file net/http removes when the request ends. Low on purpose — the body is
// already on disk at the far end, and there is no reason to hold a second copy in memory.
const uploadFormMemory = 1 << 20

// handleFsUpload writes an uploaded file into the directory named by path. It accepts
// either a multipart/form-data "file" part (browser file picker) or a raw body with a
// ?name= basename.
//
// Uploads STREAM. They used to ride through io.ReadAll under maxEditableFileSize, which
// left the same drag gesture succeeding as a copy (uncapped since the streaming work) and
// 413ing as an upload from the desktop — one gesture, two answers, decided by which side
// of the window the file came from.
func (s *Server) handleFsUpload(w http.ResponseWriter, r *http.Request) {
	q := parseFsQuery(r)
	dir := q.path
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		// FormFile, not MultipartReader: a container destination is a tar entry framed
		// with its size BEFORE its bytes, and only the spooled form knows the part's
		// length. The spool is bounded above and lands on disk, not in memory.
		if err := r.ParseMultipartForm(uploadFormMemory); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		q.path = joinChild(dir, hdr.Filename)
		if err := s.uploadStream(r.Context(), q, file, hdr.Size); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"result": "ok"})
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	q.path = joinChild(dir, name)
	if err := s.uploadStream(r.Context(), q, r.Body, r.ContentLength); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

// uploadStream writes exactly size bytes from src to q, publishing the destination only
// on success — the same contract a copy gets.
//
// A body with no declared length is refused rather than buffered. The destination is
// framed with the size before it sees a byte, so the alternative is to spool the whole
// thing somewhere first, and "somewhere" is the developer's disk either way. Browsers
// always set Content-Length for a File body; a chunked client is asked to say how much it
// is sending.
func (s *Server) uploadStream(ctx context.Context, q fsQuery, src io.Reader, size int64) error {
	if size < 0 {
		return statusErr(http.StatusLengthRequired,
			"upload needs a Content-Length: the destination is sized before it is written")
	}
	dst, err := s.createStream(ctx, q, size, 0)
	if err != nil {
		return err
	}
	if err := copyExactly(dst, src, size); err != nil {
		if f, ok := dst.(failer); ok {
			f.Fail()
		}
		dst.Close()
		return err
	}
	return dst.Close()
}

// joinChild appends a single basename to a directory path, defending against a
// filename that tries to climb out with slashes.
func joinChild(dir, name string) string {
	base := name
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if dir == "" {
		return base
	}
	return strings.TrimRight(dir, "/") + "/" + base
}

func (s *Server) handleFsMkdir(w http.ResponseWriter, r *http.Request) {
	if err := s.FsMkdir(r.Context(), parseFsQuery(r)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleFsRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.FsRename(r.Context(), parseFsQuery(r), body.To); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

// handleFsCopy and handleFsMove share one body shape. With no "items" they keep the
// original single-item contract — the source is the query's path, "to" is the exact
// destination, and the reply is {"result","skipped"} — because the SPA, the mock and the
// existing tests all speak it. With "items" they run a BATCH and reply per item.
func (s *Server) handleFsCopy(w http.ResponseWriter, r *http.Request) {
	s.handleFsTransfer(w, r, opCopy)
}

func (s *Server) handleFsMove(w http.ResponseWriter, r *http.Request) {
	s.handleFsTransfer(w, r, opMove)
}

func (s *Server) handleFsTransfer(w http.ResponseWriter, r *http.Request, op fsOp) {
	req, base, ok := decodeTransfer(w, r)
	if !ok {
		return
	}
	pairs, err := transferPairs(base, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Single item: the legacy shape, including surfacing the error as the HTTP status
	// rather than burying it in a per-item record.
	if len(req.Items) == 0 {
		src := fsQuery{source: base.source, workload: base.workload, root: base.root, path: pairs[0].From}
		dst := fsQuery{source: base.source, workload: base.workload, root: base.root, path: pairs[0].To}
		var skipped []string
		if op == opMove {
			skipped, err = s.FsMove(r.Context(), src, dst)
		} else {
			skipped, err = s.FsCopy(r.Context(), src, dst)
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, struct {
			Result  string   `json:"result"`
			Skipped []string `json:"skipped,omitempty"`
		}{"ok", skipped})
		return
	}
	// A batch always answers 200 with per-item detail: the request itself succeeded,
	// and one item failing is a fact about that item, not about the request. A status
	// code cannot carry "three of five landed".
	writeJSON(w, s.FsBatch(r.Context(), op, base, pairs))
}

// handleFsPreflight reports what a copy or move would do, and changes nothing. It
// accepts exactly the body the real endpoints do, so a UI can preflight the request it
// is about to send rather than an approximation of it.
func (s *Server) handleFsPreflight(w http.ResponseWriter, r *http.Request) {
	op := opCopy
	if r.URL.Query().Get("op") == "move" {
		op = opMove
	}
	req, base, ok := decodeTransfer(w, r)
	if !ok {
		return
	}
	pairs, err := transferPairs(base, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, s.FsPreflight(r.Context(), op, base, pairs))
}

// decodeTransfer reads the shared body. The 64 KiB bound is generous for a path list and
// keeps an unbounded body from being buffered on an unauthenticated surface.
func decodeTransfer(w http.ResponseWriter, r *http.Request) (fsTransferRequest, fsQuery, bool) {
	var req fsTransferRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return req, fsQuery{}, false
	}
	return req, parseFsQuery(r), true
}

func (s *Server) handleFsDelete(w http.ResponseWriter, r *http.Request) {
	recursive := r.URL.Query().Get("recursive") != ""
	if err := s.FsDelete(r.Context(), parseFsQuery(r), recursive); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}
