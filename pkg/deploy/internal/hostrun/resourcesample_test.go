package hostrun

import (
	"testing"
	"time"
)

// TestWorkingSetSubtractsPageCache pins the rule that makes a recorded memory
// number agree with the one `docker stats` shows for the same container.
//
// Without it the store would report cgroup "usage", which includes reclaimable
// page cache and can overstate an I/O-heavy workload by an order of magnitude.
// Two operators looking at the same container would then read two different
// numbers, and neither would be wrong about what it measured — which is exactly
// the situation that makes a dashboard untrustworthy.
func TestWorkingSetSubtractsPageCache(t *testing.T) {
	cases := []struct {
		name  string
		usage uint64
		stats map[string]uint64
		want  uint64
	}{
		{"cgroup v2", 1000, map[string]uint64{"inactive_file": 400}, 600},
		{"cgroup v1", 1000, map[string]uint64{"total_inactive_file": 300, "inactive_file": 900}, 700},
		{"no stats map", 1000, nil, 1000},
		{"cache exceeds usage", 100, map[string]uint64{"inactive_file": 400}, 0},
	}
	for _, tc := range cases {
		if got := workingSet(tc.usage, tc.stats); got != tc.want {
			t.Errorf("%s: workingSet(%d, %v) = %d, want %d", tc.name, tc.usage, tc.stats, got, tc.want)
		}
	}
}

func TestStatsSampleToResourceSample(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	s := StatsSample{
		Read:      now,
		CPUTotal:  9000,
		CPUUser:   6000,
		CPUKernel: 3000,
		MemUsage:  2048,
		MemLimit:  8192,
		MemStats:  map[string]uint64{"inactive_file": 48},
		Pids:      7,
		Blkio: []DockerBlkioEntry{
			{Op: "Read", Value: 100}, {Op: "Write", Value: 200},
			{Op: "Read", Value: 50}, {Op: "Sync", Value: 999},
		},
		Networks: map[string]DockerNetStats{"eth0": {RxBytes: 10, TxBytes: 20, RxPackets: 1, TxPackets: 2}},
	}
	rs := s.ToResourceSample()

	if !rs.Time.Equal(now) {
		t.Errorf("Time = %v, want %v", rs.Time, now)
	}
	if rs.CPUTime == nil || rs.CPUTime.Total != 9000 || rs.CPUTime.User != 6000 || rs.CPUTime.System != 3000 {
		t.Errorf("CPUTime = %+v, want total 9000 / user 6000 / system 3000", rs.CPUTime)
	}
	if rs.MemUsage != 2000 {
		t.Errorf("MemUsage = %d, want 2000 (2048 less the 48-byte page cache)", rs.MemUsage)
	}
	if rs.MemLimit != 8192 || rs.Pids != 7 {
		t.Errorf("MemLimit/Pids = %d/%d, want 8192/7", rs.MemLimit, rs.Pids)
	}
	// Read and Write are summed across devices; Sync/Async are ignored because
	// they overlap the first two and would double-count.
	if rs.DiskRead == nil || *rs.DiskRead != 150 {
		t.Errorf("DiskRead = %v, want 150", rs.DiskRead)
	}
	if rs.DiskWrite == nil || *rs.DiskWrite != 200 {
		t.Errorf("DiskWrite = %v, want 200", rs.DiskWrite)
	}
	if n, ok := rs.Networks["eth0"]; !ok || n.RxBytes != 10 || n.TxBytes != 20 {
		t.Errorf("Networks[eth0] = %+v, want rx 10 / tx 20", n)
	}
}

// TestUnobservedFamiliesStayAbsent is the distinction the whole pointer-and-nil
// shape exists for: a gVisor instance's netstack is invisible from the host and
// runc's stats carry no network counters, so those samples must produce NO
// series rather than a series reading zero. "This container moved no bytes" and
// "cornus could not see whether it did" are different claims.
func TestUnobservedFamiliesStayAbsent(t *testing.T) {
	rs := StatsSample{Read: time.Now(), MemUsage: 100}.ToResourceSample()
	if rs.Networks != nil {
		t.Errorf("Networks = %v, want nil when the sampler observed none", rs.Networks)
	}
	if rs.DiskRead != nil || rs.DiskWrite != nil {
		t.Errorf("disk counters = %v/%v, want nil when the sampler observed none", rs.DiskRead, rs.DiskWrite)
	}
}

// TestDockerStatsToResourceSampleIgnoresPreread guards the dockerhost path: the
// daemon hands back a frame carrying BOTH a current and a previous reading, and
// only the current one is the fact. Recording a difference here would throw away
// the reader's ability to rate() over a window of their own choosing.
func TestDockerStatsToResourceSampleIgnoresPreread(t *testing.T) {
	cur := StatsSample{Read: time.Unix(200, 0).UTC(), CPUTotal: 9000, CPUUser: 6000, CPUKernel: 3000, MemUsage: 512}
	prev := StatsSample{Read: time.Unix(100, 0).UTC(), CPUTotal: 4000, CPUUser: 2500, CPUKernel: 1500, MemUsage: 256}

	rs := ToDockerStats("id", "web", prev, cur).ToResourceSample()
	if rs.CPUTime == nil || rs.CPUTime.Total != 9000 {
		t.Errorf("CPUTime.Total = %v, want the cumulative 9000, not the 5000 delta", rs.CPUTime)
	}
	if rs.MemUsage != 512 {
		t.Errorf("MemUsage = %d, want the current 512", rs.MemUsage)
	}
	if !rs.Time.Equal(cur.Read) {
		t.Errorf("Time = %v, want the current read time %v", rs.Time, cur.Read)
	}
}
