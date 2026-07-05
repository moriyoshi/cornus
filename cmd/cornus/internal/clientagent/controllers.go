package clientagent

import (
	"context"
	"fmt"
	"sync"

	"cornus/pkg/api"
	"cornus/pkg/attachsession"
	"cornus/pkg/clientconduit"
	"cornus/pkg/deploywire"
	"cornus/pkg/logging"
)

// The client-side session lifecycle is split into two level-triggered dimension
// controllers, each owning one kind of live resource for a project and reconciled
// toward a desired set by Project.reconcile:
//
//   - mountController owns the client-local 9P deploy-attach sessions (a real
//     per-service spawn: a goroutine holding DeployAttach open under a cancelable
//     context until the mount is torn down or recreated).
//   - exposureController owns the conduit registrations (per-port listeners in
//     port-forward mode, or a name alias in SOCKS5 mode — the mechanism is already
//     unified behind clientconduit.Conduit.Add).
//
// Splitting the dimensions is what makes the alias-registration gap vanish by
// construction: every reconcile drives the live exposure set to equal the desired
// set, so there is no gated imperative Add to forget. Per-dimension fingerprints
// also mean a change that touches only exposure (e.g. toggling port-forwarding)
// no longer tears down a healthy 9P mount.

// mountController owns the live deploy-attach sessions for one project.
type mountController struct {
	attacher Attacher

	mu   sync.Mutex
	live map[string]*mountSession
}

// mountSession is one held deploy-attach session, keyed by service name with the
// fingerprint it was started at. The mechanics live in the shared attachsession
// package (the same primitive the Docker API proxy uses); session.Context() is
// cancelled — tearing the 9P mount down — when the session is stopped/recreated or
// when the attach exits on its own, and the service's exposure parents on that
// context so a dying mount withdraws its exposure with it.
type mountSession struct {
	session     *attachsession.Session
	fingerprint string
}

func newMountController(attacher Attacher) *mountController {
	return &mountController{attacher: attacher, live: map[string]*mountSession{}}
}

// ensure brings the mount session for name to the desired spec, blocking until it
// reports ready (or fails, or opCtx is cancelled). It returns the live session
// context (exposure parents on it) and whether a live change happened this call (a
// fresh start or a recreate). A session already running the identical fingerprint
// is left alone (changed=false). opCtx governs only the readiness wait (a
// foreground Ctrl-C aborts a slow pre-ready attach); the session, once live, is
// held under its own background-derived context so it can outlive the request that
// started it (the agent holds it across requests). Callers must serialize
// ensure/remove for a given project (the Project reconcile lock does this); the
// internal mutex only guards the live map against concurrent readers.
func (m *mountController) ensure(opCtx context.Context, name string, spec api.DeploySpec, fingerprint string) (context.Context, bool, error) {
	m.mu.Lock()
	cur := m.live[name]
	m.mu.Unlock()
	if cur != nil && cur.fingerprint == fingerprint {
		return cur.session.Context(), false, nil
	}
	if cur != nil {
		m.teardown(name, cur)
	}

	// Stream this service's deploy logs and surface its deploy errors as the attach
	// runs; the shared session handles the readiness/hold/self-exit mechanics.
	log := logging.FromContext(opCtx)
	s := attachsession.Open(m.attacher, spec, attachsession.WithEventHook(func(e deploywire.Event) {
		if e.Log != "" {
			fmt.Print(e.Log)
		}
		if e.Err != "" {
			log.ErrorContext(opCtx, "deploy error", "service", name, "error", e.Err)
		}
	}))
	if err := s.WaitReady(opCtx); err != nil {
		// A pre-ready attach failure (e.g. a read-write mount) or a Ctrl-C / caller
		// cancellation: tear the just-opened session down and report it.
		s.Stop()
		return nil, false, err
	}
	ms := &mountSession{session: s, fingerprint: fingerprint}
	m.mu.Lock()
	m.live[name] = ms
	m.mu.Unlock()
	return s.Context(), true, nil
}

// remove tears the named mount session down, reporting whether one was live.
func (m *mountController) remove(name string) bool {
	m.mu.Lock()
	cur := m.live[name]
	m.mu.Unlock()
	if cur == nil {
		return false
	}
	m.teardown(name, cur)
	return true
}

// teardown stops a session (cancelling its context, withdrawing its exposure) and
// waits for its goroutine, then drops it from the live map (only if it is still the
// recorded session, so a concurrent recreate does not lose the newer one).
func (m *mountController) teardown(name string, s *mountSession) {
	s.session.Stop()
	m.mu.Lock()
	if m.live[name] == s {
		delete(m.live, name)
	}
	m.mu.Unlock()
}

// names lists the services with a live mount session.
func (m *mountController) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.live))
	for n := range m.live {
		out = append(out, n)
	}
	return out
}

// close tears down every live mount session.
func (m *mountController) close() {
	for _, n := range m.names() {
		m.remove(n)
	}
}

// exposureController owns the live conduit registrations for one project. Each
// registration is made under a cancelable context the controller owns, derived
// from a parent supplied by the reconcile (the service's mount context for a
// mounted service, so the exposure dies with the mount; a background context for a
// mount-free one). Cancelling that context withdraws the registration through the
// conduit (a port-forward group closes; a SOCKS5 alias unregisters).
type exposureController struct {
	conduit clientconduit.Conduit

	mu   sync.Mutex
	live map[string]*exposureReg
}

type exposureReg struct {
	cancel      context.CancelFunc
	fingerprint string
	forwards    []string
}

func newExposureController(conduit clientconduit.Conduit) *exposureController {
	return &exposureController{conduit: conduit, live: map[string]*exposureReg{}}
}

// ensure brings the exposure for a service to the desired state under parent,
// returning the live forwards and whether a live change happened. A registration
// matching the fingerprint is left alone unless reparent is set (the mount context
// it hung off was replaced by a recreate, so it must be re-added under the new
// parent). The registration always goes through the conduit, so a mounted,
// port-less service still registers its short-name alias.
func (e *exposureController) ensure(parent context.Context, name string, svc Service, fingerprint string, reparent bool) ([]string, bool, error) {
	e.mu.Lock()
	cur := e.live[name]
	e.mu.Unlock()
	if cur != nil && cur.fingerprint == fingerprint && !reparent {
		return cur.forwards, false, nil
	}
	if cur != nil {
		e.remove(name) // withdraw the stale registration before re-adding
	}

	ctx, cancel := context.WithCancel(parent)
	// svc.Name is the compose service name (e.g. "web"); svc.Spec.Name is the
	// deployment name (e.g. "demo-web"). Register the former as a short alias so
	// SOCKS5 callers reach the service by its compose name. Pass ports only when
	// forwarding, so port-forward mode binds no listeners for a no-forward service
	// (Add returns early on an empty port list) and SOCKS5 ignores ports anyway.
	ports := svc.Spec.Ports
	if !svc.ForwardPorts {
		ports = nil
	}
	fwds, err := e.conduit.Add(ctx, svc.Spec.Name, ports, svc.Name)
	if err != nil {
		cancel()
		return nil, false, err
	}
	// Reach the service's declared ingress host(s) through the conduit (native or
	// emulate), when the session opted in and the spec requests one. A failure here is
	// non-fatal: it must not tear down a healthy port/mount exposure. The registration
	// rides ctx, so it is withdrawn with the rest of the exposure.
	if hosts, ierr := e.conduit.AddIngress(ctx, svc.Spec.Name, svc.Spec.Ingress, svc.Spec.Ports); ierr != nil {
		logging.FromContext(ctx).WarnContext(ctx, "ingress via conduit failed", "service", svc.Name, "error", ierr)
	} else {
		for _, h := range hosts {
			logging.FromContext(ctx).InfoContext(ctx, "ingress reachable through the conduit", "service", svc.Name, "host", h)
		}
	}
	reg := &exposureReg{cancel: cancel, fingerprint: fingerprint, forwards: forwardLines(fwds)}
	e.mu.Lock()
	e.live[name] = reg
	e.mu.Unlock()
	return reg.forwards, true, nil
}

// remove withdraws the named exposure, reporting whether one was live.
func (e *exposureController) remove(name string) bool {
	e.mu.Lock()
	cur := e.live[name]
	if cur != nil {
		delete(e.live, name)
	}
	e.mu.Unlock()
	if cur == nil {
		return false
	}
	cur.cancel()
	return true
}

// forwards reports the live local port-forwards for a service.
func (e *exposureController) forwards(name string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if reg := e.live[name]; reg != nil {
		return reg.forwards
	}
	return nil
}

// names lists the services with a live exposure registration.
func (e *exposureController) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.live))
	for n := range e.live {
		out = append(out, n)
	}
	return out
}

// close withdraws every live registration.
func (e *exposureController) close() {
	for _, n := range e.names() {
		e.remove(n)
	}
}
