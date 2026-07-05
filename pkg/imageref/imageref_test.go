package imageref

import "testing"

func TestIsBare(t *testing.T) {
	cases := []struct {
		ref  string
		bare bool
	}{
		{"app:v1", true},
		{"app", true},
		{"team/app:v1", true},
		{"team/sub/app:v1", true},
		{"app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{"localhost:5000/app:v1", false},
		{"localhost/app", false},
		{"reg.io/app", false},
		{"reg.io:5000/app:v1", false},
		{"docker.io/library/nginx", false},
	}
	for _, c := range cases {
		if got := IsBare(c.ref); got != c.bare {
			t.Errorf("IsBare(%q) = %v, want %v", c.ref, got, c.bare)
		}
	}
}

func TestQualifyBare(t *testing.T) {
	const host = "localhost:5000"
	cases := []struct {
		ref  string
		host string
		want string
	}{
		{"app:v1", host, "localhost:5000/app:v1"},
		{"team/app:v1", host, "localhost:5000/team/app:v1"},
		{
			"app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			host,
			"localhost:5000/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{"localhost:5000/app:v1", host, "localhost:5000/app:v1"},
		{"reg.io/app", host, "reg.io/app"},
		{"docker.io/library/nginx", host, "docker.io/library/nginx"},
		{"app:v1", "", "app:v1"}, // no registry host resolved: leave unchanged
	}
	for _, c := range cases {
		if got := QualifyBare(c.ref, c.host); got != c.want {
			t.Errorf("QualifyBare(%q, %q) = %q, want %q", c.ref, c.host, got, c.want)
		}
	}
}

func TestSplitHostRepo(t *testing.T) {
	cases := []struct {
		name string
		host string
		repo string
	}{
		{"app", "", "app"},
		{"team/app", "", "team/app"},
		{"localhost:5000/app", "localhost:5000", "app"},
		{"ghcr.io/me/app", "ghcr.io", "me/app"},
		{"docker.io/library/nginx", "docker.io", "library/nginx"},
	}
	for _, c := range cases {
		host, repo := SplitHostRepo(c.name)
		if host != c.host || repo != c.repo {
			t.Errorf("SplitHostRepo(%q) = (%q, %q), want (%q, %q)", c.name, host, repo, c.host, c.repo)
		}
	}
}
