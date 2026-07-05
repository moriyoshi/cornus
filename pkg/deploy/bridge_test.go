package deploy

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// bridgeClient models the caller side of a non-interactive exec: it provides
// NO stdin (Read returns EOF at once) and captures whatever the bridge writes
// to it (the process output).
type bridgeClient struct {
	mu     sync.Mutex
	out    bytes.Buffer
	closed bool
}

func (c *bridgeClient) Read([]byte) (int, error) { return 0, io.EOF }
func (c *bridgeClient) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}
func (c *bridgeClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}
func (c *bridgeClient) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

// bridgeRemote models the backend side of an exec: it emits payload after a
// delay (independent of stdin), then EOF, and records a CloseWrite half-close.
type bridgeRemote struct {
	pr             *io.PipeReader
	pw             *io.PipeWriter
	closeWriteOnce sync.Once
	closeWrote     chan struct{}
}

func newBridgeRemote(payload string, delay time.Duration) *bridgeRemote {
	pr, pw := io.Pipe()
	d := &bridgeRemote{pr: pr, pw: pw, closeWrote: make(chan struct{})}
	go func() {
		time.Sleep(delay)
		_, _ = io.WriteString(pw, payload)
		_ = pw.Close()
	}()
	return d
}

func (d *bridgeRemote) Read(p []byte) (int, error)  { return d.pr.Read(p) }
func (d *bridgeRemote) Write(p []byte) (int, error) { return len(p), nil } // discard stdin
func (d *bridgeRemote) Close() error                { return d.pr.CloseWithError(io.EOF) }
func (d *bridgeRemote) CloseWrite() error {
	d.closeWriteOnce.Do(func() { close(d.closeWrote) })
	return nil
}

// TestBridgeOutputSurvivesStdinEOF is the regression guard for Bridge: a
// non-interactive exec has an immediate stdin EOF, yet the (delayed) process
// output must still be delivered in full, and the remote write side must be
// half-closed (CloseWrite) rather than the whole tunnel torn down. The old
// full-close-on-either-EOF policy closed the remote read side on the instant
// stdin EOF, truncating the output — so this test fails against it.
func TestBridgeOutputSurvivesStdinEOF(t *testing.T) {
	client := &bridgeClient{}
	remote := newBridgeRemote("EXEC_MARKER", 50*time.Millisecond)

	done := make(chan struct{})
	go func() {
		_ = Bridge(client, remote)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Bridge did not return")
	}

	if got := client.output(); got != "EXEC_MARKER" {
		t.Fatalf("bridged output = %q, want EXEC_MARKER (output must survive an immediate stdin EOF)", got)
	}
	select {
	case <-remote.closeWrote:
	default:
		t.Fatal("stdin EOF must half-close the remote write side (CloseWrite), not tear the tunnel down")
	}
	if !client.closed {
		t.Fatal("client conn should be closed after the output stream ends")
	}
}

// TestBridgeStdcopyPassthrough confirms a non-TTY exec's stdcopy-multiplexed
// output is copied through the output direction unchanged.
func TestBridgeStdcopyPassthrough(t *testing.T) {
	client := &bridgeClient{}
	remote := newBridgeRemote("frame-bytes\x00\x01", 0)
	if err := Bridge(client, remote); err != nil {
		t.Fatalf("Bridge: %v", err)
	}
	if got := client.output(); !strings.Contains(got, "frame-bytes") {
		t.Fatalf("bridged output = %q", got)
	}
}
