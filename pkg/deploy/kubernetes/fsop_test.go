package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/kubernetes/fake"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/remotecompanion"
)

// TestFSOpRoleMountsTheAppsVolumesIntoTheCaretaker is the whole point of the kubernetes
// half: the caretaker has its own mount namespace, so a volume it was not GIVEN is a
// volume it cannot serve. The assertion pairs the declared root with the actual
// volumeMount — a config naming a path nothing is mounted at would answer every request
// with a confident "not found".
func TestFSOpRoleMountsTheAppsVolumesIntoTheCaretaker(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	spec := api.DeploySpec{
		Name: "web", Image: "img",
		// AgentForward only so that a caretaker exists at all; the fsop role rides
		// whichever caretaker the pod already has.
		AgentForward: true,
		Volumes: []api.VolumeSpec{
			{Name: "proj_data", Target: "/data"},
			{Name: "proj_seed", Target: "/seed", ReadOnly: true},
		},
	}
	pod := applyAndGetPod(t, spec)
	ctr := caretakerContainer(t, pod)
	cfg := caretakerConfigOf(t, ctr)

	if cfg.FSOp == nil {
		t.Fatal("a spec with volumes got no FSOp role")
	}
	if len(cfg.FSOp.Roots) != 2 {
		t.Fatalf("roots = %+v, want one per volume", cfg.FSOp.Roots)
	}
	byTarget := map[string]caretaker.FSOpRoot{}
	for _, r := range cfg.FSOp.Roots {
		byTarget[r.Target] = r
	}
	for _, want := range []struct {
		target   string
		readOnly bool
	}{{"/data", false}, {"/seed", true}} {
		root, ok := byTarget[want.target]
		if !ok {
			t.Fatalf("no root for %s; roots = %+v", want.target, cfg.FSOp.Roots)
		}
		if root.ReadOnly != want.readOnly {
			t.Errorf("%s root readOnly = %v, want %v", want.target, root.ReadOnly, want.readOnly)
		}
		// The declared root must correspond to a real volumeMount on the caretaker,
		// and that mount must reference the SAME volume the app container uses.
		careMount := findVolumeMount(t, ctr.VolumeMounts, root.Path)
		appMount := findVolumeMount(t, pod.Containers[0].VolumeMounts, want.target)
		if careMount.Name != appMount.Name {
			t.Errorf("%s: caretaker mounts volume %q, app mounts %q — different storage",
				want.target, careMount.Name, appMount.Name)
		}
		if careMount.ReadOnly != want.readOnly {
			t.Errorf("%s: caretaker volumeMount readOnly = %v, want %v", want.target, careMount.ReadOnly, want.readOnly)
		}
	}

	// Server-initiated, so the connection has to be findable by instance. Asserted
	// against the shared constructor for the reason its doc gives.
	if want := remotecompanion.AgentRelayInstance("web"); cfg.Instance != want {
		t.Errorf("cfg.Instance = %q, want %q", cfg.Instance, want)
	}
	if cfg.FSOp.Server != "ws://cornus:5000" {
		t.Errorf("FSOp.Server = %q, want the advertise URL", cfg.FSOp.Server)
	}
}

// TestFSOpRidesTheConnectionThePodAlreadyHas. The caretaker buckets its server-bound roles
// by URL and dials one connection per bucket. A role naming a DIFFERENT spelling of the
// same server therefore gets a second connection — and because both register under the
// same instance key, the server's registry keeps whichever connected last while the fsop
// handler may be sitting on the other one. The symptom is a stream the server opens and
// nobody ever answers, which is indistinguishable from a backend with no operator.
func TestFSOpRidesTheConnectionThePodAlreadyHas(t *testing.T) {
	// The advertise URL is deliberately NOT what the existing role dials.
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://advertised:5000")
	cfg := &caretaker.Config{
		Mounts: []caretaker.MountRole{{Server: "ws://relay.example:9000", Name: "m0", Target: "/x"}},
	}
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	var mounts []corev1.VolumeMount
	b.addFSOpRole(api.DeploySpec{
		Name:    "web",
		Volumes: []api.VolumeSpec{{Name: "proj_data", Target: "/data"}},
	}, cfg, &mounts)

	if cfg.FSOp == nil {
		t.Fatal("no FSOp role")
	}
	if cfg.FSOp.Server != "ws://relay.example:9000" {
		t.Fatalf("FSOp.Server = %q, want the mount role's server — a second bucket means a second connection",
			cfg.FSOp.Server)
	}

	// With no server-bound role to join, the advertised URL is the right answer: there
	// is no first connection for a second one to shadow.
	alone := &caretaker.Config{}
	b.addFSOpRole(api.DeploySpec{
		Name:    "web",
		Volumes: []api.VolumeSpec{{Name: "proj_data", Target: "/data"}},
	}, alone, &mounts)
	if alone.FSOp == nil || alone.FSOp.Server != "ws://advertised:5000" {
		t.Fatalf("FSOp.Server on a config with no other server-bound role = %+v, want the advertise URL", alone.FSOp)
	}
}

// TestFSOpRoleIsAbsentWithoutVolumes: the role must not appear on a pod that has nothing
// for it to serve — an empty-rooted operator would still be consulted, and would answer
// every path "unsupported" one round trip later than not being there at all.
func TestFSOpRoleIsAbsentWithoutVolumes(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	pod := applyAndGetPod(t, api.DeploySpec{Name: "web", Image: "img", AgentForward: true})
	if cfg := caretakerConfigOf(t, caretakerContainer(t, pod)); cfg.FSOp != nil {
		t.Fatalf("a volume-free spec got an FSOp role: %+v", cfg.FSOp)
	}
}

// TestFSOpRoleRidesEveryCaretaker guards the drift this file's helper exists to prevent.
// Six places in kubernetes.go assemble a caretaker; a role added to only some of them
// gives a pod a filesystem or not depending on which OTHER feature it happens to use,
// which is indistinguishable from a flaky backend.
func TestFSOpRoleRidesEveryCaretaker(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "ws://cornus:5000")
	vols := []api.VolumeSpec{{Name: "proj_data", Target: "/data"}}
	for _, tc := range []struct {
		name string
		spec api.DeploySpec
	}{
		{"agent-forward", api.DeploySpec{Name: "web", Image: "img", AgentForward: true, Volumes: vols}},
		{"docker", api.DeploySpec{Name: "web", Image: "img", Docker: &api.DockerSpec{}, Volumes: vols}},
		{"hub", api.DeploySpec{Name: "web", Image: "img", Hub: &api.HubSpec{Identity: "web"}, Volumes: vols}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := applyAndGetPod(t, tc.spec)
			cfg := caretakerConfigOf(t, caretakerContainer(t, pod))
			if cfg.FSOp == nil || len(cfg.FSOp.Roots) != 1 {
				t.Fatalf("caretaker for %s carries no FSOp root: %+v", tc.name, cfg.FSOp)
			}
			if cfg.Instance == "" {
				t.Error("FSOp is looked up by instance, but the config declares none")
			}
		})
	}
}

// TestFSOpFallsBackWhenNoCaretakerIsConnected pins the contract that makes the whole
// fallback chain work: a backend that cannot serve a path answers "unsupported" with a
// NIL error. Returning an error instead would surface to the user as a failed copy
// rather than a slower one.
func TestFSOpFallsBackWhenNoCaretakerIsConnected(t *testing.T) {
	b := NewWithClient(fake.NewSimpleClientset(), "default")
	var _ deploy.FSOperator = b

	// No registry at all.
	resp, err := b.FSOp(context.Background(), "web", api.FSOpRequest{Op: api.FSOpStat, Path: "/data"}, nil, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil so the caller falls back", err)
	}
	if resp.Code != api.FSErrUnsupported {
		t.Fatalf("code = %q (%s), want unsupported", resp.Code, resp.Error)
	}

	// A registry with nothing registered for this instance — the ordinary case
	// before a pod's caretaker has dialed in.
	b2 := NewWithClient(fake.NewSimpleClientset(), "default", WithCompanionRegistry(remotecompanion.NewRegistry()))
	resp, err = b2.FSOp(context.Background(), "web", api.FSOpRequest{Op: api.FSOpStat, Path: "/data"}, nil, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if resp.Code != api.FSErrUnsupported {
		t.Fatalf("code = %q (%s), want unsupported", resp.Code, resp.Error)
	}
}
