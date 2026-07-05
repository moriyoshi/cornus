package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cornus/pkg/caretaker"
	"cornus/pkg/observability"
)

// CaretakerCmd runs the single per-pod sidecar: it reads a JSON role config
// (from --config or the CORNUS_CARETAKER_CONFIG env var the k8s backend sets)
// and runs every role — today 9P mounts — until the pod is torn down. This is
// the multi-role successor to `mount-agent`; a pod carries exactly one caretaker
// container regardless of how many mounts (and, later, network roles) it needs.
type CaretakerCmd struct {
	Config string `kong:"env='CORNUS_CARETAKER_CONFIG',help='JSON caretaker role config.'"`
}

// Run parses the config and runs the caretaker until SIGINT/SIGTERM.
func (c *CaretakerCmd) Run(cli *CLI) error {
	cfg, err := parseCaretakerConfig(c.Config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdown, err := setupCaretakerObservability(ctx)
	if err != nil {
		return err
	}
	defer shutdown()
	return caretaker.Run(ctx, cfg)
}

// setupCaretakerObservability installs OpenTelemetry for a caretaker process
// (a no-op unless telemetry is enabled; see pkg/observability) and returns
// a shutdown func to defer.
func setupCaretakerObservability(ctx context.Context) (func(), error) {
	providers, err := observability.Setup(ctx, observability.Options{
		ServiceName:    "cornus-caretaker",
		ServiceVersion: version,
	})
	if err != nil {
		return nil, fmt.Errorf("setting up observability: %w", err)
	}
	return func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = providers.Shutdown(sctx)
	}, nil
}

// CaretakerCheckCmd exits 0 iff every role in the config is live — the caretaker
// sidecar's startup-probe check, so the app container waits until all mounts are
// up. Self-contained (no util-linux).
type CaretakerCheckCmd struct {
	Config string `kong:"env='CORNUS_CARETAKER_CONFIG',help='JSON caretaker role config.'"`
}

// Run returns nil (exit 0) when the caretaker is ready, else an error (exit 1).
func (c *CaretakerCheckCmd) Run(cli *CLI) error {
	cfg, err := parseCaretakerConfig(c.Config)
	if err != nil {
		return err
	}
	return caretaker.Ready(cfg)
}

func parseCaretakerConfig(s string) (caretaker.Config, error) {
	var cfg caretaker.Config
	if s == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return cfg, fmt.Errorf("caretaker config: %w", err)
	}
	return cfg, nil
}
