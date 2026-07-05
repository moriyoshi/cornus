//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"fmt"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/otelcollector"
)

// roleOtelCaretaker marks an embedded-OpenTelemetry-Collector companion task
// (labelRole). Like the egress companion it joins the app instance's pinned
// netns, but it does NOT relay through the cornus server — the collector receives
// the app's OTLP on shared-netns loopback and exports outward. isCompanion
// (egress_linux.go) recognizes it via the non-empty labelRole, so Delete/Status/
// List handle it with no extra code.
const roleOtelCaretaker = "otel-caretaker"

// startTelemetryCompanion starts one replica's embedded-Collector companion task,
// joining the app instance's pinned netns so it binds the OTLP receiver on that
// instance's loopback. The app is pointed at it by the OTEL_* env
// BuildTelemetryWiring injected into the app container. No privilege or
// capabilities are needed (loopback bind + outward export only).
func (b *Backend) startTelemetryCompanion(ctx context.Context, name, netnsPath string, replica int, img ctd.Image, role otelcollector.Config) (retErr error) {
	nctx := b.ns(ctx)
	compID := fmt.Sprintf("cornus-%s-otel-%d", name, replica)
	o := role
	cfg := caretaker.Config{Otel: &o}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	opts := hostrun.SpecOpts(ctx, "containerd", compID, api.DeploySpec{
		Command: []string{"caretaker"}, // the cornus image entrypoint is `cornus`
	}, img, netnsPath, nil)
	// The caretaker config rides an env var; append it after the base spec opts.
	opts = append(opts, oci.WithEnv([]string{"CORNUS_CARETAKER_CONFIG=" + string(raw)}))
	logURI, err := b.logURI(compID)
	if err != nil {
		return err
	}
	labels := map[string]string{
		deploy.LabelManaged: "true",
		deploy.LabelApp:     name,
		labelRole:           roleOtelCaretaker,
		restart.PolicyLabel: "unless-stopped",
		restart.StatusLabel: string(ctd.Running),
		restart.LogURILabel: logURI,
	}
	c, err := b.client.CreateContainer(nctx, compID, img, labels, opts)
	if err != nil {
		return fmt.Errorf("create %s: %w", compID, err)
	}
	defer func() {
		if retErr != nil {
			_ = c.Delete(nctx, ctd.WithSnapshotCleanup)
		}
	}()
	if err := b.startTask(nctx, c, logURI); err != nil {
		return fmt.Errorf("start %s: %w", compID, err)
	}
	return nil
}
