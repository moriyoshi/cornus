package server

// The structured filesystem-operation endpoint.
//
// /.cornus/v1/deploy/{name}/archive can pack a path and unpack a tar and nothing else —
// no readdir, no delete, no rename, and no way to copy within a deployment without
// dragging every byte through the caller. On kubernetes it cannot even do that much. This
// endpoint exposes deploy.FSOperator, which backends realize wherever the bytes actually
// are: through the pod's caretaker, or in process for a backend that owns its volumes.
//
// It is deliberately ONE endpoint carrying an op name rather than a REST surface per
// operation. The op set is the backend contract's, and splitting it across routes would
// mean two places to keep in step every time the contract grows.

import (
	"io"
	"net/http"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// fsopStatus maps a refusal's machine-readable code onto an HTTP status. The mapping is
// the point of the codes: a caller that must decide between "tell the user" and "do this
// the slow way instead" cannot make that decision from a message.
func fsopStatus(resp api.FSOpResponse) int {
	switch resp.Code {
	case api.FSErrNotFound:
		return http.StatusNotFound
	case api.FSErrUnsupported:
		// 501, so the caller falls back rather than reporting a failure. This is the
		// same status streamErrStatus already gives an unsupported backend.
		return http.StatusNotImplemented
	case api.FSErrReadOnly:
		return http.StatusForbidden
	case api.FSErrExists, api.FSErrNotEmpty, api.FSErrCrossDevice:
		return http.StatusConflict
	case api.FSErrNotDir, api.FSErrIsDir:
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// handleDeployFSOp serves POST /.cornus/v1/deploy/{name}/fsop.
//
// The op and its operands ride the query string; a put's tar is the request body, and a
// get's tar is the response body. Every other op answers with the api.FSOpResponse as
// JSON.
func (s *Server) handleDeployFSOp(w http.ResponseWriter, r *http.Request, backend deploy.Backend, name string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	operator, ok := backend.(deploy.FSOperator)
	if !ok {
		// "unsupported" is load-bearing text, not prose: streamErrStatus and every
		// client fallback match on it.
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "structured filesystem operations are unsupported on the " + backend.Name() + " backend",
		})
		return
	}

	q := r.URL.Query()
	req := api.FSOpRequest{
		Op:                   api.FSOp(q.Get("op")),
		Path:                 q.Get("path"),
		To:                   q.Get("to"),
		Recursive:            boolParam(q.Get("recursive")),
		NoOverwriteDirNonDir: boolParam(q.Get("noOverwriteDirNonDir")),
		CopyUIDGID:           boolParam(q.Get("copyUIDGID")),
	}
	if req.Op == "" || req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "op and path are required"})
		return
	}

	var body io.Reader
	if req.Op == api.FSOpPut {
		body = r.Body
	}
	if req.Op != api.FSOpGet {
		resp, err := operator.FSOp(r.Context(), name, req, body, nil)
		if err != nil {
			writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
			return
		}
		if resp.Error != "" {
			writeJSON(w, fsopStatus(resp), resp)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// A get streams the operator's tar straight through. As with the archive GET, the
	// 200 is deferred until the first byte so a refusal that arrives before any output
	// becomes a real error response instead of an empty, well-formed archive — which a
	// tar reader would happily accept as an empty directory.
	w.Header().Set("Content-Type", "application/x-tar")
	lw := newLazyFlushWriter(w)
	resp, err := operator.FSOp(r.Context(), name, req, nil, lw)
	switch {
	case err != nil && !lw.wrote:
		w.Header().Del("Content-Type")
		writeJSON(w, streamErrStatus(err), map[string]string{"error": err.Error()})
	case err != nil:
		lw.setStreamError(err)
	case resp.Error != "" && !lw.wrote:
		w.Header().Del("Content-Type")
		writeJSON(w, fsopStatus(resp), resp)
	case resp.Error != "":
		lw.setStreamError(fsopRespError(resp))
	}
}

// fsopRespError turns a refusal into an error for the mid-stream trailer, where there is
// no status left to carry it.
func fsopRespError(resp api.FSOpResponse) error { return &fsopError{resp: resp} }

type fsopError struct{ resp api.FSOpResponse }

func (e *fsopError) Error() string { return e.resp.Error }

// boolParam accepts the "1"/"true" spellings the archive endpoint already takes, so the
// two surfaces do not disagree about what a flag looks like.
func boolParam(v string) bool { return v == "1" || strings.EqualFold(v, "true") }
