//go:build linux

package barehost

import (
	"archive/tar"
	"os"
	"reflect"
	"testing"
	"time"

	"cornus/pkg/api"
)

func TestTarPackDirBase(t *testing.T) {
	cases := []struct{ in, dir, base string }{
		{"/etc/hosts", "/etc", "hosts"},
		{"/etc/", "/", "etc"},
		{"etc/hosts", "/etc", "hosts"},
		{"/", "/", "."},
		{"", "/", "."},
		{"/a/b/c", "/a/b", "c"},
	}
	for _, c := range cases {
		dir, base := tarPackDirBase(c.in)
		if dir != c.dir || base != c.base {
			t.Errorf("tarPackDirBase(%q) = (%q,%q), want (%q,%q)", c.in, dir, base, c.dir, c.base)
		}
	}
}

func TestTarPackArgs(t *testing.T) {
	got := tarPackArgs("/var/log/app.log")
	want := []string{"tar", "-C", "/var/log", "-cf", "-", "--", "app.log"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tarPackArgs = %v, want %v", got, want)
	}
}

func TestTarExtractArgs(t *testing.T) {
	// Default (CopyUIDGID=false) adds --no-same-owner.
	got := tarExtractArgs("/dest", api.CopyToOptions{})
	want := []string{"tar", "--no-same-owner", "-C", "/dest", "-xf", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tarExtractArgs(default) = %v, want %v", got, want)
	}
	// CopyUIDGID=true restores archive ownership (no --no-same-owner).
	got = tarExtractArgs("/dest", api.CopyToOptions{CopyUIDGID: true})
	want = []string{"tar", "-C", "/dest", "-xf", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tarExtractArgs(CopyUIDGID) = %v, want %v", got, want)
	}
}

func TestStatFromTarHeader(t *testing.T) {
	mt := time.Unix(1_700_000_000, 0).UTC()

	// Regular file: mode bits match Go's os.FileMode, LinkTarget empty.
	fh := &tar.Header{Name: "app.log", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1234, ModTime: mt}
	st := statFromTarHeader("/var/log/app.log", fh)
	if st.Name != "app.log" || st.Size != 1234 {
		t.Errorf("file stat = %+v", st)
	}
	if st.Mode != uint32(os.FileMode(0o644)) {
		t.Errorf("file mode = %#o, want %#o", st.Mode, uint32(os.FileMode(0o644)))
	}
	if st.LinkTarget != "" {
		t.Errorf("file LinkTarget = %q, want empty", st.LinkTarget)
	}

	// Symlink: LinkTarget carries the target, mode has ModeSymlink.
	sh := &tar.Header{Name: "cur", Typeflag: tar.TypeSymlink, Linkname: "/opt/v2", Mode: 0o777, ModTime: mt}
	st = statFromTarHeader("/opt/cur", sh)
	if st.LinkTarget != "/opt/v2" {
		t.Errorf("symlink LinkTarget = %q, want /opt/v2", st.LinkTarget)
	}
	if os.FileMode(st.Mode)&os.ModeSymlink == 0 {
		t.Errorf("symlink mode %v missing ModeSymlink", os.FileMode(st.Mode))
	}

	// Directory: ModeDir set, Name is the base of the requested path.
	dh := &tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mt}
	st = statFromTarHeader("/etc", dh)
	if st.Name != "etc" || os.FileMode(st.Mode)&os.ModeDir == 0 {
		t.Errorf("dir stat = %+v (mode %v)", st, os.FileMode(st.Mode))
	}
}
