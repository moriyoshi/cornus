package conduithost

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// wedgedHost binds the address and its control socket exactly as a real host
// would, and then services NEITHER. It models a host that is deadlocked, stopped,
// or stuck in a syscall: the kernel still completes connections into its backlogs,
// so from outside it is indistinguishable from a healthy host to anything that
// only dials.
//
// It is not a corpse. The distinction is the whole point: a corpse has released
// its sockets and its address can be taken, while this process still holds both.
func wedgedHost(t *testing.T, r *Registry, addr Addr, pid int) {
	t.Helper()
	ln, err := net.Listen("tcp", addr.String())
	if err != nil {
		t.Fatalf("binding the wedged host's address: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	wedgedAdvertisement(t, r, addr, pid)
}

// wedgedAdvertisement is the same wedge WITHOUT binding the address: it claims the
// rendezvous and never answers, while the listening socket is held by somebody
// else. That is what a survivor which took over — inheriting the socket rather
// than binding it — and then wedged looks like from outside.
func wedgedAdvertisement(t *testing.T, r *Registry, addr Addr, pid int) {
	t.Helper()
	if err := r.ensurePortDir(addr.Port); err != nil {
		t.Fatal(err)
	}
	socket := r.SocketPath(addr)
	_ = os.Remove(socket)
	ctl, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("binding the wedged host's control socket: %v", err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	if err := r.writeEntry(Entry{Bind: addr.String(), Pid: pid, Socket: socket}); err != nil {
		t.Fatal(err)
	}
	// Nothing ever calls Accept.
}

// A dial-only probe cannot tell a wedged host from a healthy one, because the
// kernel accepts the connection either way. Requiring an ANSWER is what makes the
// difference observable at all, and every behaviour below depends on it.
func TestProbeDistinguishesWedgedFromHealthy(t *testing.T) {
	r := testRegistry(t)

	healthyPort := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(healthyPort), &fakeRegistrar{})
	if got := probeSocket(r.SocketPath(host.Addr())); got != socketLive {
		t.Errorf("probe of a healthy host = %v, want socketLive", got)
	}

	wedgedPort := freePort(t)
	wedged, err := ParseAddr("127.0.0.1:" + strconv.Itoa(wedgedPort))
	if err != nil {
		t.Fatal(err)
	}
	wedgedHost(t, r, wedged, os.Getpid())
	// The connection itself succeeds — that is the trap.
	c, derr := net.DialTimeout("unix", r.SocketPath(wedged), time.Second)
	if derr != nil {
		t.Fatalf("a wedged host should still ACCEPT connections, but the dial failed: %v", derr)
	}
	_ = c.Close()
	if got := probeSocket(r.SocketPath(wedged)); got != socketUnresponsive {
		t.Errorf("probe of a wedged host = %v, want socketUnresponsive", got)
	}
}

// A wedged host must be reported, by name, rather than joined. Joining it would
// block in the hello handshake — with the port lock held, so one wedged process
// would stall every other participant on that port in turn.
func TestOpenReportsAWedgedHostInsteadOfHanging(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	wedgedHost(t, r, addr, os.Getpid())

	done := make(chan error, 1)
	go func() {
		_, err := openAt(t, r, addr.String(), &fakeRegistrar{})
		done <- err
	}()

	select {
	case err := <-done:
		var unresponsive *UnresponsiveError
		if !errors.As(err, &unresponsive) {
			t.Fatalf("Open against a wedged host = %v, want an UnresponsiveError", err)
		}
		if unresponsive.HostPid != os.Getpid() {
			t.Errorf("error names pid %d, want %d", unresponsive.HostPid, os.Getpid())
		}
		if !contains(unresponsive.Error(), "not answering") {
			t.Errorf("error %q does not say what is wrong", unresponsive)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Open hung against a wedged host instead of reporting it")
	}
}

// A wedged host must NOT be reaped. It still holds the listening socket, so the
// address is not free; a participant that reaped it would go on to fail at bind
// with a message about the kernel instead of about the process to go and look at.
func TestAWedgedHostIsNotReaped(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	wedgedHost(t, r, addr, os.Getpid())

	entries, err := r.Live(port)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("Live reaped a wedged host (entries = %+v); its address is still held", entries)
	}
	if entries[0].Responsive() {
		t.Error("a wedged host is reported as Responsive")
	}
	if _, err := os.Stat(r.SocketPath(addr)); err != nil {
		t.Errorf("the wedged host's control socket was removed: %v", err)
	}
}

// A survivor must NOT step past a wedged host, even though it holds a replica and
// could physically serve the address.
//
// The wedged process still holds a reference to the same listening socket. If it
// recovers and resumes accepting there are two accepters on one address, and the
// kernel splits connections between them with no error on either side — each
// answering from its own routing table. Nothing can stop a process resuming, so a
// process that might resume must not be fenced; the remedy is to kill it, and the
// error says so by pid.
//
// This is the deliberate cost of the chosen policy: a hung host takes the conduit
// down until someone kills it.
func TestTakeoverRefusesToStepPastAWedgedHost(t *testing.T) {
	requireMigration(t)
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	survivor := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{}).(*Joiner)
	addr := host.Addr()

	_ = host.Close()
	<-survivor.Done()
	// Control socket only: the survivor's replica holds the listening socket, which
	// is what a process that took over and then wedged looks like from outside.
	wedgedAdvertisement(t, r, addr, 999999)

	done := make(chan struct{})
	var p Participant
	var err error
	go func() {
		defer close(done)
		p, err = survivor.Takeover(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Takeover hung against a wedged host")
	}
	if err == nil {
		_ = p.Close()
		t.Fatal("Takeover stepped past a wedged host; if that process recovers, two accepters split the address")
	}
	var unresponsive *UnresponsiveError
	if !errors.As(err, &unresponsive) {
		t.Fatalf("Takeover past a wedged host = %v, want an UnresponsiveError", err)
	}
	if unresponsive.HostPid != 999999 {
		t.Errorf("error names pid %d, want the wedged host's 999999", unresponsive.HostPid)
	}
	if !contains(unresponsive.Error(), "kill") {
		t.Errorf("error %q does not say how to resolve it", unresponsive)
	}
}

// One wedged host must not stall unrelated conduits. The port lock is per port, so
// a probe that blocked would only ever affect its own — but this pins it, because
// a shared lock or a global probe timeout would quietly break it.
func TestAWedgedHostDoesNotStallAnotherPort(t *testing.T) {
	r := testRegistry(t)
	wedgedPort := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(wedgedPort))
	if err != nil {
		t.Fatal(err)
	}
	wedgedHost(t, r, addr, os.Getpid())

	start := time.Now()
	p := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(freePort(t)), &fakeRegistrar{})
	if !p.Hosting() {
		t.Fatal("did not host on an unrelated port")
	}
	if elapsed := time.Since(start); elapsed > probeTimeout {
		t.Errorf("opening an unrelated port took %s; a wedged conduit on another port is stalling it", elapsed)
	}
}

// The accepting right is a kernel lock, so two participants cannot both hold it —
// not by convention but by construction. This is the enforcement that replaces the
// step-past path: every participant holds a replica and could physically accept, so
// nothing else prevents the 39/21 silent split measured in
// listenerpass.TestTwoAcceptersSplitConnectionsSilently.
func TestOnlyOneParticipantHoldsTheAcceptLease(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ensurePortDir(port); err != nil {
		t.Fatal(err)
	}

	first, err := acquireLease(r.leasePath(addr))
	if err != nil {
		t.Fatalf("acquiring a free lease: %v", err)
	}
	if _, err := acquireLease(r.leasePath(addr)); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second acquisition = %v, want ErrLeaseHeld", err)
	}

	// Releasing hands it on. In the real failure the kernel does this, by any means
	// of death including SIGKILL, which is why the accepting right is a kernel
	// object rather than a record someone has to maintain.
	first.release()
	second, err := acquireLease(r.leasePath(addr))
	if err != nil {
		t.Fatalf("acquiring after release: %v", err)
	}
	second.release()
}

// A host must hold the lease for as long as it serves, and a second host on the
// same address must be refused — the lease is what makes that structural.
func TestHostingRequiresTheAcceptLease(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	addr, err := ParseAddr("127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ensurePortDir(port); err != nil {
		t.Fatal(err)
	}

	// Somebody else is accepting on this address without having advertised it.
	held, err := acquireLease(r.leasePath(addr))
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()

	_, err = openAt(t, r, addr.String(), &fakeRegistrar{})
	if !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("Open while another process holds the accept lease = %v, want ErrLeaseHeld", err)
	}
}

// And the lease must be freed when a host closes, or the address could never be
// taken over at all.
func TestClosingAHostReleasesTheAcceptLease(t *testing.T) {
	r := testRegistry(t)
	port := freePort(t)
	host := mustOpenAt(t, r, "127.0.0.1:"+strconv.Itoa(port), &fakeRegistrar{})
	addr := host.Addr()

	if _, err := acquireLease(r.leasePath(addr)); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("lease while hosting = %v, want ErrLeaseHeld", err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	l, err := acquireLease(r.leasePath(addr))
	if err != nil {
		t.Fatalf("lease after the host closed = %v, want it free", err)
	}
	l.release()
}
