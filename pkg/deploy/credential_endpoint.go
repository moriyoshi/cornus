package deploy

import "context"

// CredentialEndpoint is one endpoint-kind delivery, resolved to the address the
// workload will reach it at. The SERVER binds and serves it; the backend's only
// part is naming the network namespace to bind in.
//
// Addr is settled before the container is created, because the app discovers the
// endpoint through environment variables that have to be in the create request.
// That is why assignment and binding are separate steps: nothing about the
// address depends on the workload existing yet.
type CredentialEndpoint struct {
	Name      string // credential's logical name, for provider Env keys
	Provider  string // creddelivery provider ("" = generic)
	Addr      string // host:port INSIDE the workload's netns
	WellKnown bool   // Addr is the provider's canonical/link-local address
	Upstream  string // provider-specific upstream, when the delivery names one
}

// CredentialEndpointBinder is a backend whose workloads live in a network
// namespace this server can enter, so an endpoint delivery can be served by the
// server itself instead of by a caretaker inside the workload.
//
// The division of labour is the point. Serving a credential needs the
// deploy-attach session, which only the server holds; binding the listener where
// the workload can reach it needs to know that workload's network namespace,
// which only the backend knows. Neither half needs a process running beside the
// workload — which is what makes the caretaker, and with it CORNUS_ADVERTISE_URL
// and CORNUS_AGENT_IMAGE, unnecessary for this kind.
//
// The security model is inherited rather than invented: on kubernetes a
// credential endpoint is reachable because the caretaker shares the pod's
// namespace, and the namespace boundary is the whole of the authorization. A
// listener bound here is reachable by that workload and nothing else on the
// host, so the guarantee is the same one, not a weaker network-level substitute.
type CredentialEndpointBinder interface {
	Backend
	// BindsCredentialEndpoints reports whether this backend, AS CURRENTLY
	// CONFIGURED, runs workloads in a namespace this server can enter. False
	// sends endpoint deliveries back to a caretaker, or to a refusal on a
	// backend that has none.
	BindsCredentialEndpoints(ctx context.Context) bool
	// InstanceNetns returns a path naming replica's network namespace — a procfs
	// path or a bind-mount pin — in THIS server's mount namespace, ready to
	// open. It is called again on every rebind, so it must re-resolve rather
	// than answer from anything cached at create time: a restarted workload can
	// have a different namespace, and on the backends that pin one, a namespace
	// rebuilt by reboot recovery is a different object at the same path.
	//
	// An error means "not right now" rather than "never" — the caller retries.
	InstanceNetns(ctx context.Context, name string, replica int) (string, error)
}
