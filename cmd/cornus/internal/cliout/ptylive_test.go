package cliout

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Drives the real bubbletea live path over a PTY (both streams a terminal, so
// mode resolves to fancy and Progress goes live). Confirms spinners render, a
// Done line and a notice print above the region, and teardown is clean.
func TestLiveProgressOverPTY(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	var buf bytes.Buffer
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				close(done)
				return
			}
		}
	}()

	d := New(Options{Stdout: tty, Stderr: tty, Stdin: tty, Output: "fancy", Env: func(string) string { return "" }})
	if d.mode != ModeFancy || !d.errTTY {
		t.Fatalf("expected fancy+errTTY over pty, got mode=%v errTTY=%v", d.mode, d.errTTY)
	}
	p := d.Progress()
	if p.live == nil {
		t.Fatal("Progress did not go live over a pty")
	}
	task := p.Task("building web")
	p.SetFraction(0.5)
	time.Sleep(120 * time.Millisecond) // let a few spinner frames render
	d.Warn("heads up")                 // must print above the region
	task.Done("built web")             // permanent ✓ line above
	time.Sleep(40 * time.Millisecond)
	p.Stop()

	// Close the tty so the reader goroutine ends, then wait for it.
	tty.Close()
	<-done

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	plain := stripANSI(out)
	for _, want := range []string{"building web", "heads up", "built web"} {
		if !contains(plain, want) {
			t.Errorf("pty output missing %q; got:\n%q", want, plain)
		}
	}
	if !bytes.Contains([]byte(out), []byte{0x1b}) {
		t.Errorf("expected ANSI control sequences in live output")
	}
	_ = io.Discard
	_ = os.Stdout
}

func contains(hay, needle string) bool { return bytes.Contains([]byte(hay), []byte(needle)) }
