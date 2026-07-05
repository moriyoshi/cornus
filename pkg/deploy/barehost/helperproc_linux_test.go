//go:build linux

package barehost

// A real child process for the tests that cannot be honestly faked.
//
// The shim's whole reason to exist is what it does with REAL processes: reaping
// a reparented container init to learn its exit status, signalling a wedged shim,
// waiting for one to actually go away. A fake runtime cannot exercise any of
// that. So those tests re-execute the test binary itself as a child, the standard
// os/exec helper-process pattern — no root, no daemon, no network, and no
// dependency on any particular binary being installed on the host.

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

const helperEnv = "CORNUS_BARE_TEST_HELPER"

// TestBareHelperProcess is not a test: it is the body of the child process
// startHelper spawns. It either exits immediately with a requested status, or
// sleeps until the parent signals it.
func TestBareHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process body; only meaningful when re-executed as a child")
	}
	if v := os.Getenv(helperEnv + "_EXIT"); v != "" {
		code, err := strconv.Atoi(v)
		if err != nil {
			code = 99
		}
		os.Exit(code)
	}
	// A bare select{} would trip the runtime's deadlock detector; a long sleep
	// leaves the process signalable and idle until the parent kills it.
	time.Sleep(10 * time.Minute)
}

// startHelper spawns the helper child. exitCode == "" makes it block until
// signalled; otherwise it exits immediately with that status. The caller owns
// reaping it — several tests deliberately let the code under test do that.
func startHelper(t *testing.T, exitCode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBareHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", helperEnv+"_EXIT="+exitCode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return cmd
}
