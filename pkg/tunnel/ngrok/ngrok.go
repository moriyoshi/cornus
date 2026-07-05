// Package ngrok implements the cornus tunnel.Provider on ngrok's in-process Go
// agent SDK (golang.ngrok.com/ngrok/v2). The server hosts the tunnel inside its
// own process — no subprocess, no external binary — using an authtoken supplied
// per tunnel (or a server default), and hands each inbound connection back to
// the caller to bridge to the workload.
//
// ngrok's terms permit embedding the agent in your own application to offer
// tunnels to your users when you maintain the ngrok account (or, with prior
// written consent, when each user brings their own). See the tunnel integration
// plan for the account/consent model.
package ngrok

import (
	"context"
	"errors"
	"net"

	ngrok "golang.ngrok.com/ngrok/v2"

	"cornus/pkg/tunnel"
)

func init() {
	tunnel.Register("ngrok", func() (any, error) { return provider{}, nil })
}

type provider struct{}

// Start authenticates a fresh agent with the supplied authtoken and opens a
// listener endpoint. Each tunnel gets its own agent so tunnels are isolated and
// tearing one down never touches another. ctx governs the agent connection, so
// callers pass a context that outlives the creating request.
func (provider) Start(ctx context.Context, cred tunnel.Credential, opts tunnel.Options) (tunnel.Session, error) {
	if cred.AuthToken == "" {
		return nil, errors.New("ngrok: no authtoken provided")
	}
	agent, err := ngrok.NewAgent(ngrok.WithAuthtoken(cred.AuthToken))
	if err != nil {
		return nil, err
	}
	var eopts []ngrok.EndpointOption
	if opts.Metadata != "" {
		eopts = append(eopts, ngrok.WithMetadata(opts.Metadata))
	}
	ln, err := agent.Listen(ctx, eopts...)
	if err != nil {
		_ = agent.Disconnect()
		return nil, err
	}
	return &session{agent: agent, ln: ln}, nil
}

type session struct {
	agent ngrok.Agent
	ln    ngrok.EndpointListener
}

func (s *session) URL() string {
	if u := s.ln.URL(); u != nil {
		return u.String()
	}
	return ""
}

func (s *session) Accept() (net.Conn, error) { return s.ln.Accept() }

// Close stops the endpoint and disconnects the agent from the ngrok service.
func (s *session) Close() error {
	err := s.ln.Close()
	_ = s.agent.Disconnect()
	return err
}
