package dockerproxy

import (
	"encoding/json"
	"net/http"
)

// jsonStream writes a Docker jsonmessage progress stream (the newline-delimited
// JSON the CLI renders for pull/push). Each write is flushed so the CLI shows
// progress live.
type jsonStream struct {
	enc *json.Encoder
	f   http.Flusher
}

// jsonMessage is the subset of Docker's jsonmessage.JSONMessage the proxy emits.
type jsonMessage struct {
	Status      string           `json:"status,omitempty"`
	Aux         any              `json:"aux,omitempty"`
	Error       string           `json:"error,omitempty"`
	ErrorDetail *jsonErrorDetail `json:"errorDetail,omitempty"`
}

type jsonErrorDetail struct {
	Message string `json:"message"`
}

// newJSONStream starts a 200 chunked JSON stream on w.
func newJSONStream(w http.ResponseWriter) *jsonStream {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &jsonStream{enc: json.NewEncoder(w), f: f}
}

func (s *jsonStream) emit(m jsonMessage) {
	_ = s.enc.Encode(m)
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *jsonStream) status(msg string) { s.emit(jsonMessage{Status: msg}) }
func (s *jsonStream) aux(v any)         { s.emit(jsonMessage{Aux: v}) }

// fail emits a Docker error frame (the daemon reports push/pull failures inside
// the 200 stream, not as an HTTP error status).
func (s *jsonStream) fail(msg string) {
	s.emit(jsonMessage{Error: msg, ErrorDetail: &jsonErrorDetail{Message: msg}})
}
