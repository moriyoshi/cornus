//go:build linux

package barehost

import (
	"context"
	"fmt"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/otelcollector"
)

// withTelemetry grafts the embedded OpenTelemetry Collector onto an Apply when the
// spec opts into telemetry: it injects the OTEL_* wiring into the app env and
// composes a per-replica telemetry companion onto hooks.afterStart, so it works on
// a plain Apply AND alongside an egress/mount companion (each joins the SAME app
// netns; the collector is just another loopback listener there). The returned spec
// carries a fresh Env map, so the caller's map is never mutated.
func (b *Backend) withTelemetry(ctx context.Context, spec api.DeploySpec, hooks applyHooks) (api.DeploySpec, applyHooks, error) {
	if !spec.Telemetry.Active() {
		return spec, hooks, nil
	}
	w, err := deploy.BuildTelemetryWiring(spec, spec.Name)
	if err != nil {
		return spec, hooks, fmt.Errorf("bare: %w", err)
	}
	if b.agentImage == "" {
		return spec, hooks, fmt.Errorf("bare: telemetry needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	agent, err := b.img.pull(ctx, b.agentImage)
	if err != nil {
		return spec, hooks, fmt.Errorf("bare: pull telemetry agent image: %w", err)
	}

	env := make(map[string]string, len(spec.Env)+len(w.Env))
	for k, v := range spec.Env {
		env[k] = v
	}
	for k, v := range w.Env { // BuildTelemetryWiring already dropped user-set OTEL_* keys
		env[k] = v
	}
	spec.Env = env

	role := w.Role
	prev := hooks.afterStart
	hooks.afterStart = func(ctx context.Context, replica int, netnsPath string) error {
		if prev != nil {
			if err := prev(ctx, replica, netnsPath); err != nil {
				return err
			}
		}
		return b.startTelemetryCompanion(ctx, spec.Name, replica, netnsPath, agent, b.agentImage, role)
	}
	return spec, hooks, nil
}

// startTelemetryCompanion starts one replica's telemetry companion caretaker in the
// app instance's pinned netns. The companion record's Role marks it a companion, so
// Delete reaps it (before the app instance whose netns it joins) with no extra code.
func (b *Backend) startTelemetryCompanion(ctx context.Context, name string, replica int, netnsPath string, agent pulledImage, agentRef string, role otelcollector.Config) error {
	cs := companionSpec{
		appName:   name,
		compID:    fmt.Sprintf("cornus-%s-otel-%d", name, replica),
		replica:   replica,
		role:      roleTelemetryCaretaker,
		netnsPath: netnsPath,
		agent:     agent,
		agentRef:  agentRef,
		cfg:       caretakerConfig{Otel: &role},
	}
	if err := b.startCompanion(ctx, cs); err != nil {
		return fmt.Errorf("bare: start telemetry caretaker: %w", err)
	}
	return nil
}
