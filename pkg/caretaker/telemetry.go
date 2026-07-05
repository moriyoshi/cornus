package caretaker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/wire"
)

// Telemetry over the pod-scoped caretaker connection.
//
// The embedded Collector exports OTLP over the network to whatever endpoint it
// is configured with. That is right for a third-party backend, and wrong when the
// destination is the cornus server the caretaker is ALREADY connected to: it
// needs a separately reachable URL, a route from the pod to it, and its own
// credential — three things that can each be absent in exactly the environments
// where telemetry matters most (a locked-down NetworkPolicy, an isolated
// network, a server with no advertised URL).
//
// This role removes all three. It runs a minimal OTLP/HTTP receiver on
// pod-loopback, points the Collector at it, and forwards each export body over
// the existing multiplexed connection. The Collector is unmodified and unaware:
// it thinks it is exporting to an ordinary OTLP endpoint.
//
// # Why a loopback hop rather than a Collector exporter
//
// The obvious alternative is a custom Collector exporter that writes straight to
// a yamux stream. It was rejected because the Collector is a curated
// collector-CORE build (pkg/otelcollector) — adding a bespoke exporter means
// carrying a component that upstream does not have, in a dependency tree already
// chosen for being small and standard. A loopback listener costs one hop inside
// the pod's own network namespace and keeps the Collector stock.

// TelemetryRelayRole configures the loopback OTLP receiver that forwards the
// workload's telemetry over the caretaker's server connection.
type TelemetryRelayRole struct {
	// Server is the cornus server whose connection carries the exports. It
	// selects which pod-scoped connection this role rides.
	Server string `json:"server,omitempty"`
	// Listen is the pod-loopback address the Collector exports to.
	Listen string `json:"listen,omitempty"`
}

// DefaultTelemetryRelayPort is the loopback port the relay listens on. It sits
// just past the two OTLP receiver ports (4317/4318) the Collector itself binds,
// so the three telemetry ports in a pod read as one block.
//
// It is IMPORTED from pkg/deploy rather than restated. The two sides are opposite
// ends of one wire — pkg/deploy points the workload's
// OTEL_EXPORTER_OTLP_ENDPOINT at this port, and the code below binds it — so a
// drift does not fail anywhere: the Collector exports into a closed port and the
// workload's telemetry silently stops arriving, on a feature that is on by
// default. deploy is the lower layer (pkg/caretaker already depends on it), so it
// owns the constant and this is a plain reference, not a mirror.
//
// Note the contrast with barehost's caretakerConfig, which DOES restate
// pkg/caretaker's wire form and guards it with TestCaretakerConfigWire: that
// duplication buys barehost its zero-buildkit invariant. No such cost exists
// here, so the duplicate is deleted rather than guarded.
const DefaultTelemetryRelayPort = deploy.DefaultTelemetryRelayPort

// telemetryRelayShutdown bounds how long the listener waits for in-flight
// exports on teardown. Short: an export that has not completed by then is one
// the Collector will retry.
const telemetryRelayShutdown = 3 * time.Second

// runTelemetryRelay serves the loopback receiver until ctx is cancelled,
// forwarding every accepted export over sess.
func runTelemetryRelay(ctx context.Context, sess *yamux.Session, role TelemetryRelayRole) error {
	addr := role.Listen
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", DefaultTelemetryRelayPort)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("caretaker telemetry: listen %s: %w", addr, err)
	}
	log := logging.FromContext(ctx)
	log.InfoContext(ctx, "caretaker telemetry: relaying workload exports over the server connection", "listen", addr)

	srv := &http.Server{Handler: telemetryRelayHandler(sess)}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), telemetryRelayShutdown)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("caretaker telemetry: serve: %w", err)
	}
	return nil
}

// telemetryRelayHandler answers the OTLP/HTTP paths the Collector posts to.
//
// The status codes matter as much as the forwarding: the Collector's own retry
// logic reads them, so mapping the server's verdict back onto 200/429/400 is what
// keeps that logic working across the mux. Inventing a status here would either
// lose exports the server asked us to retry, or retry ones it will never accept.
func telemetryRelayHandler(sess *yamux.Session) http.Handler {
	mux := http.NewServeMux()
	for _, signal := range []string{"logs", "traces", "metrics"} {
		signal := signal
		mux.HandleFunc("POST /v1/"+signal, func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, telemetryRelayMaxBody+1))
			if err != nil {
				http.Error(w, "reading export: "+err.Error(), http.StatusBadRequest)
				return
			}
			if len(body) > telemetryRelayMaxBody {
				http.Error(w, "export too large", http.StatusRequestEntityTooLarge)
				return
			}
			if len(body) == 0 {
				w.WriteHeader(http.StatusOK)
				return
			}
			switch err := wire.OpenTelemetry(sess, signal, body); {
			case err == nil:
				w.WriteHeader(http.StatusOK)
			case errors.Is(err, wire.ErrTelemetryRetry):
				w.Header().Set("Retry-After", "1")
				http.Error(w, "server is shedding load", http.StatusTooManyRequests)
			default:
				// A stream failure (the connection is down, mid-redial) is
				// transient and the Collector should retry; a server rejection is
				// not, but telling them apart on this side would mean encoding
				// more verdicts than the ack carries. Erring toward retry keeps
				// exports across a reconnect, which is the far more common case.
				logging.FromContext(r.Context()).WarnContext(r.Context(),
					"caretaker telemetry: forwarding export failed", "signal", signal, "error", err)
				w.Header().Set("Retry-After", "1")
				http.Error(w, "forwarding failed: "+err.Error(), http.StatusServiceUnavailable)
			}
		})
	}
	return mux
}

// telemetryRelayMaxBody mirrors the server-side cap so an oversized export is
// refused here, inside the pod, rather than after crossing the connection.
const telemetryRelayMaxBody = 16 << 20

// telemetryRelayReady reports nil once the relay's loopback listener accepts
// connections, so the app container's startup probe waits for the whole
// telemetry path rather than only the Collector's own receiver.
func telemetryRelayReady(role TelemetryRelayRole) error {
	addr := role.Listen
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", DefaultTelemetryRelayPort)
	}
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("telemetry relay %s not ready: %w", addr, err)
	}
	_ = c.Close()
	return nil
}

// TelemetryRelayEndpoint is the OTLP endpoint a Collector should export to when
// the relay carries its telemetry.
func TelemetryRelayEndpoint(listen string) string {
	if listen == "" {
		listen = fmt.Sprintf("127.0.0.1:%d", DefaultTelemetryRelayPort)
	}
	if strings.HasPrefix(listen, "http://") || strings.HasPrefix(listen, "https://") {
		return listen
	}
	return "http://" + listen
}
