package cloudflare

import (
	"strings"
	"testing"
	"time"
)

func TestParseQuickTunnelURL(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"2024-01-01T00:00:00Z INF |  https://threaded-fathers-explore-supplier.trycloudflare.com  |", "https://threaded-fathers-explore-supplier.trycloudflare.com"},
		{"Visit it at https://abc-def.trycloudflare.com/", "https://abc-def.trycloudflare.com"},
		{"2024 INF Registered tunnel connection conn=0", ""},
		{"https://example.com is not a cf tunnel", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseQuickTunnelURL(c.line); got != c.want {
			t.Errorf("parseQuickTunnelURL(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestScanForTunnelURL(t *testing.T) {
	transcript := strings.Join([]string{
		"2024-01-01T00:00:00Z INF Thank you for trying Cloudflare Tunnel.",
		"2024-01-01T00:00:00Z INF +----------------------------------------+",
		"2024-01-01T00:00:00Z INF |  Your quick Tunnel has been created!    |",
		"2024-01-01T00:00:00Z INF |  https://silly-name-1234.trycloudflare.com  |",
		"2024-01-01T00:00:00Z INF +----------------------------------------+",
		"2024-01-01T00:00:00Z INF Registered tunnel connection",
	}, "\n")

	url, err := scanForTunnelURL(strings.NewReader(transcript), 2*time.Second)
	if err != nil {
		t.Fatalf("scanForTunnelURL: %v", err)
	}
	if url != "https://silly-name-1234.trycloudflare.com" {
		t.Fatalf("scanForTunnelURL = %q", url)
	}
}

func TestScanForTunnelURLNoURL(t *testing.T) {
	if _, err := scanForTunnelURL(strings.NewReader("no url here\nstill none\n"), 500*time.Millisecond); err == nil {
		t.Fatal("expected an error when no tunnel URL is printed")
	}
}
