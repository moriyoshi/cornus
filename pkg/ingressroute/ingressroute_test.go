package ingressroute

import "testing"

func TestPathMatches(t *testing.T) {
	cases := []struct {
		path, pathType, req string
		want                bool
	}{
		{"/", "Prefix", "/anything", true},
		{"", "Prefix", "/anything", true},
		{"/api", "Prefix", "/api", true},
		{"/api", "Prefix", "/api/v1", true},
		{"/api", "Prefix", "/apix", false},
		{"/api", "Exact", "/api", true},
		{"/api", "Exact", "/api/v1", false},
		// A trailing slash on the rule is insignificant.
		{"/api/", "Prefix", "/api/v1", true},
	}
	for _, tc := range cases {
		if got := PathMatches(tc.path, tc.pathType, tc.req); got != tc.want {
			t.Errorf("PathMatches(%q,%q,%q) = %v, want %v", tc.path, tc.pathType, tc.req, got, tc.want)
		}
	}
}

func TestMatchLenRanksSpecificity(t *testing.T) {
	if MatchLen("/") != 0 {
		t.Errorf("MatchLen(%q) = %d, want 0", "/", MatchLen("/"))
	}
	if MatchLen("/api/") != MatchLen("/api") {
		t.Error("a trailing slash must not change a rule's specificity")
	}
	if MatchLen("/api/v1") <= MatchLen("/api") {
		t.Error("a longer path must rank more specific")
	}
}

func TestSanitizeSubdomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"web", "web"},
		{"web.pr-123", "web.pr-123"},
		{"Web.PR_123", "web.pr-123"},
		// Runs of separators are preserved as runs, matching what the Kubernetes
		// backend writes into the real Ingress object.
		{"my__app", "my--app"},
		{"..web..", "web"},
		{"!!!", ""},
	}
	for _, tc := range cases {
		if got := SanitizeSubdomain(tc.in); got != tc.want {
			t.Errorf("SanitizeSubdomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  Web.Example.COM. ", "web.example.com"},
		{"web.example.com", "web.example.com"},
		{"", ""},
	} {
		if got := CanonicalHost(tc.in); got != tc.want {
			t.Errorf("CanonicalHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWithinDomain(t *testing.T) {
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"web.example.com", "example.com", true},
		{"example.com", "example.com", true},
		{"web.a.example.com", "example.com", true},
		{"webexample.com", "example.com", false},
		{"web.evil.com", "example.com", false},
		// An unset domain pins nothing, so callers can apply it unconditionally.
		{"anything.at.all", "", true},
	}
	for _, tc := range cases {
		if got := WithinDomain(tc.host, tc.domain); got != tc.want {
			t.Errorf("WithinDomain(%q,%q) = %v, want %v", tc.host, tc.domain, got, tc.want)
		}
	}
}

func TestEnvBool(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !envBool(v) {
			t.Errorf("envBool(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "maybe"} {
		if envBool(v) {
			t.Errorf("envBool(%q) = true, want false", v)
		}
	}
}
