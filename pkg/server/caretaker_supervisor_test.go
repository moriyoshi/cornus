package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cornus/pkg/caretaker"
	"cornus/pkg/hub"
)

// TestCaretakerRoleIsolation proves the pkg/supervisor restructuring of
// runCaretakerConn (pkg/caretaker): a credential role that fails on every
// attempt (its Name has no registered source, so the server's credential relay
// closes the stream immediately) does not disturb a sibling hub role sharing
// the SAME caretaker connection. Before the supervisor adoption, any one
// role's error tore the whole connection's errgroup down, killing every other
// role riding it — this proves that regression is fixed.
func TestCaretakerRoleIsolation(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "cred.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = caretaker.Run(ctx, caretaker.Config{
			Hub: &caretaker.HubRole{
				Server:   srv.URL,
				Register: []caretaker.HubService{{Name: "echo", Addr: echo.Addr().String()}},
			},
			Credentials: []caretaker.CredentialRole{{
				Server: srv.URL,
				Name:   "never-registered",
				Deliver: []caretaker.CredentialDelivery{
					{Kind: "file", Path: credPath, Format: "json"},
				},
			}},
		})
	}()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"

	// Drive real traffic through the hub role repeatedly, overlapping with the
	// credential role's continuous failures on the same connection, to prove
	// the hub role stays up throughout rather than being torn down once
	// alongside the credential role's very first failure.
	reachCtx, reachCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer reachCancel()
	for i := 0; i < 5; i++ {
		got := echoThroughHub(t, reachCtx, wsURL)
		if got != "ping\n" {
			t.Fatalf("attempt %d: echo through hub = %q, want %q (hub role was disturbed by the sibling credential role's failures)", i, got, "ping\n")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The credential role must never have succeeded (no source was ever
	// registered for it) — confirms it really was failing/retrying the whole
	// time, not coincidentally quiescent.
	if _, err := os.Stat(credPath); err == nil {
		t.Fatal("credential file should never be written: its source was never registered")
	}
}

// echoThroughHub dials the hub as a fresh spoke and reaches "echo" by name,
// retrying briefly since the caretaker's own registration races this dial.
func echoThroughHub(t *testing.T, ctx context.Context, wsURL string) string {
	t.Helper()
	sess, err := hub.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("spoke dial: %v", err)
	}
	defer sess.Close()
	for i := 0; i < 100; i++ {
		stream, err := hub.OpenTo(sess, "echo")
		if err != nil {
			t.Fatalf("open data stream: %v", err)
		}
		if _, err := stream.Write([]byte("ping\n")); err != nil {
			stream.Close()
			t.Fatalf("write: %v", err)
		}
		_ = stream.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := bufio.NewReader(stream).ReadString('\n')
		stream.Close()
		if err == nil && line == "ping\n" {
			return line
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}
