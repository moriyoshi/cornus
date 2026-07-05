package kubernetes

import (
	"context"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"cornus/pkg/deploy"
)

// permissionCheck is one (verb, resource) grant the kubernetes backend needs,
// with the human-facing consequence of it missing. subresource is "" for a plain
// resource. feature is "" for a core capability (a missing one breaks a fundamental
// deploy path) or the name of the optional feature the grant gates.
type permissionCheck struct {
	group       string
	resource    string
	subresource string
	verb        string
	feature     string
	impact      string
}

// preflightChecks is the set the backend self-verifies at startup. It mirrors the
// production RBAC Role (deploy/k8s/cornus.yaml, deploy/helm/.../rbac.yaml): one
// representative verb per resource the deploy paths use — create is the most
// telling, since a backend that cannot create a resource cannot realize the feature
// that needs it. The two feature-gated grants (ingresses, secrets) are the ones a
// default install legitimately omits (only native ingress / inline TLS need them),
// so they are reported as feature gaps, not core breakage.
var preflightChecks = []permissionCheck{
	{group: "apps", resource: "deployments", verb: "create", impact: "long-lived services cannot be deployed"},
	{group: "batch", resource: "jobs", verb: "create", impact: "one-shot / init services (restart: no) cannot be deployed"},
	{resource: "services", verb: "create", impact: "services that publish a port cannot be deployed"},
	{resource: "pods", verb: "list", impact: "deploy status, readiness waits, and reconcile cannot read pods"},
	{resource: "persistentvolumeclaims", verb: "create", impact: "services with a managed volume cannot be deployed"},
	{resource: "pods", subresource: "exec", verb: "create", impact: "cornus exec / interactive attach is unavailable"},
	{resource: "pods", subresource: "log", verb: "get", impact: "compose logs is unavailable"},
	{
		group: "networking.k8s.io", resource: "ingresses", verb: "create",
		feature: "native ingress",
		impact:  "a workload with a real x-cornus-ingress fails at deploy; NOT needed for --ingress-conduit=emulate (client-side). Grant via ingress.manageIngresses (Helm).",
	},
	{
		resource: "secrets", verb: "create",
		feature: "inline ingress TLS certificates",
		impact:  "a workload supplying inline (managed) ingress TLS certs fails at deploy. Grant via ingress.manageTLSSecrets (Helm).",
	},
}

// Preflight implements deploy.Preflighter: it runs a SelfSubjectAccessReview for
// each grant in preflightChecks against the backend's target namespace and reports
// the ones denied. SelfSubjectAccessReview needs no special RBAC (the default
// system:basic-user ClusterRole grants it to every authenticated identity); if even
// that self-check cannot run, preflight gives up quietly (returns what it has so
// far) rather than erroring — a diagnostic must never block serving.
func (b *Backend) Preflight(ctx context.Context) []deploy.PermissionGap {
	var gaps []deploy.PermissionGap
	for _, c := range preflightChecks {
		allowed, err := b.canI(ctx, c)
		if err != nil {
			return gaps // self-check unavailable; stop probing, keep what we found
		}
		if !allowed {
			gaps = append(gaps, deploy.PermissionGap{
				Verb:     c.verb,
				Resource: qualifiedResource(c),
				Feature:  c.feature,
				Impact:   c.impact,
			})
		}
	}
	return gaps
}

// canI runs one namespaced SelfSubjectAccessReview.
func (b *Backend) canI(ctx context.Context, c permissionCheck) (bool, error) {
	ssar := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace:   b.namespace,
				Group:       c.group,
				Resource:    c.resource,
				Subresource: c.subresource,
				Verb:        c.verb,
			},
		},
	}
	res, err := b.clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return res.Status.Allowed, nil
}

// qualifiedResource renders a check's resource for logs: "<resource>[/<sub>]" for
// the core group, "<resource>.<group>" otherwise, so an operator can match it
// straight to an RBAC rule.
func qualifiedResource(c permissionCheck) string {
	r := c.resource
	if c.subresource != "" {
		r += "/" + c.subresource
	}
	if c.group != "" {
		r += "." + c.group
	}
	return r
}
