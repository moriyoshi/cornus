package netdriver

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"cornus/pkg/deploy"
)

// ciliumProvider isolates a network with a native CiliumNetworkPolicy — the
// Cilium-enforced counterpart of the networkpolicy provider. One shared CNP per
// network selects the network's member pods (by the cornus.net/<netLabel>
// membership label the Engine stamps) via endpointSelector and allows INGRESS
// only from endpoints carrying the same label. Selecting an endpoint flips it to
// default-deny-ingress, so an endpoint on network A no longer accepts traffic
// from one on network B. Egress is untouched, so CoreDNS and outbound calls keep
// working; an endpoint on multiple networks accepts ingress from any of them
// (additive allow) — identical semantics to the plain NetworkPolicy provider,
// but expressed in Cilium's CRD so it enforces wherever Cilium runs.
//
// Unlike networkpolicy (emitted unconditionally, a no-op where unenforced), this
// is a real CRD that only exists on a Cilium cluster, so it Requires CapCilium
// and the Engine falls back to services-only where Cilium is absent.
type ciliumProvider struct{}

func (ciliumProvider) Name() string                                        { return "cilium" }
func (ciliumProvider) Requires() []Capability                              { return []Capability{CapCilium} }
func (ciliumProvider) WorkloadScoped(Attachment) ([]Object, error)         { return nil, nil }
func (ciliumProvider) MutatePod(Attachment, *corev1.PodTemplateSpec) error { return nil }

func (ciliumProvider) NetworkScoped(a Attachment) ([]Object, error) {
	member := map[string]any{netLabelPrefix + a.NetLabel: "true"}
	cnp := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2",
		"kind":       "CiliumNetworkPolicy",
		"metadata": map[string]any{
			"name":   a.NetLabel,
			"labels": map[string]any{deploy.LabelManaged: "true"},
		},
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": member},
			"ingress": []any{
				map[string]any{
					"fromEndpoints": []any{
						map[string]any{"matchLabels": member},
					},
				},
			},
		},
	}}
	return []Object{{Unstructured: cnp, GVR: cnpGVR, Shared: true}}, nil
}
