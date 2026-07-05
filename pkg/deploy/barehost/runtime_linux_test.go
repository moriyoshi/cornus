//go:build linux

package barehost

import (
	"errors"
	"testing"
)

func TestIsNotExist(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("container does not exist"), true},
		{errors.New("state.json: no such file or directory"), true},
		{errors.New("container with id foo not found"), true},
		{errors.New("permission denied"), false},
		{errors.New("EOF"), false},
	}
	for _, c := range cases {
		if got := isNotExist(c.err); got != c.want {
			t.Errorf("isNotExist(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestFakeRuntimeStateMachine(t *testing.T) {
	f := newFakeRuntime()
	ctx := t.Context()

	if _, err := f.State(ctx, "cornus-web-0"); err == nil {
		t.Fatal("State on unknown id: want error")
	}
	if err := f.Create(ctx, "cornus-web-0", "/bundle", createOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	st, err := f.State(ctx, "cornus-web-0")
	if err != nil {
		t.Fatalf("State after create: %v", err)
	}
	if st.Status != runcStateCreated {
		t.Errorf("status after create = %q, want %q", st.Status, runcStateCreated)
	}
	if err := f.Start(ctx, "cornus-web-0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st, _ = f.State(ctx, "cornus-web-0"); st.Status != runcStateRunning || st.Pid == 0 {
		t.Errorf("after start: status=%q pid=%d, want running with a pid", st.Status, st.Pid)
	}
	if err := f.Kill(ctx, "cornus-web-0", 15, false); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if st, _ = f.State(ctx, "cornus-web-0"); st.Status != runcStateStopped {
		t.Errorf("after kill: status=%q, want %q", st.Status, runcStateStopped)
	}
	if err := f.Delete(ctx, "cornus-web-0", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.State(ctx, "cornus-web-0"); err == nil {
		t.Fatal("State after delete: want error")
	}
}
