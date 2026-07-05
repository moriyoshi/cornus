package kubernetes

import (
	"context"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// ssarReactor installs a fake SelfSubjectAccessReview handler that allows every
// (group,resource,subresource,verb) EXCEPT those in deny (keyed by
// qualifiedResource+":"+verb), so a test can carve out specific missing grants.
func ssarReactor(cs *fake.Clientset, deny map[string]bool) {
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ssar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		ra := ssar.Spec.ResourceAttributes
		c := permissionCheck{group: ra.Group, resource: ra.Resource, subresource: ra.Subresource, verb: ra.Verb}
		key := qualifiedResource(c) + ":" + ra.Verb
		ssar.Status.Allowed = !deny[key]
		return true, ssar, nil
	})
}

// TestPreflightAllAllowed: a fully-permitted backend reports no gaps.
func TestPreflightAllAllowed(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ssarReactor(cs, nil)
	b := NewWithClient(cs, "default")
	if gaps := b.Preflight(context.Background()); len(gaps) != 0 {
		t.Fatalf("want no gaps when everything is allowed, got %+v", gaps)
	}
}

// TestPreflightReportsMissingIngress: the exact production gap this session hit — no
// ingresses grant — is reported as a FEATURE gap (native ingress), not core breakage.
func TestPreflightReportsMissingIngress(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ssarReactor(cs, map[string]bool{"ingresses.networking.k8s.io:create": true})
	b := NewWithClient(cs, "default")

	gaps := b.Preflight(context.Background())
	if len(gaps) != 1 {
		t.Fatalf("want exactly the ingresses gap, got %+v", gaps)
	}
	g := gaps[0]
	if g.Resource != "ingresses.networking.k8s.io" || g.Verb != "create" {
		t.Fatalf("wrong gap identity: %+v", g)
	}
	if g.Feature == "" {
		t.Fatalf("a missing ingresses grant must be a FEATURE gap, not core: %+v", g)
	}
}

// TestPreflightReportsMissingCore: a missing core grant (deployments) is reported
// with an empty Feature, so the server logs it as a required-permission WARN.
func TestPreflightReportsMissingCore(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ssarReactor(cs, map[string]bool{"deployments.apps:create": true})
	b := NewWithClient(cs, "default")

	gaps := b.Preflight(context.Background())
	if len(gaps) != 1 || gaps[0].Resource != "deployments.apps" {
		t.Fatalf("want the deployments gap, got %+v", gaps)
	}
	if gaps[0].Feature != "" {
		t.Fatalf("a missing core grant must have empty Feature (core), got %q", gaps[0].Feature)
	}
}
