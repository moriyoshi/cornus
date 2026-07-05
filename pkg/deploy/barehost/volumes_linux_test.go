//go:build linux

package barehost

import (
	"os"
	"testing"

	"cornus/pkg/api"
)

func TestInstanceMountsBindsAndVolumes(t *testing.T) {
	b, _ := newTestBackend(t)
	spec := api.DeploySpec{
		Name:   "web",
		Mounts: []api.Mount{{Source: "/host/src", Target: "/app", ReadOnly: true}},
		Volumes: []api.VolumeSpec{
			{Name: "shared", Target: "/data"}, // named
			{Target: "/cache"},                // anonymous
		},
	}
	mounts, vols, err := b.instanceMounts(spec, 0)
	if err != nil {
		t.Fatalf("instanceMounts: %v", err)
	}
	// 1 host bind + 2 volume binds.
	if len(mounts) != 3 {
		t.Fatalf("mounts = %d, want 3", len(mounts))
	}
	if mounts[0].Source != "/host/src" || mounts[0].Destination != "/app" {
		t.Errorf("host bind = %+v", mounts[0])
	}
	// The read-only host bind must carry "ro".
	if !hasOpt(mounts[0].Options, "ro") {
		t.Errorf("read-only bind missing ro option: %v", mounts[0].Options)
	}
	if len(vols) != 2 {
		t.Fatalf("volume backings = %d, want 2", len(vols))
	}
	// The named volume's backing directory is shared/stable; the anon one is
	// namespaced under the app. Both must exist after resolution.
	named := b.namedVolumeDir("shared")
	if st, err := os.Stat(named); err != nil || !st.IsDir() {
		t.Errorf("named volume dir not created: %v", err)
	}
	for _, v := range vols {
		if st, err := os.Stat(v.HostDir); err != nil || !st.IsDir() {
			t.Errorf("volume backing %q not created: %v", v.HostDir, err)
		}
	}
}

func TestVolumeRequiresTarget(t *testing.T) {
	b, _ := newTestBackend(t)
	_, _, err := b.instanceMounts(api.DeploySpec{Name: "x", Volumes: []api.VolumeSpec{{Name: "v"}}}, 0)
	if err == nil {
		t.Fatal("volume without a target: want error")
	}
}

func TestRemoveVolumeDeleteIfExists(t *testing.T) {
	b, _ := newTestBackend(t)
	dir := b.namedVolumeDir("gone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := b.RemoveVolume(t.Context(), "gone"); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("volume dir should be gone, stat err = %v", err)
	}
	// Removing a missing volume is a no-op success.
	if err := b.RemoveVolume(t.Context(), "never"); err != nil {
		t.Errorf("RemoveVolume(missing) = %v, want nil", err)
	}
}

func hasOpt(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}
