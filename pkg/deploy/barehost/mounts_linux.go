//go:build linux

package barehost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

var _ deploy.MountingBackend = (*Backend)(nil)

// caretakerMountsDir is the per-replica-per-mount scratch backing directory the
// companion 9P-mounts into with rshared propagation and the app binds with
// rslave. A DISTINCT directory per replica is required: sharing one path across
// replicas would let a mount event from one replica's caretaker propagate into a
// different replica's app container.
func (b *Backend) caretakerMountsDir(app string, replica, mountIdx int) string {
	return filepath.Join(b.baseDir(), "caretaker-mounts", app, fmt.Sprintf("%d-%d", replica, mountIdx))
}

// ApplyWithMounts implements deploy.MountingBackend: it realizes each AttachMount
// as a live 9P mount inside the workload via a per-replica companion caretaker,
// the remote-mode (CORNUS_BARE_REMOTE) alternative to the co-located host-9P fast
// path the server otherwise uses for bare (deploy_attach.go). The co-located path
// is strictly simpler and remains the default; this exists for full parity with
// the RemoteCapable contract (a backend reporting Remote()==true must realize
// mounts via ApplyWithMounts) and for hosts where the server cannot kernel-9P-
// mount itself.
//
// Each mount gets its own backing directory per replica, bound into the app
// container with rslave propagation and into the companion with rshared — the
// caretaker's own kernel 9P mount inside its rshared view propagates into the app
// container's rslave view of the very same directory.
func (b *Backend) ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeployStatus, error) {
	if len(mounts) == 0 {
		return b.Apply(ctx, spec)
	}
	for _, m := range mounts {
		if m.AgentImage == "" {
			return api.DeployStatus{}, fmt.Errorf("bare: client-local mounts via the sidecar path need the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
		}
	}
	agent, err := b.img.pull(ctx, mounts[0].AgentImage)
	if err != nil {
		return api.DeployStatus{}, fmt.Errorf("bare: pull mount agent image: %w", err)
	}

	// Attach targets are realized entirely via the companion mechanism, never as
	// an ordinary host path — strip them from the app container's own Mounts so
	// instanceMounts never binds a plain (meaningless) Source for them.
	attachTargets := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		attachTargets[m.Target] = true
	}
	appSpec := spec
	filtered := make([]api.Mount, 0, len(spec.Mounts))
	for _, sm := range spec.Mounts {
		if !attachTargets[sm.Target] {
			filtered = append(filtered, sm)
		}
	}
	appSpec.Mounts = filtered

	replicas := deploy.Replicas(spec)
	appBinds := make([][]specs.Mount, replicas)
	compBinds := make([][]specs.Mount, replicas)
	roles := make([][]caretakerMountRole, replicas)
	for i := 0; i < replicas; i++ {
		for mi, m := range mounts {
			dir := b.caretakerMountsDir(spec.Name, i, mi)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return api.DeployStatus{}, fmt.Errorf("bare: create mount backing dir: %w", err)
			}
			appBinds[i] = append(appBinds[i], propagatedBindMount(dir, m.Target, "rslave", m.ReadOnly))
			scratch := fmt.Sprintf("/cornus/mounts/%d", mi)
			compBinds[i] = append(compBinds[i], propagatedBindMount(dir, scratch, "rshared", false))
			roles[i] = append(roles[i], caretakerMountRole{
				Server:   m.RelayURL,
				Session:  m.Session,
				Name:     m.Name,
				Target:   scratch,
				ReadOnly: m.ReadOnly,
			})
		}
	}

	return b.applyInternal(ctx, appSpec, applyHooks{
		extraAppMounts: func(replica int) []specs.Mount { return appBinds[replica] },
		afterStart: func(ctx context.Context, replica int, netnsPath string) error {
			return b.startMountCompanion(ctx, spec.Name, replica, netnsPath, agent, mounts[0].AgentImage, roles[replica], compBinds[replica])
		},
	})
}

// startMountCompanion starts one replica's mount-relay companion caretaker in the
// app instance's pinned netns, carrying the mount roles and the rshared scratch
// binds whose 9P mounts propagate into the app container.
func (b *Backend) startMountCompanion(ctx context.Context, name string, replica int, netnsPath string, agent pulledImage, agentRef string, roles []caretakerMountRole, binds []specs.Mount) error {
	cs := companionSpec{
		appName:    name,
		compID:     fmt.Sprintf("cornus-%s-mount-%d", name, replica),
		replica:    replica,
		role:       roleMountCaretaker,
		netnsPath:  netnsPath,
		agent:      agent,
		agentRef:   agentRef,
		cfg:        caretakerConfig{Mounts: roles},
		binds:      binds,
		privileged: true, // the caretaker's own kernel 9P mount syscall needs this
	}
	if err := b.startCompanion(ctx, cs); err != nil {
		return fmt.Errorf("bare: start mount caretaker: %w", err)
	}
	return nil
}

// propagatedBindMount builds an rbind OCI mount carrying a shared-subtree
// propagation mode ("rshared"/"rslave") alongside the usual ro/rw option — unlike
// ociBindMount (spec_linux.go), which never sets propagation.
func propagatedBindMount(src, dst, propagation string, readOnly bool) specs.Mount {
	opts := []string{"rbind"}
	if readOnly {
		opts = append(opts, "ro")
	} else {
		opts = append(opts, "rw")
	}
	opts = append(opts, propagation)
	return specs.Mount{Destination: dst, Type: "bind", Source: src, Options: opts}
}
