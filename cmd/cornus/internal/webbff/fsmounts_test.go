package webbff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLookupMountMatchesOnPathBoundaries pins the matching rule itself, with no server
// and no filesystem. The cases are the ones a string-prefix implementation gets wrong.
func TestLookupMountMatchesOnPathBoundaries(t *testing.T) {
	table := sortedTable(t, []hostMount{
		{kind: mountBind, target: "/data", source: "/host/data", sourceIsDir: true},
		{kind: mountVolume, target: "/data/cache", volume: "cache"},
		{kind: mountBind, target: "/database", source: "/host/db", sourceIsDir: true},
		{kind: mountBind, target: "/etc/app.conf", source: "/host/app.conf"},
		{kind: mountOpaque, target: "/run"},
	})

	for _, tc := range []struct {
		path     string
		wantKind mountKind
		wantRel  string
		wantOK   bool
		why      string
	}{
		{"/data", mountBind, "", true, "the target itself"},
		{"/data/x.txt", mountBind, "x.txt", true, "a child of the bind"},
		{"/data/cache", mountVolume, "", true, "the nested volume wins over the bind"},
		{"/data/cache/x", mountVolume, "x", true, "and keeps winning below itself"},
		{"/database/x", mountBind, "x", true, "/data must not claim /database"},
		{"/datax", 0, "", false, "nor /datax"},
		{"/etc/app.conf", mountBind, "", true, "a file bind matches exactly"},
		{"/etc/app.conf/nope", 0, "", false, "but has no children"},
		{"/run/lock", mountOpaque, "lock", true, "tmpfs shadows what is under it"},
		{"/elsewhere", 0, "", false, "unmounted paths do not match"},
		{"/data/../database/x", mountBind, "x", true, "the path is cleaned before matching"},
	} {
		m, rel, ok := lookupMount(table, tc.path)
		if ok != tc.wantOK {
			t.Errorf("%s (%s): ok=%v, want %v", tc.path, tc.why, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if m.kind != tc.wantKind || rel != tc.wantRel {
			t.Errorf("%s (%s): kind=%v rel=%q, want kind=%v rel=%q",
				tc.path, tc.why, m.kind, rel, tc.wantKind, tc.wantRel)
		}
	}
}

// sortedTable applies the same ordering buildHostMounts does, so the lookup tests
// exercise the real precedence rather than the order the literal happens to be in.
func sortedTable(t *testing.T, in []hostMount) []hostMount {
	t.Helper()
	out := make([]hostMount, len(in))
	copy(out, in)
	for i := range out {
		out[i].order = i
	}
	sortHostMounts(out)
	return out
}

// TestBuildHostMountsClassifiesAndShadows drives the real builder through a compose
// project, so the classification is tested against what pkg/compose actually produces
// rather than against hand-written hostMount literals.
func TestBuildHostMountsClassifiesAndShadows(t *testing.T) {
	bindDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bindDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	confFile := filepath.Join(bindDir, "app.conf")
	if err := os.WriteFile(confFile, []byte("k=v"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil),
		"      - "+bindDir+":/data\n"+
			"      - "+confFile+":/etc/app.conf:ro\n"+
			"      - named-vol:/data/cache\n"+
			"      - /var/lib/anon\n")

	table := s.mountsFor("proj-web")
	if len(table) == 0 {
		t.Fatal("no mount table built for proj-web")
	}

	// The bind resolves to the real host directory...
	m, rel, ok := lookupMount(table, "/data/sub/f.txt")
	if !ok || m.kind != mountBind || !m.sourceIsDir || rel != "sub/f.txt" {
		t.Errorf("/data/sub/f.txt: %+v rel=%q ok=%v", m, rel, ok)
	}
	real, _ := filepath.EvalSymlinks(bindDir)
	if m.source != real {
		t.Errorf("bind source = %q, want the symlink-resolved %q", m.source, real)
	}
	// ...but the nested named volume shadows it, so a client-side walk must not
	// wander into the host's (empty) cache directory.
	// The name is compose's project-scoped spelling ("proj_named-vol"), not the raw
	// one from the file — which is exactly why the volume is addressed by TARGET
	// rather than by name everywhere else.
	if m, _, _ := lookupMount(table, "/data/cache/x"); m.kind != mountVolume || !strings.Contains(m.volume, "named-vol") {
		t.Errorf("/data/cache/x should resolve to the named volume, got %+v", m)
	}
	// A single-file bind is a bind, is not a directory, and is read-only.
	if m, rel, ok := lookupMount(table, "/etc/app.conf"); !ok || m.sourceIsDir || rel != "" || !m.readOnly {
		t.Errorf("file bind: %+v rel=%q ok=%v", m, rel, ok)
	}
	// An anonymous volume carries no name and must never be treated as addressable.
	if m, _, ok := lookupMount(table, "/var/lib/anon/x"); !ok || m.kind != mountAnonymous {
		t.Errorf("/var/lib/anon/x should be an anonymous volume, got %+v ok=%v", m, ok)
	}
}

// TestBuildHostMountsDropsDegenerateTargets covers the entry that would otherwise
// prefix-match the whole filesystem. `- ./data:` parses to an empty target and nothing
// upstream rejects it.
func TestBuildHostMountsDropsDegenerateTargets(t *testing.T) {
	dir := t.TempDir()
	s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil),
		"      - "+dir+":/\n")
	for _, p := range []string{"/etc/passwd", "/", "/anything"} {
		if m, _, ok := lookupMount(s.mountsFor("proj-web"), p); ok {
			t.Errorf("%s matched a degenerate target: %+v", p, m)
		}
	}
}
