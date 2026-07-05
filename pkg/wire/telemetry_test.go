package wire

import (
	"errors"
	"net"
	"testing"
	"time"
)

// TestTelemetryFrameRoundTrip pins the framing both ends must agree on. The
// signal name and body cross a length-prefixed frame, and the reader must
// recover them exactly — a mismatch here corrupts every export silently, since
// neither side ever inspects the OTLP payload.
func TestTelemetryFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		signal string
		body   []byte
	}{
		{"logs", "logs", []byte("opaque-otlp")},
		{"traces", "traces", []byte{0x00, 0xff, 0x10, 0x00}}, // binary, including NULs
		{"empty body", "metrics", []byte{}},
		{"large-ish", "logs", make([]byte, 1<<16)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			go func() {
				_, _ = client.Write([]byte(c.signal + "\n"))
				n := len(c.body)
				_, _ = client.Write([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
				_, _ = client.Write(c.body)
			}()

			_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
			signal, body, err := ReadTelemetryHeader(server)
			if err != nil {
				t.Fatalf("ReadTelemetryHeader: %v", err)
			}
			if signal != c.signal {
				t.Errorf("signal = %q, want %q", signal, c.signal)
			}
			if string(body) != string(c.body) {
				t.Errorf("body round-trip lost data: got %d bytes, want %d", len(body), len(c.body))
			}
		})
	}
}

// TestTelemetryRejectsOversizedFrame keeps a declared length from being used to
// make the server allocate arbitrarily. The cap is checked BEFORE the allocation,
// which is the whole point.
func TestTelemetryRejectsOversizedFrame(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = client.Write([]byte("logs\n"))
		n := uint32(telemetryMaxBody + 1)
		_, _ = client.Write([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
	}()

	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := ReadTelemetryHeader(server); err == nil {
		t.Fatal("an oversized declared length was accepted")
	}
}

// TestTelemetryAckVerdicts pins the three outcomes the caretaker maps back onto
// HTTP statuses for its local Collector. Getting retry and reject confused would
// either lose exports the server asked to have re-sent, or loop forever on ones
// it will never take.
func TestTelemetryAckVerdicts(t *testing.T) {
	cases := []struct {
		ack     byte
		wantErr bool
		retry   bool
	}{
		{TelemetryAckOK, false, false},
		{TelemetryAckRetry, true, true},
		{TelemetryAckReject, true, false},
		{'?', true, false}, // an unknown verdict is not a success
	}
	for _, c := range cases {
		client, server := net.Pipe()
		go func(ack byte) { _ = WriteTelemetryAck(server, ack); server.Close() }(c.ack)

		err := readTelemetryAck(client)
		client.Close()

		if (err != nil) != c.wantErr {
			t.Errorf("ack %q: err = %v, wantErr %v", c.ack, err, c.wantErr)
			continue
		}
		if got := errors.Is(err, ErrTelemetryRetry); got != c.retry {
			t.Errorf("ack %q: retry = %v, want %v", c.ack, got, c.retry)
		}
	}
}

// TestTelemetryAckTimesOut keeps a server that never answers from wedging the
// caretaker's export handler, and with it the Collector's export queue.
func TestTelemetryAckTimesOut(t *testing.T) {
	restore := telemetryAckTimeout
	telemetryAckTimeout = 50 * time.Millisecond
	defer func() { telemetryAckTimeout = restore }()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close() // the server side never writes an ack

	done := make(chan error, 1)
	go func() { done <- readTelemetryAck(client) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent server produced a successful ack")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readTelemetryAck did not honor its deadline")
	}
}
