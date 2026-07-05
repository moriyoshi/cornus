// Package creddelivery is cornus's container-facing seam for handing a brokered
// credential to a workload. It is deliberately PROVIDER-AGNOSTIC: the default
// surface is a cornus-native HTTP endpoint plus plain file materialization, and
// any cloud-specific shape (AWS IMDS today; GCP / Azure later) is an
// interchangeable adapter registered under a name — never the surface itself.
//
// An Endpoint serves one credential over HTTP, fetching a fresh value through
// the get callback (which, in the caretaker, relays back to the client source).
// A provider also declares how the app discovers it: Env returns the environment
// variables to advertise a loopback bind, and WellKnownAddr returns a canonical
// address (e.g. AWS 169.254.169.254) an SDK may reach with no env at all.
//
// Backends register from an init function; a blank import wires them in, exactly
// like pkg/credential and pkg/tunnel.
package creddelivery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"

	"cornus/pkg/credential"
)

// Getter returns a fresh credential for one delivery request. It is the seam the
// caretaker fills with a relay call back to the client source.
type Getter func(context.Context) (credential.Credential, error)

// Endpoint serves one credential to the container over HTTP.
type Endpoint interface {
	// Serve runs the HTTP server on ln until ctx is cancelled, answering each
	// request by calling get for a fresh credential. It returns a non-nil error
	// only on an unexpected failure (a clean shutdown via ctx returns nil).
	Serve(ctx context.Context, ln net.Listener, get Getter) error
	// Env returns the environment variables to inject into the APP container so
	// its client/SDK finds this endpoint bound at addr (host:port). name is the
	// credential's logical name, so multiple sources get distinct variables.
	// Empty when the provider relies solely on a well-known address.
	Env(name, addr string) map[string]string
	// WellKnownAddr returns the canonical/link-local "host:port" this provider is
	// conventionally reached at (e.g. "169.254.169.254:80"), or "" if it has none.
	WellKnownAddr() string
}

// Factory constructs an Endpoint provider from non-secret config (empty/nil for
// providers that take none). cfg mirrors the CredentialDelivery's kind-specific
// knobs — e.g. the auth-proxy providers read cfg["upstream"] to target an
// API-compatible gateway instead of the vendor's default endpoint.
type Factory func(cfg map[string]string) (Endpoint, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// DefaultProvider is the neutral provider used when none is named.
const DefaultProvider = "generic"

// Register makes an endpoint provider available under name (called from init()).
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("creddelivery: duplicate provider registration for " + name)
	}
	registry[name] = f
}

// Open constructs the named endpoint provider ("" means DefaultProvider) with the
// given non-secret config (nil is fine for providers that take none).
func Open(name string, cfg map[string]string) (Endpoint, error) {
	if name == "" {
		name = DefaultProvider
	}
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown credential delivery provider %q (available: %v)", name, Providers())
	}
	return f(cfg)
}

// Providers lists the registered provider names, sorted, for diagnostics.
func Providers() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
