package dockerproxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"cornus/pkg/client"
)

// allFilter is what `docker volume prune --all` sends: the filter that extends
// prune from anonymous volumes to every unused one.
const allFilter = `{"all":{"true":true}}`

// createVolume registers a named volume with the proxy the way compose does.
func createVolume(t *testing.T, srvURL, name string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"Name": name})
	resp := do(t, http.MethodPost, srvURL+"/volumes/create", b)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("volume create %s = %d", name, resp.StatusCode)
	}
}

// createContainerWithVolume creates (but does not start) a container mounting
// the named volume, so the proxy records a holder of it.
func createContainerWithVolume(t *testing.T, srvURL, name, volume string) string {
	t.Helper()
	b, _ := json.Marshal(createRequest{
		Image:      "img",
		HostConfig: hostConfig{Binds: []string{volume + ":/var/lib"}},
	})
	resp := do(t, http.MethodPost, srvURL+"/containers/create?name="+name, b)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("container create = %d", resp.StatusCode)
	}
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	if cr.ID == "" {
		t.Fatal("no container id")
	}
	return cr.ID
}

func volumeNames(t *testing.T, srvURL string) []string {
	t.Helper()
	resp := do(t, http.MethodGet, srvURL+"/volumes", nil)
	defer resp.Body.Close()
	var out struct{ Volumes []struct{ Name string } }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode volume list: %v", err)
	}
	names := make([]string, 0, len(out.Volumes))
	for _, v := range out.Volumes {
		names = append(names, v.Name)
	}
	slices.Sort(names)
	return names
}

func dockerMessage(t *testing.T, resp *http.Response) string {
	t.Helper()
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode docker error body: %v", err)
	}
	return body.Message
}

// TestVolumeRemoveReachesBackend is the regression guard for the silent-success
// bug: `docker volume rm` used to drop the proxy's in-memory name and answer 204
// while the backend volume — real, persistent storage, since toDeploySpec turns
// named volumes into DeploySpec.Volumes — survived. The delete must reach the
// backend, and only then may the name disappear.
func TestVolumeRemoveReachesBackend(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	createVolume(t, srv.URL, "proj_data")

	resp := do(t, http.MethodDelete, srv.URL+"/v1.43/volumes/proj_data", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("volume rm status = %d, want 204", resp.StatusCode)
	}
	if got := fa.volumesDeleted(); !slices.Equal(got, []string{"proj_data"}) {
		t.Fatalf("backend removals = %v, want [proj_data]", got)
	}
	if got := volumeNames(t, srv.URL); len(got) != 0 {
		t.Fatalf("volume list after rm = %v, want empty", got)
	}
}

// TestVolumeRemoveInUse covers Docker's conflict rule: a volume a container still
// references cannot be removed (409), and no backend removal is attempted. Once
// the container is gone the same request succeeds.
func TestVolumeRemoveInUse(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	createVolume(t, srv.URL, "proj_data")
	id := createContainerWithVolume(t, srv.URL, "web", "proj_data")

	resp := do(t, http.MethodDelete, srv.URL+"/volumes/proj_data", nil)
	if resp.StatusCode != http.StatusConflict {
		resp.Body.Close()
		t.Fatalf("volume rm in use status = %d, want 409", resp.StatusCode)
	}
	msg := dockerMessage(t, resp)
	resp.Body.Close()
	if !strings.Contains(msg, "volume is in use") || !strings.Contains(msg, id) {
		t.Fatalf("conflict message = %q, want it to name the holding container", msg)
	}
	if got := fa.volumesDeleted(); len(got) != 0 {
		t.Fatalf("backend removals = %v, want none for an in-use volume", got)
	}
	if got := volumeNames(t, srv.URL); !slices.Equal(got, []string{"proj_data"}) {
		t.Fatalf("volume list = %v, want the volume kept", got)
	}

	// docker rm releases the reference; the volume is then removable.
	resp = do(t, http.MethodDelete, srv.URL+"/containers/"+id, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("container rm = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodDelete, srv.URL+"/volumes/proj_data", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("volume rm after container rm = %d, want 204", resp.StatusCode)
	}
	if got := fa.volumesDeleted(); !slices.Equal(got, []string{"proj_data"}) {
		t.Fatalf("backend removals = %v, want [proj_data]", got)
	}
}

// TestVolumePruneRemovesUnused covers the prune contract: with Docker's `all`
// filter every unused volume is removed FROM THE BACKEND and reported in
// VolumesDeleted, while an in-use one is kept. Without the filter prune reclaims
// only anonymous volumes (API >= 1.42), of which the proxy tracks none, so it
// removes nothing — and must not claim otherwise.
func TestVolumePruneRemovesUnused(t *testing.T) {
	fa := &fakeAttacher{}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	createVolume(t, srv.URL, "proj_cache")
	createVolume(t, srv.URL, "proj_data")
	createContainerWithVolume(t, srv.URL, "web", "proj_data")

	// Default prune: named volumes are out of scope, so nothing is reclaimed.
	resp := do(t, http.MethodPost, srv.URL+"/volumes/prune", []byte(`{}`))
	out := decodePrune(t, resp)
	if len(out.VolumesDeleted) != 0 || len(fa.volumesDeleted()) != 0 {
		t.Fatalf("default prune deleted %v (backend %v), want nothing", out.VolumesDeleted, fa.volumesDeleted())
	}

	// `docker volume prune --all`: the unused named volume goes, the in-use one stays.
	resp = do(t, http.MethodPost, srv.URL+"/v1.43/volumes/prune?filters="+url.QueryEscape(allFilter), []byte(`{}`))
	out = decodePrune(t, resp)
	if !slices.Equal(out.VolumesDeleted, []string{"proj_cache"}) {
		t.Fatalf("VolumesDeleted = %v, want [proj_cache]", out.VolumesDeleted)
	}
	if got := fa.volumesDeleted(); !slices.Equal(got, []string{"proj_cache"}) {
		t.Fatalf("backend removals = %v, want [proj_cache]", got)
	}
	if got := volumeNames(t, srv.URL); !slices.Equal(got, []string{"proj_data"}) {
		t.Fatalf("volume list after prune = %v, want the in-use volume kept", got)
	}
}

type pruneResponse struct {
	VolumesDeleted []string
	SpaceReclaimed int
}

func decodePrune(t *testing.T, resp *http.Response) pruneResponse {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prune status = %d, want 200", resp.StatusCode)
	}
	var out pruneResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode prune body: %v", err)
	}
	if out.VolumesDeleted == nil {
		t.Fatal("VolumesDeleted = nil, want []")
	}
	return out
}

// TestVolumeRemoveUnsupportedBackend covers a deploy backend that cannot remove
// volumes (the server answers 501 and the client reports
// ErrVolumeRemovalUnsupported): both rm and prune must surface that, keeping the
// volume, instead of reporting a removal that did not happen.
func TestVolumeRemoveUnsupportedBackend(t *testing.T) {
	fa := &fakeAttacher{volumeErr: client.ErrVolumeRemovalUnsupported}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	createVolume(t, srv.URL, "proj_data")

	resp := do(t, http.MethodDelete, srv.URL+"/volumes/proj_data", nil)
	if resp.StatusCode != http.StatusNotImplemented {
		resp.Body.Close()
		t.Fatalf("volume rm status = %d, want 501", resp.StatusCode)
	}
	msg := dockerMessage(t, resp)
	resp.Body.Close()
	if !strings.Contains(msg, "does not support removing volumes") {
		t.Fatalf("message = %q, want an explicit unsupported error", msg)
	}

	resp = do(t, http.MethodPost, srv.URL+"/volumes/prune?filters="+url.QueryEscape(allFilter), []byte(`{}`))
	code := resp.StatusCode
	resp.Body.Close()
	if code != http.StatusNotImplemented {
		t.Fatalf("prune status = %d, want 501", code)
	}
	if got := volumeNames(t, srv.URL); !slices.Equal(got, []string{"proj_data"}) {
		t.Fatalf("volume list = %v, want the volume kept when removal is unsupported", got)
	}
}

// TestVolumeRemoveBackendError covers a backend removal that fails for any other
// reason: the failure is reported (500) and the volume stays listed.
func TestVolumeRemoveBackendError(t *testing.T) {
	fa := &fakeAttacher{volumeErr: errors.New("boom")}
	srv := httptest.NewServer(New(fa).Handler())
	defer srv.Close()

	createVolume(t, srv.URL, "proj_data")

	resp := do(t, http.MethodDelete, srv.URL+"/volumes/proj_data", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		resp.Body.Close()
		t.Fatalf("volume rm status = %d, want 500", resp.StatusCode)
	}
	msg := dockerMessage(t, resp)
	resp.Body.Close()
	if !strings.Contains(msg, "boom") {
		t.Fatalf("message = %q, want the backend error", msg)
	}
	if got := volumeNames(t, srv.URL); !slices.Equal(got, []string{"proj_data"}) {
		t.Fatalf("volume list = %v, want the volume kept after a failed removal", got)
	}
}
