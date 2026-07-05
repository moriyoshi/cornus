package compose

// This file implements compose-spec field-level deep merge for multi-file
// Compose loading (`-f base.yaml -f override.yaml`), for `extends`, and for
// `include`. The merge rules follow the compose-spec "Merge and override"
// section (https://github.com/compose-spec/compose-spec/blob/master/13-merge.md)
// and compose-go's behaviour, applied to the subset of fields cornus models on
// the typed structs in types.go.
//
// Key PRESENCE, not just "non-zero". Compose overrides a scalar when the key is
// *present* in the later file, so `privileged: false` in an override turns an
// inherited `privileged: true` back off, and `image: ""` explicitly clears it.
// cornus decodes each file into typed Go structs, where an absent key and an
// explicit zero value are indistinguishable, so the typed document alone cannot
// express that. To recover it, parseFile keeps a yaml.v3 *Node pass over the raw
// file alongside the typed decode and distils it into a *yamlMeta — a tree of
// "which keys did this file actually write, and what YAML tag did each carry".
// mergeService and friends consult that tree.
//
// The rule is therefore "present, or (failing that) non-zero":
//
//   - meta says the key is present -> the override's value wins verbatim, zero
//     or not. This is what makes a boolean turn back off and a scalar clear.
//   - meta is nil / silent about the key (no YAML tree is available for this
//     merge, e.g. a value that reached the service through `extends` rather than
//     being written in the file) -> fall back to the historical "non-zero
//     override wins" rule, so nothing that merged before stops merging now.
//
// !reset / !override. compose-spec lets an override explicitly clear or
// wholesale-replace a base value with the custom YAML tags `!reset` (null out)
// and `!override` (replace instead of merge). sigs.k8s.io/yaml round-trips
// YAML -> JSON and drops custom tags (it decodes a tagged scalar as a plain
// string, so `privileged: !reset` would even fail the typed decode), so the tags
// are harvested from the same yaml.v3 node pass: parseMergeMeta records them on
// the meta tree and rewrites the node tree so the typed decode still sees a
// well-formed document (`!reset` becomes an explicit null, `!override` loses its
// tag). The merge then honours them:
//
//   - `!reset` drops the inherited value entirely — for a scalar that falls out
//     of the presence rule above (the key is present and decodes to zero); for a
//     sequence/mapping/block it forces an empty result instead of a merge.
//   - `!override` replaces instead of merging: an additive sequence takes only
//     the override's entries, a mapping takes only the override's keys, and a
//     nested block (build/deploy/healthcheck/...) is swapped in wholesale rather
//     than field-merged.
//
// Without a tag, list and mapping merging is unchanged: sequences concatenate
// (base first, exact duplicates dropped) and mappings merge key-by-key. An
// override that merely writes an EMPTY sequence or mapping still merges (adding
// nothing) rather than clearing — matching compose-go, which is exactly why
// `!reset` exists.

import (
	yamlv3 "gopkg.in/yaml.v3"
)

const (
	// resetTag is the compose-spec `!reset` tag: drop the inherited value.
	resetTag = "!reset"
	// overrideTag is the compose-spec `!override` tag: replace the inherited
	// value instead of merging into it.
	overrideTag = "!override"
	// metaMaxDepth bounds the node walk so a pathological document (or an alias
	// chain) cannot recurse without end.
	metaMaxDepth = 100
)

// yamlMeta is the presence-and-tag view of one node of a parsed Compose file:
// which keys the file wrote at this position (fields), and whether the value at
// this position carried `!reset` or `!override`. It mirrors the shape of the
// YAML mapping tree, so merge code walks it in step with the typed structs
// (m.at("build").at("args")).
//
// Every accessor is nil-safe, and a nil *yamlMeta means "no presence
// information available" — the merge then behaves exactly as it did before
// presence tracking existed (non-zero override wins, sequences concatenate).
type yamlMeta struct {
	reset    bool // the value carried `!reset`
	override bool // the value carried `!override`
	// fields maps each key the mapping wrote to that value's own metadata. It is
	// nil for scalars and sequences. A key present with a null value still has an
	// entry — presence is what the merge asks about.
	fields map[string]*yamlMeta
}

// at returns the metadata for key, or nil when this file did not write that key
// (or when there is no metadata at all).
func (m *yamlMeta) at(key string) *yamlMeta {
	if m == nil {
		return nil
	}
	return m.fields[key]
}

// has reports whether the file wrote key at this position, regardless of the
// value it wrote (including an explicit null, false, or "").
func (m *yamlMeta) has(key string) bool { return m.at(key) != nil }

// cleared reports whether the value at this position carried `!reset`.
func (m *yamlMeta) cleared() bool { return m != nil && m.reset }

// replaced reports whether the value at this position carried `!override`.
func (m *yamlMeta) replaced() bool { return m != nil && m.override }

// parseMergeMeta parses data as a YAML node tree and distils it into the merge
// metadata for the file. It returns the metadata and the YAML bytes the typed
// decode should use: the ORIGINAL bytes when the file carries no `!reset` /
// `!override` tag (the overwhelmingly common case, so the existing decode path
// is bit-for-bit unchanged), or a re-emitted document with those tags resolved
// away when it does — `!reset` becomes an explicit null so the typed decode
// yields the zero value, and `!override` loses its tag so the value resolves
// normally instead of degrading to a string.
func parseMergeMeta(data []byte) (*yamlMeta, []byte, error) {
	var root yamlv3.Node
	if err := yamlv3.Unmarshal(data, &root); err != nil {
		return nil, nil, err
	}
	if root.Kind == 0 {
		return nil, data, nil // empty document
	}
	tagged := false
	m := metaFromNode(&root, &tagged, 0)
	if !tagged {
		return m, data, nil
	}
	clean, err := yamlv3.Marshal(&root)
	if err != nil {
		return nil, nil, err
	}
	return m, clean, nil
}

// metaFromNode builds the metadata for one node, recording and stripping any
// `!reset` / `!override` tag it carries (setting *tagged when it strips one, so
// the caller only pays for re-emitting a document that actually uses them).
func metaFromNode(n *yamlv3.Node, tagged *bool, depth int) *yamlMeta {
	m := &yamlMeta{}
	if n == nil || depth > metaMaxDepth {
		return m
	}
	if n.Kind == yamlv3.DocumentNode {
		if len(n.Content) == 1 {
			return metaFromNode(n.Content[0], tagged, depth+1)
		}
		return m
	}
	switch n.Tag {
	case resetTag:
		// Rewrite the node to an explicit null so the typed decode produces the
		// zero value; the recorded reset flag tells the merge to drop the base's
		// value rather than keep it. The anchor is preserved so any alias to this
		// node still resolves on re-emission.
		m.reset = true
		*tagged = true
		n.Kind = yamlv3.ScalarNode
		n.Tag = "!!null"
		n.Value = "null"
		n.Style = 0
		n.Content = nil
		return m
	case overrideTag:
		m.override = true
		*tagged = true
		n.Tag = "" // re-resolved from the value and style on re-emission
	}
	switch n.Kind {
	case yamlv3.AliasNode:
		// An alias contributes its target's keys at this position.
		target := metaFromNode(n.Alias, tagged, depth+1)
		return &yamlMeta{reset: m.reset, override: m.override, fields: target.fields}
	case yamlv3.MappingNode:
	default:
		return m // scalars and sequences have no per-key presence
	}
	m.fields = make(map[string]*yamlMeta, len(n.Content)/2)
	// mergeKeys collects `<<:` sources in precedence order; they fill in keys the
	// mapping does not write itself.
	var mergeKeys []*yamlMeta
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yamlv3.ScalarNode {
			continue
		}
		if k.Value == "<<" {
			if v.Kind == yamlv3.SequenceNode {
				for _, e := range v.Content {
					mergeKeys = append(mergeKeys, metaFromNode(e, tagged, depth+1))
				}
			} else {
				mergeKeys = append(mergeKeys, metaFromNode(v, tagged, depth+1))
			}
			continue
		}
		m.fields[k.Value] = metaFromNode(v, tagged, depth+1)
	}
	for _, src := range mergeKeys {
		for k, v := range src.fields {
			if _, ok := m.fields[k]; !ok {
				m.fields[k] = v
			}
		}
	}
	return m
}

// overrideScalar returns the merged value of a scalar field: the override wins
// when the file wrote the key (so an explicit false / "" / 0, including the one
// `!reset` decodes to, clears the base), else when it is non-zero. See the
// "present, or failing that non-zero" rule at the top of this file.
func overrideScalar[T comparable](m *yamlMeta, key string, base, override T) T {
	if m.has(key) {
		return override
	}
	var zero T
	if override != zero {
		return override
	}
	return base
}

// overridePtr is overrideScalar for a pointer-shaped field (`init`, and the
// cohesive x-cornus-* blocks): a written key wins even when it decoded to nil.
func overridePtr[T any](m *yamlMeta, key string, base, override *T) *T {
	if m.has(key) {
		return override
	}
	if override != nil {
		return override
	}
	return base
}

// overrideWhole returns the merged value of a sequence that is a single logical
// value rather than an additive list (command, entrypoint, healthcheck.test):
// the override replaces the base rather than concatenating, and a written key
// wins even when empty.
func overrideWhole[T any](m *yamlMeta, key string, base, override []T) []T {
	if m.has(key) {
		return override
	}
	if len(override) > 0 {
		return override
	}
	return base
}

// mergeSeq merges an additive sequence field: base then override with exact
// duplicates dropped, unless the override tagged the key `!reset` (empty result)
// or `!override` (the override's entries only).
func mergeSeq[T comparable](m *yamlMeta, key string, base, override []T) []T {
	switch c := m.at(key); {
	case c.cleared():
		return nil
	case c.replaced():
		return appendDedup(nil, override)
	default:
		return appendDedup(base, override)
	}
}

// mergeMapping merges a mapping field key-by-key with the override winning on a
// clash, unless the override tagged the key `!reset` (empty result) or
// `!override` (the override's keys only).
func mergeMapping(m *yamlMeta, key string, base, override map[string]string) map[string]string {
	switch c := m.at(key); {
	case c.cleared():
		return nil
	case c.replaced():
		return mergeStringMap(nil, override)
	default:
		return mergeStringMap(base, override)
	}
}

// mergeService deep-merges override onto base (override wins) following the
// compose-spec rules described above, and returns the combined Service. m is the
// merge metadata for the OVERRIDE's own service mapping (nil when unavailable).
// It is used by LoadDocumentWithOptions when a later file redefines a service
// already present from an earlier file, by resolveExtends, and by include.
func mergeService(m *yamlMeta, base, override ServiceDocument) ServiceDocument {
	out := base

	// Scalars: the override wins when the key is written, else when non-zero.
	out.Image = overrideScalar(m, "image", base.Image, override.Image)
	out.Restart = overrideScalar(m, "restart", base.Restart, override.Restart)
	out.ContainerName = overrideScalar(m, "container_name", base.ContainerName, override.ContainerName)
	out.Privileged = overrideScalar(m, "privileged", base.Privileged, override.Privileged)
	out.User = overrideScalar(m, "user", base.User, override.User)
	out.WorkingDir = overrideScalar(m, "working_dir", base.WorkingDir, override.WorkingDir)
	out.Hostname = overrideScalar(m, "hostname", base.Hostname, override.Hostname)
	out.StopSignal = overrideScalar(m, "stop_signal", base.StopSignal, override.StopSignal)
	out.StopGracePeriod = overrideScalar(m, "stop_grace_period", base.StopGracePeriod, override.StopGracePeriod)
	out.Init = overridePtr(m, "init", base.Init, override.Init)
	out.TTY = overrideScalar(m, "tty", base.TTY, override.TTY)
	out.StdinOpen = overrideScalar(m, "stdin_open", base.StdinOpen, override.StdinOpen)
	out.ReadOnly = overrideScalar(m, "read_only", base.ReadOnly, override.ReadOnly)
	out.ShmSize = overrideScalar(m, "shm_size", base.ShmSize, override.ShmSize)
	out.PID = overrideScalar(m, "pid", base.PID, override.PID)
	out.IPC = overrideScalar(m, "ipc", base.IPC, override.IPC)
	out.MemLimit = overrideScalar(m, "mem_limit", base.MemLimit, override.MemLimit)
	out.CPUs = overrideScalar(m, "cpus", base.CPUs, override.CPUs)
	out.AgentForward = overrideScalar(m, "x-cornus-agent-forward", base.AgentForward, override.AgentForward)

	// labels merge key-by-key, override winning on conflicting keys.
	out.Labels = Labels(mergeMapping(m, "labels", map[string]string(base.Labels), map[string]string(override.Labels)))

	// command / entrypoint are a single logical value: an override list resets
	// and replaces the base rather than concatenating (compose-spec).
	out.Command = overrideWhole(m, "command", base.Command, override.Command)
	out.Entrypoint = overrideWhole(m, "entrypoint", base.Entrypoint, override.Entrypoint)

	// x-cornus-shells is a preference ORDER, so it is a single logical value too:
	// concatenating two lists would silently rank the base's entries above the
	// override's, which is the opposite of what an override file is for.
	out.Shells = overrideWhole(m, "x-cornus-shells", base.Shells, override.Shells)

	// Mappings merge key-by-key, override winning on conflicting keys.
	out.Environment = Environment(mergeMapping(m, "environment", map[string]string(base.Environment), map[string]string(override.Environment)))

	// Additive sequences concatenate (base then override), dropping exact dupes.
	out.EnvFile = mergeSeq(m, "env_file", base.EnvFile, override.EnvFile)
	out.Ports = mergeSeq(m, "ports", base.Ports, override.Ports)
	out.Expose = mergeSeq(m, "expose", base.Expose, override.Expose)
	out.Volumes = mergeSeq(m, "volumes", base.Volumes, override.Volumes)
	out.Profiles = mergeSeq(m, "profiles", base.Profiles, override.Profiles)

	// Service-level grants of top-level configs:/secrets: are additive too.
	out.Configs = mergeSeq(m, "configs", base.Configs, override.Configs)
	out.Secrets = mergeSeq(m, "secrets", base.Secrets, override.Secrets)

	// Security & networking list keys are additive too (append-dedup); sysctls
	// is a mapping and merges key-by-key with the override winning on a clash.
	out.CapAdd = mergeSeq(m, "cap_add", base.CapAdd, override.CapAdd)
	out.CapDrop = mergeSeq(m, "cap_drop", base.CapDrop, override.CapDrop)
	out.SecurityOpt = mergeSeq(m, "security_opt", base.SecurityOpt, override.SecurityOpt)
	out.GroupAdd = mergeSeq(m, "group_add", base.GroupAdd, override.GroupAdd)
	out.ExtraHosts = ExtraHosts(mergeSeq(m, "extra_hosts", []string(base.ExtraHosts), []string(override.ExtraHosts)))
	out.DNS = StringList(mergeSeq(m, "dns", []string(base.DNS), []string(override.DNS)))
	out.DNSSearch = StringList(mergeSeq(m, "dns_search", []string(base.DNSSearch), []string(override.DNSSearch)))
	out.DNSOpt = StringList(mergeSeq(m, "dns_opt", []string(base.DNSOpt), []string(override.DNSOpt)))
	out.Sysctls = Sysctls(mergeMapping(m, "sysctls", map[string]string(base.Sysctls), map[string]string(override.Sysctls)))

	// tmpfs / devices are additive sequences (append-dedup, base first).
	out.Tmpfs = StringList(mergeSeq(m, "tmpfs", []string(base.Tmpfs), []string(override.Tmpfs)))
	out.Devices = StringList(mergeSeq(m, "devices", []string(base.Devices), []string(override.Devices)))
	// ulimits is a mapping keyed by limit name; override wins on a shared name.
	out.Ulimits = mergeUlimits(m.at("ulimits"), base.Ulimits, override.Ulimits)

	// Service network attachments are keyed by network name (compose models them
	// as a mapping even in list form), so they merge by name rather than blindly
	// appending — appending would yield duplicate attachments to one network.
	out.Networks = mergeServiceNetworks(m.at("networks"), base.Networks, override.Networks)

	// depends_on merges by dependency service name; override's
	// condition/required/restart win on a shared name.
	out.DependsOn = mergeDependsOn(m.at("depends_on"), base.DependsOn, override.DependsOn)

	// Nested structs recurse via pointer-aware helpers.
	out.Build = mergeBuild(m.at("build"), base.Build, override.Build)
	out.Deploy = mergeDeploy(m.at("deploy"), base.Deploy, override.Deploy)
	out.Healthcheck = mergeHealthcheck(m.at("healthcheck"), base.Healthcheck, override.Healthcheck)

	// x-cornus-egress / x-cornus-ingress / x-cornus-telemetry /
	// x-cornus-credentials are cohesive blocks, not field-merged: a later file's
	// block replaces the earlier one
	// wholesale, matching the project-level "last block wins" merge
	// (LoadDocumentWithOptions) and the "a service-level block fully overrides"
	// semantics. Without this a redefining file's block would be silently dropped
	// (out := base keeps base's, and these were never re-applied).
	out.Egress = overridePtr(m, "x-cornus-egress", base.Egress, override.Egress)
	out.Ingress = overridePtr(m, "x-cornus-ingress", base.Ingress, override.Ingress)
	out.Telemetry = overridePtr(m, "x-cornus-telemetry", base.Telemetry, override.Telemetry)
	out.Credentials = overridePtr(m, "x-cornus-credentials", base.Credentials, override.Credentials)
	// provider is likewise a cohesive block: a later file's provider replaces the
	// earlier one wholesale rather than field-merging type/options.
	out.Provider = overridePtr(m, "provider", base.Provider, override.Provider)

	return out
}

// mergeStringMap returns a new map holding base's entries overlaid with
// override's (override wins). It returns nil only when both inputs are empty, so
// an all-zero merge stays zero.
func mergeStringMap(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// appendDedup concatenates base then override, dropping entries in override that
// exactly equal one already present. Order is preserved (base first). It returns
// nil when the result is empty so a zero merge stays zero.
func appendDedup[T comparable](base, override []T) []T {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make([]T, 0, len(base)+len(override))
	seen := make(map[T]struct{}, len(base)+len(override))
	for _, v := range base {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range override {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// mergeServiceNetworks merges two service network attachment lists by network
// name. Base order is preserved; override-only networks are appended. For a
// network present in both, aliases concatenate (deduped) and the override's
// ipv4_address / ipv6_address / mac_address / priority win when it sets each.
// ServiceNetwork holds an []string field so it is not comparable — hence the
// by-name merge rather than appendDedup. m is the metadata for the `networks`
// key itself: `!reset` empties it, `!override` drops the base, and in mapping
// form each network's own sub-keys drive the per-attachment scalar merge.
func mergeServiceNetworks(m *yamlMeta, base, override ServiceNetworks) ServiceNetworks {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		base = nil
	}
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(ServiceNetworks, len(base))
	copy(out, base)
	idx := make(map[string]int, len(out))
	for i, sn := range out {
		idx[sn.Name] = i
	}
	for _, sn := range override {
		if i, ok := idx[sn.Name]; ok {
			nm := m.at(sn.Name)
			out[i].Aliases = mergeSeq(nm, "aliases", out[i].Aliases, sn.Aliases)
			out[i].IPv4Address = overrideScalar(nm, "ipv4_address", out[i].IPv4Address, sn.IPv4Address)
			out[i].IPv6Address = overrideScalar(nm, "ipv6_address", out[i].IPv6Address, sn.IPv6Address)
			out[i].MacAddress = overrideScalar(nm, "mac_address", out[i].MacAddress, sn.MacAddress)
			out[i].Priority = overrideScalar(nm, "priority", out[i].Priority, sn.Priority)
			continue
		}
		idx[sn.Name] = len(out)
		out = append(out, sn)
	}
	return out
}

// mergeUlimits merges two ulimits lists by limit name. Base order is preserved;
// override-only limits are appended. For a limit named in both, the override's
// bounds replace the base's wholesale. Ulimit is comparable, but the by-name
// merge (rather than appendDedup) matches compose's mapping semantics — a later
// file redefining `nofile` overrides it rather than adding a second entry.
func mergeUlimits(m *yamlMeta, base, override Ulimits) Ulimits {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		base = nil
	}
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(Ulimits, len(base))
	copy(out, base)
	idx := make(map[string]int, len(out))
	for i, u := range out {
		idx[u.Name] = i
	}
	for _, u := range override {
		if i, ok := idx[u.Name]; ok {
			out[i] = u
			continue
		}
		idx[u.Name] = len(out)
		out = append(out, u)
	}
	return out
}

// mergeDependsOn merges two depends_on lists by dependency service name. Base
// order is preserved; override-only dependencies are appended. For a dependency
// named in both, the override's condition/required/restart replace the base's.
func mergeDependsOn(m *yamlMeta, base, override DependsOn) DependsOn {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		base = nil
	}
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(DependsOn, len(base))
	copy(out, base)
	idx := make(map[string]int, len(out))
	for i, d := range out {
		idx[d.Service] = i
	}
	for _, d := range override {
		if i, ok := idx[d.Service]; ok {
			out[i] = d // override's long-form metadata wins wholesale for this dep
			continue
		}
		idx[d.Service] = len(out)
		out = append(out, d)
	}
	return out
}

// mergeBuild recurses into a service's build config. A nil side yields the other;
// with both present, fields merge per their compose-spec category. `build:
// !reset` drops it and `build: !override` swaps the whole block in.
func mergeBuild(m *yamlMeta, base, override *Build) *Build {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Context = overrideScalar(m, "context", base.Context, override.Context)
	out.Dockerfile = overrideScalar(m, "dockerfile", base.Dockerfile, override.Dockerfile)
	out.Target = overrideScalar(m, "target", base.Target, override.Target)
	out.Network = overrideScalar(m, "network", base.Network, override.Network)
	out.ShmSize = overrideScalar(m, "shm_size", base.ShmSize, override.ShmSize)
	out.DockerfileInline = overrideScalar(m, "dockerfile_inline", base.DockerfileInline, override.DockerfileInline)
	out.NoCache = overrideScalar(m, "no_cache", base.NoCache, override.NoCache)
	out.Pull = overrideScalar(m, "pull", base.Pull, override.Pull)
	out.Args = mergeMapping(m, "args", base.Args, override.Args)
	out.AdditionalContexts = mergeMapping(m, "additional_contexts", base.AdditionalContexts, override.AdditionalContexts)
	// labels merge key-by-key (override wins on a clash).
	out.Labels = mergeMapping(m, "labels", base.Labels, override.Labels)
	// Additive sequences concatenate (base first), dropping exact dupes.
	out.CacheFrom = mergeSeq(m, "cache_from", base.CacheFrom, override.CacheFrom)
	out.Secrets = mergeSeq(m, "secrets", base.Secrets, override.Secrets)
	out.SSH = mergeSeq(m, "ssh", base.SSH, override.SSH)
	out.Platforms = mergeSeq(m, "platforms", base.Platforms, override.Platforms)
	out.Tags = mergeSeq(m, "tags", base.Tags, override.Tags)
	out.CacheTo = mergeSeq(m, "cache_to", base.CacheTo, override.CacheTo)
	out.ExtraHosts = ExtraHosts(mergeSeq(m, "extra_hosts", []string(base.ExtraHosts), []string(override.ExtraHosts)))
	return &out
}

// mergeDeploy recurses into deploy: the replicas scalar overrides when written
// (else when non-zero), resources/restart_policy/update_config merge field-wise,
// and labels merge key-by-key.
func mergeDeploy(m *yamlMeta, base, override *Deploy) *Deploy {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Replicas = overrideScalar(m, "replicas", base.Replicas, override.Replicas)
	out.Resources = mergeDeployResources(m.at("resources"), base.Resources, override.Resources)
	out.RestartPolicy = mergeDeployRestartPolicy(m.at("restart_policy"), base.RestartPolicy, override.RestartPolicy)
	out.UpdateConfig = mergeUpdateConfig(m.at("update_config"), base.UpdateConfig, override.UpdateConfig)
	out.Labels = Labels(mergeMapping(m, "labels", map[string]string(base.Labels), map[string]string(override.Labels)))
	return &out
}

// mergeDeployResources merges deploy.resources: its limits and reservations
// blocks each merge field-wise.
func mergeDeployResources(m *yamlMeta, base, override *DeployResources) *DeployResources {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Limits = mergeResourceLimits(m.at("limits"), base.Limits, override.Limits)
	out.Reservations = mergeResourceLimits(m.at("reservations"), base.Reservations, override.Reservations)
	return &out
}

// mergeDeployRestartPolicy merges deploy.restart_policy; each scalar overrides
// when the override writes it (else when non-zero).
func mergeDeployRestartPolicy(m *yamlMeta, base, override *DeployRestartPolicy) *DeployRestartPolicy {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Condition = overrideScalar(m, "condition", base.Condition, override.Condition)
	out.Delay = overrideScalar(m, "delay", base.Delay, override.Delay)
	out.MaxAttempts = overrideScalar(m, "max_attempts", base.MaxAttempts, override.MaxAttempts)
	out.Window = overrideScalar(m, "window", base.Window, override.Window)
	return &out
}

// mergeUpdateConfig merges deploy.update_config; each scalar overrides when the
// override writes it (else when non-zero).
func mergeUpdateConfig(m *yamlMeta, base, override *UpdateConfig) *UpdateConfig {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Parallelism = overrideScalar(m, "parallelism", base.Parallelism, override.Parallelism)
	out.Order = overrideScalar(m, "order", base.Order, override.Order)
	out.Delay = overrideScalar(m, "delay", base.Delay, override.Delay)
	out.Monitor = overrideScalar(m, "monitor", base.Monitor, override.Monitor)
	out.MaxFailureRatio = overrideScalar(m, "max_failure_ratio", base.MaxFailureRatio, override.MaxFailureRatio)
	return &out
}

// mergeResourceLimits merges deploy.resources.limits; each scalar overrides when
// the override writes it (else when non-empty).
func mergeResourceLimits(m *yamlMeta, base, override *ResourceLimits) *ResourceLimits {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Cpus = overrideScalar(m, "cpus", base.Cpus, override.Cpus)
	out.Memory = overrideScalar(m, "memory", base.Memory, override.Memory)
	return &out
}

// mergeHealthcheck recurses into healthcheck: the test command is a single
// logical value (the override replaces it), and the remaining scalars override
// when written (else when non-zero).
func mergeHealthcheck(m *yamlMeta, base, override *Healthcheck) *Healthcheck {
	if m.cleared() {
		return nil
	}
	if m.replaced() {
		return override
	}
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Test = overrideWhole(m, "test", base.Test, override.Test)
	out.Interval = overrideScalar(m, "interval", base.Interval, override.Interval)
	out.Timeout = overrideScalar(m, "timeout", base.Timeout, override.Timeout)
	out.Retries = overrideScalar(m, "retries", base.Retries, override.Retries)
	out.StartPeriod = overrideScalar(m, "start_period", base.StartPeriod, override.StartPeriod)
	out.StartInterval = overrideScalar(m, "start_interval", base.StartInterval, override.StartInterval)
	out.Disable = overrideScalar(m, "disable", base.Disable, override.Disable)
	return &out
}

// mergeSecretDef merges two top-level secret definitions (the override's written
// — else non-zero — fields win).
func mergeSecretDef(m *yamlMeta, base, override SecretDefDocument) SecretDefDocument {
	if m.replaced() {
		return override
	}
	out := base
	out.File = overrideScalar(m, "file", base.File, override.File)
	out.Environment = overrideScalar(m, "environment", base.Environment, override.Environment)
	out.External = overrideScalar(m, "external", base.External, override.External)
	out.Name = overrideScalar(m, "name", base.Name, override.Name)
	return out
}

// mergeConfigDef merges two top-level config definitions (the override's written
// — else non-zero — fields win), mirroring mergeSecretDef.
func mergeConfigDef(m *yamlMeta, base, override ConfigDefDocument) ConfigDefDocument {
	if m.replaced() {
		return override
	}
	out := base
	out.File = overrideScalar(m, "file", base.File, override.File)
	out.Content = overrideScalar(m, "content", base.Content, override.Content)
	out.Environment = overrideScalar(m, "environment", base.Environment, override.Environment)
	out.External = overrideScalar(m, "external", base.External, override.External)
	out.Name = overrideScalar(m, "name", base.Name, override.Name)
	return out
}

// mergeVolumeDef merges two top-level volume definitions: scalars override when
// written (else when non-zero), and driver_opts/labels merge key-by-key.
func mergeVolumeDef(m *yamlMeta, base, override VolumeDefDocument) VolumeDefDocument {
	if m.replaced() {
		return override
	}
	out := base
	out.External = overrideScalar(m, "external", base.External, override.External)
	out.Name = overrideScalar(m, "name", base.Name, override.Name)
	out.Driver = overrideScalar(m, "driver", base.Driver, override.Driver)
	out.DriverOpts = mergeMapping(m, "driver_opts", base.DriverOpts, override.DriverOpts)
	out.Labels = mergeMapping(m, "labels", base.Labels, override.Labels)
	return out
}

// mergeNetworkDef merges two top-level network definitions: scalars override when
// written (else when non-zero), driver_opts/labels merge key-by-key, and the ipam
// block override-replaces when the override sets one.
func mergeNetworkDef(m *yamlMeta, base, override NetworkDefDocument) NetworkDefDocument {
	if m.replaced() {
		return override
	}
	out := base
	out.External = overrideScalar(m, "external", base.External, override.External)
	out.Name = overrideScalar(m, "name", base.Name, override.Name)
	out.Driver = overrideScalar(m, "driver", base.Driver, override.Driver)
	out.DriverOpts = mergeMapping(m, "driver_opts", base.DriverOpts, override.DriverOpts)
	out.Labels = mergeMapping(m, "labels", base.Labels, override.Labels)
	out.Attachable = overrideScalar(m, "attachable", base.Attachable, override.Attachable)
	out.Internal = overrideScalar(m, "internal", base.Internal, override.Internal)
	out.EnableIPv6 = overrideScalar(m, "enable_ipv6", base.EnableIPv6, override.EnableIPv6)
	// ipam is a single logical block: an override that sets one replaces the base
	// wholesale (compose-spec — the override's addressing is authoritative).
	out.IPAM = overridePtr(m, "ipam", base.IPAM, override.IPAM)
	return out
}
