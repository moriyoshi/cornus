package wire

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

// MaxDatagram is the largest datagram the length-prefixed codec can carry: the
// 2-byte big-endian length prefix caps a payload at 65535 bytes (a UDP payload
// never exceeds this).
const MaxDatagram = 0xffff

// WriteDatagram frames one datagram onto a byte stream: a 2-byte big-endian
// uint16 length prefix followed by the payload. It preserves message boundaries
// so UDP datagrams survive a byte-stream relay (yamux). A payload larger than
// MaxDatagram is rejected. An empty payload is written as a zero-length frame.
func WriteDatagram(w io.Writer, b []byte) error {
	if len(b) > MaxDatagram {
		return fmt.Errorf("wire: datagram too large: %d > %d", len(b), MaxDatagram)
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	_, err := w.Write(b)
	return err
}

// ReadDatagram reads one length-prefixed datagram written by WriteDatagram: the
// 2-byte length, then exactly that many bytes. A truncated frame (either the
// length header or the payload) returns an error (io.ErrUnexpectedEOF for a
// short payload). A zero-length frame returns an empty, non-nil slice.
func ReadDatagram(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(hdr[:])
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// BridgeDatagram couples a framed byte stream (length-prefixed datagrams, e.g. a
// yamux hub stream) with a datagram-oriented net.Conn (a connected UDP socket):
// datagrams read off stream are written whole to conn, and datagrams read off
// conn are framed back onto stream. It is the UDP analogue of Pipe — used at the
// hub's dial-direct end and the delivering spoke's far end to convert between the
// framed relay and a real UDP socket. Returns when either side ends; both are
// closed. One conn.Read yields one datagram, so no boundary is lost.
func BridgeDatagram(stream, conn net.Conn) { BridgeDatagramStream(stream, conn) }

// BridgeDatagramStream is BridgeDatagram for a framed stream that is only an
// io.ReadWriteCloser (e.g. the deploy backends' port-forward tunnel conn, which
// the Backend interface types as io.ReadWriteCloser). Semantics are identical.
func BridgeDatagramStream(stream io.ReadWriteCloser, conn net.Conn) {
	var once sync.Once
	closeBoth := func() { stream.Close(); conn.Close() }
	var wg sync.WaitGroup
	wg.Add(2)
	// stream -> conn
	go func() {
		defer wg.Done()
		defer once.Do(closeBoth)
		for {
			dgram, err := ReadDatagram(stream)
			if err != nil {
				return
			}
			if _, err := conn.Write(dgram); err != nil {
				return
			}
		}
	}()
	// conn -> stream
	go func() {
		defer wg.Done()
		defer once.Do(closeBoth)
		buf := make([]byte, MaxDatagram)
		for {
			n, err := conn.Read(buf)
			// A successful read yields exactly one datagram, including a
			// legitimately empty (zero-payload) one for which conn.Read
			// returns (0, nil). Frame it whenever the read succeeded so
			// empty datagrams are preserved symmetrically with the
			// stream->conn direction; only a real error ends the loop.
			if err == nil {
				if werr := WriteDatagram(stream, buf[:n]); werr != nil {
					return
				}
				continue
			}
			return
		}
	}()
	wg.Wait()
}
