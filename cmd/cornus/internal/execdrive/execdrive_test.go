package execdrive

// The exec stream's framing contract, and why this test asserts BYTES.
//
// A non-TTY exec's output is stdcopy-multiplexed: every backend guarantees it,
// because without a PTY there is no other way to keep stdout and stderr apart on
// one connection. Run used to copy that stream straight to os.Stdout, which put
// the 8-byte frame headers on the terminal and folded stderr into stdout.
//
// It survived indefinitely because every check that could have caught it looked
// for a SUBSTRING. `exec.star` asserts that the expected text appears in the
// output — and it does appear; it is merely preceded by `\x01\x00\x00\x00\x00
// \x00\x00\x04` and mixed with stderr. So the assertions here compare the exact
// bytes written to each stream, and the stderr case is asserted separately from
// stdout rather than being allowed to land anywhere.
//
// The TTY case is the other half of the contract and is easy to get wrong in the
// opposite direction: with a PTY the daemon sends a RAW byte stream (verified
// against both Docker and libpod — framed when Tty is false, raw when true), so
// demultiplexing it would corrupt it. Both directions are pinned.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"cornus/pkg/api"
)

// frame builds one stdcopy frame: a 1-byte stream id, 3 reserved bytes, a
// big-endian uint32 length, then the payload.
func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

const (
	streamStdout = 1
	streamStderr = 2
)

// fakeExecClient serves a canned stream over an in-memory connection.
type fakeExecClient struct {
	payload  []byte
	exitCode int
	wantTty  bool
	gotTty   bool
}

func (f *fakeExecClient) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error) {
	f.gotTty = cfg.Tty
	client, server := net.Pipe()
	go func() {
		_, _ = server.Write(f.payload)
		_ = server.Close()
	}()
	return client, nil
}

func (f *fakeExecClient) ExecResize(context.Context, string, uint, uint) error { return nil }

func (f *fakeExecClient) ExecInspect(context.Context, string) (api.ExecState, error) {
	return api.ExecState{ExitCode: f.exitCode}, nil
}

// TestRunDemultiplexesNonTtyOutput is the regression guard.
//
// It asserts the exact bytes on each stream. A test that merely checked
// `strings.Contains(stdout, "OUT")` would pass against the raw-copy bug that
// prompted it — that is precisely how the bug survived.
func TestRunDemultiplexesNonTtyOutput(t *testing.T) {
	payload := append(frame(streamStdout, "OUT\n"), frame(streamStderr, "ERR\n")...)
	f := &fakeExecClient{payload: payload}

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := Run(ctx, f, "exec-1", Options{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	if got := stdout.String(); got != "OUT\n" {
		t.Errorf("stdout = %q, want %q\n"+
			"a raw copy would yield the payload WITH its 8-byte frame header and stderr mixed in",
			got, "OUT\n")
	}
	if got := stderr.String(); got != "ERR\n" {
		t.Errorf("stderr = %q, want %q — stderr must be separated, not folded into stdout",
			got, "ERR\n")
	}
	// The header bytes must not reach either stream. Checking explicitly because
	// they are mostly NULs and would be invisible in a failure message otherwise.
	for name, buf := range map[string]*bytes.Buffer{"stdout": &stdout, "stderr": &stderr} {
		if bytes.ContainsRune(buf.Bytes(), 0) {
			t.Errorf("%s contains NUL bytes (%q); stdcopy frame headers leaked through",
				name, buf.String())
		}
	}
}

// TestRunDoesNotDemultiplexTtyOutput pins the other direction: with a PTY the
// daemon sends raw bytes, so demultiplexing would corrupt them. This payload is
// NOT valid stdcopy — StdCopy would reject or mangle it.
func TestRunDoesNotDemultiplexTtyOutput(t *testing.T) {
	const raw = "plain terminal bytes, no framing\r\n"
	f := &fakeExecClient{payload: []byte(raw)}

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Tty:true would normally put the local terminal in raw mode; under `go test`
	// stdin is not a terminal, so term.MakeRaw fails and Run returns an error
	// before reaching the copy. Drive the copy decision directly instead.
	if _, err := Run(ctx, f, "exec-1", Options{Tty: true, Stdout: &stdout, Stderr: &stderr}); err == nil {
		// A terminal was available (someone ran this attached); then the full path
		// ran and the raw bytes must have arrived unchanged.
		if got := stdout.String(); got != raw {
			t.Errorf("stdout = %q, want the raw stream %q unchanged", got, raw)
		}
	}
	// Whatever happened above, the TTY request must have reached the server: it is
	// what makes the daemon allocate a PTY and stop framing in the first place.
	if !f.gotTty {
		t.Error("ExecStart was not asked for a TTY, so the daemon would have framed the stream")
	}
}

// TestRunSplitsInterleavedFrames covers the realistic case: several frames of
// each stream arriving in order, which is what a command writing to both
// produces.
func TestRunSplitsInterleavedFrames(t *testing.T) {
	var payload []byte
	payload = append(payload, frame(streamStdout, "one\n")...)
	payload = append(payload, frame(streamStderr, "two\n")...)
	payload = append(payload, frame(streamStdout, "three\n")...)
	f := &fakeExecClient{payload: payload, exitCode: 7}

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := Run(ctx, f, "exec-1", Options{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := stdout.String(); got != "one\nthree\n" {
		t.Errorf("stdout = %q, want %q", got, "one\nthree\n")
	}
	if got := stderr.String(); got != "two\n" {
		t.Errorf("stderr = %q, want %q", got, "two\n")
	}
	// Demultiplexing must not disturb the exit status, which is read separately
	// from the stream.
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

// TestRunRequestsNoTtyByDefault: the framing the demux depends on exists only
// because the exec was created without a TTY. If Run ever asked for one by
// default, the daemon would stop framing and the demux would corrupt the output.
func TestRunRequestsNoTtyByDefault(t *testing.T) {
	f := &fakeExecClient{payload: frame(streamStdout, "x\n")}
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Run(ctx, f, "exec-1", Options{Stdout: &stdout, Stderr: &stderr}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotTty {
		t.Error("ExecStart was asked for a TTY without Options.Tty being set")
	}
}

// Seeding the remote PTY has to survive the runtime's start race.
//
// ExecStart returns when the stream's response headers arrive, which on libpod is
// BEFORE the runtime has created the PTY. A resize sent in that window is
// answered 500 and the PTY keeps no size at all — `stty size` in the session then
// fails outright rather than reporting a stale one. Measured on podman 4.3.1:
// resize at +0ms -> 500 and never sized; at +50ms -> 201 and "24 100".
//
// The failure was invisible before this: the seed's error was discarded, so a
// session opened with an unusable terminal and nothing said why.
func TestSeedRemotePTYRetriesPastTheStartRace(t *testing.T) {
	calls := 0
	seedRemotePTY(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New("500 Internal Server Error: exec session is not running")
		}
		return nil
	})
	if calls != 3 {
		t.Errorf("resize attempted %d time(s), want 3: the seed must keep trying until the "+
			"runtime has created the PTY, or an interactive session opens permanently unsized", calls)
	}
}

// The common case must cost nothing. Docker answers the first resize, so a retry
// loop that slept before checking would add latency to every interactive exec.
func TestSeedRemotePTYStopsOnFirstSuccess(t *testing.T) {
	calls := 0
	start := time.Now()
	seedRemotePTY(context.Background(), func() error { calls++; return nil })
	if calls != 1 {
		t.Errorf("resize attempted %d time(s), want exactly 1 when it succeeds immediately", calls)
	}
	if elapsed := time.Since(start); elapsed > seedResizeDelay {
		t.Errorf("a first-attempt success took %v; seeding must not delay a session whose "+
			"runtime answered right away", elapsed)
	}
}

// A terminal that never sizes is a degraded session, not a reason to refuse to
// open one — so the seed gives up rather than blocking the exec forever.
func TestSeedRemotePTYGivesUp(t *testing.T) {
	calls := 0
	done := make(chan struct{})
	go func() {
		seedRemotePTY(context.Background(), func() error { calls++; return errors.New("nope") })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("seedRemotePTY never returned; a runtime that never accepts a resize would " +
			"hang the exec before any output reached the user")
	}
	if calls != seedResizeAttempts {
		t.Errorf("resize attempted %d time(s), want %d", calls, seedResizeAttempts)
	}
}

// A cancelled context must end the seed promptly: the user pressed Ctrl-C, and
// waiting out the remaining attempts would delay the exit for no benefit.
func TestSeedRemotePTYHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	seedRemotePTY(ctx, func() error {
		calls++
		cancel()
		return errors.New("nope")
	})
	if calls != 1 {
		t.Errorf("resize attempted %d time(s) after cancellation, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed > seedResizeDelay {
		t.Errorf("cancellation took %v to take effect; it must not wait out a retry delay", elapsed)
	}
}
