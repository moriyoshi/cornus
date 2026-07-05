package server

import (
	"context"
	"strings"
	"testing"

	"cornus/pkg/deploy"
	"cornus/pkg/hostenv"
)

// The failure this guards is SILENT: without shared propagation the runtime binds
// the underlying directory (empty, because it is a mountpoint), the deploy reports
// running, and the workload reads nothing with no error anywhere.
//
// The trap in guarding it is that propagation alone is the WRONG test — rootful
// docker works fine with private propagation, because its daemon shares this
// server's mount namespace. So both halves are asserted here: it fires for a
// backend that crosses namespaces, and stays silent for one that does not.

type propBackend struct {
	deploy.Backend
	crosses bool
}

func (b propBackend) MountsCrossNamespace(context.Context) bool { return b.crosses }

// fixedProp is a Mapper that reports one propagation for every path.
type fixedProp struct {
	hostenv.Mapper
	p string
}

func (f fixedProp) Propagation(string) string { return f.p }

func propServer(t *testing.T, p string) *Server {
	t.Helper()
	s := &Server{}
	s.host.mapper = fixedProp{p: p}
	s.cfg.DataDir = t.TempDir()
	return s
}

func TestPropagationRefusedWhenTheRuntimeCrossesNamespaces(t *testing.T) {
	s := propServer(t, hostenv.PropagationPrivate)
	err := s.mountPropagationPrecondition(context.Background(), propBackend{crosses: true})
	if err == nil {
		t.Fatal("a client-local mount was allowed onto a runtime in another mount namespace with " +
			"private propagation; it would come up EMPTY and report success")
	}
	// The message has to say what to do, and that the ordering is not negotiable —
	// a peer group is joined at namespace creation and cannot be joined later.
	for _, want := range []string{"different mount namespace", "rshared", "precede"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// TestPropagationIgnoredForACoLocatedRuntime is the half that keeps this from
// breaking a working configuration. Rootful docker reports private propagation on
// an ordinary host and works, because no mount crosses a namespace at all.
func TestPropagationIgnoredForACoLocatedRuntime(t *testing.T) {
	s := propServer(t, hostenv.PropagationPrivate)
	if err := s.mountPropagationPrecondition(context.Background(), propBackend{crosses: false}); err != nil {
		t.Fatalf("private propagation refused a runtime that shares this server's mount namespace, "+
			"which is the ordinary rootful docker case and works: %v", err)
	}
}

// TestPropagationAllowedWhenShared: the configuration the fix produces must pass.
func TestPropagationAllowedWhenShared(t *testing.T) {
	s := propServer(t, hostenv.PropagationShared)
	if err := s.mountPropagationPrecondition(context.Background(), propBackend{crosses: true}); err != nil {
		t.Fatalf("shared propagation was refused: %v", err)
	}
}

// TestPropagationUnknownIsNotRefused: "unknown" means this server could not READ
// the propagation, which is not evidence of a defect. Refusing on it would fail
// deploys on hosts where the reading is simply unavailable; hostcheck already
// warns at preflight.
func TestPropagationUnknownIsNotRefused(t *testing.T) {
	s := propServer(t, hostenv.PropagationUnknown)
	if err := s.mountPropagationPrecondition(context.Background(), propBackend{crosses: true}); err != nil {
		t.Fatalf("an unreadable propagation was treated as a defect: %v", err)
	}
}

// TestPropagationSkippedWithoutTheCapability: a backend that does not implement
// CrossNamespaceMounter is stating its runtime shares this namespace, so the
// question is never asked.
func TestPropagationSkippedWithoutTheCapability(t *testing.T) {
	s := propServer(t, hostenv.PropagationPrivate)
	var plain deploy.Backend
	if err := s.mountPropagationPrecondition(context.Background(), plain); err != nil {
		t.Fatalf("a backend without the capability was subjected to the check: %v", err)
	}
}
