// Package hostpolicy gates the host-privilege surface a DeploySpec may request
// before a host-level deploy backend (dockerhost, containerdhost) translates it
// into a container. cornus's HTTP API is unauthenticated by default (see
// .agents/docs/AUTH_DESIGN_NOTE.md), so on a host backend an un-gated spec that
// sets Privileged or bind-mounts an arbitrary host path (e.g. "/" or
// /var/run/docker.sock) would be equivalent to remote root. The zero Policy is
// therefore default-deny: no privileged containers and no host bind mounts.
// Operators opt back in via env (FromEnv) or, for the local CLI where the
// caller already owns the host, Permissive.
package hostpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cornus/pkg/api"
)

// Policy gates privileged containers and host bind-mount sources.
type Policy struct {
	// AllowPrivileged permits DeploySpec.Privileged. Default false.
	AllowPrivileged bool
	// AllowBindPrefixes is the set of absolute directory prefixes a host bind
	// Mount.Source may live under. Empty means no host binds are permitted. A
	// prefix of "/" permits any absolute source.
	AllowBindPrefixes []string
}

// Permissive allows privileged containers and any absolute bind source. It is
// for the local `cornus deploy` CLI, where the caller already has direct
// runtime access on their own host, so gating adds friction with no security
// gain.
func Permissive() Policy {
	return Policy{AllowPrivileged: true, AllowBindPrefixes: []string{"/"}}
}

// FromEnv builds a Policy from the environment:
//   - CORNUS_ALLOW_PRIVILEGED (1/true/yes) permits privileged containers.
//   - CORNUS_ALLOW_BIND_SOURCES is a comma-separated list of absolute directory
//     prefixes permitted as host bind sources.
func FromEnv() Policy {
	return Policy{
		AllowPrivileged:   envTrue(os.Getenv("CORNUS_ALLOW_PRIVILEGED")),
		AllowBindPrefixes: splitPrefixes(os.Getenv("CORNUS_ALLOW_BIND_SOURCES")),
	}
}

// Validate rejects a spec that exceeds the policy. Backends run it before any
// runtime call so a denied spec never touches the daemon. backend names the
// calling backend in error text.
func (p Policy) Validate(backend string, spec api.DeploySpec) error {
	if spec.Privileged && !p.AllowPrivileged {
		return fmt.Errorf("%s: privileged containers are disabled by policy (set CORNUS_ALLOW_PRIVILEGED=1 to allow)", backend)
	}
	for _, m := range spec.Mounts {
		// An empty source is an anonymous/named volume, not a host bind.
		if m.Source == "" {
			continue
		}
		if !p.bindAllowed(m.Source) {
			return fmt.Errorf("%s: host bind source %q is not permitted by policy (allowed prefixes: %s; extend with CORNUS_ALLOW_BIND_SOURCES)", backend, m.Source, p.allowedList())
		}
	}
	return nil
}

// bindAllowed reports whether src is equal to or nested under any allowed prefix.
//
// The check is symlink-aware: a lexical prefix test alone is bypassable because
// the daemon follows symlinks when it sets up the bind (dockerhost
// engine.go: `bind := m.Source + ":" + m.Target`; containerdhost
// volumes_linux.go: `ociBindMount(m.Source, ...)`). A source that is lexically
// inside an allowed prefix but whose real path -- via a symlinked component --
// escapes every prefix would otherwise get an out-of-policy host path mounted
// into the container. Since cornus's HTTP API is unauthenticated by default,
// that defeats the whole boundary this package exists to enforce. We therefore
// require BOTH the lexical path and the symlink-resolved real path to sit under
// an allowed prefix.
func (p Policy) bindAllowed(src string) bool {
	clean := filepath.Clean(src)
	// Lexical gate: also rejects ".." traversal without touching the filesystem.
	if !p.lexicalAllowed(clean) {
		return false
	}
	// Symlink gate: resolve the real path (of both source and prefixes, so a
	// symlinked prefix such as /tmp -> /private/tmp doesn't cause a false
	// negative) and require the resolved source to remain under a resolved
	// prefix.
	resolved := resolveSymlinks(clean)
	for _, prefix := range p.AllowBindPrefixes {
		if underPrefix(resolveSymlinks(filepath.Clean(prefix)), resolved) {
			return true
		}
	}
	return false
}

// lexicalAllowed reports whether the cleaned path sits under an allowed prefix
// by pure string/path arithmetic, without resolving symlinks.
func (p Policy) lexicalAllowed(path string) bool {
	for _, prefix := range p.AllowBindPrefixes {
		if underPrefix(filepath.Clean(prefix), path) {
			return true
		}
	}
	return false
}

// underPrefix reports whether path is equal to or nested under prefix. Both are
// expected to be cleaned absolute paths.
func underPrefix(prefix, path string) bool {
	rel, err := filepath.Rel(prefix, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// resolveSymlinks returns the real path of p with symlinked components resolved.
// filepath.EvalSymlinks requires the path to exist; for a bind source the daemon
// has yet to create, it resolves the deepest existing ancestor and re-appends
// the not-yet-existent suffix, so a symlinked ancestor is still caught. If
// nothing resolves it returns p unchanged.
func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	rest := ""
	cur := p
	for {
		parent := filepath.Dir(cur)
		rest = filepath.Join(filepath.Base(cur), rest)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Clean(filepath.Join(resolved, rest))
		}
		if parent == cur {
			// Reached the root without resolving anything.
			return p
		}
		cur = parent
	}
}

func (p Policy) allowedList() string {
	if len(p.AllowBindPrefixes) == 0 {
		return "(none)"
	}
	return strings.Join(p.AllowBindPrefixes, ", ")
}

func envTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func splitPrefixes(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}
