//go:build linux

package barehost

// The detached shim's own behavior: its on-disk record/state/pid handling, its
// control-socket protocol, how it launches or adopts a container, and what it
// reports about an init's exit. Everything here runs unprivileged against a fake
// runtime and (where a real process is unavoidable) the helper child process.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// newTestShim builds a shim over a fake runtime with a real (empty) record dir.
func newTestShim(t *testing.T) (*shim, *fakeRuntime) {
	t.Helper()
	dir := t.TempDir()
	rt := newFakeRuntime()
	return &shim{
		cfg:       ShimConfig{ID: "cornus-web-0", DataDir: dir, Runtime: "runc"},
		recordDir: dir,
		rt:        rt,
	}, rt
}

// --- on-disk state the shim shares with the server ---

func TestShimPathsAreSiblingsOfTheRecord(t *testing.T) {
	dir := "/data/bare/records/cornus-web-0"
	if got := shimLockPath(dir); got != dir+"/shim.lock" {
		t.Errorf("lock path = %q", got)
	}
	if got := shimStatePath(dir); got != dir+"/shim.state" {
		t.Errorf("state path = %q", got)
	}
	if got := shimSocketPath(dir); got != dir+"/shim.sock" {
		t.Errorf("socket path = %q", got)
	}
}

// TestShimRecordRoundTripPreservesSupervisionFields proves the shim writes the
// record in the same shape the server reads back — the supervision fields it
// mutates (exit code/time, restart tally) must survive the round trip, since
// they are the only channel through which a shim reports what it observed.
func TestShimRecordRoundTripPreservesSupervisionFields(t *testing.T) {
	s, _ := newTestShim(t)
	want := &instanceRecord{
		ID: "cornus-web-0", App: "web", Restart: "on-failure", MaxAttempts: 3,
		RestartCount: 2, LastExitCode: 137, LastExitUnix: 1700000000,
		DesiredRunning: true, BundleDir: "/b", LogPath: "/l",
	}
	if err := s.writeRecord(want); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	got, err := s.readRecord()
	if err != nil {
		t.Fatalf("readRecord: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, want)
	}
	// The temp file used for the atomic rename must not be left behind (it lives
	// in the record dir the server enumerates).
	if _, err := os.Stat(filepath.Join(s.recordDir, "record.json.shim.tmp")); !os.IsNotExist(err) {
		t.Errorf("shim temp record left behind: %v", err)
	}
}

func TestShimReadRecordRejectsMissingAndCorrupt(t *testing.T) {
	s, _ := newTestShim(t)
	if _, err := s.readRecord(); err == nil {
		t.Error("readRecord with no record.json: want error")
	}
	if err := os.WriteFile(filepath.Join(s.recordDir, "record.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readRecord(); err == nil {
		t.Error("readRecord on a torn record: want error")
	}
}

// TestShimStateIsDiscoverableByTheServer covers both halves of the discovery
// handshake: the shim publishes its pid + socket, and the server's reader loads
// exactly that.
func TestShimStateIsDiscoverableByTheServer(t *testing.T) {
	s, _ := newTestShim(t)
	if err := s.writeState(shimState{Pid: 4242, Socket: "/run/x.sock"}); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	got, err := readShimState(s.recordDir)
	if err != nil || got == nil {
		t.Fatalf("readShimState = %v, %v", got, err)
	}
	if got.Pid != 4242 || got.Socket != "/run/x.sock" {
		t.Errorf("state = %+v, want pid 4242 on /run/x.sock", got)
	}
}

// TestReadShimStateAbsentMeansNoShim pins the (nil, nil) contract the callers
// branch on: no state file is "no shim", not an error.
func TestReadShimStateAbsentMeansNoShim(t *testing.T) {
	st, err := readShimState(t.TempDir())
	if err != nil || st != nil {
		t.Errorf("readShimState(no file) = %v, %v; want nil, nil", st, err)
	}
}

func TestReadShimStateCorruptIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(shimStatePath(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readShimState(dir); err == nil {
		t.Error("readShimState on a torn state file: want error")
	}
}

func TestShimReadPid(t *testing.T) {
	s, _ := newTestShim(t)
	pidFile := filepath.Join(s.recordDir, "pid")
	if got := s.readPid(); got != 0 {
		t.Errorf("readPid with no pid file = %d, want 0", got)
	}
	if err := os.WriteFile(pidFile, []byte(" 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.readPid(); got != 1234 {
		t.Errorf("readPid = %d, want 1234 (surrounding whitespace trimmed)", got)
	}
	if err := os.WriteFile(pidFile, []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.readPid(); got != 0 {
		t.Errorf("readPid on garbage = %d, want 0", got)
	}
}

// --- control-socket protocol ---

// controlExchange runs one request/response against handleControl over an
// in-memory pipe and returns what the shim wrote back ("" when it closed the
// connection without replying).
func controlExchange(t *testing.T, s *shim, request string) string {
	t.Helper()
	srv, cli := net.Pipe()
	go s.handleControl(srv)
	defer cli.Close()
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := cli.Write([]byte(request)); err != nil && request != "" {
		t.Fatalf("write request %q: %v", request, err)
	}
	buf := make([]byte, 64)
	n, err := cli.Read(buf)
	if err != nil {
		return ""
	}
	return string(buf[:n])
}

func TestShimControlPing(t *testing.T) {
	s, _ := newTestShim(t)
	if got := controlExchange(t, s, shimCmdPing+"\n"); got != shimReplyOK+"\n" {
		t.Errorf("ping reply = %q, want %q", got, shimReplyOK+"\n")
	}
	if s.stopping.Load() {
		t.Error("a ping must not put the shim into stopping")
	}
}

func TestShimControlStopFlipsStopping(t *testing.T) {
	s, _ := newTestShim(t)
	if got := controlExchange(t, s, shimCmdStop+"\n"); got != shimReplyOK+"\n" {
		t.Errorf("stop reply = %q, want %q", got, shimReplyOK+"\n")
	}
	if !s.stopping.Load() {
		t.Error("stop must set the stopping flag so the supervise loop breaks")
	}
}

// TestShimControlRequestVocabulary pins the verb set: a request the shim does
// not understand gets no acknowledgement at all (the server's sendShim reads a
// missing or unexpected reply as "unreachable", which is the safe answer), while
// a known verb is recognised after trimming its framing whitespace.
func TestShimControlRequestVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		request   string
		wantReply string
	}{
		{"unknown verb is not acknowledged", "restart\n", ""},
		{"empty line is not acknowledged", "\n", ""},
		{"stray whitespace around a verb is trimmed", "  " + shimCmdPing + "  \n", shimReplyOK + "\n"},
		{"a verb needs its own request, not a suffix", "unping\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestShim(t)
			if got := controlExchange(t, s, tc.request); got != tc.wantReply {
				t.Errorf("reply to %q = %q, want %q", tc.request, got, tc.wantReply)
			}
			if s.stopping.Load() {
				t.Error("only a stop request may put the shim into stopping")
			}
		})
	}
}

// TestShimControlTruncatedRequestIsIgnored covers the framing rule: a request
// with no terminating newline is never dispatched, however long the client waits.
func TestShimControlTruncatedRequestIsIgnored(t *testing.T) {
	s, _ := newTestShim(t)
	srv, cli := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handleControl(srv) }()
	if _, err := cli.Write([]byte(shimCmdStop)); err != nil { // no "\n"
		t.Fatalf("write: %v", err)
	}
	_ = cli.Close() // EOF before the newline
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleControl did not return on a truncated request")
	}
	if s.stopping.Load() {
		t.Error("a truncated request must not be executed")
	}
}

// TestShimControlStopSignalsTheRunningInit proves the stop verb is not merely
// bookkeeping: it terminates the container init the shim is supervising.
func TestShimControlStopSignalsTheRunningInit(t *testing.T) {
	s, _ := newTestShim(t)
	child := startHelper(t, "")
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }() // reap as soon as it dies
	s.initPid.Store(int64(child.Process.Pid))

	if got := controlExchange(t, s, shimCmdStop+"\n"); got != shimReplyOK+"\n" {
		t.Fatalf("stop reply = %q", got)
	}
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("stop did not terminate the supervised init")
	}
}

// TestShimServeControlAnswersOverTheRealSocket wires the whole loop the server
// actually uses — a unix socket, the accept loop, and the server-side dialer.
func TestShimServeControlAnswersOverTheRealSocket(t *testing.T) {
	s, _ := newTestShim(t)
	sock := shimSocketPath(s.recordDir)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go s.serveControl(ln)

	if !pingShim(sock) {
		t.Fatal("pingShim over a served control socket = false, want true")
	}
	if err := sendShim(sock, shimCmdStop); err != nil {
		t.Fatalf("sendShim(stop): %v", err)
	}
	if !s.stopping.Load() {
		t.Error("a stop over the real socket must set the stopping flag")
	}
	// The accept loop exits when the listener closes (shim shutdown), after which
	// the socket no longer answers.
	_ = ln.Close()
	if pingShim(sock) {
		t.Error("pingShim on a closed listener = true, want false")
	}
}

// --- launch / adopt ---

// TestShimLaunchAdoptsALiveContainer covers the case the shim exists for: a
// previous shim died but its container did not, so the new shim must take over
// the running init rather than recreating it (and must report that it cannot
// read that init's exit code).
func TestShimLaunchAdoptsALiveContainer(t *testing.T) {
	s, rt := newTestShim(t)
	rt.cs[s.cfg.ID] = &runtimeState{ID: s.cfg.ID, Status: runcStateRunning, Pid: 4242}

	pid, adopted, err := s.launch(&instanceRecord{ID: s.cfg.ID})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if pid != 4242 || !adopted {
		t.Errorf("launch = (%d, %v), want the live init (4242, adopted)", pid, adopted)
	}
	for _, c := range rt.calls {
		if strings.HasPrefix(c, "create:") || strings.HasPrefix(c, "delete:") {
			t.Errorf("adopting must not recreate the container; calls=%v", rt.calls)
		}
	}
}

// TestShimLaunchRecreatesFromTheBundle covers the normal cycle: clear the
// previous generation's runtime state, then create + start from the persisted
// bundle, reporting the pid runc wrote to the pid file.
func TestShimLaunchRecreatesFromTheBundle(t *testing.T) {
	s, rt := newTestShim(t)
	if err := os.WriteFile(filepath.Join(s.recordDir, "pid"), []byte("777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &instanceRecord{ID: s.cfg.ID, BundleDir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "web.log")}

	pid, adopted, err := s.launch(rec)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if pid != 777 || adopted {
		t.Errorf("launch = (%d, %v), want (777, not adopted)", pid, adopted)
	}
	if got := strings.Join(rt.calls, ","); got != "delete:"+s.cfg.ID+",create:"+s.cfg.ID+",start:"+s.cfg.ID {
		t.Errorf("calls = %q, want delete then create then start", got)
	}
}

func TestShimLaunchCleansUpAFailedStart(t *testing.T) {
	s, rt := newTestShim(t)
	rt.startErr = errStartBoom
	rec := &instanceRecord{ID: s.cfg.ID, BundleDir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "web.log")}

	if _, _, err := s.launch(rec); err == nil {
		t.Fatal("launch with a failing start: want error")
	}
	// A created-but-never-started container must not be left behind.
	deletes := 0
	for _, c := range rt.calls {
		if c == "delete:"+s.cfg.ID {
			deletes++
		}
	}
	if deletes != 2 {
		t.Errorf("calls = %v, want a delete before create AND a rollback delete after the failed start", rt.calls)
	}
}

func TestShimLaunchPropagatesCreateFailure(t *testing.T) {
	s, rt := newTestShim(t)
	rt.createErr = errCreateBoom
	_, _, err := s.launch(&instanceRecord{ID: s.cfg.ID, BundleDir: t.TempDir(), LogPath: filepath.Join(t.TempDir(), "web.log")})
	if err == nil {
		t.Fatal("launch with a failing create: want error")
	}
	for _, c := range rt.calls {
		if strings.HasPrefix(c, "start:") {
			t.Errorf("start must not be attempted after a failed create; calls=%v", rt.calls)
		}
	}
}

// --- exit observation ---

// TestShimWaitInitReadsItsOwnChildsExitCode is the shim's entire reason to
// exist: as a child subreaper it reaps the container init itself, so unlike the
// in-process supervisor it learns the real exit status and can apply an
// exit-code-aware on-failure policy.
func TestShimWaitInitReadsItsOwnChildsExitCode(t *testing.T) {
	s, _ := newTestShim(t)
	child := startHelper(t, "7") // exits immediately with 7
	code, known := s.waitInit(child.Process.Pid, false)
	if !known || code != 7 {
		t.Errorf("waitInit = (%d, known=%v), want (7, true)", code, known)
	}
}

// TestShimWaitInitReportsASignalledInit covers the other terminal shape: a
// killed init is reported as 128+signal (the shell convention the restart policy
// and LastExitCode carry).
func TestShimWaitInitReportsASignalledInit(t *testing.T) {
	s, _ := newTestShim(t)
	child := startHelper(t, "")
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = child.Process.Signal(syscall.SIGKILL)
	}()
	code, known := s.waitInit(child.Process.Pid, false)
	if !known || code != 128+int(syscall.SIGKILL) {
		t.Errorf("waitInit = (%d, known=%v), want (%d, true)", code, known, 128+int(syscall.SIGKILL))
	}
}

// TestShimWaitInitOnAnAdoptedInitHasNoExitCode pins the documented degradation:
// an init this shim did not create is not its child, so exit DETECTION still
// works but the status is unavailable — which is what makes restartAllowedCode
// fall back to any-exit behavior.
func TestShimWaitInitOnAnAdoptedInitHasNoExitCode(t *testing.T) {
	s, _ := newTestShim(t)
	child := startHelper(t, "")
	type result struct {
		code  int
		known bool
	}
	done := make(chan result, 1)
	go func() {
		code, known := s.waitInit(child.Process.Pid, true)
		done <- result{code, known}
	}()
	time.Sleep(100 * time.Millisecond)
	_ = child.Process.Signal(syscall.SIGKILL)
	go func() { _ = child.Wait() }() // reap; waitInit does not, for an adopted init
	select {
	case got := <-done:
		if got.known || got.code != -1 {
			t.Errorf("waitInit(adopted) = (%d, known=%v), want (-1, false)", got.code, got.known)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("waitInit did not observe the adopted init's exit")
	}
}

// --- the supervision loop's terminal decisions ---

// TestShimRunTearsDownWhenTheRecordSaysStopped covers the race the loop is
// written for: a Stop landed before this cycle started, so the shim must tear the
// container down and exit instead of launching it.
func TestShimRunTearsDownWhenTheRecordSaysStopped(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  *instanceRecord
	}{
		{"explicitly stopped", &instanceRecord{ID: "cornus-web-0", DesiredRunning: true, ExplicitlyStopped: true}},
		{"not desired running", &instanceRecord{ID: "cornus-web-0", DesiredRunning: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := newTestShim(t)
			if err := s.writeRecord(tc.rec); err != nil {
				t.Fatal(err)
			}
			if err := s.run(); err != nil {
				t.Fatalf("run: %v", err)
			}
			joined := strings.Join(rt.calls, ",")
			if !strings.Contains(joined, "delete:cornus-web-0") {
				t.Errorf("calls = %v, want the container torn down", rt.calls)
			}
			if strings.Contains(joined, "create:") {
				t.Errorf("calls = %v, want no launch for a stopped instance", rt.calls)
			}
			// The published state and socket are cleaned up on the way out, so the
			// server's next shimAlive sees no shim.
			for _, p := range []string{shimStatePath(s.recordDir), shimSocketPath(s.recordDir)} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("%s survived shim exit: %v", filepath.Base(p), err)
				}
			}
		})
	}
}

func TestShimRunFailsWithoutARecord(t *testing.T) {
	s, _ := newTestShim(t)
	if err := s.run(); err == nil {
		t.Fatal("run with no record.json: want error")
	}
}

// TestShimGracefulKillEscalatesToTheProcessBeingGone verifies the teardown path
// signals a live init and returns only once it is actually gone.
func TestShimGracefulKillTerminatesTheInit(t *testing.T) {
	s, _ := newTestShim(t)
	child := startHelper(t, "")
	waited := make(chan struct{})
	go func() { _ = child.Wait(); close(waited) }()

	s.gracefulKill(child.Process.Pid)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("gracefulKill returned but the init is still alive")
	}
	// A second call against the now-dead pid returns immediately (idempotent).
	s.gracefulKill(child.Process.Pid)
}

// --- RunShim's guards ---

func TestRunShimRequiresARecord(t *testing.T) {
	err := RunShim(ShimConfig{ID: "cornus-web-0", DataDir: t.TempDir(), Runtime: "runc"})
	if err == nil || !strings.Contains(err.Error(), "no record") {
		t.Errorf("RunShim without a record = %v, want a 'no record' error", err)
	}
}

// TestRunShimExitsQuietlyWhenAnotherShimHoldsTheLock covers the single-shim
// guard: a redundant spawn (the server calls ensureShim from several places)
// must exit nil rather than double-supervising the container.
func TestRunShimExitsQuietlyWhenAnotherShimHoldsTheLock(t *testing.T) {
	dataDir := t.TempDir()
	id := "cornus-web-0"
	recordDir := filepath.Join(dataDir, "bare", "records", id)
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(&instanceRecord{ID: id, App: "web"})
	if err := os.WriteFile(filepath.Join(recordDir, "record.json"), rec, 0o600); err != nil {
		t.Fatal(err)
	}
	// Stand in for the incumbent shim by holding its flock.
	lock, err := os.OpenFile(shimLockPath(recordDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("take incumbent lock: %v", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	if err := RunShim(ShimConfig{ID: id, DataDir: dataDir, Runtime: "runc"}); err != nil {
		t.Errorf("RunShim as a redundant spawn = %v, want nil (quiet exit)", err)
	}
	// It must not have published state or a socket over the incumbent's.
	if st, _ := readShimState(recordDir); st != nil {
		t.Errorf("a losing shim published state %+v", st)
	}
}
