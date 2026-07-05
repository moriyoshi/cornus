//go:build linux

package barehost

import (
	"context"
	"fmt"
	"os"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/egresspolicy"
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
	// In "proxy" egress mode the app is steered to the loopback proxy by env alone,
	// so the collector — a separate process that never saw those vars — silently
	// exported straight out, bypassing the client-side egress policy the operator
	// configured. Hand it the same variables. "transparent" mode needs nothing here:
	// its nftables redirect is netns-scoped and already captures the collector.
	proxyEnv := egresspolicy.ProxyEnvSubset(spec.Env)
	prev := hooks.afterStart
	hooks.afterStart = func(ctx context.Context, replica int, netnsPath string) error {
		if prev != nil {
			if err := prev(ctx, replica, netnsPath); err != nil {
				return err
			}
		}
		return b.startTelemetryCompanion(ctx, spec.Name, replica, netnsPath, agent, b.agentImage, role, w.Relay, proxyEnv)
	}
	return spec, hooks, nil
}

// startTelemetryCompanion starts one replica's telemetry companion caretaker in the
// app instance's pinned netns. The companion record's Role marks it a companion, so
// Delete reaps it (before the app instance whose netns it joins) with no extra code.
func (b *Backend) startTelemetryCompanion(ctx context.Context, name string, replica int, netnsPath string, agent pulledImage, agentRef string, role otelcollector.Config, relay *deploy.TelemetryRelay, proxyEnv map[string]string) error {
	cs := companionSpec{
		appName:   name,
		compID:    fmt.Sprintf("cornus-%s-otel-%d", name, replica),
		replica:   replica,
		role:      roleTelemetryCaretaker,
		netnsPath: netnsPath,
		agent:     agent,
		agentRef:  agentRef,
		cfg:       caretakerConfig{Otel: &role},
		extraEnv:  proxyEnv,
	}
	// ViaMux: the companion opens a caretaker connection purely to carry the
	// exports. bare shares the server's mount namespace, but the app instance runs
	// in a PINNED netns, so the workload's network is not necessarily the server's
	// — which is the case this exists for.
	if relay != nil {
		serverURL := os.Getenv("CORNUS_ADVERTISE_URL")
		if serverURL == "" {
			return fmt.Errorf("bare: telemetry viaMux requires CORNUS_ADVERTISE_URL (the cornus URL the companion dials)")
		}
		cs.cfg.TelemetryRelay = &caretakerTelemetryRelayRole{Server: serverURL, Listen: relay.Listen}
		if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
			cs.cfg.Token = tok
		}
	}
	if err := b.startCompanion(ctx, cs); err != nil {
		return fmt.Errorf("bare: start telemetry caretaker: %w", err)
	}
	return nil
}
