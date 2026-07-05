//go:build linux

package barehost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProcCgroupUnified(t *testing.T) {
	// A cgroup v2 host: single "0::<path>" line.
	proc := []byte("0::/cornus/cornus-web-0\n")
	if got := parseProcCgroup(proc, ""); got != "/cornus/cornus-web-0" {
		t.Fatalf("unified path = %q", got)
	}
	// v1-style controller lookups find nothing on a pure-v2 file.
	if got := parseProcCgroup(proc, "memory"); got != "" {
		t.Fatalf("memory on v2 = %q, want empty", got)
	}
}

func TestParseProcCgroupV1(t *testing.T) {
	proc := []byte(
		"12:pids:/cornus/web\n" +
			"7:cpu,cpuacct:/cornus/web\n" +
			"4:memory:/cornus/web\n" +
			"3:blkio:/cornus/web\n" +
			"0::/init.scope\n")
	for _, c := range []string{"cpuacct", "cpu", "memory", "pids", "blkio"} {
		if got := parseProcCgroup(proc, c); got != "/cornus/web" {
			t.Fatalf("controller %s path = %q, want /cornus/web", c, got)
		}
	}
	// The unified line is still selectable by "".
	if got := parseProcCgroup(proc, ""); got != "/init.scope" {
		t.Fatalf("unified line = %q", got)
	}
}

func TestParseCPUStatV2(t *testing.T) {
	data := []byte("usage_usec 1500\nuser_usec 1000\nsystem_usec 500\nnr_periods 0\n")
	total, user, kernel := parseCPUStatV2(data)
	// usec -> nsec.
	if total != 1_500_000 || user != 1_000_000 || kernel != 500_000 {
		t.Fatalf("cpu = %d/%d/%d", total, user, kernel)
	}
}

func TestParseIOStatV2(t *testing.T) {
	data := []byte("8:0 rbytes=111 wbytes=222 rios=3 wios=4 dbytes=0 dios=0\n253:1 rbytes=10 wbytes=20\n")
	got := parseIOStatV2(data)
	if len(got) != 4 {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Op != "read" || got[0].Value != 111 || got[0].Major != 8 || got[0].Minor != 0 {
		t.Fatalf("first read entry = %+v", got[0])
	}
	if got[1].Op != "write" || got[1].Value != 222 {
		t.Fatalf("first write entry = %+v", got[1])
	}
	if got[2].Major != 253 || got[2].Minor != 1 || got[2].Value != 10 {
		t.Fatalf("second device read entry = %+v", got[2])
	}
}

func TestParseKVUint(t *testing.T) {
	data := []byte("anon 100\nfile 2048\ninactive_file 1024\nbad line here\nempty\n")
	got := parseKVUint(data)
	if got["anon"] != 100 || got["file"] != 2048 || got["inactive_file"] != 1024 {
		t.Fatalf("kv = %v", got)
	}
	// Malformed lines are skipped, not counted.
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (%v)", len(got), got)
	}
	if parseKVUint([]byte("garbage\n")) != nil {
		t.Fatal("all-malformed content must yield nil")
	}
}

func TestParseCPUAcctStatV1(t *testing.T) {
	// USER_HZ=100 ticks -> nanoseconds.
	user, system := parseCPUAcctStatV1([]byte("user 5\nsystem 3\n"))
	if user != 5*(1e9/100) || system != 3*(1e9/100) {
		t.Fatalf("cpuacct = %d/%d", user, system)
	}
}

func TestParseBlkioV1(t *testing.T) {
	data := []byte("8:0 Read 111\n8:0 Write 222\n8:0 Sync 333\n8:0 Total 666\nTotal 666\n")
	got := parseBlkioV1(data)
	// The four "MAJ:MIN Op Value" lines pass through; the trailing bare
	// "Total 666" (2 fields) is skipped.
	if len(got) != 4 {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Op != "Read" || got[0].Value != 111 || got[0].Major != 8 {
		t.Fatalf("read entry = %+v", got[0])
	}
}

func TestReadMemMaxV2(t *testing.T) {
	dir := t.TempDir()
	max := filepath.Join(dir, "memory.max")
	if err := os.WriteFile(max, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readMemMaxV2(max); got != 0 {
		t.Fatalf(`"max" sentinel = %d, want 0`, got)
	}
	if err := os.WriteFile(max, []byte("8192\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readMemMaxV2(max); got != 8192 {
		t.Fatalf("limit = %d, want 8192", got)
	}
	// Missing file -> 0 (caller substitutes host total).
	if got := readMemMaxV2(filepath.Join(dir, "nope")); got != 0 {
		t.Fatalf("missing = %d, want 0", got)
	}
}

// TestSampleCgroup2 drives the unified sampler against a synthetic cgroup dir,
// verifying the files are wired to the right StatsSample fields.
func TestSampleCgroup2(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cpu.stat", "usage_usec 2000\nuser_usec 1200\nsystem_usec 800\n")
	write("memory.current", "4096\n")
	write("memory.max", "8192\n")
	write("memory.stat", "anon 100\ninactive_file 1024\n")
	write("pids.current", "9\n")
	write("io.stat", "8:0 rbytes=500 wbytes=600\n")

	s := sampleCgroup2(dir)
	if s.CPUTotal != 2_000_000 || s.CPUUser != 1_200_000 || s.CPUKernel != 800_000 {
		t.Fatalf("cpu = %d/%d/%d", s.CPUTotal, s.CPUUser, s.CPUKernel)
	}
	if s.MemUsage != 4096 || s.MemLimit != 8192 || s.Pids != 9 {
		t.Fatalf("mem/pids = %d/%d/%d", s.MemUsage, s.MemLimit, s.Pids)
	}
	if s.MemStats["inactive_file"] != 1024 || s.MemStats["anon"] != 100 {
		t.Fatalf("memStats = %v", s.MemStats)
	}
	if len(s.Blkio) != 2 || s.Blkio[0].Op != "read" || s.Blkio[0].Value != 500 ||
		s.Blkio[1].Op != "write" || s.Blkio[1].Value != 600 {
		t.Fatalf("blkio = %+v", s.Blkio)
	}
}
