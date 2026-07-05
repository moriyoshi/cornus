package dockerproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// eventMessage is one Docker events-API message (the subset clients consume).
// The legacy top-level status/id/from fields are kept alongside the modern
// Type/Action/Actor form: the docker CLI decodes the stream into its
// events.Message struct, which carries both.
type eventMessage struct {
	Status   string     `json:"status,omitempty"`
	ID       string     `json:"id,omitempty"`
	From     string     `json:"from,omitempty"`
	Type     string     `json:"Type"`
	Action   string     `json:"Action"`
	Actor    eventActor `json:"Actor"`
	Scope    string     `json:"scope"`
	Time     int64      `json:"time"`
	TimeNano int64      `json:"timeNano"`
}

type eventActor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

// containerEvent builds a container lifecycle event (start/die/stop/destroy)
// for rec. Attributes carry the container's labels plus image and name — the
// devcontainer CLI matches its `docker events` stream against the
// devcontainer.local_folder/.config_file labels to detect its container's
// start, so the labels must be present.
func containerEvent(action string, rec *containerRecord) eventMessage {
	attrs := map[string]string{
		"image": rec.spec.Image,
		"name":  rec.deployment,
	}
	for k, v := range rec.req.Labels {
		attrs[k] = v
	}
	now := time.Now()
	return eventMessage{
		Status:   action,
		ID:       rec.id,
		From:     rec.spec.Image,
		Type:     "container",
		Action:   action,
		Actor:    eventActor{ID: rec.id, Attributes: attrs},
		Scope:    "local",
		Time:     now.Unix(),
		TimeNano: now.UnixNano(),
	}
}

// eventHub fans published events out to the open GET /events streams.
type eventHub struct {
	mu   sync.Mutex
	subs map[chan eventMessage]struct{}
}

func newEventHub() *eventHub { return &eventHub{subs: map[chan eventMessage]struct{}{}} }

func (h *eventHub) subscribe() chan eventMessage {
	ch := make(chan eventMessage, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) unsubscribe(ch chan eventMessage) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// publish delivers m to every subscriber without blocking: a subscriber that
// stopped draining (its HTTP write stalled) just misses events.
func (h *eventHub) publish(m eventMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

// handleEvents streams container lifecycle events as NDJSON until the client
// disconnects, honoring the filter keys clients actually send (event, type,
// label, container); unknown filter keys are ignored. compose opens an events
// watcher during up/down; the devcontainer CLI blocks on `docker events
// --filter event=start` to learn that the container it just ran is up.
func (p *Proxy) handleEvents(w http.ResponseWriter, r *http.Request) {
	filters := parseEventFilters(r.URL.Query().Get("filters"))
	ch := p.hub.subscribe()
	defer p.hub.unsubscribe(ch)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	flush()

	enc := json.NewEncoder(w)
	for {
		select {
		case m := <-ch:
			if !matchesEventFilters(m, filters) {
				continue
			}
			if err := enc.Encode(m); err != nil {
				return
			}
			flush()
		case <-r.Context().Done():
			return
		}
	}
}

// parseEventFilters decodes Docker's `filters` query param into key -> set of
// values. Modern clients send the map form {"event":{"start":true}}; older
// ones the list form {"event":["start"]}. Both are honored, like
// parseLabelFilters.
func parseEventFilters(raw string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	if raw == "" {
		return out
	}
	add := func(key, val string) {
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		out[key][val] = true
	}
	var mapForm map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &mapForm); err == nil {
		for key, vals := range mapForm {
			for val, on := range vals {
				if on {
					add(key, val)
				}
			}
		}
		return out
	}
	var listForm map[string][]string
	if err := json.Unmarshal([]byte(raw), &listForm); err == nil {
		for key, vals := range listForm {
			for _, val := range vals {
				add(key, val)
			}
		}
	}
	return out
}

// matchesEventFilters applies the filter keys the proxy understands; a key
// with no values (or an unknown key) does not constrain the stream.
func matchesEventFilters(m eventMessage, filters map[string]map[string]bool) bool {
	if vals := filters["event"]; len(vals) > 0 && !vals[m.Action] {
		return false
	}
	if vals := filters["type"]; len(vals) > 0 && !vals[m.Type] {
		return false
	}
	if vals := filters["container"]; len(vals) > 0 && !vals[m.ID] && !vals[m.Actor.Attributes["name"]] {
		return false
	}
	for l := range filters["label"] {
		k, v, hasV := strings.Cut(l, "=")
		got, ok := m.Actor.Attributes[k]
		if !ok || (hasV && got != v) {
			return false
		}
	}
	return true
}
