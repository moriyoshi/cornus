package caretaker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/sync/errgroup"

	"cornus/pkg/client"
	"cornus/pkg/dockerproxy"
	"cornus/pkg/logging"
)

// DockerRole runs a Docker Engine API proxy (pkg/dockerproxy) on a pod-loopback
// endpoint, so the app container can point `docker` / `docker compose` at it and
// drive the very cornus server that manages its own stack — loopback access to the
// cornus-managed stack, no real Docker daemon in the pod.
//
// Unlike the server-bound roles (mount / credential / hub / egress), which dial the
// scoped /.cornus/v1/caretaker/attach endpoint, the Docker proxy drives the cornus CLIENT
// API (/.cornus/v1/deploy/attach, /.cornus/v1/deploy/*). The caretaker's attach-scoped token is
// rejected there by design, so this role carries its OWN client-scoped Token (the
// kubernetes backend sources it from a dedicated Secret, distinct from the caretaker
// token). The token may instead arrive out of band in CORNUS_DOCKER_CLIENT_TOKEN
// (from a Secret secretKeyRef, so it is not a literal in the pod spec), which takes
// precedence — mirroring how the server token arrives via CORNUS_TOKEN.
//
// At least one of TCPAddr / UnixPath must be set. TCPAddr binds a loopback TCP
// listener (e.g. "127.0.0.1:2375") reachable by the app container over the shared
// pod netns; UnixPath binds a unix socket on a volume shared with the app container.
type DockerRole struct {
	// Server is the cornus CLIENT API base URL the proxy drives (the advertised
	// cornus URL, same origin the hub role uses).
	Server string `json:"server"`
	// Token is the client-scoped bearer token the proxy authenticates with. Empty
	// means no auth (an unauthenticated server). CORNUS_DOCKER_CLIENT_TOKEN, when
	// set, overrides it.
	Token string `json:"token,omitempty"`
	// TCPAddr, when set, is the loopback "host:port" the proxy binds for a
	// tcp:// DOCKER_HOST (e.g. "127.0.0.1:2375").
	TCPAddr string `json:"tcpAddr,omitempty"`
	// UnixPath, when set, is the filesystem path of the unix socket the proxy binds
	// for a unix:// DOCKER_HOST (on a volume the app container also mounts).
	UnixPath string `json:"unixPath,omitempty"`
}

// dockerClientToken resolves the effective client token: the Secret-projected
// CORNUS_DOCKER_CLIENT_TOKEN env wins (so the token is not a pod-spec literal),
// falling back to the value embedded in the config JSON.
func (d DockerRole) dockerClientToken() string {
	if t := os.Getenv("CORNUS_DOCKER_CLIENT_TOKEN"); t != "" {
		return t
	}
	return d.Token
}

// runDocker serves the Docker Engine API proxy on the role's loopback endpoint(s)
// until ctx is cancelled. tc is the pod-wide dial TLS config (nil for a plain
// dial) — the same one the server-bound roles use, since the client API is the
// same origin.
func runDocker(ctx context.Context, d DockerRole, tc *tls.Config) error {
	opts := []client.Option{client.WithToken(d.dockerClientToken())}
	if tc != nil {
		opts = append(opts, client.WithTLSConfig(tc))
	}
	// WithoutPortForwards: a sidecar must not bind published-port listeners on the
	// pod loopback — nested deployments are reachable as ordinary cornus services.
	px := dockerproxy.New(client.New(d.Server, opts...), dockerproxy.WithoutPortForwards())
	srv := &http.Server{Handler: px.Handler()}

	var lns []net.Listener
	if d.TCPAddr != "" {
		ln, err := net.Listen("tcp", d.TCPAddr)
		if err != nil {
			return fmt.Errorf("caretaker docker: listen tcp %s: %w", d.TCPAddr, err)
		}
		lns = append(lns, ln)
	}
	if d.UnixPath != "" {
		_ = os.Remove(d.UnixPath) // clear a stale socket from a prior run
		ln, err := net.Listen("unix", d.UnixPath)
		if err != nil {
			return fmt.Errorf("caretaker docker: listen unix %s: %w", d.UnixPath, err)
		}
		lns = append(lns, ln)
	}
	if len(lns) == 0 {
		return fmt.Errorf("caretaker docker: no endpoint configured (need tcpAddr or unixPath)")
	}

	logging.FromContext(ctx).InfoContext(ctx, "caretaker docker endpoint listening", "tcp", d.TCPAddr, "unix", d.UnixPath, "server", d.Server)
	g, gctx := errgroup.WithContext(ctx)
	for _, ln := range lns {
		ln := ln
		g.Go(func() error {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		px.Close()
		return nil
	})
	return g.Wait()
}

// dockerReady reports whether the docker endpoint's listener(s) are bound, for the
// readiness probe (a separate `cornus caretaker-check` process, so it observes only
// cross-process-visible state: a dialable socket).
func dockerReady(d DockerRole) error {
	for proto, addr := range map[string]string{"tcp": d.TCPAddr, "unix": d.UnixPath} {
		if addr == "" {
			continue
		}
		c, err := net.DialTimeout(proto, addr, 500*time.Millisecond)
		if err != nil {
			return fmt.Errorf("docker endpoint %s not live: %w", addr, err)
		}
		c.Close()
	}
	return nil
}
