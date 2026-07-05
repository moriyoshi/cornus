package wire

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"
	"github.com/moby/patternmatcher"

	"cornus/pkg/blockcache"
)

// The 9P backing for lazy bind mounts. Unlike the eager path (one 9P client
// wrapping the export as an fsutil.FS for DiffCopy), here the build server mounts
// kernel-9p per lazy context and reads files on demand. Mirrors the SSH-forward
// tunnel: the server opens a local unix socket the kernel mounts (trans=unix),
// and proxies each connection over a new 'L'-tagged yamux stream to the caller,
// which serves a confined, read-only 9P export of that context's directory.

// Serve9PBacking runs on the caller: it accepts 'L' streams and, for each, serves
// a read-only confined 9P export of the requested named context's directory.
// dirs maps a context name to its local directory. Runs until the session ends.
// writable names an export served read-write (via writableConfinedAttacher); all
// others are served read-only. Build exports are always read-only (writable nil).
func Serve9PBacking(sess *yamux.Session, dirs map[string]string, ignores map[string]*patternmatcher.PatternMatcher, writable map[string]bool, reads *atomic.Int64) {
	ServeBackings(sess, dirs, ignores, writable, reads, nil, nil)
}

// ServeBackings is Serve9PBacking that also serves credential backings ('k') and
// egress backings ('e') from the SAME accept loop, so all backing types ride one
// session without racing loops. cred, when non-nil, is invoked per 'k' stream with
// the requested credential name and the stream (the caller reads the request and
// writes the minted credential over it). egress, when non-nil, is invoked per 'e'
// stream with the requested destination ("host:port") and the stream (the caller
// dials that destination through its own network and splices). A nil handler
// rejects that backing type; a nil dirs/writable/reads serves no 9P backings.
func ServeBackings(sess *yamux.Session, dirs map[string]string, ignores map[string]*patternmatcher.PatternMatcher, writable map[string]bool, reads *atomic.Int64, cred func(name string, conn net.Conn), egress func(dest string, conn net.Conn)) {
	for {
		tag, stream, err := acceptTagged(sess)
		if err != nil {
			return
		}
		switch tag {
		case tagLazy9P:
			go serveOne9P(stream, dirs, ignores, writable, reads)
		case tagBlockFS:
			go serveOneBlock(stream, dirs)
		case tagCredBacking:
			if cred == nil {
				stream.Close()
				continue
			}
			go serveOneCred(stream, cred)
		case tagEgressBacking:
			if egress == nil {
				stream.Close()
				continue
			}
			go serveOneLine(stream, egress)
		default:
			stream.Close()
		}
	}
}

func serveOneCred(stream net.Conn, cred func(name string, conn net.Conn)) {
	serveOneLine(stream, cred)
}

// serveOneLine reads the single leading line (the name / destination) and hands
// the rest of the stream to h. Shared by the credential and egress backings.
func serveOneLine(stream net.Conn, h func(line string, conn net.Conn)) {
	defer stream.Close()
	line, err := ReadLine(stream)
	if err != nil {
		return
	}
	h(line, stream)
}

func serveOne9P(stream net.Conn, dirs map[string]string, ignores map[string]*patternmatcher.PatternMatcher, writable map[string]bool, reads *atomic.Int64) {
	defer stream.Close()
	name, err := ReadLine(stream)
	if err != nil {
		return
	}
	dir, ok := dirs[name]
	if !ok {
		return
	}
	var attacher p9.Attacher
	if writable[name] {
		attacher, err = writableConfinedAttacher(dir)
	} else {
		attacher, err = confinedAttacherCounted(dir, ignores[name], reads)
	}
	if err != nil {
		return
	}
	_ = p9.NewServer(attacher).Handle(stream, stream)
}

// serveOneBlock serves a writable, cache-coherent block-protocol backing ('b'):
// it reads the mount name, looks up its exported directory, and runs the caller
// block server (which reuses the confined writable export).
func serveOneBlock(stream net.Conn, dirs map[string]string) {
	defer stream.Close()
	name, err := ReadLine(stream)
	if err != nil {
		return
	}
	dir, ok := dirs[name]
	if !ok {
		return
	}
	ServeBlockServer(stream, dir, defaultBlockChunk, BlockEnvOpts()...)
}

// Backing9PSocket runs on the server: it returns a unix socket the kernel-9p
// client mounts (trans=unix) for the named context. Each connection to it is
// proxied over a new 'L' stream to the caller's Serve9PBacking. The caller must
// export the same name. Returns the socket path and a cleanup func.
func Backing9PSocket(sess *yamux.Session, name string) (string, func(), error) {
	return backing9PSocket(sess, name, nil, nil, nil, proxyPipe)
}

// Backing9PSocketCached is Backing9PSocketMetered with a server-side block cache:
// when cache is non-nil the server terminates 9P in userspace and serves reads
// from the cache instead of blindly piping frames (see ServeCachingProxy). Only
// pass a non-nil cache for a read-only export whose files are immutable for the
// mount's lifetime (build contexts, deploy mounts flagged immutable); a nil cache
// behaves exactly like Backing9PSocketMetered.
func Backing9PSocketCached(sess *yamux.Session, name string, onRx, onTx func(int), cache *blockcache.Cache) (string, func(), error) {
	mode := proxyPipe
	if cache != nil {
		mode = proxyReadCache
	}
	return backing9PSocket(sess, name, onRx, onTx, cache, mode)
}

// Backing9PSocketBlock is Backing9PSocketCached for the WRITABLE, cache-coherent
// block protocol: the server terminates the mount in a writable block proxy
// (ServeBlockProxy) speaking the block protocol to the caller over a 'b' backing.
// cache must be non-nil. Pair it with a cache=mmap kernel mount (async writeback).
func Backing9PSocketBlock(sess *yamux.Session, name string, onRx, onTx func(int), cache *blockcache.Cache) (string, func(), error) {
	return backing9PSocket(sess, name, onRx, onTx, cache, proxyBlockWrite)
}

// Backing9PSocketMetered is Backing9PSocket with per-mount byte metering: onRx is
// called with the byte count of each chunk delivered toward the kernel-9p client
// (data read into the container), onTx with each chunk read from it (9P requests
// / writes out). Either callback may be nil (then it behaves exactly as
// Backing9PSocket). The topology knowledge — that the accepted conn is the
// container side — lives here, so callers only think in rx/tx.
func Backing9PSocketMetered(sess *yamux.Session, name string, onRx, onTx func(int)) (string, func(), error) {
	return backing9PSocket(sess, name, onRx, onTx, nil, proxyPipe)
}

// proxyMode selects how a backing socket's kernel-9p connection is served to the
// caller: a blind byte pipe (default writable / uncached), the read-only caching
// proxy (immutable read-only mounts), or the writable block proxy (async mounts).
type proxyMode int

const (
	proxyPipe proxyMode = iota
	proxyReadCache
	proxyBlockWrite
)

func backing9PSocket(sess *yamux.Session, name string, onRx, onTx func(int), cache *blockcache.Cache, mode proxyMode) (string, func(), error) {
	dir, err := os.MkdirTemp("", "cornus-9pback-")
	if err != nil {
		return "", nil, err
	}
	sock := filepath.Join(dir, "ctx.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go tunnel9P(conn, sess, name, onRx, onTx, cache, mode)
		}
	}()
	cleanup := func() {
		l.Close()
		os.RemoveAll(dir)
	}
	return sock, cleanup, nil
}

func tunnel9P(conn net.Conn, sess *yamux.Session, name string, onRx, onTx func(int), cache *blockcache.Cache, mode proxyMode) {
	defer conn.Close()
	// The block proxy speaks its own protocol on a distinct 'b' backing; the 9P
	// modes ride the shared 'L' backing. openTagged also assigns the stream's QoS
	// class (both are ClassBulk file backings).
	tag := byte(tagLazy9P)
	if mode == proxyBlockWrite {
		tag = tagBlockFS
	}
	stream, err := openTagged(sess, tag)
	if err != nil {
		return
	}
	defer stream.Close()
	if _, err := io.WriteString(stream, name+"\n"); err != nil {
		return
	}
	// conn is the container's kernel-9p side: bytes written to it flow into the
	// container (rx), bytes read from it are requests/writes out (tx).
	var c net.Conn = conn
	if onRx != nil || onTx != nil {
		c = &MeteredConn{Conn: conn, OnRead: onTx, OnWrite: onRx}
	}
	switch mode {
	case proxyBlockWrite:
		// Terminate the mount in the writable, cache-coherent block proxy.
		ServeBlockProxy(c, stream, cache, name, BlockEnvOpts()...)
	case proxyReadCache:
		// Terminate 9P in userspace and serve reads from the block cache.
		ServeCachingProxy(c, stream, cache, name)
	default:
		// Blindly splice frames between the kernel mount and the caller's export.
		pipe(c, stream)
	}
}
