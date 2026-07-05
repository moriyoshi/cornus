package main

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// TestServeDoesNotAnnounceWhenTheBindFails is the regression test for a banner
// that lied.
//
// `cornus serving addr=:5000` used to be logged before srv.Run(ctx) bound its
// listener, so an occupied port produced:
//
//	INFO cornus serving addr=:5000 storage=...
//	bind: address already in use
//
// which reads as a server that started and then hit an unrelated problem. The fix
// races srv.Run against srv.Ready() and announces only after the bind succeeds.
//
// The fix was closed with no test. Its shape is exactly the kind that rots
// silently: someone moves the log line back above the select for readability, and
// nothing anywhere disagrees — the server still works, the message is still
// accurate in the common case, and only the failure path lies.
//
// This runs entirely in-process: a listener occupies a loopback port, then serve
// is asked to bind the same one. No daemon, no root, no network.
func TestServeDoesNotAnnounceWhenTheBindFails(t *testing.T) {
	// Hold the port for the duration, so the bind below cannot succeed.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a loopback port here: %v", err)
	}
	defer occupied.Close()
	addr := occupied.Addr().String()

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	dataDir := t.TempDir()
	// mem:// storage and the observability store off: this test is about the
	// announce/bind ordering, and every other subsystem is cost without coverage.
	obsOff := false
	c := &ServeCmd{
		Addr:          addr,
		Storage:       "mem://",
		Obs:           &obsOff,
		BuilderAuto:   false,
		ObsRecordLogs: false,
	}
	cli := &CLI{DataDir: dataDir}

	runErr := c.Run(cli)

	logged := buf.String()
	if runErr == nil {
		t.Fatalf("serve returned nil while %s was already bound; the port conflict has to surface as an "+
			"error or the caller cannot tell a failed start from a running server.\nlog:\n%s", addr, logged)
	}
	if strings.Contains(logged, "cornus serving") {
		t.Errorf("serve logged its \"cornus serving\" banner even though the bind failed with %v. The "+
			"announce has moved back above the srv.Run/srv.Ready select, so a startup failure now prints "+
			"success first and the real error second.\nlog:\n%s", runErr, logged)
	}
	// Positive control: the failure must actually be the bind, not some earlier
	// setup error that would make the assertion above vacuously true.
	if !strings.Contains(strings.ToLower(runErr.Error()), "address already in use") {
		t.Errorf("serve failed with %v, want a bind conflict. This test proves nothing about the banner "+
			"if serve never reaches the listen step.", runErr)
	}
}
