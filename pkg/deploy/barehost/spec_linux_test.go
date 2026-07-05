//go:build linux

package barehost

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"cornus/pkg/api"
)

// seedImageConfig writes an OCI image config blob into a fresh on-disk content
// store and returns an ociImage pointing at it. No daemon, no root — this is the
// exact wrapper buildSpec consumes in production, so it proves the lifted spec
// generation works in-process.
func seedImageConfig(t *testing.T, cfg ocispec.ImageConfig) ociImage {
	t.Helper()
	store, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("content store: %v", err)
	}
	img := ocispec.Image{
		Platform: ocispec.Platform{Architecture: "amd64", OS: "linux"},
		Config:   cfg,
		RootFS:   ocispec.RootFS{Type: "layers"},
	}
	blob, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("marshal image config: %v", err)
	}
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(blob),
		Size:      int64(len(blob)),
	}
	if err := content.WriteBlob(t.Context(), store, "config-"+desc.Digest.String(), bytes.NewReader(blob), desc); err != nil {
		t.Fatalf("write config blob: %v", err)
	}
	return ociImage{store: store, config: desc}
}

func TestBuildSpecImageDefaults(t *testing.T) {
	img := seedImageConfig(t, ocispec.ImageConfig{
		Env:        []string{"PATH=/usr/bin", "FOO=bar"},
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "echo hi"},
		WorkingDir: "/app",
	})
	spec := api.DeploySpec{Name: "web"}
	s, err := buildSpec(t.Context(), "cornus-web-0", spec, img, t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	// No entrypoint/command override → args are the image entrypoint + cmd.
	if got := s.Process.Args; len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-c" || got[2] != "echo hi" {
		t.Errorf("Process.Args = %v, want [/bin/sh -c echo hi]", got)
	}
	if s.Process.Cwd != "/app" {
		t.Errorf("Process.Cwd = %q, want /app", s.Process.Cwd)
	}
	if s.Hostname != "cornus-web-0" {
		t.Errorf("Hostname = %q, want cornus-web-0", s.Hostname)
	}
	assertEnv(t, s.Process.Env, "FOO=bar")
}

func TestBuildSpecCommandOverride(t *testing.T) {
	img := seedImageConfig(t, ocispec.ImageConfig{
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "original"},
	})
	// spec.Command replaces the image CMD but keeps the image entrypoint (docker
	// semantics), and spec.Env / Hostname / cgroup pin are applied.
	spec := api.DeploySpec{
		Name:     "web",
		Command:  []string{"my-arg"},
		Env:      map[string]string{"K": "V"},
		Hostname: "custom-host",
	}
	s, err := buildSpec(t.Context(), "cornus-web-0", spec, img, t.TempDir(), "", cgroupsPath("cornus-web-0", false), nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if got := s.Process.Args; len(got) != 2 || got[0] != "/bin/sh" || got[1] != "my-arg" {
		t.Errorf("Process.Args = %v, want [/bin/sh my-arg]", got)
	}
	if s.Hostname != "custom-host" {
		t.Errorf("Hostname = %q, want custom-host", s.Hostname)
	}
	if s.Linux == nil || s.Linux.CgroupsPath != "/cornus/cornus-web-0" {
		t.Errorf("CgroupsPath = %v, want /cornus/cornus-web-0", s.Linux)
	}
	assertEnv(t, s.Process.Env, "K=V")
}

func TestBuildSpecEntrypointOverride(t *testing.T) {
	img := seedImageConfig(t, ocispec.ImageConfig{
		Entrypoint: []string{"/original"},
		Cmd:        []string{"drop-me"},
	})
	spec := api.DeploySpec{
		Name:       "web",
		Entrypoint: []string{"/custom"},
		Command:    []string{"a", "b"},
	}
	s, err := buildSpec(t.Context(), "cornus-web-0", spec, img, t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	// Explicit entrypoint replaces the image's and drops the image CMD.
	want := []string{"/custom", "a", "b"}
	if got := s.Process.Args; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Process.Args = %v, want %v", got, want)
	}
}

func TestCgroupsPath(t *testing.T) {
	if got := cgroupsPath("cornus-web-0", true); got != "cornus.slice:cornus:cornus-web-0" {
		t.Errorf("systemd cgroupsPath = %q", got)
	}
	if got := cgroupsPath("cornus-web-0", false); got != "/cornus/cornus-web-0" {
		t.Errorf("cgroupfs cgroupsPath = %q", got)
	}
}

func assertEnv(t *testing.T, env []string, want string) {
	t.Helper()
	for _, e := range env {
		if e == want {
			return
		}
	}
	t.Errorf("env %v missing %q", env, want)
}
