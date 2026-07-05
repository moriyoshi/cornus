// Package cloudflare implements the cornus tunnel.UpstreamProvider on Cloudflare
// Tunnel via the `cloudflared` binary. Unlike the listener-model backends,
// cloudflared proxies edge traffic to a local upstream we provide, so the cornus
// server stands up a shim listener (bridging to the workload) and points
// cloudflared at it. This backend adds no compile-time dependency — it shells out
// to `cloudflared`, which must be installed at runtime.
//
// v1 uses Cloudflare "quick tunnels" (`cloudflared tunnel --url …`), which are
// anonymous and print an ephemeral https://<random>.trycloudflare.com URL — a
// good fit for the ad-hoc dev/test use case. Named tunnels (stable hostnames via
// a token) are a follow-up: their ingress is configured out-of-band and does not
// compose with the dynamic per-tunnel upstream shim.
package cloudflare

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"cornus/pkg/tunnel"
)

func init() {
	tunnel.Register("cloudflare", func() (any, error) { return provider{}, nil })
}

type provider struct{}

// CredentialOptional reports true: quick tunnels need no injected credential.
func (provider) CredentialOptional() bool { return true }

// urlWait bounds how long we wait for cloudflared to report its public URL.
const urlWait = 30 * time.Second

func (provider) StartUpstream(ctx context.Context, _ tunnel.Credential, _ tunnel.Options, upstreamURL string) (tunnel.UpstreamSession, error) {
	bin := os.Getenv("CORNUS_TUNNEL_CLOUDFLARED_BIN")
	if bin == "" {
		bin = "cloudflared"
	}
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "--url", upstreamURL)
	// cloudflared prints the quick-tunnel URL (and its logs) to stderr.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cloudflare: start %s: %w", bin, err)
	}

	url, err := scanForTunnelURL(stderr, urlWait)
	if err != nil {
		_ = kill(cmd)
		return nil, fmt.Errorf("cloudflare: %w", err)
	}
	return &session{cmd: cmd, url: url}, nil
}

type session struct {
	cmd *exec.Cmd
	url string
}

func (s *session) URL() string { return s.url }

func (s *session) Close() error {
	return kill(s.cmd)
}

func kill(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	return nil
}

// scanForTunnelURL reads cloudflared's output until it prints a quick-tunnel URL,
// or the timeout elapses. After the URL is found it keeps draining r in the
// background so cloudflared never blocks on a full stderr pipe.
func scanForTunnelURL(r io.Reader, timeout time.Duration) (string, error) {
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sent := false
		for sc.Scan() {
			if !sent {
				if u := parseQuickTunnelURL(sc.Text()); u != "" {
					found <- u
					sent = true
				}
			}
			// keep draining after the URL is found
		}
		if !sent {
			close(found)
		}
	}()
	select {
	case u, ok := <-found:
		if !ok || u == "" {
			return "", fmt.Errorf("cloudflared exited before printing a tunnel URL")
		}
		return u, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("cloudflared did not print a tunnel URL within %s", timeout)
	}
}

// parseQuickTunnelURL extracts a https://<name>.trycloudflare.com URL from a
// cloudflared log line, if present.
func parseQuickTunnelURL(line string) string {
	for _, tok := range strings.Fields(line) {
		tok = strings.Trim(tok, "|+`'\"")
		if strings.HasPrefix(tok, "https://") && strings.Contains(tok, ".trycloudflare.com") {
			return strings.TrimRight(tok, "/.,")
		}
	}
	return ""
}
