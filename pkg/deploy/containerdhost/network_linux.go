//go:build linux

package containerdhost

// CNI networking: the shared machinery lives in cornus/pkg/deploy/internal/
// hostrun (CNIManager). The only containerd-specific part is teardownInstance,
// which decodes the netns/networks/ports from a container's LABELS (barehost
// passes them explicitly from its records), so cniManager wraps the shared
// manager to add it. instanceNetworks / unsupportedNetworkFeatures moved to
// hostrun and are called there.

import (
	"context"
	"encoding/json"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/deploy/internal/hostrun"
)

// cniManager wraps hostrun.CNIManager with the containerd label-decoding teardown.
type cniManager struct {
	*hostrun.CNIManager
}

func newCNIManager(dataDir string) *cniManager {
	return &cniManager{hostrun.NewCNIManager(dataDir, "containerd", "containerd")}
}

// teardownInstance detaches an instance from its networks (releasing portmap
// rules) and removes its netns, using the fields recorded in its container
// labels. Best-effort: teardown proceeds through errors.
func (m *cniManager) teardownInstance(ctx context.Context, id string, labels map[string]string) {
	nsPath := labels[labelNetNS]
	if nsPath == "" {
		return
	}
	var networks []string
	for _, n := range strings.Split(labels[labelNetworks], ",") {
		if n != "" {
			networks = append(networks, n)
		}
	}
	var ports []api.PortMapping
	if raw := labels[labelPorts]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &ports)
	}
	m.Teardown(ctx, id, nsPath, networks, ports)
}
