package blockcache

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

// originFetcher builds a Fetcher over a fixed byte slice, counting the bytes it
// serves so tests can assert cache hits (no extra origin reads).
type origin struct {
	data  []byte
	reads atomic.Int64 // bytes served
	calls atomic.Int64 // fetch invocations
}

func (o *origin) fetch(off int64, buf []byte) (int, error) {
	o.calls.Add(1)
	if off >= int64(len(o.data)) {
		return 0, io.EOF
	}
	n := copy(buf, o.data[off:])
	o.reads.Add(int64(n))
	if n < len(buf) {
		return n, io.EOF
	}
	return n, nil
}

func seq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func readAll(t *testing.T, c *Cache, id FileID, o *origin, size int64, bufSize int) []byte {
	t.Helper()
	var out []byte
	p := make([]byte, bufSize)
	var off int64
	for {
		n, err := c.ReadAt(id, size, o.fetch, p, off)
		out = append(out, p[:n]...)
		off += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadAt at %d: %v", off, err)
		}
		if n == 0 {
			t.Fatalf("ReadAt made no progress at %d", off)
		}
	}
	return out
}

func TestReadAtRoundTripVariousSizes(t *testing.T) {
	const chunk = 16
	for _, size := range []int{0, 1, 15, 16, 17, 31, 32, 100, 257} {
		data := seq(size)
		o := &origin{data: data}
		c := New(NewMemStore(chunk), chunk)
		id := FileID{Mount: "m", Path: "f", Size: int64(size), MTimeNs: 1}
		for _, bufSize := range []int{1, 7, 16, 40, 300} {
			o.reads.Store(0)
			got := readAll(t, c, id, o, int64(size), bufSize)
			if !bytes.Equal(got, data) {
				t.Fatalf("size=%d buf=%d: content mismatch", size, bufSize)
			}
		}
	}
}

func TestSecondReadIsCacheHit(t *testing.T) {
	const chunk = 64
	data := seq(1000)
	o := &origin{data: data}
	c := New(NewMemStore(chunk), chunk)
	id := FileID{Mount: "m", Path: "f", Size: int64(len(data)), MTimeNs: 1}

	_ = readAll(t, c, id, o, int64(len(data)), 128)
	firstReads := o.reads.Load()
	if firstReads < int64(len(data)) {
		t.Fatalf("first pass read %d bytes, want >= %d", firstReads, len(data))
	}
	// Second full read must serve entirely from cache: no additional origin bytes.
	_ = readAll(t, c, id, o, int64(len(data)), 128)
	if got := o.reads.Load(); got != firstReads {
		t.Fatalf("second pass hit origin: reads went %d -> %d", firstReads, got)
	}
}

func TestPartialLastChunk(t *testing.T) {
	const chunk = 100
	data := seq(250) // last chunk is 50 bytes
	o := &origin{data: data}
	c := New(NewMemStore(chunk), chunk)
	id := FileID{Mount: "m", Path: "f", Size: 250, MTimeNs: 1}
	got := readAll(t, c, id, o, 250, 250)
	if !bytes.Equal(got, data) {
		t.Fatal("partial last chunk mismatch")
	}
	// Read past EOF.
	n, err := c.ReadAt(id, 250, o.fetch, make([]byte, 10), 250)
	if n != 0 || err != io.EOF {
		t.Fatalf("read at EOF: got n=%d err=%v", n, err)
	}
}

func TestReadAtOffsetInsideChunk(t *testing.T) {
	const chunk = 32
	data := seq(200)
	o := &origin{data: data}
	c := New(NewMemStore(chunk), chunk)
	id := FileID{Mount: "m", Path: "f", Size: 200, MTimeNs: 1}
	p := make([]byte, 20)
	n, err := c.ReadAt(id, 200, o.fetch, p, 40) // spans chunk 1 and 2
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p[:n], data[40:40+n]) || n != 20 {
		t.Fatalf("mid-chunk read mismatch n=%d", n)
	}
}

// countingStore wraps MemStore to count Put calls, verifying singleflight
// collapses concurrent misses into a single fetch+store per chunk.
type countingStore struct {
	*MemStore
	puts atomic.Int64
}

func (s *countingStore) Put(id FileID, idx int, data []byte) error {
	s.puts.Add(1)
	return s.MemStore.Put(id, idx, data)
}

func TestConcurrentReadersSingleFetch(t *testing.T) {
	const chunk = 128
	data := seq(chunk) // exactly one chunk
	// Slow origin so all goroutines pile onto the same singleflight call.
	var gate sync.WaitGroup
	gate.Add(1)
	o := &origin{data: data}
	slow := func(off int64, buf []byte) (int, error) {
		gate.Wait()
		return o.fetch(off, buf)
	}
	cs := &countingStore{MemStore: NewMemStore(chunk)}
	c := New(cs, chunk)
	id := FileID{Mount: "m", Path: "f", Size: int64(len(data)), MTimeNs: 1}

	const readers = 32
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := make([]byte, len(data))
			_, err := c.ReadAt(id, int64(len(data)), slow, p, 0)
			if err != nil && err != io.EOF {
				errs[i] = err
				return
			}
			if !bytes.Equal(p, data) {
				errs[i] = errors.New("content mismatch")
			}
		}(i)
	}
	// Let readers converge on the singleflight before releasing the origin.
	gate.Done()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
	if got := cs.puts.Load(); got != 1 {
		t.Fatalf("expected exactly 1 store Put under singleflight, got %d", got)
	}
	if got := o.calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 origin fetch, got %d", got)
	}
}

func TestFetchErrorPropagates(t *testing.T) {
	c := New(NewMemStore(16), 16)
	id := FileID{Mount: "m", Path: "f", Size: 100, MTimeNs: 1}
	want := errors.New("boom")
	fail := func(off int64, buf []byte) (int, error) { return 0, want }
	_, err := c.ReadAt(id, 100, fail, make([]byte, 10), 0)
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
