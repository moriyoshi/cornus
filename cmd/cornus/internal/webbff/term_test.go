package webbff

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"cornus/pkg/api"
)

// fakeExec is an in-memory execClient: ExecStart hands back one end of a net.Pipe
// as the "exec stream", keeping the other end as the simulated shell so a test can
// drive output/read stdin without any daemon or WebSocket.
type fakeExec struct {
	mu        sync.Mutex
	shell     net.Conn
	resizes   [][2]uint
	createErr error
	startErr  error
}

func (f *fakeExec) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	return "exec-1", nil
}

func (f *fakeExec) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	c, s := net.Pipe()
	f.mu.Lock()
	f.shell = s
	f.mu.Unlock()
	return c, nil
}

func (f *fakeExec) ExecResize(ctx context.Context, execID string, height, width uint) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]uint{height, width})
	f.mu.Unlock()
	return nil
}

func (f *fakeExec) shellConn() net.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shell
}

func (f *fakeExec) lastResize() ([2]uint, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.resizes) == 0 {
		return [2]uint{}, false
	}
	return f.resizes[len(f.resizes)-1], true
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition never met: %s", what)
}

func ringContains(sess *termSession, want string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return strings.Contains(string(sess.ring.buf), want)
}

func recvChunk(t *testing.T, ch <-chan []byte, timeout time.Duration) string {
	t.Helper()
	select {
	case b := <-ch:
		return string(b)
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a live output chunk")
		return ""
	}
}

// TestTermSessionLifecycle covers the full persistence story on one session:
// buffered output replays on attach, live output flows, detach keeps the session
// alive, reattach replays the accumulated scrollback, stdin and resize are
// forwarded, and shell exit flips the session dead then reaps it after the linger.
func TestTermSessionLifecycle(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	mgr.linger = 50 * time.Millisecond

	sess, err := mgr.Create(context.Background(), "web", []string{"/bin/sh"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	shell := shellOf(t, fe)

	// Output produced with no attachment still lands in the replay ring.
	writeAll(t, shell, "hello")
	eventually(t, "ring has hello", func() bool { return ringContains(sess, "hello") })

	// First attach replays the buffered scrollback...
	att := sess.attach()
	if !strings.Contains(string(att.replay), "hello") {
		t.Fatalf("replay = %q, want it to contain %q", att.replay, "hello")
	}
	// ...and then forwards live output.
	writeAll(t, shell, "world")
	if got := recvChunk(t, att.sub.ch, time.Second); got != "world" {
		t.Fatalf("live chunk = %q, want %q", got, "world")
	}

	// Detach must NOT kill the session.
	att.detach()
	if got := mgr.Get(sess.id); got == nil {
		t.Fatal("session gone after detach; should persist")
	}
	if list := mgr.List(); len(list) != 1 || !list[0].Alive {
		t.Fatalf("List after detach = %+v, want one alive session", list)
	}

	// Reattach replays the accumulated scrollback (hello + world).
	eventually(t, "ring has world", func() bool { return ringContains(sess, "world") })
	att2 := sess.attach()
	if !strings.Contains(string(att2.replay), "helloworld") {
		t.Fatalf("reattach replay = %q, want it to contain %q", att2.replay, "helloworld")
	}

	// Stdin from the browser reaches the shell.
	readBack := make(chan string, 1)
	go func() {
		b := make([]byte, 16)
		_ = shell.SetReadDeadline(time.Now().Add(time.Second))
		n, _ := shell.Read(b)
		readBack <- string(b[:n])
	}()
	sess.input([]byte("ls\n"))
	if got := <-readBack; got != "ls\n" {
		t.Fatalf("shell stdin = %q, want %q", got, "ls\n")
	}

	// Resize is forwarded to the exec.
	sess.resize(30, 100)
	if last, ok := fe.lastResize(); !ok || last != [2]uint{30, 100} {
		t.Fatalf("last resize = %v (ok=%v), want {30 100}", last, ok)
	}

	// Shell exit ends the session, unblocks the attached browser, then reaps.
	_ = shell.Close()
	select {
	case <-att2.sub.done:
	case <-time.After(time.Second):
		t.Fatal("attached subscriber not notified of session end")
	}
	eventually(t, "session not alive", func() bool {
		info := mgr.Get(sess.id)
		return info == nil || !info.info().Alive
	})
	eventually(t, "session reaped", func() bool { return mgr.Get(sess.id) == nil })
}

// TestSubscriberCloseReasons pins the reason each teardown path records, and the
// close code it maps to. The browser relies on these to distinguish an ended
// session (prompt to reconnect) from a takeover by another tab (reattach) from a
// transient drop (silently reattach) — see paneExitAction on the web side.
func TestSubscriberCloseReasons(t *testing.T) {
	t.Run("closeFrame mapping", func(t *testing.T) {
		cases := []struct {
			reason subCloseReason
			code   websocket.StatusCode
			text   string
		}{
			{subEnded, wsCloseEnded, "ended"},
			{subSuperseded, wsCloseSuperseded, "superseded"},
			{subDetached, websocket.StatusNormalClosure, "detached"},
		}
		for _, c := range cases {
			if code, text := closeFrame(c.reason); code != c.code || text != c.text {
				t.Errorf("closeFrame(%d) = (%d, %q), want (%d, %q)", c.reason, code, text, c.code, c.text)
			}
		}
	})

	// waitClosed fails the test if the subscriber is not closed promptly.
	waitClosed := func(t *testing.T, sub *subscriber) {
		t.Helper()
		select {
		case <-sub.done:
		case <-time.After(time.Second):
			t.Fatal("subscriber never closed")
		}
	}

	t.Run("supersede", func(t *testing.T) {
		mgr := newTermManager(&fakeExec{})
		sess, err := mgr.Create(context.Background(), "web", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		att1 := sess.attach()
		sess.attach() // a second browser takes the slot
		waitClosed(t, att1.sub)
		if att1.sub.reason != subSuperseded {
			t.Fatalf("reason = %d, want subSuperseded", att1.sub.reason)
		}
	})

	t.Run("detach", func(t *testing.T) {
		mgr := newTermManager(&fakeExec{})
		sess, err := mgr.Create(context.Background(), "web", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		att := sess.attach()
		att.detach()
		waitClosed(t, att.sub)
		if att.sub.reason != subDetached {
			t.Fatalf("reason = %d, want subDetached", att.sub.reason)
		}
	})

	t.Run("shell exit ends", func(t *testing.T) {
		fe := &fakeExec{}
		mgr := newTermManager(fe)
		mgr.linger = 50 * time.Millisecond
		sess, err := mgr.Create(context.Background(), "web", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		att := sess.attach()
		_ = shellOf(t, fe).Close() // the process exits on its own
		waitClosed(t, att.sub)
		if att.sub.reason != subEnded {
			t.Fatalf("reason = %d, want subEnded", att.sub.reason)
		}
	})

	t.Run("kill ends", func(t *testing.T) {
		mgr := newTermManager(&fakeExec{})
		sess, err := mgr.Create(context.Background(), "web", nil)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		att := sess.attach()
		mgr.Kill(sess.id) // explicit teardown
		waitClosed(t, att.sub)
		if att.sub.reason != subEnded {
			t.Fatalf("reason = %d, want subEnded", att.sub.reason)
		}
	})
}

// TestTermManagerKill removes a session immediately and is idempotent.
func TestTermManagerKill(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	sess, err := mgr.Create(context.Background(), "web", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// nil cmd defaults to /bin/sh.
	if len(sess.cmd) != 1 || sess.cmd[0] != "/bin/sh" {
		t.Fatalf("default cmd = %v, want [/bin/sh]", sess.cmd)
	}
	if !mgr.Kill(sess.id) {
		t.Fatal("Kill returned false for a live session")
	}
	if mgr.Get(sess.id) != nil {
		t.Fatal("session still present after Kill")
	}
	if mgr.Kill(sess.id) {
		t.Fatal("Kill returned true for an already-removed session")
	}
}

// TestTermManagerMaxSessions enforces the session cap.
func TestTermManagerMaxSessions(t *testing.T) {
	fe := &fakeExec{}
	mgr := newTermManager(fe)
	mgr.maxSessions = 2
	var created []*termSession
	for i := 0; i < 2; i++ {
		sess, err := mgr.Create(context.Background(), "web", nil)
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		created = append(created, sess)
	}
	if _, err := mgr.Create(context.Background(), "web", nil); err == nil {
		t.Fatal("Create past the cap succeeded; want an error")
	}
	// Freeing a slot lets a new session in.
	mgr.Kill(created[0].id)
	if _, err := mgr.Create(context.Background(), "web", nil); err != nil {
		t.Fatalf("Create after freeing a slot: %v", err)
	}
	t.Cleanup(func() {
		for _, info := range mgr.List() {
			mgr.Kill(info.ID)
		}
	})
}

// TestTermManagerCreateErrors propagates backend failures.
func TestTermManagerCreateErrors(t *testing.T) {
	mgr := newTermManager(&fakeExec{createErr: errors.New("boom")})
	if _, err := mgr.Create(context.Background(), "web", nil); err == nil {
		t.Fatal("want ExecCreate error to propagate")
	}
	mgr = newTermManager(&fakeExec{startErr: errors.New("nope")})
	if _, err := mgr.Create(context.Background(), "web", nil); err == nil {
		t.Fatal("want ExecStart error to propagate")
	}
	if _, err := newTermManager(&fakeExec{}).Create(context.Background(), "", nil); err == nil {
		t.Fatal("want empty-workload error")
	}
}

func shellOf(t *testing.T, fe *fakeExec) net.Conn {
	t.Helper()
	var c net.Conn
	eventually(t, "shell connected", func() bool { c = fe.shellConn(); return c != nil })
	return c
}

// writeAll writes s to the shell side. net.Pipe writes block until the manager's
// readLoop consumes them, so a short deadline guards against a wedged test.
func writeAll(t *testing.T, shell net.Conn, s string) {
	t.Helper()
	_ = shell.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := shell.Write([]byte(s)); err != nil {
		t.Fatalf("shell write %q: %v", s, err)
	}
}
