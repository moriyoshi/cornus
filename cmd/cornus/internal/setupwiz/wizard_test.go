package setupwiz

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/clientconfig"
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
}

func (s *scriptUI) Select(title, help string, opts []Option, def int) (int, error) {
	s.log = append(s.log, "select:"+title)
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

func (s *scriptUI) Note(format string, a ...any) { s.notes = append(s.notes, format) }

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
	return w, &buf
}

func TestWizardLocalScenario(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &scriptUI{
		selects:  []int{0},            // Local
		inputs:   []string{"", ""},    // server (default), name (default "local")
		confirms: []bool{false, true}, // not running, make current
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
		t.Error("verify must not run for a local server the user says is not up")
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
		confirms: []bool{true, true, true}, // running, make current, test now
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
}

func TestWizardOverwriteGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// Seed an existing "local" context.
	seed := &clientconfig.File{Contexts: map[string]*clientconfig.Context{"local": {Server: "http://old"}}}
	if err := clientconfig.Save(path, seed); err != nil {
		t.Fatal(err)
	}
	ui := &scriptUI{
		selects:  []int{0},
		inputs:   []string{"", "local", "local2"}, // server; name "local" (exists); then "local2"
		confirms: []bool{false, false, true},      // not running; decline overwrite; make current
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
		selects:  []int{0},
		inputs:   []string{"", "fresh"},
		confirms: []bool{false, false}, // not running; do NOT switch current
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
		selects:  []int{3, 0, 0, 2},        // kube-pf; auth none; ingress off; artifact skip
		inputs:   []string{"", "", "", ""}, // kube-context; namespace(default); registry-host; name(default)
		confirms: []bool{true, false},      // make current; skip verify
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
// (running?) confirm; the flow then completes normally.
func TestWizardBackReasksPreviousStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	ui := &progUI{replies: []progReply{
		{i: 0},            // scenario: Local
		{s: "http://a:1"}, // server URL
		{b: false},        // running? no
		{err: ErrBack},    // name -> Back to the running confirm
		{b: false},        // running? no (re-asked)
		{s: "myctx"},      // name
		{b: true},         // make current
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
		{err: ErrBack},    // server URL -> Back out to the scenario picker
		{i: 0},            // scenario: Local (re-picked)
		{s: "http://c:3"}, // server URL
		{b: false},        // running? no
		{s: "c"},          // name
		{b: true},         // make current
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
