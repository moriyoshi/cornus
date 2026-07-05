package dockerhost

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// TestSameContainer pins the id comparison. It has to tolerate Docker's 12-hex
// short form in both directions (that form is a container's default HOSTNAME and
// is accepted everywhere the API takes an id), while refusing to call a short
// coincidence a match — a false positive here makes an unrelated workload
// permanently undeletable.
func TestSameContainer(t *testing.T) {
	const full = "1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
	cases := []struct {
		a, b string
		want bool
	}{
		{full, full, true},
		{full, "1f0a2b3c4d5e", true},
		{"1f0a2b3c4d5e", full, true},
		{full, "1f0a2b3c4d5", false}, // 11 chars: shorter than Docker ever hands out
		{full, "1f0a", false},
		{full, "", false},
		{"", full, false},
		{"", "", false},
		{full, "deadbeefdeadbeef", false},
	}
	for _, tc := range cases {
		if got := sameContainer(tc.a, tc.b); got != tc.want {
			t.Errorf("sameContainer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestDeleteSkipsThisServersOwnContainer is the self-destruct regression.
//
// A cornus deployed by cornus onto the same daemon carries cornus.managed /
// cornus.app like any workload, which puts it in every enumeration this backend
// makes — including the one the compose orphan sweep drives (List by
// cornus.managed=true, then Delete anything the project no longer defines).
// Delete must take the peer replica and leave the server standing.
func TestDeleteSkipsThisServersOwnContainer(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithSelfContainerID("id-cornus-web-0"))
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := f.removedSnapshot(); len(got) != 1 || got[0] != "id-cornus-web-1" {
		t.Fatalf("removed = %v, want only the peer replica id-cornus-web-1", got)
	}
	if _, ok := f.containers["id-cornus-web-0"]; !ok {
		t.Error("Delete removed the container this cornus is running in")
	}
}

// TestDeleteRemovesEverythingWhenNotSelf is the negative control: a pinned id
// that matches nothing must leave the ordinary teardown completely untouched.
func TestDeleteRemovesEverythingWhenNotSelf(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithSelfContainerID("id-cornus-server-0"))
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine", Replicas: 2}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.Delete(ctx, "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, id := range []string{"id-cornus-web-0", "id-cornus-web-1"} {
		if _, ok := f.containers[id]; ok {
			t.Errorf("Delete left %s behind", id)
		}
	}
}

// TestSelfGuardMatchesAShortID confirms the guard still fires when the pinned id
// is Docker's abbreviated form while the daemon reports the full one — the
// spelling an operator (or a HOSTNAME-derived signal) is most likely to supply.
func TestSelfGuardMatchesAShortID(t *testing.T) {
	const full = "1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
	f := &fakeDocker{containers: map[string]map[string]any{
		full: {"Id": full, "Image": "nginx:alpine", "State": "running",
			"Labels": map[string]string{deploy.LabelManaged: "true", deploy.LabelApp: "web"}},
	}}
	b := newTestBackendOpts(t, f, WithSelfContainerID("1f0a2b3c4d5e"))

	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := f.removedSnapshot(); len(got) != 0 {
		t.Fatalf("removed = %v, want nothing: the only instance is this server", got)
	}
}

// TestApplyRefusesToRecreateTheServerItself proves the recreate path stops with
// an explanation. Pressing on would hit dockerd's opaque "name is already in
// use" on the container it just failed to clear — and a recreate that DID
// succeed would have killed the process serving the request.
func TestApplyRefusesToRecreateTheServerItself(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithSelfContainerID("id-cornus-web-0"))
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
	if _, ok := f.containers["id-cornus-web-0"]; !ok {
		t.Error("the failed re-Apply removed the server's own container anyway")
	}
}

// TestStopAndRestartRefuseTheServerItself covers the lifecycle verbs. The
// refusal must be distinguishable from "no such deployment": the deployment
// plainly exists, and a bare not-found would send the operator hunting for it.
func TestStopAndRestartRefuseTheServerItself(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackendOpts(t, f, WithSelfContainerID("id-cornus-web-0"))
	ctx := context.Background()

	if _, err := b.Apply(ctx, api.DeploySpec{Name: "web", Image: "nginx:alpine"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.mu.Lock()
	f.stopped = nil
	f.mu.Unlock()

	for _, tc := range []struct {
		verb string
		call func() error
	}{
		{"stop", func() error { return b.Stop(ctx, "web") }},
		{"restart", func() error { return b.Restart(ctx, "web") }},
		{"start", func() error { return b.Start(ctx, "web") }},
	} {
		err := tc.call()
		if err == nil {
			t.Fatalf("%s of a deployment that IS this server must fail", tc.verb)
		}
		if !strings.Contains(err.Error(), "own container") {
			t.Errorf("%s error = %v, want it to name the server's own container", tc.verb, err)
		}
		if isNotFoundErr(err) {
			t.Errorf("%s error must not be ErrNotFound: the deployment plainly exists", tc.verb)
		}
	}
	f.mu.Lock()
	stopped := append([]string(nil), f.stopped...)
	f.mu.Unlock()
	if len(stopped) != 0 {
		t.Errorf("stopped = %v, want nothing: the only instance is this server", stopped)
	}
}

// removedSnapshot copies the fake's DELETE /containers/{id} log under its lock.
func (f *fakeDocker) removedSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func isNotFoundErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), deploy.ErrNotFound.Error())
}
