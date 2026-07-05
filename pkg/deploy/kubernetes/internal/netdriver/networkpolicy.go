package netdriver

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"cornus/pkg/deploy"
)

// networkPolicyProvider isolates a network with a Kubernetes NetworkPolicy: one
// shared policy per network selects the network's member pods (by the
// cornus.net/<netLabel> membership label the Engine stamps) and allows
// INGRESS only from other pods carrying the same label. Selecting a pod flips
// it to default-deny-ingress, so a pod on network A no longer accepts traffic
// from a pod on network B — real L3/L4 isolation on a FLAT pod network, no CNI
// attachment needed. Egress is untouched, so DNS (CoreDNS) and outbound calls
// keep working; a pod on multiple networks accepts ingress from any of them
// (additive allow).
//
// The policy is emitted UNCONDITIONALLY (it is a harmless no-op on a CNI that
// does not enforce NetworkPolicy, and the same manifest starts enforcing once
// the cluster runs Calico/Cilium). The Engine warns once per network when no
// enforcing CNI is detected, so the non-enforcement is legible rather than
// silent. Hence Requires() is empty — enforcement is advisory, not a hard gate.
type networkPolicyProvider struct{}

func (networkPolicyProvider) Name() string                                        { return "networkpolicy" }
func (networkPolicyProvider) Requires() []Capability                              { return nil }
func (networkPolicyProvider) WorkloadScoped(Attachment) ([]Object, error)         { return nil, nil }
func (networkPolicyProvider) MutatePod(Attachment, *corev1.PodTemplateSpec) error { return nil }

func (networkPolicyProvider) NetworkScoped(a Attachment) ([]Object, error) {
	member := map[string]string{netLabelPrefix + a.NetLabel: "true"}
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:   a.NetLabel,
			Labels: map[string]string{deploy.LabelManaged: "true", netLabelPrefix + a.NetLabel: "true"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: member},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{MatchLabels: member},
				}},
			}},
		},
	}
	return []Object{{Typed: np, Shared: true}}, nil
}
