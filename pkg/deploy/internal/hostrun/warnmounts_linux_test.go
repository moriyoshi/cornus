//go:build linux

package hostrun

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// storageWarnings captures the warnings for spec with the record TIMESTAMP
// dropped. The timestamp is the one part of the line that is meant to differ
// between two runs, so leaving it in would make TestStorageWarningsAreDeterministic
// fail whenever two calls straddle a millisecond boundary — as it did in CI on
// 2026-08-01 (`.558Z` vs `.559Z`) — while telling us nothing about the ordering
// it exists to pin.
func storageWarnings(t *testing.T, spec api.DeploySpec) string {
	t.Helper()
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
	WarnUnmappableStorageOptions(context.Background(), slog.New(slog.NewTextHandler(&buf, opts)), spec)
	return buf.String()
}

func warnLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "level=WARN") {
			n++
		}
	}
	return n
}

// TestWarnsForEachUnmappableStorageOption pins one warning per option, counted
// rather than matched by substring.
//
// These live below api.DeploySpec, so the per-field reflection guards the
// backends gained cannot see them: `Mounts` and `Volumes` are both MAPPED
// top-level fields, and the options inside them were dropped in silence anyway.
// Volume size is the one that matters most — asking for 1Gi and getting an
// unbounded host directory is not a smaller version of the request, it is a
// different guarantee, and nothing said so.
func TestWarnsForEachUnmappableStorageOption(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
		want string
	}{
		{"SELinux relabel", api.DeploySpec{Mounts: []api.Mount{{Source: "/src", Target: "/dst", SELinux: "z"}}}, "SELinux relabel option"},
		{"volume size", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "data", Target: "/data", Size: "1Gi"}}}, "ignores volume size"},
		{"volume driver", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "data", Target: "/data", Driver: "nfs"}}}, "ignores volume driver"},
		{"volume driverOpts", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "d", Target: "/d", DriverOpts: map[string]string{"o": "v"}}}}, "ignores volume driver"},
		{"volume labels", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "d", Target: "/d", Labels: map[string]string{"a": "b"}}}}, "ignores volume labels"},
		{"volume storageClass", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "d", Target: "/d", StorageClass: "fast"}}}, "ignores volume storageClass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := storageWarnings(t, tc.spec)
			if !strings.Contains(out, tc.want) {
				t.Errorf("no warning containing %q:\n%s", tc.want, out)
			}
			if n := warnLines(out); n != 1 {
				t.Errorf("got %d warnings for one requested option, want exactly 1:\n%s", n, out)
			}
		})
	}
}

// TestSilentForStorageItCanHonor is the other half: a mount and a volume using
// only what these backends DO map must produce nothing. A prelude that warns
// about everything teaches operators to skip the warnings.
func TestSilentForStorageItCanHonor(t *testing.T) {
	spec := api.DeploySpec{
		Mounts:  []api.Mount{{Source: "/src", Target: "/dst", ReadOnly: true}},
		Volumes: []api.VolumeSpec{{Name: "data", Target: "/data", ReadOnly: true}, {Target: "/anon"}},
	}
	if out := storageWarnings(t, spec); out != "" {
		t.Errorf("warned about storage options the backend honors:\n%s", out)
	}
}

// TestStorageWarningsAreDeterministic pins the attribute ordering. Warnings are
// read in logs and diffed between runs; a map-iteration-ordered list would make
// two identical deploys look different.
func TestStorageWarningsAreDeterministic(t *testing.T) {
	spec := api.DeploySpec{Volumes: []api.VolumeSpec{
		{Name: "zeta", Target: "/z", Size: "1Gi"},
		{Name: "alpha", Target: "/a", Size: "2Gi"},
	}}
	first := storageWarnings(t, spec)
	if !strings.Contains(first, "alpha, zeta") {
		t.Errorf("volume list is not sorted:\n%s", first)
	}
	for i := 0; i < 5; i++ {
		if got := storageWarnings(t, spec); got != first {
			t.Fatalf("warning output varies between runs:\n%s\n---\n%s", first, got)
		}
	}
}

// TestUTSHostnameAndHostsEntryAgree pins the invariant the two sides of this
// package exist to keep: the name a container answers to (the OCI/UTS hostname,
// set in runtimeOpts) and the name its managed /etc/hosts maps must be the SAME
// name.
//
// They came from two independent copies of the same three-line rule, and
// containerdhost's copy for /etc/hosts passed the raw instance id while
// runtimeOpts resolved spec.Hostname — so a `hostname: db` container answered to
// "db" and resolved "cornus-web-0". Testing each side alone would have missed it:
// each was individually defensible. Only the AGREEMENT is the contract.
func TestUTSHostnameAndHostsEntryAgree(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
	}{
		{"explicit hostname", api.DeploySpec{Name: "web", Image: "img", Hostname: "db"}},
		{"default hostname", api.DeploySpec{Name: "web", Image: "img"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "cornus-web-0"
			// The name runtimeOpts gives the container, via the same helper it calls.
			uts := InstanceHostname(tc.spec, id)

			// The name the hosts file actually maps.
			h := NewHostsStore(t.TempDir(), "test", "test")
			path, err := h.Create(id, InstanceHostname(tc.spec, id), "10.4.0.9")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if want := "10.4.0.9\t" + uts + "\n"; !strings.Contains(string(data), want) {
				t.Errorf("the container answers to %q but /etc/hosts does not map it; want %q in:\n%s", uts, want, data)
			}
		})
	}
}
