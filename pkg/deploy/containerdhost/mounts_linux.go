//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/runtime/restart"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/remotecompanion"
)

// roleMountCaretaker marks a remote-companion container/task (the sibling of
// egress_linux.go's roleEgressCaretaker) — named for its original
// mount-relay-only purpose, though in remote mode it is now always present
// per instance and also carries the PortForward/AgentRelay roles (see
// startRemoteCompanion).
const roleMountCaretaker = "mount-caretaker"

// caretakerMountsDir is the per-deployment scratch base for the remote
// companion's shared-propagation backing directories (see ApplyWithMounts).
// Unlike dockerhost's Docker-managed volumes, containerd has no
// daemon-managed-storage abstraction — this backend's client dialer is
// hard-coded to a local unix socket (no remote transport at all), so it is
// always co-located with the cornus server, and a plain host directory the
// server creates is exactly as valid as the volumes dockerhost provisions
// through the (possibly remote) Engine API.
func (b *Backend) caretakerMountsDir(app string, replica, mountIdx int) string {
	return filepath.Join(b.dataDir, "containerd", "caretaker-mounts", app, fmt.Sprintf("%d-%d", replica, mountIdx))
}

// caretakerAgentDir is the per-replica scratch backing directory for the
// companion's AgentRelayRole socket (remotecompanion.AgentScratchDir) —
// independent of caretakerMountsDir, so it exists (and the agent socket is
// reachable inside the app container) even for an instance with no
// client-local mounts at all.
func (b *Backend) caretakerAgentDir(app string, replica int) string {
	return filepath.Join(b.dataDir, "containerd", "caretaker-agent", app, fmt.Sprintf("%d", replica))
}

// remoteCompanionMounts is one replica's own contribution to its remote
// companion's Config: the mount roles/binds ApplyWithMounts computes for that
// replica (both empty for a plain Apply in remote mode — the companion still
// starts, just with no Mounts). serverURL, when set, overrides
// CORNUS_ADVERTISE_URL for this companion — ApplyWithMounts already has it
// (RelayURL comes from the server's own already-validated
// CORNUS_ADVERTISE_URL); a plain Apply falls back to reading the env var
// directly (see relayServerURL).
type remoteCompanionMounts struct {
	roles     []caretaker.MountRole
	binds     []specs.Mount
	serverURL string
}

// ApplyWithMounts implements deploy.MountingBackend: it realizes each
// AttachMount as a live 9P mount inside the workload via a per-replica
// remote-companion task, instead of being unsupported (containerd has no
// co-located host-mount fallback the way dockerhost does) — see
// deploy.RemoteCapable. True remote-daemon reachability is NOT achieved here
// (see caretakerMountsDir); this only changes how client-local mounts are
// realized on the (necessarily still co-located) daemon.
//
// Each mount gets its own backing directory PER REPLICA, bound into the app
// container with "rslave" propagation and into the caretaker with "rshared" —
// the caretaker's own kernel 9P mount inside its rshared view propagates into
// the app container's rslave view of the very same directory. A distinct
// directory per replica is required: sharing one path across replicas would
// let a mount event from one replica's caretaker propagate into a DIFFERENT
// replica's app container.
func (b *Backend) ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeployStatus, error) {
	if len(mounts) == 0 {
		return b.Apply(ctx, spec)
	}
	for _, m := range mounts {
		if m.AgentImage == "" {
			return api.DeployStatus{}, fmt.Errorf("containerd: client-local mounts via the sidecar path need the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
		}
	}

	// Attach targets are realized entirely via the mechanism below, never as an
	// ordinary host path — strip them from the app container's own Mounts so
	// instanceMounts never binds a plain (and meaningless) Source for them.
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
	companions := make([]remoteCompanionMounts, replicas)
	for i := 0; i < replicas; i++ {
		companions[i].serverURL = mounts[0].RelayURL
		for mi, m := range mounts {
			dir := b.caretakerMountsDir(spec.Name, i, mi)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return api.DeployStatus{}, fmt.Errorf("containerd: create mount backing dir: %w", err)
			}
			appBinds[i] = append(appBinds[i], propagatedBindMount(dir, m.Target, "rslave", m.ReadOnly))
			scratch := fmt.Sprintf("/cornus/mounts/%d", mi)
			companions[i].binds = append(companions[i].binds, propagatedBindMount(dir, scratch, "rshared", false))
			companions[i].roles = append(companions[i].roles, caretaker.MountRole{
				Server:     m.RelayURL,
				Session:    m.Session,
				Name:       m.Name,
				Target:     scratch,
				ReadOnly:   m.ReadOnly,
				AsyncCache: m.AsyncCache,
			})
		}
	}

	return b.apply(ctx, appSpec,
		func(replica int) []specs.Mount { return appBinds[replica] },
		func(replica int) remoteCompanionMounts { return companions[replica] },
	)
}

// propagatedBindMount builds an rbind OCI mount carrying a shared-subtree
// propagation mode ("rshared"/"rslave") alongside the usual ro/rw option —
// unlike ociBindMount (spec_linux.go), which never sets propagation.
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

// relayServerURL is the cornus server URL a remote companion's PortForward/
// AgentRelay roles dial, for the plain-Apply case (no deploy.AttachMount to
// source a RelayURL from) — CORNUS_ADVERTISE_URL, already used elsewhere for
// sidecar dials (egress, kubernetes credentials).
func (b *Backend) relayServerURL() string {
	return os.Getenv("CORNUS_ADVERTISE_URL")
}

// startRemoteCompanion creates and starts the always-on remote-companion
// caretaker task JOINING netnsPath (the app instance's own pinned netns, so
// its PortForwardRole can reach the instance's own ports on loopback,
// matching the egress companion's existing netns-join pattern) and, when cm
// carries any, the mount roles' scratch-directory binds (rshared
// propagation). It always carries PortForward and AgentRelay roles: any
// remote-mode instance can be port-forwarded/tunneled into and have an
// ssh-agent forwarded into an exec session, whether or not the deploy also
// uses --mount. agentImg is the already-pulled agent image (apply pulls it
// once per Apply call, not per replica).
func (b *Backend) startRemoteCompanion(ctx context.Context, name, netnsPath string, replica int, agentImg ctd.Image, cm remoteCompanionMounts) (retErr error) {
	serverURL := cm.serverURL
	if serverURL == "" {
		serverURL = b.relayServerURL()
	}
	if serverURL == "" {
		return fmt.Errorf("containerd: remote mode requires CORNUS_ADVERTISE_URL (the cornus URL the remote companion dials)")
	}
	nctx := b.ns(ctx)
	compID := fmt.Sprintf("cornus-%s-mount-%d", name, replica)
	instance := remotecompanion.InstanceKey(name, replica)
	agentSocket := remotecompanion.AgentSocketPath
	cfg := caretaker.Config{
		Instance:    instance,
		Mounts:      cm.roles,
		PortForward: &caretaker.PortForwardRole{Server: serverURL},
		AgentRelay:  &caretaker.AgentRelayRole{Server: serverURL, SocketPath: agentSocket},
	}
	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
		cfg.Token = tok
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	opts := hostrun.SpecOpts(ctx, "containerd", compID, api.DeploySpec{
		Command:    []string{"caretaker"}, // the cornus image entrypoint is `cornus`
		Privileged: true,                  // the caretaker's own kernel 9P mount syscall needs this
	}, agentImg, netnsPath, cm.binds)
	opts = append(opts, oci.WithEnv([]string{"CORNUS_CARETAKER_CONFIG=" + string(raw)}))
	logURI, err := b.logURI(compID)
	if err != nil {
		return err
	}
	labels := map[string]string{
		deploy.LabelManaged: "true",
		deploy.LabelApp:     name,
		labelRole:           roleMountCaretaker,
		restart.PolicyLabel: "unless-stopped",
		restart.StatusLabel: string(ctd.Running),
		restart.LogURILabel: logURI,
	}
	return b.createAndStartCaretaker(nctx, compID, agentImg, labels, opts, logURI)
}

// createAndStartCaretaker creates and starts one companion caretaker
// container/task, cleaning up the container on a start failure — the shared
// tail of startRemoteCompanion and (could, in a future refactor)
// startEgressCompanion.
func (b *Backend) createAndStartCaretaker(nctx context.Context, id string, img ctd.Image, labels map[string]string, opts []oci.SpecOpts, logURI string) (retErr error) {
	c, err := b.client.CreateContainer(nctx, id, img, labels, opts)
	if err != nil {
		return fmt.Errorf("containerd: create %s: %w", id, err)
	}
	defer func() {
		if retErr != nil {
			_ = c.Delete(nctx, ctd.WithSnapshotCleanup)
		}
	}()
	if err := b.startTask(nctx, c, logURI); err != nil {
		return fmt.Errorf("containerd: start %s: %w", id, err)
	}
	return nil
}
