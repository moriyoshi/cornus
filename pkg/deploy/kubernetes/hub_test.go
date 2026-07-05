package kubernetes

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/hub"
)

// TestHubDiscovery checks the discovery builder: exports register dial-direct
// (cluster Service address) or for delivery (localhost target); imports get a
// synthetic loopback IP that the DNS record and the Reach listener AGREE on — the
// invariant that makes an app's dial of a peer name funnel through the hub.
func TestHubDiscovery(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	b := &Backend{namespace: "ns"}

	spec := api.DeploySpec{
		Name: "web",
		Hub: &api.HubSpec{
			Export: []api.HubExport{
				{Name: "web", Port: 8080},                     // dial-direct
				{Name: "web-priv", Port: 9090, Deliver: true}, // delivery
			},
			Import: []api.HubImport{{Name: "db", Ports: []int{5432}}},
		},
	}

	role, records := b.hubDiscovery(spec)

	if role.Identity != "web" {
		t.Errorf("identity default = %q, want deployment name web", role.Identity)
	}
	if role.Server != "ws://cornus:5000" {
		t.Errorf("server = %q, want the advertise URL", role.Server)
	}

	reg := map[string]caretaker.HubService{}
	for _, s := range role.Register {
		reg[s.Name] = s
	}
	if got := reg["web"].Addr; got != "web.ns.svc.cluster.local:8080" || reg["web"].Target != "" {
		t.Errorf("dial-direct export = %+v, want Addr=web.ns.svc.cluster.local:8080", reg["web"])
	}
	if got := reg["web-priv"].Target; got != "127.0.0.1:9090" || reg["web-priv"].Addr != "" {
		t.Errorf("delivery export = %+v, want Target=127.0.0.1:9090", reg["web-priv"])
	}

	// The DNS record and the Reach listener for an import MUST be the same IP.
	ip := records["db"]
	if ip != hub.SyntheticIP("db") {
		t.Errorf("record ip = %q, want SyntheticIP(db) = %q", ip, hub.SyntheticIP("db"))
	}
	if len(role.Reach) != 1 || role.Reach[0].Name != "db" || role.Reach[0].Listen != ip {
		t.Fatalf("reach = %+v, want one peer db listening on %s (matching the DNS record)", role.Reach, ip)
	}
	if len(role.Reach[0].Ports) != 1 || role.Reach[0].Ports[0] != 5432 {
		t.Errorf("reach ports = %v, want [5432]", role.Reach[0].Ports)
	}
}

// TestHubWithMountsSingleCaretaker confirms the unified sidecar: a pod with both
// client-local mounts and a hub role gets exactly ONE (privileged) caretaker
// carrying BOTH the mount roles and the hub role, plus a DNS role whose synthetic-
// IP records match the hub Reach listeners.
func TestHubWithMountsSingleCaretaker(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "proj-web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/c/a", Target: "/a"}},
		Hub: &api.HubSpec{
			Export: []api.HubExport{{Name: "web", Port: 8080}},
			Import: []api.HubImport{{Name: "db", Ports: []int{5432}}},
		},
	}
	attach := []deploy.AttachMount{{Target: "/a", Session: "s", Name: "ma", RelayURL: "ws://relay"}}
	if _, err := b.ApplyWithMounts(ctx, spec, attach); err != nil {
		t.Fatalf("ApplyWithMounts: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	var ctr *corev1.Container
	caretakers := 0
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			ctr = &pod.InitContainers[i]
			caretakers++
		}
	}
	if caretakers != 1 {
		t.Fatalf("caretakers = %d, want exactly 1 for hub+mounts", caretakers)
	}
	if ctr.SecurityContext == nil || ctr.SecurityContext.Privileged == nil || !*ctr.SecurityContext.Privileged {
		t.Errorf("caretaker must be privileged for mounts, got %+v", ctr.SecurityContext)
	}

	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Name != "ma" {
		t.Errorf("caretaker mounts = %+v, want the one mount role ma", cfg.Mounts)
	}
	if cfg.Hub == nil || len(cfg.Hub.Reach) != 1 || cfg.Hub.Reach[0].Name != "db" {
		t.Fatalf("caretaker hub = %+v, want a reach for db", cfg.Hub)
	}
	// The DNS record for the imported peer must equal its Reach listener IP.
	if cfg.DNS == nil || cfg.DNS.Records["db"] != hub.SyntheticIP("db") {
		t.Errorf("dns record db = %q, want SyntheticIP(db) = %q", cfg.DNS.Records["db"], hub.SyntheticIP("db"))
	}
	if cfg.Hub.Reach[0].Listen != hub.SyntheticIP("db") {
		t.Errorf("reach listen = %q, want %q (matching the DNS record)", cfg.Hub.Reach[0].Listen, hub.SyntheticIP("db"))
	}
}

// TestHubImportDynamic checks that a spec's hub ImportDynamic block propagates
// into the embedded caretaker config as HubRole.ReachDynamic (catalog-push-driven
// dynamic import listeners), alongside — not instead of — the static imports, and
// that a spec without the block yields no reachDynamic key at all.
func TestHubImportDynamic(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "img",
		Hub: &api.HubSpec{
			Import:        []api.HubImport{{Name: "db", Ports: []int{5432}}},
			ImportDynamic: &api.HubImportDynamic{Ports: []int{80, 443}, Protocol: "udp"},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	var cfg caretaker.Config
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name != "cornus-caretaker" {
			continue
		}
		for _, e := range pod.InitContainers[i].Env {
			if e.Name == "CORNUS_CARETAKER_CONFIG" {
				if err := json.Unmarshal([]byte(e.Value), &cfg); err != nil {
					t.Fatalf("unmarshal caretaker config: %v", err)
				}
			}
		}
	}
	if cfg.Hub == nil {
		t.Fatal("no hub role in the caretaker config")
	}
	rd := cfg.Hub.ReachDynamic
	if rd == nil || len(rd.Ports) != 2 || rd.Ports[0] != 80 || rd.Ports[1] != 443 || rd.Protocol != "udp" {
		t.Fatalf("reachDynamic = %+v, want ports [80 443] protocol udp", rd)
	}
	// Static imports still ride alongside.
	if len(cfg.Hub.Reach) != 1 || cfg.Hub.Reach[0].Name != "db" {
		t.Fatalf("reach = %+v, want the static db import preserved", cfg.Hub.Reach)
	}

	// Without the block the role carries no ReachDynamic (and the JSON no key —
	// the field is omitempty, keeping old-server configs unchanged).
	role, _ := b.hubDiscovery(api.DeploySpec{
		Name: "web",
		Hub:  &api.HubSpec{Import: []api.HubImport{{Name: "db", Ports: []int{5432}}}},
	})
	if role.ReachDynamic != nil {
		t.Fatalf("reachDynamic = %+v, want nil without an ImportDynamic block", role.ReachDynamic)
	}
}

// TestHubProxyRejected confirms the hub role and enforcing proxy cannot share a pod.
func TestHubProxyRejected(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		Hub:   &api.HubSpec{Import: []api.HubImport{{Name: "db", Ports: []int{5432}}}},
		Proxy: &api.ProxySpec{Allow: []string{"db"}},
	}
	if _, err := b.Apply(context.Background(), spec); err == nil {
		t.Error("hub + proxy on one pod must be rejected")
	}
}
