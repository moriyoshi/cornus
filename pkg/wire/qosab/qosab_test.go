package qosab

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// The yamux QoS performance harness, as a self-contained set of benchmarks driven
// entirely by IN-CODE matrices — no environment variables, no stock/fork replace
// toggle. The QoS behavior is selected per session via yamux.Config, so every
// variant (including a stock-like FIFO/uncapped one) is just a Config value.
//
// Run explicitly (benchmarks do not run in the normal `go test ./...` gate):
//
//	go test -run '^$' -bench . ./pkg/wire/qosab/
//	go test -run '^$' -bench BenchmarkLatency -benchtime 1x ./pkg/wire/qosab/
//
// ns/op on the latency benchmarks is the mean RTT; avg-us / p99-us are reported
// as custom metrics. Throughput reports MiB/s via b.SetBytes.

// profiles is the emulated-link matrix.
var profiles = []LinkProfile{
	{Name: "LAN", BytesPerSec: 1e9 / 8, Latency: 200 * time.Microsecond},
	{Name: "WAN", BytesPerSec: 100e6 / 8, Latency: 20 * time.Millisecond},
}

// variant is a QoS configuration in the matrix. cfg (nil = full-QoS defaults)
// mutates yamux.Config.
type variant struct {
	name string
	cfg  func(*yamux.Config)
}

var variants = []variant{
	{"stock-like(FIFO,uncapped)", func(c *yamux.Config) { c.SchedulerMode = yamux.SchedFIFO; c.MaxDataFrame = 1 << 30 }},
	{"cap-only(FIFO)", func(c *yamux.Config) { c.SchedulerMode = yamux.SchedFIFO }},
	{"cap+urgent", func(c *yamux.Config) { c.SchedulerMode = yamux.SchedUrgentOnly }},
	{"cap+priority(WRR)", nil}, // DefaultConfig defaults (production scheduler; SendSync)
	// The production scheduler with the batched-pipelined send path (cornus fork) at
	// two pipeline depths: A/B partners of "cap+priority(WRR)" isolating the
	// send-path rework, and showing how the per-stream in-flight bound trades the
	// single-stream throughput win against mixed-workload latency/fairness.
	{"cap+priority+batched-d2", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined; c.PipelineDepth = 2 }},
	{"cap+priority+batched-d4", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined; c.PipelineDepth = 4 }},
}

// sendVariants is the send-path A/B dimension used by the send-path-focused
// benchmarks (they hold the scheduler at the production default and vary only the
// wire serialization strategy and its pipeline depth), including an effectively
// unbounded depth to expose the bufferbloat the per-stream bound prevents.
var sendVariants = []struct {
	name string
	cfg  func(*yamux.Config)
}{
	{"sync", func(c *yamux.Config) { c.SendMode = yamux.SendSync }},
	{"batched-d2", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined; c.PipelineDepth = 2 }},
	{"batched-d4", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined; c.PipelineDepth = 4 }},
	{"batched-d8", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined; c.PipelineDepth = 8 }},
	{"batched-unbounded", func(c *yamux.Config) { c.SendMode = yamux.SendBatchedPipelined; c.PipelineDepth = 1 << 20 }},
}

// scenario is a latency-sensitive workload driven under a saturating bulk mount.
type scenario struct {
	name string
	run  func(b *testing.B, client *yamux.Session)
}

var scenarios = []scenario{
	{"control-ping", benchControlPing},
	{"mount-op", benchMountOp},
}

// BenchmarkLatency drives the full variant x profile x scenario matrix: a
// latency-sensitive stream while a bulk mount saturates the shared session.
func BenchmarkLatency(b *testing.B) {
	for _, v := range variants {
		for _, p := range profiles {
			for _, sc := range scenarios {
				b.Run(v.name+"/"+p.Name+"/"+sc.name, func(b *testing.B) {
					client, cleanup, err := linkedSessions(p, 4<<20, v.cfg)
					if err != nil {
						b.Fatal(err)
					}
					defer cleanup()
					sc.run(b, client)
				})
			}
		}
	}
}

func benchControlPing(b *testing.B, client *yamux.Session) {
	b.ReportAllocs()
	stop := make(chan struct{})
	wg := startBulk(client, 4, stop)
	defer func() { close(stop); wg.Wait() }()
	time.Sleep(200 * time.Millisecond) // ramp the bulk load
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d, err := client.Ping()
		if err != nil {
			b.Fatal(err)
		}
		samples = append(samples, d)
	}
	b.StopTimer()
	reportLatency(b, samples)
}

func benchMountOp(b *testing.B, client *yamux.Session) {
	b.ReportAllocs()
	stop := make(chan struct{})
	wg := startBulk(client, 4, stop)
	defer func() { close(stop); wg.Wait() }()
	req, err := client.OpenStream()
	if err != nil {
		b.Fatal(err)
	}
	req.SetPriority(yamux.ClassHigh)
	if _, err := req.Write([]byte{tagRequest}); err != nil {
		b.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 64)
	buf := make([]byte, 64)
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		if _, err := req.Write(hdr[:]); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(req, buf); err != nil {
			b.Fatal(err)
		}
		samples = append(samples, time.Since(t0))
	}
	b.StopTimer()
	reportLatency(b, samples)
}

func reportLatency(b *testing.B, samples []time.Duration) {
	if len(samples) == 0 {
		return
	}
	s := summarize(samples)
	b.ReportMetric(float64(s.avg.Microseconds()), "avg-us")
	b.ReportMetric(float64(s.p99.Microseconds()), "p99-us")
}

// BenchmarkThroughput measures single-stream bulk throughput per variant on a
// REAL TCP loopback (not the emulated link, whose per-frame goroutine pump would
// unfairly penalize the smaller-framed variants). MiB/s via b.SetBytes.
func BenchmarkThroughput(b *testing.B) {
	for _, v := range variants {
		b.Run(v.name, func(b *testing.B) {
			b.ReportAllocs()
			client, cleanup := loopbackSession(b, v.cfg)
			defer cleanup()
			bulk, err := client.OpenStream()
			if err != nil {
				b.Fatal(err)
			}
			bulk.SetPriority(yamux.ClassBulk)
			if _, err := bulk.Write([]byte{tagBulk}); err != nil {
				b.Fatal(err)
			}
			buf := make([]byte, 1<<20)
			b.SetBytes(int64(len(buf)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := bulk.Write(buf); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFrameCap sweeps the frame cap (Config.MaxDataFrame) across profiles —
// the latency<->throughput dial, all in-code.
func BenchmarkFrameCap(b *testing.B) {
	caps := []uint32{64 << 10, 128 << 10, 256 << 10, 512 << 10, 1 << 20}
	for _, cp := range caps {
		for _, p := range profiles {
			b.Run(fmt.Sprintf("cap=%dKiB/%s", cp>>10, p.Name), func(b *testing.B) {
				client, cleanup, err := linkedSessions(p, 4<<20, func(c *yamux.Config) { c.MaxDataFrame = cp })
				if err != nil {
					b.Fatal(err)
				}
				defer cleanup()
				benchMountOp(b, client)
			})
		}
	}
}

// BenchmarkFairness reports inter-bulk fairness (min/max byte ratio; 1.0 = fair)
// across the variant x profile matrix. It is a fixed-duration experiment, so it
// runs once per case and reports the ratio as a metric (ns/op is not meaningful).
func BenchmarkFairness(b *testing.B) {
	for _, v := range variants {
		for _, p := range profiles {
			b.Run(v.name+"/"+p.Name, func(b *testing.B) {
				b.ReportAllocs()
				ratio, err := runFairness(p, 4, v.cfg, 1500*time.Millisecond)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(ratio, "fairness")
			})
		}
	}
}

// BenchmarkBulkJoint is the DB single-writer shape: ONE ClassBulk stream writing
// 1 MiB payloads (the block-protocol chunk size, which fragments into frames)
// over a real TCP loopback, while a background goroutine samples control-ping RTT.
// It reports both single-stream throughput (MiB/s via SetBytes) and the ping tail
// as custom metrics, so the send-path A/B (sync vs batched) shows the joint
// latency<->throughput effect in one benchmark. Allocs/op make the pooling win
// visible.
func BenchmarkBulkJoint(b *testing.B) {
	for _, sv := range sendVariants {
		b.Run(sv.name, func(b *testing.B) {
			b.ReportAllocs()
			client, cleanup := loopbackSession(b, sv.cfg)
			defer cleanup()
			bulk, err := client.OpenStream()
			if err != nil {
				b.Fatal(err)
			}
			bulk.SetPriority(yamux.ClassBulk)
			if _, err := bulk.Write([]byte{tagBulk}); err != nil {
				b.Fatal(err)
			}

			// Background control-ping sampler competing with the bulk stream.
			stop := make(chan struct{})
			var pingMu sync.Mutex
			var pings []time.Duration
			var pingWG sync.WaitGroup
			pingWG.Add(1)
			go func() {
				defer pingWG.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					d, err := client.Ping()
					if err != nil {
						return
					}
					pingMu.Lock()
					pings = append(pings, d)
					pingMu.Unlock()
					time.Sleep(time.Millisecond)
				}
			}()

			buf := make([]byte, 1<<20)
			b.SetBytes(int64(len(buf)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := bulk.Write(buf); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			close(stop)
			pingWG.Wait()
			pingMu.Lock()
			defer pingMu.Unlock()
			if len(pings) > 0 {
				s := summarize(pings)
				b.ReportMetric(float64(s.avg.Microseconds()), "ping-avg-us")
				b.ReportMetric(float64(s.p99.Microseconds()), "ping-p99-us")
			}
		})
	}
}

// BenchmarkScale sweeps the concurrent bulk-stream count over the emulated LAN
// link, measuring control-ping RTT under load for each send mode. It exposes
// scheduler-contention (sync) vs pipelining (batched) behavior as the stream
// fan-out grows — the input to the allocation/contention decision. The committed
// counts stay small so the whole suite stays fast under the CI `-benchtime 1x`
// smoke (256+ streams over the in-process link cost >100s); bump `counts` locally
// for a deeper manual sweep.
func BenchmarkScale(b *testing.B) {
	counts := []int{1, 8, 32}
	p := profiles[0] // LAN
	for _, sv := range sendVariants {
		for _, n := range counts {
			b.Run(fmt.Sprintf("%s/streams=%d", sv.name, n), func(b *testing.B) {
				b.ReportAllocs()
				client, cleanup, err := linkedSessions(p, 4<<20, sv.cfg)
				if err != nil {
					b.Fatal(err)
				}
				defer cleanup()
				stop := make(chan struct{})
				wg := startBulk(client, n, stop)
				defer func() { close(stop); wg.Wait() }()
				time.Sleep(200 * time.Millisecond) // ramp the bulk load
				samples := make([]time.Duration, 0, b.N)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					d, err := client.Ping()
					if err != nil {
						b.Fatal(err)
					}
					samples = append(samples, d)
				}
				b.StopTimer()
				reportLatency(b, samples)
			})
		}
	}
}

func loopbackSession(b *testing.B, cfg func(*yamux.Config)) (*yamux.Session, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	accCh := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accCh <- c }()
	cc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
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
		b.Fatal(err)
	}
	server, err := yamux.Server(sc, c)
	if err != nil {
		b.Fatal(err)
	}
	go serve(server)
	return client, func() { client.Close(); server.Close(); ln.Close() }
}
