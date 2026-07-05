package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// This file is the kubernetes backend's per-field parity story, in the shape
// incushost already carries (pkg/deploy/incushost/spec_linux_test.go):
//
//   - unsupportedFieldCases: one row per api.DeploySpec value this backend cannot
//     honor, with the warning it must produce.
//   - supportedCases: the specs it fully honors, which it must be COMPLETELY
//     SILENT for, each with an assertion that the realization actually landed (so
//     silence can never be achieved by ignoring the field).
//
// specfield_coverage_test.go then asserts by reflection that every exported
// DeploySpec field is exercised by one or the other, so a newly added field cannot
// slide into silence. warn_once_test.go asserts no message is emitted twice.
//
// Kubernetes is the most capable backend, so the supported half is deliberately
// the big one — that is the finding, and pinning it is what makes a future
// REGRESSION (a mapped field quietly becoming a warning, or a warning quietly
// becoming a silent drop) visible.

// unsupportedFieldCases enumerates, one entry per api.DeploySpec value this
// backend cannot honor, a spec that sets ONLY that value and the warning it must
// produce. Setting each ALONE (not all at once) is what makes it meaningful: a
// warning that only fires as a side effect of some other field being set would
// pass a combined spec and still leave the field silent in the case that matters.
//
// Adding a field to api.DeploySpec therefore means doing one of two things here:
// mapping it (and adding it to supportedCases, which asserts the backend stays
// silent and that the mapping landed) or adding a row.
var unsupportedFieldCases = []struct {
	field  string
	spec   api.DeploySpec       // merged onto a minimal name+image spec
	mounts []deploy.AttachMount // non-nil routes through ApplyWithAttachments
	want   string               // substring the warning must contain
}{
	// --- values whose refusal is emitted at the translation site (deployment()).
	{field: "User (username form)", spec: api.DeploySpec{User: "app"},
		want: "compose user is a username"},
	{field: "SecurityOpt (seccomp profile)", spec: api.DeploySpec{SecurityOpt: []string{"seccomp=unconfined"}},
		want: "security_opt is not mapped to securityContext"},
	{field: "SecurityOpt (valueless label)", spec: api.DeploySpec{SecurityOpt: []string{"label=disable"}},
		want: "security_opt label option is not mapped to SELinuxOptions"},
	{field: "GroupAdd (group name)", spec: api.DeploySpec{GroupAdd: []string{"docker"}},
		want: "group_add entry is a group name"},
	{field: "Tmpfs (mount options)", spec: api.DeploySpec{Tmpfs: []string{"/run:size=64m"}},
		want: "tmpfs mount options are not supported on an emptyDir"},
	{field: "PIDMode (non-host form)", spec: api.DeploySpec{PIDMode: "service:db"},
		want: "pid mode is not supported"},
	{field: "IPCMode (non-host form)", spec: api.DeploySpec{IPCMode: "shareable"},
		want: "ipc mode is not supported"},
	{field: "Ulimits", spec: api.DeploySpec{Ulimits: []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 4096}}},
		want: "ulimits are not supported"},
	{field: "Devices", spec: api.DeploySpec{Devices: []string{"/dev/fuse:/dev/fuse:rwm"}},
		want: "devices are not supported"},
	{field: "DNS (user-net records with no Multus fabric)", spec: api.DeploySpec{
		DNS: &api.DNSSpec{Records: map[string]string{"db": "10.222.0.5"}, RequireUserNet: true}},
		want: "dropping user-network DNS records"},

	// --- values that were dropped in TOTAL SILENCE until warnUnsupportedFields.
	{field: "StopSignal", spec: api.DeploySpec{StopSignal: "SIGINT"},
		want: "ignores stopSignal"},
	{field: "Init", spec: api.DeploySpec{Init: ptr.To(true)},
		want: "ignores init"},
	{field: "Ports (HostIP)", spec: api.DeploySpec{
		Ports: []api.PortMapping{{Host: 8080, Container: 80, HostIP: "127.0.0.1"}}},
		want: "ignores a published port's host address"},
	{field: "Volumes (driver / driver_opts)", spec: api.DeploySpec{
		Volumes: []api.VolumeSpec{{Name: "cache", Target: "/data", Driver: "local", DriverOpts: map[string]string{"type": "nfs"}}}},
		want: "ignores a managed volume's driver"},
	{field: "Healthcheck (startInterval)", spec: api.DeploySpec{
		Healthcheck: &api.Healthcheck{Test: []string{"CMD", "true"}, StartInterval: "1s"}},
		want: "ignores healthcheck startInterval"},
	{field: "Mounts (SELinux relabel)", spec: api.DeploySpec{
		Mounts: []api.Mount{{Source: "/host/data", Target: "/data", SELinux: "z"}}},
		mounts: []deploy.AttachMount{{Target: "/data", Name: "m0", Session: "s1", RelayURL: "http://cornus"}},
		want:   "ignores the SELinux relabel on a mount"},

	// --- NetworkAttachment SUB-fields. api.DeploySpec.Networks itself is mapped, so
	// the reflection field-coverage guards see it as covered and stop there; these
	// four rows are the level down they structurally cannot reach. Each was verified
	// unread by every netdriver pipeline (services, bridge/ipvlan/macvlan, policy,
	// cilium) before being called a drop.
	{field: "Networks (internal)", spec: api.DeploySpec{
		Networks: []api.NetworkAttachment{{Name: "backend", Internal: true}}},
		want: "ignores a network's internal flag"},
	{field: "Networks (ipam gateway / ip_range)", spec: api.DeploySpec{
		Networks: []api.NetworkAttachment{{Name: "backend", Gateway: "10.5.0.1", IPRange: "10.5.0.0/28"}}},
		want: "ignores a network's ipam gateway/ip_range"},
	{field: "Networks (IPv6)", spec: api.DeploySpec{
		Networks: []api.NetworkAttachment{{Name: "backend", EnableIPv6: true, IPv6: "fd00::5"}}},
		want: "ignores a network's IPv6 configuration"},
	{field: "Networks (attachable / priority / mac / labels)", spec: api.DeploySpec{
		Networks: []api.NetworkAttachment{{Name: "backend", Attachable: true, Priority: 10,
			MAC: "02:42:ac:11:00:02", Labels: map[string]string{"tier": "backend"}}}},
		want: "ignores a network's attachable/priority/mac/labels"},

	// --- realized ONLY from a deploy-attach session, so a stateless deploy carrying
	// them (a raw POST /.cornus/v1/deploy) drops them. Both are in supportedCases too,
	// on the attachment path, where they ARE realized.
	{field: "Credentials (stateless apply)", spec: api.DeploySpec{
		Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{Name: "aws", Backend: "aws-sts"}}}},
		want: "ignores credentials on the stateless deploy path"},
	{field: "Egress (stateless apply, relay mode)", spec: api.DeploySpec{
		Egress: &api.EgressSpec{Mode: "proxy", Default: "client"}},
		want: "ignores egress on the stateless deploy path"},
}

// applyCase runs one table spec through the REAL apply path (a fake clientset, no
// live cluster), so the warnings asserted here are the ones an operator actually
// gets — not the output of a helper only the test calls.
func applyCase(t *testing.T, b *Backend, spec api.DeploySpec, mounts []deploy.AttachMount) {
	t.Helper()
	var err error
	if len(mounts) > 0 {
		_, err = b.ApplyWithAttachments(context.Background(), spec, mounts, nil, nil)
	} else {
		_, err = b.Apply(context.Background(), spec)
	}
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
}

// TestApplyWarnsForEverySpecFieldItCannotHonor pins the refusal surface of this
// backend, one value at a time. Each of these is something another backend honors,
// so dropping it in silence would let a compose file appear to deploy correctly
// while running something else — invisible to every other gate: the build passes,
// the tests pass, the deploy succeeds, and the workload is not what the operator
// asked for.
func TestApplyWarnsForEverySpecFieldItCannotHonor(t *testing.T) {
	for _, tc := range unsupportedFieldCases {
		t.Run(tc.field, func(t *testing.T) {
			buf := captureLogs(t)
			b := NewWithClient(fake.NewSimpleClientset(), "default")
			spec := tc.spec
			spec.Name = "web"
			spec.Image = "localhost:5000/app:v1"
			applyCase(t, b, spec, tc.mounts)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("setting %s produced no warning containing %q; got:\n%s", tc.field, tc.want, buf.String())
			}
		})
	}
}

// supportedCase is one spec this backend FULLY honors. Silence alone is a weak
// contract — a backend that ignores a field is silent about it too — so every case
// also proves the realization landed.
type supportedCase struct {
	name string
	// backend builds the backend the case needs (a Knative case needs a dynamic
	// client and Knative discovery; the rest do not).
	backend func(t *testing.T) *Backend
	spec    api.DeploySpec
	mounts  []deploy.AttachMount
	creds   []deploy.AttachCredential
	egress  *deploy.AttachEgress
	// verify asserts the spec's fields actually reached the cluster objects.
	verify func(t *testing.T, b *Backend)
}

func plainBackend(t *testing.T) *Backend {
	t.Helper()
	t.Setenv("CORNUS_K8S_SIDECAR_IMAGE", "cornus:test")
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	b.allowPrivileged = true // spec.Privileged is policy-gated, not unsupported
	return b
}

func getDeployment(t *testing.T, b *Backend, name string) *appsv1.Deployment {
	t.Helper()
	dep, err := b.clientset.AppsV1().Deployments(b.namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment %s: %v", name, err)
	}
	return dep
}

// supportedCases is the MAPPED half of the story. Kubernetes realizes almost the
// whole DeploySpec, and the cases are split only where a single spec cannot carry
// two features at once (the enforcing proxy excludes the DNS/hub/docker roles;
// Knative excludes volumes, networks, mounts and ingress; a one-shot is a Job, not
// a Deployment).
func supportedCases() []supportedCase {
	return []supportedCase{
		{
			name:    "container, pod and workload fields",
			backend: plainBackend,
			spec: api.DeploySpec{
				Name:            "web",
				Image:           "localhost:5000/app:v1",
				Entrypoint:      []string{"/bin/entry"},
				Command:         []string{"serve", "--port=80"},
				Env:             map[string]string{"A": "1"},
				Ports:           []api.PortMapping{{Host: 8080, Container: 80}},
				Volumes:         []api.VolumeSpec{{Target: "/var/cache"}, {Name: "shared", Target: "/var/lib/shared", Size: "2Gi", StorageClass: "fast", ReadOnly: true, Labels: map[string]string{"team": "infra"}}},
				Networks:        []api.NetworkAttachment{{Name: "proj_backend", Aliases: []string{"web"}}},
				Restart:         "always",
				Replicas:        3,
				Privileged:      true,
				Healthcheck:     &api.Healthcheck{Test: []string{"CMD", "true"}, Interval: "5s", Timeout: "2s", StartPeriod: "1s", Retries: 3},
				Resources:       &api.Resources{CPULimit: 1.5, MemoryLimit: 512 << 20, ReservedCPU: 0.5, ReservedMemory: 128 << 20},
				UpdateConfig:    &api.UpdateConfig{Parallelism: 2, Order: "start-first"},
				User:            "1000:2000",
				WorkingDir:      "/srv/app",
				Hostname:        "web-0",
				Labels:          map[string]string{"team": "infra"},
				Origin:          &api.Origin{Project: "proj", Host: "dev-box", User: "alice"},
				StopGracePeriod: "45s",
				TTY:             true,
				StdinOpen:       true,
				ReadOnly:        true,
				CapAdd:          []string{"NET_ADMIN"},
				CapDrop:         []string{"CHOWN"},
				SecurityOpt:     []string{"no-new-privileges:true", "label=level:s0:c1"},
				GroupAdd:        []string{"2000"},
				Sysctls:         map[string]string{"net.core.somaxconn": "1024"},
				ExtraHosts:      []string{"db:10.0.0.5"},
				DNSServers:      []string{"1.1.1.1"},
				DNSSearch:       []string{"corp.internal"},
				DNSOptions:      []string{"ndots:2"},
				Tmpfs:           []string{"/run"},
				ShmSize:         128 << 20,
				PIDMode:         "host",
				IPCMode:         "host",
			},
			verify: func(t *testing.T, b *Backend) {
				dep := getDeployment(t, b, "web")
				pod := dep.Spec.Template.Spec
				c := pod.Containers[0]
				if got := strings.Join(c.Command, " "); got != "/bin/entry" {
					t.Errorf("container.Command = %q, want the spec entrypoint", got)
				}
				if got := strings.Join(c.Args, " "); got != "serve --port=80" {
					t.Errorf("container.Args = %q, want the spec command", got)
				}
				if c.WorkingDir != "/srv/app" || !c.TTY || !c.Stdin {
					t.Errorf("workingDir/tty/stdin not mapped: %q %v %v", c.WorkingDir, c.TTY, c.Stdin)
				}
				if sc := c.SecurityContext; sc == nil || sc.Privileged == nil || sc.ReadOnlyRootFilesystem == nil ||
					sc.RunAsUser == nil || *sc.RunAsUser != 1000 || sc.RunAsGroup == nil || *sc.RunAsGroup != 2000 ||
					sc.Capabilities == nil || len(sc.Capabilities.Add) != 1 || sc.AllowPrivilegeEscalation == nil ||
					sc.SELinuxOptions == nil {
					t.Errorf("container securityContext incomplete: %+v", sc)
				}
				if c.LivenessProbe == nil || c.ReadinessProbe == nil {
					t.Error("healthcheck did not become liveness+readiness probes")
				}
				if c.Resources.Limits == nil || c.Resources.Requests == nil {
					t.Errorf("resources not mapped: %+v", c.Resources)
				}
				if *dep.Spec.Replicas != 3 {
					t.Errorf("replicas = %d, want 3", *dep.Spec.Replicas)
				}
				if dep.Spec.Strategy.RollingUpdate == nil {
					t.Errorf("updateConfig did not become a rolling-update strategy: %+v", dep.Spec.Strategy)
				}
				if pod.Hostname != "web-0" || pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != 45 {
					t.Errorf("hostname/stopGracePeriod not mapped: %q %v", pod.Hostname, pod.TerminationGracePeriodSeconds)
				}
				if !pod.HostPID || !pod.HostIPC {
					t.Error("pid/ipc host modes not mapped")
				}
				if len(pod.HostAliases) != 1 || pod.DNSConfig == nil || len(pod.DNSConfig.Nameservers) != 1 {
					t.Errorf("extra_hosts / dns not mapped: %+v %+v", pod.HostAliases, pod.DNSConfig)
				}
				if psc := pod.SecurityContext; psc == nil || len(psc.SupplementalGroups) != 1 || len(psc.Sysctls) != 1 {
					t.Errorf("pod securityContext (group_add/sysctls) incomplete: %+v", psc)
				}
				if dep.Spec.Template.Annotations["team"] != "infra" {
					t.Errorf("compose labels did not become pod annotations: %v", dep.Spec.Template.Annotations)
				}
				if dep.Annotations[deploy.LabelOriginProject] != "proj" {
					t.Errorf("origin did not become deployment annotations: %v", dep.Annotations)
				}
				// Volumes: two PVCs, plus the tmpfs and shm emptyDirs.
				for _, name := range []string{"web-vol-0"} {
					if _, err := b.clientset.CoreV1().PersistentVolumeClaims(b.namespace).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
						t.Errorf("managed volume did not become a PVC: %v", err)
					}
				}
				var tmpfs, shm bool
				for _, v := range pod.Volumes {
					if v.EmptyDir == nil {
						continue
					}
					if v.Name == "cornus-shm" {
						shm = true
					}
					if strings.HasPrefix(v.Name, "tmpfs-") {
						tmpfs = true
					}
				}
				if !tmpfs || !shm {
					t.Errorf("tmpfs/shm_size did not become memory emptyDirs: %+v", pod.Volumes)
				}
				// Ports become a ClusterIP Service; networks become the services-driver
				// alias Service.
				if _, err := b.clientset.CoreV1().Services(b.namespace).Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
					t.Errorf("published port did not become a Service: %v", err)
				}
				var netLabel bool
				for k := range dep.Spec.Template.Labels {
					if strings.HasPrefix(k, "cornus.net/") {
						netLabel = true
					}
				}
				if !netLabel {
					t.Errorf("network membership label missing: %v", dep.Spec.Template.Labels)
				}
			},
		},
		{
			// The caretaker roles that fold into ONE sidecar: hub (which subsumes the
			// DNS role), the docker endpoint, telemetry and agent forwarding.
			name:    "caretaker roles (hub, dns, docker, telemetry, agentForward)",
			backend: plainBackend,
			spec: api.DeploySpec{
				Name:         "app",
				Image:        "localhost:5000/app:v1",
				DNS:          &api.DNSSpec{Records: map[string]string{"peer": "10.0.0.9"}},
				Hub:          &api.HubSpec{Identity: "app", Import: []api.HubImport{{Name: "db", Ports: []int{5432}}}},
				Docker:       &api.DockerSpec{Transport: "tcp"},
				Telemetry:    &api.TelemetrySpec{Enabled: true, Endpoint: "http://otel:4318", Protocol: "http/protobuf"},
				AgentForward: true,
			},
			verify: func(t *testing.T, b *Backend) {
				dep := getDeployment(t, b, "app")
				pod := dep.Spec.Template.Spec
				var caretakers int
				for _, c := range pod.InitContainers {
					if c.Name == "cornus-caretaker" {
						caretakers++
					}
				}
				if caretakers != 1 {
					t.Fatalf("want exactly one caretaker sidecar, got %d: %+v", caretakers, pod.InitContainers)
				}
				if pod.DNSConfig == nil || len(pod.DNSConfig.Nameservers) == 0 || pod.DNSConfig.Nameservers[0] != "127.0.0.1" {
					t.Errorf("pod resolver not pointed at the caretaker: %+v", pod.DNSConfig)
				}
				env := map[string]string{}
				for _, e := range pod.Containers[0].Env {
					env[e.Name] = e.Value
				}
				if env["DOCKER_HOST"] == "" {
					t.Errorf("docker endpoint not injected: %v", env)
				}
				if env["OTEL_EXPORTER_OTLP_ENDPOINT"] == "" {
					t.Errorf("telemetry endpoint not injected: %v", env)
				}
				if dep.Annotations[agentForwardAnnotation] != "true" {
					t.Errorf("agentForward not recorded: %v", dep.Annotations)
				}
			},
		},
		{
			name:    "enforcing proxy",
			backend: plainBackend,
			spec: api.DeploySpec{
				Name:  "web",
				Image: "localhost:5000/app:v1",
				Proxy: &api.ProxySpec{Mode: "enforcing", Allow: []string{"db"}},
			},
			verify: func(t *testing.T, b *Backend) {
				pod := getDeployment(t, b, "web").Spec.Template.Spec
				var caretaker, redirect bool
				for _, c := range pod.InitContainers {
					switch c.Name {
					case "cornus-caretaker":
						caretaker = true
					case "cornus-net-redirect":
						redirect = true
					}
				}
				if !caretaker || !redirect {
					t.Errorf("enforcing proxy did not inject a caretaker + redirect init: %+v", pod.InitContainers)
				}
			},
		},
		{
			// A run-to-completion spec is a Job, not a Deployment, and
			// RestartMaxAttempts is its backoffLimit — the one place kubernetes can
			// bound a restart count.
			name:    "one-shot workload (restart + restartMaxAttempts)",
			backend: plainBackend,
			spec: api.DeploySpec{
				Name:               "migrate",
				Image:              "localhost:5000/app:v1",
				Restart:            "on-failure",
				RestartMaxAttempts: 4,
			},
			verify: func(t *testing.T, b *Backend) {
				job, err := b.clientset.BatchV1().Jobs(b.namespace).Get(context.Background(), "migrate", metav1.GetOptions{})
				if err != nil {
					t.Fatalf("one-shot spec did not become a Job: %v", err)
				}
				if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 4 {
					t.Errorf("restartMaxAttempts did not become the backoffLimit: %v", job.Spec.BackoffLimit)
				}
			},
		},
		{
			name:    "ingress",
			backend: plainBackend,
			spec: api.DeploySpec{
				Name:    "web",
				Image:   "localhost:5000/app:v1",
				Ports:   []api.PortMapping{{Host: 8080, Container: 80}},
				Ingress: &api.IngressSpec{Enabled: true, Hosts: []string{"web.example.com"}},
			},
			verify: func(t *testing.T, b *Backend) {
				ing, err := b.clientset.NetworkingV1().Ingresses(b.namespace).Get(context.Background(), "web", metav1.GetOptions{})
				if err != nil {
					t.Fatalf("ingress spec did not become an Ingress: %v", err)
				}
				if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != "web.example.com" {
					t.Errorf("ingress host not routed: %+v", ing.Spec.Rules)
				}
			},
		},
		{
			name:    "knative service",
			backend: func(t *testing.T) *Backend { b, _ := newKnativeBackend(t, true); return b },
			spec: api.DeploySpec{
				Name:    "hello",
				Image:   "localhost:5000/app:v1",
				Ports:   []api.PortMapping{{Host: 8080, Container: 8080}},
				Knative: &api.KnativeSpec{Enabled: true, MinScale: ptr.To(1), MaxScale: ptr.To(5)},
			},
			verify: func(t *testing.T, b *Backend) {
				if _, err := b.dyn.Resource(knativeServiceGVR).Namespace(b.namespace).
					Get(context.Background(), "hello", metav1.GetOptions{}); err != nil {
					t.Fatalf("knative spec did not become a ksvc: %v", err)
				}
			},
		},
		{
			// The attachment path: client-local mounts, client-sourced credentials and
			// client-side egress all land in the SAME caretaker sidecar.
			name:    "attachments (mounts, credentials, egress)",
			backend: plainBackend,
			spec: api.DeploySpec{
				Name:        "app",
				Image:       "localhost:5000/app:v1",
				Mounts:      []api.Mount{{Source: "/host/src", Target: "/src", ReadOnly: true}},
				Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{Name: "aws", Backend: "aws-sts"}}},
				Egress:      &api.EgressSpec{Mode: "proxy", Default: "client"},
			},
			mounts: []deploy.AttachMount{{Target: "/src", ReadOnly: true, Name: "m0", Session: "s1", RelayURL: "http://cornus"}},
			creds: []deploy.AttachCredential{{
				Name: "aws", Session: "s1", RelayURL: "http://cornus",
				EnvVars: []deploy.CredentialEnvVar{{Var: "AWS_SESSION_TOKEN", Value: "tok"}},
			}},
			egress: &deploy.AttachEgress{
				Session: "s1", RelayURL: "http://cornus",
				Spec: &api.EgressSpec{Mode: "proxy", Default: "client"},
			},
			verify: func(t *testing.T, b *Backend) {
				pod := getDeployment(t, b, "app").Spec.Template.Spec
				var found bool
				for _, c := range pod.InitContainers {
					if c.Name == "cornus-caretaker" {
						found = true
					}
				}
				if !found {
					t.Fatalf("attachments did not inject a caretaker: %+v", pod.InitContainers)
				}
				var mounted, credEnv, proxyEnv bool
				for _, m := range pod.Containers[0].VolumeMounts {
					if m.MountPath == "/src" {
						mounted = true
					}
				}
				for _, e := range pod.Containers[0].Env {
					if e.Name == "AWS_SESSION_TOKEN" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
						credEnv = true
					}
					if e.Name == "HTTPS_PROXY" {
						proxyEnv = true
					}
				}
				if !mounted || !credEnv || !proxyEnv {
					t.Errorf("mount=%v credential=%v egress=%v not all realized", mounted, credEnv, proxyEnv)
				}
			},
		},
	}
}

// TestApplyIsSilentForSpecsItFullyHonors is the other half of the warn-per-field
// contract: a spec using only supported features must not emit warnings, or the
// warnings become noise operators learn to ignore — and a warning channel
// operators ignore is indistinguishable from the silent drop the per-field
// warnings exist to prevent. Each case also asserts the fields actually landed, so
// "silent" can never be satisfied by not implementing them.
func TestApplyIsSilentForSpecsItFullyHonors(t *testing.T) {
	for _, tc := range supportedCases() {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.backend(t)
			buf := captureLogs(t)
			var err error
			if tc.mounts != nil || tc.creds != nil || tc.egress != nil {
				_, err = b.ApplyWithAttachments(context.Background(), tc.spec, tc.mounts, tc.creds, tc.egress)
			} else {
				_, err = b.Apply(context.Background(), tc.spec)
			}
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if strings.Contains(buf.String(), "level=WARN") {
				t.Errorf("a fully-supported spec should warn about nothing, got:\n%s", buf.String())
			}
			tc.verify(t, b)
		})
	}
}

// TestKnativeDropsExtraPortsLoudly is the regression test for a documented
// warning that did not exist: api.KnativeSpec promises "additional published ports
// are dropped with a warning", and a revision genuinely exposes only one port, but
// chooseKnativePort dropped the rest in silence — so a two-listener service
// deployed as a ksvc lost a listener with nothing said.
func TestKnativeDropsExtraPortsLoudly(t *testing.T) {
	b, _ := newKnativeBackend(t, true)
	buf := captureLogs(t)
	if _, err := b.Apply(context.Background(), api.DeploySpec{
		Name:    "hello",
		Image:   "localhost:5000/app:v1",
		Ports:   []api.PortMapping{{Host: 8080, Container: 8080}, {Host: 9090, Container: 9090}},
		Knative: &api.KnativeSpec{Enabled: true},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(buf.String(), "a revision exposes exactly one container port") {
		t.Errorf("extra knative ports were dropped in silence; got:\n%s", buf.String())
	}
}

// TestKnativeDropsReplicasLoudly covers the second field a ksvc silently
// overrides. A Knative revision has no replica count — the autoscaler owns it —
// so `replicas: 3` alongside `knative: {enabled: true}` does not give three
// instances; it gives autoscaling WITH SCALE-TO-ZERO, i.e. an idle workload
// drops to none. That is the inverse of what the field asked for, and it
// presents as "the service keeps cold-starting" rather than as a config error,
// so it is the least likely symptom to be traced back to a dropped field.
//
// The warning fires only above 1, which is what keeps it off the path of a user
// who wrote a ksvc the Knative way: spec.Replicas is 0 when unset and
// deploy.Replicas() maps <=0 to 1. TestKnativeHonorsReplicasOfOneSilently below
// pins that half, because a warning nobody can avoid is one everybody learns to
// ignore.
func TestKnativeDropsReplicasLoudly(t *testing.T) {
	b, _ := newKnativeBackend(t, true)
	buf := captureLogs(t)
	if _, err := b.Apply(context.Background(), api.DeploySpec{
		Name:     "hello",
		Image:    "localhost:5000/app:v1",
		Replicas: 3,
		Knative:  &api.KnativeSpec{Enabled: true},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(buf.String(), "spec.Replicas is ignored") {
		t.Errorf("replicas were dropped in silence under knative; got:\n%s", buf.String())
	}
}

// TestKnativeHonorsReplicasOfOneSilently is the other half of the contract above
// and the reason the threshold is `> 1` rather than `!= 0`. An unset replica
// count (0) and an explicit `replicas: 1` both mean exactly what a ksvc already
// does — one instance when there is traffic — so neither is a dropped intent and
// neither may warn. Without this, the natural "warn whenever Replicas is set"
// implementation would fire on every compose project that writes `replicas: 1`,
// and the loud case above would drown in it.
func TestKnativeHonorsReplicasOfOneSilently(t *testing.T) {
	for _, replicas := range []int{0, 1} {
		t.Run(fmt.Sprintf("replicas=%d", replicas), func(t *testing.T) {
			b, _ := newKnativeBackend(t, true)
			buf := captureLogs(t)
			if _, err := b.Apply(context.Background(), api.DeploySpec{
				Name:     "hello",
				Image:    "localhost:5000/app:v1",
				Replicas: replicas,
				Knative:  &api.KnativeSpec{Enabled: true},
			}); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if strings.Contains(buf.String(), "spec.Replicas is ignored") {
				t.Errorf("replicas=%d is what a ksvc does anyway and must not warn; got:\n%s", replicas, buf.String())
			}
		})
	}
}
