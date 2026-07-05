package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:0":    true,
		"localhost:8080": true,
		"[::1]:9000":     true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.168.1.5:80": false,
		"example.com:80": false,
	} {
		err := requireLoopback(addr)
		if ok && err != nil {
			t.Errorf("%s: unexpected error %v", addr, err)
		}
		if !ok && err == nil {
			t.Errorf("%s: expected rejection", addr)
		}
	}
}

// TestCheckListenAddrAllowNonLoopback pins the opt-out: --allow-non-loopback is
// what turns a rejected off-host bind into an accepted one, and it must not also
// weaken anything else — a malformed --addr is still a malformed --addr, which is
// the one rejection an operator asking for an off-host bind still needs.
func TestCheckListenAddrAllowNonLoopback(t *testing.T) {
	for _, tc := range []struct {
		addr  string
		allow bool
		ok    bool
	}{
		{"127.0.0.1:0", false, true},
		{"0.0.0.0:8080", false, false},
		{"192.168.1.5:8080", false, false},
		{"0.0.0.0:8080", true, true},
		{"192.168.1.5:8080", true, true},
		{":8080", true, true},
		{"[::]:8080", true, true},
		// Still nonsense with the opt-out: no port at all.
		{"192.168.1.5", false, false},
		{"192.168.1.5", true, false},
	} {
		c := &WebCmd{Addr: tc.addr, AllowNonLoopback: tc.allow}
		err := c.checkListenAddr()
		if tc.ok && err != nil {
			t.Errorf("--addr %q --allow-non-loopback=%v: unexpected error %v", tc.addr, tc.allow, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("--addr %q --allow-non-loopback=%v: expected rejection", tc.addr, tc.allow)
		}
	}
}

// TestWebHostPolicy covers what the listen decision hands the BFF's Host guard.
// The two halves are separate contracts: --allow-host widens the pin for ANY
// bind (a loopback origin reached through an /etc/hosts alias is legitimate),
// while the pin is only DROPPED for an off-host bind the operator left unnamed —
// so a plain loopback run can never end up with the guard off.
func TestWebHostPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmd     WebCmd
		hosts   []string
		anyHost bool
	}{
		{"loopback default", WebCmd{Addr: defaultWebAddr}, nil, false},
		{"loopback with named host", WebCmd{Addr: defaultWebAddr, AllowHost: []string{"dev.local"}}, []string{"dev.local"}, false},
		// The opt-in alone costs the guard nothing: the bind is still loopback.
		{"opt-in but still loopback", WebCmd{Addr: defaultWebAddr, AllowNonLoopback: true}, nil, false},
		{"off-host unnamed drops the pin", WebCmd{Addr: "0.0.0.0:8080", AllowNonLoopback: true}, nil, true},
		{"off-host named keeps the pin", WebCmd{Addr: "0.0.0.0:8080", AllowNonLoopback: true, AllowHost: []string{"box.lan"}}, []string{"box.lan"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := tc.cmd.bffConfig(&CLI{})
			if err != nil {
				t.Fatalf("bffConfig: %v", err)
			}
			if cfg.AllowAnyHost != tc.anyHost {
				t.Errorf("AllowAnyHost: got %v, want %v", cfg.AllowAnyHost, tc.anyHost)
			}
			if strings.Join(cfg.AllowedHosts, ",") != strings.Join(tc.hosts, ",") {
				t.Errorf("AllowedHosts: got %v, want %v", cfg.AllowedHosts, tc.hosts)
			}
		})
	}
}

// TestParseLocalRoot covers the --local-root grammar.
//
// The "=" cases carry the weight. A directory whose name contains "=" is legal
// and not especially rare (Nix store paths, some build outputs), and splitting it
// as LABEL=DIR would browse a directory the caller never named while reporting
// success — wrong AND quiet. So the label split only fires when what precedes the
// "=" has no path separator in it.
func TestParseLocalRoot(t *testing.T) {
	dir := t.TempDir()
	odd := filepath.Join(dir, "a=b")
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		spec     string
		wantErr  bool
		label    string
		path     string
		readOnly bool
	}{
		{name: "bare dir", spec: dir, path: dir},
		{name: "labelled", spec: "scratch=" + dir, label: "scratch", path: dir},
		{name: "read-only", spec: dir + ":ro", path: dir, readOnly: true},
		{name: "explicit rw", spec: dir + ":rw", path: dir},
		{name: "labelled read-only", spec: "scratch=" + dir + ":ro", label: "scratch", path: dir, readOnly: true},
		// The path itself contains "=", and there is no label.
		{name: "path with equals", spec: odd, path: odd},
		// ...and the same path WITH a label still splits on the first "=".
		{name: "labelled path with equals", spec: "odd=" + odd, label: "odd", path: odd},
		{name: "empty", spec: "", wantErr: true},
		{name: "missing", spec: filepath.Join(dir, "nope"), wantErr: true},
		{name: "not a directory", spec: file, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLocalRoot(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseLocalRoot(%q) = %+v, want an error", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLocalRoot(%q): %v", tc.spec, err)
			}
			if got.Label != tc.label || got.Path != tc.path || got.ReadOnly != tc.readOnly {
				t.Errorf("parseLocalRoot(%q) = %+v, want {Label:%q Path:%q ReadOnly:%v}",
					tc.spec, got, tc.label, tc.path, tc.readOnly)
			}
		})
	}
}

// TestLocalRootRelativePathIsResolved pins the property the published-in-conduit
// path depends on: the parsed root is ABSOLUTE. The agent that may host the BFF
// is env-frozen with its own working directory, so a relative path surviving to
// the wire would name a different directory there — silently, and only for the
// published mode.
func TestLocalRootRelativePathIsResolved(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "scratch")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got, err := parseLocalRoot("scratch")
	if err != nil {
		t.Fatalf("parseLocalRoot: %v", err)
	}
	if !filepath.IsAbs(got.Path) {
		t.Errorf("Path = %q, want absolute", got.Path)
	}
	// EvalSymlinks on both sides: macOS /var is a symlink to /private/var, so the
	// literal strings differ while naming one directory.
	wantReal, _ := filepath.EvalSymlinks(sub)
	gotReal, _ := filepath.EvalSymlinks(got.Path)
	if gotReal != wantReal {
		t.Errorf("Path = %q (resolves to %q), want %q", got.Path, gotReal, wantReal)
	}
}
