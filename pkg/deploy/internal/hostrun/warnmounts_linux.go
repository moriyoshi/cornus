//go:build linux

package hostrun

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"cornus/pkg/api"
)

// WarnUnmappableStorageOptions warns for the mount and volume SUB-fields the
// host backends cannot honor. Both call it from their apply prelude.
//
// These sit one level below `api.DeploySpec`, so the per-field reflection guards
// each backend gained on 2026-07-29 cannot see them: those assert that every
// DeploySpec FIELD is mapped or warned, and `Mounts` and `Volumes` are both
// mapped. The options inside them were dropped in silence anyway.
//
// It lives in hostrun rather than in each backend because the drops happen HERE —
// InstanceMounts builds the same OCI mounts and the same plain-host-directory
// volume backings for containerdhost and barehost, so both ignore exactly the
// same set. Two copies of this list would be two copies that can disagree, which
// is how spec.Hostname came to mean different things in the two backends.
//
// InstanceMounts itself takes no context and cannot log; changing its signature
// would touch every caller for no gain, since the prelude already has a logger,
// the whole spec, and runs before anything is created.
func WarnUnmappableStorageOptions(ctx context.Context, log *slog.Logger, spec api.DeploySpec) {
	// SELinux relabelling (compose `:z` / `:Z`) is a Docker-ism: dockerhost passes
	// the token through to the daemon, which relabels. OCIBindMount emits a plain
	// bind, so on an SELinux-enforcing host the container may simply be denied
	// access to a bind that works on dockerhost — a silent drop with a failure mode
	// far from its cause.
	var relabel []string
	for _, m := range spec.Mounts {
		if m.SELinux != "" {
			relabel = append(relabel, m.Target)
		}
	}
	if len(relabel) > 0 {
		log.WarnContext(ctx, "backend ignores the SELinux relabel option on bind mounts (compose `:z` / `:Z`); the bind is made without relabelling, so on an enforcing host the container may be denied access to it",
			"targets", strings.Join(sortedUnique(relabel), ", "))
	}

	// A volume here is a plain host directory. Size in particular is worth its own
	// line: asking for 1Gi and getting an unbounded directory is not a smaller
	// version of what was requested, it is a different guarantee.
	var sized, driven, labelled, classed []string
	for _, v := range spec.Volumes {
		name := v.Name
		if name == "" {
			name = v.Target
		}
		if v.Size != "" {
			sized = append(sized, name)
		}
		if v.Driver != "" || len(v.DriverOpts) > 0 {
			driven = append(driven, name)
		}
		if len(v.Labels) > 0 {
			labelled = append(labelled, name)
		}
		if v.StorageClass != "" {
			classed = append(classed, name)
		}
	}
	if len(sized) > 0 {
		log.WarnContext(ctx, "backend ignores volume size: a volume is backed by a plain host directory with no quota, so the workload can fill the host filesystem rather than hitting the requested limit",
			"volumes", strings.Join(sortedUnique(sized), ", "))
	}
	if len(driven) > 0 {
		log.WarnContext(ctx, "backend ignores volume driver / driverOpts (Docker volume-plugin metadata); the volume is a plain host directory whatever the driver asked for",
			"volumes", strings.Join(sortedUnique(driven), ", "))
	}
	if len(labelled) > 0 {
		log.WarnContext(ctx, "backend ignores volume labels; there is no volume object to carry them",
			"volumes", strings.Join(sortedUnique(labelled), ", "))
	}
	if len(classed) > 0 {
		log.WarnContext(ctx, "backend ignores volume storageClass (a kubernetes concept); the volume is a plain host directory",
			"volumes", strings.Join(sortedUnique(classed), ", "))
	}
}

// sortedUnique makes the warning's attribute deterministic, so the same spec
// always produces the same line.
func sortedUnique(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
