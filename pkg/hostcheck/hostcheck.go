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
	CheckNetns          = "netns-host-visible"
	CheckRouting        = "workload-routing"
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
	backendPodman     = "podman"
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
	// NetnsDir is where the host-run backends pin an instance's network
	// namespace (containerdhost.NetnsDir). Supplied rather than imported because
	// the constant lives in pkg/deploy/internal/hostrun, which only packages under
	// pkg/deploy may import. Empty skips the netns check.
	NetnsDir string
	// Env and Mapper come from hostenv.Detect.
	Env    hostenv.Env
	Mapper hostenv.Mapper
	// RemoteMode reports that this backend runs in always-on remote-companion
	// mode (CORNUS_*_REMOTE), which changes how a workload is reached and so
	// changes which topology warnings are worth emitting. Supplied by pkg/server
	// from the same predicate its backend factory uses, so the check and the
	// factory cannot disagree about whether remote mode is on.
	RemoteMode bool

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
			where += " on " + article(r.Env.Runtime) + " " + r.Env.Runtime + " host"
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

// article picks "a" or "an" for a runtime name. Only "incus" needs the second
// today, but hard-coding that would put a second place to edit next to the runtime
// constants; a vowel test is the same length and cannot go stale.
func article(word string) string {
	if word == "" {
		return "a"
	}
	if strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
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

	// Where the workload NETWORK gets built is a separate question from where
	// cornus's paths resolve, and it is asked of a different set of backends, so
	// it hangs off nothing but "is cornus containerized".
	//
	// It used to live inside the Translating branch below, which quietly made it
	// unreachable in the configuration the guide recommends: bind the data dir at
	// the same path on both sides and no CORNUS_HOST_PATH_MAP is needed, so
	// nothing is translating, so the netns warning never fired. The check needs no
	// path knowledge at all.
	if in.Env.InContainer && usesCNINetworking(backend) {
		add(in.hostNetworkCheck())
	}

	// The netns pin is the other half of the same story, and it is asked only of
	// containerd: bare's OCI runtime is cornus's own child and shares its mount
	// namespace, so it reopens the pin at the path cornus created it at.
	//
	// Gated on Translating, unlike the netns question above, and the asymmetry is
	// the point. "Which netns is the bridge built in" has nothing to do with paths,
	// which is why gating THAT on Translating was a bug. "Can the runtime resolve
	// this PATH" is entirely a path question, and Translating is exactly "we know
	// how our paths correspond to the runtime's". Without it we cannot tell a
	// co-located containerd (same mount namespace, pin trivially visible) from the
	// host's (different one, pin invisible) — hostenv says so outright — so firing
	// here would warn every co-located server about a bind it does not need. It did:
	// the containerd E2E leg emitted this on all 11 server starts, advising
	// `rshared` on a directory that already resolved. The case this skips, "in a
	// container and we know nothing about your paths", is what unverifiedPathsCheck
	// is for.
	if in.Env.InContainer && in.Env.Translating && backend == backendContainerd && in.NetnsDir != "" {
		add(in.netnsCheck())
	}

	// incus asks the routing question in its own terms — not CNI, not paths — and
	// nothing here used to ask it at all: a containerized incus operator's only
	// preflight output was a warning about PATHS, on a backend where cornus hands
	// incusd no path to translate. So the remedy it named changed nothing, and the
	// consequence that does bite went unmentioned until a dial failed.
	if backend == backendIncus && in.Env.InContainer {
		add(in.incusRoutingCheck())
	}

	// A cornus that shares its runtime's mount namespace — on the host,
	// containerized beside the daemon, or driving a runtime that IS its own
	// child — hands over paths that already mean the same thing on both sides.
	// Only genuine divergence needs the path checks.
	// ...and only for a backend that actually hands the runtime a cornus-built
	// path. The warning's whole subject is "your paths may not mean what you think
	// over there", so on a backend that gives the runtime no path of cornus's it
	// warns about an impossible hazard and names a remedy (CORNUS_HOST_PATH_MAP)
	// that changes nothing — which was the ENTIRE preflight output a containerized
	// incus operator used to get, while the consequence that does bite (no route to
	// the instance) went unmentioned. See handsDataDirToRuntime.
	if in.Env.InContainer && !in.Env.Translating && !sharesMountNamespace(backend) && handsDataDirToRuntime(backend) {
		add(in.unverifiedPathsCheck())
	}

	if in.Env.Translating && !sharesMountNamespace(backend) {
		if handsDataDirToRuntime(backend) {
			add(in.dataDirCheck(backend))
		}
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

// hostNetworkCheck warns when a CNI-based host backend runs without the host's
// network namespace. Its CNI plugins attach veths to a bridge in whatever netns
// cornus is in, so in a container of its own the workload network is built
// inside that container instead of on the host: the bridge, the portmap DNAT for
// every published port, and the instance addresses all live there.
//
// What that costs is a published port: `ports:` is realized by portmap in this
// container, so the host sees nothing on it unless the cornus container itself
// published it. Reaching a workload FROM the server still works — cornus and the
// instance end up in the same netns tree by construction — so this is about host
// visibility, not about cornus's own routing.
//
// The severity splits on evidence, not on severity of consequence:
//
//   - PROVEN isolated is fatal. A published port that silently exists nowhere the
//     host can reach is the same class of silent wrongness as a bind mount that
//     resolves to an empty directory, which is what this package exists to stop.
//     The operator who wants it anyway says so with CORNUS_HOST_NETWORK=0.
//   - UNPROVEN stays a warning. This branch is what a backend with no daemon to
//     ask (bare), and any topology self-inspection cannot settle, lands on;
//     refusing to start there would reject configurations that demonstrably work,
//     including the all-in-one E2E runner where cornus, the runtime and the
//     workloads share one netns.
//
// Before containerd self-inspection existed, the fatal branch was unreachable
// outside tests — HostNetworkKnown was set only by Docker self-inspection — which
// is why making it fatal had to wait for that half.
func (in Input) hostNetworkCheck() Check {
	switch {
	case !in.Env.HostNetworkKnown:
		return Check{
			Name: CheckHostNetwork, Status: StatusWarn,
			Detail: "cannot determine whether this container shares the host's network namespace",
			Hint: "the CNI plumbing of the containerd/bare backends needs host networking; run the container with --network host, " +
				"or set " + hostenv.HostNetworkEnv + "=1 to state that it already has it",
		}
	case !in.Env.HostNetwork && in.Env.HostNetworkDeclared:
		// The operator said so, so this is acknowledged rather than discovered.
		return Check{
			Name: CheckHostNetwork, Status: StatusWarn,
			Detail: hostenv.HostNetworkEnv + " declares this container has its own network namespace: published ports are DNAT'd here, not on the host",
			Hint:   "port-forward and tunnels still work; drop " + hostenv.HostNetworkEnv + " and run with --network host to publish on the host",
		}
	case !in.Env.HostNetwork:
		return Check{
			Name: CheckHostNetwork, Status: StatusFail,
			Detail: "this container has its own network namespace, so CNI builds workload networks inside it rather than on the host: a published port is DNAT'd here, not on the host",
			Hint: "run the container with --network host, or set " + hostenv.HostNetworkEnv +
				"=0 to accept that and serve anyway (published ports then reach only this container)",
		}
	default:
		return Check{Name: CheckHostNetwork, Status: StatusOK, Detail: "sharing the host's network namespace"}
	}
}

// netnsCheck verifies that containerd can resolve the network-namespace pins
// cornus creates for it.
//
// Fatal, and it is worth being precise about why this one earns that when the
// host-network check next to it does not. Cornus pins each instance's netns under
// NetnsDir — a path in /run, which is a container-private tmpfs — and hands it to
// containerd, whose shim reopens it in the HOST's mount namespace. Measured: a
// file created there inside a `ctr`-run cornus container is not visible from
// containerd's mount namespace at all. So without the bind, EVERY deploy fails —
// and it fails late, after the image pull and after the previous healthy
// deployment has already been torn down. Saying so at startup costs an operator
// one line; not saying it costs them a running deployment.
//
// Propagation is a separate, softer verdict: with the directory bound but not
// shared, the pins cornus creates inside stay invisible outside, so it behaves
// like the missing bind — but the mount table cannot always tell us (the
// identity mapper reports what it can see, and "unknown" is common), so a wrong
// fatal here would refuse to start a server that works.
func (in Input) netnsCheck() Check {
	host, ok := in.Mapper.ToHost(in.NetnsDir)
	if !ok {
		return Check{
			Name: CheckNetns, Status: StatusFail,
			Detail: fmt.Sprintf("containerd cannot see the netns pins cornus creates in %s, so no workload could start", in.NetnsDir),
			Hint: fmt.Sprintf("bind-mount it from the host (-v %s:%s:rshared) or declare the mapping with %s=%s=<host-path>",
				in.NetnsDir, in.NetnsDir, hostenv.HostPathMapEnv, in.NetnsDir),
		}
	}
	if p := in.Mapper.Propagation(in.NetnsDir); p == hostenv.PropagationPrivate {
		return Check{
			Name: CheckNetns, Status: StatusWarn,
			Detail: fmt.Sprintf("%s is %s on this container, so netns pins cornus creates there may not reach containerd", in.NetnsDir, p),
			Hint:   fmt.Sprintf("bind it shared (-v <host-path>:%s:rshared)", in.NetnsDir),
		}
	}
	return Check{
		Name: CheckNetns, Status: StatusOK,
		Detail: fmt.Sprintf("netns pins in %s are %s on the host", in.NetnsDir, host),
	}
}

// incusRoutingCheck reports whether a containerized incus server can reach the
// instances it deploys.
//
// Incus instances are networked by incusd onto its own bridge, in the HOST's network
// namespace. The daemon host can always route there; a cornus in a container beside
// it cannot, and cannot acquire a route either — the dockerhost self-attach has no
// analogue, because a cornus container is not an incus instance.
//
// Three configurations answer it, and the first two are why this is not simply a
// warning for every containerized incus server:
//
//   - cornus IS an instance on this daemon: it is a peer of the workloads on that
//     same bridge, so it dials them directly. Nothing to warn about.
//   - remote mode: every replica gets a companion instance that relays, so the
//     server never dials the instance itself.
//   - otherwise: port-forward, tunnels and caretaker dial-backs cannot reach the
//     workload.
//
// Warn, not fail, and the boundary is worth stating: deploys themselves are
// unaffected, and a `ports:` mapping still publishes on the host because incus
// realizes it with a proxy device that listens in the DAEMON's netns, not cornus's.
// What is lost is only the server dialing a workload — real, but not silent
// (ForwardPort names this exact cause) and not corrupting.
func (in Input) incusRoutingCheck() Check {
	switch {
	case in.Env.Runtime == hostenv.RuntimeIncus && in.Env.SelfID != "":
		return Check{
			Name: CheckRouting, Status: StatusOK,
			Detail: fmt.Sprintf("this server is the incus instance %q, so it shares incusd's bridge with its workloads", in.Env.SelfID),
		}
	case in.RemoteMode:
		return Check{
			Name: CheckRouting, Status: StatusOK,
			Detail: "remote mode: workloads are reached through a per-instance companion",
		}
	default:
		// Worded for BOTH readings, because the one that decides it cannot be
		// determined here. "Containerized beside incusd" does not imply "no route":
		// if incusd runs in the SAME container (the all-in-one E2E runner does
		// exactly this), its bridge is in the netns cornus already occupies and
		// every dial works. Measured: the earlier phrasing asserted "cannot reach a
		// workload" on all 9 server starts of the incus leg, in the same run where
		// deploy-portforward.star reached one.
		//
		// Nothing available here separates the two: hostenv can prove host
		// networking only from a Docker self-inspection, and the incus inspector
		// reports no network mode (an instance HAS its own netns — it is a peer on
		// the bridge, not a sharer of the host's). So this states the condition and
		// the remedies and leaves the verdict to the dial, which names the same
		// cause when it genuinely fails (see incushost.unreachableHint). The
		// technique is unverifiedPathsCheck's: when the ambiguity is unresolvable,
		// word the message so it is true either way rather than picking one.
		return Check{
			Name: CheckRouting, Status: StatusWarn,
			Detail: "this server is containerized beside incusd: if its container has a network namespace of its own, it has no route to an instance's address on the incus bridge and port-forward, tunnels and caretaker dial-backs cannot reach a workload",
			Hint: "give the server container the host's network namespace (--network host), run it as an incus instance on this daemon, " +
				"or set CORNUS_INCUS_REMOTE=1 with CORNUS_AGENT_IMAGE and CORNUS_ADVERTISE_URL to reach instances through a per-instance companion; " +
				"port-forward names this cause if it cannot reach an instance",
		}
	}
}

// usesCNINetworking reports whether the backend realizes workload networking
// with the CNI plugins cornus itself forks — pkg/deploy/internal/hostrun's
// CNIManager, whose bridge, IPAM and portmap rules all land in the network
// namespace of THIS process.
//
// Both host-run backends share that manager (hostrun's own doc says so:
// "Shared by both host backends"), so both inherit the netns dependency. Only
// containerd was ever checked for it. bare is the more thoroughly hidden of the
// two, because sharesMountNamespace excuses it from every other check here — a
// containerized bare server produced no host-environment output whatsoever, while
// silently DNAT'ing its published ports inside its own container.
func usesCNINetworking(backend string) bool {
	return backend == backendContainerd || backend == backendBare
}

// UsesHostMountFastPath reports whether the backend realizes client-local
// mounts by having the SERVER kernel-9P-mount them and binding the mountpoint —
// the path in pkg/server's applyWithHostMounts. containerd and incus do not
// (containerd has no co-located host-mount fallback at all, and realizes
// client-local mounts only in remote mode; incus cannot realize them at all),
// so the propagation of cornus's own mounts dir is irrelevant to them.
//
// It is EXPORTED because pkg/server's deploy-attach router asks the same
// question to pick the path, and the two answers have to be the same one. They
// used to be two separate literal comparisons, which could diverge with no
// error anywhere: the server would take the host-mount path for a backend whose
// mounts-dir propagation this package never checked, so the mount would appear
// empty inside the container with every startup check green.
//
// raw may be any CORNUS_DEPLOY_BACKEND spelling or a Backend.Name(); it is
// normalized first, so "" (the dockerhost default) answers like "dockerhost".
func UsesHostMountFastPath(raw string) bool { return usesHostMountFastPath(normalizeBackend(raw)) }

func usesHostMountFastPath(backend string) bool {
	return backend == backendDockerhost || backend == backendBare || backend == backendPodman
}

// handsDataDirToRuntime reports whether the backend ever gives its runtime a path
// that cornus built under its own data dir. Only those backends can be hurt by the
// data dir being invisible to that runtime, so only they are worth checking.
//
// incus is the exclusion this exists for, and it is not a nicety. incushost hands
// incusd exactly three kinds of source: a USER's bind source (a host path by
// definition — the daemon opens it, and translating it would be the bug), a
// storage-pool volume NAME, and tmpfs. It never builds a path from its data dir;
// `b.dataDir` is carried "for parity ... and any future per-instance bookkeeping"
// and is not used to construct anything. It is not a MountingBackend either, so
// the mounts dir never reaches it.
//
// Before this gate, a containerized cornus driving a host incusd was told "the
// incus runtime cannot see cornus's data dir X: client-local mounts will be
// rejected", with a hint to bind-mount it. Both halves mislead. Client-local
// mounts are rejected on incus whatever the data dir does — the backend cannot
// realize them at all — so an operator who follows the hint does the work, watches
// the warning disappear, and has changed nothing. Advice that cannot help is worse
// than silence: it spends the reader's trust and their time.
//
// bare and kubernetes are already excluded upstream by sharesMountNamespace, for a
// different reason — they DO get cornus's paths, but those paths already mean the
// same thing on both sides.
func handsDataDirToRuntime(backend string) bool {
	return backend == backendDockerhost || backend == backendContainerd || backend == backendPodman
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
	case backendContainerd, backendBare, backendIncus, backendDockerhost, backendPodman:
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
