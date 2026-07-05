//go:build linux

package containerdhost

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	ctd "github.com/containerd/containerd"
	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
	"cornus/pkg/ingressroute"
)

// The OCI spec-opt library (envList/runtimeOpts/ociBindMount/... ) this file used
// to hold now lives in cornus/pkg/deploy/internal/hostrun (shared with barehost);
// specOpts/ociBindMount/networkNames/specAliases callers use the hostrun.*
// exports. containerLabels below stays here — it is containerd-specific (the
// restart-monitor label set has no barehost analogue, which supervises via its
// own records + monitor).

// warnUnsupported emits one warning per api.DeploySpec field this backend cannot
// honor, and is the whole of apply's refusal prelude. It is a package function
// rather than a method because it reads nothing but the spec — which is also what
// lets the coverage tests (spec_linux_test.go, specfield_coverage_linux_test.go,
// warn_once_linux_test.go) exercise the entire refusal surface without a daemon
// or a fake.
//
// The contract it serves is ".agents/docs/LTM/deploy-backend-contract.md": a
// backend that cannot honor a spec field must warn per-field, never drop it in
// silence. A silent drop is invisible to every other gate — the build passes, the
// tests pass, the deploy succeeds, and the workload is not what the operator asked
// for.
//
// Two rules for anything added here:
//
//   - Do NOT warn about Proxy/DNS/Hub/Docker/UpdateConfig/AgentForward. Those are
//     kubernetes-only on every host backend and are warned about exactly once, by
//     deploy.WarnKubernetesOnlyFields below. A second line here would contradict
//     the first; TestWarnUnsupportedNeverRepeatsAWarning counts them for that reason.
//   - Do NOT warn about a field that pkg/deploy/internal/hostrun maps (it builds
//     the OCI spec, the networks and the volume backings for both host backends) —
//     it is mapped, not dropped, just not in this package.
//
// Wording: each message names what happens INSTEAD, because "ignored" alone leaves
// the operator to guess whether the workload got a default, the image's value, or
// nothing at all.
func warnUnsupported(ctx context.Context, log *slog.Logger, spec api.DeploySpec) {
	// Kubernetes-only fields, warned once and shared across host backends.
	deploy.WarnKubernetesOnlyFields(ctx, log, spec, "CORNUS_CONTAINERD_REMOTE")
	// Mount / volume sub-options both host backends drop in hostrun (see there).
	hostrun.WarnUnmappableStorageOptions(ctx, log, spec)

	if hc := spec.Healthcheck; hc != nil && !hc.Disabled() {
		// containerd has no healthcheck engine and nothing in cornus consumes
		// health, so the check is dropped rather than half-implemented.
		log.WarnContext(ctx, "backend ignores healthcheck")
	}
	if ingressroute.Enabled(spec.Ingress) {
		// Ingress is a Kubernetes-only feature (it creates a networking.k8s.io
		// Ingress); on containerd there is no cluster ingress to program, so the
		// field is ignored rather than half-implemented. Compose files stay portable.
		//
		// Gated on the canonical predicate, not an open-coded one: a CLIENT-EMULATED
		// ingress is realized entirely on the client and asks nothing of any backend,
		// so warning about it would report a failure to do something nobody requested.
		log.WarnContext(ctx, "backend creates no cluster Ingress; the server serves this ingress itself (reach it with an ingress tunnel or CORNUS_INGRESS_LISTEN)")
	}
	if spec.Knative != nil && spec.Knative.Enabled {
		// Knative Serving needs the Knative controllers on a Kubernetes cluster;
		// containerd runs the workload as an ordinary container, so the block is
		// ignored (no autoscaling / scale-to-zero).
		log.WarnContext(ctx, "backend ignores knative (kubernetes-only feature); running as an ordinary container without autoscaling")
	}
	if feats := hostrun.UnsupportedNetworkFeatures(spec); len(feats) > 0 {
		// Every network is a generated CNI bridge (driver/driverOpts have no
		// effect); names and aliases resolve fine via the hosts-file sync.
		log.WarnContext(ctx, "backend ignores unsupported network features",
			"features", strings.Join(feats, ", "))
	}

	// Lifecycle knobs with no containerd create-time field. Stop/stopTask is this
	// backend's only shutdown path and it is hard-coded: SIGTERM, then SIGKILL
	// after stopTimeout.
	if spec.StopSignal != "" {
		log.WarnContext(ctx, "backend ignores stopSignal: a containerd container record carries no stop-signal field, and this backend's Stop always sends SIGTERM (then SIGKILL); the workload's PID 1 is signalled with SIGTERM whatever the spec asks for",
			"stopSignal", spec.StopSignal)
	}
	if spec.StopGracePeriod != "" {
		log.WarnContext(ctx, "backend ignores stopGracePeriod: the SIGTERM->SIGKILL grace is a fixed backend constant with nothing per-deployment to change it; a workload that needs longer to drain is killed when that fixed grace elapses",
			"stopGracePeriod", spec.StopGracePeriod, "actualGrace", stopTimeout.String())
	}
	if spec.Init != nil {
		log.WarnContext(ctx, "backend ignores init: there is no init binary to inject into the OCI spec (the runtime execs the image's own entrypoint as PID 1); a workload whose children are reparented to PID 1 reaps nothing, so zombies accumulate",
			"init", *spec.Init)
	}
	if spec.StdinOpen {
		log.WarnContext(ctx, "backend ignores stdinOpen: the instance's task is created with its stdio wired to the cornus log shim and no held-open stdin; a workload that reads stdin sees EOF — use `cornus exec` for an interactive session")
	}

	// Name resolution. This backend manages /etc/hosts (the peer block the
	// hosts-file sync rewrites) but never /etc/resolv.conf, so the two families
	// fail differently and are warned about separately.
	if len(spec.ExtraHosts) > 0 {
		log.WarnContext(ctx, "backend ignores extraHosts: the managed /etc/hosts carries only localhost, the instance's own name, and the cornus peer block it rewrites on every deploy; the named hosts do not resolve inside the workload",
			"extraHosts", spec.ExtraHosts)
	}
	if len(spec.DNSServers) > 0 {
		log.WarnContext(ctx, "backend ignores dnsServers: this backend writes no /etc/resolv.conf for an instance (unlike the bare backend), so there is no resolver file to point anywhere; the workload resolves through whatever /etc/resolv.conf its IMAGE ships",
			"dnsServers", spec.DNSServers)
	}
	if len(spec.DNSSearch) > 0 {
		log.WarnContext(ctx, "backend ignores dnsSearch: this backend writes no /etc/resolv.conf for an instance, so there are no search domains to add; the workload must use fully-qualified names",
			"dnsSearch", spec.DNSSearch)
	}
	if len(spec.DNSOptions) > 0 {
		log.WarnContext(ctx, "backend ignores dnsOptions: this backend writes no /etc/resolv.conf for an instance; the workload's resolver runs with its own defaults",
			"dnsOptions", spec.DNSOptions)
	}

	// Scheduling knobs the OCI/cgroup model cannot express.
	if spec.RestartMaxAttempts > 0 {
		log.WarnContext(ctx, "backend ignores restartMaxAttempts: containerd's restart monitor takes only a policy word and has no per-container attempt cap; a workload that keeps failing is restarted indefinitely rather than giving up",
			"restartMaxAttempts", spec.RestartMaxAttempts)
	}
	if r := spec.Resources; r != nil && (r.ReservedCPU > 0 || r.ReservedMemory > 0) {
		log.WarnContext(ctx, "backend ignores resource reservations: an OCI cgroup expresses caps, not guaranteed floors, and there is no scheduler here to reserve capacity against; only the limits are applied",
			"reservedCpu", r.ReservedCPU, "reservedMemory", r.ReservedMemory)
	}
}

// containerLabels builds the label set for one instance: cornus ownership and
// network-record labels (the persisted store the hosts-file sync and the startup
// reconcile pass rebuild their state from) plus containerd restart-monitor
// labels. The restart monitor (a stock containerd plugin) resurrects tasks per
// policy with no cornus involvement; logURI keeps monitor-restarted tasks logging
// through the cornus log shim.
//
// ports are the instance's actually-published host ports (nil for replica>0,
// where Apply withholds host-port publishing so portmap DNATs a host port to a
// single instance). They are recorded verbatim so netns repair re-attaches the
// exact same publishing and never installs a conflicting DNAT for a replica.
func containerLabels(spec api.DeploySpec, att hostrun.Attachment, ports []api.PortMapping, logURI string) (map[string]string, error) {
	l := map[string]string{}
	// User labels (compose `labels`) first; cornus's own management/network labels
	// are written afterwards so they always win on a key clash.
	for k, v := range spec.Labels {
		l[k] = v
	}
	l[deploy.LabelManaged] = "true"
	l[deploy.LabelApp] = spec.Name
	for k, v := range deploy.OriginToLabels(spec.Origin) {
		l[k] = v
	}
	if names := hostrun.NetworkNames(spec); len(names) > 0 {
		l[labelNetworks] = strings.Join(names, ",")
	}
	if att.Netns != "" {
		l[labelNetNS] = att.Netns
	}
	if att.IP != "" {
		l[labelIP] = att.IP
	}
	if len(att.IPs) > 0 {
		ips, err := json.Marshal(att.IPs)
		if err != nil {
			return nil, fmt.Errorf("containerd: encode net-IPs label: %w", err)
		}
		l[labelNetIPs] = string(ips)
	}
	if aliases := hostrun.SpecAliases(spec); len(aliases) > 0 {
		data, err := json.Marshal(aliases)
		if err != nil {
			return nil, fmt.Errorf("containerd: encode aliases label: %w", err)
		}
		l[labelAliases] = string(data)
	}
	if len(ports) > 0 {
		data, err := json.Marshal(ports)
		if err != nil {
			return nil, fmt.Errorf("containerd: encode ports label: %w", err)
		}
		l[labelPorts] = string(data)
	}
	// The restart policy word carries the compose deploy.restart_policy.condition
	// mapping already folded into spec.Restart by the planner (none->"no",
	// on-failure->"on-failure", any->"always"). spec.RestartMaxAttempts
	// (deploy.restart_policy.max_attempts) is NOT applied: the containerd restart
	// monitor takes only a policy word, with no per-container retry cap, so an
	// attempt limit has no field here — it is a no-op on this backend.
	policy := deploy.RestartPolicy(spec)
	if _, err := restart.NewPolicy(policy); err != nil {
		return nil, fmt.Errorf("containerd: restart policy %q: %w", policy, err)
	}
	if policy != "no" {
		l[restart.PolicyLabel] = policy
		l[restart.StatusLabel] = string(ctd.Running)
		if logURI != "" {
			l[restart.LogURILabel] = logURI
		}
	}
	return l, nil
}
