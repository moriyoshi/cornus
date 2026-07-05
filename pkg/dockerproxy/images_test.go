package dockerproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	cornusregistry "cornus/pkg/registry"
	"cornus/pkg/storage"
)

// TestImageInspectRealConfig pushes an image with a known config to an
// in-process cornus registry and confirms GET /images/{ref}/json surfaces the
// real Config (Entrypoint/Cmd/Env/User/Labels) — the devcontainer CLI derives
// the container's effective command and the devcontainer.metadata label from it.
func TestImageInspectRealConfig(t *testing.T) {
	st, err := storage.Open(context.Background(), "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()
	mux := http.NewServeMux()
	cornusregistry.New(st).Register(mux)
	reg := httptest.NewServer(mux)
	defer reg.Close()

	base, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	img, err := mutate.Config(base, v1.Config{
		Entrypoint: []string{"/entry"},
		Cmd:        []string{"serve", "--x"},
		Env:        []string{"A=1"},
		User:       "1000",
		WorkingDir: "/srv",
		Labels:     map[string]string{"devcontainer.metadata": "[]"},
	})
	if err != nil {
		t.Fatalf("mutate.Config: %v", err)
	}
	refStr := strings.TrimPrefix(reg.URL, "http://") + "/app:v1"
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("push: %v", err)
	}

	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()
	resp := do(t, http.MethodGet, srv.URL+"/images/"+refStr+"/json", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inspect status = %d", resp.StatusCode)
	}
	var body struct {
		ID     string `json:"Id"`
		Config struct {
			Entrypoint []string
			Cmd        []string
			Env        []string
			User       string
			WorkingDir string
			Labels     map[string]string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantID, _ := img.ConfigName()
	if body.ID != wantID.String() {
		t.Errorf("Id = %q, want config digest %q", body.ID, wantID)
	}
	c := body.Config
	if len(c.Entrypoint) != 1 || c.Entrypoint[0] != "/entry" {
		t.Errorf("Config.Entrypoint = %v", c.Entrypoint)
	}
	if len(c.Cmd) != 2 || c.Cmd[0] != "serve" {
		t.Errorf("Config.Cmd = %v", c.Cmd)
	}
	if len(c.Env) != 1 || c.Env[0] != "A=1" {
		t.Errorf("Config.Env = %v", c.Env)
	}
	if c.User != "1000" || c.WorkingDir != "/srv" {
		t.Errorf("Config.User/WorkingDir = %q/%q", c.User, c.WorkingDir)
	}
	if c.Labels["devcontainer.metadata"] != "[]" {
		t.Errorf("Config.Labels = %v", c.Labels)
	}
}

// TestImageInspectFallback confirms an unresolvable ref still gets the
// synthetic present-looking image (empty Config), preserving the offline
// compose behavior instead of a fatal "no such image".
func TestImageInspectFallback(t *testing.T) {
	srv := httptest.NewServer(New(&fakeAttacher{}).Handler())
	defer srv.Close()
	resp := do(t, http.MethodGet, srv.URL+"/images/no.such.registry.invalid/app:v1/json", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inspect status = %d, want 200 synthetic fallback", resp.StatusCode)
	}
	var body struct {
		ID     string         `json:"Id"`
		Config map[string]any `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID == "" || len(body.Config) != 0 {
		t.Fatalf("fallback body = %+v, want synthetic id and empty Config", body)
	}
}
