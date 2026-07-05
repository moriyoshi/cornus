package dockerhost

// Flavor names which runtime this Backend drives.
//
// The package hosts two engines behind one orchestration (see engine_iface.go),
// so a single Backend type serves both Docker and Podman. Flavor is what the
// operator-facing surfaces read: Name(), which lands in DeployStatus.Backend and
// ServerInfo.Backend, and the prefix on every error this package emits.
//
// It exists because getting this wrong is silent rather than loud. A Podman
// server whose errors all say "dockerhost:" and whose status reports "dockerhost"
// sends an operator to read Docker documentation about a Docker daemon that is
// not running, and nothing anywhere contradicts them.

import "fmt"

// Flavor values double as the backend NAME, so Name() can return one directly
// and the CORNUS_DEPLOY_BACKEND vocabulary stays in one place.
type Flavor string

const (
	// FlavorDocker drives a Docker daemon over the Engine REST API. It is the
	// zero value's meaning — see tag().
	FlavorDocker Flavor = "dockerhost"
	// FlavorPodman drives Podman over its native libpod REST API.
	FlavorPodman Flavor = "podman"
)

// WithFlavor selects the runtime this Backend drives.
//
// Omitting it leaves the zero Flavor, which reads as FlavorDocker. That is
// load-bearing rather than incidental: every existing construction site — the
// server factory, the local CLI, SelfInspector, and a long tail of tests — builds
// a Backend without mentioning a flavor and must keep behaving exactly as before.
func WithFlavor(f Flavor) Option {
	return func(b *Backend) { b.flavor = f }
}

// errf builds an error prefixed with this Backend's flavor — "dockerhost: ..."
// or "podman: ...".
//
// It exists so the 31 call sites read `b.errf("...")` instead of threading
// `b.tag()` through every argument list as a leading `%s`. That is not just
// tidiness: prepending an argument to 31 existing fmt.Errorf calls, several of
// which already carry %w and positional values, is a mechanical edit with 31
// chances to mis-order an argument, and a mis-ordered %w silently stops wrapping
// — which would break errors.Is on deploy.ErrNotFound with nothing failing to
// compile. Substituting the call NAME touches no argument at all.
//
// Wrapping still works: this is fmt.Errorf underneath, so %w behaves normally.
func (b *Backend) errf(format string, args ...any) error {
	return fmt.Errorf(b.tag()+": "+format, args...)
}

// tag is the flavor as it appears to an operator: the value of Name() and the
// prefix on this package's errors.
//
// The empty Flavor maps to FlavorDocker here, in ONE place, rather than being
// normalized at construction. Normalizing in New() would leave every other
// constructor (and every test that builds a Backend literal) able to produce a
// Backend whose tag is "", which reads as an error message beginning with ": ".
func (b *Backend) tag() string {
	if b.flavor == "" {
		return string(FlavorDocker)
	}
	return string(b.flavor)
}
