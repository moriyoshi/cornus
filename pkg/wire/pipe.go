package wire

import (
	"errors"
	"io"
	"sync"
)

// maxLineLen caps the length ReadLine will accumulate before giving up. The name
// lines it reads (context name, tag) are tiny; this bound is far larger than any
// legitimate value but stops a peer that never sends a newline from driving
// unbounded heap growth.
const maxLineLen = 4096

// pipe copies bidirectionally between a and b until either side closes. Takes
// io.ReadWriteCloser (not net.Conn) so it also accepts a plain
// io.ReadWriteCloser tunnel conn (e.g. ForwardPort's) alongside a yamux
// stream — neither of which need to be a full net.Conn for this.
func pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { a.Close(); b.Close() }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(a, b); once.Do(closeBoth) }()
	go func() { defer wg.Done(); _, _ = io.Copy(b, a); once.Do(closeBoth) }()
	wg.Wait()
}

// ReadLine reads bytes up to a newline without over-reading (so the rest of the
// stream stays intact for proxying).
func ReadLine(r io.Reader) (string, error) {
	var b []byte
	var one [1]byte
	for {
		if _, err := io.ReadFull(r, one[:]); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(b), nil
		}
		if len(b) >= maxLineLen {
			return "", errors.New("wire: line exceeds maximum length without newline")
		}
		b = append(b, one[0])
	}
}
