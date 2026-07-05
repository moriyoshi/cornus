package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/cmd/cornus/internal/sshagent"
	"cornus/pkg/api"
	"cornus/pkg/wire"
)

// ingressTunnelResult is the structured result of `cornus ingress-tunnel`.
type ingressTunnelResult struct {
	Event    string   `json:"event"`
	Scope    string   `json:"scope"`
	URL      string   `json:"url"`
	Hosts    []string `json:"hosts,omitempty"`
	HostMode string   `json:"hostMode,omitempty"`
	Target   string   `json:"target,omitempty"`
}

func (r ingressTunnelResult) Human(p cliout.Printer) {
	p.Line("Ingress tunnel for %s ready at %s", r.Scope, r.URL)
	if len(r.Hosts) > 0 {
		p.Line("  serving: %s", strings.Join(r.Hosts, ", "))
	}
	switch r.Target {
	case "controller":
		p.Line("  fronting: the cluster ingress controller")
	case "mux":
		p.Line("  fronting: this server's ingress routing")
	}
	// The mode decides what the app sees in Host, which is the difference between
	// working redirects and mystifying ones — say it plainly rather than making
	// the user infer it from the URL.
	switch r.HostMode {
	case api.HostModeAlias:
		p.Line("  host: requests keep the tunnel hostname (the app sees %s)", hostOnly(r.URL))
	case api.HostModeRewrite:
		p.Line("  host: rewritten to the ingress hostname")
	case api.HostModePassthrough:
		p.Line("  host: passed through untouched")
	}
}

// hostOnly strips the scheme from a URL for display.
func hostOnly(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	return strings.TrimSuffix(raw, "/")
}

// IngressTunnelCmd asks a cornus server to host a public tunnel in front of its
// INGRESS, rather than in front of one port of one workload the way `cornus
// tunnel` does. Everything the ingress declares — every service of a compose
// project, on its own hostname and path — becomes reachable through a single
// public URL.
//
// What the tunnel actually fronts depends on the backend: on Kubernetes it is
// the cluster's real ingress controller (so the cluster's own routing and
// certificates apply), and everywhere else it is the server's own ingress
// routing table. Either way the deployment must already declare an ingress
// (x-cornus-ingress in compose, or ingress in the deploy spec).
//
// Credential handling is identical to `cornus tunnel`: the secret is injected
// into the server's already-authenticated endpoint, and --authtoken-file is
// preferred over --authtoken because the latter puts it in argv and shell
// history. The tunnel stays up until Ctrl-C.
type IngressTunnelCmd struct {
	Server        string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL (http(s):// or ws(s)://). Falls back to the selected connection profile (see cornus config).'"`
	AuthToken     string `kong:"name='authtoken',env='CORNUS_TUNNEL_AUTHTOKEN',help='Tunnel-backend credential (e.g. an ngrok authtoken, or an SSH key/password for the ssh backend). Injected into the server; omit only if the server has a default credential. Puts the secret in argv/history — prefer --authtoken-file.'"`
	AuthTokenFile string `kong:"name='authtoken-file',type='path',help='Read the tunnel-backend credential from this file instead of --authtoken, keeping it out of argv and shell history. Mutually exclusive with --authtoken.'"`
	ForwardAgent  bool   `kong:"name='forward-agent',help='Forward the local ssh-agent (SSH_AUTH_SOCK) to the server so the ssh backend can authenticate using agent-held keys. Only supported by the ssh backend. Like ssh -A, only use this against a cornus server you trust.'"`
	Proto         string `kong:"name='proto',default='http',help='Exposed protocol: http (default) or tcp. A tcp tunnel is a raw byte stream, so the client TLS and Host reach the ingress untouched — the only way to get end-to-end TLS.'"`
	Project       string `kong:"name='project',help='Expose every deployment of this compose project behind one URL. Mutually exclusive with the deployment argument.'"`
	HostMode      string `kong:"name='host-mode',default='auto',enum='auto,passthrough,alias,rewrite',help='What the app sees in the Host header: auto (recommended), passthrough (leave untouched), alias (route the tunnel hostname to the ingress host, app sees the tunnel hostname), or rewrite (replace Host with the ingress hostname; breaks absolute redirects and Domain= cookies).'"`
	Host          string `kong:"name='host',help='Which declared ingress hostname the tunnel fronts, when the scope serves more than one.'"`
	Deployment    string `kong:"arg,optional,help='Deployment whose ingress to expose. Omit when using --project.'"`
}

// Run posts the ingress-tunnel request, prints the public URL, and keeps the
// tunnel up until Ctrl-C / SIGTERM, then tears it down.
func (c *IngressTunnelCmd) Run(cli *CLI) error {
	if (c.Project == "") == (c.Deployment == "") {
		return fmt.Errorf("give exactly one of --project or a deployment name")
	}
	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	cl := cn.Client()

	// Profile defaults fill in what the invocation did not say. Explicit flags
	// always win, so a stored default can never silently override a deliberate
	// choice on the command line.
	authTokenFile := c.AuthTokenFile
	hostMode := c.HostMode
	if t := cn.Tunnel; t != nil {
		if authTokenFile == "" && c.AuthToken == "" {
			authTokenFile = t.AuthTokenFile
		}
		// "auto" is this flag's kong default, so it is indistinguishable from
		// unset — treat it as unset and let the profile speak.
		if hostMode == "" || (hostMode == api.HostModeAuto && t.IngressHostMode != "") {
			hostMode = t.IngressHostMode
		}
	}
	authToken, err := resolveAuthToken(c.AuthToken, authTokenFile)
	if err != nil {
		return err
	}

	// Open the agent side-channel before starting the tunnel so it is already
	// registered server-side by the time the POST below asks for it (see
	// TunnelCmd.Run for the full rationale).
	var agentConn, agentChannel net.Conn
	if c.ForwardAgent {
		agentConn, err = sshagent.Dial()
		if err != nil {
			return fmt.Errorf("--forward-agent: %w", err)
		}
		agentChannel, err = cl.IngressTunnelChannel(ctx, c.Project, c.Deployment, "ssh-agent")
		if err != nil {
			agentConn.Close()
			return fmt.Errorf("--forward-agent: opening agent channel: %w", err)
		}
		go wire.Pipe(agentConn, agentChannel)
	}

	st, err := cl.IngressTunnelStart(ctx, api.IngressTunnelRequest{
		AuthToken:    authToken,
		ForwardAgent: c.ForwardAgent,
		Project:      c.Project,
		Deployment:   c.Deployment,
		HostMode:     hostMode,
		Host:         c.Host,
		Proto:        c.Proto,
	})
	if err != nil {
		if agentConn != nil {
			agentConn.Close()
			agentChannel.Close()
		}
		return fmt.Errorf("starting ingress tunnel: %w", err)
	}

	d := cli.out()
	if err := d.Emit(ingressTunnelResult{
		Event:    "ingress-tunnel",
		Scope:    st.Scope,
		URL:      st.URL,
		Hosts:    st.Hosts,
		HostMode: st.HostMode,
		Target:   st.Target,
	}); err != nil {
		return err
	}
	d.Info("Press Ctrl-C to stop.")

	<-ctx.Done()

	// Tear down with a fresh (uncancelled) context: ctx is already cancelled.
	if err := cl.IngressTunnelStop(cli.rootContext(), c.Project, c.Deployment); err != nil {
		d.Warn("stopping ingress tunnel: %v", err)
	}
	return nil
}
