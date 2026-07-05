//go:build linux

package incushost

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/deploy"
	"cornus/pkg/wire"
)

// ForwardPort bridges conn to a port inside the deployment's first instance by
// dialing the instance's own IP (Incus instances get a routable address on their
// bridge network, reachable from the daemon host). proto is "tcp" (default) or
// "udp". It returns when either side of the stream closes.
func (b *Backend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if proto != "" && proto != "tcp" && proto != "udp" {
		return fmt.Errorf("incus: unsupported port-forward protocol %q (only tcp and udp)", proto)
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
