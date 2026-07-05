// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package yamux

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// Tests for the cornus fork's batched-pipelined send path (Config.SendMode ==
// SendBatchedPipelined). The classic path is covered by the upstream suite; these
// prove the alternative path is byte-for-byte transparent — frames fragment,
// reassemble, and stay ordered while a stream keeps many frames in flight and
// control frames still interleave. Run: `cd third_party/yamux && go test -race`.

// batchedPair builds a client+server session over an in-memory pipe with the
// batched-pipelined send path enabled on both ends. mut (may be nil) further
// mutates the shared config (e.g. a small frame cap or a large window).
func batchedPair(t *testing.T, mut func(*Config)) (*Session, *Session) {
	t.Helper()
	conf := testConfNoKeepAlive()
	conf.SendMode = SendBatchedPipelined
	if mut != nil {
		mut(conf)
	}
	clientPipe, serverPipe := testConnPipe(t)
	return testClientServerConfig(t, clientPipe, serverPipe, conf.Clone(), conf.Clone())
}

// patternData returns size bytes where each byte encodes its index, so a receiver
// can detect any loss, duplication, or reordering.
func patternData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251) // 251 is prime => long non-aligned period
	}
	return data
}

// TestBatchedPipelined_Integrity writes a multi-MiB known pattern through one
// stream (fragmenting into many DATA frames) and verifies it arrives intact and
// in order over the batched-pipelined path.
func TestBatchedPipelined_Integrity(t *testing.T) {
	sizes := []int{1 << 20, 8 << 20}
	if testing.Short() {
		sizes = []int{256 << 10}
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dKiB", size>>10), func(t *testing.T) {
			client, server := batchedPair(t, func(c *Config) { c.MaxStreamWindowSize = 1 << 20 })
			data := patternData(size)

			errCh := make(chan error, 2)
			go func() {
				stream, err := server.AcceptStream()
				if err != nil {
					errCh <- err
					return
				}
				got := make([]byte, size)
				if _, err := io.ReadFull(stream, got); err != nil {
					errCh <- err
					return
				}
				if !bytes.Equal(got, data) {
					errCh <- fmt.Errorf("payload mismatch at first diff %d", firstDiff(got, data))
					return
				}
				errCh <- nil
			}()
			go func() {
				stream, err := client.OpenStream()
				if err != nil {
					errCh <- err
					return
				}
				if _, err := stream.Write(data); err != nil {
					errCh <- err
					return
				}
				errCh <- stream.Close()
			}()
			drainErrorsUntil(t, errCh, 2, 15*time.Second, "timeout")
		})
	}
}

// TestBatchedPipelined_SmallFrameCap forces heavy fragmentation with an odd,
// non-power-of-two frame cap so a single Write becomes many small frames that the
// coalescing loop must reassemble faithfully.
func TestBatchedPipelined_SmallFrameCap(t *testing.T) {
	const size = 1 << 20
	client, server := batchedPair(t, func(c *Config) {
		c.MaxDataFrame = 3000 // odd cap: exercises fragmentation + coalescing edges
		c.MaxStreamWindowSize = 512 << 10
	})
	data := patternData(size)

	errCh := make(chan error, 2)
	go func() {
		stream, err := server.AcceptStream()
		if err != nil {
			errCh <- err
			return
		}
		got := make([]byte, size)
		if _, err := io.ReadFull(stream, got); err != nil {
			errCh <- err
			return
		}
		if !bytes.Equal(got, data) {
			errCh <- fmt.Errorf("payload mismatch at first diff %d", firstDiff(got, data))
			return
		}
		errCh <- nil
	}()
	go func() {
		stream, err := client.OpenStream()
		if err != nil {
			errCh <- err
			return
		}
		if _, err := stream.Write(data); err != nil {
			errCh <- err
			return
		}
		errCh <- stream.Close()
	}()
	drainErrorsUntil(t, errCh, 2, 15*time.Second, "timeout")
}

// TestBatchedPipelined_ConcurrentStreams runs several streams at once, each
// carrying a distinct pattern echoed by the server, and verifies every stream's
// bytes are intact and unmixed — per-stream ordering under the shared batched loop.
func TestBatchedPipelined_ConcurrentStreams(t *testing.T) {
	const (
		nStreams = 8
		perSize  = 512 << 10
	)
	client, server := batchedPair(t, func(c *Config) { c.MaxStreamWindowSize = 1 << 20 })

	// Server: accept each stream and echo it back.
	go func() {
		for {
			s, err := server.AcceptStream()
			if err != nil {
				return
			}
			go func(s *Stream) { _, _ = io.Copy(s, s) }(s)
		}
	}()

	var wg sync.WaitGroup
	errCh := make(chan error, nStreams)
	for i := 0; i < nStreams; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			s, err := client.OpenStream()
			if err != nil {
				errCh <- err
				return
			}
			defer s.Close()
			// Distinct per-stream pattern: byte = (index + seed) so a cross-stream mix
			// would corrupt the check.
			data := make([]byte, perSize)
			for j := range data {
				data[j] = byte((j + seed) % 251)
			}
			done := make(chan error, 1)
			go func() {
				got := make([]byte, perSize)
				_, err := io.ReadFull(s, got)
				if err == nil && !bytes.Equal(got, data) {
					err = fmt.Errorf("stream %d payload mismatch at %d", seed, firstDiff(got, data))
				}
				done <- err
			}()
			if _, err := s.Write(data); err != nil {
				errCh <- err
				return
			}
			errCh <- <-done
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestBatchedPipelined_PingUnderBulk saturates the session with a bulk stream and
// verifies control frames (ClassUrgent pings) still complete — the batched loop
// must not starve or deadlock control behind pipelined bulk data.
func TestBatchedPipelined_PingUnderBulk(t *testing.T) {
	client, server := batchedPair(t, func(c *Config) { c.MaxStreamWindowSize = 1 << 20 })

	// Server drains everything.
	go func() {
		for {
			s, err := server.AcceptStream()
			if err != nil {
				return
			}
			go func(s *Stream) { _, _ = io.Copy(io.Discard, s) }(s)
		}
	}()

	bulk, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	bulk.SetPriority(ClassBulk)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer bulk.Close()
		buf := make([]byte, 256<<10)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := bulk.Write(buf); err != nil {
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	time.Sleep(50 * time.Millisecond) // let the bulk stream ramp
	for i := 0; i < 10; i++ {
		if _, err := client.Ping(); err != nil {
			t.Fatalf("ping %d under bulk load failed: %v", i, err)
		}
	}
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}
