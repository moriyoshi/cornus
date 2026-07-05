//go:build linux

package barehost

import (
	"bytes"
	"errors"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// newTestBackend builds a backend over a fake runtime with a real DataDir (so
// the record store works) and no image store — lifecycle tests seed records
// directly and exercise Status/List/Stop/Start/Delete/Logs without root, the
// pull/rootfs path, or a real runtime binary.
func newTestBackend(t *testing.T) (*Backend, *fakeRuntime) {
	t.Helper()
	rt := newFakeRuntime()
	b := newBackend(Config{DataDir: t.TempDir(), Runtime: "runc"}, rt, nil, false)
	// Unit tests seed records and drive lifecycle without root/CNI; keep the DNS
	// resolver from ever binding a socket. The handler/zone logic is covered
	// separately (dns_linux_test.go) with an explicitly-constructed manager.
	b.dns.enabled = false
	return b, rt
}

// seedInstance registers a record and a matching fake-runtime container so the
// lifecycle operations have something to act on.
func seedInstance(t *testing.T, b *Backend, rt *fakeRuntime, app string, replica int, running bool) *instanceRecord {
	t.Helper()
	id := instanceName(app, replica)
	rec := &instanceRecord{ID: id, App: app, Image: "reg/" + app + ":latest", Replica: replica, BundleDir: b.bundleDir(id), LogPath: b.logPath(id)}
	if err := b.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	status := runcStateCreated
	if running {
		status = runcStateRunning
	}
	rt.cs[id] = &runtimeState{ID: id, Status: status, Bundle: rec.BundleDir}
	return rec
}

func TestStatusAndList(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, true)
	seedInstance(t, b, rt, "web", 1, false)
	seedInstance(t, b, rt, "db", 0, true)

	st, err := b.Status(ctx, "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Name != "web" || st.Backend != "bare" || st.Image != "reg/web:latest" {
		t.Errorf("Status header = %+v", st)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(st.Instances))
	}
	if !st.Instances[0].Running || st.Instances[0].State != "running" {
		t.Errorf("instance 0 = %+v, want running", st.Instances[0])
	}
	if st.Instances[1].Running || st.Instances[1].State != "created" {
		t.Errorf("instance 1 = %+v, want created/not-running", st.Instances[1])
	}

	all, err := b.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List: want 2 deployments (web, db), got %d", len(all))
	}

	// Status of an unknown deployment is empty, not an error.
	empty, err := b.Status(ctx, "nope")
	if err != nil {
		t.Fatalf("Status unknown: %v", err)
	}
	if len(empty.Instances) != 0 {
		t.Errorf("unknown deployment should have no instances, got %d", len(empty.Instances))
	}
}

func TestStopKeepsRecordDeleteRemovesIt(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, true)

	if err := b.Stop(ctx, "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop keeps the record so Start can re-run it.
	recs, _ := b.recordsForApp("web")
	if len(recs) != 1 {
		t.Fatalf("Stop must keep the record, got %d", len(recs))
	}
	// The runtime container was torn down (delete verb issued).
	sawDelete := false
	for _, c := range rt.calls {
		if c == "delete:cornus-web-0" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("Stop should delete the runtime container; calls=%v", rt.calls)
	}

	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	recs, _ = b.recordsForApp("web")
	if len(recs) != 0 {
		t.Errorf("Delete must remove the record, got %d", len(recs))
	}
}

func TestStopStartUnknownWrapsErrNotFound(t *testing.T) {
	b, _ := newTestBackend(t)
	ctx := t.Context()
	for _, op := range []struct {
		name string
		fn   func() error
	}{
		{"Stop", func() error { return b.Stop(ctx, "ghost") }},
		{"Start", func() error { return b.Start(ctx, "ghost") }},
		{"Restart", func() error { return b.Restart(ctx, "ghost") }},
	} {
		if err := op.fn(); !errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("%s(unknown) = %v, want wrap of ErrNotFound", op.name, err)
		}
	}
	// Delete of an unknown name is a no-op success (delete-if-exists).
	if err := b.Delete(ctx, "ghost"); err != nil {
		t.Errorf("Delete(unknown) = %v, want nil", err)
	}
}

func TestStartRestartsStoppedInstance(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedInstance(t, b, rt, "web", 0, false) // created, not running
	// Simulate a stopped-then-deleted runtime container (Start recreates from the
	// bundle): drop the fake container so State reports "does not exist".
	delete(rt.cs, "cornus-web-0")

	if err := b.Start(ctx, "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st, _ := b.rt.State(ctx, "cornus-web-0")
	if st.Status != runcStateRunning {
		t.Errorf("after Start, runtime state = %q, want running", st.Status)
	}
}

func TestLogsFramesAsStdout(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	rec := seedInstance(t, b, rt, "web", 0, true)
	// Write raw container output to the instance's log file.
	fio, err := newFileIO(rec.LogPath)
	if err != nil {
		t.Fatalf("newFileIO: %v", err)
	}
	if _, err := fio.log.WriteString("line-one\nline-two\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	fio.Close()

	var buf bytes.Buffer
	if err := b.Logs(ctx, "web", api.LogOptions{}, &buf); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	// Demux the stdcopy stream: everything should arrive on stdout.
	var out, errOut bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errOut, &buf); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if out.String() != "line-one\nline-two\n" {
		t.Errorf("stdout = %q, want the raw log", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr should be empty (M1 frames all as stdout), got %q", errOut.String())
	}

	// Tail=1 keeps only the last line.
	buf.Reset()
	if err := b.Logs(ctx, "web", api.LogOptions{Tail: "1"}, &buf); err != nil {
		t.Fatalf("Logs tail: %v", err)
	}
	out.Reset()
	errOut.Reset()
	_, _ = stdcopy.StdCopy(&out, &errOut, &buf)
	if out.String() != "line-two\n" {
		t.Errorf("tail=1 stdout = %q, want the last line", out.String())
	}
}

func TestLogsSinceMalformedErrors(t *testing.T) {
	b, rt := newTestBackend(t)
	seedInstance(t, b, rt, "web", 0, true)
	var buf bytes.Buffer
	if err := b.Logs(t.Context(), "web", api.LogOptions{Since: "not-a-time"}, &buf); err == nil {
		t.Fatal("Logs with malformed Since: want error")
	}
}

func TestLogsUnknownDeploymentErrNotFound(t *testing.T) {
	b, _ := newTestBackend(t)
	var buf bytes.Buffer
	if err := b.Logs(t.Context(), "ghost", api.LogOptions{}, &buf); !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("Logs(unknown) = %v, want wrap of ErrNotFound", err)
	}
}

func TestApplyRejectsEmptySpec(t *testing.T) {
	b, _ := newTestBackend(t)
	if _, err := b.Apply(t.Context(), api.DeploySpec{Name: "web"}); err == nil {
		t.Error("Apply without image: want error")
	}
	if _, err := b.Apply(t.Context(), api.DeploySpec{Image: "x"}); err == nil {
		t.Error("Apply without name: want error")
	}
}
