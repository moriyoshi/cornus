//go:build linux

package incushost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"strings"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/remotecompanion"
)

// Companion caretakers on Incus — cornus-managed sibling instances of an app
// instance, running `cornus caretaker` so the server can reach INTO a workload
// it may have no route to. They are this backend's answer to
// deploy.RemoteCapable, and they exist for every replica whenever remote mode is
// on (dockerhost/containerdhost's "always-on companion" shape), carrying the
// PortForward and AgentRelay roles.
//
// Two things differ from every other host backend's companion, both forced by
// Incus rather than chosen:
//
//   - It is a SIBLING INSTANCE, not a netns-sharing sidecar. Incus exposes no
//     way for one instance to join another's network namespace (there is no
//     config key for it, and the OCI application-container path does not run
//     through anything that could pin one), so the companion instead sits on the
//     same bridge as the app instance and dials it by address. That is what
//     caretaker.PortForwardRole.Host is for: with an empty Host the caretaker
//     dials loopback because it IS in the app's netns; here it dials the app
//     instance.
//   - The AgentRelay socket rides a SHARED CUSTOM STORAGE VOLUME instead of a
//     host bind. The other backends bind a directory under the server's data
//     dir into both containers, which silently assumes the server can see the
//     runtime's filesystem — the very assumption remote mode exists to drop. A
//     custom volume is created and attached entirely through the daemon API, so
//     it holds up wherever the daemon is; the volume is mounted at the same
//     remotecompanion.AgentScratchDir in both instances, so the unix socket the
//     caretaker binds is the SSH_AUTH_SOCK an exec'd process connects to.
//
// Mounts and egress roles are deliberately NOT wired here: those need the
// caretaker's own kernel 9P mount to propagate into the app instance's mount
// namespace, which a sibling instance cannot do. That is why this backend still
// implements neither deploy.MountingBackend nor an egress attachment.

const (
	// companionRoleKey stamps a companion instance with its role, so instance
	// listing can tell a companion from an app replica. App instances never carry
	// it, which is what keeps Status/Logs/Exec replica indices meaning what they
	// have always meant.
	companionRoleKey = configKeyPrefix + "cornus.role"
	// roleMountCaretaker is the role value, matching the name
	// dockerhost/containerdhost give the same always-on companion.
	roleMountCaretaker = "mount-caretaker"
	// agentVolumeDevice is the device name under which the shared agent volume is
	// attached to both the app instance and its companion.
	agentVolumeDevice = "cornus-agent"
)

// companionName names replica i's companion. It stays inside Incus's DNS-label
// instance-name limit for the same app names instanceName accepts.
func companionName(app string, i int) string { return fmt.Sprintf("cornus-%s-mount-%d", app, i) }

// agentVolumeName names the shared agent volume for replica i. Incus storage
// volume names take the same character set as instance names.
func agentVolumeName(app string, i int) string { return fmt.Sprintf("cornus-%s-%d-agent", app, i) }

// isCompanion reports whether an instance is a companion caretaker rather than
// an app replica.
func isCompanion(in incusapi.Instance) bool { return in.Config[companionRoleKey] != "" }

// relayServerURL is the cornus server URL a companion's PortForward/AgentRelay
// roles dial back on — CORNUS_ADVERTISE_URL, as on containerdhost. It has to be
// the address reachable FROM the instance, which is exactly what the advertise
// URL already promises.
func (b *Backend) relayServerURL() string { return os.Getenv("CORNUS_ADVERTISE_URL") }

// agentVolumeDeviceFor renders the disk device attaching replica i's shared
// agent volume at remotecompanion.AgentScratchDir. Both the app instance and its
// companion get an identical one; the volume is created with security.shifted so
// two unprivileged instances with different id maps can both mount it.
func (b *Backend) agentVolumeDeviceFor(app string, i int) map[string]string {
	return map[string]string{
		"type":   "disk",
		"pool":   b.pool,
		"source": agentVolumeName(app, i),
		"path":   remotecompanion.AgentScratchDir,
	}
}

// remoteConfigError reports the operator configuration remote mode needs but
// does not have, or nil. Checked once up front in Apply so a misconfigured
// remote deploy fails before it creates anything, rather than half-way through.
func (b *Backend) remoteConfigError() error {
	if b.agentImage == "" {
		return fmt.Errorf("incus: remote mode needs the cornus agent image (set CORNUS_AGENT_IMAGE on the server)")
	}
	if b.relayServerURL() == "" {
		return fmt.Errorf("incus: remote mode requires CORNUS_ADVERTISE_URL (the cornus URL the companion instance dials back on)")
	}
	return nil
}

// ensureAgentVolume creates replica i's shared agent volume if it is not already
// there.
func (b *Backend) ensureAgentVolume(app string, i int) error {
	if err := b.conn.CreateVolume(b.pool, agentVolumeName(app, i), map[string]string{
		// Let both instances mount it despite each having its own id map.
		"security.shifted": "true",
	}); err != nil {
		return fmt.Errorf("incus: creating agent volume for %s replica %d in pool %q: %w", app, i, b.pool, err)
	}
	return nil
}

// deleteAgentVolumes removes the agent volumes of every replica of app, from 0
// up to n-1. Called after the instances are gone (Incus refuses to delete a
// volume that is still attached).
func (b *Backend) deleteAgentVolumes(app string, n int) error {
	for i := 0; i < n; i++ {
		if err := b.conn.DeleteVolume(b.pool, agentVolumeName(app, i)); err != nil {
			return fmt.Errorf("incus: deleting agent volume for %s replica %d: %w", app, i, err)
		}
	}
	return nil
}

// companionConfig is the caretaker instruction set replica i's companion runs:
// the two roles that let the server reach into an instance it cannot route to.
// Pure, so the wire form is unit-tested without a daemon.
func companionConfig(app string, i int, serverURL, appHost string) caretaker.Config {
	cfg := caretaker.Config{
		Instance:    remotecompanion.InstanceKey(app, i),
		PortForward: &caretaker.PortForwardRole{Server: serverURL, Host: appHost},
		AgentRelay:  &caretaker.AgentRelayRole{Server: serverURL, SocketPath: remotecompanion.AgentSocketPath},
	}
	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" {
		cfg.Token = tok
	}
	return cfg
}

// buildCompanionPost renders replica i's companion create request: the cornus
// agent image with its entrypoint overridden to `cornus caretaker`, the
// caretaker config on an env var, and the shared agent volume attached.
func (b *Backend) buildCompanionPost(ctx context.Context, app string, i int, appHost string) (incusapi.InstancesPost, error) {
	var credential *deploy.RegistryCredential
	if b.creds != nil {
		resolved, ok, err := b.creds(ctx, b.agentImage)
		if err != nil {
			return incusapi.InstancesPost{}, fmt.Errorf("incus: resolve registry credential for %s: %w", b.agentImage, err)
		}
		if ok {
			credential = &resolved
		}
	}
	src, err := imageSource(b.agentImage, credential)
	if err != nil {
		return incusapi.InstancesPost{}, err
	}
	raw, err := json.Marshal(companionConfig(app, i, b.relayServerURL(), appHost))
	if err != nil {
		return incusapi.InstancesPost{}, fmt.Errorf("incus: marshal caretaker config: %w", err)
	}

	post := incusapi.InstancesPost{
		Name:   companionName(app, i),
		Type:   incusapi.InstanceTypeContainer,
		Source: src,
		Start:  true,
	}
	post.Config = map[string]string{
		configKeyPrefix + deploy.LabelManaged: "true",
		configKeyPrefix + deploy.LabelApp:     app,
		companionRoleKey:                      roleMountCaretaker,
		imageConfigKey:                        b.agentImage,
		// The cornus image's own entrypoint is `cornus`; oci.entrypoint replaces it
		// wholesale, so the companion runs `cornus caretaker` rather than a server.
		"oci.entrypoint":                      ociEntrypoint([]string{"cornus", "caretaker"}),
		"environment.CORNUS_CARETAKER_CONFIG": string(raw),
		// Come back with the app instance after a host reboot or a crash.
		"boot.autorestart": "true",
	}
	post.Devices = map[string]map[string]string{
		agentVolumeDevice: b.agentVolumeDeviceFor(app, i),
	}
	return post, nil
}

// startCompanion creates and starts replica i's companion. appHost is the
// address its PortForwardRole dials for the app instance.
func (b *Backend) startCompanion(ctx context.Context, app string, i int, appHost string) error {
	post, err := b.buildCompanionPost(ctx, app, i, appHost)
	if err != nil {
		return err
	}
	if err := b.conn.CreateInstance(post); err != nil {
		return fmt.Errorf("incus: creating companion %s: %w", post.Name, err)
	}
	return nil
}

// ociEntrypoint renders argv as the shell-quoted single string Incus's
// oci.entrypoint config key takes (incusd splits it with shell-word rules before
// handing it to the init process). Single-quoting every word and escaping any
// embedded quote is the conservative encoding: it needs no knowledge of which
// characters the splitter treats specially, and the quote-escape it emits is the
// same one incusd's own shellquote.Join produces. Used for the companion's
// `cornus caretaker` argv here, and for an app instance's entrypoint override in
// spec_linux.go.
func ociEntrypoint(argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}
