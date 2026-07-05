package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"cornus/pkg/caretaker"
)

// HubCmd joins the cornus workload-to-workload overlay as a spoke from anywhere
// (e.g. a developer laptop) — the Phase 5 cross-network entry point. It reuses the
// caretaker hub role: services this host offers are registered for DELIVERY (the
// hub relays inbound to this spoke, which dials the local target — so a NAT'd host
// need not be reachable by the hub), and services this host reaches bind a local
// loopback listener that funnels into the hub.
type HubCmd struct {
	Server   string   `kong:"name='server',env='CORNUS_SERVER',help='Hub URL (ws(s):// or http(s)://) of the cornus server. Falls back to the selected connection profile (see cornus config).'"`
	Identity string   `kong:"help='This spoke identity (used for hub policy).'"`
	Register []string `kong:"name='register',help='Offer a local service to the overlay: name=host:port (relayed to this spoke via delivery). Repeatable.'"`
	Reach    []string `kong:"name='reach',help='Reach an overlay service: name=listen_ip:port (binds the local listener). Repeatable.'"`
}

// Run resolves the hub connection (explicit --server, else the selected connection
// profile — including its token/kube-auth and automatic port-forward), builds the
// hub role from the flags, and runs it until SIGINT/SIGTERM.
func (c *HubCmd) Run(cli *CLI) error {
	cfg, cleanup, err := c.caretakerConfig(cli)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return caretaker.Run(ctx, cfg)
}

// caretakerConfig validates the hub flags, resolves the server connection through
// the shared resolver (explicit --server wins; else the selected connection
// profile, with kube-auth token minting and an automatic port-forward for a
// pf-only profile), and assembles the caretaker config the hub role runs under.
// The resolved token rides the caretaker's WebSocket handshake (Authorization:
// Bearer, via caretaker.Config.Token), and the profile's TLS material (custom CA,
// mTLS client cert, insecure-skip-verify) rides the dial via
// caretaker.Config.TLSClientConfig. Flags are validated before the connection
// is resolved so bad flags never start a port-forward. The returned cleanup tears
// down anything the resolution started and is safe to defer; it is non-nil only
// on success.
func (c *HubCmd) caretakerConfig(cli *CLI) (caretaker.Config, func(), error) {
	role, err := hubRoleFromFlags(c.Server, c.Identity, c.Register, c.Reach)
	if err != nil {
		return caretaker.Config{}, nil, err
	}
	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return caretaker.Config{}, nil, err
	}
	role.Server = cn.Endpoint
	return caretaker.Config{Hub: &role, Token: cn.Token, TLSClientConfig: cn.TLS}, cn.Cleanup, nil
}

// hubRoleFromFlags parses the register/reach flag lists into a caretaker.HubRole.
// A --register entry is name=host:port (hosted via delivery); a --reach entry is
// name=listen_ip:port (a local listener forwarded to the hub).
func hubRoleFromFlags(server, identity string, register, reach []string) (caretaker.HubRole, error) {
	role := caretaker.HubRole{Server: server, Identity: identity}
	for _, r := range register {
		name, addr, ok := strings.Cut(r, "=")
		if !ok || name == "" || addr == "" {
			return role, fmt.Errorf("--register must be name=host:port, got %q", r)
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return role, fmt.Errorf("--register %q: %w", r, err)
		}
		role.Register = append(role.Register, caretaker.HubService{Name: name, Target: addr})
	}
	for _, r := range reach {
		name, listen, ok := strings.Cut(r, "=")
		if !ok || name == "" {
			return role, fmt.Errorf("--reach must be name=listen_ip:port, got %q", r)
		}
		host, portStr, err := net.SplitHostPort(listen)
		if err != nil {
			return role, fmt.Errorf("--reach %q: %w", r, err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return role, fmt.Errorf("--reach %q: bad port: %w", r, err)
		}
		role.Reach = append(role.Reach, caretaker.HubPeer{Name: name, Listen: host, Ports: []int{port}})
	}
	if len(role.Register) == 0 && len(role.Reach) == 0 {
		return role, fmt.Errorf("hub: nothing to do (give --register and/or --reach)")
	}
	return role, nil
}
