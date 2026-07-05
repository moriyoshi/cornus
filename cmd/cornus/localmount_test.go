package main

import "testing"

func TestResolveFileCacheDir(t *testing.T) {
	cases := []struct{ dataDir, dir, want string }{
		{"/data", "", ""},                           // unset stays unset
		{"/data", "/mnt/cache", "/mnt/cache"},       // absolute used verbatim
		{"/data", "filecache", "/data/filecache"},   // relative roots at data dir
		{"/data", "sub/cache", "/data/sub/cache"},   // nested relative
		{"/data", "./filecache", "/data/filecache"}, // cleaned
	}
	for _, tc := range cases {
		if got := resolveFileCacheDir(tc.dataDir, tc.dir); got != tc.want {
			t.Errorf("resolveFileCacheDir(%q, %q) = %q, want %q", tc.dataDir, tc.dir, got, tc.want)
		}
	}
}

func TestParseLocalMount(t *testing.T) {
	cases := []struct {
		in                         string
		wantErr                    bool
		src, dst                   string
		readOnly, immutable, async bool
	}{
		{in: "/a:/b", src: "/a", dst: "/b"},
		{in: "/a:/b:ro", src: "/a", dst: "/b", readOnly: true},
		{in: "/a:/b:cache", src: "/a", dst: "/b", readOnly: true, immutable: true},
		{in: "/a:/b:ro,cache", src: "/a", dst: "/b", readOnly: true, immutable: true},
		{in: "/a:/b:cache,ro", src: "/a", dst: "/b", readOnly: true, immutable: true},
		{in: "/a:/b:async", src: "/a", dst: "/b", async: true},
		{in: "/a:/b:async,ro", wantErr: true},    // async is writable; excludes ro
		{in: "/a:/b:async,cache", wantErr: true}, // excludes cache
		{in: "/a:/b:rw", wantErr: true},
		{in: "/a:/b:ro,bogus", wantErr: true},
		{in: "/a", wantErr: true},
		{in: "/a:/b:c:d", wantErr: true},
	}
	for _, tc := range cases {
		m, err := parseLocalMount(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseLocalMount(%q) = %+v, want error", tc.in, m)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseLocalMount(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if m.Source != tc.src || m.Target != tc.dst || m.ReadOnly != tc.readOnly || m.Immutable != tc.immutable || m.AsyncCache != tc.async {
			t.Errorf("parseLocalMount(%q) = %+v, want src=%s dst=%s ro=%v immutable=%v async=%v",
				tc.in, m, tc.src, tc.dst, tc.readOnly, tc.immutable, tc.async)
		}
	}
}
