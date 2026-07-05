//go:build linux

package hostrun

import (
	"os"
	"strings"
	"testing"
)

func TestHostsStoreCreateAndRemove(t *testing.T) {
	h := NewHostsStore(t.TempDir(), "test", "test")
	path, err := h.Create("cornus-web-0", "cornus-web-0", "10.4.0.9")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if path != h.Path("cornus-web-0") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"127.0.0.1\tlocalhost\n",
		"::1\tlocalhost ip6-localhost ip6-loopback\n",
		"10.4.0.9\tcornus-web-0\n",
		HostsMarkerBegin + "\n",
		HostsMarkerEnd + "\n",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("seed missing %q:\n%s", want, content)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}

	// No IP known -> no self line, still valid.
	if _, err := h.Create("cornus-db-0", "cornus-db-0", ""); err != nil {
		t.Fatalf("create without ip: %v", err)
	}
	data, _ = os.ReadFile(h.Path("cornus-db-0"))
	if strings.Contains(string(data), "cornus-db-0\n") {
		t.Fatalf("self entry written without an IP:\n%s", data)
	}

	h.Remove("cornus-web-0")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("remove left the hosts file")
	}
	h.Remove("cornus-web-0") // idempotent
}

func TestSpliceManagedSection(t *testing.T) {
	section := func(lines ...string) string {
		s := HostsMarkerBegin + "\n"
		for _, l := range lines {
			s += l + "\n"
		}
		return s + HostsMarkerEnd + "\n"
	}
	for _, tc := range []struct {
		name    string
		content string
		lines   []string
		want    string
	}{
		{
			name:    "replaces block, preserves surroundings",
			content: "127.0.0.1\tlocalhost\n" + section("stale\tentry") + "user tail\n",
			lines:   []string{"10.4.0.9\tweb"},
			want:    "127.0.0.1\tlocalhost\n" + section("10.4.0.9\tweb") + "user tail\n",
		},
		{
			name:    "empties block",
			content: section("10.4.0.9\tweb"),
			lines:   nil,
			want:    section(),
		},
		{
			name:    "missing markers appends fresh block",
			content: "127.0.0.1\tlocalhost",
			lines:   []string{"10.4.0.9\tweb"},
			want:    "127.0.0.1\tlocalhost\n" + section("10.4.0.9\tweb"),
		},
		{
			name:    "mangled markers (end before begin) appends fresh block",
			content: HostsMarkerEnd + "\n" + HostsMarkerBegin + "\n",
			lines:   []string{"10.4.0.9\tweb"},
			want:    HostsMarkerEnd + "\n" + HostsMarkerBegin + "\n" + section("10.4.0.9\tweb"),
		},
	} {
		if got := spliceManagedSection(tc.content, tc.lines); got != tc.want {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", tc.name, got, tc.want)
		}
	}
}

func TestHostsSyncPreservesUserEdits(t *testing.T) {
	h := NewHostsStore(t.TempDir(), "test", "test")
	if _, err := h.Create("web-0", "web-0", "10.4.0.9"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A user (or the container) appends an entry outside the markers.
	f, err := os.OpenFile(h.Path("web-0"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("192.168.0.1\tuser-added\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := h.sync("web-0", []string{"10.4.0.10\tdb"}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	data, _ := os.ReadFile(h.Path("web-0"))
	content := string(data)
	if !strings.Contains(content, "192.168.0.1\tuser-added\n") {
		t.Fatalf("user edit lost:\n%s", content)
	}
	if !strings.Contains(content, HostsMarkerBegin+"\n10.4.0.10\tdb\n"+HostsMarkerEnd+"\n") {
		t.Fatalf("managed block not rewritten:\n%s", content)
	}

	// Re-syncing the same lines is a no-op (deterministic content).
	before := content
	if err := h.sync("web-0", []string{"10.4.0.10\tdb"}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	data, _ = os.ReadFile(h.Path("web-0"))
	if string(data) != before {
		t.Fatal("idempotent sync changed the file")
	}

	// Syncing a missing file is a no-op, not an error.
	if err := h.sync("ghost", []string{"x\ty"}); err != nil {
		t.Fatalf("sync of missing file: %v", err)
	}
}
