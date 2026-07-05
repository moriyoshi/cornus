package kubernetes

// The kubernetes realization of deploy.FSOperator.
//
// This backend's archive trio is unsupported outright — PodExecOptions cannot express a
// tar pack or unpack, and there is no /proc/<pid>/root to reach from here — so a
// volume-backed path in a pod can be LISTED (exec works) and then neither read, written,
// nor copied. FSOp is the way in: the caretaker sidecar already holds a pod-scoped
// connection to this server, and addFSOpRole mounts the app's volumes into it.
//
// Everything about which paths are servable is the caretaker's answer, not this
// backend's: the pod's config declares its roots and refuses anything else with
// FSErrUnsupported. Guessing here would mean two places that must agree about a pod's
// mount table, one of which cannot see it.

import (
	"context"
	"io"

	"cornus/pkg/api"
	"cornus/pkg/deploywire"
	"cornus/pkg/remotecompanion"
)

// FSOp runs one structured filesystem operation against a deployment's caretaker.
//
// Every refusal here is api.FSErrUnsupported with a NIL error, which is the contract:
// the caller is meant to fall back to relaying the bytes itself, and a transport error
// would instead surface to the user as a failure. A pod whose caretaker has not connected
// yet is exactly that case — the workload still works, the fast path just is not
// available.
func (b *Backend) FSOp(ctx context.Context, name string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	if b.companions == nil {
		return api.FSOpUnsupported("this server has no caretaker registry"), nil
	}
	// The same instance key the caretaker declares (addFSOpRole). Built by the shared
	// constructor so the two ends cannot drift into two spellings of "name/0".
	instance := remotecompanion.AgentRelayInstance(name)
	sess := b.companions.Get(instance)
	if sess == nil {
		return api.FSOpUnsupported("no caretaker is connected for " + instance), nil
	}
	return deploywire.FSOp(ctx, sess, req, body, out)
}
