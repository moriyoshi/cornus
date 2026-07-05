//go:build linux

package incushost

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// countWarnings returns the number of WARN lines in captured log output.
func countWarnings(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "level=WARN") {
			n++
		}
	}
	return n
}

// TestEachUnsupportedFieldWarnsExactlyOnce drives the prelude with ONE
// unsupported field at a time and requires exactly one warning for it.
//
// Counting PER FIELD is the point, and it is the second attempt at this test.
// The first version collected the warnings for a spec requesting everything and
// asserted no message text appeared twice. That catches an identical duplicate
// and nothing else — while the bug it was written for, on 2026-07-29, was two
// DIFFERENTLY WORDED warnings for one field: incushost kept its own agentForward
// message after the shared deploy.WarnKubernetesOnlyFields already emitted one,
// so the operator saw "this backend offers no ssh-agent forwarding" directly
// above "available exactly when the server runs in remote mode". Two distinct
// strings, two distinct keys, one each — green.
//
// Re-creating that shape (a second, reworded Hub warning) confirmed the old test
// stayed green. Counting lines for a single-field spec cannot be fooled that way:
// one requested field, one warning, whatever it says.
func TestEachUnsupportedFieldWarnsExactlyOnce(t *testing.T) {
	for _, tc := range unsupportedFieldCases {
		t.Run(tc.field, func(t *testing.T) {
			buf := captureLogs(t)
			b := testBackend(newFakeConn())
			spec := tc.spec
			spec.Name = "web"
			spec.Image = "localhost:5000/app:v1"
			if _, err := b.buildInstancesPost(context.Background(), spec, 0); err != nil {
				t.Fatalf("buildInstancesPost: %v", err)
			}
			if n := countWarnings(buf.String()); n != 1 {
				t.Errorf("setting %s produced %d warnings, want exactly 1:\n%s", tc.field, n, buf.String())
			}
		})
	}
}

// TestWarnUnsupportedNeverRepeatsAWarning keeps the whole-spec check as well: it
// is weaker per field but catches a message that fires for a spec which did not
// request it, which the per-field test cannot see.
func TestWarnUnsupportedNeverRepeatsAWarning(t *testing.T) {
	buf := captureLogs(t)
	b := testBackend(newFakeConn())
	b.warnUnsupported(context.Background(), slog.Default(), supportedSpec())
	if n := countWarnings(buf.String()); n != 0 {
		t.Errorf("a fully-supported spec produced %d warnings, want none:\n%s", n, buf.String())
	}
}
