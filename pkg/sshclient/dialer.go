package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sync/singleflight"
)

// defaultDialTimeout bounds the TCP dial + SSH handshake when Options.Timeout is 0.
const defaultDialTimeout = 15 * time.Second

// maxReconnectBackoff caps the exponential backoff between reconnect attempts.
const maxReconnectBackoff = 15 * time.Second

// Hop is one resolved host in a ProxyJump chain: the coordinates and credentials
// needed to open (or tunnel through) an SSH connection to it.
type Hop struct {
	Addr          string // host:port
	User          string
	IdentityFiles []string
	KnownHosts    string
	HostKey       string
	Insecure      bool
}

// Options is the resolved SSH dial specification. Resolve produces one from a
// profile layered over ssh_config; Dial consumes it. The zero PromptPassphrase and
// an empty ProxyJump mean "no interactive unlock" and "direct connection".
type Options struct {
	Addr          string // target host:port (post ssh_config resolution)
	User          string
	IdentityFiles []string
	KnownHosts    string
	HostKey       string
	Insecure      bool
	NoAgent       bool
	Timeout       time.Duration
	// PromptPassphrase, when non-nil, unlocks a passphrase-protected identity file on
	// the FIRST connect only. It is never used on a reconnect.
	PromptPassphrase func(keyPath string) ([]byte, error)
	// ProxyJump is an ordered jump chain (outermost first); empty means direct.
	ProxyJump []Hop
	// ProxyCommand, when non-empty, means the pure-Go Dialer cannot faithfully honor
	// this host (it needs the ssh-binary fallback). Dial rejects it; clientconn reads
	// it to choose the transport.
	ProxyCommand string
}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return defaultDialTimeout
}

// Dialer holds a live (reconnecting) *ssh.Client and hands out direct-tcpip
// net.Conns to any address, resolved in the SSH server's network namespace.
type Dialer struct {
	o     Options
	group singleflight.Group

	mu      sync.Mutex
	client  *ssh.Client
	gen     uint64
	backoff time.Duration
	closed  bool
}

// Dial establishes the first SSH connection eagerly (so a misconfigured profile,
// bad host key, or wrong passphrase fails fast at command start) and returns a
// reconnecting Dialer. ctx bounds the initial dial + handshake only; the live
// connection is owned by Close, not ctx.
func Dial(ctx context.Context, o Options) (*Dialer, error) {
	if o.ProxyCommand != "" {
		return nil, fmt.Errorf("ssh: host uses ProxyCommand, which the pure-Go tunnel cannot honor; use the ssh-binary fallback")
	}
	if o.Addr == "" {
		return nil, errors.New("ssh: no destination address")
	}
	d := &Dialer{o: o}
	client, err := d.connect(ctx, true)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.client = client
	d.gen++
	gen := d.gen
	d.mu.Unlock()
	d.watch(client, gen)
	return d, nil
}

// DialContext returns a net.Conn to addr over the SSH connection, re-establishing
// the connection first if it has dropped. network/addr are what the HTTP transport
// asks for; addr is dialed in the SSH server's namespace. The returned conn is
// wrapped so a fatal I/O error primes a reconnect for the next dial.
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	client, gen, err := d.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := client.DialContext(ctx, network, addr)
	if err != nil {
		// The client may have died between ensure and dial; rebuild once and retry.
		d.markDead(gen)
		client, gen, err = d.ensureClient(ctx)
		if err != nil {
			return nil, err
		}
		conn, err = client.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
	}
	return &resilientConn{Conn: conn, d: d, gen: gen}, nil
}

// Close stops reconnecting and closes the current connection. Idempotent.
func (d *Dialer) Close() error {
	d.mu.Lock()
	d.closed = true
	client := d.client
	d.client = nil
	d.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

// ensureClient returns a live client and its generation, reconnecting under a
// single-flight guard so concurrent DialContext calls coalesce onto one reconnect.
func (d *Dialer) ensureClient(ctx context.Context) (*ssh.Client, uint64, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, 0, errors.New("ssh: dialer closed")
	}
	if d.client != nil {
		client, gen := d.client, d.gen
		d.mu.Unlock()
		return client, gen, nil
	}
	backoff := d.backoff
	d.mu.Unlock()

	type result struct {
		client *ssh.Client
		gen    uint64
	}
	v, err, _ := d.group.Do("connect", func() (any, error) {
		// Re-check under the flight: another caller may have reconnected already.
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return nil, errors.New("ssh: dialer closed")
		}
		if d.client != nil {
			client, gen := d.client, d.gen
			d.mu.Unlock()
			return result{client, gen}, nil
		}
		d.mu.Unlock()

		if backoff > 0 {
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			}
		}
		client, err := d.connect(ctx, false)
		if err != nil {
			d.mu.Lock()
			d.backoff = nextBackoff(d.backoff)
			d.mu.Unlock()
			return nil, err
		}
		d.mu.Lock()
		d.backoff = 0
		d.client = client
		d.gen++
		gen := d.gen
		d.mu.Unlock()
		d.watch(client, gen)
		return result{client, gen}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	r := v.(result)
	return r.client, r.gen, nil
}

// markDead drops the current client if it is still the given generation, so the
// next ensureClient rebuilds. A stale generation (from an already-replaced client)
// is ignored, so a dying old conn never kills a freshly reconnected one.
func (d *Dialer) markDead(gen uint64) {
	d.mu.Lock()
	var client *ssh.Client
	if d.client != nil && d.gen == gen {
		client = d.client
		d.client = nil
	}
	d.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// watch blocks (in a goroutine) until the SSH connection closes for any reason,
// then marks it dead so the next dial rebuilds — the proactive liveness signal.
func (d *Dialer) watch(client *ssh.Client, gen uint64) {
	go func() {
		_ = client.Wait()
		d.markDead(gen)
	}()
}

// connect opens a fresh SSH connection, walking any ProxyJump chain. The local
// ssh-agent (when used) authenticates every hop and is closed once all handshakes
// complete. prompt is honored only on the first connect.
func (d *Dialer) connect(ctx context.Context, firstConnect bool) (*ssh.Client, error) {
	var ag agent.Agent
	if !d.o.NoAgent {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			conn, err := net.Dial("unix", sock)
			if err == nil {
				ag = agent.NewClient(conn)
				defer conn.Close()
			}
		}
	}
	prompt := d.o.PromptPassphrase
	if !firstConnect {
		prompt = nil // never block a background reconnect on an interactive prompt
	}

	timeout := d.o.timeout()

	// Build the ordered chain of hosts to traverse: the jump hosts, then the target.
	target := Hop{
		Addr: d.o.Addr, User: d.o.User, IdentityFiles: d.o.IdentityFiles,
		KnownHosts: d.o.KnownHosts, HostKey: d.o.HostKey, Insecure: d.o.Insecure,
	}
	chain := append(append([]Hop{}, d.o.ProxyJump...), target)

	var client *ssh.Client
	for i, hop := range chain {
		cfg, err := clientConfig(hop, ag, prompt, timeout)
		if err != nil {
			if client != nil {
				_ = client.Close()
			}
			return nil, err
		}
		if i == 0 {
			// First hop: dial the TCP endpoint directly.
			c, err := dialSSH(ctx, hop.Addr, cfg, timeout)
			if err != nil {
				return nil, err
			}
			client = c
			continue
		}
		// Subsequent hop: tunnel through the previous client.
		next, err := jumpThrough(ctx, client, hop.Addr, cfg)
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		client = next
	}
	return client, nil
}

// clientConfig builds the ssh.ClientConfig for one hop.
func clientConfig(hop Hop, ag agent.Agent, prompt func(string) ([]byte, error), timeout time.Duration) (*ssh.ClientConfig, error) {
	methods, err := AuthMethods(hop.IdentityFiles, ag, prompt)
	if err != nil {
		return nil, err
	}
	hkcb, err := HostKeyCallback(hop.KnownHosts, hop.HostKey, hop.Insecure)
	if err != nil {
		return nil, err
	}
	user := hop.User
	if user == "" {
		if u := os.Getenv("USER"); u != "" {
			user = u
		} else {
			user = "root"
		}
	}
	return &ssh.ClientConfig{
		User:            user,
		Auth:            methods,
		HostKeyCallback: hkcb,
		Timeout:         timeout,
	}, nil
}

// dialSSH dials a TCP endpoint and completes the SSH handshake, honoring ctx.
func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	d := net.Dialer{Timeout: timeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh: dial %s: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ssh: handshake with %s: %w", addr, err)
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// jumpThrough opens a direct-tcpip channel to addr over the previous client and
// completes an SSH handshake to the next hop over it.
func jumpThrough(ctx context.Context, via *ssh.Client, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	raw, err := via.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh: jump to %s: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ssh: handshake with %s: %w", addr, err)
	}
	return ssh.NewClient(c, chans, reqs), nil
}

func nextBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return 500 * time.Millisecond
	}
	next := cur * 2
	if next > maxReconnectBackoff {
		return maxReconnectBackoff
	}
	return next
}

// resilientConn wraps a tunneled net.Conn so a fatal read/write error signals the
// Dialer to prime a reconnect for the next dial. It is generation-guarded via
// markDead so a dying conn from an old client never kills a newer one.
type resilientConn struct {
	net.Conn
	d      *Dialer
	gen    uint64
	failed atomic.Bool
}

func (c *resilientConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.note(err)
	return n, err
}

func (c *resilientConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.note(err)
	return n, err
}

// note marks the parent dialer dead on an error that indicates the SSH transport
// is gone. io.EOF (a peer closing this one stream) and net.ErrClosed (the local
// side, e.g. http.Transport, closing an idle conn) are normal and skipped — the
// authoritative liveness signal remains the client.Wait() watch goroutine; this
// wrapper only speeds up detection for genuine transport failures.
func (c *resilientConn) note(err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return
	}
	if c.failed.CompareAndSwap(false, true) {
		c.d.markDead(c.gen)
	}
}
