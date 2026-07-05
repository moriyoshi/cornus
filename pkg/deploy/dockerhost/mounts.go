package dockerhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/remotecompanion"
)

// roleMountCaretaker marks a remote-companion container (the sibling of
// egress.go's roleEgressCaretaker) — named for its original mount-relay-only
// purpose, though in remote mode it is now always present per instance and
// also carries the PortForward/AgentRelay roles (see startRemoteCompanion).
const roleMountCaretaker = "mount-caretaker"

// remoteCompanionMounts is one replica's own contribution to its remote
// companion's Config: the mount roles/binds ApplyWithMounts computes for that
// replica (both empty for a plain Apply in remote mode — the companion still
// starts, just with no Mounts). agentImage/serverURL, when set, override the
// backend's own b.agentImage/CORNUS_ADVERTISE_URL for this companion —
// ApplyWithMounts already has both (its caller validated AgentImage non-empty
// and RelayURL comes from the server's own already-validated
// CORNUS_ADVERTISE_URL); a plain Apply falls back to the backend-level
// sources (see WithAgentImage, relayServerURL).
type remoteCompanionMounts struct {
	roles      []caretaker.MountRole
	binds      []mountSpec
	agentImage string
	serverURL  string
}

// ApplyWithMounts implements deploy.MountingBackend: it realizes each
// AttachMount as a live 9P mount inside the workload via a per-replica
// remote-companion container, instead of dockerhost's normal co-located fast
// path (applyWithHostMounts, in pkg/server) — see deploy.RemoteCapable.
//
// Each mount gets its own Docker-managed volume PER REPLICA, used purely as a
// shared-propagation medium: the caretaker performs the actual kernel 9P mount
// inside its own (rshared) view of the volume, and that mount propagates into
// the app container's (rslave) view of the very same volume — the same
// standard Linux shared-subtree mechanism Kubernetes uses (HostToContainer/
// Bidirectional propagation), not Kubernetes-specific magic. A distinct volume
// per replica is required: sharing one volume's source path across replicas
// would let a mount event from one replica's caretaker propagate into a
// DIFFERENT replica's app container.
//
// The cornus SERVER never opens the volume's host path itself — only Engine
// API calls (create/inspect/bind) are needed, which already work against a
// non-co-located daemon (the same DOCKER_HOST=tcp://... support Apply always
// had), which is what lets this realize mounts even when the server does not
// share a filesystem with the daemon at all.
func (b *Backend) ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeployStatus, error) {
	if len(mounts) == 0 {
		return b.Apply(ctx, spec)
	}
	for _, m := range mounts {
		if m.AgentImage == "" {
			return api.DeployStatus{}, fmt.Errorf("dockerhost: client-local mounts via the sidecar path need the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
		}
	}

	// Attach targets are realized entirely via the mechanism below, never as an
	// ordinary host path — strip them from the app container's own Mounts so
	// toCreateBody never binds a plain (and meaningless) Source for them.
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
	appBinds := make([][]mountSpec, replicas)
	companions := make([]remoteCompanionMounts, replicas)
	for i := 0; i < replicas; i++ {
		companions[i].agentImage = mounts[0].AgentImage
		companions[i].serverURL = mounts[0].RelayURL
		for mi, m := range mounts {
			volName := fmt.Sprintf("cornus-%s-mount-%d-%d", spec.Name, i, mi)
			if err := b.api.volumeEnsure(ctx, api.VolumeSpec{Name: volName}); err != nil {
				return api.DeployStatus{}, fmt.Errorf("dockerhost: create mount volume: %w", err)
			}
			mp, err := b.api.volumeInspect(ctx, volName)
			if err != nil {
				return api.DeployStatus{}, fmt.Errorf("dockerhost: inspect mount volume: %w", err)
			}
			appBinds[i] = append(appBinds[i], mountSpec{
				Type:        "bind",
				Source:      mp,
				Target:      m.Target,
				ReadOnly:    m.ReadOnly,
				BindOptions: &bindOptions{Propagation: "rslave"},
			})
			scratch := fmt.Sprintf("/cornus/mounts/%d", mi)
			companions[i].binds = append(companions[i].binds, mountSpec{
				Type:        "bind",
				Source:      mp,
				Target:      scratch,
				BindOptions: &bindOptions{Propagation: "rshared"},
			})
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
		func(replica int) []mountSpec { return appBinds[replica] },
		func(replica int) remoteCompanionMounts { return companions[replica] },
	)
}

// startRemoteCompanion creates and starts the always-on remote-companion
// caretaker sharing appID's network namespace (so its PortForwardRole can
// reach the instance's own ports on loopback, matching the egress companion's
// existing NetworkMode pattern) and, when cm carries any, the mount roles'
// scratch-volume binds (rshared propagation). It always carries PortForward
// and AgentRelay roles: any remote-mode instance can be port-forwarded/
// tunneled into and have an ssh-agent forwarded into an exec session, whether
// or not the deploy also uses --mount.
func (b *Backend) startRemoteCompanion(ctx context.Context, name, appID string, replica int, agentImage string, cm remoteCompanionMounts) error {
	if cm.agentImage != "" {
		agentImage = cm.agentImage
	}
	if agentImage == "" {
		return fmt.Errorf("dockerhost: CORNUS_DOCKER_REMOTE is set but no agent image is configured (set CORNUS_AGENT_IMAGE)")
	}
	serverURL := cm.serverURL
	if serverURL == "" {
		serverURL = b.relayServerURL()
	}
	if serverURL == "" {
		return fmt.Errorf("dockerhost: remote mode requires CORNUS_ADVERTISE_URL (the cornus URL the remote companion dials)")
	}
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
	binds := append([]mountSpec{}, cm.binds...)
	body := createBody{
		Image: agentImage,
		Cmd:   []string{"caretaker"}, // the cornus image entrypoint is `cornus`
		Env:   []string{"CORNUS_CARETAKER_CONFIG=" + string(raw)},
		Labels: map[string]string{
			deploy.LabelManaged: "true",
			deploy.LabelApp:     name,
			labelRole:           roleMountCaretaker,
		},
		HostConfig: hostConfig{
			Mounts:        binds,
			NetworkMode:   "container:" + appID,
			RestartPolicy: restartPolicy{Name: "unless-stopped"},
			Privileged:    true, // the caretaker's own kernel 9P mount syscall needs this
		},
	}
	id, err := b.api.containerCreate(ctx, fmt.Sprintf("cornus-%s-mount-%d", name, replica), body)
	if err != nil {
		return err
	}
	return b.api.containerStart(ctx, id)
}

// relayServerURL is the cornus server URL a remote companion's PortForward/
// AgentRelay roles dial — the same server that drives this backend, i.e. the
// URL the companion's own attach connection already uses for any mount roles.
// When there are no mount roles (a plain Apply in remote mode) there is no
// deploy.AttachMount to source it from, so it comes from CORNUS_ADVERTISE_URL
// (the server's own advertised URL, already used elsewhere for sidecar dials).
func (b *Backend) relayServerURL() string {
	return os.Getenv("CORNUS_ADVERTISE_URL")
}
