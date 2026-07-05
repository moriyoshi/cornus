package caretaker

import (
	"context"
	"io"
	"net"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestMeterMountStreamCounts verifies meterMountStream records bytes read from the
// server as rx and bytes written to it as tx, under the mount name. It builds a
// ctMetrics directly (bypassing the package's sync.Once singleton) so the counter
// binds to a ManualReader. Hermetic.
func TestMeterMountStreamCounts(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	counter, err := mp.Meter("test").Int64Counter("caretaker.mount.io.bytes")
	if err != nil {
		t.Fatal(err)
	}
	mt := &ctMetrics{mountBytes: counter}

	// server <-> pod pipe; wrap the pod side exactly as runMountStream does.
	podSide, serverSide := net.Pipe()
	defer podSide.Close()
	defer serverSide.Close()
	metered := meterMountStream(mt, "m0", podSide)

	const in = "server-to-pod-bytes" // read by the pod = rx
	go func() {
		_, _ = io.WriteString(serverSide, in)
		serverSide.Close()
	}()
	got, err := io.ReadAll(metered) // pod reads from the server
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != in {
		t.Fatalf("payload = %q, want %q", got, in)
	}

	if rx := sumMountBytes(t, reader, "m0", "rx"); rx != int64(len(in)) {
		t.Errorf("rx = %d, want %d", rx, len(in))
	}
	if tx := sumMountBytes(t, reader, "m0", "tx"); tx != 0 {
		t.Errorf("tx = %d, want 0 (no pod->server writes)", tx)
	}
}

func sumMountBytes(t *testing.T, reader sdkmetric.Reader, name, direction string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "caretaker.mount.io.bytes" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("caretaker.mount.io.bytes is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				n, _ := dp.Attributes.Value("name")
				d, _ := dp.Attributes.Value("direction")
				if n.AsString() == name && d.AsString() == direction {
					total += dp.Value
				}
			}
		}
	}
	return total
}
