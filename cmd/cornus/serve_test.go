package main

import (
	"testing"

	"github.com/alecthomas/kong"
)

// TestServeTLSFlagsParse confirms --tls-cert/--tls-key bind onto ServeCmd so the
// server can serve HTTPS.
func TestServeTLSFlagsParse(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli, kong.Name("cornus"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{
		"serve",
		"--tls-cert", "/etc/cornus/tls.crt",
		"--tls-key", "/etc/cornus/tls.key",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cli.Serve.TLSCert != "/etc/cornus/tls.crt" {
		t.Errorf("TLSCert = %q", cli.Serve.TLSCert)
	}
	if cli.Serve.TLSKey != "/etc/cornus/tls.key" {
		t.Errorf("TLSKey = %q", cli.Serve.TLSKey)
	}
}

// TestServeAddrDefaultsToAllInterfaces pins the default at :5000, and exists to
// stop a well-meaning "secure by default" change from re-breaking remote use.
//
// Loopback is NOT a viable default: a containerized caretaker dials BACK to the
// server for client-local 9P mounts, client-side egress, credential delivery and
// workload telemetry, and it cannot reach a loopback-only bind — the host's
// 127.0.0.1 is not the container's. `BareTarget.AdvertiseHost` states the same
// requirement ("must return a routable host address, not 127.0.0.1, so a
// companion in the guest netns can reach the server"). A loopback default was
// tried on 2026-07-27 and reverted: it left those features failing later and
// elsewhere, which a startup warning cannot make good. The exposure it was meant
// to address is being handled at the auth layer instead, where the fix costs the
// user nothing.
func TestServeAddrDefaultsToAllInterfaces(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli, kong.Name("cornus"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"serve"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cli.Serve.Addr != ":5000" {
		t.Errorf("default Addr = %q, want :5000 — a loopback default breaks caretaker dial-back", cli.Serve.Addr)
	}
	if loopbackOnlyAddr(cli.Serve.Addr) {
		t.Errorf("default Addr %q is loopback-only; workloads could not reach the server", cli.Serve.Addr)
	}

	// Restricting it to this machine stays available for those who want it.
	var narrow CLI
	p2, err := kong.New(&narrow, kong.Name("cornus"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p2.Parse([]string{"serve", "--addr", "127.0.0.1:5000"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if narrow.Serve.Addr != "127.0.0.1:5000" {
		t.Errorf("--addr 127.0.0.1:5000 -> %q", narrow.Serve.Addr)
	}
	if !loopbackOnlyAddr(narrow.Serve.Addr) {
		t.Errorf("%q not classified as loopback-only", narrow.Serve.Addr)
	}
}

func TestLoopbackOnlyAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"127.0.0.2:5000", true},
		{"[::1]:5000", true},
		{"localhost:5000", true},
		{":5000", false},
		{"0.0.0.0:5000", false},
		{"[::]:5000", false},
		{"10.0.0.5:5000", false},
		{"cornus.example.com:5000", false},
		// Unparseable answers false on purpose: a wrong "you are safe" reads far
		// worse than a missing hint.
		{"", false},
		{"garbage", false},
	} {
		if got := loopbackOnlyAddr(tc.addr); got != tc.want {
			t.Errorf("loopbackOnlyAddr(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
