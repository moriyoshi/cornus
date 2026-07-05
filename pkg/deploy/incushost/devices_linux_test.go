//go:build linux

package incushost

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/remotecompanion"
)

// discardLogger is for the pure device mappers, whose refusals are asserted
// through the mapper's own return value rather than through the log.
func discardLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.DiscardHandler)
}

// TestBuildDevicesMapsEveryFilesystemTheSpecAsksFor is the regression test for a
// silent-drop this backend used to have three of: `volumes:` host binds,
// `tmpfs:` and `shm_size` were all accepted and then discarded without a word,
// so a workload deployed with a bind mount ran against an EMPTY directory in its
// own root disk and nothing said so.
//
// All three are the same incus primitive, a `disk` device
// (internal/server/device/disk.go): a non-pool `source` is a plain bind
// (disk.go:1109-1175 builds bind/ro from `readonly`), and the magic source
// "tmpfs:" is a tmpfs whose `size` becomes the mount's size= option
// (disk.go:954-1064).
func TestBuildDevicesMapsEveryFilesystemTheSpecAsksFor(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}},
		Mounts: []api.Mount{
			{Source: "/srv/data", Target: "/data"},
			{Source: "/srv/conf", Target: "/etc/app", ReadOnly: true},
		},
		Tmpfs:   []string{"/run", "/tmp:size=64m"},
		ShmSize: 128 << 20,
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	want := map[string]map[string]string{
		"cornus-port-0":  {"type": "proxy", "listen": "tcp:0.0.0.0:8080", "connect": "tcp:127.0.0.1:80", "bind": "host"},
		"cornus-mount-0": {"type": "disk", "source": "/srv/data", "path": "/data"},
		"cornus-mount-1": {"type": "disk", "source": "/srv/conf", "path": "/etc/app", "readonly": "true"},
		"cornus-tmpfs-0": {"type": "disk", "source": "tmpfs:", "path": "/run"},
		"cornus-tmpfs-1": {"type": "disk", "source": "tmpfs:", "path": "/tmp", "size": "67108864"},
		"cornus-shm":     {"type": "disk", "source": "tmpfs:", "path": "/dev/shm", "size": "134217728"},
	}
	if !reflect.DeepEqual(post.Devices, want) {
		t.Errorf("devices =\n%#v\nwant\n%#v", post.Devices, want)
	}
}

// TestBuildDevicesLeavesAWritableMountUnmarked keeps `readonly` opt-in: writing
// it as "false" would be indistinguishable in the device map from a deliberate
// read-only request that was mis-rendered, and incus's default is writable
// already.
func TestBuildDevicesLeavesAWritableMountUnmarked(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1",
		Mounts: []api.Mount{{Source: "/srv/data", Target: "/data"}},
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if v, ok := post.Devices["cornus-mount-0"]["readonly"]; ok {
		t.Errorf("readonly = %q on a writable mount, want absent", v)
	}
}

// TestBuildDevicesDropsACollidingPathInsteadOfFailingTheDeploy pins the
// conflict rule. incusd counts an instance's own disk devices per container path
// and rejects the WHOLE create when two share one ("More than one disk device
// uses the same path", disk.go:485-493) — so a compose file that names the same
// target twice would take down the entire deploy. Dropping the later device with
// a warning keeps the rest of the workload deployable, and the warning is what
// keeps the drop from being silent.
func TestBuildDevicesDropsACollidingPathInsteadOfFailingTheDeploy(t *testing.T) {
	buf := captureLogs(t)
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1",
		Mounts:  []api.Mount{{Source: "/srv/a", Target: "/data"}, {Source: "/srv/b", Target: "/data"}},
		Tmpfs:   []string{"/dev/shm"},
		ShmSize: 1 << 20,
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	// The first claimant of each path wins.
	if got := post.Devices["cornus-mount-0"]["source"]; got != "/srv/a" {
		t.Errorf("first mount on /data = %q, want /srv/a", got)
	}
	if _, ok := post.Devices["cornus-mount-1"]; ok {
		t.Error("the second mount on /data should have been dropped")
	}
	if _, ok := post.Devices["cornus-shm"]; ok {
		t.Error("shm_size should lose to an explicit tmpfs on /dev/shm")
	}
	if n := strings.Count(buf.String(), "ignores a second filesystem on the same container path"); n != 2 {
		t.Errorf("collision warnings = %d, want 2, in:\n%s", n, buf.String())
	}
}

// TestBuildDevicesKeepsTheAgentVolumeAheadOfUserMounts pins the one collision
// whose outcome is not "first entry in the spec wins": ssh-agent forwarding
// breaking silently is worse than one dropped mount, so the remote-mode agent
// volume claims its path before any user mount can.
func TestBuildDevicesKeepsTheAgentVolumeAheadOfUserMounts(t *testing.T) {
	buf := captureLogs(t)
	b := testBackend(newFakeConn())
	b.remote = true
	b.pool = "default"
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1",
		Mounts: []api.Mount{{Source: "/srv/mine", Target: remotecompanion.AgentScratchDir}},
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if got := post.Devices[agentVolumeDevice]["source"]; got != agentVolumeName("web", 0) {
		t.Errorf("agent volume device = %v, want the shared agent volume", post.Devices[agentVolumeDevice])
	}
	if _, ok := post.Devices["cornus-mount-0"]; ok {
		t.Error("a user mount must not displace the agent volume")
	}
	if !strings.Contains(buf.String(), "ignores a second filesystem on the same container path") {
		t.Errorf("the displaced mount was dropped silently:\n%s", buf.String())
	}
}

// TestBuildDevicesReturnsNilWhenThereIsNothingToAttach keeps the create request
// free of an empty device map, which reads as "this instance has devices" to
// anything inspecting it.
func TestBuildDevicesReturnsNilWhenThereIsNothingToAttach(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if post.Devices != nil {
		t.Errorf("devices = %#v, want nil", post.Devices)
	}
}

// TestBindDeviceRefusesWhatIncusWouldReject pins the bind gates against incusd's
// own checks, so a device this backend emits is one the create cannot be
// rejected over: a local source must be absolute (disk.go:495-501) and `path`
// may not be "/", which names the ROOT disk and must carry a pool and no source
// (disk.go:465-471).
func TestBindDeviceRefusesWhatIncusWouldReject(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    api.Mount
		want map[string]string // nil means refused
	}{
		{"an absolute pair maps", api.Mount{Source: "/srv/a", Target: "/data"},
			map[string]string{"type": "disk", "source": "/srv/a", "path": "/data"}},
		{"the target is cleaned so the collision check compares like with like",
			api.Mount{Source: "/srv/a", Target: "/data/"},
			map[string]string{"type": "disk", "source": "/srv/a", "path": "/data"}},
		{"read-only becomes the ro mount option", api.Mount{Source: "/srv/a", Target: "/data", ReadOnly: true},
			map[string]string{"type": "disk", "source": "/srv/a", "path": "/data", "readonly": "true"}},
		{"an empty source is a managed volume, not a bind", api.Mount{Target: "/data"}, nil},
		{"a blank source is the same", api.Mount{Source: "   ", Target: "/data"}, nil},
		{"a bare name would resolve against the daemon's cwd", api.Mount{Source: "cache", Target: "/data"}, nil},
		{"a relative path likewise", api.Mount{Source: "./src", Target: "/data"}, nil},
		{"the root target names the root disk", api.Mount{Source: "/srv/a", Target: "/"}, nil},
		{"a relative target is not a mount point", api.Mount{Source: "/srv/a", Target: "data"}, nil},
		{"an empty target is not a mount point", api.Mount{Source: "/srv/a"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dev, name, ok := bindDevice(context.Background(), discardLogger(t), 0, tc.m)
			if ok != (tc.want != nil) {
				t.Fatalf("bindDevice(%+v) ok = %v, want %v", tc.m, ok, tc.want != nil)
			}
			if !ok {
				return
			}
			if name != "cornus-mount-0" {
				t.Errorf("device name = %q", name)
			}
			if !reflect.DeepEqual(dev, tc.want) {
				t.Errorf("device = %#v, want %#v", dev, tc.want)
			}
		})
	}
}

// TestTmpfsDeviceMapsThePathAndWhatIncusCanExpressOfTheOptions pins the tmpfs
// entry translation. Incus expresses exactly one of a tmpfs's mount options,
// `size` (disk.go:960-968), so the rule is: always mount the tmpfs (a tmpfs at
// the right path with kernel defaults is far closer to the ask than no tmpfs at
// all) and report the options that did not survive.
func TestTmpfsDeviceMapsThePathAndWhatIncusCanExpressOfTheOptions(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  map[string]string // nil means refused
	}{
		{"/run", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run"}},
		{"/run/", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run"}},
		{"/run:size=64m", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run", "size": "67108864"}},
		{"/run:size=1024", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run", "size": "1024"}},
		{"/run:size=64m,mode=1777", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run", "size": "67108864"}},
		{"/run:noexec,nosuid", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run"}},
		// A percent-of-RAM size has no fixed byte count to hand incus, so the tmpfs
		// is mounted with the kernel default rather than a number cornus invented.
		{"/run:size=50%", map[string]string{"type": "disk", "source": "tmpfs:", "path": "/run"}},
		{"run", nil},
		{"/", nil},
		{"", nil},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			dev, name, ok := tmpfsDevice(context.Background(), discardLogger(t), 2, tc.entry)
			if ok != (tc.want != nil) {
				t.Fatalf("tmpfsDevice(%q) ok = %v, want %v", tc.entry, ok, tc.want != nil)
			}
			if !ok {
				return
			}
			if name != "cornus-tmpfs-2" {
				t.Errorf("device name = %q", name)
			}
			if !reflect.DeepEqual(dev, tc.want) {
				t.Errorf("device = %#v, want %#v", dev, tc.want)
			}
		})
	}
}

// TestTmpfsSizeSplitsWhatIncusTakesFromWhatItDrops pins the option parsing on
// its own, including which options are reported as dropped — that report is the
// only thing standing between an unexpressible mount option and a silent change
// in the workload's tmpfs semantics.
func TestTmpfsSizeSplitsWhatIncusTakesFromWhatItDrops(t *testing.T) {
	for _, tc := range []struct {
		opts    string
		size    string
		dropped []string
	}{
		{"", "", nil},
		{"size=64m", "67108864", nil},
		{"size=64M", "67108864", nil},
		{"size=1k", "1024", nil},
		{"size=2g", "2147483648", nil},
		{"size=4096", "4096", nil},
		{"mode=1777", "", []string{"mode=1777"}},
		{"size=64m,mode=1777,noexec", "67108864", []string{"mode=1777", "noexec"}},
		{"size=50%", "", []string{"size=50%"}},
		{"size=", "", []string{"size="}},
		{"size=-1", "", []string{"size=-1"}},
		{"size=abc", "", []string{"size=abc"}},
		{" size=64m , noexec ", "67108864", []string{"noexec"}},
		{",,", "", nil},
		// The last size wins, matching how mount(8) treats a repeated option.
		{"size=1k,size=2k", "2048", nil},
	} {
		t.Run(tc.opts, func(t *testing.T) {
			size, dropped := tmpfsSize(tc.opts)
			if size != tc.size || !reflect.DeepEqual(dropped, tc.dropped) {
				t.Errorf("tmpfsSize(%q) = (%q, %v), want (%q, %v)", tc.opts, size, dropped, tc.size, tc.dropped)
			}
		})
	}
}

// TestParseTmpfsBytesRejectsWhatItCannotRenderExactly pins the size parser,
// including the overflow guard: incus stores the byte count as an int64 too, so
// a value that wraps would silently become a tiny (or negative) tmpfs.
func TestParseTmpfsBytesRejectsWhatItCannotRenderExactly(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1024", 1024, true},
		{"0", 0, true},
		{"1k", 1024, true},
		{"1K", 1024, true},
		{"1m", 1 << 20, true},
		{"1G", 1 << 30, true},
		{" 64m ", 64 << 20, true},
		{"", 0, false},
		{"k", 0, false},
		{"-1", 0, false},
		{"-1m", 0, false},
		{"50%", 0, false},
		{"1kb", 0, false},
		{"9223372036854775807g", 0, false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseTmpfsBytes(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("parseTmpfsBytes(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
