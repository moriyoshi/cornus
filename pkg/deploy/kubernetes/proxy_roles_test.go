package kubernetes

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
)

// TestProxyFoldsAgentForwardIntoOneCaretaker pins that a pod asking for both a
// proxy and agent forwarding gets exactly ONE container named cornus-caretaker,
// carrying both roles.
//
// The regression: `if spec.Proxy != nil { injectProxy(...) }` was a standalone
// statement followed by an independent if/else-if chain whose AgentForward arm
// had no proxy guard, and neither proxy injector folded the AgentRelay role in.
// Both therefore ran and appended a container named cornus-caretaker each. A pod
// may not have two containers with one name, so the API server rejected the whole
// deploy with "spec.containers[N].name: Duplicate value" — a message about
// container naming, for a user who had asked for a proxy and agent forwarding.
//
// Proxy+DNS and Proxy+Hub are rejected at Apply, and Proxy+Docker was too when
// this was written, so AgentForward was the one combination that reached the
// injectors. Proxy+Docker now reaches them in cooperative mode — see
// TestCooperativeProxyWithDockerIsAcceptedAndCarriesTheRole, which asserts the
// same one-container invariant this test does.
func TestProxyFoldsAgentForwardIntoOneCaretaker(t *testing.T) {
	for _, mode := range []string{"", "cooperative"} {
		name := "enforcing"
		if mode != "" {
			name = mode
		}
		t.Run(name, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			b := NewWithClient(cs, "default")
			ctx := context.Background()

			spec := api.DeploySpec{
				Name:         "proj-web",
				Image:        "img",
				Proxy:        &api.ProxySpec{Mode: mode, Allow: []string{"api", "db"}},
				AgentForward: true,
			}
			if _, err := b.Apply(ctx, spec); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get deployment: %v", err)
			}
			ctrs, cfg := caretakerContainers(t, dep.Spec.Template.Spec)
			if len(ctrs) != 1 {
				t.Fatalf("got %d containers named cornus-caretaker, want exactly 1: a pod may not have two containers with the same name, so the API server rejects the whole deploy", len(ctrs))
			}
			if cfg.AgentRelay == nil {
				t.Error("the agent-relay role was dropped: `compose exec --forward-agent` would find no relay in the pod")
			}
			// Only the enforcing proxy always yields a proxy role. Cooperative mode
			// builds one per peer that DECLARES PORTS, and these peers are not
			// deployed in this fixture — so an absent proxy role there is correct,
			// and the caretaker exists to carry agent-forward alone.
			if mode == "" && cfg.Proxy == nil {
				t.Error("the proxy role was dropped from the caretaker that absorbed agent-forward")
			}
		})
	}
}

// TestEnforcingProxyWithDockerIsRejectedAtApply pins the guard that keeps the
// real role-collision out of the injectors. The ENFORCING proxy redirects ALL of
// the app's outbound TCP into the sidecar (netRedirectInit), which would capture
// the Docker endpoint's own client dials, so the combination is refused at the
// API rather than deployed into a pod that cannot work.
//
// Both spellings of enforcing are exercised: the mode word is optional and empty
// means enforcing, so a guard that only matched the literal "enforcing" would let
// the default — the dangerous one — straight through.
func TestEnforcingProxyWithDockerIsRejectedAtApply(t *testing.T) {
	for _, mode := range []string{"", "enforcing", "ENFORCING"} {
		t.Run("mode="+mode, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			b := NewWithClient(cs, "default")
			spec := api.DeploySpec{
				Name:   "proj-web",
				Image:  "img",
				Proxy:  &api.ProxySpec{Mode: mode, Allow: []string{"api"}},
				Docker: &api.DockerSpec{},
			}
			_, err := b.Apply(context.Background(), spec)
			if err == nil {
				t.Fatalf("mode %q: Apply accepted the enforcing proxy alongside the docker endpoint; it must be refused at the API, not left to fail as unreachable dials inside the pod", mode)
			}
			if !strings.Contains(err.Error(), "Docker endpoint role") {
				t.Errorf("mode %q: error does not name the conflicting role: %v", mode, err)
			}
		})
	}
}

// TestCooperativeProxyWithDockerIsAcceptedAndCarriesTheRole is the other half,
// and the reason the guard above is written against Enforcing() rather than
// `Proxy != nil`. The guard used to reject EVERY proxy mode while its message
// blamed the redirect, so a cooperative-mode user was refused for a cause that
// did not apply to their configuration.
//
// Cooperative cannot collide with the endpoint, and this is settled by the code
// rather than by a live cluster: it appends no netRedirectInit, its listeners
// start at 127.0.1.1 by construction (loopbackFor: "so it never collides with
// 127.0.0.1") while the endpoint binds 127.0.0.1, and it intercepts by hostAlias
// on a peer's DNS NAME, which the literal DOCKER_HOST=tcp://127.0.0.1:port never
// consults.
//
// Asserting only that Apply stops returning an error would be the weak version of
// this test and would pass if the endpoint were accepted and then dropped on the
// floor — which is exactly what the old TestProxyWithDockerIsRejectedAtApply
// warned about when it said the fold "is what must carry the docker role". So the
// caretaker config is checked for the role, in the single caretaker container.
func TestCooperativeProxyWithDockerIsAcceptedAndCarriesTheRole(t *testing.T) {
	for _, mode := range []string{"cooperative", "Cooperative"} {
		t.Run("mode="+mode, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			b := NewWithClient(cs, "default")
			ctx := context.Background()
			spec := api.DeploySpec{
				Name:   "proj-web",
				Image:  "img",
				Proxy:  &api.ProxySpec{Mode: mode, Allow: []string{"api"}},
				Docker: &api.DockerSpec{},
			}
			if _, err := b.Apply(ctx, spec); err != nil {
				t.Fatalf("mode %q: Apply rejected the cooperative proxy alongside the docker endpoint: %v", mode, err)
			}
			dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get deployment: %v", err)
			}
			ctrs, cfg := caretakerContainers(t, dep.Spec.Template.Spec)
			if len(ctrs) != 1 {
				t.Fatalf("got %d containers named cornus-caretaker, want exactly 1: a pod may not have two containers with one name, so the API server would reject the whole deploy", len(ctrs))
			}
			if cfg.Docker == nil {
				t.Error("the docker endpoint role was dropped: the deploy succeeds, the pod runs, and the app container's DOCKER_HOST points at a port nothing is listening on")
			}
		})
	}
}
