package wire

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	got, err := ReadLine(strings.NewReader("hello\nrest"))
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if got != "hello" {
		t.Fatalf("ReadLine = %q, want %q", got, "hello")
	}
}

// TestReadLineNoNewlineBounded proves ReadLine refuses to accumulate an unbounded
// buffer from a peer that never sends a newline, instead of growing the heap
// without limit (memory-exhaustion DoS).
func TestReadLineNoNewlineBounded(t *testing.T) {
	// A reader far larger than maxLineLen with no newline in sight.
	r := bytes.NewReader(bytes.Repeat([]byte{'a'}, maxLineLen*4))
	if _, err := ReadLine(r); err == nil {
		t.Fatal("ReadLine over a newline-less stream should return an error, got nil")
	}
}
