package dockerhost

import (
	"context"
	"encoding/json"
	"fmt"

	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/otelcollector"
)

// roleTelemetryCaretaker marks an embedded-OpenTelemetry-Collector companion
// container (labelRole). Like the egress companion it shares the app container's
// network namespace, but it does NOT relay through the cornus server — the
// collector receives the app's OTLP on shared-netns loopback and exports outward.
// isCompanion (egress.go) recognizes it via the non-empty labelRole, so Status/
// List/removeInstances handle it with no extra code.
const roleTelemetryCaretaker = "otel-caretaker"

// startTelemetryCompanion starts one replica's embedded-Collector companion in the
// app container's network namespace (NetworkMode: container:<appID>). The app is
// pointed at 127.0.0.1:<otlp-port> in that shared netns by the OTEL_* env
// BuildTelemetryWiring injected into the app container.
func (b *Backend) startTelemetryCompanion(ctx context.Context, name, appID string, replica int, role otelcollector.Config) error {
	o := role
	cfg := caretaker.Config{Otel: &o}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("dockerhost: marshal telemetry caretaker config: %w", err)
	}
	body := createBody{
		Image: b.agentImage,
		Cmd:   []string{"caretaker"}, // the cornus image entrypoint is `cornus`
		Env:   []string{"CORNUS_CARETAKER_CONFIG=" + string(raw)},
		Labels: map[string]string{
			deploy.LabelManaged: "true",
			deploy.LabelApp:     name,
			labelRole:           roleTelemetryCaretaker,
		},
		HostConfig: hostConfig{
			// Loopback OTLP receiver + outward export; no privilege, no NET_ADMIN.
			NetworkMode:   "container:" + appID,
			RestartPolicy: restartPolicy{Name: "unless-stopped"},
		},
	}
	id, err := b.api.containerCreate(ctx, fmt.Sprintf("cornus-%s-otel-%d", name, replica), body)
	if err != nil {
		return fmt.Errorf("dockerhost: create telemetry caretaker: %w", err)
	}
	return b.api.containerStart(ctx, id)
}
