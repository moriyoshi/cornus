package composecli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/cliout"
)

// In a non-live driver (plain/stream — a pipe, or --progress=stream) a
// serviceStatus must reproduce the exact append-only lines the CLI printed before
// the collapse: transitions as "<svc>  <inst>: <state>" events, transient
// diagnostics as warnings, terminal ones as errors, and a dependency wait as a
// "waiting" event. This is the contract that keeps scripted/piped output stable;
// the live collapse only applies on a fancy TTY under --progress=status.
func TestServiceStatusStreamFallback(t *testing.T) {
	var buf bytes.Buffer
	d := testDriver(&buf)
	prog := d.Progress() // plain mode -> not live
	if prog.Live() {
		t.Fatal("plain-mode Progress unexpectedly went live")
	}
	s := newServiceStatus(d, prog, "web")
	s.transition("web-0", "running")
	s.diagnostic("Back-off restarting failed container", false)
	s.diagnostic("image pull failed", true)
	s.waiting("for db (healthy)")
	s.done() // no-op on a non-live task

	got := buf.String()
	for _, want := range []string{
		"web  web-0: running\n",
		"warning: web: Back-off restarting failed container\n",
		"error: web: image pull failed\n",
		"web  waiting for db (healthy)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stream fallback missing %q; got:\n%s", want, got)
		}
	}
}

// A status-driven reportReconcile produces the same streamed output in a non-live
// driver as the standalone (nil-status) path: the fold onto a status line is only
// visible on a live TTY, so a pipe still sees per-instance transition events.
func TestReportReconcileWithStatusStreamsIdentically(t *testing.T) {
	script := []pollStep{
		{st: status(inst("web-0", "pending", false))},
		{st: status(inst("web-0", "running", true))},
	}
	var out bytes.Buffer
	d := testDriver(&out)
	prog := d.Progress()
	s := newServiceStatus(d, prog, "web")
	st := reportReconcile(context.Background(), &scriptedPoller{steps: script}, "web", "web", d, prog, s, time.Millisecond, 5*time.Second)

	got := out.String()
	for _, want := range []string{"web  web-0: pending\n", "web  web-0: running\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("status-driven reconcile missing %q; got:\n%s", want, got)
		}
	}
	if len(st.Instances) != 1 || !st.Instances[0].Running {
		t.Errorf("final status not running: %+v", st)
	}
}

// applyProgressFlag maps the --progress flag onto the driver, overriding the
// env-resolved style, and rejects an unknown value. An empty flag is a no-op.
func TestApplyProgressFlag(t *testing.T) {
	d := cliout.New(cliout.Options{Output: "plain", Env: func(string) string { return "" }})
	if err := applyProgressFlag(d, ""); err != nil || d.ProgressStyle() != cliout.ProgressStatus {
		t.Fatalf("empty flag: err=%v style=%v", err, d.ProgressStyle())
	}
	if err := applyProgressFlag(d, "stream"); err != nil || d.ProgressStyle() != cliout.ProgressStream {
		t.Fatalf("stream flag: err=%v style=%v", err, d.ProgressStyle())
	}
	if err := applyProgressFlag(d, "bogus"); err == nil {
		t.Fatal("bogus flag: want an error, got nil")
	}
}
