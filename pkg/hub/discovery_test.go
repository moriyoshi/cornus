package hub

import (
	"net"
	"strings"
	"testing"
)

func TestSyntheticIP(t *testing.T) {
	a := SyntheticIP("web")
	if a != SyntheticIP("web") {
		t.Fatal("SyntheticIP must be deterministic")
	}
	if !strings.HasPrefix(a, "127.") || net.ParseIP(a) == nil {
		t.Fatalf("SyntheticIP(%q) = %q, want a 127.0.0.0/8 address", "web", a)
	}
	if a == "127.0.0.1" {
		t.Fatal("SyntheticIP must avoid 127.0.0.1")
	}
	if strings.HasSuffix(a, ".0") {
		t.Fatalf("SyntheticIP must avoid a zero last octet: %s", a)
	}
}
