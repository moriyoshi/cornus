package caretaker

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cornus/pkg/wire"
)

// TestAgentRelayRoleRelaysLocalConnection proves runAgentRelay listens on the
// role's fixed socket path and, for each accepted local connection (standing
// in for a process inside the app container connecting to $SSH_AUTH_SOCK),
// opens a caretaker-initiated TagAgentRelay stream and splices — mirroring
// relayAgentMuxed's server-side counterpart, here played by a fake "real
// agent" echo.
func TestAgentRelayRoleRelaysLocalConnection(t *testing.T) {
	serverSideSess, caretakerSideSess := yamuxPair(t)
	sock := filepath.Join(t.TempDir(), "agent.sock")
	role := AgentRelayRole{Server: "ws://x", SocketPath: sock}

	if err := agentRelayReady(role); err == nil {
		t.Error("agentRelayReady should fail before the role starts listening")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ctx.Done()
		caretakerSideSess.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- runAgentRelay(ctx, caretakerSideSess, role) }()

	// Fake "real local agent": echo every relayed stream that arrives — the
	// readiness probe below (agentRelayReady) makes its OWN local connection
	// and so triggers its own relayed stream too, ahead of the test's real
	// one, so this must handle more than one, exactly as the real
	// relayAgentMuxed dispatches each caretaker connection independently.
	go func() {
		for {
			tag, stream, err := wire.AcceptTagged(serverSideSess)
			if err != nil {
				return
			}
			if tag != wire.TagAgentRelay {
				stream.Close()
				continue
			}
			go func() {
				defer stream.Close()
				buf := make([]byte, 4096)
				for {
					n, err := stream.Read(buf)
					if n > 0 {
						if _, werr := stream.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	waitForSocket(t, sock, 2*time.Second)
	if err := agentRelayReady(role); err != nil {
		t.Errorf("agentRelayReady should succeed once listening: %v", err)
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial agent socket: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("sign-request")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len("sign-request"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "sign-request" {
		t.Fatalf("echoed %q, want %q", buf, "sign-request")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runAgentRelay did not return after ctx cancel")
	}
}

// TestAgentRelayRoleNoRegisteredSource proves a local connection made while
// nothing on the server side is accepting still gets a clean stream open (the
// caretaker doesn't know or care whether the server has anywhere to route
// it — see relayAgentMuxed's fast-fail-when-unregistered behavior, tested
// server-side) and does not hang or panic.
func TestAgentRelayRoleNoRegisteredSource(t *testing.T) {
	_, caretakerSideSess := yamuxPair(t)
	sock := filepath.Join(t.TempDir(), "agent.sock")
	role := AgentRelayRole{Server: "ws://x", SocketPath: sock}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ctx.Done()
		caretakerSideSess.Close()
	}()
	go runAgentRelay(ctx, caretakerSideSess, role)
	waitForSocket(t, sock, 2*time.Second)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial agent socket: %v", err)
	}
	defer conn.Close()
	// Nothing accepts server-side; just prove the local dial itself doesn't
	// hang or crash the role. A short write may or may not succeed depending
	// on buffering — only the absence of a panic/hang matters here.
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write([]byte("x"))
}

// waitForSocket polls until path exists on disk (or t.Fatal on timeout) —
// runAgentRelay creates the listener asynchronously in its own goroutine.
// Checked via os.Stat, not a real dial: a probe connection would itself be
// accepted and relayed, consuming a test's one-shot fake-agent handler.
func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent socket %s not listening after %s", path, timeout)
}
