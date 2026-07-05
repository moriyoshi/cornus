package hostenv

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Mapper translates paths between this process's mount namespace and the
// host's, and reports mount propagation. Every path cornus hands to a container
// runtime is interpreted by that runtime on the HOST, so a server running
// inside a container must translate its own paths before passing them on —
// otherwise the runtime binds a path that means something else there, or
// nothing at all.
type Mapper interface {
	// ToHost translates a path in this process's mount namespace to the path
	// the host runtime must be given. ok is false when the path is not
	// host-visible at all, so callers can fail loudly instead of handing the
	// runtime a path that would silently bind an empty directory.
	//
	// When cornus runs directly on the host this is the identity: every path is
	// already a host path. Call sites therefore never need to branch on whether
	// they are containerized.
	ToHost(path string) (host string, ok bool)

	// Propagation reports the mount propagation of the mount backing path —
	// one of the Propagation* constants. Only PropagationShared lets a mount
	// this process creates become visible to the host runtime, which is the
	// precondition for the client-local-mount fast path.
	Propagation(path string) string
}

// HostPathMapEnv is the operator override that declares how this container's
// paths map to the host's, as comma-separated "container=host" pairs:
//
//	CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus,/run/cornus=/run/cornus
//
// It takes precedence over anything auto-detected, so a bad guess is always
// correctable, and it is the only mechanism available when the runtime cannot
// be asked what this container's mounts are.
const HostPathMapEnv = "CORNUS_HOST_PATH_MAP"

// Identity returns the Mapper for a cornus whose paths ARE the runtime's: one
// running directly on the host, or containerized beside the runtime it drives.
// It translates every absolute path to itself and never reports a path as
// invisible.
//
// Useful wherever a Mapper is required but no translation is wanted — a
// zero-value construction, or a test exercising the overwhelmingly common
// non-containerized case.
func Identity() Mapper { return identityMapper{} }

// identityMapper is the mapper for a cornus running directly on the host: its
// paths ARE host paths.
type identityMapper struct {
	mounts []mountEntry
}

func (m identityMapper) ToHost(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	return filepath.Clean(path), true
}

func (m identityMapper) Propagation(path string) string { return propagationOf(m.mounts, path) }

// pathEntry is one container-path -> host-path correspondence.
type pathEntry struct {
	container string
	host      string
	// explicit records that this entry came from HostPathMapEnv rather than
	// from runtime self-inspection, so an operator override wins over a
	// same-length detected entry.
	explicit bool
}

// PathMap maps a containerized cornus's paths to their host equivalents by
// longest matching prefix.
type PathMap struct {
	entries []pathEntry
	mounts  []mountEntry
}

// ToHost implements Mapper.
func (m *PathMap) ToHost(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	path = filepath.Clean(path)
	for _, e := range m.entries {
		if !pathHasPrefix(path, e.container) {
			continue
		}
		rel := strings.TrimPrefix(path, strings.TrimSuffix(e.container, "/"))
		return filepath.Join(e.host, rel), true
	}
	return "", false
}

// Propagation implements Mapper.
func (m *PathMap) Propagation(path string) string { return propagationOf(m.mounts, path) }

// Entries returns the mapping as "container=host" strings, longest prefix
// first, for the preflight and the startup summary to print.
func (m *PathMap) Entries() []string {
	out := make([]string, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.container+"="+e.host)
	}
	return out
}

func propagationOf(mounts []mountEntry, path string) string {
	e, ok := findMount(mounts, path)
	if !ok {
		return PropagationUnknown
	}
	return e.propagation
}

// newPathMap builds a PathMap, ordering entries so the longest container prefix
// wins and, at equal length, an explicit operator entry beats a detected one.
func newPathMap(entries []pathEntry, mounts []mountEntry) *PathMap {
	sorted := append([]pathEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		li, lj := len(sorted[i].container), len(sorted[j].container)
		if li != lj {
			return li > lj
		}
		return sorted[i].explicit && !sorted[j].explicit
	})
	return &PathMap{entries: sorted, mounts: mounts}
}

// parseHostPathMap parses the HostPathMapEnv value. An entry with a
// non-absolute path or no "=" is a hard error: a typo here silently disables
// the very translation it was set to fix, so it fails at startup rather than at
// the first deploy.
func parseHostPathMap(raw string) ([]pathEntry, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []pathEntry
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		container, host, ok := strings.Cut(pair, "=")
		container, host = strings.TrimSpace(container), strings.TrimSpace(host)
		if !ok || container == "" || host == "" {
			return nil, fmt.Errorf("invalid %s entry %q: want <container-path>=<host-path>", HostPathMapEnv, pair)
		}
		if !filepath.IsAbs(container) || !filepath.IsAbs(host) {
			return nil, fmt.Errorf("invalid %s entry %q: both paths must be absolute", HostPathMapEnv, pair)
		}
		entries = append(entries, pathEntry{
			container: filepath.Clean(container),
			host:      filepath.Clean(host),
			explicit:  true,
		})
	}
	return entries, nil
}
