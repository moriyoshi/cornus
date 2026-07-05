package dockerhost

import (
	"context"
	"fmt"

	"cornus/pkg/deploy"
)

var _ deploy.IDMapper = (*Backend)(nil)

// IDMap implements deploy.IDMapper.
//
// Rootless podman is the case this exists for: its daemon runs as an ordinary
// user and maps container ids into that user's subuid allocation, so a file the
// server writes with a container-side uid lands owned by something the workload
// cannot see as its own. The daemon reports the mapping directly, which is why
// this reads it rather than deriving one from /etc/subuid.
//
// Rootful podman and Docker report no ranges, which is the identity — correct,
// because they run containers in the host's id space.
//
// The name is unused: podman's mapping is a property of the DAEMON, not of any
// one container, unlike incus where it is recorded per instance. The parameter
// stays because the interface serves both.
func (b *Backend) IDMap(ctx context.Context, name string) (deploy.IDMap, error) {
	if eng, ok := b.api.(*podmanEngine); ok {
		m, err := eng.idMappings(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: read id mappings: %w", b.tag(), err)
		}
		return m, nil
	}
	// Docker. userns-remap is the one configuration where this answer would be
	// wrong, and it is refused rather than guessed: see unmappableDockerUserns.
	if err := b.unmappableDockerUserns(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

// unmappableDockerUserns refuses a Docker daemon running with userns-remap.
//
// Such a daemon maps container ids into a remap user's subuid range exactly as
// rootless podman does, but does NOT report the mapping over its API — it lives
// in /etc/subuid for a user this process may not even be able to read. Answering
// the identity would write files owned by ids the workload cannot see, which is
// the failure this facility exists to remove, and it would report success while
// doing it. So the honest answer is an error, which turns credential file
// delivery into a named refusal on that configuration.
//
// A daemon without userns-remap runs containers in the host's id space and needs
// no mapping at all.
func (b *Backend) unmappableDockerUserns(ctx context.Context) error {
	on, err := b.api.usernsRemapped(ctx)
	if err != nil {
		// Could not ask. Do not invent a mapping either way: the caller treats
		// this as unmappable, which is the safe direction.
		return fmt.Errorf("%s: cannot determine whether this daemon remaps ids: %w", b.tag(), err)
	}
	if on {
		return fmt.Errorf("%s: this daemon runs with userns-remap, and does not report the "+
			"mapping over its API; a file this server owns cannot be given ids the workload "+
			"would see as its own", b.tag())
	}
	return nil
}
