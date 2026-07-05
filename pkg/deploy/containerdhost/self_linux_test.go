//go:build linux

package containerdhost

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
)

// TestSelfIDFromCgroup pins the one inference this backend's self-identity rests
// on: containerd's default CgroupsPath is "/<namespace>/<id>", so a cornus
// deployed by cornus reads its own container id out of /proc/self/cgroup —
// anchored by the namespace, so no other cgroup layout on the host can produce
// an id this backend would then treat as itself.
func TestSelfIDFromCgroup(t *testing.T) {
	cases := []struct {
		name    string
		content string
		ns      string
		want    string
	}{{
		name:    "containerd default cgroup v2",
		content: "0::/cornus/cornus-web-0\n",
		ns:      "cornus",
		want:    "cornus-web-0",
	}, {
		name:    "containerd default cgroup v1",
		content: "12:memory:/cornus/cornus-web-0\n11:cpu,cpuacct:/cornus/cornus-web-0\n",
		ns:      "cornus",
		want:    "cornus-web-0",
	}, {
		name:    "custom namespace",
		content: "0::/staging/cornus-web-0\n",
		ns:      "staging",
		want:    "cornus-web-0",
	}, {
		// A container created with its own cgroup namespace sees the root and
		// names nothing: the guard is inert rather than wrong.
		name:    "cgroup namespace hides the path",
		content: "0::/\n",
		ns:      "cornus",
		want:    "",
	}, {
		// The host's own service manager, and a container in some OTHER
		// containerd namespace, must never be mistaken for us.
		name:    "systemd slice on the host",
		content: "0::/system.slice/cornus.service\n",
		ns:      "cornus",
		want:    "",
	}, {
		name:    "docker container",
		content: "0::/system.slice/docker-1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8.scope\n",
		ns:      "cornus",
		want:    "",
	}, {
		name:    "another containerd namespace",
		content: "0::/moby/1f0a2b3c\n",
		ns:      "cornus",
		want:    "",
	}, {
		// A deeper path is a pod/slice hierarchy, not containerd's default
		// two-segment spelling.
		name:    "deeper hierarchy",
		content: "0::/kubepods.slice/cornus/cornus-web-0\n",
		ns:      "cornus",
		want:    "",
	}, {
		name:    "empty namespace never matches",
		content: "0::/cornus/cornus-web-0\n",
		ns:      "",
		want:    "",
	}, {
		name:    "garbage",
		content: "not a cgroup file\n\n",
		ns:      "cornus",
		want:    "",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfIDFromCgroup(tc.content, tc.ns); got != tc.want {
				t.Errorf("selfIDFromCgroup(%q, %q) = %q, want %q", tc.content, tc.ns, got, tc.want)
			}
		})
	}
}

// newSelfTestBackend is newTestBackend with this cornus pinned as the container
// with the given id.
func newSelfTestBackend(t *testing.T, f *fakeClient, selfID string) *Backend {
	t.Helper()
	b, err := NewWithClient(f, Config{DataDir: t.TempDir()},
		WithPolicy(hostpolicy.Permissive()), WithSelfContainerID(selfID))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	b.net = newFakeNet()
	return b
}

// TestDeleteSkipsThisServersOwnContainer is the self-destruct regression: a
// deployment whose replica 0 is the very container cornus runs in must lose
// replica 1 and keep replica 0. This is the shape the compose orphan sweep
// reaches — it enumerates by cornus's labels and calls Delete on anything the
// project no longer defines, and a cornus deployed by cornus carries exactly
// those labels.
func TestDeleteSkipsThisServersOwnContainer(t *testing.T) {
	f := newFakeClient()
	b := newSelfTestBackend(t, f, "cornus-web-0")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := f.containers["cornus-web-0"]; !ok {
		t.Error("Delete removed the container this cornus is running in")
	}
	if _, ok := f.containers["cornus-web-1"]; ok {
		t.Error("Delete left a peer instance behind; the guard must be narrow")
	}
}

// TestDeleteRemovesEverythingWhenNotSelf is the negative control: the guard must
// not fire for a container that merely resembles the server's.
func TestDeleteRemovesEverythingWhenNotSelf(t *testing.T) {
	f := newFakeClient()
	b := newSelfTestBackend(t, f, "cornus-server-0")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, id := range []string{"cornus-web-0", "cornus-web-1"} {
		if _, ok := f.containers[id]; ok {
			t.Errorf("Delete left %s behind", id)
		}
	}
}

// TestApplyRefusesToRecreateTheServerItself proves the recreate path stops with
// an explanation instead of pressing on into a create that would collide with
// the container it just failed to clear.
func TestApplyRefusesToRecreateTheServerItself(t *testing.T) {
	f := newFakeClient()
	b := newSelfTestBackend(t, f, "cornus-web-0")
	ctx := context.Background()

	spec := api.DeploySpec{Name: "web", Image: "nginx:alpine"}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	_, err := b.Apply(ctx, spec)
	if err == nil {
		t.Fatal("re-Apply of a deployment that IS this server must fail")
	}
	if !strings.Contains(err.Error(), "own container") {
		t.Errorf("error = %v, want it to name the server's own container", err)
	}
	if _, ok := f.containers["cornus-web-0"]; !ok {
		t.Error("the failed re-Apply removed the server's own container anyway")
	}
}

// TestStopAndStartRefuseTheServerItself covers the lifecycle verbs: neither may
// act on the container running this process, and the refusal must say so rather
// than surface as a bare not-found.
func TestStopAndStartRefuseTheServerItself(t *testing.T) {
	f := newFakeClient()
	b := newSelfTestBackend(t, f, "cornus-web-0")
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, tc := range []struct {
		verb string
		call func() error
	}{
		{"stop", func() error { return b.Stop(ctx, "web") }},
		{"start", func() error { return b.Start(ctx, "web") }},
	} {
		err := tc.call()
		if err == nil {
			t.Fatalf("%s of a deployment that IS this server must fail", tc.verb)
		}
		if !strings.Contains(err.Error(), "own container") {
			t.Errorf("%s error = %v, want it to name the server's own container", tc.verb, err)
		}
		if errIsNotFound(err) {
			t.Errorf("%s error must not be ErrNotFound: the deployment plainly exists", tc.verb)
		}
	}
	c := f.containers["cornus-web-0"]
	if c == nil || c.task == nil {
		t.Fatal("the server's own task was torn down")
	}
}

func errIsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), deploy.ErrNotFound.Error())
}
