package hub

// Policy is the hub's central connection policy, enforced at relay and
// registration time. It has two independent matrices:
//
//   - reach: which caller identity may reach which callee service (relay time);
//   - register: which identity may host (register) which service name.
//
// Each matrix is enforced only when non-empty ("configure to enforce"): a nil
// *Policy, or a Policy with an empty matrix for that dimension, allows everything
// on that dimension. See .agents/docs/ARCHITECTURE.md ("Workload-to-workload hub").
type Policy struct {
	reach    map[string]map[string]bool
	register map[string]map[string]bool
}

// NewPolicy builds a policy from a reach matrix (caller → callee services) and a
// register matrix (identity → hostable service names). Returns nil (allow-all)
// when both are empty.
func NewPolicy(reach, register map[string][]string) *Policy {
	r, w := toMatrix(reach), toMatrix(register)
	if r == nil && w == nil {
		return nil
	}
	return &Policy{reach: r, register: w}
}

func toMatrix(rules map[string][]string) map[string]map[string]bool {
	if len(rules) == 0 {
		return nil
	}
	m := make(map[string]map[string]bool, len(rules))
	for k, vs := range rules {
		inner := make(map[string]bool, len(vs))
		for _, v := range vs {
			inner[v] = true
		}
		m[k] = inner
	}
	return m
}

// ReachEnforced reports whether a reach matrix is configured — i.e. whether the
// relay must resolve the caller identity and check it. When false, reach is
// allow-all and callers need not declare an identity.
func (p *Policy) ReachEnforced() bool { return p != nil && p.reach != nil }

// Allow reports whether caller may reach callee. A nil policy or an unconfigured
// reach matrix allows all; otherwise only explicitly listed pairs (an empty caller
// identity is denied).
func (p *Policy) Allow(caller, callee string) bool {
	if !p.ReachEnforced() {
		return true
	}
	return p.reach[caller][callee]
}

// AllowRegister reports whether identity may register (host) service. A nil policy
// or an unconfigured register matrix allows all; otherwise only explicitly listed
// pairs.
func (p *Policy) AllowRegister(identity, service string) bool {
	if p == nil || p.register == nil {
		return true
	}
	return p.register[identity][service]
}
