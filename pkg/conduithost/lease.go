package conduithost

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLeaseHeld reports that another process holds the accept lease for this
// address, so this one must not accept.
//
// It is distinct from a conflict or an unresponsive host because it answers a
// different question: not "may I have this address" but "may I SERVE it". Only one
// participant may, and the answer is a kernel object rather than an agreement.
var ErrLeaseHeld = errors.New("conduithost: another process holds the accept lease for this conduit")

// lease is the exclusive right to accept connections on a conduit's listening
// socket, held for as long as this process serves it.
//
// It exists because listener replication arms every participant: they all hold a
// reference to the same socket from join time, so any of them COULD accept at any
// moment, and if two do the kernel splits connections between them with no error on
// either side — measured at 39/21 in
// listenerpass.TestTwoAcceptersSplitConnectionsSilently, with each answering from
// its own routing table. Before replication the invariant was structural (a
// non-host had no socket); now it is only conventional, so it needs an enforcement
// that does not depend on anyone behaving.
//
// A kernel lock is that enforcement, and it is stronger than any agreement protocol
// for a reason specific to one machine: accepting is a local action no peer can
// veto, so a losing participant has to check something ITSELF before accepting, and
// once such a check exists the kernel supplies it with a failure detector nothing
// else can match — the lease is released on death by any means, SIGKILL included,
// with no heartbeat, timeout or quorum.
//
// The cost is deliberate and was chosen over the alternative: a WEDGED BUT LIVE
// host keeps its lease, so the conduit stays down until that process dies. A
// process that might resume must not be fenced, because nothing can stop it
// resuming; the remedy is to kill it, and the error naming its pid says so.
type lease struct{ f *os.File }

// leasePath is the accept lease for a conduit bound at a. It sits beside the
// control socket and the advertisement, one per ADDRESS rather than per port: two
// conduits can legitimately share a port directory without sharing an address.
func (r *Registry) leasePath(a Addr) string {
	return filepath.Join(r.portDir(a.Port), fileKey(a)+".accept")
}
