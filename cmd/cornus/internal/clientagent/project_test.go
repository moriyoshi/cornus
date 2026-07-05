package clientagent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/clientconduit"
	"cornus/pkg/deploywire"
	"cornus/pkg/socks5"
)

// echoDialer is a portfwd.Dialer that echoes each tunnel; a port-forward conduit
// over it lets a bound local listener round-trip bytes without a server.
type echoDialer struct{}

func (echoDialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	near, far := net.Pipe()
	go func() {
		defer far.Close()
		_, _ = io.Copy(far, far)
	}()
	return near, nil
}

func portForwardProject(t *testing.T) *Project {
	t.Helper()
	eg, err := clientconduit.Start(context.Background(), echoDialer{}, clientconduit.Config{Mode: clientconduit.ModePortForward})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eg.Close)
	return NewProject(nil, eg) // ForwardOnly services never touch the attacher
}

func forwardOnly() Service {
	return Service{
		Name:         "web",
		Spec:         api.DeploySpec{Name: "proj-web", Ports: []api.PortMapping{{Host: 0, Container: 80}}},
		ForwardPorts: true,
		ForwardOnly:  true,
	}
}

func TestProjectForwardOnlyService(t *testing.T) {
	p := portForwardProject(t)

	status, err := p.StartService(forwardOnly())
	if err != nil || status != StatusStarted {
		t.Fatalf("StartService = %q, %v; want %q", status, err, StatusStarted)
	}
	if got := p.Running(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("running = %v, want [web]", got)
	}
	fwds := p.Forwards()["web"]
	if len(fwds) != 1 || !strings.HasSuffix(fwds[0], "-> :80") {
		t.Fatalf("forwards = %v, want one entry ending in '-> :80'", fwds)
	}

	// The bound address round-trips through the echo dialer.
	addr := strings.Fields(fwds[0])[0]
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q err=%v", buf, err)
	}
	c.Close()

	// Idempotent restart request: identical spec is kept, not restarted.
	if status, err := p.StartService(forwardOnly()); err != nil || status != StatusUpToDate {
		t.Fatalf("second StartService = %q, %v; want %q", status, err, StatusUpToDate)
	}

	// down releases the listener.
	p.DownServices(nil)
	assertListenerGone(t, addr)
}

func TestDimensionFingerprints(t *testing.T) {
	base := func() Service {
		return Service{
			Name: "web",
			Spec: api.DeploySpec{
				Name:   "proj-web",
				Image:  "img:1",
				Env:    map[string]string{"A": "1", "B": "2"},
				Ports:  []api.PortMapping{{Host: 8080, Container: 80}},
				Mounts: []api.Mount{{Source: "/src", Target: "/app"}},
			},
			ForwardPorts: true,
		}
	}
	mfp := func(s Service) string { fp, err := mountFingerprint(s); mustNoErr(t, err); return fp }
	efp := func(s Service) string { fp, err := exposureFingerprint(s); mustNoErr(t, err); return fp }

	// Identical services hash equal in both dimensions.
	if mfp(base()) != mfp(base()) || efp(base()) != efp(base()) {
		t.Fatal("identical services hash differently")
	}

	// The mount dimension covers the whole DeploySpec (any spec change recreates the
	// workload). It deliberately ignores the session-shape flags, so toggling
	// port-forwarding leaves a healthy 9P mount alone.
	mountChanges := map[string]func(*Service){
		"image":   func(s *Service) { s.Spec.Image = "img:2" },
		"command": func(s *Service) { s.Spec.Command = []string{"sh"} },
		"env":     func(s *Service) { s.Spec.Env["A"] = "changed" },
		"ports":   func(s *Service) { s.Spec.Ports[0].Container = 81 },
		"mounts":  func(s *Service) { s.Spec.Mounts[0].Source = "/other" },
	}
	for what, mutate := range mountChanges {
		s := base()
		mutate(&s)
		if mfp(s) == mfp(base()) {
			t.Errorf("changing %s did not change the mount fingerprint", what)
		}
	}

	// Rotating a managed native-ingress certificate updates its Kubernetes Secret
	// without recreating the workload. Changing which Secret/hosts the Ingress uses
	// is routing shape and therefore still changes the fingerprint.
	withTLS := base()
	withTLS.Spec.Ingress = &api.IngressSpec{TLS: &api.IngressTLS{ManagedCertificates: []api.ManagedIngressCertificate{{
		Hosts:          []string{"app.example.test"},
		SecretName:     "proj-web-tls-a",
		CertificatePEM: []byte("certificate-a"),
		PrivateKeyPEM:  []byte("private-key-a"),
	}}}}
	rotated := withTLS
	rotated.Spec = mountFingerprintSpec(withTLS.Spec)
	rotated.Spec.Ingress.TLS.ManagedCertificates[0].CertificatePEM = []byte("certificate-b")
	rotated.Spec.Ingress.TLS.ManagedCertificates[0].PrivateKeyPEM = []byte("private-key-b")
	if mfp(rotated) != mfp(withTLS) {
		t.Fatal("rotating managed ingress certificate bytes changed the mount fingerprint")
	}
	remapped := withTLS
	remapped.Spec = mountFingerprintSpec(withTLS.Spec)
	remapped.Spec.Ingress.TLS.ManagedCertificates[0].SecretName = "proj-web-tls-b"
	if mfp(remapped) == mfp(withTLS) {
		t.Fatal("changing managed ingress Secret mapping did not change the mount fingerprint")
	}
	rehosted := withTLS
	rehosted.Spec = mountFingerprintSpec(withTLS.Spec)
	rehosted.Spec.Ingress.TLS.ManagedCertificates[0].Hosts = []string{"other.example.test"}
	if mfp(rehosted) == mfp(withTLS) {
		t.Fatal("changing managed ingress host mapping did not change the mount fingerprint")
	}

	noForward := base()
	noForward.ForwardPorts = false
	if mfp(noForward) != mfp(base()) {
		t.Error("toggling ForwardPorts changed the mount fingerprint (should keep the mount)")
	}

	// The exposure dimension covers the deployment name, alias, and effective ports.
	exposureChanges := map[string]func(*Service){
		"ports":        func(s *Service) { s.Spec.Ports[0].Container = 81 },
		"forwardPorts": func(s *Service) { s.ForwardPorts = false }, // drops the effective ports
		"deployment":   func(s *Service) { s.Spec.Name = "proj-web2" },
		"alias":        func(s *Service) { s.Name = "web2" },
	}
	for what, mutate := range exposureChanges {
		s := base()
		mutate(&s)
		if efp(s) == efp(base()) {
			t.Errorf("changing %s did not change the exposure fingerprint", what)
		}
	}
	// An image/env/mount change is invisible to the exposure dimension (no needless
	// listener re-bind / alias re-register on a pure workload change).
	for what, mutate := range map[string]func(*Service){
		"image":  func(s *Service) { s.Spec.Image = "img:2" },
		"env":    func(s *Service) { s.Spec.Env["A"] = "changed" },
		"mounts": func(s *Service) { s.Spec.Mounts[0].Source = "/other" },
	} {
		s := base()
		mutate(&s)
		if efp(s) != efp(base()) {
			t.Errorf("changing %s changed the exposure fingerprint (should not)", what)
		}
	}
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectRecreateOnSpecChange(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	p := portForwardProject(t)

	status, _ := p.StartService(forwardOnly())
	if status != StatusStarted {
		t.Fatalf("first start = %q, want %q", status, StatusStarted)
	}
	oldAddr := strings.Fields(p.Forwards()["web"][0])[0]

	// Same spec again: kept, same live listener.
	if status, _ := p.StartService(forwardOnly()); status != StatusUpToDate {
		t.Fatalf("re-start(same) = %q, want %q", status, StatusUpToDate)
	}
	if got := strings.Fields(p.Forwards()["web"][0])[0]; got != oldAddr {
		t.Fatalf("re-start(same) rebound listener: %s -> %s", oldAddr, got)
	}
	if !strings.Contains(logBuf.String(), "service is up-to-date") {
		t.Fatalf("missing up-to-date log line; log:\n%s", logBuf.String())
	}

	// Changed spec (container port 80 -> 81): recreated with the new spec.
	changed := forwardOnly()
	changed.Spec.Ports = []api.PortMapping{{Host: 0, Container: 81}}
	if status, _ := p.StartService(changed); status != StatusRecreated {
		t.Fatalf("re-start(changed) = %q, want %q", status, StatusRecreated)
	}
	fwds := p.Forwards()["web"]
	if len(fwds) != 1 || !strings.HasSuffix(fwds[0], "-> :81") {
		t.Fatalf("forwards after recreate = %v, want one entry ending in '-> :81'", fwds)
	}
	if !strings.Contains(logBuf.String(), "recreating service: configuration changed") {
		t.Fatalf("missing recreate log line; log:\n%s", logBuf.String())
	}
	assertListenerGone(t, oldAddr) // old listener released by the recreate teardown
}

// countingAttacher records how many deploy-attach sessions were opened, holding
// each open (Ready at once) until its context ends — so a test can tell a mount
// recreate (a new attach) from a mount that was reconciled in place.
type countingAttacher struct {
	mu     sync.Mutex
	starts int
}

func (a *countingAttacher) DeployAttach(ctx context.Context, _ api.DeploySpec, events func(deploywire.Event)) error {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	events(deploywire.Event{Ready: true})
	<-ctx.Done()
	return ctx.Err()
}

func (a *countingAttacher) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts
}

func mountedService() Service {
	return Service{
		Name: "web",
		Spec: api.DeploySpec{
			Name:   "proj-web",
			Image:  "img:1",
			Mounts: []api.Mount{{Source: "/src", Target: "/app"}},
			Ports:  []api.PortMapping{{Host: 8080, Container: 80}},
		},
		ForwardPorts: true,
	}
}

// A pure workload change (here the image) on a mounted service recreates the 9P
// mount, and the exposure re-registers under the new mount context (its old
// context died with the old mount).
func TestMountedWorkloadChangeRecreatesMountAndReExposes(t *testing.T) {
	conduit := &recordingConduit{}
	att := &countingAttacher{}
	p := NewProject(att, conduit)
	t.Cleanup(p.Close)

	if status, err := p.StartService(mountedService()); err != nil || status != StatusStarted {
		t.Fatalf("first up = %q, %v; want %q", status, err, StatusStarted)
	}
	changed := mountedService()
	changed.Spec.Image = "img:2"
	if status, err := p.StartService(changed); err != nil || status != StatusRecreated {
		t.Fatalf("re-up(changed image) = %q, %v; want %q", status, err, StatusRecreated)
	}
	if got := att.count(); got != 2 {
		t.Fatalf("attach count = %d, want 2 (the mount must recreate on a workload change)", got)
	}
	if got := len(conduit.addCalls()); got != 2 {
		t.Fatalf("conduit.Add count = %d, want 2 (exposure re-registers under the new mount)", got)
	}
}

// Toggling port-forwarding is an exposure-only change: the healthy 9P mount stays
// (no new attach), only the conduit registration is redone. This is the concrete
// per-dimension win — a session-shape change no longer tears the mount down.
func TestForwardPortsToggleKeepsMount(t *testing.T) {
	conduit := &recordingConduit{}
	att := &countingAttacher{}
	p := NewProject(att, conduit)
	t.Cleanup(p.Close)

	if status, err := p.StartService(mountedService()); err != nil || status != StatusStarted {
		t.Fatalf("first up = %q, %v; want %q", status, err, StatusStarted)
	}
	noForward := mountedService()
	noForward.ForwardPorts = false
	if status, err := p.StartService(noForward); err != nil || status != StatusRecreated {
		t.Fatalf("re-up(--no-forward-ports) = %q, %v; want %q", status, err, StatusRecreated)
	}
	if got := att.count(); got != 1 {
		t.Fatalf("attach count = %d, want 1 (the mount must survive a port-forward toggle)", got)
	}
	adds := conduit.addCalls()
	if len(adds) != 2 {
		t.Fatalf("conduit.Add count = %d, want 2 (initial + re-register with no ports)", len(adds))
	}
	if len(adds[0].ports) == 0 || len(adds[1].ports) != 0 {
		t.Fatalf("re-register should drop the ports: adds=%+v", adds)
	}
}

// Reconcile is declarative over the whole desired set: removing one service of a
// two-service project leaves the other running.
func TestReconcileRemovesOnlyTheDroppedService(t *testing.T) {
	att := &countingAttacher{}
	p := NewProject(att, &recordingConduit{})
	t.Cleanup(p.Close)

	web := mountedService()
	db := mountedService()
	db.Name = "db"
	db.Spec.Name = "proj-db"
	if _, err := p.Apply(context.Background(), []Service{web, db}); err != nil {
		t.Fatalf("apply both: %v", err)
	}
	if got := len(p.Running()); got != 2 {
		t.Fatalf("running = %d, want 2", got)
	}
	p.Remove([]string{"db"})
	running := p.Running()
	if len(running) != 1 || running[0] != "web" {
		t.Fatalf("running after removing db = %v, want [web]", running)
	}
}

// recordingConduit is a clientconduit.Conduit that records each Add call, so a
// test can assert what a service registered (alias, ports) without a real proxy.
type recordingConduit struct {
	mu      sync.Mutex
	adds    []addCall
	ingress []ingressCall
}

type addCall struct {
	name    string
	ports   []api.PortMapping
	aliases []string
}

type ingressCall struct {
	name  string
	spec  *api.IngressSpec
	ports []api.PortMapping
}

func (c *recordingConduit) Banner() []string { return nil }
func (c *recordingConduit) CAInfo() []string { return nil }

func (c *recordingConduit) Add(_ context.Context, name string, ports []api.PortMapping, aliases ...string) ([]clientconduit.Forward, error) {
	c.mu.Lock()
	c.adds = append(c.adds, addCall{name: name, ports: ports, aliases: append([]string(nil), aliases...)})
	c.mu.Unlock()
	return nil, nil
}

// AddLocal is unused by the project sessions under test (only the web surface
// publishes names), so it records nothing and reports "not published".
func (c *recordingConduit) AddLocal(context.Context, string, int, socks5.LocalDialer) (bool, error) {
	return false, nil
}

// AddIngress records the ingress registrations a service requests, so tests can
// assert them without a real proxy.
func (c *recordingConduit) AddIngress(_ context.Context, name string, in *api.IngressSpec, ports []api.PortMapping) ([]string, error) {
	c.mu.Lock()
	c.ingress = append(c.ingress, ingressCall{name: name, spec: in, ports: ports})
	c.mu.Unlock()
	if in == nil || (!in.Enabled && len(in.Hosts) == 0) {
		return nil, nil
	}
	return in.Hosts, nil
}

func (c *recordingConduit) Close() {}

// addCalls returns a snapshot of the recorded Add calls.
func (c *recordingConduit) addCalls() []addCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]addCall(nil), c.adds...)
}

// readyAttacher is a fake Attacher whose DeployAttach reports Ready at once and
// then holds the session open until its context is cancelled (mirroring a live
// deploy-attach), so a mounted service reaches the post-ready registration path.
type readyAttacher struct{}

func (readyAttacher) DeployAttach(ctx context.Context, _ api.DeploySpec, events func(deploywire.Event)) error {
	events(deploywire.Event{Ready: true})
	<-ctx.Done()
	return ctx.Err()
}

// A mounted service with NO published ports must still register its short-name
// alias with the conduit (SOCKS5 mode reaches it by name). Regression for the gap
// where the mounted path gated the whole Add on ForwardPorts.
func TestProjectMountedServiceRegistersAlias(t *testing.T) {
	conduit := &recordingConduit{}
	p := NewProject(readyAttacher{}, conduit)

	svc := Service{
		Name: "web", // compose service name -> the alias
		Spec: api.DeploySpec{
			Name:   "proj-web",                                    // deployment name
			Mounts: []api.Mount{{Source: "/src", Target: "/app"}}, // -> DeployAttach path
			// no Ports -> ForwardPorts is false
		},
	}
	status, err := p.StartService(svc)
	if err != nil || status != StatusStarted {
		t.Fatalf("StartService = %q, %v; want %q", status, err, StatusStarted)
	}

	conduit.mu.Lock()
	adds := conduit.adds
	conduit.mu.Unlock()
	if len(adds) != 1 {
		t.Fatalf("conduit.Add called %d times, want 1 (the alias must register even with no ports)", len(adds))
	}
	got := adds[0]
	if got.name != "proj-web" || len(got.ports) != 0 || len(got.aliases) != 1 || got.aliases[0] != "web" {
		t.Fatalf("Add = {name:%q ports:%v aliases:%v}, want {proj-web, no ports, [web]}", got.name, got.ports, got.aliases)
	}

	p.DownServices(nil) // cancels the attach ctx -> clean teardown
}

// orderAttacher records the deployment names in the order deploy-attach sessions
// were opened, holding each open until its context ends.
type orderAttacher struct {
	mu    sync.Mutex
	order []string
}

func (a *orderAttacher) DeployAttach(ctx context.Context, spec api.DeploySpec, events func(deploywire.Event)) error {
	a.mu.Lock()
	a.order = append(a.order, spec.Name)
	a.mu.Unlock()
	events(deploywire.Event{Ready: true})
	<-ctx.Done()
	return ctx.Err()
}

// Apply reconciles services in request order, each mount coming up (ready) before
// the next starts — the dependency ordering the CLI encodes in the request.
func TestApplyReconcilesInRequestOrder(t *testing.T) {
	att := &orderAttacher{}
	p := NewProject(att, &recordingConduit{})
	t.Cleanup(p.Close)

	var svcs []Service
	want := []string{}
	for _, n := range []string{"db", "cache", "web"} {
		s := mountedService()
		s.Name = n
		s.Spec.Name = "proj-" + n
		svcs = append(svcs, s)
		want = append(want, "proj-"+n)
	}
	if _, err := p.Apply(context.Background(), svcs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	att.mu.Lock()
	got := append([]string(nil), att.order...)
	att.mu.Unlock()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("attach order = %v, want %v", got, want)
	}
}

// selfExitAttacher reports Ready at once and then holds the session until either
// its context is cancelled or exit is closed (the workload dying on its own,
// returning nil like a clean self-exit).
type selfExitAttacher struct {
	exit chan struct{}
}

func (a *selfExitAttacher) DeployAttach(ctx context.Context, _ api.DeploySpec, events func(deploywire.Event)) error {
	events(deploywire.Event{Ready: true})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.exit:
		return nil
	}
}

// A mounted service whose deploy-attach dies on its own must have its port exposure
// withdrawn: the exposure's context is a child of the mount's context, which the
// shared attachsession cancels when the attach goroutine exits. This is the one
// behavior that moved into pkg/attachsession (the `defer cancel()` on self-exit),
// exercised here end to end through a real port-forward conduit — the mount dies,
// the bound listener must go away without any explicit Remove.
func TestMountSelfExitWithdrawsExposure(t *testing.T) {
	eg, err := clientconduit.Start(context.Background(), echoDialer{}, clientconduit.Config{Mode: clientconduit.ModePortForward})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eg.Close)
	att := &selfExitAttacher{exit: make(chan struct{})}
	p := NewProject(att, eg)
	t.Cleanup(p.Close)

	// Mounted (so it takes the deploy-attach path) with an ephemeral published port.
	svc := Service{
		Name: "web",
		Spec: api.DeploySpec{
			Name:   "proj-web",
			Image:  "img:1",
			Mounts: []api.Mount{{Source: "/src", Target: "/app"}},
			Ports:  []api.PortMapping{{Host: 0, Container: 80}},
		},
		ForwardPorts: true,
	}
	if status, err := p.StartService(svc); err != nil || status != StatusStarted {
		t.Fatalf("StartService = %q, %v; want %q", status, err, StatusStarted)
	}
	fwds := p.Forwards()["web"]
	if len(fwds) != 1 {
		t.Fatalf("forwards = %v, want one bound listener", fwds)
	}
	addr := strings.Fields(fwds[0])[0]
	if c, err := net.Dial("tcp", addr); err != nil {
		t.Fatalf("listener not bound while the mount is held: %v", err)
	} else {
		c.Close()
	}

	// The workload exits on its own — no Remove/Down. The mount context cancels,
	// cascading to the exposure child context and closing the port-forward listener.
	close(att.exit)
	assertListenerGone(t, addr)
}

func assertListenerGone(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return // listener gone
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// diagnosticAttacher emits one transient deploy diagnostic (Event.Done unset — a
// workload still starting that later comes up fine) before readying and holding
// the session open, so a test can assert how the mount controller routes a
// non-terminal diagnostic.
type diagnosticAttacher struct{}

func (diagnosticAttacher) DeployAttach(ctx context.Context, _ api.DeploySpec, events func(deploywire.Event)) error {
	events(deploywire.Event{Err: "Back-off restarting failed container"}) // transient: Done unset
	events(deploywire.Event{Ready: true})
	<-ctx.Done()
	return ctx.Err()
}

// A transient deploy diagnostic streamed while a mounted workload is still coming
// up must reach the interactive reporter (as a non-terminal notice), NOT leak
// through slog — the regression this fixes was every diagnostic, transient ones
// included, being logged via slog as a "deploy error" even when the deploy came
// up fine.
func TestTransientDeployDiagnosticGoesToReporterNotSlog(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	var mu sync.Mutex
	var notices []DeployNotice
	p := NewProject(diagnosticAttacher{}, &recordingConduit{}, WithDeployReporter(func(n DeployNotice) {
		mu.Lock()
		notices = append(notices, n)
		mu.Unlock()
	}))
	t.Cleanup(p.Close)

	if status, err := p.StartService(mountedService()); err != nil || status != StatusStarted {
		t.Fatalf("StartService = %q, %v; want %q", status, err, StatusStarted)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 {
		t.Fatalf("got %d reporter notices, want 1: %+v", len(notices), notices)
	}
	if notices[0].Terminal {
		t.Errorf("transient diagnostic reported as terminal: %+v", notices[0])
	}
	if notices[0].Service != "web" || notices[0].Message == "" {
		t.Errorf("unexpected notice: %+v", notices[0])
	}
	if got := logBuf.String(); strings.Contains(got, "deploy error") || strings.Contains(got, "deploy warning") {
		t.Errorf("diagnostic leaked to slog with a reporter set; log:\n%s", got)
	}
}

// With no reporter (the background agent, whose output IS its slog stream), a
// transient diagnostic still falls back to slog — but as a WARNING, not the
// alarming "deploy error" the old code emitted for a workload that was merely
// still starting.
func TestTransientDeployDiagnosticFallsBackToSlogWarn(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	p := NewProject(diagnosticAttacher{}, &recordingConduit{})
	t.Cleanup(p.Close)

	if status, err := p.StartService(mountedService()); err != nil || status != StatusStarted {
		t.Fatalf("StartService = %q, %v; want %q", status, err, StatusStarted)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "deploy warning") {
		t.Errorf("transient diagnostic did not fall back to a slog warning; log:\n%s", logs)
	}
	if strings.Contains(logs, "deploy error") {
		t.Errorf("transient diagnostic logged as an error (should be a warning); log:\n%s", logs)
	}
}
