//go:build linux

package incushost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/deploy"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// ForwardPort bridges conn to a port inside the deployment's first instance by
// dialing the instance's own IP (Incus instances get a routable address on their
// bridge network, reachable from the daemon host). proto is "tcp" (default) or
// "udp". It returns when either side of the stream closes.
//
// In remote mode the direct dial is exactly what cannot be relied on — a cornus
// with its own network namespace has no route to the incus bridge — so the
// traffic is rerouted through the instance's companion instead, mirroring
// dockerhost/containerdhost.
func (b *Backend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if proto != "" && proto != "tcp" && proto != "udp" {
		return fmt.Errorf("incus: unsupported port-forward protocol %q (only tcp and udp)", proto)
	}
	if b.remote {
		return b.forwardPortViaCompanion(ctx, name, port, proto, conn)
	}
	id, err := b.firstInstance(name)
	if err != nil {
		return err
	}
	ip, err := b.instanceIPv4(id)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var d net.Dialer
	if proto == "udp" {
		upstream, err := d.DialContext(ctx, "udp", addr)
		if err != nil {
			return fmt.Errorf("incus: dial instance udp %s: %w", addr, err)
		}
		wire.BridgeDatagramStream(conn, upstream)
		return nil
	}
	upstream, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("incus: dial instance %s: %w", addr, err)
	}
	return deploy.Bridge(conn, upstream)
}

// forwardPortViaCompanion reroutes ForwardPort through the deployment's first
// instance's companion caretaker connection, looked up in the per-instance
// registry (always replica 0, matching firstInstance's existing scope). The
// companion's PortForwardRole dials the app instance for each stream — by
// address rather than loopback, since an Incus companion is a sibling instance
// rather than a netns-sharing sidecar (see companion_linux.go).
func (b *Backend) forwardPortViaCompanion(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if b.companions == nil {
		return fmt.Errorf("incus: remote mode has no companion registry configured")
	}
	instance := remotecompanion.InstanceKey(name, 0)
	sess := b.companions.Get(instance)
	if sess == nil {
		return fmt.Errorf("incus: remote companion for %q is not connected yet", instance)
	}
	stream, err := wire.OpenPortForward(sess, port, proto)
	if err != nil {
		return fmt.Errorf("incus: open port-forward relay to companion: %w", err)
	}
	// wire.Pipe for BOTH protocols, and deliberately not wire.Bridge or
	// wire.BridgeDatagramStream.
	//
	// Not deploy.Bridge: a yamux stream has no CloseWrite, so Bridge's
	// half-close-on-client-EOF branch would silently no-op and leak this stream
	// until the companion's own upstream connection happened to end for unrelated
	// reasons.
	//
	// Not BridgeDatagramStream for udp: that function converts BETWEEN cornus's
	// framed-datagram encoding and a real packet socket, and there is no packet
	// socket on this side of the relay. Both ends here are already framed — the
	// caller's tunnel carries wire datagrams, and the companion's PortForwardRole
	// reads wire datagrams off the stream before writing them to the real UDP
	// socket — so the correct relay is a byte-for-byte copy. Converting would
	// strip the framing the companion is about to parse.
	wire.Pipe(conn, stream)
	return nil
}

// SupportsUDPPortForward reports that this backend can bridge udp ForwardPort
// tunnels (the server probes this before acking a UDP tunnel).
//
// This declaration was MISSING while ForwardPort above implemented udp in full,
// on both the direct and companion paths. Because the server treats the absent
// interface as "cannot", every `cornus port-forward ...:PORT/udp` against incus
// was refused with "UDP port-forward is not supported by the incus backend" —
// a capability the reference documents as working ("for both TCP and UDP") and
// that the code does implement. An optional capability interface fails exactly
// this way: forgetting to declare it is indistinguishable from not having it,
// and no build or test notices. TestEveryUDPForwardingBackendDeclaresIt in
// pkg/deploy now makes the two agree.
func (b *Backend) SupportsUDPPortForward() bool { return true }

// instanceIPv4 returns the instance's first global IPv4 address from its live
// network state, skipping loopback and the incus host-side veth. It errors when
// the instance has no usable address (not yet networked, or stopped).
func (b *Backend) instanceIPv4(id string) (string, error) {
	st, err := b.conn.InstanceState(id)
	if err != nil {
		return "", fmt.Errorf("incus: reading instance state: %w", err)
	}
	if st == nil {
		return "", fmt.Errorf("incus: instance %q: %w", id, deploy.ErrNotFound)
	}
	if ip := pickIPv4(st.Network); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("incus: instance %s has no global IPv4 address", id)
}

// waitInstanceIPv4 polls instanceIPv4 until the instance has a global IPv4 or
// ctx ends. A freshly-started instance is reachable over the Incus API before its
// interface has been addressed, so the address a companion must dial does not
// exist yet at create time — a companion pointed at an unaddressed instance would
// relay into nothing. It returns the last observed error on timeout so the caller
// reports why the address never appeared, rather than a bare deadline.
func (b *Backend) waitInstanceIPv4(ctx context.Context, id string) (string, error) {
	const poll = 200 * time.Millisecond
	t := time.NewTicker(poll)
	defer t.Stop()
	var last error
	for {
		ip, err := b.instanceIPv4(id)
		if err == nil {
			return ip, nil
		}
		// A vanished instance will never acquire an address; fail immediately
		// rather than burning the whole deadline on it.
		if errors.Is(err, deploy.ErrNotFound) {
			return "", err
		}
		last = err
		select {
		case <-ctx.Done():
			if last != nil {
				return "", fmt.Errorf("incus: waiting for instance %s to be addressed: %w (last: %v)", id, ctx.Err(), last)
			}
			return "", fmt.Errorf("incus: waiting for instance %s to be addressed: %w", id, ctx.Err())
		case <-t.C:
		}
	}
}

// pickIPv4 scans an instance's per-interface addresses for the first global
// (non-loopback) IPv4, ignoring the loopback interface entirely.
func pickIPv4(network map[string]incusapi.InstanceStateNetwork) string {
	for iface, n := range network {
		if iface == "lo" {
			continue
		}
		for _, a := range n.Addresses {
			if a.Family == "inet" && a.Scope == "global" {
				return a.Address
			}
		}
	}
	return ""
}
