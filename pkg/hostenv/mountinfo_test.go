package hostenv

import "testing"

// A trimmed but structurally real /proc/self/mountinfo from a cornus container
// started with `-v /srv/cornus:/var/lib/cornus:rshared` and
// `-v /var/run/docker.sock:/var/run/docker.sock`.
const sampleMountinfo = `2038 1905 0:186 / / rw,relatime - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/2c6e5b7f8a9d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091/fs
2039 2038 0:189 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
2044 2038 259:2 /var/lib/docker/containers/1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/nvme0n1p2 rw
2046 2038 259:2 /var/lib/docker/containers/1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8/hosts /etc/hosts rw,relatime - ext4 /dev/nvme0n1p2 rw
2100 2038 259:2 /srv/cornus /var/lib/cornus rw,relatime shared:412 - ext4 /dev/nvme0n1p2 rw
2101 2038 259:2 /srv/cornus/slaved /var/lib/cornus/slaved rw,relatime master:412 - ext4 /dev/nvme0n1p2 rw
2110 2038 0:24 /docker.sock /var/run/docker.sock rw,nosuid,nodev,noexec,relatime - tmpfs tmpfs rw
`

func TestParseMountinfo(t *testing.T) {
	entries := parseMountinfo(sampleMountinfo)
	if len(entries) != 7 {
		t.Fatalf("parsed %d entries, want 7", len(entries))
	}
	root := entries[0]
	if root.mountPoint != "/" || root.fsType != "overlay" {
		t.Errorf("root entry = %+v, want mountPoint / and fsType overlay", root)
	}
	if got := entries[4]; got.mountPoint != "/var/lib/cornus" || got.root != "/srv/cornus" {
		t.Errorf("data dir entry = %+v, want /srv/cornus -> /var/lib/cornus", got)
	}
}

func TestParseMountinfoSkipsMalformed(t *testing.T) {
	// A line with no "-" separator, and one too short to be a mount at all.
	in := "garbage\n1 2 0:1 / /a rw shared:1 no-separator ext4 /dev/x rw\n" + sampleMountinfo
	if got, want := len(parseMountinfo(in)), 7; got != want {
		t.Errorf("parsed %d entries, want %d (malformed lines skipped)", got, want)
	}
}

func TestPropagation(t *testing.T) {
	entries := parseMountinfo(sampleMountinfo)
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/var/lib/cornus", PropagationShared},
		{"/var/lib/cornus/mounts/abc", PropagationShared}, // inherits its backing mount
		{"/var/lib/cornus/slaved", PropagationSlave},      // deeper mount wins over its parent
		{"/etc/hosts", PropagationPrivate},
		{"/tmp", PropagationPrivate}, // falls back to the root mount
	} {
		if got := propagationOf(entries, tc.path); got != tc.want {
			t.Errorf("propagation(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	if got := propagationOf(nil, "/anything"); got != PropagationUnknown {
		t.Errorf("propagation with no mount table = %q, want %q", got, PropagationUnknown)
	}
}

// A mount can be both shared and slave; only "shared" makes a mount we create
// visible to the host, so it must win — in EITHER order. The kernel writes
// "shared:N master:M", which is the order that catches a scan that keeps
// overwriting instead of returning on the first "shared:".
func TestPropagationSharedBeatsMaster(t *testing.T) {
	for _, optional := range []string{"shared:9 master:5", "master:5 shared:9"} {
		in := "1 2 0:1 /src /dst rw " + optional + " - ext4 /dev/x rw\n"
		if got := propagationOf(parseMountinfo(in), "/dst"); got != PropagationShared {
			t.Errorf("propagation with optional fields %q = %q, want %q", optional, got, PropagationShared)
		}
	}
}

func TestPathHasPrefix(t *testing.T) {
	for _, tc := range []struct {
		path, prefix string
		want         bool
	}{
		{"/var/lib/cornus", "/var/lib/cornus", true},
		{"/var/lib/cornus/mounts", "/var/lib/cornus", true},
		{"/var/lib/cornus-old", "/var/lib/cornus", false}, // component-wise, not string-wise
		{"/var/lib/cornusx/y", "/var/lib/cornus", false},
		{"/var/lib", "/var/lib/cornus", false},
		{"/anything", "/", true},
	} {
		if got := pathHasPrefix(tc.path, tc.prefix); got != tc.want {
			t.Errorf("pathHasPrefix(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestUnescapeOctal(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`/srv/cornus`, `/srv/cornus`},
		{`/srv/my\040data`, `/srv/my data`},
		{`/srv/a\011b`, "/srv/a\tb"},
		{`/srv/back\134slash`, `/srv/back\slash`},
		{`/srv/not\9octal`, `/srv/not\9octal`},
	} {
		if got := unescapeOctal(tc.in); got != tc.want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFindMountPicksLongestPrefix(t *testing.T) {
	entries := parseMountinfo(sampleMountinfo)
	got, ok := findMount(entries, "/var/lib/cornus/slaved/deeper")
	if !ok {
		t.Fatal("findMount found nothing")
	}
	if got.mountPoint != "/var/lib/cornus/slaved" {
		t.Errorf("mountPoint = %q, want /var/lib/cornus/slaved", got.mountPoint)
	}
}
