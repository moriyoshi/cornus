// Package compose parses a Docker Compose file and translates its services into
// cornus deployment specs (api.DeploySpec) plus build instructions. It covers
// the common subset: image, build, command, environment, ports, volumes
// (bind mounts), restart, depends_on, and deploy.replicas.
package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	shellwords "github.com/mattn/go-shellwords"

	"cornus/pkg/logging"
)

// ProjectDocument is the data model of a parsed Compose file: exactly the
// shape that maps onto Compose YAML (json-tagged for `compose config`'s
// rendered dump) and what the parser/merger/extends/include machinery reads
// and writes. It carries no behavior — see Project for the internal,
// behavior-bearing representation built on top of it (dependency ordering,
// deploy-plan translation, profile-view derivation).
type ProjectDocument struct {
	Name     string                        `json:"name"`
	Services map[string]ServiceDocument    `json:"services"`
	Secrets  map[string]SecretDefDocument  `json:"secrets"`
	Configs  map[string]ConfigDefDocument  `json:"configs"`
	Volumes  map[string]VolumeDefDocument  `json:"volumes"`
	Networks map[string]NetworkDefDocument `json:"networks"`
	// Include is the top-level `include:` sequence (compose-spec). Each entry
	// pulls another Compose model into this project. It is a load-time directive,
	// not runtime config: processInclude folds each included model in (the
	// including file winning on conflict) and clears the field. See include.go.
	Include IncludeList `json:"include"`
	// Egress is a PROJECT-LEVEL `x-cornus-egress:` block: the default client-side-
	// egress policy for every service that does not declare its own. It uses the
	// compose extension prefix (`x-`) so the file stays valid for standard compose
	// tooling, which ignores `x-*` keys. A service-level block fully overrides it (no
	// field-level merge). See Egress and api.EgressSpec.
	Egress *EgressDocument `json:"x-cornus-egress"`
	// Ingress is a PROJECT-LEVEL `x-cornus-ingress:` block: the project-wide client
	// overrides of the server ingress defaults (domain / class_name / tls). Unlike
	// Egress, it does NOT enable ingress for every service — ingress stays opt-in per
	// service (a service needs its own `x-cornus-ingress`). This block only supplies
	// defaults that each opted-in service inherits by FIELD (a service value wins);
	// anything still unset falls back to the server defaults. See Ingress.
	Ingress *IngressDocument `json:"x-cornus-ingress"`
	// Telemetry is a PROJECT-LEVEL `x-cornus-telemetry:` block: the default
	// embedded-Collector config for every service that does not declare its own.
	// Like Egress it ENABLES the feature for each inheriting service (a
	// service-level block fully overrides it, no field-level merge), so a whole
	// project ships telemetry to one OTLP backend with a single block. See Telemetry.
	Telemetry *TelemetryDocument `json:"x-cornus-telemetry"`
}

// IncludeList is the top-level `include:` sequence.
type IncludeList []IncludeRef

// IncludeRef is one `include:` entry. Path names one or more Compose files that
// make up the included model. ProjectDirectory is the base directory for
// resolving the included model's own relative paths (defaults to the included
// file's directory). EnvFile names env file(s) used to interpolate the included
// model (defaults to the included model's own sibling .env).
type IncludeRef struct {
	Path             []string // one or more compose files
	ProjectDirectory string
	EnvFile          []string
}

// UnmarshalJSON accepts the short string form (`- common.yml` => a single
// Compose file) or the long object form
// (`- {path: [a.yml, b.yml], project_directory: ..., env_file: ...}`), where
// path and env_file each accept a bare string or a list.
func (i *IncludeList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("include: %w", err)
	}
	out := make(IncludeList, 0, len(raw))
	for _, item := range raw {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			out = append(out, IncludeRef{Path: []string{s}})
			continue
		}
		var obj struct {
			Path             json.RawMessage `json:"path"`
			ProjectDirectory string          `json:"project_directory"`
			EnvFile          json.RawMessage `json:"env_file"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			return fmt.Errorf("include entry: %w", err)
		}
		path, err := decodeStringOrList(obj.Path)
		if err != nil {
			return fmt.Errorf("include.path: %w", err)
		}
		envFile, err := decodeStringOrList(obj.EnvFile)
		if err != nil {
			return fmt.Errorf("include.env_file: %w", err)
		}
		out = append(out, IncludeRef{Path: path, ProjectDirectory: obj.ProjectDirectory, EnvFile: envFile})
	}
	*i = out
	return nil
}

// VolumeDef is a top-level `volumes:` entry. A null value (e.g. `cache:` with no
// body) decodes to the zero VolumeDef, i.e. a project-scoped managed volume.
// External marks a pre-existing volume that cornus must not scope or provision;
// Name overrides the backing resource name (compose's `name:` field). Driver and
// DriverOpts select and parameterize the volume plugin (compose `driver` /
// `driver_opts`); Labels is user metadata. All three flow onto a NAMED volume's
// api.VolumeSpec — dockerhost creates the volume with them; k8s/containerd
// realise a subset (see api.VolumeSpec).
type VolumeDefDocument struct {
	External   bool
	Name       string
	Driver     string
	DriverOpts map[string]string
	Labels     map[string]string
}

// VolumeDef is the internal representation of a top-level volume definition —
// VolumeDefDocument's data with the JSON decoding stripped away. NewProject
// derives it from the document (see volumeDefFromDocument).
type VolumeDef struct {
	External   bool
	Name       string
	Driver     string
	DriverOpts map[string]string
	Labels     map[string]string
}

// UnmarshalJSON decodes the object form, stringifying scalar driver_opts and
// label values (e.g. `size: 100`) via decodeKeyVals like environment does. A
// null body decodes to the zero VolumeDefDocument (a project-scoped managed
// volume).
func (v *VolumeDefDocument) UnmarshalJSON(data []byte) error {
	var obj struct {
		External   bool            `json:"external"`
		Name       string          `json:"name"`
		Driver     string          `json:"driver"`
		DriverOpts json.RawMessage `json:"driver_opts"`
		Labels     json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("volume: %w", err)
	}
	opts, err := decodeKeyVals(obj.DriverOpts)
	if err != nil {
		return fmt.Errorf("volume driver_opts: %w", err)
	}
	labels, err := decodeKeyVals(obj.Labels)
	if err != nil {
		return fmt.Errorf("volume labels: %w", err)
	}
	v.External, v.Name, v.Driver, v.DriverOpts, v.Labels = obj.External, obj.Name, obj.Driver, opts, labels
	return nil
}

// NetworkDef is a top-level `networks:` entry. A null value (e.g. `frontend:`
// with no body) decodes to the zero NetworkDef, i.e. a project-scoped managed
// network. External marks a pre-existing network cornus must not scope or
// provision; Name overrides the backing resource name; Driver and DriverOpts
// select and parameterize the backend network fabric (compose `driver` /
// `driver_opts` — driver names are backend-specific: Docker's own drivers on
// dockerhost, a netdriver provider pipeline on kubernetes).
type NetworkDefDocument struct {
	External   bool
	Name       string
	Driver     string
	DriverOpts map[string]string
	// IPAM carries the network's address management config (compose `ipam`);
	// Attachable/Internal/EnableIPv6 are the compose network toggles; Labels is
	// user metadata. These flow onto every member's api.NetworkAttachment —
	// dockerhost realises them on the created network; k8s/containerd realise a
	// subset (see api.NetworkAttachment).
	IPAM       *IPAM
	Attachable bool
	Internal   bool
	EnableIPv6 bool
	Labels     map[string]string
}

// NetworkDef is the internal representation of a top-level network definition —
// NetworkDefDocument's data with the JSON decoding stripped away. NewProject
// derives it from the document (see networkDefFromDocument).
type NetworkDef struct {
	External   bool
	Name       string
	Driver     string
	DriverOpts map[string]string
	IPAM       *IPAM
	Attachable bool
	Internal   bool
	EnableIPv6 bool
	Labels     map[string]string
}

// IPAM is a network's `ipam:` block (compose-spec 06-networks.md). cornus honours
// the Config list (dockerhost forwards it to Docker's IPAM.Config); the `driver`
// and `options` sub-keys are parsed-but-dropped (Docker's default IPAM driver is
// used on the primary local backend). Only the first Config entry is realised on
// backends that accept a single subnet.
type IPAM struct {
	Config []IPAMConfig
}

// IPAMConfig is one `ipam.config` entry: a Subnet CIDR and optional Gateway and
// IPRange (the sub-range within the subnet cornus/Docker may allocate from).
type IPAMConfig struct {
	Subnet  string
	Gateway string
	IPRange string
}

// UnmarshalJSON decodes the object form, stringifying scalar driver_opts and
// label values via decodeKeyVals like environment does, and decoding the `ipam`
// block's config list.
func (n *NetworkDefDocument) UnmarshalJSON(data []byte) error {
	var obj struct {
		External   bool            `json:"external"`
		Name       string          `json:"name"`
		Driver     string          `json:"driver"`
		DriverOpts json.RawMessage `json:"driver_opts"`
		Attachable bool            `json:"attachable"`
		Internal   bool            `json:"internal"`
		EnableIPv6 bool            `json:"enable_ipv6"`
		Labels     json.RawMessage `json:"labels"`
		IPAM       *struct {
			Config []struct {
				Subnet  string `json:"subnet"`
				Gateway string `json:"gateway"`
				IPRange string `json:"ip_range"`
			} `json:"config"`
		} `json:"ipam"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	opts, err := decodeKeyVals(obj.DriverOpts)
	if err != nil {
		return fmt.Errorf("network driver_opts: %w", err)
	}
	labels, err := decodeKeyVals(obj.Labels)
	if err != nil {
		return fmt.Errorf("network labels: %w", err)
	}
	n.External, n.Name, n.Driver, n.DriverOpts = obj.External, obj.Name, obj.Driver, opts
	n.Attachable, n.Internal, n.EnableIPv6, n.Labels = obj.Attachable, obj.Internal, obj.EnableIPv6, labels
	if obj.IPAM != nil {
		ipam := &IPAM{}
		for _, c := range obj.IPAM.Config {
			ipam.Config = append(ipam.Config, IPAMConfig{Subnet: c.Subnet, Gateway: c.Gateway, IPRange: c.IPRange})
		}
		n.IPAM = ipam
	}
	return nil
}

// ServiceNetworks is a service's `networks:` attachments. Compose accepts a
// list of network names or a map of name -> per-attachment settings; map
// entries with a null body attach with no extra settings. Map order is not
// meaningful in YAML, so entries are sorted by name for determinism.
type ServiceNetworks []ServiceNetwork

// ServiceNetwork is one service-level network attachment. IPv4Address is the
// compose `ipv4_address` pin: the member's fixed address on this network — the
// user's escape hatch when the deterministic plan-time allocation (usernet.go)
// collides or a specific address is required.
type ServiceNetwork struct {
	Name        string
	Aliases     []string
	IPv4Address string
	// IPv6Address / MacAddress pin this member's per-network IPv6 address and MAC
	// (compose long-form `ipv6_address` / `mac_address`); Priority orders the
	// attachment (compose `priority`: higher joins first, its gateway is the
	// default route). They ride onto api.NetworkAttachment — real on dockerhost,
	// ignored where the fabric cannot express them.
	IPv6Address string
	MacAddress  string
	Priority    int
}

// UnmarshalJSON accepts ["net1", "net2"] or {net1: {aliases: [...]}, net2: null}.
func (s *ServiceNetworks) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		out := make(ServiceNetworks, 0, len(list))
		for _, name := range list {
			out = append(out, ServiceNetwork{Name: name})
		}
		*s = out
		return nil
	}
	var m map[string]*struct {
		Aliases     []string `json:"aliases"`
		IPv4Address string   `json:"ipv4_address"`
		IPv6Address string   `json:"ipv6_address"`
		MacAddress  string   `json:"mac_address"`
		Priority    int      `json:"priority"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("networks: %w", err)
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(ServiceNetworks, 0, len(names))
	for _, name := range names {
		sn := ServiceNetwork{Name: name}
		if v := m[name]; v != nil {
			sn.Aliases = v.Aliases
			sn.IPv4Address = v.IPv4Address
			sn.IPv6Address = v.IPv6Address
			sn.MacAddress = v.MacAddress
			sn.Priority = v.Priority
		}
		out = append(out, sn)
	}
	*s = out
	return nil
}

// SecretDef is a top-level secret definition. cornus realises only file-based
// secrets (as read-only bind mounts); the other forms are parsed for fidelity
// but not materialised. File keeps first position so the build-secret consumer
// (which reads only SecretDef.File) is unaffected.
//   - File: a host path whose contents become the secret.
//   - Environment: the name of an environment variable holding the secret value
//     (not materialised — needs value injection cornus cannot express as a mount).
//   - External: a pre-existing cluster/engine secret (not materialised here).
//   - Name: overrides the backing resource name (parsed for fidelity).
type SecretDefDocument struct {
	File        string `json:"file"`
	Environment string `json:"environment"`
	External    bool   `json:"external"`
	Name        string `json:"name"`
}

// SecretDef is the internal representation of a top-level secret definition —
// SecretDefDocument's data with the JSON tags stripped away. NewProject derives
// it from the document (see secretDefFromDocument).
type SecretDef struct {
	File        string
	Environment string
	External    bool
	Name        string
}

// ConfigDef is a top-level `configs:` definition (compose-spec 09-configs.md).
// cornus realises only file-based configs (as read-only bind mounts); the other
// forms are parsed for fidelity but not materialised.
//   - File: a host path whose contents become the config.
//   - Content: inline literal config content (not materialised — cornus has no
//     place to stage the generated file as a mount source).
//   - Environment: the name of an environment variable holding the config value
//     (not materialised).
//   - External: a pre-existing cluster/engine config (not materialised here).
//   - Name: overrides the backing resource name (parsed for fidelity).
type ConfigDefDocument struct {
	File        string `json:"file"`
	Content     string `json:"content"`
	Environment string `json:"environment"`
	External    bool   `json:"external"`
	Name        string `json:"name"`
}

// ConfigDef is the internal representation of a top-level config definition —
// ConfigDefDocument's data with the JSON tags stripped away. NewProject derives
// it from the document (see configDefFromDocument).
type ConfigDef struct {
	File        string
	Content     string
	Environment string
	External    bool
	Name        string
}

// ConfigRef is one service-level `configs:` grant (compose-spec 05-services.md).
// Source names a top-level config; Target is the container path it is mounted
// at; UID/GID/Mode request ownership/permissions on the materialised file. Since
// cornus realises configs as read-only bind mounts, UID/GID/Mode cannot be
// expressed and are ignored (a bind mount inherits the host file's metadata).
type ConfigRef struct {
	Source string `json:"source"`
	Target string `json:"target"`
	UID    string `json:"uid"`
	GID    string `json:"gid"`
	Mode   string `json:"mode"`
}

// ConfigRefs is a service's `configs:` grant list.
type ConfigRefs []ConfigRef

// UnmarshalJSON accepts the short form (a bare string = source name) or the long
// object form ({source, target, uid, gid, mode}) per compose-spec.
func (c *ConfigRefs) UnmarshalJSON(data []byte) error {
	refs, err := decodeGrantRefs(data)
	if err != nil {
		return fmt.Errorf("configs: %w", err)
	}
	out := make(ConfigRefs, len(refs))
	for i, r := range refs {
		out[i] = ConfigRef(r)
	}
	*c = out
	return nil
}

// SecretRef is one service-level `secrets:` grant (compose-spec 05-services.md).
// It mirrors ConfigRef: Source names a top-level secret; Target is the container
// path; UID/GID/Mode are ignored under the bind-mount realisation.
type SecretRef struct {
	Source string `json:"source"`
	Target string `json:"target"`
	UID    string `json:"uid"`
	GID    string `json:"gid"`
	Mode   string `json:"mode"`
}

// SecretRefs is a service's runtime `secrets:` grant list.
type SecretRefs []SecretRef

// UnmarshalJSON accepts the short form (a bare string = source name) or the long
// object form ({source, target, uid, gid, mode}) per compose-spec.
func (s *SecretRefs) UnmarshalJSON(data []byte) error {
	refs, err := decodeGrantRefs(data)
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	out := make(SecretRefs, len(refs))
	for i, r := range refs {
		out[i] = SecretRef(r)
	}
	*s = out
	return nil
}

// grantRef is the shared shape of a config/secret service-level grant, decoded
// once by decodeGrantRefs and converted to the public ConfigRef/SecretRef.
type grantRef struct {
	Source string `json:"source"`
	Target string `json:"target"`
	UID    string `json:"uid"`
	GID    string `json:"gid"`
	Mode   string `json:"mode"`
}

// decodeGrantRefs decodes a service-level configs/secrets grant list. Each entry
// is either a bare string (the source name, short form) or an object with
// {source, target, uid, gid, mode} (long form). Mode accepts a YAML scalar in
// bare (mode: 0440) or quoted ("0440") form via scalarStr.
func decodeGrantRefs(data []byte) ([]grantRef, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]grantRef, 0, len(raw))
	for _, item := range raw {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			out = append(out, grantRef{Source: s})
			continue
		}
		var obj struct {
			Source string    `json:"source"`
			Target string    `json:"target"`
			UID    string    `json:"uid"`
			GID    string    `json:"gid"`
			Mode   scalarStr `json:"mode"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			return nil, err
		}
		out = append(out, grantRef{
			Source: obj.Source,
			Target: obj.Target,
			UID:    obj.UID,
			GID:    obj.GID,
			Mode:   string(obj.Mode),
		})
	}
	return out, nil
}

// Restart is a service `restart:` policy (`no`, `always`, `on-failure`,
// `on-failure:N`, `unless-stopped`). It decodes generously: besides the string
// forms it accepts a bare boolean, because YAML 1.1 coerces the bare word `no`
// (the compose default) to `false`. `false` is read back as `"no"`; only `true`
// (from `restart: yes`/`true`/`on`, which name no valid policy) is rejected.
// Whichever form is given, the resulting policy is validated so an unknown value
// fails at parse time rather than silently reaching a backend.
type Restart string

func (r *Restart) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		var b bool
		if err := json.Unmarshal(data, &b); err != nil {
			return fmt.Errorf("restart: %w", err)
		}
		// YAML `no` (the default) arrives here as false; `yes`/`true`/`on` as
		// true, which names no valid policy.
		if b {
			return fmt.Errorf(`restart: true is not a valid policy; use "no", "always", "on-failure", or "unless-stopped"`)
		}
		str = "no"
	}
	if err := validateRestart(str); err != nil {
		return err
	}
	*r = Restart(str)
	return nil
}

// validateRestart accepts the empty value (unset) and the compose-spec policies:
// `no`, `always`, `unless-stopped`, `on-failure`, and `on-failure:N` (N a
// non-negative integer max-retry count).
func validateRestart(s string) error {
	switch s {
	case "", "no", "always", "unless-stopped", "on-failure":
		return nil
	}
	if n, ok := strings.CutPrefix(s, "on-failure:"); ok {
		if v, err := strconv.Atoi(n); err == nil && v >= 0 {
			return nil
		}
	}
	return fmt.Errorf(`restart: %q is not a valid policy; use "no", "always", "on-failure", "on-failure:N", or "unless-stopped"`, s)
}

// split separates the `on-failure:N` short form into the bare policy word and
// its max-attempt count, mirroring how api.DeploySpec keeps them in separate
// fields (Restart + RestartMaxAttempts). Every other value yields a count of 0
// (the backend default: unlimited retries). The value is assumed already
// validated by UnmarshalJSON, so the numeric suffix parses cleanly.
func (r Restart) split() (policy string, maxAttempts int) {
	if n, ok := strings.CutPrefix(string(r), "on-failure:"); ok {
		v, _ := strconv.Atoi(n)
		return "on-failure", v
	}
	return string(r), 0
}

// ServiceDocument is one Compose service as it maps onto the Compose file
// (json-tagged, custom-decoded). See Service for the internal representation
// NewProject derives from it.
type ServiceDocument struct {
	Image         string          `json:"image"`
	Build         *Build          `json:"build"`
	Entrypoint    Command         `json:"entrypoint"`
	Command       Command         `json:"command"`
	Environment   Environment     `json:"environment"`
	EnvFile       EnvFiles        `json:"env_file"`
	Ports         Ports           `json:"ports"`
	Expose        ExposeList      `json:"expose"`
	Volumes       []Volume        `json:"volumes"`
	Networks      ServiceNetworks `json:"networks"`
	Restart       Restart         `json:"restart"`
	DependsOn     DependsOn       `json:"depends_on"`
	Deploy        *Deploy         `json:"deploy"`
	Healthcheck   *Healthcheck    `json:"healthcheck"`
	ContainerName string          `json:"container_name"`
	Privileged    bool            `json:"privileged"`
	// Common runtime keys plumbed through to api.DeploySpec (see translateService).
	// User is "uid[:gid]" or "user[:group]"; WorkingDir is the container cwd;
	// Hostname is the container hostname; Labels is user metadata (map or
	// KEY=VALUE list form); StopSignal/StopGracePeriod control shutdown; Init
	// requests an init process (pointer to distinguish unset from false); TTY,
	// StdinOpen, and ReadOnly are the corresponding boolean container toggles.
	User            string `json:"user"`
	WorkingDir      string `json:"working_dir"`
	Hostname        string `json:"hostname"`
	Labels          Labels `json:"labels"`
	StopSignal      string `json:"stop_signal"`
	StopGracePeriod string `json:"stop_grace_period"`
	Init            *bool  `json:"init"`
	TTY             bool   `json:"tty"`
	StdinOpen       bool   `json:"stdin_open"`
	ReadOnly        bool   `json:"read_only"`
	// Security and networking keys plumbed through to api.DeploySpec (see
	// translateService). CapAdd/CapDrop add/drop Linux capabilities;
	// SecurityOpt is the container security options list (verbatim on
	// dockerhost, best-effort elsewhere); GroupAdd adds supplementary groups
	// (group name or GID); Sysctls sets namespaced kernel params (map or
	// KEY=VALUE list); ExtraHosts adds /etc/hosts entries (list "host:ip" or
	// map host->ip, normalised to "host:ip"); DNS/DNSSearch/DNSOpt configure
	// the resolver (each a single value or a list).
	CapAdd      []string   `json:"cap_add"`
	CapDrop     []string   `json:"cap_drop"`
	SecurityOpt []string   `json:"security_opt"`
	GroupAdd    []string   `json:"group_add"`
	Sysctls     Sysctls    `json:"sysctls"`
	ExtraHosts  ExtraHosts `json:"extra_hosts"`
	DNS         StringList `json:"dns"`
	DNSSearch   StringList `json:"dns_search"`
	DNSOpt      StringList `json:"dns_opt"`
	// Resource & host-namespace keys plumbed through to api.DeploySpec (see
	// translateService). Ulimits sets process rlimits; Tmpfs mounts tmpfs at
	// container paths; Devices maps host devices; ShmSize sizes /dev/shm; PID and
	// IPC select the container's PID/IPC namespace mode. MemLimit and CPUs are the
	// non-deploy equivalents of deploy.resources.limits.memory/.cpus and route into
	// the SAME api.Resources limits (deploy.resources wins when both are set).
	Ulimits  Ulimits    `json:"ulimits"`
	Tmpfs    StringList `json:"tmpfs"`
	Devices  StringList `json:"devices"`
	ShmSize  scalarStr  `json:"shm_size"`
	PID      string     `json:"pid"`
	IPC      string     `json:"ipc"`
	MemLimit scalarStr  `json:"mem_limit"`
	CPUs     scalarStr  `json:"cpus"`
	// Configs and Secrets are service-level grant refs to top-level `configs:` /
	// `secrets:` definitions. cornus realises file-based ones as read-only bind
	// mounts (see translateService).
	Configs ConfigRefs `json:"configs"`
	Secrets SecretRefs `json:"secrets"`
	// Profiles gate whether the service is active: a service with a non-empty
	// profiles list is loaded only when one of its profiles is activated
	// (compose --profile / COMPOSE_PROFILES). An empty list means always active.
	Profiles []string `json:"profiles"`
	// Extends names a base service this one inherits from (compose-spec
	// `extends`). It is a load-time directive, not runtime config: resolveExtends
	// expands it into a fully-merged Service and clears the field. See extends.go.
	Extends *Extends `json:"extends"`
	// Provider delegates the service's lifecycle to an external provider plugin
	// (compose-spec `provider:`) instead of building/pulling an image. Mutually
	// exclusive with image/build/deploy. See Provider and translateService.
	Provider *Provider `json:"provider"`
	// Egress is a service `x-cornus-egress:` block: route the container's OUTBOUND
	// traffic through a client-side vantage point. A service-level block fully
	// overrides the project-level default. See Egress and api.EgressSpec.
	Egress *EgressDocument `json:"x-cornus-egress"`
	// Ingress is a service `x-cornus-ingress:` block: request a public HTTP(S)
	// Ingress fronting the service (kubernetes only). Its mere presence enables
	// ingress; the bare form `x-cornus-ingress: {}` (or `true`) takes every default
	// (host auto-derived from the server base domain). See Ingress and api.IngressSpec.
	Ingress *IngressDocument `json:"x-cornus-ingress"`
	// AgentForward is a service `x-cornus-agent-forward: true` flag: wires a
	// caretaker AgentRelayRole for this service (kubernetes only), so `cornus
	// compose exec --forward-agent` can relay a local ssh-agent into an exec
	// session. Maps straight onto api.DeploySpec.AgentForward — no sub-fields to
	// configure, so a plain bool (unlike Egress/Ingress, which have project-level
	// defaults or nested options) is enough.
	AgentForward bool `json:"x-cornus-agent-forward"`
	// Telemetry is a service `x-cornus-telemetry:` block: run an embedded
	// OpenTelemetry Collector in the caretaker for this service and auto-wire the
	// container's OTEL_* env to it. Its presence enables telemetry; the endpoint is
	// required. Maps to api.TelemetrySpec. See TelemetryDocument.
	Telemetry *TelemetryDocument `json:"x-cornus-telemetry"`
}

// Service is the internal representation of a Compose service: ServiceDocument's
// data with the JSON decoding stripped away and its Egress/Ingress narrowed to
// the internal Egress/Ingress types. NewProject derives it from the document
// (see serviceFromDocument). Its field lineup mirrors ServiceDocument.
type Service struct {
	Image         string
	Build         *Build
	Entrypoint    Command
	Command       Command
	Environment   Environment
	EnvFile       EnvFiles
	Ports         Ports
	Expose        ExposeList
	Volumes       []Volume
	Networks      ServiceNetworks
	Restart       Restart
	DependsOn     DependsOn
	Deploy        *Deploy
	Healthcheck   *Healthcheck
	ContainerName string
	Privileged    bool

	User            string
	WorkingDir      string
	Hostname        string
	Labels          Labels
	StopSignal      string
	StopGracePeriod string
	Init            *bool
	TTY             bool
	StdinOpen       bool
	ReadOnly        bool

	CapAdd      []string
	CapDrop     []string
	SecurityOpt []string
	GroupAdd    []string
	Sysctls     Sysctls
	ExtraHosts  ExtraHosts
	DNS         StringList
	DNSSearch   StringList
	DNSOpt      StringList

	Ulimits  Ulimits
	Tmpfs    StringList
	Devices  StringList
	ShmSize  scalarStr
	PID      string
	IPC      string
	MemLimit scalarStr
	CPUs     scalarStr

	Configs  ConfigRefs
	Secrets  SecretRefs
	Profiles []string
	Extends  *Extends
	Provider *Provider
	Egress   *Egress
	Ingress  *Ingress

	AgentForward bool
	Telemetry    *Telemetry
}

// Extends is a service `extends:` directive: the base service to inherit from,
// optionally in another Compose file. Service is the base service name (required);
// File, when set, is the Compose file that defines it (relative to the current
// file's directory, or absolute).
type Extends struct {
	Service string `json:"service"`
	File    string `json:"file"`
}

// UnmarshalJSON accepts the short string form (`extends: base` => the base
// service name in the same file) or the long object form
// (`extends: {service: base, file: ../common.yml}`).
func (e *Extends) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.Service = s
		e.File = ""
		return nil
	}
	var obj struct {
		Service string `json:"service"`
		File    string `json:"file"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("extends: %w", err)
	}
	e.Service, e.File = obj.Service, obj.File
	return nil
}

// Provider is a service `provider:` block (compose-spec 05-services.md): the
// service's lifecycle is delegated to an external provider plugin named by Type
// instead of being built/pulled and run as a container. Options carry
// provider-specific configuration passed to the plugin as `--key=value` flags.
// A provider service is mutually exclusive with image/build/deploy (enforced in
// translateService). The same type is used in both ServiceDocument and Service:
// it needs no document->internal narrowing.
type Provider struct {
	// Type names the external component. cornus runs the Docker CLI plugin
	// `docker-<type>` if present, else a binary `<type>` on PATH. Required.
	Type string
	// Options is the provider-specific `options:` map, flattened to an ordered,
	// deterministic list of key/value(s) so the derived flags are stable.
	Options ProviderOptions
}

// ProviderOptions is the flattened form of a provider `options:` map: one entry
// per option key, each with its one-or-more values (a scalar yields a single
// value; a sequence yields several, later emitted as repeated flags). Entries
// are sorted by key at decode time so the resulting flag order is deterministic.
type ProviderOptions []ProviderOption

// ProviderOption is a single provider option: its key and one-or-more string
// values. A scalar option has exactly one value; a list option has several.
type ProviderOption struct {
	Key    string
	Values []string
}

// UnmarshalJSON decodes the provider block. It accepts the object form
// (`{type: awesomecloud, options: {...}}`); the `options` map is flattened into
// a sorted ProviderOptions so downstream flag construction is deterministic.
func (p *Provider) UnmarshalJSON(data []byte) error {
	var obj struct {
		Type    string                     `json:"type"`
		Options map[string]json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("provider: %w", err)
	}
	p.Type = obj.Type
	keys := make([]string, 0, len(obj.Options))
	for k := range obj.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p.Options = make(ProviderOptions, 0, len(keys))
	for _, k := range keys {
		vals, err := providerOptionValues(obj.Options[k])
		if err != nil {
			return fmt.Errorf("provider option %q: %w", k, err)
		}
		p.Options = append(p.Options, ProviderOption{Key: k, Values: vals})
	}
	return nil
}

// MarshalJSON renders the provider block back to its Compose shape
// (`{type, options}` with options as a scalar-or-list map) so `compose config`
// round-trips it — the internal Options is a flattened, ordered slice that would
// otherwise serialize as a list of {Key, Values} objects.
func (p Provider) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	if p.Type != "" {
		out["type"] = p.Type
	}
	if len(p.Options) > 0 {
		opts := make(map[string]any, len(p.Options))
		for _, o := range p.Options {
			if len(o.Values) == 1 {
				opts[o.Key] = o.Values[0]
			} else {
				opts[o.Key] = o.Values
			}
		}
		out["options"] = opts
	}
	return json.Marshal(out)
}

// Flags renders the options as `--key=value` command-line flags in the sorted,
// deterministic order fixed at decode time. A list option becomes one flag per
// value (compose passes repeated flags), matching how the provider plugin
// protocol expects service options.
func (o ProviderOptions) Flags() []string {
	var out []string
	for _, opt := range o {
		for _, v := range opt.Values {
			out = append(out, fmt.Sprintf("--%s=%s", opt.Key, v))
		}
	}
	return out
}

// providerOptionValues decodes one option value: a scalar (string/number/bool)
// yields a single string; a sequence of scalars yields several. Any other shape
// (nested map, list of maps) is rejected — provider options are flat.
func providerOptionValues(raw json.RawMessage) ([]string, error) {
	if s, ok := scalarJSONToString(raw); ok {
		return []string{s}, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := scalarJSONToString(item)
			if !ok {
				return nil, fmt.Errorf("list values must be scalars")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a scalar or a list of scalars")
}

// scalarJSONToString converts a scalar JSON value (string, number, bool) to its
// string form, reporting false for any non-scalar (object, array, null). Numbers
// render without a trailing ".0" so `port: 5432` becomes "5432", not "5432.0".
func scalarJSONToString(raw json.RawMessage) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		return "", false
	}
}

// Egress is a service `x-cornus-egress:` block: route the container's OUTBOUND
// traffic through a client-side vantage point (for air-gapped / VPN / corporate-
// proxy / SASE networks). It uses the compose extension prefix (`x-`) so the file
// stays valid for standard compose tooling. It maps directly to api.EgressSpec; see
// that type for the mode and routing semantics.
type EgressDocument struct {
	Mode       string            `json:"mode"`
	Gateway    string            `json:"gateway"`
	Proxies    map[string]string `json:"proxies"`
	Rules      []EgressRule      `json:"rules"`
	Script     string            `json:"script"`
	Default    string            `json:"default"`
	ListenPort int               `json:"listen_port"`
}

// Egress is the internal representation of an `x-cornus-egress:` block —
// EgressDocument's data with the JSON tags stripped away. NewProject derives it
// from the document (see egressFromDocument).
type Egress struct {
	Mode       string
	Gateway    string
	Proxies    map[string]string
	Rules      []EgressRule
	Script     string
	Default    string
	ListenPort int
}

// EgressRule is one `egress.rules` entry: a destination pattern and the route it
// takes ("client", "gateway", "cluster", or "deny").
type EgressRule struct {
	Pattern string `json:"pattern"`
	Route   string `json:"route"`
}

// TelemetryDocument is a service `x-cornus-telemetry:` block: run an embedded
// OpenTelemetry Collector in the caretaker and auto-wire the container to it. It
// uses the compose extension prefix (`x-`) so the file stays valid for standard
// compose tooling. Its presence enables telemetry; `endpoint` is required. It maps
// to api.TelemetrySpec; see that type for field semantics.
type TelemetryDocument struct {
	Endpoint           string            `json:"endpoint"`
	Protocol           string            `json:"protocol"`
	Headers            map[string]string `json:"headers"`
	Insecure           bool              `json:"insecure"`
	Signals            []string          `json:"signals"`
	ServiceName        string            `json:"service_name"`
	ResourceAttributes map[string]string `json:"resource_attributes"`
	GRPCPort           int               `json:"grpc_port"`
	HTTPPort           int               `json:"http_port"`
	Debug              bool              `json:"debug"`
}

// Telemetry is the internal representation of an `x-cornus-telemetry:` block —
// TelemetryDocument's data with the JSON tags stripped away. NewProject derives it
// from the document (see telemetryFromDocument).
type Telemetry struct {
	Endpoint           string
	Protocol           string
	Headers            map[string]string
	Insecure           bool
	Signals            []string
	ServiceName        string
	ResourceAttributes map[string]string
	GRPCPort           int
	HTTPPort           int
	Debug              bool
}

// toDocument rebuilds the `x-cornus-telemetry:` document from the internal form (a
// nil receiver yields nil, so a service without telemetry re-serializes cleanly).
func (t *Telemetry) toDocument() *TelemetryDocument {
	if t == nil {
		return nil
	}
	return &TelemetryDocument{
		Endpoint:           t.Endpoint,
		Protocol:           t.Protocol,
		Headers:            t.Headers,
		Insecure:           t.Insecure,
		Signals:            t.Signals,
		ServiceName:        t.ServiceName,
		ResourceAttributes: t.ResourceAttributes,
		GRPCPort:           t.GRPCPort,
		HTTPPort:           t.HTTPPort,
		Debug:              t.Debug,
	}
}

// Ingress is a service `x-cornus-ingress:` block: request a public HTTP(S) Ingress
// fronting the service (kubernetes only). It uses the compose extension prefix
// (`x-`) so the file stays valid for standard compose tooling. Its presence enables
// ingress; an empty block (`{}` or `true`) takes every default, so a per-PR preview
// gets a public URL with no host wiring (host auto-derived from the server base
// domain). It maps to api.IngressSpec; see that type for field semantics.
type IngressDocument struct {
	// Host and Hosts both name external hostnames; Host is scalar sugar for a single
	// entry and is unioned with Hosts. Empty asks the server to auto-derive one from
	// the base domain and the service name.
	Host   string   `json:"host"`
	Hosts  []string `json:"hosts"`
	Domain string   `json:"domain"`
	// Subdomain overrides the label(s) prefixed to the base domain when the host is
	// auto-derived. Empty defaults to "<service>.<project>", so different projects
	// get distinct hostnames. Ignored when an explicit host is given.
	Subdomain   string            `json:"subdomain"`
	Path        string            `json:"path"`
	PathType    string            `json:"path_type"`
	Port        int               `json:"port"`
	ClassName   string            `json:"class_name"`
	Annotations map[string]string `json:"annotations"`
	TLS         *IngressTLS       `json:"tls"`
}

// Ingress is the internal representation of an `x-cornus-ingress:` block —
// IngressDocument's data with the JSON decoding stripped away. NewProject
// derives it from the document (see ingressFromDocument).
type Ingress struct {
	Host        string
	Hosts       []string
	Domain      string
	Subdomain   string
	Path        string
	PathType    string
	Port        int
	ClassName   string
	Annotations map[string]string
	TLS         *IngressTLS
}

// IngressTLS is the `x-cornus-ingress.tls` block requesting HTTPS. It maps to
// api.IngressTLS.
type IngressTLS struct {
	SecretName    string `json:"secret_name"`
	ClusterIssuer string `json:"cluster_issuer"`
}

// UnmarshalJSON accepts the bare boolean form (`x-cornus-ingress: true` => enable
// with all defaults) alongside the object form. `false` yields a nil-equivalent
// empty block; presence of the key still enables ingress at translate time, so
// `false` is treated the same as an empty object (use omission to disable).
func (in *IngressDocument) UnmarshalJSON(data []byte) error {
	var enabled bool
	if err := json.Unmarshal(data, &enabled); err == nil {
		*in = IngressDocument{}
		return nil
	}
	type ingressAlias IngressDocument
	var obj ingressAlias
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("x-cornus-ingress: %w", err)
	}
	*in = IngressDocument(obj)
	return nil
}

// Healthcheck is a service `healthcheck:` block. Test is the probe command in
// string-or-list form (a string runs via the shell, a list is Docker's CMD
// form); Disable turns any inherited image healthcheck off.
type Healthcheck struct {
	Test          HealthcheckTest `json:"test"`
	Interval      string          `json:"interval"`
	Timeout       string          `json:"timeout"`
	Retries       int             `json:"retries"`
	StartPeriod   string          `json:"start_period"`
	StartInterval string          `json:"start_interval"`
	Disable       bool            `json:"disable"`
}

// HealthcheckTest is the `healthcheck.test` value: a bare string (run via the
// shell, i.e. CMD-SHELL) or a list whose first element is CMD/CMD-SHELL/NONE.
type HealthcheckTest []string

// UnmarshalJSON accepts "curl -f http://localhost" or ["CMD", "curl", ...].
func (t *HealthcheckTest) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*t = list
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("healthcheck.test: %w", err)
	}
	*t = HealthcheckTest{s}
	return nil
}

// DockerTest returns the healthcheck test in Docker's CMD form (the value cornus
// stores on api.Healthcheck.Test). Disable wins (=> ["NONE"]); a bare string
// becomes ["CMD-SHELL", str]; a list whose first element is already a CMD marker
// passes through, otherwise it is treated as an exec command (prefixed "CMD").
func (h *Healthcheck) DockerTest() []string {
	if h.Disable {
		return []string{"NONE"}
	}
	t := h.Test
	if len(t) == 0 {
		return nil
	}
	switch t[0] {
	case "NONE", "CMD", "CMD-SHELL":
		return append([]string(nil), t...)
	}
	if len(t) == 1 {
		// Single bare string: run through the shell, as Compose does.
		return []string{"CMD-SHELL", t[0]}
	}
	return append([]string{"CMD"}, t...)
}

// EnvFiles is the service env_file list. Each entry loads KEY=VALUE pairs into
// the service environment.
type EnvFiles []EnvFileRef

// EnvFileRef is one env_file entry.
type EnvFileRef struct {
	Path     string
	Required bool
}

// UnmarshalJSON accepts "path", ["a","b"], or [{path, required}] forms. A bare
// path is required by default (matching Compose).
func (e *EnvFiles) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*e = EnvFiles{{Path: s, Required: true}}
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("env_file: %w", err)
	}
	out := make(EnvFiles, 0, len(raw))
	for _, item := range raw {
		var p string
		if err := json.Unmarshal(item, &p); err == nil {
			out = append(out, EnvFileRef{Path: p, Required: true})
			continue
		}
		var obj struct {
			Path     string `json:"path"`
			Required *bool  `json:"required"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			return fmt.Errorf("env_file entry: %w", err)
		}
		required := true
		if obj.Required != nil {
			required = *obj.Required
		}
		out = append(out, EnvFileRef{Path: obj.Path, Required: required})
	}
	*e = out
	return nil
}

// Deploy carries the subset of the compose deploy: key cornus uses.
//
// The four swarm-orchestrator-only sub-keys (placement, mode, endpoint_mode,
// rollback_config) are deliberately NOT modelled here: they are scheduler
// concepts a deploy-to-a-single-server model cannot honour, so the loader warns
// about them (warnUnsupportedFields) rather than silently dropping them.
type Deploy struct {
	Replicas      int                  `json:"replicas"`
	Resources     *DeployResources     `json:"resources"`
	RestartPolicy *DeployRestartPolicy `json:"restart_policy"`
	// Labels are SERVICE-level labels (swarm applies them to the service object,
	// as opposed to the container-level `labels:`). cornus has a single Labels map
	// per workload, so translateService merges these with the service `labels:`
	// — the conflation is intentional and documented there.
	Labels       Labels        `json:"labels"`
	UpdateConfig *UpdateConfig `json:"update_config"`
}

// DeployRestartPolicy is `deploy.restart_policy:`. Condition (none/on-failure/
// any) folds into the workload restart policy and is AUTHORITATIVE over the
// service-level `restart:` (compose-spec). MaxAttempts caps retries for an
// on-failure policy (dockerhost RestartPolicy.MaximumRetryCount). Delay and
// Window are swarm rollout-timing values with no per-container equivalent on any
// cornus backend; they are parsed for fidelity but not applied.
type DeployRestartPolicy struct {
	Condition   string `json:"condition"`
	Delay       string `json:"delay"`
	MaxAttempts int    `json:"max_attempts"`
	Window      string `json:"window"`
}

// UpdateConfig is `deploy.update_config:`. Parallelism and Order map onto the
// kubernetes Deployment rolling-update strategy (see api.UpdateConfig). Delay,
// Monitor and MaxFailureRatio are swarm rollout-timing/health concepts with no
// Deployment equivalent — parsed for fidelity but not applied.
type UpdateConfig struct {
	Parallelism     int       `json:"parallelism"`
	Order           string    `json:"order"`
	Delay           string    `json:"delay"`
	Monitor         string    `json:"monitor"`
	MaxFailureRatio scalarStr `json:"max_failure_ratio"`
}

// DeployResources is `deploy.resources:`. Limits caps the workload (honoured on
// dockerhost + kubernetes + containerd); Reservations requests a guaranteed
// floor (kubernetes resources.requests, dockerhost MemoryReservation — see
// api.Resources). Both share the cpus/memory ResourceLimits shape.
type DeployResources struct {
	Limits       *ResourceLimits `json:"limits"`
	Reservations *ResourceLimits `json:"reservations"`
}

// ResourceLimits is `deploy.resources.limits:`. Cpus is a decimal core count
// ("0.5"); Memory is a size string ("512M", "1Gi"). Both accept a YAML scalar in
// either quoted (string) or bare (number) form.
type ResourceLimits struct {
	Cpus   scalarStr `json:"cpus"`
	Memory scalarStr `json:"memory"`
}

// scalarStr decodes a JSON string or number into its string form, so a compose
// scalar written bare (cpus: 0.5) or quoted (cpus: "0.5") both parse.
type scalarStr string

func (s *scalarStr) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = scalarStr(str)
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(data, &num); err != nil {
		return fmt.Errorf("scalar: %w", err)
	}
	*s = scalarStr(num.String())
	return nil
}

// parseCPUs parses a compose cpus value (decimal core count) into a fractional
// core count. An empty value yields 0.
func parseCPUs(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// parseSize parses a compose memory size ("512M", "1g", "1Gi", "1048576") into
// bytes. Suffixes are power-of-1024 (b, k, m, g, t, with an optional trailing
// "b"/"ib"), matching Docker Compose's RAMInBytes. An empty value yields 0.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	// Split the numeric prefix from the unit suffix.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	numStr, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	if numStr == "" {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	// Normalise "kb"/"kib"/"ki"/"k" to the leading unit letter (all power-of-1024).
	unit = strings.TrimSuffix(unit, "b")
	unit = strings.TrimSuffix(unit, "i")
	var mult float64 = 1
	switch unit {
	case "":
		mult = 1
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	case "t":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("invalid size unit in %q", s)
	}
	return int64(num * mult), nil
}

// Build describes how to build a service image. It accepts either a context
// path string or an object.
type Build struct {
	Context    string            `json:"context"`
	Dockerfile string            `json:"dockerfile"`
	Args       map[string]string `json:"args"`
	// Target is the Dockerfile multi-stage target stage (build.target).
	Target string `json:"target"`
	// CacheFrom lists registry references to import build cache from
	// (build.cache_from). Only registry references are meaningful over the wire.
	CacheFrom []string `json:"cache_from"`
	// AdditionalContexts are extra named build contexts (name -> host dir).
	// Only path/dir values are kept; image-reference values (e.g.
	// docker-image://...) are dropped since the transport syncs directories.
	AdditionalContexts map[string]string `json:"additional_contexts"`
	// Secrets are the top-level secrets this build references. The short form is a
	// bare id/source; the long form additionally carries target/uid/gid/mode
	// (parsed for fidelity — cornus's build-secret plumbing carries only the id, so
	// target/uid/gid/mode are not yet honored; translateService warns when set).
	Secrets []BuildSecret `json:"secrets"`
	// SSH are SSH agent forwarding specs for RUN --mount=type=ssh, each a bare
	// id ("default") or "id=source" where source is an agent socket path. A bare
	// id resolves to $SSH_AUTH_SOCK when the build runs.
	SSH []string `json:"ssh"`
	// Labels are image labels applied to the built image (build.labels), map or
	// KEY=VALUE list form. They map to the buildkit "label:<k>=<v>" frontend attrs.
	Labels map[string]string `json:"labels"`
	// NoCache disables the build cache for this build (build.no_cache), mapping to
	// the frontend "no-cache" attr.
	NoCache bool `json:"no_cache"`
	// Pull always attempts to pull a newer version of the base image
	// (build.pull), mapping to the frontend "image-resolve-mode: pull" attr.
	Pull bool `json:"pull"`
	// Platforms are the target build platforms (build.platforms), e.g.
	// "linux/amd64". They map to the frontend "platform" attr (comma-joined).
	// Multi-platform output additionally needs the engine's emulators.
	Platforms []string `json:"platforms"`
	// Tags are additional image references to tag/push the result as
	// (build.tags), beyond the service image. They are added to the image
	// exporter's name list.
	Tags []string `json:"tags"`
	// Network is the build-time network mode (build.network): one of "default",
	// "none", or "host". It maps to the frontend "force-network-mode" attr.
	Network string `json:"network"`
	// CacheTo lists build-cache export specs (build.cache_to), each a buildx-style
	// "type=registry,ref=..." string or a bare registry ref (=> type=registry).
	// They parallel CacheFrom and map to the solve's cache exports.
	CacheTo []string `json:"cache_to"`
	// ExtraHosts adds custom /etc/hosts entries during the build (build.extra_hosts),
	// list "host:ip" or map host->ip, mapping to the frontend "add-hosts" attr.
	ExtraHosts ExtraHosts `json:"extra_hosts"`
	// ShmSize sizes /dev/shm for RUN steps (build.shm_size), a size string
	// ("128M"), mapping to the frontend "shm-size" attr (bytes).
	ShmSize scalarStr `json:"shm_size"`
	// DockerfileInline is an inline Dockerfile body (build.dockerfile_inline). When
	// set it supersedes Dockerfile / the context Dockerfile: the build runs against
	// this content served as a synthetic Dockerfile.
	DockerfileInline string `json:"dockerfile_inline"`
}

// BuildSecret is one service build.secrets grant. Source names a top-level
// secret; Target/UID/GID/Mode are the long-form attributes (compose-spec
// build.md). cornus's build-secret transport carries only the id today, so
// Target/UID/GID/Mode are parsed for fidelity but not yet honored (a warning is
// emitted when any is set).
type BuildSecret struct {
	Source string
	Target string
	UID    string
	GID    string
	Mode   string
}

// UnmarshalJSON accepts a bare context string or a
// {context,dockerfile,args,additional_contexts,secrets,ssh} object.
func (b *Build) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		b.Context = s
		return nil
	}
	type rawBuild struct {
		Context            string          `json:"context"`
		Dockerfile         string          `json:"dockerfile"`
		Target             string          `json:"target"`
		Args               json.RawMessage `json:"args"`
		CacheFrom          json.RawMessage `json:"cache_from"`
		AdditionalContexts json.RawMessage `json:"additional_contexts"`
		Secrets            json.RawMessage `json:"secrets"`
		SSH                json.RawMessage `json:"ssh"`
		Labels             json.RawMessage `json:"labels"`
		NoCache            bool            `json:"no_cache"`
		Pull               bool            `json:"pull"`
		Platforms          json.RawMessage `json:"platforms"`
		Tags               json.RawMessage `json:"tags"`
		Network            string          `json:"network"`
		CacheTo            json.RawMessage `json:"cache_to"`
		ExtraHosts         json.RawMessage `json:"extra_hosts"`
		ShmSize            scalarStr       `json:"shm_size"`
		DockerfileInline   string          `json:"dockerfile_inline"`
	}
	var rb rawBuild
	if err := json.Unmarshal(data, &rb); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	b.Context = rb.Context
	b.Dockerfile = rb.Dockerfile
	b.Target = rb.Target
	b.NoCache = rb.NoCache
	b.Pull = rb.Pull
	b.Network = rb.Network
	b.ShmSize = rb.ShmSize
	b.DockerfileInline = rb.DockerfileInline
	args, err := decodeKeyVals(rb.Args)
	if err != nil {
		return fmt.Errorf("build.args: %w", err)
	}
	b.Args = args

	cacheFrom, err := decodeStringOrList(rb.CacheFrom)
	if err != nil {
		return fmt.Errorf("build.cache_from: %w", err)
	}
	b.CacheFrom = cacheFrom

	ctxs, err := decodeKeyVals(rb.AdditionalContexts)
	if err != nil {
		return fmt.Errorf("build.additional_contexts: %w", err)
	}
	for name, val := range ctxs {
		// Skip image-reference contexts (e.g. docker-image://...); only
		// directories are forwarded over the wire.
		if strings.Contains(val, "://") {
			continue
		}
		if b.AdditionalContexts == nil {
			b.AdditionalContexts = map[string]string{}
		}
		b.AdditionalContexts[name] = val
	}

	secrets, err := decodeBuildSecrets(rb.Secrets)
	if err != nil {
		return fmt.Errorf("build.secrets: %w", err)
	}
	b.Secrets = secrets

	ssh, err := decodeSSH(rb.SSH)
	if err != nil {
		return fmt.Errorf("build.ssh: %w", err)
	}
	b.SSH = ssh

	labels, err := decodeKeyVals(rb.Labels)
	if err != nil {
		return fmt.Errorf("build.labels: %w", err)
	}
	b.Labels = labels

	platforms, err := decodeStringOrList(rb.Platforms)
	if err != nil {
		return fmt.Errorf("build.platforms: %w", err)
	}
	b.Platforms = platforms

	tags, err := decodeStringOrList(rb.Tags)
	if err != nil {
		return fmt.Errorf("build.tags: %w", err)
	}
	b.Tags = tags

	cacheTo, err := decodeStringOrList(rb.CacheTo)
	if err != nil {
		return fmt.Errorf("build.cache_to: %w", err)
	}
	b.CacheTo = cacheTo

	if len(rb.ExtraHosts) > 0 {
		if err := json.Unmarshal(rb.ExtraHosts, &b.ExtraHosts); err != nil {
			return fmt.Errorf("build.extra_hosts: %w", err)
		}
	}
	return nil
}

// decodeSSH decodes a service build.ssh value into a list of "id" or
// "id=source" entries. It accepts a list of strings (["default", "id=/sock"])
// or a map ({default: null, id: /sock}); a null/empty map value yields a bare
// id. Map entries are sorted for a deterministic order.
func decodeSSH(data json.RawMessage) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		out := make([]string, 0, len(list))
		for _, s := range list {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	var m map[string]*string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v == nil || strings.TrimSpace(*v) == "" {
			out = append(out, k)
		} else {
			out = append(out, k+"="+*v)
		}
	}
	sort.Strings(out)
	return out, nil
}

// decodeBuildSecrets decodes a service build.secrets value into a list of build
// secret refs. It accepts the short form (a bare id/source, ["mysecret"]) or the
// long form ([{source, target, uid, gid, mode}]). Mode accepts a YAML scalar in
// bare (mode: 0440) or quoted ("0440") form via scalarStr.
func decodeBuildSecrets(data json.RawMessage) ([]BuildSecret, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]BuildSecret, 0, len(raw))
	for _, item := range raw {
		var id string
		if err := json.Unmarshal(item, &id); err == nil {
			if id != "" {
				out = append(out, BuildSecret{Source: id})
			}
			continue
		}
		var obj struct {
			Source string    `json:"source"`
			Target string    `json:"target"`
			UID    string    `json:"uid"`
			GID    string    `json:"gid"`
			Mode   scalarStr `json:"mode"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			return nil, err
		}
		if obj.Source == "" {
			continue
		}
		out = append(out, BuildSecret{
			Source: obj.Source,
			Target: obj.Target,
			UID:    obj.UID,
			GID:    obj.GID,
			Mode:   string(obj.Mode),
		})
	}
	return out, nil
}

// decodeStringOrList decodes a value that may be a bare string ("a") or a list
// of strings (["a","b"]) into a slice, dropping empty entries. Used for
// build.cache_from.
func decodeStringOrList(data json.RawMessage) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		if one == "" {
			return nil, nil
		}
		return []string{one}, nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// Command is an entrypoint/command override (string or list form).
type Command []string

// UnmarshalJSON accepts "a b c" or ["a","b","c"]. The string form is split with
// github.com/mattn/go-shellwords — the same splitter Docker Compose (compose-go)
// uses for the command/entrypoint string form — honouring quotes and escapes, so
// `sh -c "echo hi"` becomes ["sh","-c","echo hi"], not a naive whitespace split.
// The parser runs with go-shellwords' defaults (ParseEnv=false, ParseComment=false,
// ParseBacktick=false): a `#` is a literal, not a comment, and no env expansion is
// done — matching compose-go's behaviour rather than google/shlex, which strips
// `#` comments and errors on a trailing backslash.
func (c *Command) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*c = list
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("command: %w", err)
	}
	fields, err := shellwords.Parse(s)
	if err != nil {
		return fmt.Errorf("command %q: %w", s, err)
	}
	*c = fields
	return nil
}

// Environment is the service environment (map or list form).
type Environment map[string]string

// UnmarshalJSON accepts {KEY: VALUE} or ["KEY=VALUE", "KEY"].
func (e *Environment) UnmarshalJSON(data []byte) error {
	m, err := decodeKeyVals(data)
	if err != nil {
		return fmt.Errorf("environment: %w", err)
	}
	*e = m
	return nil
}

// Labels is the service `labels` metadata (map or list form). It shares the
// map-or-KEY=VALUE-list decoding with Environment.
type Labels map[string]string

// UnmarshalJSON accepts {KEY: VALUE} or ["KEY=VALUE", "KEY"].
func (l *Labels) UnmarshalJSON(data []byte) error {
	m, err := decodeKeyVals(data)
	if err != nil {
		return fmt.Errorf("labels: %w", err)
	}
	*l = m
	return nil
}

// Sysctls is the service `sysctls` map of namespaced kernel parameters. It
// shares the map-or-KEY=VALUE-list decoding with Environment/Labels, so both
// `sysctls: {net.core.somaxconn: 1024}` and `sysctls: ["net.core.somaxconn=1024"]`
// parse.
type Sysctls map[string]string

// UnmarshalJSON accepts {KEY: VALUE} or ["KEY=VALUE"].
func (s *Sysctls) UnmarshalJSON(data []byte) error {
	m, err := decodeKeyVals(data)
	if err != nil {
		return fmt.Errorf("sysctls: %w", err)
	}
	*s = m
	return nil
}

// Ulimit is one entry of a service `ulimits:` block (compose-spec). Name is the
// bare limit name ("nofile", "nproc"); Soft and Hard are the soft and hard
// bounds. The shorthand scalar form (`nproc: 65535`) sets Soft == Hard.
type Ulimit struct {
	Name string
	Soft int64
	Hard int64
}

// Ulimits is a service `ulimits:` map of limit-name -> bounds. YAML map order is
// not meaningful, so entries are sorted by name for deterministic output.
type Ulimits []Ulimit

// UnmarshalJSON accepts the compose map form where each value is either a bare
// integer (shorthand: soft == hard) or an object {soft, hard}:
//
//	ulimits:
//	  nproc: 65535
//	  nofile:
//	    soft: 20000
//	    hard: 40000
func (u *Ulimits) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("ulimits: %w", err)
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make(Ulimits, 0, len(names))
	for _, name := range names {
		raw := m[name]
		var n int64
		if err := json.Unmarshal(raw, &n); err == nil {
			out = append(out, Ulimit{Name: name, Soft: n, Hard: n})
			continue
		}
		var obj struct {
			Soft int64 `json:"soft"`
			Hard int64 `json:"hard"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("ulimits %q: %w", name, err)
		}
		out = append(out, Ulimit{Name: name, Soft: obj.Soft, Hard: obj.Hard})
	}
	*u = out
	return nil
}

// StringList is a Compose value that may be written as a single scalar or a
// list of scalars (compose `dns`, `dns_search`, `dns_opt`). Both forms decode
// to a flat slice.
type StringList []string

// UnmarshalJSON accepts "a" or ["a", "b"].
func (s *StringList) UnmarshalJSON(data []byte) error {
	out, err := decodeStringOrList(data)
	if err != nil {
		return fmt.Errorf("string list: %w", err)
	}
	*s = out
	return nil
}

// ExtraHosts is the service `extra_hosts` list of custom /etc/hosts entries.
// Compose accepts a list ("host:ip") or a map (host -> ip); both are normalised
// to "host:ip" strings. Map order is not meaningful in YAML, so map entries are
// sorted by host for determinism (mirroring the other map-form decoders).
type ExtraHosts []string

// UnmarshalJSON accepts ["host:ip", ...] or {host: ip, ...}.
func (e *ExtraHosts) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		out := make(ExtraHosts, 0, len(list))
		for _, s := range list {
			if s != "" {
				out = append(out, s)
			}
		}
		*e = out
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("extra_hosts: %w", err)
	}
	hosts := make([]string, 0, len(m))
	for h := range m {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	out := make(ExtraHosts, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h+":"+m[h])
	}
	*e = out
	return nil
}

// Compose depends_on conditions (compose-spec long form). These gate a
// dependent service's start on the dependency reaching the named state.
const (
	// DependsOnStarted waits until the dependency has started (the default and
	// the only condition the short list form can express).
	DependsOnStarted = "service_started"
	// DependsOnHealthy waits until the dependency reports healthy (requires a
	// healthcheck; backends without a health concept can never satisfy it).
	DependsOnHealthy = "service_healthy"
	// DependsOnCompleted waits until the dependency ran to completion with a
	// zero exit code (a one-shot/init dependency).
	DependsOnCompleted = "service_completed_successfully"
)

// Dependency is one entry of a service's depends_on: the dependency service
// name plus the long-form metadata (compose-spec). Condition defaults to
// service_started and Required defaults to true when omitted.
type Dependency struct {
	Service   string
	Condition string // service_started (default) | service_healthy | service_completed_successfully
	Required  bool   // whether the dependency's failure/absence aborts the dependent (default true)
	Restart   bool   // restart the dependent when the dependency is restarted (parsed for fidelity)
}

// DependsOn lists service dependencies (list or map form). It preserves the
// long-form per-dependency condition/required/restart metadata; the short list
// form yields service_started + required.
type DependsOn []Dependency

// Names returns the dependency service names in order (used for topological
// ordering, which is condition-agnostic).
func (d DependsOn) Names() []string {
	out := make([]string, len(d))
	for i, dep := range d {
		out[i] = dep.Service
	}
	return out
}

// dependencyOpts is the long-form value of one depends_on entry.
type dependencyOpts struct {
	Condition string `json:"condition"`
	Required  *bool  `json:"required"`
	Restart   bool   `json:"restart"`
}

// UnmarshalJSON accepts ["svc"] (short form: service_started, required) or
// {svc: {condition: ..., required: ..., restart: ...}} (long form). A null or
// empty map value takes the defaults. Map iteration is randomized, so entries
// are sorted by service name for deterministic ordering (mirroring the other
// map-form decoders in this file).
func (d *DependsOn) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		out := make(DependsOn, len(list))
		for i, name := range list {
			out[i] = Dependency{Service: name, Condition: DependsOnStarted, Required: true}
		}
		*d = out
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("depends_on: %w", err)
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make(DependsOn, 0, len(names))
	for _, name := range names {
		dep := Dependency{Service: name, Condition: DependsOnStarted, Required: true}
		raw := m[name]
		if len(raw) > 0 && string(raw) != "null" {
			var opts dependencyOpts
			if err := json.Unmarshal(raw, &opts); err != nil {
				return fmt.Errorf("depends_on %q: %w", name, err)
			}
			if opts.Condition != "" {
				dep.Condition = opts.Condition
			}
			if opts.Required != nil {
				dep.Required = *opts.Required
			}
			dep.Restart = opts.Restart
		}
		out = append(out, dep)
	}
	*d = out
	return nil
}

// ExposeList is the service `expose:` list — container ports advertised to
// other services on the same network without host-publishing. Accepts ints,
// numeric strings ("80"), or port-range strings ("3000-3005"), the latter
// expanded inclusively into individual ports.
type ExposeList []int

func (e *ExposeList) UnmarshalJSON(data []byte) error {
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("expose: %w", err)
	}
	for _, v := range raw {
		switch n := v.(type) {
		case float64:
			*e = append(*e, int(n))
		case string:
			start, end, err := parsePortRange(n)
			if err != nil {
				return fmt.Errorf("expose %q: %w", n, err)
			}
			for p := start; p <= end; p++ {
				*e = append(*e, p)
			}
		default:
			return fmt.Errorf("expose: unsupported entry %v", v)
		}
	}
	return nil
}

// Port is a published port mapping.
type Port struct {
	Host      int
	Container int
	Protocol  string
	// HostIP is the optional host interface a published port binds to (the
	// leading component of "ip:host:container"). Empty binds all interfaces.
	HostIP string
}

// Ports is the service `ports:` list. It decodes each YAML list element into one
// or more Port values: a single element may be a port range (e.g.
// "8000-8010:8000-8010"), which Compose expands into one mapping per port, so a
// per-element decode is done at the slice level to allow that fan-out.
type Ports []Port

// UnmarshalJSON decodes the raw list, flattening any range element into the
// individual Port mappings it expands to.
func (ps *Ports) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("ports: %w", err)
	}
	out := make(Ports, 0, len(raw))
	for _, item := range raw {
		ports, err := parsePortEntry(item)
		if err != nil {
			return err
		}
		out = append(out, ports...)
	}
	*ps = out
	return nil
}

// parsePortEntry decodes one `ports:` list element. The short string form
// ("8080:80", "8080:80/tcp", "80", "ip:8080:80", and their range variants)
// may expand into multiple mappings; the long object form
// {target, published, protocol} yields a single mapping.
func parsePortEntry(data json.RawMessage) ([]Port, error) {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return parsePortString(s)
	}
	var obj struct {
		Target    int    `json:"target"`
		Published any    `json:"published"`
		Protocol  string `json:"protocol"`
		HostIP    string `json:"host_ip"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("port: %w", err)
	}
	p := Port{Container: obj.Target, Protocol: obj.Protocol, HostIP: obj.HostIP}
	switch v := obj.Published.(type) {
	case string:
		p.Host, _ = strconv.Atoi(v)
	case float64:
		p.Host = int(v)
	}
	if p.Host == 0 {
		p.Host = p.Container
	}
	return []Port{p}, nil
}

// parsePortString parses a short-form port string, expanding ranges. A bare
// range ("3000-3005") maps the published range to an equal container range; a
// paired range ("A-B:C-D") expands pairwise and requires equal lengths.
func parsePortString(s string) ([]Port, error) {
	proto := "tcp"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto = s[i+1:]
		s = s[:i]
	}
	// Capture an optional leading host-IP component. IPv6 literals are bracketed
	// ("[::1]:8080:80") so the address's own colons are not confused with the
	// host/container separators; an IPv4 host IP ("127.0.0.1:8080:80") is simply
	// the first of three colon-separated fields. Every port a range expands to
	// shares this same host IP.
	var hostIP string
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return nil, fmt.Errorf("invalid port %q: unbalanced brackets", s)
		}
		hostIP = s[1:end]
		rest := s[end+1:]
		if !strings.HasPrefix(rest, ":") {
			return nil, fmt.Errorf("invalid port %q", s)
		}
		s = rest[1:]
	}
	parts := strings.Split(s, ":")
	if hostIP == "" && len(parts) == 3 {
		hostIP = parts[0]
		parts = parts[1:]
	}
	var hostSpec, containerSpec string
	switch len(parts) {
	case 1:
		hostSpec, containerSpec = parts[0], parts[0]
	case 2:
		hostSpec, containerSpec = parts[0], parts[1]
	default:
		return nil, fmt.Errorf("invalid port %q", s)
	}
	hostStart, hostEnd, err := parsePortRange(hostSpec)
	if err != nil {
		return nil, fmt.Errorf("port %q: %w", s, err)
	}
	contStart, contEnd, err := parsePortRange(containerSpec)
	if err != nil {
		return nil, fmt.Errorf("port %q: %w", s, err)
	}
	hostLen, contLen := hostEnd-hostStart, contEnd-contStart
	if hostLen != contLen {
		// A host-port range mapped to a single container port ("8000-8010:80"):
		// Docker picks an ephemeral host port from the range, which cornus's
		// fixed-host-port model cannot express. Approximate to the FIRST host port
		// and warn, rather than failing the load. A single host port with a
		// container range, or two ranges of unequal length, is genuinely invalid.
		if hostLen > 0 && contLen == 0 {
			// Pure parse-time helper with no request context; log against the default.
			ctx := context.Background()
			logging.FromContext(ctx).WarnContext(ctx, "host-port range narrowed to its first port; range selection is unsupported",
				slog.Group("compose", "port", s, "chosen", hostStart))
			return []Port{{Host: hostStart, Container: contStart, Protocol: proto, HostIP: hostIP}}, nil
		}
		return nil, fmt.Errorf("port %q: range lengths differ (host %d, container %d)", s, hostLen+1, contLen+1)
	}
	out := make([]Port, 0, hostLen+1)
	for i := 0; i <= hostLen; i++ {
		out = append(out, Port{Host: hostStart + i, Container: contStart + i, Protocol: proto, HostIP: hostIP})
	}
	return out, nil
}

// parsePortRange parses a single port ("80") or a range ("8000-8010") into its
// inclusive [start, end] bounds. A single port yields start == end.
func parsePortRange(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "-"); i >= 0 {
		start, err := strconv.Atoi(strings.TrimSpace(s[:i]))
		if err != nil {
			return 0, 0, fmt.Errorf("range %q: %w", s, err)
		}
		end, err := strconv.Atoi(strings.TrimSpace(s[i+1:]))
		if err != nil {
			return 0, 0, fmt.Errorf("range %q: %w", s, err)
		}
		if end < start {
			return 0, 0, fmt.Errorf("range %q: end before start", s)
		}
		if err := validatePortBound(start); err != nil {
			return 0, 0, fmt.Errorf("range %q: %w", s, err)
		}
		if err := validatePortBound(end); err != nil {
			return 0, 0, fmt.Errorf("range %q: %w", s, err)
		}
		return start, end, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, err
	}
	if err := validatePortBound(n); err != nil {
		return 0, 0, err
	}
	return n, n, nil
}

// validatePortBound rejects a user-specified port outside the valid TCP/UDP
// range. Port 0 is permitted here because it is a legitimate user value and the
// "default to container port" sentinel lives downstream in parsePortEntry, not
// in the parsed range endpoints.
func validatePortBound(n int) error {
	if n < 0 || n > 65535 {
		return fmt.Errorf("port %d out of range (0-65535)", n)
	}
	return nil
}

// Volume is a bind/volume mount.
type Volume struct {
	Source   string
	Target   string
	ReadOnly bool
	// SELinux carries the `:z` (shared) or `:Z` (private) relabel option from the
	// mount, or "" when none. Only meaningful for bind mounts.
	SELinux string
	// Named is true when Source is a named volume rather than a host path.
	Named bool
	// NoCreateHostPath disables auto-creating a missing bind source. It is the
	// inverse of compose's long-syntax `bind.create_host_path` (default true), so
	// the zero value keeps Docker's default (create). Only set when the long form
	// carries `create_host_path: false`. Only meaningful for bind mounts.
	NoCreateHostPath bool
}

// UnmarshalJSON accepts "src:dst", "src:dst:ro", "src:dst:ro,z", or the long
// object form {type, source, target, read_only, bind: {selinux}}.
func (v *Volume) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return v.parseString(s)
	}
	var obj struct {
		Type     string `json:"type"`
		Source   string `json:"source"`
		Target   string `json:"target"`
		ReadOnly bool   `json:"read_only"`
		Bind     *struct {
			SELinux string `json:"selinux"`
			// CreateHostPath defaults to true in compose; a pointer distinguishes an
			// explicit `false` from an absent key.
			CreateHostPath *bool `json:"create_host_path"`
		} `json:"bind"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("volume: %w", err)
	}
	v.Source, v.Target, v.ReadOnly = obj.Source, obj.Target, obj.ReadOnly
	if obj.Bind != nil {
		v.SELinux = normalizeSELinux(obj.Bind.SELinux)
		if obj.Bind.CreateHostPath != nil && !*obj.Bind.CreateHostPath {
			v.NoCreateHostPath = true
		}
	}
	v.Named = obj.Type == "volume"
	return nil
}

func (v *Volume) parseString(s string) error {
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		// Anonymous volume at a container path; nothing to bind.
		v.Target = parts[0]
		v.Named = true
	case 2:
		v.Source, v.Target = parts[0], parts[1]
	case 3:
		v.Source, v.Target = parts[0], parts[1]
		// The options field is comma-separated (e.g. "ro,z"): recognise the
		// read-only flag and the SELinux relabel token (z/Z). The shared ("z") and
		// private ("Z") relabel tokens are mutually exclusive, so a value carrying
		// both (e.g. "z,Z") is an error rather than a silent last-wins.
		for _, opt := range strings.Split(parts[2], ",") {
			switch opt := strings.TrimSpace(opt); opt {
			case "ro":
				v.ReadOnly = true
			case "rw":
				v.ReadOnly = false
			case "z", "Z":
				if v.SELinux != "" && v.SELinux != opt {
					return fmt.Errorf("volume %q: conflicting SELinux relabel options z and Z", s)
				}
				v.SELinux = opt
			}
		}
	default:
		return fmt.Errorf("invalid volume %q", s)
	}
	if v.Source != "" && !isHostPath(v.Source) {
		v.Named = true
	}
	return nil
}

// normalizeSELinux maps a long-form bind.selinux value to the canonical "z"/"Z"
// relabel token, or "" when unset/unrecognised.
func normalizeSELinux(s string) string {
	switch strings.TrimSpace(s) {
	case "z", "Z":
		return strings.TrimSpace(s)
	}
	return ""
}

// isHostPath reports whether a volume source is a host path (bind mount) rather
// than a named volume.
func isHostPath(src string) bool {
	return strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~")
}

// decodeKeyVals decodes a Compose map-or-list of key/values into a map. List
// entries are "KEY=VALUE" (a bare "KEY" yields an empty value). Scalar map
// values (numbers, bools) are stringified.
func decodeKeyVals(data json.RawMessage) (map[string]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		out := make(map[string]string, len(list))
		for _, item := range list {
			k, v, _ := strings.Cut(item, "=")
			out[k] = v
		}
		return out, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = scalarString(v)
	}
	return out, nil
}

func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}
