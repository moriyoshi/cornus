package kubernetes

import (
	"context"
	"log/slog"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// warnUnsupportedFields warns, one line per api.DeploySpec value this backend
// accepts but cannot realize, for the gaps that do NOT already have a warning at
// the point they are translated (compose `user`, `security_opt`, `group_add`,
// `tmpfs` options, `pid`/`ipc`, `ulimits` and `devices` each warn inside
// deployment(); this covers the rest).
//
// It exists because these were all being accepted and dropped in TOTAL SILENCE.
// Kubernetes is the most capable backend, which makes a silent drop here the
// hardest to notice: the deploy succeeds, the Deployment rolls out, and the one
// knob the operator wrote is simply not there. Each message therefore names what
// happens INSTEAD, not just that the field was ignored — "ignored" leaves the
// reader guessing whether the workload is safe.
//
// It is called from applyWorkload, the single funnel every apply path (plain,
// mounts, credentials, egress; Deployment, Job and Knative Service alike) routes
// through, so it runs exactly ONCE per apply. That matters: the same warning
// emitted twice reads as two different problems, and no strings.Contains
// assertion can see the difference (see TestApplyNeverRepeatsAWarning).
func warnUnsupportedFields(ctx context.Context, spec api.DeploySpec) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))

	// compose stop_signal. A pod has no per-container stop signal: the kubelet
	// sends SIGTERM and, after terminationGracePeriodSeconds, SIGKILL.
	if spec.StopSignal != "" {
		log.WarnContext(ctx, "backend ignores stopSignal: a Kubernetes pod has no per-container stop signal; the kubelet always sends SIGTERM and then SIGKILL after the termination grace period, so the process never receives the requested signal",
			"stopSignal", spec.StopSignal)
	}

	// compose init. There is no tini-equivalent pod field; the image's own
	// entrypoint stays PID 1.
	if spec.Init != nil {
		log.WarnContext(ctx, "backend ignores init: a pod has no init-process (tini) field, so the image's own entrypoint stays PID 1 and orphaned children are not reaped",
			"init", *spec.Init)
	}

	// compose `127.0.0.1:8080:80`. A ClusterIP Service and a containerPort have no
	// host-interface binding, so the restriction the operator asked for is not
	// applied at all.
	for _, p := range spec.Ports {
		if p.HostIP != "" {
			log.WarnContext(ctx, "backend ignores a published port's host address: a ClusterIP Service has no host-interface binding, so the port is reachable on every cluster address instead of being restricted to the requested one",
				"hostIP", p.HostIP, "port", p.Container)
		}
	}

	// compose top-level volumes.<name>.driver / driver_opts. Kubernetes storage is
	// selected by the PVC's storageClass, not by a Docker volume plugin.
	for _, v := range spec.Volumes {
		if v.Driver != "" || len(v.DriverOpts) > 0 {
			log.WarnContext(ctx, "backend ignores a managed volume's driver and driver_opts: Kubernetes has no Docker-volume-plugin concept; the volume is provisioned by its storageClass (the cluster default when the spec names none) instead",
				"volume", v.Target, "driver", v.Driver)
		}
	}

	// compose healthcheck.start_interval. A Probe has a single PeriodSeconds, so
	// the start-period cadence collapses into the normal interval.
	if hc := spec.Healthcheck; hc != nil && !hc.Disabled() && len(hc.Test) > 0 && hc.StartInterval != "" {
		log.WarnContext(ctx, "backend ignores healthcheck startInterval: a Kubernetes probe has one period and no distinct start-period cadence; the probe runs at the normal interval throughout the start period",
			"startInterval", hc.StartInterval)
	}

	// compose `:z` / `:Z` on a bind mount. A mount reaches the pod over 9P from the
	// caretaker sidecar (cornus never writes a hostPath), and nothing in that path
	// relabels the source.
	for _, m := range spec.Mounts {
		if m.SELinux != "" {
			log.WarnContext(ctx, "backend ignores the SELinux relabel on a mount: the source is served into the pod over 9P by the caretaker rather than bind-mounted, so it keeps its existing SELinux context",
				"target", m.Target, "selinux", m.SELinux)
		}
	}

	// compose top-level networks.<name>.* sub-fields. Verified unread by EVERY
	// netdriver pipeline (services, bridge/ipvlan/macvlan, policy, cilium) before
	// warning unconditionally here — had any driver honored one of them, the
	// message would have to be driver-aware and this would be the wrong place for
	// it. multus.go's cniConfig already documents most of these as ignored, in a
	// comment the operator who wrote the field never reads; that is exactly the
	// gap. The reflection field-coverage guards cannot see any of this: they assert
	// coverage of api.DeploySpec's own fields, and Networks IS covered — one level
	// down is invisible to them.
	for _, n := range spec.Networks {
		// The isolation-relevant one, and the only one with a safety consequence.
		// Docker's `internal: true` builds a network with no external route. Here
		// the pod keeps its primary cluster network regardless, so outbound works
		// normally — and the isolation cornus does offer (driver "policy" /
		// driver_opts.policy=true) is INGRESS-only by construction, so nothing in
		// the backend restricts egress on account of this field.
		if n.Internal {
			log.WarnContext(ctx, "backend ignores a network's internal flag: a pod keeps its primary cluster network whatever its user-network attachments say, so the workload still reaches the outside world; cornus's network isolation (driver \"policy\") restricts ingress only, so use an egress policy if outbound must be closed",
				"network", n.Name)
		}
		// host-local IPAM in the generated NAD allocates across the subnet; there is
		// no conflist knob for a gateway or a narrower range that would be faithful.
		// dockerhost is the backend that realises these.
		if n.Gateway != "" || n.IPRange != "" {
			log.WarnContext(ctx, "backend ignores a network's ipam gateway/ip_range: the generated NAD uses host-local IPAM over the whole subnet, so addresses are allocated from the full range and the gateway is the CNI's own; only the dockerhost backend realises these",
				"network", n.Name, "gateway", n.Gateway, "ipRange", n.IPRange)
		}
		// The Multus pipeline builds a v4-only host-local/static IPAM config.
		if n.EnableIPv6 || n.IPv6 != "" {
			log.WarnContext(ctx, "backend ignores a network's IPv6 configuration: the generated NAD's IPAM is IPv4-only, so the attachment gets no v6 address and a pinned one is not assigned",
				"network", n.Name, "enableIPv6", n.EnableIPv6, "ipv6", n.IPv6)
		}
		// Docker-daemon concepts with no Kubernetes or Multus equivalent.
		if n.Attachable || n.Priority != 0 || n.MAC != "" || len(n.Labels) > 0 {
			log.WarnContext(ctx, "backend ignores a network's attachable/priority/mac/labels: these are Docker network-object concepts with no Kubernetes or Multus equivalent, so the attachment gets a CNI-assigned MAC, no ordering preference, and carries no network labels",
				"network", n.Name)
		}
	}
}

// warnStatelessOnlyAttachments warns for the two spec blocks this backend realizes
// ONLY from a deploy-attach session: brokered credentials and relay-mode egress.
// Both are driven by state that lives on the CLIENT (the source backend that mints
// the credential; the egress terminus that performs the real dial), so the caretaker
// is handed them as an AttachCredential / AttachEgress list, never read from the
// spec. A stateless `POST /.cornus/v1/deploy` carrying either therefore reached
// Apply and was dropped without a word — the deploy succeeded and the workload ran
// with no credential and no diverted egress.
//
// Called from Apply only. ApplyWithAttachments must NOT call it: that is the path
// where both ARE realized.
func warnStatelessOnlyAttachments(ctx context.Context, spec api.DeploySpec) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	if spec.Credentials != nil {
		log.WarnContext(ctx, "backend ignores credentials on the stateless deploy path: a brokered credential is minted on the client and served to the caretaker over a deploy-attach session, so this deploy has nothing to fetch from and the workload starts with no credential; deploy through a session to realize it",
			"sources", len(spec.Credentials.Sources))
	}
	// env-mode egress is realized client-side (the proxy variables are already in
	// spec.Env by the time the request is made), so only the relay modes are lost.
	if spec.Egress.NeedsRelay() {
		log.WarnContext(ctx, "backend ignores egress on the stateless deploy path: proxy/transparent egress is relayed through the caretaker's deploy-attach session, so with no session the workload egresses straight from the cluster instead of through the client",
			"mode", spec.Egress.Mode)
	}
}
