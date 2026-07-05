package dockerproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// networkStore fakes Docker networks. cornus doesn't model container networks
// (the deploy backend owns connectivity), but compose creates and attaches a
// `<project>_default` network, so the proxy must accept and echo them.
type networkStore struct {
	mu     sync.Mutex
	byName map[string]*fakeNetwork
	byID   map[string]*fakeNetwork
}

type fakeNetwork struct {
	ID     string
	Name   string
	Driver string
	Labels map[string]string
}

func newNetworkStore() *networkStore {
	return &networkStore{byName: map[string]*fakeNetwork{}, byID: map[string]*fakeNetwork{}}
}

func (s *networkStore) create(name, driver string, labels map[string]string) *fakeNetwork {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.byName[name]; ok {
		// Docker semantics: labels are create-time-only; a create that reuses an
		// existing network does not merge or replace them.
		return n
	}
	if driver == "" {
		driver = "bridge"
	}
	if labels == nil {
		labels = map[string]string{}
	}
	n := &fakeNetwork{ID: newID(), Name: name, Driver: driver, Labels: labels}
	s.byName[name] = n
	s.byID[n.ID] = n
	return n
}

func (s *networkStore) get(ref string) *fakeNetwork {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.byID[ref]; ok {
		return n
	}
	return s.byName[ref]
}

func (s *networkStore) del(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.byID[ref]
	if n == nil {
		n = s.byName[ref]
	}
	if n != nil {
		delete(s.byID, n.ID)
		delete(s.byName, n.Name)
	}
}

func (s *networkStore) all() []*fakeNetwork {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*fakeNetwork, 0, len(s.byID))
	for _, n := range s.byID {
		out = append(out, n)
	}
	return out
}

// networkJSON renders a network for both GET /networks (list) and
// GET /networks/{id} (inspect); docker uses the same shape for both, with
// Labels top-level. Compose's reconverge refuses to reuse a network whose
// com.docker.compose.* labels don't match, so the create-time labels must echo.
func networkJSON(n *fakeNetwork) map[string]any {
	labels := n.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return map[string]any{
		"Name":       n.Name,
		"Id":         n.ID,
		"Driver":     n.Driver,
		"Scope":      "local",
		"IPAM":       map[string]any{"Driver": "default", "Config": []any{}},
		"Containers": map[string]any{},
		"Options":    map[string]any{},
		"Labels":     labels,
	}
}

func (p *Proxy) handleNetworkCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string            `json:"Name"`
		Driver string            `json:"Driver"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dockerError(w, http.StatusBadRequest, "invalid network create body")
		return
	}
	n := p.networks.create(req.Name, req.Driver, req.Labels)
	writeJSON(w, http.StatusCreated, map[string]string{"Id": n.ID, "Warning": ""})
}

func (p *Proxy) handleNetworkList(w http.ResponseWriter, r *http.Request) {
	// Optional name filter: {"name":{"proj_default":true}}.
	want := networkNameFilter(r.URL.Query().Get("filters"))
	out := make([]map[string]any, 0)
	for _, n := range p.networks.all() {
		if want != "" && n.Name != want {
			continue
		}
		out = append(out, networkJSON(n))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleNetworkPrune serves POST /networks/prune (docker network prune, also
// reached via `docker system prune`). It is registered ahead of the /networks/
// catch-all so the request does not fall into handleNetworkItem (which would 405
// on POST). cornus's networks are fakes with no usage tracking, so nothing is
// reclaimed; returning the empty prune shape honors the "prune succeeds"
// contract.
func (p *Proxy) handleNetworkPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"NetworksDeleted": []string{}})
}

func (p *Proxy) handleNetworkItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/networks/")
	ref, action, _ := strings.Cut(rest, "/")
	if ref == "" {
		dockerError(w, http.StatusBadRequest, "missing network id")
		return
	}
	// compose attaches/detaches containers; cornus doesn't model networks, so
	// connect/disconnect are accepted as no-ops.
	if (action == "connect" || action == "disconnect") && r.Method == http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	switch r.Method {
	case http.MethodGet:
		n := p.networks.get(ref)
		if n == nil {
			dockerError(w, http.StatusNotFound, "no such network: "+ref)
			return
		}
		writeJSON(w, http.StatusOK, networkJSON(n))
	case http.MethodDelete:
		p.networks.del(ref)
		w.WriteHeader(http.StatusNoContent)
	default:
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func networkNameFilter(raw string) string {
	if raw == "" {
		return ""
	}
	var f map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return ""
	}
	for name := range f["name"] {
		return name
	}
	return ""
}
