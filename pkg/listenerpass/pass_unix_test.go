//go:build unix

package listenerpass

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// childEnv makes the test binary re-exec as the receiving process, so the
// cross-process test below crosses a real process boundary.
const childEnv = "CORNUS_LISTENERPASS_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(runChild())
	}
	os.Exit(m.Run())
}

// runChild receives a listener on the inherited socket (fd 3), reports readiness,
// then serves exactly one connection. It runs in a separate process, which is the
// point: same-process SCM_RIGHTS exercises the encoding but cannot show that a
// descriptor genuinely crosses into another process's table.
func runChild() int {
	f := os.NewFile(3, "ctl")
	conn, err := net.FileConn(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: FileConn:", err)
		return 1
	}
	ln, err := Receive(conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: Receive:", err)
		return 1
	}
	if _, err := conn.Write([]byte("ready\n")); err != nil {
		fmt.Fprintln(os.Stderr, "child: ready:", err)
		return 1
	}
	c, err := ln.Accept()
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: Accept:", err)
		return 1
	}
	// Report the SERVING process's pid, so the parent can prove the connection was
	// handled across a real process boundary rather than by itself.
	_, _ = c.Write([]byte(fmt.Sprintf("served-by-pid %d\n", os.Getpid())))
	_ = c.Close()
	return 0
}

// socketpair returns two connected unix-domain sockets without touching the
// filesystem.
func socketpair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	conns := make([]*net.UnixConn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "sp")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("FileConn: %v", err)
		}
		conns[i] = c.(*net.UnixConn)
		t.Cleanup(func() { _ = conns[i].Close() })
	}
	return conns[0], conns[1]
}

func dialAndRead(t *testing.T, addr string) (string, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(c)
	return strings.TrimSpace(string(b)), err
}

// THE property this package exists for: after the original listener is closed, the
// address is still bound and still serving, because the replica holds the same
// kernel socket. Everything about ownership migration rests on this.
func TestReplicaKeepsTheAddressBoundAcrossProcesses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	parent, childSide := socketpair(t)
	childFile, err := childSide.File() // an *os.File the child can inherit
	if err != nil {
		t.Fatalf("child socket File: %v", err)
	}
	defer childFile.Close()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), childEnv+"=1")
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	defer func() { _, _ = cmd.Process.Wait() }()

	// Send only after the child exists: its pid is required on Windows, and doing it
	// in this order here keeps the test honest about the real call sequence.
	if err := Send(parent, ln, Peer{Pid: cmd.Process.Pid}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_ = childSide.Close() // the child owns its end now

	ready := bufio.NewReader(parent)
	_ = parent.SetReadDeadline(time.Now().Add(10 * time.Second))
	if line, err := ready.ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("child never reported ready (line=%q err=%v)", line, err)
	}

	// Now kill the original. If replication did not really share the socket, the
	// address goes down here and the dial below is refused.
	if err := ln.Close(); err != nil {
		t.Fatalf("closing the original listener: %v", err)
	}

	got, err := dialAndRead(t, addr)
	if err != nil {
		t.Fatalf("dialing %s after the original listener closed: %v", addr, err)
	}
	want := fmt.Sprintf("served-by-pid %d", cmd.Process.Pid)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if cmd.Process.Pid == os.Getpid() {
		t.Fatal("the 'child' is this very process; the test is not crossing a process boundary")
	}
	t.Logf("original listener closed in pid %d; served by pid %d", os.Getpid(), cmd.Process.Pid)
	if err := cmd.Wait(); err != nil {
		t.Errorf("child exited with %v", err)
	}
}

// The assertion above only has teeth if closing the sole listener really does take
// the address down. Without this, a Send that silently did nothing would still let
// that test pass on a machine where the port happened to be reachable.
func TestWithoutAReplicaClosingTheListenerTakesTheAddressDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dialAndRead(t, addr); err == nil {
		t.Fatalf("dialing %s succeeded after its only listener closed", addr)
	}
}

// The two references are one socket, not two binds: a connection made while both
// are open can be accepted by either. Accepting on the replica while the original
// is still open and never accepts is what shows they share a backlog.
func TestReplicaSharesTheOriginalBacklog(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	a, b := socketpair(t)
	done := make(chan error, 1)
	var replica net.Listener
	go func() {
		var err error
		replica, err = Receive(b)
		done <- err
	}()
	if err := Send(a, ln, Peer{Pid: os.Getpid()}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Receive: %v", err)
	}
	defer replica.Close()

	if replica.Addr().String() != addr {
		t.Errorf("replica bound %s, want the original's %s", replica.Addr(), addr)
	}

	go func() {
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			_ = c.Close()
		}
	}()
	// The ORIGINAL never accepts; if these were separate sockets this would hang.
	if tl, ok := replica.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(5 * time.Second))
	}
	c, err := replica.Accept()
	if err != nil {
		t.Fatalf("the replica could not accept a connection made to the original's address: %v", err)
	}
	_ = c.Close()
}

// Closing one reference must not disturb the other, or a joiner tidying up would
// take the conduit down with it.
func TestClosingTheReplicaLeavesTheOriginalServing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	a, b := socketpair(t)
	done := make(chan error, 1)
	var replica net.Listener
	go func() {
		var err error
		replica, err = Receive(b)
		done <- err
	}()
	if err := Send(a, ln, Peer{Pid: os.Getpid()}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if err := replica.Close(); err != nil {
		t.Fatalf("closing the replica: %v", err)
	}

	go func() {
		if c, err := ln.Accept(); err == nil {
			_, _ = c.Write([]byte("original\n"))
			_ = c.Close()
		}
	}()
	got, err := dialAndRead(t, addr)
	if err != nil {
		t.Fatalf("the original stopped serving after the replica closed: %v", err)
	}
	if got != "original" {
		t.Errorf("got %q, want %q", got, "original")
	}
}

// The documented hazard, made observable: if something consumes the bytes the
// descriptor is attached to, Receive must say what happened rather than report a
// bare protocol error. The failure is otherwise near-impossible to diagnose,
// because it only shows up when messages arrive back-to-back.
func TestReceiveExplainsAMissingDescriptor(t *testing.T) {
	a, b := socketpair(t)
	if _, err := a.Write(encodeHeader(0)); err != nil { // header with no ancillary data
		t.Fatal(err)
	}
	_, err := Receive(b)
	if err == nil {
		t.Fatal("Receive accepted a message carrying no descriptor")
	}
	if !strings.Contains(err.Error(), "buffered reader") {
		t.Errorf("error %q does not point at the likely cause", err)
	}
}
