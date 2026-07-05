package setupwiz

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/clientconfig"
	"cornus/pkg/svcforward"
)

// promptTimeout bounds every wait for a prompt. A wizard that never asks the
// next question must FAIL, not hang: an end-to-end test that blocks forever on a
// missing prompt is worse than no test at all, so nothing here reads without a
// deadline.
const promptTimeout = 10 * time.Second

// ptySession drives the wizard through a real terminal: a pty pair with the
// driver's three streams bound to the slave (so the mode resolves to fancy and
// NewUI picks the bubbletea dialogs), and the test writing keys to / reading
// rendered bytes from the master.
type ptySession struct {
	t     *testing.T
	ptmx  *os.File
	tty   *os.File
	mu    sync.Mutex
	buf   bytes.Buffer
	read  chan struct{} // closed when the reader goroutine ends
	stdin int           // cursor into the stripped output, so expect() only ever
	// matches text rendered AFTER the previous match (a prompt title also appears
	// in the transcript line printed once it is answered).
}

func newPTYSession(t *testing.T) *ptySession {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	// A zero-sized window makes the renderer's wrapping arbitrary; pin it.
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Skipf("pty setsize: %v", err)
	}
	s := &ptySession{t: t, ptmx: ptmx, tty: tty, read: make(chan struct{})}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := ptmx.Read(b)
			if n > 0 {
				s.mu.Lock()
				s.buf.Write(b[:n])
				s.mu.Unlock()
			}
			if err != nil {
				close(s.read)
				return
			}
		}
	}()
	t.Cleanup(func() {
		tty.Close()
		ptmx.Close()
		<-s.read
	})
	return s
}

// output returns everything rendered so far, with ANSI control sequences removed.
func (s *ptySession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stripANSISeqs(s.buf.String())
}

// expect waits until want is rendered after the previous match, or fails with
// everything captured so far.
func (s *ptySession) expect(want string) {
	s.t.Helper()
	deadline := time.Now().Add(promptTimeout)
	for {
		out := s.output()
		if s.stdin <= len(out) {
			if i := strings.Index(out[s.stdin:], want); i >= 0 {
				s.stdin += i + len(want)
				return
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out after %s waiting for %q.\n--- output so far ---\n%s", promptTimeout, want, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// send types keys at the terminal. It is only ever called after expect saw the
// prompt, which is what guarantees the program is already reading in raw mode.
func (s *ptySession) send(keys string) {
	s.t.Helper()
	if _, err := s.ptmx.Write([]byte(keys)); err != nil {
		s.t.Fatalf("write %q to pty: %v\n--- output so far ---\n%s", keys, err, s.output())
	}
}

// raw returns the unstripped bytes (to assert the live path really emitted ANSI).
func (s *ptySession) raw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// stripANSISeqs removes CSI escape sequences (the setupwiz-local twin of
// cliout's stripANSI, which is unexported there).
func stripANSISeqs(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestWizardOverPTY drives one short path (scenario pick + three answers) end to
// end through a real terminal: the rich bubbletea dialogs, real key decoding, and
// the driver's fancy rendering — the layer the Update/View unit tests cannot
// reach. Every wait is bounded (see promptTimeout), so a missing prompt fails
// with the captured output instead of hanging.
func TestWizardOverPTY(t *testing.T) {
	s := newPTYSession(t)
	path := filepath.Join(t.TempDir(), "config.yaml")

	d := cliout.New(cliout.Options{
		Stdout: s.tty, Stderr: s.tty, Stdin: s.tty,
		Output: "fancy",
		Env:    func(string) string { return "" },
	})
	if d.Mode() != cliout.ModeFancy || !d.InTTY() || !d.ErrTTY() {
		t.Fatalf("expected a fully interactive driver over a pty: mode=%v inTTY=%v errTTY=%v", d.Mode(), d.InTTY(), d.ErrTTY())
	}
	ui := NewUI(d)
	if _, ok := ui.(*teaUI); !ok {
		t.Fatalf("NewUI over a pty = %T, want the rich teaUI", ui)
	}

	w := NewWizard(d, ui, path)
	// Hermetic seams: nothing here may touch the network, a cluster, or the
	// developer's ssh_config.
	w.Discover = func(context.Context, svcforward.DiscoverOptions) (svcforward.DiscoverResult, error) {
		return svcforward.DiscoverResult{}, os.ErrNotExist
	}
	w.Verify = func(context.Context, string, string) VerifyResult {
		return VerifyResult{OK: true, Detail: "ok"}
	}
	w.Ingress = func(context.Context, *Answers) IngressFacts { return IngressFacts{} }
	w.SSHHosts = nil

	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background()) }()

	// Declining "already set up" skips the verify prompt, so this is the shortest
	// complete path that still exercises the interesting bits: the one step that
	// renders two prompts of its own (the confirm and, on a "no", the runtime
	// picker), and — because Bare is the daemonless backend where cornus itself
	// supervises the workloads — the systemd unit offer that follows.
	s.expect("Which deployment scenario are you configuring?")
	s.expect("Local server")
	s.send("\r") // take the highlighted first option

	s.expect("Is the cornus server already set up?")
	s.send("n")

	s.expect("Which container runtime will this server drive?")
	s.expect("Bare (no daemon)")
	// Arrow presses are DERIVED from the picker order, not counted by hand: an
	// inserted backend shifts every later option, and a hardcoded count would
	// then select a different runtime while every assertion still passed.
	s.send(strings.Repeat("\x1b[B", backendIndex(backendBare)) + "\r")

	s.expect("Server URL")
	s.send("\r") // accept the default

	s.expect("Context name")
	s.send("ptybox\r") // typed, not defaulted: proves real key decoding

	s.expect("Make this the current (default) context?")
	s.send("y")

	// The daemonless local server is offered a unit; skip writing one here.
	s.expect("Setup artifact: cornus.service")
	s.send("\x1b[B\x1b[B\r") // down, down, enter: Skip

	s.expect("setup complete for context")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wizard: %v\n--- output ---\n%s", err, s.output())
		}
	case <-time.After(promptTimeout):
		t.Fatalf("wizard did not return after the final prompt.\n--- output ---\n%s", s.output())
	}

	// The rendered transcript records the answers the terminal actually delivered.
	out := s.output()
	for _, want := range []string{
		"Which deployment scenario are you configuring?: Local server",
		"Is the cornus server already set up?: no",
		"Which container runtime will this server drive?: Bare (no daemon)",
		"Server URL: http://127.0.0.1:5000",
		"Context name: ptybox",
		// The "no" answer must reach the guide, over a real terminal too.
		"No server yet",
		"CORNUS_DEPLOY_BACKEND=bare",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pty transcript missing %q\n--- output ---\n%s", want, out)
		}
	}
	if !strings.Contains(s.raw(), "\x1b[") {
		t.Error("expected ANSI control sequences from the rich UI over a pty")
	}

	// And the answers reached disk as the profile the flow describes.
	f, err := clientconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := f.Contexts["ptybox"]
	if c == nil || c.Server != "http://127.0.0.1:5000" {
		t.Fatalf("context saved over the pty run: %+v\n--- output ---\n%s", c, out)
	}
	if f.CurrentContext != "ptybox" {
		t.Errorf("current-context = %q, want ptybox", f.CurrentContext)
	}
}
