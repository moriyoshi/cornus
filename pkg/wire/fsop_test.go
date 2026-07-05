package wire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFSOpFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := []byte(`{"op":"list","path":"/data"}`)
	if err := WriteFSOpFrame(&buf, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := WriteFSOpFrame(&buf, nil); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	got, err := ReadFSOpFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = %q, want %q", got, want)
	}
	empty, err := ReadFSOpFrame(&buf)
	if err != nil {
		t.Fatalf("read empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty frame = %q", empty)
	}
}

func TestFSOpFrameRefusesAnOversizedLength(t *testing.T) {
	// A peer-supplied length is an allocation instruction; the cap is what keeps it
	// from being one. Hand-build the header so the check is exercised on the READ
	// side, which is the side that trusts nothing.
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xff, 0xff, 0xff})
	if _, err := ReadFSOpFrame(&buf); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want an exceeds-the-limit refusal", err)
	}
}

func TestFSOpBodyRoundTripsAcrossChunkBoundaries(t *testing.T) {
	// Larger than one chunk, and deliberately not a multiple of it, so the final
	// short chunk is exercised too.
	payload := bytes.Repeat([]byte("cornus"), (fsopChunk/6)*3+17)
	var framed bytes.Buffer
	if err := WriteFSOpBody(&framed, bytes.NewReader(payload)); err != nil {
		t.Fatalf("write body: %v", err)
	}

	var out bytes.Buffer
	if err := ReadFSOpBody(bytes.NewReader(framed.Bytes()), &out); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("body round-trip differs: got %d bytes, want %d", out.Len(), len(payload))
	}

	// The pull-style reader must agree with the push-style one byte for byte.
	pulled, err := io.ReadAll(FSOpBodyReader(bytes.NewReader(framed.Bytes())))
	if err != nil {
		t.Fatalf("body reader: %v", err)
	}
	if !bytes.Equal(pulled, payload) {
		t.Fatalf("FSOpBodyReader disagrees with ReadFSOpBody")
	}
}

// TestFSOpBodyReportsTruncation is the reason the body is framed at all. A tar reader
// handed a stream that simply stopped reports a short but well-formed archive — so
// without the terminator, a copy that died halfway through would land as a smaller
// directory and report success.
func TestFSOpBodyReportsTruncation(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), fsopChunk*2)
	var framed bytes.Buffer
	if err := WriteFSOpBody(&framed, bytes.NewReader(payload)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	// Cut the terminator off, and part of the last chunk with it.
	cut := framed.Bytes()[:framed.Len()-64]

	var out bytes.Buffer
	if err := ReadFSOpBody(bytes.NewReader(cut), &out); !errors.Is(err, ErrFSOpTruncated) {
		t.Fatalf("ReadFSOpBody err = %v, want ErrFSOpTruncated", err)
	}
	if _, err := io.ReadAll(FSOpBodyReader(bytes.NewReader(cut))); !errors.Is(err, ErrFSOpTruncated) {
		t.Fatalf("FSOpBodyReader err = %v, want ErrFSOpTruncated", err)
	}
}

// TestFSOpBodyDistinguishesEmptyFromTruncated pins the pair the terminator exists to
// separate: zero bytes that finished, versus zero bytes because the peer died.
func TestFSOpBodyDistinguishesEmptyFromTruncated(t *testing.T) {
	var framed bytes.Buffer
	if err := WriteFSOpBody(&framed, bytes.NewReader(nil)); err != nil {
		t.Fatalf("write empty body: %v", err)
	}
	var out bytes.Buffer
	if err := ReadFSOpBody(bytes.NewReader(framed.Bytes()), &out); err != nil {
		t.Fatalf("empty body: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty body produced %d bytes", out.Len())
	}
	if err := ReadFSOpBody(bytes.NewReader(nil), &out); !errors.Is(err, ErrFSOpTruncated) {
		t.Fatalf("no body at all: err = %v, want ErrFSOpTruncated", err)
	}
}

// TestFSOpStreamsAreBulkClassed keeps a multi-gigabyte copy from being scheduled ahead
// of the hub's control channel. It is a one-line property that no other test observes.
func TestFSOpStreamsAreBulkClassed(t *testing.T) {
	if got, want := streamClassForTag(tagFSOp), streamClassForTag(tagMount); got != want {
		t.Fatalf("fsop stream class = %d, want the bulk class %d", got, want)
	}
	if streamClassForTag(tagFSOp) == streamClassForTag(tagAgentRelay) {
		t.Fatalf("fsop is still on the default (normal) class")
	}
}
