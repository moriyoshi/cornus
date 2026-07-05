//go:build linux

package barehost

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/containers"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/snapshots"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/ingressroute"
)

// warnUnsupported emits one warning per api.DeploySpec field this backend cannot
// honor, and is the whole of applyInternal's refusal prelude. It is a package
// function rather than a method because it reads nothing but the spec — which is
// also what lets the coverage tests (spec_linux_test.go,
// specfield_coverage_linux_test.go, warn_once_linux_test.go) exercise the entire
// refusal surface without an OCI runtime.
//
// The contract it serves is ".agents/docs/LTM/deploy-backend-contract.md": a
// backend that cannot honor a spec field must warn per-field, never drop it in
// silence. This backend is the reason that rule needed enforcing — it dropped
// spec.Ingress entirely, and nothing in the tree noticed.
//
// Two rules for anything added here:
//
//   - Do NOT warn about Proxy/DNS/Hub/Docker/UpdateConfig/AgentForward. Those are
//     kubernetes-only on every host backend and are warned about exactly once, by
//     deploy.WarnKubernetesOnlyFields below. A second line here would contradict
//     the first; TestWarnUnsupportedNeverRepeatsAWarning counts per field for that
//     reason.
//   - Do NOT warn about a field that pkg/deploy/internal/hostrun maps (it builds
//     the OCI spec, the networks and the volume backings for both host backends) —
//     it is mapped, not dropped, just not in this package.
//
// Wording: each message names what happens INSTEAD, because "ignored" alone leaves
// the operator to guess whether the workload got a default, the image's value, or
// nothing at all.
func warnUnsupported(ctx context.Context, log *slog.Logger, spec api.DeploySpec) {
	// Kubernetes-only fields. The empty remoteModeEnv is deliberate: this backend
	// keeps its companions single-purpose and has no ssh-agent forwarding at all,
	// so the helper takes its "no story here" arm (see AgentForwardEnabled).
	deploy.WarnKubernetesOnlyFields(ctx, log, spec, "")
	// Mount / volume sub-options both host backends drop in hostrun (see there).
	hostrun.WarnUnmappableStorageOptions(ctx, log, spec)

	if hc := spec.Healthcheck; hc != nil && !hc.Disabled() {
		log.WarnContext(ctx, "bare backend ignores healthcheck (no probe engine)")
	}
	if ingressroute.Enabled(spec.Ingress) {
		// Ingress is a Kubernetes-only feature (it programs a networking.k8s.io
		// Ingress); the bare backend has no cluster ingress to create. Every other
		// host backend says so; bare used to drop the field in silence, which is
		// the one outcome that leaves the operator with no way to find out.
		//
		// Gated on the canonical predicate: a CLIENT-EMULATED ingress is realized
		// entirely on the client and asks nothing of this backend.
		log.WarnContext(ctx, "bare backend creates no cluster Ingress; the server serves this ingress itself (reach it with an ingress tunnel or CORNUS_INGRESS_LISTEN)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		// Knative Serving needs the Knative controllers on a Kubernetes cluster; the
		// bare backend runs the workload as an ordinary container, so the block is
		// ignored (no autoscaling / scale-to-zero).
		log.WarnContext(ctx, "bare backend ignores knative (kubernetes-only feature); running as an ordinary container without autoscaling")
	}
	if feats := hostrun.UnsupportedNetworkFeatures(spec); len(feats) > 0 {
		log.WarnContext(ctx, "bare backend ignores unsupported network features",
			"features", strings.Join(feats, ", "))
	}

	// Lifecycle knobs with no field in the OCI bundle and no per-deployment hook in
	// this backend's own supervision. stopInstance is the only shutdown path and it
	// is hard-coded: SIGTERM, poll, SIGKILL after defaultStopGrace.
	if spec.StopSignal != "" {
		log.WarnContext(ctx, "bare backend ignores stopSignal: an OCI bundle carries no stop-signal field, and this backend's Stop and its supervisor always send SIGTERM (then SIGKILL); the workload's PID 1 is signalled with SIGTERM whatever the spec asks for",
			"stopSignal", spec.StopSignal)
	}
	if spec.StopGracePeriod != "" {
		log.WarnContext(ctx, "bare backend ignores stopGracePeriod: the SIGTERM->SIGKILL grace is a fixed backend constant with nothing per-deployment to change it; a workload that needs longer to drain is killed when that fixed grace elapses",
			"stopGracePeriod", spec.StopGracePeriod, "actualGrace", defaultStopGrace.String())
	}
	if spec.Init != nil {
		log.WarnContext(ctx, "bare backend ignores init: there is no init binary to inject into the bundle (the runtime execs the image's own entrypoint as PID 1); a workload whose children are reparented to PID 1 reaps nothing, so zombies accumulate",
			"init", *spec.Init)
	}
	if spec.StdinOpen {
		log.WarnContext(ctx, "bare backend ignores stdinOpen: an instance's stdin is /dev/null (its stdout and stderr go to the deployment's log file); a workload that reads stdin sees EOF — use `cornus exec` for an interactive session")
	}

	// Name resolution. This backend owns BOTH the managed /etc/hosts and the
	// managed /etc/resolv.conf, so dnsServers and dnsSearch are honored (see
	// createInstance) — extraHosts and dnsOptions are the two it writes nothing for.
	if len(spec.ExtraHosts) > 0 {
		log.WarnContext(ctx, "bare backend ignores extraHosts: the managed /etc/hosts carries only localhost, the instance's own name, and the cornus peer block it rewrites on every deploy; the named hosts do not resolve inside the workload",
			"extraHosts", spec.ExtraHosts)
	}
	if len(spec.DNSOptions) > 0 {
		log.WarnContext(ctx, "bare backend ignores dnsOptions: the managed /etc/resolv.conf is generated with nameservers and search domains only, and nothing plumbs an options line into it; the workload's resolver runs with its own defaults",
			"dnsOptions", spec.DNSOptions)
	}

	// Scheduling knobs the OCI/cgroup model cannot express.
	if r := spec.Resources; r != nil && (r.ReservedCPU > 0 || r.ReservedMemory > 0) {
		log.WarnContext(ctx, "bare backend ignores resource reservations: an OCI cgroup expresses caps, not guaranteed floors, and there is no scheduler here to reserve capacity against; only the limits are applied",
			"reservedCpu", r.ReservedCPU, "reservedMemory", r.ReservedMemory)
	}
}

// ociImage is a minimal oci.Image over our in-process content store: it is all
// oci.WithImageConfig needs to read an image's config (env, entrypoint, cmd,
// user, workingdir) and generate a runtime spec — no containerd daemon, no
// containerd image store. config is the image manifest's config descriptor,
// resolved during the pull (image_linux.go).
type ociImage struct {
	store  content.Store
	config ocispec.Descriptor
}

func (i ociImage) Config(ctx context.Context) (ocispec.Descriptor, error) { return i.config, nil }
func (i ociImage) ContentStore() content.Store                            { return i.store }

var _ oci.Image = ociImage{}

// specClient is a minimal oci.Client: oci.GenerateSpec requires a Client, but
// the spec opts cornus uses only reach for SnapshotService via a few opts we do
// not apply (rootfs is prepared out-of-band, image_linux.go). sn may be nil for
// pure spec generation.
type specClient struct {
	sn snapshots.Snapshotter
}

func (c specClient) SnapshotService(string) snapshots.Snapshotter { return c.sn }

var _ oci.Client = specClient{}

// buildSpec generates the OCI runtime spec (config.json contents) for one
// instance and returns it. The image-independent + image-config spec opts are the
// shared hostrun.SpecOpts (identical to containerdhost); barehost additionally
// feeds them to oci.GenerateSpec in-process (no daemon) via the ociImage/
// specClient wrappers, sets an absolute rootfs, and pins Linux.CgroupsPath — the
// two things the daemonless path owns that the containerd shim used to.
func buildSpec(ctx context.Context, id string, spec api.DeploySpec, img ociImage, rootfsPath, netnsPath, cgroupPath string, mounts []specs.Mount) (*specs.Spec, error) {
	// oci.WithImageConfig reads the image config from the content store, whose
	// containerd-library implementation requires a namespace on the context.
	ctx = namespaces.WithNamespace(ctx, contentNamespace)
	// Root.Path must be the absolute, already-mounted rootfs BEFORE the image
	// config opts run: WithImageConfig resolves the image user's supplementary
	// GIDs by reading <rootfs>/etc/group directly (there is no daemon-managed
	// snapshot to consult, unlike containerd), so the path has to be set first.
	opts := append([]oci.SpecOpts{oci.WithRootFSPath(rootfsPath)}, hostrun.SpecOpts(ctx, "bare", id, spec, img, netnsPath, mounts)...)
	if cgroupPath != "" {
		opts = append(opts, withCgroupsPath(cgroupPath))
	}
	s, err := oci.GenerateSpec(ctx, specClient{}, &containers.Container{ID: id}, opts...)
	if err != nil {
		return nil, fmt.Errorf("bare: generate spec for %s: %w", id, err)
	}
	return s, nil
}

// writeBundleConfig marshals the spec to <bundleDir>/config.json, the file the
// OCI runtime reads. The bundle dir must already exist with a rootfs/ beside it.
func writeBundleConfig(bundleDir string, s *specs.Spec) error {
	data, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		return fmt.Errorf("bare: marshal config.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), data, 0o644); err != nil {
		return fmt.Errorf("bare: write config.json: %w", err)
	}
	return nil
}

// cgroupsPath returns the spec's Linux.CgroupsPath for an instance. systemd form
// (slice:prefix:name) when the runtime drives systemd cgroups, else a cgroupfs
// path. The bare backend owns this because there is no daemon/shim to assign it.
func cgroupsPath(id string, systemd bool) string {
	if systemd {
		return "cornus.slice:cornus:" + id
	}
	return "/cornus/" + id
}

func withCgroupsPath(path string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Linux == nil {
			s.Linux = &specs.Linux{}
		}
		s.Linux.CgroupsPath = path
		return nil
	}
}
