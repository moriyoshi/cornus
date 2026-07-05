package main

import (
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/deploy/containerdhost"
)

// TestLogShimCommandNameMatchesTheContainerdURIKey enforces the invariant
// LogShimCmd states in a comment and nothing checked: the CLI command name and
// the containerd binary-log-URI query key are the same string, used from two
// ends.
//
// containerd is handed `binary:///path/to/cornus?containerd-log-shim=<path>`,
// which its NewBinaryCmd turns into argv ["containerd-log-shim", "<path>"] — the
// query KEY becomes the subcommand name. If the two drift, nothing fails to build
// and nothing errors: containerd starts a task, execs a subcommand that does not
// exist, the shim dies, and the workload runs with its logs going nowhere.
// `cornus logs` returns empty for a container that is working fine, which reads
// as "the workload printed nothing" — the least likely explanation to be doubted.
//
// # Why this test was rewritten
//
// It used to declare its own `containerdLogShimArg = "containerd-log-shim"` and
// compare the kong tag against THAT — a third copy of the literal, living in the
// test. Its docstring argued the duplication was fine because "the point of the
// test is that the two spellings agree, so the literal has to appear twice
// somewhere, better here next to an assertion". That reasoning is wrong, and the
// attestation audit caught it: changing the production constant left this test
// green, because the test was a COPY of one side rather than an OBSERVER of both.
// A guard that carries its own copy of the value it guards cannot fail the way the
// code fails.
//
// The literal now lives once, as containerdhost.LogShimArg in that package's only
// build-tag-free file — it was previously in logs_linux.go, unreachable off Linux,
// which is what pushed the old test into copying it. The kong side cannot be
// derived from the constant (a struct tag must be a compile-time literal), so a
// comparison is unavoidable; what matters is that both sides of it are production
// values now.
func TestLogShimCommandNameMatchesTheContainerdURIKey(t *testing.T) {
	// Both registrations must agree: the hidden top-level alias, which is what the
	// URI targets, and the canonical nested spelling. Checking only one leaves the
	// other free to drift.
	for _, tc := range []struct {
		what  string
		owner reflect.Type
		field string
	}{
		{"hidden top-level alias (what the containerd log URI execs)", reflect.TypeOf(CLI{}), "LogShim"},
		{"canonical `cornus daemon` spelling", reflect.TypeOf(DaemonCmd{}), "LogShim"},
	} {
		t.Run(tc.owner.Name()+"."+tc.field, func(t *testing.T) {
			field, ok := tc.owner.FieldByName(tc.field)
			if !ok {
				t.Fatalf("%s has no %s field; this test has lost track of the command it guards",
					tc.owner.Name(), tc.field)
			}
			tag := field.Tag.Get("kong")
			want := "name='" + containerdhost.LogShimArg + "'"
			if !strings.Contains(tag, want) {
				t.Errorf("the %s is not named %q (kong tag: %q).\n"+
					"containerd derives the subcommand name from the binary log URI's query key "+
					"(containerdhost.LogShimArg), so a mismatch means it execs a subcommand that does not "+
					"exist: the shim dies, the workload keeps running, and `cornus logs` returns empty for a "+
					"healthy container.", tc.what, containerdhost.LogShimArg, tag)
			}
		})
	}
}
