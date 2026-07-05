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
	"cornus/pkg/sshclient"
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
	// Enroll proves possession of the selected key and enrolls it after the profile
	// has been saved (default EnrollSSHKey).
	Enroll func(ctx context.Context, configPath, contextName, code string) error
	// EnrollmentCode obtains a code automatically when the topology permits it.
	// SSH scenarios execute the local command on the remote host.
	EnrollmentCode func(ctx context.Context, a *Answers) (string, error)
	// Ingress probes the server's advertised ingress facts to propose an
	// ingress-via-conduit mode (default probeIngress).
	Ingress func(ctx context.Context, a *Answers) IngressFacts
	// SSHHosts lists the connectable Host aliases in the user's ssh_config, which
	// the SSH destination step offers as a picker (default sshclient.ConfigHosts).
	// It is a seam so tests never read the developer's real ~/.ssh/config.
	SSHHosts func() []sshclient.ConfigHost
	// WriteFile writes an artifact (default os.WriteFile).
	WriteFile func(name string, data []byte, perm os.FileMode) error
	// Stat guards artifact overwrites (default os.Stat).
	Stat func(name string) (os.FileInfo, error)
}

// NewWizard builds a Wizard bound to the driver, UI, and config path, with the
// production seams installed.
func NewWizard(d *cliout.Driver, ui UI, configPath string) *Wizard {
	return &Wizard{
		ui:             ui,
		d:              d,
		configPath:     configPath,
		Discover:       svcforward.Discover,
		Verify:         VerifyConnection,
		Enroll:         EnrollSSHKey,
		EnrollmentCode: SSHEnrollmentCode,
		Ingress:        probeIngress,
		SSHHosts:       sshclient.ConfigHosts,
		WriteFile:      os.WriteFile,
		Stat:           os.Stat,
	}
}

// scenarioDef is one entry of the scenario list.
type scenarioDef struct {
	// Scenario is the enum value this entry configures. It is stated rather than
	// derived from the slice index so the picker's ORDER (which groups related
	// scenarios for a reader) is independent of the enum's numbering (which is
	// append-only). Deriving one from the other silently renumbers every later
	// scenario the moment an entry is inserted rather than appended.
	Scenario Scenario
	// Key is the --scenario preset name (stable, kebab-case, script-facing).
	Key string
	// Label and Desc are what the picker shows.
	Label string
	Desc  string
}

// scenarioDefs is the single source of truth for the scenario list. Both the
// picker's options and the --scenario preset names are derived from it, so a
// name can never address a scenario the picker does not offer, and the two
// lists cannot drift apart. The four SSH entries are adjacent deliberately:
// they are one topology (a server on a remote host, reached through a tunnel)
// differing only in which runtime that server drives.
var scenarioDefs = []scenarioDef{
	{ScenarioLocal, "local", "Local server", "cornus serve on this machine"},
	{ScenarioSSHDocker, "ssh-docker", "Remote Docker host (SSH)", "reach a docker host over an SSH tunnel"},
	{ScenarioSSHContainerd, "ssh-containerd", "Remote containerd host (SSH)", "reach a containerd host over an SSH tunnel"},
	{ScenarioSSHBare, "ssh-bare", "Remote daemonless host (SSH)", "the server there drives runc/crun itself, with no daemon"},
	{ScenarioSSHIncus, "ssh-incus", "Remote Incus host (SSH)", "deploy as Incus application containers"},
	{ScenarioKubePortForward, "kube-port-forward", "Kubernetes (auto port-forward)", "in-cluster install, reached by port-forward"},
	{ScenarioKubeURL, "kube-url", "Kubernetes (direct URL)", "in-cluster install, reached by an ingress URL"},
	{ScenarioURL, "url", "Other server URL", "a server at an already-known URL"},
	{ScenarioDockerContainer, "docker-container", "Docker host (server in a container)", "run the server as a container on this docker host"},
}

// scenarioOptions is the picker's option list, derived from scenarioDefs.
var scenarioOptions = func() []Option {
	opts := make([]Option, len(scenarioDefs))
	for i, d := range scenarioDefs {
		opts[i] = Option{Label: d.Label, Desc: d.Desc}
	}
	return opts
}()

// ScenarioNames returns the valid --scenario preset names, in picker order.
func ScenarioNames() []string {
	names := make([]string, len(scenarioDefs))
	for i, d := range scenarioDefs {
		names[i] = d.Key
	}
	return names
}

// ParseScenario maps a --scenario preset name to its Scenario, case- and
// space-insensitively. An unknown name reports the full valid list, since the
// flag's whole purpose is to skip the picker that would have shown it.
func ParseScenario(name string) (Scenario, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	for _, d := range scenarioDefs {
		if d.Key == key {
			return d.Scenario, nil
		}
	}
	return 0, fmt.Errorf("unknown scenario %q; valid scenarios: %s", name, strings.Join(ScenarioNames(), ", "))
}

// ScenarioLabel is the human name of a scenario, as the picker spells it, or
// "" for a value no picker entry offers.
func ScenarioLabel(s Scenario) string {
	for _, d := range scenarioDefs {
		if d.Scenario == s {
			return d.Label
		}
	}
	return ""
}

// scenarioIndex is the picker position of a scenario, used to place the cursor
// on a re-pick. An unknown scenario starts at the top.
func scenarioIndex(s Scenario) int {
	for i, d := range scenarioDefs {
		if d.Scenario == s {
			return i
		}
	}
	return 0
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

// Run executes the whole flow, starting at the scenario picker. The picker plus
// the scenario's questions form a back-navigable region: Esc at any question
// steps back one; Esc at the first question returns to the scenario picker.
// Materialization (Save) is a single atomic point after that region, so an abort
// or an unfinished flow never leaves partial state on disk. Post-save prompts
// (verify, artifacts) are optional and tolerate Esc/Ctrl-C without discarding
// the saved context.
func (w *Wizard) Run(ctx context.Context) error { return w.run(ctx, nil) }

// RunScenario runs the same flow for a preset scenario, skipping the picker —
// the `cornus setup --scenario NAME` entry point. With no picker to return to,
// backing out of the first question aborts, exactly as Esc on the picker does.
func (w *Wizard) RunScenario(ctx context.Context, sc Scenario) error {
	if ScenarioLabel(sc) == "" {
		return fmt.Errorf("scenario %d is not one of the %d known scenarios", sc, len(scenarioDefs))
	}
	return w.run(ctx, &sc)
}

func (w *Wizard) run(ctx context.Context, preset *Scenario) error {
	w.ui.Note("cornus setup — writing to %s", w.configPath)

	f, err := clientconfig.Load(w.configPath)
	if err != nil {
		return err
	}
	origCurrent := f.CurrentContext

	var a Answers
	for {
		if preset != nil {
			a = Answers{Scenario: *preset}
			w.ui.Note("scenario: %s", ScenarioLabel(*preset))
		} else {
			idx, serr := w.ui.Select("Which deployment scenario are you configuring?", "", scenarioOptions, scenarioIndex(a.Scenario))
			if errors.Is(serr, ErrBack) {
				// Esc at the very first screen has nowhere to go back to: treat as quit.
				return ErrAborted
			}
			if serr != nil {
				return serr
			}
			sc := scenarioDefs[idx].Scenario
			if sc != a.Scenario {
				a = Answers{Scenario: sc} // drop stale answers when the scenario changes
			}
		}
		if rerr := w.runSteps(w.scenarioSteps(ctx, &a, f, origCurrent)); rerr != nil {
			if errors.Is(rerr, ErrBack) {
				if preset != nil {
					// No picker to fall back to; the first question is the first screen.
					return ErrAborted
				}
				continue // backed out of the first question -> re-pick the scenario
			}
			return rerr
		}
		break
	}

	// The setup artifacts belong here for the same reason, and they are the
	// guide's commands in file form: a unit that would listen somewhere else, or
	// helm values that disagree with the profile, are exactly what offering them
	// before the answers were in would produce. They stay INSIDE the pre-save
	// region, so Ctrl-C keeps meaning "nothing was written" — the prompt already
	// carries an explicit Skip, and Esc declines it too.
	if err := w.writeArtifacts(&a); err != nil && !errors.Is(err, ErrBack) {
		return err
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
	if err := w.maybeEnroll(ctx, &a); err != nil {
		if errors.Is(err, ErrAborted) {
			w.guidance(&a, a.Name, built)
			return nil
		}
		if !errors.Is(err, ErrBack) {
			return err
		}
	}
	if err := w.maybeVerify(ctx, &a); err != nil {
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
// scenario: the shared server-setup gate first, then the scenario's own
// questions, then the shared name and current-context steps.
func (w *Wizard) scenarioSteps(ctx context.Context, a *Answers, f *clientconfig.File, origCurrent string) []step {
	steps := []step{w.serverSetupStep(a)}
	switch a.Scenario {
	case ScenarioLocal:
		steps = append(steps, w.localSteps(a)...)
	case ScenarioSSHDocker, ScenarioSSHContainerd, ScenarioSSHBare, ScenarioSSHIncus:
		steps = append(steps, w.sshSteps(a)...)
	case ScenarioKubePortForward:
		steps = append(steps, w.kubePortForwardSteps(ctx, a)...)
	case ScenarioKubeURL:
		steps = append(steps, w.kubeURLSteps(ctx, a)...)
	case ScenarioURL:
		steps = append(steps, w.urlSteps(a)...)
	case ScenarioDockerContainer:
		steps = append(steps, w.dockerContainerSteps(a)...)
	}
	return append(steps, w.nameStep(a, f), w.currentStep(a, f, origCurrent))
}

// isSSHScenario reports whether the scenario reaches its server through an SSH
// tunnel. The transport is backend-agnostic — all four host backends use the
// identical question list — so this is the right test wherever the tunnel, and
// not the runtime behind it, is what matters.
func isSSHScenario(s Scenario) bool {
	switch s {
	case ScenarioSSHDocker, ScenarioSSHContainerd, ScenarioSSHBare, ScenarioSSHIncus:
		return true
	}
	return false
}

// scenarioBackend is the CORNUS_DEPLOY_BACKEND the server behind this scenario
// drives. For the local scenario it is the user's answer; for the SSH scenarios
// the scenario itself names it. It is empty (the dockerhost default) for the
// scenarios where the question does not arise.
func scenarioBackend(a *Answers) string {
	switch a.Scenario {
	case ScenarioLocal:
		return a.LocalBackend
	case ScenarioSSHContainerd:
		return backendContainerd
	case ScenarioSSHBare:
		return backendBare
	case ScenarioSSHIncus:
		return backendIncus
	}
	return backendDocker
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

// localBackends are the runtimes a `cornus serve` on this machine can drive,
// paired with the CORNUS_DEPLOY_BACKEND value that selects each.
//
// Kubernetes belongs here even though the reference calls it "server/in-cluster
// only": that restriction is on a SERVERLESS `cornus deploy`, which falls back
// to dockerhost with a warning. `cornus serve` is a server, and the backend
// falls back from in-cluster config to the ordinary KUBECONFIG rules, so a
// laptop server deploying into kind is a supported topology — and a different
// one from the two kube SCENARIOS, which configure a client reaching a cornus
// that runs inside the cluster.
var localBackends = []struct {
	Key string
	Option
}{
	{backendDocker, Option{Label: "Docker", Desc: "the default; needs /var/run/docker.sock"}},
	{backendContainerd, Option{Label: "containerd", Desc: "no dockerd; needs root and CNI plugins"}},
	{backendBare, Option{Label: "Bare (no daemon)", Desc: "drive runc/crun directly; needs root, an OCI runtime, and CNI plugins"}},
	{backendIncus, Option{Label: "Incus", Desc: "incusd 6.3+ with skopeo and umoci on the daemon host"}},
	{backendKubernetes, Option{Label: "Kubernetes", Desc: "deploy into a cluster your kubeconfig reaches (k3s, kind, minikube, remote)"}},
}

var localBackendOptions = func() []Option {
	opts := make([]Option, len(localBackends))
	for i, b := range localBackends {
		opts[i] = b.Option
	}
	return opts
}()

// serverSetupStep is the first question of every scenario: does a server exist
// yet? A "no" is not a dead end — the profile is still worth writing, and the
// answer is what lets the wizard show the right setup guide up front instead of
// burying it after the last question, and skip the work that cannot succeed
// against a server that is not listening (the ingress probe, key enrollment,
// the connection test).
//
// It is ONE step with an inner loop rather than two steps, following
// sshDestinationStep and nameStep. That is load-bearing, not stylistic: a guide
// rendered as its own promptless step would break runSteps' back navigation —
// travelling backwards it would return nil, flip the direction to forward, and
// bounce, so Back could never reach the confirm.
func (w *Wizard) serverSetupStep(a *Answers) step {
	return step{ask: func() error {
		for {
			ready, err := w.ui.Confirm("Is the cornus server already set up?", a.ServerReady)
			if err != nil {
				return err // ErrBack unwinds to the previous step, or to the picker
			}
			a.ServerReady = ready
			if ready {
				a.LocalBackend = backendDocker
				return nil
			}
			// Only the local scenario has to be asked which runtime it will drive;
			// every other scenario's name already says so. The answer shapes the
			// guide only — it never reaches the saved profile.
			if a.Scenario == ScenarioLocal {
				def := 0
				for i, b := range localBackends {
					if b.Key == a.LocalBackend {
						def = i
					}
				}
				idx, serr := w.ui.Select("Which container runtime will this server drive?",
					"sets CORNUS_DEPLOY_BACKEND when you start it", localBackendOptions, def)
				if errors.Is(serr, ErrBack) {
					continue // back from the runtime picker re-asks the confirm
				}
				if serr != nil {
					return serr
				}
				a.LocalBackend = localBackends[idx].Key
			}
			// Same reason as the runtime question above: the guide is printed
			// next, and "install the binary" versus "docker run" is the whole
			// shape of it. Only the docker-host scenario is asked — a
			// containerized cornus needs a docker daemon to run in, which is
			// exactly what the containerd, bare, and incus hosts do not have.
			if a.Scenario == ScenarioSSHDocker {
				yes, cerr := w.ui.Confirm("Will the server run as a container on the remote host?", a.Containerized)
				if errors.Is(cerr, ErrBack) {
					continue // back from here re-asks the confirm, same step
				}
				if cerr != nil {
					return cerr
				}
				a.Containerized = yes
			}
			w.showServerGuide(a)
			return nil
		}
	}}
}

func (w *Wizard) localSteps(a *Answers) []step {
	return []step{
		w.inputStep(Question{Title: "Server URL", Default: "http://127.0.0.1:5000", Validate: validateServerURL},
			func() string { return a.Server }, func(v string) { a.Server = v }),
	}
}

// dockerContainerSteps asks what a containerized server needs beyond a plain
// loopback profile: where its data directory lives ON THE HOST. That path is
// not cosmetic — it is the bind that lets the daemon resolve the mountpoints the
// server hands it, and without it client-local mounts are refused.
func (w *Wizard) dockerContainerSteps(a *Answers) []step {
	return []step{
		w.inputStep(Question{Title: "Server URL", Default: "http://127.0.0.1:5000", Validate: validateServerURL},
			func() string { return a.Server }, func(v string) { a.Server = v }),
		w.inputStep(Question{
			Title:    "Host directory for the server's data dir",
			Help:     "bind-mounted into the container; the daemon must be able to see it",
			Default:  "/srv/cornus",
			Validate: validateAbsPath,
		}, func() string { return a.ContainerDataDir }, func(v string) { a.ContainerDataDir = v }),
	}
}

// sshDestinationOther is the picker entry that falls through to free text. It is
// always offered, because ~/.ssh/config is a convenience, not an inventory: the
// host being configured may simply not be in it.
const sshDestinationOther = "Other (type a destination)"

// sshDestinationStep asks for the SSH destination, offering the Host aliases
// found in ~/.ssh/config as a picker when there are any and falling back to the
// free-text prompt otherwise (no config, unreadable, or only wildcard patterns).
// It is one step, not two: the picker and the free-text prompt are alternative
// renderings of the same question, so Back from the free-text entry returns to
// the picker (like nameStep's overwrite loop) and Back from the picker unwinds
// to the previous question.
func (w *Wizard) sshDestinationStep(a *Answers) step {
	free := w.inputStep(Question{Title: "SSH destination", Help: "an ssh_config Host alias or host[:port]", Example: "remote-devbox", Validate: validateNonEmpty},
		func() string { return a.SSHHost }, func(v string) { a.SSHHost = v })
	return step{ask: func() error {
		var hosts []sshclient.ConfigHost
		if w.SSHHosts != nil {
			hosts = w.SSHHosts()
		}
		if len(hosts) == 0 {
			return free.ask()
		}
		opts := make([]Option, 0, len(hosts)+1)
		// Default to "Other" (the pre-existing behavior) unless a previous answer
		// names one of the aliases: picking one of several aliases for the user
		// would be a guess, and the wrong guess is one keypress from being saved.
		def := len(hosts)
		for i, h := range hosts {
			opts = append(opts, Option{Label: h.Alias, Desc: h.Summary()})
			if h.Alias == a.SSHHost {
				def = i
			}
		}
		opts = append(opts, Option{Label: sshDestinationOther, Desc: "a host[:port] or an alias not in ~/.ssh/config"})
		for {
			idx, err := w.ui.Select("SSH destination", "Host aliases from ~/.ssh/config", opts, def)
			if err != nil {
				return err
			}
			if idx < len(hosts) {
				a.SSHHost = hosts[idx].Alias
				return nil
			}
			if err := free.ask(); err != nil {
				if errors.Is(err, ErrBack) {
					continue // back from the free text returns to the alias list
				}
				return err
			}
			return nil
		}
	}}
}

func (w *Wizard) sshSteps(a *Answers) []step {
	sni := w.inputStep(Question{Title: "TLS server name", Help: "the hostname the server certificate is issued for", Example: "remote-devbox"},
		func() string { return a.ServerName }, func(v string) { a.ServerName = v })
	sni.skip = func() bool { return !a.SSHTLS }
	ca := w.inputStep(Question{Title: "CA certificate path", Help: "empty uses the system trust store", Example: "/etc/ssl/certs/ca.pem", Validate: validateFileExistsOrEmpty},
		func() string { return a.CACert }, func(v string) { a.CACert = v })
	ca.skip = func() bool { return !a.SSHTLS }
	steps := []step{
		w.sshDestinationStep(a),
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
	}
	dataDir := w.inputStep(Question{
		Title:    "Host directory for the server's data dir",
		Help:     "bind-mounted into the container ON THE REMOTE HOST; the daemon there must be able to see it",
		Default:  "/srv/cornus",
		Validate: validateAbsPath,
	}, func() string { return a.ContainerDataDir }, func(v string) { a.ContainerDataDir = v })
	dataDir.skip = func() bool { return !a.Containerized }
	steps = append(steps, dataDir)
	steps = append(steps, w.sshKeyAuthSteps(a, func() string { return a.SSHIdentityFile })...)
	return append(steps, w.registryHostStep(a))
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
	steps = append(steps, w.ingressStep(ctx, a), w.ingressTunnelStep(ctx, a))
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
	steps = append(steps, w.ingressStep(ctx, a), w.ingressTunnelStep(ctx, a))
	return append(steps, w.registryHostStep(a))
}

func (w *Wizard) urlSteps(a *Answers) []step {
	ca := w.inputStep(Question{Title: "CA certificate path", Help: "empty uses the system trust store", Example: "/etc/ssl/certs/ca.pem", Validate: validateFileExistsOrEmpty},
		func() string { return a.CACert }, func(v string) { a.CACert = v })
	ca.skip = func() bool { return !isHTTPS(a.Server) }
	skipVerify := w.confirmStep("Skip TLS certificate verification (testing only)?",
		func() bool { return a.Insecure }, func(v bool) { a.Insecure = v })
	skipVerify.skip = func() bool { return !isHTTPS(a.Server) }
	steps := []step{
		w.inputStep(Question{Title: "Server URL", Example: "https://cornus.example.com:5000", Validate: validateServerURL},
			func() string { return a.Server }, func(v string) { a.Server = v }),
		ca, skipVerify,
	}
	steps = append(steps, w.sshKeyAuthSteps(a, func() string { return "" })...)
	return append(steps, w.registryHostStep(a))
}

// sshKeyAuthSteps is the shared non-Kubernetes authentication choice. SSH-key
// sessions are recommended; static tokens remain available for existing operator
// JWT deployments. Only a key path or agent fingerprint is persisted.
func (w *Wizard) sshKeyAuthSteps(a *Answers, identityDefault func() string) []step {
	choice := 0
	if a.Token != "" {
		choice = 1
	}
	choiceStep := step{ask: func() error {
		idx, err := w.ui.Select("Authentication", "", []Option{
			{Label: "SSH key", Desc: "enroll a public key and mint short-lived sessions"},
			{Label: "Static token", Desc: "store a fixed bearer token / JWT"},
			{Label: "None", Desc: "no bearer credential"},
		}, choice)
		if err != nil {
			return err
		}
		if idx != choice {
			a.Token, a.KeyAuthIdentityFile, a.KeyAuthFingerprint = "", "", ""
		}
		choice = idx
		return nil
	}}
	identity := w.inputStep(Question{
		Title: "SSH key identity file", Help: "empty selects a key from ssh-agent by fingerprint", Example: "~/.ssh/id_ed25519", Validate: validateFileExistsOrEmpty,
	}, func() string {
		if a.KeyAuthIdentityFile != "" {
			return a.KeyAuthIdentityFile
		}
		return identityDefault()
	}, func(v string) { a.KeyAuthIdentityFile = v })
	identity.skip = func() bool { return choice != 0 }
	fingerprint := w.inputStep(Question{Title: "SSH-agent key fingerprint", Help: "SHA256 fingerprint shown by ssh-add -l", Validate: validateNonEmpty},
		func() string { return a.KeyAuthFingerprint }, func(v string) { a.KeyAuthFingerprint = v })
	fingerprint.skip = func() bool { return choice != 0 || a.KeyAuthIdentityFile != "" }
	token := w.inputStep(Question{Title: "Bearer token", Secret: true},
		func() string { return a.Token }, func(v string) { a.Token = v })
	token.skip = func() bool { return choice != 1 }
	return []step{choiceStep, identity, fingerprint, token}
}

func (w *Wizard) maybeEnroll(ctx context.Context, a *Answers) error {
	if a.KeyAuthIdentityFile == "" && a.KeyAuthFingerprint == "" {
		return nil
	}
	// Enrollment is a round trip to a running server, so there is nothing to
	// offer until one exists. Name the command instead of asking a question
	// whose only outcome would be a connection error.
	if !a.ServerReady {
		w.d.Info("enroll the SSH key once the server is up: cornus --context %s auth enroll --code CODE", a.Name)
		return nil
	}
	ok, err := w.ui.Confirm("Enroll this SSH key now?", true)
	if err != nil || !ok {
		return err
	}
	code := ""
	if isSSHScenario(a.Scenario) {
		code, err = w.EnrollmentCode(ctx, a)
		if err != nil {
			w.ui.Note("could not fetch the enrollment code over SSH: %v", err)
		}
	}
	if code == "" {
		code, err = w.ui.Input(Question{Title: "Enrollment code", Help: "run 'cornus auth enrollment-code' on the server host", Secret: true, Validate: validateNonEmpty})
		if err != nil {
			return err
		}
	}
	w.d.Step("enrolling SSH key")
	if err := w.Enroll(ctx, w.configPath, a.Name, strings.TrimSpace(code)); err != nil {
		w.d.Warn("could not enroll the SSH key: %s", err)
		w.d.Info("the profile was saved; retry with: cornus --context %s auth enroll --code CODE", a.Name)
		return nil
	}
	w.d.Success("SSH key enrolled")
	return nil
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
	// Don't offer to verify a server the user says isn't set up yet — it would be
	// a guaranteed failure.
	if !a.ServerReady {
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

// validateAbsPath rejects a relative path: it names a location on the HOST, and
// a relative one would be resolved against whatever directory the operator
// happens to run docker from.
func validateAbsPath(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	if !strings.HasPrefix(s, "/") {
		return fmt.Errorf("must be an absolute path (it names a directory on the host)")
	}
	return nil
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
	if isSSHScenario(a.Scenario) {
		return sanitizeName(sshHostPart(a.SSHHost))
	}
	switch a.Scenario {
	case ScenarioLocal:
		return "local"
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
