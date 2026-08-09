// Package wire carries cornus sessions (build and deploy) over a single
// WebSocket: the byte stream is yamux-multiplexed into tagged streams (a control
// channel plus, on demand, per-context 9P backings), and it provides the
// spec-agnostic transport primitives — dial/accept, tagged stream open/accept,
// bidirectional pipe, and a confined 9P directory server — that both the build
// wire (pkg/build/buildwire) and the deploy wire (pkg/deploywire) reuse.
//
// wire deliberately has NO BuildKit dependency, so a build-free consumer (such
// as the `cornus daemon docker` proxy via pkg/deploywire) can link the transport without
// pulling in the BuildKit dependency tree. The package invariant holds for every
// user: the caller is the 9P server (it exports its own local files) and the
// cornus server is the 9P client.
package wire

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

const readLimit = 64 << 20 // generous: yamux frames over a single WS message

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	// The default 256 KiB stream window would let a 1 MiB block-protocol frame
	// (and the kernel-9p msize) trickle through ~4 RTT-bound window refills,
	// erasing the block cache's throughput win. Raise it so several full chunks can
	// be in flight per stream. The window is allocated lazily, so idle streams do
	// not pay for it. A large window used to risk starving the latency-sensitive
	// control channel behind bulk mount data, but the forked yamux
	// (third_party/yamux) now handles that: control frames are strict-priority and
	// data frames are capped (128 KiB) so they never monopolize the send loop — see
	// streamClassForTag and the A/B in pkg/wire/qosab.
	cfg.MaxStreamWindowSize = 16 << 20
	// Batched, pipelined send path (cornus fork): one conn.Write per frame — one
	// WebSocket message, vs two on the classic path — and no synchronous per-frame
	// wire round-trip, so a single bulk stream (the writable block/DB mount) keeps
	// several frames in flight. Bounded to PipelineDepth frames per stream so the
	// QoS scheduler, not a fat downstream buffer, stays where frames queue and
	// control frames keep interleaving. The real-TCP A/B (pkg/wire/qosab/netemab)
	// showed a large single-stream throughput win (clean-LAN +141%, and it beats
	// even the uncapped stock path) with equal-or-better control latency under bulk
	// saturation and ~46% fewer allocs; depth 4 is the measured knee. See JOURNAL.
	cfg.SendMode = yamux.SendBatchedPipelined
	cfg.PipelineDepth = 4
	return cfg
}

// wsWriteBufferSize is the buffer every cornus WebSocket CLIENT masks and flushes
// through, and it is sized to hold one whole yamux frame.
//
// It matters only on the client side, and only because of masking. RFC 6455 makes
// a client mask every byte it sends, and coder/websocket masks and flushes in
// chunks bounded by this buffer — so at the 4096-byte default a client issues one
// write syscall per 4 KiB regardless of how the bytes are framed. For cornus that
// lands on the DEPLOY CALLER, which dials and serves the export, so it is paid on
// read replies: a container reading 16 MiB from its mount cost the caller ~4300
// write syscalls, about a third of all syscall time on that path and 11-16% of its
// CPU (profiled; see .agents/docs/JOURNAL.md).
//
// 128 KiB is the forked yamux's single-DATA-frame cap (defaultMaxDataFrame), plus
// room for that frame's 12-byte yamux header and the masked WebSocket header, so
// one yamux frame leaves in one write. Larger buys nothing: yamux never hands the
// connection more than one frame at a time.
//
// This needs the in-repo websocket fork (third_party/websocket), which adds the
// option; upstream v1.8.15 has no way to reach the buffer.
const wsWriteBufferSize = (128 << 10) + 4096

// withWriteBuffer applies wsWriteBufferSize to opts, allocating them if the caller
// had none. Every client dial in this package goes through it, so no dial can
// quietly keep the 4 KiB default.
func withWriteBuffer(opts *websocket.DialOptions) *websocket.DialOptions {
	if opts == nil {
		opts = &websocket.DialOptions{}
	}
	opts.WriteBufferSize = wsWriteBufferSize
	return opts
}

// dial opens the WebSocket to a cornus endpoint and returns a yamux client
// session over it.
func dial(ctx context.Context, url string) (*yamux.Session, error) {
	c, _, err := websocket.Dial(ctx, url, withWriteBuffer(nil))
	if err != nil {
		return nil, fmt.Errorf("wire: dial %s: %w", url, err)
	}
	c.SetReadLimit(readLimit)
	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	return yamux.Client(nc, yamuxConfig())
}

// accept upgrades an HTTP request to a WebSocket and returns a yamux server
// session over it.
func accept(w http.ResponseWriter, r *http.Request) (*yamux.Session, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, fmt.Errorf("wire: accept: %w", err)
	}
	c.SetReadLimit(readLimit)
	nc := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	return yamux.Server(nc, yamuxConfig())
}

// streamClassForTag maps a backing's 1-byte tag to its QoS send class for the
// forked yamux scheduler (third_party/yamux): the control channel is
// latency-sensitive (ClassHigh — a heavy WRR weight), bulk file backings (9P
// mounts + the writable block protocol) are ClassBulk so they yield to control
// and share fairly among themselves, and everything else stays ClassNormal.
// (Session control frames — window updates, FIN, ping — are ClassUrgent inside
// yamux regardless, always strictly ahead of any data.)
func streamClassForTag(tag byte) uint8 {
	switch tag {
	case tagControl:
		return yamux.ClassHigh
	case tagLazy9P, tagMount, tagBlockFS, tagFSOp:
		// All file backings (the 9P mount 'L', the caretaker mount 'M', the
		// writable block protocol 'b', and the filesystem-operation stream 'S')
		// carry bulk file data. 'S' belongs here even though most of its ops are
		// small: a get/put carries a whole tar, and one multi-gigabyte copy on
		// ClassNormal would starve the hub's control channel for its duration.
		return yamux.ClassBulk
	default:
		return yamux.ClassNormal
	}
}

// openTagged opens a yamux stream, assigns its QoS class by tag, and writes the
// 1-byte tag.
func openTagged(sess *yamux.Session, tag byte) (net.Conn, error) {
	s, err := sess.OpenStream()
	if err != nil {
		return nil, err
	}
	s.SetPriority(streamClassForTag(tag))
	if _, err := s.Write([]byte{tag}); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// acceptTagged accepts a yamux stream, reads its 1-byte tag, and assigns its QoS
// class by that tag.
func acceptTagged(sess *yamux.Session) (byte, net.Conn, error) {
	s, err := sess.AcceptStream()
	if err != nil {
		return 0, nil, err
	}
	var tag [1]byte
	if _, err := s.Read(tag[:]); err != nil {
		s.Close()
		return 0, nil, err
	}
	s.SetPriority(streamClassForTag(tag[0]))
	return tag[0], s, nil
}
