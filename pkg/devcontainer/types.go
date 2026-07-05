// Package devcontainer parses a Dev Container definition
// (`.devcontainer/devcontainer.json`, https://containers.dev) and translates it
// into the same model `cornus compose` already drives: a compose.Project plus a
// side channel of lifecycle hooks. It supports the single-container flavor
// (`image` / `build.dockerfile`) and the compose-based flavor
// (`dockerComposeFile` + `service` + `runServices`), reusing the compose package
// for translation to api.DeploySpec.
package devcontainer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// spec is the subset of the Dev Container schema cornus acts on. Fields it does
// not implement (features, customizations, hostRequirements, ...) are decoded as
// raw JSON only so their presence can be reported as a warning.
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
// registry cache imports); Options is still parsed only to warn.
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
	Type     string // "bind" (default) or "volume"
	ReadOnly bool
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
			m, err := parseMountString(s)
			if err != nil {
				return err
			}
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
		m := Mount{Source: obj.Source, Target: obj.Target, Type: obj.Type}
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
func parseMountString(s string) (Mount, error) {
	var m Mount
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
		}
	}
	if m.Target == "" {
		return Mount{}, fmt.Errorf("mount %q: missing target", s)
	}
	return m, nil
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

// shellArgv wraps a shell command string in the container's default shell.
func shellArgv(s string) []string {
	return []string{"/bin/sh", "-c", s}
}
