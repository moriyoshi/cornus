package githubcli

import "testing"

// TestBinResolution reads the resolved executable directly, which is the only
// way to pin the fall-through cases: asserting through Fetch would pass
// vacuously on a machine that happens to have a working `gh` on PATH.
func TestBinResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		cfg  map[string]string
		want string
	}{
		{"unset env, silent spec", "", nil, "gh"},
		// A set-but-empty override must fall through to the default, not exec "".
		{"empty env is not a binary named empty", "", map[string]string{}, "gh"},
		{"env supplies it", "/opt/gh/bin/gh", nil, "/opt/gh/bin/gh"},
		{"config wins over env", "/opt/gh/bin/gh", map[string]string{"command": "/usr/local/bin/gh-wrapper"}, "/usr/local/bin/gh-wrapper"},
		{"config alone", "", map[string]string{"command": "/usr/local/bin/gh-wrapper"}, "/usr/local/bin/gh-wrapper"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(binEnv, tc.env)
			src, err := newSource(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := src.(*source).bin; got != tc.want {
				t.Fatalf("resolved bin = %q, want %q", got, tc.want)
			}
		})
	}
}
