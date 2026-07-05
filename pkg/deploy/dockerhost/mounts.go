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

// companionPlan is one replica's contribution to its companion caretaker's
// Config, collected BEFORE anything is created so a single container can carry
// every role that replica needs instead of one container per role.
//
// Two roles land here (mounts from ApplyWithMounts, egress from
// ApplyWithEgress); the remote-mode roles the backend adds itself
// (PortForward/AgentRelay) are not plan-driven, since they depend only on
// b.remote. Everything may be empty: in remote mode the companion still starts,
// just with no plan-contributed roles.
//
// agentImage/serverURL, when set, override the backend's own
// b.agentImage/CORNUS_ADVERTISE_URL for this companion — the attachment entry
// points already have both (their caller validated AgentImage non-empty, and
// RelayURL comes from the server's own already-validated CORNUS_ADVERTISE_URL);
// a plain Apply falls back to the backend-level sources (see WithAgentImage,
// relayServerURL).
type companionPlan struct {
	roles      []caretaker.MountRole
	egress     *caretaker.EgressRole
	binds      []mountSpec
	agentImage string
	serverURL  string
	// mark is the SO_MARK the companion stamps on its own sockets, and capAdd
	// the extra capabilities it needs — both driven by transparent egress. See
	// companionEgressMark (egress.go).
	mark   int
	capAdd []string
}

// hasRoles reports whether the plan contributes any role, i.e. whether a
// companion must exist even when the backend is not in remote mode.
func (p companionPlan) hasRoles() bool { return len(p.roles) > 0 || p.egress != nil }

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
	return b.ApplyWithAttachments(ctx, spec, mounts, nil, nil)
}

// planMounts provisions each (replica, mount) volume pair and returns the app
// spec with attach targets stripped, the per-replica app-side binds, and the
// per-replica companion contributions. Split out of ApplyWithMounts so
// ApplyWithAttachments can fold the result into one companion alongside egress.
func (b *Backend) planMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeploySpec, [][]mountSpec, []companionPlan, error) {
	for _, m := range mounts {
		if m.AgentImage == "" {
			return spec, nil, nil, fmt.Errorf("dockerhost: client-local mounts via the sidecar path need the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
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
	companions := make([]companionPlan, replicas)
	for i := 0; i < replicas; i++ {
		companions[i].agentImage = mounts[0].AgentImage
		companions[i].serverURL = mounts[0].RelayURL
		for mi, m := range mounts {
			volName := fmt.Sprintf("cornus-%s-mount-%d-%d", spec.Name, i, mi)
			if err := b.api.volumeEnsure(ctx, api.VolumeSpec{Name: volName}); err != nil {
				return spec, nil, nil, fmt.Errorf("dockerhost: create mount volume: %w", err)
			}
			mp, err := b.api.volumeInspect(ctx, volName)
			if err != nil {
				return spec, nil, nil, fmt.Errorf("dockerhost: inspect mount volume: %w", err)
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

	return appSpec, appBinds, companions, nil
}

// startCompanion creates and starts ONE companion caretaker per replica,
// sharing appID's network namespace (NetworkMode: container:<app>) and carrying
// every role that replica needs — mounts and egress from the plan, plus
// PortForward and AgentRelay whenever the backend is in remote mode, so any
// remote-mode instance can be port-forwarded/tunneled into and have an ssh-agent
// forwarded into an exec session whether or not the deploy uses --mount.
//
// Every role wants the same netns, so folding them into one container is what
// the caretaker was always able to do (pkg/caretaker.Run composes an arbitrary
// role set under one supervisor tree); running one container per role was an
// artifact of each role arriving through its own entry point. Two containers in
// one netns also meant the egress redirect captured the other companion's server
// dial — impossible now that a single process holds one process-wide Mark.
//
// Naming and role label follow whichever companion this REPLACES, so existing
// tooling and E2E assertions keep matching: remote mode keeps the mount
// caretaker's identity (it exists for every instance regardless of the plan),
// and a non-remote egress-only deploy keeps the egress caretaker's.
func (b *Backend) startCompanion(ctx context.Context, name, appID string, replica int, plan companionPlan) error {
	agentImage := plan.agentImage
	if agentImage == "" {
		agentImage = b.agentImage
	}
	if agentImage == "" {
		return fmt.Errorf("dockerhost: a companion caretaker is needed but no agent image is configured (set CORNUS_AGENT_IMAGE)")
	}
	cfg := caretaker.Config{
		Mounts: plan.roles,
		Egress: plan.egress,
		Mark:   plan.mark,
		// Flight records go in the companion's own scratch dir, which is a
		// Docker-managed volume, so an incident spanning the server and its
		// caretaker can be reconstructed rather than stopping at the seam.
		ActivityDir: remotecompanion.AgentScratchDir + "/activity",
	}
	if b.remote {
		// PortForward/AgentRelay exist to serve the SERVER, so they need the URL
		// the companion dials back on; the egress role carries its own.
		serverURL := plan.serverURL
		if serverURL == "" {
			serverURL = b.relayServerURL()
		}
		if serverURL == "" {
			return fmt.Errorf("dockerhost: remote mode requires CORNUS_ADVERTISE_URL (the cornus URL the remote companion dials)")
		}
		cfg.Instance = remotecompanion.InstanceKey(name, replica)
		cfg.PortForward = &caretaker.PortForwardRole{Server: serverURL}
		cfg.AgentRelay = &caretaker.AgentRelayRole{Server: serverURL, SocketPath: remotecompanion.AgentSocketPath}
	}
	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
		cfg.Token = tok
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	compName, role := fmt.Sprintf("cornus-%s-egress-%d", name, replica), roleEgressCaretaker
	if b.remote {
		compName, role = fmt.Sprintf("cornus-%s-mount-%d", name, replica), roleMountCaretaker
	}
	body := createBody{
		Image: agentImage,
		Cmd:   []string{"caretaker"}, // the cornus image entrypoint is `cornus`
		Env:   []string{"CORNUS_CARETAKER_CONFIG=" + string(raw)},
		Labels: map[string]string{
			deploy.LabelManaged: "true",
			deploy.LabelApp:     name,
			labelRole:           role,
		},
		HostConfig: hostConfig{
			Mounts:        append([]mountSpec{}, plan.binds...),
			NetworkMode:   "container:" + appID,
			RestartPolicy: restartPolicy{Name: "unless-stopped"},
			CapAdd:        plan.capAdd,
			// Only the caretaker's own kernel 9P mount syscall needs privilege.
			// An egress-only companion on a co-located daemon stays unprivileged
			// exactly as before, and remote mode keeps its blanket privilege since
			// any of its instances may gain a mount on a later apply.
			Privileged: b.remote || len(plan.roles) > 0,
		},
	}
	id, err := b.api.containerCreate(ctx, compName, body)
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
