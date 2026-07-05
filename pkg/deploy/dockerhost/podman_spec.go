package dockerhost

// createBody -> libpod SpecGenerator.
//
// This is where the two APIs diverge most, and where the divergences are most
// likely to be silent. libpod is not a renamed Docker API: it takes env as a
// MAP, the stop signal as an INTEGER, mount semantics as fstab-style option
// STRINGS, and CPU limits as raw OCI quota/period with no NanoCpus equivalent.
// A field spelled Docker's way is not rejected — encoding/json simply omits what
// the target struct has no home for, and the container comes up missing it.
//
// So the golden-body tests beside this file assert the exact keys and types on
// the wire, not that the request succeeded.
//
// Translating createBody (rather than api.DeploySpec) is deliberate: it keeps
// toCreateBody — with its accumulated spec-field coverage and its
// warnUnsupported bookkeeping — as the single place the deploy spec is
// interpreted, and makes this a pure wire adaptation with no policy in it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// specGenerator is the libpod container-create body. Its seven embedded structs
// are anonymous upstream, so the whole thing is FLAT on the wire.
//
// Casing is inconsistent by libpod's own design and must be copied exactly:
// mostly snake_case, `healthconfig` all-lowercase, `startupHealthConfig`
// camelCase, and `Networks` with a capital N because that field carries no JSON
// tag at all upstream.
type specGenerator struct {
	Image      string            `json:"image"`
	Name       string            `json:"name,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	User       string            `json:"user,omitempty"`
	WorkDir    string            `json:"work_dir,omitempty"`
	Hostname   string            `json:"hostname,omitempty"`
	Terminal   *bool             `json:"terminal,omitempty"`
	Stdin      *bool             `json:"stdin,omitempty"`

	StopSignal  *int  `json:"stop_signal,omitempty"`
	StopTimeout *uint `json:"stop_timeout,omitempty"`

	RestartPolicy string `json:"restart_policy,omitempty"`
	RestartTries  *uint  `json:"restart_tries,omitempty"`

	PortMappings []libpodPortMapping `json:"portmappings,omitempty"`

	// Networks carries a capital N because the upstream field has NO json tag,
	// so Go's encoder uses the field name. Go's decoder is case-insensitive so
	// "networks" would also be accepted, but this is what podman itself emits.
	Networks map[string]libpodPerNetwork `json:"Networks,omitempty"`
	NetNS    *libpodNamespace            `json:"netns,omitempty"`
	PidNS    *libpodNamespace            `json:"pidns,omitempty"`
	IpcNS    *libpodNamespace            `json:"ipcns,omitempty"`

	DNSServers []string `json:"dns_server,omitempty"`
	DNSSearch  []string `json:"dns_search,omitempty"`
	DNSOptions []string `json:"dns_option,omitempty"`
	HostAdd    []string `json:"hostadd,omitempty"`

	Privileged         *bool    `json:"privileged,omitempty"`
	ReadOnlyFilesystem *bool    `json:"read_only_filesystem,omitempty"`
	CapAdd             []string `json:"cap_add,omitempty"`
	CapDrop            []string `json:"cap_drop,omitempty"`
	Groups             []string `json:"groups,omitempty"`
	SelinuxOpts        []string `json:"selinux_opts,omitempty"`
	ApparmorProfile    string   `json:"apparmor_profile,omitempty"`
	SeccompProfilePath string   `json:"seccomp_profile_path,omitempty"`
	NoNewPrivileges    *bool    `json:"no_new_privileges,omitempty"`
	Mask               []string `json:"mask,omitempty"`
	Unmask             []string `json:"unmask,omitempty"`

	Sysctl map[string]string `json:"sysctl,omitempty"`

	Mounts  []ociMount          `json:"mounts,omitempty"`
	Volumes []libpodNamedVolume `json:"volumes,omitempty"`
	Devices []libpodDevice      `json:"devices,omitempty"`
	ShmSize *int64              `json:"shm_size,omitempty"`
	Init    *bool               `json:"init,omitempty"`

	ResourceLimits *ociResources `json:"resource_limits,omitempty"`
	Rlimits        []ociRlimit   `json:"r_limits,omitempty"`
	HealthConfig   *healthConfig `json:"healthconfig,omitempty"`
	CgroupsMode    string        `json:"cgroups_mode,omitempty"`
}

// libpodPortMapping is strictly better than Docker's split representation:
// typed uint16 ports and a native contiguous range, in one flat array.
type libpodPortMapping struct {
	HostIP        string `json:"host_ip,omitempty"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

type libpodPerNetwork struct {
	Aliases     []string `json:"aliases,omitempty"`
	StaticIPs   []string `json:"static_ips,omitempty"`
	StaticMAC   string   `json:"static_mac,omitempty"`
	InterfaceNm string   `json:"interface_name,omitempty"`
}

// libpodNamespace replaces Docker's overloaded magic strings ("host",
// "container:<id>") with a structured {nsmode, value}.
type libpodNamespace struct {
	NSMode string `json:"nsmode,omitempty"`
	Value  string `json:"value,omitempty"`
}

// ociMount is the raw OCI mount. Everything Docker expresses structurally —
// read-only, propagation, SELinux relabel, tmpfs size — is an fstab-style token
// in Options here.
type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type,omitempty"`
	Source      string   `json:"source,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// libpodNamedVolume is UNTAGGED upstream, so its wire keys are the Go field
// names. An anonymous volume is one with an empty Name.
type libpodNamedVolume struct {
	Name        string   `json:"Name,omitempty"`
	Dest        string   `json:"Dest"`
	Options     []string `json:"Options,omitempty"`
	IsAnonymous bool     `json:"IsAnonymous,omitempty"`
}

// libpodDevice: only Path is read server-side, as a colon-separated
// "host[:container[:perms]]" string. Supplying major/minor would be ignored.
type libpodDevice struct {
	Path string `json:"path"`
}

type ociResources struct {
	CPU    *ociCPU    `json:"cpu,omitempty"`
	Memory *ociMemory `json:"memory,omitempty"`
}

type ociCPU struct {
	Quota  *int64  `json:"quota,omitempty"`
	Period *uint64 `json:"period,omitempty"`
}

type ociMemory struct {
	Limit       *int64 `json:"limit,omitempty"`
	Reservation *int64 `json:"reservation,omitempty"`
}

type ociRlimit struct {
	Type string `json:"type"`
	Soft int64  `json:"soft"`
	Hard int64  `json:"hard"`
}

// cpuPeriodDefault is the CFS period libpod's own compat layer uses when
// converting a NanoCpus value, so a container created either way gets the same
// shares. libpod has NO NanoCpus equivalent — the arithmetic is the client's.
const cpuPeriodDefault uint64 = 100000

// containerCreate maps createBody onto a SpecGenerator and creates the container.
func (e *podmanEngine) containerCreate(ctx context.Context, name string, body createBody) (string, error) {
	spec, err := toSpecGenerator(name, body)
	if err != nil {
		return "", err
	}
	resp, err := e.do(ctx, http.MethodPost, "/containers/create", spec)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusCreated, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("podman: container create returned no id")
	}
	return out.ID, nil
}

// toSpecGenerator is the whole translation, kept pure so it can be tested
// against a golden body without a server.
func toSpecGenerator(name string, b createBody) (*specGenerator, error) {
	s := &specGenerator{
		// Qualified for the same reason imagePull qualifies: podman resolves no
		// short name on its own, and create looks the image up by this exact
		// string — a pull stored as docker.io/library/nginx would not be found
		// again under "nginx".
		Image:      qualifyImageRef(b.Image),
		Name:       name,
		Command:    b.Cmd,
		Entrypoint: b.Entrypoint,
		Labels:     b.Labels,
		User:       b.User,
		WorkDir:    b.WorkingDir,
		Hostname:   b.Hostname,
		Sysctl:     b.HostConfig.Sysctls,
		CapAdd:     b.HostConfig.CapAdd,
		CapDrop:    b.HostConfig.CapDrop,
		Groups:     b.HostConfig.GroupAdd,
		DNSSearch:  b.HostConfig.DnsSearch,
		DNSOptions: b.HostConfig.DnsOptions,
		HostAdd:    b.HostConfig.ExtraHosts,
		DNSServers: b.HostConfig.Dns,
		Devices:    toLibpodDevices(b.HostConfig.Devices),
		// The image's own cgroups behaviour is the default; naming it explicitly
		// keeps a containers.conf that disables cgroups from silently producing
		// containers with no cgroup (and therefore no stats at all).
		CgroupsMode: "enabled",
	}

	// Env: Docker's []string{"K=V"} becomes a map. A bare "K" (no '=') means an
	// empty value, matching Docker's own handling.
	if len(b.Env) > 0 {
		s.Env = make(map[string]string, len(b.Env))
		for _, kv := range b.Env {
			k, v, _ := strings.Cut(kv, "=")
			s.Env[k] = v
		}
	}

	if b.Tty {
		s.Terminal = podmanBool(true)
	}
	if b.OpenStdin {
		s.Stdin = podmanBool(true)
	}
	if b.HostConfig.Privileged {
		s.Privileged = podmanBool(true)
	}
	if b.HostConfig.ReadonlyRootfs {
		s.ReadOnlyFilesystem = podmanBool(true)
	}
	if b.HostConfig.Init != nil && *b.HostConfig.Init {
		s.Init = podmanBool(true)
	}
	if b.HostConfig.ShmSize > 0 {
		v := b.HostConfig.ShmSize
		s.ShmSize = &v
	}

	// Stop signal: Docker sends the NAME ("SIGTERM"), libpod wants the NUMBER.
	if b.StopSignal != "" {
		n, ok := signalNumber(b.StopSignal)
		if !ok {
			return nil, fmt.Errorf("podman: unsupported stop signal %q", b.StopSignal)
		}
		s.StopSignal = &n
	}
	if b.StopTimeout != nil && *b.StopTimeout >= 0 {
		v := uint(*b.StopTimeout)
		s.StopTimeout = &v
	}

	if rp := b.HostConfig.RestartPolicy.Name; rp != "" {
		s.RestartPolicy = rp
		if rp == "on-failure" && b.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
			v := uint(b.HostConfig.RestartPolicy.MaximumRetryCount)
			s.RestartTries = &v
		}
	}

	s.PortMappings = toLibpodPorts(b.HostConfig.PortBindings)
	s.HealthConfig = b.Healthcheck

	if err := applyResources(s, b.HostConfig); err != nil {
		return nil, err
	}
	applySecurityOpts(s, b.HostConfig.SecurityOpt)
	applyUlimits(s, b.HostConfig.Ulimits)
	applyMounts(s, b.HostConfig)
	applyNamespaces(s, b.HostConfig)
	applyNetworks(s, b)

	return s, nil
}

// applyNetworks wires the container's user-defined networks.
//
// The mandatory part: libpod's Validate() REJECTS a spec that carries Networks
// unless netns is explicitly bridge — "networks and static ip/mac address can
// only be used with Bridge mode networking". Docker infers bridge; libpod does
// not, and the failure is a create error rather than a silently degraded
// container, so this is the rare divergence that fails loudly.
func applyNetworks(s *specGenerator, b createBody) {
	if b.NetworkingConfig == nil || len(b.NetworkingConfig.EndpointsConfig) == 0 {
		return
	}
	s.Networks = make(map[string]libpodPerNetwork, len(b.NetworkingConfig.EndpointsConfig))
	for name, ep := range b.NetworkingConfig.EndpointsConfig {
		pn := libpodPerNetwork{Aliases: ep.Aliases, StaticMAC: ep.MacAddress}
		if ep.IPAMConfig != nil {
			if ep.IPAMConfig.IPv4Address != "" {
				pn.StaticIPs = append(pn.StaticIPs, ep.IPAMConfig.IPv4Address)
			}
			if ep.IPAMConfig.IPv6Address != "" {
				pn.StaticIPs = append(pn.StaticIPs, ep.IPAMConfig.IPv6Address)
			}
		}
		s.Networks[name] = pn
	}
	s.NetNS = &libpodNamespace{NSMode: "bridge"}
}

// applyNamespaces maps Docker's overloaded namespace strings.
//
// NetworkMode is the interesting one: "container:<id>" is how the caretaker
// companion shares a workload's netns, which is what makes port-forward work at
// all under a rootless daemon. It must survive translation into
// {nsmode:"container", value:"<id>"} or that whole path silently loses its
// shared network.
func applyNamespaces(s *specGenerator, hc hostConfig) {
	if ns := toNamespace(hc.NetworkMode); ns != nil {
		s.NetNS = ns
	}
	if ns := toNamespace(hc.PidMode); ns != nil {
		s.PidNS = ns
	}
	if ns := toNamespace(hc.IpcMode); ns != nil {
		s.IpcNS = ns
	}
}

func toNamespace(mode string) *libpodNamespace {
	switch {
	case mode == "":
		return nil
	case mode == "host":
		return &libpodNamespace{NSMode: "host"}
	case mode == "private":
		return &libpodNamespace{NSMode: "private"}
	case mode == "none":
		return &libpodNamespace{NSMode: "none"}
	case strings.HasPrefix(mode, "container:"):
		return &libpodNamespace{NSMode: "container", Value: strings.TrimPrefix(mode, "container:")}
	default:
		// A bare name is a user-defined network in Docker's NetworkMode. Networks
		// are carried in Networks/netns:bridge instead (see applyNetworks), so it
		// is not a namespace here.
		return nil
	}
}

// applyResources converts Docker's flat limits into the raw OCI shape.
//
// NanoCpus has no libpod equivalent, so the CFS arithmetic is done here:
// quota = nanocpus / 10000 against a 100000 period, which is what libpod's own
// compat layer does. Getting this wrong is off by four orders of magnitude.
func applyResources(s *specGenerator, hc hostConfig) error {
	var res ociResources
	if hc.NanoCpus > 0 {
		quota := hc.NanoCpus / 10000
		period := cpuPeriodDefault
		res.CPU = &ociCPU{Quota: &quota, Period: &period}
	}
	if hc.Memory > 0 || hc.MemoryReservation > 0 {
		m := &ociMemory{}
		if hc.Memory > 0 {
			v := hc.Memory
			m.Limit = &v
		}
		if hc.MemoryReservation > 0 {
			v := hc.MemoryReservation
			m.Reservation = &v
		}
		res.Memory = m
	}
	if res.CPU != nil || res.Memory != nil {
		s.ResourceLimits = &res
	}
	return nil
}

// applySecurityOpts decomposes Docker's opaque SecurityOpt list into libpod's
// typed fields. There is no `security_opt` on a SpecGenerator at all, so an
// unrecognised entry has nowhere to go and is dropped rather than smuggled.
func applySecurityOpts(s *specGenerator, opts []string) {
	for _, o := range opts {
		k, v, hasValue := strings.Cut(o, "=")
		switch {
		case k == "label" && hasValue:
			s.SelinuxOpts = append(s.SelinuxOpts, v)
		case k == "apparmor" && hasValue:
			s.ApparmorProfile = v
		case k == "seccomp" && hasValue:
			s.SeccompProfilePath = v
		case k == "no-new-privileges":
			// Docker accepts both the bare flag and "=true"/"=false".
			s.NoNewPrivileges = podmanBool(!hasValue || v == "true")
		case k == "mask" && hasValue:
			s.Mask = append(s.Mask, strings.Split(v, ":")...)
		case k == "unmask" && hasValue:
			s.Unmask = append(s.Unmask, strings.Split(v, ":")...)
		case k == "systempaths" && v == "unconfined":
			s.Unmask = append(s.Unmask, "ALL")
		}
	}
}

func applyUlimits(s *specGenerator, ulimits []ulimit) {
	for _, u := range ulimits {
		// libpod wants OCI names (RLIMIT_NOFILE) where Docker uses bare ones
		// (nofile). A server-side wire shim accepts -1 for "unlimited" despite the
		// upstream type being unsigned.
		name := strings.ToUpper(u.Name)
		if !strings.HasPrefix(name, "RLIMIT_") {
			name = "RLIMIT_" + name
		}
		s.Rlimits = append(s.Rlimits, ociRlimit{Type: name, Soft: u.Soft, Hard: u.Hard})
	}
}

// applyMounts converts binds, structured mounts and tmpfs into libpod's three
// parallel arrays.
//
// Every semantic Docker expresses with a field becomes an OPTION STRING here:
// read-only is "ro", propagation is "rslave", SELinux relabel is "z"/"Z", tmpfs
// size is "size=64m". A dropped option is not an error, just a mount that does
// not behave as asked — the rshared propagation the caretaker mount-relay
// depends on is exactly such an option.
func applyMounts(s *specGenerator, hc hostConfig) {
	for _, bind := range hc.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) < 2 {
			continue
		}
		m := ociMount{Destination: parts[1], Type: "bind", Source: parts[0], Options: []string{"rbind"}}
		if len(parts) == 3 && parts[2] != "" {
			m.Options = append(m.Options, strings.Split(parts[2], ",")...)
		} else {
			m.Options = append(m.Options, "rw")
		}
		s.Mounts = append(s.Mounts, m)
	}

	for _, m := range hc.Mounts {
		switch m.Type {
		case "bind":
			om := ociMount{Destination: m.Target, Type: "bind", Source: m.Source, Options: []string{"rbind"}}
			om.Options = append(om.Options, rwOption(m.ReadOnly))
			if m.BindOptions != nil && m.BindOptions.Propagation != "" {
				om.Options = append(om.Options, m.BindOptions.Propagation)
			}
			s.Mounts = append(s.Mounts, om)
		case "tmpfs":
			s.Mounts = append(s.Mounts, ociMount{
				Destination: m.Target, Type: "tmpfs", Source: "tmpfs",
				Options: []string{"rw", "nosuid", "nodev"},
			})
		default: // "volume", named or anonymous
			v := libpodNamedVolume{Name: m.Source, Dest: m.Target}
			v.Options = append(v.Options, rwOption(m.ReadOnly))
			if m.Source == "" {
				v.IsAnonymous = true
			}
			s.Volumes = append(s.Volumes, v)
		}
	}

	// Docker's Tmpfs map is path -> comma-separated options.
	for path, opts := range hc.Tmpfs {
		om := ociMount{Destination: path, Type: "tmpfs", Source: "tmpfs"}
		if opts == "" {
			om.Options = []string{"rw", "nosuid", "nodev"}
		} else {
			om.Options = strings.Split(opts, ",")
		}
		s.Mounts = append(s.Mounts, om)
	}
}

func rwOption(readOnly bool) string {
	if readOnly {
		return "ro"
	}
	return "rw"
}

// toLibpodPorts flattens Docker's PortBindings map into libpod's array.
//
// A binding with HostPort "" or "0" is dropped rather than sent as 0: libpod
// reads 0 as "pick a random host port", where cornus's contract for an
// unspecified host port is "do not publish". Publishing a workload's port on a
// random host interface because a field was empty is precisely the kind of
// surprise a deploy tool must not spring.
func toLibpodPorts(bindings map[string][]portBinding) []libpodPortMapping {
	var out []libpodPortMapping
	for spec, binds := range bindings {
		portStr, proto, _ := strings.Cut(spec, "/")
		if proto == "" {
			proto = "tcp"
		}
		cport, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			continue
		}
		for _, b := range binds {
			if b.HostPort == "" || b.HostPort == "0" {
				continue
			}
			hport, err := strconv.ParseUint(b.HostPort, 10, 16)
			if err != nil {
				continue
			}
			out = append(out, libpodPortMapping{
				HostIP:        b.HostIP,
				ContainerPort: uint16(cport),
				HostPort:      uint16(hport),
				Protocol:      proto,
			})
		}
	}
	// Map iteration is randomized; a stable order keeps the request byte-stable
	// for the reuse fingerprint and for anyone reading a request log.
	sortPortMappings(out)
	return out
}

func sortPortMappings(p []libpodPortMapping) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0; j-- {
			a, b := p[j-1], p[j]
			if a.ContainerPort < b.ContainerPort ||
				(a.ContainerPort == b.ContainerPort && a.Protocol <= b.Protocol) {
				break
			}
			p[j-1], p[j] = p[j], p[j-1]
		}
	}
}

func toLibpodDevices(devs []deviceMapping) []libpodDevice {
	var out []libpodDevice
	for _, d := range devs {
		// Only Path is consumed server-side, in CLI "src:dst:perms" form.
		path := d.PathOnHost
		if d.PathInContainer != "" && d.PathInContainer != d.PathOnHost {
			path += ":" + d.PathInContainer
		}
		if d.CgroupPermissions != "" && d.CgroupPermissions != "rwm" {
			if !strings.Contains(path, ":") {
				path += ":" + d.PathOnHost
			}
			path += ":" + d.CgroupPermissions
		}
		out = append(out, libpodDevice{Path: path})
	}
	return out
}

// signalNumber maps a signal NAME to its number. libpod's stop_signal is an
// integer where Docker's is a string, so this is the whole of that translation.
// Only the signals a stop policy plausibly uses are listed; anything else is an
// error rather than a silent 0, which would mean SIGHUP-by-accident.
func signalNumber(name string) (int, bool) {
	n := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(name), "SIG"))
	switch n {
	case "HUP":
		return 1, true
	case "INT":
		return 2, true
	case "QUIT":
		return 3, true
	case "KILL":
		return 9, true
	case "USR1":
		return 10, true
	case "USR2":
		return 12, true
	case "TERM":
		return 15, true
	case "STOP":
		return 19, true
	}
	// A bare number is also a legal Docker spelling.
	if v, err := strconv.Atoi(name); err == nil && v > 0 && v < 64 {
		return v, true
	}
	return 0, false
}

// ensure the podman engine satisfies the seam.
var _ Engine = (*podmanEngine)(nil)

// podmanBool returns a pointer to v.
//
// Named distinctly from the test helper boolPtr: libpod uses *bool for the
// fields where "unset" must mean "take containers.conf's default" rather than
// "false", so the pointer-ness is load-bearing rather than stylistic.
func podmanBool(v bool) *bool { return &v }
