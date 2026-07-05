// Package-local translation of the agent's ingress inventory for the web BFF.
//
// This file is all that remains of the agent hosting web UIs. It did so for one
// reason — that was where the conduit lived — and once a conduit became a
// rendezvous at an address, `cornus web` could host its own BFF and publish it
// into whichever conduit is serving. See the JOURNAL entry "the conduit rendezvous design, as built".
package clientagent

import (
	"cornus/cmd/cornus/internal/webbff"
)

// ToBFFIngress restates an ingress inventory in the BFF's own vocabulary. The two
// shapes are field-identical and stay so deliberately: this package HOSTS webbff,
// so webbff cannot import it back and the structs cannot be shared. It is exported
// because the out-of-process AgentView (`cornus web`'s socketAgentView) needs the
// same translation on the far side of the control socket, and two hand-written
// copies would be free to drift.
func ToBFFIngress(in []AgentIngress) []webbff.AgentIngress {
	if len(in) == 0 {
		return nil
	}
	out := make([]webbff.AgentIngress, 0, len(in))
	for _, e := range in {
		w := webbff.AgentIngress{Mode: e.Mode, Domain: e.Domain, Trust: e.Trust}
		if c := e.Controller; c != nil {
			w.Controller = &webbff.AgentIngressController{
				KubeContext: c.KubeContext,
				Namespace:   c.Namespace,
				Service:     c.Service,
				HTTPPort:    c.HTTPPort,
				HTTPSPort:   c.HTTPSPort,
			}
		}
		out = append(out, w)
	}
	return out
}
