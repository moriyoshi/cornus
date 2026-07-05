package webbff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// fakeCapturer stands in for an exec into the container. It records what it was
// asked to run so a test can assert the pid travelled as an ARGUMENT rather than
// being spliced into the script, and hands back canned stdout.
type fakeCapturer struct {
	mu    sync.Mutex
	out   string
	err   error
	calls [][]string
}

func (f *fakeCapturer) Capture(_ context.Context, _ string, cmd []string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	out, err := f.out, f.err
	f.mu.Unlock()
	return out, err
}

func (f *fakeCapturer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeCapturer) lastCall() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

func TestProbeProcParsesOutput(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		wantCwd  string
		wantComm string
	}{
		{"both", "cwd=/srv/app\ncomm=vim\n", "/srv/app", "vim"},
		{"tty line endings", "cwd=/srv/app\r\ncomm=vim\r\n", "/srv/app", "vim"},
		{"comm only", "cwd=\ncomm=bash\n", "", "bash"},
		{"cwd only", "cwd=/tmp\ncomm=\n", "/tmp", ""},
		{"nothing", "", "", ""},
		{"unrelated noise", "hello\n", "", ""},
		// A relative or empty readlink is not a directory we can send anyone to.
		{"relative cwd rejected", "cwd=srv/app\n", "", ""},
		// A cwd whose directory was removed names a place nothing can be opened at.
		{"deleted cwd rejected", "cwd=/gone (deleted)\ncomm=sh\n", "", "sh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeProc(context.Background(), &fakeCapturer{out: tc.out}, "web", []string{"/bin/sh"}, 42)
			if got.cwd != tc.wantCwd || got.comm != tc.wantComm {
				t.Fatalf("probeProc(%q) = (%q, %q), want (%q, %q)", tc.out, got.cwd, got.comm, tc.wantCwd, tc.wantComm)
			}
		})
	}
}

func TestProbeProcPassesPIDAsAnArgument(t *testing.T) {
	fc := &fakeCapturer{out: "cwd=/tmp\n"}
	probeProc(context.Background(), fc, "web", []string{"/bin/busybox", "sh"}, 4242)
	call := fc.lastCall()
	// The interpreter is the session's own shell, applet and all.
	if len(call) < 2 || call[0] != "/bin/busybox" || call[1] != "sh" {
		t.Fatalf("probe ran under %q, want the session's own shell", call)
	}
	if call[len(call)-1] != "4242" {
		t.Fatalf("pid did not travel as the last argument: %q", call)
	}
	for _, a := range call[:len(call)-1] {
		if strings.Contains(a, "4242") {
			t.Fatalf("pid was spliced into the script text: %q", a)
		}
	}
	// A capture error must be indistinguishable from "learned nothing": the caller
	// keeps its last answer either way, and an error path that returned a partial
	// procInfo would let a failed exec overwrite a good directory.
	empty := func(p procInfo) bool { return p.cwd == "" && p.comm == "" && len(p.argv) == 0 }
	if got := probeProc(context.Background(), &fakeCapturer{err: context.DeadlineExceeded}, "web", []string{"/bin/sh"}, 1); !empty(got) {
		t.Fatalf("failed capture returned %+v, want the zero procInfo", got)
	}
	if got := probeProc(context.Background(), fc, "web", nil, 1); !empty(got) {
		t.Fatalf("probe with no interpreter returned %+v, want the zero procInfo", got)
	}
}

// The script itself, against a REAL /proc. Everything else here is a test of the
// parser; this is the only thing that can catch the script being wrong — the
// last-paren split, tpgid landing in $6, and readlink/comm being readable at all.
// Those are the parts most likely to be subtly wrong and least likely to be caught
// by reasoning about them.
func TestProcProbeScriptReadsRealProc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/<pid>/stat is Linux-specific")
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc on this host")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}

	// The setup is contrived on purpose, and each contrivance kills a specific way
	// the script can be wrong while still looking right:
	//
	//   - The shell runs on a PTY and INTERACTIVELY (-i), so job control puts a
	//     foreground job in its own process group. Without that the child shares
	//     the shell's group, tpgid names the shell, and "read the foreground
	//     process" is indistinguishable from "read the shell".
	//   - The foreground program sits in a DIFFERENT directory (/usr) than the
	//     shell (/tmp), so reading /proc/<shell>/cwd by mistake gives a wrong
	//     answer rather than the right one by luck.
	//   - THE SHELL'S OWN NAME contains a paren, so splitting /proc/<pid>/stat at
	//     the first ")" instead of the last shifts every field and misreads tpgid.
	//     It has to be the SHELL's name, not the child's: the stat line being
	//     parsed is the shell's, and a paren anywhere else leaves the two splits
	//     identical and the bug invisible.
	//   - The foreground program's name also contains a paren, so `comm` is shown
	//     to survive one intact rather than being truncated at it.
	dir := t.TempDir()
	copyExe := func(src, name string) string {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Skipf("no %s to copy: %v", src, err)
		}
		dst := filepath.Join(dir, name)
		if err := os.WriteFile(dst, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	oddShell := copyExe("/bin/sh", "s)h")
	oddName := copyExe("/bin/sleep", "a)b")

	sh := exec.Command(oddShell, "-i")
	sh.Dir = "/tmp"
	ptmx, err := pty.Start(sh)
	if err != nil {
		t.Skipf("cannot allocate a pty here: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		_ = sh.Process.Kill()
		_ = sh.Wait()
	}()
	pid := sh.Process.Pid
	// A subshell so the program becomes its own foreground JOB; `exec` keeps the
	// pid so comm and cwd describe the program rather than a wrapper shell.
	if _, err := ptmx.Write([]byte("(cd /usr && exec '" + oddName + "' 30)\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var info procInfo
	for time.Now().Before(deadline) {
		out, err := exec.Command("/bin/sh", "-c", procProbeScript, "sh", strconv.Itoa(pid)).Output()
		if err == nil {
			info = parseProbeOutput(string(out))
			if info.comm == "a)b" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if info.comm != "a)b" {
		t.Fatalf("script read comm %q, want %q — the foreground job, with its paren intact", info.comm, "a)b")
	}
	if info.cwd != "/usr" {
		t.Fatalf("script read cwd %q, want /usr (the FOREGROUND program's directory, not the shell's /tmp)", info.cwd)
	}
}

// parseProbeOutput is probeProc's parsing half, reachable without a capturer so
// the script test above can use exactly the parser the real path uses rather than
// a second copy that could agree with a wrong script.
func parseProbeOutput(out string) procInfo {
	return probeProc(context.Background(), &fakeCapturer{out: out}, "", []string{"/bin/sh"}, 0)
}

// The end-to-end fallback: a session whose shell says nothing gets its tab name
// and directory from the probe, anchored on the pid the wrapper announced.
func TestTermSessionFallsBackToProcProbe(t *testing.T) {
	fe := &fakeExec{}
	fc := &fakeCapturer{out: "cwd=/srv/app\ncomm=vim\n"}
	mgr := newTermManager(fe)
	mgr.cap = fc
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	// No pid yet: probing must not start, because there is nothing to anchor it on
	// and a probe on a guessed pid would report a stranger's directory.
	mgr.List()
	if fc.count() != 0 {
		t.Fatalf("probed %d times before any pid was announced, want 0", fc.count())
	}

	// The wrapper announces the pid, invisibly, before the shell's first output.
	shell := fe.shellConn()
	if _, err := shell.Write([]byte(esc + "]5379;cornus-pid=4242" + bel + "$ ")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pid announced", func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		return ts.pid == 4242
	})

	mgr.List()
	eventually(t, "probe reported", func() bool {
		info := ts.info()
		return info.Cwd == "/srv/app" && info.Title == "vim"
	})
	// The pid it probed is the announced one, not ExecState.Pid or a guess.
	if call := fc.lastCall(); call[len(call)-1] != "4242" {
		t.Fatalf("probed pid %q, want the announced 4242", call[len(call)-1])
	}
}

// OSC beats the probe wherever both have an answer: it came off the session's own
// output at the moment it changed, where a probe is at best procProbeTTL stale.
func TestTermSessionPrefersOSCOverTheProbe(t *testing.T) {
	fe := &fakeExec{}
	fc := &fakeCapturer{out: "cwd=/stale\ncomm=node\narg=node\narg=/usr/bin/codex\n"}
	mgr := newTermManager(fe)
	mgr.cap = fc
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	shell := fe.shellConn()
	_, _ = shell.Write([]byte(esc + "]5379;cornus-pid=4242" + bel))
	_, _ = shell.Write([]byte(esc + "]0;vim README.md" + bel))
	_, _ = shell.Write([]byte(esc + "]7;file:///srv/live" + st))
	eventually(t, "osc landed", func() bool {
		info := ts.info()
		return info.Title == "vim README.md" && info.Cwd == "/srv/live"
	})

	// The probe still RUNS even though OSC answered both — it is no longer only a
	// fallback for the reported values, it is also the only source of the live
	// foreground program that scopes the detection rules. What must not happen is
	// the probe's answers overwriting OSC's.
	mgr.List()
	eventually(t, "probe ran", func() bool { return fc.count() > 0 })
	// The probe reports comm=node with codex in argv — identification must see
	// THROUGH the runtime, which comm alone never could.
	eventually(t, "agent resolved through the runtime", func() bool { return ts.det.currentAgent() == "codex" })
	if info := ts.info(); info.Title != "vim README.md" || info.Cwd != "/srv/live" {
		t.Fatalf("probe overwrote OSC values: %+v", info)
	}
}

// The point of probing a session that OSC already describes: the rules are scoped
// to the program in the FOREGROUND, and only the probe can see a program the user
// started inside a shell. Without this the scope would be stuck at the launch argv
// and an agent run from a prompt would never be detected.
func TestTermSessionScopesRulesToTheLiveForegroundProgram(t *testing.T) {
	fe := &fakeExec{}
	fc := &fakeCapturer{out: "cwd=/srv\ncomm=claude\narg=claude\n"}
	mgr := newTermManager(fe)
	mgr.cap = fc
	// Launched as a plain shell, so the launch argv names no agent at all.
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/bash"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)
	if got := ts.det.currentAgent(); got != "" {
		t.Fatalf("a shell session started with agent %q, want none", got)
	}

	if _, err := fe.shellConn().Write([]byte(esc + "]5379;cornus-pid=4242" + bel)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pid announced", func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		return ts.pid == 4242
	})
	mgr.List()
	eventually(t, "foreground program learned", func() bool { return ts.det.currentAgent() == "claude" })

	// And a shell in the foreground is NOT an agent: at a prompt the session is
	// idle by definition, and there is no manifest for a shell to classify with.
	ts.det.setForeground("bash", []string{"bash"})
	if got := ts.det.currentAgent(); got != "" {
		t.Fatalf("a shell in the foreground resolved to agent %q, want none", got)
	}
}

// A container with no readable /proc must stop being asked, or a polled session
// there pays an exec every few seconds forever to learn nothing.
func TestTermSessionStopsProbingAfterRepeatedMisses(t *testing.T) {
	fe := &fakeExec{}
	fc := &fakeCapturer{out: ""} // nothing readable
	mgr := newTermManager(fe)
	mgr.cap = fc
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	shell := fe.shellConn()
	_, _ = shell.Write([]byte(esc + "]5379;cornus-pid=4242" + bel))
	eventually(t, "pid announced", func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		return ts.pid == 4242
	})

	// Drive more rounds than the miss budget, clearing the TTL each time so the
	// only thing that can stop it is the budget itself.
	for i := 0; i < procProbeMisses+3; i++ {
		ts.mu.Lock()
		ts.probeAt = time.Time{}
		ts.mu.Unlock()
		mgr.List()
		eventually(t, "probe settled", func() bool {
			ts.mu.Lock()
			defer ts.mu.Unlock()
			return !ts.probing
		})
	}
	if got := fc.count(); got != procProbeMisses {
		t.Fatalf("probed %d times against an unreadable /proc, want it to stop after %d", got, procProbeMisses)
	}
}

// The probe replaces a session's foreground program from its own goroutine while the
// browser is polling the session list, so the agent name has two concurrent users.
// It was read straight off the detector's field by info(), under the session lock
// but not the detector's — safe while the field was immutable, a data race the
// moment the probe started moving it.
//
// setForeground is called in a loop rather than through maybeProbe because the probe
// is TTL-bounded to one call every few seconds: the writer is the same, and this way
// the window is actually hit. Only meaningful under -race, which is where it was
// verified to fail before the fix.
func TestSessionListAndProbeDoNotRaceOnTheAgentName(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	ts, err := mgr.Create(context.Background(), "web", []string{"/bin/bash"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Kill(ts.id)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			ts.det.setForeground("claude", []string{"claude"})
			ts.det.setForeground("bash", []string{"bash"})
		}
	}()
	for i := 0; i < 200; i++ {
		mgr.List()
	}
	<-done
}
