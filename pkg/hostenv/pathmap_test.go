package hostenv

import (
	"strings"
	"testing"
)

func TestParseHostPathMap(t *testing.T) {
	entries, err := parseHostPathMap("/var/lib/cornus=/srv/cornus, /run/cornus=/run/cornus")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(entries))
	}
	if entries[0].container != "/var/lib/cornus" || entries[0].host != "/srv/cornus" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if !entries[0].explicit {
		t.Error("entry 0 should be marked explicit")
	}
	if got, _ := parseHostPathMap("   "); got != nil {
		t.Errorf("empty value = %v, want nil", got)
	}
}

// A typo here silently disables the very translation the operator set it to
// fix, so it must be a hard error rather than a skipped entry.
func TestParseHostPathMapRejectsMalformed(t *testing.T) {
	for _, in := range []string{
		"/var/lib/cornus",            // no "="
		"/var/lib/cornus=",           // no host path
		"=/srv/cornus",               // no container path
		"var/lib/cornus=/srv",        // container path not absolute
		"/var/lib/cornus=srv/cornus", // host path not absolute
	} {
		if _, err := parseHostPathMap(in); err == nil {
			t.Errorf("parseHostPathMap(%q) = nil error, want a failure", in)
		} else if !strings.Contains(err.Error(), HostPathMapEnv) {
			t.Errorf("error for %q does not name %s: %v", in, HostPathMapEnv, err)
		}
	}
}

func TestPathMapToHost(t *testing.T) {
	m := newPathMap([]pathEntry{
		{container: "/var/lib/cornus", host: "/srv/cornus"},
		{container: "/var/lib/cornus/mounts", host: "/mnt/fast/mounts"},
		{container: "/run/cornus", host: "/run/cornus"},
	}, nil)

	for _, tc := range []struct {
		in, want string
		wantOK   bool
	}{
		{"/var/lib/cornus", "/srv/cornus", true},
		{"/var/lib/cornus/blobs/x", "/srv/cornus/blobs/x", true},
		// The longer prefix wins even though the shorter one also matches.
		{"/var/lib/cornus/mounts/sess/a", "/mnt/fast/mounts/sess/a", true},
		{"/run/cornus/netns/abc", "/run/cornus/netns/abc", true},
		// Not host-visible: the caller must fail rather than bind this.
		{"/tmp/scratch", "", false},
		{"", "", false},
	} {
		got, ok := m.ToHost(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("ToHost(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// An operator override exists to correct a bad auto-detected guess, so at equal
// prefix length it must win.
func TestPathMapExplicitBeatsDetected(t *testing.T) {
	m := newPathMap([]pathEntry{
		{container: "/var/lib/cornus", host: "/wrong/detected"},
		{container: "/var/lib/cornus", host: "/right/explicit", explicit: true},
	}, nil)
	if got, _ := m.ToHost("/var/lib/cornus/x"); got != "/right/explicit/x" {
		t.Errorf("ToHost = %q, want /right/explicit/x", got)
	}
}

// Outside a container our paths ARE host paths, so call sites can translate
// unconditionally instead of branching.
func TestIdentityMapper(t *testing.T) {
	m := identityMapper{mounts: parseMountinfo(sampleMountinfo)}
	got, ok := m.ToHost("/var/lib/cornus/mounts/./sess")
	if !ok || got != "/var/lib/cornus/mounts/sess" {
		t.Errorf("ToHost = (%q, %v), want (/var/lib/cornus/mounts/sess, true)", got, ok)
	}
	if _, ok := m.ToHost(""); ok {
		t.Error("ToHost(\"\") should not report ok")
	}
	if got := m.Propagation("/var/lib/cornus"); got != PropagationShared {
		t.Errorf("Propagation = %q, want %q", got, PropagationShared)
	}
}

func TestPathMapEntries(t *testing.T) {
	m := newPathMap([]pathEntry{
		{container: "/a", host: "/host/a"},
		{container: "/a/deeper", host: "/host/deep"},
	}, nil)
	got := m.Entries()
	want := []string{"/a/deeper=/host/deep", "/a=/host/a"} // longest prefix first
	if len(got) != len(want) {
		t.Fatalf("Entries() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Entries()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
