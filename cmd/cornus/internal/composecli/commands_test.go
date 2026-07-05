package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
	"cornus/pkg/compose"

	"github.com/alecthomas/kong"
)

// scriptedDeleter records the deployment names passed to Delete and can fail a
// chosen one, standing in for *client.Client in removeDeployments tests. Its
// Status feeds the post-delete teardown wait: with poller nil it reports the
// deployment already gone (zero instances) so the wait returns at once; set
// poller to drive a scripted terminate→gone progression.
type scriptedDeleter struct {
	deleted []string
	failOn  string
	poller  *scriptedPoller
}

func (s *scriptedDeleter) Delete(_ context.Context, name string) error {
	s.deleted = append(s.deleted, name)
	if name == s.failOn {
		return errors.New("boom")
	}
	return nil
}

func (s *scriptedDeleter) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	if s.poller != nil {
		return s.poller.Status(ctx, name)
	}
	return api.DeployStatus{Name: name}, nil // no instances: already gone
}

// TestRemoveDeploymentsOnForegroundExit pins the regression this change fixes:
// terminating a foreground `up` must remove the mount-free deployments it
// created (Ctrl-C stops everything it brought up, like `docker compose up`). It
// deletes each service's deployment resource in reverse selection order, and one
// delete failing must not stop the rest.
func TestRemoveDeploymentsOnForegroundExit(t *testing.T) {
	plans := map[string]compose.ServicePlan{
		"web": {Service: "web", Resource: "demo-web"},
		"api": {Service: "api", Resource: "demo-api"},
		"db":  {Service: "db", Resource: "demo-db"},
	}
	d := cliout.New(cliout.Options{Stdout: io.Discard, Stderr: io.Discard, Output: "plain"})

	t.Run("reverse order, all deleted", func(t *testing.T) {
		del := &scriptedDeleter{}
		removeDeployments(del, plans, []string{"web", "api", "db"}, d)
		want := []string{"demo-db", "demo-api", "demo-web"}
		if !reflect.DeepEqual(del.deleted, want) {
			t.Fatalf("deleted = %v; want %v", del.deleted, want)
		}
	})

	t.Run("one failure does not stop the rest", func(t *testing.T) {
		del := &scriptedDeleter{failOn: "demo-api"}
		removeDeployments(del, plans, []string{"web", "api", "db"}, d)
		want := []string{"demo-db", "demo-api", "demo-web"}
		if !reflect.DeepEqual(del.deleted, want) {
			t.Fatalf("deleted = %v; want %v (all attempted despite the failure)", del.deleted, want)
		}
	})

	t.Run("nothing to remove is a no-op", func(t *testing.T) {
		del := &scriptedDeleter{}
		removeDeployments(del, plans, nil, d)
		if len(del.deleted) != 0 {
			t.Fatalf("deleted = %v; want none", del.deleted)
		}
	})

	// The regression this change adds: after deleting, a foreground `up` exit must
	// wait for the cluster-side teardown to drain (poll status until zero
	// instances) and report the transitions, the way `down` does — not claim
	// "removed" the instant the delete is accepted.
	t.Run("waits for teardown reconcile like down", func(t *testing.T) {
		del := &scriptedDeleter{poller: &scriptedPoller{steps: []pollStep{
			{st: status(inst("web-0", "pending", false))}, // still terminating
			{st: status()}, // gone
		}}}
		var out bytes.Buffer
		removeDeployments(del, plans, []string{"web"}, testDriver(&out))
		if n := del.poller.count(); n < 2 {
			t.Fatalf("teardown wait polled status %d times; want it to poll until gone (>=2)", n)
		}
		got := out.String()
		for _, want := range []string{"web  web-0: pending\n", "web  removed\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("teardown output missing %q; got:\n%s", want, got)
			}
		}
	})
}

// TestDownWaitFlag pins the `down` synchronous-by-default behavior: --wait
// defaults to true (matching `docker compose down`), and --no-wait turns it off.
func TestDownWaitFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"default is wait", []string{"down"}, true},
		{"explicit --wait", []string{"down", "--wait"}, true},
		{"--no-wait opts out", []string{"down", "--no-wait"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cli struct {
				Down DownCmd `kong:"cmd"`
			}
			parser, err := kong.New(&cli, kong.Name("cornus"))
			if err != nil {
				t.Fatalf("kong.New: %v", err)
			}
			if _, err := parser.Parse(c.args); err != nil {
				t.Fatalf("parse %v: %v", c.args, err)
			}
			if cli.Down.Wait != c.want {
				t.Fatalf("Wait = %v; want %v", cli.Down.Wait, c.want)
			}
		})
	}
}

// TestHoldForeground pins the foreground `up` block-vs-exit decision. The
// regression it guards: a compose that publishes no ports (no forwards, no
// mounts) must still stay attached like `docker compose up`, not exit at once.
func TestHoldForeground(t *testing.T) {
	cases := []struct {
		name      string
		detached  bool
		services  int
		sessions  int
		noForward bool
		want      bool
	}{
		{"no ports, no mounts still holds (the bug)", false, 2, 0, false, true},
		{"forwarded ports hold", false, 1, 0, false, true},
		{"client-local mounts hold", false, 1, 1, false, true},
		{"mounts hold even with --no-forward-ports", false, 1, 1, true, true},
		{"--no-forward-ports, no mounts exits", false, 2, 0, true, false},
		{"detached exits", true, 2, 0, false, false},
		{"no services selected exits", false, 0, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := holdForeground(c.detached, c.services, c.sessions, c.noForward); got != c.want {
				t.Fatalf("holdForeground(%v, %d, %d, %v) = %v; want %v", c.detached, c.services, c.sessions, c.noForward, got, c.want)
			}
		})
	}
}

// TestShutdownExit pins the exit-code contract for a foreground `up` whose
// startup deploy loop bails out: a cancelled context (interactive Ctrl-C /
// SIGINT) is a clean shutdown (nil error, remove the mount-free deployments) so
// the process exits 0 on every backend, while a genuine error with a live
// context propagates unchanged and removes nothing. Guards the regression where
// a SIGINT racing the slower kubernetes reconcile left the client still in the
// startup loop, so it returned bare context.Canceled (exit 1) instead of the
// exit 0 docker/containerd produced by reaching the steady-state hold first.
func TestShutdownExit(t *testing.T) {
	genuine := errors.New("build boom")
	cases := []struct {
		name       string
		genuine    error
		ctxErr     error
		wantErr    error
		wantRemove bool
	}{
		{"ctrl-c during startup exits clean and removes", nil, context.Canceled, nil, true},
		{"ctrl-c wins over a wrapped cancel error", genuine, context.Canceled, nil, true},
		{"genuine error with live ctx propagates", genuine, nil, genuine, false},
		{"no error, live ctx is a plain return", nil, nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err, remove := shutdownExit(c.genuine, c.ctxErr)
			if err != c.wantErr {
				t.Fatalf("shutdownExit(%v, %v) err = %v; want %v", c.genuine, c.ctxErr, err, c.wantErr)
			}
			if remove != c.wantRemove {
				t.Fatalf("shutdownExit(%v, %v) remove = %v; want %v", c.genuine, c.ctxErr, remove, c.wantRemove)
			}
		})
	}
}

func TestResolveBuildSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/run/agent.sock")

	// Compose file build.ssh + a bare "default": both resolve to $SSH_AUTH_SOCK,
	// an explicit "id=socket" keeps its socket.
	got, err := resolveBuildSSH([]string{"default", "keyed=/tmp/k.sock"}, nil)
	if err != nil {
		t.Fatalf("resolveBuildSSH: %v", err)
	}
	want := map[string]string{"default": "/run/agent.sock", "keyed": "/tmp/k.sock"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveBuildSSH = %v; want %v", got, want)
	}

	// A --ssh entry overrides the compose-file entry with the same id.
	got, err = resolveBuildSSH([]string{"default=/file.sock"}, []string{"default=/cli.sock"})
	if err != nil {
		t.Fatalf("resolveBuildSSH override: %v", err)
	}
	if got["default"] != "/cli.sock" {
		t.Fatalf("cli override = %q; want /cli.sock", got["default"])
	}

	// No SSH requested: nil map, no error.
	if got, err := resolveBuildSSH(nil, nil); err != nil || got != nil {
		t.Fatalf("resolveBuildSSH(nil, nil) = %v, %v; want nil, nil", got, err)
	}

	// A bare id with no agent socket available is an error.
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := resolveBuildSSH([]string{"default"}, nil); err == nil {
		t.Fatal("resolveBuildSSH with no socket: expected error, got nil")
	}
}

// psTestData builds a small project: "web" and "api" are created (with running
// summaries), "db" is not created and falls back to its spec image.
func psTestData() (order []string, plans map[string]compose.ServicePlan, byResource map[string]api.DeployStatus) {
	order = []string{"web", "api", "db"}
	plans = map[string]compose.ServicePlan{
		"web": {Service: "web", Resource: "demo-web", Spec: api.DeploySpec{Image: "web:spec"}},
		"api": {Service: "api", Resource: "demo-api", Spec: api.DeploySpec{Image: "api:spec"}},
		"db":  {Service: "db", Resource: "demo-db", Spec: api.DeploySpec{Image: "db:spec"}},
	}
	byResource = map[string]api.DeployStatus{
		// web reports its own image; api reports none, so it falls back to the spec.
		"demo-web": {Name: "demo-web", Image: "web:pushed", Instances: []api.InstanceStatus{inst("web-0", "running", true)}},
		"demo-api": {Name: "demo-api", Instances: []api.InstanceStatus{inst("api-0", "pending", false)}},
	}
	return
}

// psOutDriver returns a plain-mode driver whose stdout (where the scripting/json
// ps paths write, via d.Out()) is captured in buf.
func psOutDriver(buf *bytes.Buffer) *cliout.Driver {
	return cliout.New(cliout.Options{Stdout: buf, Stderr: io.Discard, Output: "plain"})
}

func TestPsRows(t *testing.T) {
	order, plans, byResource := psTestData()
	got := psRows(order, plans, byResource)
	want := []psRow{
		{Service: "web", Name: "demo-web", Image: "web:pushed", Status: "1/1 running", created: true},
		{Service: "api", Name: "demo-api", Image: "api:spec", Status: "0/1 running", created: true},
		{Service: "db", Name: "demo-db", Image: "db:spec", Status: "not created", created: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("psRows =\n%#v\nwant\n%#v", got, want)
	}
}

func TestRenderPsQuiet(t *testing.T) {
	order, plans, byResource := psTestData()
	rows := psRows(order, plans, byResource)
	var out bytes.Buffer
	if err := renderPs(psOutDriver(&out), rows, true, false, "table"); err != nil {
		t.Fatalf("renderPs quiet: %v", err)
	}
	// Only created services (web, api); the not-created db is skipped. One
	// resource id per line.
	if got, want := out.String(), "demo-web\ndemo-api\n"; got != want {
		t.Fatalf("quiet output = %q; want %q", got, want)
	}
}

func TestRenderPsServices(t *testing.T) {
	order, plans, byResource := psTestData()
	rows := psRows(order, plans, byResource)
	var out bytes.Buffer
	if err := renderPs(psOutDriver(&out), rows, false, true, "table"); err != nil {
		t.Fatalf("renderPs services: %v", err)
	}
	// Every service name in dependency order, regardless of created state.
	if got, want := out.String(), "web\napi\ndb\n"; got != want {
		t.Fatalf("services output = %q; want %q", got, want)
	}
}

func TestRenderPsJSON(t *testing.T) {
	order, plans, byResource := psTestData()
	rows := psRows(order, plans, byResource)
	var out bytes.Buffer
	if err := renderPs(psOutDriver(&out), rows, false, false, "json"); err != nil {
		t.Fatalf("renderPs json: %v", err)
	}
	var got []psRow
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal json output %q: %v", out.String(), err)
	}
	// The unexported `created` field is not serialized, so compare on the
	// exported fields only.
	want := []psRow{
		{Service: "web", Name: "demo-web", Image: "web:pushed", Status: "1/1 running"},
		{Service: "api", Name: "demo-api", Image: "api:spec", Status: "0/1 running"},
		{Service: "db", Name: "demo-db", Image: "db:spec", Status: "not created"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("json rows = %#v; want %#v", got, want)
	}
}

func TestRenderPsUnknownFormat(t *testing.T) {
	order, plans, byResource := psTestData()
	rows := psRows(order, plans, byResource)
	var out bytes.Buffer
	err := renderPs(psOutDriver(&out), rows, false, false, "yaml")
	if err == nil {
		t.Fatal("renderPs with unknown format: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("error = %v; want it to mention unsupported format", err)
	}
}

func TestDaemonHeldService(t *testing.T) {
	cases := []struct {
		name        string
		mountCount  int
		daemonAlive bool
		want        bool
	}{
		{"mounts and daemon alive", 2, true, true},
		{"mounts but no daemon", 2, false, false},
		{"no mounts but daemon alive", 0, true, false},
		{"no mounts and no daemon", 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := daemonHeldService(c.mountCount, c.daemonAlive); got != c.want {
				t.Fatalf("daemonHeldService(%d, %v) = %v; want %v", c.mountCount, c.daemonAlive, got, c.want)
			}
		})
	}
}

// TestParseBuildArgs checks --build-arg parsing: KEY=VALUE sets the value, a bare
// KEY takes it from the environment, and an empty name is rejected.
func TestParseBuildArgs(t *testing.T) {
	t.Setenv("FROM_ENV", "envval")
	got, err := parseBuildArgs([]string{"K=v", "EMPTY=", "FROM_ENV"})
	if err != nil {
		t.Fatalf("parseBuildArgs: %v", err)
	}
	want := map[string]string{"K": "v", "EMPTY": "", "FROM_ENV": "envval"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseBuildArgs = %v, want %v", got, want)
	}
	if got, err := parseBuildArgs(nil); err != nil || got != nil {
		t.Fatalf("parseBuildArgs(nil) = %v, %v; want nil, nil", got, err)
	}
	if _, err := parseBuildArgs([]string{"=oops"}); err == nil {
		t.Fatal("parseBuildArgs with empty name should error")
	}
}

// fakeVolRemover records DeleteVolume calls and can return a scripted error per
// resource name, standing in for *client.Client in removeProjectVolumes tests.
type fakeVolRemover struct {
	calls []string
	errs  map[string]error
}

func (f *fakeVolRemover) DeleteVolume(_ context.Context, name string) error {
	f.calls = append(f.calls, name)
	return f.errs[name]
}

// TestRemoveProjectVolumes pins `down --volumes`: it removes project-scoped named
// volumes (external skipped, explicit name honored, resource names sorted by
// source), treats an unsupported backend as a soft skip, and returns the first
// hard error after attempting the rest.
func TestRemoveProjectVolumes(t *testing.T) {
	defs := map[string]compose.VolumeDef{
		"data":  {},
		"cache": {Name: "shared"},
		"ext":   {External: true},
	}

	t.Run("removes named, skips external", func(t *testing.T) {
		f := &fakeVolRemover{}
		var out bytes.Buffer
		if err := removeProjectVolumes(context.Background(), f, "proj", defs, psOutDriver(&out)); err != nil {
			t.Fatalf("removeProjectVolumes: %v", err)
		}
		// Sources sorted: cache -> "shared" (explicit name), data -> "proj_data";
		// ext is external and skipped.
		want := []string{"shared", "proj_data"}
		if !reflect.DeepEqual(f.calls, want) {
			t.Fatalf("calls = %v, want %v", f.calls, want)
		}
	})

	t.Run("unsupported backend is a soft skip", func(t *testing.T) {
		f := &fakeVolRemover{errs: map[string]error{"shared": client.ErrVolumeRemovalUnsupported}}
		var out bytes.Buffer
		if err := removeProjectVolumes(context.Background(), f, "proj", defs, psOutDriver(&out)); err != nil {
			t.Fatalf("unsupported should be a soft skip, got %v", err)
		}
		// Stops at the first volume (sorted: cache -> "shared").
		if !reflect.DeepEqual(f.calls, []string{"shared"}) {
			t.Fatalf("calls = %v, want [shared]", f.calls)
		}
	})

	t.Run("hard error returned after attempting the rest", func(t *testing.T) {
		boom := errors.New("boom")
		f := &fakeVolRemover{errs: map[string]error{"shared": boom}}
		var out bytes.Buffer
		err := removeProjectVolumes(context.Background(), f, "proj", defs, psOutDriver(&out))
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		// Did not stop early: both non-external volumes were attempted.
		if !reflect.DeepEqual(f.calls, []string{"shared", "proj_data"}) {
			t.Fatalf("calls = %v, want [shared proj_data]", f.calls)
		}
	})
}

func TestNeedsBackgroundAgent(t *testing.T) {
	relay := api.DeploySpec{Egress: &api.EgressSpec{Mode: "proxy"}} // proxy/transparent -> relay
	transp := api.DeploySpec{Egress: &api.EgressSpec{Mode: "transparent"}}
	envEg := api.DeploySpec{Egress: &api.EgressSpec{Mode: "env"}} // env mode is stateless
	plain := api.DeploySpec{}
	mount := api.DeploySpec{Mounts: []api.Mount{{}}}
	ported := api.DeploySpec{Ports: []api.PortMapping{{}}}

	cases := []struct {
		name  string
		specs []api.DeploySpec
		mode  string
		want  bool
	}{
		// The bug this guards: a relay-only service with no conduit and no ports must
		// still go to the background agent, or `up -d` deploys then tears it down.
		{"relay-only, no conduit", []api.DeploySpec{relay}, clientconduit.ModeNone, true},
		{"relay-only, port-forward", []api.DeploySpec{relay}, clientconduit.ModePortForward, true},
		{"transparent relay-only", []api.DeploySpec{transp}, clientconduit.ModeNone, true},
		{"env egress is stateless", []api.DeploySpec{envEg}, clientconduit.ModePortForward, false},
		{"plain service, no conduit", []api.DeploySpec{plain}, clientconduit.ModeNone, false},
		{"client-local mount", []api.DeploySpec{mount}, clientconduit.ModeNone, true},
		{"ports with port-forward", []api.DeploySpec{ported}, clientconduit.ModePortForward, true},
		{"ports without a forward mode", []api.DeploySpec{ported}, clientconduit.ModeNone, false},
		{"socks5 holds the proxy", []api.DeploySpec{plain}, clientconduit.ModeSocks5, true},
	}
	for _, tc := range cases {
		if got := needsBackgroundAgent(tc.specs, tc.mode); got != tc.want {
			t.Errorf("%s: needsBackgroundAgent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpResolveIngressAppliesEmulatedCAFlags(t *testing.T) {
	cn := &clientconn.Conn{Config: clientconn.Config{Conduit: &clientconfig.Conduit{
		Ingress: &clientconfig.Ingress{
			Mode:      "emulate",
			CAFile:    "profile-ca.pem",
			CAKeyFile: "profile-ca.key",
		},
	}}}
	rt := &runtime{applyIngress: cn.ApplyIngressConfig}
	cmd := &UpCmd{
		IngressConduit:      "emulate",
		IngressEmulateCA:    "cli-ca.pem",
		IngressEmulateCAKey: "cli-ca.key",
	}
	cfg := clientconduit.Config{Socks5Suffix: ".example.internal"}
	if err := cmd.resolveIngress(context.Background(), rt, &cfg); err != nil {
		t.Fatalf("resolveIngress: %v", err)
	}
	if cfg.Ingress == nil {
		t.Fatal("resolved ingress is nil")
	}
	if cfg.Ingress.Mode != "emulate" || cfg.Ingress.CAFile != "cli-ca.pem" || cfg.Ingress.CAKeyFile != "cli-ca.key" {
		t.Errorf("resolved ingress = %+v", cfg.Ingress)
	}
	if cfg.Ingress.SuffixDomain != "example.internal" {
		t.Errorf("suffix domain = %q, want example.internal", cfg.Ingress.SuffixDomain)
	}
}
