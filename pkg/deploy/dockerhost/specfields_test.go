package dockerhost

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"cornus/pkg/api"
)

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test, so tests can assert on the backend's per-field warnings.
// pkg/logging.FromContext derives every backend logger from slog.Default(), so
// swapping it here captures the warnings apply actually emits.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// unsupportedFieldCases enumerates, one entry per api.DeploySpec field this
// backend cannot honor, a spec that sets ONLY that field and the warning it must
// produce. It is the machine-checkable half of this backend's parity story: a
// field that is neither mapped nor listed here is a field that gets accepted and
// then dropped in total silence, which is invisible to every other gate — the
// build passes, the tests pass, the deploy succeeds, and the workload is not
// what the operator asked for. dockerhost is the DEFAULT backend, so a silent
// drop here is the one that reaches the most people.
//
// Adding a field to api.DeploySpec therefore means doing one of two things here:
// mapping it (and adding it to supportedSpec, which asserts this backend stays
// silent) or adding a row. TestEveryDeploySpecFieldIsMappedOrWarned enforces it.
var unsupportedFieldCases = []struct {
	field string
	spec  api.DeploySpec // merged onto a minimal name+image spec
	want  string         // substring the warning must contain
}{
	// Kubernetes-only fields. These come from deploy.WarnKubernetesOnlyFields,
	// which this backend's prelude calls exactly once — do NOT add a second,
	// dockerhost-local warning for any of them (see warnUnsupported).
	{"Proxy", api.DeploySpec{Proxy: &api.ProxySpec{}}, "ignores proxy"},
	{"DNS", api.DeploySpec{DNS: &api.DNSSpec{}}, "ignores dns records"},
	{"Hub", api.DeploySpec{Hub: &api.HubSpec{}}, "ignores hub"},
	{"Docker", api.DeploySpec{Docker: &api.DockerSpec{}}, "ignores docker"},
	{"UpdateConfig", api.DeploySpec{UpdateConfig: &api.UpdateConfig{Parallelism: 2}}, "ignores updateConfig"},
	{"AgentForward", api.DeploySpec{AgentForward: true}, "ignores agentForward"},
	// dockerhost's own refusals.
	{"Credentials", api.DeploySpec{Credentials: &api.CredentialSpec{}}, "ignores credentials"},
	{"Ingress", api.DeploySpec{Ingress: &api.IngressSpec{Enabled: true}}, "creates no cluster Ingress"},
	{"Knative", api.DeploySpec{Knative: &api.KnativeSpec{Enabled: true}}, "ignores knative"},
	// A per-entry refusal inside a field that IS otherwise mapped: the limits and
	// the MEMORY reservation land in the create body, the CPU reservation cannot.
	{"Resources.ReservedCPU", api.DeploySpec{Resources: &api.Resources{ReservedCPU: 0.5}}, "ignores a CPU reservation"},
}

// TestApplyWarnsForEverySpecFieldItCannotHonor pins the refusal surface of this
// backend, one field at a time, through the REAL apply path — so a warning that
// exists only in a helper nobody calls cannot pass. Each of these is a field
// another backend honors, so dropping it in silence would let a compose file
// appear to deploy correctly while running something else. Setting each field
// ALONE (not all of them at once) is what makes the test meaningful: a warning
// that only fires as a side effect of some other field being set would pass a
// combined spec and still leave the field silent in the case that matters.
//
// It also asserts EXACTLY ONE warning line per single-field spec, which is the
// half a strings.Contains assertion structurally cannot see. When the
// kubernetes-only warnings moved into deploy.WarnKubernetesOnlyFields, a backend
// that kept its own branch for one of those fields emitted two warnings that
// CONTRADICTED each other, and the suite stayed green because every assertion
// only asked whether some line matched. Counting lines per field catches that
// even when the two messages are worded differently — which the real bug's were,
// and which a duplicate-message counter alone would miss.
func TestApplyWarnsForEverySpecFieldItCannotHonor(t *testing.T) {
	for _, tc := range unsupportedFieldCases {
		t.Run(tc.field, func(t *testing.T) {
			buf := captureLogs(t)
			b := newTestBackend(t, &fakeDocker{})
			spec := tc.spec
			spec.Name = "web"
			spec.Image = "localhost:5000/app:v1"
			if _, err := b.Apply(context.Background(), spec); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("setting %s produced no warning containing %q; got:\n%s", tc.field, tc.want, buf.String())
			}
			if n := countWarnings(buf.String()); n != 1 {
				t.Errorf("setting %s produced %d warnings, want exactly 1 — two warnings for one field say different"+
					" things about the same drop, and the operator cannot tell which is true; got:\n%s", tc.field, n, buf.String())
			}
		})
	}
}

// countWarnings counts WARN records in a captured slog text stream.
func countWarnings(log string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "level=WARN") {
			n++
		}
	}
	return n
}

// supportedSpec is a DeploySpec that exercises every api.DeploySpec field this
// backend realizes — whether in the create body (toCreateBody), through a shared
// helper (deploy.Replicas / RestartPolicy / StopGracePeriodSeconds /
// BuildTelemetryWiring), or, for Egress, outside this backend entirely.
//
// Egress is the one entry that is here for a subtler reason than "mapped".
// "env" mode is realized on the CLIENT by clientproxy.ApplyEgressEnv, which
// merges the proxy variables into spec.Env before the spec ever leaves the
// machine; the relay modes ("proxy"/"transparent") reach this backend as a
// deploy.AttachEgress through ApplyWithEgress/ApplyWithAttachments and are
// realized by a companion caretaker — and the spec that then reaches apply
// STILL carries spec.Egress. So a warning for spec.Egress would fire on the
// path where egress is working, which is why silence is the assertion.
func supportedSpec() api.DeploySpec {
	return api.DeploySpec{
		Name:               "web",
		Image:              "localhost:5000/app:v1",
		Command:            []string{"serve"},
		Entrypoint:         []string{"/bin/entry"},
		Env:                map[string]string{"A": "1"},
		Ports:              []api.PortMapping{{Host: 8080, Container: 80}},
		Mounts:             []api.Mount{{Source: "/srv/data", Target: "/data", ReadOnly: true, SELinux: "z"}},
		Volumes:            []api.VolumeSpec{{Name: "cache", Target: "/var/cache", Driver: "local", Labels: map[string]string{"a": "1"}}},
		Networks:           []api.NetworkAttachment{{Name: "backend", Aliases: []string{"web"}}},
		Telemetry:          &api.TelemetrySpec{Endpoint: "otel-backend:4317"},
		Restart:            "on-failure",
		RestartMaxAttempts: 3,
		Replicas:           2,
		Privileged:         true,
		Healthcheck:        &api.Healthcheck{Test: []string{"CMD", "true"}, Interval: "30s", Retries: 3},
		Resources:          &api.Resources{CPULimit: 1.5, MemoryLimit: 512 << 20, ReservedMemory: 64 << 20},
		User:               "1000:1000",
		WorkingDir:         "/srv",
		Hostname:           "db",
		Labels:             map[string]string{"team": "infra"},
		Origin:             &api.Origin{Project: "proj"},
		StopSignal:         "SIGINT",
		StopGracePeriod:    "30s",
		Init:               boolPtr(true),
		TTY:                true,
		StdinOpen:          true,
		ReadOnly:           true,
		CapAdd:             []string{"NET_ADMIN"},
		CapDrop:            []string{"CHOWN"},
		SecurityOpt:        []string{"no-new-privileges:true"},
		GroupAdd:           []string{"docker"},
		Sysctls:            map[string]string{"net.ipv4.ip_forward": "1"},
		ExtraHosts:         []string{"db:10.0.0.5"},
		DNSServers:         []string{"1.1.1.1"},
		DNSSearch:          []string{"corp.internal"},
		DNSOptions:         []string{"ndots:2"},
		Ulimits:            []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}},
		Tmpfs:              []string{"/run:size=64m"},
		Devices:            []string{"/dev/fuse:/dev/fuse:rwm"},
		ShmSize:            128 << 20,
		PIDMode:            "host",
		IPCMode:            "host",
		Egress:             &api.EgressSpec{Mode: "env"},
	}
}

func boolPtr(b bool) *bool { return &b }

// TestApplyIsSilentForASpecItFullyHonors is the other half of the warn-per-field
// contract: a spec using only supported features must not emit warnings, or the
// warnings become noise operators learn to ignore — and a warning channel
// operators ignore is indistinguishable from the silent drop this backend's
// per-field warnings exist to prevent.
func TestApplyIsSilentForASpecItFullyHonors(t *testing.T) {
	buf := captureLogs(t)
	b := newTestBackend(t, &fakeDocker{})
	b.agentImage = "cornus:latest" // the telemetry collector companion needs one
	if _, err := b.Apply(context.Background(), supportedSpec()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("a fully-supported spec should warn about nothing, got:\n%s", buf.String())
	}
}

// TestSupportedSpecReachesTheCreateBody keeps supportedSpec honest. Silence
// alone cannot tell "this backend honors the field" from "this backend never
// looks at the field": both are quiet. Spot-checking that the spec's values
// actually arrive in the Docker create request is what makes the silence above
// mean something — and it is deliberately spread across the create body's
// separate destinations (Config, HostConfig, the shared-helper fields, the
// networking config) rather than one of them.
func TestSupportedSpecReachesTheCreateBody(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	b.agentImage = "cornus:latest"
	spec := supportedSpec()
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var app *createBody
	for i := range f.created {
		if f.created[i].Labels[labelRole] == "" {
			app = &f.created[i]
			break
		}
	}
	if app == nil {
		t.Fatalf("no app container created (created=%d)", len(f.created))
	}
	// Config-level fields.
	if strings.Join(app.Cmd, " ") != "serve" || strings.Join(app.Entrypoint, " ") != "/bin/entry" {
		t.Errorf("Cmd/Entrypoint = %v / %v", app.Cmd, app.Entrypoint)
	}
	if app.User != "1000:1000" || app.WorkingDir != "/srv" || app.Hostname != "db" || app.StopSignal != "SIGINT" {
		t.Errorf("User/WorkingDir/Hostname/StopSignal = %q/%q/%q/%q", app.User, app.WorkingDir, app.Hostname, app.StopSignal)
	}
	if !app.Tty || !app.OpenStdin {
		t.Errorf("Tty/OpenStdin = %v/%v", app.Tty, app.OpenStdin)
	}
	if app.Healthcheck == nil || app.Healthcheck.Retries != 3 {
		t.Errorf("Healthcheck = %+v", app.Healthcheck)
	}
	// Fields realized through a shared deploy helper rather than a direct
	// assignment — the ones a "does the backend reference spec.X?" grep misses.
	if app.StopTimeout == nil || *app.StopTimeout != 30 {
		t.Errorf("StopTimeout = %v, want 30 (from stopGracePeriod)", app.StopTimeout)
	}
	if app.HostConfig.RestartPolicy.Name != "on-failure" || app.HostConfig.RestartPolicy.MaximumRetryCount != 3 {
		t.Errorf("RestartPolicy = %+v", app.HostConfig.RestartPolicy)
	}
	// HostConfig-level fields.
	hc := app.HostConfig
	if !hc.Privileged || !hc.ReadonlyRootfs || hc.Init == nil || !*hc.Init {
		t.Errorf("Privileged/ReadonlyRootfs/Init = %v/%v/%v", hc.Privileged, hc.ReadonlyRootfs, hc.Init)
	}
	if hc.NanoCpus != int64(1.5*1e9) || hc.Memory != 512<<20 || hc.MemoryReservation != 64<<20 {
		t.Errorf("NanoCpus/Memory/MemoryReservation = %d/%d/%d", hc.NanoCpus, hc.Memory, hc.MemoryReservation)
	}
	if hc.ShmSize != 128<<20 || hc.PidMode != "host" || hc.IpcMode != "host" {
		t.Errorf("ShmSize/PidMode/IpcMode = %d/%q/%q", hc.ShmSize, hc.PidMode, hc.IpcMode)
	}
	if len(hc.CapAdd) != 1 || len(hc.CapDrop) != 1 || len(hc.SecurityOpt) != 1 || len(hc.GroupAdd) != 1 {
		t.Errorf("capAdd/capDrop/securityOpt/groupAdd = %v/%v/%v/%v", hc.CapAdd, hc.CapDrop, hc.SecurityOpt, hc.GroupAdd)
	}
	if hc.Sysctls["net.ipv4.ip_forward"] != "1" || len(hc.ExtraHosts) != 1 {
		t.Errorf("Sysctls/ExtraHosts = %v/%v", hc.Sysctls, hc.ExtraHosts)
	}
	if len(hc.Dns) != 1 || len(hc.DnsSearch) != 1 || len(hc.DnsOptions) != 1 {
		t.Errorf("Dns/DnsSearch/DnsOptions = %v/%v/%v", hc.Dns, hc.DnsSearch, hc.DnsOptions)
	}
	if len(hc.Ulimits) != 1 || hc.Tmpfs["/run"] != "size=64m" || len(hc.Devices) != 1 {
		t.Errorf("Ulimits/Tmpfs/Devices = %v/%v/%v", hc.Ulimits, hc.Tmpfs, hc.Devices)
	}
	if len(hc.Binds) != 1 || !strings.HasSuffix(hc.Binds[0], ":ro,z") {
		t.Errorf("Binds = %v, want the bind with its ro + SELinux options", hc.Binds)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Source != "cache" {
		t.Errorf("managed volume Mounts = %+v", hc.Mounts)
	}
	// Networking config + the loop-count field.
	if hc.NetworkMode != "backend" || app.NetworkingConfig == nil {
		t.Errorf("NetworkMode/NetworkingConfig = %q/%+v", hc.NetworkMode, app.NetworkingConfig)
	}
	apps := 0
	for _, c := range f.created {
		if c.Labels[labelRole] == "" {
			apps++
		}
	}
	if apps != 2 {
		t.Errorf("created %d app containers, want 2 (spec.Replicas)", apps)
	}
}
