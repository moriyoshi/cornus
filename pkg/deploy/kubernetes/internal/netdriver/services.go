package netdriver

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"cornus/pkg/deploy"
	"cornus/pkg/logging"
)

// dns1123LabelMaxLen is the RFC1123 label limit Kubernetes enforces on a
// Service name (metadata.name). An alias longer than this cannot be a valid
// Service name — and, being a single DNS label, could never resolve as a bare
// hostname anyway — so such an alias is skipped rather than truncated (a
// hash-truncated name would silently fail to match the alias clients look up).
const dns1123LabelMaxLen = 63

// servicesProvider is the DNS baseline: one headless Service (ClusterIP: None)
// per attachment alias, selecting the workload's pods, so a member resolves by
// its bare service name (and aliases) via CoreDNS on ANY cluster — no fabric,
// no published ports needed. It is prepended to every fabric pipeline.
//
// Known shared-namespace limitation: alias Services are created best-effort —
// if two projects both have a service "web", the first deployment's Service
// keeps the name (create-if-absent). The project-unique `<project>-<service>`
// ClusterIP Service (created by the backend for published ports) is unaffected.
type servicesProvider struct{}

func (servicesProvider) Name() string                                        { return "services" }
func (servicesProvider) Requires() []Capability                              { return nil }
func (servicesProvider) NetworkScoped(Attachment) ([]Object, error)          { return nil, nil }
func (servicesProvider) MutatePod(Attachment, *corev1.PodTemplateSpec) error { return nil }

func (servicesProvider) WorkloadScoped(a Attachment) ([]Object, error) {
	// A headless Service may carry no ports and still yield DNS A records;
	// include the workload's container ports when it has any, so SRV lookups
	// and port-aware clients work too.
	var ports []corev1.ServicePort
	for _, p := range a.Spec.Ports {
		proto := corev1.ProtocolTCP
		if p.Protocol == "udp" {
			proto = corev1.ProtocolUDP
		}
		ports = append(ports, corev1.ServicePort{
			// Include the protocol so tcp+udp on the same container port yield
			// distinct, valid port names (Kubernetes rejects duplicate names in
			// a multi-port Service).
			Name:       "p" + strconv.Itoa(p.Container) + "-" + strings.ToLower(string(proto)),
			Port:       int32(p.Container),
			TargetPort: intstr.FromInt32(int32(p.Container)),
			Protocol:   proto,
		})
	}
	log := logging.FromContext(context.Background(), slog.String("component", "netdriver"))
	var out []Object
	for _, alias := range a.Net.Aliases {
		name := sanitizeDNS1123(alias)
		if name == "" {
			continue
		}
		if len(name) > dns1123LabelMaxLen {
			// Kubernetes rejects a Service name over 63 chars; emitting it would
			// fail the whole Engine.Apply. A name this long is not a resolvable
			// single DNS label either, so skip it (best-effort DNS) with a warning
			// rather than fail the deploy.
			log.Warn("skipping network alias too long for a Kubernetes Service name (max 63 chars); it will not resolve via DNS",
				"alias", alias, "sanitized", name, "workload", a.Spec.Name, "network", a.Net.Name)
			continue
		}
		out = append(out, Object{Typed: &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{deploy.LabelManaged: "true", netLabelPrefix + a.NetLabel: "true"},
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: corev1.ClusterIPNone,
				Selector:  a.Selector,
				Ports:     ports,
			},
		}})
	}
	return out, nil
}
