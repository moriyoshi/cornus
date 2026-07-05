package dockerproxy

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"cornus/pkg/api"
)

// toDeploySpec translates a Docker create request into a cornus DeploySpec for
// a single-replica deployment named name. Bind mounts (Binds and Mounts of
// Type=="bind") become spec.Mounts; the deploy-attach client classifies which
// sources are caller-local and serves those over 9P. Named volumes are skipped
// (deferred).
func toDeploySpec(name string, req createRequest) api.DeploySpec {
	spec := api.DeploySpec{Name: name, Image: req.Image, Replicas: 1, Command: req.Cmd, Entrypoint: req.Entrypoint}

	if len(req.Env) > 0 {
		spec.Env = make(map[string]string, len(req.Env))
		for _, e := range req.Env {
			k, v, _ := strings.Cut(e, "=")
			if k != "" {
				spec.Env[k] = v
			}
		}
	}

	if rp := req.HostConfig.RestartPolicy.Name; rp != "" && rp != "no" {
		spec.Restart = rp
	}

	for portProto, binds := range req.HostConfig.PortBindings {
		container, proto := splitPortProto(portProto)
		if container == 0 {
			continue
		}
		for _, b := range binds {
			host, err := strconv.Atoi(b.HostPort)
			if err != nil {
				continue
			}
			spec.Ports = append(spec.Ports, api.PortMapping{Host: host, Container: container, Protocol: proto})
		}
	}

	for _, bind := range req.HostConfig.Binds {
		if v, ok := parseNamedVolume(bind); ok {
			spec.Volumes = append(spec.Volumes, v)
		} else if m, ok := parseBind(bind); ok {
			spec.Mounts = append(spec.Mounts, m)
		}
	}
	for _, mp := range req.HostConfig.Mounts {
		switch {
		case mp.Type == "bind" && mp.Source != "" && mp.Target != "":
			spec.Mounts = append(spec.Mounts, api.Mount{Source: mp.Source, Target: mp.Target, ReadOnly: mp.ReadOnly})
		case mp.Type == "volume" && mp.Source != "" && mp.Target != "":
			// A named volume. The compose plugin already project-scopes Source
			// (e.g. "proj_cache"); pass it through as the shared volume name.
			spec.Volumes = append(spec.Volumes, api.VolumeSpec{Name: mp.Source, Target: mp.Target, ReadOnly: mp.ReadOnly})
		}
	}

	// User-defined networks: each EndpointsConfig entry (already project-scoped
	// by the compose plugin, e.g. "proj_default") becomes a NetworkAttachment
	// carrying its DNS aliases. Docker's predefined networks are skipped — a
	// plain `docker run` sends EndpointsConfig["default"], which is the builtin
	// bridge, not a user network (ensuring it 403s: "operation is not permitted
	// on predefined default network"). The compose service label is ensured as
	// the first alias so members resolve each other by bare service name even
	// when the client sent none. Keys are sorted for a deterministic spec.
	if eps := req.NetworkingConfig.EndpointsConfig; len(eps) > 0 {
		nets := make([]string, 0, len(eps))
		for net := range eps {
			if !predefinedNetworks[net] {
				nets = append(nets, net)
			}
		}
		sort.Strings(nets)
		svc := req.Labels["com.docker.compose.service"]
		for _, net := range nets {
			aliases := eps[net].Aliases
			if svc != "" && !slices.Contains(aliases, svc) {
				aliases = append([]string{svc}, aliases...)
			}
			spec.Networks = append(spec.Networks, api.NetworkAttachment{Name: net, Aliases: aliases})
		}
	}
	return spec
}

// predefinedNetworks are Docker's builtin networks plus the CLI's "default"
// placeholder: connectivity defaults, never user-defined compose networks.
var predefinedNetworks = map[string]bool{"default": true, "bridge": true, "host": true, "none": true}

// splitPortProto parses a Docker port key like "80/tcp" into (80, "tcp").
func splitPortProto(s string) (int, string) {
	proto := "tcp"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto = s[i+1:]
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, proto
	}
	return n, proto
}

// parseBind parses a Docker -v bind spec "src:dst[:opts]" into an api.Mount. It
// returns ok=false for a bare named volume (source is not a host path) — those
// are handled by parseNamedVolume — and for anything without a host source.
func parseBind(bind string) (api.Mount, bool) {
	src, dst, ro, ok := splitVolumeSpec(bind)
	if !ok || src == "" || !isHostPath(src) {
		return api.Mount{}, false
	}
	return api.Mount{Source: src, Target: dst, ReadOnly: ro}, true
}

// parseNamedVolume parses a Docker -v spec whose source is a bare volume name
// ("cache:/var/cache[:ro]") into a named api.VolumeSpec. A source that looks like
// a host path (absolute, ./, ~/) is a bind, not a named volume, so it returns
// ok=false and parseBind handles it.
func parseNamedVolume(bind string) (api.VolumeSpec, bool) {
	src, dst, ro, ok := splitVolumeSpec(bind)
	if !ok || src == "" || isHostPath(src) {
		return api.VolumeSpec{}, false
	}
	return api.VolumeSpec{Name: src, Target: dst, ReadOnly: ro}, true
}

// splitVolumeSpec splits "src:dst[:opts]" into its parts, reporting ok=false
// unless both a source and a destination are present.
func splitVolumeSpec(spec string) (src, dst string, readOnly, ok bool) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false, false
	}
	src, dst = parts[0], parts[1]
	if len(parts) >= 3 {
		for _, opt := range strings.Split(parts[2], ",") {
			if opt == "ro" {
				readOnly = true
			}
		}
	}
	return src, dst, readOnly, true
}

// isHostPath reports whether a -v source is a host path (bind mount) rather than
// a named volume, matching Docker's rule: absolute, ./-relative, or ~-relative.
func isHostPath(src string) bool {
	return strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~")
}
