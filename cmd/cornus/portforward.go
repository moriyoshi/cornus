package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"cornus/pkg/api"
	"cornus/pkg/portfwd"
)

// PortForwardCmd forwards one or more local ports to container ports of a
// deployment's first instance (kubectl port-forward). For a cluster connection
// profile it tunnels each connection straight to the workload pod over the
// Kubernetes pods/portforward SPDY subresource using the developer's kubeconfig
// credentials (the server's own ServiceAccount usually cannot), falling back to
// the server proxy only when the direct attempt cannot open a tunnel. For a
// non-cluster profile it tunnels through the cornus server, which bridges to the
// container. Either way it reaches ports that were never published to a host or
// exposed via a Service. A "/udp" suffix (compose ports notation) forwards
// datagrams instead of a byte stream — supported on the dockerhost and containerd
// backends; kubernetes port-forward is TCP-only and such mappings are skipped
// with a warning. It stays in the foreground until Ctrl-C.
type PortForwardCmd struct {
	Server    string   `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL (http(s):// or ws(s)://). Falls back to the selected connection profile (see cornus config).'"`
	Address   string   `kong:"name='address',default='127.0.0.1',help='Local address to bind the listeners on.'"`
	ViaServer *bool    `kong:"name='via-server',negatable,help='Route the forward through the cornus server proxy instead of connecting to the pod directly with your kubeconfig (cluster profiles only). --no-via-server forces the direct path. Overrides CORNUS_VIA_SERVER and the profile.'"`
	Name      string   `kong:"arg,required,help='Deployment name to forward to.'"`
	Ports     []string `kong:"arg,required,help='Port mappings, each LOCAL:REMOTE (or a bare PORT for the same local and container port), optionally with a /tcp or /udp suffix (default tcp), e.g. 5353:53/udp.'"`
}

// portSpec is one parsed LOCAL:REMOTE[/PROTO] forwarding rule.
type portSpec struct {
	local  int
	remote int
	proto  string // "tcp" or "udp"
}

// parsePortSpec parses "LOCAL:REMOTE" or a bare "PORT" (local == remote), with
// an optional "/tcp" or "/udp" suffix (compose ports notation; default tcp).
// Ports must be in 1..65535.
func parsePortSpec(s string) (portSpec, error) {
	proto := "tcp"
	rest := s
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		proto = strings.ToLower(strings.TrimSpace(rest[i+1:]))
		rest = rest[:i]
		if proto != "tcp" && proto != "udp" {
			return portSpec{}, fmt.Errorf("invalid port mapping %q: protocol %q is not tcp or udp", s, proto)
		}
	}
	localStr, remoteStr, hasColon := strings.Cut(rest, ":")
	if !hasColon {
		remoteStr = localStr
	}
	local, err := parsePort(localStr)
	if err != nil {
		return portSpec{}, fmt.Errorf("invalid port mapping %q: %w", s, err)
	}
	remote, err := parsePort(remoteStr)
	if err != nil {
		return portSpec{}, fmt.Errorf("invalid port mapping %q: %w", s, err)
	}
	return portSpec{local: local, remote: remote, proto: proto}, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if p < 1 || p > 65535 {
		return 0, fmt.Errorf("port %d out of range (1-65535)", p)
	}
	return p, nil
}

// Run binds a local listener per mapping and forwards each accepted connection
// over its own tunnel until Ctrl-C / SIGTERM.
func (c *PortForwardCmd) Run(cli *CLI) error {
	mappings := make([]api.PortMapping, 0, len(c.Ports))
	for _, p := range c.Ports {
		spec, err := parsePortSpec(p)
		if err != nil {
			return err
		}
		mappings = append(mappings, api.PortMapping{Host: spec.local, Container: spec.remote, Protocol: spec.proto})
	}

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()

	d := cli.out()
	g, err := portfwd.Start(ctx, cn.Dialer(cn.ViaServer(c.ViaServer)), c.Name, mappings,
		portfwd.WithBindAddress(c.Address),
		portfwd.WithStrictBind(),
		portfwd.WithLogf(func(format string, args ...any) {
			d.Warn(format, args...)
		}))
	if err != nil {
		return err
	}
	for _, f := range g.Forwards() {
		if f.Mapping.Protocol == "udp" {
			d.Info("Forwarding %s -> %d/udp", f.Local, f.Mapping.Container)
			continue
		}
		d.Info("Forwarding %s -> %d", f.Local, f.Mapping.Container)
	}

	<-ctx.Done()
	g.Close()
	d.Done("port-forward stopped")
	return nil
}
