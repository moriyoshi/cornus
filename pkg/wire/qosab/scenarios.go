package qosab

import (
	"encoding/binary"
	"io"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

// Non-test helpers for the benchmark harness in qosab_test.go. The harness is
// fork-only (it configures the QoS scheduler via yamux.Config), so it sets stream
// classes directly — no env vars, no reflection, no stock/fork replace toggle;
// the "variants" (including a stock-like FIFO/uncapped one) are Config values in
// the embedded matrix.

// Per-stream dispatch tags for the emulated server.
const (
	tagBulk    = byte('B') // server drains; models a bulk mount write stream
	tagRequest = byte('R') // server echoes; models a latency-sensitive mount op
)

type stats struct{ avg, p50, p99, max time.Duration }

func summarize(samples []time.Duration) stats {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var total time.Duration
	for _, d := range samples {
		total += d
	}
	n := len(samples)
	return stats{
		avg: total / time.Duration(n),
		p50: samples[n/2],
		p99: samples[(n*99)/100],
		max: samples[n-1],
	}
}

// serve accepts streams and dispatches by their first (tag) byte: bulk streams
// are drained, request streams echoed (a 4-byte length prefix -> that many reply
// bytes). It assigns the accepted stream's send class to match the tag.
func serve(sess *yamux.Session) {
	for {
		s, err := sess.AcceptStream()
		if err != nil {
			return
		}
		var tag [1]byte
		if _, err := io.ReadFull(s, tag[:]); err != nil {
			s.Close()
			continue
		}
		switch tag[0] {
		case tagBulk:
			s.SetPriority(yamux.ClassBulk)
			go func(s net.Conn) { _, _ = io.Copy(io.Discard, s) }(s)
		case tagRequest:
			s.SetPriority(yamux.ClassHigh)
			go echo(s)
		default:
			s.Close()
		}
	}
}

func echo(s net.Conn) {
	defer s.Close()
	var hdr [4]byte
	resp := make([]byte, 256)
	for {
		if _, err := io.ReadFull(s, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if int(n) > len(resp) {
			resp = make([]byte, n)
		}
		if _, err := s.Write(resp[:n]); err != nil {
			return
		}
	}
}

// linkedSessions builds a client+server yamux session over an emulated link.
// cfg (may be nil) mutates the yamux.Config — this is how a matrix variant selects
// the scheduler mode / frame cap.
func linkedSessions(p LinkProfile, window uint32, cfg func(*yamux.Config)) (*yamux.Session, func(), error) {
	ca, cb := newLink(p)
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.EnableKeepAlive = false
	c.MaxStreamWindowSize = window
	if cfg != nil {
		cfg(c)
	}
	client, err := yamux.Client(ca, c)
	if err != nil {
		ca.Close()
		cb.Close()
		return nil, nil, err
	}
	server, err := yamux.Server(cb, c)
	if err != nil {
		client.Close()
		ca.Close()
		cb.Close()
		return nil, nil, err
	}
	go serve(server)
	return client, func() { client.Close(); server.Close(); ca.Close(); cb.Close() }, nil
}

// startBulk opens n bulk streams (ClassBulk) saturating the link until stop.
func startBulk(client *yamux.Session, n int, stop <-chan struct{}) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		s, err := client.OpenStream()
		if err != nil {
			continue
		}
		s.SetPriority(yamux.ClassBulk)
		if _, err := s.Write([]byte{tagBulk}); err != nil {
			s.Close()
			continue
		}
		wg.Add(1)
		go func(s net.Conn) {
			defer wg.Done()
			defer s.Close()
			buf := make([]byte, 1<<20)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := s.Write(buf); err != nil {
					return
				}
			}
		}(s)
	}
	return &wg
}

// runFairness runs n equal bulk streams for dur and returns the min/max
// bytes-written ratio (1.0 = perfectly fair).
func runFairness(p LinkProfile, n int, cfg func(*yamux.Config), dur time.Duration) (float64, error) {
	client, cleanup, err := linkedSessions(p, 4<<20, cfg)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	stop := make(chan struct{})
	counts := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		s, err := client.OpenStream()
		if err != nil {
			close(stop)
			wg.Wait()
			return 0, err
		}
		s.SetPriority(yamux.ClassBulk)
		if _, err := s.Write([]byte{tagBulk}); err != nil {
			s.Close()
			close(stop)
			wg.Wait()
			return 0, err
		}
		wg.Add(1)
		go func(idx int, s net.Conn) {
			defer wg.Done()
			defer s.Close()
			buf := make([]byte, 256<<10)
			for {
				select {
				case <-stop:
					return
				default:
				}
				m, err := s.Write(buf)
				atomic.AddInt64(&counts[idx], int64(m))
				if err != nil {
					return
				}
			}
		}(i, s)
	}
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	minC, maxC := counts[0], counts[0]
	for _, c := range counts {
		if c < minC {
			minC = c
		}
		if c > maxC {
			maxC = c
		}
	}
	if maxC == 0 {
		return 0, nil
	}
	return float64(minC) / float64(maxC), nil
}
