//go:build !unix && !windows

package listenerpass

import "net"

// Everywhere else (js/wasm, plan9) there is no way to hand a live socket to
// another process. Callers are expected to check Supported and degrade — without
// replication a bound address dies with the process that owns it, which is a
// weaker guarantee but not a broken one.

func supported() bool { return false }

func send(_ *net.UnixConn, _ net.Listener, _ Peer) error { return ErrUnsupported }

func receive(_ *net.UnixConn) (net.Listener, error) { return nil, ErrUnsupported }

// verify has nothing to check where replication cannot happen: no replica can
// exist, so any listener handed here is the caller's own and already known good.
func verify(_ net.Listener) error { return nil }
