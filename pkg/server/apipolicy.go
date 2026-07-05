package server

// apiPolicy authorizes API actions (deploy, build) per caller identity. It
// mirrors the hub.Policy "configure to enforce" pattern: a nil *apiPolicy (env
// unset) allows everything, so the dev default is unchanged; once configured it
// allows only the identity/action pairs listed (an entry may carry "*" to allow
// all actions for that identity).
//
// The policy is orthogonal to authentication: it is enforced whenever CONFIGURED,
// even when bearer auth is off. But since an EMPTY identity is always DENIED under
// a configured policy (fail closed), in practice it REQUIRES an identifying
// credential — a JWT `sub` or an mTLS CommonName. The opaque static token and an
// anonymous caller carry no identity and are therefore denied.
type apiPolicy struct {
	rules map[string]map[string]bool
}

// newAPIPolicy builds a policy from a rules map (identity -> allowed actions).
// Returns nil (allow-all) when the map is empty/absent.
func newAPIPolicy(rules map[string][]string) *apiPolicy {
	if len(rules) == 0 {
		return nil
	}
	m := make(map[string]map[string]bool, len(rules))
	for identity, actions := range rules {
		inner := make(map[string]bool, len(actions))
		for _, a := range actions {
			inner[a] = true
		}
		m[identity] = inner
	}
	return &apiPolicy{rules: m}
}

// Allow reports whether identity may perform action. A nil policy (unconfigured)
// allows all. When configured, an empty identity is denied (fail closed) and a
// non-empty identity must be listed with the action or with the wildcard "*".
func (p *apiPolicy) Allow(identity, action string) bool {
	if p == nil {
		return true
	}
	if identity == "" {
		return false
	}
	actions := p.rules[identity]
	return actions[action] || actions["*"]
}

// AllowExec reports whether identity may exec into (or interactively attach to)
// a workload: the dedicated "exec" action, which the broader "deploy" action
// implies. The implication is what keeps existing policies working — a
// deploy-capable identity could always exec before the action existed, and
// still can. The refinement's value is exec-ONLY identities: a rule granting
// just "exec" lets an identity shell into workloads without being able to
// apply, delete, or restart them. Note the deliberate converse: exec cannot be
// DENIED to a deploy-capable identity.
func (p *apiPolicy) AllowExec(identity string) bool {
	return p.Allow(identity, "exec") || p.Allow(identity, "deploy")
}

// MentionsAction reports whether ANY identity's rule lists action explicitly
// (the "*" wildcard deliberately does not count). Opt-in enforcement gates use
// it: e.g. registry pull authz turns on only when some rule mentions "pull",
// so a policy written before that action existed — including a pure
// {"admin":["*"]} — keeps its old, authn-only pull behavior.
func (p *apiPolicy) MentionsAction(action string) bool {
	if p == nil {
		return false
	}
	for _, actions := range p.rules {
		if actions[action] {
			return true
		}
	}
	return false
}
