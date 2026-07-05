//go:build linux

package incushost

import (
	"bytes"
	"context"
	"log/slog"
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
	if !post.Start {
		t.Fatal("instances must be created started")
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
	if _, err := imageSource("  "); err == nil || !strings.Contains(err.Error(), "empty image reference") {
		t.Fatalf("blank ref: got %v", err)
	}
	if _, err := imageSource("bad ref/with spaces:v1"); err == nil {
		t.Fatal("malformed ref: expected an error")
	}
}

// TestInsecureRegistryHonorsTheEnvAllowlist pins that a non-localhost registry
// is addressed over https unless the operator listed it in
// CORNUS_INCUS_INSECURE_REGISTRIES — the only escape hatch for a plain-HTTP
// registry that is not on loopback.
func TestInsecureRegistryHonorsTheEnvAllowlist(t *testing.T) {
	t.Setenv("CORNUS_INCUS_INSECURE_REGISTRIES", "")
	src, err := imageSource("reg.internal:5000/app:v1")
	if err != nil {
		t.Fatalf("imageSource: %v", err)
	}
	if src.Server != "https://reg.internal:5000" {
		t.Fatalf("unlisted registry served over %q, want https", src.Server)
	}

	t.Setenv("CORNUS_INCUS_INSECURE_REGISTRIES", "other.example, reg.internal:5000")
	src, err = imageSource("reg.internal:5000/app:v1")
	if err != nil {
		t.Fatalf("imageSource: %v", err)
	}
	if src.Server != "http://reg.internal:5000" {
		t.Fatalf("allowlisted registry served over %q, want http", src.Server)
	}

	// The bare host (without the port) is accepted too.
	t.Setenv("CORNUS_INCUS_INSECURE_REGISTRIES", "reg.internal")
	src, err = imageSource("reg.internal:5000/app:v1")
	if err != nil {
		t.Fatalf("imageSource: %v", err)
	}
	if src.Server != "http://reg.internal:5000" {
		t.Fatalf("host-only allowlist entry did not match: %q", src.Server)
	}
}

// TestBuildInstancesPostWarnsForEverySpecFieldItCannotHonor pins the refusal
// surface of this backend. Each of these is a field another backend honors, so
// silently dropping it would let a compose file appear to deploy correctly while
// running something else. The contract is warn-per-field; this test fails if a
// future change starts swallowing one.
func TestBuildInstancesPostWarnsForEverySpecFieldItCannotHonor(t *testing.T) {
	buf := captureLogs(t)
	b := testBackend(newFakeConn())
	spec := api.DeploySpec{
		Name:        "web",
		Image:       "localhost:5000/app:v1",
		Entrypoint:  []string{"/bin/entry"},
		Command:     []string{"serve"},
		User:        "1000:1000",
		WorkingDir:  "/srv",
		Mounts:      []api.Mount{{Source: "/host", Target: "/in"}},
		Volumes:     []api.VolumeSpec{{Target: "/data"}},
		Healthcheck: &api.Healthcheck{Test: []string{"CMD", "true"}},
		Ingress:     &api.IngressSpec{Enabled: true},
		Knative:     &api.KnativeSpec{Enabled: true},
		Networks:    []api.NetworkAttachment{{Name: "backend"}},
	}
	if _, err := b.buildInstancesPost(context.Background(), spec, 0); err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{
		"entrypoint override is not applied",
		"ignores user override",
		"ignores workingDir override",
		"ignores mounts",
		"ignores managed volumes",
		"ignores healthcheck",
		"creates no cluster Ingress",
		"ignores knative",
		"ignores user-defined networks",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing warning %q in:\n%s", want, logs)
		}
	}
}

// TestBuildInstancesPostIsSilentForASpecItFullyHonors is the other half of the
// warn-per-field contract: a spec using only supported features must not emit
// warnings, or the warnings become noise operators learn to ignore.
func TestBuildInstancesPostIsSilentForASpecItFullyHonors(t *testing.T) {
	buf := captureLogs(t)
	b := testBackend(newFakeConn())
	spec := api.DeploySpec{
		Name:      "web",
		Image:     "localhost:5000/app:v1",
		Env:       map[string]string{"A": "1"},
		Ports:     []api.PortMapping{{Host: 8080, Container: 80}},
		Labels:    map[string]string{"team": "infra"},
		Resources: &api.Resources{CPULimit: 1, MemoryLimit: 1 << 20},
		Restart:   "always",
	}
	if _, err := b.buildInstancesPost(context.Background(), spec, 0); err != nil {
		t.Fatalf("buildInstancesPost: %v", err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("a fully-supported spec should warn about nothing, got:\n%s", buf.String())
	}
}
