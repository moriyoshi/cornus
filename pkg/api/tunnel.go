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
