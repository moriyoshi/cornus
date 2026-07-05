package dockerhost

// ssh:// podman endpoints.
//
// Podman's own tooling produces these routinely — `podman system connection add`
// stores destinations like
// ssh://core@host:22/run/user/1000/podman/podman.sock — so an operator copying
// their existing configuration into CORNUS_PODMAN_SOCKET arrives here.
//
// The transport is SSH's direct-streamlocal channel: a UNIX socket on the remote
// host, not a TCP port. That is why this cannot reuse the plain dialers — and
// why it lives here rather than in pkg/runtimeendpoint, which sits below
// pkg/sshclient in the import graph.
//
// NOTE ON SCOPE, because the two are easy to confuse and the docs must not:
// this is cornus running LOCALLY and driving a REMOTE runtime socket. It is not
// the SSH-tunnel context in docs/guides/remote-docker-hosts.md, where cornus's
// SERVER runs on the far end and the tunnel carries the cornus API. Both are
// supported; they are different topologies with different failure modes.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"cornus/pkg/runtimeendpoint"
	"cornus/pkg/sshclient"
)

// podmanSSHEndpoint builds an Endpoint that reaches a remote podman socket over
// SSH.
//
// The SSH connection is established EAGERLY, matching pkg/sshclient.Dial's own
// rationale: a bad host key, an unreadable identity file or a wrong destination
// should fail at configuration time rather than at the first deploy. The
// resulting Dialer reconnects on its own thereafter.
//
// Its lifetime is the process's. That is a deliberate, bounded leak: exactly one
// Dialer exists per podman endpoint per process, an Endpoint is a value with no
// Close, and the alternative — threading a closer through every construction
// site — buys nothing for a connection the server holds until it exits.
func podmanSSHEndpoint(ctx context.Context, raw string) (runtimeendpoint.Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return runtimeendpoint.Endpoint{}, fmt.Errorf("%s: %w", envPodmanSocket, err)
	}
	sock := u.Path
	if sock == "" || sock == "/" {
		return runtimeendpoint.Endpoint{}, fmt.Errorf(
			"%s=%q names no socket path on the remote host; podman spells these "+
				"ssh://user@host/run/user/<uid>/podman/podman.sock", envPodmanSocket, raw)
	}

	dest := u.Host // host[:port], possibly with user@ split out below
	if u.User != nil && u.User.Username() != "" {
		dest = u.User.Username() + "@" + dest
	}

	// Resolve through ssh_config so an alias, IdentityFile, User, Port and
	// ProxyJump the operator already maintains apply here too — the same
	// resolution every other cornus SSH path uses.
	opts, err := sshclient.Resolve(dest, sshclient.Options{}, true)
	if err != nil {
		return runtimeendpoint.Endpoint{}, fmt.Errorf("%s: resolving %q: %w", envPodmanSocket, dest, err)
	}
	dialer, err := sshclient.Dial(ctx, opts)
	if err != nil {
		return runtimeendpoint.Endpoint{}, fmt.Errorf("%s: connecting to %q: %w", envPodmanSocket, dest, err)
	}

	// "unix" selects SSH's direct-streamlocal channel, so the socket is opened in
	// the REMOTE host's filesystem.
	dial := func(ctx context.Context) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", sock)
	}
	// nonLocal is asserted true: the runtime is on another machine by
	// construction, so its container IPs cannot be routable from here. That is
	// what makes port-forward explain itself instead of timing out.
	return runtimeendpoint.FromDialer(dial, "http://podman", "podman", true), nil
}

// isSSHEndpoint reports whether a CORNUS_PODMAN_SOCKET value names an ssh://
// destination.
func isSSHEndpoint(v string) bool { return strings.HasPrefix(v, "ssh://") }
