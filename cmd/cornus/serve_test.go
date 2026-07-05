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
