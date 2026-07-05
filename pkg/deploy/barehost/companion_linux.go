//go:build linux

package barehost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/otelcollector"
)

// The caretaker config the companion runs on rides a JSON env var
// (CORNUS_CARETAKER_CONFIG). We mirror the subset of caretaker.Config's wire
// contract we emit with local structs rather than importing cornus/pkg/caretaker:
// that package's runtime (client/hub/dockerproxy) transitively links
// github.com/moby/buildkit, and barehost deliberately links ZERO buildkit (its
// whole point is a lean daemonless backend — see the package doc / JOURNAL). The
// JSON tags below MUST match caretaker.Config exactly; TestCaretakerConfigWire
// (companion_linux_test.go, which MAY import caretaker since test deps don't
// count toward the binary) guards against drift.
type caretakerConfig struct {
	Mounts []caretakerMountRole `json:"mounts,omitempty"`
	Egress *caretakerEgressRole `json:"egress,omitempty"`
	// Otel is the embedded OpenTelemetry Collector role. Its type is
	// otelcollector.Config (the SAME type caretaker.OtelRole aliases), so the JSON
	// wire form matches caretaker.Config.Otel by construction. pkg/otelcollector's
	// tag-neutral config.go carries no heavy deps (it imports only "errors"), so
	// this does not compromise barehost's zero-buildkit invariant.
	Otel  *otelcollector.Config `json:"otel,omitempty"`
	Mark  int                   `json:"mark,omitempty"`
	Token string                `json:"token,omitempty"`
}

// caretakerMountRole mirrors caretaker.MountRole.
type caretakerMountRole struct {
	Server   string `json:"server"`
	Session  string `json:"session"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// caretakerEgressRole mirrors caretaker.EgressRole.
type caretakerEgressRole struct {
	Server        string           `json:"server"`
	Session       string           `json:"session"`
	Mode          string           `json:"mode,omitempty"`
	ListenPort    int              `json:"listenPort,omitempty"`
	Rules         []api.EgressRule `json:"rules,omitempty"`
	Script        string           `json:"script,omitempty"`
	Default       string           `json:"default,omitempty"`
	SetupRedirect bool             `json:"setupRedirect,omitempty"`
}

// Companion caretaker containers — cornus-managed siblings of an app instance
// that run `cornus caretaker` joining the instance's pinned netns, realizing the
// optional MountingBackend / EgressBackend interfaces. Unlike containerdhost
// (which leans on the containerd restart-monitor and container labels), a bare
// companion is an ordinary runc container with its own instanceRecord (Role set)
// so the in-process supervisor restarts it and Delete reaps it exactly like an
// app instance — minus the CNI/hosts/resolv state it does not own.
//
// bare deliberately keeps its companions single-purpose (an egress role OR a
// mount role), NOT the always-on PortForward/AgentRelay companion containerdhost
// bundles: the bare runtime is co-located with the server, so ForwardPort
// netns-dials the instance directly (exec_linux.go) and needs no relay sidecar.

const (
	// roleMountCaretaker marks a client-local-mount relay companion (the
	// CORNUS_BARE_REMOTE sidecar mount path; mounts_linux.go).
	roleMountCaretaker = "mount-caretaker"
	// roleEgressCaretaker marks a client-side-egress companion (egress_linux.go).
	roleEgressCaretaker = "egress-caretaker"
	// roleTelemetryCaretaker marks an embedded-OpenTelemetry-Collector companion
	// (telemetry_linux.go): it receives the app's OTLP on shared-netns loopback and
	// exports outward. Unlike egress it does NOT relay through the cornus server.
	roleTelemetryCaretaker = "otel-caretaker"
)

// isCompanionRec reports whether a record is a companion caretaker rather than
// an app instance.
func isCompanionRec(rec *instanceRecord) bool { return rec.Role != "" }

// companionSpec parameterizes startCompanion for one companion caretaker.
type companionSpec struct {
	appName    string          // the deployment the companion belongs to
	compID     string          // runtime/container id (cornus-<app>-<role>-<replica>)
	replica    int             // the app replica this companion accompanies
	role       string          // roleMountCaretaker | roleEgressCaretaker
	netnsPath  string          // the app instance's pinned netns the companion joins
	agent      pulledImage     // the already-pulled cornus agent image
	agentRef   string          // the agent image ref (for the record's Image field)
	cfg        caretakerConfig // the caretaker instruction set (rides an env var)
	binds      []specs.Mount   // extra OCI mounts (mount companion's rshared scratch dirs)
	privileged bool            // the caretaker's own kernel 9P mount syscall needs this
	caps       []string        // extra capabilities (CAP_NET_ADMIN for transparent egress)
}

// startCompanion prepares a companion caretaker's rootfs+bundle from the cornus
// agent image, generates its OCI spec JOINING the app instance's netns (so its
// egress redirect / port reachability / mount propagation act in the same
// network+mount context), runs it, records it (Role set), and supervises it. On
// any failure it unwinds what it created so the caller sees a clean slate.
func (b *Backend) startCompanion(ctx context.Context, cs companionSpec) (retErr error) {
	if cs.netnsPath == "" {
		return fmt.Errorf("bare: companion %s has no app netns to join", cs.compID)
	}
	if tok := os.Getenv("CORNUS_CARETAKER_TOKEN"); tok != "" && cs.cfg.Token == "" {
		cs.cfg.Token = tok
	}
	raw, err := json.Marshal(cs.cfg)
	if err != nil {
		return fmt.Errorf("bare: marshal caretaker config: %w", err)
	}

	bundle := b.bundleDir(cs.compID)
	rootfs := filepath.Join(bundle, "rootfs")
	snapKey := "cornus-" + cs.compID
	if err := os.MkdirAll(b.recordDir(cs.compID), 0o700); err != nil {
		return fmt.Errorf("bare: companion record dir: %w", err)
	}
	if err := b.img.prepareRootfs(ctx, snapKey, cs.agent.chainID, rootfs); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = b.img.removeRootfs(ctx, snapKey, rootfs)
		}
	}()

	// The cornus image entrypoint is `cornus`, so Command ["caretaker"] runs
	// `cornus caretaker`; the config rides CORNUS_CARETAKER_CONFIG.
	compSpec := api.DeploySpec{
		Name:       cs.appName,
		Command:    []string{"caretaker"},
		Env:        map[string]string{"CORNUS_CARETAKER_CONFIG": string(raw)},
		Privileged: cs.privileged,
		CapAdd:     cs.caps,
	}
	cgPath := cgroupsPath(cs.compID, b.systemdCgroup)
	s, err := buildSpec(ctx, cs.compID, compSpec, cs.agent.img, rootfs, cs.netnsPath, cgPath, cs.binds)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bundle, 0o711); err != nil {
		return fmt.Errorf("bare: companion bundle dir: %w", err)
	}
	if err := writeBundleConfig(bundle, s); err != nil {
		return err
	}

	fio, err := newFileIO(b.logPath(cs.compID))
	if err != nil {
		return err
	}
	defer fio.Close()
	if err := b.rt.Create(ctx, cs.compID, bundle, createOpts{IO: fio, PidFile: filepath.Join(b.recordDir(cs.compID), "pid")}); err != nil {
		return fmt.Errorf("bare: create companion %s: %w", cs.compID, err)
	}
	defer func() {
		if retErr != nil {
			_ = b.rt.Delete(ctx, cs.compID, true)
		}
	}()
	if err := b.rt.Start(ctx, cs.compID); err != nil {
		return fmt.Errorf("bare: start companion %s: %w", cs.compID, err)
	}

	// Record with Role set (NetNS left empty: the companion joins the app's netns
	// but does not own it, so Delete must not tear it down). unless-stopped so the
	// supervisor resurrects it with the app.
	if err := b.writeRecord(&instanceRecord{
		ID:             cs.compID,
		App:            cs.appName,
		Image:          cs.agentRef,
		Replica:        cs.replica,
		Role:           cs.role,
		SnapshotKey:    snapKey,
		ChainID:        cs.agent.chainID.String(),
		BundleDir:      bundle,
		RootfsDir:      rootfs,
		CgroupPath:     cgPath,
		LogPath:        b.logPath(cs.compID),
		Restart:        "unless-stopped",
		DesiredRunning: true,
	}); err != nil {
		return err
	}
	if pid := b.readPid(cs.compID); pid > 0 {
		b.super.watch(cs.compID, pid)
	}
	return nil
}
