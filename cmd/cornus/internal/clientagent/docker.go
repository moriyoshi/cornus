package clientagent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"cornus/pkg/api"
	"cornus/pkg/dockerproxy"
	"cornus/pkg/supervisor"
)

// dockerFrontend is one Docker Engine API socket the agent serves, backed by a
// shared per-server connection + conduit.
type dockerFrontend struct {
	socket string
	ln     net.Listener
	srv    *http.Server
	proxy  *dockerproxy.Proxy
	conn   *connState
	egKey  conduitKey
	token  *supervisor.Token
}

// doDockerServe binds a Docker API socket backed by the shared server connection
// and conduit, so `docker` / `docker compose` against that socket drive the
// remote cornus server. Idempotent per socket path.
func (a *Agent) doDockerServe(req Request) Response {
	if req.Socket == "" {
		return Response{OK: false, Error: "docker-serve: missing socket"}
	}
	a.beginRequest()
	defer a.endRequest()
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.dockers[req.Socket]; ok {
		// Loud rather than a silent "OK" that ignores the new flags — matching the
		// old standalone proxy's bind-conflict error.
		return Response{OK: false, Error: fmt.Sprintf("a docker frontend is already serving %s (stop it with `cornus daemon stop`, or use a different --socket)", req.Socket)}
	}
	cs, err := a.ensureConnLocked(req.Conn)
	if err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	es, err := a.ensureConduitLocked(cs, req.Conduit.Runtime(), req.Socket)
	if err != nil {
		a.releaseConnLocked(cs)
		return Response{OK: false, Error: err.Error()}
	}
	egKey := conduitKeyOf(req.Conduit.Runtime(), req.Socket)
	fail := func(err error) Response {
		a.releaseConduitLocked(cs, egKey)
		a.releaseConnLocked(cs)
		return Response{OK: false, Error: err.Error()}
	}

	if err := os.MkdirAll(filepath.Dir(req.Socket), 0o755); err != nil {
		return fail(err)
	}
	if err := os.Remove(req.Socket); err != nil && !os.IsNotExist(err) {
		return fail(err)
	}
	ln, err := net.Listen("unix", req.Socket)
	if err != nil {
		return fail(err)
	}

	var opts []dockerproxy.Option
	if req.NoForwardPorts {
		opts = append(opts, dockerproxy.WithoutPortForwards())
	} else {
		// Published ports go through the shared per-server conduit (bound listeners
		// in port-forward mode, or the shared SOCKS5 proxy spanning docker+compose).
		opts = append(opts, dockerproxy.WithConduit(func(ctx context.Context, name string, ports []api.PortMapping) error {
			_, e := es.eg.Add(ctx, name, ports)
			return e
		}))
	}
	proxy := dockerproxy.New(cs.client, opts...)
	srv := &http.Server{Handler: proxy.Handler()}
	sock := req.Socket
	fe := &dockerFrontend{socket: sock, ln: ln, srv: srv, proxy: proxy, conn: cs, egKey: egKey}
	fe.token = a.sup.Add("docker:"+sock, supervisor.ServiceFunc(func(ctx context.Context) error {
		go func() { <-ctx.Done(); _ = srv.Close() }()
		err := srv.Serve(ln)
		if ctx.Err() == nil {
			// Unexpected exit (not a docker-stop / shutdown, which cancel ctx): reap
			// the frontend so its shared conn/conduit refs and socket are released
			// rather than orphaned (the RemoveOnExit child is already forgotten).
			go a.reapDocker(sock)
		}
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}), supervisor.RemoveOnExit)
	a.dockers[sock] = fe
	return Response{OK: true, Banners: es.eg.Banner()}
}

// reapDocker releases a docker frontend whose http.Server exited on its own,
// so its shared conn/conduit refs and socket do not leak. The supervisor has
// already forgotten the (RemoveOnExit) child, so this must not call Remove.
func (a *Agent) reapDocker(socket string) {
	a.mu.Lock()
	fe := a.dockers[socket]
	if fe == nil {
		a.mu.Unlock()
		return
	}
	delete(a.dockers, socket)
	a.releaseConduitLocked(fe.conn, fe.egKey)
	a.releaseConnLocked(fe.conn)
	tok := fe.token
	a.mu.Unlock()
	fe.proxy.Close() // stop any held container deploy-attach sessions
	// Remove the (already self-forgotten) child so its ctx is cancelled and the
	// srv.Close watcher goroutine unparks — safe: t.done is closed, forget is a no-op.
	a.sup.Remove(tok)
	_ = fe.ln.Close()
	_ = os.Remove(fe.socket)
	a.armIdle() // the agent may now be idle
}

// doDockerStop stops the docker frontend on the given socket: it tears down every
// container session, closes the http server + listener, removes the socket, and
// releases the shared conn/conduit refs.
func (a *Agent) doDockerStop(req Request) Response {
	a.mu.Lock()
	fe := a.dockers[req.Socket]
	if fe == nil {
		a.mu.Unlock()
		return Response{OK: true} // nothing serving this socket
	}
	delete(a.dockers, req.Socket)
	a.releaseConduitLocked(fe.conn, fe.egKey)
	a.releaseConnLocked(fe.conn)
	a.mu.Unlock()

	fe.proxy.Close()       // stop every container's deploy-attach session
	a.sup.Remove(fe.token) // cancel -> srv.Close -> Serve returns -> child drained
	_ = os.Remove(fe.socket)
	return Response{OK: true}
}
