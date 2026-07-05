package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// TestRelayMountRemoteSingleReplicaLogsReset covers the operator-diagnosability
// path for the most common real-world mount failure: a workload pod presents a
// deploy-attach session id this single-replica server no longer holds (the server
// restarted, or the owning deploy-attach reconnected under a fresh id), so every
// mount stream is reset. The relay tells the caretaker nothing beyond closing the
// stream, so the server-side WARN (logMountReset) is the ONLY signal an operator
// gets — without it the pod logs "connection reset by peer" with no matching
// server reason. Assert the WARN fires and carries the reason and mount name.
func TestRelayMountRemoteSingleReplicaLogsReset(t *testing.T) {
	s := newTestServerObj(t)
	if s.hubDistributed() {
		t.Skip("test server has a distributed hub; this covers the single-replica reset")
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	// A dummy stream; relayMountRemote returns immediately on a single-replica
	// server (no peer can hold a session this process does not) after logging why.
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	s.relayMountRemote(context.Background(), c2, "deadbeefdeadbeefdeadbeefdeadbeef", "m0")

	got := buf.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "mount relay: reset stream") {
		t.Fatalf("unknown-session mount on a single-replica server must log a WARN reset; got %q", got)
	}
	if !strings.Contains(got, "reason=") || !strings.Contains(got, "stale") {
		t.Fatalf("reset log must explain the reason (stale session id); got %q", got)
	}
	if !strings.Contains(got, "mount.name=m0") {
		t.Fatalf("reset log must carry the mount name; got %q", got)
	}
	// The raw session id is a capability and must never be logged verbatim — only
	// its digest (matching traceMountRelay).
	if strings.Contains(got, "deadbeefdeadbeefdeadbeefdeadbeef") {
		t.Fatalf("reset log must not leak the raw session id; got %q", got)
	}
}
