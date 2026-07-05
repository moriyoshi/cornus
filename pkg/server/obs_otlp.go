package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"cornus/pkg/logging"
	"cornus/pkg/obsstore"
)

// The OTLP/HTTP receive side of the built-in observability store: where a
// workload's own telemetry lands when its telemetry spec names no external
// backend.
//
// The whole receiver is this thin because the two formats already agree. The
// caretaker's embedded Collector speaks OTLP/HTTP protobuf, and the store ingests
// OTLP/HTTP protobuf, so the server's job is to check the request and hand the
// bytes across — no decode, no re-encode, no schema of our own in the middle.
//
// Only HTTP is served, not gRPC. The default wiring picks `http/protobuf` for
// exactly this reason: a second listener and a gRPC stack would buy nothing when
// the one client is a Collector that speaks both.

// otlpMaxBodyBytes caps one export request. The Collector batches, so a
// legitimate request is well under this; the cap is what stops a
// misconfigured or hostile sender from turning an export into a memory
// exhaustion.
const otlpMaxBodyBytes = 16 << 20 // 16 MiB

// protobufContentType is the only encoding the receiver accepts. OTLP also
// defines a JSON encoding, but nothing in cornus emits it, and accepting a
// format with no producer would be untested surface.
const protobufContentType = "application/x-protobuf"

// handleOTLPLogs, handleOTLPTraces and handleOTLPMetrics serve
// POST /.cornus/v1/otlp/v1/{logs,traces,metrics}.
func (s *Server) handleOTLPLogs(w http.ResponseWriter, r *http.Request) {
	s.receiveOTLP(w, r, obsSignalLogs, &collogs.ExportLogsServiceResponse{})
}

func (s *Server) handleOTLPTraces(w http.ResponseWriter, r *http.Request) {
	s.receiveOTLP(w, r, obsSignalTraces, &coltrace.ExportTraceServiceResponse{})
}

func (s *Server) handleOTLPMetrics(w http.ResponseWriter, r *http.Request) {
	s.receiveOTLP(w, r, obsSignalMetrics, &colmetrics.ExportMetricsServiceResponse{})
}

// The three OTLP signals, as they appear in the `/v1/{signal}` path segment and
// in the caretaker mux stream's header line.
const (
	obsSignalLogs    = "logs"
	obsSignalTraces  = "traces"
	obsSignalMetrics = "metrics"
)

// acceptOTLP is the one place a received export is acted on, shared by the HTTP
// receiver and the caretaker mux path so the two can never diverge.
//
// The store and the forwarder are independent: either, both, or (unreachably,
// since the routes would not be registered) neither. Storing first and forwarding
// second is deliberate — the local copy is the one cornus can guarantee, and a
// forwarder drop must not cost the record that was already safely ingested.
func (s *Server) acceptOTLP(signal string, body []byte) error {
	var err error
	if s.obs != nil {
		switch signal {
		case obsSignalLogs:
			err = s.obs.IngestLogs(body)
		case obsSignalTraces:
			err = s.obs.IngestTraces(body)
		case obsSignalMetrics:
			err = s.obs.IngestMetrics(body)
		default:
			return fmt.Errorf("unknown OTLP signal %q", signal)
		}
	} else {
		// Gateway mode: with no store there is nothing in the path that would
		// otherwise parse this, so validate explicitly. Skipping it would make
		// cornus answer 200 to a payload it cannot understand and then hand it to
		// the upstream, which rejects it — the sender, having been told success,
		// has already discarded its copy. A relay that launders bad payloads into
		// silent upstream failures is worse than one that refuses them.
		//
		// The cost is only paid when there is no store; when there is, the store's
		// own decode is the validation and parsing twice would be pure overhead on
		// the hot path.
		err = validateOTLP(signal, body)
	}
	if err != nil {
		return err
	}
	// Enqueue is non-blocking and drops when saturated; see obsexport.go.
	s.obsExport.Enqueue(signal, body)
	return nil
}

// validateOTLP reports whether body parses as the export request for signal.
func validateOTLP(signal string, body []byte) error {
	var msg proto.Message
	switch signal {
	case obsSignalLogs:
		msg = &collogs.ExportLogsServiceRequest{}
	case obsSignalTraces:
		msg = &coltrace.ExportTraceServiceRequest{}
	case obsSignalMetrics:
		msg = &colmetrics.ExportMetricsServiceRequest{}
	default:
		return fmt.Errorf("unknown OTLP signal %q", signal)
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		return fmt.Errorf("not a valid OTLP %s export: %w", signal, err)
	}
	return nil
}

// receiveOTLP is the shared body of the three signal endpoints.
//
// The status codes follow the OTLP/HTTP spec, because the sender is a real
// Collector whose retry behavior depends on them: 400 means the payload is
// broken and retrying will not help, 429 means back off and retry, 200 means
// accepted.
func (s *Server) receiveOTLP(w http.ResponseWriter, r *http.Request, signal string, resp proto.Message) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.apiPolicy.Allow(Identity(r), "observe") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to export telemetry"})
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !isProtobufContentType(ct) {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
			"error": "unsupported content type " + ct + "; this endpoint accepts " + protobufContentType,
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, otlpMaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": "export request exceeds the receiver limit",
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reading export request: " + err.Error()})
		return
	}
	if len(body) == 0 {
		// An empty body is a well-formed no-op export, not an error.
		writeProto(w, resp)
		return
	}

	if err := s.acceptOTLP(signal, body); err != nil {
		if errors.Is(err, obsstore.ErrBackpressure) {
			// Tell the Collector to retry rather than dropping: unlike the
			// stdout recorder, whose source keeps flowing regardless, an
			// exporter holds the only copy of these spans and will re-send them.
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "observability store is shedding load"})
			return
		}
		// Anything else means the payload did not parse as OTLP. Retrying it
		// unchanged cannot succeed, so say so with a 400 and let the sender
		// discard it.
		logging.FromContext(r.Context()).WarnContext(r.Context(), "OTLP export rejected",
			"signal", signal, "bytes", len(body), "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeProto(w, resp)
}

// isProtobufContentType reports whether ct names OTLP's protobuf encoding,
// tolerating parameters ("application/x-protobuf; charset=utf-8").
func isProtobufContentType(ct string) bool {
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' {
			ct = ct[:i]
			break
		}
	}
	for len(ct) > 0 && ct[len(ct)-1] == ' ' {
		ct = ct[:len(ct)-1]
	}
	return ct == protobufContentType || ct == "application/protobuf"
}

// writeProto sends an empty success response in the encoding the sender expects.
// OTLP defines the success body as the (empty) ExportXxxServiceResponse message,
// and Collectors do parse it, so an empty 200 with no body is not sufficient.
func writeProto(w http.ResponseWriter, msg proto.Message) {
	b, err := proto.Marshal(msg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", protobufContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
