//go:build linux

package incushost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// busyState is an instance state with plausible non-zero counters, used to prove
// the metrics translation moves every field to the right place rather than
// happening to agree on zero.
func busyState() *incusapi.InstanceState {
	return &incusapi.InstanceState{
		Status:     "Running",
		StatusCode: incusapi.Running,
		CPU:        incusapi.InstanceStateCPU{Usage: 1_234_000_000},
		Memory:     incusapi.InstanceStateMemory{Usage: 64 << 20, Total: 512 << 20},
		Processes:  17,
		Network: map[string]incusapi.InstanceStateNetwork{
			"eth0": {Counters: incusapi.InstanceStateNetworkCounters{
				BytesReceived: 100, PacketsReceived: 10, ErrorsReceived: 1, PacketsDroppedInbound: 2,
				BytesSent: 200, PacketsSent: 20, ErrorsSent: 3, PacketsDroppedOutbound: 4,
			}},
		},
	}
}

// TestSampleMetricsTranslatesInstanceStateCounters pins the metrics mapping the
// observability collector records: Incus's structured state becomes a neutral
// ResourceSample with memory, pids and per-interface network counters carried
// exactly, and CPU reported as a total only (Incus has no user/system split, so
// inventing one would be a lie in every recorded series).
func TestSampleMetricsTranslatesInstanceStateCounters(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = busyState()

	s, err := b.SampleMetrics(context.Background(), "web", 0)
	if err != nil {
		t.Fatalf("SampleMetrics: %v", err)
	}
	if s.CPUTime == nil || s.CPUTime.Total != 1_234_000_000 {
		t.Fatalf("cpu total = %+v", s.CPUTime)
	}
	if s.CPUTime.User != 0 || s.CPUTime.System != 0 {
		t.Errorf("Incus reports no user/system split; got %+v", s.CPUTime)
	}
	if s.MemUsage != 64<<20 || s.MemLimit != 512<<20 {
		t.Errorf("memory = %d/%d", s.MemUsage, s.MemLimit)
	}
	if s.Pids != 17 {
		t.Errorf("pids = %d, want 17", s.Pids)
	}
	n, ok := s.Networks["eth0"]
	if !ok {
		t.Fatalf("no eth0 counters in %+v", s.Networks)
	}
	if n.RxBytes != 100 || n.RxPackets != 10 || n.TxBytes != 200 || n.TxPackets != 20 {
		t.Errorf("eth0 counters = %+v", n)
	}
	if s.Time.IsZero() {
		t.Error("sample must be timestamped")
	}
}

// TestSampleMetricsAddressesTheRequestedReplica pins that the replica index
// selects the instance sampled: a per-replica metric attributed to the wrong
// instance is worse than no metric.
func TestSampleMetricsAddressesTheRequestedReplica(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	zero, one := busyState(), busyState()
	one.Processes = 99
	f.states["cornus-web-0"] = zero
	f.states["cornus-web-1"] = one

	s, err := b.SampleMetrics(context.Background(), "web", 1)
	if err != nil {
		t.Fatalf("SampleMetrics: %v", err)
	}
	if s.Pids != 99 {
		t.Fatalf("replica 1 pids = %d, want 99 (sampled the wrong instance?)", s.Pids)
	}
}

// TestSampleMetricsOnAnAbsentReplicaIsNotFound pins that asking for a replica
// that does not exist is deploy.ErrNotFound, so the collector can drop the
// series instead of recording zeros.
func TestSampleMetricsOnAnAbsentReplicaIsNotFound(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	if _, err := b.SampleMetrics(context.Background(), "web", 5); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("absent replica: want ErrNotFound, got %v", err)
	}
	if _, err := b.SampleMetrics(context.Background(), "ghost", 0); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("absent deployment: want ErrNotFound, got %v", err)
	}
}

// TestSamplerReportsNotFoundWhenTheInstanceDisappears pins the race a live
// sampler actually hits: the instance is listed, then deleted before its state
// is read. The seam reports (nil, nil) for that, which must become ErrNotFound
// rather than a zero-valued sample.
func TestSamplerReportsNotFoundWhenTheInstanceDisappears(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	sample := b.sampler("cornus-web-0") // never created
	if _, err := sample(); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("vanished instance: want ErrNotFound, got %v", err)
	}
}

// TestStatsWritesOneDockerFrameThenStops pins the non-streaming Stats contract
// shared with the other host backends: exactly one Docker-format stats object,
// carrying the instance id and the deployment name, then EOF.
func TestStatsWritesOneDockerFrameThenStops(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.states[id] = busyState()

	var buf bytes.Buffer
	if err := b.Stats(context.Background(), "web", api.StatsOptions{}, &buf); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	dec := json.NewDecoder(&buf)
	var frame hostrun.DockerStats
	if err := dec.Decode(&frame); err != nil {
		t.Fatalf("decoding stats frame: %v", err)
	}
	if frame.ID != id || frame.Name != "/web" {
		t.Errorf("frame identity = %q/%q, want %q//web", frame.ID, frame.Name, id)
	}
	if frame.Memory.Usage != 64<<20 || frame.Pids.Current != 17 {
		t.Errorf("frame counters = mem %d pids %d", frame.Memory.Usage, frame.Pids.Current)
	}
	if frame.Networks["eth0"].RxBytes != 100 {
		t.Errorf("frame networks = %+v", frame.Networks)
	}
	if dec.More() {
		t.Error("non-streaming Stats must write exactly one frame")
	}
}

// TestStatsPropagatesASamplingFailure pins that a daemon error while reading
// state fails the stream instead of emitting a fabricated zero frame.
func TestStatsPropagatesASamplingFailure(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	// Model the instance being torn down between the listing and the state read:
	// the seam reports "no such instance" for a name that was just listed.
	f.states[id] = nil

	var buf bytes.Buffer
	err := b.Stats(context.Background(), "web", api.StatsOptions{}, &buf)
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("want ErrNotFound when the instance cannot be read, got %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("no frame should be written on failure, got %q", buf.String())
	}
}

// TestLogsWarnsOnlyAboutTheTimeOptionsItCannotHonor pins the exact boundary of
// this backend's log support after the console-attach work. An Incus console is
// a serial byte stream that carries NO per-line timestamps in either form
// (ring-buffer snapshot or live attach), so --since/--until/--timestamps have no
// source at all and are warned about; --follow and --tail do have one and must
// NOT warn, because warning about an option that was in fact honored teaches
// operators to ignore the warnings.
func TestLogsWarnsOnlyAboutTheTimeOptionsItCannotHonor(t *testing.T) {
	buf := captureLogs(t)
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.consoles[id] = []byte("line\n")

	err := b.Logs(context.Background(), "web", api.LogOptions{
		Since:      "1h",
		Until:      "10m",
		Timestamps: true,
	}, new(bytes.Buffer))
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{
		"cannot filter console logs by time",
		"cannot prefix console logs with timestamps",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing warning %q in:\n%s", want, logs)
		}
	}
	for _, unwanted := range []string{"does not follow", "does not tail"} {
		if strings.Contains(logs, unwanted) {
			t.Errorf("stale %q warning: the option is supported now:\n%s", unwanted, logs)
		}
	}
}

// TestLogsDoesNotWarnForSupportedOptions pins that the options the console CAN
// satisfy pass through silently — "all" (the whole snapshot), a line count, and
// a follow.
func TestLogsDoesNotWarnForSupportedOptions(t *testing.T) {
	for _, opts := range []api.LogOptions{
		{Tail: "all"},
		{Tail: "1"},
		{Tail: "0"},
		{Follow: true},
	} {
		buf := captureLogs(t)
		f := newFakeConn()
		b := testBackend(f)
		id := applyOne(t, b, f, "web")
		f.consoles[id] = []byte("line\n")
		f.liveConsole = nil

		ctx := context.Background()
		if opts.Follow {
			f.liveConsole = []byte("")
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel() // the follow ends as soon as the sink reports EOF
		}
		if err := b.Logs(ctx, "web", opts, new(bytes.Buffer)); err != nil {
			t.Fatalf("Logs(%+v): %v", opts, err)
		}
		if strings.Contains(buf.String(), "level=WARN") {
			t.Fatalf("%+v should not warn, got:\n%s", opts, buf.String())
		}
	}
}

// TestLogsTailReturnsOnlyTheLastLines proves --tail is really applied to the
// console snapshot rather than warned about and ignored. This is the assertion
// e2e/scenarios/mcp-stdio-tools.star currently skips for the incus target.
func TestLogsTailReturnsOnlyTheLastLines(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.consoles[id] = []byte("one\ntwo\nthree\n")

	for _, tc := range []struct {
		tail string
		want string
	}{
		{"1", "three\n"},
		{"2", "two\nthree\n"},
		{"10", "one\ntwo\nthree\n"}, // more than there is: everything
		{"all", "one\ntwo\nthree\n"},
		{"", "one\ntwo\nthree\n"},
		{"0", ""}, // docker's `--tail 0`: no history at all
	} {
		var buf bytes.Buffer
		if err := b.Logs(context.Background(), "web", api.LogOptions{Tail: tc.tail}, &buf); err != nil {
			t.Fatalf("Logs(tail=%q): %v", tc.tail, err)
		}
		if got := destdcopy(t, buf.Bytes()); got != tc.want {
			t.Errorf("tail %q = %q, want %q", tc.tail, got, tc.want)
		}
	}
}

// TestLogsRejectsAMalformedTail pins that a tail value that is neither "all" nor
// a non-negative number is an error. Before tail was implemented any value was
// accepted and ignored; now that it is honored, silently treating garbage as
// "everything" would return more than the caller asked for.
func TestLogsRejectsAMalformedTail(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	for _, tail := range []string{"twelve", "-3", "1.5"} {
		if err := b.Logs(context.Background(), "web", api.LogOptions{Tail: tail}, new(bytes.Buffer)); err == nil {
			t.Errorf("tail %q should be rejected", tail)
		}
	}
}

// TestLogsFollowStreamsTheLiveConsoleAfterTheSnapshot proves --follow really
// attaches to the live console: the snapshot is replayed first, the attached
// stream's output follows it, and cancelling the context both ends the stream
// and detaches (releasing the instance's single console slot).
func TestLogsFollowStreamsTheLiveConsoleAfterTheSnapshot(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	id := applyOne(t, b, f, "web")
	f.consoles[id] = []byte("history\n")
	f.liveConsole = []byte("live\n")

	ctx, cancel := context.WithCancel(context.Background())
	var buf syncBuffer
	done := make(chan error, 1)
	go func() { done <- b.Logs(ctx, "web", api.LogOptions{Follow: true}, &buf) }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(destdcopy(t, buf.Bytes()), "live\n") {
		if time.Now().After(deadline) {
			t.Fatalf("live console output never arrived, got %q", destdcopy(t, buf.Bytes()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Logs(follow): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Logs(follow) did not return after the context was cancelled")
	}

	if got, want := destdcopy(t, buf.Bytes()), "history\nlive\n"; got != want {
		t.Errorf("follow output = %q, want %q (snapshot then live)", got, want)
	}
	if f.attachedConsole != id {
		t.Errorf("attached to console %q, want %q", f.attachedConsole, id)
	}
	if f.consoleDetached.Load() == 0 {
		t.Error("the console was never detached, so the instance's console slot leaks")
	}
}

// TestLogsFollowReportsAnUnavailableConsole pins that a console that cannot be
// attached is an error rather than a silent degradation to a snapshot: Incus
// allows one console client per instance, and quietly returning history for a
// --follow would look like a workload that stopped producing output.
func TestLogsFollowReportsAnUnavailableConsole(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	err := b.Logs(context.Background(), "web", api.LogOptions{Follow: true}, new(bytes.Buffer))
	if err == nil || !strings.Contains(err.Error(), "attaching to the console") {
		t.Fatalf("want an attach error, got %v", err)
	}
}

// syncBuffer is a bytes.Buffer safe to read while the follow goroutine writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// destdcopy strips the stdcopy stdout framing Logs wraps its output in, so a
// test can assert on the bytes the workload actually produced.
func destdcopy(t *testing.T, framed []byte) string {
	t.Helper()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, bytes.NewReader(framed)); err != nil {
		t.Fatalf("destdcopy: %v", err)
	}
	return out.String()
}

// TestLogsRejectsAMalformedUntil pins that a bad time value is an error even
// though the filter itself cannot be applied: accepting it would silently return
// unfiltered logs for a request the caller believes was honored.
func TestLogsRejectsAMalformedUntil(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	if err := b.Logs(context.Background(), "web", api.LogOptions{Until: "not-a-time"}, new(bytes.Buffer)); err == nil {
		t.Fatal("expected an error for a malformed --until")
	}
}

// TestLogsAddressesTheRequestedReplica pins that --instance selects which
// replica's console is streamed.
func TestLogsAddressesTheRequestedReplica(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.consoles["cornus-web-0"] = []byte("zero\n")
	f.consoles["cornus-web-1"] = []byte("one\n")

	var buf bytes.Buffer
	if err := b.Logs(context.Background(), "web", api.LogOptions{Instance: 1}, &buf); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(buf.String(), "one") || strings.Contains(buf.String(), "zero") {
		t.Fatalf("streamed the wrong replica: %q", buf.String())
	}
}

// TestStatusIgnoresInstancesThisBackendDoesNotManage pins the ownership filter:
// an instance in the same Incus project that cornus did not create (no
// user.cornus.managed key) must never appear in a deployment's status, and must
// not make List invent a deployment for it.
func TestStatusIgnoresInstancesThisBackendDoesNotManage(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	f.insts["someones-vm"] = &incusapi.Instance{
		Name: "someones-vm", Status: "Running", StatusCode: incusapi.Running,
		InstancePut: incusapi.InstancePut{Config: map[string]string{"user." + deploy.LabelApp: "web"}},
	}

	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 1 || st.Instances[0].ID != "cornus-web-0" {
		t.Fatalf("unmanaged instance leaked into status: %+v", st.Instances)
	}
	list, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("unmanaged instance leaked into list: %+v", list)
	}
}

// TestDeployStatusReportsIncusStateVocabulary pins the state mapping: the State
// string is Incus's own lowercased vocabulary (which is not portable), while
// Running is the portable boolean derived from the numeric status code — so a
// "Frozen" instance is reported, and is not running.
func TestDeployStatusReportsIncusStateVocabulary(t *testing.T) {
	st := deployStatus("web", []incusapi.Instance{
		{Name: "cornus-web-1", Status: "Frozen", StatusCode: incusapi.Frozen},
		{Name: "cornus-web-0", Status: "Running", StatusCode: incusapi.Running,
			InstancePut: incusapi.InstancePut{Config: map[string]string{imageConfigKey: "localhost:5000/app:v1"}}},
	}, nil)
	if st.Backend != "incus" || st.Name != "web" {
		t.Fatalf("status meta = %+v", st)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(st.Instances))
	}
	// Sorted by name, so replica 0 comes first regardless of input order.
	if st.Instances[0].ID != "cornus-web-0" || !st.Instances[0].Running || st.Instances[0].State != "running" {
		t.Errorf("replica 0 = %+v", st.Instances[0])
	}
	if st.Instances[1].State != "frozen" || st.Instances[1].Running {
		t.Errorf("a frozen instance must be reported and not running: %+v", st.Instances[1])
	}
	if st.Image != "localhost:5000/app:v1" {
		t.Errorf("image = %q (must come from the instance config, Incus does not surface the OCI ref)", st.Image)
	}
}

// TestOriginFromConfigIgnoresNonCornusConfigKeys pins that provenance is
// reconstructed only from the user.* namespace cornus stamps, and that an
// instance with no origin keys reports no origin (rather than an empty one).
func TestOriginFromConfigIgnoresNonCornusConfigKeys(t *testing.T) {
	got := originFromConfig(map[string]string{
		"user." + deploy.LabelOriginUser: "alice",
		"limits.memory":                  "1024",
		"environment.PATH":               "/bin",
	})
	if got == nil || got.User != "alice" {
		t.Fatalf("origin = %+v", got)
	}
	if origin := originFromConfig(map[string]string{"limits.memory": "1024"}); origin != nil {
		t.Fatalf("no origin keys should yield nil, got %+v", origin)
	}
}
