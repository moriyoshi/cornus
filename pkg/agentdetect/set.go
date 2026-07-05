package agentdetect

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	herdr "cornus/third_party/herdr"
)

// Set is the loaded manifests, indexed by every name that resolves to one.
type Set struct {
	byName map[string]*Manifest
	ids    []string
}

var (
	bundledOnce sync.Once
	bundledSet  *Set
	bundledErr  error
)

// Bundled returns the vendored herdr manifests, parsed once per process.
//
// A manifest that fails to parse is a hard error rather than a skip: they arrive
// as a vendored set from one upstream commit, so one of them being broken means
// the bundle is wrong, not that one agent is unsupported. That is the opposite of
// the policy for USER rule files, where skipping the bad one is right.
func Bundled() (*Set, error) {
	bundledOnce.Do(func() {
		agents, err := herdr.Manifests()
		if err != nil {
			bundledErr = err
			return
		}
		bundledSet, bundledErr = NewSet(agents)
	})
	return bundledSet, bundledErr
}

// NewSet parses and indexes a collection of manifests.
func NewSet(agents []herdr.Agent) (*Set, error) {
	s := &Set{byName: map[string]*Manifest{}}
	for _, a := range agents {
		m, err := ParseManifest(a.TOML)
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", a.ID, err)
		}
		s.ids = append(s.ids, m.ID)
		// The manifest's own id and aliases are authoritative, not the filename:
		// upstream uses aliases for exactly the case where the binary is not named
		// after the agent ("claude-code" for claude).
		for _, name := range append([]string{m.ID}, m.Aliases...) {
			key := normalizeLookupName(name)
			if key == "" {
				continue
			}
			if _, dup := s.byName[key]; dup {
				// First wins, deterministically, because agents are sorted by id.
				continue
			}
			s.byName[key] = m
		}
	}
	sort.Strings(s.ids)
	return s, nil
}

// Lookup returns the manifest for an agent name, or nil.
func (s *Set) Lookup(name string) *Manifest {
	if s == nil {
		return nil
	}
	return s.byName[normalizeLookupName(strings.TrimSpace(name))]
}

// Knows reports whether a name resolves to a manifest. It is the predicate
// AgentName takes, so identification and classification agree on what an agent
// is by construction rather than by two lists kept in step.
func (s *Set) Knows(name string) bool { return s.Lookup(name) != nil }

// IDs lists the manifest ids, sorted.
func (s *Set) IDs() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.ids...)
}

// Identify resolves the agent a foreground process is, restricted to agents this
// set has a manifest for. An empty result means "not an agent we can classify",
// which callers must treat as "report nothing" rather than guessing.
func (s *Set) Identify(comm string, argv []string) string {
	name := AgentName(comm, argv, s.Knows)
	if name == "" {
		return ""
	}
	// Canonicalise: an alias identified the process, but callers key manifests and
	// report labels by the manifest's own id, and "claude-code" vs "claude" being
	// two names for one agent is exactly what aliases exist to hide.
	if m := s.Lookup(name); m != nil {
		return m.ID
	}
	return name
}
