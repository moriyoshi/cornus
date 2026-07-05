package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cornus/pkg/activity"
	"cornus/pkg/logging"
)

// LiveInstanceHeader carries the serving process's own activity instance id, so
// a client can distinguish its still-open lifetime record (it is running) from
// a previous incarnation's (it died without unwinding).
const LiveInstanceHeader = "Cornus-Activity-Live-Instance"

// handleActivity serves GET /.cornus/v1/activity: this server's flight records,
// as NDJSON, one event per line.
//
// It exists because the operator is almost never on the machine the server ran
// on — it is behind an SSH tunnel, in a cluster, or in a container — so a
// recorder only readable from local disk would be unreadable in practice. It
// still answers post-mortem questions, because the records live under the data
// dir, which is the thing deployments keep persistent: a replacement server
// serves its predecessor's flight. Only "no server ever comes back" needs
// `cornus activity --local`.
//
// Unlike /.cornus/v1/info this is NOT auth-exempt. Records carry deployment
// names, image refs and caller identity, so it goes through the same policy gate
// as the rest of the API.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.apiPolicy.Allow(Identity(r), "activity") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to read the activity log"})
		return
	}
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid since (want RFC3339): " + err.Error()})
			return
		}
		since = t
	}
	unfinished := q.Get("unfinished") != ""
	if q.Get("follow") != "" {
		if unfinished {
			// "Unfinished" is a property of the stream as a whole, not of an event:
			// the same begin is unfinished until its end arrives. Streaming a
			// filtered subset would emit records that the very next line makes
			// false, with no way to retract them. Say so rather than serve a view
			// that lies as it goes.
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "follow and unfinished are incompatible: unfinished is resolved over the whole stream, so it is a snapshot, not a feed. Poll without follow, or follow and pair begin/end yourself.",
			})
			return
		}
		s.streamActivity(w, r, since, q.Get("kind"))
		return
	}
	events, err := activity.Read(ActivityDir(s.cfg))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	events = filterActivity(events, since, q.Get("kind"), unfinished)

	// Name the live incarnation, so a reader can tell "still running" from "died
	// without unwinding" — both of which look like an open lifetime record. A
	// running server reported as an unclean exit would be alarming and wrong.
	if inst := s.activity.Instance(); inst != "" {
		w.Header().Set(LiveInstanceHeader, inst)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return // client hung up
		}
	}
}

// activityKeepAlive bounds how long a followed stream may stay silent. A flight
// recorder is quiet exactly when nothing is going wrong, which is most of the
// time — and an idle connection is what a proxy, a load balancer or a NAT reaps.
// SSE has a keep-alive built in: a comment line, which every conforming reader
// discards.
const activityKeepAlive = 30 * time.Second

// ActivityEventName is the SSE event name each record is sent under, so a reader
// can register one handler for records and ignore anything added later.
const ActivityEventName = "activity"

// streamActivity serves follow mode: the backlog, then every record as it is
// appended, until the client goes away.
//
// The transport is Server-Sent Events rather than the bare NDJSON the one-shot
// read returns. A followed stream is a different thing from a document: it is
// long-lived, mostly idle, and has to survive whatever sits between the operator
// and the server. SSE is the framing that already answers all three — a defined
// keep-alive (a comment line), a media type intermediaries know not to buffer,
// and a named event so the payload can gain siblings without breaking readers.
// The one-shot path stays NDJSON, which is what the records are on disk.
//
// The history and the live tail come from ONE tailer rather than a read followed
// by a watch. That is the whole correctness argument: records written between
// "read what is there" and "start watching" would otherwise be lost, and those
// are precisely the records of a system under load.
func (s *Server) streamActivity(w http.ResponseWriter, r *http.Request, since time.Time, kind string) {
	ctx := r.Context()
	log := logging.FromContext(ctx, slog.String("component", "activity"))
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported by this server"})
		return
	}
	if inst := s.activity.Instance(); inst != "" {
		w.Header().Set(LiveInstanceHeader, inst)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Proxies that buffer a response defeat the entire point of following one, and
	// nginx in particular buffers by default until told otherwise.
	w.Header().Set("X-Accel-Buffering", "no")
	// Commit the response before the first record: a follower that sees no
	// headers until something happens is indistinguishable from one that failed
	// to connect, and this stream can legitimately be silent for hours.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// The poll loop is written out here rather than delegated to activity.Follow
	// because this stream needs a tick even when nothing was appended — that is
	// when the keep-alive has to go out. The tailer is the shared part; the pacing
	// is the transport's business.
	tailer := activity.NewTailer(ActivityDir(s.cfg))
	tick := time.NewTicker(activity.DefaultPollInterval)
	defer tick.Stop()
	lastWrite := time.Now()
	for {
		events, err := tailer.Next()
		if err != nil {
			// The response is already committed, so a read failure can only be
			// logged. Give up rather than spin on a directory we cannot read.
			log.WarnContext(ctx, "activity follow: reading the records failed", "error", err)
			return
		}
		wrote := false
		for _, e := range events {
			if !matchActivity(e, since, kind) {
				continue
			}
			if err := writeSSEEvent(w, ActivityEventName, e); err != nil {
				return // client hung up
			}
			wrote = true
		}
		if !wrote && time.Since(lastWrite) >= activityKeepAlive {
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			wrote = true
		}
		if wrote {
			flusher.Flush()
			lastWrite = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// writeSSEEvent frames one record as an SSE message.
//
// The payload is compact JSON on a single data line, which is what keeps this
// simple: SSE splits a payload across as many data lines as it has newlines, and
// a reader then has to rejoin them. json.Marshal never emits a bare newline, so
// one record is always exactly one data line.
func writeSSEEvent(w io.Writer, name string, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload)
	return err
}

// filterActivity applies the query filters. unfinished is resolved over the
// WHOLE stream before the other filters, because an activity's begin and end can
// straddle any window — narrowing first would report a completed activity as
// unfinished simply because its end fell outside the range.
func filterActivity(events []activity.Event, since time.Time, kind string, unfinishedOnly bool) []activity.Event {
	if unfinishedOnly {
		events = activity.UnfinishedFrom(events)
	}
	if since.IsZero() && kind == "" {
		return events
	}
	out := events[:0:0]
	for _, e := range events {
		if matchActivity(e, since, kind) {
			out = append(out, e)
		}
	}
	return out
}

// matchActivity is the per-event half of filterActivity, shared with the
// followed stream — where there is no "whole set" to filter, only one record at
// a time. Kind matching is case-insensitive so `--kind Deploy` is not silently
// empty.
func matchActivity(e activity.Event, since time.Time, kind string) bool {
	if kind != "" && !strings.EqualFold(string(e.Kind), kind) {
		return false
	}
	if !since.IsZero() {
		ts := e.Unix()
		if ts.IsZero() || ts.Before(since) {
			return false
		}
	}
	return true
}
