package webbff

// The per-workload mount table: what really sits behind a container path.
//
// The explorer's virtual namespace names a container path, but a container path is not
// one kind of thing. It may be
//
//   - a client-local BIND, whose bytes live on the developer's own disk and reach the
//     container over 9P (pkg/deploywire) — so the explorer can read and write them
//     directly, with no daemon round trip at all;
//   - a named VOLUME, which only the server can see;
//   - an ANONYMOUS volume, which is per-replica and cannot be named in a request;
//   - a tmpfs or a device, which is neither, and exists here only because it SHADOWS
//     whatever it is mounted over.
//
// That last category is why every kind is recorded rather than just the binds. With
// `- ./data:/data` and a volume at `/data/cache`, the host directory has an ordinary
// (usually empty) `cache` in it, while the container sees the volume. Resolving
// `/data/cache/x` to the host would read the wrong bytes and, worse, write them.
//
// Nothing in pkg/compose, pkg/api or pkg/server rejects overlapping mount targets, so
// this nesting is legal, reachable configuration rather than a corner case someone has
// to construct deliberately.

import (
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"cornus/pkg/api"
)

// mountKind classifies what backs a container path.
type mountKind int

const (
	// mountBind is a client-local bind: source is a real directory (or file) on the
	// developer's machine.
	mountBind mountKind = iota
	// mountVolume is a named volume. Server-side; not resolvable from here.
	mountVolume
	// mountAnonymous is an anonymous volume. api.VolumeSpec leaves Name empty for
	// these, and they are per-replica, so they can never be addressed as a shared
	// location — they are shadows and nothing more.
	mountAnonymous
	// mountOpaque is a tmpfs or a device mapping: real in the container, backed by
	// nothing the explorer can reach.
	mountOpaque
)

// hostMount is one entry of a workload's mount table.
type hostMount struct {
	kind mountKind
	// target is the container path, path.Clean'd and absolute. An entry whose target
	// is neither is dropped at build time rather than carried and skipped later.
	target string
	// source is the symlink-resolved absolute HOST path, for mountBind only. It is
	// the exact root the 9P export was rooted at (pkg/wire/writablefs.go), which is
	// what makes containment here agree with what the container is held to.
	source string
	// sourceIsDir distinguishes a directory bind from a single-file bind (compose
	// configs and secrets become read-only file binds).
	sourceIsDir bool
	volume      string
	readOnly    bool
	// order is the declaration index. Compose appends config/secret binds AFTER the
	// `volumes:` list, so two entries can legitimately claim one target; docker's rule
	// is last-wins, and a length-only sort does not express that.
	order int
}

// buildHostMounts records every workload's mount table, keyed by deployment name
// (plan.Resource — the same name the virtual namespace's first segment carries). It
// runs from loadProject beside buildLocalRoots, and the result is never mutated
// afterwards, so no lock is needed.
func (s *Server) buildHostMounts() {
	s.hostMounts = map[string][]hostMount{}
	for _, svc := range s.order {
		plan := s.plans[svc]
		var table []hostMount
		add := func(m hostMount) {
			t := pathpkg.Clean("/" + strings.TrimPrefix(m.target, "/"))
			// An empty compose target (`- ./data:`) parses to "" and nothing upstream
			// rejects it; left alone it would prefix-match the entire filesystem. "/"
			// is dropped for the same reason — a mount over the container root tells
			// us nothing useful and its boundary test degenerates.
			if m.target == "" || t == "/" || t == "." {
				return
			}
			m.target = t
			m.order = len(table)
			table = append(table, m)
		}

		for _, m := range plan.Spec.Mounts {
			hm := hostMount{kind: mountBind, target: m.Target, readOnly: m.ReadOnly}
			if m.Source != "" {
				// Resolve exactly as the 9P export does. Where buildLocalRoots falls
				// back to the unresolved path when EvalSymlinks fails, the export
				// ERRORS — so a mount we cannot resolve is not recorded as a bind, and
				// falls through to the container.
				if real, err := filepath.EvalSymlinks(m.Source); err == nil {
					if abs, err := filepath.Abs(real); err == nil {
						if fi, err := os.Lstat(abs); err == nil {
							hm.source, hm.sourceIsDir = abs, fi.IsDir()
						}
					}
				}
			}
			// A bind whose source did not resolve is still a SHADOW: the container has
			// something there, we just cannot serve it.
			if hm.source == "" {
				hm.kind = mountOpaque
			}
			add(hm)
		}
		for _, v := range plan.Spec.Volumes {
			kind := mountVolume
			if v.Name == "" {
				kind = mountAnonymous
			}
			add(hostMount{kind: kind, target: v.Target, volume: v.Name, readOnly: v.ReadOnly})
		}
		for _, t := range plan.Spec.Tmpfs {
			// "/run:size=64m" — the options are not ours to interpret, only the path.
			path := t
			if i := strings.IndexByte(path, ':'); i >= 0 {
				path = path[:i]
			}
			add(hostMount{kind: mountOpaque, target: path})
		}
		for _, d := range plan.Spec.Devices {
			// "host:container[:perms]"; the container path is what shadows. A bare
			// "host" maps the device at the same path in the container.
			parts := strings.Split(d, ":")
			target := parts[0]
			if len(parts) > 1 {
				target = parts[1]
			}
			add(hostMount{kind: mountOpaque, target: target})
		}

		sortHostMounts(table)
		if len(table) > 0 {
			s.hostMounts[plan.Resource] = table
		}
	}
}

// sortHostMounts puts a table into lookup precedence: longest target first, so the most
// specific mount wins, with ties broken by declaration order, last wins. The tie-break
// is not decoration — compose appends config and secret binds after the `volumes:` list,
// so two entries can legitimately claim one target, and docker's rule is last-wins.
func sortHostMounts(table []hostMount) {
	sort.SliceStable(table, func(i, j int) bool {
		if len(table[i].target) != len(table[j].target) {
			return len(table[i].target) > len(table[j].target)
		}
		return table[i].order > table[j].order
	})
}

// lookupMount returns the mount governing a container path, and the remainder of the
// path relative to that mount's target.
//
// Matching is on PATH BOUNDARIES, not string prefixes: "/data" must not claim
// "/database". A file bind matches only its exact target, since nothing lives under a
// file.
func lookupMount(table []hostMount, p string) (m hostMount, rel string, ok bool) {
	clean := pathpkg.Clean("/" + strings.TrimPrefix(p, "/"))
	for _, e := range table {
		switch {
		case clean == e.target:
			rel = ""
		case strings.HasPrefix(clean, e.target+"/"):
			if e.kind == mountBind && !e.sourceIsDir {
				continue // a single-file bind has no children
			}
			rel = strings.TrimPrefix(clean, e.target+"/")
		default:
			continue
		}
		return e, rel, true
	}
	return hostMount{}, "", false
}

// mountsFor returns a workload's table. A workload with no compose plan (deployed from
// elsewhere, or not part of this project) simply has none, and every path under it stays
// a container path.
func (s *Server) mountsFor(workload string) []hostMount { return s.hostMounts[workload] }

// originMatchesHere reports whether a deployment was created from THIS machine and THIS
// project directory.
//
// The mount table is read from the compose file on disk, but api.DeployStatus carries no
// mount information, so the table and the running workload are joined by name alone.
// Deploy from one branch, switch to another where the mount moved, and the table would
// name a directory the container never had — reads would return the wrong bytes and
// writes would land in the wrong place, with nothing to notice it.
//
// api.Origin is CLIENT-ATTESTED, so this is a correctness guard against a stale or
// foreign deployment, not a security control. It is deliberately conservative: anything
// it cannot confirm falls back to treating the path as a container path, which is always
// correct, only slower.
func originMatchesHere(st api.DeployStatus, baseDir string) bool {
	if st.Origin == nil || st.Origin.Directory == "" || baseDir == "" {
		return false
	}
	host, err := os.Hostname()
	if err != nil || st.Origin.Host == "" || st.Origin.Host != host {
		return false
	}
	a, err1 := filepath.EvalSymlinks(st.Origin.Directory)
	b, err2 := filepath.EvalSymlinks(baseDir)
	if err1 != nil || err2 != nil {
		return st.Origin.Directory == baseDir
	}
	return a == b
}
