package cliout

import (
	"bytes"
	"sync"
	"testing"

	"cornus/pkg/build/buildprog"

	"github.com/creack/pty"
)

// Regression guard for "cornus (compose) build emits no build output on a real
// terminal": in fancy mode over a TTY, BuildProgress must still deliver every
// RUN log line and step transition to stderr. A previous revision drove build
// output through the live bubbletea Progress, which drops queued tea.Println
// messages when its render loop stalls on a terminal query (as on TERM=xterm) —
// so a fast/early-failing build printed nothing. Here the PTY deliberately never
// answers any query, reproducing that stall; the output must survive anyway.
func TestBuildProgressEmitsOverPTY(t *testing.T) {
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
		t.Fatalf("expected fancy+errTTY over a pty, got mode=%v errTTY=%v", d.mode, d.errTTY)
	}

	sink, finish := d.BuildProgress()
	// The regression was structural: build output was routed through the live
	// bubbletea Progress, which silently drops it when its render loop stalls or is
	// torn down early. Guard the fix directly — BuildProgress must NOT stand up a
	// live program; it writes straight to stderr. (A timing-based reproduction of
	// the drop is too env-dependent to assert on: in-process bubbletea renders
	// instantly and reads the process TERM, so it would not stall here.)
	if d.activeProgram() != nil {
		t.Fatal("BuildProgress started a live program; build output must go straight to stderr so it cannot be dropped")
	}
	// A fast build: a step starts, emits RUN output, then everything completes and
	// finish() fires immediately — no settle time, the shape that used to drop.
	sink.Call(buildprog.Event{Vertex: "[1/2] RUN echo hi", Status: "start"})
	sink.Log("BUILD-OUTPUT-MARKER\n")
	sink.Call(buildprog.Event{Vertex: "[1/2] RUN echo hi", Status: "done"})
	sink.Call(buildprog.Event{Vertex: "[2/2] COPY . .", Status: "cached"})
	finish()

	tty.Close()
	<-done

	mu.Lock()
	out := stripANSI(buf.String())
	mu.Unlock()
	for _, want := range []string{"BUILD-OUTPUT-MARKER", "[1/2] RUN echo hi", "[2/2] COPY . ."} {
		if !contains(out, want) {
			t.Errorf("build output missing %q on a fancy TTY; got:\n%q", want, out)
		}
	}
}
