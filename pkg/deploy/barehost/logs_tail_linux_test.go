package barehost

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// writeLogFile writes body to a temp file and returns it opened for reading,
// positioned at the start, the way readLogs opens an instance's log.
func writeLogFile(t *testing.T, body string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// readRest returns everything left in f from its current offset — i.e. exactly
// what readLogs would go on to copy to the caller.
func readRest(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSeekToLastLinesTailZeroReplaysNothing is the regression test for a real
// defect: seekToLastLines treated n <= 0 as "keep the whole file", but Attach
// passes Tail:"0" to mean "no history" when cfg.Logs is false. So `attach`
// without logs replayed the ENTIRE log before streaming live output, and a
// `Logs` call with Tail:"0" printed everything instead of nothing — the
// opposite of docker's `--tail 0`. Zero must seek to EOF; only a negative tail
// means "whole file".
func TestSeekToLastLinesTailZeroReplaysNothing(t *testing.T) {
	const body = "one\ntwo\nthree\n"
	f := writeLogFile(t, body)
	if err := seekToLastLines(f, 0); err != nil {
		t.Fatalf("seekToLastLines(0): %v", err)
	}
	if got := readRest(t, f); got != "" {
		t.Fatalf("tail 0 replayed %q, want nothing", got)
	}
}

// TestSeekToLastLines covers the surrounding vocabulary so the zero case above
// cannot be "fixed" by breaking a neighbour.
func TestSeekToLastLines(t *testing.T) {
	const body = "one\ntwo\nthree\n"

	tests := []struct {
		name string
		body string
		n    int
		want string
	}{
		{"negative keeps the whole file", body, -1, body},
		{"tail 1 keeps the last line", body, 1, "three\n"},
		{"tail 2 keeps the last two", body, 2, "two\nthree\n"},
		{"tail at line count keeps all", body, 3, body},
		{"tail beyond line count keeps all", body, 99, body},
		{"tail 0 on an empty file", "", 0, ""},
		{"negative on an empty file", "", -1, ""},
		{"tail 1 with no trailing newline", "one\ntwo", 1, "two"},
		{"tail 0 with no trailing newline", "one\ntwo", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := writeLogFile(t, tt.body)
			if err := seekToLastLines(f, tt.n); err != nil {
				t.Fatalf("seekToLastLines(%d): %v", tt.n, err)
			}
			if got := readRest(t, f); got != tt.want {
				t.Fatalf("seekToLastLines(%d) left %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

// TestSeekToLastLinesTailZeroThenFollow proves the follow case the Attach path
// actually depends on: after a zero tail the offset sits at EOF, so content
// appended AFTER the seek is still delivered. A fix that suppressed history by
// refusing to read at all would break live streaming; this pins that it does not.
func TestSeekToLastLinesTailZeroThenFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("old-one\nold-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := seekToLastLines(f, 0); err != nil {
		t.Fatalf("seekToLastLines(0): %v", err)
	}
	// Append after the seek, as a running workload would.
	af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := af.WriteString("live-one\n"); err != nil {
		t.Fatal(err)
	}
	af.Close()

	if got := readRest(t, f); got != "live-one\n" {
		t.Fatalf("after tail 0 + append, read %q, want only the appended line", got)
	}
}
