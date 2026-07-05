//go:build linux

package barehost

// Per-record locking, shared by the server and the detached shim.
//
// The instance record is the bare backend's only store of desired state — there
// is no daemon holding it in memory — and TWO independent actors read-modify-write
// it: the cornus server (Stop/Start/Delete/reboot recovery) and the supervisor
// that applies the restart policy (an in-process goroutine by default, a detached
// `cornus daemon bare-shim` process under CORNUS_BARE_SHIM). Each side used to
// read the whole record, mutate a couple of fields in memory, and write the whole
// record back, so a write that raced a Stop resurrected the fields Stop had just
// set — see the package tests for the exact interleavings.
//
// The lock is an advisory flock on a STABLE sibling path, <recordDir>/record.lock,
// never on record.json itself: record.json is replaced by a temp-file rename, so
// a lock taken on it would be a lock on an inode that the next write unlinks, and
// two writers would happily hold "the lock" on two different inodes. record.lock
// is only ever created and flocked, never renamed over.
//
// flock is per open file description, so a second os.OpenFile of the same path
// within one process conflicts with the first exactly like a second process
// would. One mechanism therefore serializes BOTH the in-process supervisor
// against the server's API calls and the detached shim against the server.
//
// Deadlock discipline: the lock is held only across the in-memory read-mutate-
// write of the JSON file — never across a runc invocation, a shim control-socket
// round trip, a restart backoff, or a wait on a container init. The shim's OTHER
// lock, <recordDir>/shim.lock (the single-shim guard in shim_linux.go), is held
// for the shim's entire lifetime; it is deliberately a different file, and the
// server never takes it, so the two can never form a cycle.
//
// Reads are NOT locked: record.json is published by rename, so a reader always
// sees one complete generation. Only the read-modify-write cycles need ordering.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// recordFileName is the published record; recordTmpName / shimRecordTmpName are
	// the per-writer staging files renamed over it (distinct names so a stray temp
	// from a crashed writer is attributable).
	recordFileName    = "record.json"
	recordTmpName     = "record.json.tmp"
	shimRecordTmpName = "record.json.shim.tmp"
	// recordLockName is the stable path the per-record flock is taken on.
	recordLockName = "record.lock"
	// recordLockTimeout bounds the wait for the lock. Every critical section is a
	// bounded in-memory file rewrite, so contention resolves in microseconds; this
	// is a safety valve against a wedged holder, not a normal outcome.
	recordLockTimeout = 5 * time.Second
)

// errRecordUnchanged aborts an updateRecordAt mutation without writing anything.
// A mutate func returns it when the freshly-read record shows the work is moot
// (typically: an operator's Stop landed while we were restarting).
var errRecordUnchanged = errors.New("bare: record unchanged")

func recordPath(recordDir string) string     { return filepath.Join(recordDir, recordFileName) }
func recordLockPath(recordDir string) string { return filepath.Join(recordDir, recordLockName) }

// recordLock is one held exclusive lock on an instance record.
type recordLock struct{ f *os.File }

// lockRecordDir takes the exclusive per-record lock. A missing record directory
// surfaces as an fs.ErrNotExist error (the instance was deleted) rather than
// creating one — resurrecting a deleted record dir is precisely the corruption
// this exists to prevent.
//
// It polls with LOCK_NB rather than blocking in the kernel so the wait is bounded
// and does not pin an OS thread for an unbounded time.
func lockRecordDir(recordDir string) (*recordLock, error) {
	f, err := os.OpenFile(recordLockPath(recordDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(recordLockTimeout)
	delay := 250 * time.Microsecond
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &recordLock{f: f}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EINTR {
			_ = f.Close()
			return nil, fmt.Errorf("bare: lock record %s: %w", recordDir, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("bare: lock record %s: timed out after %s", recordDir, recordLockTimeout)
		}
		time.Sleep(delay)
		if delay < 2*time.Millisecond {
			delay *= 2
		}
	}
}

// release drops the lock. Closing the descriptor releases the flock too; the
// explicit LOCK_UN keeps the intent visible.
func (l *recordLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
}

// readRecordFile loads one instance's record from its record directory.
func readRecordFile(recordDir string) (*instanceRecord, error) {
	data, err := os.ReadFile(recordPath(recordDir))
	if err != nil {
		return nil, err
	}
	var rec instanceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("bare: parse record %s: %w", filepath.Base(recordDir), err)
	}
	return &rec, nil
}

// writeRecordFile publishes a record atomically: write a staging file in the same
// directory, then rename it over record.json. Callers must already hold the
// record lock when the write is part of a read-modify-write cycle.
func writeRecordFile(recordDir string, rec *instanceRecord, tmpName string) error {
	data, err := json.MarshalIndent(rec, "", "\t")
	if err != nil {
		return fmt.Errorf("bare: marshal record: %w", err)
	}
	tmp := filepath.Join(recordDir, tmpName)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("bare: write record: %w", err)
	}
	if err := os.Rename(tmp, recordPath(recordDir)); err != nil {
		return fmt.Errorf("bare: commit record: %w", err)
	}
	return nil
}

// updateRecordAt is THE read-modify-write primitive both the server and the shim
// go through: it takes the per-record lock, re-reads the record from disk (so the
// mutation is applied to whatever the other side last wrote, not to a stale copy
// the caller has been holding), applies mutate, and publishes the result.
//
// mutate must be a pure in-memory function — the lock is held while it runs.
// Returning errRecordUnchanged (or any error) aborts without writing.
//
// A record that has been deleted surfaces as an fs.ErrNotExist error and nothing
// is written, so a supervisor finishing a restart cannot recreate a record the
// operator just deleted.
func updateRecordAt(recordDir string, tmpName string, mutate func(*instanceRecord) error) (*instanceRecord, error) {
	lk, err := lockRecordDir(recordDir)
	if err != nil {
		return nil, err
	}
	defer lk.release()
	rec, err := readRecordFile(recordDir)
	if err != nil {
		return nil, err
	}
	if err := mutate(rec); err != nil {
		return nil, err
	}
	if err := writeRecordFile(recordDir, rec, tmpName); err != nil {
		return nil, err
	}
	return rec, nil
}
