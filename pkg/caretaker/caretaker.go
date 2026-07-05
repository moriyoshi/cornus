// Package caretaker is the runtime of cornus's single per-pod sidecar. It runs
// any combination of ROLES the pod needs, so a workload never carries more than
// one cornus sidecar container:
//
//   - mount: dial a cornus server's mount relay for a caller-local 9P export
//     and kernel-9p-mount it inside the pod (the former standalone mount-agent);
//     with Bidirectional propagation the mount reaches the app container.
//
// Future roles (a network proxy, a per-network DNS resolver) plug in here the
// same way. Config is delivered as one JSON blob (an env var) so the k8s backend
// can assemble every role a pod needs into ONE caretaker container. The mount
// primitives are linux-only (deploywire.Mount9P is build-tagged with a no-op
// stub elsewhere), so this package still cross-compiles.
//
// Failure model: each role is a supervised child (pkg/supervisor), isolated
// from its siblings — a mount hiccuping does not disturb an unrelated
// credential or egress role on the very same connection. A connection to one
// server is itself a supervised child too, redialed with backoff if it drops,
// independent of a pod's OTHER server connections and of the proxy/DNS/docker
// roles. Only a truly unrecoverable startup error (a malformed TLS config)
// still fails Run outright; everything else self-heals in place rather than
// exiting the process for Kubernetes (or a host-backend restart policy) to
// notice and restart.
package caretaker

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"cornus/pkg/deploywire"
	"cornus/pkg/hub"
	"cornus/pkg/logging"
	"cornus/pkg/supervisor"
	"cornus/pkg/wire"
)

// MountRole is one 9P mount the caretaker establishes and holds: it dials
// Server's relay for Session's export named Name and mounts it at Target inside
// the pod (read-only when ReadOnly).
type MountRole struct {
	Server     string `json:"server"`
	Session    string `json:"session"`
	Name       string `json:"name"`
	Target     string `json:"target"`
	ReadOnly   bool   `json:"readOnly,omitempty"`
	AsyncCache bool   `json:"asyncCache,omitempty"` // cache=mmap writable block-proxy mount
}

// CredentialRole is one client-sourced credential the caretaker serves into the
// pod: it fetches the value on demand from the caller by dialing Server's relay
// for Session's credential backing named Name, and surfaces it through each
// Deliver entry (an HTTP endpoint and/or a file). TTL bounds how long a fetched
// value is reused before re-minting ("" = the credential's own expiry, or a
// short default).
type CredentialRole struct {
	Server  string               `json:"server"`
	Session string               `json:"session"`
	Name    string               `json:"name"`
	TTL     string               `json:"ttl,omitempty"`
	Deliver []CredentialDelivery `json:"deliver,omitempty"`
}

// CredentialDelivery is one resolved way the caretaker surfaces a credential.
// The kubernetes backend resolves the spec's provider-agnostic delivery into
// this concrete form (in particular the bind Addr for an endpoint), so the
// caretaker just binds and serves.
type CredentialDelivery struct {
	Kind string `json:"kind"` // "endpoint" | "file"
	// endpoint:
	Provider  string `json:"provider,omitempty"` // creddelivery provider ("generic", "aws-imds", ...)
	Addr      string `json:"addr,omitempty"`     // resolved bind "host:port"
	WellKnown bool   `json:"wellKnown,omitempty"`
	Upstream  string `json:"upstream,omitempty"` // auth-proxy providers: override the vendor API / gateway
	// file:
	Path   string `json:"path,omitempty"`
	Format string `json:"format,omitempty"`
}

// Config is the caretaker's full instruction set, one per pod.
type Config struct {
	// Instance, when set, identifies which app instance (deployment name +
	// replica) this caretaker is the companion for — e.g. "web/0". It rides
	// the attach dial as a query parameter so the server can register this
	// connection in its per-instance companion registry (see PortForward /
	// AgentRelay below, both of which are looked up by instance). Roles that
	// are session-scoped (Mounts, Credentials) don't need it.
	Instance    string           `json:"instance,omitempty"`
	Mounts      []MountRole      `json:"mounts,omitempty"`
	Credentials []CredentialRole `json:"credentials,omitempty"`
	Proxy       *ProxyRole       `json:"proxy,omitempty"`
	DNS         *DNSRole         `json:"dns,omitempty"`
	Hub         *HubRole         `json:"hub,omitempty"`
	// Egress, when set, routes the app container's outbound connections through a
	// client-side vantage point via the pod-scoped caretaker connection (see
	// EgressRole). It is server-bound (rides the same connection as mount/hub).
	Egress *EgressRole `json:"egress,omitempty"`
	// PortForward, when set, lets the server reach ports inside this instance
	// through the caretaker (which shares the instance's network namespace) —
	// used to reroute cornus port-forward/tunnel for a remote-mode dockerhost/
	// containerdhost companion, whose own ForwardPort can no longer dial the
	// instance directly. See PortForwardRole.
	PortForward *PortForwardRole `json:"portForward,omitempty"`
	// AgentRelay, when set, relays ssh-agent-forwarding connections from inside
	// this instance to whichever `cornus exec --forward-agent` session
	// currently holds the real local agent for it. See AgentRelayRole.
	AgentRelay *AgentRelayRole `json:"agentRelay,omitempty"`
	// Docker, when set, runs a Docker Engine API proxy on a pod-loopback endpoint
	// (see DockerRole) so the app container can drive the cornus server that manages
	// its own stack. Unlike the server-bound roles above it dials the cornus CLIENT
	// API, so it carries its own client-scoped token, independent of Token.
	Docker *DockerRole `json:"docker,omitempty"`
	// Otel, when set, runs an embedded OpenTelemetry Collector as a self-contained
	// role: an OTLP receiver on pod-loopback that exports the workload's telemetry
	// to an external backend. Like proxy/dns/docker it does NOT dial the cornus
	// server. See OtelRole.
	Otel *OtelRole `json:"otel,omitempty"`
	// Mark, when non-zero, is the SO_MARK the caretaker stamps on every socket it
	// opens (mount relay dials and proxy upstream dials). It is set only for the
	// enforcing-proxy-with-mounts case, where the caretaker runs as root and so
	// cannot be exempted from the egress redirect by uid; the net-redirect rules
	// exempt this firewall mark instead.
	Mark int `json:"mark,omitempty"`
	// Token, when set, is the bearer token the caretaker sends on its handshake to
	// the cornus server (Authorization: Bearer). It is required only when the
	// server has bearer authentication enabled; the kubernetes backend injects it
	// into server-bound sidecars (mount / hub roles). It may instead arrive out of
	// band in the CORNUS_TOKEN env var (sourced from a Kubernetes Secret via
	// secretKeyRef, so the token never appears in the pod spec as a literal), which
	// takes precedence — see applyEnvToken. Empty means no auth (the default).
	Token string `json:"token,omitempty"`
	// TLSClientConfig, when non-nil, is the client TLS configuration applied to
	// every server-bound WebSocket dial (custom CA roots, an mTLS client
	// certificate, or InsecureSkipVerify — whatever a connection profile
	// supplies). It is process-local and deliberately NOT serialized: the config
	// otherwise travels as one JSON blob in a pod-spec env var, where a live
	// *tls.Config cannot ride. In-process callers (the `cornus hub` CLI) set it
	// directly; the sidecar path uses the file-based TLS field instead. It takes
	// precedence over TLS when both are set. nil dials exactly as before (plain
	// ws://, or wss:// against the system trust store).
	TLSClientConfig *tls.Config `json:"-"`
	// TLS is the serializable (sidecar-path) TLS hook: PEM file paths the
	// caretaker loads at startup to build the dial TLS config, so TLS material
	// can be projected into the pod (e.g. from a Secret) and referenced from the
	// config JSON. A load failure is a startup error (fail fast). Ignored when
	// TLSClientConfig is set.
	TLS *TLSFiles `json:"tls,omitempty"`
}

// TLSFiles is the file-based form of the caretaker's dial TLS configuration —
// the shape that can ride the JSON config blob in a pod spec. CAFile, when set,
// is a PEM CA bundle ADDED to the system roots (so a server cert signed by
// either verifies). CertFile/KeyFile, when set (both required together), are a
// PEM client certificate and key presented for mTLS.
type TLSFiles struct {
	CAFile   string `json:"caFile,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
}

// load builds a *tls.Config from the file paths. An empty TLSFiles yields nil
// (dial exactly as with no TLS hook at all).
func (f *TLSFiles) load() (*tls.Config, error) {
	tc := &tls.Config{}
	if f.CAFile != "" {
		pem, err := os.ReadFile(f.CAFile)
		if err != nil {
			return nil, fmt.Errorf("caretaker: tls ca: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("caretaker: tls ca %s: no certificates found", f.CAFile)
		}
		tc.RootCAs = pool
	}
	if f.CertFile != "" || f.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(f.CertFile, f.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("caretaker: tls client cert: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	if tc.RootCAs == nil && len(tc.Certificates) == 0 {
		return nil, nil
	}
	return tc, nil
}

// tlsClientConfig resolves the effective dial TLS config: the in-process
// TLSClientConfig wins; else the file-based TLS fields are loaded; nil when
// neither is configured (the historical non-TLS dial, byte-identical).
func (cfg Config) tlsClientConfig() (*tls.Config, error) {
	if cfg.TLSClientConfig != nil {
		return cfg.TLSClientConfig, nil
	}
	if cfg.TLS == nil {
		return nil, nil
	}
	return cfg.TLS.load()
}

// applyEnvToken overlays a secret-sourced bearer token (CORNUS_TOKEN) onto cfg,
// taking precedence over any token embedded in the config JSON. The kubernetes
// backend prefers this path (a Secret secretKeyRef) so the token is not a literal
// in the pod spec; the embedded Config.Token is the backward-compatible fallback.
func applyEnvToken(cfg *Config) {
	if t := os.Getenv("CORNUS_TOKEN"); t != "" {
		cfg.Token = t
	}
}

// Run starts every configured role and blocks until ctx is cancelled (pod
// teardown), then unwinds them. Each server connection and each of
// proxy/DNS/docker is a supervised child (supervisor.Restart): a runtime
// failure retries that one child in place, with capped exponential backoff,
// rather than tearing down the others or exiting the process. Only a
// startup-time configuration error (a malformed TLS config, checked before any
// child is registered) fails Run outright.
func Run(ctx context.Context, cfg Config) error {
	applyEnvToken(&cfg)
	tc, err := cfg.tlsClientConfig()
	if err != nil {
		return err
	}
	logging.FromContext(ctx).InfoContext(ctx, "caretaker: starting",
		"instance", cfg.Instance, "mounts", len(cfg.Mounts), "credentials", len(cfg.Credentials),
		"proxy", cfg.Proxy != nil, "dns", cfg.DNS != nil, "hub", cfg.Hub != nil,
		"egress", cfg.Egress != nil, "docker", cfg.Docker != nil,
		"portForward", cfg.PortForward != nil, "agentRelay", cfg.AgentRelay != nil,
		"otel", cfg.Otel != nil)
	sup := supervisor.New(ctx, nil)
	for _, sb := range groupByServer(cfg) {
		sb := sb
		sb.tlsConf = tc // pod-wide, like the token: every server dial uses it
		sup.Add("conn:"+sb.server, supervisor.ServiceFunc(func(ctx context.Context) error {
			return runCaretakerConn(ctx, sb, cfg.Mark)
		}), supervisor.Restart)
	}
	if cfg.Proxy != nil {
		p := *cfg.Proxy
		sup.AddSystem("proxy", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runProxy(ctx, p, cfg.Mark)
		}), supervisor.Restart)
	}
	if cfg.DNS != nil {
		d := *cfg.DNS
		sup.AddSystem("dns", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runDNS(ctx, d)
		}), supervisor.Restart)
	}
	if cfg.Docker != nil {
		d := *cfg.Docker
		sup.AddSystem("docker", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runDocker(ctx, d, tc)
		}), supervisor.Restart)
	}
	if cfg.Otel != nil {
		o := *cfg.Otel
		sup.AddSystem("otel", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runOtel(ctx, o)
		}), supervisor.Restart)
	}
	<-ctx.Done()
	sup.Wait()
	return ctx.Err()
}

// serverBundle is everything one pod-scoped caretaker connection carries for a
// single cornus server: its mount roles and (when present) the hub role. Mounts
// and hub normally target the same server (the advertised cornus URL), so a pod
// holds one unified connection.
type serverBundle struct {
	server      string
	mounts      []MountRole
	creds       []CredentialRole
	hub         *HubRole
	egress      *EgressRole
	portForward *PortForwardRole
	agentRelay  *AgentRelayRole
	instance    string      // pod-wide instance identity, see Config.Instance
	token       string      // bearer token for the server handshake; "" = no auth
	tlsConf     *tls.Config // client TLS for the WebSocket dial; nil = the plain historical dial
}

// groupByServer buckets the mount roles and hub role by server URL, preserving
// first-seen order, so each distinct server gets one pod-scoped connection.
func groupByServer(cfg Config) []serverBundle {
	var order []string
	byServer := map[string]*serverBundle{}
	get := func(server string) *serverBundle {
		b := byServer[server]
		if b == nil {
			b = &serverBundle{server: server}
			byServer[server] = b
			order = append(order, server)
		}
		return b
	}
	for _, m := range cfg.Mounts {
		b := get(m.Server)
		b.mounts = append(b.mounts, m)
	}
	for _, c := range cfg.Credentials {
		b := get(c.Server)
		b.creds = append(b.creds, c)
	}
	if cfg.Hub != nil {
		get(cfg.Hub.Server).hub = cfg.Hub
	}
	if cfg.Egress != nil {
		get(cfg.Egress.Server).egress = cfg.Egress
	}
	if cfg.PortForward != nil {
		get(cfg.PortForward.Server).portForward = cfg.PortForward
	}
	if cfg.AgentRelay != nil {
		get(cfg.AgentRelay.Server).agentRelay = cfg.AgentRelay
	}
	out := make([]serverBundle, 0, len(order))
	for _, s := range order {
		sb := *byServer[s]
		sb.token = cfg.Token // pod-wide; the same server token authenticates every bundle
		sb.instance = cfg.Instance
		out = append(out, sb)
	}
	return out
}

// runCaretakerConn dials ONE pod-scoped, always-on connection to a cornus
// server's unified caretaker endpoint and runs every server-bound role over it: a
// control stream (hub registration when a hub role is present), one 'M' stream per
// mount (each carrying its deploy-attach session + name, so the connection is not
// tied to any single session), hub egress ('D') from the reach listeners, and hub
// ingress ('I') delivery. Each role is its own supervised child of a
// connection-scoped supervisor: a role that errors is retried in place — it
// reopens its own tagged stream over this SAME session — without disturbing its
// siblings. Only the session itself dying (sess.CloseChan) ends
// runCaretakerConn, so the caller's supervisor (Run) redials the whole
// connection; ordinary ctx cancellation (pod teardown) unwinds cleanly with no
// restart.
func runCaretakerConn(ctx context.Context, sb serverBundle, mark int) error {
	ctx, span := tracer().Start(ctx, "caretaker.conn", trace.WithAttributes(
		attribute.String("caretaker.conn.server", sb.server),
		attribute.Int("caretaker.conn.mounts", len(sb.mounts)),
		attribute.Bool("caretaker.conn.hub", sb.hub != nil),
	))
	defer span.End()
	fail := func(err error) error {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	// Carry the conn span's trace context to the server so its request span links
	// back (end-to-end trace); one handshake per pod. When the server has bearer
	// auth enabled, add the Authorization header so the handshake is accepted.
	header := propagationHeader(ctx)
	if sb.token != "" {
		header.Set("Authorization", "Bearer "+sb.token)
	}
	// A nil sb.tlsConf dials exactly as the historical wire.DialControlHeader
	// (which is DialControlHeaderTLS with a nil config).
	sess, err := wire.DialControlHeaderTLS(ctx, caretakerURL(sb.server, sb.instance), markControl(mark), header, sb.tlsConf)
	if err != nil {
		return fail(fmt.Errorf("caretaker: dial %s: %w", sb.server, err))
	}
	defer sess.Close()
	// Close sess as soon as ctx is done, not only once this function finally
	// returns past connSup.Wait() below: PortForwardRole's accept loop blocks
	// in sess.AcceptStream() with no owned stream/listener of its own to close
	// on shutdown (every other role either owns a stream it can close itself,
	// like a mount, or a local listener, like egress) — closing sess is the
	// only way to unblock it. Close is idempotent, so this races harmlessly
	// with the plain defer above and with the sess.CloseChan() branch below.
	go func() {
		<-ctx.Done()
		sess.Close()
	}()

	// Control stream: hub registration when a hub role is present; otherwise held
	// open (reserved) so an unreachable endpoint fails fast and later phases have a
	// channel.
	ctl, err := wire.OpenTagged(sess, wire.TagControl)
	if err != nil {
		return fail(fmt.Errorf("caretaker: open control: %w", err))
	}
	defer ctl.Close()

	logging.FromContext(ctx).InfoContext(ctx, "caretaker: connected",
		"server", sb.server, "mounts", len(sb.mounts), "credentials", len(sb.creds),
		"hub", sb.hub != nil, "egress", sb.egress != nil)

	deliverTargets := map[string]hubTarget{}
	if sb.hub != nil {
		// Watch asks the hub to push catalog updates back on this control stream
		// (dynamic import discovery). Old servers ignore the unknown JSON field.
		reg := hub.Registration{Identity: sb.hub.Identity, Watch: sb.hub.wantsDynamicReach()}
		for _, svc := range sb.hub.Register {
			reg.Services = append(reg.Services, hub.Service{Name: svc.Name, Addr: svc.Addr, Protocol: svc.Protocol})
			if svc.Addr == "" && svc.Target != "" {
				deliverTargets[svc.Name] = hubTarget{addr: svc.Target, proto: svc.Protocol}
			}
		}
		if err := json.NewEncoder(ctl).Encode(reg); err != nil {
			return fail(fmt.Errorf("caretaker: hub register: %w", err))
		}
	}

	connSup := supervisor.New(ctx, nil)
	for _, m := range sb.mounts {
		m := m
		connSup.Add("mount:"+m.Name, supervisor.ServiceFunc(func(ctx context.Context) error {
			return runMountStream(ctx, sess, m)
		}), supervisor.Restart)
	}
	for _, c := range sb.creds {
		c := c
		connSup.Add("cred:"+c.Name, supervisor.ServiceFunc(func(ctx context.Context) error {
			return runCredential(ctx, sess, c)
		}), supervisor.Restart)
	}
	if sb.egress != nil {
		e := *sb.egress
		connSup.AddSystem("egress", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runEgress(ctx, sess, e, mark)
		}), supervisor.Restart)
	}
	if sb.hub != nil {
		hubRole := sb.hub
		connSup.AddSystem("hub", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runHubRoleBundle(ctx, sess, ctl, hubRole, deliverTargets)
		}), supervisor.Restart)
	}
	if sb.portForward != nil {
		connSup.AddSystem("portforward", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runPortForwardAccept(ctx, sess)
		}), supervisor.Restart)
	}
	if sb.agentRelay != nil {
		role := *sb.agentRelay
		connSup.AddSystem("agentrelay", supervisor.ServiceFunc(func(ctx context.Context) error {
			return runAgentRelay(ctx, sess, role)
		}), supervisor.Restart)
	}

	select {
	case <-ctx.Done():
		connSup.Wait()
		return nil
	case <-sess.CloseChan():
		connSup.Wait()
		return fail(fmt.Errorf("caretaker: connection to %s closed", sb.server))
	}
}

// runHubRoleBundle runs every hub sub-role (ingress delivery, the peer reach
// listeners, dynamic-reach catalog watching) as one supervised unit within
// runCaretakerConn — these three share fate with EACH OTHER (a listener bind
// failure here retries the whole bundle, not just that one listener), but not
// with the connection's mount/credential/egress roles, which are supervised
// independently. This is a deliberate scoping choice: the hub sub-roles are one
// cohesive feature, and splitting each individual peer:port listener into its
// own supervised child would add plumbing (startReachListeners currently drives
// listeners via an errgroup, not per-listener Services) for little practical
// isolation benefit, since they all serve the one hub role.
func runHubRoleBundle(ctx context.Context, sess *yamux.Session, ctl net.Conn, hubRole *HubRole, deliverTargets map[string]hubTarget) error {
	g, gctx := errgroup.WithContext(ctx)
	// Registration itself already happened (the control-stream Encode, done once
	// in runCaretakerConn before this bundle is even started) and needs no
	// ongoing goroutine of its own — a Register-only hub role (no Reach, no
	// delivery targets, no dynamic reach) would otherwise leave g with nothing to
	// wait on, so g.Wait() would return immediately and the supervisor would
	// busy-restart this bundle forever. Keep it alive for ctx's lifetime instead.
	g.Go(func() error { <-gctx.Done(); return nil })
	if len(deliverTargets) > 0 {
		g.Go(func() error { return serveIngress(gctx, sess, deliverTargets) })
	}
	if err := startReachListeners(gctx, g, sess, hubRole.Reach); err != nil {
		return err
	}
	if hubRole.wantsDynamicReach() {
		g.Go(func() error { return runDynamicReach(gctx, ctl, sess, hubRole) })
	}
	return g.Wait()
}

// runMountStream establishes one 9P mount over an existing caretaker session: it
// opens a mount stream (name line), bridges a local unix socket the kernel-9p
// client dials (trans=unix) to that stream, mounts synchronously (returns only
// once the mount is live), and unmounts on teardown. Mirrors the old per-mount
// runMount, minus its own WebSocket — the stream rides the shared connection.
func runMountStream(ctx context.Context, sess *yamux.Session, m MountRole) error {
	mt := metrics()
	ctx, span := tracer().Start(ctx, "caretaker.mount", trace.WithAttributes(
		attribute.String("caretaker.mount.name", m.Name),
		attribute.String("caretaker.mount.target", m.Target),
		attribute.Bool("caretaker.mount.read_only", m.ReadOnly),
	))
	defer span.End()
	labels := metric.WithAttributes(attribute.String("name", m.Name))
	start := time.Now()
	log := logging.FromContext(ctx)

	fail := func(err error) error {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		mt.mountFail.Add(ctx, 1, labels)
		log.WarnContext(ctx, "caretaker mount: failed", "name", m.Name, "target", m.Target, "error", err)
		return err
	}

	stream, err := wire.OpenTagged(sess, wire.TagMount)
	if err != nil {
		return fail(fmt.Errorf("mount %s: open stream: %w", m.Name, err))
	}
	defer stream.Close()
	// The pod-scoped connection is not session-scoped, so each mount stream carries
	// its deploy-attach session then name (two lines) for the server to route.
	if _, err := io.WriteString(stream, m.Session+"\n"+m.Name+"\n"); err != nil {
		return fail(fmt.Errorf("mount %s: send session/name: %w", m.Name, err))
	}

	tmp, err := os.MkdirTemp("", "cornus-mnt-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(tmp)
	sock := filepath.Join(tmp, "9p.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return fail(err)
	}
	defer l.Close()
	metered := meterMountStream(mt, m.Name, stream)
	// When tracing is on, the caretaker.mount span also carries the stream's
	// byte totals; finishBytes is deferred after span.End above, so it stamps
	// the attributes just before the span ends.
	traced, finishBytes := traceMountBytes(span, metered)
	defer finishBytes()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		wire.Pipe(conn, traced)
	}()

	// Logged BEFORE the (blocking) mount so a stalled handshake is attributable to
	// a specific mount in real time — e.g. the second of several mounts hanging.
	log.InfoContext(ctx, "caretaker mount: attaching", "name", m.Name, "target", m.Target, "read_only", m.ReadOnly)
	if err := deploywire.Mount9P(sock, m.Target, m.ReadOnly, m.AsyncCache); err != nil {
		return fail(fmt.Errorf("mount %s at %s: %w", m.Name, m.Target, err))
	}
	defer deploywire.Unmount9P(m.Target)

	mt.mountLatency.Record(ctx, time.Since(start).Seconds(), labels)
	mt.mountUp.Add(ctx, 1, labels)
	defer mt.mountUp.Add(context.Background(), -1, labels)
	span.AddEvent("mount live")
	log.InfoContext(ctx, "caretaker mount: live", "name", m.Name, "target", m.Target, "took", time.Since(start).String())

	<-ctx.Done()
	log.InfoContext(ctx, "caretaker mount: detaching", "name", m.Name, "target", m.Target)
	return nil
}

// Ready reports nil when every role is live — the readiness the sidecar's
// startup probe checks so the app container waits until all mounts and
// credentials are up. It runs as a separate `cornus caretaker-check` process, so
// it observes only cross-process-visible state: kernel mountpoints, files in the
// shared volume, and TCP listeners.
func Ready(cfg Config) error {
	for _, m := range cfg.Mounts {
		if !IsMountpoint(m.Target) {
			return fmt.Errorf("mount %s not live at %s", m.Name, m.Target)
		}
	}
	for _, c := range cfg.Credentials {
		if err := credentialReady(c); err != nil {
			return err
		}
	}
	if cfg.Docker != nil {
		if err := dockerReady(*cfg.Docker); err != nil {
			return err
		}
	}
	if cfg.Egress != nil {
		if err := egressReady(*cfg.Egress); err != nil {
			return err
		}
	}
	if cfg.AgentRelay != nil {
		if err := agentRelayReady(*cfg.AgentRelay); err != nil {
			return err
		}
	}
	if cfg.Otel != nil {
		if err := otelReady(*cfg.Otel); err != nil {
			return err
		}
	}
	return nil
}

// IsMountpoint reports whether path appears as a mount point in
// /proc/self/mountinfo (field 5, 0-indexed 4) — self-contained, so the probe
// needs no util-linux in the image.
func IsMountpoint(path string) bool {
	want := filepath.Clean(path)
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 && filepath.Clean(fields[4]) == want {
			return true
		}
	}
	return false
}

// caretakerURL builds the WebSocket URL for a pod's single pod-scoped, unified
// caretaker connection (mounts + hub), independent of any deploy-attach session.
// instance, when non-empty, rides as a query parameter so the server can
// register this connection in its per-instance companion registry.
func caretakerURL(server, instance string) string {
	u := wsBase(server) + "/.cornus/v1/caretaker/attach"
	if instance != "" {
		u += "?instance=" + url.QueryEscape(instance)
	}
	return u
}

// wsBase normalizes a server URL to its ws(s):// origin with no trailing slash.
func wsBase(server string) string {
	switch {
	case strings.HasPrefix(server, "https://"):
		server = "wss://" + strings.TrimPrefix(server, "https://")
	case strings.HasPrefix(server, "http://"):
		server = "ws://" + strings.TrimPrefix(server, "http://")
	}
	return strings.TrimRight(server, "/")
}
