//go:build linux

package builder

import "testing"

func TestParseKeepBytes(t *testing.T) {
	cases := []struct {
		in     string
		want   int64
		wantOK bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"0", 0, false},
		{"-1", 0, false},
		{"garbage", 0, false},
		{"1024", 1024, true},
		{"2GB", 2 * 1024 * 1024 * 1024, true},
		{"512m", 512 * 1024 * 1024, true},
		{" 1g ", 1024 * 1024 * 1024, true},
	}
	for _, c := range cases {
		got, ok := parseKeepBytes(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("parseKeepBytes(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestDefaultGCPolicyBounded verifies the worker GC policy is non-empty and that
// every rule carries a keep bound, so the build cache dir cannot grow without
// limit. The env override must flow through to the max-used-space cap.
func TestDefaultGCPolicyBounded(t *testing.T) {
	t.Setenv(buildCacheKeepBytesEnv, "3GB")
	policies := defaultGCPolicy(t.TempDir())
	if len(policies) == 0 {
		t.Fatal("defaultGCPolicy returned no rules")
	}
	const wantCap = int64(3) * 1024 * 1024 * 1024
	sawCap := false
	for i, p := range policies {
		if p.MaxUsedSpace == 0 && p.ReservedSpace == 0 && p.MinFreeSpace == 0 {
			t.Errorf("rule %d has no keep bound: %+v", i, p)
		}
		if p.MaxUsedSpace == wantCap {
			sawCap = true
		}
	}
	if !sawCap {
		t.Errorf("env override %s did not reach any rule's MaxUsedSpace; got %+v", buildCacheKeepBytesEnv, policies)
	}
}
