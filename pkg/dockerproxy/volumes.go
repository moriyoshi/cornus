package dockerproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// volumeStore fakes named volumes so compose's volume create/list/prune calls
// succeed. cornus's first slice only wires bind mounts through 9P; named
// volumes are accepted but not backed (deferred).
type volumeStore struct {
	mu     sync.Mutex
	byName map[string]bool
}

func newVolumeStore() *volumeStore { return &volumeStore{byName: map[string]bool{}} }

func volumeJSON(name string) map[string]any {
	return map[string]any{
		"Name":       name,
		"Driver":     "local",
		"Mountpoint": "/var/lib/cornus/volumes/" + name,
		"Scope":      "local",
		"Labels":     map[string]any{},
		"Options":    map[string]any{},
	}
}

func (p *Proxy) handleVolumeCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dockerError(w, http.StatusBadRequest, "invalid volume create body")
		return
	}
	p.volumes.mu.Lock()
	p.volumes.byName[req.Name] = true
	p.volumes.mu.Unlock()
	writeJSON(w, http.StatusCreated, volumeJSON(req.Name))
}

func (p *Proxy) handleVolumeList(w http.ResponseWriter, _ *http.Request) {
	p.volumes.mu.Lock()
	vols := make([]map[string]any, 0, len(p.volumes.byName))
	for name := range p.volumes.byName {
		vols = append(vols, volumeJSON(name))
	}
	p.volumes.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"Volumes": vols, "Warnings": []string{}})
}

// handleVolumePrune serves POST /volumes/prune (docker volume prune, also
// reached via `docker system prune`). It is registered ahead of the /volumes/
// catch-all so the request does not fall into handleVolumeItem (which would 405
// on POST). cornus's named volumes are unbacked, so there is nothing to reclaim;
// returning the empty prune shape honors the "prune succeeds" contract.
func (p *Proxy) handleVolumePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"VolumesDeleted": []string{}, "SpaceReclaimed": 0})
}

func (p *Proxy) handleVolumeItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/volumes/")
	if name == "" {
		dockerError(w, http.StatusBadRequest, "missing volume name")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, volumeJSON(name))
	case http.MethodDelete:
		p.volumes.mu.Lock()
		delete(p.volumes.byName, name)
		p.volumes.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
