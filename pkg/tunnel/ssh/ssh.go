// Package ssh implements the cornus tunnel.Provider on top of SSH remote port
// forwarding (the SSH "tcpip-forward" global request), using
// golang.org/x/crypto/ssh — already in cornus's dependency tree, so this backend
// adds no new heavy dependency. It works against self-hostable SSH tunnel
// servers (sish, serveo, pinggy, localhost.run) and any plain sshd with
// GatewayPorts enabled. The cornus server dials the SSH endpoint, requests a
// remote-forward listener, and hands each forwarded connection back to the
// manager to bridge to the workload.
//
// Configuration is server-side (the operator picks the SSH endpoint); the
// per-tunnel Credential.AuthToken carries the SSH private key (PEM) or password.
package ssh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"cornus/pkg/sshclient"
	"cornus/pkg/tunnel"
)

func init() {
	tunnel.Register("ssh", func() (any, error) { return provider{}, nil })
}

type provider struct{}

// sshConfig is the operator-side configuration read from the environment.
type sshConfig struct {
	addr           string // CORNUS_TUNNEL_SSH_ADDR (host:port)
	user           string // CORNUS_TUNNEL_SSH_USER
	bindAddr       string // CORNUS_TUNNEL_SSH_BIND (remote bind, default 0.0.0.0:0)
	urlTemplate    string // CORNUS_TUNNEL_SSH_URL_TEMPLATE, {port} placeholder
	urlFromSession bool   // CORNUS_TUNNEL_SSH_URL_FROM_SESSION: read the URL the service prints
	knownHosts     string // CORNUS_TUNNEL_SSH_KNOWN_HOSTS (file path)
	hostKey        string // CORNUS_TUNNEL_SSH_HOSTKEY (authorized_keys-format line)
	insecure       bool   // CORNUS_TUNNEL_SSH_INSECURE (dev only: skip host-key verification)
}

func loadSSHConfig() (sshConfig, error) {
	c := sshConfig{
		addr:           os.Getenv("CORNUS_TUNNEL_SSH_ADDR"),
		user:           os.Getenv("CORNUS_TUNNEL_SSH_USER"),
		bindAddr:       os.Getenv("CORNUS_TUNNEL_SSH_BIND"),
		urlTemplate:    os.Getenv("CORNUS_TUNNEL_SSH_URL_TEMPLATE"),
		urlFromSession: os.Getenv("CORNUS_TUNNEL_SSH_URL_FROM_SESSION") != "",
		knownHosts:     os.Getenv("CORNUS_TUNNEL_SSH_KNOWN_HOSTS"),
		hostKey:        os.Getenv("CORNUS_TUNNEL_SSH_HOSTKEY"),
		insecure:       os.Getenv("CORNUS_TUNNEL_SSH_INSECURE") != "",
	}
	if c.addr == "" {
		return c, errors.New("ssh: CORNUS_TUNNEL_SSH_ADDR is required (host:port of the SSH tunnel endpoint)")
	}
	if c.user == "" {
		c.user = "cornus"
	}
	if c.bindAddr == "" {
		c.bindAddr = "0.0.0.0:0"
	}
	return c, nil
}

func (provider) Start(ctx context.Context, cred tunnel.Credential, opts tunnel.Options) (tunnel.Session, error) {
	cfg, err := loadSSHConfig()
	if err != nil {
		return nil, err
	}
	auths, err := authMethods(cred.AuthToken, cred.Agent)
	if err != nil {
		return nil, err
	}
	hostKeyCB, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.user,
		Auth:            auths,
		HostKeyCallback: hostKeyCB,
		Timeout:         15 * time.Second,
	}

	// Honor ctx cancellation during the dial.
	d := net.Dialer{Timeout: clientCfg.Timeout}
	rawConn, err := d.DialContext(ctx, "tcp", cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("ssh: dial %s: %w", cfg.addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, cfg.addr, clientCfg)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("ssh: handshake with %s: %w", cfg.addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	ln, err := client.Listen("tcp", cfg.bindAddr)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh: remote-forward request (%s): %w", cfg.bindAddr, err)
	}

	url := resolveURL(cfg, client, ln)
	return &tunnel.ListenerSession{
		Listener:   ln,
		PublicURL:  url,
		ExtraClose: client.Close,
	}, nil
}

// authMethods builds the SSH auth method list from the injected credential.
// When ag is non-nil (the caller forwarded its local ssh-agent), its signers
// are tried first — this is how a passphrase-protected key becomes usable,
// since the agent (not cornus) holds the decrypted key. token, if also set,
// contributes a second method: a PEM private key if it parses as one,
// otherwise a password. At least one of ag or token is required.
func authMethods(token string, ag agent.Agent) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if ag != nil {
		methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
	}
	if token != "" {
		// A PEM-armored token is a private key. Parse it as such and never fall
		// back to password auth for it: an encrypted key yields a
		// *ssh.PassphraseMissingError, and treating that as a password would leak
		// the entire key blob to the remote sshd as a password attempt.
		if strings.HasPrefix(strings.TrimSpace(token), "-----BEGIN") {
			signer, err := ssh.ParsePrivateKey([]byte(token))
			if err != nil {
				var passErr *ssh.PassphraseMissingError
				if errors.As(err, &passErr) {
					return nil, errors.New("ssh: credential is a passphrase-protected private key, which is not supported; provide an unencrypted key PEM or a password, or use --forward-agent")
				}
				return nil, fmt.Errorf("ssh: credential looks like a private key PEM but could not be parsed: %w", err)
			}
			methods = append(methods, ssh.PublicKeys(signer))
		} else {
			// Not a PEM private key — treat it as a password.
			methods = append(methods, ssh.Password(token))
		}
	}
	if len(methods) == 0 {
		return nil, errors.New("ssh: no credential provided (forward an ssh-agent with --forward-agent, or supply an SSH private key PEM or password)")
	}
	return methods, nil
}

// hostKeyCallback builds the host-key verifier, failing closed: with no
// known-hosts file, no pinned key, and no explicit insecure opt-in, it errors
// rather than trusting any host key. It delegates to the shared
// sshclient.HostKeyCallback, wrapping the "not configured" error with the
// tunnel-specific env-var guidance.
func hostKeyCallback(cfg sshConfig) (ssh.HostKeyCallback, error) {
	cb, err := sshclient.HostKeyCallback(cfg.knownHosts, cfg.hostKey, cfg.insecure)
	if err != nil {
		return nil, errors.New("ssh: host-key verification not configured; set CORNUS_TUNNEL_SSH_KNOWN_HOSTS or CORNUS_TUNNEL_SSH_HOSTKEY (or CORNUS_TUNNEL_SSH_INSECURE=1 for dev)")
	}
	return cb, nil
}

// resolveURL determines the public URL. In url-from-session mode it reads the URL
// the tunnel service announces on an SSH session (sish/serveo/pinggy/…). Otherwise
// it substitutes the bound remote port into the configured template, or falls back
// to tcp://host:port.
func resolveURL(cfg sshConfig, client *ssh.Client, ln net.Listener) string {
	if cfg.urlFromSession {
		if u := readSessionURL(client, 10*time.Second); u != "" {
			return u
		}
		// fall through to the template/host:port form
	}
	port := boundPort(ln, cfg.bindAddr)
	if cfg.urlTemplate != "" {
		return strings.ReplaceAll(cfg.urlTemplate, "{port}", strconv.Itoa(port))
	}
	host, _, err := net.SplitHostPort(cfg.addr)
	if err != nil {
		host = cfg.addr
	}
	return fmt.Sprintf("tcp://%s:%d", host, port)
}

// boundPort returns the remote port the forward was assigned: the listener's
// resolved port when available (x/crypto/ssh fills it in for a numeric :0 bind),
// else the requested bind port.
func boundPort(ln net.Listener, bindAddr string) int {
	if ln != nil {
		if _, p, err := net.SplitHostPort(ln.Addr().String()); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n != 0 {
				return n
			}
		}
	}
	if _, p, err := net.SplitHostPort(bindAddr); err == nil {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	return 0
}

// readSessionURL opens an SSH session and scans its output for the first http(s)
// URL the tunnel service prints. Best-effort and time-bounded; returns "" on any
// failure so the caller can fall back.
func readSessionURL(client *ssh.Client, timeout time.Duration) string {
	sess, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return ""
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return ""
	}
	if err := sess.Shell(); err != nil {
		return ""
	}
	found := make(chan string, 1)
	scan := func(r *bufio.Scanner) {
		for r.Scan() {
			if u := firstURL(r.Text()); u != "" {
				select {
				case found <- u:
				default:
				}
				return
			}
		}
	}
	go scan(bufio.NewScanner(stdout))
	go scan(bufio.NewScanner(stderr))
	select {
	case u := <-found:
		return u
	case <-time.After(timeout):
		return ""
	}
}

// firstURL returns the first whitespace-delimited token that looks like an
// http(s) URL, stripped of trailing punctuation.
func firstURL(line string) string {
	for _, tok := range strings.Fields(line) {
		if strings.HasPrefix(tok, "http://") || strings.HasPrefix(tok, "https://") {
			return strings.TrimRight(tok, ".,)\"'")
		}
	}
	return ""
}
