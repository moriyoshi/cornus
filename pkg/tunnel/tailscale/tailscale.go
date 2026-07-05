// Package tailscale implements the cornus tunnel.UpstreamProvider on Tailscale
// Funnel via the `tailscale` binary. Like the cloudflare backend, Funnel proxies
// public edge traffic to a local upstream we provide, so the cornus server stands
// up a shim listener (bridging to the workload) and points Funnel at it.
//
// This backend adds NO compile-time dependency — it shells out to `tailscale`,
// which must be installed at runtime on a node already joined to a tailnet with
// Funnel enabled (`tailscale up` + the funnel node attribute in the tailnet ACL
// policy). That out-of-band join is why Funnel needs no cornus-injected
// credential (CredentialOptional). The subprocess route is deliberate: adding the
// `tailscale.com` module to go.mod would force k8s.io/* up across the whole module
// graph (Go MVS is build-tag-agnostic), which cornus pins — see the public-tunnels
// LTM note. A CLI subprocess sidesteps that entirely.
//
// `tailscale funnel <port>` runs in the foreground, proxies https://<node>.ts.net/
// to http://127.0.0.1:<port>, prints the public URL, and tears the funnel config
// down when the process is killed — a clean fit for the subprocess lifecycle
// (Close kills the process). Only one funnel serves the node's HTTPS port (443) at
// a time, so concurrent tunnels on one node conflict; that is a documented Funnel
// limitation, not a cornus bug.
package tailscale

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"cornus/pkg/tunnel"
)

func init() {
	tunnel.Register("tailscale", func() (any, error) { return provider{}, nil })
}

type provider struct{}

// CredentialOptional reports true: the node joins the tailnet out-of-band
// (`tailscale up`), so cornus injects no per-tunnel credential.
func (provider) CredentialOptional() bool { return true }

// urlWait bounds how long we wait for `tailscale funnel` to report its public URL.
const urlWait = 30 * time.Second

func (provider) StartUpstream(ctx context.Context, _ tunnel.Credential, _ tunnel.Options, upstreamURL string) (tunnel.UpstreamSession, error) {
	bin := os.Getenv("CORNUS_TUNNEL_TAILSCALE_BIN")
	if bin == "" {
		bin = "tailscale"
	}
	target, err := funnelTarget(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("tailscale: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, "funnel", target)
	// `tailscale funnel` prints its "Available on the internet" block to stdout;
	// merge stderr in too so a diagnostic there is not lost and neither pipe can
	// fill and block the child.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, fmt.Errorf("tailscale: start %s: %w", bin, err)
	}
	// The parent's write end must be closed so the reader sees EOF once the child
	// exits; only the child then holds it.
	pw.Close()

	pub, err := scanForFunnelURL(pr, urlWait)
	if err != nil {
		_ = kill(cmd)
		pr.Close()
		return nil, fmt.Errorf("tailscale: %w", err)
	}
	return &session{cmd: cmd, url: pub, out: pr}, nil
}

type session struct {
	cmd *exec.Cmd
	url string
	out io.Closer
}

func (s *session) URL() string { return s.url }

func (s *session) Close() error {
	err := kill(s.cmd)
	if s.out != nil {
		s.out.Close()
	}
	return err
}

func kill(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	return nil
}

// funnelTarget derives the argument for `tailscale funnel` from the shim's
// upstream URL (e.g. "http://127.0.0.1:54321"). Funnel's shorthand takes a bare
// local port; we fall back to the full URL if no port is present.
func funnelTarget(upstreamURL string) (string, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return "", fmt.Errorf("parse upstream URL %q: %w", upstreamURL, err)
	}
	if p := u.Port(); p != "" {
		return p, nil
	}
	if upstreamURL == "" {
		return "", fmt.Errorf("empty upstream URL")
	}
	return upstreamURL, nil
}

// scanForFunnelURL reads `tailscale funnel` output until it prints a public
// *.ts.net URL, or the timeout elapses. After the URL is found it keeps draining r
// in the background so the child never blocks on a full pipe.
func scanForFunnelURL(r io.Reader, timeout time.Duration) (string, error) {
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sent := false
		for sc.Scan() {
			if !sent {
				if u := parseFunnelURL(sc.Text()); u != "" {
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
			return "", fmt.Errorf("tailscale funnel exited before printing a public URL")
		}
		return u, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("tailscale funnel did not print a public URL within %s", timeout)
	}
}

// parseFunnelURL extracts a https://<node>.ts.net URL from a `tailscale funnel`
// output line, if present.
func parseFunnelURL(line string) string {
	for _, tok := range strings.Fields(line) {
		tok = strings.Trim(tok, "|+`'\"")
		if strings.HasPrefix(tok, "https://") && strings.Contains(tok, ".ts.net") {
			return strings.TrimRight(tok, "/.,")
		}
	}
	return ""
}
