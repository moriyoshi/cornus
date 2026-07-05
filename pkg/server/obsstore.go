package server

import (
	"context"
	"net/http"
	"os"
	"time"

	"cornus/pkg/config"
	"cornus/pkg/logging"
	"cornus/pkg/obsstore"
	"cornus/pkg/supervisor"
)

// obsMaintainInterval is how often retention and compaction run. Hourly is well
// under the coarsest retention the store can express (whole days), so data never
// outlives its window by more than an hour, while staying far off any hot path.
const obsMaintainInterval = time.Hour

// openObsStore opens the built-in observability store for cfg, or returns
// (nil, nil) when there is nothing to open.
//
// Every failure mode here is non-fatal by design, and the reason is worth
// stating: observability is the thing you consult when something else is broken.
// A server that refuses to start because its flight-data recorder would not open
// has turned a diagnostic aid into an outage. So a store that cannot open is
// logged loudly and left nil, and every caller treats nil as "the feature is
// off".
func openObsStore(ctx context.Context, cfg config.Config) obsstore.Store {
	if !cfg.ObsEnabled {
		return nil
	}
	log := logging.FromContext(ctx)
	if !obsstore.Compiled() {
		// Say which build produced this binary, not just that the feature is
		// missing: the remedy is a build flag, and naming it is the difference
		// between a two-minute fix and an afternoon.
		//
		// Reaching here means someone passed --obs explicitly, because an
		// unspecified --obs resolves to obsstore.Compiled() (see cmd/cornus/serve.go).
		// So this is a plain `go build` binary, not a released one: every released
		// binary and the published image link the store in.
		log.WarnContext(ctx, "observability store requested but not compiled into this binary; workload telemetry will not be recorded",
			"remedy", "rebuild with -tags \"imbh sable_extern_lib\" (see `make test-imbh` for the CGO_LDFLAGS this needs), or use a released cornus binary or image — those all ship the store")
		return nil
	}
	dir := cfg.ObservabilityDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WarnContext(ctx, "observability store disabled: cannot create its directory", "dir", dir, "error", err)
		return nil
	}
	st, err := obsstore.Open(obsstore.Config{
		Dir:       dir,
		Retention: cfg.ObsRetention,
		MaxBytes:  cfg.ObsMaxBytes,
		// Cap admitted work so a workload that suddenly logs in a tight loop
		// sheds at the door rather than growing the server's heap until it is
		// the outage. The recorder counts what it loses.
		MaxInFlight: obsMaxInFlight,
	})
	if err != nil {
		log.WarnContext(ctx, "observability store disabled: open failed", "dir", dir, "error", err)
		return nil
	}
	log.InfoContext(ctx, "observability store open", "dir", dir,
		"retention", cfg.ObsRetention, "maxBytes", cfg.ObsMaxBytes, "recordLogs", cfg.ObsRecordLogs)
	return st
}

// obsMaxInFlight bounds concurrently admitted store operations. It is generous
// relative to the number of workloads one server hosts, so it only engages under
// genuine flooding rather than shaping normal traffic.
const obsMaxInFlight = 256

// obsEnabled reports whether the STORE is live on this server, i.e. whether
// anything can be queried back.
func (s *Server) obsEnabled() bool { return s.obs != nil }

// obsIngestEnabled reports whether the server can accept telemetry at all —
// because it has a store to keep it in, an upstream to forward it to, or both.
// It is the gate for the receive surface, which is useful in the
// forward-only configuration where obsEnabled is false.
func (s *Server) obsIngestEnabled() bool { return s.obs != nil || s.obsExport != nil }

// superviseObsExport registers the re-export worker. Supervised like the other
// long-lived loops, so a panic restarts it in place rather than silently ending
// forwarding while ingest carries on.
func (s *Server) superviseObsExport() {
	if s.obsExport == nil {
		return
	}
	s.sup.Add("obs-export", supervisor.ServiceFunc(s.obsExport.Serve), supervisor.Restart)
}

// superviseObsMaintenance registers the retention/compaction loop. It is a
// supervised child like the GC loop, so a transient failure restarts in place
// and each run is recorded in the flight log rather than vanishing.
func (s *Server) superviseObsMaintenance() {
	if !s.obsEnabled() {
		return
	}
	s.sup.Add("obs-maintain", supervisor.ServiceFunc(func(ctx context.Context) error {
		t := time.NewTicker(obsMaintainInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				if err := s.obs.Maintain(ctx); err != nil {
					// Returning the error would restart the loop with
					// backoff, which buys nothing for a periodic task: the
					// next tick retries anyway, and a failing compaction is
					// not a reason to stop applying retention.
					logging.FromContext(ctx).WarnContext(ctx, "observability store maintenance failed", "error", err)
				}
			}
		}
	}), supervisor.Restart)
}

// closeObsStore releases the store. It runs after the supervisor has drained, so
// nothing is still writing when the handle goes away.
func (s *Server) closeObsStore() {
	if s.obs == nil {
		return
	}
	_ = s.obs.Close()
	s.obs = nil
}

// handleObsStatus serves GET /.cornus/v1/obs/status: what the store is holding,
// how far back it reaches, and whether it is dropping anything.
//
// The dropped counter is the point of this endpoint. Without it, an empty query
// result is ambiguous between "nothing happened" and "the store shed the
// evidence", and those call for opposite responses.
func (s *Server) handleObsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	st, err := s.obs.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.obsExport != nil {
		sent, dropped, failed := s.obsExport.Stats()
		st.Export = &obsstore.ExportStatus{
			Endpoint: s.obsExport.cfg.Endpoint,
			Sent:     sent,
			Dropped:  dropped,
			Failed:   failed,
		}
	}
	if r := s.metricsRecorder; r != nil {
		st.Metrics = &obsstore.MetricsStatus{
			Interval: r.interval,
			Replicas: r.Replicas(),
			Sampled:  r.Sampled(),
			Failed:   r.Failed(),
			Dropped:  r.Dropped(),
		}
	}
	writeJSON(w, http.StatusOK, st)
}
