// Package devcontainer parses a Dev Container definition
// (`.devcontainer/devcontainer.json`, https://containers.dev) and translates it
// into the same model `cornus compose` already drives: a compose.Project plus a
// side channel of lifecycle hooks. It supports the single-container flavor
// (`image` / `build.dockerfile`) and the compose-based flavor
// (`dockerComposeFile` + `service` + `runServices`), reusing the compose package
// for translation to api.DeploySpec.
//
// # Supported schema subset
//
// cornus implements a deliberate subset of the Dev Container schema. The
// boundary is a contract with two halves, and TestCompatibilityBoundary /
// TestSupportedSubsetIsSilent hold both:
//
//   - Implemented — applied to the deployed container, and silent:
//     name, image, build (dockerfile, context, args, target, cacheFrom, and the
//     expressible part of options), workspaceFolder, workspaceMount, mounts
//     (bind, volume, tmpfs), forwardPorts, appPort, containerEnv, remoteEnv,
//     runArgs (the expressible part; single-container only), overrideCommand,
//     containerUser, remoteUser, dockerComposeFile, service, runServices, and
//     every lifecycle command. `name` sets the project name only when the
//     compose file does not name the project itself.
//   - Recognised but not implemented — always reported in Result.Warnings,
//     naming the field and what happens instead: features, customizations,
//     hostRequirements, runArgs on the compose-based flavor, image/build on the
//     compose-based flavor, and any individual runArgs / build.options flag or
//     mount option with no cornus equivalent.
//
// A recognised field must never fall outside those two lists: silently
// half-applying a definition is the one behaviour this package must not have.
// Anything else in the schema is not decoded at all.
package devcontainer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// spec is the subset of the Dev Container schema cornus acts on. Every field
// here is either applied to the resulting compose project or reported in
// Result.Warnings — nothing this struct decodes may be dropped silently, which
// is what TestCompatibilityBoundary pins. Fields cornus does not implement at
// all (features, customizations, hostRequirements) are decoded as raw JSON only
// so their presence can be warned about.
type spec struct {
	Name  string     `json:"name"`
	Image string     `json:"image"`
	Build *buildSpec `json:"build"`

	// Single-container placement.
	WorkspaceFolder string    `json:"workspaceFolder"`
	WorkspaceMount  string    `json:"workspaceMount"`
	Mounts          MountList `json:"mounts"`
	ForwardPorts    PortList  `json:"forwardPorts"`
	AppPort         PortList  `json:"appPort"`
	ContainerEnv    StringMap `json:"containerEnv"`
	RemoteEnv       StringMap `json:"remoteEnv"`
	RunArgs         []string  `json:"runArgs"`
	OverrideCommand *bool     `json:"overrideCommand"`
	ContainerUser   string    `json:"containerUser"`
	RemoteUser      string    `json:"remoteUser"`

	// Compose-based.
	DockerComposeFile StringList `json:"dockerComposeFile"`
	Service           string     `json:"service"`
	RunServices       []string   `json:"runServices"`

	// Lifecycle commands.
	InitializeCommand    *LifecycleCommand `json:"initializeCommand"`
	OnCreateCommand      *LifecycleCommand `json:"onCreateCommand"`
	UpdateContentCommand *LifecycleCommand `json:"updateContentCommand"`
	PostCreateCommand    *LifecycleCommand `json:"postCreateCommand"`
	PostStartCommand     *LifecycleCommand `json:"postStartCommand"`
	PostAttachCommand    *LifecycleCommand `json:"postAttachCommand"`

	// Detected-but-unimplemented fields, kept raw purely to warn when non-empty.
	Features         json.RawMessage `json:"features"`
	Customizations   json.RawMessage `json:"customizations"`
	HostRequirements json.RawMessage `json:"hostRequirements"`
}

// buildSpec is a devcontainer `build:` object. Target and CacheFrom are threaded
// through the build wire (build.target -> frontend "target" stage; cacheFrom ->
// registry cache imports); Options is a `docker build` argv whose expressible
// flags are mapped onto compose.Build (see applyBuildOptions) and whose
// remainder is warned about.
type buildSpec struct {
	Dockerfile string     `json:"dockerfile"`
	Context    string     `json:"context"`
	Args       StringMap  `json:"args"`
	Target     string     `json:"target"`
	CacheFrom  StringList `json:"cacheFrom"`
	Options    []string   `json:"options"`
}

// StringList accepts a bare string or a list of strings ("a" or ["a","b"]).
type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	// A JSON null decodes into a bare string as "" with a nil error, which would
	// otherwise yield StringList{""} (length 1) and be mistaken for a real
	// single-element list. Treat null as an empty list explicitly.
	if string(data) == "null" {
		*s = nil
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = StringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("expected string or list of strings: %w", err)
	}
	*s = many
	return nil
}

// StringMap decodes an object whose values may be scalars (string/number/bool),
// stringifying non-string values (mirroring compose's decodeKeyVals).
type StringMap map[string]string

func (m *StringMap) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(StringMap, len(raw))
	for k, v := range raw {
		out[k] = scalarToString(v)
	}
	*m = out
	return nil
}

func scalarToString(v any) string {
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

// Mount is one devcontainer `mounts` entry.
type Mount struct {
	Source   string
	Target   string
	Type     string // "bind" (default), "volume", or "tmpfs"
	ReadOnly bool
	// Spec is the entry as written (the mount string, or the target for the
	// object form), used to name the entry in warnings.
	Spec string
	// Unsupported lists the mount-string options cornus recognised as options but
	// cannot honour (e.g. bind-propagation), so the caller can warn about them.
	Unsupported []string
}

// MountList accepts a list whose entries are either a docker mount string
// ("source=...,target=...,type=bind") or an object {source,target,type}.
type MountList []Mount

func (l *MountList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("mounts: %w", err)
	}
	out := make(MountList, 0, len(raw))
	for _, item := range raw {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			m, unknown, err := parseMountString(s)
			if err != nil {
				return err
			}
			m.Unsupported = unknown
			m.Spec = s
			out = append(out, m)
			continue
		}
		var obj struct {
			Source   string `json:"source"`
			Target   string `json:"target"`
			Type     string `json:"type"`
			ReadOnly *bool  `json:"readonly"`
		}
		if err := json.Unmarshal(item, &obj); err != nil {
			return fmt.Errorf("mounts entry: %w", err)
		}
		m := Mount{Source: obj.Source, Target: obj.Target, Type: obj.Type, Spec: obj.Target}
		if obj.ReadOnly != nil {
			m.ReadOnly = *obj.ReadOnly
		}
		out = append(out, m)
	}
	*l = out
	return nil
}

// parseMountString parses the comma-separated docker `--mount` form
// ("source=...,target=...,type=bind,readonly"). Keys are matched case-
// insensitively with the common aliases (src/source, dst/destination/target).
// Options cornus cannot honour are returned as unsupported rather than dropped
// on the floor; "consistency" is excluded because it is a macOS-only
// performance hint that is a no-op wherever cornus runs the container.
func parseMountString(s string) (m Mount, unsupported []string, err error) {
	for _, field := range strings.Split(s, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, val, hasVal := strings.Cut(field, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "source", "src":
			m.Source = val
		case "target", "destination", "dst":
			m.Target = val
		case "type":
			m.Type = val
		case "readonly", "ro":
			// Bare "readonly" or "readonly=true".
			m.ReadOnly = !hasVal || val == "" || val == "true" || val == "1"
		case "consistency":
			// macOS-only performance hint; a no-op here, so not worth a warning.
		default:
			unsupported = append(unsupported, key)
		}
	}
	if m.Target == "" {
		return Mount{}, nil, fmt.Errorf("mount %q: missing target", s)
	}
	return m, unsupported, nil
}

// Port is a resolved published-port mapping.
type Port struct {
	Host      int
	Container int
}

// PortList accepts a single port or a list; each entry is an int, "port", or
// "host:port" (an optional leading host-IP component is dropped).
type PortList []Port

func (l *PortList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Not a list: try a single scalar entry.
		p, err := parsePort(data)
		if err != nil {
			return fmt.Errorf("port: %w", err)
		}
		*l = PortList{p}
		return nil
	}
	out := make(PortList, 0, len(raw))
	for _, item := range raw {
		p, err := parsePort(item)
		if err != nil {
			return err
		}
		out = append(out, p)
	}
	*l = out
	return nil
}

func parsePort(data json.RawMessage) (Port, error) {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		return Port{Host: n, Container: n}, nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return Port{}, fmt.Errorf("expected int or string, got %s", data)
	}
	parts := strings.Split(s, ":")
	// Drop a leading host-IP (ip:host:container).
	if len(parts) == 3 {
		parts = parts[1:]
	}
	nums := make([]int, len(parts))
	for i, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return Port{}, fmt.Errorf("port %q: %w", s, err)
		}
		nums[i] = v
	}
	switch len(nums) {
	case 1:
		return Port{Host: nums[0], Container: nums[0]}, nil
	case 2:
		return Port{Host: nums[0], Container: nums[1]}, nil
	default:
		return Port{}, fmt.Errorf("invalid port %q", s)
	}
}

// LifecycleCommand is a devcontainer lifecycle command in any of its three
// forms: a shell string, an argv list, or an object mapping labels to
// string/argv commands (which run in parallel). Commands holds one argv per
// resolved command; a shell string becomes ["/bin/sh","-c",<s>].
type LifecycleCommand struct {
	Commands [][]string
}

func (c *LifecycleCommand) UnmarshalJSON(data []byte) error {
	// String form: run via the shell.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.Commands = [][]string{shellArgv(s)}
		return nil
	}
	// Argv form.
	var argv []string
	if err := json.Unmarshal(data, &argv); err == nil {
		c.Commands = [][]string{argv}
		return nil
	}
	// Object form: label -> string|argv, run in parallel. Sort by label for
	// deterministic ordering (map order is not meaningful).
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("lifecycle command: %w", err)
	}
	labels := make([]string, 0, len(obj))
	for k := range obj {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	for _, label := range labels {
		var one string
		if err := json.Unmarshal(obj[label], &one); err == nil {
			c.Commands = append(c.Commands, shellArgv(one))
			continue
		}
		var oneArgv []string
		if err := json.Unmarshal(obj[label], &oneArgv); err != nil {
			return fmt.Errorf("lifecycle command %q: %w", label, err)
		}
		c.Commands = append(c.Commands, oneArgv)
	}
	return nil
}

// substitute expands devcontainer variables in every argv element in place. It
// is called with a substituter that does NOT report unresolved references: a
// lifecycle command is handed to a shell, where a leftover ${FOO} is a
// legitimate shell variable rather than a devcontainer one.
func (c *LifecycleCommand) substitute(sub func(string) string) {
	if c == nil {
		return
	}
	for i, argv := range c.Commands {
		for j := range argv {
			c.Commands[i][j] = sub(argv[j])
		}
	}
}

// shellArgv wraps a shell command string in the container's default shell.
func shellArgv(s string) []string {
	return []string{"/bin/sh", "-c", s}
}
