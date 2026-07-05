//go:build linux

package incushost

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test, so tests can assert on the backend's per-field warnings.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// TestBuildInstancesPostTranslatesSpecToInstanceConfig pins the backend's core
// translation: a DeploySpec becomes one Incus container create request whose
// user.* metadata carries cornus's ownership and provenance, whose
// environment.*/limits.* keys carry the workload's env and resource caps, and
// which starts on create.
func TestBuildInstancesPostTranslatesSpecToInstanceConfig(t *testing.T) {
	b := testBackend(newFakeConn())
	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		Env:   map[string]string{"FOO": "bar", "EMPTY": ""},
		Origin: &api.Origin{
			Project: "proj",
			Host:    "dev-box",
			User:    "alice",
			Git:     &api.GitOrigin{Remote: "git@example.com:o/r", Branch: "main", Commit: "abc123", Dirty: true},
		},
		Labels:     map[string]string{"team": "infra"},
		Privileged: true,
		Resources:  &api.Resources{CPULimit: 1.5, MemoryLimit: 512 << 20},
	}
	post, err := b.buildInstancesPost(context.Background(), spec, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}

	if post.Name != "cornus-web-0" {
		t.Fatalf("instance name = %q", post.Name)
	}
	if post.Type != incusapi.InstanceTypeContainer {
		t.Fatalf("instance type = %q, want container", post.Type)
	}
	// Created STOPPED, deliberately. Apply starts them in a second pass, because
	// the gap between create and start is the only window in which an instance's
	// id map both exists and is not yet in use — which is what makes credential
	// file delivery possible on this backend (credential_file_linux.go).
	//
	// This replaces an assertion that instances are created STARTED. The workload
	// still ends up running, and that is the contract worth holding; it is pinned
	// by TestApplyStartsEveryInstance below, which observes the running state
	// rather than this flag.
	if post.Start {
		t.Fatal("instances must be created STOPPED: Apply starts them in a second pass, and the " +
			"window between the two is what credential file delivery depends on")
	}
	if post.Source.Protocol != "oci" || post.Source.Alias != "app:v1" {
		t.Fatalf("image source not wired: %+v", post.Source)
	}

	want := map[string]string{
		"user." + deploy.LabelManaged:         "true",
		"user." + deploy.LabelApp:             "web",
		"user.cornus.image":                   "localhost:5000/app:v1",
		"user.team":                           "infra",
		"user." + deploy.LabelOriginProject:   "proj",
		"user." + deploy.LabelOriginHost:      "dev-box",
		"user." + deploy.LabelOriginUser:      "alice",
		"user." + deploy.LabelOriginGitRemote: "git@example.com:o/r",
		"user." + deploy.LabelOriginGitBranch: "main",
		"user." + deploy.LabelOriginGitCommit: "abc123",
		"user." + deploy.LabelOriginGitDirty:  "true",
		"environment.FOO":                     "bar",
		"environment.EMPTY":                   "",
		"security.privileged":                 "true",
		"limits.cpu.allowance":                "150%",
		"limits.memory":                       "536870912",
		"boot.autorestart":                    "true",
	}
	for k, v := range want {
		if got, ok := post.Config[k]; !ok || got != v {
			t.Errorf("config[%q] = %q (present=%v), want %q", k, got, ok, v)
		}
	}
}

// TestBuildInstancesPostOmitsUnsetResourceAndPrivilegeKeys pins the negative
// half: an ordinary spec must not stamp limits or a privilege escalation the
// operator never asked for.
func TestBuildInstancesPostOmitsUnsetResourceAndPrivilegeKeys(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name:      "web",
		Image:     "localhost:5000/app:v1",
		Resources: &api.Resources{}, // set, but with nothing capped
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	for _, k := range []string{"security.privileged", "limits.cpu.allowance", "limits.memory"} {
		if v, ok := post.Config[k]; ok {
			t.Errorf("config[%q] should be absent, got %q", k, v)
		}
	}
}

// TestBuildInstancesPostCPULimitBecomesAPercentAllowance pins the arithmetic of
// the CPU cap: Incus's limits.cpu.allowance is a percentage of one core's time,
// so a fractional core count has to be scaled, not passed through.
func TestBuildInstancesPostCPULimitBecomesAPercentAllowance(t *testing.T) {
	b := testBackend(newFakeConn())
	for _, tc := range []struct {
		cpu  float64
		want string
	}{
		{0.25, "25%"},
		{1, "100%"},
		{2.5, "250%"},
	} {
		post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
			Name: "web", Image: "localhost:5000/app:v1",
			Resources: &api.Resources{CPULimit: tc.cpu},
		}, 0)
		if err != nil {
			t.Fatalf("cpu %v: %v", tc.cpu, err)
		}
		if got := post.Config["limits.cpu.allowance"]; got != tc.want {
			t.Errorf("CPULimit %v -> %q, want %q", tc.cpu, got, tc.want)
		}
	}
}

// TestBuildInstancesPostRestartPolicyDrivesAutorestart pins the restart-policy
// mapping: only an explicit "no" leaves Incus's autorestart off, so a crashed
// long-lived workload comes back exactly on the policies that promise it.
func TestBuildInstancesPostRestartPolicyDrivesAutorestart(t *testing.T) {
	b := testBackend(newFakeConn())
	for _, tc := range []struct {
		restart string
		want    string // "" means the key must be absent
	}{
		{"", "true"}, // default is unless-stopped
		{"always", "true"},
		{"unless-stopped", "true"},
		{"on-failure", "true"},
		{"no", ""},
	} {
		post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
			Name: "web", Image: "localhost:5000/app:v1", Restart: tc.restart,
		}, 0)
		if err != nil {
			t.Fatalf("restart %q: %v", tc.restart, err)
		}
		got, ok := post.Config["boot.autorestart"]
		if tc.want == "" && ok {
			t.Errorf("restart %q: boot.autorestart should be absent, got %q", tc.restart, got)
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("restart %q: boot.autorestart = %q, want %q", tc.restart, got, tc.want)
		}
	}
}

// TestBuildInstancesPostOwnershipKeysOutrankUserLabels pins that a user label
// cannot forge or disown cornus's management metadata: `labels:` in a compose
// file naming cornus.managed/cornus.app must lose, otherwise a workload could
// make itself invisible to (or steal instances from) another deployment.
func TestBuildInstancesPostOwnershipKeysOutrankUserLabels(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		Labels: map[string]string{
			deploy.LabelManaged: "false",
			deploy.LabelApp:     "someone-else",
		},
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if got := post.Config["user."+deploy.LabelManaged]; got != "true" {
		t.Errorf("managed label = %q, want true", got)
	}
	if got := post.Config["user."+deploy.LabelApp]; got != "web" {
		t.Errorf("app label = %q, want web", got)
	}
}

// TestBuildInstancesPostPublishesPortsOnReplicaZeroOnly pins the cross-backend
// publish contract at the translation level: one host port has exactly one DNAT
// target, so replicas 1+ get no proxy device at all (and no empty device map).
func TestBuildInstancesPostPublishesPortsOnReplicaZeroOnly(t *testing.T) {
	b := testBackend(newFakeConn())
	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		Ports: []api.PortMapping{{Host: 8080, Container: 80}, {Host: 9090, Container: 90}},
	}
	zero, err := b.buildInstancesPost(context.Background(), spec, 0)
	if err != nil {
		t.Fatalf("replica 0: %v", err)
	}
	if len(zero.Devices) != 2 {
		t.Fatalf("replica 0 devices = %v, want 2", zero.Devices)
	}
	if _, ok := zero.Devices["cornus-port-0"]; !ok {
		t.Fatalf("device naming changed: %v", zero.Devices)
	}
	one, err := b.buildInstancesPost(context.Background(), spec, 1)
	if err != nil {
		t.Fatalf("replica 1: %v", err)
	}
	if one.Devices != nil {
		t.Fatalf("replica 1 devices = %v, want nil", one.Devices)
	}
}

// TestProxyDeviceRendersHostListenerAndContainerTarget pins the proxy-device
// rendering, which is what actually makes a published port reachable: the host
// side listens on the requested protocol/address/port and connects to the
// container's loopback on the container port.
func TestProxyDeviceRendersHostListenerAndContainerTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pm          api.PortMapping
		wantListen  string
		wantConnect string
	}{
		{"defaults to tcp on all addresses", api.PortMapping{Host: 8080, Container: 80}, "tcp:0.0.0.0:8080", "tcp:127.0.0.1:80"},
		{"honors udp", api.PortMapping{Host: 53, Container: 5353, Protocol: "UDP"}, "udp:0.0.0.0:53", "udp:127.0.0.1:5353"},
		{"honors a pinned host address", api.PortMapping{Host: 8080, Container: 80, HostIP: "127.0.0.1"}, "tcp:127.0.0.1:8080", "tcp:127.0.0.1:80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dev, name := proxyDevice(3, tc.pm)
			if dev == nil {
				t.Fatal("expected a device")
			}
			if name != "cornus-port-3" {
				t.Errorf("device name = %q", name)
			}
			if dev["type"] != "proxy" || dev["bind"] != "host" {
				t.Errorf("device type/bind = %q/%q", dev["type"], dev["bind"])
			}
			if dev["listen"] != tc.wantListen {
				t.Errorf("listen = %q, want %q", dev["listen"], tc.wantListen)
			}
			if dev["connect"] != tc.wantConnect {
				t.Errorf("connect = %q, want %q", dev["connect"], tc.wantConnect)
			}
		})
	}

	// A mapping with no host port publishes nothing (compose `- "80"` exposes
	// the container port without asking for a host listener).
	if dev, name := proxyDevice(0, api.PortMapping{Container: 80}); dev != nil || name != "" {
		t.Fatalf("unpublished mapping produced a device: %v %q", dev, name)
	}
}

// TestBuildInstancesPostRejectsUnusableImageReference pins that a spec whose
// image cannot be turned into an Incus OCI source fails at translation time,
// rather than creating an instance that can never start.
func TestBuildInstancesPostRejectsUnusableImageReference(t *testing.T) {
	b := testBackend(newFakeConn())
	for _, ref := range []string{"", "   ", "NOT A REF"} {
		if _, err := b.buildInstancesPost(context.Background(), api.DeploySpec{Name: "web", Image: ref}, 0); err == nil {
			t.Errorf("image %q: expected an error", ref)
		}
	}
}

// TestImageSourceRejectsEmptyAndMalformedReferences pins the same refusal at the
// pure mapping, including the message shape an operator sees.
func TestImageSourceRejectsEmptyAndMalformedReferences(t *testing.T) {
	if _, err := imageSource("  ", nil); err == nil || !strings.Contains(err.Error(), "empty image reference") {
		t.Fatalf("blank ref: got %v", err)
	}
	if _, err := imageSource("bad ref/with spaces:v1", nil); err == nil {
		t.Fatal("malformed ref: expected an error")
	}
}

func TestImageSourceEmbedsEscapedPullCredential(t *testing.T) {
	cred := &deploy.RegistryCredential{Username: "cornus-internal", Password: "token:@/?"}
	src, err := imageSource("localhost:5000/team/app:v1", cred)
	if err != nil {
		t.Fatal(err)
	}
	if src.Server != "http://cornus-internal:token%3A%40%2F%3F@localhost:5000" {
		t.Fatalf("credentialed server = %q", src.Server)
	}
	if src.Alias != "team/app:v1" || src.Protocol != "oci" || src.Type != "image" {
		t.Fatalf("credential changed image source identity: %+v", src)
	}
}

func TestBuildInstancesPostKeepsCredentialOutOfCornusMetadata(t *testing.T) {
	b := testBackend(newFakeConn())
	const token = "short-lived-sensitive-token"
	var resolvedRef string
	b.creds = func(_ context.Context, ref string) (deploy.RegistryCredential, bool, error) {
		resolvedRef = ref
		return deploy.RegistryCredential{Username: "cornus-internal", Password: token}, true, nil
	}
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}
	post, err := b.buildInstancesPost(context.Background(), spec, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedRef != spec.Image {
		t.Fatalf("credential resolver ref = %q, want %q", resolvedRef, spec.Image)
	}
	if !strings.Contains(post.Source.Server, token) {
		t.Fatalf("Incus source does not carry credential: %q", post.Source.Server)
	}
	if got := post.Config[imageConfigKey]; got != spec.Image {
		t.Fatalf("Cornus image metadata = %q, want original ref %q", got, spec.Image)
	}
	for key, value := range post.Config {
		if strings.Contains(value, token) {
			t.Fatalf("sensitive token leaked into Cornus metadata %q", key)
		}
	}
}

// TestInsecureRegistryHonorsTheEnvAllowlist pins that a non-localhost registry
// is addressed over https unless the operator listed it in
// CORNUS_INCUS_INSECURE_REGISTRIES — the only escape hatch for a plain-HTTP
// registry that is not on loopback.
func TestInsecureRegistryHonorsTheEnvAllowlist(t *testing.T) {
	t.Setenv("CORNUS_INCUS_INSECURE_REGISTRIES", "")
	src, err := imageSource("reg.internal:5000/app:v1", nil)
	if err != nil {
		t.Fatalf("imageSource: %v", err)
	}
	if src.Server != "https://reg.internal:5000" {
		t.Fatalf("unlisted registry served over %q, want https", src.Server)
	}

	t.Setenv("CORNUS_INCUS_INSECURE_REGISTRIES", "other.example, reg.internal:5000")
	src, err = imageSource("reg.internal:5000/app:v1", nil)
	if err != nil {
		t.Fatalf("imageSource: %v", err)
	}
	if src.Server != "http://reg.internal:5000" {
		t.Fatalf("allowlisted registry served over %q, want http", src.Server)
	}

	// The bare host (without the port) is accepted too.
	t.Setenv("CORNUS_INCUS_INSECURE_REGISTRIES", "reg.internal")
	src, err = imageSource("reg.internal:5000/app:v1", nil)
	if err != nil {
		t.Fatalf("imageSource: %v", err)
	}
	if src.Server != "http://reg.internal:5000" {
		t.Fatalf("host-only allowlist entry did not match: %q", src.Server)
	}
}

// unsupportedFieldCases enumerates, one entry per api.DeploySpec field this
// backend cannot honor, a spec that sets ONLY that field and the warning it must
// produce. It is the machine-checkable half of this backend's parity story: a
// field that is neither mapped nor listed here is a field that gets accepted and
// then dropped in total silence, which is invisible to every other gate — the
// build passes, the tests pass, the deploy succeeds, and the workload is not
// what the operator asked for.
//
// Adding a field to api.DeploySpec therefore means doing one of two things here:
// mapping it (and adding it to the supported spec in
// TestBuildInstancesPostIsSilentForASpecItFullyHonors) or adding a row.
var unsupportedFieldCases = []struct {
	field string
	spec  api.DeploySpec // merged onto a minimal name+image spec
	want  string         // substring the warning must contain
}{
	{"Command (without Entrypoint)", api.DeploySpec{Command: []string{"serve"}}, "ignores a command-only override"},
	{"User (username form)", api.DeploySpec{User: "app"}, "ignores a username-form user"},
	{"WorkingDir (relative)", api.DeploySpec{WorkingDir: "srv/app"}, "ignores a relative workingDir"},
	{"Hostname", api.DeploySpec{Hostname: "db"}, "ignores hostname"},
	{"StopSignal", api.DeploySpec{StopSignal: "SIGINT"}, "ignores stopSignal"},
	{"StopGracePeriod", api.DeploySpec{StopGracePeriod: "30s"}, "ignores stopGracePeriod"},
	{"ReadOnly", api.DeploySpec{ReadOnly: true}, "ignores readOnly"},
	{"CapAdd", api.DeploySpec{CapAdd: []string{"NET_ADMIN"}}, "ignores capAdd"},
	{"CapDrop", api.DeploySpec{CapDrop: []string{"CHOWN"}}, "ignores capDrop"},
	{"SecurityOpt", api.DeploySpec{SecurityOpt: []string{"no-new-privileges:true"}}, "ignores securityOpt"},
	{"GroupAdd", api.DeploySpec{GroupAdd: []string{"docker"}}, "ignores groupAdd"},
	{"ExtraHosts", api.DeploySpec{ExtraHosts: []string{"db:10.0.0.5"}}, "ignores extraHosts"},
	{"DNSServers", api.DeploySpec{DNSServers: []string{"1.1.1.1"}}, "ignores dnsServers"},
	{"DNSSearch", api.DeploySpec{DNSSearch: []string{"corp.internal"}}, "ignores dnsSearch"},
	{"DNSOptions", api.DeploySpec{DNSOptions: []string{"ndots:2"}}, "ignores dnsOptions"},
	{"Devices", api.DeploySpec{Devices: []string{"/dev/fuse:/dev/fuse:rwm"}}, "ignores devices"},
	{"Init", api.DeploySpec{Init: boolPtr(true)}, "ignores init"},
	{"TTY", api.DeploySpec{TTY: true}, "ignores tty"},
	{"StdinOpen", api.DeploySpec{StdinOpen: true}, "ignores stdinOpen"},
	{"PIDMode", api.DeploySpec{PIDMode: "host"}, "ignores pidMode"},
	{"IPCMode", api.DeploySpec{IPCMode: "host"}, "ignores ipcMode"},
	{"RestartMaxAttempts", api.DeploySpec{Restart: "on-failure", RestartMaxAttempts: 3}, "ignores restartMaxAttempts"},
	{"Resources.ReservedCPU", api.DeploySpec{Resources: &api.Resources{ReservedCPU: 0.5}}, "ignores resource reservations"},
	{"Resources.ReservedMemory", api.DeploySpec{Resources: &api.Resources{ReservedMemory: 1 << 20}}, "ignores resource reservations"},
	{"UpdateConfig", api.DeploySpec{UpdateConfig: &api.UpdateConfig{Parallelism: 2}}, "ignores updateConfig"},
	{"Telemetry", api.DeploySpec{Telemetry: &api.TelemetrySpec{Endpoint: "http://otel:4318"}}, "ignores telemetry"},
	{"Credentials", api.DeploySpec{Credentials: &api.CredentialSpec{}}, "ignores credentials"},
	{"Egress", api.DeploySpec{Egress: &api.EgressSpec{Mode: "proxy"}}, "ignores egress"},
	{"Proxy", api.DeploySpec{Proxy: &api.ProxySpec{}}, "ignores proxy"},
	{"DNS", api.DeploySpec{DNS: &api.DNSSpec{}}, "ignores dns records"},
	{"Hub", api.DeploySpec{Hub: &api.HubSpec{}}, "ignores hub"},
	{"Docker", api.DeploySpec{Docker: &api.DockerSpec{}}, "ignores docker"},
	{"AgentForward", api.DeploySpec{AgentForward: true}, "ignores agentForward"},
	{"Ingress", api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true}}, "creates no cluster Ingress"},
	{"Knative", api.DeploySpec{Knative: &api.KnativeSpec{Enabled: true}}, "ignores knative"},
	// Pinned on the CONSEQUENCE clause, not on "ignores user-defined networks".
	// That prefix survives a revert to the bare message, so asserting it would let
	// the useful half of this warning be deleted in green. What the operator has to
	// learn here is that ignoring the block leaves the workloads sharing one bridge
	// — i.e. NOT segmented — which is the opposite of what "ignored" suggests and
	// the less safe reading of the two.
	{"Networks", api.DeploySpec{Networks: []api.NetworkAttachment{{Name: "backend"}}}, "NOT segmented from one another"},
	// Per-entry refusals inside a field that IS otherwise mapped.
	{"Sysctls (name incus owns)", api.DeploySpec{Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "1024"}}, "ignores a sysctl incus sets itself"},
	{"Sysctls (unusable name)", api.DeploySpec{Sysctls: map[string]string{"net.ipv4 forward=1": "1"}}, "ignores a sysctl whose name"},
	{"Ulimits (undocumented limit)", api.DeploySpec{Ulimits: []api.Ulimit{{Name: "stack", Soft: 1, Hard: 1}}}, "ignores an rlimit incus does not document"},
	{"Ulimits (inverted bounds)", api.DeploySpec{Ulimits: []api.Ulimit{{Name: "nofile", Soft: 4096, Hard: 1024}}}, "ignores an rlimit whose soft bound"},
	{"Mounts (no source)", api.DeploySpec{Mounts: []api.Mount{{Target: "/data"}}}, "ignores a mount with no source"},
	{"Mounts (relative source)", api.DeploySpec{Mounts: []api.Mount{{Source: "cache", Target: "/data"}}}, "ignores a mount with a relative source"},
	{"Mounts (root target)", api.DeploySpec{Mounts: []api.Mount{{Source: "/host", Target: "/"}}}, "ignores a mount without an absolute non-root target"},
	{"Mounts (SELinux relabel)", api.DeploySpec{Mounts: []api.Mount{{Source: "/host", Target: "/in", SELinux: "z"}}}, "ignores the SELinux relabel"},
	{"Tmpfs (relative path)", api.DeploySpec{Tmpfs: []string{"run"}}, "ignores a tmpfs entry without an absolute non-root path"},
	{"Tmpfs (inexpressible option)", api.DeploySpec{Tmpfs: []string{"/run:noexec"}}, "ignores tmpfs mount options"},
	{"Volumes (relative target)", api.DeploySpec{Volumes: []api.VolumeSpec{{Target: "data"}}}, "ignores a managed volume"},
	{"Volumes (unparseable size)", api.DeploySpec{Volumes: []api.VolumeSpec{{Target: "/data", Size: "lots"}}}, "ignores a managed volume"},
	{"Volumes (driver)", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "cache", Target: "/data", Driver: "local"}}}, "ignores a managed volume's driver options"},
	{"Volumes (labels)", api.DeploySpec{Volumes: []api.VolumeSpec{{Name: "cache", Target: "/data", Labels: map[string]string{"a": "1"}}}}, "ignores a managed volume's labels"},
}

func boolPtr(b bool) *bool { return &b }

// TestBuildInstancesPostWarnsForEverySpecFieldItCannotHonor pins the refusal
// surface of this backend, one field at a time. Each of these is a field another
// backend honors, so dropping it in silence would let a compose file appear to
// deploy correctly while running something else. Setting each field ALONE (not
// all of them at once) is what makes the test meaningful: a warning that only
// fires as a side effect of some other field being set would pass a combined
// spec and still leave the field silent in the case that matters.
func TestBuildInstancesPostWarnsForEverySpecFieldItCannotHonor(t *testing.T) {
	for _, tc := range unsupportedFieldCases {
		t.Run(tc.field, func(t *testing.T) {
			buf := captureLogs(t)
			b := testBackend(newFakeConn())
			spec := tc.spec
			spec.Name = "web"
			spec.Image = "localhost:5000/app:v1"
			if _, err := b.buildInstancesPost(context.Background(), spec, 0); err != nil {
				t.Fatalf("buildInstancesPost: %v", err)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("setting %s produced no warning containing %q; got:\n%s", tc.field, tc.want, buf.String())
			}
		})
	}
}

// TestBuildInstancesPostAppliesEntrypointOverride is the regression test for a
// parity gap this backend used to declare permanent: an `entrypoint:` in a
// compose file was warned about and DROPPED, so the instance quietly ran the
// image's own entrypoint instead of the one the operator asked for — a container
// running something other than what the spec says, on the one backend where that
// is invisible from the outside.
//
// The fix is the key the companion already relies on. Incus's oci.entrypoint
// (internal/instance/config.go; consumed in driver_lxc.go via shellquote.Split
// into lxc.execute.cmd) replaces the image's whole argv, which is precisely
// cornus's entrypoint semantics: an explicit entrypoint drops the image CMD, and
// spec.Command becomes its arguments. This pins that argv, including the
// entrypoint-only case where no command follows.
func TestBuildInstancesPostAppliesEntrypointOverride(t *testing.T) {
	tests := []struct {
		name       string
		entrypoint []string
		command    []string
		want       []string
	}{
		{"entrypoint alone replaces the argv and drops the image CMD", []string{"/bin/entry"}, nil, []string{"/bin/entry"}},
		{"command supplies the arguments to the override", []string{"/bin/entry"}, []string{"serve", "--port=80"}, []string{"/bin/entry", "serve", "--port=80"}},
		{"an argument needing quoting survives", []string{"/bin/sh", "-c"}, []string{"echo 'hi there'"}, []string{"/bin/sh", "-c", "echo 'hi there'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testBackend(newFakeConn())
			post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
				Name:       "web",
				Image:      "localhost:5000/app:v1",
				Entrypoint: tt.entrypoint,
				Command:    tt.command,
			}, 0)
			if err != nil {
				t.Fatalf("buildInstancesPost: %v", err)
			}
			if got, want := post.Config["oci.entrypoint"], ociEntrypoint(tt.want); got != want {
				t.Fatalf("oci.entrypoint = %q, want %q", got, want)
			}
		})
	}
}

// TestBuildInstancesPostLeavesEntrypointToTheImageWhenUnset keeps the override
// opt-in: with no entrypoint in the spec the key must be ABSENT, not empty.
// incusd seeds oci.entrypoint from the image's own config.json only when the
// create request left it unset (cmd/incusd/instance.go), so writing the key
// unconditionally would pin an argv the image never declared.
func TestBuildInstancesPostLeavesEntrypointToTheImageWhenUnset(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name:    "web",
		Image:   "localhost:5000/app:v1",
		Command: []string{"serve"},
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if _, ok := post.Config["oci.entrypoint"]; ok {
		t.Errorf("oci.entrypoint = %q for a command-only spec; the image entrypoint must survive", post.Config["oci.entrypoint"])
	}
}

// TestBuildInstancesPostAppliesWorkingDirAndNumericUser is the regression test
// for the second half of the same parity gap the entrypoint fix closed: a
// `working_dir:` and a numeric `user:` were warned about and DROPPED, so the
// process ran as whatever uid and in whatever directory the IMAGE declared,
// while the compose file said otherwise.
//
// oci.cwd / oci.uid / oci.gid are oci.entrypoint's siblings, declared alongside
// it (internal/instance/config.go) and consumed the same way: incusd seeds each
// from the image's config.json only when the create request left it empty
// (cmd/incusd/instance.go), and driver_lxc.go prefers the config key over the
// image value when writing lxc.init.cwd / lxc.init.uid / lxc.init.gid. Writing
// them at create time is therefore exactly an override.
func TestBuildInstancesPostAppliesWorkingDirAndNumericUser(t *testing.T) {
	tests := []struct {
		name       string
		user       string
		workingDir string
		want       map[string]string // key -> value; "" means the key must be ABSENT
	}{
		{
			name:       "uid and gid both override",
			user:       "1000:2000",
			workingDir: "/srv/app",
			want:       map[string]string{"oci.uid": "1000", "oci.gid": "2000", "oci.cwd": "/srv/app"},
		},
		{
			// A uid-only user must leave oci.gid unset, so incusd still seeds the
			// image's own group. Writing a gid cornus invented would run the process
			// in a group nobody asked for.
			name: "a uid-only user leaves the image's group alone",
			user: "1000",
			want: map[string]string{"oci.uid": "1000", "oci.gid": "", "oci.cwd": ""},
		},
		{
			name: "root is an override like any other, not an absent value",
			user: "0:0",
			want: map[string]string{"oci.uid": "0", "oci.gid": "0"},
		},
		{
			// The uint32 ceiling incusd's IsUint32 enforces, to prove the boundary is
			// inclusive rather than accidentally excluded.
			name: "the largest uid incus accepts still maps",
			user: "4294967295",
			want: map[string]string{"oci.uid": "4294967295"},
		},
		{
			name:       "workingDir alone maps without touching the ids",
			workingDir: "/",
			want:       map[string]string{"oci.cwd": "/", "oci.uid": "", "oci.gid": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := testBackend(newFakeConn())
			post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
				Name:       "web",
				Image:      "localhost:5000/app:v1",
				User:       tt.user,
				WorkingDir: tt.workingDir,
			}, 0)
			if err != nil {
				t.Fatalf("buildInstancesPost: %v", err)
			}
			for k, want := range tt.want {
				got, ok := post.Config[k]
				if want == "" {
					if ok {
						t.Errorf("config[%q] = %q, want absent", k, got)
					}
					continue
				}
				if !ok || got != want {
					t.Errorf("config[%q] = %q (present=%v), want %q", k, got, ok, want)
				}
			}
		})
	}
}

// TestBuildInstancesPostKeepsTheImagesOwnUserAndCwdWhenUnexpressible is the
// other half of the mapping: every `user:` incusd's validators would reject must
// leave the config keys ABSENT and warn, never write a value that would fail the
// create or a partial value that changes half of what was asked for.
//
// The username case is the permanent one — oci.uid/oci.gid are IsUint32 and this
// backend never sees the image's /etc/passwd to resolve a name (kubernetes'
// securityContext has the identical limit). The relative-workingDir case is the
// same shape for oci.cwd's IsAbsFilePath.
func TestBuildInstancesPostKeepsTheImagesOwnUserAndCwdWhenUnexpressible(t *testing.T) {
	tests := []struct {
		name       string
		user       string
		workingDir string
		wantWarn   string
	}{
		{"a bare username cannot become a uid", "app", "", "ignores a username-form user"},
		{"a group NAME poisons an otherwise numeric uid", "1000:staff", "", "ignores a username-form user"},
		{"a username with a numeric gid is still a username", "app:2000", "", "ignores a username-form user"},
		{"a negative uid is not a uint32", "-1", "", "ignores a username-form user"},
		{"a uid past 2^32-1 is not a uint32", "4294967296", "", "ignores a username-form user"},
		{"an empty uid component is not a uint32", ":2000", "", "ignores a username-form user"},
		{"a relative workingDir cannot become oci.cwd", "", "srv/app", "ignores a relative workingDir"},
		{"a bare directory name is relative too", "", "app", "ignores a relative workingDir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureLogs(t)
			b := testBackend(newFakeConn())
			post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
				Name:       "web",
				Image:      "localhost:5000/app:v1",
				User:       tt.user,
				WorkingDir: tt.workingDir,
			}, 0)
			if err != nil {
				t.Fatalf("buildInstancesPost: %v", err)
			}
			for _, k := range []string{"oci.uid", "oci.gid", "oci.cwd"} {
				if v, ok := post.Config[k]; ok {
					t.Errorf("config[%q] = %q, want absent for an unexpressible value", k, v)
				}
			}
			if !strings.Contains(buf.String(), tt.wantWarn) {
				t.Errorf("missing warning %q in:\n%s", tt.wantWarn, buf.String())
			}
		})
	}
}

// TestOCINumericUserMatchesIncussValidator pins the parser against the check
// incusd actually runs. oci.uid / oci.gid are
// validate.Optional(validate.IsUint32), i.e. strconv.ParseUint(value, 10, 32) —
// so anything this reports ok for must be a value the create request cannot be
// rejected over, and the string must go through verbatim rather than being
// re-formatted from a parsed integer.
func TestOCINumericUserMatchesIncussValidator(t *testing.T) {
	tests := []struct {
		user    string
		uid     string
		gid     string
		ok      bool
		comment string
	}{
		{user: "", ok: false, comment: "an unset user maps to nothing at all"},
		{user: "1000", uid: "1000", ok: true},
		{user: "1000:1000", uid: "1000", gid: "1000", ok: true},
		{user: "0", uid: "0", ok: true, comment: "root is a real override"},
		{user: "0007", uid: "0007", ok: true, comment: "the operator's literal spelling survives"},
		{user: "4294967295", uid: "4294967295", ok: true, comment: "the uint32 ceiling"},
		{user: "4294967296", ok: false, comment: "one past the uint32 ceiling"},
		{user: "1000:4294967296", ok: false, comment: "an out-of-range gid rejects the whole value"},
		{user: "-1", ok: false, comment: "uint32, so no negatives"},
		{user: "app", ok: false},
		{user: "app:app", ok: false},
		{user: "1000:staff", ok: false, comment: "no partial mapping: the uid is dropped too"},
		{user: ":1000", ok: false},
		{user: "1000:", ok: false},
		{user: "1000:1000:1000", ok: false, comment: "only the first colon splits, so the remainder must parse whole"},
		{user: " 1000", ok: false, comment: "ParseUint takes no surrounding space"},
		{user: "0x10", ok: false, comment: "base 10 only"},
		{user: "+1000", ok: false, comment: "ParseUint takes no sign"},
	}
	for _, tt := range tests {
		name := tt.user
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			uid, gid, ok := ociNumericUser(tt.user)
			if ok != tt.ok || uid != tt.uid || gid != tt.gid {
				t.Errorf("ociNumericUser(%q) = (%q, %q, %v), want (%q, %q, %v) %s",
					tt.user, uid, gid, ok, tt.uid, tt.gid, tt.ok, tt.comment)
			}
		})
	}
}

// supportedSpec is a DeploySpec that exercises every field this backend maps.
// It is shared by the silence test and by the mapping tests, so the two can
// never drift apart: anything added here must be honored, and anything honored
// must be added here.
func supportedSpec() api.DeploySpec {
	return api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		// Honoured since the probe engine landed (health_linux.go): recorded on the
		// instance config and probed by cornus, so buildInstancesPost must stay
		// SILENT about it.
		Healthcheck: &api.Healthcheck{Test: []string{"CMD", "true"}, Interval: "5s"},
		Entrypoint:  []string{"/bin/entry"},
		Command:     []string{"serve"},
		User:        "1000:1000",
		WorkingDir:  "/srv",
		Env:         map[string]string{"A": "1"},
		Ports:       []api.PortMapping{{Host: 8080, Container: 80}},
		Labels:      map[string]string{"team": "infra"},
		Origin:      &api.Origin{Project: "proj"},
		Resources:   &api.Resources{CPULimit: 1, MemoryLimit: 1 << 20},
		Restart:     "always",
		Replicas:    1,
		Privileged:  true,
		Mounts:      []api.Mount{{Source: "/srv/data", Target: "/data", ReadOnly: true}},
		Volumes: []api.VolumeSpec{
			{Target: "/var/cache"},
			{Name: "shared", Target: "/var/lib/shared", Size: "1Gi", ReadOnly: true},
		},
		Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
		Ulimits: []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
		Tmpfs:   []string{"/run", "/tmp:size=64m"},
		ShmSize: 128 << 20,
	}
}

// TestBuildInstancesPostIsSilentForASpecItFullyHonors is the other half of the
// warn-per-field contract: a spec using only supported features must not emit
// warnings, or the warnings become noise operators learn to ignore — and a
// warning channel operators ignore is indistinguishable from the silent drop
// this backend's per-field warnings exist to prevent.
func TestBuildInstancesPostIsSilentForASpecItFullyHonors(t *testing.T) {
	buf := captureLogs(t)
	b := testBackend(newFakeConn())
	if _, err := b.buildInstancesPost(context.Background(), supportedSpec(), 0); err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("a fully-supported spec should warn about nothing, got:\n%s", buf.String())
	}
}

// TestBuildInstancesPostMapsSysctlsAndUlimits pins the two config-key families
// this backend translates a spec into beyond the oci.* set.
//
// linux.sysctl.<name> and limits.kernel.<name> were chosen because incusd both
// VALIDATES and CONSUMES them: internal/instance/config.go accepts the two
// prefixes (linux.sysctl.* for containers at :1641-1644, the whole
// limits.kernel.* family at :1515-1640) and
// internal/server/instance/drivers/driver_lxc.go rewrites each into the liblxc
// config the container actually starts with — linux.sysctl.X -> lxc.sysctl.X at
// :1316-1332 and limits.kernel.X -> lxc.prlimit.X at :1302-1313.
func TestBuildInstancesPostMapsSysctlsAndUlimits(t *testing.T) {
	b := testBackend(newFakeConn())
	post, err := b.buildInstancesPost(context.Background(), api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		Sysctls: map[string]string{
			"net.ipv4.ip_forward":       "1",
			"net.core.somaxconn":        "1024",
			"kernel.shmmax":             "68719476736",
			"net.ipv4.conf.all-forward": "0",
		},
		Ulimits: []api.Ulimit{
			{Name: "nofile", Soft: 1024, Hard: 4096},
			{Name: "nproc", Soft: 65535, Hard: 65535},
			{Name: "core", Soft: -1, Hard: -1},
			{Name: "MEMLOCK", Soft: 8192, Hard: -1},
		},
	}, 0)
	if err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	want := map[string]string{
		"linux.sysctl.net.ipv4.ip_forward":       "1",
		"linux.sysctl.net.core.somaxconn":        "1024",
		"linux.sysctl.kernel.shmmax":             "68719476736",
		"linux.sysctl.net.ipv4.conf.all-forward": "0",
		"limits.kernel.nofile":                   "1024:4096",
		"limits.kernel.nproc":                    "65535",
		"limits.kernel.core":                     "unlimited",
		"limits.kernel.memlock":                  "8192:unlimited",
	}
	for k, v := range want {
		if got, ok := post.Config[k]; !ok || got != v {
			t.Errorf("config[%q] = %q (present=%v), want %q", k, got, ok, v)
		}
	}
}

// TestIncusSysctlKeyRefusesWhatItWillNotSplice pins the sysctl name gate. The
// second group is the interesting one: incusd's validator for linux.sysctl.* is
// validate.IsAny, so these names WOULD be accepted at create time — they are
// refused because writing them would produce a key that quietly does nothing
// (the two incus sets itself, after ours) or a key other than the one the
// operator named.
func TestIncusSysctlKeyRefusesWhatItWillNotSplice(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string // "" means refused
	}{
		{"net.ipv4.ip_forward", "linux.sysctl.net.ipv4.ip_forward"},
		{"kernel.shmmax", "linux.sysctl.kernel.shmmax"},
		{"net.ipv4.conf.eth0-x.rp_filter", "linux.sysctl.net.ipv4.conf.eth0-x.rp_filter"},
		{"", ""},
		{"net.ipv4.", ""},
		{".leading", ""},
		{"has space", ""},
		{"has=equals", ""},
		{"net/ipv4/ip_forward", ""},
		// Set by incusd itself in the application-container branch, AFTER the
		// linux.sysctl.* keys are emitted, so incus's value is the one that lands.
		{"net.ipv4.ping_group_range", ""},
		{"net.ipv4.ip_unprivileged_port_start", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := incusSysctlKey(tc.name)
			if (tc.key == "") == ok || key != tc.key {
				t.Errorf("incusSysctlKey(%q) = (%q, %v), want (%q, %v)", tc.name, key, ok, tc.key, tc.key != "")
			}
		})
	}
}

// TestIncusUlimitRendersLiblxcPrlimitValues pins the rlimit rendering against
// liblxc's lxc.prlimit value grammar (a single number for both bounds,
// "soft:hard" for separate ones, "unlimited" for infinity) and against the name
// set incus's own validator documents. A name outside that set is refused rather
// than written: incusd's catch-all would accept the create, but the value is
// only resolved when liblxc starts the instance, and this backend creates with
// Start:true — so an undocumented name trades a dropped limit for a deploy that
// never comes up.
func TestIncusUlimitRendersLiblxcPrlimitValues(t *testing.T) {
	for _, tc := range []struct {
		u       api.Ulimit
		key     string
		value   string
		comment string
	}{
		{u: api.Ulimit{Name: "nofile", Soft: 1024, Hard: 1024}, key: "limits.kernel.nofile", value: "1024", comment: "compose's scalar shorthand sets both bounds"},
		{u: api.Ulimit{Name: "nofile", Soft: 1024, Hard: 4096}, key: "limits.kernel.nofile", value: "1024:4096"},
		{u: api.Ulimit{Name: "NOFILE", Soft: 1, Hard: 1}, key: "limits.kernel.nofile", value: "1", comment: "the config key is lower-case"},
		{u: api.Ulimit{Name: " nproc ", Soft: 1, Hard: 1}, key: "limits.kernel.nproc", value: "1"},
		{u: api.Ulimit{Name: "core", Soft: -1, Hard: -1}, key: "limits.kernel.core", value: "unlimited", comment: "docker spells infinity -1"},
		{u: api.Ulimit{Name: "memlock", Soft: 8192, Hard: -1}, key: "limits.kernel.memlock", value: "8192:unlimited"},
		{u: api.Ulimit{Name: "nofile", Soft: 0, Hard: 0}, key: "limits.kernel.nofile", value: "0", comment: "a zero limit is a real limit, not an absent one"},
		{u: api.Ulimit{Name: "stack"}, comment: "liblxc knows it, incus does not document it, so it stays a warning"},
		{u: api.Ulimit{Name: "rss"}, comment: "same"},
		{u: api.Ulimit{Name: ""}, comment: "an unnamed limit names nothing"},
		{u: api.Ulimit{Name: "nofile", Soft: 4096, Hard: 1024}, comment: "the kernel rejects soft > hard"},
		{u: api.Ulimit{Name: "nofile", Soft: -1, Hard: 1024}, comment: "an unlimited soft under a finite hard is the same inversion"},
	} {
		name := tc.u.Name + "/" + tc.comment
		t.Run(name, func(t *testing.T) {
			key, value, ok := incusUlimit(tc.u)
			if (tc.key == "") == ok || key != tc.key || value != tc.value {
				t.Errorf("incusUlimit(%+v) = (%q, %q, %v), want (%q, %q, %v)",
					tc.u, key, value, ok, tc.key, tc.value, tc.key != "")
			}
		})
	}
}

// TestPullCredentialSurvivesIncusAuthfileEncoding is the regression test for the
// one part of this backend's credential path that unit tests could previously
// only assume. It reproduces what incusd does with the userinfo cornus embeds in
// InstanceSource.Server (client/oci_images.go, runSkopeo): it base64s
// `uri.User.String()` — the PERCENT-ENCODED form — straight into a
// containers-auth.json `auth` value, and skopeo base64-decodes it and splits on
// the first colon to get the literal username and password.
//
// Nothing re-decodes the percent-escapes in between. So a credential containing
// any character Go's userinfo encoder escapes arrives at the registry MANGLED,
// and the pull fails with a 401 that names no cause. Cornus's internal pull
// credential survives only because of what it is made of: the username is a
// fixed ASCII identifier and the password is an HS256 JWT, whose base64url
// alphabet plus '.' separators contains nothing that gets escaped.
//
// That is a real constraint on the credential issuer, not a property of this
// file, which is why it is pinned here: if the internal token ever grows a '/',
// '@', '%' or '?' — a raw base64 (not base64url) token would be enough — incus
// pulls break and no other test in the tree would notice.
func TestPullCredentialSurvivesIncusAuthfileEncoding(t *testing.T) {
	// A representative HS256 JWT: three base64url segments joined by '.'.
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzY29wZSI6InJlZ2lzdHJ5OnB1bGwifQ.7Hy-_aBcD0123456789"
	cred := &deploy.RegistryCredential{Username: "cornus-internal", Password: jwt}

	src, err := imageSource("localhost:5000/team/app:v1", cred)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(src.Server)
	if err != nil {
		t.Fatalf("Incus must be able to parse the source server: %v", err)
	}
	if uri.User == nil {
		t.Fatal("no userinfo in the image source, so incusd would write no authfile at all")
	}

	// What incusd writes, and what skopeo reads back out of it.
	decoded, err := base64.StdEncoding.DecodeString(
		base64.StdEncoding.EncodeToString([]byte(uri.User.String())))
	if err != nil {
		t.Fatalf("authfile auth value: %v", err)
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		t.Fatalf("authfile auth value %q has no user:password separator", decoded)
	}
	if user != cred.Username {
		t.Errorf("registry receives username %q, want %q", user, cred.Username)
	}
	if pass != cred.Password {
		t.Errorf("registry receives password %q, want %q — percent-encoding was not undone anywhere,"+
			" so this credential cannot authenticate", pass, cred.Password)
	}
}

// TestPullCredentialEncodingIsLossyForUnsafeCharacters documents the OTHER side
// of the same contract, so the constraint above is visible as a fact rather than
// an untested belief: a credential with characters the userinfo encoder escapes
// does NOT survive the round trip. It asserts the current (broken-for-such-a-
// credential) behavior deliberately — if this test ever starts failing because
// the encoding was made lossless, delete it and relax the issuer constraint.
func TestPullCredentialEncodingIsLossyForUnsafeCharacters(t *testing.T) {
	cred := &deploy.RegistryCredential{Username: "cornus-internal", Password: "tok/en@1"}
	src, err := imageSource("localhost:5000/team/app:v1", cred)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(src.Server)
	if err != nil {
		t.Fatal(err)
	}
	_, pass, _ := strings.Cut(uri.User.String(), ":")
	if pass == cred.Password {
		t.Skip("userinfo encoding is now lossless for these characters; the issuer constraint can be relaxed")
	}
	if pass != "tok%2Fen%401" {
		t.Fatalf("unexpected encoding %q; the incus authfile round trip needs re-analysing", pass)
	}
}
