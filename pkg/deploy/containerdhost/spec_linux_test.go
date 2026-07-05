//go:build linux

package containerdhost

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test, so tests can assert on the backend's per-field warnings.
// logging.FromContext (which apply and warnUnsupported use) derives from
// slog.Default, so replacing it captures everything the prelude emits.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func boolPtr(b bool) *bool { return &b }

// unsupportedFieldCases enumerates, one entry per api.DeploySpec field this
// backend cannot honor, a spec that sets ONLY that field and the warning it must
// produce. It is the machine-checkable half of this backend's parity story: a
// field that is neither mapped nor listed here is a field that gets accepted and
// then dropped in total silence, which is invisible to every other gate — the
// build passes, the tests pass, the deploy succeeds, and the workload is not
// what the operator asked for.
//
// Adding a field to api.DeploySpec therefore means doing one of two things here:
// mapping it (and adding it to supportedSpec, which asserts this backend stays
// silent about it) or adding a row. TestEveryDeploySpecFieldIsMappedOrWarned
// enforces that by reflection, so the choice cannot be skipped.
//
// Note what is deliberately ABSENT: Mounts, Volumes, Networks, Sysctls, Ulimits,
// Tmpfs, Devices, ShmSize, PIDMode, IPCMode, SecurityOpt and GroupAdd are mapped
// by pkg/deploy/internal/hostrun (the OCI spec / network / volume machinery both
// host backends share), and Telemetry/Credentials/Egress/Mounts are realized
// outside this package (deploy.BuildTelemetryWiring and the pkg/server attachment
// entry points). None of those is a silent drop, so none of them warns.
var unsupportedFieldCases = []struct {
	field string
	spec  api.DeploySpec // merged onto a minimal name+image spec
	want  string         // substring the warning must contain
}{
	{"StopSignal", api.DeploySpec{StopSignal: "SIGINT"}, "ignores stopSignal"},
	{"StopGracePeriod", api.DeploySpec{StopGracePeriod: "30s"}, "ignores stopGracePeriod"},
	{"Init", api.DeploySpec{Init: boolPtr(true)}, "ignores init"},
	{"StdinOpen", api.DeploySpec{StdinOpen: true}, "ignores stdinOpen"},
	{"ExtraHosts", api.DeploySpec{ExtraHosts: []string{"db:10.0.0.5"}}, "ignores extraHosts"},
	{"DNSServers", api.DeploySpec{DNSServers: []string{"1.1.1.1"}}, "ignores dnsServers"},
	{"DNSSearch", api.DeploySpec{DNSSearch: []string{"corp.internal"}}, "ignores dnsSearch"},
	{"DNSOptions", api.DeploySpec{DNSOptions: []string{"ndots:2"}}, "ignores dnsOptions"},
	{"RestartMaxAttempts", api.DeploySpec{Restart: "on-failure", RestartMaxAttempts: 3}, "ignores restartMaxAttempts"},
	{"Resources.ReservedCPU", api.DeploySpec{Resources: &api.Resources{ReservedCPU: 0.5}}, "ignores resource reservations"},
	{"Resources.ReservedMemory", api.DeploySpec{Resources: &api.Resources{ReservedMemory: 1 << 20}}, "ignores resource reservations"},
	{"Healthcheck", api.DeploySpec{Healthcheck: &api.Healthcheck{Test: []string{"CMD", "true"}}}, "ignores healthcheck"},
	{"Ingress", api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true}}, "creates no cluster Ingress"},
	{"Knative", api.DeploySpec{Knative: &api.KnativeSpec{Enabled: true}}, "ignores knative"},
	// Kubernetes-only fields. The warnings come from the SHARED
	// deploy.WarnKubernetesOnlyFields, which every host backend calls; the rows
	// are here because this backend still has to prove it calls it.
	{"Proxy", api.DeploySpec{Proxy: &api.ProxySpec{}}, "ignores proxy"},
	{"DNS", api.DeploySpec{DNS: &api.DNSSpec{}}, "ignores dns records"},
	{"Hub", api.DeploySpec{Hub: &api.HubSpec{}}, "ignores hub"},
	{"Docker", api.DeploySpec{Docker: &api.DockerSpec{}}, "ignores docker"},
	{"UpdateConfig", api.DeploySpec{UpdateConfig: &api.UpdateConfig{Parallelism: 2}}, "ignores updateConfig"},
	{"AgentForward", api.DeploySpec{AgentForward: true}, "ignores agentForward"},
	// Per-network refusals inside a field that IS otherwise mapped: the network
	// NAME and its aliases are realized, these knobs of it are not.
	{"Networks (driver)", api.DeploySpec{Networks: []api.NetworkAttachment{{Name: "back", Driver: "macvlan"}}}, "unsupported network features"},
	{"Networks (static ip)", api.DeploySpec{Networks: []api.NetworkAttachment{{Name: "back", IP: "10.9.0.4"}}}, "unsupported network features"},
}

// TestApplyWarnsForEverySpecFieldItCannotHonor pins the refusal surface of this
// backend, one field at a time. Each of these is a field another backend honors,
// so dropping it in silence would let a compose file appear to deploy correctly
// while running something else. Setting each field ALONE (not all of them at
// once) is what makes the test meaningful: a warning that only fires as a side
// effect of some other field being set would pass a combined spec and still leave
// the field silent in the case that matters.
func TestApplyWarnsForEverySpecFieldItCannotHonor(t *testing.T) {
	for _, tc := range unsupportedFieldCases {
		t.Run(tc.field, func(t *testing.T) {
			buf := captureLogs(t)
			spec := tc.spec
			spec.Name = "web"
			spec.Image = "localhost:5000/app:v1"
			warnUnsupported(context.Background(), slog.Default(), spec)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("setting %s produced no warning containing %q; got:\n%s", tc.field, tc.want, buf.String())
			}
		})
	}
}

// supportedSpec is a DeploySpec that exercises every field this backend honors —
// whether it maps the field itself (labels, restart policy, ports), delegates it
// to pkg/deploy/internal/hostrun (the OCI spec, the networks, the volume
// backings), or has it realized outside the backend entirely (Telemetry via
// deploy.BuildTelemetryWiring; Credentials/Egress/Mounts through the pkg/server
// attachment entry points, where an unimplementable one is REFUSED with an error
// rather than dropped).
//
// It is the other half of the coverage story: anything listed here must be
// honored, and anything honored must be listed here.
func supportedSpec() api.DeploySpec {
	return api.DeploySpec{
		Name:        "web",
		Image:       "localhost:5000/app:v1",
		Entrypoint:  []string{"/bin/entry"},
		Command:     []string{"serve"},
		User:        "1000:1000",
		WorkingDir:  "/srv",
		Hostname:    "web-0",
		Env:         map[string]string{"A": "1"},
		Ports:       []api.PortMapping{{Host: 8080, Container: 80}},
		Labels:      map[string]string{"team": "infra"},
		Origin:      &api.Origin{Project: "proj"},
		Resources:   &api.Resources{CPULimit: 1, MemoryLimit: 1 << 20},
		Restart:     "always",
		Replicas:    1,
		Privileged:  true,
		Mounts:      []api.Mount{{Source: "/srv/data", Target: "/data", ReadOnly: true}},
		Volumes:     []api.VolumeSpec{{Name: "shared", Target: "/var/lib/shared"}},
		Networks:    []api.NetworkAttachment{{Name: "front", Aliases: []string{"web"}}},
		Sysctls:     map[string]string{"net.ipv4.ip_forward": "1"},
		Ulimits:     []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
		Tmpfs:       []string{"/run", "/tmp:size=64m"},
		Devices:     []string{"/dev/fuse:/dev/fuse:rwm"},
		ShmSize:     128 << 20,
		ReadOnly:    true,
		TTY:         true,
		CapAdd:      []string{"NET_ADMIN"},
		CapDrop:     []string{"CHOWN"},
		SecurityOpt: []string{"no-new-privileges:true"},
		GroupAdd:    []string{"1000"},
		PIDMode:     "host",
		IPCMode:     "host",
		Telemetry:   &api.TelemetrySpec{Endpoint: "http://otel:4318"},
		Credentials: &api.CredentialSpec{},
		Egress:      &api.EgressSpec{Mode: "proxy"},
	}
}

// TestApplyIsSilentForASpecItFullyHonors is the other half of the warn-per-field
// contract: a spec using only supported features must not emit warnings, or the
// warnings become noise operators learn to ignore — and a warning channel
// operators ignore is indistinguishable from the silent drop this backend's
// per-field warnings exist to prevent.
func TestApplyIsSilentForASpecItFullyHonors(t *testing.T) {
	buf := captureLogs(t)
	warnUnsupported(context.Background(), slog.Default(), supportedSpec())
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("a fully-supported spec should warn about nothing, got:\n%s", buf.String())
	}
}
