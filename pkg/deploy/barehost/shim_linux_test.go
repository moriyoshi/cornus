//go:build linux

package barehost

import "testing"

// TestRestartAllowedCode covers the shim's exit-code-aware restart policy — the
// refinement the in-process path (restartAllowed) cannot make because it has no
// exit code for a reparented init.
func TestRestartAllowedCode(t *testing.T) {
	cases := []struct {
		name    string
		restart string
		max     int
		count   int
		code    int
		known   bool
		want    bool
	}{
		{"always restarts on clean exit", "always", 0, 0, 0, true, true},
		{"always restarts on failure", "always", 0, 5, 3, true, true},
		{"unless-stopped restarts on clean exit", "unless-stopped", 0, 0, 0, true, true},
		{"empty policy defaults to restart", "", 0, 0, 1, true, true},
		{"no never restarts", "no", 0, 0, 1, true, false},

		// on-failure with a KNOWN code: restart only on nonzero.
		{"on-failure skips clean exit", "on-failure", 0, 0, 0, true, false},
		{"on-failure restarts nonzero exit", "on-failure", 0, 0, 7, true, true},
		{"on-failure respects max attempts", "on-failure", 3, 3, 7, true, false},
		{"on-failure under max restarts", "on-failure", 3, 2, 7, true, true},

		// on-failure with an UNKNOWN code (adopted init): fall back to any-exit.
		{"on-failure unknown code falls back to any-exit", "on-failure", 0, 0, -1, false, true},
		{"on-failure unknown code still honors max", "on-failure", 2, 2, -1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &instanceRecord{Restart: tc.restart, MaxAttempts: tc.max, RestartCount: tc.count}
			if got := restartAllowedCode(rec, tc.code, tc.known); got != tc.want {
				t.Errorf("restartAllowedCode(%q, code=%d, known=%v, count=%d, max=%d) = %v, want %v",
					tc.restart, tc.code, tc.known, tc.count, tc.max, got, tc.want)
			}
		})
	}
}
