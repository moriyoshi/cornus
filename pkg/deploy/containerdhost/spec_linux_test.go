//go:build linux

package containerdhost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/containerd/containerd/runtime/restart"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/internal/hostrun"
)

// The OCI spec-opt tests (envList/ociBindMount/runtimeOpts) moved with their
// functions to cornus/pkg/deploy/internal/hostrun. What remains here is
// containerd-specific: the restart-monitor label assembly (containerLabels) and
// the insecure-registry parse.

func TestContainerLabels(t *testing.T) {
	spec := api.DeploySpec{
		Name: "web",
		Networks: []api.NetworkAttachment{
			{Name: "front", Aliases: []string{"web-alias"}},
			{Name: "back"},
		},
	}
	att := hostrun.Attachment{
		Netns: "/run/cornus/netns/web-0",
		IP:    "10.4.1.5",
		IPs:   map[string]string{"front": "10.4.1.5", "back": "10.4.2.5"},
	}
	l, err := containerLabels(spec, att, nil, "binary:///usr/bin/cornus?id=x")
	if err != nil {
		t.Fatalf("containerLabels: %v", err)
	}
	if l[deploy.LabelManaged] != "true" || l[deploy.LabelApp] != "web" {
		t.Fatalf("ownership labels missing: %v", l)
	}
	if l[labelNetworks] != "front,back" {
		t.Fatalf("networks label = %q", l[labelNetworks])
	}
	if l[labelNetNS] != "/run/cornus/netns/web-0" {
		t.Fatalf("netns label = %q", l[labelNetNS])
	}
	if l[labelIP] != "10.4.1.5" {
		t.Fatalf("ip label = %q", l[labelIP])
	}
	var ips map[string]string
	if err := json.Unmarshal([]byte(l[labelNetIPs]), &ips); err != nil || ips["back"] != "10.4.2.5" {
		t.Fatalf("net-IPs label = %q (%v)", l[labelNetIPs], err)
	}
	var aliases map[string][]string
	if err := json.Unmarshal([]byte(l[labelAliases]), &aliases); err != nil ||
		len(aliases["front"]) != 1 || aliases["front"][0] != "web-alias" {
		t.Fatalf("aliases label = %q (%v)", l[labelAliases], err)
	}
	// Default restart policy is unless-stopped -> monitor labels present.
	if l[restart.PolicyLabel] != "unless-stopped" || l[restart.StatusLabel] != "running" {
		t.Fatalf("restart labels = %v", l)
	}
	if !strings.HasPrefix(l[restart.LogURILabel], "binary://") {
		t.Fatalf("log uri label = %q", l[restart.LogURILabel])
	}
}

func TestContainerLabelsNoRestart(t *testing.T) {
	l, err := containerLabels(api.DeploySpec{Name: "web", Restart: "no"}, hostrun.Attachment{}, nil, "")
	if err != nil {
		t.Fatalf("containerLabels: %v", err)
	}
	if _, ok := l[restart.PolicyLabel]; ok {
		t.Fatal("restart policy 'no' must not set monitor labels")
	}
}

func TestContainerLabelsInvalidPolicy(t *testing.T) {
	if _, err := containerLabels(api.DeploySpec{Name: "web", Restart: "sometimes"}, hostrun.Attachment{}, nil, ""); err == nil {
		t.Fatal("invalid restart policy should error")
	}
}

func TestParseInsecureRegistries(t *testing.T) {
	got := parseInsecureRegistries(" reg.example.com:5000 , other.local ,")
	if !got["reg.example.com:5000"] || !got["other.local"] || len(got) != 2 {
		t.Fatalf("parsed = %v", got)
	}
}
