package server

import (
	"context"
	"errors"
	"net"

	"cornus/pkg/logging"
	"cornus/pkg/obsstore"
	"cornus/pkg/wire"
)

// The caretaker-mux arm of the OTLP receive path.
//
// It is the same acceptance as the HTTP receiver — same store, same re-export
// forwarder, same signal vocabulary — reached over the pod's existing server
// connection rather than over a fresh network dial. Sharing acceptOTLP between
// the two is what guarantees a workload's telemetry is treated identically
// whichever way it arrived; the transport is the only difference.
//
// Note what this path does NOT need: an advertised server URL, a route from the
// pod to it, or a credential of its own. The connection is already established
// and already authenticated, which is the entire reason the mux option exists.

// acceptTelemetryMuxed reads one export off a telemetry stream, accepts it, and
// acknowledges the outcome.
//
// The verdict is a single byte because the caretaker turns it straight back into
// an HTTP status for its local Collector: accepted, retry, or give up. Anything
// richer would have no consumer — the Collector's retry logic only distinguishes
// those three.
func (s *Server) acceptTelemetryMuxed(ctx context.Context, stream net.Conn) {
	defer stream.Close()

	signal, body, err := wire.ReadTelemetryHeader(stream)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "caretaker telemetry: unreadable export", "error", err)
		_ = wire.WriteTelemetryAck(stream, wire.TelemetryAckReject)
		return
	}
	if !s.obsIngestEnabled() {
		// A caretaker wired for the mux against a server with nowhere to put
		// telemetry. Reject rather than ask for a retry: retrying cannot help,
		// and a silent accept would lose the data while looking healthy.
		logging.FromContext(ctx).WarnContext(ctx, "caretaker telemetry: export received but this server has no store and no re-export upstream",
			"signal", signal, "remedy", "start the server with --obs and/or --obs-export-endpoint")
		_ = wire.WriteTelemetryAck(stream, wire.TelemetryAckReject)
		return
	}

	if err := s.acceptOTLP(signal, body); err != nil {
		ack := wire.TelemetryAckReject
		if errors.Is(err, obsstore.ErrBackpressure) {
			// The sender still holds the only copy, exactly as on the HTTP path,
			// so ask it to retry rather than dropping.
			ack = wire.TelemetryAckRetry
		} else {
			logging.FromContext(ctx).WarnContext(ctx, "caretaker telemetry: export rejected",
				"signal", signal, "bytes", len(body), "error", err)
		}
		_ = wire.WriteTelemetryAck(stream, ack)
		return
	}
	_ = wire.WriteTelemetryAck(stream, wire.TelemetryAckOK)
}
