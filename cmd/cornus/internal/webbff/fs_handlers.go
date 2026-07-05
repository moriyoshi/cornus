package webbff

// HTTP adapters for the file-explorer surface (/.cornus/web/fs*). Each is a thin
// wrapper over the value-returning core methods in fs.go, following the same
// writeJSON / writeErr / statusErr conventions as the rest of the BFF.

import (
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

// handleFsUpload writes an uploaded file into the directory named by path. It accepts
// either a multipart/form-data "file" part (browser file picker) or a raw body with a
// ?name= basename.
func (s *Server) handleFsUpload(w http.ResponseWriter, r *http.Request) {
	q := parseFsQuery(r)
	dir := q.path
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxEditableFileSize+1))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		q.path = joinChild(dir, hdr.Filename)
		if err := s.FsWrite(r.Context(), q, data); err != nil {
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
	data, err := io.ReadAll(io.LimitReader(r.Body, maxEditableFileSize+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q.path = joinChild(dir, name)
	if err := s.FsWrite(r.Context(), q, data); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
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

// handleFsCopy copies the file named by the request query to the virtual path in the
// JSON body's "to" field. Both endpoints ride the same source context (virtual for
// the SPA), so a single "to" virtual path can land the file under any other mount.
func (s *Server) handleFsCopy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src := parseFsQuery(r)
	dst := fsQuery{source: src.source, workload: src.workload, root: src.root, path: body.To}
	if err := s.FsCopy(r.Context(), src, dst); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}

func (s *Server) handleFsDelete(w http.ResponseWriter, r *http.Request) {
	recursive := r.URL.Query().Get("recursive") != ""
	if err := s.FsDelete(r.Context(), parseFsQuery(r), recursive); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"result": "ok"})
}
