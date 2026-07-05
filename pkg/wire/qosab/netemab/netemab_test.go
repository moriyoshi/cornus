package netemab

import (
	"context"
	"io"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/ooni/netem"
)

// The netem-backed QoS harness: it runs the yamux QoS matrix over a REAL TCP stack
// (gVisor userspace netstack) on emulated L3 links with delay, packet loss, MTU,
// and bandwidth — the conditions the reliable in-memory link cannot model. Like
// the parent qosab it is entirely IN-CODE: no environment variables, and the QoS
// "variant" (scheduler mode + frame cap, including a stock-like FIFO/uncapped one)
// is a yamux.Config value in the matrix. It lives in a nested module so ooni/netem's
// heavy gVisor dependency stays out of cornus's main go.mod; run it explicitly:
//
//	cd pkg/wire/qosab/netemab && go test -run TestNetem -v
//
// First build compiles gVisor (~minutes, then cached).

// ---- bandwidth emulation (netem has no bandwidth field on LinkConfig) ----

// bwWrapper is a netem LinkNICWrapper that rate-limits a NIC to a bandwidth. The
// LeftNICWrapper/RightNICWrapper hooks are the intended seam; this paces frame
// READS off the NIC so the link forwards at the bandwidth rate and the stack's TX
// queue backpressures (real serialization, composed with the link's delay/PLR).
// Accuracy note: userspace time.Sleep pacing is good to ~±30% through ~10 MiB/s and
// under-delivers above that under load — netem overwrites Deadline, so we cannot
// offload the pacing to its delivery timer.
type bwWrapper struct{ bytesPerSec float64 }

func (w bwWrapper) WrapNIC(nic netem.NIC) netem.NIC { return &bwNIC{NIC: nic, bw: w.bytesPerSec} }

var _ netem.LinkNICWrapper = bwWrapper{}

type bwNIC struct {
	netem.NIC
	bw    float64
	mu    sync.Mutex
	bytes int
}

// bwBurst is the byte quantum accumulated before sleeping. A fixed BYTE burst (not
// a time threshold, which would grow with bandwidth and throttle TCP) keeps bursts
// small and sleeps coarse enough to be reasonably accurate at any rate.
const bwBurst = 16 << 10

func (n *bwNIC) ReadFrameNonblocking() (*netem.Frame, error) {
	f, err := n.NIC.ReadFrameNonblocking()
	if err != nil || n.bw <= 0 || f == nil || len(f.Payload) == 0 {
		return f, err
	}
	n.mu.Lock()
	n.bytes += len(f.Payload)
	var nap time.Duration
	if n.bytes >= bwBurst {
		nap = time.Duration(float64(n.bytes) / n.bw * float64(time.Second))
		n.bytes = 0
	}
	n.mu.Unlock()
	if nap > 0 {
		time.Sleep(nap)
	}
	return f, err
}

// ---- matrices ----

// linkCond is one emulated-link condition: MTU, per-direction bandwidth (0 =
// unlimited), one-way delay, and packet-loss rate.
type linkCond struct {
	name  string
	mtu   uint32
	bw    float64
	delay time.Duration
	plr   float64
}

var conds = []linkCond{
	{"clean-LAN", 1500, 0, 1 * time.Millisecond, 0},
	{"WAN-20ms", 1500, 0, 20 * time.Millisecond, 0},
	{"WAN-loss1%", 1500, 0, 5 * time.Millisecond, 0.01},
	{"cap-10MiB", 1500, 10 << 20, 2 * time.Millisecond, 0},
	{"jumbo-9000", 9000, 0, 2 * time.Millisecond, 0},
}

// variant is a QoS configuration (nil = full-QoS defaults).
type variant struct {
	name string
	cfg  func(*yamux.Config)
}

var variants = []variant{
	{"stock-like", func(c *yamux.Config) { c.SchedulerMode = yamux.SchedFIFO; c.MaxDataFrame = 1 << 30 }},
	{"cap+priority", nil}, // production scheduler, synchronous send path
	// The production scheduler with the batched-pipelined send path (cornus fork):
	// the send-path A/B partner of "cap+priority", measured over a REAL TCP stack.
	{"cap+priority+batched", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined }},
}

// ---- netem session over a two-host star with the given link condition ----

func netemSession(tb testing.TB, cond linkCond, cfg func(*yamux.Config)) (*yamux.Session, func()) {
	tb.Helper()
	logger := &netem.NullLogger{}
	router := netem.NewRouter(logger)
	ca := netem.MustNewCA()
	mtu := cond.mtu
	if mtu == 0 {
		mtu = 1500
	}
	lc := &netem.LinkConfig{
		LeftToRightDelay: cond.delay,
		RightToLeftDelay: cond.delay,
		LeftToRightPLR:   cond.plr,
		RightToLeftPLR:   cond.plr,
	}
	if cond.bw > 0 {
		// Rate-limit each host's egress once (LeftNICWrapper wraps the host NIC).
		lc.LeftNICWrapper = bwWrapper{bytesPerSec: cond.bw}
	}
	var links []*netem.Link
	mk := func(addr string) *netem.UNetStack {
		st, err := netem.NewUNetStack(logger, mtu, addr, ca, "0.0.0.0")
		if err != nil {
			tb.Fatal(err)
		}
		port := netem.NewRouterPort(router)
		links = append(links, netem.NewLink(logger, st, port, lc))
		router.AddRoute(addr, port)
		return st
	}
	closeLinks := func() {
		for _, l := range links {
			l.Close()
		}
	}
	serverStack := mk("10.0.0.1")
	clientStack := mk("10.0.0.2")

	ln, err := (&netem.Net{Stack: serverStack}).ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 9000})
	if err != nil {
		closeLinks()
		tb.Fatal(err)
	}
	accCh := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accCh <- c }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cc, err := (&netem.Net{Stack: clientStack}).DialContext(ctx, "tcp", "10.0.0.1:9000")
	if err != nil {
		ln.Close()
		closeLinks()
		tb.Fatal(err)
	}
	sc := <-accCh

	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.EnableKeepAlive = false
	c.MaxStreamWindowSize = 4 << 20
	if cfg != nil {
		cfg(c)
	}
	client, err := yamux.Client(cc, c)
	if err != nil {
		tb.Fatal(err)
	}
	server, err := yamux.Server(sc, c)
	if err != nil {
		tb.Fatal(err)
	}
	go drain(server)
	return client, func() { client.Close(); server.Close(); ln.Close(); closeLinks() }
}

func drain(sess *yamux.Session) {
	for {
		s, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go func(s net.Conn) { _, _ = io.Copy(io.Discard, s) }(s)
	}
}

func startBulk(client *yamux.Session, n int, stop <-chan struct{}) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		s, err := client.OpenStream()
		if err != nil {
			continue
		}
		s.SetPriority(yamux.ClassBulk)
		wg.Add(1)
		go func(s net.Conn) {
			defer wg.Done()
			defer s.Close()
			buf := make([]byte, 256<<10)
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

type stats struct{ avg, p50, p99, max time.Duration }

func summarize(s []time.Duration) stats {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var tot time.Duration
	for _, d := range s {
		tot += d
	}
	n := len(s)
	return stats{tot / time.Duration(n), s[n/2], s[(n*99)/100], s[n-1]}
}

func measureThroughput(tb testing.TB, client *yamux.Session, dur time.Duration) float64 {
	bulk, err := client.OpenStream()
	if err != nil {
		tb.Fatal(err)
	}
	bulk.SetPriority(yamux.ClassBulk)
	defer bulk.Close()
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(dur)
	var written int64
	for time.Now().Before(deadline) {
		nn, err := bulk.Write(buf)
		written += int64(nn)
		if err != nil {
			break
		}
	}
	return float64(written) / (1 << 20) / dur.Seconds()
}

// ---- matrix tests (env-free; run explicitly in this nested module) ----

// TestNetemLatencyMatrix drives variant x link-condition: control(ping) RTT under
// a saturating bulk mount, over real TCP.
func TestNetemLatencyMatrix(t *testing.T) {
	for _, v := range variants {
		for _, cond := range conds {
			client, cleanup := netemSession(t, cond, v.cfg)
			stop := make(chan struct{})
			wg := startBulk(client, 4, stop)
			time.Sleep(400 * time.Millisecond)
			var samples []time.Duration
			for i := 0; i < 12; i++ {
				d, err := client.Ping()
				if err != nil {
					break
				}
				samples = append(samples, d)
			}
			close(stop)
			wg.Wait()
			cleanup()
			if len(samples) == 0 {
				t.Logf("  %-13s %-11s (no ping samples)", v.name, cond.name)
				continue
			}
			s := summarize(samples)
			t.Logf("  %-13s %-11s ping avg=%s p99=%s", v.name, cond.name,
				s.avg.Round(time.Microsecond), s.p99.Round(time.Microsecond))
		}
	}
}

// TestNetemThroughputMatrix reports single-stream throughput per link condition —
// it exercises the bandwidth emulator (cap-10MiB) and MTU (jumbo-9000) dimensions,
// across the send-path variants so the batched-pipelined path's throughput is
// measured over a real TCP stack (the DB single-writer shape).
func TestNetemThroughputMatrix(t *testing.T) {
	for _, v := range variants {
		for _, cond := range conds {
			client, cleanup := netemSession(t, cond, v.cfg)
			tput := measureThroughput(t, client, 3*time.Second)
			cleanup()
			t.Logf("  %-13s %-11s throughput=%.1f MiB/s", v.name, cond.name, tput)
		}
	}
}
