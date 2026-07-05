//go:build linux

package incushost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// Managed volumes (DeploySpec.Volumes) on Incus.
//
// A managed volume differs from a bind Mount in that nothing exists to bind: the
// backend has to PROVISION the storage. On Incus that is a custom storage volume
// in the configured pool, attached with the same `disk` device shape the
// remote-mode agent volume already uses — `pool` plus a volume-name `source`
// (internal/server/device/disk.go:1090-1175, where a device with a `pool` is
// resolved through mountPoolVolume rather than treated as a host path). `size`
// and `security.shifted` are both valid config on a custom filesystem volume
// (internal/server/storage/utils.go:480-506), and `size` is applied
// best-effort: the dir driver logs and skips when the backing filesystem has no
// quota support (drivers/driver_dir_utils.go:105-112) rather than failing the
// create.
//
// The lifecycle follows the same docker-parity rule the host backends' volume
// store uses (pkg/deploy/internal/hostrun): a NAMED volume is shared and
// project-scoped, so it survives Delete and is only removed by
// `compose down --volumes` (deploy.VolumeRemover); an ANONYMOUS volume belongs
// to one replica of one deployment and is reaped when that deployment goes away.
//
// Reaping is the part with a leak in it, because Delete gets a NAME, not a spec:
// nothing at delete time knows which volumes an Apply created. That is why each
// replica carries the anonymous volume names it was created with in its own
// config (anonVolumesConfigKey), and deleteApp reads them off the instances
// BEFORE deleting them. The alternative — deriving names at delete time — cannot
// work: the target paths they hash are only in the spec.

const (
	// anonVolumesConfigKey stamps a replica with the comma-separated names of the
	// anonymous storage volumes created for it, so Delete can reap exactly those.
	anonVolumesConfigKey = configKeyPrefix + "cornus.volumes.anon"
	// namedVolumePrefix prefixes the incus volume backing a compose NAMED volume.
	// The logical name is never used verbatim: it is slugged and hashed, so a
	// compose name that incus's own validator would reject (over 64 characters,
	// containing a slash or one of its reserved URL characters — see
	// shared/validate/validate.go:563-597, applied to every volume create at
	// cmd/incusd/storage_volumes.go:678-682) still produces a usable volume, and
	// so the name RemoveVolume derives is byte-for-byte the one Apply created.
	namedVolumePrefix = "cornus-vol-"
)

// managedVolume is one resolved entry of spec.Volumes: the incus custom volume
// to provision and the disk device that attaches it.
type managedVolume struct {
	// Volume is the incus custom storage volume name.
	Volume string
	// Device is the instance device name the volume is attached under.
	Device string
	// Target is the container path it is mounted at.
	Target string
	// Size is the volume's `size` config value (a decimal byte count), or "".
	Size string
	// ReadOnly mounts it read-only.
	ReadOnly bool
	// Anonymous marks a volume this deployment owns outright, so Delete reaps it.
	Anonymous bool
}

// skippedVolume is one entry that could not be resolved, with the reason to warn
// about. Kept separate from the plan so the planner stays pure and the same plan
// can be built by Apply (to provision) and by buildDevices (to attach and warn)
// without warning twice.
type skippedVolume struct {
	Spec   api.VolumeSpec
	Reason string
}

// volumePlan resolves replica i's spec.Volumes into the volumes to provision and
// the entries that cannot be provisioned. It is pure: the same spec and replica
// always produce the same names, which is what lets Apply create exactly the
// volumes buildInstancesPost attaches.
func volumePlan(spec api.DeploySpec, i int) ([]managedVolume, []skippedVolume) {
	var plan []managedVolume
	var skipped []skippedVolume
	for vi, v := range spec.Volumes {
		target := filepath.Clean(v.Target)
		if !filepath.IsAbs(target) || target == "/" {
			skipped = append(skipped, skippedVolume{v, "an incus disk path must be an absolute path other than \"/\" (which names the instance's root disk); no volume is provisioned and nothing is mounted"})
			continue
		}
		mv := managedVolume{
			Device:    fmt.Sprintf("cornus-vol-%d", vi),
			Target:    target,
			ReadOnly:  v.ReadOnly,
			Anonymous: v.Name == "",
		}
		if v.Name == "" {
			mv.Volume = anonVolumeName(spec.Name, i, target)
		} else {
			mv.Volume = namedVolumeName(v.Name)
		}
		if !incusVolumeNameOK(mv.Volume) {
			skipped = append(skipped, skippedVolume{v, "the deployment name is too long to derive an incus volume name from (incus caps a volume name at 64 characters); no volume is provisioned and nothing is mounted"})
			continue
		}
		if v.Size != "" {
			n, ok := parseVolumeBytes(v.Size)
			if !ok {
				skipped = append(skipped, skippedVolume{v, "the requested size is not a byte quantity this backend can render for incus; the volume is provisioned WITHOUT a quota, so it can grow to fill the pool"})
				// Fall through: an unsized volume is far closer to the ask than none.
			} else {
				mv.Size = strconv.FormatInt(n, 10)
			}
		}
		plan = append(plan, mv)
	}
	return plan, skipped
}

// anonVolumeName names the volume backing an anonymous volume: one per replica
// per target, so two replicas never share private storage and two different
// targets in one replica never collide. The target is hashed rather than
// embedded because a container path contains slashes, which incus's volume-name
// validator rejects.
func anonVolumeName(app string, i int, target string) string {
	sum := sha256.Sum256([]byte(target))
	return fmt.Sprintf("cornus-%s-%d-vol-%s", app, i, hex.EncodeToString(sum[:])[:8])
}

// namedVolumeName names the volume backing a compose NAMED volume. It is a pure
// function of the logical name — the only input Delete/RemoveVolume ever has —
// and combines a readable slug (so `incus storage volume list` is legible) with
// a hash of the ORIGINAL name (so two logical names that slug the same, e.g.
// "app_cache" and "app-cache", never share storage).
func namedVolumeName(logical string) string {
	sum := sha256.Sum256([]byte(logical))
	return namedVolumePrefix + volumeSlug(logical) + "-" + hex.EncodeToString(sum[:])[:8]
}

// volumeSlug reduces an arbitrary logical volume name to a short lower-case
// [a-z0-9-] token, so the derived incus name always satisfies incus's validator
// regardless of what compose called the volume.
func volumeSlug(s string) string {
	var b strings.Builder
	prevDash := true // suppresses a leading dash
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && b.Len() < 30 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 30 {
		out = strings.Trim(out[:30], "-")
	}
	if out == "" {
		// A name made entirely of separators still needs a legal, stable token; the
		// hash appended by the caller is what keeps it unique.
		return "v"
	}
	return out
}

// incusVolumeNameOK reports whether a derived name satisfies the checks incusd
// runs on every storage-volume create (validate.IsAPIName, applied at
// cmd/incusd/storage_volumes.go:678-682): at most 64 characters, no whitespace,
// none of the reserved URL characters, and alphanumeric at both ends. Every name
// this package derives is built from [a-z0-9-] tokens, so only the length bound
// can realistically bite — but the check is written against incusd's rule rather
// than against that assumption, because a create rejected here is a whole failed
// deploy.
func incusVolumeNameOK(name string) bool {
	if len(name) < 2 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || strings.ContainsRune("?&+\"'`*/", r) {
			return false
		}
	}
	return isAlphanumeric(rune(name[0])) && isAlphanumeric(rune(name[len(name)-1]))
}

func isAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// parseVolumeBytes parses a VolumeSpec.Size into a byte count. It accepts the
// kubernetes-quantity spelling the field is documented with ("1Gi") as well as
// the SI and bare-byte forms, and returns the count as a plain integer so the
// value handed to incus needs no suffix table of its own — units.ParseByteSizeString,
// the parser behind the `size` property's IsSize validator, reads a suffix-less
// value with a multiplier of 1 (shared/units/units.go:23-57).
func parseVolumeBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
		{"kB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40},
		{"k", 1e3}, {"m", 1 << 20}, {"g", 1 << 30}, {"t", 1 << 40},
		{"B", 1},
	}
	digits, mult := s, int64(1)
	for _, sfx := range suffixes {
		if rest, ok := strings.CutSuffix(s, sfx.suffix); ok {
			digits, mult = rest, sfx.mult
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(digits), 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/mult {
		return 0, false
	}
	return n * mult, true
}

// ensureManagedVolumes provisions replica i's managed volumes before its
// instance is created (an instance whose disk device names a volume that is not
// there fails to start). Create-if-absent at the seam, so a named volume shared
// with another deployment — or one that survived a previous Delete, which is the
// whole point of a named volume — is reused rather than re-created.
func (b *Backend) ensureManagedVolumes(spec api.DeploySpec, i int) error {
	plan, _ := volumePlan(spec, i)
	for _, v := range plan {
		config := map[string]string{
			// Let instances with different id maps mount it, matching the agent
			// volume: replicas of one deployment, and the several deployments that
			// may share a named volume, do not necessarily map ids the same way.
			"security.shifted": "true",
			// Ownership metadata, so a stray volume is identifiable in
			// `incus storage volume list` without cross-referencing an instance.
			configKeyPrefix + deploy.LabelManaged: "true",
			configKeyPrefix + deploy.LabelApp:     spec.Name,
		}
		if v.Size != "" {
			config["size"] = v.Size
		}
		if err := b.conn.CreateVolume(b.pool, v.Volume, config); err != nil {
			return fmt.Errorf("incus: creating volume %q for %s replica %d in pool %q: %w", v.Volume, spec.Name, i, b.pool, err)
		}
	}
	return nil
}

// anonVolumeStamp renders the config value that records replica i's ANONYMOUS
// volume names on the instance itself, or "" when it has none. It is what makes
// Delete's reap possible: the names hash container paths that only ever existed
// in the spec, so nothing at delete time could recompute them.
func anonVolumeStamp(spec api.DeploySpec, i int) string {
	plan, _ := volumePlan(spec, i)
	var names []string
	for _, v := range plan {
		if v.Anonymous {
			names = append(names, v.Volume)
		}
	}
	return strings.Join(names, ",")
}

// anonVolumesOf reads the anonymous volume names back off an instance's config.
func anonVolumesOf(config map[string]string) []string {
	raw := config[anonVolumesConfigKey]
	if raw == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// deleteManagedVolumes removes the given anonymous volumes. Called after the
// instances are gone, because Incus refuses to delete an attached volume, and
// delete-if-exists at the seam so a partially-created deployment reaps cleanly.
func (b *Backend) deleteManagedVolumes(names []string) error {
	for _, name := range names {
		if err := b.conn.DeleteVolume(b.pool, name); err != nil {
			return fmt.Errorf("incus: deleting volume %q: %w", name, err)
		}
	}
	return nil
}

// RemoveVolume implements deploy.VolumeRemover for `compose down --volumes`:
// it removes the custom storage volume backing a NAMED, project-scoped volume.
// Anonymous volumes are not reachable here by design — they have no logical
// name — and are reaped by Delete instead.
//
// Delete-if-exists, per the interface contract: the volume never having been
// created (or having already been removed) is a success, which is what
// DeleteVolume gives at the seam.
func (b *Backend) RemoveVolume(_ context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("incus: removing volume: no volume name given")
	}
	volume := namedVolumeName(name)
	if err := b.conn.DeleteVolume(b.pool, volume); err != nil {
		return fmt.Errorf("incus: removing volume %q (incus volume %q) from pool %q: %w", name, volume, b.pool, err)
	}
	return nil
}

// volumeDevices appends the disk devices for replica i's managed volumes,
// warning per entry for anything that could not be provisioned.
func (b *Backend) volumeDevices(ctx context.Context, log *slog.Logger, spec api.DeploySpec, i int, claim func(map[string]string, string) bool) {
	plan, skipped := volumePlan(spec, i)
	for _, v := range plan {
		dev := map[string]string{
			"type":   "disk",
			"pool":   b.pool,
			"source": v.Volume,
			"path":   v.Target,
		}
		if v.ReadOnly {
			dev["readonly"] = "true"
		}
		claim(dev, v.Device)
	}
	for _, s := range skipped {
		log.WarnContext(ctx, "backend ignores a managed volume: "+s.Reason,
			"volume", s.Spec.Name, "target", s.Spec.Target, "size", s.Spec.Size)
	}
	// Compose volume metadata with no Incus analogue. Driver/DriverOpts select a
	// DOCKER volume plugin; incus chooses storage by pool, so there is nothing to
	// pass them to. Labels have no per-volume user-metadata equivalent that
	// survives the create either.
	for _, v := range spec.Volumes {
		if v.Driver != "" || len(v.DriverOpts) > 0 {
			log.WarnContext(ctx, "backend ignores a managed volume's driver options: those select a docker volume plugin, and incus picks storage by pool instead; the volume is provisioned on the configured incus pool",
				"volume", v.Name, "target", v.Target, "driver", v.Driver, "pool", b.pool)
		}
		if len(v.Labels) > 0 {
			log.WarnContext(ctx, "backend ignores a managed volume's labels: cornus stamps its own ownership metadata on the incus volume and has no place for user labels alongside it; the labels are not recorded anywhere",
				"volume", v.Name, "target", v.Target)
		}
	}
}
