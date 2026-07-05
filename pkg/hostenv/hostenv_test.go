package hostenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc writes a procRoot whose self/mountinfo and self/cgroup hold the
// given content, and returns its path.
func fakeProc(t *testing.T, mountinfo, cgroup string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"mountinfo": mountinfo, "cgroup": cgroup} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(root, "self", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// noEnv/noFile are the "nothing set, nothing present" test seams.
func noEnv(string) string { return "" }
func noFile(string) bool  { return false }
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetectOnHostReturnsIdentityMapper(t *testing.T) {
	// A host mount table: no container-managed binds, no container cgroup.
	proc := fakeProc(t, "2038 1905 259:2 / / rw,relatime - ext4 /dev/nvme0n1p2 rw\n", "0::/user.slice/user-1000.slice\n")
	env, m, err := Detect(context.Background(), Options{
		Runtime: RuntimeDocker, procRoot: proc, getenv: noEnv, hostname: "buildhost", exists: noFile,
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if env.InContainer {
		t.Error("InContainer = true on a host mount table")
	}
	got, ok := m.ToHost("/var/lib/cornus/mounts/x")
	if !ok || got != "/var/lib/cornus/mounts/x" {
		t.Errorf("ToHost = (%q, %v), want the identity", got, ok)
	}
}

func TestDetectInContainerWithExplicitMap(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "0::/\n")
	env, m, err := Detect(context.Background(), Options{
		Runtime:  RuntimeContainerd,
		procRoot: proc,
		getenv:   envMap(map[string]string{HostPathMapEnv: "/var/lib/cornus=/srv/cornus"}),
		hostname: "cornus", exists: noFile,
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !env.InContainer {
		t.Error("InContainer = false despite a docker-managed /etc/hosts bind")
	}
	if env.Runtime != RuntimeContainerd {
		t.Errorf("Runtime = %q, want %q", env.Runtime, RuntimeContainerd)
	}
	if !env.Translating {
		t.Error("Translating = false despite an explicit CORNUS_HOST_PATH_MAP")
	}
	got, ok := m.ToHost("/var/lib/cornus/mounts/sess/a")
	if !ok || got != "/srv/cornus/mounts/sess/a" {
		t.Errorf("ToHost = (%q, %v), want (/srv/cornus/mounts/sess/a, true)", got, ok)
	}
	// Nothing else is host-visible, and the caller must be able to tell.
	if _, ok := m.ToHost("/tmp/elsewhere"); ok {
		t.Error("ToHost reported ok for an unmapped path")
	}
}

// A malformed override silently disables the translation it was set to fix, so
// it fails startup rather than degrading.
func TestDetectRejectsMalformedHostPathMap(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "")
	_, _, err := Detect(context.Background(), Options{
		procRoot: proc, getenv: envMap(map[string]string{HostPathMapEnv: "not-a-pair"}), exists: noFile,
	})
	if err == nil {
		t.Fatal("Detect accepted a malformed CORNUS_HOST_PATH_MAP")
	}
}

func TestDetectSelfInspection(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "0::/\n")
	var asked []string
	env, m, err := Detect(context.Background(), Options{
		Runtime:  RuntimeDocker,
		procRoot: proc,
		getenv:   noEnv,
		hostname: "cornus",
		exists:   noFile,
		Inspect: func(_ context.Context, id string) (SelfInspect, error) {
			asked = append(asked, id)
			return SelfInspect{
				ID:          id,
				Hostname:    "cornus",
				NetworkMode: "host",
				Mounts: []MountPoint{
					{Source: "/srv/cornus", Destination: "/var/lib/cornus"},
					{Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if env.Err != nil {
		t.Fatalf("Env.Err = %v", env.Err)
	}
	if env.SelfID != selfContainerID {
		t.Errorf("SelfID = %q, want %q", env.SelfID, selfContainerID)
	}
	if len(asked) != 1 || asked[0] != selfContainerID {
		t.Errorf("inspected %v, want just the mountinfo-derived id", asked)
	}
	if !env.Translating {
		t.Error("Translating = false after a confirmed self-inspection")
	}
	if !env.HostNetworkKnown || !env.HostNetwork {
		t.Errorf("HostNetwork = %v (known %v), want true/true", env.HostNetwork, env.HostNetworkKnown)
	}
	if got, ok := m.ToHost("/var/lib/cornus/blobs"); !ok || got != "/srv/cornus/blobs" {
		t.Errorf("ToHost = (%q, %v), want (/srv/cornus/blobs, true)", got, ok)
	}
}

// The explicit override must win over what self-inspection reports, so a bad
// auto-detected mapping is always correctable.
func TestDetectExplicitOverridesInspection(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "")
	_, m, err := Detect(context.Background(), Options{
		procRoot: proc,
		getenv:   envMap(map[string]string{HostPathMapEnv: "/var/lib/cornus=/corrected"}),
		hostname: "cornus", exists: noFile,
		Inspect: func(_ context.Context, id string) (SelfInspect, error) {
			return SelfInspect{ID: id, Mounts: []MountPoint{
				{Source: "/srv/cornus", Destination: "/var/lib/cornus"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got, _ := m.ToHost("/var/lib/cornus/x"); got != "/corrected/x" {
		t.Errorf("ToHost = %q, want /corrected/x", got)
	}
}

// A candidate whose mounts do not appear in our own mount table describes some
// OTHER container. Trusting it would produce a confident, wholly wrong map.
func TestDetectRejectsUnconfirmedCandidate(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "")
	env, m, err := Detect(context.Background(), Options{
		procRoot: proc, getenv: noEnv, hostname: "cornus", exists: noFile,
		Inspect: func(_ context.Context, id string) (SelfInspect, error) {
			return SelfInspect{ID: id, Mounts: []MountPoint{
				{Source: "/srv/other", Destination: "/not/in/our/mount/table"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if env.SelfID != "" {
		t.Errorf("SelfID = %q, want empty after a rejected candidate", env.SelfID)
	}
	if !errors.Is(env.Err, errSelfUnconfirmed) {
		t.Errorf("Env.Err = %v, want errSelfUnconfirmed", env.Err)
	}
	if env.Translating {
		t.Error("Translating = true with no confirmed mapping")
	}
	// Nothing was learned, so we fall back to the identity rather than
	// declaring every path unmappable.
	if got, ok := m.ToHost("/var/lib/cornus"); !ok || got != "/var/lib/cornus" {
		t.Errorf("ToHost = (%q, %v), want the identity fallback", got, ok)
	}
}

// A cornus containerized ALONGSIDE the runtime it drives (the E2E runner's
// dockerd-in-the-same-container shape) is in a container but shares the
// runtime's mount namespace: its paths already agree, and the inner daemon has
// never heard of the outer container we run in. Translating there would break a
// working setup, so detection must fall back to the identity.
func TestDetectCoLocatedInContainerStaysIdentity(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "0::/\n")
	env, m, err := Detect(context.Background(), Options{
		Runtime: RuntimeDocker, procRoot: proc, getenv: noEnv, hostname: "runner", exists: noFile,
		// The in-container daemon does not know the id we mined from our own
		// mount table, exactly as a sibling dockerd would not.
		Inspect: func(context.Context, string) (SelfInspect, error) {
			return SelfInspect{}, errors.New("No such container")
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !env.InContainer {
		t.Error("InContainer = false")
	}
	if env.Translating {
		t.Error("Translating = true against a runtime that never created us")
	}
	if got, ok := m.ToHost("/var/lib/cornus/mounts/x"); !ok || got != "/var/lib/cornus/mounts/x" {
		t.Errorf("ToHost = (%q, %v), want the identity", got, ok)
	}
}

// An inspect failure is reported verbatim so the preflight can explain an empty
// map with a real cause instead of a bare "not found".
func TestDetectSurfacesInspectError(t *testing.T) {
	proc := fakeProc(t, sampleMountinfo, "")
	sentinel := errors.New("permission denied on /var/run/docker.sock")
	env, _, err := Detect(context.Background(), Options{
		procRoot: proc, getenv: noEnv, hostname: "cornus", exists: noFile,
		Inspect: func(context.Context, string) (SelfInspect, error) { return SelfInspect{}, sentinel },
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !errors.Is(env.Err, sentinel) {
		t.Errorf("Env.Err = %v, want the inspect error", env.Err)
	}
}

func TestDetectNoSelfCandidate(t *testing.T) {
	// In a container (per KUBERNETES_SERVICE_HOST) but with nothing in /proc
	// naming an id: the case where CORNUS_HOST_PATH_MAP is the only answer.
	proc := fakeProc(t, "2038 1905 0:186 / / rw - overlay overlay rw\n", "0::/\n")
	env, _, err := Detect(context.Background(), Options{
		procRoot: proc,
		getenv:   envMap(map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1"}),
		hostname: "web-7d9f8b6c5d-abcde", exists: noFile,
		Inspect: func(context.Context, string) (SelfInspect, error) {
			t.Fatal("Inspect called with no candidate id")
			return SelfInspect{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !env.InContainer {
		t.Error("InContainer = false inside a Kubernetes pod")
	}
	if !errors.Is(env.Err, errNoSelfCandidate) {
		t.Errorf("Env.Err = %v, want errNoSelfCandidate", env.Err)
	}
	if !strings.Contains(env.Err.Error(), HostPathMapEnv) {
		t.Errorf("Env.Err should point at %s: %v", HostPathMapEnv, env.Err)
	}
}

func TestConfirmSelf(t *testing.T) {
	mounts := parseMountinfo(sampleMountinfo)
	for name, tc := range map[string]struct {
		self SelfInspect
		want bool
	}{
		"a reported mount is in our table": {
			SelfInspect{Mounts: []MountPoint{{Destination: "/var/lib/cornus"}}}, true,
		},
		"no reported mount is in our table": {
			SelfInspect{Mounts: []MountPoint{{Destination: "/elsewhere"}}}, false,
		},
		"no mounts, hostname matches": {
			SelfInspect{Hostname: "cornus"}, true,
		},
		"no mounts, hostname differs": {
			SelfInspect{Hostname: "someone-else"}, false,
		},
		"nothing to check": {
			SelfInspect{}, true,
		},
	} {
		if got := confirmSelf(tc.self, mounts, "cornus"); got != tc.want {
			t.Errorf("%s: confirmSelf = %v, want %v", name, got, tc.want)
		}
	}
}

func TestLooksContainerized(t *testing.T) {
	host := "2038 1905 259:2 / / rw - ext4 /dev/nvme0n1p2 rw\n"
	for name, tc := range map[string]struct {
		mountinfo, cgroup string
		getenv            func(string) string
		exists            func(string) bool
		want              bool
	}{
		"plain host":            {host, "0::/user.slice\n", noEnv, noFile, false},
		"dockerenv marker":      {host, "0::/user.slice\n", noEnv, func(p string) bool { return p == "/.dockerenv" }, true},
		"podman marker":         {host, "0::/user.slice\n", noEnv, func(p string) bool { return p == "/run/.containerenv" }, true},
		"kubernetes env":        {host, "0::/\n", envMap(map[string]string{"KUBERNETES_SERVICE_HOST": "10.96.0.1"}), noFile, true},
		"docker-managed binds":  {sampleMountinfo, "0::/\n", noEnv, noFile, true},
		"container cgroup path": {host, "0::/system.slice/docker-" + selfContainerID + ".scope\n", noEnv, noFile, true},
	} {
		proc := fakeProc(t, tc.mountinfo, tc.cgroup)
		opts := Options{procRoot: proc, getenv: tc.getenv, exists: tc.exists}.resolve()
		if got := looksContainerized(opts, parseMountinfo(tc.mountinfo)); got != tc.want {
			t.Errorf("%s: looksContainerized = %v, want %v", name, got, tc.want)
		}
	}
}

// Found by running this against a real Docker daemon: a container whose only
// mount is the docker socket failed to confirm itself, because Docker reports
// the destination as /var/run/docker.sock while the kernel records the mount at
// /run/docker.sock (/var/run being a symlink). The container then fell back to
// the identity mapper and silently stopped translating — on the most ordinary
// setup there is.
func TestConfirmSelfResolvesSymlinkedDestinations(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "run")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "var-run")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The mount table records the resolved spelling...
	mounts := parseMountinfo("1 2 0:1 /src " + filepath.Join(real, "sub") + " rw - ext4 /dev/x rw\n")
	// ...while the runtime reports the symlinked one.
	self := SelfInspect{Mounts: []MountPoint{{Destination: filepath.Join(link, "sub")}}}
	if !confirmSelf(self, mounts, "irrelevant") {
		t.Error("confirmSelf rejected a destination that differs only by a symlinked prefix")
	}
}
