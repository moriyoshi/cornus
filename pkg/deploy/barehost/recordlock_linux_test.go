//go:build linux

package barehost

// The per-record lock, and the corruption it exists to prevent.
//
// Two independent actors read-modify-write the same instance record: the cornus
// server (Stop/Start/Delete/reboot recovery) and whatever is applying the restart
// policy — an in-process goroutine by default, a detached `cornus daemon
// bare-shim` PROCESS under CORNUS_BARE_SHIM. So the tests here come in three
// shapes: lost-update tests within one process, the same across two processes
// (a real child, since a mutex could never cover that), and the semantic failure
// the whole thing is about — an explicitly stopped workload coming back.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// seedRecordDir creates a record directory with one record in it, without going
// through a Backend — these tests exercise the record store itself.
func seedRecordDir(t *testing.T, rec *instanceRecord) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), rec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir record dir: %v", err)
	}
	if err := writeRecordFile(dir, rec, recordTmpName); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	return dir
}

// bumpRestartCount is the mutation both sides of the lost-update tests apply. The
// Gosched widens the read-modify-write window enough that an UNLOCKED version
// loses updates reliably rather than occasionally — the point of the test is to
// fail loudly when the lock is gone.
func bumpRestartCount(r *instanceRecord) error {
	n := r.RestartCount
	runtime.Gosched()
	r.RestartCount = n + 1
	return nil
}

// tryFlock reports whether an exclusive flock on path can be taken right now from
// a fresh descriptor. flock is per open file description, so this answers the same
// question another PROCESS would get.
func tryFlock(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return false
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return true
}

// --- lost updates within one process ---

// TestConcurrentRecordUpdatesDoNotLoseWrites is the in-process lost-update test:
// every increment must land. The two writer roles use the two real staging-file
// names (the server's and the shim's), which is also how the production writers
// differ. Without the lock the final tally comes out short.
func TestConcurrentRecordUpdatesDoNotLoseWrites(t *testing.T) {
	dir := seedRecordDir(t, &instanceRecord{ID: "cornus-web-0", App: "web", DesiredRunning: true})

	const writers, bumps = 8, 40
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		tmp := recordTmpName
		if i%2 == 1 {
			tmp = shimRecordTmpName
		}
		wg.Add(1)
		go func(tmp string) {
			defer wg.Done()
			for j := 0; j < bumps; j++ {
				if _, err := updateRecordAt(dir, tmp, bumpRestartCount); err != nil {
					t.Errorf("updateRecordAt: %v", err)
					return
				}
			}
		}(tmp)
	}
	wg.Wait()

	rec, err := readRecordFile(dir)
	if err != nil {
		t.Fatalf("readRecordFile: %v", err)
	}
	if want := writers * bumps; rec.RestartCount != want {
		t.Errorf("restartCount = %d, want %d: %d update(s) were lost to an unserialized read-modify-write",
			rec.RestartCount, want, want-rec.RestartCount)
	}
}

// --- lost updates across two processes ---

// recordBumpHelperEnv names the record dir the helper child bumps; _N is how many
// times. See helperproc_linux_test.go for the pattern.
const recordBumpHelperEnv = "CORNUS_BARE_TEST_RECORD_BUMP"

// TestBareRecordBumpHelperProcess is not a test: it is the body of the child
// process TestRecordLockSerializesAcrossProcesses spawns. It hammers the same
// record the parent does, through the same locked primitive the shim uses.
func TestBareRecordBumpHelperProcess(t *testing.T) {
	dir := os.Getenv(recordBumpHelperEnv)
	if dir == "" {
		t.Skip("helper process body; only meaningful when re-executed as a child")
	}
	n, err := strconv.Atoi(os.Getenv(recordBumpHelperEnv + "_N"))
	if err != nil {
		t.Fatalf("bad helper iteration count: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := updateRecordAt(dir, shimRecordTmpName, bumpRestartCount); err != nil {
			t.Fatalf("child updateRecordAt: %v", err)
		}
	}
	os.Exit(0)
}

// TestRecordLockSerializesAcrossProcesses is the test no mutex can pass: the
// server and the detached shim are separate processes, so the record lock has to
// be a kernel lock. The child and the parent bump the same counter the same
// number of times; every bump must survive.
func TestRecordLockSerializesAcrossProcesses(t *testing.T) {
	dir := seedRecordDir(t, &instanceRecord{ID: "cornus-web-0", App: "web", DesiredRunning: true})

	const bumps = 200
	cmd := exec.Command(os.Args[0], "-test.run=^TestBareRecordBumpHelperProcess$")
	cmd.Env = append(os.Environ(),
		recordBumpHelperEnv+"="+dir,
		recordBumpHelperEnv+"_N="+strconv.Itoa(bumps),
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	for i := 0; i < bumps; i++ {
		if _, err := updateRecordAt(dir, recordTmpName, bumpRestartCount); err != nil {
			t.Fatalf("parent updateRecordAt: %v", err)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process failed: %v", err)
	}

	rec, err := readRecordFile(dir)
	if err != nil {
		t.Fatalf("readRecordFile: %v", err)
	}
	if want := 2 * bumps; rec.RestartCount != want {
		t.Errorf("restartCount = %d, want %d: %d cross-process update(s) were lost",
			rec.RestartCount, want, want-rec.RestartCount)
	}
}

// --- why the lock is not on record.json ---

// TestRecordLockSurvivesRecordRewrites pins the reason the lock lives on a
// separate, stable path. Records are published by renaming a staging file over
// record.json, which swaps the inode out; a lock held on record.json would then
// be a lock on an unlinked inode, and the next process to come along would lock
// the NEW inode and believe it had exclusive access. The control subtest
// demonstrates exactly that failure on record.json itself.
func TestRecordLockSurvivesRecordRewrites(t *testing.T) {
	rec := &instanceRecord{ID: "cornus-web-0", App: "web", DesiredRunning: true}
	dir := seedRecordDir(t, rec)

	lk, err := lockRecordDir(dir)
	if err != nil {
		t.Fatalf("lockRecordDir: %v", err)
	}
	for i := 0; i < 3; i++ {
		rec.RestartCount = i
		if err := writeRecordFile(dir, rec, recordTmpName); err != nil {
			t.Fatalf("writeRecordFile: %v", err)
		}
	}
	if tryFlock(t, recordLockPath(dir)) {
		t.Error("the record lock was obtainable while held: rewriting the record must not release it")
	}
	lk.release()
	if !tryFlock(t, recordLockPath(dir)) {
		t.Error("the record lock was not released")
	}

	t.Run("locking record.json instead would not hold", func(t *testing.T) {
		f, err := os.OpenFile(recordPath(dir), os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("open record: %v", err)
		}
		defer f.Close()
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatalf("flock record: %v", err)
		}
		if err := writeRecordFile(dir, rec, shimRecordTmpName); err != nil {
			t.Fatalf("writeRecordFile: %v", err)
		}
		if !tryFlock(t, recordPath(dir)) {
			t.Skip("the rename did not detach the lock on this filesystem")
		}
	})
}

// --- a deleted record must stay deleted ---

// TestUpdateRecordDoesNotResurrectADeletedRecord covers the other half of the
// Delete race: a supervisor that finishes a restart just after Delete reaped the
// record must not republish it, or List grows a ghost deployment that no longer
// has a rootfs, netns, or bundle.
func TestUpdateRecordDoesNotResurrectADeletedRecord(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	if err := b.removeRecord(rec.ID); err != nil {
		t.Fatalf("removeRecord: %v", err)
	}

	if _, err := b.updateRecord(rec.ID, bumpRestartCount); err == nil {
		t.Fatal("updateRecord on a deleted record must fail rather than recreate it")
	}
	if _, err := os.Stat(b.recordDir(rec.ID)); !os.IsNotExist(err) {
		t.Errorf("record dir was recreated by an update: %v", err)
	}
	recs, err := b.listRecords()
	if err != nil {
		t.Fatalf("listRecords: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("deleted record came back: %+v", recs)
	}
}

// --- the semantic failure: a stopped workload coming back ---

// TestRestartRacingAStopDoesNotResurrectTheInstance drives the real in-process
// supervisor through the exact interleaving that used to lose a Stop: the
// supervisor re-reads the record, decides to restart, and the operator's Stop
// lands WHILE the runtime is creating the container — i.e. after the decision,
// before the supervisor persists the attempt. Writing back the record it read
// before the Stop cleared DesiredRunning/ExplicitlyStopped, so the next startup
// reconcile relaunched a deployment the operator had stopped.
func TestRestartRacingAStopDoesNotResurrectTheInstance(t *testing.T) {
	b, rt := newTestBackend(t)
	rec := seedInstance(t, b, rt, "web", 0, false)
	rec.DesiredRunning = true
	rec.Restart = "always"
	if err := b.writeRecord(rec); err != nil {
		t.Fatal(err)
	}
	writePidFile(t, b, rec.ID, "4242")

	var once sync.Once
	rt.setCreateHook(func(string) {
		once.Do(func() {
			if err := b.Stop(context.Background(), "web"); err != nil {
				t.Errorf("Stop during restart: %v", err)
			}
		})
	})

	runOnExit(t, b, rec.ID, time.Second)

	after, err := b.readRecord(rec.ID)
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if after.DesiredRunning || !after.ExplicitlyStopped {
		t.Errorf("the racing restart clobbered the stop intent: desiredRunning=%v explicitlyStopped=%v",
			after.DesiredRunning, after.ExplicitlyStopped)
	}
	// And the container the restart brought up is stopped again, so the record and
	// the runtime agree.
	if st, err := rt.State(context.Background(), rec.ID); err == nil && st.Status == runcStateRunning {
		t.Errorf("instance left running after a stop that raced its restart: %+v", st)
	}
}
