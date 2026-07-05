package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"cornus/pkg/clientconduit"
	"cornus/pkg/socks5"
)

// Socks5Cmd runs a client-side SOCKS5 split-tunnel proxy that reaches a cornus
// server's workloads by name. A CONNECT target whose host:port matches a
// resolution rule (by default, any host bearing the --service-host-suffix, e.g.
// web.cornus.internal) is tunneled to that service through the server's
// port-forward transport; every other destination is dialed directly from this
// machine. It is the ad-hoc counterpart to the per-session conduit mode selected
// by `cornus config set-context --conduit-mode socks5`. Stays foreground until
// Ctrl-C.
type Socks5Cmd struct {
	Server         string   `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL (http(s):// or ws(s)://). Falls back to the selected connection profile (see cornus config).'"`
	Listen         string   `kong:"name='listen',help='Local address to bind the SOCKS5 proxy on (default 127.0.0.1:1080, or the profile value).'"`
	ServiceSuffix  string   `kong:"name='service-host-suffix',help='Host suffix whose CONNECT targets are tunneled to the matching service (default .cornus.internal); other hosts egress directly.'"`
	Resolve        []string `kong:"name='resolve',help='Advanced resolution rule PATTERN=REPLACE (repeatable, ordered, first match wins); replaces the suffix default. PATTERN matches host:port, REPLACE yields service:port (sed-style \\1 backrefs).'"`
	ViaServer      *bool    `kong:"name='via-server',negatable,help='Route tunneled connections through the cornus server proxy instead of connecting to pods directly with your kubeconfig (cluster profiles only). --no-via-server forces the direct path. Overrides CORNUS_VIA_SERVER and the profile.'"`
	AllowOpenProxy bool     `kong:"name='allow-non-loopback',help='Permit binding --listen to a non-loopback address. Refused by default: this proxy has no authentication and dials arbitrary destinations from this host, so off-host it is an open proxy for anyone who can reach it.'"`
}

// Run binds the SOCKS5 listener and serves it until Ctrl-C / SIGTERM.
func (c *Socks5Cmd) Run(cli *CLI) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()

	// Start from the profile's SOCKS5 settings, then apply explicit flag overrides.
	cfg := cn.ConduitConfig(clientconduit.ModeSocks5)
	cfg.Mode = clientconduit.ModeSocks5
	if c.Listen != "" {
		cfg.Socks5Listen = c.Listen
	}
	if c.ServiceSuffix != "" {
		cfg.Socks5Suffix = c.ServiceSuffix
	}
	cfg.Socks5AllowNonLoopback = c.AllowOpenProxy
	if len(c.Resolve) > 0 {
		rules, err := parseResolveRules(c.Resolve)
		if err != nil {
			return err
		}
		cfg.Socks5Resolve = nil
		for _, r := range rules {
			cfg.Socks5Resolve = append(cfg.Socks5Resolve, socks5.Rule{Pattern: r.Pattern, Replace: r.Replace})
		}
	}

	eg, err := clientconduit.Start(ctx, cn.Dialer(cn.ViaServer(c.ViaServer)), cfg)
	if err != nil {
		return err
	}
	defer eg.Close()
	d := cli.out()
	for _, line := range eg.Banner() {
		d.Info("%s", line)
	}

	<-ctx.Done()
	d.Done("socks5 proxy stopped")
	return nil
}
