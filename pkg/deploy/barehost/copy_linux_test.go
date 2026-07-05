//go:build linux

package barehost

import (
	"bytes"
	"errors"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

func TestCopyUnknownDeploymentErrNotFound(t *testing.T) {
	b, _ := newTestBackend(t)
	if _, err := b.StatPath(t.Context(), "ghost", "/etc"); !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("StatPath(unknown) = %v, want ErrNotFound", err)
	}
	if _, err := b.CopyFrom(t.Context(), "ghost", "/etc", &bytes.Buffer{}); !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("CopyFrom(unknown) = %v, want ErrNotFound", err)
	}
	if err := b.CopyTo(t.Context(), "ghost", "/etc", &bytes.Buffer{}, api.CopyToOptions{}); !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("CopyTo(unknown) = %v, want ErrNotFound", err)
	}
}

func TestCopyRequiresRunningInstance(t *testing.T) {
	b, rt := newTestBackend(t)
	seedInstance(t, b, rt, "svc", 0, false) // created, not running
	if _, err := b.StatPath(t.Context(), "svc", "/etc"); err == nil {
		t.Error("StatPath on a non-running instance should error (needs /proc/<pid>/root)")
	}
}
