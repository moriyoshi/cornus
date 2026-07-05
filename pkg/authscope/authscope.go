// Package authscope maps a third party's JWT claims onto a cornus scope.
//
// Cornus verifies two kinds of JWT. The ones it MINTS always name a cornus scope
// — authtoken.Issue refuses to produce a scopeless token — so authtoken.Grant
// decides what they may reach and this package has nothing to say about them.
// The ones it merely VERIFIES, through an operator-configured single-key or JWKS
// verifier, carry whatever claims their issuer chose. A Kubernetes ServiceAccount
// token has no scope claim and cannot be given one; an OIDC ID token's scope
// spells `openid profile email`, which names nothing cornus knows.
//
// So for externally-issued tokens the scope claim is EVIDENCE, never a grant.
// Configuring a JWKS makes an issuer able to prove identity; it must not thereby
// make it able to grant cornus authority to itself. An issuer that emits
// `scope: api` — carelessly, or because its scopes mean something else entirely
// in its own vocabulary — would otherwise hand out full access without any rule
// of the operator's ever matching. The rules here are the only thing that grants,
// and a claim named `scope` is matchable like any other claim, so an issuer that
// genuinely cooperates can still be honored — explicitly, by a rule that says so.
//
// The model is an ordered allowlist: first matching rule wins, and no match
// grants nothing. That keeps the fail-closed property authtoken.Grant has,
// while making "this ServiceAccount gets registry pull and nothing else"
// expressible — which the previous "an unscoped JWT is a full credential" rule
// could not say at all.
package authscope

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"cornus/pkg/authtoken"
)

// Matcher is one test against one claim. Exactly one test must be set; a matcher
// naming none is a configuration error rather than a matcher that accepts
// everything, because an empty `prefix:` typed by accident would otherwise widen
// a rule to the whole world silently.
//
// There is deliberately no regular-expression matcher. An authorization
// allowlist is the last place to want catastrophic backtracking, and an
// unanchored pattern that matches more than its author read it as is a hole that
// reviews do not catch. Glob covers the shapes that come up (`path.Match`, where
// `*` does not cross `/`, which is what makes prefix rules over
// `system:serviceaccount:ns:name` behave); anything beyond it should be a claim
// the issuer states directly.
type Matcher struct {
	// Equals is an exact value. Typed `any` so a rule can match a bool
	// (`email_verified: {equals: true}`) or a number, and so that an unset
	// matcher is distinguishable from `equals: false`.
	Equals   any      `json:"equals,omitempty" yaml:"equals,omitempty"`
	Prefix   string   `json:"prefix,omitempty" yaml:"prefix,omitempty"`
	Suffix   string   `json:"suffix,omitempty" yaml:"suffix,omitempty"`
	Glob     string   `json:"glob,omitempty" yaml:"glob,omitempty"`
	AnyOf    []string `json:"any_of,omitempty" yaml:"any_of,omitempty"`
	Contains string   `json:"contains,omitempty" yaml:"contains,omitempty"`
}

// Rule grants Scope to a token whose claims satisfy every entry in Match.
//
// Match is a CONJUNCTION. Rules are a disjunction only in the sense that the
// first satisfied one wins, so widening a policy means adding a rule and
// narrowing one means adding a claim to an existing rule's Match.
type Rule struct {
	// Name is for the audit log and error messages. Optional; a rule without one
	// is reported by its position.
	Name  string             `json:"name,omitempty" yaml:"name,omitempty"`
	Scope string             `json:"scope" yaml:"scope"`
	Match map[string]Matcher `json:"match" yaml:"match"`
}

// Map is an ordered rule set. The zero value matches nothing, which is the
// correct behavior for an unconfigured server: an external verifier with no
// scope map grants no access at all.
type Map struct {
	Rules []Rule `json:"rules" yaml:"rules"`
}

// ErrNoMatch is returned by Match when no rule accepted the claims. It is a
// distinct error so the caller can tell "policy exists and declined this token"
// from "policy could not be consulted", which are different operator problems.
var ErrNoMatch = errors.New("no scope-map rule matched the token's claims")

// Load reads and validates a scope map from a YAML (or JSON — YAML is a
// superset) file. A malformed or empty map is an error rather than a warning: an
// authorization policy that silently failed to load is worse than a server that
// refuses to start.
func Load(pathname string) (*Map, error) {
	raw, err := os.ReadFile(pathname)
	if err != nil {
		return nil, fmt.Errorf("authscope: reading scope map: %w", err)
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("authscope: %s: %w", pathname, err)
	}
	return m, nil
}

// Parse decodes and validates a scope map.
func Parse(raw []byte) (*Map, error) {
	var m Map
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing scope map: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate reports every structural problem in the map. It is called by Parse,
// and separately by the server when it synthesizes a rule from
// CORNUS_JWT_DEFAULT_SCOPE, so a bad value fails at startup either way.
func (m *Map) Validate() error {
	if m == nil || len(m.Rules) == 0 {
		return errors.New("scope map has no rules")
	}
	for i, r := range m.Rules {
		where := ruleLabel(r, i)
		if strings.TrimSpace(r.Scope) == "" {
			return fmt.Errorf("rule %s names no scope", where)
		}
		// Reuse the verifier's own scope vocabulary rather than a second copy of
		// it, so a map cannot grant a scope the access matrix does not know.
		if _, err := authtoken.Grant(r.Scope); err != nil {
			return fmt.Errorf("rule %s: %w", where, err)
		}
		if len(r.Match) == 0 {
			return fmt.Errorf("rule %s matches no claims, so it would grant %q to every token this verifier accepts; say so with an explicit matcher if that is the intent", where, r.Scope)
		}
		for claim, matcher := range r.Match {
			if err := matcher.validate(); err != nil {
				return fmt.Errorf("rule %s, claim %q: %w", where, claim, err)
			}
		}
	}
	return nil
}

func (mt Matcher) validate() error {
	n := 0
	if mt.Equals != nil {
		n++
	}
	if mt.Prefix != "" {
		n++
	}
	if mt.Suffix != "" {
		n++
	}
	if mt.Glob != "" {
		n++
	}
	if len(mt.AnyOf) > 0 {
		n++
	}
	if mt.Contains != "" {
		n++
	}
	switch {
	case n == 0:
		return errors.New("matcher names no test (want one of equals, prefix, suffix, glob, any_of, contains)")
	case n > 1:
		return errors.New("matcher names more than one test; use separate rules, or separate claims, rather than combining tests on one claim")
	}
	if mt.Glob != "" {
		if _, err := path.Match(mt.Glob, ""); err != nil {
			return fmt.Errorf("invalid glob %q: %w", mt.Glob, err)
		}
	}
	return nil
}

// Match resolves claims to a scope, returning the granted scope and the name of
// the rule that granted it (for the audit log). It returns ErrNoMatch when no
// rule accepted the claims — which is the fail-closed default, not an anomaly.
func (m *Map) Match(claims map[string]any) (scope, rule string, err error) {
	if m == nil {
		return "", "", ErrNoMatch
	}
	for i, r := range m.Rules {
		if r.matches(claims) {
			return r.Scope, ruleLabel(r, i), nil
		}
	}
	return "", "", ErrNoMatch
}

func (r Rule) matches(claims map[string]any) bool {
	for claim, matcher := range r.Match {
		v, ok := lookup(claims, claim)
		if !ok || !matcher.matches(v) {
			return false
		}
	}
	return true
}

// lookup resolves a claim name against the claim set, LITERAL NAME FIRST.
//
// The order matters and is not arbitrary. A legacy Kubernetes ServiceAccount
// token carries `kubernetes.io/serviceaccount/namespace` as one flat claim whose
// name contains no dots but does contain slashes, while a bound token nests the
// same information under `kubernetes.io` -> `namespace`. Trying the literal name
// first means a claim that genuinely contains dots is never silently
// reinterpreted as a path into something else.
func lookup(claims map[string]any, name string) (any, bool) {
	if v, ok := claims[name]; ok {
		return v, true
	}
	parts := strings.Split(name, ".")
	if len(parts) == 1 {
		return nil, false
	}
	return lookupPath(claims, parts)
}

// lookupPath walks a dotted path, trying the LONGEST key at each level first and
// backtracking if the remainder does not resolve.
//
// One segment per dot would be wrong for the claim sets this exists to read: a
// bound Kubernetes token nests everything under a claim literally named
// `kubernetes.io`, so `kubernetes.io.pod.name` has to consume two segments at
// the first step. Longest-first with backtracking resolves that without the
// operator having to quote anything, and paths are a handful of segments long,
// so the cost is nil.
func lookupPath(cur any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return cur, true
	}
	obj, ok := cur.(map[string]any)
	if !ok {
		return nil, false
	}
	for n := len(parts); n >= 1; n-- {
		v, ok := obj[strings.Join(parts[:n], ".")]
		if !ok {
			continue
		}
		if got, ok := lookupPath(v, parts[n:]); ok {
			return got, true
		}
	}
	return nil, false
}

func (mt Matcher) matches(v any) bool {
	switch {
	case mt.Equals != nil:
		return equalValues(mt.Equals, v)
	case mt.Prefix != "":
		s, ok := v.(string)
		return ok && strings.HasPrefix(s, mt.Prefix)
	case mt.Suffix != "":
		s, ok := v.(string)
		return ok && strings.HasSuffix(s, mt.Suffix)
	case mt.Glob != "":
		s, ok := v.(string)
		if !ok {
			return false
		}
		matched, err := path.Match(mt.Glob, s)
		return err == nil && matched
	case len(mt.AnyOf) > 0:
		s, ok := v.(string)
		if !ok {
			return false
		}
		for _, want := range mt.AnyOf {
			if s == want {
				return true
			}
		}
		return false
	case mt.Contains != "":
		return containsValue(v, mt.Contains)
	}
	return false
}

// containsValue tests membership in the two shapes a multi-valued claim takes: a
// JSON array (`groups: ["a","b"]`) and a space-separated string, which is how
// OAuth spells a scope list. Both are common enough that requiring the operator
// to know which one their issuer emits would be a needless trap.
func containsValue(v any, want string) bool {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && s == want {
				return true
			}
		}
		return false
	case []string:
		for _, s := range t {
			if s == want {
				return true
			}
		}
		return false
	case string:
		for _, f := range strings.Fields(t) {
			if f == want {
				return true
			}
		}
		return false
	}
	return false
}

// equalValues compares a configured value with a claim value across the type
// differences the two decoders introduce. JSON decodes every number as float64
// while YAML decodes an integer as int, so `exp: {equals: 3}` from a YAML map
// would never equal a float64(3) claim without normalizing. Strings and bools
// compare directly.
func equalValues(want, got any) bool {
	if ws, ok := want.(string); ok {
		gs, ok := got.(string)
		return ok && ws == gs
	}
	if wb, ok := want.(bool); ok {
		gb, ok := got.(bool)
		return ok && wb == gb
	}
	wf, wok := toFloat(want)
	gf, gok := toFloat(got)
	return wok && gok && wf == gf
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

func ruleLabel(r Rule, i int) string {
	if r.Name != "" {
		return strconv.Quote(r.Name)
	}
	return "#" + strconv.Itoa(i+1)
}

// DefaultScopeMap builds the one-rule map CORNUS_JWT_DEFAULT_SCOPE stands for:
// every token an external verifier accepts earns `scope`. It exists so that
// "any token from this verifier is a full credential" — the behavior cornus had
// before scopes were enforced — remains expressible in one env var, but has to
// be SAID rather than being inferred from the absence of a scope claim.
func DefaultScopeMap(scope string) (*Map, error) {
	m := &Map{Rules: []Rule{{
		Name:  "CORNUS_JWT_DEFAULT_SCOPE",
		Scope: scope,
		// `sub` is present on every token worth accepting, so this matches
		// anything the verifier already authenticated without being the
		// empty-Match rule Validate rejects.
		Match: map[string]Matcher{"sub": {Glob: "*"}},
	}}}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Append returns a map whose rules are m's followed by other's, so a
// CORNUS_JWT_DEFAULT_SCOPE catch-all can sit BEHIND an explicit file: specific
// rules keep deciding, and the default only catches what none of them claimed.
func (m *Map) Append(other *Map) *Map {
	switch {
	case m == nil:
		return other
	case other == nil:
		return m
	}
	out := &Map{Rules: make([]Rule, 0, len(m.Rules)+len(other.Rules))}
	out.Rules = append(out.Rules, m.Rules...)
	out.Rules = append(out.Rules, other.Rules...)
	return out
}
