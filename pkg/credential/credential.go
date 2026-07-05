// Package credential is cornus's client-side seam for minting the credentials a
// deployed workload retrieves on demand. A Source runs on the caller's machine
// (using the caller's own cloud/API credentials) and produces a neutral
// Credential — a bag of values plus an optional expiry — that the cornus server
// relays to the workload's caretaker sidecar for delivery.
//
// Backends register themselves under a name via Register (from an init function;
// a blank import of the backend package wires it in), exactly like pkg/tunnel.
// The model is deliberately cloud-agnostic: cloud specifics live only inside a
// named backend, never in this package or in the transport.
package credential

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Credential is a neutral, cloud-agnostic secret: named values plus an optional
// expiry. A Source produces it; a delivery (pkg/creddelivery) renders it for the
// container. The JSON form is the cornus-native contract the generic delivery
// endpoint serves, so keep the tags stable.
type Credential struct {
	Values     map[string]string `json:"values"`
	Expiration time.Time         `json:"expiration,omitempty"`
}

// Source mints or reads one credential on demand. Implementations run on the
// CLIENT for the lifetime of a deploy-attach session.
type Source interface {
	// Fetch returns a fresh credential. params carries optional per-request hints
	// from the consumer (usually empty); most backends ignore it.
	Fetch(ctx context.Context, params map[string]string) (Credential, error)
}

// Factory constructs a Source from its non-secret config map.
type Factory func(cfg map[string]string) (Source, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a source backend available under name. Backends call it from an
// init function. Registering the same name twice panics — two backends fighting
// for one name is a programming error, not a runtime condition.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("credential: duplicate backend registration for " + name)
	}
	registry[name] = f
}

// Open constructs the named backend with cfg. It errors when the name is unknown
// (typically because its package was not blank-imported, or a build tag gates it).
func Open(name string, cfg map[string]string) (Source, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown credential backend %q (available: %v)", name, Backends())
	}
	return f(cfg)
}

// Backends lists the registered backend names, sorted, for diagnostics.
func Backends() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
