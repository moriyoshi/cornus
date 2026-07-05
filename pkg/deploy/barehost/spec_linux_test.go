//go:build linux

package barehost

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"cornus/pkg/api"
)

// seedImageConfig writes an OCI image config blob into a fresh on-disk content
// store and returns an ociImage pointing at it. No daemon, no root — this is the
// exact wrapper buildSpec consumes in production, so it proves the lifted spec
// generation works in-process.
func seedImageConfig(t *testing.T, cfg ocispec.ImageConfig) ociImage {
	t.Helper()
	store, err := local.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("content store: %v", err)
	}
	img := ocispec.Image{
		Platform: ocispec.Platform{Architecture: "amd64", OS: "linux"},
		Config:   cfg,
		RootFS:   ocispec.RootFS{Type: "layers"},
	}
	blob, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("marshal image config: %v", err)
	}
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(blob),
		Size:      int64(len(blob)),
	}
	if err := content.WriteBlob(t.Context(), store, "config-"+desc.Digest.String(), bytes.NewReader(blob), desc); err != nil {
		t.Fatalf("write config blob: %v", err)
	}
	return ociImage{store: store, config: desc}
}

func TestBuildSpecImageDefaults(t *testing.T) {
	img := seedImageConfig(t, ocispec.ImageConfig{
		Env:        []string{"PATH=/usr/bin", "FOO=bar"},
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "echo hi"},
		WorkingDir: "/app",
	})
	spec := api.DeploySpec{Name: "web"}
	s, err := buildSpec(t.Context(), "cornus-web-0", spec, img, t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	// No entrypoint/command override → args are the image entrypoint + cmd.
	if got := s.Process.Args; len(got) != 3 || got[0] != "/bin/sh" || got[1] != "-c" || got[2] != "echo hi" {
		t.Errorf("Process.Args = %v, want [/bin/sh -c echo hi]", got)
	}
	if s.Process.Cwd != "/app" {
		t.Errorf("Process.Cwd = %q, want /app", s.Process.Cwd)
	}
	if s.Hostname != "cornus-web-0" {
		t.Errorf("Hostname = %q, want cornus-web-0", s.Hostname)
	}
	assertEnv(t, s.Process.Env, "FOO=bar")
}

func TestBuildSpecCommandOverride(t *testing.T) {
	img := seedImageConfig(t, ocispec.ImageConfig{
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "original"},
	})
	// spec.Command replaces the image CMD but keeps the image entrypoint (docker
	// semantics), and spec.Env / Hostname / cgroup pin are applied.
	spec := api.DeploySpec{
		Name:     "web",
		Command:  []string{"my-arg"},
		Env:      map[string]string{"K": "V"},
		Hostname: "custom-host",
	}
	s, err := buildSpec(t.Context(), "cornus-web-0", spec, img, t.TempDir(), "", cgroupsPath("cornus-web-0", false), nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if got := s.Process.Args; len(got) != 2 || got[0] != "/bin/sh" || got[1] != "my-arg" {
		t.Errorf("Process.Args = %v, want [/bin/sh my-arg]", got)
	}
	if s.Hostname != "custom-host" {
		t.Errorf("Hostname = %q, want custom-host", s.Hostname)
	}
	if s.Linux == nil || s.Linux.CgroupsPath != "/cornus/cornus-web-0" {
		t.Errorf("CgroupsPath = %v, want /cornus/cornus-web-0", s.Linux)
	}
	assertEnv(t, s.Process.Env, "K=V")
}

func TestBuildSpecEntrypointOverride(t *testing.T) {
	img := seedImageConfig(t, ocispec.ImageConfig{
		Entrypoint: []string{"/original"},
		Cmd:        []string{"drop-me"},
	})
	spec := api.DeploySpec{
		Name:       "web",
		Entrypoint: []string{"/custom"},
		Command:    []string{"a", "b"},
	}
	s, err := buildSpec(t.Context(), "cornus-web-0", spec, img, t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	// Explicit entrypoint replaces the image's and drops the image CMD.
	want := []string{"/custom", "a", "b"}
	if got := s.Process.Args; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("Process.Args = %v, want %v", got, want)
	}
}

func TestCgroupsPath(t *testing.T) {
	if got := cgroupsPath("cornus-web-0", true); got != "cornus.slice:cornus:cornus-web-0" {
		t.Errorf("systemd cgroupsPath = %q", got)
	}
	if got := cgroupsPath("cornus-web-0", false); got != "/cornus/cornus-web-0" {
		t.Errorf("cgroupfs cgroupsPath = %q", got)
	}
}

func assertEnv(t *testing.T, env []string, want string) {
	t.Helper()
	for _, e := range env {
		if e == want {
			return
		}
	}
	t.Errorf("env %v missing %q", env, want)
}

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test, so tests can assert on the backend's per-field warnings.
// logging.FromContext (which applyInternal and warnUnsupported use) derives from
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
// what the operator asked for. That is not hypothetical here: this backend
// dropped spec.Ingress entirely, and six kubernetes-only fields besides.
//
// Adding a field to api.DeploySpec therefore means doing one of two things here:
// mapping it (and adding it to supportedSpec, which asserts this backend stays
// silent about it) or adding a row. TestEveryDeploySpecFieldIsMappedOrWarned
// enforces that by reflection, so the choice cannot be skipped.
//
// Note what is deliberately ABSENT: Mounts, Volumes, Networks, Sysctls, Ulimits,
// Tmpfs, Devices, ShmSize, PIDMode, IPCMode, SecurityOpt and GroupAdd are mapped
// by pkg/deploy/internal/hostrun (the OCI spec / network / volume machinery both
// host backends share); DNSServers/DNSSearch and RestartMaxAttempts ARE mapped
// here (the managed resolv.conf and the supervisor's attempt cap), unlike on
// containerdhost; and Telemetry/Credentials/Egress/Mounts are realized outside
// this package. None of those is a silent drop, so none of them warns.
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
	{"DNSOptions", api.DeploySpec{DNSOptions: []string{"ndots:2"}}, "ignores dnsOptions"},
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
// whether it maps the field itself (the managed hosts/resolv files, the
// supervisor's restart policy and attempt cap), delegates it to
// pkg/deploy/internal/hostrun (the OCI spec, the networks, the volume backings),
// or has it realized outside the backend entirely (Telemetry via
// deploy.BuildTelemetryWiring; Credentials/Egress/Mounts through the pkg/server
// attachment entry points, where an unimplementable one is REFUSED with an error
// rather than dropped).
//
// It is the other half of the coverage story: anything listed here must be
// honored, and anything honored must be listed here.
func supportedSpec() api.DeploySpec {
	return api.DeploySpec{
		Name:               "web",
		Image:              "localhost:5000/app:v1",
		Entrypoint:         []string{"/bin/entry"},
		Command:            []string{"serve"},
		User:               "1000:1000",
		WorkingDir:         "/srv",
		Hostname:           "web-0",
		Env:                map[string]string{"A": "1"},
		Ports:              []api.PortMapping{{Host: 8080, Container: 80}},
		Labels:             map[string]string{"team": "infra"},
		Origin:             &api.Origin{Project: "proj"},
		Resources:          &api.Resources{CPULimit: 1, MemoryLimit: 1 << 20},
		Restart:            "on-failure",
		RestartMaxAttempts: 3,
		Replicas:           1,
		Privileged:         true,
		Mounts:             []api.Mount{{Source: "/srv/data", Target: "/data", ReadOnly: true}},
		Volumes:            []api.VolumeSpec{{Name: "shared", Target: "/var/lib/shared"}},
		Networks:           []api.NetworkAttachment{{Name: "front", Aliases: []string{"web"}}},
		Sysctls:            map[string]string{"net.ipv4.ip_forward": "1"},
		Ulimits:            []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
		Tmpfs:              []string{"/run", "/tmp:size=64m"},
		Devices:            []string{"/dev/fuse:/dev/fuse:rwm"},
		ShmSize:            128 << 20,
		ReadOnly:           true,
		TTY:                true,
		CapAdd:             []string{"NET_ADMIN"},
		CapDrop:            []string{"CHOWN"},
		SecurityOpt:        []string{"no-new-privileges:true"},
		GroupAdd:           []string{"1000"},
		PIDMode:            "host",
		IPCMode:            "host",
		DNSServers:         []string{"1.1.1.1"},
		DNSSearch:          []string{"corp.internal"},
		Telemetry:          &api.TelemetrySpec{Endpoint: "http://otel:4318"},
		Credentials:        &api.CredentialSpec{},
		Egress:             &api.EgressSpec{Mode: "proxy"},
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
