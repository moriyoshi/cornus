package e2e

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.starlark.net/starlark"
)

// nopTarget is a Target with no external runtime: it lets the harness machinery
// (Starlark interpreter, serve(), registry_roundtrip(), asserts) be tested
// without Docker or kind. The cornus server runs with its default backend,
// which the registry scenario never invokes.
type nopTarget struct{}

func (nopTarget) Name() string                               { return "test" }
func (nopTarget) Setup(context.Context) error                { return nil }
func (nopTarget) Teardown(context.Context) error             { return nil }
func (nopTarget) ServeEnv() []string                         { return nil }
func (nopTarget) PrepareImage(context.Context, string) error { return nil }

// TestHarnessRegistryScenario runs a real registry round-trip scenario through
// the Starlark harness against a prebuilt cornus binary. It is skipped unless
// CORNUS_BIN points to that binary (built with `go build ./cmd/cornus`).
func TestHarnessRegistryScenario(t *testing.T) {
	bin := os.Getenv("CORNUS_BIN")
	if bin == "" {
		t.Skip("set CORNUS_BIN to a cornus binary to run the e2e harness test")
	}

	scenario := filepath.Join(t.TempDir(), "registry.star")
	script := `
serve()
digest = registry_roundtrip(ref = "harness/demo:v1")
log("digest " + digest)
assert_contains(digest, "sha256:")
`
	if err := os.WriteFile(scenario, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	out := io.Discard
	if testing.Verbose() {
		out = os.Stdout
	}
	h := New(nopTarget{}, bin, "mem://", out)
	if err := h.RunFile(context.Background(), scenario); err != nil {
		t.Fatalf("scenario failed: %v", err)
	}
}

func TestScenariosParse(t *testing.T) {
	matches, err := filepath.Glob("../../e2e/scenarios/*.star")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no scenarios found")
	}
	for _, m := range matches {
		if err := Check(m); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}

// TestPredeclaredNamesInSync guards Check's resolve set against drift from the
// actual builtins registered in predeclared().
func TestPredeclaredNamesInSync(t *testing.T) {
	h := New(nopTarget{}, "", "", io.Discard)
	got := h.predeclared()
	names := predeclaredNames()
	for k := range got {
		if !names[k] {
			t.Errorf("predeclared() defines %q but predeclaredNames() omits it", k)
		}
	}
	for n := range names {
		if _, ok := got[n]; !ok {
			t.Errorf("predeclaredNames() lists %q but predeclared() omits it", n)
		}
	}
}

// TestComposeBuildImageRefs covers the enumeration behind the kube target's
// compose_up image pre-load: `build:` services (and only those) map to
// <registry>/<project>-<service>:latest, with the project name resolved the
// way the compose CLI resolves it (explicit -p verbatim, else the directory
// default).
func TestComposeBuildImageRefs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "refsproj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	src := `
services:
  web:
    build: ./web
  worker:
    build:
      context: ./worker
  cache:
    image: alpine:3.20
    command: ["sleep", "infinity"]
`
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Explicit project name: used verbatim.
	refs, project, err := composeBuildImageRefs("127.0.0.1:5000", file, "myproj")
	if err != nil {
		t.Fatal(err)
	}
	if project != "myproj" {
		t.Errorf("project = %q, want myproj", project)
	}
	want := []string{
		"127.0.0.1:5000/myproj-web:latest",
		"127.0.0.1:5000/myproj-worker:latest",
	}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("refs[%d] = %q, want %q", i, refs[i], want[i])
		}
	}

	// No project name: the compose default (the file's directory name).
	refs, project, err = composeBuildImageRefs("127.0.0.1:5000", file, "")
	if err != nil {
		t.Fatal(err)
	}
	if project != "refsproj" {
		t.Errorf("default project = %q, want refsproj", project)
	}
	if len(refs) != 2 || refs[0] != "127.0.0.1:5000/refsproj-web:latest" {
		t.Errorf("default-project refs = %v", refs)
	}

	// Build-free compose file: no refs, so the pre-load is a no-op.
	noBuild := filepath.Join(dir, "nobuild.yaml")
	if err := os.WriteFile(noBuild, []byte("services:\n  cache:\n    image: alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, _, err = composeBuildImageRefs("127.0.0.1:5000", noBuild, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("build-free compose file produced refs %v", refs)
	}
}

// TestCheckCatchesResolveErrors ensures Check resolves (not just parses): an
// undefined-name reference — which parse-only would miss — must be rejected,
// while a valid scenario using the DSL's top-level for and global reassignment
// passes (proving Check shares RunFile's FileOptions).
func TestCheckCatchesResolveErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.star")
	if err := os.WriteFile(bad, []byte("no_such_builtin(name = \"x\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(bad); err == nil {
		t.Error("Check accepted a reference to an undefined name")
	}

	good := filepath.Join(dir, "good.star")
	// Top-level for + reassignment of a global — both allowed by the scenario
	// FileOptions, so this must resolve cleanly.
	src := "serve()\nn = 0\nfor x in [1, 2]:\n    n = x\n    log(msg = str(n))\n"
	if err := os.WriteFile(good, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Check(good); err != nil {
		t.Errorf("Check rejected a valid scenario: %v", err)
	}
}

// TestBuiltinsRequireServe verifies the deploy/status/wait/action/remove builtins
// surface a clean "call serve() first" error — instead of panicking on a nil
// h.client — when invoked before serve() has run.
func TestBuiltinsRequireServe(t *testing.T) {
	h := New(nopTarget{}, "", "", io.Discard)
	h.ctx = context.Background()
	cases := []struct {
		name string
		call func() (starlark.Value, error)
	}{
		{"deploy", func() (starlark.Value, error) {
			return h.bDeploy(nil, nil, starlark.Tuple{starlark.String("app"), starlark.String("img")}, nil)
		}},
		{"status", func() (starlark.Value, error) {
			return h.bStatus(nil, nil, starlark.Tuple{starlark.String("app")}, nil)
		}},
		{"wait", func() (starlark.Value, error) {
			return h.bWait(nil, nil, starlark.Tuple{starlark.String("app")}, nil)
		}},
		{"start", func() (starlark.Value, error) {
			return h.action("start")(nil, nil, starlark.Tuple{starlark.String("app")}, nil)
		}},
		{"remove", func() (starlark.Value, error) {
			return h.bRemove(nil, nil, starlark.Tuple{starlark.String("app")}, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call() // must not panic on the nil h.client
			if err == nil || !strings.Contains(err.Error(), "serve()") {
				t.Fatalf("%s before serve(): err = %v, want mention of serve()", tc.name, err)
			}
		})
	}
}

// tunnelStub writes an executable shell stub standing in for `cornus tunnel`.
func tunnelStub(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cornus-stub")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestTunnelFailsFastWithoutURL is the regression guard for the stdout write-end
// leak: a tunnel process that exits without printing a public URL must make the
// reader observe EOF and fail fast, not block for the full 60s timeout.
func TestTunnelFailsFastWithoutURL(t *testing.T) {
	h := New(nopTarget{}, tunnelStub(t, "exit 0\n"), "", io.Discard)
	h.ctx = context.Background()
	t.Cleanup(h.stopPortForwards)

	type res struct {
		v   starlark.Value
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := h.bTunnel(nil, nil, nil, []starlark.Tuple{
			{starlark.String("name"), starlark.String("app")},
			{starlark.String("port"), starlark.MakeInt(8080)},
			{starlark.String("server"), starlark.String("http://127.0.0.1:1")},
		})
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		if r.err == nil || !strings.Contains(r.err.Error(), "before printing a public URL") {
			t.Fatalf("err = %v, want 'exited before printing a public URL'", r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("bTunnel hung: pr never saw EOF (regression of the pw write-end leak)")
	}
}

// TestTunnelReportsURL covers the success path: the reader parses the URL from
// the "ready at " line and returns it while the tunnel process keeps running.
func TestTunnelReportsURL(t *testing.T) {
	h := New(nopTarget{}, tunnelStub(t, "echo 'Tunnel to app:8080 ready at http://pub.example'\nsleep 30\n"), "", io.Discard)
	h.ctx = context.Background()
	t.Cleanup(h.stopPortForwards)

	type res struct {
		v   starlark.Value
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := h.bTunnel(nil, nil, nil, []starlark.Tuple{
			{starlark.String("name"), starlark.String("app")},
			{starlark.String("port"), starlark.MakeInt(8080)},
			{starlark.String("server"), starlark.String("http://127.0.0.1:1")},
		})
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("err = %v, want URL", r.err)
		}
		if s, ok := starlark.AsString(r.v); !ok || s != "http://pub.example" {
			t.Fatalf("url = %v, want http://pub.example", r.v)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("bTunnel did not report the URL in time")
	}
}

// blockingWriter records the first time and payload written to it, so a test can
// assert that writeAfterSettle held the input back until the child's output went
// quiet rather than blasting it up front (the flake fixed in bExecTTY, where an
// interactive shell's startup cursor-position query swallows pre-typed bytes).
type blockingWriter struct {
	got   chan string
	start time.Time
	at    time.Duration
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.at = time.Since(b.start)
	select {
	case b.got <- string(p):
	default:
	}
	return len(p), nil
}

// TestWriteAfterSettleWaitsForQuiet asserts writeAfterSettle defers the write
// until output has been idle for `settle`, and that continued activity keeps
// pushing the write back (the input must never land while the child is still
// emitting its prompt / cursor query).
func TestWriteAfterSettleWaitsForQuiet(t *testing.T) {
	defer func(s, m time.Duration) { execTTYSettle, execTTYMaxWait = s, m }(execTTYSettle, execTTYMaxWait)
	execTTYSettle = 50 * time.Millisecond
	execTTYMaxWait = 5 * time.Second

	w := &blockingWriter{got: make(chan string, 1), start: time.Now()}
	activity := make(chan struct{}, 1)
	go writeAfterSettle(context.Background(), w, "RUN\n", activity)

	// Pulse activity a few times ~30ms apart (< settle): the write must stay held.
	for i := 0; i < 4; i++ {
		activity <- struct{}{}
		time.Sleep(30 * time.Millisecond)
		select {
		case s := <-w.got:
			t.Fatalf("input written during activity (%q) at %v; must wait for quiet", s, w.at)
		default:
		}
	}
	// Now go quiet: the write should land shortly after `settle`.
	select {
	case s := <-w.got:
		if s != "RUN\n" {
			t.Fatalf("wrote %q, want %q", s, "RUN\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input was never written after output went quiet")
	}
}

// TestWriteAfterSettleSilentChild asserts a child that never produces output
// still receives the input once the overall deadline elapses.
func TestWriteAfterSettleSilentChild(t *testing.T) {
	defer func(s, m time.Duration) { execTTYSettle, execTTYMaxWait = s, m }(execTTYSettle, execTTYMaxWait)
	execTTYSettle = 50 * time.Millisecond
	execTTYMaxWait = 100 * time.Millisecond

	w := &blockingWriter{got: make(chan string, 1), start: time.Now()}
	activity := make(chan struct{}, 1) // never pulsed
	go writeAfterSettle(context.Background(), w, "RUN\n", activity)

	select {
	case s := <-w.got:
		if s != "RUN\n" {
			t.Fatalf("wrote %q, want %q", s, "RUN\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("silent child never received input by the deadline")
	}
}

// TestWriteAfterSettleContextCancel asserts a cancelled harness context stops the
// writer without emitting input (no write to a torn-down PTY).
func TestWriteAfterSettleContextCancel(t *testing.T) {
	defer func(s, m time.Duration) { execTTYSettle, execTTYMaxWait = s, m }(execTTYSettle, execTTYMaxWait)
	execTTYSettle = 50 * time.Millisecond
	execTTYMaxWait = 5 * time.Second

	w := &blockingWriter{got: make(chan string, 1), start: time.Now()}
	activity := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { writeAfterSettle(ctx, w, "RUN\n", activity); close(done) }()

	activity <- struct{}{} // enter the quiet-wait loop
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("writeAfterSettle did not return after context cancel")
	}
	select {
	case s := <-w.got:
		t.Fatalf("input %q written despite context cancel", s)
	default:
	}
}

// TestComposeBuiltinTimeout asserts the compose builtin's defensive cap converts
// a compose subcommand that never returns — the failure mode of a FOREGROUND
// `compose up` (no -d), which holds the session until Ctrl-C — into a prompt,
// diagnosable error instead of hanging the whole suite until the CI job cap.
func TestComposeBuiltinTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakecornus")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services:\n  web:\n    image: alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	defer func(d time.Duration) { composeCallTimeout = d }(composeCallTimeout)
	composeCallTimeout = 200 * time.Millisecond

	h := New(nopTarget{}, fake, "mem://", io.Discard)
	h.ctx = context.Background()
	h.registryHost = "127.0.0.1:0"

	fn := h.compose("up")
	start := time.Now()
	_, err := fn(nil, nil, nil, []starlark.Tuple{{starlark.String("file"), starlark.String(file)}})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "did not return within") {
		t.Fatalf("error %q should explain the foreground-hold timeout", err)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("builtin took %v; the defensive cap did not fire promptly", el)
	}
}

// TestComposeBuiltinHarnessCancel asserts a harness-wide cancel (not the
// defensive cap) surfaces as a plain error, not the misleading foreground-hold
// hint — the cap must only claim a hold when it actually fired.
func TestComposeBuiltinHarnessCancel(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fakecornus")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services:\n  web:\n    image: alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	defer func(d time.Duration) { composeCallTimeout = d }(composeCallTimeout)
	composeCallTimeout = time.Hour // never the cause here

	ctx, cancel := context.WithCancel(context.Background())
	h := New(nopTarget{}, fake, "mem://", io.Discard)
	h.ctx = ctx
	h.registryHost = "127.0.0.1:0"

	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	fn := h.compose("up")
	_, err := fn(nil, nil, nil, []starlark.Tuple{{starlark.String("file"), starlark.String(file)}})
	if err == nil {
		t.Fatal("expected an error from the cancelled harness ctx, got nil")
	}
	if strings.Contains(err.Error(), "did not return within") {
		t.Fatalf("harness cancel must not be reported as a foreground-hold timeout: %v", err)
	}
}

// TestKubeWaitDiagNonKube confirms the wait-timeout kube diagnostics are a no-op
// on non-kube targets, so a docker/containerd `wait` timeout message is unchanged.
func TestKubeWaitDiagNonKube(t *testing.T) {
	h := New(nopTarget{}, "", "", io.Discard)
	if diag := h.kubeWaitDiag("anything"); diag != "" {
		t.Errorf("kubeWaitDiag on a non-kube target should be empty, got %q", diag)
	}
}
