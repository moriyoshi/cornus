// Package sqliteab drives a REAL SQLite workload over the cornus block-protocol
// transport, in-process, with no kernel 9p mount, root, or container. It stands
// up the same stack production uses for a writable async mount minus the kernel:
//
//	SQLite (mattn/go-sqlite3, real C library)
//	  -> a psanford/sqlite3vfs VFS (this package)
//	    -> a hugelgupf/p9 client  ("the kernel")
//	      -> wire.ServeBlockProxy (+ server-side block cache)
//	        -> a yamux stream (the in-repo fork; SendSync vs SendBatchedPipelined)
//	          -> wire.ServeBlockServer (the authoritative file owner)
//	            -> a real temp directory
//
// Because the whole thing is in one process over a loopback TCP yamux session,
// the yamux send-path A/B (the batched-pipelined rework vs the classic
// synchronous path) can be measured against an actual database workload — the
// headline use case for the writable block mount.
//
// It is a NESTED module (like ../qosab/netemab) so mattn/go-sqlite3's cgo SQLite
// amalgamation and psanford/sqlite3vfs never enter cornus's main go.mod and the
// normal `go test ./...` gate stays pure-Go and fast. It always builds the
// in-repo yamux fork (the replace in go.mod). Run explicitly:
//
//	cd pkg/wire/sqliteab && go test -run TestSQLite -v
//	cd pkg/wire/sqliteab && go test -run '^$' -bench . -benchmem
package sqliteab

import (
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/blockcache"
	"cornus/pkg/wire"
)

// chunkSize matches blockcache.DefaultChunkSize (1 MiB), which mirrors the kernel
// 9p mount msize so one block == one kernel Tread/Twrite unit in production.
const chunkSize = 1 << 20

// yamuxCfg builds a session config that mirrors cornus's production yamuxConfig()
// (16 MiB stream window, the forked QoS scheduler + 128 KiB frame cap defaults)
// but with the send path and pipeline depth chosen by the caller, so a benchmark
// can A/B the classic synchronous send path against the batched-pipelined one.
func yamuxCfg(mode yamux.SendMode, depth int) *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.EnableKeepAlive = false
	c.MaxStreamWindowSize = 16 << 20
	c.SendMode = mode
	if depth > 0 {
		c.PipelineDepth = depth
	}
	return c
}

// countConn wraps a net.Conn counting bytes in each direction, so a benchmark can
// see the block-protocol traffic (read amplification, write payloads) directly
// rather than inferring it from wall-clock on a fast loopback transport.
type countConn struct {
	net.Conn
	read    atomic.Int64 // bytes the caller received (requests from the proxy)
	written atomic.Int64 // bytes the caller sent (responses: read-fill data + hashes)
}

func (c *countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *countConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

// delayConn injects an asynchronous, order-preserving delivery delay on Writes —
// a NETWORK latency model (bytes written now arrive `latency` later) that, unlike
// sleeping in Read/Write inline, does NOT serialize the caller, so pipelined
// requests overlap the latency. Used to measure speculative prefetch's
// latency-hiding on a simulated high-latency link.
type delayConn struct {
	net.Conn
	lat  time.Duration
	ch   chan timedBuf
	done chan struct{}
}

type timedBuf struct {
	at time.Time
	b  []byte
}

func newDelayConn(c net.Conn, latency time.Duration) *delayConn {
	d := &delayConn{Conn: c, lat: latency, ch: make(chan timedBuf, 4096), done: make(chan struct{})}
	go func() {
		for {
			select {
			case tb := <-d.ch:
				if wait := time.Until(tb.at); wait > 0 {
					time.Sleep(wait)
				}
				if _, err := d.Conn.Write(tb.b); err != nil {
					return
				}
			case <-d.done:
				return
			}
		}
	}()
	return d
}

func (d *delayConn) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	select {
	case d.ch <- timedBuf{at: time.Now().Add(d.lat), b: b}:
		return len(p), nil
	case <-d.done:
		return 0, net.ErrClosed
	}
}

func (d *delayConn) Close() error {
	select {
	case <-d.done:
	default:
		close(d.done)
	}
	return d.Conn.Close()
}

// blockStack is a live in-process block-protocol stack. Root is the p9 attach
// root the VFS opens files under; Dir is the authoritative directory the caller
// writes through to. Close tears everything down.
type blockStack struct {
	Root  p9.File
	Dir   string
	cache *blockcache.Cache

	caller *countConn // the caller-side block-protocol stream, byte-counted
	client *yamux.Session
	server *yamux.Session
	conns  []net.Conn
	p9c    *p9.Client
}

// FromCaller returns bytes the caller has SENT to the proxy (read-fill block data
// + write-reply hashes) — the read-amplification / fetch indicator. ToCaller is
// bytes the caller received (write payloads + read requests). ResetCounters zeroes
// both, to isolate a measured phase from setup.
func (s *blockStack) FromCaller() int64 { return s.caller.written.Load() }
func (s *blockStack) ToCaller() int64   { return s.caller.read.Load() }
func (s *blockStack) ResetCounters() {
	s.caller.written.Store(0)
	s.caller.read.Store(0)
}

// loopbackPair returns a connected pair of loopback TCP conns (real socket
// buffers — more representative of the production WebSocket-over-TCP transport
// than net.Pipe's unbuffered rendezvous, which distorts the pipelined send path).
func loopbackPair() (net.Conn, net.Conn, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	dc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, nil, err
	}
	r := <-ch
	if r.err != nil {
		dc.Close()
		return nil, nil, r.err
	}
	return dc, r.c, nil
}

// newBlockStack wires the full stack with the default 1 MiB block/chunk size and
// the in-memory cache store.
func newBlockStack(dir string, mode yamux.SendMode, depth int, features uint32) (*blockStack, error) {
	return newBlockStackChunk(dir, mode, depth, features, chunkSize, 0, nil, 0)
}

// newBlockStackChunk wires the full stack over a yamux session using the given
// send path, block-coherence features (FeatSubBlockHash / FeatDeferHash, 0 = the
// classic per-write full-block path), and block/chunk size (== the p9 msize, as in
// production). dir is the authoritative directory (writes land here); it should be
// a fresh temp dir owned by the caller.
// mkStore builds a cache Store for the given chunk size (nil => in-memory), so a
// benchmark can run over the production on-disk cache (DiskStore) as well as
// MemStore.
func newBlockStackChunk(dir string, mode yamux.SendMode, depth int, features uint32, chunk, readahead int64, mkStore func(int64) blockcache.Store, latency time.Duration) (*blockStack, error) {
	if chunk <= 0 {
		chunk = chunkSize
	}
	var store blockcache.Store
	if mkStore != nil {
		store = mkStore(chunk)
	} else {
		store = blockcache.NewMemStore(chunk)
	}
	cache := blockcache.New(store, chunk)

	dialConn, accConn, err := loopbackPair()
	if err != nil {
		return nil, fmt.Errorf("loopback: %w", err)
	}
	cfg := yamuxCfg(mode, depth)
	client, err := yamux.Client(dialConn, cfg)
	if err != nil {
		dialConn.Close()
		accConn.Close()
		return nil, fmt.Errorf("yamux client: %w", err)
	}
	server, err := yamux.Server(accConn, cfg)
	if err != nil {
		client.Close()
		accConn.Close()
		return nil, fmt.Errorf("yamux server: %w", err)
	}

	// One bulk stream carries the block protocol, exactly as a mount backing does
	// in production (ClassBulk — yields to control, shares fairly among bulk).
	proxyStream, err := client.OpenStream()
	if err != nil {
		client.Close()
		server.Close()
		return nil, fmt.Errorf("open stream: %w", err)
	}
	proxyStream.SetPriority(yamux.ClassBulk)

	// Kernel side: a p9 client speaks to ServeBlockProxy's userspace p9.Server.
	kc1, kc2 := net.Pipe()

	// ServeBlockProxy sends its block-protocol hello on proxyStream, which makes
	// the server side observe the new stream so AcceptStream returns.
	go wire.ServeBlockProxy(kc2, proxyStream, cache, "m", wire.WithBlockFeatures(features), wire.WithReadahead(readahead))
	callerStream, err := server.AcceptStream()
	if err != nil {
		client.Close()
		server.Close()
		kc1.Close()
		kc2.Close()
		return nil, fmt.Errorf("accept stream: %w", err)
	}
	var callerConn net.Conn = callerStream
	if latency > 0 {
		callerConn = newDelayConn(callerConn, latency)
	}
	caller := &countConn{Conn: callerConn}
	go wire.ServeBlockServer(caller, dir, cache.ChunkSize(), wire.WithBlockFeatures(features))

	p9c, err := p9.NewClient(kc1, p9.WithMessageSize(uint32(chunk)))
	if err != nil {
		client.Close()
		server.Close()
		kc1.Close()
		kc2.Close()
		return nil, fmt.Errorf("p9 client: %w", err)
	}
	root, err := p9c.Attach("")
	if err != nil {
		p9c.Close()
		client.Close()
		server.Close()
		kc1.Close()
		kc2.Close()
		return nil, fmt.Errorf("p9 attach: %w", err)
	}

	return &blockStack{
		Root:   root,
		Dir:    dir,
		cache:  cache,
		caller: caller,
		client: client,
		server: server,
		conns:  []net.Conn{dialConn, accConn, kc1, kc2, callerConn},
		p9c:    p9c,
	}, nil
}

// Close tears down the p9 client, both yamux sessions, and all conns.
func (s *blockStack) Close() {
	if s.p9c != nil {
		s.p9c.Close()
	}
	if s.client != nil {
		s.client.Close()
	}
	if s.server != nil {
		s.server.Close()
	}
	for _, c := range s.conns {
		if c != nil {
			c.Close()
		}
	}
}
