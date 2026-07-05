package composecli

import "testing"

// TestVersionSkew pins the skew predicate behind the client's up-front warning:
// flag only a real, visible mismatch; never two identical builds (including two
// "dev" builds, which cannot be told apart) or an unknown version on either side.
func TestVersionSkew(t *testing.T) {
	cases := []struct {
		client, server string
		want           bool
	}{
		{"1.2.0", "1.3.0", true},  // real release skew
		{"dev", "1.3.0", true},    // dev client vs released server
		{"1.2.0", "dev", true},    // released client vs dev server
		{"1.2.0", "1.2.0", false}, // matched
		{"dev", "dev", false},     // two dev builds — indistinguishable, don't warn
		{"", "1.2.0", false},      // client version unknown
		{"1.2.0", "", false},      // server too old to advertise a version
		{"", "", false},           // both unknown
	}
	for _, c := range cases {
		if got := versionSkew(c.client, c.server); got != c.want {
			t.Errorf("versionSkew(%q,%q) = %v, want %v", c.client, c.server, got, c.want)
		}
	}
}
