//go:build linux

// Package hostrun holds the daemon-agnostic machinery shared by the host deploy
// backends that drive an OCI runtime without a container daemon — barehost
// (runc/crun/youki directly) and containerdhost (the containerd libraries, no
// dockerd). Both were built from the same code; this package is where the
// identical parts live so they are maintained once. It deliberately links ZERO
// github.com/moby/buildkit and no daemon client — only the containerd libraries
// and standard OCI types — so barehost stays lean (see its package doc).
package hostrun

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// SpecOpts assembles the OCI runtime spec options for one instance. backend is
// the log-group label of the calling backend ("bare"/"containerd"), id becomes
// the container hostname, img provides the image config (any oci.Image — a
// containerd image or an in-process content-store wrapper), netnsPath (when
// non-empty) joins the instance to its pinned network namespace, and mounts are
// the pre-resolved bind mounts. The individual opts touch only *specs.Spec /
// api.DeploySpec, so they are backend-independent; each backend feeds the result
// to its own spec generator (oci.GenerateSpec in-process for barehost,
// client.NewContainer(WithNewSpec(...)) for containerd).
func SpecOpts(ctx context.Context, backend, id string, spec api.DeploySpec, img oci.Image, netnsPath string, mounts []specs.Mount) []oci.SpecOpts {
	return append(imageArgOpts(spec, img), runtimeOpts(ctx, backend, id, spec, netnsPath, mounts)...)
}

// imageArgOpts applies the image config and the docker entrypoint/command
// semantics: an explicit entrypoint replaces the image's and drops the image CMD
// (unless a command is also given); an explicit command alone keeps the image
// entrypoint.
func imageArgOpts(spec api.DeploySpec, img oci.Image) []oci.SpecOpts {
	switch {
	case len(spec.Entrypoint) > 0:
		args := append(append([]string{}, spec.Entrypoint...), spec.Command...)
		return []oci.SpecOpts{oci.WithImageConfig(img), oci.WithProcessArgs(args...)}
	case len(spec.Command) > 0:
		return []oci.SpecOpts{oci.WithImageConfigArgs(img, spec.Command)}
	default:
		return []oci.SpecOpts{oci.WithImageConfig(img)}
	}
}

// runtimeOpts assembles the image-independent spec options (hostname, env, netns,
// mounts, privilege, resources). backend is the slog group label for the
// unsupported-field warnings.
func runtimeOpts(ctx context.Context, backend, id string, spec api.DeploySpec, netnsPath string, mounts []specs.Mount) []oci.SpecOpts {
	log := logging.FromContext(ctx, slog.Group(backend, "deployment", spec.Name))
	// The instance ID is the container's hostname (docker parity); the managed
	// /etc/hosts seeds a matching entry so it resolves inside the container. An
	// explicit compose `hostname` overrides that default.
	hostname := id
	if spec.Hostname != "" {
		hostname = spec.Hostname
	}
	opts := []oci.SpecOpts{oci.WithHostname(hostname)}
	if env := envList(spec); len(env) > 0 {
		opts = append(opts, oci.WithEnv(env))
	}
	// compose `user` (uid[:gid] or name[:group]): WithUser resolves any of those
	// forms against the image's /etc/passwd|group.
	if spec.User != "" {
		opts = append(opts, oci.WithUser(spec.User))
	}
	if spec.WorkingDir != "" {
		opts = append(opts, oci.WithProcessCwd(spec.WorkingDir))
	}
	if spec.TTY {
		opts = append(opts, oci.WithTTY)
	}
	if spec.ReadOnly {
		opts = append(opts, oci.WithRootFSReadonly())
	}
	if len(spec.CapAdd) > 0 {
		opts = append(opts, oci.WithAddedCapabilities(spec.CapAdd))
	}
	if len(spec.CapDrop) > 0 {
		opts = append(opts, oci.WithDroppedCapabilities(spec.CapDrop))
	}
	// compose `group_add`: only NUMERIC GIDs can be expressed at spec-build time
	// (a group name would need resolution against the image's /etc/group).
	if gids := numericGIDs(ctx, backend, spec.Name, spec.GroupAdd); len(gids) > 0 {
		opts = append(opts, withAdditionalGIDs(gids))
	}
	if len(spec.Sysctls) > 0 {
		opts = append(opts, withSysctls(spec.Sysctls))
	}
	// compose `security_opt`: only `no-new-privileges[:true]` maps to an OCI field;
	// `label=`/`seccomp=`/`apparmor=` need runtime-specific structures and are warned.
	for _, so := range spec.SecurityOpt {
		if so == "no-new-privileges" || so == "no-new-privileges:true" {
			opts = append(opts, oci.WithNoNewPrivileges)
			continue
		}
		log.WarnContext(ctx, "security_opt is not mapped to the OCI spec; only no-new-privileges is supported", "security_opt", so)
	}
	if len(spec.Ulimits) > 0 {
		opts = append(opts, withRlimits(spec.Ulimits))
	}
	if len(spec.Tmpfs) > 0 {
		opts = append(opts, oci.WithMounts(tmpfsMounts(spec.Tmpfs)))
	}
	for _, d := range spec.Devices {
		host, container, perms := parseDevice(d)
		opts = append(opts, oci.WithDevices(host, container, perms))
	}
	if spec.ShmSize > 0 {
		opts = append(opts, withShmSize(spec.ShmSize))
	}
	// compose `pid` / `ipc`: only the "host" form maps to an OCI change (leaving the
	// namespace so the container joins the host's). Other forms are warned about.
	if spec.PIDMode != "" {
		if spec.PIDMode == "host" {
			opts = append(opts, withoutNamespace(specs.PIDNamespace))
		} else {
			log.WarnContext(ctx, "pid mode is not mapped to the OCI spec; only \"host\" is supported", "pid", spec.PIDMode)
		}
	}
	if spec.IPCMode != "" {
		if spec.IPCMode == "host" {
			opts = append(opts, withoutNamespace(specs.IPCNamespace))
		} else {
			log.WarnContext(ctx, "ipc mode is not mapped to the OCI spec; only \"host\" is supported", "ipc", spec.IPCMode)
		}
	}
	if netnsPath != "" {
		opts = append(opts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: netnsPath,
		}))
	}
	if len(mounts) > 0 {
		opts = append(opts, oci.WithMounts(mounts))
	}
	if spec.Privileged {
		opts = append(opts, oci.WithPrivileged, oci.WithAllDevicesAllowed, oci.WithHostDevices)
	}
	if r := spec.Resources; r != nil {
		if r.MemoryLimit > 0 {
			opts = append(opts, oci.WithMemoryLimit(uint64(r.MemoryLimit)))
		}
		if r.CPULimit > 0 {
			// Fractional cores -> CFS quota over the standard 100ms period.
			opts = append(opts, oci.WithCPUCFS(int64(r.CPULimit*100000), 100000))
		}
	}
	return opts
}

// EnvList flattens spec.Env to sorted KEY=VALUE form (deterministic ordering).
func EnvList(spec api.DeploySpec) []string { return envList(spec) }

func envList(spec api.DeploySpec) []string {
	if len(spec.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+spec.Env[k])
	}
	return env
}

// OCIBindMount builds a read-write or read-only rbind OCI mount.
func OCIBindMount(src, dst string, readOnly bool) specs.Mount {
	opts := []string{"rbind"}
	if readOnly {
		opts = append(opts, "ro")
	} else {
		opts = append(opts, "rw")
	}
	return specs.Mount{Destination: dst, Type: "bind", Source: src, Options: opts}
}

// numericGIDs parses compose group_add entries into numeric supplementary GIDs.
// A group name is skipped with a warning rather than resolved against /etc/group.
func numericGIDs(ctx context.Context, backend, deployment string, groups []string) []uint32 {
	log := logging.FromContext(ctx, slog.Group(backend, "deployment", deployment))
	var out []uint32
	for _, g := range groups {
		n, err := strconv.ParseUint(g, 10, 32)
		if err != nil {
			log.WarnContext(ctx, "group_add entry is a group name, which AdditionalGids cannot express (only numeric GIDs); ignoring", "group", g)
			continue
		}
		out = append(out, uint32(n))
	}
	return out
}

func withAdditionalGIDs(gids []uint32) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Process == nil {
			s.Process = &specs.Process{}
		}
		s.Process.User.AdditionalGids = append(s.Process.User.AdditionalGids, gids...)
		return nil
	}
}

func withSysctls(sysctls map[string]string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Linux == nil {
			s.Linux = &specs.Linux{}
		}
		if s.Linux.Sysctl == nil {
			s.Linux.Sysctl = make(map[string]string, len(sysctls))
		}
		for k, v := range sysctls {
			s.Linux.Sysctl[k] = v
		}
		return nil
	}
}

func withRlimits(ulimits []api.Ulimit) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Process == nil {
			s.Process = &specs.Process{}
		}
		for _, u := range ulimits {
			s.Process.Rlimits = append(s.Process.Rlimits, specs.POSIXRlimit{
				Type: "RLIMIT_" + strings.ToUpper(u.Name),
				Soft: uint64(u.Soft),
				Hard: uint64(u.Hard),
			})
		}
		return nil
	}
}

func tmpfsMounts(tmpfs []string) []specs.Mount {
	out := make([]specs.Mount, 0, len(tmpfs))
	for _, t := range tmpfs {
		path, optStr, _ := strings.Cut(t, ":")
		opts := []string{"nosuid", "nodev", "noexec"}
		if optStr != "" {
			opts = append(opts, strings.Split(optStr, ",")...)
		}
		out = append(out, specs.Mount{
			Destination: path,
			Type:        "tmpfs",
			Source:      "tmpfs",
			Options:     opts,
		})
	}
	return out
}

func withShmSize(size int64) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		sizeOpt := fmt.Sprintf("size=%d", size)
		for i, m := range s.Mounts {
			if m.Destination != "/dev/shm" {
				continue
			}
			kept := make([]string, 0, len(m.Options)+1)
			for _, o := range m.Options {
				if !strings.HasPrefix(o, "size=") {
					kept = append(kept, o)
				}
			}
			s.Mounts[i].Options = append(kept, sizeOpt)
			return nil
		}
		s.Mounts = append(s.Mounts, specs.Mount{
			Destination: "/dev/shm",
			Type:        "tmpfs",
			Source:      "shm",
			Options:     []string{"nosuid", "noexec", "nodev", "mode=1777", sizeOpt},
		})
		return nil
	}
}

func withoutNamespace(t specs.LinuxNamespaceType) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Linux == nil {
			return nil
		}
		filtered := s.Linux.Namespaces[:0]
		for _, ns := range s.Linux.Namespaces {
			if ns.Type != t {
				filtered = append(filtered, ns)
			}
		}
		s.Linux.Namespaces = filtered
		return nil
	}
}

// parseDevice splits a compose device mapping "host:container[:perms]" into its
// components. A missing container path defaults to the host path; missing perms
// default to "rwm" (read, write, mknod), matching Docker/Compose.
func parseDevice(s string) (host, container, perms string) {
	parts := strings.SplitN(s, ":", 3)
	host = parts[0]
	container = host
	perms = "rwm"
	if len(parts) >= 2 && parts[1] != "" {
		container = parts[1]
	}
	if len(parts) == 3 && parts[2] != "" {
		perms = parts[2]
	}
	return host, container, perms
}

// NetworkNames lists the spec's user-defined network names in order.
func NetworkNames(spec api.DeploySpec) []string {
	names := make([]string, 0, len(spec.Networks))
	for _, n := range spec.Networks {
		if n.Name != "" {
			names = append(names, n.Name)
		}
	}
	return names
}

// SpecAliases collects the spec's per-network aliases (network -> aliases), nil
// when the spec declares none.
func SpecAliases(spec api.DeploySpec) map[string][]string {
	var out map[string][]string
	for _, n := range spec.Networks {
		if n.Name == "" || len(n.Aliases) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[n.Name] = append(out[n.Name], n.Aliases...)
	}
	return out
}
