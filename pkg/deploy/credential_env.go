package deploy

import (
	"fmt"
	"sort"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/egresspolicy"
)

// ServerDelivers is what THIS deploy's backend lets the server realize on its
// own, without a process running beside the workload.
//
// It is a struct rather than a pair of bools on purpose. The two capabilities
// are independent and they split the backends in OPPOSITE directions —
// containerd can be entered but (today) has no server-written bind, while a
// remote dockerhost has neither — so the arguments genuinely differ per call.
// Two adjacent bools at a call site is the shape where a swap compiles, passes
// review, and silently routes a delivery to the wrong place.
type ServerDelivers struct {
	// Files: the backend resolves paths this server writes, so a rendered file
	// can be an ordinary read-only bind (CredentialBinder).
	Files bool
	// Endpoints: the workload's network namespace is one this server can enter,
	// so a listener can be bound inside it (CredentialEndpointBinder).
	Endpoints bool
}

// NeedsCaretaker reports whether d must be served by a process running alongside
// the workload for its whole lifetime, given what this deploy's backend lets the
// server do itself.
//
// This is THE discriminator for the whole credential path — the server's split of
// a source's deliveries, its decision whether a companion is needed at all, and
// each backend's refusal of what it cannot serve all route through it. They were
// separate `Kind ==` comparisons once, which is exactly the shape that lets one
// drift and silently disagree with the others.
//
// The capabilities are parameters rather than constants because the answers
// genuinely differ by backend: kubernetes has a caretaker in the pod and keeps
// using it for everything; a host backend needs one only for what it cannot
// reach. A zero ServerDelivers reproduces the kubernetes behaviour exactly.
func NeedsCaretaker(d api.CredentialDelivery, can ServerDelivers) bool {
	switch d.Kind {
	case "env":
		// Fixed at container start; the server resolved it at deploy time.
		return false
	case "file":
		return !can.Files
	default:
		// "" means endpoint (api.CredentialDelivery). An endpoint is a listener
		// INSIDE the workload's network namespace — which is exactly why this
		// used to be unconditionally true. It is not: a server that can enter
		// that namespace binds the listener there itself, and the namespace
		// boundary remains the whole of the authorization either way, so the
		// guarantee is the caretaker's rather than a weaker substitute for it.
		return !can.Endpoints
	}
}

// DeliveryKindName is d's kind as an operator should see it in a message.
// api.CredentialDelivery documents that an empty Kind means "endpoint", so a
// spec that omitted `kind:` must be told the name of what it got rather than an
// empty string in the middle of a sentence.
func DeliveryKindName(d api.CredentialDelivery) string {
	if d.Kind == "" {
		return "endpoint"
	}
	return d.Kind
}

// SpecCaretakerKinds returns the sorted, distinct delivery kinds spec declares
// that still need a caretaker under this backend's capability. Empty means the
// server can realize the whole credential set itself — no caretaker, and
// therefore no CORNUS_ADVERTISE_URL and no CORNUS_AGENT_IMAGE.
//
// It reads the SPEC because the decision has to be made before the deploy is
// routed to a backend, which is upstream of where deliveries get split into
// AttachCredential.Deliveries / .EnvVars / .Files.
func SpecCaretakerKinds(spec api.DeploySpec, can ServerDelivers) []string {
	if spec.Credentials == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, src := range spec.Credentials.Sources {
		for _, d := range src.Deliveries {
			if NeedsCaretaker(d, can) {
				seen[DeliveryKindName(d)] = true
			}
		}
	}
	return sortedKeys(seen)
}

// CredentialRuntimeKinds is SpecCaretakerKinds for the post-split form a
// backend receives, so a backend that cannot serve a runtime delivery can say
// which kind it choked on. Empty means the attachment is env-only.
func CredentialRuntimeKinds(creds []AttachCredential) []string {
	seen := map[string]bool{}
	for _, c := range creds {
		for _, d := range c.Deliveries {
			seen[DeliveryKindName(d)] = true
		}
	}
	return sortedKeys(seen)
}

// sortedKeys returns set's keys in sorted order, or nil when empty — so a caller
// can use the result both as a list and as an "is there anything" predicate.
func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WithCredentialEnv returns a copy of spec with every deploy-time-resolved
// credential env delivery merged into spec.Env, for the host backends. It is the
// whole of realizing an env-kind credential there: the value was fetched from the
// client at deploy time, and a host backend's container env is set at create, so
// there is nothing left to serve and no companion to serve it with.
//
// Unlike kubernetes, which routes these through a Secret + secretKeyRef
// (addCredentialEnvVars / applyCredentialEnvSecret), a host backend has no Secret
// indirection to hide behind — the value lands in the container's config and is
// readable by anyone who can talk to the daemon. That is inherent to the delivery
// KIND, not to this implementation (api.CredentialDelivery.EnvVar says as much
// and points at the file/endpoint deliveries instead), so it is documented rather
// than worked around.
//
// It ERRORS on a collision with the egress proxy variables instead of merging.
// In proxy mode the caretaker proxy is authoritative and its vars deliberately
// overwrite caller-set ones (withEgressProxyEnv), so a credential delivered into
// HTTPS_PROXY would be silently discarded at container start — the workload would
// come up healthy, with the wrong value, and nothing in any log would say so.
// Failing the deploy is the only outcome that cannot be mistaken for success.
func WithCredentialEnv(spec api.DeploySpec, creds []AttachCredential) (api.DeploySpec, error) {
	vars := make(map[string]string)
	for _, c := range creds {
		for _, ev := range c.EnvVars {
			if prev, dup := vars[ev.Var]; dup && prev != ev.Value {
				return spec, fmt.Errorf("credential env delivery for %s is declared twice with different values", ev.Var)
			}
			vars[ev.Var] = ev.Value
		}
	}
	if len(vars) == 0 {
		return spec, nil
	}
	// Only proxy mode injects the proxy vars; transparent captures at the netns
	// and sets no env, so a name that is merely proxy-SHAPED is fine there.
	if spec.Egress != nil && spec.Egress.Mode == "proxy" {
		if owned := egresspolicy.ProxyEnvSubset(vars); len(owned) > 0 {
			names := make([]string, 0, len(owned))
			for k := range owned {
				names = append(names, k)
			}
			sort.Strings(names)
			return spec, fmt.Errorf(
				"credential env delivery collides with client-side egress in proxy mode: %v %s set by the egress proxy and would be overwritten at container start; rename the variable or use a file/endpoint delivery",
				names, plural(len(names)))
		}
	}
	env := make(map[string]string, len(spec.Env)+len(vars))
	for k, v := range spec.Env {
		env[k] = v
	}
	for k, v := range vars {
		env[k] = v
	}
	spec.Env = env
	return spec, nil
}

// RealizeCredentials is the whole of a host backend's credential story, shared by
// dockerhost/podman, containerd and bare so the three cannot drift: refuse any
// delivery still needing a caretaker, merge the deploy-time-resolved env
// deliveries into the container environment, and drop the spec's credential
// block once they are realized.
//
// File deliveries need nothing here: the server prepares their directory under
// its mounts dir and adds an ordinary read-only entry to spec.Mounts before the
// backend ever sees the spec, so a backend realizes them as it realizes any bind.
//
// Dropping the block is not tidiness. warnUnsupported logs "the workload sees
// none of the declared credentials" whenever spec.Credentials is set; leaving it
// in place would make a deploy that DID receive its credentials log that it had
// not, which is worse than the silence it replaced.
//
// unsupported explains, in the backend's own terms, why a remaining kind cannot
// be served here — appended to the refusal so an operator reads a cause rather
// than only a kind. "credentials are not supported" would be false now that env
// and (where the backend can place bytes) file delivery work.
func RealizeCredentials(spec api.DeploySpec, creds []AttachCredential, backend, unsupported string) (api.DeploySpec, error) {
	if len(creds) == 0 {
		return spec, nil
	}
	if kinds := CredentialRuntimeKinds(creds); len(kinds) > 0 {
		return spec, fmt.Errorf(
			"client-sourced credentials with %s delivery are not yet supported by the %s backend: %s",
			strings.Join(kinds, "/"), backend, unsupported)
	}
	spec, err := WithCredentialEnv(spec, creds)
	if err != nil {
		return spec, err
	}
	spec.Credentials = nil
	return spec, nil
}

// plural picks the verb for a list of n names, so the collision error reads as a
// sentence for both one and several.
func plural(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
