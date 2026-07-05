package api

// TunnelRequest is the JSON body of POST /.cornus/v1/deploy/{name}/tunnel: it asks the
// server to host a public tunnel to Port inside the named deployment's first
// instance. AuthToken is the tunnel-backend credential (e.g. an ngrok
// authtoken); it may be empty when the server is configured with a default
// credential (CORNUS_TUNNEL_AUTHTOKEN). It is a bearer secret — sent only over
// the server's authenticated endpoint and never logged or persisted.
//
// ForwardAgent asks the server to authenticate using the caller's local
// ssh-agent instead of (or in addition to) AuthToken — only meaningful for the
// ssh backend. The caller must have already opened the "ssh-agent" tunnel
// channel (see the tunnel/channel/{purpose} endpoint) for name before sending
// this request.
type TunnelRequest struct {
	AuthToken    string `json:"authToken,omitempty"`
	ForwardAgent bool   `json:"forwardAgent,omitempty"`
	Port         int    `json:"port"`
	Proto        string `json:"proto,omitempty"` // "http" (default) or "tcp"
}

// TunnelStatus is the JSON response of the tunnel endpoint: the current state of
// a deployment's tunnel. URL is the public address clients use to reach it.
type TunnelStatus struct {
	Active bool   `json:"active"`
	URL    string `json:"url,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// Ingress-tunnel host modes. They decide what the front door sees in the Host
// header of a request arriving through the tunnel, which is the one thing a
// tunnel cannot get right on its own: the public hostname a provider hands out
// (abc123.ngrok.app) is not the hostname the ingress was declared for
// (web.myapp.example.com).
const (
	// HostModeAuto asks the server to pick. It requests the ingress host as the
	// tunnel's public hostname; if the provider grants it, the request needs no
	// adjustment at all (HostModePassthrough). Otherwise it falls back to
	// HostModeAlias, which routes without touching the request.
	HostModeAuto = "auto"
	// HostModePassthrough leaves the request untouched. Correct when the tunnel's
	// public hostname already IS an ingress host, and the only correct mode for a
	// raw-TCP tunnel, where there is no HTTP request to adjust.
	HostModePassthrough = "passthrough"
	// HostModeAlias registers the tunnel's public hostname as an additional name
	// for the ingress host, so routing resolves while the app still sees the
	// hostname the client actually used. Preferred over rewriting: the app's
	// redirects, Domain= cookies and CORS origins stay on the name the browser
	// is on.
	HostModeAlias = "alias"
	// HostModeRewrite replaces the Host header with the ingress host before
	// routing. Needed by apps that key on their configured hostname, at the cost
	// of absolute redirects and cookies pointing at a name the client cannot
	// reach.
	HostModeRewrite = "rewrite"
)

// IngressTunnelRequest is the JSON body of POST /.cornus/v1/ingress-tunnel: it
// asks the server to host a public tunnel in front of its INGRESS rather than a
// single workload port, so a whole project's declared hosts and paths are
// reachable through one URL.
//
// Exactly one of Project or Deployment scopes the tunnel. AuthToken and
// ForwardAgent carry the same meaning as in TunnelRequest — the credential is a
// bearer secret, sent only over the authenticated endpoint and never logged or
// persisted.
type IngressTunnelRequest struct {
	AuthToken    string `json:"authToken,omitempty"`
	ForwardAgent bool   `json:"forwardAgent,omitempty"`
	// Project scopes the tunnel to every deployment of a compose project, so all
	// of its services sit behind one URL.
	Project string `json:"project,omitempty"`
	// Deployment scopes the tunnel to a single deployment's ingress.
	Deployment string `json:"deployment,omitempty"`
	// HostMode is one of the HostMode* constants; empty means HostModeAuto.
	HostMode string `json:"hostMode,omitempty"`
	// Host selects which ingress host the tunnel fronts when the scope resolves
	// to more than one, and is the rewrite target for HostModeRewrite. Empty
	// picks the scope's only host, or fails when the choice is ambiguous.
	Host string `json:"host,omitempty"`
	// Proto is the exposed protocol hint: "http" (default) or "tcp". A "tcp"
	// tunnel is a raw byte stream, so the client's TLS and Host reach the front
	// door untouched — the only way to get end-to-end TLS.
	Proto string `json:"proto,omitempty"`
}

// IngressTunnelStatus is the JSON response of the ingress-tunnel endpoint.
type IngressTunnelStatus struct {
	Active bool   `json:"active"`
	URL    string `json:"url,omitempty"`
	// Scope is the tunnel's key, "project/<name>" or "deployment/<name>".
	Scope string `json:"scope,omitempty"`
	// Hosts are the ingress hostnames reachable through the tunnel.
	Hosts []string `json:"hosts,omitempty"`
	// HostMode is the mode actually in effect, which for HostModeAuto is the one
	// the server resolved to.
	HostMode string `json:"hostMode,omitempty"`
	// Target says what the tunnel is fronting: "controller" for a real cluster
	// ingress controller, "mux" for the server's own routing table.
	Target string `json:"target,omitempty"`
}
