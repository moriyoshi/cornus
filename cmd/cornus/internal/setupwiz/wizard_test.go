package setupwiz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/clientconfig"
	"cornus/pkg/sshclient"
	"cornus/pkg/svcforward"
)

// scriptUI is a UI that replays queued answers and records what it was asked. An
// exhausted queue returns ErrAborted, so a truncated script models an abort.
type scriptUI struct {
	selects  []int
	inputs   []string
	confirms []bool
	notes    []string
	log      []string
	// offered records the option list of every Select, so a test can assert what
	// the user was shown and not just what they picked.
	offered [][]Option
}

func (s *scriptUI) Select(title, help string, opts []Option, def int) (int, error) {
	s.log = append(s.log, "select:"+title)
	s.offered = append(s.offered, opts)
	if len(s.selects) == 0 {
		return 0, ErrAborted
	}
	v := s.selects[0]
	s.selects = s.selects[1:]
	return v, nil
}

func (s *scriptUI) Input(q Question) (string, error) {
	s.log = append(s.log, "input:"+q.Title)
	if len(s.inputs) == 0 {
		return "", ErrAborted
	}
	v := s.inputs[0]
	s.inputs = s.inputs[1:]
	if v == "" {
		v = q.Default
	}
	if q.Validate != nil {
		if err := q.Validate(v); err != nil {
			return "", err
		}
	}
	return v, nil
}

func (s *scriptUI) Confirm(question string, def bool) (bool, error) {
	s.log = append(s.log, "confirm:"+question)
	if len(s.confirms) == 0 {
		return false, ErrAborted
	}
	v := s.confirms[0]
	s.confirms = s.confirms[1:]
	return v, nil
}

// Note records the FORMATTED line, not the format string. Recording the format
// was a false negative waiting to happen: once the wizard began pre-rendering a
// styled line and passing it as Note("%s", line), every note collapsed to "%s"
// and assertions about what the user reads still passed.
func (s *scriptUI) Note(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	s.notes = append(s.notes, line)
	// Also into the ordered log: notes and prompts interleave, and where a note
	// lands relative to a question is itself a requirement.
	s.log = append(s.log, "note:"+line)
}

// newTestWizard builds a Wizard over a buffer-backed plain driver and the given
// scriptUI, with no-op-ish seams the individual tests override as needed.
func newTestWizard(t *testing.T, ui UI, path string) (*Wizard, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: &buf, Stderr: &buf, Stdin: strings.NewReader(""), Output: "plain"})
	w := NewWizard(d, ui, path)
	// Default seams to hermetic stubs; tests override where they matter.
	w.Discover = func(context.Context, svcforward.DiscoverOptions) (svcforward.DiscoverResult, error) {
		return svcforward.DiscoverResult{}, os.ErrNotExist
	}
	w.Verify = func(context.Context, string, string) VerifyResult {
		return VerifyResult{OK: true, Detail: "ok"}
	}
	w.Ingress = func(context.Context, *Answers) IngressFacts {
		return IngressFacts{}
	}
	// Never read the developer's real ~/.ssh/config: the SSH destination step
	// picks its rendering (picker vs free text) from this list.
	w.SSHHosts = func() []sshclient.ConfigHost { return nil }
	return w, &buf
}

func TestWizardLocalScenario(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects:  []int{0, 0},         // Local; runtime: Docker
		inputs:   []string{"", ""},    // server (default), name (default "local")
		confirms: []bool{false, true}, // not set up, make current
	}
	verifyCalled := false
	w, _ := newTestWizard(t, ui, path)
	w.Verify = func(context.Context, string, string) VerifyResult {
		verifyCalled = true
		return VerifyResult{OK: true, Detail: "ok"}
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	f, err := clientconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := f.Contexts["local"]
	if c == nil || c.Server != "http://127.0.0.1:5000" {
		t.Fatalf("local context: %+v", c)
	}
	if f.CurrentContext != "local" {
		t.Fatalf("current-context = %q, want local", f.CurrentContext)
	}
	if verifyCalled {
		t.Error("verify must not run against a server the user says is not set up")
	}

	// Raw-YAML golden pins the field names/shape.
	data, _ := os.ReadFile(path)
	want := "contexts:\n  local:\n    server: http://127.0.0.1:5000\ncurrent-context: local\n"
	if string(data) != want {
		t.Errorf("saved yaml:\n got %q\nwant %q", data, want)
	}
}

func TestWizardLocalRunningOffersVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects:  []int{0},
		inputs:   []string{"", ""},
		confirms: []bool{true, true, true}, // already set up, make current, test now
	}
	verifyCalled := false
	w, _ := newTestWizard(t, ui, path)
	w.Verify = func(context.Context, string, string) VerifyResult {
		verifyCalled = true
		return VerifyResult{OK: true, Detail: "connected"}
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !verifyCalled {
		t.Error("verify should run when the user says the server is up and accepts the test")
	}
	// A server that already exists gets no runtime question and no setup guide.
	if containsString(ui.log, "select:Which container runtime will this server drive?") {
		t.Errorf("the runtime question was asked despite an existing server: %v", ui.log)
	}
	if strings.Contains(strings.Join(ui.notes, "\n"), "No server yet") {
		t.Errorf("the setup guide was shown despite an existing server: %v", ui.notes)
	}
}

// The whole point of the gate: a user with no server gets the guide up front,
// tailored to the runtime they picked, and the wizard stops attempting the work
// that cannot succeed against a server that is not listening.
func TestWizardServerNotReadyGuidesAndSkipsLiveWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects:  []int{0, 2, 2},      // Local; runtime: Bare; skip the unit offer
		inputs:   []string{"", ""},    // server (default), name (default "local")
		confirms: []bool{false, true}, // not set up, make current
	}
	w, buf := newTestWizard(t, ui, path)
	w.Verify = func(context.Context, string, string) VerifyResult {
		t.Fatal("verify must not run against a server that is not set up")
		return VerifyResult{}
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The guide names the runtime's real prerequisites, not a generic checklist,
	// and it is the ONLY place they appear.
	joined := strings.Join(ui.notes, "\n")
	for _, want := range []string{
		"No server yet", "Local server", "docs: " + setupGuideURL + "#local-bare",
		"CORNUS_DEPLOY_BACKEND=bare", "OCI runtime", "/opt/cni/bin", "daemon preflight",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("setup guide missing %q: %v", want, ui.notes)
		}
	}
	// The closing checklist must NOT repeat it: by then the answers those commands
	// are built from are committed to disk, so setup instructions there would
	// explain how to build a server the profile already assumes.
	out := buf.String()
	for _, forbidden := range []string{"Install the cornus binary", "OCI runtime", "daemon preflight"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("closing checklist repeats the setup step %q:\n%s", forbidden, out)
		}
	}
	// The profile is written regardless — a missing server is not a reason to
	// discard the answers the user just gave.
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["local"]; c == nil || c.Server != "http://127.0.0.1:5000" {
		t.Fatalf("profile should still be saved: %+v", c)
	}
}

// The runtime question shapes guidance only; nothing about it may reach the
// saved profile (the backend is the SERVER's business, not the client's).
func TestWizardLocalBackendNeverReachesTheProfile(t *testing.T) {
	for idx := range localBackends {
		path := filepath.Join(t.TempDir(), "config.yaml")
		ui := &scriptUI{
			// The trailing Skip is consumed only by bare, which is the one
			// backend offered a unit; the others never reach it.
			selects:  []int{0, idx, 2},
			inputs:   []string{"", ""},
			confirms: []bool{false, true},
		}
		w, _ := newTestWizard(t, ui, path)
		if err := w.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		data, _ := os.ReadFile(path)
		want := "contexts:\n  local:\n    server: http://127.0.0.1:5000\ncurrent-context: local\n"
		if string(data) != want {
			t.Errorf("runtime %d leaked into the profile:\n got %q\nwant %q", idx, data, want)
		}
	}
}

func TestWizardOverwriteGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Seed an existing "local" context.
	seed := &clientconfig.File{Contexts: map[string]*clientconfig.Context{"local": {Server: "http://old"}}}
	if err := clientconfig.Save(path, seed); err != nil {
		t.Fatal(err)
	}
	ui := &scriptUI{
		selects:  []int{0, 0},                     // Local; runtime: Docker
		inputs:   []string{"", "local", "local2"}, // server; name "local" (exists); then "local2"
		confirms: []bool{false, false, true},      // not set up; decline overwrite; make current
	}
	w, _ := newTestWizard(t, ui, path)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if f.Contexts["local"].Server != "http://old" {
		t.Error("declined overwrite must leave the original context untouched")
	}
	if f.Contexts["local2"] == nil {
		t.Error("expected the renamed context local2 to be saved")
	}
}

func TestWizardExistingCurrentContextNotStolen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := &clientconfig.File{
		CurrentContext: "old",
		Contexts:       map[string]*clientconfig.Context{"old": {Server: "http://old"}},
	}
	if err := clientconfig.Save(path, seed); err != nil {
		t.Fatal(err)
	}
	ui := &scriptUI{
		selects:  []int{0, 0}, // Local; runtime: Docker
		inputs:   []string{"", "fresh"},
		confirms: []bool{false, false}, // not set up; do NOT switch current
	}
	w, _ := newTestWizard(t, ui, path)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if f.CurrentContext != "old" {
		t.Errorf("current-context = %q, want old (declined switch)", f.CurrentContext)
	}
	if f.Contexts["fresh"] == nil {
		t.Error("fresh context should still be saved")
	}
}

func TestWizardAbortLeavesNoConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Truncated: scenario chosen, but no answer for the first input -> ErrAborted
	// before materialization.
	ui := &scriptUI{selects: []int{0}}
	w, _ := newTestWizard(t, ui, path)
	err := w.Run(context.Background())
	if err == nil || err != ErrAborted {
		t.Fatalf("Run err = %v, want ErrAborted", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Errorf("aborted setup must not create the config file (stat err=%v)", serr)
	}
}

func TestWizardKubePortForwardAutoDetect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects:  []int{5, 0, 0, 2, 2},      // kube-pf; auth none; ingress off; tunnel; artifact skip
		inputs:   []string{"", "", "", ""},  // kube-context; namespace(default); registry-host; name(default)
		confirms: []bool{true, true, false}, // already set up; make current; skip verify
	}
	discoverCalled := false
	w, _ := newTestWizard(t, ui, path)
	w.Discover = func(context.Context, svcforward.DiscoverOptions) (svcforward.DiscoverResult, error) {
		discoverCalled = true
		return svcforward.DiscoverResult{Service: "cornus", RemotePort: 5000, Managed: "helm"}, nil
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !discoverCalled {
		t.Error("Discover seam should be called for the kube port-forward scenario")
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["cluster"]
	if c == nil || c.PortForward == nil || c.PortForward.Service != "cornus" || c.PortForward.Namespace != "default" {
		t.Fatalf("kube port-forward context: %+v", c)
	}
}

func TestWizardURLSSHKeyEnrollsAfterSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	identity := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(identity, []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui := &scriptUI{
		selects:  []int{7, 0}, // URL; SSH key auth
		inputs:   []string{"http://cornus.example", identity, "", "laptop", "one-time-code"},
		confirms: []bool{true, true, true, false}, // already set up; current; enroll; skip verify
	}
	w, _ := newTestWizard(t, ui, path)
	enrolled := false
	w.Enroll = func(_ context.Context, configPath, contextName, code string) error {
		enrolled = true
		if configPath != path || contextName != "laptop" || code != "one-time-code" {
			t.Fatalf("Enroll args = %q %q %q", configPath, contextName, code)
		}
		file, err := clientconfig.Load(path)
		if err != nil || file.Contexts["laptop"] == nil {
			t.Fatalf("profile was not saved before enrollment: file=%+v err=%v", file, err)
		}
		return nil
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !enrolled {
		t.Fatal("post-save enrollment was not called")
	}
	file, _ := clientconfig.Load(path)
	keyAuth := file.Contexts["laptop"].KeyAuth
	if keyAuth == nil || keyAuth.IdentityFile != identity || keyAuth.Name != "laptop" {
		t.Fatalf("saved key-auth = %+v", keyAuth)
	}
	for _, want := range []string{"select:Authentication", "input:Enrollment code"} {
		if !containsString(ui.log, want) {
			t.Errorf("wizard log missing %q: %v", want, ui.log)
		}
	}
}

func TestWizardURLStaticTokenRemainsAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects:  []int{7, 1}, // URL; static token
		inputs:   []string{"http://cornus.example", "fixed-token", "", "legacy"},
		confirms: []bool{true, true, false}, // already set up; current; skip verify
	}
	w, _ := newTestWizard(t, ui, path)
	w.Enroll = func(context.Context, string, string, string) error {
		t.Fatal("static-token branch attempted SSH enrollment")
		return nil
	}
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	file, _ := clientconfig.Load(path)
	profile := file.Contexts["legacy"]
	if profile.Token != "fixed-token" || profile.KeyAuth != nil {
		t.Fatalf("static-token profile = %+v", profile)
	}
}

// The two new scenarios reuse the SSH question list verbatim — the tunnel is
// backend-agnostic — so what has to be proven is that they produce the SAME
// profile as ssh-docker while carrying their own backend into the artifact.
func TestWizardBareAndIncusSSHScenarios(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pick    int
		backend string
	}{
		{"ssh-bare", 3, backendBare},
		{"ssh-incus", 4, backendIncus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			ui := &scriptUI{
				selects: []int{tc.pick, 2, 1}, // scenario; auth None; artifact: print to stdout
				// ssh host, user, identity, remote addr, registry host, name
				inputs:   []string{"remote-devbox", "ops", "", "", "", ""},
				confirms: []bool{true, false, true, false}, // set up; no remote TLS; make current; skip verify
			}
			w, buf := newTestWizard(t, ui, path)
			if err := w.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			f, _ := clientconfig.Load(path)
			c := f.Contexts["remote-devbox"]
			if c == nil || c.SSHTunnel == nil {
				t.Fatalf("%s context: %+v", tc.name, c)
			}
			if c.SSHTunnel.Addr != "remote-devbox" || c.SSHTunnel.User != "ops" {
				t.Fatalf("ssh tunnel = %+v", c.SSHTunnel)
			}
			// The backend is the SERVER's business: it must shape the artifact and
			// the guidance, and reach the client profile nowhere.
			if strings.Contains(string(mustRead(t, path)), tc.backend) {
				t.Errorf("backend %q leaked into the saved profile:\n%s", tc.backend, mustRead(t, path))
			}
			out := buf.String()
			if !strings.Contains(out, "CORNUS_DEPLOY_BACKEND="+tc.backend) {
				t.Errorf("printed systemd unit does not select %q:\n%s", tc.backend, out)
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// progUI replays a single ordered list of replies across all prompt kinds, so a
// test can interleave a Back (err: ErrBack) at any point to exercise navigation.
type progReply struct {
	i   int
	s   string
	b   bool
	err error
}

type progUI struct {
	replies []progReply
	pos     int
}

func (p *progUI) next() progReply {
	if p.pos >= len(p.replies) {
		return progReply{err: ErrAborted}
	}
	r := p.replies[p.pos]
	p.pos++
	return r
}

func (p *progUI) Select(title, help string, opts []Option, def int) (int, error) {
	r := p.next()
	return r.i, r.err
}

func (p *progUI) Input(q Question) (string, error) {
	r := p.next()
	if r.err != nil {
		return "", r.err
	}
	v := r.s
	if v == "" {
		v = q.Default
	}
	if q.Validate != nil {
		if err := q.Validate(v); err != nil {
			return "", err
		}
	}
	return v, nil
}

func (p *progUI) Confirm(question string, def bool) (bool, error) {
	r := p.next()
	if r.err != nil {
		return false, r.err
	}
	return r.b, nil
}

func (p *progUI) Note(format string, a ...any) {}

// TestWizardBackReasksPreviousStep: Back at the name prompt returns to the prior
// (server URL) question; the flow then completes normally.
func TestWizardBackReasksPreviousStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &progUI{replies: []progReply{
		{i: 0},            // scenario: Local
		{b: false},        // server set up? no
		{i: 2},            // runtime: Bare
		{s: "http://a:1"}, // server URL
		{err: ErrBack},    // name -> Back to the server URL
		{s: "http://a:1"}, // server URL (re-asked)
		{s: "myctx"},      // name
		{b: true},         // make current
		{i: 2},            // skip the systemd unit offer (Bare is daemonless)
	}}
	w, _ := newTestWizard(t, ui, path)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, err := clientconfig.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := f.Contexts["myctx"]
	if c == nil || c.Server != "http://a:1" {
		t.Fatalf("context after back-navigation: %+v", c)
	}
	if f.CurrentContext != "myctx" {
		t.Errorf("current = %q, want myctx", f.CurrentContext)
	}
}

// TestWizardBackOutToScenarioPicker: Back at the first question of a scenario
// returns to the scenario picker, where a fresh (re)selection proceeds cleanly.
func TestWizardBackOutToScenarioPicker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &progUI{replies: []progReply{
		{i: 0},            // scenario: Local
		{err: ErrBack},    // server set up? -> Back out to the scenario picker
		{i: 0},            // scenario: Local (re-picked)
		{b: true},         // server set up? yes
		{s: "http://c:3"}, // server URL
		{s: "c"},          // name
		{b: true},         // make current
		{b: false},        // skip the connection test
	}}
	w, _ := newTestWizard(t, ui, path)
	if err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["c"]; c == nil || c.Server != "http://c:3" {
		t.Fatalf("context after back-out: %+v", c)
	}
}

// The confirm and the runtime picker are two prompts of ONE step, so Back from
// the picker re-asks the confirm rather than unwinding to the scenario picker
// (the sshDestinationStep contract). Getting this wrong would make the runtime
// answer unreachable once given.
func TestWizardBackFromRuntimePickerReasksTheConfirm(t *testing.T) {
	ui := &progUI{replies: []progReply{
		{b: false},     // server set up? no
		{err: ErrBack}, // runtime picker -> back
		{b: false},     // server set up? no (re-asked, same step)
		{i: 3},         // runtime: Incus
	}}
	w, _ := newTestWizard(t, ui, filepath.Join(t.TempDir(), "config.yaml"))
	a := Answers{Scenario: ScenarioLocal}
	if err := w.serverSetupStep(&a).ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if a.LocalBackend != backendIncus {
		t.Fatalf("LocalBackend = %q, want incus", a.LocalBackend)
	}
	if ui.pos != 4 {
		t.Errorf("consumed %d replies, want 4 (confirm, back, confirm, runtime)", ui.pos)
	}
}

// Back at the confirm is the step's own Back: it must propagate so the runner
// re-asks the previous question, or unwinds to the scenario picker.
func TestWizardBackAtServerSetupConfirmPropagates(t *testing.T) {
	ui := &progUI{replies: []progReply{{err: ErrBack}}}
	w, _ := newTestWizard(t, ui, filepath.Join(t.TempDir(), "config.yaml"))
	a := Answers{Scenario: ScenarioLocal}
	if err := w.serverSetupStep(&a).ask(); !errors.Is(err, ErrBack) {
		t.Fatalf("ask err = %v, want ErrBack", err)
	}
}

// Only the local scenario has to be asked which runtime it drives; every other
// scenario's own name already says so.
func TestWizardRuntimeQuestionIsLocalOnly(t *testing.T) {
	for _, sc := range []Scenario{ScenarioSSHBare, ScenarioSSHIncus, ScenarioURL, ScenarioKubeURL, ScenarioDockerContainer} {
		ui := &scriptUI{confirms: []bool{false}}
		w, _ := newTestWizard(t, ui, filepath.Join(t.TempDir(), "config.yaml"))
		a := Answers{Scenario: sc}
		if err := w.serverSetupStep(&a).ask(); err != nil {
			t.Fatalf("scenario %d ask: %v", sc, err)
		}
		if len(ui.offered) != 0 {
			t.Errorf("scenario %d was asked for a runtime: %+v", sc, ui.offered)
		}
	}
}

// The picker and the enum are one data structure split across two files, joined
// by scenarioDefs. A gap does not fail to compile — it silently runs the wrong
// flow for the chosen label, which is about the worst way for this to break.
func TestScenarioOptionsCoverEveryScenario(t *testing.T) {
	// Highest enum value, kept in step with the const block.
	const last = ScenarioSSHIncus
	if got, want := len(scenarioOptions), int(last)+1; got != want {
		t.Fatalf("scenarioOptions has %d entries, want %d — every scenario must be offerable", got, want)
	}
	seen := map[Scenario]bool{}
	for i, d := range scenarioDefs {
		if seen[d.Scenario] {
			t.Fatalf("scenarioDefs[%d] repeats scenario %d — two picker entries would run the same flow", i, d.Scenario)
		}
		seen[d.Scenario] = true
		if scenarioIndex(d.Scenario) != i {
			t.Errorf("scenarioIndex(%d) = %d, want %d", d.Scenario, scenarioIndex(d.Scenario), i)
		}
	}
	// Every scenario must reach a real flow and real guidance; a missing case
	// would silently fall through to the URL defaults.
	for s := ScenarioLocal; s <= last; s++ {
		if !seen[s] {
			t.Errorf("scenario %d is in the enum but has no picker entry", s)
		}
		g := guideFor(&Answers{Scenario: s})
		if len(g.Setup) == 0 {
			t.Errorf("scenario %d has no server-setup guide", s)
		}
		if len(g.Next) == 0 {
			t.Errorf("scenario %d has no next-steps guidance", s)
		}
		if g.Doc == "" {
			t.Errorf("scenario %d has no docs link", s)
		}
	}
}

// The local picker must offer every backend a `cornus serve` can actually
// drive, each with real prerequisites. Kubernetes is the one worth pinning:
// the reference calls it "server/in-cluster only", but that restriction is on a
// SERVERLESS `cornus deploy` — `cornus serve` is a server, and the backend falls
// back from in-cluster config to KUBECONFIG, so a laptop server deploying into
// kind is supported and must be offered.
func TestLocalBackendsCoverEveryDrivableRuntime(t *testing.T) {
	want := []string{backendDocker, backendContainerd, backendBare, backendIncus, backendKubernetes}
	if len(localBackends) != len(want) {
		t.Fatalf("localBackends has %d entries, want %d", len(localBackends), len(want))
	}
	for i, w := range want {
		if localBackends[i].Key != w {
			t.Errorf("localBackends[%d] = %q, want %q", i, localBackends[i].Key, w)
		}
		if localBackends[i].Label == "" || localBackends[i].Desc == "" {
			t.Errorf("localBackends[%d] (%q) has no label or description", i, w)
		}
		if backendPrereqs[w] == "" {
			t.Errorf("backend %q has no prerequisite line, so its guide would be blank", w)
		}
		// Every choice must reach a guide that names it, or the question is
		// decorative.
		setup := guideFor(&Answers{Scenario: ScenarioLocal, LocalBackend: w})
		if !containsString(setup.Setup, backendPrereqs[w]) {
			t.Errorf("local guide for %q omits its prerequisites", w)
		}
		if w != backendDocker && !strings.Contains(strings.Join(setup.Setup, "\n"), "CORNUS_DEPLOY_BACKEND="+w) {
			t.Errorf("local guide for %q never shows how to select it", w)
		}
	}
}

// The kubernetes guide has to carry the registry trap. It is the one failure a
// correct-looking local+cluster setup still walks into: the NODES pull the
// image, so a loopback registry address resolves on the node to the node.
func TestLocalKubernetesGuideCarriesTheRegistryCaveat(t *testing.T) {
	g := guideFor(&Answers{Scenario: ScenarioLocal, LocalBackend: backendKubernetes})
	joined := strings.Join(g.Setup, "\n")
	for _, want := range []string{"CORNUS_ADVERTISE_REGISTRY", "nodes pull", "KUBECONFIG", "CORNUS_K8S_NAMESPACE"} {
		if !strings.Contains(joined, want) {
			t.Errorf("local kubernetes guide missing %q:\n%s", want, joined)
		}
	}
	// It must not tell the user to sudo: no local runtime is involved.
	if strings.Contains(joined, "sudo ") {
		t.Errorf("local kubernetes guide should not require sudo:\n%s", joined)
	}
	// And it must name the in-cluster arrangement the project is built around,
	// which avoids the registry trap rather than working around it.
	for _, want := range []string{"INSIDE the cluster", "helm install cornus oci://ghcr.io/moriyoshi/charts/cornus"} {
		if !strings.Contains(joined, want) {
			t.Errorf("local kubernetes guide missing %q:\n%s", want, joined)
		}
	}
}

// The kube scenarios must lead with Helm, matching what the installation and
// quick-start pages already call recommended, and must never hand out an
// unpinned branch URL: that manifest installs a privileged StatefulSet with
// broad RBAC, so tracking `main` gives a user a manifest that does not match
// the image they pulled.
func TestKubeGuideLeadsWithHelmAndPinsNoBranch(t *testing.T) {
	for _, sc := range []Scenario{ScenarioKubePortForward, ScenarioKubeURL} {
		g := guideFor(&Answers{Scenario: sc})
		if len(g.Setup) == 0 || !strings.Contains(g.Setup[0], "helm install cornus oci://ghcr.io/moriyoshi/charts/cornus") {
			t.Errorf("scenario %d does not lead with the Helm install: %v", sc, g.Setup)
		}
		joined := strings.Join(g.Setup, "\n")
		if strings.Contains(joined, "/cornus/main/") {
			t.Errorf("scenario %d hands out an unpinned branch manifest URL:\n%s", sc, joined)
		}
	}
}

// Every arrangement the wizard can configure must land the reader on a section
// that covers THAT arrangement. The docs gate cannot check this: it validates
// links found in Markdown, and these are printed from Go, so nothing else stands
// between a rename and the wizard sending users to an anchor that scrolls
// nowhere.
func TestEveryGuideDocAnchorExistsInTheSetupGuide(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "guides", "server-setup.md"))
	if err != nil {
		t.Fatalf("read the setup guide the wizard points at: %v", err)
	}
	// Collect one Answers per landable arrangement: the local scenario branches
	// per backend, so it contributes one per runtime.
	var cases []*Answers
	for _, b := range localBackends {
		cases = append(cases, &Answers{Scenario: ScenarioLocal, LocalBackend: b.Key})
	}
	for s := ScenarioLocal; s <= ScenarioSSHIncus; s++ {
		if s != ScenarioLocal {
			cases = append(cases, &Answers{Scenario: s})
		}
	}
	seen := map[string]bool{}
	for _, a := range cases {
		doc := guideFor(a).Doc
		route, anchor, ok := strings.Cut(doc, "#")
		if !ok || route != setupGuideURL {
			t.Errorf("scenario %d backend %q: doc %q is not an anchor into %s",
				a.Scenario, a.LocalBackend, doc, setupGuideURL)
			continue
		}
		if !strings.Contains(string(page), "{#"+anchor+"}") {
			t.Errorf("scenario %d backend %q points at #%s, which the guide does not define",
				a.Scenario, a.LocalBackend, anchor)
		}
		seen[anchor] = true
	}
	// Distinct arrangements must not collapse onto one section; that would make
	// the pointer useless for whichever one lost.
	if len(seen) < 9 {
		t.Errorf("only %d distinct guide sections are reachable from the wizard: %v", len(seen), seen)
	}
}

// Every documentation reference the wizard prints must be a complete URL. A
// site-relative route like "/guides/server-setup" is meaningful inside the docs
// site and inert in a terminal: it cannot be clicked, and pasting it into a
// browser goes nowhere. This catches a new bare route anywhere in the guide
// text, which is the only place they creep back in.
func TestGuidanceDocReferencesAreCompleteURLs(t *testing.T) {
	// The section prefixes of the docs site. Deliberately narrow so that real
	// filesystem paths in the prose (/opt/cni/bin, /var/run/docker.sock,
	// ~/.kube/config) are not mistaken for doc routes.
	routes := []string{"/guides/", "/reference/", "/introduction/", "/cli/", "/cookbook/", "/architecture/"}
	// A route is legitimate exactly when docsBase immediately precedes it, so
	// check each occurrence in place rather than trying to subtract the URLs
	// first — deleting the base would leave its own route behind and report
	// every correct reference as a violation.
	scan := func(t *testing.T, where, text string) {
		t.Helper()
		for _, r := range routes {
			for at := 0; ; {
				i := strings.Index(text[at:], r)
				if i < 0 {
					break
				}
				at += i
				if !strings.HasSuffix(text[:at], docsBase) {
					t.Errorf("%s contains the bare docs route %q — wrap it in docsURL(): %q", where, r, text)
				}
				at += len(r)
			}
		}
	}
	for s := ScenarioLocal; s <= ScenarioSSHIncus; s++ {
		for _, b := range localBackends {
			g := guideFor(&Answers{Scenario: s, LocalBackend: b.Key})
			scan(t, "Doc", g.Doc)
			scan(t, "Synopsis", g.Synopsis)
			for _, line := range append(append([]string{}, g.Setup...), g.Next...) {
				scan(t, "guide text", line)
			}
		}
	}
	// And the generated artifacts, which are read far from any browser context.
	unit, err := renderSystemd(systemdData{Backend: backendBare})
	if err != nil {
		t.Fatal(err)
	}
	scan(t, "systemd unit", unit)
	scan(t, "container run command", containerRunCommand(&Answers{Scenario: ScenarioDockerContainer}))
}

// A URL must be the last thing on its line, or end at whitespace. Terminals
// that linkify output extend the link to the next space, so a trailing period
// becomes part of the target and the link 404s — the one way an absolute URL is
// still worse than useless.
func TestGuidanceURLsEndCleanly(t *testing.T) {
	for s := ScenarioLocal; s <= ScenarioSSHIncus; s++ {
		for _, b := range localBackends {
			g := guideFor(&Answers{Scenario: s, LocalBackend: b.Key})
			for _, line := range append(append([]string{g.Doc}, g.Setup...), g.Next...) {
				for at := 0; ; {
					i := strings.Index(line[at:], docsBase)
					if i < 0 {
						break
					}
					at += i
					end := at + len(docsBase)
					for end < len(line) && line[end] != ' ' {
						end++
					}
					if url := line[at:end]; strings.HasSuffix(url, ".") || strings.HasSuffix(url, ",") ||
						strings.HasSuffix(url, ";") || strings.HasSuffix(url, ")") {
						t.Errorf("URL %q ends in punctuation a linkifying terminal would swallow, in %q", url, line)
					}
					at = end
				}
			}
		}
	}
}

// Every Doc must parse as an absolute https URL, not merely start with the right
// characters.
func TestGuidanceDocIsAWellFormedURL(t *testing.T) {
	for s := ScenarioLocal; s <= ScenarioSSHIncus; s++ {
		doc := guideFor(&Answers{Scenario: s}).Doc
		u, err := url.Parse(doc)
		if err != nil {
			t.Errorf("scenario %d doc %q does not parse: %v", s, doc, err)
			continue
		}
		if u.Scheme != "https" || u.Host == "" || u.Path == "" {
			t.Errorf("scenario %d doc %q is not an absolute https URL with a path", s, doc)
		}
	}
}

// The guidance must only name commands that exist. `cornus version --health` was
// printed here for months and is not a flag `version` accepts.
func TestGuidanceNamesNoUnknownCommands(t *testing.T) {
	for s := ScenarioLocal; s <= ScenarioSSHIncus; s++ {
		g := guideFor(&Answers{Scenario: s})
		joined := strings.Join(append(append([]string{}, g.Setup...), g.Next...), "\n")
		if strings.Contains(joined, "version --health") {
			t.Errorf("scenario %d names `cornus version --health`, which is not a valid flag:\n%s", s, joined)
		}
	}
}

// A containerized server on a REMOTE docker host is the combination neither
// scenario covered: docker-container emits a loopback profile with no tunnel,
// and ssh-docker assumed a binary install. The tunnel and the profile are
// identical either way — only the far end differs — so this is a question, not a
// tenth picker entry.
func TestSSHDockerContainerizedRemoteServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects: []int{2}, // auth: None
		// ssh host, user, identity, remote addr, host data dir, registry host, name
		inputs:   []string{"remote-devbox", "ops", "", "127.0.0.1:8443", "/srv/data", "", "boxed"},
		confirms: []bool{false, true, false, true}, // not set up; containerized; no remote TLS; make current
	}
	w, _ := newTestWizard(t, ui, path)
	w.WriteFile = func(string, []byte, os.FileMode) error {
		t.Fatal("a containerized remote server has no binary for a systemd unit to start")
		return nil
	}
	if err := w.RunScenario(context.Background(), ScenarioSSHDocker); err != nil {
		t.Fatalf("RunScenario: %v", err)
	}

	// The profile is an ordinary SSH tunnel — containerization changes only what
	// runs on the far end.
	f, _ := clientconfig.Load(path)
	c := f.Contexts["boxed"]
	if c == nil || c.SSHTunnel == nil || c.SSHTunnel.Addr != "remote-devbox" || c.SSHTunnel.RemoteAddr != "127.0.0.1:8443" {
		t.Fatalf("tunnel profile: %+v", c)
	}
	if strings.Contains(string(mustRead(t, path)), "container") {
		t.Errorf("containerization must not reach the saved profile:\n%s", mustRead(t, path))
	}

	// The guide is the container shape. It precedes the SSH questions, so like
	// every other scenario it shows the defaults it is about to propose — the
	// answered 8443 and /srv/data appear in the profile above, not here.
	joined := strings.Join(ui.notes, "\n")
	for _, want := range []string{
		"docker run -d --name cornus",
		"-p 127.0.0.1:5000:5000",              // the default it will propose next
		"/srv/cornus:/var/lib/cornus:rshared", // likewise
		"serve --addr :5000",
		"No systemd unit is needed",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("containerized remote guide missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Install the cornus binary on the remote host") {
		t.Errorf("containerized remote guide still tells the user to install a binary:\n%s", joined)
	}
	// And no artifact prompt at all.
	if containsString(ui.log, "select:Setup artifact: cornus.service") {
		t.Errorf("a systemd unit was offered for a containerized remote server: %v", ui.log)
	}
}

// The command builder follows the answers even though the guide is printed
// before them: a reader who overrides the defaults must still be able to get the
// right command, and the published port must track the address the tunnel dials
// or the profile reaches nothing.
func TestSSHContainerRunCommandFollowsTheAnswers(t *testing.T) {
	a := &Answers{
		Scenario: ScenarioSSHDocker, Containerized: true,
		SSHHost: "remote-devbox", SSHRemoteAddr: "127.0.0.1:8443", ContainerDataDir: "/srv/data",
	}
	got := sshContainerRunCommand(a)
	for _, want := range []string{
		"ssh remote-devbox 'docker run -d --name cornus",
		"-p 127.0.0.1:8443:8443",
		"-v /srv/data:/var/lib/cornus:rshared",
		"serve --addr :8443'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("remote run command missing %q:\n%s", want, got)
		}
	}
	// A malformed remote address falls back to the documented default rather
	// than producing "-p 127.0.0.1::".
	for addr, want := range map[string]string{
		"127.0.0.1:5000": "5000",
		"127.0.0.1:8443": "8443",
		"":               "5000",
		"127.0.0.1":      "5000",
	} {
		if got := sshRemotePort(addr); got != want {
			t.Errorf("sshRemotePort(%q) = %q, want %q", addr, got, want)
		}
	}
}

// The question is asked only where a containerized cornus could run: it needs a
// docker daemon on the far end, which is precisely what the containerd, bare,
// and incus hosts do not have.
func TestContainerizedQuestionIsDockerHostOnly(t *testing.T) {
	for _, sc := range []Scenario{ScenarioSSHContainerd, ScenarioSSHBare, ScenarioSSHIncus, ScenarioLocal} {
		ui := &scriptUI{confirms: []bool{false}, selects: []int{0}}
		w, _ := newTestWizard(t, ui, filepath.Join(t.TempDir(), "config.yaml"))
		a := Answers{Scenario: sc}
		if err := w.serverSetupStep(&a).ask(); err != nil {
			t.Fatalf("scenario %d: %v", sc, err)
		}
		if containsString(ui.log, "confirm:Will the server run as a container on the remote host?") {
			t.Errorf("scenario %d was asked the containerized question: %v", sc, ui.log)
		}
	}
}

// k3s is the cluster the quick start actually walks through, so it belongs in
// the examples the picker and the guide name.
func TestKubernetesExamplesNameK3s(t *testing.T) {
	var desc string
	for _, b := range localBackends {
		if b.Key == backendKubernetes {
			desc = b.Desc
		}
	}
	if !strings.Contains(desc, "k3s") {
		t.Errorf("the local Kubernetes option does not name k3s: %q", desc)
	}
	if !strings.Contains(backendPrereqs[backendKubernetes], "k3s") {
		t.Errorf("the kubernetes prerequisites do not name k3s: %q", backendPrereqs[backendKubernetes])
	}
}

// Each SSH scenario must select its own deploy backend, in both the guide and
// the systemd unit — they are the only two places the choice is expressed, and
// a unit that disagrees with the guide sends the user in circles.
func TestSSHScenariosCarryTheirBackend(t *testing.T) {
	for _, tc := range []struct {
		sc      Scenario
		backend string
	}{
		{ScenarioSSHDocker, backendDocker},
		{ScenarioSSHContainerd, backendContainerd},
		{ScenarioSSHBare, backendBare},
		{ScenarioSSHIncus, backendIncus},
	} {
		a := &Answers{Scenario: tc.sc}
		if !isSSHScenario(tc.sc) {
			t.Errorf("scenario %d is not recognized as an SSH scenario", tc.sc)
		}
		if got := scenarioBackend(a); got != tc.backend {
			t.Errorf("scenarioBackend(%d) = %q, want %q", tc.sc, got, tc.backend)
		}
		unit, err := renderSystemd(systemdData{Addr: "127.0.0.1:5000", Backend: scenarioBackend(a)})
		if err != nil {
			t.Fatal(err)
		}
		if tc.backend != backendDocker && !strings.Contains(unit, "CORNUS_DEPLOY_BACKEND="+tc.backend) {
			t.Errorf("scenario %d unit does not select %q:\n%s", tc.sc, tc.backend, unit)
		}
		if want := backendPrereqs[tc.backend]; !containsString(guideFor(a).Setup, want) {
			t.Errorf("scenario %d guide omits its prerequisites %q", tc.sc, want)
		}
	}
}

// --- --scenario presets ---

// The preset names and the picker are two views of scenarioDefs; if they ever
// stop covering the same enum, `--scenario` silently configures the wrong flow.
func TestScenarioNamesCoverEveryScenario(t *testing.T) {
	names := ScenarioNames()
	if got, want := len(names), int(ScenarioSSHIncus)+1; got != want {
		t.Fatalf("ScenarioNames has %d entries, want %d", got, want)
	}
	seen := map[string]bool{}
	for i, n := range names {
		if n == "" || seen[n] {
			t.Fatalf("scenario name %d = %q is empty or duplicated", i, n)
		}
		seen[n] = true
		sc, err := ParseScenario(n)
		if err != nil {
			t.Fatalf("ParseScenario(%q): %v", n, err)
		}
		// Names and options are two views of the same row, so they must agree
		// entry by entry — but neither is tied to the enum's numbering.
		if sc != scenarioDefs[i].Scenario {
			t.Fatalf("ParseScenario(%q) = %d, want %d", n, sc, scenarioDefs[i].Scenario)
		}
		if ScenarioLabel(sc) != scenarioOptions[i].Label {
			t.Errorf("label for %q = %q, want %q", n, ScenarioLabel(sc), scenarioOptions[i].Label)
		}
	}
}

func TestParseScenarioUnknownListsValidNames(t *testing.T) {
	_, err := ParseScenario("kube")
	if err == nil {
		t.Fatal("ParseScenario(\"kube\") = nil error, want a rejection")
	}
	for _, name := range ScenarioNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not list the valid name %q", err, name)
		}
	}
	// Casing and stray spaces are a typo, not a different scenario.
	if sc, err := ParseScenario("  Kube-URL "); err != nil || sc != ScenarioKubeURL {
		t.Errorf("ParseScenario(\"  Kube-URL \") = %v, %v; want ScenarioKubeURL", sc, err)
	}
}

// RunScenario asks the scenario's questions directly: no picker Select at all,
// and the answers land in the right flow.
func TestRunScenarioSkipsThePicker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		inputs: []string{"", ""}, // server URL (default), name (default "local")
		// Already set up, so neither the runtime picker nor a guide appears and
		// the only Select that could show up is the scenario picker itself.
		confirms: []bool{true, true, false}, // set up; make current; skip verify
	}
	w, _ := newTestWizard(t, ui, path)
	if err := w.RunScenario(context.Background(), ScenarioLocal); err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	if len(ui.offered) != 0 {
		t.Errorf("the scenario picker was shown despite a preset: %+v", ui.offered)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["local"]; c == nil || c.Server != "http://127.0.0.1:5000" {
		t.Fatalf("preset local context: %+v", c)
	}
	if !containsString(ui.notes, "scenario: Local server") {
		t.Errorf("the preset scenario was not announced: %v", ui.notes)
	}
}

// Back at the first question has nowhere to go (there is no picker), so it must
// abort rather than loop or fall through to a half-answered save.
func TestRunScenarioBackAtFirstQuestionAborts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &progUI{replies: []progReply{{err: ErrBack}}}
	w, _ := newTestWizard(t, ui, path)
	if err := w.RunScenario(context.Background(), ScenarioLocal); !errors.Is(err, ErrAborted) {
		t.Fatalf("RunScenario err = %v, want ErrAborted", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Errorf("aborted preset run must not create the config file (stat err=%v)", serr)
	}
}

func TestRunScenarioRejectsAnOutOfRangeScenario(t *testing.T) {
	w, _ := newTestWizard(t, &scriptUI{}, filepath.Join(t.TempDir(), "config.yaml"))
	if err := w.RunScenario(context.Background(), Scenario(len(scenarioDefs))); err == nil {
		t.Fatal("RunScenario accepted a scenario outside the enum")
	}
}

// The new scenario must not silently inherit another's guidance.
func TestDockerContainerScenarioHasItsOwnGuidance(t *testing.T) {
	g := guideFor(&Answers{Scenario: ScenarioDockerContainer})
	if g.Doc != setupGuideURL+"#in-a-container" {
		t.Errorf("doc = %q, want the container-install section of the setup guide", g.Doc)
	}
	if len(g.Setup) == 0 || !strings.Contains(g.Setup[0], "daemon preflight") {
		t.Errorf("setup = %v, want the preflight to lead (the binds are the difficulty)", g.Setup)
	}
	// Nothing is bundled: this arrangement needs only docker, so the guide gives
	// the command rather than a compose file (which would need Compose) or a
	// shell script (which would need auditing).
	joined := strings.Join(append(append([]string{g.Synopsis}, g.Setup...), g.Next...), "\n")
	for _, forbidden := range []string{"cornus-compose.yaml", "docker compose", "cornus-run.sh"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("container guide still references %q:\n%s", forbidden, joined)
		}
	}
}

// The closing checklist carries only what to do NEXT, whether or not a server
// existed. The setup half belongs before the save — where its commands still
// reflect answers the user can change, and where acting on them is still the
// point. Repeating it afterwards inverts cause and effect.
func TestClosingGuidanceNeverRepeatsTheSetupSteps(t *testing.T) {
	for _, ready := range []bool{true, false} {
		w, buf := newTestWizard(t, &scriptUI{}, filepath.Join(t.TempDir(), "config.yaml"))
		a := &Answers{Scenario: ScenarioLocal, ServerReady: ready}
		w.guidance(a, "local", BuildContext(*a))
		out := buf.String()
		if strings.Contains(out, "Install the cornus binary on this machine") {
			t.Errorf("ServerReady=%v: closing checklist repeated a setup step:\n%s", ready, out)
		}
		// What it must still carry.
		if !strings.Contains(out, "next steps:") || !strings.Contains(out, "equivalent command:") {
			t.Errorf("ServerReady=%v: closing checklist lost its own content:\n%s", ready, out)
		}
	}
}

// The guide must appear the moment the user says no server exists — before the
// scenario asks anything else. The questions that follow (server URL, published
// port, host data directory) are answers ABOUT the setup, so a reader who has
// not been told what they are about to run is being asked to describe something
// they have not seen. This is the requirement the flow exists to serve, so it is
// pinned by position, not by content.
func TestServerGuidePrecedesTheQuestionsItInforms(t *testing.T) {
	ui := &scriptUI{
		inputs:   []string{"http://127.0.0.1:8080", "/data/cornus", "boxed"},
		confirms: []bool{false, true}, // not set up; make current
		selects:  []int{2},            // skip nothing (no artifact for this scenario)
	}
	w, _ := newTestWizard(t, ui, filepath.Join(t.TempDir(), "config.yaml"))
	if err := w.RunScenario(context.Background(), ScenarioDockerContainer); err != nil {
		t.Fatalf("RunScenario: %v", err)
	}
	idx := func(prefix string) int {
		for i, e := range ui.log {
			if strings.HasPrefix(e, prefix) {
				return i
			}
		}
		return -1
	}
	confirm := idx("confirm:Is the cornus server already set up?")
	guide := idx("note:No server yet")
	firstQuestion := idx("input:Server URL")
	if confirm < 0 || guide < 0 || firstQuestion < 0 {
		t.Fatalf("missing a step in the flow: confirm=%d guide=%d question=%d\n%v",
			confirm, guide, firstQuestion, ui.log)
	}
	if !(confirm < guide && guide < firstQuestion) {
		t.Errorf("guide must fall between the ready confirm and the first question, got confirm=%d guide=%d question=%d\n%v",
			confirm, guide, firstQuestion, ui.log)
	}
}

// Being printed before those answers, the guide shows the wizard's own defaults
// for what it cannot know yet — which are exactly what the prompts will propose,
// so the two agree unless the reader deliberately departs from both.
func TestServerGuideShowsTheDefaultsItIsAboutToPropose(t *testing.T) {
	a := &Answers{Scenario: ScenarioDockerContainer}
	joined := strings.Join(guideFor(a).Setup, "\n")
	for _, want := range []string{"127.0.0.1:5000:5000", "/srv/cornus:/var/lib/cornus:rshared"} {
		if !strings.Contains(joined, want) {
			t.Errorf("guide missing the default %q it will propose next:\n%s", want, joined)
		}
	}
	// And those really are the prompts' defaults, not a second set of constants.
	var w Wizard
	for _, st := range w.dockerContainerSteps(a) {
		_ = st
	}
	if got := portOf(""); got != "5000" {
		t.Errorf("port default drifted from the guide: %q", got)
	}
	if got := containerBinds(""); !strings.Contains(got, "/srv/cornus") {
		t.Errorf("data-dir default drifted from the guide: %q", got)
	}
}

// --- SSH destination: ssh_config Host-alias picker ---

func fixtureSSHHosts() []sshclient.ConfigHost {
	return []sshclient.ConfigHost{
		{Alias: "devbox", HostName: "10.0.0.5", User: "ops", Port: "2222"},
		{Alias: "prod", HostName: "prod.example.com", Port: "22"},
	}
}

func newDestWizard(t *testing.T, ui UI, hosts []sshclient.ConfigHost) *Wizard {
	t.Helper()
	w, _ := newTestWizard(t, ui, filepath.Join(t.TempDir(), "config.yaml"))
	w.SSHHosts = func() []sshclient.ConfigHost { return hosts }
	return w
}

func TestSSHDestinationPicksConfigAlias(t *testing.T) {
	ui := &scriptUI{selects: []int{1}} // "prod"
	w := newDestWizard(t, ui, fixtureSSHHosts())
	var a Answers
	if err := w.sshDestinationStep(&a).ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if a.SSHHost != "prod" {
		t.Fatalf("SSHHost = %q, want prod", a.SSHHost)
	}
	if len(ui.offered) != 1 {
		t.Fatalf("expected exactly one Select, got %d", len(ui.offered))
	}
	opts := ui.offered[0]
	// Aliases in config order, each annotated, plus the always-present escape hatch.
	if len(opts) != 3 || opts[0].Label != "devbox" || opts[1].Label != "prod" || opts[2].Label != sshDestinationOther {
		t.Fatalf("offered = %+v", opts)
	}
	if opts[0].Desc != "ops@10.0.0.5:2222" {
		t.Errorf("alias desc = %q, want the resolved target", opts[0].Desc)
	}
}

// A host may simply not be in ~/.ssh/config, so free text stays reachable even
// when aliases were found.
func TestSSHDestinationOtherFallsThroughToFreeText(t *testing.T) {
	ui := &scriptUI{selects: []int{2}, inputs: []string{"box.example.com:2200"}}
	w := newDestWizard(t, ui, fixtureSSHHosts())
	var a Answers
	if err := w.sshDestinationStep(&a).ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if a.SSHHost != "box.example.com:2200" {
		t.Fatalf("SSHHost = %q, want the typed destination", a.SSHHost)
	}
	if !containsString(ui.log, "input:SSH destination") {
		t.Errorf("free-text prompt was not shown: %v", ui.log)
	}
}

// No config / unreadable / only wildcard patterns all reach the wizard as an
// empty list, and must ask the plain free-text question with no picker at all.
func TestSSHDestinationNoAliasesAsksFreeText(t *testing.T) {
	ui := &scriptUI{inputs: []string{"remote-devbox"}}
	w := newDestWizard(t, ui, nil)
	var a Answers
	if err := w.sshDestinationStep(&a).ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if a.SSHHost != "remote-devbox" {
		t.Fatalf("SSHHost = %q", a.SSHHost)
	}
	if len(ui.offered) != 0 {
		t.Errorf("a picker was shown with no aliases: %+v", ui.offered)
	}
}

// Back inside the free-text entry returns to the alias list (they are two
// renderings of one question), not to the previous question.
func TestSSHDestinationBackFromFreeTextReturnsToPicker(t *testing.T) {
	ui := &progUI{replies: []progReply{
		{i: 2},         // "Other"
		{err: ErrBack}, // free text -> back
		{i: 0},         // picker again: devbox
	}}
	w := newDestWizard(t, ui, fixtureSSHHosts())
	var a Answers
	if err := w.sshDestinationStep(&a).ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if a.SSHHost != "devbox" {
		t.Fatalf("SSHHost = %q, want devbox", a.SSHHost)
	}
	if ui.pos != 3 {
		t.Errorf("consumed %d replies, want 3 (picker, back, picker)", ui.pos)
	}
}

// Back at the picker itself is the step's own Back: it must propagate so the
// step runner re-asks the previous question.
func TestSSHDestinationBackAtPickerPropagates(t *testing.T) {
	ui := &progUI{replies: []progReply{{err: ErrBack}}}
	w := newDestWizard(t, ui, fixtureSSHHosts())
	var a Answers
	if err := w.sshDestinationStep(&a).ask(); !errors.Is(err, ErrBack) {
		t.Fatalf("ask err = %v, want ErrBack", err)
	}
}

func TestValidateAbsPath(t *testing.T) {
	if err := validateAbsPath("/srv/cornus"); err != nil {
		t.Errorf("absolute path rejected: %v", err)
	}
	for _, bad := range []string{"", "   ", "srv/cornus", "./data"} {
		if err := validateAbsPath(bad); err == nil {
			t.Errorf("validateAbsPath(%q) = nil, want an error (it names a host directory)", bad)
		}
	}
}
