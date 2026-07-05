//go:build linux

package barehost

import (
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// TestPlanFoldsMountsAndEgressIntoOneCompanion covers the combination the server
// used to reject outright on this backend: a deploy declaring BOTH client-local
// mounts and client-side egress.
//
// The dispatch routes anything with egress through an AttachingBackend and only
// falls back to EgressBackend for an egress-ONLY deploy, so before bare
// implemented AttachingBackend this pairing failed with "client-side egress is
// not yet supported by the bare backend" even though each feature worked alone.
// Now both fold into one plan, which becomes one companion carrying both roles —
// and one process means the transparent redirect's netns-scoped nftables chain
// has no sibling companion to capture, since Mark is process-wide.
func TestPlanFoldsMountsAndEgressIntoOneCompanion(t *testing.T) {
	b, _ := newTestBackend(t)

	spec := api.DeploySpec{
		Name:   "web",
		Image:  "nginx:alpine",
		Mounts: []api.Mount{{Source: "/never/used", Target: "/data"}},
		Egress: &api.EgressSpec{Mode: "transparent"},
	}
	mounts := []deploy.AttachMount{{
		Target: "/data", Session: "sess-1", Name: "m0",
		RelayURL: "ws://cornus.host:5000", AgentImage: "cornus:latest",
	}}
	appSpec, appBinds, plans, err := b.planMounts(spec, mounts)
	if err != nil {
		t.Fatalf("planMounts: %v", err)
	}
	// The attach target is realized purely through propagation, never as a plain
	// host bind on the app container.
	if len(appSpec.Mounts) != 0 {
		t.Errorf("app spec mounts = %+v, want the attach target stripped", appSpec.Mounts)
	}
	if len(appBinds) != 1 || len(appBinds[0]) != 1 {
		t.Fatalf("app binds = %+v, want one propagation bind for one replica", appBinds)
	}

	egress := &deploy.AttachEgress{Session: "sess-1", RelayURL: "ws://cornus.host:5000", AgentImage: "cornus:latest", Spec: spec.Egress}
	_, ep, err := b.planEgress(appSpec, egress)
	if err != nil {
		t.Fatalf("planEgress: %v", err)
	}
	plans[0].egress = ep.egress
	plans[0].mark = ep.mark
	plans[0].caps = ep.caps

	plan := plans[0]
	if len(plan.roles) != 1 || plan.roles[0].Name != "m0" {
		t.Errorf("plan mount roles = %+v, want the m0 relay", plan.roles)
	}
	if plan.egress == nil || plan.egress.Mode != "transparent" || !plan.egress.SetupRedirect {
		t.Errorf("plan egress role = %+v, want transparent + SetupRedirect", plan.egress)
	}
	if plan.mark != egressMark {
		t.Errorf("plan Mark = %d, want %d (one process, one mark, every role exempted)", plan.mark, egressMark)
	}
	if len(plan.caps) != 1 || plan.caps[0] != "CAP_NET_ADMIN" {
		t.Errorf("plan caps = %v, want [CAP_NET_ADMIN]", plan.caps)
	}
	if !plan.hasRoles() {
		t.Error("hasRoles = false for a plan carrying both roles")
	}
}

// TestPlanEgressAloneNeedsNoPrivilege pins that folding did not widen the
// egress-only companion's privileges: only the caretaker's own kernel 9P mount
// syscall needs privilege, so a plan with no mount roles must not ask for it.
func TestPlanEgressAloneNeedsNoPrivilege(t *testing.T) {
	b, _ := newTestBackend(t)

	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine", Egress: &api.EgressSpec{Mode: "proxy"}}
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "ws://x", AgentImage: "cornus:latest", Spec: spec.Egress}
	app, plan, err := b.planEgress(spec, egress)
	if err != nil {
		t.Fatalf("planEgress: %v", err)
	}
	if len(plan.roles) != 0 {
		t.Errorf("plan mount roles = %+v, want none", plan.roles)
	}
	if plan.mark != 0 || len(plan.caps) != 0 {
		t.Errorf("proxy mode programs no redirect, so it needs no mark/caps: mark=%d caps=%v", plan.mark, plan.caps)
	}
	// Proxy mode is authoritative for the app's proxy env.
	if app.Env["HTTP_PROXY"] == "" {
		t.Errorf("app env = %v, want the proxy-mode HTTP_PROXY injected", app.Env)
	}
}

// TestApplyWithAttachmentsRejectsRuntimeCredentialDeliveries pins the credential
// kinds this backend still cannot realize. The message must name the KIND: since
// env delivery works, "credentials are not supported" is now false and would send
// an operator looking in the wrong place.
func TestApplyWithAttachmentsRejectsRuntimeCredentialDeliveries(t *testing.T) {
	b, _ := newTestBackend(t)

	creds := []deploy.AttachCredential{{
		Name: "aws", Session: "s", RelayURL: "ws://x",
		Deliveries: []api.CredentialDelivery{{Kind: "file", Path: "/creds/aws.json"}},
	}}
	_, err := b.ApplyWithAttachments(t.Context(), api.DeploySpec{Name: "web", Image: "nginx:alpine"}, nil, creds, nil)
	if err == nil {
		t.Fatal("a file credential delivery should be rejected on bare")
	}
	if !strings.Contains(err.Error(), "file") {
		t.Errorf("error = %v, want it to name the file delivery kind", err)
	}
}
