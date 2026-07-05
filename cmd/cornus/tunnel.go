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

// tunnelResult is the structured result of `cornus tunnel`: the public URL as a
// human line, or a JSON object in json mode.
type tunnelResult struct {
	Event string `json:"event"`
	Name  string `json:"name"`
	Port  int    `json:"port"`
	URL   string `json:"url"`
}

func (r tunnelResult) Human(p cliout.Printer) {
	p.Line("Tunnel to %s:%d ready at %s", r.Name, r.Port, r.URL)
}

// TunnelCmd asks a cornus server to host a public tunnel to a deployment's port
// so the running application can be reached from anywhere (e.g. sharing
// in-progress work, receiving webhooks). The server hosts the tunnel in-process
// and bridges it to the workload — the tunnel reaches ports the workload never
// published, on any backend, just like `cornus port-forward`, but with a public
// URL instead of a local listener.
//
// The tunnel credential — an ngrok authtoken, an SSH key/password for the ssh
// backend, or nothing for cloudflare/tailscale — is injected into the
// server's already-authenticated endpoint: the server cannot know it
// beforehand. --authtoken puts the secret in argv, which is visible to other
// users on the machine via `ps` and often lands in shell history — prefer
// --authtoken-file, the CORNUS_TUNNEL_AUTHTOKEN env var, or (best) omitting
// it entirely when the server has a default credential
// (CORNUS_TUNNEL_AUTHTOKEN set server-side instead, out of band). The tunnel
// stays up until Ctrl-C, which tears it down.
type TunnelCmd struct {
	Server        string `kong:"name='server',env='CORNUS_SERVER',help='Remote cornus server URL (http(s):// or ws(s)://). Falls back to the selected connection profile (see cornus config).'"`
	AuthToken     string `kong:"name='authtoken',env='CORNUS_TUNNEL_AUTHTOKEN',help='Tunnel-backend credential (e.g. an ngrok authtoken, or an SSH key/password for the ssh backend). Injected into the server; omit only if the server has a default credential. Also read from NGROK_AUTHTOKEN as a legacy alias. Puts the secret in argv/history — prefer --authtoken-file.'"`
	AuthTokenFile string `kong:"name='authtoken-file',type='path',help='Read the tunnel-backend credential from this file instead of --authtoken, keeping it out of argv and shell history. Mutually exclusive with --authtoken.'"`
	ForwardAgent  bool   `kong:"name='forward-agent',help='Forward the local ssh-agent (SSH_AUTH_SOCK) to the server so the ssh backend can authenticate using agent-held keys instead of a raw token or password. Only supported by the ssh backend. Like ssh -A, only use this against a cornus server you trust: while the tunnel is starting, the server can ask the forwarded agent to sign arbitrary challenges, not only ones from the relay.'"`
	Proto         string `kong:"name='proto',default='http',help='Exposed protocol: http (default) or tcp.'"`
	Name          string `kong:"arg,required,help='Deployment name to expose.'"`
	Port          int    `kong:"arg,required,help='Container port to expose through the tunnel.'"`
}

// resolveAuthToken picks the credential to inject: at most one of a direct
// --authtoken value or an --authtoken-file path (the latter read and trimmed
// of a single trailing newline, matching how e.g. `kubectl create secret
// --from-file` round-trips a file's contents). If neither yields a value, it
// falls back to the legacy NGROK_AUTHTOKEN env var — --authtoken's own kong
// binding predates the generic CORNUS_TUNNEL_AUTHTOKEN name, and many ngrok
// users already have it set from other tools.
func resolveAuthToken(token, tokenFile string) (string, error) {
	if token != "" && tokenFile != "" {
		return "", fmt.Errorf("--authtoken and --authtoken-file are mutually exclusive")
	}
	switch {
	case tokenFile != "":
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("reading --authtoken-file: %w", err)
		}
		return strings.TrimSuffix(string(b), "\n"), nil
	case token != "":
		return token, nil
	default:
		return os.Getenv("NGROK_AUTHTOKEN"), nil
	}
}

// Run posts the tunnel request, prints the public URL, and keeps the tunnel up
// until Ctrl-C / SIGTERM, then tears it down.
func (c *TunnelCmd) Run(cli *CLI) error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range (1-65535)", c.Port)
	}
	authToken, err := resolveAuthToken(c.AuthToken, c.AuthTokenFile)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cli.rootContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cn, err := cli.requireConn(c.Server)
	if err != nil {
		return err
	}
	defer cn.Cleanup()
	cl := cn.Client()

	// Open the agent side-channel before starting the tunnel, so it is already
	// registered server-side by the time the POST below asks for it. The
	// spliced goroutine, and thus the channel and the local agent dial, end on
	// their own once the server is done with the channel (right after the SSH
	// handshake that consults it) — see pkg/server/deploy_tunnel.go.
	var agentConn, agentChannel net.Conn
	if c.ForwardAgent {
		agentConn, err = sshagent.Dial()
		if err != nil {
			return fmt.Errorf("--forward-agent: %w", err)
		}
		agentChannel, err = cl.TunnelChannel(ctx, c.Name, "ssh-agent")
		if err != nil {
			agentConn.Close()
			return fmt.Errorf("--forward-agent: opening agent channel: %w", err)
		}
		go wire.Pipe(agentConn, agentChannel)
	}

	st, err := cl.TunnelStart(ctx, c.Name, api.TunnelRequest{
		AuthToken:    authToken,
		ForwardAgent: c.ForwardAgent,
		Port:         c.Port,
		Proto:        c.Proto,
	})
	if err != nil {
		if agentConn != nil {
			agentConn.Close()
			agentChannel.Close()
		}
		return fmt.Errorf("starting tunnel: %w", err)
	}
	d := cli.out()
	if err := d.Emit(tunnelResult{Event: "tunnel", Name: c.Name, Port: c.Port, URL: st.URL}); err != nil {
		return err
	}
	d.Info("Press Ctrl-C to stop.")

	<-ctx.Done()

	// Tear the tunnel down with a fresh (uncancelled) context: ctx is already
	// cancelled. rootContext still traces the teardown under the invocation span.
	if err := cl.TunnelStop(cli.rootContext(), c.Name); err != nil {
		d.Warn("stopping tunnel: %v", err)
	}
	d.Done("tunnel stopped")
	return nil
}
