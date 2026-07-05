package webbff

// Detection rules for agentdetect.go: TOML documents (our own schema) matched
// against the rendered bottom buffer of a session's screen to classify it as
// blocked or working. A default set is embedded; users may extend it with files
// under ~/.config/cornus/agent-detection/*.toml. User files add to (never replace)
// the defaults, and a malformed file or pattern is logged and skipped so a bad
// override can never break detection.

import (
	"embed"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	toml "github.com/pelletier/go-toml"
)

//go:embed rules.toml
var embeddedRules embed.FS

// ruleFile is the TOML schema: a list of [[rule]] tables.
type ruleFile struct {
	Rules []ruleSpec `toml:"rule"`
}

type ruleSpec struct {
	State   string `toml:"state"`   // "blocked" | "working"
	Pattern string `toml:"pattern"` // Go (RE2) regexp, matched against the rendered bottom buffer
	Agent   string `toml:"agent"`   // optional: only apply when the detected agent matches
	// Agents is Agent's plural. It exists because the useful scope for a rule is
	// usually a SET of programs — a password prompt belongs to ssh and sudo and
	// gpg alike — and spelling that as one rule per program multiplies the pattern
	// that actually matters. Both forms may be used; a rule scoped either way
	// applies only to the programs it names.
	Agents []string `toml:"agents"`
}

// compiledRule is a ruleSpec with its pattern compiled once.
type compiledRule struct {
	state sessionState
	// agents is the scope, Agent and Agents merged. Empty means "every session",
	// which is what a user's own rule gets when it names nothing.
	agents  []string
	pattern *regexp.Regexp
}

// ruleSet is the merged, compiled detection rules.
type ruleSet struct {
	rules []compiledRule
}

// matches reports whether any rule for the given state hits the rendered screen
// text, given the program currently in the FOREGROUND of the session.
//
// agent is the live foreground program (see detector.setAgent), not the argv the
// session was launched with. That distinction is the whole point of the scope: a
// pattern like `\by/n\b` is a prompt when the program showing it is prompting, and
// is just text when the program showing it is `cat`, `less`, `git log` or `grep`.
// Every built-in rule is scoped for exactly that reason — before they were, all
// four of those examples classified a session as needing attention.
//
// An unscoped rule still applies to every session. That is the documented contract
// for user-authored rules, where the author knows what they are asking for; the
// built-ins cannot make that assumption because they run against everyone.
//
// An UNKNOWN foreground program (empty agent) therefore matches no scoped rule.
// That is deliberate and conservative: the cost is a missed prompt in a session we
// could not identify, against a false "needs you" on every session that merely
// displays the wrong words.
func (rs *ruleSet) matches(state sessionState, agent, screen string) bool {
	if rs == nil {
		return false
	}
	for _, r := range rs.rules {
		if r.state != state {
			continue
		}
		if !r.inScope(agent) {
			continue
		}
		if r.pattern.MatchString(screen) {
			return true
		}
	}
	return false
}

// inScope reports whether this rule applies to a session whose foreground program
// is agent. A rule naming no program applies to all of them.
func (r compiledRule) inScope(agent string) bool {
	if len(r.agents) == 0 {
		return true
	}
	for _, a := range r.agents {
		if a == agent {
			return true
		}
	}
	return false
}

// loadRules builds the rule set from the embedded defaults plus any user override
// files. It is called once per BFF (per `cornus web` start).
func loadRules() *ruleSet {
	rs := &ruleSet{}
	if data, err := embeddedRules.ReadFile("rules.toml"); err != nil {
		// Should never happen: rules.toml is embedded at build time.
		slog.Error("agent-detection: embedded rules unreadable", "err", err)
	} else {
		rs.add("<embedded>", data)
	}
	for _, p := range userRuleFiles() {
		data, err := os.ReadFile(p)
		if err != nil {
			slog.Warn("agent-detection: skipping unreadable rule file", "path", p, "err", err)
			continue
		}
		rs.add(p, data)
	}
	return rs
}

// add parses one TOML document and appends its valid, compilable rules. Invalid
// rules are logged and skipped individually; a document that fails to parse is
// skipped whole.
func (rs *ruleSet) add(source string, data []byte) {
	var f ruleFile
	if err := toml.Unmarshal(data, &f); err != nil {
		slog.Warn("agent-detection: skipping malformed rule file", "source", source, "err", err)
		return
	}
	for i, spec := range f.Rules {
		state := sessionState(spec.State)
		if state != stateBlocked && state != stateWorking {
			slog.Warn("agent-detection: skipping rule with unknown state",
				"source", source, "index", i, "state", spec.State)
			continue
		}
		re, err := regexp.Compile(spec.Pattern)
		if err != nil {
			slog.Warn("agent-detection: skipping rule with bad pattern",
				"source", source, "index", i, "pattern", spec.Pattern, "err", err)
			continue
		}
		// Agent and Agents merge rather than one winning: a rule may reasonably
		// spell a single program either way, and silently dropping one of them
		// would widen the rule's scope to everything, which is the failure this
		// scoping exists to prevent.
		var agents []string
		if spec.Agent != "" {
			agents = append(agents, spec.Agent)
		}
		for _, a := range spec.Agents {
			if a != "" {
				agents = append(agents, a)
			}
		}
		rs.rules = append(rs.rules, compiledRule{state: state, agents: agents, pattern: re})
	}
}

// userRuleFiles lists ~/.config/cornus/agent-detection/*.toml (sorted for a stable
// load order). A missing directory just yields no files.
func userRuleFiles() []string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "cornus", "agent-detection", "*.toml"))
	sort.Strings(matches)
	return matches
}
