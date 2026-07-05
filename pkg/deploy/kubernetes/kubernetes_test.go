package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
)

func TestApplyCreatesDeploymentAndService(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:     "web",
		Image:    "localhost:5000/web:v1",
		Replicas: 3,
		Env:      map[string]string{"LOG": "info"},
		Ports:    []api.PortMapping{{Host: 8080, Container: 80}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if *dep.Spec.Replicas != 3 {
		t.Fatalf("replicas = %d", *dep.Spec.Replicas)
	}
	if dep.Labels["cornus.app"] != "web" || dep.Labels["cornus.managed"] != "true" {
		t.Fatalf("labels = %v", dep.Labels)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if c.Image != "localhost:5000/web:v1" || len(c.Ports) != 1 || c.Ports[0].ContainerPort != 80 {
		t.Fatalf("container = %+v", c)
	}

	if _, err := cs.CoreV1().Services("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("get service: %v", err)
	}

	list, err := b.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("List = %v, %v", list, err)
	}
}

// TestStatusOfSurfacesWedgeDiagnostics verifies statusOf attaches a per-instance
// Message explaining why a non-running instance is stuck: a crash-looping
// sidecar (init container), a wedged app container, or an unschedulable pod. This
// is the signal the deploy-attach readiness wait streams to the caller so a
// crash loop is reported instead of the session silently claiming success.
func TestStatusOfSurfacesWedgeDiagnostics(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "mnt"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: execContainer, Image: "cornus:e2e"}},
			}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	waiting := func(reason, msg string) corev1.ContainerState {
		return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}
	}
	cases := []struct {
		name string
		pod  corev1.Pod
		want string
	}{
		{
			name: "crash-looping sidecar",
			pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mnt-0"}, Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					Name: "cornus-caretaker", State: waiting("CrashLoopBackOff", "back-off 5m0s restarting failed container"),
				}},
			}},
			want: "cornus-caretaker: CrashLoopBackOff: back-off 5m0s restarting failed container",
		},
		{
			name: "image pull error on app",
			pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mnt-0"}, Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: execContainer, State: waiting("ImagePullBackOff", "Back-off pulling image"),
				}},
			}},
			want: "app: ImagePullBackOff: Back-off pulling image",
		},
		{
			name: "unschedulable pod",
			pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mnt-0"}, Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes are available",
				}},
			}},
			want: "pod: Unschedulable: 0/3 nodes are available",
		},
		{
			name: "benign ContainerCreating yields no diagnostic",
			pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "mnt-0"}, Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: execContainer, State: waiting("ContainerCreating", ""),
				}},
			}},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := statusOf(dep, []corev1.Pod{tc.pod}, "kubernetes")
			if len(st.Instances) != 1 {
				t.Fatalf("instances = %d, want 1", len(st.Instances))
			}
			if st.Instances[0].Running {
				t.Fatal("instance must not be running (ReadyReplicas 0)")
			}
			if got := st.Instances[0].Message; got != tc.want {
				t.Errorf("Message = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServiceDualProtocolPortNames guards against duplicate ServicePort names:
// a DNS-style spec publishing the same container port on tcp and udp must yield
// two ports with distinct, valid names, or the API server rejects the Service
// with spec.ports[1].name: Duplicate value.
func TestServiceDualProtocolPortNames(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	svc := b.service(api.DeploySpec{
		Name: "dns",
		Ports: []api.PortMapping{
			{Host: 53, Container: 53, Protocol: "tcp"},
			{Host: 53, Container: 53, Protocol: "udp"},
		},
	})
	if svc == nil {
		t.Fatal("service() = nil, want a Service with two ports")
	}
	if len(svc.Spec.Ports) != 2 {
		t.Fatalf("ports = %d, want 2", len(svc.Spec.Ports))
	}
	seen := map[string]bool{}
	for _, p := range svc.Spec.Ports {
		if p.Name == "" {
			t.Errorf("port %d has empty name (invalid in a multi-port Service)", p.Port)
		}
		if seen[p.Name] {
			t.Errorf("duplicate ServicePort name %q", p.Name)
		}
		seen[p.Name] = true
	}
	if svc.Spec.Ports[0].Name == svc.Spec.Ports[1].Name {
		t.Errorf("tcp and udp ports share name %q", svc.Spec.Ports[0].Name)
	}
}

// TestApplyEntrypoint confirms an explicit entrypoint takes the container
// Command (ENTRYPOINT) slot with spec.Command as its Args, while a
// command-only spec puts spec.Command in Args and leaves Command unset so the
// image ENTRYPOINT is preserved (Docker semantics; matches dockerhost and
// containerd).
func TestApplyEntrypoint(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:       "dc",
		Image:      "img",
		Entrypoint: []string{"/bin/sh"},
		Command:    []string{"-c", "sleep infinity"},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "dc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if len(c.Command) != 1 || c.Command[0] != "/bin/sh" {
		t.Fatalf("command = %v, want [/bin/sh]", c.Command)
	}
	if len(c.Args) != 2 || c.Args[0] != "-c" {
		t.Fatalf("args = %v, want [-c, sleep infinity]", c.Args)
	}

	plain := api.DeploySpec{Name: "plain", Image: "img", Command: []string{"run"}}
	if _, err := b.Apply(ctx, plain); err != nil {
		t.Fatalf("Apply plain: %v", err)
	}
	dep, err = cs.AppsV1().Deployments("default").Get(ctx, "plain", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get plain deployment: %v", err)
	}
	c = dep.Spec.Template.Spec.Containers[0]
	if len(c.Command) != 0 || len(c.Args) != 1 || c.Args[0] != "run" {
		t.Fatalf("plain command/args = %v/%v, want []/[run]", c.Command, c.Args)
	}
}

func TestApplyPrivileged(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	// Default: no securityContext on the app container.
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "plain", Image: "img"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "plain", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sc := dep.Spec.Template.Spec.Containers[0].SecurityContext; sc != nil && sc.Privileged != nil && *sc.Privileged {
		t.Fatal("app container should not be privileged by default")
	}

	// Default-deny: a privileged spec is rejected (the API is unauthenticated).
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "priv", Image: "img", Privileged: true}); err == nil {
		t.Fatal("Apply should reject a privileged spec by default")
	} else if !strings.Contains(err.Error(), "privileged containers are disabled") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Opt-in (CORNUS_ALLOW_PRIVILEGED): securityContext.privileged=true on the
	// app container.
	b.allowPrivileged = true
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "priv", Image: "img", Privileged: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err = cs.AppsV1().Deployments("default").Get(ctx, "priv", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	sc := dep.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Fatalf("app container SecurityContext = %+v, want Privileged=true", sc)
	}
}

// TestCommonKeysDeployment asserts the batch of common runtime keys map onto
// the container/pod/securityContext where kubernetes can express them.
func TestCommonKeysDeployment(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{
		Name:            "web",
		Image:           "img",
		User:            "1000:2000",
		WorkingDir:      "/srv",
		Hostname:        "web-host",
		Labels:          map[string]string{"role": "web"},
		StopGracePeriod: "1m30s",
		TTY:             true,
		StdinOpen:       true,
		ReadOnly:        true,
	})
	ctr := dep.Spec.Template.Spec.Containers[0]
	if ctr.WorkingDir != "/srv" {
		t.Errorf("WorkingDir = %q, want /srv", ctr.WorkingDir)
	}
	if !ctr.TTY {
		t.Error("TTY = false, want true")
	}
	if !ctr.Stdin {
		t.Error("Stdin = false, want true")
	}
	sc := ctr.SecurityContext
	if sc == nil {
		t.Fatal("expected a securityContext")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("ReadOnlyRootFilesystem = %v, want true", sc.ReadOnlyRootFilesystem)
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 1000 {
		t.Errorf("RunAsUser = %v, want 1000", sc.RunAsUser)
	}
	if sc.RunAsGroup == nil || *sc.RunAsGroup != 2000 {
		t.Errorf("RunAsGroup = %v, want 2000", sc.RunAsGroup)
	}
	podSpec := dep.Spec.Template.Spec
	if podSpec.Hostname != "web-host" {
		t.Errorf("pod Hostname = %q, want web-host", podSpec.Hostname)
	}
	if podSpec.TerminationGracePeriodSeconds == nil || *podSpec.TerminationGracePeriodSeconds != 90 {
		t.Errorf("TerminationGracePeriodSeconds = %v, want 90", podSpec.TerminationGracePeriodSeconds)
	}
	// compose labels ride as pod-template ANNOTATIONS, not labels.
	if got := dep.Spec.Template.Annotations["role"]; got != "web" {
		t.Errorf("pod annotation role = %q, want web", got)
	}
	if _, ok := dep.Spec.Template.Labels["role"]; ok {
		t.Error("compose labels must not become pod labels")
	}
}

// TestResourceKeysDeployment asserts the resource & host-namespace batch maps
// onto the pod: tmpfs / shm_size -> memory-backed emptyDir volumes + mounts, and
// pid/ipc "host" -> HostPID / HostIPC. ulimits and devices have no equivalent.
func TestResourceKeysDeployment(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{
		Name:    "web",
		Image:   "img",
		Tmpfs:   []string{"/run", "/tmp:size=64m"},
		ShmSize: 128 * 1024 * 1024,
		PIDMode: "host",
		IPCMode: "host",
		Ulimits: []api.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, // ignored
		Devices: []string{"/dev/null"},                                  // ignored
	})
	podSpec := dep.Spec.Template.Spec
	if !podSpec.HostPID {
		t.Error("HostPID = false, want true for pid: host")
	}
	if !podSpec.HostIPC {
		t.Error("HostIPC = false, want true for ipc: host")
	}

	// tmpfs + shm -> memory-backed emptyDir volumes.
	byName := map[string]corev1.Volume{}
	for _, v := range podSpec.Volumes {
		byName[v.Name] = v
	}
	for _, name := range []string{"tmpfs-0", "tmpfs-1", "cornus-shm"} {
		v, ok := byName[name]
		if !ok {
			t.Fatalf("volume %q missing: %+v", name, podSpec.Volumes)
		}
		if v.EmptyDir == nil || v.EmptyDir.Medium != corev1.StorageMediumMemory {
			t.Errorf("volume %q is not a memory emptyDir: %+v", name, v.VolumeSource)
		}
	}
	if sl := byName["cornus-shm"].EmptyDir.SizeLimit; sl == nil || sl.Value() != 128*1024*1024 {
		t.Errorf("cornus-shm SizeLimit = %v, want 128Mi", byName["cornus-shm"].EmptyDir.SizeLimit)
	}

	// The mounts wire the volumes into the container at the right paths.
	mountPath := map[string]string{}
	for _, m := range podSpec.Containers[0].VolumeMounts {
		mountPath[m.Name] = m.MountPath
	}
	if mountPath["tmpfs-0"] != "/run" || mountPath["tmpfs-1"] != "/tmp" || mountPath["cornus-shm"] != "/dev/shm" {
		t.Errorf("volume mounts = %v", mountPath)
	}
}

// TestDeployReservationsAndUpdateStrategy asserts deploy.resources.reservations
// become the container's resources.requests and deploy.update_config becomes the
// Deployment rolling-update strategy.
func TestDeployReservationsAndUpdateStrategy(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{
		Name:  "web",
		Image: "img",
		Resources: &api.Resources{
			CPULimit:       1.0,
			MemoryLimit:    512 * 1024 * 1024,
			ReservedCPU:    0.25,
			ReservedMemory: 128 * 1024 * 1024,
		},
		UpdateConfig: &api.UpdateConfig{Parallelism: 2, Order: "start-first"},
	})

	res := dep.Spec.Template.Spec.Containers[0].Resources
	if q := res.Limits[corev1.ResourceCPU]; q.MilliValue() != 1000 {
		t.Errorf("limits.cpu = %s, want 1000m", q.String())
	}
	if q := res.Requests[corev1.ResourceCPU]; q.MilliValue() != 250 {
		t.Errorf("requests.cpu = %s, want 250m", q.String())
	}
	if q := res.Requests[corev1.ResourceMemory]; q.Value() != 128*1024*1024 {
		t.Errorf("requests.memory = %s, want 128Mi", q.String())
	}

	// update_config start-first -> surge-first rolling update.
	strat := dep.Spec.Strategy
	if strat.Type != appsv1.RollingUpdateDeploymentStrategyType || strat.RollingUpdate == nil {
		t.Fatalf("strategy = %+v, want RollingUpdate", strat)
	}
	if got := strat.RollingUpdate.MaxSurge.IntValue(); got != 2 {
		t.Errorf("MaxSurge = %d, want 2 (parallelism)", got)
	}
	if got := strat.RollingUpdate.MaxUnavailable.IntValue(); got != 0 {
		t.Errorf("MaxUnavailable = %d, want 0 (start-first)", got)
	}
}

// TestDeployUpdateStrategyStopFirst asserts the default (stop-first) order maps
// to maxUnavailable-first rolling update.
func TestDeployUpdateStrategyStopFirst(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{
		Name:         "web",
		Image:        "img",
		UpdateConfig: &api.UpdateConfig{Order: "stop-first"}, // parallelism 0 => 1
	})
	strat := dep.Spec.Strategy
	if strat.Type != appsv1.RollingUpdateDeploymentStrategyType || strat.RollingUpdate == nil {
		t.Fatalf("strategy = %+v, want RollingUpdate", strat)
	}
	if got := strat.RollingUpdate.MaxUnavailable.IntValue(); got != 1 {
		t.Errorf("MaxUnavailable = %d, want 1", got)
	}
	if got := strat.RollingUpdate.MaxSurge.IntValue(); got != 0 {
		t.Errorf("MaxSurge = %d, want 0 (stop-first)", got)
	}
}

// TestResourceKeysNonHostPidIpcNoOp asserts a non-host pid/ipc form does not set
// HostPID/HostIPC (it is warned about, not applied).
func TestResourceKeysNonHostPidIpcNoOp(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{Name: "web", Image: "img", PIDMode: "service:o", IPCMode: "shareable"})
	podSpec := dep.Spec.Template.Spec
	if podSpec.HostPID || podSpec.HostIPC {
		t.Errorf("non-host pid/ipc must not set HostPID/HostIPC (pid=%v ipc=%v)", podSpec.HostPID, podSpec.HostIPC)
	}
}

// TestCommonKeysUsernameWarnsNoRunAsUser asserts a username-form compose user
// cannot be expressed as a numeric securityContext and is dropped (with a warn),
// while other keys still apply.
func TestCommonKeysUsernameWarnsNoRunAsUser(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{Name: "web", Image: "img", User: "nginx", ReadOnly: true})
	sc := dep.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatalf("securityContext = %+v, want ReadOnlyRootFilesystem=true", sc)
	}
	if sc.RunAsUser != nil {
		t.Errorf("RunAsUser = %v, want nil (username cannot be numeric)", sc.RunAsUser)
	}
}

// TestSecurityNetKeysDeployment asserts the security & networking batch maps
// onto the container securityContext (capabilities, no-new-privileges), the pod
// securityContext (supplementalGroups, sysctls), pod HostAliases (extra_hosts),
// and pod DNSConfig/DNSPolicy (dns/dns_search/dns_opt).
func TestSecurityNetKeysDeployment(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{
		Name:        "web",
		Image:       "img",
		CapAdd:      []string{"NET_ADMIN"},
		CapDrop:     []string{"MKNOD"},
		SecurityOpt: []string{"no-new-privileges:true"},
		GroupAdd:    []string{"1001", "staff"}, // "staff" (a name) must be skipped
		Sysctls:     map[string]string{"net.core.somaxconn": "1024"},
		ExtraHosts:  []string{"a.example:10.0.0.1", "b.example:10.0.0.1", "c.example:10.0.0.2"},
		DNSServers:  []string{"8.8.8.8", "1.1.1.1"},
		DNSSearch:   []string{"example.com"},
		DNSOptions:  []string{"timeout:2", "use-vc"},
	})
	podSpec := dep.Spec.Template.Spec
	sc := podSpec.Containers[0].SecurityContext
	if sc == nil || sc.Capabilities == nil {
		t.Fatalf("container securityContext = %+v, want capabilities", sc)
	}
	if len(sc.Capabilities.Add) != 1 || sc.Capabilities.Add[0] != "NET_ADMIN" {
		t.Errorf("capabilities.add = %v, want [NET_ADMIN]", sc.Capabilities.Add)
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "MKNOD" {
		t.Errorf("capabilities.drop = %v, want [MKNOD]", sc.Capabilities.Drop)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("AllowPrivilegeEscalation = %v, want false (no-new-privileges)", sc.AllowPrivilegeEscalation)
	}

	psc := podSpec.SecurityContext
	if psc == nil {
		t.Fatal("expected a pod securityContext")
	}
	// Only the numeric GID survives; the group name "staff" is skipped.
	if !reflect.DeepEqual(psc.SupplementalGroups, []int64{1001}) {
		t.Errorf("SupplementalGroups = %v, want [1001]", psc.SupplementalGroups)
	}
	if len(psc.Sysctls) != 1 || psc.Sysctls[0].Name != "net.core.somaxconn" || psc.Sysctls[0].Value != "1024" {
		t.Errorf("Sysctls = %+v", psc.Sysctls)
	}

	// extra_hosts: hostnames sharing an IP are grouped into one HostAlias.
	want := []corev1.HostAlias{
		{IP: "10.0.0.1", Hostnames: []string{"a.example", "b.example"}},
		{IP: "10.0.0.2", Hostnames: []string{"c.example"}},
	}
	if !reflect.DeepEqual(podSpec.HostAliases, want) {
		t.Errorf("HostAliases = %+v, want %+v", podSpec.HostAliases, want)
	}

	if podSpec.DNSPolicy != corev1.DNSNone {
		t.Errorf("DNSPolicy = %q, want None", podSpec.DNSPolicy)
	}
	if podSpec.DNSConfig == nil {
		t.Fatal("expected a pod DNSConfig")
	}
	if !reflect.DeepEqual(podSpec.DNSConfig.Nameservers, []string{"8.8.8.8", "1.1.1.1"}) {
		t.Errorf("Nameservers = %v", podSpec.DNSConfig.Nameservers)
	}
	if !reflect.DeepEqual(podSpec.DNSConfig.Searches, []string{"example.com"}) {
		t.Errorf("Searches = %v", podSpec.DNSConfig.Searches)
	}
	if len(podSpec.DNSConfig.Options) != 2 ||
		podSpec.DNSConfig.Options[0].Name != "timeout" || podSpec.DNSConfig.Options[0].Value == nil || *podSpec.DNSConfig.Options[0].Value != "2" ||
		podSpec.DNSConfig.Options[1].Name != "use-vc" || podSpec.DNSConfig.Options[1].Value != nil {
		t.Errorf("Options = %+v", podSpec.DNSConfig.Options)
	}
}

// TestSecurityNetKeysDefaultDNSUntouched asserts a spec with no dns/dns_search/
// dns_opt leaves the pod's default DNS policy (empty) in place — the compose DNS
// wiring must not disturb the cluster default.
func TestSecurityNetKeysDefaultDNSUntouched(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	dep := b.deployment(context.Background(), api.DeploySpec{Name: "web", Image: "img"})
	podSpec := dep.Spec.Template.Spec
	if podSpec.DNSPolicy != "" || podSpec.DNSConfig != nil {
		t.Errorf("DNSPolicy=%q DNSConfig=%+v, want defaults untouched", podSpec.DNSPolicy, podSpec.DNSConfig)
	}
	if podSpec.SecurityContext != nil {
		t.Errorf("pod securityContext = %+v, want nil", podSpec.SecurityContext)
	}
}

// TestApplyRejectsBindMounts confirms the stateless deploy never falls back to a
// node hostPath: a spec with a bind mount is rejected, and no Deployment or
// hostPath volume is created.
func TestApplyRejectsBindMounts(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "web",
		Image:  "img",
		Mounts: []api.Mount{{Source: "/srv", Target: "/data", ReadOnly: true}},
	}
	if _, err := b.Apply(ctx, spec); err == nil {
		t.Fatal("Apply with a bind mount must be rejected (no hostPath on kubernetes)")
	}
	if _, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("no Deployment should have been created for a rejected spec")
	}
}

// TestAnonymousVolumeCreatesPVCAndMount confirms a managed volume is realised as
// a dynamically-provisioned PVC plus the matching pod volume + container mount.
func TestAnonymousVolumeCreatesPVCAndMount(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "img",
		Volumes: []api.VolumeSpec{{Target: "/data"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// (a) the PVC exists with the default request, RWO, and the deployment label.
	pvc, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, "web-vol-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if pvc.Labels["cornus.app"] != "web" || pvc.Labels["cornus.managed"] != "true" {
		t.Fatalf("pvc labels = %v", pvc.Labels)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("pvc access modes = %v", pvc.Spec.AccessModes)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != "1Gi" {
		t.Fatalf("pvc storage = %q, want 1Gi", q.String())
	}
	if pvc.Spec.StorageClassName != nil {
		t.Fatalf("storageClassName = %v, want nil (cluster default)", *pvc.Spec.StorageClassName)
	}

	// (b) the pod template references the PVC and mounts it at the target.
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	vols := dep.Spec.Template.Spec.Volumes
	if len(vols) != 1 || vols[0].Name != "vol-0" || vols[0].PersistentVolumeClaim == nil || vols[0].PersistentVolumeClaim.ClaimName != "web-vol-0" {
		t.Fatalf("pod volumes = %+v", vols)
	}
	mounts := dep.Spec.Template.Spec.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != "vol-0" || mounts[0].MountPath != "/data" {
		t.Fatalf("volume mounts = %+v", mounts)
	}
}

// TestAnonymousVolumePopulateInitContainer asserts each managed volume gets a
// populate initContainer that, on first start only, copies the image's content at
// the target into the otherwise-empty PVC — mirroring how Docker seeds an
// anonymous volume from the image. The initContainer must run the app image, mount
// the SAME PVC at a scratch path (not the target, so the image content stays
// visible), and its script must be idempotent (copy only when the volume is empty).
func TestAnonymousVolumePopulateInitContainer(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "img",
		Volumes: []api.VolumeSpec{{Target: "/data"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	inits := dep.Spec.Template.Spec.InitContainers
	if len(inits) != 1 {
		t.Fatalf("init containers = %d, want 1: %+v", len(inits), inits)
	}
	ic := inits[0]
	if ic.Name != "cornus-volinit-0" {
		t.Errorf("init name = %q, want cornus-volinit-0", ic.Name)
	}
	if ic.Image != "img" {
		t.Errorf("init image = %q, want the app image img", ic.Image)
	}
	// Mounts the same PVC-backed volume, at a scratch path distinct from the target
	// (so the image's baked /data is still visible to be copied from).
	if len(ic.VolumeMounts) != 1 || ic.VolumeMounts[0].Name != "vol-0" {
		t.Fatalf("init volume mounts = %+v", ic.VolumeMounts)
	}
	scratch := ic.VolumeMounts[0].MountPath
	if scratch == "/data" {
		t.Fatalf("init mounts the PVC at the target /data; must be a scratch path so image content stays visible")
	}
	if len(ic.Command) != 3 || ic.Command[0] != "/bin/sh" || ic.Command[1] != "-c" {
		t.Fatalf("init command = %v, want /bin/sh -c <script>", ic.Command)
	}
	script := ic.Command[2]
	// Copy-only-when-empty (Docker parity): the script must guard on the volume
	// being empty and copy the image's /data into the scratch mount.
	for _, want := range []string{"ls -A", scratch, "cp -a", "/data"} {
		if !strings.Contains(script, want) {
			t.Errorf("populate script missing %q: %s", want, script)
		}
	}
}

// TestManagedResourcesOwnedByDeployment confirms managed volumes are ephemeral by
// OWNERSHIP: the Service and PVCs carry the Deployment as their controller owner,
// so deleting the Deployment lets Kubernetes garbage-collect them (docker rm -v
// semantics). The fake clientset does not run the GC controller, so we assert the
// ownership wiring here and that Delete removes the Deployment; the actual cascade
// is covered by the live kind E2E (deploy-volumes.star). UID is not asserted — the
// fake clientset leaves it empty, while the real API server populates it.
func TestManagedResourcesOwnedByDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "web",
		Image:   "img",
		Ports:   []api.PortMapping{{Host: 80, Container: 80}}, // so a Service is created
		Volumes: []api.VolumeSpec{{Target: "/data"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ownedByWeb := func(kind string, refs []metav1.OwnerReference) {
		t.Helper()
		for _, r := range refs {
			if r.Kind == "Deployment" && r.Name == "web" && r.Controller != nil && *r.Controller {
				return
			}
		}
		t.Fatalf("%s is not controller-owned by the web Deployment: %+v", kind, refs)
	}

	pvc, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, "web-vol-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	ownedByWeb("PVC", pvc.OwnerReferences)

	svc, err := cs.CoreV1().Services("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	ownedByWeb("Service", svc.OwnerReferences)

	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("Deployment still exists after Delete")
	}
}

// TestNamedVolumeSharedAndPersistent confirms a NAMED volume differs from an
// anonymous one in exactly the two ways that matter: two deployments naming the
// same volume share ONE backing PVC, and that PVC is NOT owned by any deployment,
// so it survives `cornus delete` of one of them (Docker named-volume semantics).
func TestNamedVolumeSharedAndPersistent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	web := api.DeploySpec{Name: "web", Image: "img", Volumes: []api.VolumeSpec{{Name: "proj_cache", Target: "/data"}}}
	worker := api.DeploySpec{Name: "worker", Image: "img", Volumes: []api.VolumeSpec{{Name: "proj_cache", Target: "/cache"}}}
	if _, err := b.Apply(ctx, web); err != nil {
		t.Fatalf("apply web: %v", err)
	}
	if _, err := b.Apply(ctx, worker); err != nil {
		t.Fatalf("apply worker: %v", err)
	}

	claimOf := func(dep string) string {
		d, err := cs.AppsV1().Deployments("default").Get(ctx, dep, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", dep, err)
		}
		vols := d.Spec.Template.Spec.Volumes
		if len(vols) != 1 || vols[0].PersistentVolumeClaim == nil {
			t.Fatalf("%s pod volumes = %+v", dep, vols)
		}
		return vols[0].PersistentVolumeClaim.ClaimName
	}

	// (a) both deployments reference the same shared claim.
	shared := claimOf("web")
	if got := claimOf("worker"); got != shared {
		t.Fatalf("web claim %q != worker claim %q; a named volume must be shared", shared, got)
	}
	if shared == "web-vol-0" || shared == "worker-vol-0" {
		t.Fatalf("named claim %q looks per-deployment; want a stable shared name", shared)
	}

	// (b) exactly one PVC exists for the name, un-owned, and volume-labelled.
	pvcs, err := cs.CoreV1().PersistentVolumeClaims("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pvcs: %v", err)
	}
	if len(pvcs.Items) != 1 {
		t.Fatalf("pvc count = %d, want 1 shared claim: %+v", len(pvcs.Items), pvcs.Items)
	}
	pvc := pvcs.Items[0]
	if pvc.Name != shared {
		t.Fatalf("pvc name = %q, want %q", pvc.Name, shared)
	}
	if len(pvc.OwnerReferences) != 0 {
		t.Fatalf("named-volume PVC must be un-owned (persist across delete); got %+v", pvc.OwnerReferences)
	}
	if pvc.Labels[deploy.LabelVolume] != "proj_cache" || pvc.Labels[deploy.LabelManaged] != "true" {
		t.Fatalf("named-volume PVC labels = %v", pvc.Labels)
	}
	if pvc.Labels[deploy.LabelApp] != "" {
		t.Fatalf("named-volume PVC must not carry an app label (it is shared): %v", pvc.Labels)
	}

	// (c) deleting one owner leaves the shared claim intact for the other.
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("delete web: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, shared, metav1.GetOptions{}); err != nil {
		t.Fatalf("shared PVC gone after deleting one deployment: %v", err)
	}
}

// TestVolumeStorageClassAndSize confirms an explicit Size + StorageClass flow
// through to the provisioned PVC.
func TestVolumeStorageClassAndSize(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:    "db",
		Image:   "img",
		Volumes: []api.VolumeSpec{{Target: "/var/lib", Size: "10Gi", StorageClass: "fast"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	pvc, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, "db-vol-0", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pvc: %v", err)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != "10Gi" {
		t.Fatalf("pvc storage = %q, want 10Gi", q.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast" {
		t.Fatalf("storageClassName = %v, want fast", pvc.Spec.StorageClassName)
	}
}

// TestNetworkServicesFallback drives a networked deploy on a plain fake (no
// dynamic client, no Multus in discovery): the bridge driver degrades to
// services-only DNS — the deploy succeeds, the headless alias Service selects
// the pods, and the pod template carries the membership label.
func TestNetworkServicesFallback(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "proj_default", Driver: "bridge", Aliases: []string{"web"}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	svc, err := cs.CoreV1().Services("default").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get alias service: %v", err)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("alias service ClusterIP = %q, want None (headless)", svc.Spec.ClusterIP)
	}
	if svc.Spec.Selector["cornus.app"] != "proj-web" {
		t.Errorf("alias service selector = %v, want the deployment's pods", svc.Spec.Selector)
	}
	if len(svc.OwnerReferences) != 1 || svc.OwnerReferences[0].Name != "proj-web" {
		t.Errorf("alias service owners = %+v, want owned by the Deployment", svc.OwnerReferences)
	}

	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	var member bool
	for k := range dep.Spec.Template.Labels {
		member = member || strings.HasPrefix(k, "cornus.net/")
	}
	if !member {
		t.Errorf("pod template labels = %v, want a cornus.net/* membership label", dep.Spec.Template.Labels)
	}
	// Fallback means no Multus annotation.
	if _, ok := dep.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"]; ok {
		t.Error("services fallback must not add Multus annotations")
	}
}

// TestNetworkMultusBridge drives the capable path end to end through the
// backend: with the NAD CRD discovered and a dynamic client wired
// (NewWithClients), a bridge-driver network yields the shared
// NetworkAttachmentDefinition and the Multus attachment annotation on the pod
// template, alongside the DNS alias Service.
func TestNetworkMultusBridge(t *testing.T) {
	cs := fake.NewSimpleClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("fake discovery type assertion failed")
	}
	fd.Resources = append(fd.Resources, &metav1.APIResourceList{
		GroupVersion: "k8s.cni.cncf.io/v1",
		APIResources: []metav1.APIResource{{Name: "network-attachment-definitions"}},
	})
	nadGVR := schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		nadGVR: "NetworkAttachmentDefinitionList",
	})
	b := NewWithClients(cs, dyn, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "proj_front", Driver: "bridge", Aliases: []string{"web"}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The shared NAD exists, un-owned, with a bridge CNI config.
	nads, err := dyn.Resource(nadGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil || len(nads.Items) != 1 {
		t.Fatalf("NADs = %v (err %v), want exactly one", nads, err)
	}
	nad := nads.Items[0]
	if len(nad.GetOwnerReferences()) != 0 {
		t.Error("shared NAD must be un-owned")
	}
	config, _, _ := unstructured.NestedString(nad.Object, "spec", "config")
	if !strings.Contains(config, `"type":"bridge"`) {
		t.Errorf("NAD config = %s, want a bridge CNI config", config)
	}

	// The pod template carries the Multus attachment annotation naming the NAD.
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got := dep.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"]; got != nad.GetName() {
		t.Errorf("networks annotation = %q, want %q", got, nad.GetName())
	}
	// And the DNS baseline still applies.
	if _, err := cs.CoreV1().Services("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Errorf("alias service missing: %v", err)
	}
}

// TestNetworkPolicyIsolation drives the `policy` driver through the backend on
// a plain fake (no enforcing CNI): the deploy succeeds, a NetworkPolicy is
// emitted for the network (isolating its member pods) alongside the DNS alias
// Service, and the pod carries the membership label the policy selects.
func TestNetworkPolicyIsolation(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "proj_iso", Driver: "policy", Aliases: []string{"web"}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	nps, err := cs.NetworkingV1().NetworkPolicies("default").List(ctx, metav1.ListOptions{})
	if err != nil || len(nps.Items) != 1 {
		t.Fatalf("NetworkPolicies = %v (err %v), want exactly one", nps, err)
	}
	np := nps.Items[0]
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	// The policy's podSelector must match a label the pod template actually has.
	for k, v := range np.Spec.PodSelector.MatchLabels {
		if dep.Spec.Template.Labels[k] != v {
			t.Errorf("NetworkPolicy selects %s=%s but the pod template lacks it (%v)", k, v, dep.Spec.Template.Labels)
		}
	}
	// DNS baseline still applies.
	if _, err := cs.CoreV1().Services("default").Get(ctx, "web", metav1.GetOptions{}); err != nil {
		t.Errorf("alias service missing under the policy driver: %v", err)
	}
}

// TestProxyInjectsRedirectAndCaretaker verifies the enforcing-proxy pod shape:
// a privileged net-redirect init container that iptables-captures app egress,
// and the caretaker running the proxy role as the exempt uid with the allow-set
// in its config.
func TestProxyInjectsRedirectAndCaretaker(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		Proxy: &api.ProxySpec{Allow: []string{"api", "db"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	inits := dep.Spec.Template.Spec.InitContainers
	var redirect, ctr *corev1.Container
	for i := range inits {
		switch inits[i].Name {
		case "cornus-net-redirect":
			redirect = &inits[i]
		case "cornus-caretaker":
			ctr = &inits[i]
		}
	}
	if redirect == nil || ctr == nil {
		t.Fatalf("want both a net-redirect init and a caretaker; got %+v", inits)
	}
	// The redirect init has NET_ADMIN (to program nftables) and points at the port.
	if redirect.SecurityContext == nil || redirect.SecurityContext.Capabilities == nil ||
		len(redirect.SecurityContext.Capabilities.Add) != 1 || redirect.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" {
		t.Errorf("net-redirect init must add NET_ADMIN, got %+v", redirect.SecurityContext)
	}
	if strings.Join(redirect.Args, " ") != "net-redirect --to-port 15001 --exempt-uid 1337" {
		t.Errorf("net-redirect args = %v", redirect.Args)
	}
	// The caretaker runs the proxy role as the exempt uid, NOT privileged.
	if ctr.SecurityContext == nil || ctr.SecurityContext.RunAsUser == nil || *ctr.SecurityContext.RunAsUser != 1337 {
		t.Errorf("caretaker must run as uid 1337, got %+v", ctr.SecurityContext)
	}
	if ctr.SecurityContext.Privileged != nil && *ctr.SecurityContext.Privileged {
		t.Error("proxy caretaker must not be privileged")
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.Proxy == nil || cfg.Proxy.ListenPort != 15001 || strings.Join(cfg.Proxy.Allow, ",") != "api,db" {
		t.Errorf("caretaker proxy config = %+v, want listen 15001 allow [api db]", cfg.Proxy)
	}
}

// TestProxyCooperativeInjectsHostAliases verifies the no-privilege proxy pod
// shape: hostAliases point each peer at a distinct loopback, a plain caretaker
// sidecar carries the cooperative upstream table, and there is NO net-redirect
// init container and no NET_ADMIN / special uid.
func TestProxyCooperativeInjectsHostAliases(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-client",
		Image: "img",
		Proxy: &api.ProxySpec{
			Mode:  "cooperative",
			Allow: []string{"web", "api"},
			Ports: map[string][]int{"web": {80}, "api": {9000}},
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-client", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	podSpec := dep.Spec.Template.Spec

	// No enforcing plumbing.
	for _, ic := range podSpec.InitContainers {
		if ic.Name == "cornus-net-redirect" {
			t.Error("cooperative mode must NOT add a net-redirect init container")
		}
	}
	// hostAliases: sorted allow => api=127.0.1.1, web=127.0.1.2.
	got := map[string]string{}
	for _, ha := range podSpec.HostAliases {
		for _, h := range ha.Hostnames {
			got[h] = ha.IP
		}
	}
	if got["api"] != "127.0.1.1" || got["web"] != "127.0.1.2" {
		t.Errorf("hostAliases = %v, want api=127.0.1.1 web=127.0.1.2", got)
	}

	var ctr *corev1.Container
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == "cornus-caretaker" {
			ctr = &podSpec.InitContainers[i]
		}
	}
	if ctr == nil {
		t.Fatal("want a caretaker sidecar")
	}
	if ctr.SecurityContext != nil && ctr.SecurityContext.RunAsUser != nil {
		t.Errorf("cooperative caretaker needs no special uid, got %+v", ctr.SecurityContext)
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.Proxy == nil || cfg.Proxy.Mode != "cooperative" || len(cfg.Proxy.Coop) != 2 {
		t.Fatalf("caretaker proxy config = %+v, want cooperative with 2 upstreams", cfg.Proxy)
	}
	byListen := map[string]caretaker.CoopUpstream{}
	for _, u := range cfg.Proxy.Coop {
		byListen[u.Listen] = u
	}
	if u := byListen["127.0.1.2"]; u.Forward != "web.default.svc.cluster.local" || !reflect.DeepEqual(u.Ports, []int{80}) {
		t.Errorf("web upstream = %+v, want forward web.default.svc.cluster.local ports [80]", u)
	}
	if u := byListen["127.0.1.1"]; u.Forward != "api.default.svc.cluster.local" || !reflect.DeepEqual(u.Ports, []int{9000}) {
		t.Errorf("api upstream = %+v, want forward api.default.svc.cluster.local ports [9000]", u)
	}
}

// TestDNSInjection verifies the caretaker DNS role wiring: a spec with DNS
// records yields a caretaker running the DNS role (with the discovered kube-dns
// upstream and the namespace search domain), only NET_BIND_SERVICE, and a pod
// dnsConfig that points the app's resolver at the sidecar.
func TestDNSInjection(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.0.10"},
	})
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		DNS:   &api.DNSSpec{Records: map[string]string{"db": "10.222.0.3"}},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec

	// dnsConfig routes the resolver through the sidecar, keeping the cluster search domain.
	if pod.DNSPolicy != corev1.DNSNone || pod.DNSConfig == nil ||
		len(pod.DNSConfig.Nameservers) != 1 || pod.DNSConfig.Nameservers[0] != "127.0.0.1" {
		t.Errorf("pod dnsConfig = %+v, want DNSNone + nameserver 127.0.0.1", pod.DNSConfig)
	}
	if len(pod.DNSConfig.Searches) == 0 || pod.DNSConfig.Searches[0] != "default.svc.cluster.local" {
		t.Errorf("dns searches = %v, want the namespace search domain first", pod.DNSConfig.Searches)
	}

	var ctr *corev1.Container
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			ctr = &pod.InitContainers[i]
		}
	}
	if ctr == nil {
		t.Fatal("want a caretaker sidecar")
	}
	if ctr.SecurityContext == nil || ctr.SecurityContext.Capabilities == nil ||
		len(ctr.SecurityContext.Capabilities.Add) != 1 || ctr.SecurityContext.Capabilities.Add[0] != "NET_BIND_SERVICE" {
		t.Errorf("DNS caretaker must add only NET_BIND_SERVICE, got %+v", ctr.SecurityContext)
	}
	var cfg caretaker.Config
	for _, e := range ctr.Env {
		if e.Name == "CORNUS_CARETAKER_CONFIG" {
			_ = json.Unmarshal([]byte(e.Value), &cfg)
		}
	}
	if cfg.DNS == nil || cfg.DNS.Records["db"] != "10.222.0.3" {
		t.Fatalf("caretaker DNS role = %+v, want record db=10.222.0.3", cfg.DNS)
	}
	if cfg.DNS.Upstream != "10.96.0.10:53" {
		t.Errorf("DNS upstream = %q, want the discovered kube-dns 10.96.0.10:53", cfg.DNS.Upstream)
	}
	if cfg.DNS.Domain != "default.svc.cluster.local" {
		t.Errorf("DNS domain = %q, want default.svc.cluster.local", cfg.DNS.Domain)
	}
}

// TestDNSProxyRejected confirms the guard: the DNS role and the proxy cannot yet
// share a pod (they would need two conflicting caretaker containers).
func TestDNSProxyRejected(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{
		Name:  "proj-web",
		Image: "img",
		DNS:   &api.DNSSpec{Records: map[string]string{"db": "10.222.0.3"}},
		Proxy: &api.ProxySpec{Allow: []string{"db"}},
	}
	if _, err := b.Apply(context.Background(), spec); err == nil {
		t.Error("DNS + proxy on one pod must be rejected")
	}
}

// TestNetworkDetachedRejectedWithMounts confirms the guard: a default
// (detached) network cannot be combined with client-local mounts, whose
// sidecar needs the cluster network to reach the 9P relay.
func TestNetworkDetachedRejectedWithMounts(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{
		Name:  "app",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "proj_main", Driver: "bridge", Default: true},
		},
	}
	mounts := []deploy.AttachMount{{Target: "/data", Session: "s", Name: "m", RelayURL: "ws://relay"}}
	if _, err := b.ApplyWithMounts(context.Background(), spec, mounts); err == nil {
		t.Fatal("detached network + client-local mounts must be rejected")
	}
}

func TestLifecycle(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()
	spec := api.DeploySpec{Name: "api", Image: "img", Replicas: 2}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Stop scales to 0 and remembers the replica count.
	if err := b.Stop(ctx, "api"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if *dep.Spec.Replicas != 0 || dep.Annotations[replicasAnnotation] != "2" {
		t.Fatalf("after stop: replicas=%d ann=%q", *dep.Spec.Replicas, dep.Annotations[replicasAnnotation])
	}

	// Start restores the remembered count.
	if err := b.Start(ctx, "api"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dep, _ = cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if *dep.Spec.Replicas != 2 {
		t.Fatalf("after start: replicas=%d", *dep.Spec.Replicas)
	}

	// Restart stamps the pod template.
	if err := b.Restart(ctx, "api"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	dep, _ = cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if dep.Spec.Template.Annotations[restartAnnotation] == "" {
		t.Fatalf("restart annotation not set")
	}

	// Delete removes the deployment.
	if err := b.Delete(ctx, "api"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment still exists after delete")
	}
}

// TestLifecycleRetriesOnConflict simulates the deployment controller writing to
// the Deployment between our Get and Update: every first Update per verb call
// is rejected with a 409 Conflict. Stop/Start/Restart must retry with a fresh
// read instead of surfacing the conflict (seen live as lifecycle.star's
// "restart svc: 500 ... the object has been modified").
func TestLifecycleRetriesOnConflict(t *testing.T) {
	cs := fake.NewSimpleClientset()
	conflicts := 0
	cs.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts%2 == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps", Resource: "deployments"}, "api",
				errors.New("the object has been modified"))
		}
		conflicts++
		return false, nil, nil
	})
	b := NewWithClient(cs, "default")
	ctx := context.Background()
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "api", Image: "img", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := b.Stop(ctx, "api"); err != nil {
		t.Fatalf("Stop did not retry on conflict: %v", err)
	}
	if err := b.Start(ctx, "api"); err != nil {
		t.Fatalf("Start did not retry on conflict: %v", err)
	}
	if err := b.Restart(ctx, "api"); err != nil {
		t.Fatalf("Restart did not retry on conflict: %v", err)
	}

	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if *dep.Spec.Replicas != 2 {
		t.Fatalf("after start: replicas=%d", *dep.Spec.Replicas)
	}
	if dep.Spec.Template.Annotations[restartAnnotation] == "" {
		t.Fatalf("restart annotation not set")
	}
}

// TestApplyRetriesOnConflict simulates the deployment controller writing to an
// existing Deployment between applyDeployment's Get and Update: the first
// update is rejected with a 409 Conflict. A re-Apply must retry with a fresh
// ResourceVersion instead of surfacing the conflict and failing the deploy.
func TestApplyRetriesOnConflict(t *testing.T) {
	cs := fake.NewSimpleClientset()
	ctx := context.Background()
	b := NewWithClient(cs, "default")

	// First Apply creates the Deployment (Create path, no update/conflict).
	if _, err := b.Apply(ctx, api.DeploySpec{Name: "api", Image: "img", Replicas: 1}); err != nil {
		t.Fatalf("initial Apply: %v", err)
	}

	// Now reject the first update-deployment (the existing-object path) with a
	// conflict, mimicking a concurrent controller write.
	conflicts := 0
	cs.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps", Resource: "deployments"}, "api",
				errors.New("the object has been modified"))
		}
		return false, nil, nil
	})

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "api", Image: "img2", Replicas: 3}); err != nil {
		t.Fatalf("re-Apply did not retry on conflict: %v", err)
	}
	if conflicts != 1 {
		t.Fatalf("expected exactly one injected conflict, got %d", conflicts)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "img2" || *dep.Spec.Replicas != 3 {
		t.Fatalf("desired spec not applied after retry: image=%q replicas=%d",
			dep.Spec.Template.Spec.Containers[0].Image, *dep.Spec.Replicas)
	}
}

// TestLogsStreamsPod confirms Logs selects the deployment's pod by app label and
// wraps the (unframed) kube log stream in stdcopy stdout framing, so a caller
// can demultiplex it. The fake clientset's GetLogs yields the bytes "fake logs".
func TestLogsStreamsPod(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "default",
			Labels:    labels("web"),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	b := NewWithClient(cs, "default")

	var buf bytes.Buffer
	if err := b.Logs(context.Background(), "web", api.LogOptions{Follow: true, Tail: "10"}, &buf); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, &buf); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if stdout.String() != "fake logs" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "fake logs")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr should be empty, got %q", stderr.String())
	}
}

// TestLogsNoPods reports a clear error when the deployment has no pods.
func TestLogsNoPods(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	if err := b.Logs(context.Background(), "missing", api.LogOptions{}, io.Discard); err == nil {
		t.Fatal("expected an error when no pods match")
	}
}

// TestStatsNotSupported confirms the kubernetes backend reports a clear
// not-supported error for stats (needs metrics-server; out of scope).
func TestStatsNotSupported(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	if err := b.Stats(context.Background(), "web", api.StatsOptions{}, io.Discard); err == nil {
		t.Fatal("expected Stats to be unsupported on the kubernetes backend")
	}
}

// TestArchiveNotSupported confirms the kubernetes backend reports a clear
// not-supported error for cp/archive (tar-over-exec; out of scope).
func TestArchiveNotSupported(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	if _, err := b.StatPath(context.Background(), "web", "/data"); err == nil {
		t.Fatal("expected StatPath to be unsupported on the kubernetes backend")
	}
	if _, err := b.CopyFrom(context.Background(), "web", "/data", io.Discard); err == nil {
		t.Fatal("expected CopyFrom to be unsupported on the kubernetes backend")
	}
	if err := b.CopyTo(context.Background(), "web", "/data", bytes.NewReader(nil), api.CopyToOptions{}); err == nil {
		t.Fatal("expected CopyTo to be unsupported on the kubernetes backend")
	}
}

// TestStatusHealthAndExitCode confirms Status maps a Deployment's readiness to
// Docker's health vocabulary (readiness probe present -> "healthy"/"starting")
// and that health and ExitCode stay coherent: a ready/"healthy" instance never
// carries an exit code, and ExitCode is surfaced only for a non-ready
// (terminated) instance. The readiness-count health and the name-sorted-pod exit
// status need not describe the same replica, so reporting both at once would be
// incoherent.
func TestStatusHealthAndExitCode(t *testing.T) {
	// Two desired replicas, one ready; the app container declares a readiness
	// probe so readiness carries a health meaning.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", Labels: labels("web")},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:           execContainer,
					ReadinessProbe: &corev1.Probe{},
				}}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	// Pod 0 (instance 0, ready): still running. Pod 1 (instance 1, not ready): app
	// container terminated with code 3.
	pod0 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-0pod", Namespace: "default", Labels: labels("web")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  execContainer,
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1pod", Namespace: "default", Labels: labels("web")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  execContainer,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3}},
		}}},
	}
	cs := fake.NewSimpleClientset(dep, pod0, pod1)
	b := NewWithClient(cs, "default")

	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 2 {
		t.Fatalf("instances = %d, want 2", len(st.Instances))
	}
	// Instance 0 is ready -> healthy and running, so it must NOT carry an exit code.
	i0 := st.Instances[0]
	if i0.Health != "healthy" {
		t.Fatalf("instance 0 Health = %q, want healthy", i0.Health)
	}
	if i0.ExitCode != nil {
		t.Fatalf("instance 0 ExitCode = %v, want nil (a healthy instance has no exit code)", *i0.ExitCode)
	}
	// Instance 1 is not ready -> starting and terminated, so its exit code surfaces.
	i1 := st.Instances[1]
	if i1.Health != "starting" {
		t.Fatalf("instance 1 Health = %q, want starting", i1.Health)
	}
	if i1.ExitCode == nil || *i1.ExitCode != 3 {
		t.Fatalf("instance 1 ExitCode = %v, want 3", i1.ExitCode)
	}
}

// TestStatusNoProbeHealthUnknown confirms that without a readiness/liveness
// probe, health is unknown ("") -- Kubernetes has no health signal to report.
func TestStatusNoProbeHealthUnknown(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default", Labels: labels("web")},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: execContainer}}},
			},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	cs := fake.NewSimpleClientset(dep)
	b := NewWithClient(cs, "default")
	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 1 || st.Instances[0].Health != "" {
		t.Fatalf("instance Health = %q, want empty", st.Instances[0].Health)
	}
}

func TestStatusNotFound(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	st, err := b.Status(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 0 {
		t.Fatalf("expected no instances, got %d", len(st.Instances))
	}
}

// TestExecCreateResolvesPodAndInspect confirms ExecCreate resolves the
// deployment's pod by app label, returns a non-empty id, and that ExecInspect
// follows the Docker exec lifecycle: created-but-not-started is NOT running,
// a started exec is Running, and a finished exec reports Running=false with
// the exit code (and Pid stays 0 — Kubernetes never surfaces the remote PID).
// It does NOT open a stream — that needs a live apiserver and is covered by
// the kind E2E.
func TestExecCreateResolvesPodAndInspect(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "default",
			Labels:    labels("web"),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	id, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true, AttachStdin: true})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if id == "" {
		t.Fatal("ExecCreate returned an empty exec id")
	}

	// The session was recorded against the running pod, targeting the app
	// container.
	b.mu.Lock()
	sess := b.sessions[id]
	b.mu.Unlock()
	if sess == nil {
		t.Fatal("no session recorded for the returned exec id")
	}
	if sess.pod != "web-abc123" {
		t.Fatalf("session pod = %q, want web-abc123", sess.pod)
	}
	if sess.container != "app" {
		t.Fatalf("session container = %q, want app", sess.container)
	}

	// Created but not started: NOT running (Docker exec lifecycle).
	st, err := b.ExecInspect(ctx, id)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if st.Running || st.ExitCode != 0 {
		t.Fatalf("ExecInspect = %+v, want Running=false ExitCode=0 before start", st)
	}

	// startSession (invoked by ExecStart when it opens the stream) flips the
	// exec to Running.
	b.startSession(sess)
	st, err = b.ExecInspect(ctx, id)
	if err != nil {
		t.Fatalf("ExecInspect after start: %v", err)
	}
	if !st.Running || st.ExitCode != 0 {
		t.Fatalf("ExecInspect = %+v, want Running=true ExitCode=0 after start", st)
	}

	// finishSession (invoked by ExecStart when the stream ends) flips Running off
	// and records the exit code. Pid stays 0: Kubernetes cannot surface the
	// remote process id.
	b.finishSession(sess, 7)
	st, err = b.ExecInspect(ctx, id)
	if err != nil {
		t.Fatalf("ExecInspect after finish: %v", err)
	}
	if st.Running || st.ExitCode != 7 || st.Pid != 0 {
		t.Fatalf("ExecInspect = %+v, want Running=false ExitCode=7 Pid=0", st)
	}
}

// TestExecCreateNoPods reports a clear error when the deployment has no pods.
func TestExecCreateNoPods(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	if _, err := b.ExecCreate(context.Background(), "missing", api.ExecConfig{Cmd: []string{"sh"}}); err == nil {
		t.Fatal("expected an error when no pods match")
	}
}

// TestExecResizeNonBlocking confirms ExecResize does not block or panic when no
// stream is consuming the size channel: the first resize fills the depth-1
// buffer and the second is dropped, all without blocking.
func TestExecResizeNonBlocking(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "default",
			Labels:    labels("web"),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	id, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	// Two resizes back-to-back with nothing draining the channel: neither blocks.
	if err := b.ExecResize(ctx, id, 24, 80); err != nil {
		t.Fatalf("ExecResize (1): %v", err)
	}
	if err := b.ExecResize(ctx, id, 40, 120); err != nil {
		t.Fatalf("ExecResize (2, buffer full): %v", err)
	}

	// The buffered (first) size is available to a consumer.
	select {
	case s := <-b.sessions[id].sizeCh:
		if s.Height != 24 || s.Width != 80 {
			t.Fatalf("buffered size = %+v, want {Width:80 Height:24}", s)
		}
	default:
		t.Fatal("expected a buffered terminal size")
	}
}

// TestExecResizeUnknownID errors for an unknown exec id.
func TestExecResizeUnknownID(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	if err := b.ExecResize(context.Background(), "nope", 24, 80); err == nil {
		t.Fatal("expected an error for an unknown exec id")
	}
}

// TestExecInspectUnknownID errors for an unknown exec id.
func TestExecInspectUnknownID(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	if _, err := b.ExecInspect(context.Background(), "nope"); err == nil {
		t.Fatal("expected an error for an unknown exec id")
	}
}

func TestDeploymentHealthProbeAndResources(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{
		Name:  "web",
		Image: "img",
		Healthcheck: &api.Healthcheck{
			Test:        []string{"CMD", "curl", "-f", "http://localhost"},
			Interval:    "30s",
			Timeout:     "5s",
			StartPeriod: "1m",
			Retries:     4,
		},
		Resources: &api.Resources{CPULimit: 0.5, MemoryLimit: 512 * 1024 * 1024},
	}
	dep := b.deployment(context.Background(), spec)
	c := dep.Spec.Template.Spec.Containers[0]

	lp := c.LivenessProbe
	if lp == nil || lp.Exec == nil {
		t.Fatalf("expected exec liveness probe, got %+v", lp)
	}
	if strings.Join(lp.Exec.Command, ",") != "curl,-f,http://localhost" {
		t.Fatalf("probe cmd = %v", lp.Exec.Command)
	}
	if lp.PeriodSeconds != 30 || lp.TimeoutSeconds != 5 || lp.InitialDelaySeconds != 60 || lp.FailureThreshold != 4 {
		t.Fatalf("probe timings = %+v", lp)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.Exec == nil {
		t.Fatalf("expected readiness probe too")
	}

	cpu := c.Resources.Limits[corev1.ResourceCPU]
	if cpu.MilliValue() != 500 {
		t.Fatalf("cpu limit = %v", cpu.String())
	}
	mem := c.Resources.Limits[corev1.ResourceMemory]
	if mem.Value() != 512*1024*1024 {
		t.Fatalf("mem limit = %v", mem.String())
	}
}

func TestDeploymentHealthcheckShellAndDisable(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")

	shell := b.deployment(context.Background(), api.DeploySpec{
		Name:        "s",
		Image:       "img",
		Healthcheck: &api.Healthcheck{Test: []string{"CMD-SHELL", "curl -f http://localhost"}},
	})
	lp := shell.Spec.Template.Spec.Containers[0].LivenessProbe
	if lp == nil || lp.Exec == nil || len(lp.Exec.Command) != 3 || lp.Exec.Command[0] != "/bin/sh" {
		t.Fatalf("shell probe = %+v", lp)
	}

	off := b.deployment(context.Background(), api.DeploySpec{
		Name:        "o",
		Image:       "img",
		Healthcheck: &api.Healthcheck{Test: []string{"NONE"}},
	})
	if p := off.Spec.Template.Spec.Containers[0].LivenessProbe; p != nil {
		t.Fatalf("disabled healthcheck should produce no probe, got %+v", p)
	}

	plain := b.deployment(context.Background(), api.DeploySpec{Name: "p", Image: "img"})
	pc := plain.Spec.Template.Spec.Containers[0]
	if pc.LivenessProbe != nil || pc.Resources.Limits != nil {
		t.Fatalf("plain spec should have no probe/limits: %+v", pc)
	}
}

// captureLogs redirects the default slog logger into a buffer for the duration
// of the test, so tests can assert on the backend's warnings.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// TestRestartPolicyMapsToWorkloadKind confirms restart policy selects the
// workload kind: long-lived policies ("always", "unless-stopped", the empty
// default) are Deployments, and run-to-completion policies ("no", "on-failure")
// are one-shots deployed as Jobs. Building a deployment must NOT warn about
// restart policy anymore — one-shots are honored via jobFromDeployment, not
// upgraded to restart-always with a warning.
func TestRestartPolicyMapsToWorkloadKind(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	buf := captureLogs(t)

	for _, tc := range []struct {
		policy  string
		oneShot bool
	}{
		{"", false},
		{"always", false},
		{"unless-stopped", false},
		{"no", true},
		{"on-failure", true},
		{"on-failure:3", true},
	} {
		buf.Reset()
		spec := api.DeploySpec{Name: "web", Image: "img", Restart: tc.policy}
		if got := deploy.IsOneShot(spec); got != tc.oneShot {
			t.Errorf("restart %q: IsOneShot = %v, want %v", tc.policy, got, tc.oneShot)
		}
		dep := b.deployment(context.Background(), spec)
		if dep == nil {
			t.Fatalf("restart %q: deployment not built", tc.policy)
		}
		if strings.Contains(buf.String(), "cannot honor restart policy") {
			t.Errorf("restart %q: unexpected restart-policy warning (one-shots are Jobs now): %s", tc.policy, buf.String())
		}
	}
}

// TestJobFromDeployment confirms the one-shot pod template is converted to a Job
// that runs once with the right pod restartPolicy and backoff, and keeps the
// cornus.app selector label so exec/logs/status resolve its pods.
func TestJobFromDeployment(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	for _, tc := range []struct {
		policy      string
		maxAttempts int
		wantRestart corev1.RestartPolicy
		wantBackoff int32
	}{
		{"no", 0, corev1.RestartPolicyNever, 0},
		{"on-failure", 0, corev1.RestartPolicyOnFailure, 6},
		{"on-failure", 3, corev1.RestartPolicyOnFailure, 3},
	} {
		spec := api.DeploySpec{Name: "init", Image: "img", Restart: tc.policy, RestartMaxAttempts: tc.maxAttempts}
		job := jobFromDeployment(spec, b.deployment(context.Background(), spec))
		if job.Spec.Template.Spec.RestartPolicy != tc.wantRestart {
			t.Errorf("restart %q: pod restartPolicy = %q, want %q", tc.policy, job.Spec.Template.Spec.RestartPolicy, tc.wantRestart)
		}
		if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != tc.wantBackoff {
			t.Errorf("restart %q(max %d): backoffLimit = %v, want %d", tc.policy, tc.maxAttempts, job.Spec.BackoffLimit, tc.wantBackoff)
		}
		if job.Spec.Completions == nil || *job.Spec.Completions != 1 {
			t.Errorf("restart %q: completions = %v, want 1", tc.policy, job.Spec.Completions)
		}
		if job.Labels[deploy.LabelApp] != "init" {
			t.Errorf("restart %q: job missing cornus.app=init selector label: %v", tc.policy, job.Labels)
		}
	}
}

// TestExecCommand confirms execCommand passes a plain command through unchanged
// and wraps an Env-carrying exec in `env KEY=VALUE ... cmd...` so the kube
// backend honors Docker's exec Env without a shell.
func TestExecCommand(t *testing.T) {
	if got := execCommand(api.ExecConfig{Cmd: []string{"sh", "-c", "echo hi"}}); !slicesEqual(got, []string{"sh", "-c", "echo hi"}) {
		t.Errorf("plain command should pass through, got %q", got)
	}
	got := execCommand(api.ExecConfig{
		Cmd: []string{"sh", "-c", "echo VAL=$FOO"},
		Env: []string{"FOO=bar", "BAZ=qux"},
	})
	want := []string{"env", "FOO=bar", "BAZ=qux", "sh", "-c", "echo VAL=$FOO"}
	if !slicesEqual(got, want) {
		t.Errorf("execCommand with Env = %q, want %q", got, want)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestExecCreateWarnsUnsupportedFields confirms ExecCreate warns per-exec for
// each exec-config field the pods/exec subresource cannot express (WorkingDir,
// User, Privileged) while staying silent for Env (honored via env(1) wrapping),
// still creates the exec, and stays silent when none of them is set.
func TestExecCreateWarnsUnsupportedFields(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "default",
			Labels:    labels("web"),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	b := NewWithClient(cs, "default")
	ctx := context.Background()
	buf := captureLogs(t)

	// A plain exec warns about nothing.
	if _, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"true"}}); err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if s := buf.String(); strings.Contains(s, "cannot honor exec option") {
		t.Fatalf("plain exec should not warn, got: %s", s)
	}

	buf.Reset()
	id, err := b.ExecCreate(ctx, "web", api.ExecConfig{
		Cmd:        []string{"true"},
		Env:        []string{"FOO=bar"},
		WorkingDir: "/srv",
		User:       "1000",
		Privileged: true,
	})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if id == "" {
		t.Fatal("exec with unsupported fields must still be created")
	}
	s := buf.String()
	for _, field := range []string{"WorkingDir", "User", "Privileged"} {
		if !strings.Contains(s, "option="+field) {
			t.Errorf("expected a warning for exec option %s, log: %s", field, s)
		}
	}
	// Env is honored via env(1) wrapping (execCommand), so it must NOT warn.
	if strings.Contains(s, "option=Env") {
		t.Errorf("Env is honored via env(1) and must not warn, log: %s", s)
	}
}

// TestMuxWriters confirms that the writers ExecStart and Attach bridge non-TTY
// output through emit stdcopy-multiplexed frames on the single conn, which a
// stdcopy demultiplexer (what the docker CLI runs) separates back into stdout
// and stderr.
func TestMuxWriters(t *testing.T) {
	var conn bytes.Buffer
	stdout, stderr := muxWriters(&conn)
	if _, err := stdout.Write([]byte("to stdout\n")); err != nil {
		t.Fatalf("stdout write: %v", err)
	}
	if _, err := stderr.Write([]byte("to stderr\n")); err != nil {
		t.Fatalf("stderr write: %v", err)
	}
	var o, e bytes.Buffer
	if _, err := stdcopy.StdCopy(&o, &e, &conn); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if o.String() != "to stdout\n" {
		t.Errorf("demuxed stdout = %q, want %q", o.String(), "to stdout\n")
	}
	if e.String() != "to stderr\n" {
		t.Errorf("demuxed stderr = %q, want %q", e.String(), "to stderr\n")
	}
}

// TestLifecycleMissingDeployment asserts the cross-backend contract: Stop,
// Start, and Restart of a name with no Deployment error wrapping
// deploy.ErrNotFound (not a raw apierrors NotFound), while Delete stays
// delete-if-exists.
func TestLifecycleMissingDeployment(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	ctx := context.Background()
	for verb, fn := range map[string]func(context.Context, string) error{
		"Stop":    b.Stop,
		"Start":   b.Start,
		"Restart": b.Restart,
	} {
		if err := fn(ctx, "ghost"); !errors.Is(err, deploy.ErrNotFound) {
			t.Errorf("%s(ghost) = %v, want error wrapping deploy.ErrNotFound", verb, err)
		}
	}
	if err := b.Delete(ctx, "ghost"); err != nil {
		t.Errorf("Delete(ghost) = %v, want nil (delete-if-exists)", err)
	}
}

// TestLogsSinceValidation asserts the shared since grammar on the kubernetes
// backend: garbage is an error (previously it was silently ignored, returning
// ALL logs), while a valid duration form is accepted.
func TestLogsSinceValidation(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "default",
			Labels:    labels("web"),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	b := NewWithClient(cs, "default")

	err := b.Logs(context.Background(), "web", api.LogOptions{Since: "yesterdayish"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid since") {
		t.Fatalf("Logs with garbage since = %v, want invalid-since error", err)
	}
	if err := b.Logs(context.Background(), "web", api.LogOptions{Since: "10m"}, io.Discard); err != nil {
		t.Fatalf("Logs with duration since: %v", err)
	}
}

// TestNetworkMultusStaticPin drives the pinned-addressing path (compose user
// networks, matrix row A') through the backend: an attachment carrying a
// plan-time static IP plus RequireUserNet DNS records yields a static-IPAM NAD,
// the JSON network-selection annotation with the pinned address, the DNS
// caretaker serving the peer records, and a Recreate strategy (a rolling
// update would briefly run two pods with the same static IP).
func TestNetworkMultusStaticPin(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-dns", Namespace: "kube-system"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.0.10"},
	})
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatal("fake discovery type assertion failed")
	}
	fd.Resources = append(fd.Resources, &metav1.APIResourceList{
		GroupVersion: "k8s.cni.cncf.io/v1",
		APIResources: []metav1.APIResource{{Name: "network-attachment-definitions"}},
	})
	nadGVR := schema.GroupVersionResource{Group: "k8s.cni.cncf.io", Version: "v1", Resource: "network-attachment-definitions"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		nadGVR: "NetworkAttachmentDefinitionList",
	})
	b := NewWithClients(cs, dyn, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "mesh-a",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "mesh_mesh", Driver: "bridge", Aliases: []string{"a"}, IP: "10.222.14.7/24"},
		},
		DNS: &api.DNSSpec{
			Records:        map[string]string{"a": "10.222.14.7", "b": "10.222.14.9"},
			RequireUserNet: true,
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The shared NAD delegates to static IPAM and declares the ips capability.
	nads, err := dyn.Resource(nadGVR).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil || len(nads.Items) != 1 {
		t.Fatalf("NADs = %v (err %v), want exactly one", nads, err)
	}
	config, _, _ := unstructured.NestedString(nads.Items[0].Object, "spec", "config")
	for _, want := range []string{`"ipam":{"type":"static"}`, `"capabilities":{"ips":true}`} {
		if !strings.Contains(config, want) {
			t.Errorf("NAD config missing %s: %s", want, config)
		}
	}

	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "mesh-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	// JSON selection annotation carrying the pinned address.
	ann := dep.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"]
	if !strings.HasPrefix(ann, "[") || !strings.Contains(ann, `"ips":["10.222.14.7/24"]`) {
		t.Errorf("networks annotation = %s, want the JSON form pinning 10.222.14.7/24", ann)
	}
	// Static addressing forbids rolling updates.
	if dep.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("strategy = %q, want Recreate for a pinned static IP", dep.Spec.Strategy.Type)
	}
	// The DNS caretaker is injected with the peer records (Multus resolved, so
	// RequireUserNet is satisfied).
	pod := dep.Spec.Template.Spec
	var cfg caretaker.Config
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			for _, e := range pod.InitContainers[i].Env {
				if e.Name == "CORNUS_CARETAKER_CONFIG" {
					_ = json.Unmarshal([]byte(e.Value), &cfg)
				}
			}
		}
	}
	if cfg.DNS == nil || cfg.DNS.Records["b"] != "10.222.14.9" {
		t.Fatalf("caretaker DNS role = %+v, want the peer record b=10.222.14.9", cfg.DNS)
	}
	if pod.DNSPolicy != corev1.DNSNone {
		t.Errorf("pod DNSPolicy = %q, want DNSNone routed through the caretaker", pod.DNSPolicy)
	}
}

// TestDNSRequireUserNetSkippedWithoutMultus: on a cluster without the Multus
// fabric the netdriver falls back to services-only DNS, so RequireUserNet
// records (which point at secondary IPs that will never exist) must NOT inject
// the caretaker — the pod keeps the default cluster DNS. An explicit DNSSpec
// (RequireUserNet false) still injects; TestDNSInjection covers that.
func TestDNSRequireUserNetSkippedWithoutMultus(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:  "mesh-a",
		Image: "img",
		Networks: []api.NetworkAttachment{
			{Name: "mesh_mesh", Driver: "bridge", Aliases: []string{"a"}, IP: "10.222.14.7/24"},
		},
		DNS: &api.DNSSpec{
			Records:        map[string]string{"b": "10.222.14.9"},
			RequireUserNet: true,
		},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "mesh-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec
	for i := range pod.InitContainers {
		if pod.InitContainers[i].Name == "cornus-caretaker" {
			t.Fatal("caretaker injected although the Multus fabric is unavailable")
		}
	}
	if pod.DNSPolicy == corev1.DNSNone {
		t.Error("pod resolver rerouted to a caretaker that was never injected")
	}
	// And the services fallback left the annotation off entirely.
	if _, ok := dep.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"]; ok {
		t.Error("services fallback must not add Multus annotations")
	}
}

// TestReapExpiredSessions verifies the exec-session registry is bounded: a
// finished session past its TTL is evicted, while an in-flight session and a
// recently-finished one (still needed for the caller's post-exec inspect) are
// retained. This guards against the unbounded growth that would otherwise leak
// one entry per exec on a long-lived backend.
func TestReapExpiredSessions(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	now := time.Now()

	running := &execSession{pod: "p", started: true}
	recent := &execSession{pod: "p", started: true}
	b.finishSession(recent, 0)
	expired := &execSession{pod: "p", started: true, done: true, finishedAt: now.Add(-execSessionTTL - time.Second)}

	b.mu.Lock()
	b.sessions["running"] = running
	b.sessions["recent"] = recent
	b.sessions["expired"] = expired
	b.reapExpiredSessionsLocked(now)
	_, hasRunning := b.sessions["running"]
	_, hasRecent := b.sessions["recent"]
	_, hasExpired := b.sessions["expired"]
	n := len(b.sessions)
	b.mu.Unlock()

	if !hasRunning {
		t.Error("in-flight session was reaped; only finished sessions past TTL should be evicted")
	}
	if !hasRecent {
		t.Error("recently-finished session was reaped before its TTL elapsed")
	}
	if hasExpired {
		t.Error("finished session past its TTL was not reaped (registry would leak)")
	}
	if n != 2 {
		t.Errorf("sessions = %d, want 2 (running + recent)", n)
	}
}

// TestFinishSessionStampsFinishedAt guards the reaper's precondition: a
// finished session must record when it finished so the TTL sweep can evict it.
func TestFinishSessionStampsFinishedAt(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	sess := &execSession{pod: "p", started: true}
	before := time.Now()
	b.finishSession(sess, 7)
	if !sess.done {
		t.Fatal("finishSession did not mark the session done")
	}
	if sess.exitCode != 7 {
		t.Errorf("exitCode = %d, want 7", sess.exitCode)
	}
	if sess.finishedAt.Before(before) {
		t.Errorf("finishedAt = %v, want >= %v", sess.finishedAt, before)
	}
}

// caretakerContainers returns every injected cornus-caretaker container in a pod
// spec, and its parsed config env (last one wins). Used by the Docker-role tests to
// assert there is exactly ONE caretaker no matter how many roles are folded in.
func caretakerContainers(t *testing.T, podSpec corev1.PodSpec) ([]corev1.Container, caretaker.Config) {
	t.Helper()
	var ctrs []corev1.Container
	var cfg caretaker.Config
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == "cornus-caretaker" {
			ctrs = append(ctrs, podSpec.InitContainers[i])
			for _, e := range podSpec.InitContainers[i].Env {
				if e.Name == "CORNUS_CARETAKER_CONFIG" {
					_ = json.Unmarshal([]byte(e.Value), &cfg)
				}
			}
		}
	}
	return ctrs, cfg
}

// TestDockerInjectionTCP checks a docker-only pod: one caretaker carrying the
// docker role bound to the default loopback TCP port, and a DOCKER_HOST env on the
// app container pointing at it.
func TestDockerInjectionTCP(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "http://cornus:5000")
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "proj-ci", Image: "img", Docker: &api.DockerSpec{}}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "proj-ci", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	podSpec := dep.Spec.Template.Spec
	ctrs, cfg := caretakerContainers(t, podSpec)
	if len(ctrs) != 1 {
		t.Fatalf("want exactly one caretaker, got %d", len(ctrs))
	}
	if cfg.Docker == nil || cfg.Docker.TCPAddr != "127.0.0.1:2375" || cfg.Docker.UnixPath != "" {
		t.Fatalf("docker role = %+v, want TCPAddr 127.0.0.1:2375", cfg.Docker)
	}
	if cfg.Docker.Server != "http://cornus:5000" {
		t.Errorf("docker server = %q, want the advertised URL", cfg.Docker.Server)
	}
	if v := appEnv(podSpec)["DOCKER_HOST"]; v != "tcp://127.0.0.1:2375" {
		t.Errorf("DOCKER_HOST = %q, want tcp://127.0.0.1:2375", v)
	}
}

// TestDockerInjectionUnix checks the unix transport: a shared emptyDir carries the
// socket, mounted into both the app container and the caretaker, and DOCKER_HOST is
// a unix:// URL.
func TestDockerInjectionUnix(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "proj-ci", Image: "img", Docker: &api.DockerSpec{Transport: "unix"}}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "proj-ci", metav1.GetOptions{})
	podSpec := dep.Spec.Template.Spec
	ctrs, cfg := caretakerContainers(t, podSpec)
	if len(ctrs) != 1 {
		t.Fatalf("want one caretaker, got %d", len(ctrs))
	}
	if cfg.Docker == nil || cfg.Docker.UnixPath != "/cornus/docker/docker.sock" || cfg.Docker.TCPAddr != "" {
		t.Fatalf("docker role = %+v, want UnixPath /cornus/docker/docker.sock only", cfg.Docker)
	}
	if v := appEnv(podSpec)["DOCKER_HOST"]; v != "unix:///cornus/docker/docker.sock" {
		t.Errorf("DOCKER_HOST = %q, want unix:///cornus/docker/docker.sock", v)
	}
	// The shared volume must exist and be mounted into both the app and caretaker.
	hasVol := false
	for _, vol := range podSpec.Volumes {
		if vol.Name == "cornus-docker-sock" && vol.EmptyDir != nil {
			hasVol = true
		}
	}
	if !hasVol {
		t.Fatal("want a cornus-docker-sock emptyDir volume")
	}
	mounted := func(c corev1.Container) bool {
		for _, m := range c.VolumeMounts {
			if m.Name == "cornus-docker-sock" && m.MountPath == "/cornus/docker" {
				return true
			}
		}
		return false
	}
	if !mounted(podSpec.Containers[0]) {
		t.Error("app container must mount the docker socket volume")
	}
	if !mounted(ctrs[0]) {
		t.Error("caretaker must mount the docker socket volume")
	}
}

// TestDockerInjectionBoth checks that "both" binds tcp+unix and DOCKER_HOST prefers
// the TCP endpoint.
func TestDockerInjectionBoth(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "proj-ci", Image: "img", Docker: &api.DockerSpec{Transport: "both", Port: 2400}}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "proj-ci", metav1.GetOptions{})
	podSpec := dep.Spec.Template.Spec
	_, cfg := caretakerContainers(t, podSpec)
	if cfg.Docker == nil || cfg.Docker.TCPAddr != "127.0.0.1:2400" || cfg.Docker.UnixPath == "" {
		t.Fatalf("docker role = %+v, want both tcp:2400 and a unix path", cfg.Docker)
	}
	if v := appEnv(podSpec)["DOCKER_HOST"]; v != "tcp://127.0.0.1:2400" {
		t.Errorf("DOCKER_HOST = %q, want the tcp endpoint in both mode", v)
	}
}

// TestDockerFoldsIntoHub checks that a pod with BOTH a hub and a docker role gets a
// SINGLE caretaker carrying both — no duplicate cornus-caretaker container.
func TestDockerFoldsIntoHub(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	ctx := context.Background()

	spec := api.DeploySpec{
		Name:   "proj-ci",
		Image:  "img",
		Hub:    &api.HubSpec{Export: []api.HubExport{{Name: "web", Port: 80}}},
		Docker: &api.DockerSpec{},
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "proj-ci", metav1.GetOptions{})
	ctrs, cfg := caretakerContainers(t, dep.Spec.Template.Spec)
	if len(ctrs) != 1 {
		t.Fatalf("hub + docker must share ONE caretaker, got %d", len(ctrs))
	}
	if cfg.Hub == nil || cfg.Docker == nil {
		t.Fatalf("the single caretaker must carry both roles: hub=%v docker=%v", cfg.Hub, cfg.Docker)
	}
}

// TestDockerClientTokenSecret checks the hardened auth path: the client token is a
// secretKeyRef env (CORNUS_DOCKER_CLIENT_TOKEN), not a literal in the config JSON.
func TestDockerClientTokenSecret(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	b.clientTokenSecret, b.clientTokenSecretKey = "cornus-client-token", "token"
	ctx := context.Background()

	spec := api.DeploySpec{Name: "proj-ci", Image: "img", Docker: &api.DockerSpec{}}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "proj-ci", metav1.GetOptions{})
	ctrs, cfg := caretakerContainers(t, dep.Spec.Template.Spec)
	if cfg.Docker == nil || cfg.Docker.Token != "" {
		t.Fatalf("client token must NOT be embedded in the config JSON, got %+v", cfg.Docker)
	}
	var ref *corev1.SecretKeySelector
	for _, e := range ctrs[0].Env {
		if e.Name == "CORNUS_DOCKER_CLIENT_TOKEN" && e.ValueFrom != nil {
			ref = e.ValueFrom.SecretKeyRef
		}
	}
	if ref == nil || ref.Name != "cornus-client-token" || ref.Key != "token" {
		t.Fatalf("want a CORNUS_DOCKER_CLIENT_TOKEN secretKeyRef to cornus-client-token/token, got %+v", ref)
	}
}

// TestDockerClientTokenLiteral checks the fallback: with only CORNUS_CLIENT_TOKEN
// set, the token is embedded in the config JSON.
func TestDockerClientTokenLiteral(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewWithClient(cs, "default")
	b.clientToken = "lit-token"
	ctx := context.Background()

	spec := api.DeploySpec{Name: "proj-ci", Image: "img", Docker: &api.DockerSpec{}}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dep, _ := cs.AppsV1().Deployments("default").Get(ctx, "proj-ci", metav1.GetOptions{})
	_, cfg := caretakerContainers(t, dep.Spec.Template.Spec)
	if cfg.Docker == nil || cfg.Docker.Token != "lit-token" {
		t.Fatalf("docker role token = %+v, want embedded lit-token", cfg.Docker)
	}
}

// TestDockerProxyRejected checks that combining the docker role with the enforcing
// proxy is rejected (their egress interception conflicts).
func TestDockerProxyRejected(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{
		Name:   "proj-ci",
		Image:  "img",
		Docker: &api.DockerSpec{},
		Proxy:  &api.ProxySpec{Allow: []string{"api"}},
	}
	if _, err := b.Apply(context.Background(), spec); err == nil {
		t.Fatal("expected docker + enforcing proxy to be rejected")
	}
}

// TestDockerBadTransportRejected checks transport validation.
func TestDockerBadTransportRejected(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	spec := api.DeploySpec{Name: "proj-ci", Image: "img", Docker: &api.DockerSpec{Transport: "vsock"}}
	if _, err := b.Apply(context.Background(), spec); err == nil {
		t.Fatal("expected an invalid docker.transport to be rejected")
	}
}

// selfPod builds a Pod named podName in ns whose `cornus` container carries
// image, used to exercise discoverSelfImage's self-Pod read.
func selfPod(podName, ns, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar-noise", Image: "some/other:img"},
			{Name: selfContainerName, Image: image},
		}},
	}
}

// TestSidecarImageDiscoveredFromOwnPod is the regression guard for the bug where
// the injected sidecars ran the WORKLOAD's image instead of a cornus image: with
// no CORNUS_K8S_SIDECAR_IMAGE override, the backend must discover the server's
// own image from its Pod and give that to sidecarImageFor — never the app image.
func TestSidecarImageDiscoveredFromOwnPod(t *testing.T) {
	t.Setenv("CORNUS_K8S_SIDECAR_IMAGE", "")
	t.Setenv("POD_NAME", "cornus-0")
	t.Setenv("POD_NAMESPACE", "cornus-system")
	const own = "ghcr.io/moriyoshi/cornus:9.9.9"
	cs := fake.NewSimpleClientset(selfPod("cornus-0", "cornus-system", own))

	b := NewWithClient(cs, "cornus-system")
	if b.sidecarImage != own {
		t.Fatalf("backend sidecarImage = %q, want discovered own image %q", b.sidecarImage, own)
	}
	// A workload with a NON-cornus app image must still get the cornus sidecar image.
	got := b.sidecarImageFor(api.DeploySpec{Image: "nginx:latest"})
	if got != own {
		t.Errorf("sidecarImageFor = %q, want %q (must not fall back to the app image)", got, own)
	}
}

// TestSidecarImageEnvOverrideWins checks the explicit override beats discovery.
func TestSidecarImageEnvOverrideWins(t *testing.T) {
	t.Setenv("CORNUS_K8S_SIDECAR_IMAGE", "registry.example/cornus:pinned")
	t.Setenv("POD_NAME", "cornus-0")
	t.Setenv("POD_NAMESPACE", "cornus-system")
	cs := fake.NewSimpleClientset(selfPod("cornus-0", "cornus-system", "ghcr.io/moriyoshi/cornus:9.9.9"))

	b := NewWithClient(cs, "cornus-system")
	if b.sidecarImage != "registry.example/cornus:pinned" {
		t.Fatalf("backend sidecarImage = %q, want the env override", b.sidecarImage)
	}
}

// TestSidecarImageFallsBackToAppImage checks that when discovery finds nothing
// (no self Pod), sidecarImageFor preserves the legacy app-image fallback.
func TestSidecarImageFallsBackToAppImage(t *testing.T) {
	t.Setenv("CORNUS_K8S_SIDECAR_IMAGE", "")
	t.Setenv("POD_NAME", "cornus-0")
	t.Setenv("POD_NAMESPACE", "cornus-system")
	cs := fake.NewSimpleClientset() // no Pod → discovery returns ""

	b := NewWithClient(cs, "cornus-system")
	if b.sidecarImage != "" {
		t.Fatalf("backend sidecarImage = %q, want empty (nothing discovered)", b.sidecarImage)
	}
	if got := b.sidecarImageFor(api.DeploySpec{Image: "myapp:1"}); got != "myapp:1" {
		t.Errorf("sidecarImageFor fallback = %q, want app image myapp:1", got)
	}
}
