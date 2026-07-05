// Package hostenv answers one question the deploy backends and the server
// preflight both need: is this cornus process running INSIDE a container on the
// very host whose container runtime it drives, and if so, which host paths do
// its own paths correspond to?
//
// It matters because every path cornus hands to a container runtime — a bind
// source, a volume backing directory, a pinned netns, a log file, the log-shim
// binary — is resolved by that runtime in the HOST's mount namespace, not in
// cornus's. Run the server on the host and the two are the same, which is why
// nothing needed this until now. Run it in a container and they diverge
// silently: the runtime binds a path that means something else there, or
// nothing at all, and the workload comes up with an empty directory and no
// error. Translating at the boundary is what makes the containerized server a
// supportable topology rather than one that half-works.
//
// The mapping comes from two sources, operator override first:
//
//   - CORNUS_HOST_PATH_MAP (HostPathMapEnv), authoritative and always
//     available — the only mechanism when the runtime cannot be asked;
//   - self-inspection, where we work out which container we are and ask the
//     runtime for its mounts. Convenient, but a guess: see selfid.go for why
//     the candidate is confirmed before it is trusted.
//
// Detect never fails just because it learned nothing: with no evidence that our
// paths diverge from the runtime's it returns an identity Mapper, so call sites
// translate unconditionally and never branch on whether they are containerized,
// and a cornus co-located with its runtime keeps behaving as it always has (see
// Env.Translating). It DOES fail on a malformed CORNUS_HOST_PATH_MAP, because a
// typo there silently disables the translation it was set to fix.
package hostenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Self-inspection outcomes that are not runtime errors. Both leave the map to
// whatever CORNUS_HOST_PATH_MAP supplied, and are surfaced through Env.Err so
// the preflight can say which one happened.
var (
	// errNoSelfCandidate means nothing in /proc named a container id — a plain
	// cgroup v2 container with no runtime-managed binds and a custom hostname.
	errNoSelfCandidate = errors.New("hostenv: cannot determine this container's id from /proc (set " + HostPathMapEnv + ")")
	// errSelfUnconfirmed means every candidate inspected cleanly but described
	// some other container (see confirmSelf).
	errSelfUnconfirmed = errors.New("hostenv: no candidate container id matched this process (set " + HostPathMapEnv + ")")
)

// Runtime names for Env.Runtime.
const (
	RuntimeDocker     = "docker"
	RuntimeContainerd = "containerd"
	RuntimeUnknown    = "unknown"
)

// Env describes this process's relationship to the host's container runtime.
type Env struct {
	// InContainer reports whether cornus is running inside a container.
	InContainer bool
	// Runtime is the runtime this cornus drives, as told to Detect.
	Runtime string
	// SelfID is our own container id, when one could be resolved AND confirmed.
	// Empty otherwise, including when cornus runs on the host.
	SelfID string
	// HostNetwork reports whether our container shares the host's network
	// namespace. Only meaningful when HostNetworkKnown is set: it comes from
	// runtime self-inspection, and the /proc/1/ns/net comparison that would
	// otherwise answer it is wrong inside a container without host PID (our own
	// init is PID 1, in our own netns, so the two always match).
	HostNetwork      bool
	HostNetworkKnown bool
	// Translating reports that a real path mapping is in effect, i.e. that this
	// process's paths and the host runtime's genuinely diverge and we know how.
	//
	// InContainer alone does NOT imply it. A cornus containerized ALONGSIDE the
	// runtime it drives — the Docker-in-Docker shape the E2E runner uses, server
	// and dockerd in one container — is in a container yet shares the runtime's
	// mount namespace, so its paths already agree and translating would be
	// wrong. We only claim divergence on evidence: an operator-supplied
	// CORNUS_HOST_PATH_MAP, or a runtime that confirms it created THIS
	// container (and therefore is the host's, reached through a bind-mounted
	// socket). Absent both, Detect falls back to the identity mapper, which is
	// exactly the behaviour of every cornus before this package existed.
	Translating bool
	// Err records why self-inspection could not complete, for the preflight to
	// explain an empty map. It is not a failure of Detect: an operator-supplied
	// CORNUS_HOST_PATH_MAP may well cover everything anyway, and a co-located
	// containerized cornus needs no map at all.
	Err error
}

// MountPoint is one of a container's mounts as the runtime reports it.
type MountPoint struct {
	// Source is the path on the HOST.
	Source string
	// Destination is where it appears inside the container.
	Destination string
}

// SelfInspect is the subset of a runtime's container-inspect result hostenv
// uses. Keeping it minimal is what lets this package stay free of any
// pkg/deploy import, so the backends can depend on it without a cycle.
type SelfInspect struct {
	ID          string
	Hostname    string
	NetworkMode string
	Mounts      []MountPoint
}

// Inspector inspects a container by id. Backends adapt their own client to
// this (see dockerhost's selfInspector).
type Inspector func(ctx context.Context, id string) (SelfInspect, error)

// Options configures Detect. The zero value is valid: it reads the real /proc
// and environment and performs no self-inspection.
type Options struct {
	// Runtime names the runtime this cornus drives (RuntimeDocker,
	// RuntimeContainerd). Defaults to RuntimeUnknown.
	Runtime string
	// Inspect, when set, enables self-inspection. Leave nil for a runtime whose
	// containers cannot be inspected by id, or when only the operator override
	// should be honoured.
	Inspect Inspector

	// procRoot, getenv, hostname and exists are test seams; each falls back to
	// the real thing.
	procRoot string
	getenv   func(string) string
	hostname string
	exists   func(string) bool
}

func (o Options) resolve() Options {
	if o.Runtime == "" {
		o.Runtime = RuntimeUnknown
	}
	if o.procRoot == "" {
		o.procRoot = "/proc"
	}
	if o.getenv == nil {
		o.getenv = os.Getenv
	}
	if o.hostname == "" {
		o.hostname, _ = os.Hostname()
	}
	if o.exists == nil {
		o.exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	return o
}

// Detect resolves this process's host environment and the Mapper for it.
//
// The returned Mapper is never nil: outside a container (and with no operator
// override) it is the identity, so callers translate unconditionally.
//
// The error is reserved for operator misconfiguration — today, a malformed
// CORNUS_HOST_PATH_MAP. Everything else degrades into Env.Err.
func Detect(ctx context.Context, opts Options) (Env, Mapper, error) {
	opts = opts.resolve()

	explicit, err := parseHostPathMap(opts.getenv(HostPathMapEnv))
	if err != nil {
		return Env{}, nil, err
	}

	mounts := readMountinfo(opts.procRoot)
	env := Env{
		Runtime:     opts.Runtime,
		InContainer: looksContainerized(opts, mounts),
	}

	entries := explicit
	if env.InContainer && opts.Inspect != nil {
		self, err := resolveSelf(ctx, opts, mounts)
		switch {
		case err != nil:
			env.Err = err
		default:
			env.SelfID = self.ID
			env.HostNetwork = self.NetworkMode == "host"
			env.HostNetworkKnown = true
			for _, m := range self.Mounts {
				if m.Source == "" || m.Destination == "" {
					continue
				}
				entries = append(entries, pathEntry{
					container: filepath.Clean(m.Destination),
					host:      filepath.Clean(m.Source),
				})
			}
		}
	}
	// No evidence that our paths diverge from the runtime's: stay on the
	// identity, so a co-located containerized cornus keeps working exactly as
	// it did rather than being told none of its paths are host-visible.
	if len(entries) == 0 {
		return env, identityMapper{mounts: mounts}, nil
	}
	env.Translating = true
	return env, newPathMap(entries, mounts), nil
}

// resolveSelf inspects each candidate id in order and returns the first whose
// inspect result is consistent with this process (see confirmSelf). The last
// inspect error is returned when nothing is confirmed, so the preflight can
// report a real cause ("permission denied on the socket") rather than a bare
// "not found".
func resolveSelf(ctx context.Context, opts Options, mounts []mountEntry) (SelfInspect, error) {
	cgroup, _ := os.ReadFile(filepath.Join(opts.procRoot, "self", "cgroup"))
	candidates := selfIDCandidates(mounts, string(cgroup), opts.hostname)
	if len(candidates) == 0 {
		return SelfInspect{}, errNoSelfCandidate
	}
	var lastErr error
	for _, id := range candidates {
		self, err := opts.Inspect(ctx, id)
		if err != nil {
			lastErr = err
			continue
		}
		if self.ID == "" {
			self.ID = id
		}
		if confirmSelf(self, mounts, opts.hostname) {
			return self, nil
		}
	}
	if lastErr != nil {
		return SelfInspect{}, lastErr
	}
	return SelfInspect{}, errSelfUnconfirmed
}

// confirmSelf reports whether an inspect result really describes THIS process's
// container. A wrong id would otherwise produce a confident, wholly incorrect
// path map — worse than no map at all, since every translated path would point
// to some other container's storage.
//
// The evidence, in order of strength:
//
//   - a mount destination the runtime reports must be a mount point in our own
//     table. Runtime-managed binds and volumes always show up as one, so if the
//     container has mounts and NONE of them appear here, it is not us. The test
//     is deliberately "is a mount point", not "is backed by a mount": the
//     latter is true of every path on the system and would confirm anything.
//   - failing that, the hostname must match, which is the case Docker's default
//     (HOSTNAME = short id) already makes likely.
//   - a container with neither mounts nor a comparable hostname offers nothing
//     to check, so the candidate is accepted on the strength of its source.
func confirmSelf(self SelfInspect, mounts []mountEntry, hostname string) bool {
	if len(self.Mounts) > 0 {
		for _, m := range self.Mounts {
			if m.Destination == "" {
				continue
			}
			if hasMountPoint(mounts, m.Destination) {
				return true
			}
		}
		return false
	}
	if self.Hostname != "" && hostname != "" {
		return self.Hostname == hostname
	}
	return true
}

// looksContainerized reports whether this process runs inside a container,
// combining independent markers: no single one covers every runtime and cgroup
// version.
func looksContainerized(opts Options, mounts []mountEntry) bool {
	// Docker and Podman both drop a marker file in the container's root.
	if opts.exists("/.dockerenv") || opts.exists("/run/.containerenv") {
		return true
	}
	// A Kubernetes pod always carries the API service in its environment.
	if opts.getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	// A runtime-managed per-container bind (/etc/hosts and friends) names our
	// own id in the mount table — the same evidence selfid.go mines.
	if len(selfIDsFromMountinfo(mounts)) > 0 {
		return true
	}
	// cgroup v1, and cgroup v2 under systemd, name the container in the path.
	cgroup, err := os.ReadFile(filepath.Join(opts.procRoot, "self", "cgroup"))
	return err == nil && len(selfIDsFromCgroup(string(cgroup))) > 0
}
