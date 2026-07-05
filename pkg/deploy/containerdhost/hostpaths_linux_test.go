//go:build linux

package containerdhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/hostenv"
)

// tableMapper maps by longest-prefix from a fixed table; an absent prefix is
// not host-visible.
type tableMapper map[string]string

func (m tableMapper) ToHost(p string) (string, bool) {
	for container, host := range m {
		trimmed := strings.TrimSuffix(container, "/")
		if p == trimmed {
			return host, true
		}
		if strings.HasPrefix(p, trimmed+"/") {
			return host + strings.TrimPrefix(p, trimmed), true
		}
	}
	return "", false
}

func (m tableMapper) Propagation(string) string { return hostenv.PropagationShared }

func mapperBackend(dataDir string, m hostenv.Mapper) *Backend {
	return &Backend{dataDir: dataDir, mapper: m}
}

// Mount sources cornus provisioned are translated; a user's bind source is
// already a host path (containerd is what opens it) and must be untouched.
func TestHostMountsTranslatesOnlyOurOwn(t *testing.T) {
	b := mapperBackend("/var/lib/cornus", tableMapper{"/var/lib/cornus": "/srv/cornus"})
	got, err := b.hostMounts([]specs.Mount{
		{Source: "/var/lib/cornus/containerd/volumes/named/data", Destination: "/data"},
		{Source: "/var/lib/cornus/containerd/hosts/cornus-web-0", Destination: "/etc/hosts"},
		{Source: "/home/dev/src", Destination: "/src"},
		{Source: "/var/lib/cornus-elsewhere/x", Destination: "/other"},
	})
	if err != nil {
		t.Fatalf("hostMounts: %v", err)
	}
	want := []string{
		"/srv/cornus/containerd/volumes/named/data",
		"/srv/cornus/containerd/hosts/cornus-web-0",
		"/home/dev/src",
		"/var/lib/cornus-elsewhere/x",
	}
	for i, w := range want {
		if got[i].Source != w {
			t.Errorf("mount %d source = %q, want %q", i, got[i].Source, w)
		}
	}
}

// The silent failure this exists to prevent: containerd would create the
// missing path and start the workload against an empty directory.
func TestHostMountsRejectsInvisibleSource(t *testing.T) {
	b := mapperBackend("/var/lib/cornus", tableMapper{"/opt/other": "/srv/other"})
	_, err := b.hostMounts([]specs.Mount{
		{Source: "/var/lib/cornus/containerd/volumes/named/data", Destination: "/data"},
	})
	if err == nil {
		t.Fatal("accepted a mount source containerd cannot see")
	}
	if !strings.Contains(err.Error(), "CORNUS_HOST_PATH_MAP") {
		t.Errorf("error should name the remedy: %v", err)
	}
}

func TestHostMountsDoesNotMutateInput(t *testing.T) {
	b := mapperBackend("/var/lib/cornus", tableMapper{"/var/lib/cornus": "/srv/cornus"})
	in := []specs.Mount{{Source: "/var/lib/cornus/containerd/x", Destination: "/x"}}
	if _, err := b.hostMounts(in); err != nil {
		t.Fatal(err)
	}
	if in[0].Source != "/var/lib/cornus/containerd/x" {
		t.Errorf("input was mutated: %q", in[0].Source)
	}
}

// The overwhelmingly common case: cornus and containerd share a filesystem, so
// the log URI must name the running binary and nothing is staged.
func TestLogShimBinaryUsesTheRunningExecutable(t *testing.T) {
	dir := t.TempDir()
	b := mapperBackend(dir, hostenv.Identity())
	got, err := b.logShimBinary()
	if err != nil {
		t.Fatalf("logShimBinary: %v", err)
	}
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if got != exe {
		t.Errorf("log shim = %q, want the running executable %q", got, exe)
	}
	if _, err := os.Stat(b.stagedShimDir()); !os.IsNotExist(err) {
		t.Error("staged a copy even though the running binary is host-visible")
	}
}

// When our executable lives only inside the container image, the shim — which
// runs on the HOST — could never exec it, so a copy must be staged somewhere
// the host can see and the URI must name THAT.
func TestLogShimBinaryStagesWhenExecutableIsInvisible(t *testing.T) {
	dir := t.TempDir()
	// The data dir is host-visible; the executable's own path is not.
	b := mapperBackend(dir, tableMapper{dir: "/srv/cornus"})

	got, err := b.logShimBinary()
	if err != nil {
		t.Fatalf("logShimBinary: %v", err)
	}
	if !strings.HasPrefix(got, "/srv/cornus/") {
		t.Errorf("log shim = %q, want a path under the host-visible data dir", got)
	}
	staged := filepath.Join(dir, "containerd", "bin", filepath.Base(got))
	st, err := os.Stat(staged)
	if err != nil {
		t.Fatalf("staged binary not written: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("staged binary is not executable: %v", st.Mode())
	}
	// Content-addressed, so an upgraded cornus stages a NEW file and leaves the
	// one older containers still name in their restart labels intact.
	if !strings.HasPrefix(filepath.Base(got), "cornus-") {
		t.Errorf("staged name = %q, want a cornus-<hash> name", filepath.Base(got))
	}
	// Memoized: a second call must not re-hash and re-copy.
	again, err := b.logShimBinary()
	if err != nil || again != got {
		t.Errorf("second call = (%q, %v), want the memoized %q", again, err, got)
	}
}

// A data dir containerd cannot see leaves nowhere to stage to, and that must be
// an error rather than a URI naming a path the shim will fail to exec.
func TestLogShimBinaryFailsWithNowhereToStage(t *testing.T) {
	b := mapperBackend(t.TempDir(), tableMapper{"/opt/other": "/srv/other"})
	if _, err := b.logShimBinary(); err == nil {
		t.Fatal("resolved a log shim with no host-visible location to stage into")
	}
}

// GC must keep anything a surviving container still names — deleting one breaks
// the restart monitor's resurrection of a running workload — and keep the copy
// this process itself hands out.
func TestGCStagedShimsKeepsReferencedAndCurrent(t *testing.T) {
	dir := t.TempDir()
	b := mapperBackend(dir, tableMapper{dir: "/srv/cornus"})
	current, err := b.logShimBinary()
	if err != nil {
		t.Fatal(err)
	}
	binDir := b.stagedShimDir()
	referenced := filepath.Join(binDir, "cornus-000000000000beef")
	orphan := filepath.Join(binDir, "cornus-0000000000000bad")
	for _, p := range []string{referenced, orphan} {
		if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A real log URI is a whole binary://... string, so the staged path appears
	// as a substring rather than as the entire value.
	b.gcStagedShims([]string{"binary:///srv/cornus/containerd/bin/cornus-000000000000beef?containerd-log-shim=/x"})

	if _, err := os.Stat(referenced); err != nil {
		t.Error("deleted a staged binary a container still names in its restart label")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("kept an unreferenced staged binary")
	}
	if _, err := os.Stat(filepath.Join(binDir, filepath.Base(current))); err != nil {
		t.Error("deleted the staged binary this process hands out")
	}
}

// Nothing is ever staged on a co-located server, so the GC must not care that
// the directory does not exist.
func TestGCStagedShimsWithNothingStaged(t *testing.T) {
	b := mapperBackend(t.TempDir(), hostenv.Identity())
	b.gcStagedShims(nil) // must not panic
}

func TestUnderDir(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/var/lib/cornus", "/var/lib/cornus", true},
		{"/var/lib/cornus/containerd/x", "/var/lib/cornus", true},
		{"/var/lib/cornus-old/x", "/var/lib/cornus", false},
		{"/var/lib", "/var/lib/cornus", false},
		{"", "/var/lib", false},
		{"/var/lib", "", false},
	} {
		if got := underDir(tc.path, tc.dir); got != tc.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}

// TestHostMountsLeavesAnAlreadyTranslatedSourceAlone is the guard on the one
// genuine risk in letting this backend take server-written credential files.
//
// TWO translators now see the same mount. The server rewrites sources under its
// mounts dir (hostVisibleMountSources) and this backend rewrites sources under
// its data dir (hostMounts), and both consult the SAME hostenv.Mapper — and the
// mounts dir IS under the data dir. Translating twice would either fail loudly
// or, worse, produce a path that looks plausible and names nothing.
//
// What prevents it is the underDir gate: once the server has rewritten a source
// to the HOST spelling, it is no longer under this process's data dir, so this
// translator skips it. That is a real property rather than a lucky accident, but
// it is invisible at the call site, so it is pinned here.
func TestHostMountsLeavesAnAlreadyTranslatedSourceAlone(t *testing.T) {
	b := mapperBackend("/var/lib/cornus", tableMapper{"/var/lib/cornus": "/srv/cornus"})
	// Exactly what the server hands over for a credential file: it wrote
	// /var/lib/cornus/mounts/creds-<session>/0 and already translated it.
	const alreadyHost = "/srv/cornus/mounts/creds-deadbeef/0"
	got, err := b.hostMounts([]specs.Mount{{Source: alreadyHost, Destination: "/creds"}})
	if err != nil {
		t.Fatalf("hostMounts on an already-translated source: %v", err)
	}
	if got[0].Source != alreadyHost {
		t.Fatalf("source was translated a second time: %q -> %q; the workload would bind a "+
			"path that names nothing", alreadyHost, got[0].Source)
	}
}

// TestHostMountsStillTranslatesAnUntranslatedCredentialDir is the other half:
// the skip above must be because the path is ALREADY host-spelled, not because
// credential directories are somehow exempt. A server that did not translate
// (a non-containerized one, where the mapper is the identity) still hands over
// its own spelling, and this backend must handle it.
func TestHostMountsStillTranslatesAnUntranslatedCredentialDir(t *testing.T) {
	b := mapperBackend("/var/lib/cornus", tableMapper{"/var/lib/cornus": "/srv/cornus"})
	got, err := b.hostMounts([]specs.Mount{
		{Source: "/var/lib/cornus/mounts/creds-deadbeef/0", Destination: "/creds"},
	})
	if err != nil {
		t.Fatalf("hostMounts: %v", err)
	}
	if got[0].Source != "/srv/cornus/mounts/creds-deadbeef/0" {
		t.Fatalf("credential dir source = %q, want the host spelling", got[0].Source)
	}
}
