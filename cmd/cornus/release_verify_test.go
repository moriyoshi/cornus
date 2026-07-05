//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The release pipeline refuses to publish a binary that does not report both the
// observability store and the embedded collector; the assertion itself lives in
// .github/scripts/verify-release-binary.sh, run once per platform leg.
//
// It used to pipe the feature report through `tee /dev/stderr` so the log would
// show it. git-bash on the windows-latest runner cannot open /dev/stderr, tee
// exited non-zero, and `set -o pipefail` made the whole pipeline fail — so the
// windows/amd64 leg of v0.0.0 aborted with "does not carry the observability
// store" against a binary that carried it perfectly well. The failure was
// invisible to every other leg because Linux and macOS have a working
// /dev/stderr.
//
// These tests drive the script against stub binaries with stderr wired to a
// socket, which is a file descriptor /dev/stderr cannot be reopened from — the
// same shape as the Windows environment, reachable from a POSIX runner.
const verifyScript = "../../.github/scripts/verify-release-binary.sh"

// writeStubBinary writes an executable that answers
// `version --features --output json` with the given report, standing in for a
// freshly built cornus.
func writeStubBinary(t *testing.T, features string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cornus-stub")
	body := "#!/bin/sh\nprintf '%s\\n' '" + features + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

// runWithSocketStderr runs `bash <args...>` with stderr pointing at a socket, so
// that opening /dev/stderr (i.e. /proc/self/fd/2) fails exactly as it does under
// git-bash on Windows. It returns the combined captured output and the exit code.
func runWithSocketStderr(t *testing.T, args ...string) (string, int) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Skipf("socketpair unavailable, cannot simulate an unopenable /dev/stderr: %v", err)
	}
	sink := os.NewFile(uintptr(fds[0]), "stderr-socket")
	peer := os.NewFile(uintptr(fds[1]), "stderr-socket-peer")
	defer sink.Close()
	defer peer.Close()

	// Drain the peer so a chatty script cannot block on a full socket buffer.
	drained := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := peer.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		drained <- sb.String()
	}()

	cmd := exec.Command("bash", args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = sink
	err = cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run bash %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	sink.Close()
	stderr := <-drained
	return out.String() + stderr, code
}

// requireBrokenDevStderr asserts that this host really does reproduce the
// Windows condition. Without it the tests below could pass vacuously on a
// platform where /dev/stderr reopens fine even from a socket, certifying nothing.
func requireBrokenDevStderr(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	_, code := runWithSocketStderr(t, "-c", "set -o pipefail; printf x | tee /dev/stderr >/dev/null")
	if code == 0 {
		t.Skip("this host can still open /dev/stderr from a socket; cannot reproduce the git-bash condition")
	}
}

// TestVerifyReleaseBinaryAcceptsAllInOneBuild is the regression proper: a binary
// reporting both features must verify OK even where /dev/stderr cannot be opened.
func TestVerifyReleaseBinaryAcceptsAllInOneBuild(t *testing.T) {
	requireBrokenDevStderr(t)

	stub := writeStubBinary(t, `{"otelcollector":"yes","obsstore":"yes","version":"1.2.3"}`)
	out, code := runWithSocketStderr(t, verifyScript, stub)
	if code != 0 {
		t.Fatalf("verification rejected an all-in-one binary (exit %d):\n%s", code, out)
	}
	// The report has to reach the log — showing it is why `tee` was there.
	if !strings.Contains(out, `"obsstore":"yes"`) {
		t.Errorf("feature report not echoed to the build log:\n%s", out)
	}
}

// TestVerifyReleaseBinaryRejectsStubbedFeatures keeps the check from degrading
// into a rubber stamp: a mistyped build tag still compiles and silently selects
// the no-op store, which is the only thing this script exists to catch.
func TestVerifyReleaseBinaryRejectsStubbedFeatures(t *testing.T) {
	requireBrokenDevStderr(t)

	for _, tc := range []struct {
		name     string
		features string
		wantMsg  string
	}{
		{
			name:     "obsstore stubbed",
			features: `{"otelcollector":"yes","obsstore":"no","version":"1.2.3"}`,
			wantMsg:  "does not carry the observability store",
		},
		{
			name:     "collector stubbed",
			features: `{"otelcollector":"no","obsstore":"yes","version":"1.2.3"}`,
			wantMsg:  "does not carry the embedded OpenTelemetry Collector",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := writeStubBinary(t, tc.features)
			out, code := runWithSocketStderr(t, verifyScript, stub)
			if code == 0 {
				t.Fatalf("verification accepted %s:\n%s", tc.features, out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("expected message %q, got:\n%s", tc.wantMsg, out)
			}
		})
	}
}
