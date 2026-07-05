//go:build linux

package incushost

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// buildInstancesPost maps a DeploySpec to the Incus create request for replica i.
// Published host ports are realized as proxy devices and, per the cross-backend
// contract, are attached to replica 0 only (one DNAT target per host port);
// replicas 1+ get no port devices. Fields Incus cannot honor for an OCI
// application container are warned about per-field (never silently dropped).
func (b *Backend) buildInstancesPost(ctx context.Context, spec api.DeploySpec, i int) (incusapi.InstancesPost, error) {
	log := logging.FromContext(ctx, slog.Group("incus", "deployment", spec.Name))

	src, err := imageSource(spec.Image)
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
	warnUnsupported(ctx, log, spec)

	post := incusapi.InstancesPost{
		Name:   instanceName(spec.Name, i),
		Type:   incusapi.InstanceTypeContainer,
		Source: src,
		Start:  true,
	}
	post.Config = config
	post.Devices = map[string]map[string]string{}

	// Published ports on replica 0 only.
	if i == 0 {
		for pi, pm := range spec.Ports {
			dev, name := proxyDevice(pi, pm)
			if dev != nil {
				post.Devices[name] = dev
			}
		}
	}
	if len(post.Devices) == 0 {
		post.Devices = nil
	}
	return post, nil
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

// warnUnsupported emits one warning per set spec field the Incus OCI backend
// does not (yet) map, so an operator sees exactly what was ignored.
func warnUnsupported(ctx context.Context, log *slog.Logger, spec api.DeploySpec) {
	if len(spec.Entrypoint) > 0 || len(spec.Command) > 0 {
		log.WarnContext(ctx, "incus OCI backend runs the image's own entrypoint; command/entrypoint override is not applied")
	}
	if spec.User != "" {
		log.WarnContext(ctx, "backend ignores user override", "user", spec.User)
	}
	if spec.WorkingDir != "" {
		log.WarnContext(ctx, "backend ignores workingDir override", "workingDir", spec.WorkingDir)
	}
	if len(spec.Mounts) > 0 {
		log.WarnContext(ctx, "backend ignores mounts (client-local mounts land with the caretaker companion)")
	}
	if len(spec.Volumes) > 0 {
		log.WarnContext(ctx, "backend ignores managed volumes")
	}
	if spec.Healthcheck != nil {
		log.WarnContext(ctx, "backend ignores healthcheck")
	}
	if spec.Ingress != nil {
		log.WarnContext(ctx, "backend ignores ingress (kubernetes-only feature)")
	}
	if spec.Knative != nil {
		log.WarnContext(ctx, "backend ignores knative (kubernetes-only feature); running as an ordinary container")
	}
	if len(spec.Networks) > 0 {
		log.WarnContext(ctx, "backend ignores user-defined networks")
	}
}
