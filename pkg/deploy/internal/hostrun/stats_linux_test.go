//go:build linux

package hostrun

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const netDevFixture = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:  102010    1010    0    0    0     0          0         0   102010    1010    0    0    0     0       0          0
  eth0: 1234567     890    1    2    0     0          0         0  7654321      98    3    4    0     0       0          0
  eth1:9876543      12    0    0    0     0          0         0      100       1    0    0    0     0       0          0
`

func TestParseNetDev(t *testing.T) {
	got := parseNetDev(strings.NewReader(netDevFixture))
	if _, ok := got["lo"]; ok {
		t.Fatal("loopback must be excluded")
	}
	if len(got) != 2 {
		t.Fatalf("interfaces = %v", got)
	}
	e := got["eth0"]
	if e.RxBytes != 1234567 || e.RxPackets != 890 || e.RxErrors != 1 || e.RxDropped != 2 {
		t.Fatalf("eth0 rx = %+v", e)
	}
	if e.TxBytes != 7654321 || e.TxPackets != 98 || e.TxErrors != 3 || e.TxDropped != 4 {
		t.Fatalf("eth0 tx = %+v", e)
	}
	// An interface name glued to its first counter (no space after the colon)
	// still parses.
	if got["eth1"].RxBytes != 9876543 || got["eth1"].TxBytes != 100 {
		t.Fatalf("eth1 = %+v", got["eth1"])
	}
}

func TestParseNetDevEmpty(t *testing.T) {
	if got := parseNetDev(strings.NewReader("garbage\nno interfaces here\n")); got != nil {
		t.Fatalf("want nil for content without interfaces, got %v", got)
	}
}

// TestToDockerStatsJSONShape pins the docker-CLI-visible field names of the
// stats frame: memory_stats.stats, blkio_stats.io_service_bytes_recursive and
// networks.
func TestToDockerStatsJSONShape(t *testing.T) {
	cur := StatsSample{
		Read:     time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		MemUsage: 4096,
		MemLimit: 8192,
		MemStats: map[string]uint64{"inactive_file": 1024},
		Blkio:    []DockerBlkioEntry{{Major: 8, Minor: 0, Op: "read", Value: 111}},
		Networks: map[string]DockerNetStats{"eth0": {RxBytes: 1, TxBytes: 2, RxPackets: 3, TxPackets: 4}},
	}
	raw, err := json.Marshal(ToDockerStats("id1", "web", StatsSample{}, cur))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var frame struct {
		Name   string `json:"name"`
		Memory struct {
			Usage uint64            `json:"usage"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
		Blkio struct {
			Entries []struct {
				Op    string `json:"op"`
				Value uint64 `json:"value"`
			} `json:"io_service_bytes_recursive"`
		} `json:"blkio_stats"`
		Networks map[string]struct {
			RxBytes   uint64 `json:"rx_bytes"`
			TxBytes   uint64 `json:"tx_bytes"`
			RxPackets uint64 `json:"rx_packets"`
			TxPackets uint64 `json:"tx_packets"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.Name != "/web" {
		t.Fatalf("name = %q (docker prefixes a slash)", frame.Name)
	}
	if frame.Memory.Usage != 4096 || frame.Memory.Stats["inactive_file"] != 1024 {
		t.Fatalf("memory_stats = %+v", frame.Memory)
	}
	if len(frame.Blkio.Entries) != 1 || frame.Blkio.Entries[0].Op != "read" || frame.Blkio.Entries[0].Value != 111 {
		t.Fatalf("blkio_stats = %+v", frame.Blkio)
	}
	n, ok := frame.Networks["eth0"]
	if !ok || n.RxBytes != 1 || n.TxBytes != 2 || n.RxPackets != 3 || n.TxPackets != 4 {
		t.Fatalf("networks = %+v", frame.Networks)
	}
}

// TestStreamStatsNoStream verifies the --no-stream path: exactly one frame with
// a zeroed precpu block (docker shows 0% CPU for the first frame).
func TestStreamStatsNoStream(t *testing.T) {
	var buf bytes.Buffer
	calls := 0
	sample := func() (StatsSample, error) {
		calls++
		return StatsSample{Read: time.Unix(100, 0), CPUTotal: 500, SysUsage: 1000}, nil
	}
	if err := StreamStats(context.Background(), &buf, "id1", "web", false, sample); err != nil {
		t.Fatalf("StreamStats: %v", err)
	}
	if calls != 1 {
		t.Fatalf("sample called %d times, want 1", calls)
	}
	var frame struct {
		CPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"cpu_stats"`
		PreCPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"precpu_stats"`
	}
	if err := json.Unmarshal(buf.Bytes(), &frame); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if frame.CPU.Usage.Total != 500 {
		t.Fatalf("cpu_stats total = %d, want 500", frame.CPU.Usage.Total)
	}
	if frame.PreCPU.Usage.Total != 0 {
		t.Fatalf("precpu_stats total = %d, want 0 (zeroed first frame)", frame.PreCPU.Usage.Total)
	}
}
