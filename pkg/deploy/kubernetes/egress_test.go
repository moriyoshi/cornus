package kubernetes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
)

// TestEgressProxyInjectsCaretakerAndEnv verifies the Mode 2 (proxy) pod shape: an
// unprivileged caretaker sidecar carrying the egress role, and the app container
// pointed at the loopback forward proxy via socks5h proxy env vars.
func TestEgressProxyInjectsCaretakerAndEnv(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		Egress: &api.EgressSpec{
			Mode:  "proxy",
			Rules: []api.EgressRule{{Pattern: "*.internal", Route: "cluster"}, {Pattern: "0.0.0.0/0", Route: "client"}},
		},
	}
	egress := &deploy.AttachEgress{
		Session:  "sess-abc",
		RelayURL: "wss://cornus.svc/.cornus/v1/caretaker/attach",
		Spec:     spec.Egress,
	}
	if _, err := b.ApplyWithAttachments(ctx, spec, nil, nil, egress); err != nil {
		t.Fatalf("ApplyWithAttachments: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	// Caretaker sidecar carries the egress role, unprivileged.
	var ctr *corev1.Container
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			ctr = &pod.InitContainers[i]
		}
	}
	if ctr == nil {
		t.Fatalf("no caretaker sidecar injected; inits=%+v", pod.InitContainers)
	}
	if ctr.SecurityContext != nil && ctr.SecurityContext.Privileged != nil && *ctr.SecurityContext.Privileged {
		t.Error("egress proxy caretaker must not be privileged")
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.Egress == nil {
		t.Fatalf("caretaker config has no egress role: %+v", cfg)
	}
	if cfg.Egress.Session != "sess-abc" || cfg.Egress.Server != "wss://cornus.svc/.cornus/v1/caretaker/attach" {
		t.Errorf("egress role session/server = %q/%q", cfg.Egress.Session, cfg.Egress.Server)
	}
	if cfg.Egress.Mode != "proxy" || cfg.Egress.ListenPort != 15002 || len(cfg.Egress.Rules) != 2 {
		t.Errorf("egress role = %+v", cfg.Egress)
	}

	// App container: HTTP(S)_PROXY use the http:// scheme (full HTTP proxy), ALL_PROXY
	// uses socks5h:// (SOCKS catch-all with remote DNS).
	env := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		env[e.Name] = e.Value
	}
	httpWant := "http://127.0.0.1:15002"
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		if env[k] != httpWant {
			t.Errorf("app env %s = %q, want %q", k, env[k], httpWant)
		}
	}
	socksWant := "socks5h://127.0.0.1:15002"
	for _, k := range []string{"ALL_PROXY", "all_proxy"} {
		if env[k] != socksWant {
			t.Errorf("app env %s = %q, want %q", k, env[k], socksWant)
		}
	}
	if !strings.Contains(env["NO_PROXY"], "127.0.0.1") || !strings.Contains(env["NO_PROXY"], "*.internal") {
		t.Errorf("NO_PROXY = %q, want loopback + cluster-routed *.internal", env["NO_PROXY"])
	}
}

// TestEgressProxyRejectedWithProxyNetwork confirms egress and the enforcing proxy
// cannot share a pod (both intercept egress).
func TestEgressProxyRejectedWithProxyNetwork(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	spec := api.DeploySpec{
		Name:   "proj-web",
		Image:  "img",
		Egress: &api.EgressSpec{Mode: "proxy"},
		Proxy:  &api.ProxySpec{Allow: []string{"api"}},
	}
	egress := &deploy.AttachEgress{Session: "s", RelayURL: "wss://x", Spec: spec.Egress}
	if _, err := b.ApplyWithAttachments(context.Background(), spec, nil, nil, egress); err == nil {
		t.Fatal("expected egress+proxy to be rejected")
	}
}

// TestEgressRelayRejectedWithDetachedNetwork confirms a relay-mode egress caretaker
// (proxy/transparent) cannot share a pod with a default (detached) network: the
// caretaker needs the cluster network to reach the egress relay, exactly like the
// mount agent. Env mode needs no relay and stays allowed on a detached pod.
func TestEgressRelayRejectedWithDetachedNetwork(t *testing.T) {
	detached := []api.NetworkAttachment{{Name: "proj_main", Driver: "bridge", Default: true}}

	for _, mode := range []string{"proxy", "transparent"} {
		b := NewWithClient(fake.NewSimpleClientset(), "default")
		spec := api.DeploySpec{Name: "app", Image: "img", Networks: detached, Egress: &api.EgressSpec{Mode: mode}}
		egress := &deploy.AttachEgress{Session: "s", RelayURL: "wss://x", Spec: spec.Egress}
		if _, err := b.ApplyWithAttachments(context.Background(), spec, nil, nil, egress); err == nil {
			t.Fatalf("mode %q: detached network + relay egress must be rejected", mode)
		}
	}

	// Env mode: no relay, so a detached pod is fine (nothing to reject here — the env
	// vars are injected by the CLIENT, not by this backend, so egress is nil).
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{Name: "app", Image: "img", Networks: detached, Egress: &api.EgressSpec{Mode: "env"}}
	if _, err := b.ApplyWithAttachments(context.Background(), spec, nil, nil, nil); err != nil {
		t.Fatalf("env-mode egress on a detached pod must be allowed: %v", err)
	}
}

// TestEgressTransparentInjectsRedirectAndUID verifies the Mode 3 (transparent)
// egress-only pod shape: a NET_ADMIN net-redirect init capturing app egress into the
// caretaker port, the caretaker running as the exempt uid (no privilege, no proxy
// env), and its config carrying the transparent egress role.
func TestEgressTransparentInjectsRedirectAndUID(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "proj-web",
		Image:  "img",
		Egress: &api.EgressSpec{Mode: "transparent", Rules: []api.EgressRule{{Pattern: "*", Route: "client"}}},
	}
	egress := &deploy.AttachEgress{Session: "s1", RelayURL: "wss://cornus.svc", Spec: spec.Egress}
	if _, err := b.ApplyWithAttachments(ctx, spec, nil, nil, egress); err != nil {
		t.Fatalf("ApplyWithAttachments: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	var redirect, ctr *corev1.Container
	for i := range pod.InitContainers {
		switch pod.InitContainers[i].Name {
		case "cornus-net-redirect":
			redirect = &pod.InitContainers[i]
		case "cornus-caretaker":
			ctr = &pod.InitContainers[i]
		}
	}
	if redirect == nil || ctr == nil {
		t.Fatalf("want a net-redirect init and a caretaker; got %+v", pod.InitContainers)
	}
	if strings.Join(redirect.Args, " ") != "net-redirect --to-port 15002 --exempt-uid 1337" {
		t.Errorf("net-redirect args = %v", redirect.Args)
	}
	if redirect.SecurityContext == nil || redirect.SecurityContext.Capabilities == nil ||
		len(redirect.SecurityContext.Capabilities.Add) != 1 || redirect.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" {
		t.Errorf("net-redirect must add NET_ADMIN, got %+v", redirect.SecurityContext)
	}
	if ctr.SecurityContext == nil || ctr.SecurityContext.RunAsUser == nil || *ctr.SecurityContext.RunAsUser != 1337 {
		t.Errorf("transparent egress caretaker must run as uid 1337, got %+v", ctr.SecurityContext)
	}
	if ctr.SecurityContext.Privileged != nil && *ctr.SecurityContext.Privileged {
		t.Error("transparent egress-only caretaker must not be privileged")
	}
	// No proxy env is injected in transparent mode.
	for _, e := range pod.Containers[0].Env {
		if e.Name == "HTTP_PROXY" || e.Name == "ALL_PROXY" {
			t.Errorf("transparent mode must not inject proxy env, found %s", e.Name)
		}
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.Egress == nil || cfg.Egress.Mode != "transparent" || cfg.Egress.ListenPort != 15002 {
		t.Errorf("egress role = %+v, want transparent on 15002", cfg.Egress)
	}
	if cfg.Mark != 0 {
		t.Errorf("egress-only transparent must exempt by uid, not mark; Mark=%d", cfg.Mark)
	}
}

// TestEgressTransparentWithMountsUsesMark verifies that a pod with BOTH client-local
// mounts and transparent egress carries ONE privileged (root) caretaker exempted
// from the redirect by a firewall mark (uid exemption is impossible when the
// caretaker must be root for the mount).
func TestEgressTransparentWithMountsUsesMark(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "proj-web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/src", Target: "/data"}},
		Egress: &api.EgressSpec{Mode: "transparent"},
	}
	mounts := []deploy.AttachMount{{Target: "/data", Session: "s1", Name: "data", RelayURL: "wss://cornus.svc"}}
	egress := &deploy.AttachEgress{Session: "s1", RelayURL: "wss://cornus.svc", Spec: spec.Egress}
	if _, err := b.ApplyWithAttachments(ctx, spec, mounts, nil, egress); err != nil {
		t.Fatalf("ApplyWithAttachments: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	pod := dep.Spec.Template.Spec

	var redirect, ctr *corev1.Container
	for i := range pod.InitContainers {
		switch pod.InitContainers[i].Name {
		case "cornus-net-redirect":
			redirect = &pod.InitContainers[i]
		case "cornus-caretaker":
			ctr = &pod.InitContainers[i]
		}
	}
	if redirect == nil || ctr == nil {
		t.Fatalf("want redirect + caretaker; got %+v", pod.InitContainers)
	}
	if strings.Join(redirect.Args, " ") != "net-redirect --to-port 15002 --exempt-mark 1337" {
		t.Errorf("net-redirect args = %v, want mark exemption", redirect.Args)
	}
	if ctr.SecurityContext == nil || ctr.SecurityContext.Privileged == nil || !*ctr.SecurityContext.Privileged {
		t.Errorf("mounts+egress caretaker must be privileged, got %+v", ctr.SecurityContext)
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.Mark != 1337 {
		t.Errorf("caretaker Mark = %d, want 1337 (redirect exemption)", cfg.Mark)
	}
	if cfg.Egress == nil || len(cfg.Mounts) != 1 {
		t.Errorf("want one caretaker carrying both egress and the mount; egress=%v mounts=%d", cfg.Egress, len(cfg.Mounts))
	}
}
