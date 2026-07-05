package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cornus/pkg/api"
)

// TestDeployLockReleaseRemovesEntry proves the per-name deploy mutex is pruned
// from the map once released, so a server that churns through unique deployment
// names (per-PR preview environments deployed then deleted) does not accumulate
// a permanent mutex per name ever seen.
func TestDeployLockReleaseRemovesEntry(t *testing.T) {
	s := &Server{deployLocks: map[string]*deployLock{}}

	l := s.acquireDeployLock("pr-1")
	if got := len(s.deployLocks); got != 1 {
		t.Fatalf("after acquire: map size = %d, want 1", got)
	}
	s.releaseDeployLock("pr-1", l)
	if got := len(s.deployLocks); got != 0 {
		t.Fatalf("after release: map size = %d, want 0 (entry must be pruned)", got)
	}
}

// TestDeployLockSerializesSameNameAndPrunes hammers the same name from many
// goroutines: the shared counter is mutated only inside the critical section, so
// a lost update (caught by -race, or a wrong final count) would mean the lock did
// not serialise. It also asserts the map is empty afterwards — reference counting
// must never delete a mutex a waiter is about to take, nor leak one.
func TestDeployLockSerializesSameNameAndPrunes(t *testing.T) {
	s := &Server{deployLocks: map[string]*deployLock{}}

	const workers = 64
	var wg sync.WaitGroup
	counter := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := s.acquireDeployLock("web")
			counter++ // guarded by the per-name lock
			s.releaseDeployLock("web", l)
		}()
	}
	wg.Wait()

	if counter != workers {
		t.Fatalf("counter = %d, want %d (lost update => not serialised)", counter, workers)
	}
	if got := len(s.deployLocks); got != 0 {
		t.Fatalf("map size = %d after all releases, want 0", got)
	}
}

// TestGCEndpointConflictWhenRunning proves POST /.cornus/v1/gc participates in the same
// gcRunning no-overlap gate the periodic scheduler uses: if a sweep is already in
// flight the manual request is refused with 409 rather than starting a concurrent
// (not-concurrency-safe) storage.GC, and the refused request leaves the gate set.
func TestGCEndpointConflictWhenRunning(t *testing.T) {
	s := &Server{} // nil apiPolicy => allow-all; nil store is never reached
	s.gcRunning.Store(true)

	req := httptest.NewRequest(http.MethodPost, "/.cornus/v1/gc", nil)
	rec := httptest.NewRecorder()
	s.handleGC(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 while a run is in flight", rec.Code)
	}
	if !s.gcRunning.Load() {
		t.Fatalf("gcRunning was cleared by a refused request; the in-flight run's gate was stolen")
	}
}

// TestReadPreambleRejectsOversizedLine proves readPreamble bounds the preamble
// read: a client that streams data with no newline can no longer make the server
// buffer it without limit (a memory-exhaustion DoS). The read fails once the cap
// is exceeded instead of growing forever.
func TestReadPreambleRejectsOversizedLine(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()

	// Writer streams well past maxPreambleBytes with no '\n'. net.Pipe writes
	// block until read, so run it in a goroutine; it unblocks and exits when the
	// deferred c1.Close tears the pipe down.
	go func() {
		chunk := bytes.Repeat([]byte{'a'}, 4096)
		for {
			if _, err := c2.Write(chunk); err != nil {
				return
			}
		}
	}()

	var cfg api.ExecStartConfig
	if _, err := readPreamble(c1, &cfg); err == nil {
		t.Fatal("readPreamble accepted an unbounded no-newline stream")
	} else if !strings.Contains(err.Error(), "without a newline") {
		t.Fatalf("err = %v, want a preamble-size-cap error", err)
	}
}

// TestReadPreamblePreservesTrailingStream proves the size-bounded reader still
// hands the caller a conn positioned at the raw stream that follows the newline:
// bytes written after the preamble must be readable through the returned
// preambleConn (they may already be buffered while scanning for '\n').
func TestReadPreamblePreservesTrailingStream(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	go func() {
		b, _ := json.Marshal(api.ExecStartConfig{})
		c2.Write(append(b, '\n'))
		c2.Write([]byte("RAWSTREAM"))
	}()

	var cfg api.ExecStartConfig
	pc, err := readPreamble(c1, &cfg)
	if err != nil {
		t.Fatalf("readPreamble: %v", err)
	}
	buf := make([]byte, len("RAWSTREAM"))
	if _, err := io.ReadFull(pc, buf); err != nil {
		t.Fatalf("read trailing stream: %v", err)
	}
	if string(buf) != "RAWSTREAM" {
		t.Fatalf("trailing stream = %q, want %q", buf, "RAWSTREAM")
	}
}
