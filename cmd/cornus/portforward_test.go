package main

import "testing"

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		in            string
		local, remote int
		proto         string
		wantErr       bool
	}{
		{"8080:80", 8080, 80, "tcp", false},
		{"5432", 5432, 5432, "tcp", false},
		{" 9000 : 9001 ", 9000, 9001, "tcp", false},
		{"8080:80/tcp", 8080, 80, "tcp", false},
		{"5353:53/udp", 5353, 53, "udp", false},
		{"53/udp", 53, 53, "udp", false},
		{"5353:53/UDP", 5353, 53, "udp", false},
		{"", 0, 0, "", true},
		{"abc", 0, 0, "", true},
		{"80:", 0, 0, "", true},
		{"0:80", 0, 0, "", true},
		{"70000:80", 0, 0, "", true},
		{"80:0", 0, 0, "", true},
		{"5353:53/sctp", 0, 0, "", true},
		{"5353:53/", 0, 0, "", true},
	}
	for _, c := range cases {
		got, err := parsePortSpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parsePortSpec(%q) = %+v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePortSpec(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got.local != c.local || got.remote != c.remote || got.proto != c.proto {
			t.Errorf("parsePortSpec(%q) = %d:%d/%s, want %d:%d/%s", c.in, got.local, got.remote, got.proto, c.local, c.remote, c.proto)
		}
	}
}
