//go:build linux

package barehost

// The SERVER side of the shim handshake: dialling a shim's control socket,
// deciding whether one is alive, and stopping one. These are the decisions that
// determine whether the backend tears a container down itself or trusts a shim to
// have done it, so the "not handled" answers matter as much as the happy path.

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serveFakeShim answers the shim control protocol on a unix socket, letting the
// test choose the reply per command ("" means close without replying, which is
// how a wedged shim behaves). It returns the socket path.
func serveFakeShim(t *testing.T, dir string, reply func(cmd string) string) string {
	t.Helper()
	sock := filepath.Join(dir, "fake-shim.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				buf := make([]byte, 64)
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				if r := reply(strings.TrimSpace(string(buf[:n]))); r != "" {
					_, _ = conn.Write([]byte(r))
				}
			}()
		}
	}()
	return sock
}

func okReply(string) string { return shimReplyOK + "\n" }

// --- sendShim / pingShim ---

func TestSendShimAcceptsBothOKFramings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"newline-terminated ok", shimReplyOK + "\n"},
		{"bare ok", shimReplyOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := serveFakeShim(t, t.TempDir(), func(string) string { return tc.reply })
			if err := sendShim(sock, shimCmdPing); err != nil {
				t.Errorf("sendShim with reply %q = %v, want nil", tc.reply, err)
			}
		})
	}
}

func TestSendShimRejectsAnUnexpectedReply(t *testing.T) {
	sock := serveFakeShim(t, t.TempDir(), func(string) string { return "busy\n" })
	err := sendShim(sock, shimCmdStop)
	if err == nil {
		t.Fatal("sendShim with a non-ok reply: want error")
	}
	if !strings.Contains(err.Error(), "unexpected shim reply") {
		t.Errorf("error = %v, want it to name the unexpected reply", err)
	}
}

func TestSendShimFailsWhenTheShimNeverAnswers(t *testing.T) {
	// A shim that accepts the connection and closes without replying is wedged;
	// the server must not read that as success.
	sock := serveFakeShim(t, t.TempDir(), func(string) string { return "" })
	if err := sendShim(sock, shimCmdPing); err == nil {
		t.Error("sendShim against a silent shim: want error")
	}
}

func TestSendShimFailsOnAnUnreachableSocket(t *testing.T) {
	if err := sendShim(filepath.Join(t.TempDir(), "absent.sock"), shimCmdPing); err == nil {
		t.Error("sendShim to a nonexistent socket: want error")
	}
}

func TestPingShimMirrorsReachability(t *testing.T) {
	live := serveFakeShim(t, t.TempDir(), okReply)
	if !pingShim(live) {
		t.Error("pingShim on a responsive socket = false, want true")
	}
	if pingShim(filepath.Join(t.TempDir(), "absent.sock")) {
		t.Error("pingShim on a nonexistent socket = true, want false")
	}
}

// TestSendShimTransmitsTheRequestedVerb guards against the server and shim
// drifting apart on the wire: the shim must actually receive "stop", not "ping".
func TestSendShimTransmitsTheRequestedVerb(t *testing.T) {
	got := make(chan string, 4)
	sock := serveFakeShim(t, t.TempDir(), func(cmd string) string {
		got <- cmd
		return shimReplyOK + "\n"
	})
	if err := sendShim(sock, shimCmdStop); err != nil {
		t.Fatalf("sendShim: %v", err)
	}
	select {
	case cmd := <-got:
		if cmd != shimCmdStop {
			t.Errorf("shim received %q, want %q", cmd, shimCmdStop)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shim never received the request")
	}
}

// --- shimAlive ---

// writeShimStateFor publishes a shim state under id's record dir, as a live shim
// would.
func writeShimStateFor(t *testing.T, b *Backend, id string, st shimState) {
	t.Helper()
	dir := b.recordDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &shim{recordDir: dir}
	if err := s.writeState(st); err != nil {
		t.Fatalf("publish shim state: %v", err)
	}
}

// deadPid returns a pid that is certainly gone: a helper child started, killed,
// and reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	child := startHelper(t, "0")
	pid := child.Process.Pid
	if err := child.Wait(); err != nil {
		// A nonzero status would still mean it exited, but "0" should be clean.
		t.Logf("helper exited with %v", err)
	}
	return pid
}

// TestShimAliveNeedsBothALivePidAndAResponsiveSocket covers the PID-reuse-safe
// liveness rule. Either half failing must read as "no shim", because the caller
// then does the teardown itself rather than trusting a shim to.
func TestShimAliveNeedsBothALivePidAndAResponsiveSocket(t *testing.T) {
	b, _ := newTestBackend(t)
	sock := serveFakeShim(t, t.TempDir(), okReply)
	gone := deadPid(t)

	cases := []struct {
		name  string
		id    string
		state *shimState
		want  bool
	}{
		{"no state file at all", "cornus-none-0", nil, false},
		{"live pid and a responsive socket", "cornus-live-0", &shimState{Pid: os.Getpid(), Socket: sock}, true},
		{"live pid but an unreachable socket", "cornus-wedged-0", &shimState{Pid: os.Getpid(), Socket: filepath.Join(t.TempDir(), "absent.sock")}, false},
		{"dead pid with a responsive socket", "cornus-stale-0", &shimState{Pid: gone, Socket: sock}, false},
		{"state with no pid", "cornus-nopid-0", &shimState{Socket: sock}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.state != nil {
				writeShimStateFor(t, b, tc.id, *tc.state)
			}
			if got := b.shimAlive(tc.id); got != tc.want {
				t.Errorf("shimAlive = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- ensureShim / spawnShim ---

// TestEnsureShimDoesNotSpawnWhenOneAlreadyAnswers pins the fast path: ensureShim
// is called from createInstance, Start and reconcile alike, so a redundant spawn
// on every call would be a process leak.
func TestEnsureShimDoesNotSpawnWhenOneAlreadyAnswers(t *testing.T) {
	b, _ := newTestBackend(t)
	id := "cornus-web-0"
	sock := serveFakeShim(t, t.TempDir(), okReply)
	writeShimStateFor(t, b, id, shimState{Pid: os.Getpid(), Socket: sock})

	if err := b.ensureShim(id); err != nil {
		t.Fatalf("ensureShim with a live shim = %v, want nil", err)
	}
	// spawnShim's first side effect is opening the per-instance shim log; its
	// absence proves nothing was spawned.
	if _, err := os.Stat(filepath.Join(b.recordDir(id), "shim.log")); !os.IsNotExist(err) {
		t.Errorf("a shim was spawned despite a live one answering (shim.log: %v)", err)
	}
}

func TestSpawnShimFailsWithoutARecordDir(t *testing.T) {
	b, _ := newTestBackend(t)
	err := b.spawnShim("cornus-ghost-0") // no record dir was ever created
	if err == nil {
		t.Fatal("spawnShim for an unknown instance: want error")
	}
	if !strings.Contains(err.Error(), "shim log") {
		t.Errorf("error = %v, want it to name the shim log it could not open", err)
	}
}

// --- shimStop ---

// TestShimStopReportsNotHandledWithoutALiveShim covers the answer the callers
// branch on: false means "no shim dealt with this", so stopSupervised and
// teardownSupervised must stop the container themselves. A companion (never
// shim-supervised) and an already-exited shim both land here.
func TestShimStopReportsNotHandledWithoutALiveShim(t *testing.T) {
	b, _ := newTestBackend(t)

	if b.shimStop("cornus-nostate-0") {
		t.Error("shimStop with no published state = true, want false")
	}

	writeShimStateFor(t, b, "cornus-reaped-0", shimState{Pid: deadPid(t), Socket: filepath.Join(t.TempDir(), "absent.sock")})
	start := time.Now()
	if b.shimStop("cornus-reaped-0") {
		t.Error("shimStop against an already-exited shim = true, want false")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("shimStop against a dead pid took %v; it must not wait out the grace period", elapsed)
	}
}

// TestShimStopReportsHandledWhenTheShimAcceptsAndExits is the true branch: the
// shim took the stop over its socket and went away, so the caller must NOT also
// tear the container down (the shim already deleted it).
func TestShimStopReportsHandledWhenTheShimAcceptsAndExits(t *testing.T) {
	b, _ := newTestBackend(t)
	child := startHelper(t, "")
	go func() { _ = child.Wait() }() // reap it as the real server does after spawnShim

	sock := serveFakeShim(t, t.TempDir(), func(cmd string) string {
		if cmd == shimCmdStop {
			_ = child.Process.Kill() // a real shim exits on stop
		}
		return shimReplyOK + "\n"
	})
	writeShimStateFor(t, b, "cornus-web-0", shimState{Pid: child.Process.Pid, Socket: sock})

	if !b.shimStop("cornus-web-0") {
		t.Error("shimStop = false, want true when the shim accepted the stop and exited")
	}
}

// TestShimStopKillsAWedgedShim covers the recovery path: an unreachable shim is
// signalled dead so a fresh one can take its flock later — but the result is
// still "not handled", because a shim with no signal handler dies leaving the
// CONTAINER running for the caller to stop.
func TestShimStopKillsAWedgedShim(t *testing.T) {
	b, _ := newTestBackend(t)
	child := startHelper(t, "")
	exited := make(chan struct{})
	go func() { _ = child.Wait(); close(exited) }()

	// A live pid whose control socket does not exist: wedged.
	writeShimStateFor(t, b, "cornus-web-0", shimState{
		Pid:    child.Process.Pid,
		Socket: filepath.Join(t.TempDir(), "absent.sock"),
	})

	if b.shimStop("cornus-web-0") {
		t.Error("shimStop on a wedged shim = true, want false (the container is the caller's to stop)")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("a wedged shim was not signalled dead")
	}
}
