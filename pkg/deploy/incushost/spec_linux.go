//go:build linux

package incushost

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/ingressroute"
	"cornus/pkg/logging"
)

// buildInstancesPost maps a DeploySpec to the Incus create request for replica i.
// Published host ports are realized as proxy devices and, per the cross-backend
// contract, are attached to replica 0 only (one DNAT target per host port);
// replicas 1+ get no port devices. Fields Incus cannot honor for an OCI
// application container are warned about per-field (never silently dropped).
func (b *Backend) buildInstancesPost(ctx context.Context, spec api.DeploySpec, i int) (incusapi.InstancesPost, error) {
	log := logging.FromContext(ctx, slog.Group("incus", "deployment", spec.Name))

	var credential *deploy.RegistryCredential
	if b.creds != nil {
		resolved, ok, err := b.creds(ctx, spec.Image)
		if err != nil {
			return incusapi.InstancesPost{}, fmt.Errorf("incus: resolve registry credential for %s: %w", spec.Image, err)
		}
		if ok {
			credential = &resolved
		}
	}
	src, err := imageSource(spec.Image, credential)
	if err != nil {
		return incusapi.InstancesPost{}, err
	}

	config := map[string]string{}
	// Ownership + app identity, stored in Incus's user.* metadata namespace.
	config[configKeyPrefix+deploy.LabelManaged] = "true"
	config[configKeyPrefix+deploy.LabelApp] = spec.Name
	// Provenance: the flat cornus.origin.* set, same keys as the other backends.
	for k, v := range deploy.OriginToLabels(spec.Origin) {
		config[configKeyPrefix+k] = v
	}
	// User labels (compose `labels:`) — cornus's own keys always win on a clash,
	// so apply user labels first.
	for k, v := range spec.Labels {
		config[configKeyPrefix+k] = v
	}
	config[configKeyPrefix+deploy.LabelManaged] = "true"
	config[configKeyPrefix+deploy.LabelApp] = spec.Name
	config[imageConfigKey] = spec.Image

	// Environment (compose `environment:`) → Incus environment.* config keys.
	for k, v := range spec.Env {
		config["environment."+k] = v
	}
	// Resource caps (compose `deploy.resources`) → Incus limits.* config keys.
	if r := spec.Resources; r != nil {
		if r.CPULimit > 0 {
			// limits.cpu accepts a fractional core count as "N" — Incus reads a
			// bare number as an allowance. Render with enough precision.
			config["limits.cpu.allowance"] = fmt.Sprintf("%d%%", int(r.CPULimit*100))
		}
		if r.MemoryLimit > 0 {
			config["limits.memory"] = strconv.FormatInt(r.MemoryLimit, 10)
		}
	}
	// Entrypoint override (compose `entrypoint:`) → oci.entrypoint, the same key
	// the companion uses to run `cornus caretaker`.
	//
	// Grounded in the vendored incus v6 source (there is no incusd here to try it
	// against): internal/instance/config.go registers "oci.entrypoint" as a valid
	// instance key ("Override the entry point of an OCI container", condition: OCI
	// container, liveupdate: no), cmd/incusd/instance.go seeds it from the image's
	// config.json Process.Args ONLY when the create request left it empty, and
	// internal/server/instance/drivers/driver_lxc.go does
	//
	//	entrypoint := config.Process.Args
	//	if d.expandedConfig["oci.entrypoint"] != "" {
	//	    entrypoint, err = shellquote.Split(...)
	//	}
	//
	// before feeding it to lxc.execute.cmd. So the key REPLACES the whole image
	// argv, which is exactly cornus's entrypoint semantics (api.DeploySpec:
	// an explicit Entrypoint drops the image CMD and Command supplies its
	// arguments) — the same argv hostrun.imageArgOpts builds for bare/containerd.
	// A command-ONLY override asks for something else entirely (keep the image
	// ENTRYPOINT, replace only its arguments) and stays unsupported; see
	// warnUnsupported.
	if len(spec.Entrypoint) > 0 {
		argv := append(append([]string{}, spec.Entrypoint...), spec.Command...)
		config["oci.entrypoint"] = ociEntrypoint(argv)
	}
	// Working directory (compose `working_dir`) → oci.cwd, and a NUMERIC user
	// (compose `user`) → oci.uid / oci.gid. These are oci.entrypoint's siblings:
	// all four are declared together in internal/instance/config.go (oci.cwd at
	// :688-695, oci.gid at :697-704, oci.uid at :706-713) and all four carry the
	// same override semantics on both ends of the daemon —
	//
	//   - cmd/incusd/instance.go seeds each from the image's own config.json
	//     (Process.Cwd :255-257, Process.User.UID :259-261, .GID :263-265) ONLY
	//     when the create request left the key empty, so setting it here means the
	//     image's value is never written; and
	//   - internal/server/instance/drivers/driver_lxc.go consumes each on start,
	//     preferring d.expandedConfig over the image config: oci.cwd →
	//     lxc.init.cwd (:2408-2418), oci.uid → lxc.init.uid (:2421-2431), oci.gid
	//     → lxc.init.gid (:2434-2444).
	//
	// Both writes are guarded by what the key's validator accepts, because incusd
	// rejects the whole create otherwise: oci.cwd is
	// validate.Optional(validate.IsAbsFilePath) (a filepath.IsAbs check,
	// shared/validate/validate.go:718-725) and oci.uid/oci.gid are
	// validate.Optional(validate.IsUint32) (strconv.ParseUint(v, 10, 32),
	// :93-101). A relative workingDir and a username-form user are therefore
	// unexpressible rather than merely unimplemented; both keep warning, and say
	// so. See warnUnsupported.
	if spec.WorkingDir != "" && filepath.IsAbs(spec.WorkingDir) {
		config["oci.cwd"] = spec.WorkingDir
	}
	if uid, gid, ok := ociNumericUser(spec.User); ok {
		config["oci.uid"] = uid
		// A uid-only `user:` leaves oci.gid unset on purpose, so incusd still seeds
		// it from the image's declared group — the same "override only what was
		// asked for" behavior the other backends give a uid-only compose `user`.
		if gid != "" {
			config["oci.gid"] = gid
		}
	}

	// Kernel parameters (compose `sysctls`) → Incus linux.sysctl.<name>.
	//
	// The key is container-only and validate.IsAny
	// (internal/instance/config.go:1641-1644), and incusd CONSUMES it in
	// internal/server/instance/drivers/driver_lxc.go:1316-1332, which turns every
	// linux.sysctl.<name> in the expanded config into lxc.sysctl.<name>. Two names
	// are deliberately NOT written; see incusOwnedSysctls.
	for _, name := range sortedKeys(spec.Sysctls) {
		if key, ok := incusSysctlKey(name); ok {
			config[key] = spec.Sysctls[name]
		}
	}
	// Process rlimits (compose `ulimits`) → Incus limits.kernel.<name>.
	//
	// Same shape as the sysctls above: internal/instance/config.go:1515-1640
	// validates the whole limits.kernel.* family, and driver_lxc.go:1302-1313
	// consumes it into lxc.prlimit.<name>. Only the limit names incus's own
	// validator enumerates are written (incusKernelLimits) and only when the
	// bounds are orderable; see incusUlimit.
	for _, u := range spec.Ulimits {
		if key, value, ok := incusUlimit(u); ok {
			config[key] = value
		}
	}
	// The anonymous storage volumes provisioned for this replica, recorded on the
	// instance so Delete can reap exactly those. See volumes_linux.go.
	if stamp := anonVolumeStamp(spec, i); stamp != "" {
		config[anonVolumesConfigKey] = stamp
	}
	// Privileged (policy-gated in Apply) → security.privileged.
	if spec.Privileged {
		config["security.privileged"] = "true"
	}
	// Restart policy → Incus boot.autorestart (restart the instance if it stops
	// unexpectedly). "no" leaves it off; every other policy ("always",
	// "unless-stopped", "on-failure") maps to on. Incus has no attempt cap, so
	// RestartMaxAttempts is not expressible (documented, like containerd).
	if deploy.RestartPolicy(spec) != "no" {
		config["boot.autorestart"] = "true"
	}

	// Per-field warnings for spec knobs an Incus OCI application container cannot
	// take at create time. These are honored by the docker/k8s backends; surface
	// the gap rather than dropping it silently (cross-backend contract).
	b.warnUnsupported(ctx, log, spec)

	post := incusapi.InstancesPost{
		Name:   instanceName(spec.Name, i),
		Type:   incusapi.InstanceTypeContainer,
		Source: src,
		Start:  true,
	}
	post.Config = config
	post.Devices = b.buildDevices(ctx, log, spec, i)
	return post, nil
}

// sortedKeys returns a map's keys in ascending order, so both the config the
// backend builds and the warnings it emits are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sysctlNamePattern is the shape a compose `sysctls:` name must have before it
// is spliced into an Incus config key. incusd validates linux.sysctl.* with
// validate.IsAny (internal/instance/config.go:1641-1644), i.e. it checks
// NOTHING about the suffix — so this guard exists to keep an odd name from
// forging a different config key rather than to anticipate a rejection.
var sysctlNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_-]+)*$`)

// incusOwnedSysctls are the sysctls incusd sets ITSELF for an OCI application
// container. Both are written from startCommon's application-container branch
// (driver_lxc.go:2366-2375), which runs AFTER initLXC has emitted the
// linux.sysctl.* keys (driver_lxc.go:1316-1332), so for these two names incus's
// own value is the one liblxc applies last and a cornus-written key would be a
// key that silently does nothing. They are therefore skipped and warned about
// rather than written.
var incusOwnedSysctls = []string{
	"net.ipv4.ping_group_range",
	"net.ipv4.ip_unprivileged_port_start",
}

// incusSysctlKey maps a compose sysctl name to the Incus config key that sets
// it, returning ok=false for a name this backend will not write.
func incusSysctlKey(name string) (string, bool) {
	if !sysctlNamePattern.MatchString(name) || slices.Contains(incusOwnedSysctls, name) {
		return "", false
	}
	return "linux.sysctl." + name, true
}

// incusKernelLimits are the rlimit names this backend maps onto
// limits.kernel.<name>. It is exactly the set incusd's own validator enumerates
// (internal/instance/config.go:1515-1634); the trailing catch-all at :1636
// accepts any other suffix at CREATE time, but the value only ever reaches
// liblxc as lxc.prlimit.<name> (driver_lxc.go:1302-1313), which resolves the
// name against its own RLIMIT table when the instance STARTS. A name outside
// this set would therefore be accepted by the create and then break the start —
// and this backend creates instances with Start:true, so that is a failed
// deploy. Staying inside the documented set keeps an unmappable limit a warning
// instead.
var incusKernelLimits = []string{
	"as", "core", "cpu", "data", "fsize", "locks",
	"memlock", "nice", "nofile", "nproc", "rtprio", "sigpending",
}

// incusUlimit renders one compose ulimit as the limits.kernel.<name> key and
// value Incus takes, returning ok=false for anything unmappable.
//
// The value form is liblxc's: a single number sets both bounds, "soft:hard"
// sets them separately, and "unlimited" is the infinity spelling — which is what
// a NEGATIVE bound means in the docker/compose model this spec follows. A soft
// bound above a finite hard bound is refused rather than written, because the
// kernel rejects it and the instance would fail to start.
func incusUlimit(u api.Ulimit) (key, value string, ok bool) {
	name := strings.ToLower(strings.TrimSpace(u.Name))
	if !slices.Contains(incusKernelLimits, name) {
		return "", "", false
	}
	// An unlimited soft bound under a finite hard bound is the same inversion as
	// soft > hard, just spelled with a negative.
	if u.Soft < 0 && u.Hard >= 0 {
		return "", "", false
	}
	if u.Soft >= 0 && u.Hard >= 0 && u.Soft > u.Hard {
		return "", "", false
	}
	render := func(v int64) string {
		if v < 0 {
			return "unlimited"
		}
		return strconv.FormatInt(v, 10)
	}
	if u.Soft == u.Hard {
		return "limits.kernel." + name, render(u.Soft), true
	}
	return "limits.kernel." + name, render(u.Soft) + ":" + render(u.Hard), true
}

// proxyDevice renders one published-port mapping as an Incus proxy device
// (host-side listener DNAT'd to the container port). Returns (nil, "") for a
// mapping with no host port (nothing to publish).
func proxyDevice(idx int, pm api.PortMapping) (map[string]string, string) {
	if pm.Host == 0 {
		return nil, ""
	}
	proto := strings.ToLower(pm.Protocol)
	if proto == "" {
		proto = "tcp"
	}
	hostIP := pm.HostIP
	if hostIP == "" {
		hostIP = "0.0.0.0"
	}
	name := fmt.Sprintf("cornus-port-%d", idx)
	return map[string]string{
		"type":    "proxy",
		"listen":  fmt.Sprintf("%s:%s:%d", proto, hostIP, pm.Host),
		"connect": fmt.Sprintf("%s:127.0.0.1:%d", proto, pm.Container),
		"bind":    "host",
	}, name
}

// ociNumericUser parses a compose `user` value into the uid and optional gid
// strings Incus's oci.uid / oci.gid keys accept, returning ok=false for anything
// they would reject.
//
// The accepted forms are "uid" and "uid:gid" where each component parses as a
// uint32 — deliberately the SAME check incusd applies (validate.IsUint32 is
// strconv.ParseUint(value, 10, 32)), so a value this returns ok for is a value
// the create request cannot be rejected over. That excludes a username or group
// name ("app", "1000:staff"), a negative id, and anything past 2^32-1. A partial
// mapping is not offered for "uid:groupname": honoring the uid while silently
// dropping the group would run the process in a group the operator did not ask
// for, which is worse than not honoring the field at all.
//
// Components are returned as strings rather than numbers because that is what
// goes into the config map, and re-formatting a parsed integer would silently
// normalize a value ("0007" -> "7") the operator wrote.
func ociNumericUser(user string) (uid, gid string, ok bool) {
	if user == "" {
		return "", "", false
	}
	uidStr, gidStr, hasGid := strings.Cut(user, ":")
	if _, err := strconv.ParseUint(uidStr, 10, 32); err != nil {
		return "", "", false
	}
	if hasGid {
		if _, err := strconv.ParseUint(gidStr, 10, 32); err != nil {
			return "", "", false
		}
		return uidStr, gidStr, true
	}
	return uidStr, "", true
}

// warnUnsupported emits one warning per set spec field the Incus OCI backend
// does not (yet) map, so an operator sees exactly what was ignored.
func (b *Backend) warnUnsupported(ctx context.Context, log *slog.Logger, spec api.DeploySpec) {
	// An entrypoint override IS honored (oci.entrypoint; see buildInstancesPost).
	// A command-only override is not: oci.entrypoint replaces the entire argv, so
	// expressing "keep the image's ENTRYPOINT, replace only its arguments" would
	// need the image's ENTRYPOINT/CMD split, which this backend never sees — incus
	// pulls the image itself, and the flattened Process.Args it exposes has already
	// lost the boundary between the two.
	if len(spec.Entrypoint) == 0 && len(spec.Command) > 0 {
		log.WarnContext(ctx, "backend ignores a command-only override: incus can only replace the whole argv, not the image entrypoint's arguments; set entrypoint too to override it", "command", spec.Command)
	}
	// A NUMERIC user IS honored (oci.uid/oci.gid). A username- or group-name-form
	// one is not, and cannot be: both keys are validate.IsUint32, and resolving a
	// name would need the image's /etc/passwd, which this backend never sees
	// (incus pulls the image itself). Kubernetes' securityContext has the same
	// numeric-only limit, and warns the same way.
	if spec.User != "" {
		if _, _, ok := ociNumericUser(spec.User); !ok {
			log.WarnContext(ctx, "backend ignores a username-form user: incus's oci.uid/oci.gid take numeric ids only, and the image's /etc/passwd is not visible here to resolve a name; use a numeric uid[:gid]", "user", spec.User)
		}
	}
	// An ABSOLUTE workingDir IS honored (oci.cwd). A relative one is not: oci.cwd
	// is validated with IsAbsFilePath, so incusd would reject the create outright
	// — and this backend cannot resolve it itself, because the image's own cwd
	// (what it would be relative to) is not visible here either.
	if spec.WorkingDir != "" && !filepath.IsAbs(spec.WorkingDir) {
		log.WarnContext(ctx, "backend ignores a relative workingDir: incus's oci.cwd must be an absolute path, and the image's own working directory is not visible here to resolve it against; use an absolute path", "workingDir", spec.WorkingDir)
	}
	// Per-entry sysctl and ulimit refusals. The mapped cases are written in
	// buildInstancesPost; these are the values that would not survive the trip.
	for _, name := range sortedKeys(spec.Sysctls) {
		if slices.Contains(incusOwnedSysctls, name) {
			log.WarnContext(ctx, "backend ignores a sysctl incus sets itself for an OCI container: incusd writes this one after the linux.sysctl.* keys, so its value is the one that lands; the workload gets incus's value, not this one",
				"sysctl", name, "value", spec.Sysctls[name])
			continue
		}
		if _, ok := incusSysctlKey(name); !ok {
			log.WarnContext(ctx, "backend ignores a sysctl whose name it will not splice into an incus config key; the kernel parameter keeps its default in the workload",
				"sysctl", name)
		}
	}
	for _, u := range spec.Ulimits {
		if _, _, ok := incusUlimit(u); ok {
			continue
		}
		if !slices.Contains(incusKernelLimits, strings.ToLower(strings.TrimSpace(u.Name))) {
			log.WarnContext(ctx, "backend ignores an rlimit incus does not document: limits.kernel.* reaches liblxc as lxc.prlimit.<name>, which resolves the name when the instance STARTS, so writing an undocumented one would trade a dropped limit for a deploy that never comes up; the process keeps the daemon's inherited limit",
				"ulimit", u.Name, "supported", strings.Join(incusKernelLimits, ","))
			continue
		}
		log.WarnContext(ctx, "backend ignores an rlimit whose soft bound exceeds its hard bound: the kernel rejects that pair and the instance would fail to start; the process keeps the daemon's inherited limit",
			"ulimit", u.Name, "soft", u.Soft, "hard", u.Hard)
	}
	// Container identity and lifecycle knobs with no Incus config key at all. Each
	// of these was checked against internal/instance/config.go's key table (the
	// whole set of keys an instance create may carry) and against how
	// driver_lxc.go builds the liblxc config for an application container.
	if spec.Hostname != "" {
		log.WarnContext(ctx, "backend ignores hostname: incus fixes an OCI container's hostname to its INSTANCE NAME (lxc.uts.name, plus the /etc/hostname and /etc/hosts it generates and bind-mounts over the rootfs on every start) and has no config key to override it; the workload's hostname is its instance name",
			"hostname", spec.Hostname, "actual", instanceName(spec.Name, 0))
	}
	if spec.StopSignal != "" {
		log.WarnContext(ctx, "backend ignores stopSignal: incus has no per-instance stop-signal key, and cornus stops an instance with a FORCED state change (force=true, timeout=0), which incusd turns into an immediate Stop rather than a signalled Shutdown; the workload's PID 1 is killed, not signalled",
			"stopSignal", spec.StopSignal)
	}
	if spec.StopGracePeriod != "" {
		log.WarnContext(ctx, "backend ignores stopGracePeriod: incus's only shutdown-timeout key (boot.host_shutdown_timeout) applies to daemon shutdown and cluster evacuation, not to an ordinary stop, and this backend's Stop is a forced one; the workload gets no grace period before it is killed",
			"stopGracePeriod", spec.StopGracePeriod)
	}
	if spec.ReadOnly {
		log.WarnContext(ctx, "backend ignores readOnly: a read-only root needs an overriding ROOT disk device, which incus requires to also name a storage pool (so it would move the instance's storage) and which incus's own code calls unlikely to work well; the workload's root filesystem stays writable")
	}
	if len(spec.CapAdd) > 0 {
		log.WarnContext(ctx, "backend ignores capAdd: an incus container has no per-capability config key — its capability set follows from security.privileged and security.nesting; the workload keeps the default set (use privileged for a workload that needs more)",
			"capAdd", spec.CapAdd)
	}
	if len(spec.CapDrop) > 0 {
		log.WarnContext(ctx, "backend ignores capDrop: an incus container has no per-capability config key, so capabilities cannot be narrowed below the default set; the workload keeps the default set",
			"capDrop", spec.CapDrop)
	}
	if len(spec.SecurityOpt) > 0 {
		log.WarnContext(ctx, "backend ignores securityOpt: incus's security.* keys are not docker's security-option vocabulary — there is no no-new-privileges, seccomp-profile or apparmor-profile key for an application container; the workload runs under incus's own default confinement",
			"securityOpt", spec.SecurityOpt)
	}
	if len(spec.GroupAdd) > 0 {
		log.WarnContext(ctx, "backend ignores groupAdd: incus's oci.gid sets the process's PRIMARY group and there is no supplementary-group key; the workload's supplementary groups are whatever the image gives it",
			"groupAdd", spec.GroupAdd)
	}
	if len(spec.ExtraHosts) > 0 {
		log.WarnContext(ctx, "backend ignores extraHosts: incusd generates an OCI container's /etc/hosts from the instance name and bind-mounts it over the rootfs on every start, so an entry added here would be overwritten; the workload sees only incus's generated hosts file",
			"extraHosts", spec.ExtraHosts)
	}
	if len(spec.DNSServers) > 0 {
		log.WarnContext(ctx, "backend ignores dnsServers: incusd owns an OCI container's /etc/resolv.conf — it writes the file from the instance's own DHCP lease and bind-mounts it over the rootfs — so a nameserver set here would be overwritten; the workload resolves through the incus network's resolver",
			"dnsServers", spec.DNSServers)
	}
	if len(spec.DNSSearch) > 0 {
		log.WarnContext(ctx, "backend ignores dnsSearch: incusd owns an OCI container's /etc/resolv.conf and rewrites it on every start; the workload gets only the search domains the incus network's DHCP lease supplies",
			"dnsSearch", spec.DNSSearch)
	}
	if len(spec.DNSOptions) > 0 {
		log.WarnContext(ctx, "backend ignores dnsOptions: incusd owns an OCI container's /etc/resolv.conf and rewrites it on every start; the workload's resolver runs with its own defaults",
			"dnsOptions", spec.DNSOptions)
	}
	if len(spec.Devices) > 0 {
		log.WarnContext(ctx, "backend ignores devices: mapping a host device needs an incus unix-char/unix-block device, which has to know the node's type and would hand the workload host hardware that hostpolicy does not gate (it gates bind sources and privileged, not devices); no host device is exposed to the workload",
			"devices", spec.Devices)
	}
	if spec.Init != nil {
		log.WarnContext(ctx, "backend ignores init: incus decides an OCI container's PID 1 itself — an image whose entrypoint is a real init runs it directly, anything else runs under liblxc's own init — and exposes no key to change that; the workload gets whichever of the two incus picks",
			"init", *spec.Init)
	}
	if spec.TTY {
		log.WarnContext(ctx, "backend ignores tty: incus gives a container no terminals of its own (it sets lxc.tty.max to 0) and an application container's PID 1 gets no pseudo-terminal; the workload's stdout and stderr go to the instance console log, unpaged and unechoed")
	}
	if spec.StdinOpen {
		log.WarnContext(ctx, "backend ignores stdinOpen: an incus instance's PID 1 has no held-open stdin (there is no create-time equivalent of docker's OpenStdin); a workload that reads stdin sees EOF — use `cornus exec` for an interactive session")
	}
	if spec.PIDMode != "" {
		log.WarnContext(ctx, "backend ignores pidMode: incus gives every container its own PID namespace and has no key to join another's (not even the host's); the workload sees only its own processes",
			"pidMode", spec.PIDMode)
	}
	if spec.IPCMode != "" {
		log.WarnContext(ctx, "backend ignores ipcMode: incus gives every container its own IPC namespace and has no key to join or share another's; the workload's System V IPC and POSIX message queues stay private to it",
			"ipcMode", spec.IPCMode)
	}
	if spec.RestartMaxAttempts > 0 {
		log.WarnContext(ctx, "backend ignores restartMaxAttempts: incus's boot.autorestart is a boolean with no attempt cap; a workload that keeps failing is restarted indefinitely rather than giving up",
			"restartMaxAttempts", spec.RestartMaxAttempts)
	}
	if r := spec.Resources; r != nil && (r.ReservedCPU > 0 || r.ReservedMemory > 0) {
		log.WarnContext(ctx, "backend ignores resource reservations: incus's limits.* keys are caps, not guaranteed floors, and there is no scheduler here to reserve capacity against; only the limits are applied",
			"reservedCpu", r.ReservedCPU, "reservedMemory", r.ReservedMemory)
	}
	// Caretaker-shaped features. Each needs an agent running INSIDE (or alongside)
	// the workload with a role this backend's companion does not carry; the
	// remote-mode companion carries only PortForward and AgentRelay.
	if spec.Telemetry != nil {
		log.WarnContext(ctx, "backend ignores telemetry: exporting it needs a caretaker collector attached to the workload, which this backend has no attachment path for; no OTLP endpoint is injected and nothing is exported")
	}
	if spec.Credentials != nil {
		log.WarnContext(ctx, "backend ignores credentials: brokered client-minted credentials are delivered by a caretaker (deploy.AttachingBackend), which this backend does not implement; no credential reaches the workload")
	}
	if spec.Egress != nil {
		log.WarnContext(ctx, "backend ignores egress: routing outbound traffic through a client-side vantage point needs a caretaker egress companion (deploy.EgressBackend), which this backend does not implement; the workload egresses straight from the incus host")
	}
	deploy.WarnKubernetesOnlyFields(ctx, log, spec, "CORNUS_INCUS_REMOTE")
	// The three predicates below are the canonical ones, matching the other host
	// backends: a DISABLED healthcheck (`healthcheck: {disable: true}`), a
	// CLIENT-EMULATED ingress (realized entirely on the client), and a knative
	// block that is present but not enabled all ask nothing of this backend, so
	// warning about them would report a failure to do something nobody requested.
	if hc := spec.Healthcheck; hc != nil && !hc.Disabled() {
		log.WarnContext(ctx, "backend ignores healthcheck: incus has no instance-level probe; a workload that goes unhealthy without exiting keeps being reported as running")
	}
	if ingressroute.Enabled(spec.Ingress) {
		log.WarnContext(ctx, "backend creates no cluster Ingress; the server serves this ingress itself (reach it with an ingress tunnel or CORNUS_INGRESS_LISTEN)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		log.WarnContext(ctx, "backend ignores knative (kubernetes-only feature); running as an ordinary container")
	}
	if len(spec.Networks) > 0 {
		// Wholesale, which is why no NetworkAttachment sub-field needs its own
		// warning here the way it does on the other host backends
		// (hostrun.UnsupportedNetworkFeatures) — there is no per-network object to
		// carry a driver, an IPAM range or an isolation flag in the first place.
		// Saying what happens INSTEAD matters more than usual for this one: bare
		// "ignored" reads as "the workloads cannot reach each other", when the
		// truth is the opposite and less safe — they all share one bridge.
		log.WarnContext(ctx, "backend ignores user-defined networks: every instance joins incusd's own bridge and takes a DHCP lease from it, so the workloads are NOT segmented from one another and a network's aliases are not registered; reach a peer by its instance address or a published port",
			"networks", len(spec.Networks))
	}
}
