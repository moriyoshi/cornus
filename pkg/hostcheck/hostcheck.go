// Package hostcheck turns what pkg/hostenv detected into an operator-facing
// verdict: can this cornus actually drive the host runtime it has been pointed
// at, and if not, which knob fixes it?
//
// It exists because the failure it guards against is silent. A containerized
// cornus that hands the runtime a path only IT can see does not error — the
// runtime happily binds a fresh empty directory, the workload starts, and the
// data the operator expected is simply absent. Nothing in a log says why. The
// checks here are the difference between that and a startup message naming the
// missing bind mount.
//
// Severity is calibrated to what actually breaks, not to how unusual the
// configuration looks:
//
//   - StatusFail is reserved for a configuration where deploys would silently
//     do the wrong thing. `cornus serve` refuses to start on one.
//   - StatusWarn marks a capability that is unavailable but whose absence is
//     reported at the point of use (a client-local mount that will be rejected
//     rather than silently emptied). Serving continues.
//
// Deliberately NOT a failure: being containerized. A cornus co-located with its
// runtime needs nothing from this package, and one whose paths diverge is a
// supported topology as soon as the binds are right.
package hostcheck

import (
	"fmt"
	"strings"

	"cornus/pkg/deploywire"
	"cornus/pkg/hostenv"
)

// Status is a check's verdict.
type Status string

const (
	// StatusOK means the check passed, or did not apply to this configuration.
	StatusOK Status = "ok"
	// StatusWarn means a capability is unavailable. Serving continues, and the
	// capability reports its own absence when something asks for it.
	StatusWarn Status = "warn"
	// StatusFail means deploys would silently misbehave. Serving must not start.
	StatusFail Status = "fail"
)

// Check names, stable enough for an operator to search for.
const (
	CheckColocation     = "runtime-colocation"
	CheckDataDir        = "data-dir-host-visible"
	CheckPropagation    = "mount-propagation"
	CheckClientMounts   = "client-local-mounts"
	CheckHostNetwork    = "host-network"
	CheckSelfInspection = "self-inspection"
)

// Check is one verdict with the remediation for it.
type Check struct {
	Name   string
	Status Status
	// Detail states what was found.
	Detail string
	// Hint states what to change. Empty for a passing check.
	Hint string
}

// Deploy backend names, as CORNUS_DEPLOY_BACKEND spells them.
const (
	backendDockerhost = "dockerhost"
	backendContainerd = "containerd"
	backendBare       = "bare"
	backendIncus      = "incus"
	backendKubernetes = "kubernetes"
)

// Input is what Run needs to reach a verdict.
type Input struct {
	// Backend is the raw CORNUS_DEPLOY_BACKEND value; empty means the
	// dockerhost default, and "k8s" is accepted for "kubernetes", exactly as
	// the server's own backend selector does.
	Backend string
	// DataDir and MountsDir are the server's config.Config paths.
	DataDir   string
	MountsDir string
	// Env and Mapper come from hostenv.Detect.
	Env    hostenv.Env
	Mapper hostenv.Mapper

	// canMountLocal is a test seam over deploywire.CanMountLocal.
	canMountLocal func() error
}

// Result is the full verdict.
type Result struct {
	Env    hostenv.Env
	Checks []Check
}

// Failed reports whether any check would let deploys silently misbehave, i.e.
// whether `cornus serve` must refuse to start.
func (r Result) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// Problems returns the checks an operator needs to act on, worst first.
func (r Result) Problems() []Check {
	var fails, warns []Check
	for _, c := range r.Checks {
		switch c.Status {
		case StatusFail:
			fails = append(fails, c)
		case StatusWarn:
			warns = append(warns, c)
		}
	}
	return append(fails, warns...)
}

// Capability reports whether the named check found its capability usable.
//
// A check that did not run means nothing here constrains it — the backend
// realizes that capability some other way, or the question does not arise — so
// an absent check reads as capable rather than as unknown.
func (r Result) Capability(name string) bool {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Status == StatusOK
		}
	}
	return true
}

// Summary is the one-line startup log: where cornus runs relative to its
// runtime, and whether path translation is in effect.
func (r Result) Summary() string {
	where := "cornus runs on the host"
	if r.Env.InContainer {
		where = "cornus runs in a container"
		if id := r.Env.SelfID; id != "" {
			where += " (" + shortID(id) + ")"
		}
		if r.Env.Runtime != "" && r.Env.Runtime != hostenv.RuntimeUnknown {
			where += " on a " + r.Env.Runtime + " host"
		}
	}
	if !r.Env.Translating {
		// Not translating covers two shapes that behave identically: cornus on
		// the host, and cornus containerized beside the runtime it drives.
		if r.Env.InContainer {
			return where + ", co-located with its runtime; paths need no translation"
		}
		return where + "; runtime paths need no translation"
	}
	return where + "; translating its paths for the runtime"
}

// Run evaluates every check that applies to this configuration.
func (in Input) Run() Result {
	if in.canMountLocal == nil {
		in.canMountLocal = deploywire.CanMountLocal
	}
	backend := normalizeBackend(in.Backend)
	res := Result{Env: in.Env}
	add := func(c Check) { res.Checks = append(res.Checks, c) }

	add(Check{Name: CheckColocation, Status: StatusOK, Detail: res.Summary()})

	// A cornus that shares its runtime's mount namespace — on the host,
	// containerized beside the daemon, or driving a runtime that IS its own
	// child — hands over paths that already mean the same thing on both sides.
	// Only genuine divergence needs the path checks.
	if in.Env.InContainer && !in.Env.Translating && !sharesMountNamespace(backend) {
		add(in.unverifiedPathsCheck())
	}

	if in.Env.Translating && !sharesMountNamespace(backend) {
		add(in.dataDirCheck(backend))
		if in.Env.Err != nil {
			add(Check{
				Name: CheckSelfInspection, Status: StatusOK,
				Detail: "could not ask the runtime about this container: " + in.Env.Err.Error(),
				Hint:   "harmless while " + hostenv.HostPathMapEnv + " covers every path cornus hands the runtime",
			})
		}
		if usesHostMountFastPath(backend) {
			add(in.propagationCheck())
		}
		if backend == backendContainerd {
			add(in.hostNetworkCheck())
		}
	}

	if usesHostMountFastPath(backend) {
		add(in.clientMountCheck())
	}
	return res
}

// unverifiedPathsCheck covers the case detection cannot resolve: cornus is in a
// container, but nothing established whether its paths and the runtime's
// diverge, so they are assumed to match.
//
// Two very different situations produce this, and they are genuinely
// indistinguishable from inside:
//
//   - the runtime is inside this same container (Docker-in-Docker), where the
//     assumption is right and everything works;
//   - the runtime is the host's, reached through a bind-mounted socket, where
//     the assumption is wrong and every path cornus hands over lands somewhere
//     else. The containerd backend cannot be asked which container it is
//     running in, so it always lands here unless the operator says.
//
// A warning rather than a failure, precisely because the first case is
// legitimate and common. What it buys is that the second case is no longer
// completely silent: an operator who reads it knows which knob settles it.
func (in Input) unverifiedPathsCheck() Check {
	detail := "cornus is containerized and the runtime does not report it, so its paths are assumed to match the runtime's"
	if in.Env.Err != nil {
		detail += " (" + in.Env.Err.Error() + ")"
	}
	return Check{
		Name: CheckSelfInspection, Status: StatusWarn,
		Detail: detail,
		Hint: "correct if the runtime runs inside this container; if it is the HOST's runtime, declare the mapping with " +
			hostenv.HostPathMapEnv + "=<container-path>=<host-path> or deploys will silently use the wrong paths",
	}
}

// dataDirCheck verifies the runtime can see the server's data directory.
//
// The severity splits on how much of the backend depends on it. containerd
// hands the runtime a path under DataDir for EVERY deploy — volume backings,
// the managed /etc/hosts, the log file, the log-shim binary — so an unmappable
// DataDir means every workload comes up with empty volumes and no hosts file.
// That is the silent corruption this package exists to prevent, so it is fatal.
// On dockerhost only client-local mounts are affected (volumes are
// daemon-managed, with no server-side path at all), and that path reports its
// own failure at deploy time, so plain deploys are left working.
func (in Input) dataDirCheck(backend string) Check {
	host, ok := in.Mapper.ToHost(in.DataDir)
	if ok {
		return Check{
			Name: CheckDataDir, Status: StatusOK,
			Detail: fmt.Sprintf("data dir %s is %s on the host", in.DataDir, host),
		}
	}
	status, impact := StatusWarn, "client-local mounts will be rejected"
	if backend == backendContainerd {
		status = StatusFail
		impact = "every deploy would get empty volumes and no managed /etc/hosts"
	}
	return Check{
		Name: CheckDataDir, Status: status,
		Detail: fmt.Sprintf("the %s runtime cannot see cornus's data dir %s: %s", backend, in.DataDir, impact),
		Hint: fmt.Sprintf("bind-mount it from the host (-v <host-path>:%s) or declare the mapping with %s=%s=<host-path>",
			in.DataDir, hostenv.HostPathMapEnv, in.DataDir),
	}
}

// propagationCheck verifies that a mount cornus makes under MountsDir reaches
// the host. Without shared propagation the kernel 9P mount succeeds, stays
// invisible outside this container, and the runtime binds the still-empty
// directory underneath it.
func (in Input) propagationCheck() Check {
	switch got := in.Mapper.Propagation(in.MountsDir); got {
	case hostenv.PropagationShared:
		return Check{
			Name: CheckPropagation, Status: StatusOK,
			Detail: fmt.Sprintf("%s propagates mounts to the host", in.MountsDir),
		}
	case hostenv.PropagationUnknown:
		return Check{
			Name: CheckPropagation, Status: StatusWarn,
			Detail: fmt.Sprintf("cannot determine the mount propagation of %s", in.MountsDir),
			Hint:   "if client-local mounts come up empty, re-run the container with :rshared on the data-dir bind",
		}
	default:
		return Check{
			Name: CheckPropagation, Status: StatusWarn,
			Detail: fmt.Sprintf("%s has %s propagation, so a mount cornus makes there stays invisible to the runtime", in.MountsDir, got),
			Hint:   "add :rshared to the data-dir bind mount (-v <host-path>:" + in.DataDir + ":rshared)",
		}
	}
}

// clientMountCheck reports whether this process can perform the kernel 9P mount
// the co-located client-local-mount path needs at all — CAP_SYS_ADMIN and the
// 9p module, neither of which is specific to being containerized.
func (in Input) clientMountCheck() Check {
	if err := in.canMountLocal(); err != nil {
		return Check{
			Name: CheckClientMounts, Status: StatusWarn,
			Detail: "client-local mounts unavailable: " + err.Error(),
			Hint:   "run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded",
		}
	}
	return Check{Name: CheckClientMounts, Status: StatusOK, Detail: "client-local mounts available"}
}

// hostNetworkCheck warns when the containerd backend runs without the host's
// network namespace. Its CNI plugins attach veths to a bridge in whatever netns
// cornus is in, so in a container of its own the workload network is built
// inside that container instead of on the host.
//
// A warning, not a failure: making it fatal is the deferred half of this work,
// and doing so now would refuse to start for a configuration that some
// deploys — anything not needing to be reached from the host — still survive.
func (in Input) hostNetworkCheck() Check {
	switch {
	case !in.Env.HostNetworkKnown:
		return Check{
			Name: CheckHostNetwork, Status: StatusWarn,
			Detail: "cannot determine whether this container shares the host's network namespace",
			Hint:   "the containerd backend's CNI plumbing needs host networking; run the container with --network host",
		}
	case !in.Env.HostNetwork:
		return Check{
			Name: CheckHostNetwork, Status: StatusWarn,
			Detail: "this container has its own network namespace, so CNI will build workload networks inside it rather than on the host",
			Hint:   "run the container with --network host",
		}
	default:
		return Check{Name: CheckHostNetwork, Status: StatusOK, Detail: "sharing the host's network namespace"}
	}
}

// usesHostMountFastPath reports whether the backend realizes client-local
// mounts by having the SERVER kernel-9P-mount them and binding the mountpoint —
// the path in pkg/server's applyWithHostMounts. containerd and incus do not
// (containerd has no co-located host-mount fallback at all), so the propagation
// of cornus's own mounts dir is irrelevant to them.
func usesHostMountFastPath(backend string) bool {
	return backend == backendDockerhost || backend == backendBare
}

// sharesMountNamespace reports whether the backend's runtime resolves paths in
// THIS process's mount namespace however cornus is deployed, making translation
// unnecessary — and a check for it actively misleading.
//
//   - bare is daemonless: it execs the OCI runtime as cornus's own child, which
//     therefore inherits cornus's mount namespace. A containerized bare server
//     runs its workloads inside that same container, so its data dir needs no
//     host visibility at all.
//   - kubernetes never hands the server's paths to anything; every client-side
//     attachment is realized by the pod caretaker.
func sharesMountNamespace(backend string) bool {
	return backend == backendBare || backend == backendKubernetes
}

// BackendName canonicalizes a raw CORNUS_DEPLOY_BACKEND value, so a caller's
// error message names the backend the same way these checks reasoned about it.
func BackendName(raw string) string { return normalizeBackend(raw) }

// normalizeBackend maps a raw CORNUS_DEPLOY_BACKEND value to a canonical name,
// mirroring the server's own selector: empty is the dockerhost default and
// "k8s" is an alias.
func normalizeBackend(raw string) string {
	switch strings.TrimSpace(raw) {
	case "":
		return backendDockerhost
	case "k8s", backendKubernetes:
		return backendKubernetes
	case backendContainerd, backendBare, backendIncus, backendDockerhost:
		return strings.TrimSpace(raw)
	default:
		// An unknown value is the server's problem to reject; treat it as the
		// default here rather than silently skipping every check.
		return backendDockerhost
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
