package compose

// This file implements compose-spec field-level deep merge for multi-file
// Compose loading (`-f base.yaml -f override.yaml`). The merge rules follow the
// compose-spec "Merge and override" section
// (https://github.com/compose-spec/compose-spec/blob/master/13-merge.md) and
// compose-go's behaviour, applied to the subset of fields cornus models on the
// typed structs in types.go.
//
// IMPORTANT limitation — zero means "unset". cornus decodes each file into typed
// Go structs, where an absent key and an explicit zero value are
// indistinguishable (e.g. `image: ""` and no `image:` both yield ""). Compose
// only overrides a scalar when the key is *present*; since we cannot observe
// presence, every scalar rule below treats a zero/empty override value as "not
// set" and keeps the base value. In practice a user does not write an explicit
// empty scalar in an override to clear a base value — that is what the
// `!reset` / `!override` tags exist for (see below).
//
// !reset / !override are NOT supported. compose-spec lets an override explicitly
// clear or wholesale-replace a base value with the custom YAML tags `!reset`
// (null out) and `!override` (replace instead of merge). The loader decodes YAML
// through sigs.k8s.io/yaml, which round-trips YAML -> JSON and discards all
// custom tags before cornus ever sees the document, so the tags cannot reach
// this merge layer. Supporting them would require a yaml.v3 Node-level pass in
// loadFile (interpolate + tag detection) ahead of the JSON decode; that is a
// larger change and is deferred. Until then, an override can only add to or
// replace-when-non-empty a base value, never explicitly clear it.

// mergeService deep-merges override onto base (override wins) following the
// compose-spec rules described above, and returns the combined Service. It is
// used by LoadWithOptions when a later file redefines a service already present
// from an earlier file.
func mergeService(base, override ServiceDocument) ServiceDocument {
	out := base

	// Scalars: override replaces only when it carries a non-zero value.
	if override.Image != "" {
		out.Image = override.Image
	}
	if override.Restart != "" {
		out.Restart = override.Restart
	}
	if override.ContainerName != "" {
		out.ContainerName = override.ContainerName
	}
	if override.Privileged {
		out.Privileged = true
	}
	if override.User != "" {
		out.User = override.User
	}
	if override.WorkingDir != "" {
		out.WorkingDir = override.WorkingDir
	}
	if override.Hostname != "" {
		out.Hostname = override.Hostname
	}
	if override.StopSignal != "" {
		out.StopSignal = override.StopSignal
	}
	if override.StopGracePeriod != "" {
		out.StopGracePeriod = override.StopGracePeriod
	}
	if override.Init != nil {
		out.Init = override.Init
	}
	if override.TTY {
		out.TTY = true
	}
	if override.StdinOpen {
		out.StdinOpen = true
	}
	if override.ReadOnly {
		out.ReadOnly = true
	}
	if override.ShmSize != "" {
		out.ShmSize = override.ShmSize
	}
	if override.PID != "" {
		out.PID = override.PID
	}
	if override.IPC != "" {
		out.IPC = override.IPC
	}
	if override.MemLimit != "" {
		out.MemLimit = override.MemLimit
	}
	if override.CPUs != "" {
		out.CPUs = override.CPUs
	}

	// labels merge key-by-key, override winning on conflicting keys.
	if m := mergeStringMap(map[string]string(base.Labels), map[string]string(override.Labels)); m != nil {
		out.Labels = Labels(m)
	}

	// command / entrypoint are a single logical value: an override list resets
	// and replaces the base rather than concatenating (compose-spec).
	if len(override.Command) > 0 {
		out.Command = override.Command
	}
	if len(override.Entrypoint) > 0 {
		out.Entrypoint = override.Entrypoint
	}

	// Mappings merge key-by-key, override winning on conflicting keys.
	out.Environment = mergeEnvironment(base.Environment, override.Environment)

	// Additive sequences concatenate (base then override), dropping exact dupes.
	out.EnvFile = appendDedup(base.EnvFile, override.EnvFile)
	out.Ports = appendDedup(base.Ports, override.Ports)
	out.Expose = appendDedup(base.Expose, override.Expose)
	out.Volumes = appendDedup(base.Volumes, override.Volumes)
	out.Profiles = appendDedup(base.Profiles, override.Profiles)

	// Security & networking list keys are additive too (append-dedup); sysctls
	// is a mapping and merges key-by-key with the override winning on a clash.
	out.CapAdd = appendDedup(base.CapAdd, override.CapAdd)
	out.CapDrop = appendDedup(base.CapDrop, override.CapDrop)
	out.SecurityOpt = appendDedup(base.SecurityOpt, override.SecurityOpt)
	out.GroupAdd = appendDedup(base.GroupAdd, override.GroupAdd)
	out.ExtraHosts = ExtraHosts(appendDedup([]string(base.ExtraHosts), []string(override.ExtraHosts)))
	out.DNS = StringList(appendDedup([]string(base.DNS), []string(override.DNS)))
	out.DNSSearch = StringList(appendDedup([]string(base.DNSSearch), []string(override.DNSSearch)))
	out.DNSOpt = StringList(appendDedup([]string(base.DNSOpt), []string(override.DNSOpt)))
	if m := mergeStringMap(map[string]string(base.Sysctls), map[string]string(override.Sysctls)); m != nil {
		out.Sysctls = Sysctls(m)
	}

	// tmpfs / devices are additive sequences (append-dedup, base first).
	out.Tmpfs = StringList(appendDedup([]string(base.Tmpfs), []string(override.Tmpfs)))
	out.Devices = StringList(appendDedup([]string(base.Devices), []string(override.Devices)))
	// ulimits is a mapping keyed by limit name; override wins on a shared name.
	out.Ulimits = mergeUlimits(base.Ulimits, override.Ulimits)

	// Service network attachments are keyed by network name (compose models them
	// as a mapping even in list form), so they merge by name rather than blindly
	// appending — appending would yield duplicate attachments to one network.
	out.Networks = mergeServiceNetworks(base.Networks, override.Networks)

	// depends_on merges by dependency service name; override's
	// condition/required/restart win on a shared name.
	out.DependsOn = mergeDependsOn(base.DependsOn, override.DependsOn)

	// Nested structs recurse via pointer-aware helpers.
	out.Build = mergeBuild(base.Build, override.Build)
	out.Deploy = mergeDeploy(base.Deploy, override.Deploy)
	out.Healthcheck = mergeHealthcheck(base.Healthcheck, override.Healthcheck)

	// x-cornus-egress / x-cornus-ingress are cohesive blocks, not field-merged: a
	// later file's block replaces the earlier one wholesale, matching the
	// project-level "last block wins" merge (LoadDocumentWithOptions) and the
	// "a service-level block fully overrides" semantics. Without this a
	// redefining file's egress/ingress would be silently dropped (out := base
	// keeps base's, and these were never re-applied).
	if override.Egress != nil {
		out.Egress = override.Egress
	}
	if override.Ingress != nil {
		out.Ingress = override.Ingress
	}
	// provider is likewise a cohesive block: a later file's provider replaces the
	// earlier one wholesale rather than field-merging type/options.
	if override.Provider != nil {
		out.Provider = override.Provider
	}

	return out
}

// mergeEnvironment merges two environment maps key-by-key, override winning on
// conflicts. Base keys absent from override are kept.
func mergeEnvironment(base, override Environment) Environment {
	m := mergeStringMap(map[string]string(base), map[string]string(override))
	if m == nil {
		return nil
	}
	return Environment(m)
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
// by-name merge rather than appendDedup.
func mergeServiceNetworks(base, override ServiceNetworks) ServiceNetworks {
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
			out[i].Aliases = appendDedup(out[i].Aliases, sn.Aliases)
			if sn.IPv4Address != "" {
				out[i].IPv4Address = sn.IPv4Address
			}
			if sn.IPv6Address != "" {
				out[i].IPv6Address = sn.IPv6Address
			}
			if sn.MacAddress != "" {
				out[i].MacAddress = sn.MacAddress
			}
			if sn.Priority != 0 {
				out[i].Priority = sn.Priority
			}
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
func mergeUlimits(base, override Ulimits) Ulimits {
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
func mergeDependsOn(base, override DependsOn) DependsOn {
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
// with both present, fields merge per their compose-spec category.
func mergeBuild(base, override *Build) *Build {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if override.Context != "" {
		out.Context = override.Context
	}
	if override.Dockerfile != "" {
		out.Dockerfile = override.Dockerfile
	}
	if override.Target != "" {
		out.Target = override.Target
	}
	// Scalars: override wins when set.
	if override.Network != "" {
		out.Network = override.Network
	}
	if override.ShmSize != "" {
		out.ShmSize = override.ShmSize
	}
	if override.DockerfileInline != "" {
		out.DockerfileInline = override.DockerfileInline
	}
	if override.NoCache {
		out.NoCache = true
	}
	if override.Pull {
		out.Pull = true
	}
	out.Args = mergeStringMap(base.Args, override.Args)
	out.AdditionalContexts = mergeStringMap(base.AdditionalContexts, override.AdditionalContexts)
	// labels merge key-by-key (override wins on a clash).
	if m := mergeStringMap(base.Labels, override.Labels); m != nil {
		out.Labels = m
	}
	// Additive sequences concatenate (base first), dropping exact dupes.
	out.CacheFrom = appendDedup(base.CacheFrom, override.CacheFrom)
	out.Secrets = appendDedup(base.Secrets, override.Secrets)
	out.SSH = appendDedup(base.SSH, override.SSH)
	out.Platforms = appendDedup(base.Platforms, override.Platforms)
	out.Tags = appendDedup(base.Tags, override.Tags)
	out.CacheTo = appendDedup(base.CacheTo, override.CacheTo)
	out.ExtraHosts = ExtraHosts(appendDedup([]string(base.ExtraHosts), []string(override.ExtraHosts)))
	return &out
}

// mergeDeploy recurses into deploy: the replicas scalar overrides when non-zero,
// resources/restart_policy/update_config merge field-wise, and labels merge
// key-by-key.
func mergeDeploy(base, override *Deploy) *Deploy {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if override.Replicas != 0 {
		out.Replicas = override.Replicas
	}
	out.Resources = mergeDeployResources(base.Resources, override.Resources)
	out.RestartPolicy = mergeDeployRestartPolicy(base.RestartPolicy, override.RestartPolicy)
	out.UpdateConfig = mergeUpdateConfig(base.UpdateConfig, override.UpdateConfig)
	if m := mergeStringMap(map[string]string(base.Labels), map[string]string(override.Labels)); m != nil {
		out.Labels = Labels(m)
	}
	return &out
}

// mergeDeployResources merges deploy.resources: its limits and reservations
// blocks each merge field-wise.
func mergeDeployResources(base, override *DeployResources) *DeployResources {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	out.Limits = mergeResourceLimits(base.Limits, override.Limits)
	out.Reservations = mergeResourceLimits(base.Reservations, override.Reservations)
	return &out
}

// mergeDeployRestartPolicy merges deploy.restart_policy; each scalar overrides
// when the override sets it non-zero.
func mergeDeployRestartPolicy(base, override *DeployRestartPolicy) *DeployRestartPolicy {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if override.Condition != "" {
		out.Condition = override.Condition
	}
	if override.Delay != "" {
		out.Delay = override.Delay
	}
	if override.MaxAttempts != 0 {
		out.MaxAttempts = override.MaxAttempts
	}
	if override.Window != "" {
		out.Window = override.Window
	}
	return &out
}

// mergeUpdateConfig merges deploy.update_config; each scalar overrides when the
// override sets it non-zero.
func mergeUpdateConfig(base, override *UpdateConfig) *UpdateConfig {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if override.Parallelism != 0 {
		out.Parallelism = override.Parallelism
	}
	if override.Order != "" {
		out.Order = override.Order
	}
	if override.Delay != "" {
		out.Delay = override.Delay
	}
	if override.Monitor != "" {
		out.Monitor = override.Monitor
	}
	if override.MaxFailureRatio != "" {
		out.MaxFailureRatio = override.MaxFailureRatio
	}
	return &out
}

// mergeResourceLimits merges deploy.resources.limits; each scalar overrides when
// the override sets it non-empty.
func mergeResourceLimits(base, override *ResourceLimits) *ResourceLimits {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if override.Cpus != "" {
		out.Cpus = override.Cpus
	}
	if override.Memory != "" {
		out.Memory = override.Memory
	}
	return &out
}

// mergeHealthcheck recurses into healthcheck: the test command is a single
// logical value (override replaces when non-empty), and the remaining scalars
// override when non-zero.
func mergeHealthcheck(base, override *Healthcheck) *Healthcheck {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if len(override.Test) > 0 {
		out.Test = override.Test
	}
	if override.Interval != "" {
		out.Interval = override.Interval
	}
	if override.Timeout != "" {
		out.Timeout = override.Timeout
	}
	if override.Retries != 0 {
		out.Retries = override.Retries
	}
	if override.StartPeriod != "" {
		out.StartPeriod = override.StartPeriod
	}
	if override.StartInterval != "" {
		out.StartInterval = override.StartInterval
	}
	if override.Disable {
		out.Disable = true
	}
	return &out
}

// mergeSecretDef merges two top-level secret definitions (override's non-zero
// fields win).
func mergeSecretDef(base, override SecretDefDocument) SecretDefDocument {
	out := base
	if override.File != "" {
		out.File = override.File
	}
	if override.Environment != "" {
		out.Environment = override.Environment
	}
	if override.External {
		out.External = true
	}
	if override.Name != "" {
		out.Name = override.Name
	}
	return out
}

// mergeConfigDef merges two top-level config definitions (override's non-zero
// fields win), mirroring mergeSecretDef.
func mergeConfigDef(base, override ConfigDefDocument) ConfigDefDocument {
	out := base
	if override.File != "" {
		out.File = override.File
	}
	if override.Content != "" {
		out.Content = override.Content
	}
	if override.Environment != "" {
		out.Environment = override.Environment
	}
	if override.External {
		out.External = true
	}
	if override.Name != "" {
		out.Name = override.Name
	}
	return out
}

// mergeVolumeDef merges two top-level volume definitions: scalars override when
// non-zero, and driver_opts/labels merge key-by-key.
func mergeVolumeDef(base, override VolumeDefDocument) VolumeDefDocument {
	out := base
	if override.External {
		out.External = true
	}
	if override.Name != "" {
		out.Name = override.Name
	}
	if override.Driver != "" {
		out.Driver = override.Driver
	}
	out.DriverOpts = mergeStringMap(base.DriverOpts, override.DriverOpts)
	out.Labels = mergeStringMap(base.Labels, override.Labels)
	return out
}

// mergeNetworkDef merges two top-level network definitions: scalars override when
// non-zero, bool toggles override when the override sets them, driver_opts/labels
// merge key-by-key, and the ipam block override-replaces when the override sets one.
func mergeNetworkDef(base, override NetworkDefDocument) NetworkDefDocument {
	out := base
	if override.External {
		out.External = true
	}
	if override.Name != "" {
		out.Name = override.Name
	}
	if override.Driver != "" {
		out.Driver = override.Driver
	}
	out.DriverOpts = mergeStringMap(base.DriverOpts, override.DriverOpts)
	out.Labels = mergeStringMap(base.Labels, override.Labels)
	if override.Attachable {
		out.Attachable = true
	}
	if override.Internal {
		out.Internal = true
	}
	if override.EnableIPv6 {
		out.EnableIPv6 = true
	}
	// ipam is a single logical block: an override that sets one replaces the base
	// wholesale (compose-spec — the override's addressing is authoritative).
	if override.IPAM != nil {
		out.IPAM = override.IPAM
	}
	return out
}
