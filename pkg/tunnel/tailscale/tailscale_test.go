package tailscale

import (
	"strings"
	"testing"
	"time"

	"cornus/pkg/tunnel"
)

func TestRegisteredAsUpstreamProvider(t *testing.T) {
	b, err := tunnel.Open("tailscale")
	if err != nil {
		t.Fatalf("Open(tailscale): %v", err)
	}
	if _, ok := b.(tunnel.UpstreamProvider); !ok {
		t.Fatalf("tailscale backend does not implement UpstreamProvider (%T)", b)
	}
	co, ok := b.(tunnel.CredentialOptional)
	if !ok || !co.CredentialOptional() {
		t.Fatalf("tailscale backend should be CredentialOptional")
	}
}

func TestParseFunnelURL(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"https://myhost.tail1234.ts.net/", "https://myhost.tail1234.ts.net"},
		{"|-- https://foo.tailabcd.ts.net/ proxy http://127.0.0.1:5000", "https://foo.tailabcd.ts.net"},
		{"Available on the internet:", ""},
		{"|-- proxy http://127.0.0.1:5000", ""},
		{"https://example.com is not a funnel", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseFunnelURL(c.line); got != c.want {
			t.Errorf("parseFunnelURL(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestScanForFunnelURL(t *testing.T) {
	transcript := strings.Join([]string{
		"Available on the internet:",
		"",
		"https://myhost.tail1234.ts.net/",
		"|-- proxy http://127.0.0.1:54321",
		"",
		"Press Ctrl+C to exit.",
	}, "\n")

	url, err := scanForFunnelURL(strings.NewReader(transcript), 2*time.Second)
	if err != nil {
		t.Fatalf("scanForFunnelURL: %v", err)
	}
	if url != "https://myhost.tail1234.ts.net" {
		t.Fatalf("scanForFunnelURL = %q", url)
	}
}

func TestScanForFunnelURLNoURL(t *testing.T) {
	if _, err := scanForFunnelURL(strings.NewReader("nothing here\nstill nothing\n"), 500*time.Millisecond); err == nil {
		t.Fatal("expected an error when no public URL is printed")
	}
}

func TestFunnelTarget(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"http://127.0.0.1:54321", "54321", false},
		{"http://localhost:8080", "8080", false},
		{"http://127.0.0.1", "http://127.0.0.1", false}, // no port: pass through
		{"", "", true},
		{"://bad", "", true},
	}
	for _, c := range cases {
		got, err := funnelTarget(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("funnelTarget(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("funnelTarget(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("funnelTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
