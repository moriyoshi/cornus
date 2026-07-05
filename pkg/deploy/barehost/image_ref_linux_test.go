//go:build linux

package barehost

// Image-reference normalization and the snapshotter/rootfs error paths that do
// not need a registry or root. The pull itself is network-bound and stays out of
// the unit suite (it is covered by the E2E harness).

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

// TestNormalizeRefExpandsDockerShorthand pins what the resolver is handed: a
// bare "nginx" must become a fully qualified reference, or the registry dial
// goes somewhere unintended.
func TestNormalizeRefExpandsDockerShorthand(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nginx", "docker.io/library/nginx:latest"},
		{"nginx:1.27", "docker.io/library/nginx:1.27"},
		{"library/nginx", "docker.io/library/nginx:latest"},
		{"myorg/app:v2", "docker.io/myorg/app:v2"},
		// An explicit registry (including the co-located cornus one) is preserved
		// host and port intact, and only the tag is defaulted.
		{"localhost:5000/app", "localhost:5000/app:latest"},
		{"localhost:5000/app:dev", "localhost:5000/app:dev"},
		{"ghcr.io/org/app:sha-abc", "ghcr.io/org/app:sha-abc"},
	}
	for _, c := range cases {
		got, err := normalizeRef(c.in)
		if err != nil {
			t.Errorf("normalizeRef(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRefRejectsUnusableReferences(t *testing.T) {
	for _, in := range []string{"", "app::", "reg.io/App", "reg.io/app:", "reg.io/app@sha256:nothex"} {
		if got, err := normalizeRef(in); err == nil {
			t.Errorf("normalizeRef(%q) = %q, want an error", in, got)
		}
	}
}

func TestParseInsecureRegistriesIgnoresBlanks(t *testing.T) {
	got := parseInsecureRegistries(" reg.internal:5000 , , other.host ")
	if !got["reg.internal:5000"] || !got["other.host"] {
		t.Errorf("parsed = %v, want both hosts trimmed and present", got)
	}
	if len(got) != 2 {
		t.Errorf("parsed = %v, want empty entries dropped", got)
	}
	if len(parseInsecureRegistries("")) != 0 {
		t.Error("an unset list must yield no insecure registries")
	}
}

// TestNewImageStoreRejectsAnUnknownSnapshotter fails a misconfiguration at
// construction rather than at the first deploy.
func TestNewImageStoreRejectsAnUnknownSnapshotter(t *testing.T) {
	_, err := newImageStore(t.TempDir(), "btrfs")
	if err == nil {
		t.Fatal("newImageStore with an unsupported snapshotter: want error")
	}
	if !strings.Contains(err.Error(), "btrfs") {
		t.Errorf("error = %v, want it to name the rejected snapshotter", err)
	}
}

// TestNewImageStoreHonorsTheNativeEscapeHatch covers the docker-in-docker
// setting: "native" must be taken at its word, never silently upgraded to
// overlay (overlay-upon-overlay is rejected by the kernel).
func TestNewImageStoreHonorsTheNativeEscapeHatch(t *testing.T) {
	s, err := newImageStore(t.TempDir(), "native")
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	if s.snName != "native" {
		t.Errorf("snapshotter = %q, want native", s.snName)
	}
	if s.content == nil || s.applier == nil {
		t.Error("the store must carry both a content CAS and a layer applier")
	}
}

// TestPrepareRootfsFailsForAnUnknownChain covers the failure a caller must not
// mistake for success: without the committed layer chain there is nothing to
// mount, and createInstance rolls back on this error.
func TestPrepareRootfsFailsForAnUnknownChain(t *testing.T) {
	s, err := newImageStore(t.TempDir(), "native")
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	chain := digest.FromString("a chain that was never unpacked")
	err = s.prepareRootfs(t.Context(), "cornus-web-0", chain, filepath.Join(t.TempDir(), "rootfs"))
	if err == nil {
		t.Fatal("prepareRootfs off an unknown chain: want error")
	}
	if !strings.Contains(err.Error(), "prepare rootfs snapshot") {
		t.Errorf("error = %v, want it to name the failed snapshot prepare", err)
	}
}

// TestRemoveRootfsReportsAnUnknownSnapshot keeps Delete honest: a snapshot key
// that cannot be released is surfaced, not swallowed into a silent disk leak.
func TestRemoveRootfsReportsAnUnknownSnapshot(t *testing.T) {
	s, err := newImageStore(t.TempDir(), "native")
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	if err := s.removeRootfs(t.Context(), "never-prepared", t.TempDir()); err == nil {
		t.Error("removeRootfs for an unknown snapshot key: want error")
	}
}
