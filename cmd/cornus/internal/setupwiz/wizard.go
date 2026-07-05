package setupwiz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/clientconfig"
	"cornus/pkg/svcforward"
)

// contextNameRE is the allowed context-name shape (mirrors kubeconfig-ish names):
// a leading alphanumeric, then alphanumerics, dot, underscore, or hyphen.
var contextNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Wizard drives the UI-agnostic question flow. Its behavior-carrying dependencies
// are field-injection seams (not package vars, so parallel tests don't race): the
// test flow can stub cluster discovery, verification, and filesystem writes.
type Wizard struct {
	ui         UI
	d          *cliout.Driver
	configPath string

	// Discover auto-detects the in-cluster cornus Service (default svcforward.Discover).
	Discover func(context.Context, svcforward.DiscoverOptions) (svcforward.DiscoverResult, error)
	// Verify runs the post-save connection check (default VerifyConnection).
	Verify func(ctx context.Context, configPath, contextName string) VerifyResult
	// Ingress probes the server's advertised ingress facts to propose an
	// ingress-via-conduit mode (default probeIngress).
	Ingress func(ctx context.Context, a *Answers) IngressFacts
	// WriteFile writes an artifact (default os.WriteFile).
	WriteFile func(name string, data []byte, perm os.FileMode) error
	// Stat guards artifact overwrites (default os.Stat).
	Stat func(name string) (os.FileInfo, error)
}

// NewWizard builds a Wizard bound to the driver, UI, and config path, with the
// production seams installed.
func NewWizard(d *cliout.Driver, ui UI, configPath string) *Wizard {
	return &Wizard{
		ui:         ui,
		d:          d,
		configPath: configPath,
		Discover:   svcforward.Discover,
		Verify:     VerifyConnection,
		Ingress:    probeIngress,
		WriteFile:  os.WriteFile,
		Stat:       os.Stat,
	}
}

var scenarioOptions = []Option{
	{Label: "Local server", Desc: "cornus serve on this machine"},
	{Label: "Remote Docker host (SSH)", Desc: "reach a docker host over an SSH tunnel"},
	{Label: "Remote containerd host (SSH)", Desc: "reach a containerd host over an SSH tunnel"},
	{Label: "Kubernetes (auto port-forward)", Desc: "in-cluster install, reached by port-forward"},
	{Label: "Kubernetes (direct URL)", Desc: "in-cluster install, reached by an ingress URL"},
	{Label: "Other server URL", Desc: "a server at an already-known URL"},
}

// step is one back-navigable unit of the flow: ask asks exactly one question and
// stores the answer; skip (when non-nil and true) makes the step transparent —
// stepped over in the current direction of travel, so a conditional block is
// invisible to Back.
type step struct {
	skip func() bool
	ask  func() error
}

// runSteps runs steps with back navigation. A step returning ErrBack re-asks the
// previous non-skipped step; ErrBack from the first step returns ErrBack so the
// caller unwinds to the scenario picker. A real error aborts.
func (w *Wizard) runSteps(steps []step) error {
	i, dir := 0, 1
	for i >= 0 && i < len(steps) {
		s := steps[i]
		if s.skip != nil && s.skip() {
			i += dir
			continue
		}
		switch err := s.ask(); {
		case errors.Is(err, ErrBack):
			i, dir = i-1, -1
		case err != nil:
			return err
		default:
			i, dir = i+1, 1
		}
	}
	if i < 0 {
		return ErrBack
	}
	return nil
}

// Run executes the whole flow. The scenario picker plus the scenario's questions
// form a back-navigable region: Esc at any question steps back one; Esc at the
// first question returns to the scenario picker. Materialization (Save) is a
// single atomic point after that region, so an abort or an unfinished flow never
// leaves partial state on disk. Post-save prompts (verify, artifacts) are
// optional and tolerate Esc/Ctrl-C without discarding the saved context.
func (w *Wizard) Run(ctx context.Context) error {
	w.ui.Note("cornus setup — writing to %s", w.configPath)

	f, err := clientconfig.Load(w.configPath)
	if err != nil {
		return err
	}
	origCurrent := f.CurrentContext

	var a Answers
	for {
		idx, serr := w.ui.Select("Which deployment scenario are you configuring?", "", scenarioOptions, int(a.Scenario))
		if errors.Is(serr, ErrBack) {
			// Esc at the very first screen has nowhere to go back to: treat as quit.
			return ErrAborted
		}
		if serr != nil {
			return serr
		}
		sc := Scenario(idx)
		if sc != a.Scenario {
			a = Answers{Scenario: sc} // drop stale answers when the scenario changes
		}
		if rerr := w.runSteps(w.scenarioSteps(ctx, &a, f, origCurrent)); rerr != nil {
			if errors.Is(rerr, ErrBack) {
				continue // backed out of the first question -> re-pick the scenario
			}
			return rerr
		}
		break
	}

	built := BuildContext(a)
	if f.Contexts == nil {
		f.Contexts = map[string]*clientconfig.Context{}
	}
	f.Contexts[a.Name] = built
	if err := clientconfig.Save(w.configPath, f); err != nil {
		return err
	}
	w.d.Done("context %q saved to %s", a.Name, w.configPath)

	// Post-save prompts are optional. Esc (ErrBack) skips just that prompt; Ctrl-C
	// (ErrAborted) stops the remaining prompts but still prints guidance, because
	// the context is already saved.
	if err := w.maybeVerify(ctx, &a); err != nil {
		if errors.Is(err, ErrAborted) {
			w.guidance(&a, a.Name, built)
			return nil
		}
		if !errors.Is(err, ErrBack) {
			return err
		}
	}
	if err := w.writeArtifacts(&a); err != nil {
		if errors.Is(err, ErrAborted) {
			w.guidance(&a, a.Name, built)
			return nil
		}
		if !errors.Is(err, ErrBack) {
			return err
		}
	}
	w.guidance(&a, a.Name, built)
	return nil
}

// scenarioSteps builds the ordered, back-navigable question list for the chosen
// scenario, appending the shared name and current-context steps at the end.
func (w *Wizard) scenarioSteps(ctx context.Context, a *Answers, f *clientconfig.File, origCurrent string) []step {
	var steps []step
	switch a.Scenario {
	case ScenarioLocal:
		steps = w.localSteps(a)
	case ScenarioSSHDocker, ScenarioSSHContainerd:
		steps = w.sshSteps(a)
	case ScenarioKubePortForward:
		steps = w.kubePortForwardSteps(ctx, a)
	case ScenarioKubeURL:
		steps = w.kubeURLSteps(ctx, a)
	case ScenarioURL:
		steps = w.urlSteps(a)
	}
	return append(steps, w.nameStep(a, f), w.currentStep(a, f, origCurrent))
}

// --- step constructors ---

// inputStep asks a free-text question, storing via set. On a re-ask after Back
// the current value (get) is offered as the default so prior input is preserved;
// secret answers are never echoed back as a default.
func (w *Wizard) inputStep(q Question, get func() string, set func(string)) step {
	return step{ask: func() error {
		qq := q
		if cur := get(); cur != "" && !q.Secret {
			qq.Default = cur
		}
		v, err := w.ui.Input(qq)
		if err != nil {
			return err
		}
		set(v)
		return nil
	}}
}

func (w *Wizard) confirmStep(question string, get func() bool, set func(bool)) step {
	return step{ask: func() error {
		v, err := w.ui.Confirm(question, get())
		if err != nil {
			return err
		}
		set(v)
		return nil
	}}
}

func (w *Wizard) portStep(title string, def int, get func() int, set func(int)) step {
	return step{ask: func() error {
		d := def
		if cur := get(); cur != 0 {
			d = cur
		}
		for {
			v, err := w.ui.Input(Question{Title: title, Default: strconv.Itoa(d)})
			if err != nil {
				return err
			}
			n, perr := strconv.Atoi(strings.TrimSpace(v))
			if perr != nil || n <= 0 || n > 65535 {
				w.ui.Note("please enter a port between 1 and 65535")
				continue
			}
			set(n)
			return nil
		}
	}}
}

func (w *Wizard) registryHostStep(a *Answers) step {
	return w.inputStep(Question{
		Title:   "Registry host override (optional)",
		Help:    "host[:port] built images are tagged with; empty = auto-detect from the server",
		Example: "registry.example.com:5000",
	}, func() string { return a.RegistryHost }, func(v string) { a.RegistryHost = v })
}

// nameStep asks the context name (defaulting to a scenario-derived suggestion)
// and, when the name already exists, an overwrite confirm. Back from the overwrite
// confirm re-asks the name; Back from the name input propagates to the prior step.
func (w *Wizard) nameStep(a *Answers, f *clientconfig.File) step {
	return step{ask: func() error {
		def := orDefault(a.Name, defaultName(a))
		for {
			name, err := w.ui.Input(Question{Title: "Context name", Default: def, Validate: validateContextName})
			if err != nil {
				return err
			}
			if _, exists := f.Contexts[name]; exists {
				ok, cerr := w.ui.Confirm(fmt.Sprintf("Context %q already exists. Overwrite it?", name), false)
				if cerr != nil {
					if errors.Is(cerr, ErrBack) {
						def = name
						continue
					}
					return cerr
				}
				if !ok {
					def = name
					continue
				}
			}
			a.Name = name
			return nil
		}
	}}
}

// currentStep asks whether the new context becomes the current one: default YES
// when there is no current context (a fresh user), default NO when switching an
// existing default; asks nothing when the name already is the current context.
// origCurrent is captured once (in Run) so repeated back/forward traversal stays
// idempotent.
func (w *Wizard) currentStep(a *Answers, f *clientconfig.File, origCurrent string) step {
	return step{ask: func() error {
		var ok bool
		var err error
		switch {
		case origCurrent == "":
			ok, err = w.ui.Confirm("Make this the current (default) context?", true)
		case origCurrent != a.Name:
			ok, err = w.ui.Confirm(fmt.Sprintf("Switch the current context from %q to %q?", origCurrent, a.Name), false)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		if ok {
			f.CurrentContext = a.Name
		} else {
			f.CurrentContext = origCurrent
		}
		return nil
	}}
}

// --- scenario step lists (question order mirrors the documented flow) ---

func (w *Wizard) localSteps(a *Answers) []step {
	return []step{
		w.inputStep(Question{Title: "Server URL", Default: "http://127.0.0.1:5000", Validate: validateServerURL},
			func() string { return a.Server }, func(v string) { a.Server = v }),
		w.confirmStep("Is a cornus server already running there?",
			func() bool { return a.LocalServerRunning }, func(v bool) { a.LocalServerRunning = v }),
	}
}

func (w *Wizard) sshSteps(a *Answers) []step {
	tokenNeeded := a.Token != ""

	sni := w.inputStep(Question{Title: "TLS server name", Help: "the hostname the server certificate is issued for", Example: "remote-devbox"},
		func() string { return a.ServerName }, func(v string) { a.ServerName = v })
	sni.skip = func() bool { return !a.SSHTLS }
	ca := w.inputStep(Question{Title: "CA certificate path", Help: "empty uses the system trust store", Example: "/etc/ssl/certs/ca.pem", Validate: validateFileExistsOrEmpty},
		func() string { return a.CACert }, func(v string) { a.CACert = v })
	ca.skip = func() bool { return !a.SSHTLS }
	tok := w.inputStep(Question{Title: "Bearer token", Secret: true},
		func() string { return a.Token }, func(v string) { a.Token = v })
	tok.skip = func() bool { return !tokenNeeded }

	return []step{
		w.inputStep(Question{Title: "SSH destination", Help: "an ssh_config Host alias or host[:port]", Example: "remote-devbox", Validate: validateNonEmpty},
			func() string { return a.SSHHost }, func(v string) { a.SSHHost = v }),
		w.inputStep(Question{Title: "SSH user", Help: "empty defers to ssh_config, then the current user", Example: "deploy"},
			func() string { return a.SSHUser }, func(v string) { a.SSHUser = v }),
		w.inputStep(Question{Title: "SSH identity file", Help: "empty uses the ssh-agent / ssh_config default", Example: "~/.ssh/id_ed25519", Validate: validateFileExistsOrEmpty},
			func() string { return a.SSHIdentityFile }, func(v string) { a.SSHIdentityFile = v }),
		w.inputStep(Question{Title: "Remote server address", Default: "127.0.0.1:5000"},
			func() string { return a.SSHRemoteAddr }, func(v string) { a.SSHRemoteAddr = v }),
		w.confirmStep("Does the remote server terminate TLS itself?",
			func() bool { return a.SSHTLS },
			func(v bool) {
				if !v {
					a.ServerName, a.CACert = "", ""
				}
				a.SSHTLS = v
			}),
		sni, ca,
		w.confirmStep("Does the server require a bearer token?",
			func() bool { return tokenNeeded },
			func(v bool) {
				tokenNeeded = v
				if !v {
					a.Token = ""
				}
			}),
		tok,
		w.registryHostStep(a),
	}
}

func (w *Wizard) kubePortForwardSteps(ctx context.Context, a *Answers) []step {
	detected := false
	nsStep := step{ask: func() error {
		ns, err := w.ui.Input(Question{Title: "Namespace", Default: orDefault(a.Namespace, "default")})
		if err != nil {
			return err
		}
		a.Namespace = ns
		res, derr := w.Discover(ctx, svcforward.DiscoverOptions{KubeContext: a.KubeContext, Namespace: ns})
		if derr == nil {
			a.PFService, a.PFRemotePort, detected = res.Service, res.RemotePort, true
			w.ui.Note("detected service %s/%s port %d (%s)", ns, res.Service, res.RemotePort, res.Managed)
		} else {
			detected = false
			w.ui.Note("could not auto-detect the cornus service: %v", derr)
		}
		return nil
	}}
	svc := w.inputStep(Question{Title: "Service name", Default: "cornus"},
		func() string { return a.PFService }, func(v string) { a.PFService = v })
	svc.skip = func() bool { return detected }
	port := w.portStep("Service port", 5000, func() int { return a.PFRemotePort }, func(v int) { a.PFRemotePort = v })
	port.skip = func() bool { return detected }

	steps := []step{
		w.inputStep(Question{Title: "kubeconfig context", Help: "empty uses the current kube context", Example: "prod-cluster"},
			func() string { return a.KubeContext }, func(v string) { a.KubeContext = v }),
		nsStep, svc, port,
	}
	steps = append(steps, w.kubeAuthSteps(a, "default")...)
	steps = append(steps, w.ingressStep(ctx, a))
	return append(steps, w.registryHostStep(a))
}

func (w *Wizard) kubeURLSteps(ctx context.Context, a *Answers) []step {
	ca := w.inputStep(Question{Title: "CA certificate path", Help: "empty uses the system trust store", Example: "/etc/ssl/certs/ca.pem", Validate: validateFileExistsOrEmpty},
		func() string { return a.CACert }, func(v string) { a.CACert = v })
	ca.skip = func() bool { return !isHTTPS(a.Server) }

	steps := []step{
		w.inputStep(Question{Title: "Server URL", Help: "the ingress URL of the in-cluster cornus", Example: "https://cornus.example.com", Validate: validateServerURL},
			func() string { return a.Server }, func(v string) { a.Server = v }),
		ca,
	}
	steps = append(steps, w.kubeAuthSteps(a, "default")...)
	steps = append(steps, w.ingressStep(ctx, a))
	return append(steps, w.registryHostStep(a))
}

func (w *Wizard) urlSteps(a *Answers) []step {
	tokenNeeded := a.Token != ""

	ca := w.inputStep(Question{Title: "CA certificate path", Help: "empty uses the system trust store", Example: "/etc/ssl/certs/ca.pem", Validate: validateFileExistsOrEmpty},
		func() string { return a.CACert }, func(v string) { a.CACert = v })
	ca.skip = func() bool { return !isHTTPS(a.Server) }
	skipVerify := w.confirmStep("Skip TLS certificate verification (testing only)?",
		func() bool { return a.Insecure }, func(v bool) { a.Insecure = v })
	skipVerify.skip = func() bool { return !isHTTPS(a.Server) }
	tok := w.inputStep(Question{Title: "Bearer token", Secret: true},
		func() string { return a.Token }, func(v string) { a.Token = v })
	tok.skip = func() bool { return !tokenNeeded }

	return []step{
		w.inputStep(Question{Title: "Server URL", Example: "https://cornus.example.com:5000", Validate: validateServerURL},
			func() string { return a.Server }, func(v string) { a.Server = v }),
		ca, skipVerify,
		w.confirmStep("Does the server require a bearer token?",
			func() bool { return tokenNeeded },
			func(v bool) {
				tokenNeeded = v
				if !v {
					a.Token = ""
				}
			}),
		tok,
		w.registryHostStep(a),
	}
}

// kubeAuthSteps builds the authentication sub-flow shared by the two kube
// scenarios: a choice select, then the fields for the chosen method. Switching
// the choice clears the other method's fields so an abandoned branch never leaks
// into the saved context.
func (w *Wizard) kubeAuthSteps(a *Answers, fallbackNamespace string) []step {
	choice := 0
	if a.Token != "" {
		choice = 1
	} else if a.KubeAuthServiceAccount != "" || a.KubeAuthAudience != "" {
		choice = 2
	}
	choiceStep := step{ask: func() error {
		idx, err := w.ui.Select("Authentication", "", []Option{
			{Label: "None", Desc: "no bearer token"},
			{Label: "Static token", Desc: "a fixed bearer token / JWT"},
			{Label: "Kubernetes ServiceAccount", Desc: "mint a short-lived token via the cluster"},
		}, choice)
		if err != nil {
			return err
		}
		if idx != choice {
			a.Token, a.KubeAuthNamespace, a.KubeAuthServiceAccount, a.KubeAuthAudience = "", "", "", ""
		}
		choice = idx
		return nil
	}}
	tok := w.inputStep(Question{Title: "Bearer token", Secret: true},
		func() string { return a.Token }, func(v string) { a.Token = v })
	tok.skip = func() bool { return choice != 1 }
	saNs := step{
		skip: func() bool { return choice != 2 },
		ask: func() error {
			def := orDefault(a.KubeAuthNamespace, orDefault(a.Namespace, fallbackNamespace))
			v, err := w.ui.Input(Question{Title: "ServiceAccount namespace", Default: def})
			if err != nil {
				return err
			}
			a.KubeAuthNamespace = v
			return nil
		},
	}
	saName := w.inputStep(Question{Title: "ServiceAccount name", Default: "cornus"},
		func() string { return a.KubeAuthServiceAccount }, func(v string) { a.KubeAuthServiceAccount = v })
	saName.skip = func() bool { return choice != 2 }
	aud := w.inputStep(Question{Title: "Audience", Help: "must equal the server CORNUS_JWT_AUDIENCE", Example: "cornus"},
		func() string { return a.KubeAuthAudience }, func(v string) { a.KubeAuthAudience = v })
	aud.skip = func() bool { return choice != 2 }
	return []step{choiceStep, tok, saNs, saName, aud}
}

func (w *Wizard) maybeVerify(ctx context.Context, a *Answers) error {
	// Don't offer to verify a local server the user says isn't up yet — it would
	// be a guaranteed failure.
	if a.Scenario == ScenarioLocal && !a.LocalServerRunning {
		return nil
	}
	ok, err := w.ui.Confirm("Test the connection now?", true)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	w.d.Step("verifying connection")
	res := w.Verify(ctx, w.configPath, a.Name)
	if res.OK {
		w.d.Success("%s", res.Detail)
		return nil
	}
	w.d.Warn("could not verify the connection: %s", res.Detail)
	for _, h := range res.Hints {
		w.d.Info("%s", h)
	}
	w.d.Info("the profile was saved; retry with: cornus --context %s version", a.Name)
	return nil
}

// --- validators & name helpers ---

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func validateNonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("a value is required")
	}
	return nil
}

func validateContextName(s string) error {
	if s == "" {
		return fmt.Errorf("a context name is required")
	}
	if !contextNameRE.MatchString(s) {
		return fmt.Errorf("must start alphanumeric and use only letters, digits, '.', '_', '-'")
	}
	return nil
}

func validateServerURL(s string) error {
	if s == "" {
		return fmt.Errorf("a server URL is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	return nil
}

func validateFileExistsOrEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if _, err := os.Stat(s); err != nil {
		return fmt.Errorf("cannot read %s: %w", s, err)
	}
	return nil
}

func isHTTPS(server string) bool {
	u, err := url.Parse(server)
	return err == nil && u.Scheme == "https"
}

// defaultName computes the suggested context name for the chosen scenario.
func defaultName(a *Answers) string {
	switch a.Scenario {
	case ScenarioLocal:
		return "local"
	case ScenarioSSHDocker, ScenarioSSHContainerd:
		return sanitizeName(sshHostPart(a.SSHHost))
	case ScenarioKubePortForward:
		return kubeName(a.KubeContext, a.Namespace)
	case ScenarioKubeURL, ScenarioURL:
		return hostName(a.Server)
	}
	return "cornus"
}

func hostName(server string) string {
	if u, err := url.Parse(server); err == nil && u.Hostname() != "" {
		return sanitizeName(u.Hostname())
	}
	return sanitizeName(server)
}

func sshHostPart(dest string) string {
	if i := strings.LastIndex(dest, "@"); i >= 0 {
		dest = dest[i+1:]
	}
	if i := strings.Index(dest, ":"); i >= 0 {
		dest = dest[:i]
	}
	return dest
}

func kubeName(kctx, ns string) string {
	if kctx != "" {
		return sanitizeName(kctx)
	}
	if ns != "" && ns != "default" {
		return sanitizeName(ns)
	}
	return "cluster"
}

// sanitizeName maps an arbitrary string to a valid context name: allowed
// characters are kept, others become '-', and leading/trailing separators are
// trimmed so the result satisfies contextNameRE (or falls back to "cornus").
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" || !contextNameRE.MatchString(out) {
		return "cornus"
	}
	return out
}
