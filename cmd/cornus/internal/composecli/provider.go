package composecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/compose"
)

// This file implements the compose-spec `provider:` feature: a service whose
// lifecycle is delegated to an external provider plugin instead of being built
// and deployed as a container. cornus locates the plugin, invokes it for the
// up/down lifecycle, streams its progress, and injects the environment
// variables it reports into services that depend_on it.
//
// Protocol (compose-spec provider plugins):
//
//   - Discovery: for `provider.type: awesomecloud`, run the Docker CLI plugin
//     `docker-awesomecloud` if on PATH, else a binary `awesomecloud`.
//   - Invocation: `<binary> compose --project-name=<project> up|down [--k=v ...] "<service>"`.
//   - Stdout: newline-delimited JSON `{"type":"...","message":"..."}`:
//     info  -> progress line; debug -> verbose only; error -> failure;
//     setenv (KEY=VALUE) -> exposed to dependents as <SERVICE>_KEY (prefixed);
//     rawsetenv (KEY=VALUE) -> exposed as-is.
//   - A non-zero exit (or any `error` message) fails the service.

// providerCommand is a provider plugin lifecycle verb.
type providerCommand string

const (
	providerUp   providerCommand = "up"
	providerDown providerCommand = "down"
	providerStop providerCommand = "stop"
)

// providerLifecycle maps a `cornus compose` lifecycle action to the provider
// plugin command(s) it runs for a provider service. `stop` maps to the plugin's
// own `stop` verb; `start` re-runs `up` (idempotent bring-up, per the spec
// providers must be idempotent); `restart` is stop-then-up. `down` is handled
// separately by DownCmd.
var providerLifecycle = map[string][]providerCommand{
	"stop":    {providerStop},
	"start":   {providerUp},
	"restart": {providerStop, providerUp},
}

// providerMessage is one line of a provider plugin's stdout JSON protocol.
type providerMessage struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// providerExecFunc builds the *exec.Cmd for a provider invocation. It is a field
// on providerRunner so tests can substitute a fake plugin without a real binary.
type providerExecFunc func(ctx context.Context, bin string, args []string) *exec.Cmd

// providerRunner drives provider plugin invocations. The zero value uses the
// real os/exec-backed command builder.
type providerRunner struct {
	exec providerExecFunc
}

// realProviderExec is the default command builder: a plain os/exec invocation.
func realProviderExec(ctx context.Context, bin string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}

// resolveProviderBinary locates the plugin binary for a provider type, honouring
// the Docker CLI plugin (`docker-<type>`) then a bare `<type>` on PATH.
func resolveProviderBinary(typ string) (string, error) {
	if p, err := exec.LookPath("docker-" + typ); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath(typ); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("provider plugin for type %q not found (looked for docker-%s and %s on PATH)", typ, typ, typ)
}

// run invokes the provider plugin for one service and lifecycle command. On the
// up command it returns the environment variables the plugin reported (already
// prefixed per the protocol) for injection into dependents; down returns none.
// A plugin `error` message or a non-zero exit is surfaced as an error.
func (r providerRunner) run(ctx context.Context, plan compose.ProviderPlan, projectName, service string, cmd providerCommand, d *cliout.Driver) (map[string]string, error) {
	execFn := r.exec
	if execFn == nil {
		execFn = realProviderExec
	}
	bin, err := resolveProviderBinary(plan.Type)
	if err != nil {
		return nil, err
	}
	args := []string{"compose", "--project-name=" + projectName, string(cmd)}
	args = append(args, plan.Flags...)
	args = append(args, service)

	c := execFn(ctx, bin, args)
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", service, err)
	}
	var stderr strings.Builder
	c.Stderr = &stderr
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("provider %q: start %s: %w", service, plan.Type, err)
	}

	// info/debug lines surface as this service's progress. debug is verbose-only,
	// but cornus has no compose --verbose yet, so route both through Info-style
	// per-service events and let the driver's mode decide visibility.
	onInfo := func(msg string) {
		if d != nil {
			_ = d.Event(svcEvent(service, "provider", msg))
		}
	}
	env, provErr := parseProviderStream(stdout, service, onInfo)

	waitErr := c.Wait()
	// A plugin-reported error wins over the generic exit status: it carries the
	// actionable message. Otherwise a non-zero exit fails the service, annotated
	// with any stderr the plugin emitted.
	if provErr != nil {
		return nil, fmt.Errorf("provider %q (%s): %s", service, plan.Type, provErr)
	}
	if waitErr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("provider %q (%s): %w: %s", service, plan.Type, waitErr, s)
		}
		return nil, fmt.Errorf("provider %q (%s): %w", service, plan.Type, waitErr)
	}
	return env, nil
}

// parseProviderStream reads a provider plugin's newline-delimited JSON stdout,
// forwarding info/debug messages to onInfo and accumulating setenv/rawsetenv
// values into the returned environment map (setenv keys are prefixed with the
// service name per the protocol; rawsetenv keys pass through unchanged). It
// returns a non-nil error for the first `error` message. Lines that are not
// valid protocol JSON are forwarded to onInfo verbatim, so a plugin that prints
// plain text still shows progress rather than failing the run.
func parseProviderStream(stdout io.Reader, service string, onInfo func(string)) (map[string]string, error) {
	env := map[string]string{}
	prefix := providerEnvPrefix(service)
	sc := bufio.NewScanner(stdout)
	// Provider messages can carry long values (URLs, tokens); raise the line cap
	// well above bufio's 64 KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var firstErr error
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg providerMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Type == "" {
			if onInfo != nil {
				onInfo(line)
			}
			continue
		}
		switch msg.Type {
		case "info", "debug":
			if onInfo != nil {
				onInfo(msg.Message)
			}
		case "error":
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", msg.Message)
			}
		case "setenv":
			if k, v, ok := strings.Cut(msg.Message, "="); ok {
				env[prefix+k] = v
			}
		case "rawsetenv":
			if k, v, ok := strings.Cut(msg.Message, "="); ok {
				env[k] = v
			}
		default:
			// Unknown message type: forward its text as progress rather than drop it.
			if onInfo != nil && msg.Message != "" {
				onInfo(msg.Message)
			}
		}
	}
	if err := sc.Err(); err != nil && firstErr == nil {
		firstErr = err
	}
	return env, firstErr
}

// providerEnvPrefix is the env-var prefix a provider service contributes to its
// dependents (compose-spec: "<SERVICE_NAME>_<VARIABLE>"). The service name is
// upper-cased and every character outside [A-Z0-9_] becomes '_', so a service
// "my-db" contributes MY_DB_<VAR>.
func providerEnvPrefix(service string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(service) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteByte('_')
	return b.String()
}

// -- runtime provider bookkeeping ------------------------------------------------

// providerState is the per-`up` mutable state for provider services, held behind
// a pointer on runtime so the struct stays copyable without a by-value lock.
// env records, per provider service name, the vars its plugin reported at `up`
// time; ready holds a channel per selected provider service, closed when its
// plugin `up` completes so a dependent can gate on it. mu guards both maps
// against the concurrent per-service up goroutines.
type providerState struct {
	mu    sync.Mutex
	env   map[string]map[string]string
	ready map[string]chan struct{}
}

// initProviderState prepares the per-`up` provider bookkeeping: a fresh env map
// and one readiness channel per selected provider service (closed when its
// plugin `up` completes). Call once at the start of each up path, before the
// per-service goroutines launch.
func (rt *runtime) initProviderState(names []string) {
	st := &providerState{
		env:   map[string]map[string]string{},
		ready: map[string]chan struct{}{},
	}
	for _, n := range names {
		if rt.plans[n].Provider != nil {
			st.ready[n] = make(chan struct{})
		}
	}
	rt.providers = st
}

// runProviderService runs a provider service's plugin `up`, records the env vars
// it reported, and signals readiness so dependents waiting on it can proceed.
// Used by both up paths in place of build+deploy for a provider service.
func (rt *runtime) runProviderService(ctx context.Context, name string, d *cliout.Driver) error {
	plan := rt.plans[name]
	env, err := rt.providerRunner.run(ctx, *plan.Provider, rt.projectName, name, providerUp, d)
	if err != nil {
		return err
	}
	if st := rt.providers; st != nil {
		st.mu.Lock()
		st.env[name] = env
		ch := st.ready[name]
		st.mu.Unlock()
		if ch != nil {
			close(ch)
		}
	}
	d.Event(svcEvent(name, "provider", "up"))
	return nil
}

// runProviderReload re-runs a provider service's plugin `up` on a foreground
// `--watch` reload and refreshes its recorded env, so an edited provider config
// takes effect and dependents re-deployed by the reload see current env. Unlike
// runProviderService it signals no readiness channel: the reload deploys
// dependents sequentially after every provider has been re-run (see
// reloadAndReconcile), so no cross-goroutine gating is needed. The plugin is
// required to be idempotent, matching the detached path, which re-execs the CLI
// (re-running provider `up`) on every reload.
func (rt *runtime) runProviderReload(ctx context.Context, name string, d *cliout.Driver) error {
	plan := rt.plans[name]
	env, err := rt.providerRunner.run(ctx, *plan.Provider, rt.projectName, name, providerUp, d)
	if err != nil {
		return fmt.Errorf("provider %s: %w", name, err)
	}
	if st := rt.providers; st != nil {
		st.mu.Lock()
		st.env[name] = env
		st.mu.Unlock()
	}
	d.Event(svcEvent(name, "provider", "reloaded"))
	return nil
}

// providerReadyChan returns the readiness channel for a provider service (closed
// once its plugin `up` completes), or nil when the service is not a selected
// provider or providers were never initialized.
func (rt *runtime) providerReadyChan(name string) chan struct{} {
	st := rt.providers
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ready[name]
}

// injectProviderEnv augments spec with the environment variables contributed by
// every provider service that `name` depends_on (compose-spec: a dependent
// receives its providers' reported vars). The service's own environment wins on
// a key clash. spec.Env is cloned before mutation so the shared plan map is left
// untouched. A no-op when providers were never initialized.
func (rt *runtime) injectProviderEnv(name string, spec *api.DeploySpec) {
	st := rt.providers
	if st == nil {
		return
	}
	svc, ok := rt.project.Services()[name]
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, dep := range svc.DependsOn {
		env := st.env[dep.Service]
		if len(env) == 0 {
			continue
		}
		merged := make(map[string]string, len(spec.Env)+len(env))
		for k, v := range env {
			merged[k] = v
		}
		for k, v := range spec.Env { // service's own env wins
			merged[k] = v
		}
		spec.Env = merged
	}
}
