package clientconduit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"cornus/pkg/socks5"
	"cornus/pkg/wire"
)

// maxUnixPathLen is the shortest sun_path any supported kernel gives us (Linux is
// 108, macOS and the BSDs are 104). Exceeding it fails inside bind(2) with a
// message naming neither the path nor the limit, so it is checked up front.
const maxUnixPathLen = 104

// upstreamProbeTimeout bounds the liveness probe of a lent socket. Connecting to a
// unix socket with a live listener is immediate, so anything slower is not "busy".
const upstreamProbeTimeout = 2 * time.Second

// upstreamSeq keeps two publications in one process from colliding on a path.
var upstreamSeq atomic.Uint64

// publishOverSocket serves d on a unix socket and returns its path.
//
// It exists because a published name is served by a listener with NO ADDRESS — that
// is the point of pkg/memlisten, and why nothing is bound that the kernel could
// recycle to a squatter. The conduit host therefore cannot reach it by any ordinary
// means, so the publisher lends it a socket instead: the host dials the path, this
// side answers by opening the addressless listener, and the two are spliced.
//
// The host publishes over a socket too, rather than registering its in-process
// dialer directly. It costs one local hop for that one name and buys a great deal:
// a published name is then the same kind of thing wherever it came from, so it
// replays after a takeover exactly like any other claim instead of needing a case
// of its own.
func publishOverSocket(ctx context.Context, dir, name string, d socks5.LocalDialer) (string, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	reapDeadUpstreams(dir)
	path := filepath.Join(dir, fmt.Sprintf("%s-%d-%d.sock", sanitizeForPath(name), os.Getpid(), upstreamSeq.Add(1)))
	if len(path) > maxUnixPathLen {
		return "", nil, fmt.Errorf("clientconduit: upstream socket path is %d bytes, over the %d-byte unix socket limit: %s (set CORNUS_AGENT_DIR to a shorter directory)", len(path), maxUnixPathLen, path)
	}
	_ = os.Remove(path) // a corpse from an unclean exit must not block the bind
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", nil, fmt.Errorf("clientconduit: publishing %s over a socket: %w", name, err)
	}

	var once sync.Once
	closer := func() {
		once.Do(func() {
			_ = ln.Close()
			_ = os.Remove(path)
		})
	}
	go func() {
		defer closer()
		for {
			c, err := ln.Accept()
			if err != nil {
				return // closed
			}
			go func() {
				defer c.Close()
				// Open the addressless listener for this one connection and splice. A
				// failure closes the accepted side, so the caller sees a closed connection
				// rather than a silent hang.
				local, err := d.DialLocal(ctx)
				if err != nil {
					return
				}
				defer local.Close()
				wire.Pipe(c, local)
			}()
		}
	}()
	return path, closer, nil
}

// unixLocalDialer reaches a name published by another process — or by this one —
// through the socket its publisher lent.
type unixLocalDialer struct{ path string }

func (u unixLocalDialer) DialLocal(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", u.path)
	if err != nil {
		return nil, fmt.Errorf("clientconduit: reaching the published name's upstream at %s: %w", u.path, err)
	}
	return c, nil
}

// sanitizeForPath reduces a published name to something safe in a filename, so a
// host with dots and colons does not produce a path that is legal on one platform
// and not another. Collisions are impossible anyway — the pid and sequence make the
// name unique — so this only has to be readable.
func sanitizeForPath(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "name"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return string(out)
}

// upstreamDir is where this process lends sockets for the names it publishes. It
// sits beside the rendezvous so it inherits the same isolation: CORNUS_AGENT_DIR
// separates agents, and it must separate their upstreams too.
func upstreamDir(registryDir string) string {
	return filepath.Join(registryDir, "upstream")
}

// reapDeadUpstreams removes sockets whose publisher is gone.
//
// A publisher unlinks its own socket on the way out, but only when it gets to run
// its teardown: a SIGKILL, or any exit that skips it, leaves the file behind. The
// directory would otherwise grow by one entry per `cornus web` that ever died
// badly, forever, with nothing to prompt anyone to look.
//
// Liveness is decided by DIALING, the same way the rendezvous decides it for
// conduits, and for the same reason: the pid embedded in the name can be recycled
// onto an unrelated process, and a live pid says nothing about whether it still
// holds this socket. Anything that answers is kept; only a refused or absent
// connection is reaped, so a slow publisher is never mistaken for a dead one.
func reapDeadUpstreams(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sock") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		c, err := net.DialTimeout("unix", path, upstreamProbeTimeout)
		if err == nil {
			_ = c.Close()
			continue // somebody is home
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			_ = os.Remove(path)
		}
	}
}
