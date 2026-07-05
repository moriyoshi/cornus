package wire

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Id mapping on the RAW 9P path.
//
// The block protocol rewrites ownership by terminating 9P in userspace and
// answering GetAttr itself (see WithReportedOwner). The default mount has no such
// termination — it splices frames — and measured against it, terminating 9P costs
// ~12-15% throughput and ~20% fsync latency, which is a poor trade for a rewrite
// of two fields.
//
// So this rewrites the two fields in the frame stream instead. Only Rgetattr
// carries ownership under 9P2000.L (dirents in Rreaddir do not, and Rstat is
// 9P2000.u, which this mount never speaks), so exactly one message type needs
// touching and everything else is spliced as before — including the bulk Rread
// payloads, which stream through without being buffered whole.

// 9P framing and Rgetattr layout. Verified against hugelgupf/p9 rather than from
// memory of the spec: p9.go declares `msgTgetattr msgType = 24`, and the encoders
// fix the field order — rgetattr.encode writes Valid, QID, Attr, and Attr.encode
// writes Mode, UID, GID before its run of 8-byte fields.
const (
	ninepHeaderLen = 7  // size[4] type[1] tag[2]
	msgRgetattr    = 25 // Tgetattr is 24, so its reply is 25

	rgetattrValidOff = ninepHeaderLen                // valid[8]
	rgetattrUIDOff   = rgetattrValidOff + 8 + 13 + 4 // + qid[13] + mode[4]
	rgetattrGIDOff   = rgetattrUIDOff + 4            // uid[4]
	rgetattrMinLen   = rgetattrGIDOff + 4            // through gid[4]

	// Which fields the reply actually carries (linux P9_GETATTR_*). Rewriting a
	// field the server did not report would invent ownership for a response that
	// never claimed to know it.
	getattrValidUID = 0x4
	getattrValidGID = 0x8
)

// pipeMappingOwner splices a kernel-9p connection to the caller's export, like
// pipe, while reporting uid/gid as the owner of every file.
//
// Requests (kernel -> caller) are untouched. Only the reply direction is parsed,
// and only far enough to find message boundaries.
func pipeMappingOwner(kernelConn, remoteStream io.ReadWriteCloser, uid, gid uint32) {
	var once sync.Once
	closeBoth := func() { kernelConn.Close(); remoteStream.Close() }
	var wg sync.WaitGroup
	wg.Add(2)
	// Replies flow caller -> kernel: the direction ownership travels in.
	go func() {
		defer wg.Done()
		_ = mapOwnerStream(kernelConn, remoteStream, uid, gid)
		once.Do(closeBoth)
	}()
	// Requests flow kernel -> caller, unchanged.
	go func() {
		defer wg.Done()
		_, _ = io.Copy(remoteStream, kernelConn)
		once.Do(closeBoth)
	}()
	wg.Wait()
}

// mapOwnerStream copies 9P messages from src to dst, rewriting the ownership in
// each Rgetattr.
//
// It reads only the 5 bytes needed to identify a message, then either rewrites it
// (Rgetattr is 160 bytes) or splices its remainder straight through. That keeps a
// 1 MiB Rread payload streaming rather than buffering it whole, which is what
// makes this close to the cost of the plain splice.
func mapOwnerStream(dst io.Writer, src io.Reader, uid, gid uint32) error {
	var hdr [5]byte // size[4] type[1]
	msg := make([]byte, 0, rgetattrMinLen+64)
	for {
		if _, err := io.ReadFull(src, hdr[:]); err != nil {
			return err
		}
		size := binary.LittleEndian.Uint32(hdr[:])
		if size < ninepHeaderLen {
			return fmt.Errorf("wire: 9P frame of %d bytes is shorter than a header", size)
		}
		if hdr[4] != msgRgetattr || size < rgetattrMinLen {
			// Not ownership-bearing: header out, remainder spliced.
			if _, err := dst.Write(hdr[:]); err != nil {
				return err
			}
			if _, err := io.CopyN(dst, src, int64(size)-int64(len(hdr))); err != nil {
				return err
			}
			continue
		}
		if cap(msg) < int(size) {
			msg = make([]byte, size)
		}
		msg = msg[:size]
		copy(msg[:len(hdr)], hdr[:])
		if _, err := io.ReadFull(src, msg[len(hdr):]); err != nil {
			return err
		}
		valid := binary.LittleEndian.Uint64(msg[rgetattrValidOff:])
		if valid&getattrValidUID != 0 {
			binary.LittleEndian.PutUint32(msg[rgetattrUIDOff:], uid)
		}
		if valid&getattrValidGID != 0 {
			binary.LittleEndian.PutUint32(msg[rgetattrGIDOff:], gid)
		}
		if _, err := dst.Write(msg); err != nil {
			return err
		}
	}
}
