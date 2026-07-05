package agentproc

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent.json")
	if err := WriteState(path, State{Pid: 4321, Socket: "/run/a.sock", Log: "/run/a.log"}); err != nil {
		t.Fatal(err)
	}
	st, err := ReadState(path)
	if err != nil || st == nil {
		t.Fatalf("ReadState = %v, %v", st, err)
	}
	if st.Pid != 4321 || st.Socket != "/run/a.sock" {
		t.Errorf("round-trip mismatch: %+v", st)
	}
	RemoveState(path)
	st, err = ReadState(path)
	if err != nil || st != nil {
		t.Fatalf("after remove: %v, %v; want nil, nil", st, err)
	}
}

func TestListenBindsWritesStateAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Socket: filepath.Join(dir, "agent.sock"), StatePath: filepath.Join(dir, "agent.json")}
	// A stale socket file must be removed before binding.
	if err := os.WriteFile(spec.Socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, cleanup, err := Listen(spec)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	st, _ := ReadState(spec.StatePath)
	if st == nil || st.Pid != os.Getpid() {
		t.Fatalf("state after Listen = %+v, want pid %d", st, os.Getpid())
	}
	cleanup()
	if _, err := os.Stat(spec.Socket); !os.IsNotExist(err) {
		t.Errorf("socket not removed on cleanup")
	}
	if st, _ := ReadState(spec.StatePath); st != nil {
		t.Errorf("state not removed on cleanup")
	}
	_ = ln
}

func TestWithLockSerializes(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	var inside atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = withLock(lock, func() error {
				if inside.Add(1) != 1 {
					overlap.Store(true)
				}
				time.Sleep(2 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
		}()
	}
	wg.Wait()
	if overlap.Load() {
		t.Fatal("withLock allowed concurrent entry")
	}
}

func TestEnsureRunningFastPathNoSpawn(t *testing.T) {
	spawned := false
	old := spawnProcess
	spawnProcess = func([]string, string) (int, error) { spawned = true; return 0, nil }
	defer func() { spawnProcess = old }()

	spec := Spec{Socket: "/anything", StatePath: filepath.Join(t.TempDir(), "s.json")}
	ping := func(string) (int, bool) { return 2, true } // already live
	if err := EnsureRunning(spec, ping); err != nil {
		t.Fatal(err)
	}
	if spawned {
		t.Fatal("spawned despite a live instance")
	}
}

func TestEnsureRunningSpawnsWhenDead(t *testing.T) {
	var live atomic.Bool
	old := spawnProcess
	spawnProcess = func([]string, string) (int, error) { live.Store(true); return 1234, nil }
	defer func() { spawnProcess = old }()

	spec := Spec{Socket: "/anything", StatePath: filepath.Join(t.TempDir(), "s.json"), SpawnArgs: []string{"daemon", "agent"}}
	ping := func(string) (int, bool) { return 0, live.Load() } // dead until "spawned"
	if err := EnsureRunning(spec, ping); err != nil {
		t.Fatal(err)
	}
	if !live.Load() {
		t.Fatal("EnsureRunning did not spawn")
	}
}

func TestStopGracefulRemovesState(t *testing.T) {
	spec := Spec{Socket: "/x.sock", StatePath: filepath.Join(t.TempDir(), "s.json")}
	if err := WriteState(spec.StatePath, State{Pid: 999999, Socket: spec.Socket}); err != nil {
		t.Fatal(err)
	}
	if err := Stop(spec, func(string) error { return nil }); err != nil { // graceful stop succeeds
		t.Fatal(err)
	}
	if st, _ := ReadState(spec.StatePath); st != nil {
		t.Fatal("state not removed after graceful stop")
	}
}

func TestStopNoStateReturnsSendErr(t *testing.T) {
	spec := Spec{Socket: "/x.sock", StatePath: filepath.Join(t.TempDir(), "missing.json")}
	wantErr := fmt.Errorf("unreachable")
	err := Stop(spec, func(string) error { return wantErr })
	if err != wantErr {
		t.Fatalf("Stop = %v, want the sendStop error when no state exists", err)
	}
}
