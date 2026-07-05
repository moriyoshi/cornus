package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hugelgupf/p9/p9"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// TestMountBytesMetered drives the unified caretaker mount relay end-to-end and
// asserts the server records per-mount RX bytes on cornus.mount.io.bytes. A
// ManualReader is installed as the global MeterProvider before the server is
// built, so its instruments bind to it. Hermetic (no root / kernel 9p).
func TestMountBytesMetered(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	dir := t.TempDir()
	const marker = "METERED-MOUNT-PAYLOAD"
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}

	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	srv := newTestServer(t, fb) // New() -> newInstruments() binds to the manual reader
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", wsBase)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data", ReadOnly: true}},
		},
		LocalMounts: []deploywire.LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as, map[string]string{"m0": dir}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var mounts []deploy.AttachMount
	select {
	case mounts = <-fb.mounts:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
	}
	session := mounts[0].Session

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach")
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()

	stream, err := wire.OpenTagged(mux, wire.TagMount)
	if err != nil {
		t.Fatalf("open mount stream: %v", err)
	}
	if _, err := io.WriteString(stream, session+"\n"+"m0"+"\n"); err != nil {
		t.Fatalf("send session/name: %v", err)
	}
	p9c, err := p9.NewClient(stream)
	if err != nil {
		t.Fatalf("p9 client: %v", err)
	}
	root, err := p9c.Attach("")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	_, f, err := root.Walk([]string{"marker"})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if _, _, err := f.Open(p9.ReadOnly); err != nil {
		t.Fatalf("open: %v", err)
	}
	buf := make([]byte, 64)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != marker {
		t.Fatalf("content = %q, want %q", buf[:n], marker)
	}

	// The server records rx as it writes file data toward the pod; poll a moment
	// so the recording (which happens in the relay's copy goroutine) is observed.
	deadline := time.Now().Add(3 * time.Second)
	for {
		rx := sumMountBytes(t, reader, "m0", "rx")
		if rx > 0 {
			if rx < int64(len(marker)) {
				t.Errorf("cornus.mount.io.bytes rx = %d, want >= %d (payload)", rx, len(marker))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cornus.mount.io.bytes{name=m0,direction=rx} never recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sumMountBytes collects and sums the cornus.mount.io.bytes points matching the
// given mount name and direction.
func sumMountBytes(t *testing.T, reader sdkmetric.Reader, name, direction string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "cornus.mount.io.bytes" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("cornus.mount.io.bytes is %T, want Sum[int64]", m.Data)
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
