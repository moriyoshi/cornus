package server

import (
	"context"
	"os"
	"sync"

	psnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"cornus/pkg/observability"
)

// The cornus server's own resource usage: CPU, memory, threads, file
// descriptors, disk and network I/O.
//
// # Why these ride the meter instead of the metrics recorder
//
// The workload collector samples on its own ticker and pushes OTLP straight at
// the acceptance path (metricsrecorder.go). These do the opposite: they are
// registered as OBSERVABLE instruments on the global meter and sampled by the
// SDK during collection. Two reasons.
//
// First, destinations. An instrument on the meter reaches every reader the
// provider has — the built-in store via the in-process bridge, the Prometheus
// /metrics endpoint, and any external OTLP backend — from one implementation.
// The recorder's push path reaches only the store and the re-export upstream, so
// duplicating the server's own numbers there would mean maintaining two answers
// to the same question.
//
// Second, the objection that rules the meter out for workloads does not apply
// here. An observable callback runs during collection, so a slow one stalls the
// whole cycle; that is a real risk for a call into a Docker socket or a
// Kubernetes API server, and not a risk for reading this process's own /proc
// entry, which cannot block on anything remote.
//
// # What the network counters actually measure
//
// process.Process has no portable per-process network accounting, so
// cornus.server.network.io reads the NETWORK NAMESPACE's counters. In a
// container — the deployment this is most useful for — that is the server's
// traffic and nothing else. On a host install it is the whole host's traffic,
// which is a different quantity wearing the same name. The metric is named with
// a `cornus.` prefix rather than semconv's `process.network.io` precisely so it
// does not claim to be the per-process figure that name promises.

// selfMetricsOnce guards registration. The instruments are process-global (they
// bind to the global meter), so registering twice — two Servers in one test
// binary — would double every reported value.
var selfMetricsOnce sync.Once

// registerSelfMetrics installs the observable instruments reporting this
// process's resource usage.
//
// Registration errors are ignored throughout, matching newInstruments and
// registerFileCacheMetrics: on error the SDK hands back a working no-op, so a
// failure costs that one series and nothing else. When telemetry is off entirely
// the callbacks bind to a no-op meter and never fire, so the /proc reads below
// cost nothing in the default configuration.
func (s *Server) registerSelfMetrics() {
	if !s.cfg.ObsRecordMetrics {
		return
	}
	// Point the SDK bridge at this server's acceptance path, so everything on the
	// meter lands in the store and the re-export upstream alongside the workload
	// metrics. Same seam as the two recorders, and for the same reason: a feed
	// that writes to the store directly silently skips re-export.
	//
	// The bridge's reader was installed by observability.Setup back at process
	// start; this is the destination it has been waiting for. Exports collected
	// in between were dropped, which at a one-minute interval is none.
	if s.obsIngestEnabled() {
		observability.SetMetricSink(func(otlp []byte) error {
			return s.acceptOTLP(obsSignalMetrics, otlp)
		})
	}
	selfMetricsOnce.Do(registerProcessMetrics)
}

func registerProcessMetrics() {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		// Nothing to report and nothing to do about it. Not fatal: a server that
		// cannot describe itself must still serve.
		return
	}
	m := observability.Meter()

	cpuTime, _ := m.Float64ObservableCounter("process.cpu.time",
		metric.WithUnit("s"),
		metric.WithDescription("Total CPU time consumed by the cornus server process."))
	memUsage, _ := m.Int64ObservableUpDownCounter("process.memory.usage",
		metric.WithUnit("By"),
		metric.WithDescription("Resident set size of the cornus server process."))
	memVirtual, _ := m.Int64ObservableUpDownCounter("process.memory.virtual",
		metric.WithUnit("By"),
		metric.WithDescription("Virtual memory size of the cornus server process."))
	threads, _ := m.Int64ObservableUpDownCounter("process.thread.count",
		metric.WithUnit("{thread}"),
		metric.WithDescription("OS threads in the cornus server process."))
	fds, _ := m.Int64ObservableUpDownCounter("process.open_file_descriptor.count",
		metric.WithUnit("{count}"),
		metric.WithDescription("File descriptors held open by the cornus server process."))
	diskIO, _ := m.Int64ObservableCounter("process.disk.io",
		metric.WithUnit("By"),
		metric.WithDescription("Bytes read from and written to disk by the cornus server process."))
	netIO, _ := m.Int64ObservableCounter("cornus.server.network.io",
		metric.WithUnit("By"),
		metric.WithDescription("Bytes transferred in the cornus server's network namespace. Namespace-scoped, not per-process: on a host install this is the host's traffic."))

	var (
		userMode   = metric.WithAttributes(attribute.String(attrCPUMode, "user"))
		systemMode = metric.WithAttributes(attribute.String(attrCPUMode, "system"))
		ioRead     = metric.WithAttributes(attribute.String(attrDiskDirection, "read"))
		ioWrite    = metric.WithAttributes(attribute.String(attrDiskDirection, "write"))
		netRecv    = metric.WithAttributes(attribute.String(attrNetDirection, "receive"))
		netSend    = metric.WithAttributes(attribute.String(attrNetDirection, "transmit"))
	)

	// One callback for all of them rather than one per instrument: every value
	// below comes from the same few reads, and splitting them would multiply the
	// /proc traffic by the instrument count for no gain. Each read failing
	// independently is deliberate — a kernel that will not report file
	// descriptors should not also cost us the memory figure.
	_, _ = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		if t, err := proc.TimesWithContext(ctx); err == nil {
			// gopsutil reports these in seconds already, which is the unit
			// semconv specifies.
			o.ObserveFloat64(cpuTime, t.User, userMode)
			o.ObserveFloat64(cpuTime, t.System, systemMode)
		}
		if mi, err := proc.MemoryInfoWithContext(ctx); err == nil && mi != nil {
			o.ObserveInt64(memUsage, int64(mi.RSS))
			o.ObserveInt64(memVirtual, int64(mi.VMS))
		}
		if n, err := proc.NumThreadsWithContext(ctx); err == nil {
			o.ObserveInt64(threads, int64(n))
		}
		if n, err := proc.NumFDsWithContext(ctx); err == nil {
			o.ObserveInt64(fds, int64(n))
		}
		if io, err := proc.IOCountersWithContext(ctx); err == nil && io != nil {
			o.ObserveInt64(diskIO, int64(io.ReadBytes), ioRead)
			o.ObserveInt64(diskIO, int64(io.WriteBytes), ioWrite)
		}
		if ifaces, err := psnet.IOCountersWithContext(ctx, false); err == nil && len(ifaces) > 0 {
			// pernic=false already returns a single summed entry, so there is no
			// per-interface breakdown to attach and none is claimed.
			o.ObserveInt64(netIO, int64(ifaces[0].BytesRecv), netRecv)
			o.ObserveInt64(netIO, int64(ifaces[0].BytesSent), netSend)
		}
		return nil
	}, cpuTime, memUsage, memVirtual, threads, fds, diskIO, netIO)
}
