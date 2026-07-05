package kubernetes

// The kubernetes realization of deploy.FSOperator.
//
// FSOp is the FAST path for a volume-backed path, and the only path that needs nothing
// from the app's image: the caretaker sidecar already holds a pod-scoped connection to
// this server, and addFSOpRole mounts the app's volumes into it. It reports real errnos,
// and a copy between two paths of one volume never leaves the pod.
//
// It is no longer the ONLY way in. archive.go added tar-over-exec, which reaches the
// container image as well — the thing the caretaker structurally cannot see, since it
// has its own mount namespace. The two are complementary and the planner prefers this
// one where it applies (fsplan.go): tar needs a tar in the app image, and this does not.
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
