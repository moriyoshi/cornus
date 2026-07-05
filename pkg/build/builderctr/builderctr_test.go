package builderctr

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestOptionDefaults(t *testing.T) {
	var o Options
	if got := o.name(); got != DefaultName {
		t.Errorf("name() = %q, want %q", got, DefaultName)
	}
	if got := o.addr(); got != DefaultAddr {
		t.Errorf("addr() = %q, want %q", got, DefaultAddr)
	}
	// The cache volume is derived from the name so two differently-named builders
	// never share a build cache.
	if got := o.volume(); got != DefaultName+"-cache" {
		t.Errorf("volume() = %q, want %q", got, DefaultName+"-cache")
	}

	set := Options{Name: "b", Addr: "127.0.0.1:1", Volume: "v"}
	if set.name() != "b" || set.addr() != "127.0.0.1:1" || set.volume() != "v" {
		t.Errorf("explicit options not honored: %+v", set)
	}
	// Whitespace-only is treated as unset, not as a valid name.
	if (Options{Name: "   "}).name() != DefaultName {
		t.Error("blank name should fall back to the default")
	}
}

// TestCanMountLeavesNothingBehind guards the probe's side effects: it runs a real
// bind mount, so it must undo it and remove its temp dir. A leaked mount would
// accumulate on every server start.
func TestCanMountLeavesNothingBehind(t *testing.T) {
	before, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Skip("cannot read temp dir")
	}
	countProbes := func(entries []os.DirEntry) int {
		n := 0
		for _, e := range entries {
			if len(e.Name()) > 17 && e.Name()[:17] == "cornus-mountprobe" {
				n++
			}
		}
		return n
	}
	got := CanMount()
	t.Logf("CanMount() = %v (uid=%d)", got, os.Geteuid())

	after, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b, a := countProbes(before), countProbes(after); a > b {
		t.Fatalf("probe leaked temp dirs: before=%d after=%d", b, a)
	}
}

func TestHostBaseImage(t *testing.T) {
	// On this host the derived base must be a real distro tag, never empty. The
	// point of deriving it is libc compatibility with a dynamically linked cornus.
	got := hostBaseImage()
	if got == "" {
		t.Fatal("hostBaseImage() = \"\"")
	}
	t.Logf("hostBaseImage() = %q", got)
	if !strings.Contains(got, ":") {
		t.Errorf("hostBaseImage() = %q, want a tagged ref", got)
	}
}

// TestBinaryTagIsContentAddressed proves the builder image tag tracks the binary's
// content: identical bytes reuse the image, changed bytes force a rebuild.
func TestBinaryTagIsContentAddressed(t *testing.T) {
	dir := t.TempDir()
	a, b, c := dir+"/a", dir+"/b", dir+"/c"
	if err := os.WriteFile(a, []byte("binary-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("binary-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c, []byte("binary-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	ta, err := binaryTag(a)
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := binaryTag(b)
	tc, _ := binaryTag(c)
	if ta != tb {
		t.Errorf("same content produced different tags: %q vs %q", ta, tb)
	}
	if ta == tc {
		t.Errorf("different content produced the same tag: %q", ta)
	}
	if len(ta) != 12 {
		t.Errorf("tag %q length = %d, want 12", ta, len(ta))
	}
}

// TestWriteBuildContext proves the streamed context carries exactly the
// Dockerfile and an executable cornus binary the daemon can COPY.
func TestWriteBuildContext(t *testing.T) {
	dir := t.TempDir()
	exe := dir + "/cornus"
	payload := []byte("\x7fELF-not-really")
	if err := os.WriteFile(exe, payload, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeBuildContext(&buf, "FROM scratch\n", exe, int64(len(payload))); err != nil {
		t.Fatal(err)
	}

	found := map[string]string{}
	modes := map[string]int64{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		found[h.Name] = string(b)
		modes[h.Name] = h.Mode
	}
	if found["Dockerfile"] != "FROM scratch\n" {
		t.Errorf("Dockerfile = %q", found["Dockerfile"])
	}
	if found["cornus"] != string(payload) {
		t.Errorf("cornus payload = %q", found["cornus"])
	}
	if modes["cornus"] != 0o755 {
		t.Errorf("cornus mode = %o, want 755 (must be executable)", modes["cornus"])
	}
}

// TestBuildStreamErr proves a failure reported inside the daemon's JSON stream is
// surfaced. A failed build still returns HTTP 200, so ignoring the stream would
// silently treat a broken image as ready.
func TestBuildStreamErr(t *testing.T) {
	ok := `{"stream":"Step 1/3"}` + "\n" + `{"stream":"Successfully built abc"}` + "\n"
	if err := buildStreamErr(strings.NewReader(ok)); err != nil {
		t.Errorf("successful stream reported error: %v", err)
	}
	bad := `{"stream":"Step 1/3"}` + "\n" + `{"errorDetail":{"message":"package runc not found"},"error":"boom"}` + "\n"
	err := buildStreamErr(strings.NewReader(bad))
	if err == nil {
		t.Fatal("failed build stream reported no error")
	}
	if !strings.Contains(err.Error(), "runc not found") {
		t.Errorf("error = %v, want the daemon's detail message", err)
	}
}

// TestFingerprintTracksExportMode is the guard for the 405 regression: a builder
// created for a push-to-registry server must NOT be reused by a server whose
// registry re-exports the Docker daemon (it is read-only there, so the builder
// would push at it and get 405). The fingerprint has to differ so Ensure
// recreates the container instead of adopting it.
func TestFingerprintTracksExportMode(t *testing.T) {
	push := Options{}
	load := Options{DockerExport: true}
	if push.fingerprint() == load.fingerprint() {
		t.Fatalf("export mode not reflected in fingerprint: %q", push.fingerprint())
	}
	// Fields that change container behavior must all participate.
	for name, o := range map[string]Options{
		"image":  {Image: "pinned:1"},
		"base":   {BaseImage: "debian:12"},
		"addr":   {Addr: "127.0.0.1:6000"},
		"volume": {Volume: "other"},
	} {
		if o.fingerprint() == push.fingerprint() {
			t.Errorf("%s not reflected in fingerprint", name)
		}
	}
	// Identical options must be stable, or every call would recreate the builder.
	if (Options{DockerExport: true}).fingerprint() != load.fingerprint() {
		t.Error("fingerprint is not stable for identical options")
	}
}

func TestDockerSocketPath(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	if got := dockerSocketPath(); got != "/var/run/docker.sock" {
		t.Errorf("default socket = %q", got)
	}
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	if got := dockerSocketPath(); got != "/run/user/1000/docker.sock" {
		t.Errorf("unix socket = %q", got)
	}
	// A tcp daemon cannot be handed to the container as a bind mount; "" makes
	// Ensure refuse with an explanation rather than start a builder that cannot
	// deliver its results.
	t.Setenv("DOCKER_HOST", "tcp://10.0.0.5:2375")
	if got := dockerSocketPath(); got != "" {
		t.Errorf("tcp DOCKER_HOST should yield no socket path, got %q", got)
	}
}

// TestEnsureRefusesDockerExportWithoutSocket proves the tcp-daemon case fails
// with an actionable message instead of starting a builder whose exports would
// silently go nowhere.
func TestEnsureRefusesDockerExportWithoutSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://10.0.0.5:2375")
	_, err := Ensure(context.Background(), Options{DockerExport: true})
	if err == nil {
		t.Fatal("Ensure accepted DockerExport with a tcp DOCKER_HOST")
	}
	for _, want := range []string{"--storage", "--builder-url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not suggest %s: %v", want, err)
		}
	}
}
