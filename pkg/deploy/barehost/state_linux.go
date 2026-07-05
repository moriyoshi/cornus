//go:build linux

package barehost

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"cornus/pkg/api"
)

// instanceRecord is the per-instance state the bare backend persists under
// <DataDir>/bare/records/<id>/record.json. It is the source of truth containerd
// kept in its metadata DB / container labels — there is no daemon store here, so
// Status/List/Delete/Start/Stop read and mutate these records. M1 carries the
// fields those operations need; M4 extends it with the supervision/desired-state
// fields (desired_running, explicitly_stopped, restart_count, last_exit, ...).
type instanceRecord struct {
	ID          string `json:"id"`
	App         string `json:"app"`
	Image       string `json:"image"`
	Replica     int    `json:"replica"`
	SnapshotKey string `json:"snapshotKey"`
	ChainID     string `json:"chainID"`
	BundleDir   string `json:"bundleDir"`
	RootfsDir   string `json:"rootfsDir"`
	CgroupPath  string `json:"cgroupPath"`
	LogPath     string `json:"logPath"`
	Restart     string `json:"restart,omitempty"`
	CreatedUnix int64  `json:"createdUnix,omitempty"`

	// Origin is the deployment's lineage (project, client host/user/dir/git, and
	// the server-stamped subject). The bare backend has no daemon label store, so
	// it persists the structured origin directly rather than as cornus.origin.*
	// label strings; Status/List surface it on api.DeployStatus.
	Origin *api.Origin `json:"origin,omitempty"`

	// Role, when non-empty, marks this record as a cornus-managed COMPANION
	// caretaker (roleMountCaretaker / roleEgressCaretaker) rather than an app
	// instance: it runs alongside a deployment, joins an app instance's pinned
	// netns (which it does NOT own), and carries no CNI/hosts/resolv state of its
	// own. Status/List omit companions (they are not app replicas); Delete reaps
	// them but skips their (absent) network teardown. Empty for app instances.
	Role string `json:"role,omitempty"`

	// Supervision (M4): the desired/observed state the in-process supervisor and
	// the startup reconcile act on. DesiredRunning is the operator's intent
	// (cleared by Stop, set by Apply/Start); ExplicitlyStopped mirrors
	// containerd's explicitly-stopped label so a stopped workload is not
	// resurrected; MaxAttempts caps on-failure restarts; RestartCount is the
	// running tally (for backoff + the cap).
	DesiredRunning    bool `json:"desiredRunning,omitempty"`
	ExplicitlyStopped bool `json:"explicitlyStopped,omitempty"`
	MaxAttempts       int  `json:"maxAttempts,omitempty"`
	RestartCount      int  `json:"restartCount,omitempty"`

	// LastExitCode / LastExitUnix record the most recent init exit the detached
	// shim observed (shim_linux.go). The shim reaps the reparented init as its own
	// child, so unlike the in-process supervisor it has the real exit status; these
	// drive exit-code-aware on-failure restarts and surface the last failure.
	LastExitCode int   `json:"lastExitCode,omitempty"`
	LastExitUnix int64 `json:"lastExitUnix,omitempty"`

	// Networking (M3): the instance's netns pin, network memberships, resolved
	// IPs, per-network aliases, published ports, and hosts-file path. These
	// replace the containerd backend's container labels — teardown, the
	// hosts-file sync, and (M4) reboot recovery rebuild their state from here.
	Networks   []string            `json:"networks,omitempty"`
	NetNS      string              `json:"netns,omitempty"`
	IP         string              `json:"ip,omitempty"`     // primary
	NetIPs     map[string]string   `json:"netIPs,omitempty"` // network -> IP
	Aliases    map[string][]string `json:"aliases,omitempty"`
	Ports      []api.PortMapping   `json:"ports,omitempty"` // published (replica 0 only)
	HostsPath  string              `json:"hostsPath,omitempty"`
	ResolvPath string              `json:"resolvPath,omitempty"`
}

func (b *Backend) baseDir() string            { return filepath.Join(b.dataDir, "bare") }
func (b *Backend) recordsDir() string         { return filepath.Join(b.baseDir(), "records") }
func (b *Backend) bundlesDir() string         { return filepath.Join(b.baseDir(), "bundles") }
func (b *Backend) logsDir() string            { return filepath.Join(b.baseDir(), "logs") }
func (b *Backend) recordDir(id string) string { return filepath.Join(b.recordsDir(), id) }
func (b *Backend) bundleDir(id string) string { return filepath.Join(b.bundlesDir(), id) }
func (b *Backend) logPath(id string) string   { return filepath.Join(b.logsDir(), id+".log") }

// writeRecord publishes a WHOLE record (temp file + rename within the same
// directory), creating the per-instance record dir if needed. It is for the
// creation path — createInstance and startCompanion, which mint a record rather
// than amend one. Anything that amends an EXISTING record must go through
// updateRecord instead, so the amendment lands on a fresh read under the
// per-record lock; see recordlock_linux.go.
func (b *Backend) writeRecord(rec *instanceRecord) error {
	dir := b.recordDir(rec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("bare: record dir: %w", err)
	}
	lk, err := lockRecordDir(dir)
	if err != nil {
		return err
	}
	defer lk.release()
	return writeRecordFile(dir, rec, recordTmpName)
}

// updateRecord read-modify-writes one instance's record under the per-record
// lock: it re-reads the record from disk, applies mutate, and publishes the
// result, returning the record as written. The caller's own (possibly stale) copy
// is deliberately not an input — that staleness is what corrupted records before
// the lock existed. mutate must not block. See recordlock_linux.go.
func (b *Backend) updateRecord(id string, mutate func(*instanceRecord) error) (*instanceRecord, error) {
	return updateRecordAt(b.recordDir(id), recordTmpName, mutate)
}

// readRecord loads one instance's record. Unlocked by design: records are
// published by rename, so a reader always sees one complete generation.
func (b *Backend) readRecord(id string) (*instanceRecord, error) {
	return readRecordFile(b.recordDir(id))
}

// listRecords enumerates every persisted instance record, sorted by id. A torn
// or unreadable record is skipped rather than failing the whole listing (a
// half-written record dir must not break Status/List).
func (b *Backend) listRecords() ([]*instanceRecord, error) {
	entries, err := os.ReadDir(b.recordsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("bare: list records: %w", err)
	}
	var out []*instanceRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, err := b.readRecord(e.Name())
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// recordsForApp returns a deployment's instance records, sorted by replica index.
func (b *Backend) recordsForApp(app string) ([]*instanceRecord, error) {
	all, err := b.listRecords()
	if err != nil {
		return nil, err
	}
	var out []*instanceRecord
	for _, r := range all {
		if r.App == app {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Replica < out[j].Replica })
	return out, nil
}

// removeRecord deletes an instance's record directory. It takes the per-record
// lock first so it cannot land in the middle of a supervisor's read-modify-write
// (which would republish record.json into a directory Delete had already emptied,
// leaving a ghost deployment in List). A holder that acquires the lock after the
// removal finds no record.json and writes nothing.
func (b *Backend) removeRecord(id string) error {
	dir := b.recordDir(id)
	if lk, err := lockRecordDir(dir); err == nil {
		defer lk.release() // released after the removal; the unlinked inode is ours
	}
	return os.RemoveAll(dir)
}
