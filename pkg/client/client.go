// Package client is an HTTP client for a running cornus server's /.cornus/v1/*
// endpoints (deploy lifecycle and build). It is what `cornus compose` uses to
// redirect Compose commands to the service.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/codes"

	"cornus/pkg/api"
	"cornus/pkg/build/buildprog"
	"cornus/pkg/build/buildwire"
	"cornus/pkg/deploywire"
	"cornus/pkg/observability"
	"cornus/pkg/wire"
)

// Client talks to a cornus server.
type Client struct {
	base string
	http *http.Client
	// tls is the client TLS config for an https endpoint (custom CA and/or mTLS
	// client certificate from a connection profile). nil uses the system defaults.
	// It is applied to both the REST http.Client transport and every WebSocket dial
	// (exec/attach/portforward, build, deploy-attach) so one profile secures all of
	// them.
	tls   *tls.Config
	token string
	// dialer, when set, replaces the transport's DialContext for both the REST
	// http.Client and every WebSocket dial, so all traffic to the server is routed
	// through it — an SSH-tunnel dialer that returns a net.Conn to the remote server
	// over an SSH connection. nil dials directly.
	dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Option configures a Client.
type Option func(*Client)

// WithToken sets the bearer token sent as "Authorization: Bearer <token>" on every
// request (including the archive PUT and the WebSocket attach handshakes). An empty
// token sends no header. It overrides the CORNUS_TOKEN default.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithTLSConfig sets the client TLS config used for the REST transport and every
// WebSocket dial — a custom CA bundle and/or an mTLS client certificate, as a
// connection profile supplies. A nil config leaves the system defaults in place.
func WithTLSConfig(tc *tls.Config) Option {
	return func(c *Client) { c.tls = tc }
}

// WithDialer routes every connection to the server — the REST transport and every
// WebSocket dial — through dial, which returns a net.Conn to the given address. It
// is how an SSH-tunnel connection profile makes all client traffic ride one SSH
// connection. A nil dial leaves direct dialing in place.
func WithDialer(dial func(ctx context.Context, network, addr string) (net.Conn, error)) Option {
	return func(c *Client) { c.dialer = dial }
}

// New returns a client for the server at base (e.g. "http://localhost:5000"). A
// ws:// or wss:// base — the spelling WebSocket-heavy surfaces pass around — is
// normalized to its http(s) equivalent; the WebSocket dial helpers convert back
// per call, so either scheme works for every method. The bearer token defaults
// to $CORNUS_TOKEN and can be overridden with WithToken.
func New(base string, opts ...Option) *Client {
	base = strings.TrimRight(base, "/")
	if rest, ok := strings.CutPrefix(base, "ws://"); ok {
		base = "http://" + rest
	} else if rest, ok := strings.CutPrefix(base, "wss://"); ok {
		base = "https://" + rest
	}
	c := &Client{
		base:  base,
		http:  &http.Client{Transport: otelhttp.NewTransport(baseTransport(nil, nil))},
		token: os.Getenv("CORNUS_TOKEN"),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.tls != nil || c.dialer != nil {
		c.http = &http.Client{Transport: otelhttp.NewTransport(baseTransport(c.tls, c.dialer))}
	}
	return c
}

// baseTransport returns the client's underlying HTTP transport: a clone of
// http.DefaultTransport (preserving proxy support via http.ProxyFromEnvironment,
// dial timeouts, connection pooling, and HTTP/2 via ForceAttemptHTTP2), with tc
// applied as the TLS config when non-nil. Cloning rather than building a bare
// &http.Transport{TLSClientConfig: tc} matters: a bare transport would drop
// HTTP(S)_PROXY and, with a non-nil TLSClientConfig, disable HTTP/2, so the TLS
// path would silently diverge from the plain-HTTP default. New wraps the result
// in otelhttp.NewTransport for client spans and W3C trace-context injection; this
// stays a plain *http.Transport so those defaults can be asserted directly.
func baseTransport(tc *tls.Config, dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if tc != nil {
		tr.TLSClientConfig = tc
	}
	if dial != nil {
		tr.DialContext = dial
	}
	return tr
}

// clientTransport bundles the client's TLS config and custom dialer for the
// WebSocket and build/deploy-attach dials, so a connection profile's TLS and
// SSH-tunnel dialer reach every surface, not just the REST transport.
func (c *Client) clientTransport() wire.ClientTransport {
	return wire.ClientTransport{TLS: c.tls, DialContext: c.dialer}
}

// validatePrivateKeyTransport refuses to serialize private-key material onto a
// connection where another host can observe it. HTTPS protects a direct remote
// connection; a custom dialer is the SSH-tunnel path and is protected by that
// tunnel; and plain HTTP is acceptable only on loopback (including the automatic
// Kubernetes port-forward endpoint).
func (c *Client) validatePrivateKeyTransport() error {
	if c.dialer != nil {
		return nil
	}
	u, err := url.Parse(c.base)
	if err != nil {
		return fmt.Errorf("managed ingress certificates require a protected server connection: invalid endpoint: %w", err)
	}
	if strings.EqualFold(u.Scheme, "https") || (strings.EqualFold(u.Scheme, "http") && loopbackHost(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("managed ingress certificates contain private-key material and cannot be sent to plaintext endpoint %q; use HTTPS, an SSH tunnel, or a loopback Kubernetes port-forward", c.base)
}

func loopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasManagedIngressCertificates(spec api.DeploySpec) bool {
	return spec.Ingress != nil && spec.Ingress.TLS != nil && len(spec.Ingress.TLS.ManagedCertificates) != 0
}

// setAuth adds the Authorization header to req when a token is configured.
func (c *Client) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// dialHeader builds the header for a WebSocket attach dial: the current span's
// W3C trace context (so the server's attach handler span links back to this
// client span, and onward to the caretaker) plus the bearer Authorization when a
// token is set. Unlike the REST path — where otelhttp injects trace context on
// every request automatically — the WebSocket dials bypass c.http, so the context
// is injected here explicitly. Returns nil when neither is present, matching the
// nil-means-no-header contract the wire dial helpers accept. Zero-cost when
// telemetry is off: InjectHTTP yields an empty header.
func (c *Client) dialHeader(ctx context.Context) http.Header {
	h := observability.InjectHTTP(ctx)
	if c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

// Host returns the registry host (base without scheme), e.g. "localhost:5000".
func (c *Client) Host() string {
	if u, err := url.Parse(c.base); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(c.base, "http://"), "https://")
}

// RegistryToken returns the bearer token used to authenticate against the
// server's builtin registry (the same token as the API). Empty means no auth.
func (c *Client) RegistryToken() string { return c.token }

// RegistrySecure reports whether the builtin registry is reached over https.
func (c *Client) RegistrySecure() bool { return strings.HasPrefix(c.base, "https://") }

// RegistryTransport returns an http.RoundTripper carrying the client's TLS
// config (custom CA / mTLS from a connection profile), for reaching the builtin
// registry. It clones the default transport so proxy support, timeouts, pooling,
// and HTTP/2 are preserved (see New). Bearer auth is supplied separately by the
// caller (e.g. go-containerregistry's authn), not injected here.
func (c *Client) RegistryTransport() http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.tls != nil {
		tr.TLSClientConfig = c.tls
	}
	// Route registry traffic through the same tunnel as the API, else a registry
	// reachable only through the SSH tunnel would be unreachable from the CLI.
	if c.dialer != nil {
		tr.DialContext = c.dialer
	}
	return tr
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)
	return c.http.Do(req)
}

// streamError surfaces a mid-stream server-side failure after a streaming
// response body has been drained to EOF. The server cannot change the
// committed 200 once bytes have flowed, so it reports a backend error that
// struck mid-stream in the X-Cornus-Stream-Error trailer; Go's http client
// populates resp.Trailer only after the body reaches EOF, which the callers'
// io.Copy guarantees. A non-empty trailer means the output above is truncated,
// not complete.
func streamError(resp *http.Response) error {
	if msg := resp.Trailer.Get(api.StreamErrorTrailer); msg != "" {
		return fmt.Errorf("stream error after partial output: %s", msg)
	}
	return nil
}

// APIError is a non-2xx response from the cornus server. It carries the HTTP
// status code so callers can tell a transient server-side condition (a 5xx or
// 429, where retrying the same request may succeed) from a terminal one (a 4xx:
// bad spec, not found, an immutable-field conflict). Its Error() text is the same
// "<status>: <message>" form the plain fmt.Errorf produced before, so existing
// message matching and user-facing output are unchanged.
type APIError struct {
	StatusCode int    // e.g. 500
	Status     string // the HTTP status line, e.g. "500 Internal Server Error"
	Message    string // the server's {"error":...} text, or the trimmed raw body
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Status, e.Message)
}

// Transient reports whether retrying the request that produced this error may
// succeed: a server-side 5xx or a 429 Too Many Requests. A 4xx is terminal.
func (e *APIError) Transient() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

// apiError extracts the {"error": "..."} message from a non-2xx response as a
// typed *APIError (which still satisfies error, so callers that only propagate
// it are unaffected).
func apiError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_ = json.Unmarshal(b, &e)
	msg := e.Error
	if msg == "" {
		msg = strings.TrimSpace(string(b))
	}
	return &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Message: msg}
}

// Info fetches the server's self-description (GET /.cornus/v1/info), including the
// registry host its deploy targets should pull from. Servers that predate the
// endpoint answer 404; callers treat any error as "no advertised host" and fall
// back to Host().
func (c *Client) Info(ctx context.Context) (api.ServerInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/info", nil)
	if err != nil {
		return api.ServerInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.ServerInfo{}, apiError(resp)
	}
	var info api.ServerInfo
	return info, json.NewDecoder(resp.Body).Decode(&info)
}

// StorageUsage fetches the server's non-destructive disk-usage report
// (GET /.cornus/v1/storage): the registry CAS footprint and, when the block cache
// is enabled, its footprint. It never mutates state (unlike POST /.cornus/v1/gc).
func (c *Client) StorageUsage(ctx context.Context) (api.StorageUsage, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/storage", nil)
	if err != nil {
		return api.StorageUsage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.StorageUsage{}, apiError(resp)
	}
	var u api.StorageUsage
	return u, json.NewDecoder(resp.Body).Decode(&u)
}

// Deploy applies a deployment spec (POST /.cornus/v1/deploy).
func (c *Client) Deploy(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	if hasManagedIngressCertificates(spec) {
		if err := c.validatePrivateKeyTransport(); err != nil {
			return api.DeployStatus{}, err
		}
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return api.DeployStatus{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/.cornus/v1/deploy", bytes.NewReader(body))
	if err != nil {
		return api.DeployStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.DeployStatus{}, apiError(resp)
	}
	var st api.DeployStatus
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// TunnelStart hosts a public tunnel to a deployment's port on the server
// (POST /.cornus/v1/deploy/{name}/tunnel) and returns its public URL. authToken is the
// tunnel-backend credential (e.g. an ngrok authtoken); it may be empty when the
// server carries a default. It is a bearer secret, sent only over the
// authenticated server endpoint.
func (c *Client) TunnelStart(ctx context.Context, name string, req api.TunnelRequest) (api.TunnelStatus, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return api.TunnelStatus{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/tunnel", bytes.NewReader(body))
	if err != nil {
		return api.TunnelStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.TunnelStatus{}, apiError(resp)
	}
	var st api.TunnelStatus
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// TunnelStatus reports a deployment's current tunnel (GET /.cornus/v1/deploy/{name}/tunnel).
func (c *Client) TunnelStatus(ctx context.Context, name string) (api.TunnelStatus, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/tunnel", nil)
	if err != nil {
		return api.TunnelStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.TunnelStatus{}, apiError(resp)
	}
	var st api.TunnelStatus
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// TunnelStop tears a deployment's tunnel down (DELETE /.cornus/v1/deploy/{name}/tunnel).
func (c *Client) TunnelStop(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/tunnel", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return nil
}

// TunnelChannel opens a side-channel WebSocket to a deployment's tunnel (ws
// .../.cornus/v1/deploy/{name}/tunnel/channel/{purpose}) and returns the raw
// net.Conn for the caller to bridge to a local resource. It carries no
// preamble — name and purpose are already in the URL — so the returned conn is
// a plain bidirectional byte stream from the moment it's returned.
//
// This is a small, deliberately generic mechanism: purpose picks what the
// channel is for. Today the server only recognizes "ssh-agent" (forwarding the
// caller's local ssh-agent to the ssh tunnel backend's outbound handshake, via
// ForwardAgent on the following TunnelStart call), but a future feature can
// reuse this same method with a new purpose string instead of adding another
// endpoint.
func (c *Client) TunnelChannel(ctx context.Context, name, purpose string) (net.Conn, error) {
	ctx, span := observability.Tracer().Start(ctx, "cornus.client.tunnelchannel")
	defer span.End()
	u := wsAttachURL(c.base, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/tunnel/channel/"+url.PathEscape(purpose))
	conn, err := wire.DialConnControlHeaderCT(ctx, u, nil, c.dialHeader(ctx), c.clientTransport())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return conn, nil
}

// ExecAgentChannel opens the ssh-agent-forwarding side channel for a
// --forward-agent exec session against deployment name (ws
// .../.cornus/v1/deploy/{name}/exec-agent-channel) and returns it as a yamux
// CLIENT session: the server opens a new stream on it for every local
// connection a process inside the instance makes to the forwarded agent
// socket (see pkg/caretaker's AgentRelayRole), and the caller accepts each and
// relays it to the real local agent (see cmd/cornus/exec.go).
func (c *Client) ExecAgentChannel(ctx context.Context, name string) (*yamux.Session, error) {
	u := wsAttachURL(c.base, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/exec-agent-channel")
	return wire.DialControlHeaderCT(ctx, u, nil, c.dialHeader(ctx), c.clientTransport())
}

// List reports all managed deployments (GET /.cornus/v1/deploy).
func (c *Client) List(ctx context.Context) ([]api.DeployStatus, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/deploy", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp)
	}
	var list []api.DeployStatus
	return list, json.NewDecoder(resp.Body).Decode(&list)
}

// Status reports one deployment (GET /.cornus/v1/deploy/{name}).
func (c *Client) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/deploy/"+url.PathEscape(name), nil)
	if err != nil {
		return api.DeployStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.DeployStatus{}, apiError(resp)
	}
	var st api.DeployStatus
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// Delete removes a deployment (DELETE /.cornus/v1/deploy/{name}).
func (c *Client) Delete(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/.cornus/v1/deploy/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiError(resp)
	}
	return nil
}

// ErrVolumeRemovalUnsupported is returned by DeleteVolume when the server's
// deploy backend cannot remove volumes (HTTP 501). Callers (compose down
// --volumes) treat it as a soft skip rather than a hard failure.
var ErrVolumeRemovalUnsupported = errors.New("server deploy backend does not support removing volumes")

// DeleteVolume removes a named, project-scoped volume by its resource name
// (DELETE /.cornus/v1/volume/{name}), backing `compose down --volumes`. The backend is
// delete-if-exists, so removing an absent volume succeeds. Returns
// ErrVolumeRemovalUnsupported when the backend cannot remove volumes.
func (c *Client) DeleteVolume(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/.cornus/v1/volume/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotImplemented {
		return ErrVolumeRemovalUnsupported
	}
	if resp.StatusCode != http.StatusNoContent {
		return apiError(resp)
	}
	return nil
}

// Action runs a lifecycle action (start/stop/restart) on a deployment.
func (c *Client) Action(ctx context.Context, name, action string) error {
	resp, err := c.do(ctx, http.MethodPost, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/"+action, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return apiError(resp)
	}
	return nil
}

// Logs streams a deployment's logs from GET /.cornus/v1/deploy/{name}/logs into w,
// copying the response body until the stream ends or ctx is cancelled. The
// bytes are stdcopy-multiplexed frames (per the deploy.Backend.Logs contract),
// so the caller can demultiplex stdout/stderr. A backend failure mid-stream
// (after the 200 committed) arrives as the X-Cornus-Stream-Error trailer and
// is returned as an error alongside whatever partial output reached w.
func (c *Client) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	q := url.Values{}
	if opts.Follow {
		q.Set("follow", "1")
	}
	if opts.Stdout {
		q.Set("stdout", "1")
	}
	if opts.Stderr {
		q.Set("stderr", "1")
	}
	if opts.Timestamps {
		q.Set("timestamps", "1")
	}
	if opts.Tail != "" {
		q.Set("tail", opts.Tail)
	}
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Until != "" {
		q.Set("until", opts.Until)
	}
	path := "/.cornus/v1/deploy/" + url.PathEscape(name) + "/logs"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}
	return streamError(resp)
}

// Stats streams a deployment's Docker-format container metrics from
// GET /.cornus/v1/deploy/{name}/stats into w. opts.Stream=false requests a single
// stats object (docker's --no-stream); the bytes are Docker's own stats JSON,
// copied through until the stream ends or ctx is cancelled. As with Logs, a
// mid-stream backend failure is returned as an error via the
// X-Cornus-Stream-Error trailer after the partial output.
func (c *Client) Stats(ctx context.Context, name string, opts api.StatsOptions, w io.Writer) error {
	q := url.Values{}
	if !opts.Stream {
		q.Set("stream", "0")
	}
	path := "/.cornus/v1/deploy/" + url.PathEscape(name) + "/stats"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}
	return streamError(resp)
}

// archivePath builds /.cornus/v1/deploy/{name}/archive?path=... with the given extra
// query params.
func archivePath(name, path string, extra url.Values) string {
	q := url.Values{}
	q.Set("path", path)
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return "/.cornus/v1/deploy/" + url.PathEscape(name) + "/archive?" + q.Encode()
}

// StatPath returns metadata for path inside the named deployment (HEAD
// /.cornus/v1/deploy/{name}/archive), parsed from the X-Docker-Container-Path-Stat
// response header (docker cp / archive HEAD).
func (c *Client) StatPath(ctx context.Context, name, path string) (api.PathStat, error) {
	resp, err := c.do(ctx, http.MethodHead, archivePath(name, path, nil), nil)
	if err != nil {
		return api.PathStat{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.PathStat{}, apiError(resp)
	}
	return api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
}

// CopyFrom streams a tar of path (from the named deployment) into w and returns
// the path's stat parsed from the X-Docker-Container-Path-Stat response header
// (GET /.cornus/v1/deploy/{name}/archive; docker cp from container). The stat is read
// from the header before the body is copied. A mid-stream backend failure
// (X-Cornus-Stream-Error trailer) is returned as an error: the tar written to
// w is truncated and must not be treated as complete.
func (c *Client) CopyFrom(ctx context.Context, name, path string, w io.Writer) (api.PathStat, error) {
	resp, err := c.do(ctx, http.MethodGet, archivePath(name, path, nil), nil)
	if err != nil {
		return api.PathStat{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.PathStat{}, apiError(resp)
	}
	st, err := api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
	if err != nil {
		return api.PathStat{}, err
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return api.PathStat{}, err
	}
	if err := streamError(resp); err != nil {
		return api.PathStat{}, err
	}
	return st, nil
}

// CopyTo PUTs a tar read from r into path inside the named deployment (PUT
// /.cornus/v1/deploy/{name}/archive; docker cp into container). opts carries Docker's
// noOverwriteDirNonDir/copyUIDGID flags.
func (c *Client) CopyTo(ctx context.Context, name, path string, r io.Reader, opts api.CopyToOptions) error {
	extra := url.Values{}
	if opts.NoOverwriteDirNonDir {
		extra.Set("noOverwriteDirNonDir", "1")
	}
	if opts.CopyUIDGID {
		extra.Set("copyUIDGID", "1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+archivePath(name, path, extra), r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return nil
}

// ExecCreate creates an exec in the named deployment's first instance (POST
// /.cornus/v1/deploy/{name}/exec) and returns the backend exec id (docker exec create).
func (c *Client) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodPost, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/exec", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", apiError(resp)
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// ExecInspect reports an exec's state (GET /.cornus/v1/deploy/exec/{id}/json).
func (c *Client) ExecInspect(ctx context.Context, execID string) (api.ExecState, error) {
	resp, err := c.do(ctx, http.MethodGet, "/.cornus/v1/deploy/exec/"+url.PathEscape(execID)+"/json", nil)
	if err != nil {
		return api.ExecState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return api.ExecState{}, apiError(resp)
	}
	var st api.ExecState
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// ExecResize resizes the exec's TTY to height rows by width columns (POST
// /.cornus/v1/deploy/exec/{id}/resize?h=&w=). It is an out-of-band control-plane call,
// separate from the ExecStart tunnel.
func (c *Client) ExecResize(ctx context.Context, execID string, height, width uint) error {
	path := fmt.Sprintf("/.cornus/v1/deploy/exec/%s/resize?h=%d&w=%d", url.PathEscape(execID), height, width)
	resp, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return nil
}

// ExecStart opens the exec-start WebSocket tunnel (ws .../.cornus/v1/deploy/exec/{id}/
// start), writes the ExecStartConfig preamble, and returns the raw net.Conn for
// the caller to bridge to a local stdio stream. The preamble is a single
// newline-delimited JSON frame the server reads before bridging.
func (c *Client) ExecStart(ctx context.Context, execID string, cfg api.ExecStartConfig) (net.Conn, error) {
	ctx, span := observability.Tracer().Start(ctx, "cornus.client.exec")
	defer span.End()
	conn, err := wire.DialConnControlHeaderCT(ctx, wsAttachURL(c.base, "/.cornus/v1/deploy/exec/"+url.PathEscape(execID)+"/start"), nil, c.dialHeader(ctx), c.clientTransport())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if err := writePreamble(conn, cfg); err != nil {
		conn.Close()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return conn, nil
}

// Attach opens the attach WebSocket tunnel (ws .../.cornus/v1/deploy/{name}/attach),
// writes the AttachConfig preamble, and returns the raw net.Conn for the caller
// to bridge to a local stdio stream (docker attach).
func (c *Client) Attach(ctx context.Context, name string, cfg api.AttachConfig) (net.Conn, error) {
	ctx, span := observability.Tracer().Start(ctx, "cornus.client.attach")
	defer span.End()
	conn, err := wire.DialConnControlHeaderCT(ctx, wsAttachURL(c.base, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/attach"), nil, c.dialHeader(ctx), c.clientTransport())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if err := writePreamble(conn, cfg); err != nil {
		conn.Close()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return conn, nil
}

// PortForward opens the port-forward WS tunnel (ws .../.cornus/v1/deploy/{name}/portforward),
// writes the PortForwardConfig preamble (container port + protocol), and returns
// the raw net.Conn for the caller to splice to a local connection (kubectl
// port-forward). For proto "tcp" (or empty) one tunnel carries one connection's
// raw byte stream; open a fresh one per accepted local connection. For proto
// "udp" the tunnel carries length-prefixed datagram frames (wire.WriteDatagram)
// for one client flow; PortForward consumes the server's newline-JSON
// PortForwardAck before returning, so an error here (including the ack's
// rejection when the backend cannot forward UDP, e.g. kubernetes) means no
// tunnel. A server predating the ack closes the tunnel on a udp preamble, which
// surfaces as an error too.
func (c *Client) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	ctx, span := observability.Tracer().Start(ctx, "cornus.client.portforward")
	defer span.End()
	conn, err := wire.DialConnControlHeaderCT(ctx, wsAttachURL(c.base, "/.cornus/v1/deploy/"+url.PathEscape(name)+"/portforward"), nil, c.dialHeader(ctx), c.clientTransport())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if err := writePreamble(conn, api.PortForwardConfig{Port: port, Protocol: proto}); err != nil {
		conn.Close()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if proto == "udp" {
		if err := readPortForwardAck(ctx, conn); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// readPortForwardAck reads the server's single newline-JSON PortForwardAck off a
// udp port-forward tunnel. It reads byte-by-byte up to the newline so no bytes of
// the datagram stream that follows are buffered away from the caller. A non-empty
// ack error, a malformed ack, or an early close (an old server that does not
// speak the udp ack) all fail the dial.
//
// The read is tied to ctx: any ctx deadline is applied to the connection, and a
// watcher goroutine pushes the read deadline into the past if ctx is cancelled.
// Without this a server that completes the WS upgrade but never sends the ack
// would block the byte-by-byte read forever, unkillable via ctx. On a successful
// ack the read deadline is cleared so the datagram stream that follows is not
// subject to it; on any error the caller closes conn, so clearing is moot there.
func readPortForwardAck(ctx context.Context, conn net.Conn) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			// Unblock the in-progress conn.Read; a past deadline makes it return
			// a timeout error immediately.
			_ = conn.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	err := func() error {
		var line []byte
		buf := make([]byte, 1)
		for {
			if _, err := conn.Read(buf); err != nil {
				return fmt.Errorf("udp port-forward: reading server ack (server may not support UDP port-forward): %w", err)
			}
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
			if len(line) > 8<<10 {
				return fmt.Errorf("udp port-forward: server ack too long")
			}
		}
		var ack api.PortForwardAck
		if err := json.Unmarshal(line, &ack); err != nil {
			return fmt.Errorf("udp port-forward: invalid server ack: %w", err)
		}
		if ack.Error != "" {
			return fmt.Errorf("udp port-forward: %s", ack.Error)
		}
		return nil
	}()
	// Stop the watcher and wait for it to exit before clearing the deadline, so a
	// late SetReadDeadline(now) from a just-cancelled ctx cannot outlive the clear.
	close(stop)
	<-done
	_ = conn.SetReadDeadline(time.Time{})
	return err
}

// writePreamble marshals v as a single newline-delimited JSON frame and writes
// it as the first message on conn, so the server can decode the start/attach
// config before switching the connection to a raw bidirectional stream.
func writePreamble(conn net.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = conn.Write(b)
	return err
}

// BuildRequest describes a build to run on the server.
type BuildRequest struct {
	ContextDir string
	Dockerfile string
	Tag        string
	Args       map[string]string
	Push       bool
	// Target is the Dockerfile multi-stage target stage (compose/devcontainer
	// build.target). Empty builds the final stage.
	Target string
	// CacheFrom lists registry references to import build cache from
	// (compose/devcontainer build.cache_from). Each entry is forwarded as a
	// type=registry cache import.
	CacheFrom []string
	// BuildContexts are additional named build contexts (name -> host dir).
	// Each dir is served to the server's build engine over 9P.
	BuildContexts map[string]string
	// Secrets are build secrets (id -> host file path). Each file's bytes are
	// read locally and forwarded to the server for use as a build secret.
	Secrets map[string]string
	// SSH are SSH agents forwarded for RUN --mount=type=ssh (id -> local agent
	// socket path). Each socket is tunneled back to this caller over the session.
	SSH map[string]string
	// NoCache disables the build cache for this build (compose build --no-cache).
	NoCache bool
	// Labels are image labels applied to the built image (compose build.labels).
	Labels map[string]string
	// Pull always attempts to pull a newer base image (compose build.pull).
	Pull bool
	// Platforms are the target build platforms (compose build.platforms).
	Platforms []string
	// Tags are additional image references to tag/push the result as (compose
	// build.tags), beyond Tag.
	Tags []string
	// Network is the build-time network mode (compose build.network): default/none/host.
	Network string
	// CacheTo lists build-cache export specs (compose build.cache_to), each a
	// buildx-style "type=...,k=v" string or a bare registry ref. Each is forwarded
	// as a cache export.
	CacheTo []string
	// ExtraHosts adds custom /etc/hosts entries during the build (compose
	// build.extra_hosts), each normalised "host:ip".
	ExtraHosts []string
	// ShmSize sizes /dev/shm for RUN steps in bytes (compose build.shm_size).
	ShmSize int64
	// DockerfileInline is an inline Dockerfile body (compose build.dockerfile_inline).
	// When non-empty it supersedes Dockerfile: the content is served as a synthetic
	// Dockerfile instead of reading one from the context.
	DockerfileInline string
}

// Build runs a build on the cornus server over 9P/WebSocket: the context
// directory is served to the server's build engine on demand, and progress
// events are delivered to progress (a nil Sink is fine).
func (c *Client) Build(ctx context.Context, req BuildRequest, progress buildprog.Sink) error {
	ctx, span := observability.Tracer().Start(ctx, "cornus.client.build")
	defer span.End()
	dfName := req.Dockerfile
	if dfName == "" {
		dfName = "Dockerfile"
	}
	ctxAbs, err := filepath.Abs(req.ContextDir)
	if err != nil {
		return err
	}
	dfDir := filepath.Dir(filepath.Join(ctxAbs, dfName))

	// dockerfile_inline supersedes the context Dockerfile: stage the inline body
	// as a synthetic Dockerfile in a temp dir and serve that as the dockerfile
	// tree, so the build uses the inline content regardless of req.Dockerfile.
	if req.DockerfileInline != "" {
		tmp, err := os.MkdirTemp("", "cornus-inline-df-")
		if err != nil {
			return fmt.Errorf("staging inline dockerfile: %w", err)
		}
		defer os.RemoveAll(tmp)
		dfName = "Dockerfile"
		if err := os.WriteFile(filepath.Join(tmp, dfName), []byte(req.DockerfileInline), 0o644); err != nil {
			return fmt.Errorf("staging inline dockerfile: %w", err)
		}
		dfDir = tmp
	}

	spec := buildwire.BuildSpec{
		Target:         req.Tag,
		TargetStage:    req.Target,
		DockerfileName: filepath.Base(dfName),
		BuildArgs:      req.Args,
		Push:           req.Push,
		Insecure:       true,
		NoCache:        req.NoCache,
		Pull:           req.Pull,
		Labels:         req.Labels,
		Platforms:      req.Platforms,
		Tags:           req.Tags,
		Network:        req.Network,
		ExtraHosts:     req.ExtraHosts,
		ShmSize:        req.ShmSize,
	}
	// cache_from entries are registry references; forward them as type=registry
	// cache imports (the existing --cache-from type=registry,ref=... plumbing).
	for _, ref := range req.CacheFrom {
		if ref == "" {
			continue
		}
		spec.CacheImports = append(spec.CacheImports, buildwire.CacheOption{
			Type:  "registry",
			Attrs: map[string]string{"ref": ref},
		})
	}
	// cache_to entries are buildx-style export specs (or a bare registry ref);
	// forward each as a cache export, parallel to the cache_from imports above.
	for _, s := range req.CacheTo {
		opt, err := parseCacheExport(s)
		if err != nil {
			return err
		}
		if opt.Type != "" {
			spec.CacheExports = append(spec.CacheExports, opt)
		}
	}
	opts := buildwire.ServeOpts{ContextDir: ctxAbs, DockerfileDir: dfDir}

	for name, dir := range req.BuildContexts {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("build context %q: %w", name, err)
		}
		if opts.NamedContexts == nil {
			opts.NamedContexts = map[string]string{}
		}
		opts.NamedContexts[name] = absDir
		spec.NamedContexts = append(spec.NamedContexts, name)
	}

	for id, path := range req.Secrets {
		val, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read build secret %q from %s: %w", id, path, err)
		}
		if opts.Secrets == nil {
			opts.Secrets = map[string][]byte{}
		}
		opts.Secrets[id] = val
		spec.SecretIDs = append(spec.SecretIDs, id)
	}

	for id, sock := range req.SSH {
		if opts.SSHSockets == nil {
			opts.SSHSockets = map[string]string{}
		}
		opts.SSHSockets[id] = sock
		spec.SSHIDs = append(spec.SSHIDs, id)
	}
	sort.Strings(spec.SSHIDs)

	_, err = buildwire.Serve(ctx, wsAttachURL(c.base, "/.cornus/v1/build/attach"), spec, opts, progress, c.dialHeader(ctx), c.clientTransport())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// parseCacheExport parses a compose build.cache_to entry into a wire cache
// option. A buildx-style "type=registry,ref=...,k=v" string sets Type from the
// "type" field and the rest as Attrs; a bare value with no "=" is treated as a
// registry reference (type=registry,ref=<value>), and a spec that omits "type="
// defaults to type=registry. An empty entry yields the zero option (skipped by
// the caller).
func parseCacheExport(s string) (buildwire.CacheOption, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return buildwire.CacheOption{}, nil
	}
	if !strings.Contains(s, "=") {
		return buildwire.CacheOption{Type: "registry", Attrs: map[string]string{"ref": s}}, nil
	}
	opt := buildwire.CacheOption{Attrs: map[string]string{}}
	for _, field := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			return buildwire.CacheOption{}, fmt.Errorf("invalid cache_to option %q (want key=value)", field)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "type" {
			opt.Type = v
		} else {
			opt.Attrs[k] = v
		}
	}
	if opt.Type == "" {
		opt.Type = "registry"
	}
	return opt, nil
}

// DeployAttach opens a long-lived deploy-attach session on the server: it sends
// the spec, serves any caller-local bind-mount directories over 9P for the
// container's lifetime, and streams events to the callback until ctx is
// cancelled or the server tears down. A mount whose Source is a filesystem path
// is treated as caller-local and served over 9P, read-only or read-write per the
// mount's ReadOnly flag. Named-volume/bare-name sources pass through unchanged as
// server-host mounts.
func (c *Client) DeployAttach(ctx context.Context, spec api.DeploySpec, events func(deploywire.Event)) error {
	if hasManagedIngressCertificates(spec) {
		if err := c.validatePrivateKeyTransport(); err != nil {
			return err
		}
	}
	ctx, span := observability.Tracer().Start(ctx, "cornus.client.deploy.attach")
	defer span.End()
	// An async (writable, cache-coherent) mount requires a single writer: its
	// coherence model assumes every mutation flows through one block proxy, so two
	// replicas writing the same source would not see each other's writes.
	if spec.Replicas > 1 {
		for _, m := range spec.Mounts {
			if m.AsyncCache && isLocalSource(m.Source) {
				return fmt.Errorf("mount %s: --local-mount async requires replicas <= 1 (single writer), got %d", m.Source, spec.Replicas)
			}
		}
	}
	as := deploywire.DeployAttachSpec{Spec: spec}
	localDirs := map[string]string{}
	for i, m := range spec.Mounts {
		if !isLocalSource(m.Source) {
			continue
		}
		abs, err := filepath.Abs(expandHome(m.Source))
		if err != nil {
			return err
		}
		name := fmt.Sprintf("m%d", i)
		lm := deploywire.LocalMount{Index: i, Name: name, ReadOnly: m.ReadOnly, Immutable: m.Immutable, AsyncCached: m.AsyncCache}
		// A 9P mount root must be a directory. When the source is a single file
		// (Compose file-based configs/secrets bind one file), export its parent
		// directory and record the basename as Subpath so the server binds just
		// that file. Directory sources (the common case) export as-is.
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			localDirs[name] = filepath.Dir(abs)
			lm.Subpath = filepath.Base(abs)
		} else {
			localDirs[name] = abs
		}
		as.LocalMounts = append(as.LocalMounts, lm)
	}
	// Client-sourced credentials: the caller runs each source backend and answers
	// the workload's fetch requests over the session for its lifetime. Only the
	// backend name + non-secret config travel; the secret is minted on demand here.
	if spec.Credentials != nil {
		for _, src := range spec.Credentials.Sources {
			as.CredentialSources = append(as.CredentialSources, deploywire.CredentialBacking{
				Name:    src.Name,
				Backend: src.Backend,
				Config:  src.Config,
			})
		}
	}
	err := deploywire.Serve(ctx, wsAttachURL(c.base, "/.cornus/v1/deploy/attach"), as, localDirs, events, c.dialHeader(ctx), c.clientTransport())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// LocalMountSources returns the mount sources in spec that are caller-local
// filesystem paths — exactly the mounts DeployAttach serves over 9P for the
// session's lifetime. A stateless Deploy POST has no client session to serve
// them, so a caller wanting a detached apply can use this to detect (and
// reject) such specs up front. Named-volume/bare-name sources are not
// included; they resolve on the server host and detach fine.
func LocalMountSources(spec api.DeploySpec) []string {
	var srcs []string
	for _, m := range spec.Mounts {
		if isLocalSource(m.Source) {
			srcs = append(srcs, m.Source)
		}
	}
	return srcs
}

// isLocalSource reports whether a mount source is a caller-local filesystem path
// (absolute or ./ ../ ~ relative) rather than a named volume / bare name.
func isLocalSource(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~") || s == "." || s == ".."
}

// expandHome expands a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// wsAttachURL converts the server base URL to a WebSocket attach URL for path.
func wsAttachURL(base, path string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return strings.TrimRight(base, "/") + path
}
