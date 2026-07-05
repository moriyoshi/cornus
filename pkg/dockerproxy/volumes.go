package dockerproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"cornus/pkg/client"
)

// volumeStore tracks the named volumes the proxy has been told about (docker
// volume create, compose's project volumes) so list and inspect can answer for
// them.
//
// The store is only the NAME index — the data lives in the deploy backend:
// toDeploySpec (translate.go) turns a container's named volumes into
// DeploySpec.Volumes and the backend provisions real, persistent storage for
// them. So removal must reach the backend (handleVolumeItem / handleVolumePrune
// call deployAttacher.DeleteVolume); dropping the local map entry alone would
// report a removal while the data survives.
type volumeStore struct {
	mu     sync.Mutex
	byName map[string]bool
}

func newVolumeStore() *volumeStore { return &volumeStore{byName: map[string]bool{}} }

func (s *volumeStore) add(name string) {
	s.mu.Lock()
	s.byName[name] = true
	s.mu.Unlock()
}

// forget drops the local name entry. Only called once the backend has actually
// removed the volume.
func (s *volumeStore) forget(name string) {
	s.mu.Lock()
	delete(s.byName, name)
	s.mu.Unlock()
}

// names returns the tracked volume names, sorted for a deterministic prune order
// and prune report.
func (s *volumeStore) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.byName))
	for name := range s.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

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
	p.volumes.add(req.Name)
	writeJSON(w, http.StatusCreated, volumeJSON(req.Name))
}

func (p *Proxy) handleVolumeList(w http.ResponseWriter, _ *http.Request) {
	names := p.volumes.names()
	vols := make([]map[string]any, 0, len(names))
	for _, name := range names {
		vols = append(vols, volumeJSON(name))
	}
	writeJSON(w, http.StatusOK, map[string]any{"Volumes": vols, "Warnings": []string{}})
}

// volumeUsers returns the ids of the containers that reference the named volume,
// sorted. Docker's rule for `volume rm` and `volume prune` counts EVERY container
// holding the volume, running or not — only `docker rm` releases the reference —
// so this scans the whole registry, not just running records. A record's spec is
// fixed at create time (containers are immutable), so it is safe to read without
// the record lock.
func (p *Proxy) volumeUsers(name string) []string {
	var ids []string
	for _, rec := range p.reg.all() {
		for _, v := range rec.spec.Volumes {
			if v.Name == name {
				ids = append(ids, rec.id)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// removeVolume removes the volume from the deploy backend and, only once that
// succeeded, forgets the local name. The backend is delete-if-exists, so a name
// the store never saw (the proxy's index is in-memory and does not survive an
// agent restart, and a `docker run -v name:/path` creates a volume the store was
// never told about) is still removed rather than silently left behind.
func (p *Proxy) removeVolume(ctx context.Context, name string) error {
	if err := p.attacher.DeleteVolume(ctx, name); err != nil {
		return err
	}
	p.volumes.forget(name)
	return nil
}

// writeVolumeRemovalError shapes a failed backend removal as a Docker error. A
// backend without the volume-removal capability answers 501 Not Implemented, so
// the docker CLI prints that removing volumes is unsupported here; anything else
// is a 500 carrying the backend's message. Never a 2xx: reporting success while
// the persistent data survives is exactly the failure mode this guards.
func writeVolumeRemovalError(w http.ResponseWriter, name string, err error) {
	if errors.Is(err, client.ErrVolumeRemovalUnsupported) {
		dockerError(w, http.StatusNotImplemented,
			"remove "+name+": this cornus deploy backend does not support removing volumes")
		return
	}
	dockerError(w, http.StatusInternalServerError, "remove "+name+": "+err.Error())
}

// pruneResult renders Docker's prune response shape. SpaceReclaimed is reported
// as 0 because cornus's backends do not report a removed volume's size — the
// names in VolumesDeleted are the truthful part of the answer.
func pruneResult(deleted []string) map[string]any {
	if deleted == nil {
		deleted = []string{}
	}
	return map[string]any{"VolumesDeleted": deleted, "SpaceReclaimed": 0}
}

// handleVolumePrune serves POST /volumes/prune (docker volume prune, also
// reached via `docker system prune --volumes`). It is registered ahead of the
// /volumes/ catch-all so the request does not fall into handleVolumeItem (which
// would 405 on POST).
//
// Semantics follow the Engine API the proxy advertises (1.43): since 1.42 prune
// reclaims only ANONYMOUS volumes unless the `all` filter is set, and named
// volumes — the only kind the proxy tracks, since compose and `-v name:/path`
// both name theirs — are kept. So without `all` nothing is reclaimed, and with
// it every tracked volume no container references is removed FROM THE BACKEND
// and reported. A removal that fails aborts with the error; volumes already
// removed stay removed (Docker's prune shape has no partial-failure form).
func (p *Proxy) handleVolumePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !pruneAllFilter(r.URL.Query().Get("filters")) {
		writeJSON(w, http.StatusOK, pruneResult(nil))
		return
	}
	var deleted []string
	for _, name := range p.volumes.names() {
		if len(p.volumeUsers(name)) > 0 {
			continue
		}
		if err := p.removeVolume(r.Context(), name); err != nil {
			writeVolumeRemovalError(w, name, err)
			return
		}
		deleted = append(deleted, name)
	}
	writeJSON(w, http.StatusOK, pruneResult(deleted))
}

// pruneAllFilter reports whether the prune request carries Docker's `all` filter
// (`docker volume prune --all`), which extends prune from anonymous volumes to
// every unused one. Both filter encodings are accepted (parseEventFilters), as
// is the "1" spelling of true.
func pruneAllFilter(raw string) bool {
	vals := parseEventFilters(raw)["all"]
	return vals["true"] || vals["1"]
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
		// Docker refuses to remove a volume a container still references, with
		// 409 Conflict naming the holders (`docker volume rm -f` only suppresses
		// "no such volume", never this). The alternative — removing storage out
		// from under a live container — is why this is a hard conflict.
		if users := p.volumeUsers(name); len(users) > 0 {
			dockerError(w, http.StatusConflict,
				"remove "+name+": volume is in use - ["+strings.Join(users, ", ")+"]")
			return
		}
		if err := p.removeVolume(r.Context(), name); err != nil {
			writeVolumeRemovalError(w, name, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		dockerError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
